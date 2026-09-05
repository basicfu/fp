package grpcapi

import (
	"context"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/store"
)

// configBufferSize 是每个订阅者的缓冲深度。
//
// 配置变更是极低频事件（人点保存），缓冲只用来吸收 gRPC 写入的瞬时抖动。
// 满了就摘掉订阅者——见 fanout 的说明。
const configBufferSize = 8

// ConfigEvent 是推给一条 Watch 流的配置事件。
type ConfigEvent struct {
	// Type 是变了的分区。**空串表示"分区未知，请重拉全部绑定"**，
	// 来源是 store.ConfigSignal.Gap（Redis 订阅重建，漏读且不知道漏了哪些）。
	Type string
	// Seq 仅用于日志与排障：SDK 拉的是"当前版本"不是"第 Seq 版"。
	Seq int64
}

// configSubscriber 是 ConfigHub 对信号源的全部依赖。拆出接口是为了测试
// 能换上一个可精确控制的假实现（同 revokeSubscriber）。
type configSubscriber interface {
	Subscribe(ctx context.Context) (<-chan store.ConfigSignal, func(), error)
}

// ConfigHub 把一份 Redis 配置订阅扇出给进程内所有 Watch 流。
type ConfigHub struct {
	pub configSubscriber

	mu     sync.RWMutex
	next   uint64
	subs   map[uint64]*configSub
	closed bool

	ready chan struct{}
}

type configSub struct {
	appID uuid.UUID
	ch    chan ConfigEvent
}

func newConfigHub() *ConfigHub {
	return &ConfigHub{subs: make(map[uint64]*configSub), ready: make(chan struct{})}
}

// NewConfigHub 构造 ConfigHub。
func NewConfigHub(pub *store.ConfigPublisher) *ConfigHub {
	h := newConfigHub()
	h.pub = pub
	return h
}

// Run 订阅 Redis 并把信号扇出，直到 ctx 取消。
func (h *ConfigHub) Run(ctx context.Context) error {
	signals, closeSub, err := h.pub.Subscribe(ctx)
	if err != nil {
		return err
	}
	defer closeSub()
	close(h.ready)
	h.run(ctx, signals)
	return nil
}

// Ready 在订阅**真正建立**之后关闭。
//
// 光靠"建流没报错"是不够的：流建立了、但服务端还没订上 Redis 的那段时间里，
// 配置变更会丢，而 SDK 却以为推送可用。与 RevokeHub.Ready 同一理由。
func (h *ConfigHub) Ready() <-chan struct{} { return h.ready }

func (h *ConfigHub) run(ctx context.Context, signals <-chan store.ConfigSignal) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			h.fanout(sig)
		}
	}
}

// Subscribe 登记一个订阅者，返回它的事件 channel 与摘除函数。
func (h *ConfigHub) Subscribe(appID uuid.UUID) (<-chan ConfigEvent, func()) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		ch := make(chan ConfigEvent)
		close(ch)
		return ch, func() {}
	}
	h.next++
	id := h.next
	sub := &configSub{appID: appID, ch: make(chan ConfigEvent, configBufferSize)}
	h.subs[id] = sub
	h.mu.Unlock()

	// once 包住：Watch handler 里是 defer 调的，而 fanout 摘除时也会删同一个
	// id，两边都不该因为重复操作而 panic 或误删后来复用的 id。
	var once sync.Once
	return sub.ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, id)
			h.mu.Unlock()
		})
	}
}

// Close 关闭全部订阅者 channel，让所有 Watch handler 返回。
//
// 它存在的唯一理由是让 grpc.Server.GracefulStop 能够返回：Watch 是永不
// 主动结束的长流，不关掉订阅就没有任何机制能让那些 handler 退出。
func (h *ConfigHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.subs {
		close(sub.ch)
	}
	h.subs = nil
	h.closed = true
}

// fanout 把一条信号分发给相关订阅者。
//
// **缓冲满就摘掉订阅者**（关掉 channel 并从表里删除），而不是丢弃这一条
// 信号。丢弃是有洞的：待发的可能是 WEB 分区、被丢的是 DEFAULT，SDK 只会
// 重拉 WEB。而摘掉的后果被完整兜住——流结束 → SDK 重连 → 收到 ready →
// 重拉全部配置。与 RevokeHub 缓冲满时的处理同一逻辑。
//
// 全程持写锁：摘除要删 map、关 channel，与发送必须互斥，否则会出现
// "向已关闭的 channel 发送"。配置变更是极低频事件，写锁的代价可以忽略。
func (h *ConfigHub) fanout(sig store.ConfigSignal) {
	ev := ConfigEvent{Type: sig.Type, Seq: sig.Seq}
	if sig.Gap {
		// 订阅重建，漏读且不知道漏了哪些——空串让 SDK 重拉全部绑定。
		ev = ConfigEvent{}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for id, sub := range h.subs {
		// Gap 发给所有人；普通信号只发给它自己那个应用。
		if !sig.Gap && sub.appID != sig.AppID {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			slog.Warn("grpcapi: 配置事件缓冲已满，摘掉该订阅者；SDK 会重连并重拉配置",
				"appID", sub.appID)
			close(sub.ch)
			delete(h.subs, id)
		}
	}
}
