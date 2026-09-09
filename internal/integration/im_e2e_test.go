package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/imgrpc"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/basicfu/fp/internal/im/registry"
	"github.com/basicfu/fp/internal/im/wsapi"
	"github.com/basicfu/fp/internal/testsupport"
	fpim "github.com/basicfu/fp/sdk/im"
)

// staticAuth 是测试用的假 Authenticator：不起真实的身份平台（fp），
// token 直接查表映射到 Subject。task 17 的重点是验证 fp-im 内部（跨节点
// 转发、注册表、bus）这条链路真跑起来，不是再测一遍身份校验——那是
// fpauth 包自己的单元测试的职责。
type staticAuth map[string]model.Subject

func (a staticAuth) Verify(_ context.Context, req auth.VerifyRequest) (model.Subject, error) {
	if s, ok := a[req.Token]; ok {
		return s, nil
	}
	return model.Subject{}, auth.ErrUnauthorized
}

// imNode 是测试里起的一个完整 im 节点的句柄：client 连 wsURL，业务 server
// 连 grpcAddr，live 暴露给测试轮询"这个节点的存活视图有没有看到另一个节点"。
type imNode struct {
	id       string
	live     *registry.Liveness
	wsURL    string
	grpcAddr string
}

// startNode 起一个完整的 im 节点：registry、bus、hub、wsapi、imgrpc 全部
// 用真实 Redis 接线，装配顺序照抄 cmd/fp-im/main.go 的 run()——先订阅节点
// 频道、再写首个心跳、再同步 Refresh 一次存活视图，最后才起监听。顺序错了
// （比如先起监听再订阅）会在握手脚本清理"存活列表之外的残留连接"那一步上
// 出现难以复现的偶发失败：见 cmd/fp-im/main.go run() 里"债务 1"那段注释。
//
// 心跳周期给 200ms（而不是生产的秒级）：验收项⑥（顶号）依赖节点 A 的
// live 视图先看到节点 B 持有 server 流，这段传播延迟就是心跳周期本身，
// 测试用轮询等待这件事发生，但心跳周期太长会把测试拖得没必要地慢。
// imCreds 让业务 server 的凭据校验在 e2e 里不必回源到 fp。
type imCreds map[string]string

func (c imCreds) VerifyAppCredential(_ context.Context, app, secret string) error {
	if c[app] != secret {
		return errors.New("凭据无效")
	}
	return nil
}

func startNode(t *testing.T, rdb *redis.Client, id string, apps auth.AppConfigSource, authn auth.Authenticator) *imNode {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	run, err := redisx.NewRunner(rdb, 0, 1)
	if err != nil {
		t.Fatalf("redisx.NewRunner: %v", err)
	}
	t.Cleanup(run.Close)

	live := registry.NewLiveness(rdb, id, 200*time.Millisecond, time.Second)
	conns := registry.NewConns(rdb, run, 30*time.Minute)
	b := bus.New(rdb, run)
	h := hub.New(id, conns, live, b, apps)

	// 先订阅节点频道、起分发协程，确保别的节点后面往我的频道发信封时
	// 我已经在监听——与 cmd/fp-im/main.go 的 runBusLoop 同一个理由。
	ch, closeFn, err := b.Subscribe(ctx, id)
	if err != nil {
		t.Fatalf("bus.Subscribe: %v", err)
	}
	t.Cleanup(closeFn)
	go func() {
		for sig := range ch {
			if sig.Kind == bus.SignalEnvelope {
				h.HandleEnvelope(ctx, sig.Env)
			}
		}
	}()

	// 为 apps 里声明的每个 app 打开 srv 表追踪——这一行现在是**照抄生产
	// 装配**（cmd/fp-im/main.go 的 trackApps，位置同样在下面那次同步
	// Refresh 之前），不再是绕开缺陷的补丁。
	//
	// 它此前是个补丁：生产代码那时根本没有这一步，追踪只在 hub.Deliver
	// 第一次需要转发时惰性发生，于是每个 app 的第一条上行/事件必定因为
	// 候选列表为空被静默丢弃；这里提前调一次，把那个缺陷从测试视野里
	// 抹平了，还把它注释成"生产里良性"——它不良性，丢的是每个应用在每个
	// 新节点上的第一条上线通知，永不重发。缺陷已在 cmd/fp-im 修掉（甲一），
	// 并由 cmd/fp-im 的 TestServeDeliversFirstConnectEventAfterStartup
	// 直接守着（那条测试跑的是 serve() 本身，不是手抄的装配）。
	for _, appID := range apps.Apps() {
		live.TrackApp(appID)
	}

	// 再写首个心跳、同步 Refresh 一次：HTTP/gRPC 开始服务之前，本节点看到
	// 的存活视图必须是 Redis 里当下真实的全量存活节点，否则握手脚本会把
	// "存活列表之外的节点"上同 subject 的连接当残留清掉。live.Run 内部
	// 首轮虽然也会做一次 Beat+Refresh，但那是异步的，这里必须先同步跑一次
	// ——原样照抄 cmd/fp-im/main.go run() 里"债务 1"那段注释的结论。
	if err := live.Beat(ctx); err != nil {
		t.Fatalf("live.Beat: %v", err)
	}
	if err := live.Refresh(ctx); err != nil {
		t.Fatalf("live.Refresh: %v", err)
	}
	go live.Run(ctx)

	wsLis := listenAddr(t, "127.0.0.1:0")
	httpSrv := &http.Server{Handler: wsapi.New(wsapi.Deps{
		Hub: h, Conns: conns, Live: live, Auth: authn, Apps: apps, Guests: wsapi.NewGuestLimiter(),
		Cfg: wsapi.Config{AuthTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second, SendQueue: 64},
	})}
	go httpSrv.Serve(wsLis)
	t.Cleanup(func() { httpSrv.Close() })

	grpcLis := listenAddr(t, "127.0.0.1:0")
	g := imgrpc.New(imgrpc.Deps{Hub: h, Creds: imCreds{"a1": "s1"}})
	go g.Serve(grpcLis)
	t.Cleanup(func() { g.Stop(context.Background()) })

	return &imNode{
		id:       id,
		live:     live,
		wsURL:    "ws://" + wsLis.Addr().String() + "/",
		grpcAddr: grpcLis.Addr().String(),
	}
}

// imApps 是 auth.AppConfigSource 的静态实现，只声明一个应用 a1，策略可配置
// （"replace" 用于顶号场景⑥）。
//
// app 配置现在来自 fp。这些 e2e 场景验的是 ws 协议、连接注册、跨节点转发
// 这些**与 fp 无关**的行为，全部走访客身份握手（访客路径不经过 fp），所以
// 用静态实现而不是连带起一整套 fp。
type imApps map[string]model.AppConfig

func (a imApps) Load(context.Context, string) error     { return nil }
func (a imApps) Get(app string) (model.AppConfig, bool) { c, ok := a[app]; return c, ok }
func (a imApps) Apps() []string {
	out := make([]string, 0, len(a))
	for k := range a {
		out = append(out, k)
	}
	return out
}

func writeApps(t *testing.T, policy string) imApps {
	t.Helper()
	return imApps{"a1": {
		AppID: "a1", ConnPolicy: model.Policy(policy), ConnLimit: 3,
		AllowGuest: true, GuestIPRate: 20,
	}}
}

// uniqueNodeID 保证同一次测试进程内多次调用生成的 nodeId 互不冲突——同一个
// Redis 被本包所有测试共享，同一次 TestImTwoNodesEndToEnd 内部起的两个
// 节点必须彼此不同，单靠时间戳在同一毫秒内起两个节点会撞车，必须叠加一个
// 单调递增的计数器。
var nodeIDCounter atomic.Int64

func uniqueNodeID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), nodeIDCounter.Add(1))
}

// TestImTwoNodesEndToEnd 是本任务的核心：同一进程内起两个真实 im 节点
// （不同 nodeId、各自独立的 ws/gRPC 监听），共用同一个真实 Redis，一个
// fpim.Server 连节点 B，一个 fpim.Client 连节点 A，走通 spec 第二、五、
// 六节的主路径：连接事件跨节点到达、上行消息跨节点转发、推送跨节点到达、
// 离线返回 NotOnline、查会话、顶号、踢人。
//
// 这条测试单独跑得出的价值：这里每一步都是真实网络往返 + 真实 Redis
// pub/sub + 真实握手脚本，任何一个包各自的单元测试都只测了"自己这一层
// 内部逻辑自洽"，测不出"两个节点接起来之后，转发路径真的能把消息从 A
// 送到 B、再从 B 送回 A"这件事。
func TestImTwoNodesEndToEnd(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	apps := writeApps(t, "replace")
	authn := staticAuth{"tok-1": model.User("1"), "tok-2": model.User("2")}

	a := startNode(t, rdb, uniqueNodeID("im-a"), apps, authn)
	b := startNode(t, rdb, uniqueNodeID("im-b"), apps, authn)

	// 业务 server 连节点 B。
	srv, err := fpim.NewServer(fpim.ServerConfig{Addr: b.grpcAddr, AppID: "a1", AppSecret: "s1", Insecure: true})
	if err != nil {
		t.Fatalf("fpim.NewServer: %v", err)
	}
	defer srv.Close()

	var mu sync.Mutex
	var inbound []fpim.Inbound
	var events []fpim.Event
	srv.OnMessage(func(_ context.Context, in fpim.Inbound) error {
		mu.Lock()
		inbound = append(inbound, in)
		mu.Unlock()
		return nil
	})
	srv.OnEvent(func(_ context.Context, ev fpim.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	waitUntil(t, srv.StreamHealthy, "server 到节点 B 的接入流应就绪")

	// 节点 A 的存活视图有传播延迟：B 注册"持有 a1 的 server 流"之后，A 要
	// 等到下一次 Refresh（心跳周期 200ms）才知道。必须轮询等到这件事真的
	// 发生，再让 client 连 A、发消息——否则 A 转发消息时 ServerNodes("a1")
	// 拿到的候选列表是空的，消息会被静默丢弃，测试会在一个和"跨节点转发
	// 坏了"完全不同的原因上失败（"A 还没来得及知道 B"），把两种失败原因
	// 混在一起，排查故障会被引向错误的方向。
	//
	// 断言不是"非空"而是"恰好等于 [B]"：这条测试要成立的前提是节点 A
	// 全程没有任何业务服务流，"A 看到的候选列表里唯一的节点就是 B"这件事
	// 应该被显式钉在代码里，而不是靠读者自己推导——如果哪天这条测试被改坏、
	// A 自己也意外注册了一条 server 流，len(nodes)>0 不会报警，但
	// nodes[0]==b.id 会。
	waitUntil(t, func() bool {
		nodes := a.live.ServerNodes("a1")
		return len(nodes) == 1 && nodes[0] == b.id
	}, "节点 A 应看到节点 B 是 a1 唯一持有 server 流的节点")

	// client 连节点 A。
	cli, err := fpim.Dial(context.Background(), fpim.ClientConfig{URL: a.wsURL, App: "a1", Token: "tok-1", OS: "linux"})
	if err != nil {
		t.Fatalf("fpim.Dial: %v", err)
	}
	defer cli.Close()
	got := make(chan []byte, 8)
	cli.OnMessage(func(p []byte) { got <- p })

	// ① 连接事件跨节点到达：client 连的是 A，业务 server 连的是 B，
	// Connected 事件必须经 A→Redis→B 才能到达 server。
	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) == 1 && events[0].Kind == fpim.EventConnected
	}, "server 应收到 Connected 事件")
	mu.Lock()
	connID := events[0].ConnID
	mu.Unlock()

	// ② client 发消息跨节点到达业务服务：节点 A 没有 a1 的 server 流，
	// 消息必须转发一跳到节点 B。
	if err := cli.Send(context.Background(), []byte(`{"q":1}`)); err != nil {
		t.Fatalf("cli.Send: %v", err)
	}
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(inbound) == 1 }, "server 应收到 client 发上来的消息")
	mu.Lock()
	got0 := inbound[0]
	mu.Unlock()
	if got0.Subject != fpim.User("1") || got0.ConnID != connID || string(got0.Payload) != `{"q":1}` {
		t.Fatalf("server 收到的 Inbound 不对：%+v", got0)
	}

	// ③ 业务服务推送跨节点到达 client：推送从节点 B 发出，client 在节点 A。
	res, err := srv.Push(context.Background(), fpim.User("1"), []byte(`{"r":2}`))
	if err != nil {
		t.Fatalf("srv.Push: %v", err)
	}
	if res.Status != fpim.Sent || res.Nodes != 1 {
		t.Fatalf("Push 应返回 Sent{Nodes:1}，实际 %+v", res)
	}
	select {
	case p := <-got:
		if string(p) != `{"r":2}` {
			t.Fatalf("client 收到的推送内容不对：%s", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client 5 秒内没有收到推送")
	}

	// ④ 推送给不在线的主体应返回 NotOnline。
	if res, err := srv.Push(context.Background(), fpim.User("2"), []byte(`1`)); err != nil || res.Status != fpim.NotOnline {
		t.Fatalf("推送给离线 subject 应返回 NotOnline，实际 %+v，err=%v", res, err)
	}

	// ⑤ 查会话应返回正确的节点与系统标识。
	sess, err := srv.Sessions(context.Background(), fpim.User("1"))
	if err != nil {
		t.Fatalf("srv.Sessions: %v", err)
	}
	if len(sess) != 1 || sess[0].ConnID != connID || sess[0].Node != a.id || sess[0].OS != "linux" {
		t.Fatalf("Sessions 结果不对：%+v", sess)
	}

	// ⑥ 顶号：同一 subject 从节点 B 再连一次，节点 A 上的旧连接应收到
	// 被踢的关闭码（4003），业务 server 应收到原因为 replaced 的断开事件。
	closed := make(chan int, 1)
	cli.OnClose(func(code int) { closed <- code })
	cli2, err := fpim.Dial(context.Background(), fpim.ClientConfig{URL: b.wsURL, App: "a1", Token: "tok-1"})
	if err != nil {
		t.Fatalf("fpim.Dial(cli2): %v", err)
	}
	defer cli2.Close()
	select {
	case code := <-closed:
		if code != fpim.CloseKicked {
			t.Fatalf("被顶替的旧连接应以 %d 关闭，实际 %d", fpim.CloseKicked, code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("旧连接 5 秒内没有被顶掉")
	}
	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range events {
			if ev.Kind == fpim.EventDisconnected && ev.ConnID == connID {
				return ev.Reason == "replaced"
			}
		}
		return false
	}, "server 应收到原因为 replaced 的 Disconnected 事件")

	// 顶号还必须正确处理注册表：握手脚本把旧连接标识返回进被顶替列表这件
	// 事本身，不等于它已经真的把旧条目从 fp:im:{app:subject}:conn 里删掉。
	// 如果脚本只顶对了本地连接、关对了旧 ws，却没删对注册表条目（或者删错了
	// 标识），旧条目会残留成一条指向节点 A 的死记录：后续推送会对着这条死
	// 记录去 Publish，节点 A 早已没有这条连接，SPUBLISH 的订阅者数会是 0，
	// 但如果同一次 PushMany 里还命中了 cli2 那条真实连接，Nodes 计数依然会
	// 让业务方以为"全部送达"——这类账目不平的问题不会体现在①-⑥的任何一条
	// 断言上，只有专门查一次会话、核对"注册表里现在到底是谁"才能测到。
	sess2, err := srv.Sessions(context.Background(), fpim.User("1"))
	if err != nil {
		t.Fatalf("顶号之后 srv.Sessions: %v", err)
	}
	if len(sess2) != 1 || sess2[0].Node != b.id || sess2[0].ConnID == connID {
		t.Fatalf("顶号之后注册表应只剩节点 B 上的新连接（旧连接标识 %s 不应再出现），实际 %+v", connID, sess2)
	}

	// ⑦ 踢人：业务服务主动踢掉该 subject 的全部连接，cli2 应收到被踢的
	// 关闭码。
	closed2 := make(chan int, 1)
	cli2.OnClose(func(code int) { closed2 <- code })
	if err := srv.Kick(context.Background(), fpim.User("1")); err != nil {
		t.Fatalf("srv.Kick: %v", err)
	}
	select {
	case code := <-closed2:
		if code != fpim.CloseKicked {
			t.Fatalf("Kick 应以 %d 关闭连接，实际 %d", fpim.CloseKicked, code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kick 5 秒内没有生效")
	}
}
