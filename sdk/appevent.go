package fpsdk

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"google.golang.org/grpc"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// 业务事件种类（AppEvent.Kind），与 fp 服务端逐字相同。
const (
	// EventLoginSucceeded：Payload 为 {"session_id","client_ref"}。
	EventLoginSucceeded = "login_succeeded"
	// EventLoggedOut：Payload 为 {"session_id","client_ref","reason"}。
	EventLoggedOut = "logged_out"
	// EventAccountDeleted：账号已彻底删除，Payload 为 {}。
	EventAccountDeleted = "account_deleted"
	// EventWechatSubscription：Payload 为 {"subscribed"}。
	EventWechatSubscription = "wechat_subscription"
	// EventProfileChanged：用户资料（昵称、头像、密码、绑定的手机号 / 邮箱 / 微信）改了，Payload 为 {}。
	// 不说改了什么，业务方据此重取资料；同一次操作可能收到多条。
	EventProfileChanged = "profile_changed"
)

// EventLoggedOut 的 reason，与 fp 服务端逐字相同。以后可能新增，不认识的 reason 按 LoggedOutKick 处理。
const (
	LoggedOutLogout          = "logout"
	LoggedOutKick            = "kick"
	LoggedOutFreeze          = "freeze"
	LoggedOutPasswordChanged = "password_changed"
	LoggedOutAccountDeletion = "account_deletion"
	// LoggedOutExpired：空闲超时或到达绝对上限。
	LoggedOutExpired = "expired"
	// LoggedOutRevoked：fp 校验时才发现会话已被撤销（撤销当时没能写完事件），原因已不可考。
	LoggedOutRevoked = "revoked"
)

// AppEvent 是 fp 经推送流可靠投递的一条业务事件，见 Options.OnAppEvent。
type AppEvent struct {
	// ID 全局唯一。同一事件可能收到不止一次（超时重投、fp 实例切换），按它幂等处理。
	ID string
	// Seq 是本应用内递增的序号。
	Seq    int64
	Kind   string
	UserID string
	// Payload 是该种类的 JSON 内容，见 Event* 常量。
	Payload   json.RawMessage
	CreatedAt time.Time
}

// appEventQueue 是 fp 服务端每条流的在途上限（100）之上留的余量：满了就丢，没确认的 fp 会重投。
const appEventQueue = 128

// capAppEvents 是 WatchHello 里声明接收业务事件的能力名，与服务端 grpcapi.CapAppEvents 相同。
const capAppEvents = "app_events"

// appEventWorker 在一条推送流的生命周期内串行处理业务事件并回确认。
//
// 不在收流循环里直接调回调：回调多半要写库，同步调用会把撤销推送一起卡住。
// 单 goroutine 串行处理，同一用户的事件才按到达顺序执行。
type appEventWorker struct {
	ch   chan *fpv1.AppEvent
	done chan struct{}
}

func (c *Client) startAppEventWorker(ctx context.Context, stream grpc.BidiStreamingClient[fpv1.WatchRequest, fpv1.WatchResponse], sendMu *sync.Mutex) *appEventWorker {
	w := &appEventWorker{ch: make(chan *fpv1.AppEvent, appEventQueue), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-w.ch:
				ev := AppEvent{
					ID: m.GetId(), Seq: m.GetSeq(), Kind: m.GetKind(), UserID: m.GetUserId(),
					Payload: json.RawMessage(m.GetPayload()), CreatedAt: time.UnixMilli(m.GetCreatedAtMs()),
				}
				if err := c.opts.OnAppEvent(ctx, ev); err != nil {
					c.opts.Logger.Warn("fpsdk: 业务事件处理失败，等 fp 重投", "id", ev.ID, "kind", ev.Kind, "err", err)
					continue
				}
				sendMu.Lock()
				err := stream.Send(&fpv1.WatchRequest{Msg: &fpv1.WatchRequest_Ack{Ack: &fpv1.EventAck{Ids: []string{ev.ID}}}})
				sendMu.Unlock()
				if err != nil {
					// 流断了：没确认的 fp 会在重连后补发，这里不重试。
					return
				}
			}
		}
	}()
	return w
}

// offer 非阻塞地交给 worker；队列满时丢弃，等 fp 超时重投。
func (w *appEventWorker) offer(m *fpv1.AppEvent) {
	select {
	case w.ch <- m:
	default:
	}
}
