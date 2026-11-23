package grpcapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/cluster"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// CapLoginCheck 是 WatchHello 里「本流回答登录校验」的能力名。
const CapLoginCheck = "login_check"

// LoginCheckNodes 是转发对节点心跳与流登记的依赖，*cluster.Liveness 满足它。
// TrackApp、SetServing 在转发器的锁内调用，实现必须是不阻塞的内存操作。
type LoginCheckNodes interface {
	NodeID() string
	TrackApp(app string)
	Refresh(ctx context.Context) error
	ServerNodes(app string) []string
	SetServing(ctx context.Context, app string, on bool) error
	DropLocal(node string)
}

// LoginCheckBus 是节点频道，*cluster.Bus 满足它。
type LoginCheckBus interface {
	Publish(ctx context.Context, nodeID string, payload []byte) (int64, error)
	Subscribe(ctx context.Context, nodeID string) (<-chan cluster.Signal, func(), error)
}

// lcStreamBuffer 是每条流待发登录校验的缓冲。满了说明那条流读不动，换别的流或别的实例。
const lcStreamBuffer = 64

// lcMessage 是节点频道上的登录校验请求（t=lc_req）与应答（t=lc_resp），字段名是跨实例的线上格式。
type lcMessage struct {
	T            string `json:"t"`
	CallID       string `json:"call_id"`
	ReplyNode    string `json:"reply_node,omitempty"`
	AppID        string `json:"app_id,omitempty"`
	DeviceHeader string `json:"device_header,omitempty"`
	IP           string `json:"ip,omitempty"`
	Allow        bool   `json:"allow,omitempty"`
	ClientRef    string `json:"client_ref,omitempty"`
	Code         string `json:"code,omitempty"`
	Msg          string `json:"msg,omitempty"`
	NoStream     bool   `json:"no_stream,omitempty"`
}

// lcResult 是一次等待的结局；gone 表示这一处没能问到（流断了、对方没有流），调用方换下一处。
type lcResult struct {
	ans  service.LoginCheckAnswer
	gone bool
}

// lcStream 是一条声明了 login_check 的 Watch 流。calls 由 relay.mu 保护。
type lcStream struct {
	app   uuid.UUID
	out   chan *fpv1.LoginCheck
	calls map[string]chan lcResult
}

// LoginCheckRelay 把登录校验发到本实例的 Watch 流上，本实例没有流时经节点频道定向转发给持流的实例。
//
// 登记用 fp 核心自己的 cluster 键（fp:core:*），与 fp-im 的键不重叠。只有声明了 login_check 的流
// 才进登记：没设回调的老 SDK 不会被选中，免得每次都白等 3 秒。
type LoginCheckRelay struct {
	nodes LoginCheckNodes
	bus   LoginCheckBus
	// serveTimeout 是替别的实例问本地流时等应用的上限，与发起方的全程超时一致。
	serveTimeout time.Duration

	mu      sync.Mutex
	streams map[uuid.UUID][]*lcStream
	rr      uint64
	pending map[string]chan lcResult
	tracked map[uuid.UUID]bool
}

// NewLoginCheckRelay 构造转发器。
func NewLoginCheckRelay(nodes LoginCheckNodes, bus LoginCheckBus) *LoginCheckRelay {
	return &LoginCheckRelay{
		nodes: nodes, bus: bus, serveTimeout: service.LoginCheckTimeout,
		streams: map[uuid.UUID][]*lcStream{}, pending: map[string]chan lcResult{}, tracked: map[uuid.UUID]bool{},
	}
}

// NodeID 是本实例在登记里的标识。
func (r *LoginCheckRelay) NodeID() string { return r.nodes.NodeID() }

// Register 登记一条流。第一条流登记时本实例才在 Redis 里声明服务该应用。
func (r *LoginCheckRelay) Register(app uuid.UUID) *lcStream {
	s := &lcStream{app: app, out: make(chan *fpv1.LoginCheck, lcStreamBuffer), calls: map[string]chan lcResult{}}
	r.mu.Lock()
	defer r.mu.Unlock()
	// SetServing 只改内存，放在锁内：锁外调用时并发的注册与注销可能后发先至，把登记反转成与流的实际有无相反。
	if len(r.streams[app]) == 0 {
		r.nodes.TrackApp(app.String())
		_ = r.nodes.SetServing(context.Background(), app.String(), true)
	}
	r.streams[app] = append(r.streams[app], s)
	return s
}

// Unregister 注销一条流；它上面还在等的校验立刻判「没问到」，发起方好换下一处而不是等满超时。
func (r *LoginCheckRelay) Unregister(s *lcStream) {
	r.mu.Lock()
	list := r.streams[s.app]
	for i, x := range list {
		if x == s {
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(r.streams, s.app)
		_ = r.nodes.SetServing(context.Background(), s.app.String(), false)
	} else {
		r.streams[s.app] = list
	}
	calls := s.calls
	s.calls = map[string]chan lcResult{}
	r.mu.Unlock()
	for _, ch := range calls {
		ch <- lcResult{gone: true}
	}
}

// Reply 收下流上发来的应答。只认发给这条流的 call_id：别的流（哪怕同一应用）答不了它。
func (r *LoginCheckRelay) Reply(s *lcStream, m *fpv1.LoginCheckReply) {
	r.mu.Lock()
	ch := s.calls[m.GetCallId()]
	delete(s.calls, m.GetCallId())
	r.mu.Unlock()
	if ch != nil {
		ch <- lcResult{ans: service.LoginCheckAnswer{
			Allow: m.GetAllow(), ClientRef: m.GetClientRef(), Code: m.GetCode(), Msg: m.GetMsg(),
		}}
	}
}

// AskLocal 发给本实例该应用的一条流并等回答。没有流返回 handled=false；发出去之前那条流断了就换本实例的另一条。
func (r *LoginCheckRelay) AskLocal(ctx context.Context, call service.LoginCheckCall) (service.LoginCheckAnswer, bool, error) {
	tried := map[*lcStream]bool{}
	for {
		ch := make(chan lcResult, 1)
		r.mu.Lock()
		var s *lcStream
		list := r.streams[call.AppID]
		for i := range list {
			cand := list[(int(r.rr)+i)%len(list)]
			if !tried[cand] {
				s = cand
				break
			}
		}
		r.rr++
		if s == nil {
			r.mu.Unlock()
			return service.LoginCheckAnswer{}, false, nil
		}
		tried[s] = true
		s.calls[call.CallID] = ch
		r.mu.Unlock()

		select {
		case s.out <- &fpv1.LoginCheck{CallId: call.CallID, DeviceHeader: call.DeviceHeader, Ip: call.IP}:
		default:
			r.forget(s, call.CallID)
			continue
		}
		select {
		case res := <-ch:
			if res.gone {
				continue
			}
			return res.ans, true, nil
		case <-ctx.Done():
			r.forget(s, call.CallID)
			return service.LoginCheckAnswer{}, true, ctx.Err()
		}
	}
}

func (r *LoginCheckRelay) forget(s *lcStream, callID string) {
	r.mu.Lock()
	delete(s.calls, callID)
	r.mu.Unlock()
}

// Candidates 返回本实例之外持有该应用流的实例，按 call_id 做 HRW 排序：同一次校验的先后稳定，
// 不同校验均匀摊到各实例上。
func (r *LoginCheckRelay) Candidates(ctx context.Context, app uuid.UUID, callID string) []string {
	self := r.nodes.NodeID()
	var others []string
	for _, n := range r.ServerNodes(ctx, app) {
		if n != self {
			others = append(others, n)
		}
	}
	return cluster.Rank(others, callID)
}

// ServerNodes 返回登记里持有该应用流的实例（含本实例）。第一次问某个应用时同步刷新一次快照：
// 此前没追踪它，快照里必然是空的，不刷新的话头一个登录要等到下一次心跳才转发得出去。
func (r *LoginCheckRelay) ServerNodes(ctx context.Context, app uuid.UUID) []string {
	r.mu.Lock()
	fresh := !r.tracked[app]
	r.tracked[app] = true
	r.mu.Unlock()
	if fresh {
		r.nodes.TrackApp(app.String())
		if err := r.nodes.Refresh(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("grpcapi: 刷新登录校验登记失败", "err", err, "appId", app)
		}
	}
	return r.nodes.ServerNodes(app.String())
}

// AskRemote 把校验发到 node 的节点频道并等应答。对方不在线（订阅数为 0）或回了没有流，返回 handled=false。
func (r *LoginCheckRelay) AskRemote(ctx context.Context, node string, call service.LoginCheckCall) (service.LoginCheckAnswer, bool, error) {
	ch := make(chan lcResult, 1)
	r.mu.Lock()
	r.pending[call.CallID] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.pending[call.CallID] == ch {
			delete(r.pending, call.CallID)
		}
		r.mu.Unlock()
	}()

	payload, err := json.Marshal(lcMessage{
		T: "lc_req", CallID: call.CallID, ReplyNode: r.nodes.NodeID(), AppID: call.AppID.String(),
		DeviceHeader: call.DeviceHeader, IP: call.IP,
	})
	if err != nil {
		return service.LoginCheckAnswer{}, false, err
	}
	n, err := r.bus.Publish(ctx, node, payload)
	if err != nil {
		return service.LoginCheckAnswer{}, false, err
	}
	if n == 0 {
		// 只剔本地视图：订阅数为 0 也可能是对方正在重连，它下次心跳会自己长回来。
		r.nodes.DropLocal(node)
		return service.LoginCheckAnswer{}, false, nil
	}
	select {
	case res := <-ch:
		if res.gone {
			return service.LoginCheckAnswer{}, false, nil
		}
		return res.ans, true, nil
	case <-ctx.Done():
		return service.LoginCheckAnswer{}, true, ctx.Err()
	}
}

// Run 订阅本节点频道直到 ctx 取消，订阅断了每秒重试。重订阅期间丢的消息不补：发起方会超时或换下一个实例。
func (r *LoginCheckRelay) Run(ctx context.Context) {
	for {
		sigs, closeFn, err := r.bus.Subscribe(ctx, r.nodes.NodeID())
		if err == nil {
			r.consume(ctx, sigs)
			closeFn()
		} else if ctx.Err() == nil {
			slog.Warn("grpcapi: 订阅登录校验节点频道失败", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (r *LoginCheckRelay) consume(ctx context.Context, sigs <-chan cluster.Signal) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-sigs:
			if !ok {
				return
			}
			if sig.Kind != cluster.SignalMessage {
				continue
			}
			var m lcMessage
			if err := json.Unmarshal(sig.Payload, &m); err != nil {
				continue
			}
			switch m.T {
			case "lc_req":
				go r.serveRemote(ctx, m)
			case "lc_resp":
				r.mu.Lock()
				ch := r.pending[m.CallID]
				r.mu.Unlock()
				if ch != nil {
					select {
					case ch <- lcResult{gone: m.NoStream, ans: service.LoginCheckAnswer{
						Allow: m.Allow, ClientRef: m.ClientRef, Code: m.Code, Msg: m.Msg,
					}}:
					default:
					}
				}
			}
		}
	}
}

// serveRemote 替别的实例问本地流。本地没有流立刻回 no_stream；应用超时不回，发起方自己会超时。
func (r *LoginCheckRelay) serveRemote(parent context.Context, m lcMessage) {
	app, err := uuid.Parse(m.AppID)
	if err != nil || m.ReplyNode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, r.serveTimeout)
	defer cancel()
	ans, handled, err := r.AskLocal(ctx, service.LoginCheckCall{
		CallID: m.CallID, AppID: app, DeviceHeader: m.DeviceHeader, IP: m.IP,
	})
	if err != nil {
		return
	}
	payload, err := json.Marshal(lcMessage{
		T: "lc_resp", CallID: m.CallID, NoStream: !handled,
		Allow: ans.Allow, ClientRef: ans.ClientRef, Code: ans.Code, Msg: ans.Msg,
	})
	if err != nil {
		return
	}
	if _, err := r.bus.Publish(ctx, m.ReplyNode, payload); err != nil && ctx.Err() == nil {
		slog.Warn("grpcapi: 回登录校验应答失败", "err", err, "node", m.ReplyNode)
	}
}
