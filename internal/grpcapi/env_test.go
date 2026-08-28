package grpcapi

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// grpcEnv 是一套跑在 bufconn 上的完整 fp 服务端。
//
// 用 bufconn 而不是真监听端口：不占端口、不受防火墙影响、测试并行时不冲突，
// 而且走的是真实的 gRPC 编解码与拦截器链——比直接调 handler 函数有意义得多，
// 因为 metadata 认证、错误码映射这些恰恰只在真实链路上才会被执行到。
type grpcEnv struct {
	client fpv1.AuthServiceClient
	// server 是跑在 bufconn 上的 *Server 本身，测试用它直接触发关闭
	// （见 TestShutdownCompletesWithOpenWatchStream），不必另起一套装配。
	server *Server
	app    *domain.Application
	appID  string
	secret string

	// 下面这些字段照搬 service 层 authEnv 的装配结果，供测试直接操纵服务端状态。
	auth     *service.AuthService
	sessions *service.SessionService
	accounts *service.AccountService
	users    *service.UserService
	sms      *notify.FakeProvider
	codes    *notify.CodeService

	// apps 用于创建/操纵应用（newApplication、setRotateInterval）；pool 用于绕过
	// service 层直接改库（disableApplication——第一阶段没有停用应用的管理接口，
	// 见 auth.go activeApp 的注释）；clock 是注入服务端 SessionService 的假时钟，
	// 供 setRotateInterval + clock.Advance 确定性地跨越 rotate_interval 之类的边界。
	apps  *service.ApplicationService
	pool  *pgxpool.Pool
	clock *fakeClock
}

// fakeClock 让测试能精确推进服务端时钟。
//
// Advance 的形参是 time.Duration，调用时必须带单位：Advance(2 * time.Second)。
// 裸整数 Advance(2000) 是 2000 纳秒，取整成毫秒后是 0，时钟纹丝不动——而这类
// 测试通常还有别的断言先命中，于是照样变绿，只是想验证的路径从未被执行到。
type fakeClock struct{ ms atomic.Int64 }

func newFakeClock(start int64) *fakeClock {
	c := &fakeClock{}
	c.ms.Store(start)
	return c
}

func (c *fakeClock) Now() int64              { return c.ms.Load() }
func (c *fakeClock) Advance(d time.Duration) { c.ms.Add(d.Milliseconds()) }

func newGRPCEnv(t *testing.T) *grpcEnv {
	t.Helper()

	// 按 internal/service/auth_test.go 的 authEnv 装配 pool / rdb / 各 service。
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	apps := service.NewApplicationService(pool)
	users := service.NewUserService(pool)
	epochs := store.NewEpochStore(rdb)
	clk := newFakeClock(time.Now().UnixMilli())
	// revokePub 同时喂给 sessions（发布撤销）和 hub（订阅撤销）——两个方向
	// 共用同一个 *store.RevokePublisher，与生产环境的 cmd/fp/main.go 装配一致。
	revokePub := store.NewRevokePublisher(rdb)
	sessions := service.NewSessionServiceWithClock(
		store.NewSessionStore(rdb), revokePub, epochs, clk.Now)
	logs := service.NewLoginLogService(pool)
	codes := notify.NewCodeService(rdb)

	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(users)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	if err := reg.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}

	sms := notify.NewFakeProvider(notify.ChannelSMS, "fake")
	// 显式关闭频率限制（[]RateRule{} 而不是 nil——nil 会套用默认的
	// "30 秒 1 条"）：多个用例需要对同一个手机号连发验证码。
	sender := notify.NewSender(pool, store.NewRateLimiter(rdb), []notify.RateRule{})
	sender.AddProvider(sms)

	authSvc := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users, Sessions: sessions, Logs: logs,
		Registry: reg, Notifier: sender, Codes: codes,
	})

	env := &grpcEnv{
		auth:     authSvc,
		sessions: sessions,
		accounts: service.NewAccountService(users, sessions, epochs, logs),
		users:    users,
		sms:      sms,
		codes:    codes,
		apps:     apps,
		pool:     pool,
		clock:    clk,
	}

	// 创建一个启用了 password 与 sms_code 两种登录方式的应用，
	// 记下它的 appID 与明文 secret（ApplicationService.Create 的第二个返回值）。
	primary := env.newApplication(t)
	env.app, env.appID, env.secret = primary.app, primary.appID, primary.secret

	// server 起在这里而不是懒加载，且先等 Ready() 才起 bufconn 的 Serve：
	// Server.Run 只有在 hub 真正订阅上 Redis 之后才关闭 Ready()（见
	// RevokeHub.Ready 的注释），这里照抄 cmd/fp/main.go 的编排顺序，
	// 保证 newGRPCEnv 返回之后，任何测试紧接着触发的撤销（比如 Logout）
	// 都能被 Watch 流收到，不会因为"服务端到底订上了没"而变得不确定。
	//
	// runCtx 单独控制 Run（进而那条 Redis 订阅）的生命周期，与下面
	// srv.Shutdown 管的 gRPC 服务生命周期分开：Shutdown 只负责关 hub 与
	// GracefulStop，并不会让 Run 返回，不单独取消 runCtx 的话每个用例
	// 都会在测试进程里永久多留一条 Redis 订阅连接。
	runCtx, cancelRun := context.WithCancel(context.Background())
	t.Cleanup(cancelRun)

	srv := New(Deps{Auth: authSvc, Apps: apps, Pub: revokePub})
	go func() { _ = srv.Run(runCtx) }()
	select {
	case <-srv.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("等待 gRPC 服务的撤销中继就绪超时")
	}
	env.server = srv

	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	env.client = fpv1.NewAuthServiceClient(conn)
	return env
}

// authed 返回带本应用凭据的 ctx。
func (e *grpcEnv) authed(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, mdAppID, e.appID, mdAppSecret, e.secret)
}

// testApplication 是一个已创建、已启用 password 与 sms_code 登录方式的应用，
// 及其仅创建时可见一次的明文 secret。
type testApplication struct {
	app    *domain.Application
	appID  string
	secret string
}

// phoneCounter 保证同一次测试进程内多次生成的手机号互不冲突。
var phoneCounter atomic.Int64

// randomPhone 生成一个格式合法（11 位、以 1 开头）且进程内唯一的手机号。
func randomPhone() string {
	n := phoneCounter.Add(1) % 100000000
	return fmt.Sprintf("138%08d", n)
}

// newApplication 在同一个 fp 部署下创建另一个应用，用于验证跨应用隔离等场景。
// name/slug 都带随机后缀，避免与 env 自身的应用或彼此冲突。
func (e *grpcEnv) newApplication(t *testing.T) *testApplication {
	t.Helper()
	ctx := context.Background()

	suffix := uuid.NewString()
	app, secret, err := e.apps.Create(ctx, "测试应用-"+suffix, "test-app-"+suffix)
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	for _, typ := range []string{connector.TypePassword, connector.TypeSMSCode} {
		if err := e.apps.SetConnector(ctx, app.ID, typ, true, nil); err != nil {
			t.Fatalf("启用 %s: %v", typ, err)
		}
	}
	return &testApplication{app: app, appID: app.AppID, secret: secret}
}

// setRotateInterval 把 env 当前应用的轮换间隔改成给定秒数，会话策略其余字段
// 保持不变，并把 env.app 更新为改后的值。
func (e *grpcEnv) setRotateInterval(t *testing.T, seconds int32) {
	t.Helper()
	policy := e.app.Session
	policy.RotateIntervalSeconds = seconds
	updated, err := e.apps.UpdateSessionPolicy(context.Background(), e.app.ID, policy)
	if err != nil {
		t.Fatalf("更新会话策略: %v", err)
	}
	e.app = updated
}

// disableApplication 直接改库停用 env 当前应用。
//
// 第一阶段还没有把应用置为 DISABLED 的管理接口（见 service/auth.go 里
// activeApp 的注释），这是当前唯一能触达"应用已停用"分支的办法。
func (e *grpcEnv) disableApplication(t *testing.T) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE application SET status = $1 WHERE id = $2`,
		domain.ApplicationStatusDisabled, e.app.ID,
	); err != nil {
		t.Fatalf("停用应用: %v", err)
	}
}

// loginWithPassword 走 gRPC 的完整流程：短信验证码注册一个新用户、为其设置
// 密码、再用密码登录，返回签发的 token。
//
// ctx 须携带应用凭据（见 authed）。密码本身没有专门的 gRPC 写入接口（这是
// 后续任务的范围），SetPassword 这一步直接调用 service 层；登录判定路径
// （SendLoginCode / Login 本身）仍然全部经过 gRPC，这样才检验到映射层代码。
func (e *grpcEnv) loginWithPassword(t *testing.T, ctx context.Context) string {
	t.Helper()
	phone := randomPhone()

	if _, err := e.client.SendLoginCode(ctx, &fpv1.SendLoginCodeRequest{Phone: phone}); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	code := e.sms.LastParam("code")
	if code == "" {
		t.Fatal("假短信供应商没有收到验证码")
	}

	signup, err := e.client.Login(ctx, &fpv1.LoginRequest{
		ConnectorType: connector.TypeSMSCode,
		Credentials:   map[string]string{"phone": phone, "code": code},
	})
	if err != nil {
		t.Fatalf("Login(sms_code): %v", err)
	}

	userID, err := uuid.Parse(signup.GetUser().GetId())
	if err != nil {
		t.Fatalf("解析 userId %q: %v", signup.GetUser().GetId(), err)
	}
	const password = "hunter2hunter2"
	if err := e.users.SetPassword(context.Background(), userID, password); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	login, err := e.client.Login(ctx, &fpv1.LoginRequest{
		ConnectorType: connector.TypePassword,
		Credentials:   map[string]string{"account": phone, "password": password},
	})
	if err != nil {
		t.Fatalf("Login(password): %v", err)
	}
	return login.GetToken()
}
