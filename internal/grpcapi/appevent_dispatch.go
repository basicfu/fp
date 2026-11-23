package grpcapi

import (
	"cmp"
	"context"
	"hash/fnv"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// CapAppEvents 是 WatchHello 里「本流收业务事件并确认」的能力名。
const CapAppEvents = "app_events"

// EventSource 是投递对事件表的全部依赖，*service.AppEvents 满足它。
type EventSource interface {
	Pending(ctx context.Context, appID uuid.UUID, afterSeq int64, limit int) ([]domain.AppEvent, error)
	Ack(ctx context.Context, appID uuid.UUID, ids []uuid.UUID) error
}

// EventLease 是投递租约，*store.Lease 满足它。
type EventLease interface {
	Acquire(ctx context.Context, key, owner string, ttl time.Duration) (bool, error)
	Renew(ctx context.Context, key, owner string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, key, owner string) error
}

// EventWaker 是「有新事件」的唤醒信号源，*store.AppEventSignal 满足它。可为 nil，只靠轮询。
type EventWaker interface {
	Subscribe(ctx context.Context) (<-chan uuid.UUID, func(), error)
}

// DispatchConfig 是投递参数，零值字段取默认值。测试缩短它们以免真等 10 秒。
type DispatchConfig struct {
	LeaseTTL   time.Duration // 默认 15 秒
	LeaseRenew time.Duration // 默认 5 秒
	AckTimeout time.Duration // 默认 10 秒
	// Poll 是轮询事件表与检查超时的间隔，默认 1 秒。唤醒信号到达时立刻处理，不等它。
	Poll time.Duration
	// InflightPerStream 是每条流已发未确认的上限，默认 100。
	InflightPerStream int
}

func (c DispatchConfig) withDefaults() DispatchConfig {
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = 15 * time.Second
	}
	if c.LeaseRenew <= 0 {
		c.LeaseRenew = 5 * time.Second
	}
	if c.AckTimeout <= 0 {
		c.AckTimeout = 10 * time.Second
	}
	if c.Poll <= 0 {
		c.Poll = time.Second
	}
	if c.InflightPerStream <= 0 {
		c.InflightPerStream = 100
	}
	return c
}

// eventFetchBatch 是每次从事件表取的条数；本地待发队列超过 eventQueueMax 就先不取，等流消化。
// ackQueueMax 是待落库确认的排队上限，ackBatchMax 是一次落库最多合并的确认条数。
const (
	eventFetchBatch = 200
	eventQueueMax   = 2000
	ackQueueMax     = 4096
	ackBatchMax     = 500
)

// ackReq 是一次流上的确认，排队等落库。
type ackReq struct {
	app uuid.UUID
	ids []uuid.UUID
}

// EventDispatcher 把事件表里的业务事件经本实例的 Watch 流投给业务方。
//
// 每个应用在本实例上有声明了 app_events 的流时，起一个投递循环去抢该应用的 Redis 租约；
// 全部 fp 实例里只有持有者投递，所以一条事件不会被两个实例同时发出。持有者的流全断开时它释放租约，
// 别的实例在下一次轮询时接手，从未确认的最小 seq 重新投递——所以已发未确认的事件可能重复，业务方按 id 幂等。
type EventDispatcher struct {
	src   EventSource
	lease EventLease
	waker EventWaker
	cfg   DispatchConfig

	root   context.Context
	cancel context.CancelFunc
	// acks 是待落库的确认：落库在单独的协程里，不占收消息的协程（同一条流上还有登录校验的应答）。
	acks chan ackReq

	mu      sync.Mutex
	apps    map[uuid.UUID]*appDispatch
	nextID  uint64
	closed  bool
	running sync.WaitGroup
}

// NewEventDispatcher 构造投递器。waker 可为 nil。
func NewEventDispatcher(src EventSource, lease EventLease, waker EventWaker, cfg DispatchConfig) *EventDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	d := &EventDispatcher{
		src: src, lease: lease, waker: waker, cfg: cfg.withDefaults(),
		root: ctx, cancel: cancel, acks: make(chan ackReq, ackQueueMax),
		apps: map[uuid.UUID]*appDispatch{},
	}
	d.running.Add(1)
	go func() {
		defer d.running.Done()
		d.runAcks()
	}()
	return d
}

// eventStream 是一条声明了 app_events 的 Watch 流在投递器里的登记。
type eventStream struct {
	id uint64
	// out 由投递循环非阻塞写入、Watch handler 读出发送。容量等于在途上限，正常情况下写不满。
	out chan *fpv1.AppEvent
}

// appDispatch 是一个应用在本实例上的投递状态。streams、acked 由 d.mu 保护。
type appDispatch struct {
	appID uuid.UUID
	// streams 按登记先后排列：选流用下标，顺序稳定才能让同一用户落在同一条流上。
	streams []*eventStream
	acked   []uuid.UUID
	wake    chan struct{}
	cancel  context.CancelFunc
	// owner 是租约值，按投递循环区分而不按实例：同一应用在本实例上先后起的两个循环若共用 owner，
	// 旧循环迟到的释放会删掉新循环的租约，别的实例随即抢到，同一应用就有两处在投。
	owner string
}

func (a *appDispatch) poke() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// leaseKey 是应用的投递租约键。
func leaseKey(appID uuid.UUID) string { return "fp:evt:lease:" + appID.String() }

// Register 登记一条流，返回它的事件 channel 与注销函数（必须调用）。投递器已关闭时返回的 channel 永不出数据。
func (d *EventDispatcher) Register(appID uuid.UUID) (*eventStream, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	s := &eventStream{id: d.nextID, out: make(chan *fpv1.AppEvent, d.cfg.InflightPerStream)}
	if d.closed {
		return s, func() {}
	}
	a := d.apps[appID]
	if a == nil {
		ctx, cancel := context.WithCancel(d.root)
		a = &appDispatch{appID: appID, wake: make(chan struct{}, 1), cancel: cancel, owner: uuid.NewString()}
		d.apps[appID] = a
		d.running.Add(1)
		go func() {
			defer d.running.Done()
			d.runApp(ctx, a)
		}()
	}
	a.streams = append(a.streams, s)
	a.poke()

	var once sync.Once
	return s, func() { once.Do(func() { d.unregister(a, s) }) }
}

func (d *EventDispatcher) unregister(a *appDispatch, s *eventStream) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, x := range a.streams {
		if x == s {
			a.streams = append(a.streams[:i:i], a.streams[i+1:]...)
			break
		}
	}
	if len(a.streams) == 0 && d.apps[a.appID] == a {
		// 本实例没有这个应用的流了：停掉投递循环、交出租约，别的实例才能接手。
		delete(d.apps, a.appID)
		a.cancel()
		return
	}
	a.poke()
}

// Ack 收下流上发来的确认，排队后立即返回；落库在 runAcks 里，落库后才通知投递循环。
// 队列满时丢弃并记日志：没落库的事件超时后重投，业务方本来就按 id 幂等。
func (d *EventDispatcher) Ack(_ context.Context, appID uuid.UUID, ids []string) {
	parsed := make([]uuid.UUID, 0, len(ids))
	for _, s := range ids {
		if id, err := uuid.Parse(s); err == nil {
			parsed = append(parsed, id)
		}
	}
	if len(parsed) == 0 {
		return
	}
	select {
	case d.acks <- ackReq{app: appID, ids: parsed}:
	default:
		slog.Warn("grpcapi: 确认队列已满，这批等超时重投", "appId", appID, "count", len(parsed))
	}
}

// runAcks 把排队的确认按应用合并后落库。关闭时把已排队的写完再退出：业务方确认完立刻断流、实例随即下线（发版）
// 是常态，丢掉这些确认的话接手的实例会再投一遍。
func (d *EventDispatcher) runAcks() {
	for {
		select {
		case r := <-d.acks:
			d.flushAcks(r)
		case <-d.root.Done():
			for {
				select {
				case r := <-d.acks:
					d.flushAcks(r)
				default:
					return
				}
			}
		}
	}
}

// flushAcks 以 first 开头，顺手取走队列里已有的确认（至多 ackBatchMax 条），每个应用一次落库。
func (d *EventDispatcher) flushAcks(first ackReq) {
	byApp := map[uuid.UUID][]uuid.UUID{first.app: first.ids}
	n := len(first.ids)
drain:
	for n < ackBatchMax {
		select {
		case r := <-d.acks:
			byApp[r.app] = append(byApp[r.app], r.ids...)
			n += len(r.ids)
		default:
			break drain
		}
	}
	for appID, ids := range byApp {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := d.src.Ack(ctx, appID, ids)
		cancel()
		if err != nil {
			slog.Error("grpcapi: 确认业务事件失败，等超时重投", "err", err, "appId", appID)
			continue
		}
		d.mu.Lock()
		if a := d.apps[appID]; a != nil {
			a.acked = append(a.acked, ids...)
			a.poke()
		}
		d.mu.Unlock()
	}
}

// Run 订阅唤醒信号直到 ctx 取消。订阅失败只记日志：投递循环有轮询兜底。
func (d *EventDispatcher) Run(ctx context.Context) {
	if d.waker == nil {
		return
	}
	ch, closeFn, err := d.waker.Subscribe(ctx)
	if err != nil {
		slog.Warn("grpcapi: 订阅业务事件唤醒失败，只靠轮询投递", "err", err)
		return
	}
	defer closeFn()
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-ch:
			if !ok {
				return
			}
			d.mu.Lock()
			if a := d.apps[id]; a != nil {
				a.poke()
			}
			d.mu.Unlock()
		}
	}
}

// Close 停掉全部投递循环并释放租约。
func (d *EventDispatcher) Close() {
	d.mu.Lock()
	d.closed = true
	d.apps = map[uuid.UUID]*appDispatch{}
	d.mu.Unlock()
	d.cancel()
	d.running.Wait()
}

// inflight 是一条已发出、等确认的事件。
type inflight struct {
	ev     domain.AppEvent
	stream uint64
	sentAt time.Time
}

// queued 是取到本地、还没发出的事件。stream 非零是上次发往的流：超时重投优先发回它。
type queued struct {
	ev     domain.AppEvent
	stream uint64
}

// deliveryState 是持有租约期间的投递进度。只在投递循环的 goroutine 里读写。
type deliveryState struct {
	cursor   int64
	queue    []queued
	inflight map[uuid.UUID]*inflight
}

func newDeliveryState() *deliveryState {
	return &deliveryState{inflight: map[uuid.UUID]*inflight{}}
}

func (d *EventDispatcher) runApp(ctx context.Context, a *appDispatch) {
	key := leaseKey(a.appID)
	held := false
	var st *deliveryState
	var renewedAt time.Time
	defer func() {
		if held {
			// ctx 已取消，释放要用新的：不释放的话别的实例要等满租约 TTL 才能接手。
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := d.lease.Release(rctx, key, a.owner); err != nil {
				slog.Warn("grpcapi: 释放投递租约失败，等它过期", "err", err, "appId", a.appID)
			}
			cancel()
		}
	}()

	tick := time.NewTicker(d.cfg.Poll)
	defer tick.Stop()
	for {
		now := time.Now()
		switch {
		case !held:
			ok, err := d.lease.Acquire(ctx, key, a.owner, d.cfg.LeaseTTL)
			if err != nil && ctx.Err() == nil {
				slog.Warn("grpcapi: 抢投递租约失败", "err", err, "appId", a.appID)
			}
			if ok {
				held, st, renewedAt = true, newDeliveryState(), now
			}
		case now.Sub(renewedAt) >= d.cfg.LeaseRenew:
			ok, err := d.lease.Renew(ctx, key, a.owner, d.cfg.LeaseTTL)
			switch {
			case err != nil && ctx.Err() != nil:
			case err != nil:
				// 续不上不等于丢了：租约还有余量，下一轮再续；真过期了会在下一次续期时发现。
				slog.Warn("grpcapi: 续投递租约失败", "err", err, "appId", a.appID)
			case !ok:
				// 已被别的实例接手（本实例卡顿超过 TTL）。已发未确认的交给新持有者重投。
				slog.Warn("grpcapi: 投递租约已被接手", "appId", a.appID)
				held, st = false, nil
			default:
				renewedAt = now
			}
		}
		if held {
			d.step(ctx, a, st, now)
		}

		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-tick.C:
		}
	}
}

// step 推进一轮：收确认、回收断流与超时的、补取、按用户选流发出。
func (d *EventDispatcher) step(ctx context.Context, a *appDispatch, st *deliveryState, now time.Time) {
	d.mu.Lock()
	streams := append([]*eventStream(nil), a.streams...)
	acked := a.acked
	a.acked = nil
	d.mu.Unlock()

	for _, id := range acked {
		delete(st.inflight, id)
		for i, q := range st.queue {
			if q.ev.ID == id {
				st.queue = append(st.queue[:i], st.queue[i+1:]...)
				break
			}
		}
	}

	live := map[uint64]*eventStream{}
	for _, s := range streams {
		live[s.id] = s
	}
	var back []queued
	perStream := map[uint64]int{}
	for id, f := range st.inflight {
		_, alive := live[f.stream]
		timedOut := now.Sub(f.sentAt) >= d.cfg.AckTimeout
		if !alive || timedOut {
			// 超时只说明业务方慢或回调失败，留在原流重投；原流断了 live 里没有它，选流时自然换掉。
			back = append(back, queued{ev: f.ev, stream: f.stream})
			delete(st.inflight, id)
			continue
		}
		perStream[f.stream]++
	}
	if len(back) > 0 {
		st.queue = mergeBySeq(back, st.queue)
	}

	if len(st.queue) < eventQueueMax {
		evs, err := d.src.Pending(ctx, a.appID, st.cursor, eventFetchBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("grpcapi: 读取未确认事件失败", "err", err, "appId", a.appID)
			}
		} else if len(evs) > 0 {
			for _, ev := range evs {
				st.queue = append(st.queue, queued{ev: ev})
			}
			st.cursor = evs[len(evs)-1].Seq
		}
	}

	if len(streams) == 0 {
		return
	}
	// 同一用户一次只有一条在途：前一条没确认（回调失败会超时重投）时后面的不发，否则业务方可能先处理了
	// 后一条（logged_out），再收到重投的前一条（login_succeeded）把状态改回去。排在前面的发不出去时同理。
	blocked := map[uuid.UUID]bool{}
	for _, f := range st.inflight {
		blocked[f.ev.UserID] = true
	}
	kept := st.queue[:0]
	for _, q := range st.queue {
		if blocked[q.ev.UserID] {
			kept = append(kept, q)
			continue
		}
		blocked[q.ev.UserID] = true
		s := live[q.stream]
		if s == nil {
			s = streams[pickStream(q.ev.UserID, len(streams))]
		}
		if perStream[s.id] >= d.cfg.InflightPerStream || !trySend(s, q.ev) {
			kept = append(kept, q)
			continue
		}
		perStream[s.id]++
		st.inflight[q.ev.ID] = &inflight{ev: q.ev, stream: s.id, sentAt: now}
	}
	st.queue = kept
}

// pickStream 按用户固定选流：流的集合不变时同一用户总落在同一条流上。
func pickStream(user uuid.UUID, n int) int {
	h := fnv.New32a()
	_, _ = h.Write(user[:])
	return int(h.Sum32() % uint32(n))
}

func trySend(s *eventStream, ev domain.AppEvent) bool {
	msg := &fpv1.AppEvent{
		Id: ev.ID.String(), Seq: ev.Seq, Kind: ev.Kind, Payload: ev.Payload,
		CreatedAtMs: ev.CreatedAt.UnixMilli(),
	}
	if ev.UserID != uuid.Nil {
		msg.UserId = ev.UserID.String()
	}
	select {
	case s.out <- msg:
		return true
	default:
		return false
	}
}

// mergeBySeq 把回收的事件按 seq 并回待发队列（两者各自有序）。
func mergeBySeq(back, queue []queued) []queued {
	slices.SortFunc(back, func(x, y queued) int { return cmp.Compare(x.ev.Seq, y.ev.Seq) })
	out := make([]queued, 0, len(back)+len(queue))
	i, j := 0, 0
	for i < len(back) && j < len(queue) {
		if back[i].ev.Seq <= queue[j].ev.Seq {
			out = append(out, back[i])
			i++
		} else {
			out = append(out, queue[j])
			j++
		}
	}
	out = append(out, back[i:]...)
	return append(out, queue[j:]...)
}
