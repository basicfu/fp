// phase2_env_test.go 装配第二阶段集成测试用的完整环境：一个（或两个）真实
// 监听端口上的 fp + 连上去的真实 SDK 客户端。风格上参照本包已有的
// env_test.go（phase1），但底层网络换成真实回环监听而非 httptest——
// 第二阶段测的是 gRPC + SDK 这条链路本身，httptest 只覆盖了 HTTP 管理面。
package integration_test

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/grpcapi"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
	fpsdk "github.com/basicfu/fp/sdk"
)

// phase2Services 是从共享的 pool/rdb 装配出的一整套 service 层对象，
// 按 cmd/fp/main.go 的方式接线。拆成独立类型是为了 spawnPeer 能给"第二个
// 实例"配出一套完全独立的 Go 对象图——两个实例之间因此只通过 pool/rdb
// 相连，不共享任何进程内引用，"撤销必须经过 Redis 才能跨实例生效"这条
// 性质才是被结构性保证的，而不是恰好成立（见 TestRevokeCrossesFpInstances）。
type phase2Services struct {
	apps      *service.ApplicationService
	users     *service.UserService
	sessions  *service.SessionService
	accounts  *service.AccountService
	auth      *service.AuthService
	sms       *notify.FakeProvider
	revokePub *store.RevokePublisher
}

func wireServices(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client) phase2Services {
	t.Helper()

	users := service.NewUserService(pool)
	logs := service.NewLoginLogService(pool)
	codes := notify.NewCodeService(rdb)

	epochs := store.NewEpochStore(rdb)
	revokePub := store.NewRevokePublisher(rdb)
	sessions := service.NewSessionService(store.NewSessionStore(rdb), revokePub, epochs)

	registry := connector.NewRegistry()
	if err := registry.Register(connector.NewPassword(users)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	if err := registry.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}
	apps := service.NewApplicationService(pool, registry)

	sms := notify.NewFakeProvider(notify.ChannelSMS, "fake")
	// []RateRule{} 是显式关闭频率限制，仅用于测试——生产装配千万别照抄，
	// 理由与 env_test.go（phase1）newEnv 里的同一行注释一致。
	sender := notify.NewSender(pool, store.NewRateLimiter(rdb), []notify.RateRule{})
	sender.AddProvider(sms)

	accounts := service.NewAccountService(users, sessions, epochs, logs)
	auth := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users, Sessions: sessions, Logs: logs,
		Registry: registry, Notifier: sender, Codes: codes,
	})

	return phase2Services{
		apps: apps, users: users, sessions: sessions,
		accounts: accounts, auth: auth, sms: sms, revokePub: revokePub,
	}
}

// phase2Env 是一套真实端口上的 fp + 一个连上去的 SDK 客户端。
//
// 用真实回环监听而非 bufconn：验收项 8 要求"fp 不可用时业务不中断"，而
// bufconn 无法模拟"服务端消失"。这里必须能 Stop() 掉监听、再在原端口重新
// 起一个（见 stopFp / restartFp）。
type phase2Env struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	phase2Services

	addr      string
	server    *grpcapi.Server
	cancelRun context.CancelFunc
	sdk       *fpsdk.Client

	app              *domain.Application
	appID, appSecret string
}

// newPhase2Env 装配一套完整的 fp：真实 Postgres/Redis、一个启用了短信登录的
// 应用、一个真实监听端口上的 grpcapi.Server，以及一个已连上它的 SDK 客户端。
//
// opts 用于在连接建立前调整 SDK 的 Options（例如打开 AllowStaleOnOutage、
// 改小 DegradedCacheTTL）——追加在默认值（Addr/AppID/AppSecret/Insecure）
// 之后应用，因此可以覆盖除这四项之外的任何字段。
func newPhase2Env(t *testing.T, opts ...func(*fpsdk.Options)) *phase2Env {
	t.Helper()

	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	e := &phase2Env{
		pool: pool, rdb: rdb,
		phase2Services: wireServices(t, pool, rdb),
	}

	ctx := context.Background()
	app, secret, err := e.apps.Create(ctx, "phase2-集成测试-"+uuid.NewString(), "phase2-"+uuid.NewString())
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	for _, typ := range []string{connector.TypePassword, connector.TypeSMSCode} {
		if err := e.apps.SetConnector(ctx, app.ID, typ, true, nil); err != nil {
			t.Fatalf("启用 %s: %v", typ, err)
		}
	}
	e.app, e.appID, e.appSecret = app, app.AppID, secret

	e.startServer(t)
	e.sdk = e.dial(t, opts...)
	return e
}

// spawnPeer 在共享的 pool/rdb 之上起第二个 fp 实例：同一个应用（appID/
// appSecret 相同，因为它们是 Postgres 里的同一行，任何一个实例的
// ApplicationService 都能看到），但完全独立的一套 service 层对象、一个全新的
// grpcapi.Server、一个新的监听端口，因而是一条独立的 Redis 撤销订阅——
// RevokeHub 在每次 grpcapi.New 时都会新建，两个实例之间唯一的桥梁只有共享的
// PG/Redis。
//
// 不自动 dial 一个 SDK 客户端：多数用到 spawnPeer 的测试只需要
// peer.accounts 之类的 service 层入口去直接操纵服务端状态；需要连它的客户端
// 时调用方显式调 peer.dial(t)。
func (e *phase2Env) spawnPeer(t *testing.T) *phase2Env {
	t.Helper()
	peer := &phase2Env{
		pool: e.pool, rdb: e.rdb,
		phase2Services: wireServices(t, e.pool, e.rdb),
		app:            e.app,
		appID:          e.appID,
		appSecret:      e.appSecret,
	}
	peer.startServer(t)
	return peer
}

// startServer 在 e.addr 上起一个真实监听的 grpcapi.Server（e.addr 为空串时
// 由系统分配随机端口），并阻塞到撤销中继确认订阅上 Redis 才返回。
//
// 顺序镜像 cmd/fp/main.go 与 internal/grpcapi/env_test.go 的 grpcEnv：
// Watch 一旦被接受就会给客户端发 ready，SDK 把它当作"此后的撤销不会漏推"的
// 承诺，这个承诺只有在 hub 已经真正订阅上 Redis 之后才成立——不等 Ready()
// 就返回的话，测试紧接着触发的撤销可能落在"服务端还没订上"这段窗口里，
// 永久丢失且没有任何信号。
//
// restartFp 复用这个方法：e.addr 在首次调用后已经记下了真实地址，第二次
// 调用会在同一地址上重新监听。
func (e *phase2Env) startServer(t *testing.T) {
	t.Helper()

	lis := listenAddr(t, e.addr)
	e.addr = lis.Addr().String()

	srv := grpcapi.New(grpcapi.Deps{Auth: e.auth, Apps: e.apps, Pub: e.revokePub})
	e.server = srv

	// runCtx 单独控制 ServeWhenReady 内部那条 Redis 订阅的生命周期，与
	// srv.Shutdown 管的 gRPC 服务生命周期分开——理由见 stopFp 的注释，
	// 与 grpcEnv 里同名字段的注释完全一致。
	runCtx, cancelRun := context.WithCancel(context.Background())
	e.cancelRun = cancelRun
	t.Cleanup(cancelRun)

	go func() { _ = srv.ServeWhenReady(runCtx, lis, 5*time.Second) }()
	select {
	case <-srv.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("等待 gRPC 服务的撤销中继就绪超时")
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	})
}

// listenAddr 在 addr 上起一个真实回环监听；addr 为空串时由系统分配端口。
//
// 重试是给 restartFp 用的：上一个监听器刚关闭，操作系统释放端口可能有极短
// 的滞后（尤其是 Windows）。做法与 sdk/client_test.go 的 startStub 一致。
func listenAddr(t *testing.T, addr string) net.Listener {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	var lis net.Listener
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		lis, err = net.Listen("tcp", addr)
		if err == nil {
			return lis
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("net.Listen(%q): %v", addr, err)
	return nil
}

// stopFp 停掉 gRPC 监听，模拟 fp 宕机。
//
// 同时取消 runCtx：Shutdown 只关 hub（让 Watch handler 返回）与
// GracefulStop（停止接受连接），并不会让 startServer 内部经 ServeWhenReady
// 间接起的那个 Run(ctx) goroutine 返回——它只认自己的 ctx。不单独取消的话，
// 这条 Redis 订阅连接会在整个测试二进制的生命周期内一直挂着，多实例测试
// 与"掐订阅连接"的测试都依赖能精确数出当前有几条订阅连接，泄漏会直接
// 污染那些断言。
func (e *phase2Env) stopFp(t *testing.T) {
	t.Helper()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e.server.Shutdown(shutdownCtx)
	e.cancelRun()
}

// restartFp 在原地址重新起一个 fp 实例，模拟宕机恢复。
func (e *phase2Env) restartFp(t *testing.T) {
	t.Helper()
	e.startServer(t)
}

// dial 建一个连到本实例的 SDK 客户端。
//
// Insecure: true——测试环境没有 TLS。这正是 Insecure 这个选项存在的理由，
// 也是它必须默认关闭的理由：集成测试里出现它是合理的，生产配置里出现就是
// 事故。
func (e *phase2Env) dial(t *testing.T, opts ...func(*fpsdk.Options)) *fpsdk.Client {
	t.Helper()
	o := fpsdk.Options{
		Addr:      e.addr,
		AppID:     e.appID,
		AppSecret: e.appSecret,
		Insecure:  true,
	}
	for _, fn := range opts {
		fn(&o)
	}
	client, err := fpsdk.New(o)
	if err != nil {
		t.Fatalf("fpsdk.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// updateSessionPolicy 在 e.app 当前的会话策略基础上应用 mutate 并写回
// Postgres。两个实例共享同一个应用行，因此无论经哪个实例的 apps 调用，
// 变更都会立即对另一个实例生效——不需要任何跨实例同步。
func (e *phase2Env) updateSessionPolicy(t *testing.T, mutate func(*domain.SessionPolicy)) {
	t.Helper()
	p := e.app.Session
	mutate(&p)
	updated, err := e.apps.UpdateSessionPolicy(context.Background(), e.app.ID, p)
	if err != nil {
		t.Fatalf("更新会话策略: %v", err)
	}
	e.app = updated
}

// login 走一次完整的短信验证码登录（发码 → 取码 → 登录），全部经由 e.sdk
// 这个已经连上本实例的客户端——这样断言的是 SDK 到 service 层的完整链路，
// 不是绕开 SDK 直接调用 service。
func (e *phase2Env) login(t *testing.T) (token string, userID uuid.UUID, sessionID string) {
	t.Helper()
	ctx := context.Background()
	phone := randomPhase2Phone()

	if err := e.sdk.Auth().SendLoginCode(ctx, phone); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	code := e.sms.LastParam("code")
	if code == "" {
		t.Fatal("假短信供应商没有收到验证码")
	}
	res, err := e.sdk.Auth().Login(ctx, fpsdk.LoginInput{
		ConnectorType: connector.TypeSMSCode,
		Credentials:   map[string]string{"phone": phone, "code": code},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	uid, err := uuid.Parse(res.User.GetId())
	if err != nil {
		t.Fatalf("解析 userId %q: %v", res.User.GetId(), err)
	}
	return res.Token, uid, res.SessionID
}

// phase2PhoneCounter 保证同一次测试进程内多次生成的手机号互不冲突。
var phase2PhoneCounter atomic.Int64

func randomPhase2Phone() string {
	n := phase2PhoneCounter.Add(1) % 100000000
	return fmt.Sprintf("139%08d", n)
}

// defaultWaitTimeout 是 waitUntil 的默认超时。
const defaultWaitTimeout = 5 * time.Second

// waitUntil 轮询到 cond 为真，超时（5 秒）则以 msg 失败。
//
// 推送、重连、跨实例传播都是异步的，断言必须轮询而不是 sleep 一个固定
// 时长——固定 sleep 要么太短导致偶发失败，要么太长把测试拖慢，且两者都会
// 被"加长 sleep"糊弄过去而掩盖真实的回归。
func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	waitUntilTimeout(t, defaultWaitTimeout, cond, msg)
}

// waitUntilTimeout 类似 waitUntil，但允许调用方指定超时。
func waitUntilTimeout(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// pubsubClientIDPattern 从一行 CLIENT LIST 输出里取 id=<数字> 字段。
var pubsubClientIDPattern = regexp.MustCompile(`\bid=(\d+)`)

// killPubSubConnection 找到 rdb 上唯一一条 TYPE pubsub 的连接并杀掉它，模拟
// 一次 Redis 订阅抖动（故障转移、网络毛刺）——go-redis 会静默重连并重发
// SUBSCRIBE。
//
// 用 TYPE pubsub 过滤、且只在"确认此刻恰好只有一条"时才动手：杀错连接会把
// 会话存储也一起断掉，那样测出来的是"Redis 挂了"而不是"订阅抖动了"。
//
// 轮询等待恰好一条，而不是假设第一次查询就已经如此：前一个测试收尾时，
// cancelRun 只是发出取消信号，并不等 RevokeHub.Run 内部的 goroutine 真正
// 退出、真正关掉它那条 Redis 订阅连接（这条不等待的取舍与 grpcEnv 一致，
// 见 stopFp 的注释）；轮询把这段极短的收尾尾巴吸收掉，避免本函数在前一个
// 测试的订阅连接还没来得及关闭时，把"两条"误判为异常而失败，也避免在
// 找不到目标时误杀一条不相关的连接。
func killPubSubConnection(t *testing.T, rdb *redis.Client) {
	t.Helper()
	ctx := context.Background()

	var id string
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := rdb.Do(ctx, "CLIENT", "LIST", "TYPE", "pubsub").Text()
		if err != nil {
			t.Fatalf("CLIENT LIST TYPE pubsub: %v", err)
		}
		lines := nonEmptyLines(raw)
		if len(lines) == 1 {
			m := pubsubClientIDPattern.FindStringSubmatch(lines[0])
			if m == nil {
				t.Fatalf("CLIENT LIST 输出解析不出 id: %q", lines[0])
			}
			id = m[1]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待恰好一条 pubsub 连接超时，当前有 %d 条：%q", len(lines), raw)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := rdb.ClientKillByFilter(ctx, "ID", id).Err(); err != nil {
		t.Fatalf("CLIENT KILL ID %s: %v", id, err)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
