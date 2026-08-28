package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
)

// revokeChannel 是撤销事件的 Redis 广播频道。
// fp 的每个实例都订阅它，再通过各自持有的 gRPC 双向流推给 SDK（计划二）。
const revokeChannel = "fp:revoke"

// RevokePublisher 广播撤销事件。
type RevokePublisher struct {
	rdb *redis.Client
}

// NewRevokePublisher 构造 RevokePublisher。
func NewRevokePublisher(rdb *redis.Client) *RevokePublisher {
	return &RevokePublisher{rdb: rdb}
}

// Publish 广播一次撤销。
//
// 没有订阅者时不算错误：推送只是把撤销延迟从 cache_ttl 压到近乎实时的**加速手段**，
// 权威撤销已经通过删除 Redis 会话完成了。
func (p *RevokePublisher) Publish(ctx context.Context, ev domain.RevokeEvent) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("store: 序列化撤销事件: %w", err)
	}
	if err := p.rdb.Publish(ctx, revokeChannel, raw).Err(); err != nil {
		return fmt.Errorf("store: 广播撤销事件: %w", err)
	}
	return nil
}

// RevokeSignalKind 区分事件流上的两类信号。
type RevokeSignalKind int

const (
	// RevokeSignalEvent 是一条撤销事件。
	RevokeSignalEvent RevokeSignalKind = iota
	// RevokeSignalGap 表示事件流出现了缺口：订阅刚刚重建，
	// 期间发布的事件已永久丢失，且无法知道丢了哪些。
	RevokeSignalGap
)

// RevokeSignal 是订阅流上的一条信号。
//
// 命名取"缺口"而非"重订阅"，是因为消费方关心的是**后果**不是成因。
// 将来若换成 Redis Stream 等别的承载方式，这个信号的含义原样成立。
type RevokeSignal struct {
	Kind RevokeSignalKind
	// Event 仅在 Kind == RevokeSignalEvent 时有效。
	Event domain.RevokeEvent
}

// Subscribe 订阅撤销事件。返回的 channel 在 ctx 取消或调用 close 时关闭。
//
// ctx 取消必须显式转成 sub.Close()：go-redis 的 PubSub reader goroutine
// 跑在 context.TODO() 上，它的输出 channel 只在 PubSub.Close() 时才关。光靠下面
// select 里的 <-ctx.Done() 是不够的——out 有 64 的缓冲，两个 case 常常同时就绪，
// Go 会随机选一个；而在完全没有消息流入时，goroutine 会一直阻塞在
// range sub.ChannelWithSubscriptions() 上，out 永不关闭，Redis 那条订阅连接也
// 一直挂着。计划二的 gRPC 中继正是这里的调用方，每条中继泄漏一个连接是
// 实打实的泄漏。
//
// 关键在于：那个等 ctx.Done() 的看门狗 goroutine 必须**也能被 closeFn 叫醒**。
// 直接监听调用方传进来的 ctx 的话，调用方若传 context.Background() 再靠 closeFn
// 清理（对一个返回了 close 函数的 API 来说是完全合理的用法），看门狗就永远等不到
// Done，一路阻塞到进程退出——泄漏从"连接"变成"goroutine"，一条 gRPC 中继一个。
// 所以这里派生一个可取消的子 ctx，closeFn 里先 cancel 再 Close：两种清理途径
// （取消父 ctx / 调 closeFn）都能让看门狗退出。
//
// 派生子 ctx 不影响订阅本身：redis.Client.Subscribe 只在最初那次 SUBSCRIBE
// 往返里用 ctx，之后的消息读取跑在 PubSub 自己的 goroutine 上。
//
// 用 ChannelWithSubscriptions 而不是 Channel：连接抖动时 go-redis 会**静默重连
// 并重发 SUBSCRIBE**——既不报错也不关 channel。Channel() 只投递 *Message，
// 重连这件事对调用方完全不可见，那个窗口丢的撤销事件永久消失且不留任何痕迹：
// 日志里没有、监控里没有，调用方（RevokeHub.Run）与它背后的 SDK 都会以为
// 推送一直健康。ChannelWithSubscriptions 额外投递 *Subscription，go-redis 的
// resubscribe() 在每次重连时都会重发 SUBSCRIBE，Redis 的确认回包就被解析成
// 一条 *Subscription{Kind:"subscribe"}。
//
// 下面的分发循环把**每一条**这样的确认都当成重连、翻译成 RevokeSignalGap，
// 不做"第一条例外"的特判——因为真正的初次订阅确认已经被上面那次同步的
// sub.Receive(ctx) 读走并消耗掉了，不会再出现在 ChannelWithSubscriptions()
// 里。如果这里还留一个"跳过第一条"的分支，效果是把第一次真实重连误判成
// 初次订阅而放过：CLIENT KILL 实测验证过这一点——按"第一条不算"实现时，
// 断连后 5 秒内一条 Gap 信号都不会出现，本任务要防的静默失效换了个地方
// 重新出现在防它的代码里。
//
// 注意 ChannelWithSubscriptions 与 Channel 互斥：go-redis 内部对同一个 PubSub
// 先调用其中一个、后调用另一个会 panic，所以全仓库不能再有别处对同一个
// sub 调用 Channel()。
func (p *RevokePublisher) Subscribe(ctx context.Context) (<-chan RevokeSignal, func(), error) {
	sub := p.rdb.Subscribe(ctx, revokeChannel)
	// Receive 会阻塞到订阅确认返回，确保这之后发布的消息不会丢。
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("store: 订阅撤销频道: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	// ctx 取消时主动关掉底层订阅，这才是让 reader goroutine 退出的唯一途径。
	go func() {
		<-ctx.Done()
		_ = sub.Close()
	}()

	out := make(chan RevokeSignal, 64)
	go func() {
		defer close(out)

		// resubscribeCount 数的是本循环观察到的 SUBSCRIBE 确认回包，
		// 也就是 go-redis 重连的次数。这里能看到的每一条都必然来自重连——
		// 真正"初次订阅"的那一条确认已经被上面 sub.Receive(ctx) 那次同步调用
		// 读走了（那正是它存在的意义：阻塞到确认，Subscribe 才能安全返回），
		// 不会再流到这个 channel 里。
		resubscribeCount := 0

		for msg := range sub.ChannelWithSubscriptions() {
			var sig RevokeSignal
			switch m := msg.(type) {
			case *redis.Subscription:
				if m.Kind != "subscribe" {
					continue
				}
				resubscribeCount++
				slog.Warn("store: Redis 订阅已重建，期间的撤销事件已丢失",
					"resubscribeCount", resubscribeCount)
				sig = RevokeSignal{Kind: RevokeSignalGap}
			case *redis.Message:
				var ev domain.RevokeEvent
				if err := json.Unmarshal([]byte(m.Payload), &ev); err != nil {
					slog.Error("store: 解析撤销事件失败", "err", err)
					continue
				}
				sig = RevokeSignal{Kind: RevokeSignalEvent, Event: ev}
			default:
				continue
			}

			select {
			case out <- sig:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, func() { cancel(); _ = sub.Close() }, nil
}
