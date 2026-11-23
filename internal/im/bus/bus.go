package bus

import (
	"context"
	"log/slog"

	"github.com/basicfu/fp/internal/cluster"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/redis/go-redis/v9"
)

type SignalKind = cluster.SignalKind

const (
	SignalEnvelope = cluster.SignalMessage
	// SignalGap 表示 go-redis 断线后自动重订阅了一次：中间的消息已经丢了，
	// 上层要把本地连接重新登记一遍（spec 第七节"Redis 断线重连"）。
	SignalGap = cluster.SignalGap
)

type Signal struct {
	Kind SignalKind
	Env  Envelope
}

// Bus 是 cluster.Bus 的薄封装：只负责信封的编解码，频道与重连语义都在 cluster 里。
type Bus struct{ b *cluster.Bus }

func New(c redis.UniversalClient, run *cluster.Runner) *Bus {
	return &Bus{b: cluster.NewBus(c, run, model.ClusterKeys())}
}

// Publish 返回 SPUBLISH 的结果：收到这条消息的订阅者数量（语义见 cluster.Bus.Publish）。
func (b *Bus) Publish(ctx context.Context, nodeID string, env Envelope) (int64, error) {
	return b.b.Publish(ctx, nodeID, env.Encode())
}

// Subscribe 订阅本节点频道。返回的 closeFn 幂等。
func (b *Bus) Subscribe(ctx context.Context, nodeID string) (<-chan Signal, func(), error) {
	in, closeIn, err := b.b.Subscribe(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan Signal, 1024)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(out)
		for raw := range in {
			sig := Signal{Kind: raw.Kind}
			if raw.Kind == cluster.SignalMessage {
				env, err := Decode(raw.Payload)
				if err != nil {
					slog.Warn("bus: 丢弃损坏的信封", "err", err, "len", len(raw.Payload))
					continue
				}
				sig.Env = env
			}
			select {
			case out <- sig:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, func() { cancel(); closeIn() }, nil
}
