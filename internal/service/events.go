package service

import (
	"context"
	"log/slog"

	"github.com/basicfu/fp/internal/domain"
)

// EventPublisher 广播推送事件。*store.RevokePublisher 满足它。
type EventPublisher interface {
	Publish(ctx context.Context, ev domain.RevokeEvent) error
}

// publishEvent 发布失败只记日志：推送只是加速手段，SDK 另有缓存 TTL 与定时重拉兜底。
func publishEvent(ctx context.Context, pub EventPublisher, ev domain.RevokeEvent) {
	if pub == nil {
		return
	}
	if err := pub.Publish(ctx, ev); err != nil {
		slog.Error("service: 广播事件失败", "err", err, "kind", ev.Kind)
	}
}
