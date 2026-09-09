// phase2_env_test.go 装配第二阶段集成测试用的完整环境：一个（或两个）真实
// 监听端口上的 fp + 连上去的真实 SDK 客户端。风格上参照本包已有的
// env_test.go（phase1），但底层网络换成真实回环监听而非 httptest——
// 第二阶段测的是 gRPC + SDK 这条链路本身，httptest 只覆盖了 HTTP 管理面。
package integration_test

import (
	"context"
	"fmt"
	"net"
	"reflect"
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
	authz     *service.AuthzService
	// configPub 与 revokePub 同一装配方式：喂给 startServer 里的
	// grpcapi.Deps.ConfigPub，让 grpcapi.Server 内部的 configHub 有一个
	// 真实可用的 Redis 订阅源。
	configPub *store.ConfigPublisher
	// configs 是配置中心的 service 层入口（Task 15）。phase2Env 是纯 gRPC
	// 环境、没有 HTTP 服务端——Task 8 的 httpapi 测试已经覆盖了
	// HTTP→service 这一段，这里的测试直接调它的 Save，验证 service→
	// Redis→gRPC→SDK 剩下这一段完整链路。
	configs *service.ConfigService
	// imCreds 供 IM 网关身份的测试生成凭据。
	imCreds *service.IMCredentialService
}

func wireServices(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client) phase2Services {
	t.Helper()

	users := service.NewUserService(pool)
	logs := service.NewLoginLogService(pool)
	codes := notify.NewCodeService(rdb)

	epochs := store.NewEpochStore(rdb)
	revokePub := store.NewRevokePublisher(rdb)
	configPub := store.NewConfigPublisher(rdb)
	sessions := service.NewSessionService(store.NewSessionStore(rdb), revokePub, epochs)

	registry := connector.NewRegistry()
	if err := registry.Register(connector.NewPassword(users)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	if err := registry.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}
	apps := service.NewApplicationService(pool, registry, service.WithIMConfigPublisher(configPub))
	imCreds := service.NewIMCredentialService(pool)

	sms := notify.NewFakeProvider(notify.ChannelSMS, "fake")
	// []RateRule{} 是显式关闭频率限制，仅用于测试——生产装配千万别照抄，
	// 理由与 env_test.go（phase1）newEnv 里的同一行注释一致。
	sender := notify.NewSender(pool, store.NewRateLimiter(rdb), []notify.RateRule{})
	sender.AddProvider(sms)

	accounts := service.NewAccountService(users, sessions, epochs, logs)
	authz := service.NewAuthzService(pool)
	auth := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users, Sessions: sessions, Logs: logs,
		Registry: registry, Notifier: sender, Codes: codes, Authz: authz,
	})

	configs := service.NewConfigService(pool, configPub)

	return phase2Services{
		apps: apps, users: users, sessions: sessions,
		accounts: accounts, auth: auth, sms: sms, revokePub: revokePub, authz: authz,
		configPub: configPub, configs: configs, imCreds: imCreds,
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

	srv := grpcapi.New(grpcapi.Deps{
		Auth: e.auth, Apps: e.apps, Pub: e.revokePub, Authz: e.authz,
		Configs: e.configs, ConfigPub: e.configPub, IMCreds: e.imCreds,
	})
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

// saveConfig 是 service.ConfigService.Save 的薄封装，供测试按 YAML 字面量
// 直接写配置：yamlText 就是管理端会提交的那份原文，形如
// "fee_rate: 0.02\n"。
//
// 直接调 service 层、不经 HTTP：Task 8 的 httpapi 测试已经覆盖了
// HTTP→service 这一段，本包（phase2Env）本来就没有 HTTP 服务端，这里要
// 验证的是 service→Redis→gRPC→SDK 这条链路，从 service 层入口开始即可，
// 见 phase2Services.configs 字段的注释。
func (e *phase2Env) saveConfig(t *testing.T, typ, yamlText string, push bool) int64 {
	t.Helper()
	seq, err := e.configs.Save(context.Background(), e.app.ID, typ, yamlText, push)
	if err != nil {
		t.Fatalf("保存配置（分区 %s）: %v", typ, err)
	}
	return seq
}

// login 走一次完整的短信验证码登录（发码 → 取码 → 登录），全部经由 e.sdk
// 这个已经连上本实例的客户端——这样断言的是 SDK 到 service 层的完整链路，
// 不是绕开 SDK 直接调用 service。
func (e *phase2Env) login(t *testing.T) (token string, userID uuid.UUID, sessionID string) {
	t.Helper()
	return e.loginWithPhone(t, randomPhase2Phone())
}

// loginWithPhone 用指定手机号登录。同一个手机号多次登录得到的是**同一个
// 用户**，供需要"改完角色再登一次"的测试使用。
func (e *phase2Env) loginWithPhone(t *testing.T, phone string) (token string, userID uuid.UUID, sessionID string) {
	t.Helper()
	ctx := context.Background()

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

// waitStable 轮询 get()，直到它连续 settle 这么长时间都没有变化（用
// reflect.DeepEqual 比较相邻两次取值），返回落定后的值；超过 timeout
// 仍未落定则以 t.Fatal 失败。
//
// 用途：某些回调（尤其是 OnError）在"决定要不要把新快照换上去"之前就已经
// 同步触发——回调 fire 到测试 goroutine 从 channel 上解除阻塞之间只保证
// "回调已经被调用过"这一件事，并不保证同一个重载 goroutine 后续几行
// （比如 fpsdk.Binding[T].applySnapshot 里 raise(err) 之后还有的
// missing 检查与 DeepEqual 比较）已经跑完。直接在 <-ch 之后立刻读快照，
// 断言与"重载 goroutine 到底有没有再多做一步把快照换掉"之间就有一段没有
// 任何同步关系的真空期——对一个正确的实现（错误分支里 raise 之后立即
// return）这段真空期是空的，但对一个假想的错误实现（raise 之后还会继续
// 换指针）它就是真实存在的窗口，会让测试的抓获率从确定性的 100% 掉到
// 一个不稳定的分数。轮询到值不再变化，就是把这段真空期真正等完，而不是
// 猜一个固定时长。
func waitStable[T any](t *testing.T, timeout, settle time.Duration, get func() T) T {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := get()
	stableSince := time.Now()
	for {
		if time.Since(stableSince) >= settle {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待快照落定超时（%v 内值持续变化）", timeout)
		}
		time.Sleep(10 * time.Millisecond)
		cur := get()
		if !reflect.DeepEqual(cur, last) {
			last = cur
			stableSince = time.Now()
		}
	}
}

// pubsubClientLinePattern 从一行 CLIENT LIST 输出里取 id=<数字> 与
// db=<数字> 两个字段。CLIENT LIST 的字段顺序固定（id 在前、db 在
// age/idle/flags 之后），一条正则按这个顺序两段匹配即可，不需要分别编译
// 两条正则再各自扫一遍。
var pubsubClientLinePattern = regexp.MustCompile(`\bid=(\d+)\b.*\bdb=(\d+)\b`)

// killPubSubConnection 找到 rdb 所在库上全部 TYPE pubsub 的连接并逐一杀掉，
// 模拟一次 Redis 订阅抖动（故障转移、网络毛刺）——go-redis 会静默重连并
// 重发 SUBSCRIBE。
//
// 按 db 过滤、且杀掉本库内的全部（而不是曾经的"确认此刻恰好只有一条"）：
//
//   - **按 db 过滤**：CLIENT LIST TYPE pubsub 是 Redis 服务器级命令，
//     不区分调用方连的是哪个逻辑库，共享同一个 Redis 实例的其他进程
//     （本机其他测试、其他服务）留下的 pubsub 连接会混进结果里。用
//     rdb.Options().DB 拿到本测试自己连的库号，按 db=<该库号> 过滤掉
//     跨库的噪音，不用指望"环境里没有别的进程"这个测不了、也不该测的
//     前提。
//
//   - **杀本库内的全部，不再要求恰好一条**：Task 7（配置中心推送）之后，
//     一个 fp 实例结构性地同时持有两条常驻订阅——RevokeHub（撤销）与
//     ConfigHub（配置），且都落在同一个逻辑库里。这意味着哪怕上面的 db
//     过滤把跨库/跨进程的噪音全部排除掉，本库里"恰好一条"这个断言也
//     永远不可能再成立：稳定就是两条。继续要求恰好一条，会让这个
//     helper 在任何环境下都无法用完这条轮询窗口，只会超时。
//
//     全杀而不是挑一条杀，是因为这个 helper 本来只关心撤销那一条（本
//     函数存在的唯一理由是给 TestRedisSubscriptionBlipDoesNotSilentlyLoseRevocations
//     制造"撤销订阅断线重连"），但两条连接对 CLIENT LIST 来说彼此没有
//     任何可靠区分特征（都是同一个 fp 进程发起、同一个 db、同一种
//     客户端库），专门去猜哪条是撤销、哪条是配置既做不到、也没必要：
//     两条一起断开，RevokeHub 那条照样会重连并触发 Gap→Purge，正是该
//     测试要验证的效果；ConfigHub 那条重连触发的 Gap 是良性的——它只是
//     让 SDK 多做一次"重拉全部配置"，该测试没有任何断言绑定配置状态。
//     这个写法还有一个好处：以后如果再加第三条常驻订阅，这个 helper
//     不需要再改。
//
// 轮询等待"本库至少一条"，而不是假设第一次查询就已经如此：前一个测试
// 收尾时，cancelRun 只是发出取消信号，并不等 RevokeHub.Run / ConfigHub.Run
// 内部的 goroutine 真正退出、真正关掉它们各自的 Redis 订阅连接（这条不
// 等待的取舍与 grpcEnv 一致，见 stopFp 的注释）；轮询把这段极短的收尾
// 尾巴吸收掉，避免在上一个测试的订阅连接还没来得及关闭、本测试自己的
// 连接还没建立完成之间的窗口里，把瞬时的 0 条误判为异常而失败。
//
// 不改 store.RevokePublisher / store.ConfigPublisher 去给连接打名字
// （CLIENT SETNAME）以便精确区分：这是一个测试 helper 的定位需求，为它
// 去改生产代码投入产出不成比例，按 db 过滤 + 全杀已经能确定性地达到
// 这个 helper 的目的。
func killPubSubConnection(t *testing.T, rdb *redis.Client) {
	t.Helper()
	ctx := context.Background()
	wantDB := fmt.Sprintf("%d", rdb.Options().DB)

	var ids []string
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := rdb.Do(ctx, "CLIENT", "LIST", "TYPE", "pubsub").Text()
		if err != nil {
			t.Fatalf("CLIENT LIST TYPE pubsub: %v", err)
		}
		ids = ids[:0]
		for _, line := range nonEmptyLines(raw) {
			m := pubsubClientLinePattern.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("CLIENT LIST 输出解析不出 id/db: %q", line)
			}
			if m[2] != wantDB {
				continue // 跨库/跨进程的噪音连接，不是本测试自己的订阅。
			}
			ids = append(ids, m[1])
		}
		if len(ids) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待本库（db=%s）至少一条 pubsub 连接超时，"+
				"当前 TYPE pubsub 全量输出：%q", wantDB, raw)
		}
		time.Sleep(20 * time.Millisecond)
	}

	for _, id := range ids {
		if err := rdb.ClientKillByFilter(ctx, "ID", id).Err(); err != nil {
			t.Fatalf("CLIENT KILL ID %s: %v", id, err)
		}
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
