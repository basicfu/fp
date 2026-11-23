package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/basicfu/fp/internal/cluster"
	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

var lcKeys = cluster.Keys{Prefix: "fp:core"}

// countingBus 数本实例发出的节点频道消息，用来断言本地有流时不碰 Redis。
type countingBus struct {
	*cluster.Bus
	published atomic.Int64
}

func (b *countingBus) Publish(ctx context.Context, node string, payload []byte) (int64, error) {
	b.published.Add(1)
	return b.Bus.Publish(ctx, node, payload)
}

// lcCluster 是共用一套 PG/Redis 的多个 fp 实例，各有自己的节点心跳、节点频道与转发器。
type lcCluster struct {
	rdb    *redis.Client
	app    *domain.Application
	secret string
	nodes  []*lcNode
	// 下面几个供账号接口的端到端测试在同一套服务上起 HTTP 接口。
	apps     *service.ApplicationService
	users    *service.UserService
	auth     *service.AuthService
	sessions *service.SessionService
}

type lcNode struct {
	name      string
	live      *cluster.Liveness
	bus       *countingBus
	relay     *LoginCheckRelay
	client    fpv1.AuthServiceClient
	stopRelay context.CancelFunc
}

// newLCCluster 起 n 个实例。heartbeat[i] 为真的实例跑心跳循环（写自己的登记）；
// 为假的不跑，它的存活快照只在测试显式刷新时变，用来模拟「登记还没跟上」。
func newLCCluster(t *testing.T, heartbeat ...bool) *lcCluster {
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
	app, secret, err := apps.Create(context.Background(), "登录校验", "lc-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	c := &lcCluster{rdb: rdb, app: app, secret: secret, apps: apps, users: users, auth: authSvc, sessions: sessions}

	runner, err := cluster.NewRunner(rdb, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, beat := range heartbeat {
		name := fmt.Sprintf("node-%c-%s", 'A'+i, uuid.NewString()[:6])
		live := cluster.NewLiveness(rdb, lcKeys, name, 100*time.Millisecond, 5*time.Second)
		bus := &countingBus{Bus: cluster.NewBus(rdb, runner, lcKeys)}
		relay := NewLoginCheckRelay(live, bus)
		srv := New(Deps{Auth: authSvc, Apps: apps, Pub: revokePub, Configs: configs, ConfigPub: configPub, LoginCheck: relay})

		runCtx, cancelRun := context.WithCancel(context.Background())
		relayCtx, stopRelay := context.WithCancel(runCtx)
		if beat {
			go live.Run(runCtx)
		}
		lis := bufconn.Listen(1 << 20)
		go func() { _ = srv.ServeWhenReady(relayCtx, lis, 5*time.Second) }()
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
		t.Cleanup(func() {
			_ = conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Shutdown(ctx)
			cancelRun()
		})
		c.nodes = append(c.nodes, &lcNode{
			name: name, live: live, bus: bus, relay: relay,
			client: fpv1.NewAuthServiceClient(conn), stopRelay: stopRelay,
		})
		waitFor(t, 2*time.Second, func() bool {
			n, _ := rdb.PubSubShardNumSub(context.Background(), lcKeys.NodeChannel(name)).Result()
			return n[lcKeys.NodeChannel(name)] == 1
		}, "节点频道没订阅上")
	}
	return c
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// answer 在 node 上开一条 Watch 流，hello 声明 caps，收到登录校验交给 fn 作答（fn 返回 nil 不答）。
// 返回关流函数。声明了 login_check 时等它在转发器里登记好才返回。
func (c *lcCluster) answer(t *testing.T, node int, caps []string, fn func(*fpv1.LoginCheck) *fpv1.LoginCheckReply) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(context.Background(),
		mdAppID, c.app.AppID, mdAppSecret, c.secret))
	stream, err := c.nodes[node].client.Watch(ctx)
	if err != nil {
		cancel()
		t.Fatalf("Watch: %v", err)
	}
	if caps != nil {
		if err := stream.Send(&fpv1.WatchRequest{Msg: &fpv1.WatchRequest_Hello{Hello: &fpv1.WatchHello{Caps: caps}}}); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if lc := msg.GetLoginCheck(); lc != nil {
				if r := fn(lc); r != nil {
					r.CallId = lc.GetCallId()
					_ = stream.Send(&fpv1.WatchRequest{Msg: &fpv1.WatchRequest_Reply{Reply: r}})
				}
			}
		}
	}()
	relay := c.nodes[node].relay
	if slices.Contains(caps, CapLoginCheck) {
		waitFor(t, 2*time.Second, func() bool { return relay.localStreams(c.app.ID) > 0 }, "流没有登记进转发器")
	}
	var once sync.Once
	closeFn := func() {
		once.Do(func() {
			cancel()
			waitFor(t, 2*time.Second, func() bool { return relay.localStreams(c.app.ID) == 0 }, "流断开后没有注销")
		})
	}
	t.Cleanup(func() { once.Do(cancel) })
	return closeFn
}

func allowAs(ref string) func(*fpv1.LoginCheck) *fpv1.LoginCheckReply {
	return func(*fpv1.LoginCheck) *fpv1.LoginCheckReply {
		return &fpv1.LoginCheckReply{Allow: true, ClientRef: ref}
	}
}

// warm 让 node 的转发器追踪本应用并刷新到能看到 want 这些持流实例。
func (c *lcCluster) warm(t *testing.T, node int, want ...string) {
	t.Helper()
	n := c.nodes[node]
	n.relay.ServerNodes(context.Background(), c.app.ID)
	waitFor(t, 3*time.Second, func() bool {
		_ = n.live.Refresh(context.Background())
		got := n.live.ServerNodes(c.app.ID.String())
		for _, w := range want {
			if !slices.Contains(got, w) {
				return false
			}
		}
		return true
	}, "登记里看不到持流实例")
}

// callIDRanking 找一个让 first 在候选里排第一的 call_id，好确定性地走「先试它」的路径。
func callIDRanking(nodes []string, first string) string {
	for i := 0; ; i++ {
		id := fmt.Sprintf("call-%d", i)
		if cluster.Rank(nodes, id)[0] == first {
			return id
		}
	}
}

func (c *lcCluster) check(node int, opts service.LoginCheckOptions) *service.LoginCheck {
	return service.NewLoginCheck(c.nodes[node].relay, opts)
}

func TestLoginCheckForwardedToInstanceWithStream(t *testing.T) {
	c := newLCCluster(t, false, true)
	var gotHeader, gotIP string
	c.answer(t, 1, []string{CapLoginCheck}, func(m *fpv1.LoginCheck) *fpv1.LoginCheckReply {
		gotHeader, gotIP = m.GetDeviceHeader(), m.GetIp()
		return &fpv1.LoginCheckReply{Allow: true, ClientRef: "dev-42"}
	})
	c.warm(t, 0, c.nodes[1].name)

	ref, err := c.check(0, service.LoginCheckOptions{}).Ask(context.Background(), c.app, `{"sign_pub":"x"}`, "1.2.3.4")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ref != "dev-42" || gotHeader != `{"sign_pub":"x"}` || gotIP != "1.2.3.4" {
		t.Fatalf("ref=%q header=%q ip=%q", ref, gotHeader, gotIP)
	}
}

func TestLoginCheckLocalStreamTouchesNoRedis(t *testing.T) {
	c := newLCCluster(t, false)
	c.answer(t, 0, []string{CapLoginCheck}, allowAs("local"))
	before := c.nodes[0].bus.published.Load()

	ref, err := c.check(0, service.LoginCheckOptions{}).Ask(context.Background(), c.app, "h", "ip")
	if err != nil || ref != "local" {
		t.Fatalf("Ask = %q, %v", ref, err)
	}
	if n := c.nodes[0].bus.published.Load() - before; n != 0 {
		t.Fatalf("本地有流却发了 %d 条节点频道消息", n)
	}
}

func TestLoginCheckStreamJustClosedRepliesNoStreamThenNext(t *testing.T) {
	c := newLCCluster(t, false, true, true)
	b, cc := c.nodes[1].name, c.nodes[2].name
	closeB := c.answer(t, 1, []string{CapLoginCheck}, allowAs("from-B"))
	c.answer(t, 2, []string{CapLoginCheck}, allowAs("from-C"))
	c.warm(t, 0, b, cc)

	// B 的流断了，但 A 的登记快照还没刷新：A 仍会先试 B。
	closeB()
	id := callIDRanking([]string{b, cc}, b)
	start := time.Now()
	ref, err := c.check(0, service.LoginCheckOptions{NewCallID: func() string { return id }}).
		Ask(context.Background(), c.app, "h", "ip")
	if err != nil || ref != "from-C" {
		t.Fatalf("Ask = %q, %v，期望 B 回没有流后换到 C", ref, err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("换到 C 用了 %v，应当立刻换", d)
	}
}

func TestLoginCheckDeadNodeSkipped(t *testing.T) {
	c := newLCCluster(t, false, true, true)
	b, cc := c.nodes[1].name, c.nodes[2].name
	c.answer(t, 1, []string{CapLoginCheck}, allowAs("from-B"))
	c.answer(t, 2, []string{CapLoginCheck}, allowAs("from-C"))
	c.warm(t, 0, b, cc)

	// B 不再订阅节点频道（崩溃），登记还在：发过去订阅数为 0。
	c.nodes[1].stopRelay()
	waitFor(t, 2*time.Second, func() bool {
		n, _ := c.rdb.PubSubShardNumSub(context.Background(), lcKeys.NodeChannel(b)).Result()
		return n[lcKeys.NodeChannel(b)] == 0
	}, "B 的节点频道订阅没断")

	id := callIDRanking([]string{b, cc}, b)
	start := time.Now()
	ref, err := c.check(0, service.LoginCheckOptions{NewCallID: func() string { return id }}).
		Ask(context.Background(), c.app, "h", "ip")
	if err != nil || ref != "from-C" {
		t.Fatalf("Ask = %q, %v", ref, err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("跳过死节点用了 %v", d)
	}
	if slices.Contains(c.nodes[0].live.ServerNodes(c.app.ID.String()), b) {
		t.Fatal("死节点没从本地视图剔除")
	}
}

func TestLoginCheckAppSilentIsUnavailable(t *testing.T) {
	c := newLCCluster(t, false, true)
	c.answer(t, 1, []string{CapLoginCheck}, func(*fpv1.LoginCheck) *fpv1.LoginCheckReply { return nil })
	c.warm(t, 0, c.nodes[1].name)

	start := time.Now()
	_, err := c.check(0, service.LoginCheckOptions{Timeout: 300 * time.Millisecond}).Ask(context.Background(), c.app, "h", "ip")
	assertCode(t, err, domain.CodeLoginCheckUnavailable)
	if d := time.Since(start); d < 250*time.Millisecond || d > 2*time.Second {
		t.Fatalf("超时用了 %v", d)
	}
}

func TestLoginCheckRejectionCodePassedThrough(t *testing.T) {
	c := newLCCluster(t, false, true)
	c.answer(t, 1, []string{CapLoginCheck}, func(*fpv1.LoginCheck) *fpv1.LoginCheckReply {
		return &fpv1.LoginCheckReply{Allow: false, Code: "version_too_low", Msg: "请升级"}
	})
	c.warm(t, 0, c.nodes[1].name)

	_, err := c.check(0, service.LoginCheckOptions{}).Ask(context.Background(), c.app, "h", "ip")
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "version_too_low" || de.Msg != "请升级" {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginCheckAllowWithoutClientRefIsUnavailable(t *testing.T) {
	c := newLCCluster(t, false)
	c.answer(t, 0, []string{CapLoginCheck}, allowAs(""))
	_, err := c.check(0, service.LoginCheckOptions{}).Ask(context.Background(), c.app, "h", "ip")
	assertCode(t, err, domain.CodeLoginCheckUnavailable)
}

func TestLoginCheckStreamWithoutCapNotRegistered(t *testing.T) {
	c := newLCCluster(t, false, true)
	// 老 SDK：发了 hello 但没声明 login_check（或干脆不发 hello）。
	c.answer(t, 1, []string{CapAppEvents}, allowAs("never"))
	c.answer(t, 1, nil, allowAs("never"))
	time.Sleep(300 * time.Millisecond)

	if n, _ := c.rdb.HLen(context.Background(), lcKeys.Srv(c.app.ID.String())).Result(); n != 0 {
		t.Fatalf("没声明 login_check 的流进了登记: %d", n)
	}
	start := time.Now()
	_, err := c.check(0, service.LoginCheckOptions{}).Ask(context.Background(), c.app, "h", "ip")
	assertCode(t, err, domain.CodeLoginCheckUnavailable)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("没有候选也等了 %v", d)
	}
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err = %v，期望码 %s", err, code)
	}
}

// localStreams 是测试辅助：本实例登记的该应用 login_check 流数。
func (r *LoginCheckRelay) localStreams(app uuid.UUID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.streams[app])
}

// servingNodes 记下 SetServing 的最终状态；hook 在 SetServing 里插一段动作，模拟调用方在这里被抢占。
type servingNodes struct {
	mu      sync.Mutex
	serving map[string]bool
	hook    func(on bool)
}

func (n *servingNodes) NodeID() string                { return "node-test" }
func (n *servingNodes) TrackApp(string)               {}
func (n *servingNodes) Refresh(context.Context) error { return nil }
func (n *servingNodes) ServerNodes(string) []string   { return nil }
func (n *servingNodes) DropLocal(string)              {}
func (n *servingNodes) state(app string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.serving[app]
}
func (n *servingNodes) SetServing(_ context.Context, app string, on bool) error {
	n.mu.Lock()
	hook := n.hook
	n.hook = nil
	n.mu.Unlock()
	if hook != nil {
		hook(on)
	}
	n.mu.Lock()
	n.serving[app] = on
	n.mu.Unlock()
	return nil
}

// 最后一条流注销、同时又来一条新流：注销那次 SetServing(false) 晚落地的话，登记被反转成「不服务」，
// 而本实例明明有流，别的实例再也不会把登录校验转过来。
func TestLoginCheckRelayServingNotInvertedByLateUnregister(t *testing.T) {
	nodes := &servingNodes{serving: map[string]bool{}}
	r := NewLoginCheckRelay(nodes, nil)
	app := uuid.New()
	old := r.Register(app)

	registered := make(chan struct{})
	nodes.hook = func(on bool) {
		if on {
			return
		}
		go func() {
			r.Register(app)
			close(registered)
		}()
		select {
		case <-registered:
		case <-time.After(200 * time.Millisecond):
		}
	}
	r.Unregister(old)
	<-registered
	if !nodes.state(app.String()) {
		t.Fatal("还有流，登记却是不服务")
	}
}
