package bus

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/redis/go-redis/v9"
)

type SignalKind int

const (
	SignalEnvelope SignalKind = iota
	// SignalGap 表示 go-redis 断线后自动重订阅了一次：中间的消息已经丢了，
	// 上层要把本地连接重新登记一遍（spec 第七节"Redis 断线重连"）。
	SignalGap
)

type Signal struct {
	Kind SignalKind
	Env  Envelope
}

type Bus struct {
	c   redis.UniversalClient
	run *redisx.Runner
}

func New(c redis.UniversalClient, run *redisx.Runner) *Bus { return &Bus{c: c, run: run} }

// Publish 走写管道，SPUBLISH 的返回值（订阅者数）没人关心。
func (b *Bus) Publish(ctx context.Context, nodeID string, env Envelope) error {
	return b.run.Run(ctx, func(p redis.Pipeliner) {
		p.SPublish(ctx, model.NodeChannel(nodeID), env.Encode())
	})
}

// Subscribe 订阅本节点频道。返回的 closeFn 幂等。
//
// 出通道缓冲 1024：节点频道是全节点所有跨节点消息的总入口，缓冲小了会在
// 突发时阻塞 go-redis 的读协程。
//
// 用 ChannelWithSubscriptions 而不是 Channel：连接抖动时 go-redis 会静默重连
// 并重发 SSUBSCRIBE，Channel() 对调用方完全不可见这件事，丢失的消息永久
// 消失且不留痕迹。ChannelWithSubscriptions 额外投递 *redis.Subscription，
// 把这次重连翻译成下面的 SignalGap。
//
// 注意 ChannelWithSubscriptions 与 Channel 互斥：go-redis 内部对同一个 PubSub
// 先调用其中一个、后调用另一个会 panic，全仓库不能再对同一个 sub 调用 Channel()。
func (b *Bus) Subscribe(ctx context.Context, nodeID string) (<-chan Signal, func(), error) {
	sub := b.c.SSubscribe(ctx, model.NodeChannel(nodeID))
	// Receive 会阻塞到订阅确认返回，确保这之后发布的消息不会丢。
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("bus: 订阅节点频道: %w", err)
	}

	out := make(chan Signal, 1024)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(out)
		defer sub.Close()
		// 这里不设"跳过第一条"的特判：初次订阅确认已经被上面那次同步的
		// sub.Receive(ctx) 读走并消耗掉了，不会再出现在
		// ChannelWithSubscriptions() 里，所以这个循环能看到的每一条
		// *redis.Subscription 都必然来自重连。实测验证过这一点（CLIENT KILL
		// TYPE pubsub 制造重连，观察到 Receive 之后、重连之前的窗口里
		// ChannelWithSubscriptions 不会再投递任何 *redis.Subscription；
		// 完整过程见 task-6-report.md）。若在这里加一个"第一条不算"的分支，
		// 效果是把第一次真实重连误判成初次订阅而放过：Redis 抖动后第一次
		// 断线不会产生 SignalGap，上层不会重新登记连接，连接静默失联——
		// 这正是本函数要防的问题，换个地方重新出现在防它的代码里。
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-sub.ChannelWithSubscriptions():
				if !ok {
					return
				}
				var sig Signal
				switch v := m.(type) {
				case *redis.Subscription:
					sig = Signal{Kind: SignalGap}
				case *redis.Message:
					env, err := Decode([]byte(v.Payload))
					if err != nil {
						slog.Warn("bus: 丢弃损坏的信封", "err", err, "len", len(v.Payload))
						continue
					}
					sig = Signal{Kind: SignalEnvelope, Env: env}
				default:
					continue
				}
				// SignalGap 和 SignalEnvelope 用同一处 guarded send：
				// 若各写各的、SignalGap 那支不带 <-ctx.Done()，close(out) 前
				// out 恰好写满时这个分支会永久阻塞，goroutine 泄漏。
				select {
				case out <- sig:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, cancel, nil
}
