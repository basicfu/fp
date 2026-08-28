package grpcapi

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
)

// revokeBufferSize 是每个订阅者的缓冲深度。
//
// 撤销是低频事件（管理员操作、用户登出），缓冲主要用来吸收 gRPC 写入的
// 瞬时抖动，不需要很深。满了就丢——见 fanout 的说明。
const revokeBufferSize = 64

// revokeSubscriber 是 RevokeHub 对撤销信号源的全部依赖。生产环境唯一的
// 实现是 *store.RevokePublisher；拆出这个接口纯粹是为了测试能换上一个
// 可以精确控制"订阅几时完成"的假实现，证明 Ready()（见其注释）确实等
// 订阅真正建立才关闭，而不是在发起订阅的同时就关闭——真实 Redis 在局域网
// 内的订阅确认通常只要个位数毫秒，光靠时序观测"关闭前"这件事并不可靠，
// 哪怕重新引入这个任务要防的 bug，观测窗口也大概率照样通过。
// 见 server_test.go 的 TestReadyWaitsForSubscriptionToComplete。
type revokeSubscriber interface {
	Subscribe(ctx context.Context) (<-chan store.RevokeSignal, func(), error)
}

// RevokeHub 把一份 Redis 撤销订阅扇出给进程内所有 Watch 流。
//
// 只开一份订阅：每条流各订一份的话，连接数与反序列化开销都随流数线性增长，
// 而它们收到的是完全相同的消息。
type RevokeHub struct {
	pub revokeSubscriber

	mu     sync.RWMutex
	next   uint64
	subs   map[uint64]*revokeSub
	closed bool

	// subscribeCalls 数的是 h.pub.Subscribe 被调用的次数。
	// 生产路径（Run）只会调用一次，恒为 1；测试用它验证"N 个 hub.Subscribe(appID)
	// 注册不会触发额外的 Redis 订阅"（见 redisSubscribeCalls）。
	subscribeCalls atomic.Int64

	// ready 在 h.pub.Subscribe 确认订阅建立后关闭，见 Run 与 Ready 的注释。
	ready chan struct{}
}

// HubEvent 是推给一条 Watch 流的消息。
type HubEvent struct {
	// Purge 为 true 时要求 SDK 丢弃**全部**缓存，此时 Revoke 字段无意义。
	Purge  bool
	Reason string
	Revoke domain.RevokeEvent
}

type revokeSub struct {
	appID uuid.UUID
	ch    chan HubEvent
}

// newRevokeHub 构造一个还没接上任何信号源的 RevokeHub。
//
// 拆出这个不接 pub 的内部入口，是为了测试能完全绕开 Redis：
// newTestHubWithFakeSignals 用它拼出一个只喂 run(ctx, 假 channel) 的 hub，
// 直接测分发逻辑本身（尤其 Gap → Purge），不依赖真实 Redis 订阅/断连——
// 那既慢又不稳定，而且测的根本不是分发逻辑，是 Redis 客户端的重连行为。
func newRevokeHub() *RevokeHub {
	return &RevokeHub{subs: make(map[uint64]*revokeSub), ready: make(chan struct{})}
}

// NewRevokeHub 构造 RevokeHub。
func NewRevokeHub(pub *store.RevokePublisher) *RevokeHub {
	h := newRevokeHub()
	h.pub = pub
	return h
}

// subscribe 是 h.pub.Subscribe 的计数包装。
//
// 计数本身只有测试关心，但生产路径 Run 也经过这个方法，而不是绕开它直接
// 调 h.pub.Subscribe——这样测试断言的是 Run 实际会执行的代码，不是另一份
// 平行实现。
func (h *RevokeHub) subscribe(ctx context.Context) (<-chan store.RevokeSignal, func(), error) {
	h.subscribeCalls.Add(1)
	return h.pub.Subscribe(ctx)
}

// Run 订阅 Redis 并持续扇出，直到 ctx 取消。整个进程只调用一次。
//
// ctx 必须是可取消的：Subscribe 内部虽已派生子 ctx 并由 closeFn 兜底，
// 但这里的 defer 是唯一保证进程退出时那条 Redis 连接被关掉的地方。
//
// 订阅确认建立后、开始分发之前关闭 h.ready（见 Ready 的注释）。顺序刻意
// 写死在这两步之间：关早了，Ready() 就是一句假承诺；关晚了（比如挪到
// h.run 返回之后）就等同于永不 ready，调用方会一直卡在启动阶段。
//
// Run 的返回值契约是"ctx 结束就返回 nil，非 nil 只代表订阅本身真的失败
// 了"——h.run 分发循环早就是这么做的（ctx.Done() 时 return nil），这里
// 让 h.subscribe 失败的分支跟它保持一致：如果失败是因为 ctx 在订阅确认
// 之前就被取消（ctx.Err() != nil），说明这不是订阅出了问题，是调用方
// 主动要求关闭（一次正常的 SIGTERM/Ctrl-C），照样返回 nil。
//
// 这条统一契约不是装饰性的一致性追求：调用方（grpcapi.Server.ServeWhenReady
// 进而 cmd/fp/main.go）用 Run 的返回值是不是 nil 来判断"这次退出算不算
// 故障"，进而决定进程退出码是 0 还是 1。如果这里对 ctx 取消导致的失败
// 也原样透传错误，一次操作员或编排系统发起的、完全正常的关闭信号，只要
// 恰好落在"订阅确认还没收到"这个窗口内（网络慢、Redis 抖动时能拉长到
// 接近超时前的任意时刻），就会被上报成致命错误——K8s 滚动发布或驱逐撞
// 上这个窗口，会把一次干净关闭误计成一次崩溃重启。
//
// 用 ctx.Err() != nil 判断，而不是 errors.Is(err, context.Canceled)：
// 前者直接问"我自己这个 ctx 是不是已经结束了"，不依赖底层订阅实现
// （真实 Redis 客户端、还是测试用的假实现）具体怎么包装 ctx 被取消时
// 返回的错误——只要是因为我方 ctx 结束导致的失败，不管错误值长什么样，
// 都不该当成真失败上报；顺带也覆盖了 ctx 带截止时间时的
// context.DeadlineExceeded，而不只是 context.Canceled 这一种。
func (h *RevokeHub) Run(ctx context.Context) error {
	signals, closeFn, err := h.subscribe(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer closeFn()
	close(h.ready)
	return h.run(ctx, signals)
}

// Ready 在 Redis 订阅确认建立后关闭。
//
// 只有经 Run 驱动的订阅才会关闭它；测试里绕开 Run、直接给 run(ctx, signals)
// 喂假信号的路径（newTestHubWithFakeSignals）不会关闭 ready——那些测试
// 验证的是分发逻辑本身，不关心订阅是否就绪。
//
// 调用方必须在开始接受 gRPC 连接之前等待它（grpcapi.Server.Ready 转发
// 的就是这个 channel，main.go 带超时地等它）：Watch 一旦被处理就会给
// 客户端发 ready，SDK 把 ready 当作"此后的撤销不会漏推"的承诺（proto
// WatchReady 的注释）。这份承诺只有在本 hub 已经真正订阅上 Redis 之后
// 才成立——没订阅上时，Watch 仍能正常走完自己的"注册到 hub → 发 ready"
// 这一步（那只是进程内的 map 操作，不依赖 Redis），但期间发布的撤销
// 事件在 Redis 侧根本没有本实例的订阅去接收，永久丢失，SDK 却毫不知情、
// 不会收紧本地缓存窗口。fp 重启时全部 SDK 同时重连，这个窗口正好撞在
// 进程刚起、订阅还没建好的那一刻，是最容易触发的时候。
func (h *RevokeHub) Ready() <-chan struct{} {
	return h.ready
}

// run 是纯分发循环：不关心 signals 从哪来（真实 Redis 订阅，还是测试直接
// 投喂的 fake channel），只负责把信号翻译成对订阅者的动作。Run 自己就是
// 靠这个方法实现的；拆出来纯粹是为了测试能绕开 Redis 单独验证这部分逻辑。
func (h *RevokeHub) run(ctx context.Context, signals <-chan store.RevokeSignal) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case sig, ok := <-signals:
			if !ok {
				return nil
			}
			switch sig.Kind {
			case store.RevokeSignalGap:
				// 订阅刚刚重建，期间的事件已永久丢失，且不知道丢了哪些。
				// 唯一安全的动作是让本实例下所有 SDK 清空缓存。
				h.broadcastPurge("redis 订阅重建")
			default:
				h.fanout(sig.Event)
			}
		}
	}
}

// fanout 把一条事件分发给匹配的订阅者。
//
// 写入用非阻塞 select：一条卡住的 gRPC 流（客户端被 SIGSTOP、网络黑洞、
// 对端不读）会让写入永久阻塞，同步写就会把整个中继堵死——一个客户端的
// 故障演变成全平台推送失效。
//
// 丢弃是安全的：推送只是把撤销延迟从 cache_ttl 压到近乎实时的**加速手段**，
// 权威撤销早已通过删除 Redis 会话完成。丢一条的后果是那个 SDK 最长
// cache_ttl 之后回源被拒——正是没有推送时的行为。
func (h *RevokeHub) fanout(ev domain.RevokeEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.subs {
		// AppID 为 Nil 表示跨全部应用（改密、冻结）——必须推给所有人。
		// 把 Nil 当成一个具体应用 ID 去等值比较，这两类撤销一条也推不出去。
		if ev.AppID != uuid.Nil && ev.AppID != sub.appID {
			continue
		}
		select {
		case sub.ch <- HubEvent{Revoke: ev}:
		default:
			slog.Warn("grpcapi: 撤销事件被丢弃，订阅者缓冲已满",
				"appId", sub.appID, "reason", ev.Reason)
		}
	}
}

// broadcastPurge 让本实例下的**全部**订阅者清空缓存。
//
// 刻意不按 appID 过滤：触发它的前提正是"不知道丢了哪些事件"，
// 自然也不知道涉及哪些应用。按应用过滤等于在没有依据的情况下缩小范围。
//
// 范围仅限本实例：其他 fp 实例的订阅没有中断过，它们的 SDK 不需要清缓存。
// 爆炸半径就是真正丢了事件的那一台。
//
// 缓冲满时**不能**像 fanout 那样直接丢弃。两者的代价不对等：丢一条撤销
// 只让一个 token 多活一个 cache_ttl，丢一条 purge 会让整个 SDK 的缓存
// 停在一个**已知不可信**的状态，而且没有任何后续机制会纠正它。
//
// 所以改为关掉那条流。这不激进——撤销频率约每秒 0.1 次，填满 64 格缓冲
// 需要十分钟不读，而 keepalive 十秒就该把这样的连接判死了。关流之后 SDK
// 会重连并 purge（Task 10 已有），复用现成机制，不必新造一套补发逻辑。
func (h *RevokeHub) broadcastPurge(reason string) {
	// 写锁：下面可能要删订阅者。purge 罕见，锁的粒度不重要。
	h.mu.Lock()
	defer h.mu.Unlock()

	slog.Warn("grpcapi: 广播缓存清空指令", "reason", reason, "subscribers", len(h.subs))
	for id, sub := range h.subs {
		select {
		case sub.ch <- HubEvent{Purge: true, Reason: reason}:
		default:
			// Go 允许 range 期间 delete。关掉 channel 会让 Watch 走到
			// !ok 分支正常返回；它 deferred 的注销函数再 delete 一次是空操作。
			// 这里先从 map 摘掉，Close() 就不会二次关闭同一个 channel。
			slog.Warn("grpcapi: 订阅者缓冲已满且需要 purge，关闭该流", "appId", sub.appID)
			close(sub.ch)
			delete(h.subs, id)
		}
	}
}

// Subscribe 登记一个订阅者。返回的函数必须被调用，否则订阅者永久驻留。
func (h *RevokeHub) Subscribe(appID uuid.UUID) (<-chan HubEvent, func()) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		ch := make(chan HubEvent)
		close(ch)
		return ch, func() {}
	}

	sub := &revokeSub{appID: appID, ch: make(chan HubEvent, revokeBufferSize)}
	h.next++
	id := h.next
	h.subs[id] = sub
	h.mu.Unlock()

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
// 主动结束的长流，不关掉订阅就没有任何机制能让那些 handler 退出，
// 进程会在关闭阶段永远挂住。
//
// 关闭在写锁下进行，与 fanout 的读锁互斥，因此不会出现"向已关闭的
// channel 发送"。之后 Subscribe 返回一个已关闭的 channel，调用方
// （Watch）会立刻走到 !ok 分支正常返回。
func (h *RevokeHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.subs {
		close(sub.ch)
	}
	h.subs = nil
	h.closed = true
}

// subscriberCount 是测试辅助：当前登记的订阅者数。
func (h *RevokeHub) subscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// redisSubscribeCalls 是测试辅助：h.pub.Subscribe 被调用过多少次。
func (h *RevokeHub) redisSubscribeCalls() int64 {
	return h.subscribeCalls.Load()
}
