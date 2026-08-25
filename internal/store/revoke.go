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

// Subscribe 订阅撤销事件。返回的 channel 在 ctx 取消或调用 close 时关闭。
//
// ctx 取消必须显式转成 sub.Close()：go-redis 的 PubSub.Channel() 内部
// reader goroutine 跑在 context.TODO() 上，它的 msgCh 只在 PubSub.Close()
// 时才关。光靠下面 select 里的 <-ctx.Done() 是不够的——out 有 64 的缓冲，
// 两个 case 常常同时就绪，Go 会随机选一个；而在完全没有消息流入时，
// goroutine 会一直阻塞在 range sub.Channel() 上，out 永不关闭，
// Redis 那条订阅连接也一直挂着。计划二的 gRPC 中继正是这里的调用方，
// 每条中继泄漏一个连接是实打实的泄漏。
func (p *RevokePublisher) Subscribe(ctx context.Context) (<-chan domain.RevokeEvent, func(), error) {
	sub := p.rdb.Subscribe(ctx, revokeChannel)
	// Receive 会阻塞到订阅确认返回，确保这之后发布的消息不会丢。
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("store: 订阅撤销频道: %w", err)
	}

	// ctx 取消时主动关掉底层订阅，这才是让 reader goroutine 退出的唯一途径。
	go func() {
		<-ctx.Done()
		_ = sub.Close()
	}()

	out := make(chan domain.RevokeEvent, 64)
	go func() {
		defer close(out)
		for msg := range sub.Channel() {
			var ev domain.RevokeEvent
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				slog.Error("store: 解析撤销事件失败", "err", err)
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, func() { _ = sub.Close() }, nil
}
