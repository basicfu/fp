package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/im/config"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	"github.com/basicfu/fp/internal/testsupport"
	fpim "github.com/basicfu/fp/sdk/im"
)

// 本文件是 cmd/fp-im 的第一批测试。这个包此前零测试，而整分支最终复审
// 点名的三个缺陷（甲一、甲二、乙一）全部落在这里：集成测试把装配顺序
// **手抄**了一遍，所以这段代码里任何顺序退化都不会让它变红。
//
// 这里的测试直接调用 serve() 起真实节点（真实 Redis、真实 ws/gRPC 监听、
// 真实的 SDK 做客户端），不再手抄任何装配顺序。

// waitUntil 轮询等待条件成立，超时即失败。不用固定睡眠再断言：睡眠短了
// 会在机器负载重时假失败，睡长了每条测试都白白慢下来。
func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// staticApps 是 fpappcfg.Fetcher 的静态实现，供这些测试注入。
//
// app 配置现在来自 fp，而这些测试验的是装配顺序、连接注册、优雅关闭、
// 重登记这类**与 fp 无关**的行为——为它们连带起一整套 fp 不值得。
//
// 注入的是 Fetcher 而不是整个 AppConfigSource：真实的 fpappcfg.Source
// 连同它的 onLoad 回调（TrackApp + 同步 Refresh）仍然在链路上，「甲一」
// 那条测试因此测的还是真东西。
//
// 这些测试全部用访客身份握手：访客路径不经过 fp（见设计第四节 4.1）。
type staticApps map[string]model.AppConfig

func (a staticApps) Fetch(_ context.Context, app string) (model.AppConfig, error) {
	c, ok := a[app]
	if !ok {
		return model.AppConfig{}, errors.New("no such app")
	}
	return c, nil
}

// guestApps 是只声明 a1、允许访客的配置。
func guestApps() staticApps {
	return staticApps{"a1": {
		AppID: "a1", ConnPolicy: model.PolicyReplace, ConnLimit: 3,
		AllowGuest: true, GuestIPRate: 100,
	}}
}

// testConfig 构造一份指向测试 Redis、端口全交给操作系统分配的配置。
// 不走 config.Load：那条路径读一个 YAML 文件，测试要在同一个进程里起两个
// 配置不同的节点，为此各写一个临时文件只是把结构体字面量绕了一圈。
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	url := os.Getenv("FP_TEST_REDIS_URL")
	if url == "" {
		t.Fatal("缺少 FP_TEST_REDIS_URL，请用 ./scripts/test.sh 跑测试")
	}
	return &config.Config{
		// dev 而不是 prod：Insecure() 由它推导，下面那个必然不通的地址
		// 也就不会去要求 TLS。
		Env:   "dev",
		Log:   config.Log{Level: "warn"},
		HTTP:  config.HTTP{Addr: "127.0.0.1:0"},
		GRPC:  config.Listen{Addr: "127.0.0.1:0"},
		Redis: config.Endpoint{URL: url},
		// 访客握手不会走到 Authenticator，这个地址永远不会被真的拨号；
		// 但 fpauth.New 要求两项都非空，所以给一个必然不通的地址，万一哪天
		// 真的被拨了，失败会立刻暴露而不是悄悄连上别的东西。
		FPSDK: config.FPSDK{Addr: "127.0.0.1:1", Secret: "im-secret"},
		// 心跳 200ms（而不是生产的 3 秒）：测试里要等的传播延迟就是心跳周期
		// 本身，取生产值只会把每条测试拖慢十几倍。
		Node: config.Node{
			Heartbeat: config.Duration(200 * time.Millisecond),
			DeadAfter: config.Duration(2 * time.Second),
		},
		Conn: config.Conn{
			FieldTTL:    config.Duration(30 * time.Minute),
			FieldRenew:  config.Duration(10 * time.Minute),
			IdleTimeout: config.Duration(60 * time.Second),
			AuthTimeout: config.Duration(2 * time.Second),
			SendQueue:   64,
		},
		Pipeline: config.Pipeline{FlushSize: 1},
	}
}

// nodeIDCounter 保证同一个测试进程里起的节点标识互不相同。
// 生产的 model.NewNodeID 是"主机名-进程号-启动毫秒"，同进程内两个节点只
// 剩毫秒能区分，撞上的后果见 model.NewNodeID 的注释。
var nodeIDCounter atomic.Int64

// startTestNode 用 serve() 起一个真实节点，返回它的 ws 与 gRPC 地址，
// 以及一个"触发优雅关闭并等 serve 真正返回"的函数。
// staticCreds 让业务 server 的凭据校验在测试里不必回源到 fp。
type staticCreds map[string]string

func (c staticCreds) VerifyAppCredential(_ context.Context, app, secret string) error {
	if c[app] != secret {
		return errors.New("凭据无效")
	}
	return nil
}

func startTestNode(t *testing.T, cfg *config.Config, apps staticApps) (nodeID, wsURL, grpcAddr string, stop func()) {
	t.Helper()
	nodeID = fmt.Sprintf("im-test-%d-%d", time.Now().UnixNano(), nodeIDCounter.Add(1))
	ctx, cancel := context.WithCancel(context.Background())
	type addrs struct{ http, grpc string }
	ready := make(chan addrs, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, cfg, slog.Default(), serveOptions{
			nodeID:  nodeID,
			ready:   func(h, g string) { ready <- addrs{h, g} },
			fetcher: apps,
			creds:   staticCreds{"a1": "s1"},
		})
	}()
	var a addrs
	select {
	case a = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("serve 在开始服务之前就返回了: %v", err)
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("serve 15 秒内没有开始服务")
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("serve 返回错误: %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Error("serve 30 秒内没有完成关闭")
			}
		})
	}
	t.Cleanup(stop)
	return nodeID, "ws://" + a.http + "/ws", a.grpc, stop
}

// TestServeDeliversFirstConnectEventAfterStartup 是甲一的验收测试：
// 节点刚起来、第一个 client 就连上来时，它的连接建立事件必须送达业务
// server，不能被静默丢弃。
//
// 缺陷版本的行为：hub.Deliver 在需要跨节点转发时先调 live.TrackApp 再读
// live.ServerNodes(app)，而 TrackApp 只是把 app 加进待刷新集合，真正的
// 内容要等下一次 Refresh 才有；装配代码从头到尾没有对配置里的 app 做过
// 预先追踪，于是第一条事件的候选列表必然为空，事件被静默丢弃（零日志），
// 之后也不会重发——业务方永远收不到这条上线通知。
func TestServeDeliversFirstConnectEventAfterStartup(t *testing.T) {
	ctx := context.Background()
	rdb := testsupport.NewTestRedis(t)

	// 节点 B：业务 server 接在这里。它自己不会有任何 ws 连接。
	_, _, grpcB, _ := startTestNode(t, testConfig(t), guestApps())
	srv, err := fpim.NewServer(fpim.ServerConfig{Addr: grpcB, AppID: "a1", AppSecret: "s1", Insecure: true})
	if err != nil {
		t.Fatalf("fpim.NewServer: %v", err)
	}
	defer srv.Close()
	var mu sync.Mutex
	var events []fpim.Event
	srv.OnEvent(func(_ context.Context, ev fpim.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	waitUntil(t, srv.StreamHealthy, "业务 server 到节点 B 的接入流应就绪")
	// 等 B 真的把自己写进 a1 的服务表，再起节点 A：这样 A 在装配阶段那次
	// 同步 Refresh 的时刻，Redis 里就已经有 B 可读——"启动后第一条事件不
	// 丢"这条性质才是被真正验证的东西，而不是在测"A 起得比 B 晚多久"。
	waitUntil(t, func() bool {
		return rdb.HLen(ctx, model.SrvKey("a1")).Val() == 1
	}, "节点 B 应在 a1 的服务表里登记自己")

	// 节点 A：client 接在这里。A 上没有任何 server 流，连接事件必须跨节点
	// 转发到 B 才能到达业务 server。
	_, wsA, _, _ := startTestNode(t, testConfig(t), guestApps())
	cli, err := fpim.Dial(ctx, fpim.ClientConfig{URL: wsA, App: "a1", Guest: uuid.NewString(), OS: "linux"})
	if err != nil {
		t.Fatalf("fpim.Dial: %v", err)
	}
	defer cli.Close()

	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range events {
			if ev.Kind == fpim.EventConnected {
				return true
			}
		}
		return false
	}, "节点刚启动就连上来的第一条连接，它的建立事件必须送达业务 server："+
		"装配时没有对配置里的 app 预先追踪的话，这条事件会因为转发候选列表为空被静默丢弃，且永不重发")
}

// dialWS 拨一个到 wsURL 的原始 ws 连接（不经 fpim SDK），用来发送 SDK
// 没有暴露的自定义字段（这里是 kind）。
func dialWS(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// sendFrame 把 v 序列化成 JSON 文本帧发出去。
func sendFrame(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

// readFrame 读一帧并解析成 model.Frame；连接被关闭时 err 非空，
// 调用方用 websocket.CloseStatus(err) 取关闭码。
func readFrame(t *testing.T, c *websocket.Conn) (model.Frame, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		return model.Frame{}, err
	}
	var f model.Frame
	if uerr := json.Unmarshal(b, &f); uerr != nil {
		t.Fatal(uerr)
	}
	return f, nil
}

// appsWithBizAuth 是一份声明了 biz_auth 的配置，verifyURL 通常是一个自签
// 证书的 httptest.NewTLSServer 地址。
func appsWithBizAuth(verifyURL string) staticApps {
	return staticApps{"a1": {
		AppID: "a1", ConnPolicy: model.PolicyReplace, ConnLimit: 5,
		AllowGuest: true, GuestIPRate: 100,
		BizAuth: &model.BizAuth{
			VerifyURL: verifyURL,
			Timeout:   model.Duration(2 * time.Second),
			CacheSize: 100,
		},
	}}
}

// TestServeWithBizAuthStartsAndDispatches 是 Task 6 装配的验收测试：配置带
// biz_auth 时进程要能正常启动，且带 kind=biz 的握手要被路由到业务方的验证
// 地址，而不是 fp 认证器。
//
// 断言刻意留在"回调确实被调用了一次"这一层，不去追究回调成功与否：假的
// 验证服务用自签证书，配置校验又强制 HTTPS，进程侧 HTTP 客户端默认不信任
// 自签证书，所以这次回调注定失败、握手以 4004 收尾。真正的回调成功路径由
// bizauth 包内测试覆盖（那里可以自由注入 Client）。这里只验证装配：
// kind=biz 有没有被送到 bizauth 而不是 fpauth。
func TestServeWithBizAuthStartsAndDispatches(t *testing.T) {
	var calls atomic.Int64
	verify := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user_id":"biz-42"}`))
	}))
	// 计数挂在 ConnState 而不是 handler 里：进程侧的 HTTP 客户端不信任这张
	// 自签证书，TLS 握手会在证书校验阶段就被客户端中止（client 发一个
	// fatal alert 后连接关闭），请求内容根本没有机会送到 handler——实测
	// 确实如此：handler 里的计数器永远是 0，就算装配是对的。ConnState 在
	// Accept 之后、TLS 握手之前就会触发 StateNew，能在不依赖握手成功的
	// 前提下证明"这次握手真的把 TCP 连接打到了这个地址"。
	verify.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			calls.Add(1)
		}
	}
	verify.StartTLS()
	defer verify.Close()

	apps := appsWithBizAuth(verify.URL)
	_, wsURL, _, _ := startTestNode(t, testConfig(t), apps)

	c := dialWS(t, wsURL)
	defer c.CloseNow()
	sendFrame(t, c, map[string]any{"t": "auth", "app": "a1", "token": "whatever", "kind": "biz"})

	// 证书不被信任，所以握手最终会失败并拿到 4004。但请求确实发出去了，
	// 这证明 cmd 里的装配把 kind=biz 路由到了 bizauth 而不是 fpauth。
	_, err := readFrame(t, c)
	if websocket.CloseStatus(err) != model.CloseUnavailable {
		t.Fatalf("回调失败应当以 4004 关闭，实际 %v", err)
	}
	waitUntil(t, func() bool { return calls.Load() > 0 },
		"带 kind=biz 的握手必须打到业务方的验证地址；打不到说明装配把它错误地路由给了 fp 认证器")
}

// fakeHandshaker 是 reregistrar 需要的 registry.Conns 子集的假实现。
type fakeHandshaker struct {
	// block 非 nil 时，每次调用先等它被关闭——用来把一轮重登记按在
	// "正在跑"的状态上，验证它没有占住调用方的协程。
	block chan struct{}
	err   error

	calls atomic.Int32
}

func (f *fakeHandshaker) Handshake(_ context.Context, _, _, _ string, _ model.ConnMeta, _ model.Policy, _ int, _ []string) (registry.HandshakeResult, error) {
	if f.block != nil {
		<-f.block
	}
	f.calls.Add(1)
	return registry.HandshakeResult{}, f.err
}

// fakeLiveness 是 reregistrar 需要的 registry.Liveness 子集的假实现。
type fakeLiveness struct {
	present    bool
	presentErr error
	beats      atomic.Int32
}

func (f *fakeLiveness) Beat(context.Context) error { f.beats.Add(1); return nil }
func (f *fakeLiveness) SelfPresent(context.Context) (bool, error) {
	return f.present, f.presentErr
}
func (f *fakeLiveness) LiveNodes() []string { return []string{"im-a"} }

// hubWithConns 建一个带 n 条连接的 hub，供重登记遍历。
func hubWithConns(t *testing.T, n int) *hub.Hub {
	t.Helper()
	h, _, _, _, _ := hubtest.NewHub()
	for i := 0; i < n; i++ {
		c := &hubtest.Conn{ConnID: fmt.Sprintf("c%d", i)}
		h.AddConn(context.Background(), "a1", model.User(fmt.Sprintf("%d", i)), c, model.ConnMeta{Node: "im-a"}, "ua")
	}
	return h
}

// TestGapReregisterCountsFailures 是甲二问题二的回归测试：重登记的失败
// 必须被计数（并汇总进日志），不能像原来那样用 `_, _ =` 整个丢掉。
//
// 丢掉的后果：Redis 数据真丢过的场景下，失败的那条连接会永久从注册表
// 消失——client 每 25 秒发一次心跳，空闲超时不会触发，套接字一直开着，
// 但推送对它恒为"不在线"，无日志、无重试。
func TestGapReregisterCountsFailures(t *testing.T) {
	h := hubWithConns(t, 3)
	hs := &fakeHandshaker{err: context.DeadlineExceeded}
	done := make(chan [2]int, 1)
	r := &reregistrar{
		conns: hs, live: &fakeLiveness{present: false}, log: slog.Default(),
		onDone: func(total, failed int) { done <- [2]int{total, failed} },
	}
	r.onGap(context.Background(), h)
	select {
	case got := <-done:
		if got[0] != 3 || got[1] != 3 {
			t.Fatalf("3 条连接全部登记失败时应报告 total=3 failed=3，实际 total=%d failed=%d", got[0], got[1])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("重登记 10 秒内没有跑完")
	}
}

// TestGapReregisterSkippedWhenNodeEntryPresent 覆盖设计文档第七节写的
// 前置条件：只有发现自己在节点表里的条目已经消失（Redis 重启过、数据
// 没了）时才需要全量重登记。多数 Gap 只是一次普通的订阅重连，数据都还
// 在，这一层判断让"全量重登记"这条昂贵的路径本身变得罕见。
func TestGapReregisterSkippedWhenNodeEntryPresent(t *testing.T) {
	h := hubWithConns(t, 2)
	hs := &fakeHandshaker{}
	live := &fakeLiveness{present: true}
	done := make(chan struct{})
	r := &reregistrar{
		conns: hs, live: live, log: slog.Default(),
		onDone: func(int, int) { close(done) },
	}
	r.onGap(context.Background(), h)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Gap 处理 10 秒内没有跑完")
	}
	if hs.calls.Load() != 0 {
		t.Fatalf("本节点条目仍在时不应做任何重登记，实际跑了 %d 条", hs.calls.Load())
	}
	if live.beats.Load() != 1 {
		t.Fatalf("不管要不要重登记，Gap 之后都必须补写一次心跳，实际 %d 次", live.beats.Load())
	}
}

// TestGapReregisterDoesNotBlockCaller 是甲二问题一的回归测试：重登记必须
// 跑在独立协程里，onGap 立刻返回。
//
// 原实现直接跑在节点频道的消费协程里：按每秒 2000 条限速，十万连接的
// 节点会独占这个协程约 50 秒，这期间本节点频道上所有的推送、上行、踢人
// 信封全部停止处理，总线出通道（容量 1024）满了之后 Redis 客户端库会在
// 超时后丢弃，只留一行库内日志。
func TestGapReregisterDoesNotBlockCaller(t *testing.T) {
	h := hubWithConns(t, 1)
	hs := &fakeHandshaker{block: make(chan struct{})}
	done := make(chan struct{})
	r := &reregistrar{
		conns: hs, live: &fakeLiveness{present: false}, log: slog.Default(),
		onDone: func(int, int) { close(done) },
	}

	returned := make(chan bool, 1)
	go func() { returned <- r.onGap(context.Background(), h) }()
	select {
	case started := <-returned:
		if !started {
			t.Fatal("第一次 Gap 应真的起一轮重登记")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onGap 必须立刻返回：重登记跑在调用方（节点频道消费协程）里的话，" +
			"重登记期间本节点频道上所有推送/上行/踢人信封都会停止处理")
	}

	// 上一轮还卡在 Handshake 里没跑完，此时再来一次 Gap 应该被合并掉，
	// 而不是又起一轮和它抢 Redis。
	if r.onGap(context.Background(), h) {
		t.Fatal("上一轮重登记还在跑时，新的 Gap 应被合并而不是再起一轮")
	}

	close(hs.block)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("放行之后重登记 10 秒内没有跑完")
	}
	if hs.calls.Load() != 1 {
		t.Fatalf("两次 Gap 合并成一轮、共 1 条连接，应只登记 1 次，实际 %d 次", hs.calls.Load())
	}
}

// TestServeGracefulShutdownClosesWebSockets 是乙一的验收测试：收到退出
// 信号时，本节点的 ws 连接必须被主动关闭（4004 服务不可用：和 client
// 自身无关，退避后重连），并且走完整的拆连接路径——注册表条目被删掉、
// 断开事件被发出。
//
// 缺陷版本的行为：关闭时只停 gRPC 和 HTTP，而 http.Server 按 Go 的契约
// 既不等待也不关闭被劫持的连接（ws 走的正是劫持）。于是每一次发版，
// 所有 ws 连接都不发关闭帧、不删注册表条目、不发断开事件，进程直接退出：
// client 只看到连接被硬中断（读不到任何状态码），业务方的在线表里留着
// 一条永远不会收到断开事件的连接，注册表里留着一条要等 field TTL
// （默认 30 分钟）才消失的残留条目。设计文档第七节只承诺"节点崩溃时
// 断开事件不会发出"，从没说每次发版都是这样。
func TestServeGracefulShutdownClosesWebSockets(t *testing.T) {
	ctx := context.Background()
	rdb := testsupport.NewTestRedis(t)

	// 业务 server 接在同一个节点上：断开事件要能被观察到，就必须有一条
	// 还活着的 server 流——这条流也顺带验证了关闭顺序（先断 ws、再停
	// gRPC），顺序反了断开事件就没有出口。
	_, wsURL, grpcAddr, stop := startTestNode(t, testConfig(t), guestApps())
	srv, err := fpim.NewServer(fpim.ServerConfig{Addr: grpcAddr, AppID: "a1", AppSecret: "s1", Insecure: true})
	if err != nil {
		t.Fatalf("fpim.NewServer: %v", err)
	}
	defer srv.Close()
	var mu sync.Mutex
	var events []fpim.Event
	srv.OnEvent(func(_ context.Context, ev fpim.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	waitUntil(t, srv.StreamHealthy, "业务 server 的接入流应就绪")

	guest := uuid.NewString()
	cli, err := fpim.Dial(ctx, fpim.ClientConfig{URL: wsURL, App: "a1", Guest: guest, OS: "linux"})
	if err != nil {
		t.Fatalf("fpim.Dial: %v", err)
	}
	defer cli.Close()
	codes := make(chan int, 4)
	cli.OnClose(func(code int) { codes <- code })
	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) == 1 && events[0].Kind == fpim.EventConnected
	}, "业务 server 应先收到 Connected 事件")

	connKey := model.ConnKey("a1", "g:"+guest)
	if n := rdb.HLen(ctx, connKey).Val(); n != 1 {
		t.Fatalf("关闭之前注册表里应有 1 条连接，实际 %d", n)
	}

	stop() // 触发优雅关闭，并等 serve 真正返回

	select {
	case code := <-codes:
		if code != fpim.CloseUnavailable {
			t.Fatalf("节点下线时 ws 连接应以 %d（服务不可用：与 client 自身无关，退避后重连）关闭，实际 %d",
				fpim.CloseUnavailable, code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("节点下线时必须主动关闭 ws 连接：http.Server 既不等待也不关闭被劫持的连接，" +
			"不主动关的话 client 只会看到连接被硬中断，读不到任何关闭码")
	}

	// serve 已经返回，拆连接必须在那之前全部走完，所以这里直接断言，
	// 不需要再轮询等待。
	if n := rdb.HLen(ctx, connKey).Val(); n != 0 {
		t.Fatalf("优雅关闭必须走正常拆连接路径删掉注册表条目，实际还剩 %d 条（要等 field TTL 才消失）", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[1].Kind != fpim.EventDisconnected {
		t.Fatalf("优雅关闭必须发出断开事件（否则业务方在线表里留下一条永不下线的连接），实际收到 %+v", events)
	}
}

// TestGapReregisterTreatsSelfPresentErrorAsMissing 覆盖 SelfPresent 的错误
// 分支：查不出"自己的节点条目还在不在"时，必须按最坏情况（条目已经没了）
// 处理，照常做全量重登记。
//
// 方向不能反：多做一次重登记的代价是一批限速的写入，漏做一次的代价是这些
// 连接在注册表里永久不可见、推送对它们恒为"不在线"，直到 client 自己重连。
// 用 present=true 配 err!=nil 构造，确保测的是"错误压过了返回值"，而不是
// 恰好因为返回值是 false 才重登记。
func TestGapReregisterTreatsSelfPresentErrorAsMissing(t *testing.T) {
	h := hubWithConns(t, 2)
	hs := &fakeHandshaker{}
	done := make(chan [2]int, 1)
	r := &reregistrar{
		conns: hs,
		live:  &fakeLiveness{present: true, presentErr: errors.New("redis 不可达")},
		log:   slog.Default(),
		onDone: func(total, failed int) {
			done <- [2]int{total, failed}
		},
	}
	r.onGap(context.Background(), h)
	select {
	case got := <-done:
		if got[0] != 2 {
			t.Fatalf("查询失败时应按需要重登记处理、登记全部 2 条，实际 total=%d", got[0])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("重登记 10 秒内没有跑完")
	}
	if hs.calls.Load() != 2 {
		t.Fatalf("应真的跑了 2 条握手，实际 %d 条", hs.calls.Load())
	}
}
