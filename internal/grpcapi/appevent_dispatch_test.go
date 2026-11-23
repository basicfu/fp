package grpcapi

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// eventsOnApps 让事件开关对测试应用打开：账号配置的落库不归本任务。
type eventsOnApps struct{ *service.ApplicationService }

func (a eventsOnApps) GetByIDCached(ctx context.Context, id uuid.UUID) (*domain.Application, error) {
	app, err := a.ApplicationService.GetByIDCached(ctx, id)
	if app != nil {
		app.Account.Events = true
	}
	return app, err
}

func (a eventsOnApps) List(ctx context.Context) ([]domain.Application, error) {
	apps, err := a.ApplicationService.List(ctx)
	for i := range apps {
		apps[i].Account.Events = true
	}
	return apps, err
}

// eventCluster 是共用一套 PG/Redis 的多个 fp 实例，各有自己的投递器，模拟多实例部署。
type eventCluster struct {
	apps   *service.ApplicationService
	events *service.AppEvents
	appID  uuid.UUID
	extID  string
	secret string
	nodes  []*eventNode
}

type eventNode struct {
	client     fpv1.AuthServiceClient
	dispatcher *EventDispatcher
	shutdown   func()
}

// AckTimeout 留足余量：机器高负载时 600ms 内的确认会被误判超时而重投，
// 破坏「每条事件只投一条流」的断言。
var testDispatch = DispatchConfig{
	LeaseTTL: 3 * time.Second, LeaseRenew: time.Second, AckTimeout: 3 * time.Second, Poll: 50 * time.Millisecond,
}

func newEventCluster(t *testing.T, n int) *eventCluster {
	t.Helper()
	return newEventClusterWith(t, n, true)
}

// newEventClusterWith 的 forceOn 为 false 时事件开关走真实的应用配置(只有流声明了 app_events 才打开)。
func newEventClusterWith(t *testing.T, n int, forceOn bool) *eventCluster {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	users := service.NewUserService(pool)
	revokePub := store.NewRevokePublisher(rdb)
	sessions := service.NewSessionService(store.NewSessionStore(rdb), revokePub, store.NewEpochStore(rdb))
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(users)); err != nil {
		t.Fatal(err)
	}
	configPub := store.NewConfigPublisher(rdb)
	apps := service.NewApplicationService(pool, reg, service.WithIMConfigPublisher(configPub))
	configs := service.NewConfigService(pool, configPub)
	authSvc := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users, Sessions: sessions, Logs: service.NewLoginLogService(pool), Registry: reg,
	})
	signal := store.NewAppEventSignal(rdb)
	var src service.AppEventApps = apps
	if forceOn {
		src = eventsOnApps{apps}
	}
	events := service.NewAppEvents(pool, src, signal)

	app, secret, err := apps.Create(context.Background(), "事件投递", "evt-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	c := &eventCluster{apps: apps, events: events, appID: app.ID, extID: app.AppID, secret: secret}
	for i := 0; i < n; i++ {
		d := NewEventDispatcher(events, store.NewLease(rdb), signal, testDispatch)
		srv := New(Deps{Auth: authSvc, Apps: apps, Pub: revokePub, Configs: configs, ConfigPub: configPub, Events: d})
		runCtx, cancelRun := context.WithCancel(context.Background())
		lis := bufconn.Listen(1 << 20)
		go func() { _ = srv.ServeWhenReady(runCtx, lis, 5*time.Second) }()
		select {
		case <-srv.Ready():
		case <-time.After(5 * time.Second):
			t.Fatal("等待 gRPC 服务就绪超时")
		}
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		node := &eventNode{client: fpv1.NewAuthServiceClient(conn), dispatcher: d, shutdown: func() {
			once.Do(func() {
				_ = conn.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				srv.Shutdown(ctx)
				cancelRun()
			})
		}}
		t.Cleanup(node.shutdown)
		c.nodes = append(c.nodes, node)
	}
	return c
}

// watcher 是一条声明了 app_events 的测试流。
type watcher struct {
	t      *testing.T
	name   string
	stream grpc.BidiStreamingClient[fpv1.WatchRequest, fpv1.WatchResponse]
	cancel context.CancelFunc
	got    chan *fpv1.AppEvent
	// autoAck 为 true 时收到就确认。
	autoAck bool
	sendMu  sync.Mutex
}

func (c *eventCluster) watch(t *testing.T, node int, name string, autoAck bool) *watcher {
	t.Helper()
	ctx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(context.Background(),
		mdAppID, c.extID, mdAppSecret, c.secret))
	stream, err := c.nodes[node].client.Watch(ctx)
	if err != nil {
		cancel()
		t.Fatalf("Watch: %v", err)
	}
	w := &watcher{t: t, name: name, stream: stream, cancel: cancel, got: make(chan *fpv1.AppEvent, 256), autoAck: autoAck}
	t.Cleanup(cancel)
	if err := stream.Send(&fpv1.WatchRequest{Msg: &fpv1.WatchRequest_Hello{Hello: &fpv1.WatchHello{Caps: []string{CapAppEvents}}}}); err != nil {
		t.Fatalf("发 hello: %v", err)
	}
	ready := make(chan struct{})
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.GetReady() != nil {
				close(ready)
			}
			if ev := msg.GetAppEvent(); ev != nil {
				if w.autoAck {
					w.ack(ev.GetId())
				}
				w.got <- ev
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("流没有就绪")
	}
	return w
}

func (w *watcher) ack(ids ...string) {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	_ = w.stream.Send(&fpv1.WatchRequest{Msg: &fpv1.WatchRequest_Ack{Ack: &fpv1.EventAck{Ids: ids}}})
}

// next 等一条事件，超时失败。
func (w *watcher) next(timeout time.Duration) *fpv1.AppEvent {
	w.t.Helper()
	select {
	case ev := <-w.got:
		return ev
	case <-time.After(timeout):
		w.t.Fatalf("%s 在 %v 内没收到事件", w.name, timeout)
		return nil
	}
}

// none 断言 d 内收不到事件。
func (w *watcher) none(d time.Duration) {
	w.t.Helper()
	select {
	case ev := <-w.got:
		w.t.Fatalf("%s 收到了不该收到的事件 seq=%d", w.name, ev.GetSeq())
	case <-time.After(d):
	}
}

func (c *eventCluster) append(t *testing.T, user uuid.UUID, kind string) {
	t.Helper()
	if err := c.events.Append(context.Background(), c.appID, user, kind, map[string]string{"k": kind}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// 应用第一次有流声明 app_events 时自动开始收事件;从没声明过的应用不写事件表,免得堆积。
func TestAppEventsAutoEnabledByHello(t *testing.T) {
	c := newEventClusterWith(t, 1, false)
	ctx := context.Background()
	pending := func() int {
		evs, err := c.events.Pending(ctx, c.appID, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		return len(evs)
	}
	c.append(t, uuid.New(), domain.EventLoginSucceeded)
	if n := pending(); n != 0 {
		t.Fatalf("没有流声明过 app_events 却写了 %d 条", n)
	}

	// 只声明登录校验的流不算。
	sctx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(ctx, mdAppID, c.extID, mdAppSecret, c.secret))
	defer cancel()
	other, err := c.nodes[0].client.Watch(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Send(&fpv1.WatchRequest{Msg: &fpv1.WatchRequest_Hello{Hello: &fpv1.WatchHello{Caps: []string{CapLoginCheck}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Recv(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if app, _ := c.apps.GetByID(ctx, c.appID); app.Account.Events {
		t.Fatal("没声明 app_events 的流打开了事件")
	}

	w := c.watch(t, 0, "A", true)
	deadline := time.Now().Add(3 * time.Second)
	for {
		app, err := c.apps.GetByID(ctx, c.appID)
		if err != nil {
			t.Fatal(err)
		}
		if app.Account.Events {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("声明 app_events 后没有自动打开事件")
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.append(t, uuid.New(), domain.EventLoggedOut)
	if ev := w.next(3 * time.Second); ev.GetKind() != domain.EventLoggedOut {
		t.Fatalf("收到的不是打开后写的事件: %+v", ev)
	}
}

func TestAppEventEachEventGoesToOneStream(t *testing.T) {
	c := newEventCluster(t, 1)
	a := c.watch(t, 0, "A", true)
	b := c.watch(t, 0, "B", true)

	const n = 30
	for i := 0; i < n; i++ {
		c.append(t, uuid.New(), domain.EventLoginSucceeded)
	}
	seen := map[string]int{}
	deadline := time.After(5 * time.Second)
	for len(seen) < n {
		select {
		case ev := <-a.got:
			seen[ev.GetId()]++
		case ev := <-b.got:
			seen[ev.GetId()]++
		case <-deadline:
			t.Fatalf("5 秒内只收到 %d/%d 条", len(seen), n)
		}
	}
	a.none(300 * time.Millisecond)
	b.none(10 * time.Millisecond)
	for id, k := range seen {
		if k != 1 {
			t.Fatalf("事件 %s 收到 %d 次", id, k)
		}
	}
}

func TestAppEventSameUserSameStreamInOrder(t *testing.T) {
	c := newEventCluster(t, 1)
	a := c.watch(t, 0, "A", true)
	b := c.watch(t, 0, "B", true)

	user := uuid.New()
	for i := 0; i < 6; i++ {
		kind := domain.EventLoginSucceeded
		if i%2 == 1 {
			kind = domain.EventLoggedOut
		}
		c.append(t, user, kind)
	}
	var got []*fpv1.AppEvent
	var from string
	deadline := time.After(5 * time.Second)
	for len(got) < 6 {
		select {
		case ev := <-a.got:
			got, from = append(got, ev), from+"A"
		case ev := <-b.got:
			got, from = append(got, ev), from+"B"
		case <-deadline:
			t.Fatalf("只收到 %d 条", len(got))
		}
	}
	if from != "AAAAAA" && from != "BBBBBB" {
		t.Fatalf("同一用户的事件分到了两条流: %s", from)
	}
	for i := 1; i < len(got); i++ {
		if got[i].GetSeq() <= got[i-1].GetSeq() {
			t.Fatalf("乱序: %d 在 %d 之后", got[i].GetSeq(), got[i-1].GetSeq())
		}
	}
	if got[0].GetUserId() != user.String() || got[1].GetKind() != domain.EventLoggedOut {
		t.Fatalf("事件内容不对: %+v", got[:2])
	}
}

// 超时没确认：优先重投回原流（业务方多半只是慢了），原流断了才换到另一条。
func TestAppEventUnackedIsRedeliveredOnSameStreamUntilItDies(t *testing.T) {
	c := newEventCluster(t, 1)
	a := c.watch(t, 0, "A", false)
	b := c.watch(t, 0, "B", false)

	c.append(t, uuid.New(), domain.EventLoggedOut)
	var first *fpv1.AppEvent
	var firstOn *watcher
	select {
	case first = <-a.got:
		firstOn = a
	case first = <-b.got:
		firstOn = b
	case <-time.After(3 * time.Second):
		t.Fatal("没收到事件")
	}
	other := a
	if firstOn == a {
		other = b
	}
	again := firstOn.next(2 * testDispatch.AckTimeout)
	if again.GetId() != first.GetId() {
		t.Fatalf("重投的不是同一条事件")
	}
	other.none(10 * time.Millisecond)

	// 原流断了：换到另一条。
	firstOn.cancel()
	moved := other.next(2 * testDispatch.AckTimeout)
	if moved.GetId() != first.GetId() {
		t.Fatalf("换流重投的不是同一条事件")
	}
	other.ack(moved.GetId())
	other.none(2 * testDispatch.AckTimeout)
}

// 同一用户 E1 回调失败（不确认）时 E2 不能先发：业务方若先处理了 E2（如 logged_out），
// 之后重投的 E1（login_succeeded）会把状态改回在线。
func TestAppEventSameUserWaitsForEarlierAck(t *testing.T) {
	c := newEventCluster(t, 1)
	a := c.watch(t, 0, "A", false)

	user := uuid.New()
	c.append(t, user, domain.EventLoginSucceeded)
	c.append(t, user, domain.EventLoggedOut)
	e1 := a.next(3 * time.Second)
	if e1.GetKind() != domain.EventLoginSucceeded {
		t.Fatalf("第一条是 %s", e1.GetKind())
	}
	deadline := time.Now().Add(2 * testDispatch.AckTimeout)
	for time.Now().Before(deadline) {
		select {
		case ev := <-a.got:
			if ev.GetId() != e1.GetId() {
				t.Fatalf("E1 未确认时收到了 %s(seq=%d)", ev.GetKind(), ev.GetSeq())
			}
		case <-time.After(time.Until(deadline)):
		}
	}
	a.ack(e1.GetId())
	for {
		ev := a.next(3 * time.Second)
		if ev.GetId() == e1.GetId() {
			continue // 确认前已在路上的重投
		}
		if ev.GetKind() != domain.EventLoggedOut {
			t.Fatalf("E1 确认后收到 %s", ev.GetKind())
		}
		break
	}
}

// 租约 owner 按投递循环区分：同一实例上前后两个投递循环共用 owner 的话，旧循环迟到的释放
// 会删掉新循环的租约，别的实例随即也能抢到，同一应用就有两处在投。
func TestAppEventLeaseOwnerPerDispatchLoop(t *testing.T) {
	lease := &recordingLease{}
	d := NewEventDispatcher(nopEventSource{}, lease, nil, testDispatch)
	defer d.Close()
	app := uuid.New()
	_, unregister := d.Register(app)
	first := lease.waitOwners(t, 1)
	unregister()
	_, unregister2 := d.Register(app)
	defer unregister2()
	owners := lease.waitOwners(t, 2)
	if owners[0] == owners[1] || owners[0] != first[0] {
		t.Fatalf("两个投递循环的 owner = %v", owners)
	}
}

type nopEventSource struct{}

func (nopEventSource) Pending(context.Context, uuid.UUID, int64, int) ([]domain.AppEvent, error) {
	return nil, nil
}
func (nopEventSource) Ack(context.Context, uuid.UUID, []uuid.UUID) error { return nil }

// recordingLease 总是让抢租约成功，记下每个不同的 owner。
type recordingLease struct {
	mu     sync.Mutex
	owners []string
}

func (l *recordingLease) Acquire(_ context.Context, _, owner string, _ time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.owners) == 0 || l.owners[len(l.owners)-1] != owner {
		l.owners = append(l.owners, owner)
	}
	return true, nil
}
func (l *recordingLease) Renew(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (l *recordingLease) Release(context.Context, string, string) error { return nil }

func (l *recordingLease) waitOwners(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		got := append([]string(nil), l.owners...)
		l.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("没等到 %d 次抢租约", n)
	return nil
}

func TestAppEventAckedIsNotRedelivered(t *testing.T) {
	c := newEventCluster(t, 1)
	a := c.watch(t, 0, "A", true)

	c.append(t, uuid.New(), domain.EventLoginSucceeded)
	a.next(3 * time.Second)
	a.none(2 * testDispatch.AckTimeout)

	pending, err := c.events.Pending(context.Background(), c.appID, 0, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("确认之后事件表里还有 %d 条未确认, err=%v", len(pending), err)
	}
}

func TestAppEventReconnectGetsUnacked(t *testing.T) {
	c := newEventCluster(t, 1)
	a := c.watch(t, 0, "A", false)

	user := uuid.New()
	c.append(t, user, domain.EventLoginSucceeded)
	c.append(t, user, domain.EventLoggedOut)
	e1 := a.next(3 * time.Second)
	a.ack(e1.GetId())
	a.next(3 * time.Second)
	a.cancel()

	b := c.watch(t, 0, "B", true)
	got := b.next(3 * time.Second)
	if got.GetKind() != domain.EventLoggedOut {
		t.Fatalf("重连后补发的是 %s，期望未确认的 logged_out", got.GetKind())
	}
	b.none(300 * time.Millisecond)
}

func TestAppEventOldWatcherWithoutHelloGetsNothing(t *testing.T) {
	c := newEventCluster(t, 1)
	ctx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(context.Background(),
		mdAppID, c.extID, mdAppSecret, c.secret))
	defer cancel()
	stream, err := c.nodes[0].client.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if msg, err := stream.Recv(); err != nil || msg.GetReady() == nil {
		t.Fatalf("第一条不是 ready: %v %v", msg, err)
	}
	c.append(t, uuid.New(), domain.EventLoginSucceeded)
	got := make(chan *fpv1.WatchResponse, 1)
	go func() {
		if msg, err := stream.Recv(); err == nil {
			got <- msg
		}
	}()
	select {
	case msg := <-got:
		t.Fatalf("没发 hello 的流收到了: %v", msg)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestAppEventLeaseFailoverToAnotherInstance(t *testing.T) {
	c := newEventCluster(t, 2)
	a := c.watch(t, 0, "A@node0", false)
	// 先让 node0 拿到租约，再把 node1 的流连上。
	c.append(t, uuid.New(), domain.EventLoginSucceeded)
	e1 := a.next(3 * time.Second)
	b := c.watch(t, 1, "B@node1", true)

	// node0 持有租约时 node1 的流什么都收不到，哪怕事件一直没确认。
	b.none(2 * testDispatch.AckTimeout)

	user := uuid.New()
	c.append(t, user, domain.EventLoggedOut)
	a.next(3 * time.Second)

	// node0 上的流全断：它交出租约，node1 接手并补发两条未确认的。
	a.cancel()
	got := map[string]bool{}
	for len(got) < 2 {
		// 比租约 TTL 短：证明是主动交出而不是等过期。
		got[b.next(1500*time.Millisecond).GetId()] = true
	}
	if !got[e1.GetId()] {
		t.Fatal("接手的实例没有补发 node0 发过但未确认的事件")
	}
	pending, err := c.events.Pending(context.Background(), c.appID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(pending) > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		pending, _ = c.events.Pending(context.Background(), c.appID, 0, 10)
	}
	if len(pending) != 0 {
		t.Fatalf("接手后确认没落库，还剩 %d 条", len(pending))
	}
}

func TestAppEventInstanceShutdownHandsOver(t *testing.T) {
	c := newEventCluster(t, 2)
	a := c.watch(t, 0, "A@node0", false)
	c.append(t, uuid.New(), domain.EventLoginSucceeded)
	e1 := a.next(3 * time.Second)
	b := c.watch(t, 1, "B@node1", true)

	// 整个实例关闭（发版）：不等租约过期就该交接。
	c.nodes[0].shutdown()
	if got := b.next(1500 * time.Millisecond); got.GetId() != e1.GetId() {
		t.Fatalf("接手后收到的不是未确认的那条")
	}
}

// blockingAckSource 的 Ack 卡住直到 release 关闭，模拟 PG 慢。
type blockingAckSource struct {
	nopEventSource
	release chan struct{}
	mu      sync.Mutex
	acked   []uuid.UUID
}

func (s *blockingAckSource) Ack(_ context.Context, _ uuid.UUID, ids []uuid.UUID) error {
	<-s.release
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked = append(s.acked, ids...)
	return nil
}

// 确认在收消息的协程里同步写 PG 的话，PG 一慢，同一条流上登录校验的应答也跟着排队，登录跟着超时。
func TestAppEventAckDoesNotBlockReceiver(t *testing.T) {
	src := &blockingAckSource{release: make(chan struct{})}
	d := NewEventDispatcher(src, &recordingLease{}, nil, testDispatch)
	app := uuid.New()
	ids := []string{uuid.NewString(), uuid.NewString()}
	done := make(chan struct{})
	go func() {
		d.Ack(context.Background(), app, ids[:1])
		d.Ack(context.Background(), app, ids[1:])
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		close(src.release)
		t.Fatal("Ack 阻塞在写库上")
	}
	close(src.release)
	// 关闭时把排队的确认写完：业务方确认完立刻断流、实例随即下线是常态。
	d.Close()
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.acked) != 2 {
		t.Fatalf("落库的确认 = %v", src.acked)
	}
}
