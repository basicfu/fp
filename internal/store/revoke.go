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

// Subscribe 订阅撤销事件。
// 返回的 channel 在 ctx 取消或调用 close 时关闭。
func (p *RevokePublisher) Subscribe(ctx context.Context) (<-chan domain.RevokeEvent, func(), error) {
	sub := p.rdb.Subscribe(ctx, revokeChannel)
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("store: 订阅撤销频道: %w", err)
	}

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
