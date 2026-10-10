package service_test

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func newAdminService(t *testing.T) *service.AdminService {
	t.Helper()
	return service.NewAdminService(testsupport.NewTestDB(t), testsupport.NewTestRedis(t))
}

func TestEnsureBootstrapCreatesAdminOnce(t *testing.T) {
	svc := newAdminService(t)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("首次 EnsureBootstrap: %v", err)
	}
	// 重复调用不应报错，也不应改写密码。
	if err := svc.EnsureBootstrap(ctx, "admin", "another-password"); err != nil {
		t.Fatalf("重复 EnsureBootstrap: %v", err)
	}

	if _, err := svc.Login(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("原密码应仍然有效: %v", err)
	}
	if _, err := svc.Login(ctx, "admin", "another-password"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("新密码不应生效, err = %v", err)
	}
}

func TestEnsureBootstrapSkipsWhenEmpty(t *testing.T) {
	svc := newAdminService(t)
	if err := svc.EnsureBootstrap(context.Background(), "", ""); err != nil {
		t.Fatalf("未配置引导管理员时应静默跳过: %v", err)
	}
}

// bcrypt 的 72 字节上限对引导管理员密码同样适用；必须在启动路径上给出
// 清晰的 400，而不是把 bcrypt 的原始报错一路捅到进程退出。
func TestEnsureBootstrapRejectsTooLongPassword(t *testing.T) {
	svc := newAdminService(t)
	long := strings.Repeat("a", 73)
	if err := svc.EnsureBootstrap(context.Background(), "admin", long); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("73 字节 err = %v, want ErrInvalidArgument", err)
	}
}

func TestLoginAndAuthenticate(t *testing.T) {
	svc := newAdminService(t)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	token, err := svc.Login(ctx, "admin", "secret123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if len(token) < 32 {
		t.Fatalf("token 长度 = %d, 太短", len(token))
	}

	id, username, err := svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if username != "admin" {
		t.Fatalf("username = %q, want admin", username)
	}
	// 必须比 uuid.Nil，不能比 id.String() == ""：零值 uuid.UUID 会被
	// 格式化成一串全零（00000000-0000-...），永远不是空串，那个断言恒为真。
	if id == uuid.Nil {
		t.Fatal("adminID 为空")
	}
}

func TestAuthenticateRejectsUnknownToken(t *testing.T) {
	svc := newAdminService(t)
	_, _, err := svc.Authenticate(context.Background(), "not-a-real-token")
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestLogoutInvalidatesToken(t *testing.T) {
	svc := newAdminService(t)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	token, err := svc.Login(ctx, "admin", "secret123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, _, err := svc.Authenticate(ctx, token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("登出后 err = %v, want ErrUnauthorized", err)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	svc := newAdminService(t)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	if _, err := svc.Login(ctx, "admin", "wrong"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
	if _, err := svc.Login(ctx, "nobody", "secret123456"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("未知用户名 err = %v, want ErrInvalidCredential", err)
	}
}

// 先验凭据、后报状态（见 domain.CodeAccountFrozen 的说明）：不知道密码的人，不该借
// "已停用"这个不同的错误探出用户名存在。
func TestLoginReportsDisabledOnlyAfterPasswordIsVerified(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	if _, err := e.pool.Exec(ctx, `UPDATE admin SET status = 'DISABLED'`); err != nil {
		t.Fatalf("停用管理员: %v", err)
	}

	_, err := e.svc.Login(ctx, "admin", "wrong")
	if !errors.Is(err, domain.ErrInvalidCredential) || adminErrCode(err) != domain.CodeAdminCredentialInvalid {
		t.Fatalf("停用账号 + 错误密码 err = %v (code %q), want ErrInvalidCredential / ADMIN_CREDENTIAL_INVALID", err, adminErrCode(err))
	}
	_, err = e.svc.Login(ctx, "admin", "secret123456")
	if !errors.Is(err, domain.ErrForbidden) || adminErrCode(err) != domain.CodeAdminDisabled {
		t.Fatalf("停用账号 + 正确密码 err = %v (code %q), want ErrForbidden / ADMIN_DISABLED", err, adminErrCode(err))
	}
}

// 登录失败的两条路径都要付出一次 bcrypt 的代价，否则响应时间会泄露账号信息：用户名不存在时
// 若直接返回，几十毫秒的时间差就能问出"这个用户名存不存在"；已停用的账号若在比对密码之前
// 就返回，则能问出"它存在、且已停用"。
//
// 不写死毫秒数：Login 里除了 bcrypt 还有 Redis 与 Postgres 各一次往返，绝对值随机器、CPU 与
// 网络漂移，这台机器上调出来的阈值换一台就可能误报或失效。改为当场量一次同 cost 的 bcrypt
// 比对当参照，只要求登录耗时"不低于它的 0.6 倍"；只设下限、不设上限。去掉比对后登录只剩那
// 两次往返，远低于一次 bcrypt。
func TestLoginFailurePathsPayBcryptCost(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	if _, err := e.pool.Exec(ctx, `UPDATE admin SET status = 'DISABLED'`); err != nil {
		t.Fatalf("停用管理员: %v", err)
	}
	// 参照用 bcrypt.DefaultCost，要与 service 的 bcryptCost 一致，否则 0.6 倍的下限没有意义。
	// 两个常量同为 10；这里核对库里实际存的哈希，以后谁改了其中一个，这条会红而不是悄悄失准。
	var stored string
	if err := e.pool.QueryRow(ctx, `SELECT password_hash FROM admin WHERE username = 'admin'`).Scan(&stored); err != nil {
		t.Fatalf("读管理员密码哈希: %v", err)
	}
	if cost, err := bcrypt.Cost([]byte(stored)); err != nil || cost != bcrypt.DefaultCost {
		t.Fatalf("service 存的哈希 cost = %d (err %v)，参照用的是 bcrypt.DefaultCost = %d", cost, err, bcrypt.DefaultCost)
	}
	minElapsed := bcryptReference(t) * 6 / 10

	cases := []struct{ name, username string }{
		{"用户名不存在", "nobody"},
		{"已停用账号 + 错误密码", "admin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			_, err := e.svc.Login(ctx, c.username, "definitely-wrong-password")
			elapsed := time.Since(start)

			if !errors.Is(err, domain.ErrInvalidCredential) {
				t.Fatalf("err = %v, want ErrInvalidCredential", err)
			}
			if elapsed < minElapsed {
				t.Errorf("耗时 %v < %v（一次 bcrypt 比对的 0.6 倍），说明跳过了 bcrypt，响应时间会泄露账号信息", elapsed, minElapsed)
			}
		})
	}
}

// bcryptReference 当场量一次 bcrypt 比对要多久，作为计时断言的参照。取三次里最小的：
// 调度或 GC 的抖动只会把单次量大，最小值才最接近真实开销；参照虚高会让下限断言误报。
func bcryptReference(t *testing.T) time.Duration {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("x"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	var best time.Duration
	for i := range 3 {
		start := time.Now()
		_ = bcrypt.CompareHashAndPassword(hash, []byte("definitely-wrong-password"))
		if d := time.Since(start); i == 0 || d < best {
			best = d
		}
	}
	return best
}

// 登录名里的 NUL 或非法 UTF-8 原样进 SQL 会让 PG 报 22021：未登录就能触发 500 与一条 error 日志。
// 这样的名字存不进库（ChangeAccount 拒绝），按"用户不存在"处理。
func TestLoginTreatsUnstorableUsernameAsUnknown(t *testing.T) {
	svc := newAdminService(t)
	cases := []struct{ name, username string }{
		{"含 NUL", "a\x00b"},
		{"含非法 UTF-8", "a\xffb"},
	}
	for _, c := range cases {
		_, err := svc.Login(context.Background(), c.username, "x")
		if !errors.Is(err, domain.ErrInvalidCredential) || adminErrCode(err) != domain.CodeAdminCredentialInvalid {
			t.Errorf("%s: err = %v (code %q), want ErrInvalidCredential / ADMIN_CREDENTIAL_INVALID", c.name, err, adminErrCode(err))
		}
	}
}

type adminTestEnv struct {
	svc  *service.AdminService
	pool *pgxpool.Pool
	rdb  *redis.Client
}

// 只调一次 NewTestDB / NewTestRedis：它们每次调用都会清库，二次调用会冲掉前面造的数据。
func newAdminTestEnv(t *testing.T) adminTestEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	return adminTestEnv{svc: service.NewAdminService(pool, rdb), pool: pool, rdb: rdb}
}

func (e adminTestEnv) bootstrap(t *testing.T, username, password string) {
	t.Helper()
	if err := e.svc.EnsureBootstrap(context.Background(), username, password); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
}

// loginAs 登录并返回 token 与管理员 ID。
func (e adminTestEnv) loginAs(t *testing.T, username, password string) (string, uuid.UUID) {
	t.Helper()
	token, err := e.svc.Login(context.Background(), username, password)
	if err != nil {
		t.Fatalf("Login(%q): %v", username, err)
	}
	id, _, err := e.svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	return token, id
}

func (e adminTestEnv) adminRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM admin`).Scan(&n); err != nil {
		t.Fatalf("统计 admin 行数: %v", err)
	}
	return n
}

// insertAdminRow 直接插一行管理员并返回它的 ID，用来造旧版本留下的多行库：EnsureBootstrap
// 现在只在表为空时才建，造不出来。createdAt 显式给出，"最早一行"才不取决于插入先后。
func (e adminTestEnv) insertAdminRow(t *testing.T, username, password, status string, createdAt time.Time) uuid.UUID {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	var id uuid.UUID
	err = e.pool.QueryRow(context.Background(), `
		INSERT INTO admin (username, password_hash, display_name, status, created_at)
		VALUES ($1, $2, $1, $3, $4) RETURNING id`, username, string(hash), status, createdAt).Scan(&id)
	if err != nil {
		t.Fatalf("插入管理员 %q: %v", username, err)
	}
	return id
}

// insertLegacyAdmins 造旧版本留下的两行库，返回较晚那一行（admin/admin）的 ID：最早的 root
// 是 reset-password 保留的行，较晚的 admin/admin 会被它停用（哈希不动）。
func (e adminTestEnv) insertLegacyAdmins(t *testing.T) uuid.UUID {
	t.Helper()
	now := time.Now()
	e.insertAdminRow(t, "root", "root-password", "ACTIVE", now.Add(-48*time.Hour))
	return e.insertAdminRow(t, "admin", "admin", "ACTIVE", now.Add(-24*time.Hour))
}

func adminErrCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// epoch 读管理端会话纪元；键还不存在时为 0（与服务端一致）。
func (e adminTestEnv) epoch(t *testing.T) int64 {
	t.Helper()
	v, err := e.rdb.Get(context.Background(), "fp:admin:epoch").Int64()
	if errors.Is(err, redis.Nil) {
		return 0
	}
	if err != nil {
		t.Fatalf("读纪元: %v", err)
	}
	return v
}

// tokenKeys 数 Redis 里现存的管理端会话。
func (e adminTestEnv) tokenKeys(t *testing.T) int {
	t.Helper()
	keys, err := e.rdb.Keys(context.Background(), "fp:admin:tok:*").Result()
	if err != nil {
		t.Fatalf("列管理端会话: %v", err)
	}
	return len(keys)
}

// waitUntilBlockedBy 轮询到有语句卡在 pid 持有的锁上。拿锁等待当同步信号，
// 比 sleep 凑时序可靠：它成立时，被卡住的那条语句之前的步骤必已全部完成。
func waitUntilBlockedBy(t *testing.T, pool *pgxpool.Pool, pid int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND $1 = ANY(pg_blocking_pids(pid))`, pid).Scan(&n)
		if err != nil {
			t.Fatalf("查询锁等待: %v", err)
		}
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("等待超时：没有语句卡在持锁事务的锁上")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// redisCmdHook 在每条单命令发出之前调一次回调，拨号与管道原样放行。
// 回调自己判断是不是要拦的那条命令，并负责只触发一次。
type redisCmdHook struct {
	before func(ctx context.Context, cmd redis.Cmder)
}

func (h *redisCmdHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *redisCmdHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.before != nil {
			h.before(ctx, cmd)
		}
		return next(ctx, cmd)
	}
}

func (h *redisCmdHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// isEpochCmd 判断 cmd 是不是对纪元键执行的 name 命令（name 为小写，如 "get"、"incr"）。
func isEpochCmd(cmd redis.Cmder, name string) bool {
	args := cmd.Args()
	return cmd.Name() == name && len(args) == 2 && args[1] == "fp:admin:epoch"
}

// hookedRedis 返回一个连着同一个 Redis、但挂了 h 的新客户端，测试结束时关闭。
// 不能直接对 testsupport.NewTestRedis 返回的客户端 AddHook：它是整个测试进程共用的
// 单例，go-redis 又没有摘除 hook 的办法，hook 会漏进之后所有的用例。也不从它的
// Options() 复制：选项里带着它的推送处理器，新客户端会与它共用。
func hookedRedis(t *testing.T, h redis.Hook) *redis.Client {
	t.Helper()
	c, err := store.OpenRedis(context.Background(), os.Getenv("FP_TEST_REDIS_URL"))
	if err != nil {
		t.Fatalf("连接测试 Redis: %v", err)
	}
	c.AddHook(h)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// 登录名可改之后，"用户名冲突"不再能代表"管理员已存在"：改名后重启，
// admin 这个名字空出来，旧实现会再插入一个已知密码的 admin/admin。
func TestEnsureBootstrapDoesNotRecreateDefaultAdminAfterRename(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "admin")
	_, id := e.loginAs(t, "admin", "admin")
	if _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "root", OldPassword: "admin"}); err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}

	e.bootstrap(t, "admin", "admin") // 模拟重启
	if n := e.adminRows(t); n != 1 {
		t.Fatalf("admin 行数 = %d, want 1", n)
	}
	if _, err := e.svc.Login(ctx, "admin", "admin"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("改名后 admin/admin 不该再能登录, err = %v", err)
	}
}

func TestAuthenticateRejectsLegacyTokenWithoutEpoch(t *testing.T) {
	e := newAdminTestEnv(t)
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")
	// 升级前签发的 token payload 只有 id|username。
	if err := e.rdb.Set(context.Background(), "fp:admin:tok:legacy", id.String()+"|admin", time.Hour).Err(); err != nil {
		t.Fatalf("写旧格式 token: %v", err)
	}
	if _, _, err := e.svc.Authenticate(context.Background(), "legacy"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestChangeAccountRejectsWrongOldPassword(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	_, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{
		Username: "admin", OldPassword: "nope", NewPassword: "new-password",
	})
	if !errors.Is(err, domain.ErrInvalidArgument) || adminErrCode(err) != domain.CodeAdminOldPasswordWrong {
		t.Fatalf("err = %v (code %q), want ErrInvalidArgument / ADMIN_OLD_PASSWORD_WRONG", err, adminErrCode(err))
	}
	if _, err := e.svc.Login(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("失败的修改不该动旧密码: %v", err)
	}
}

// 改账号成功后全部会话作废，包括发起修改的这一个：不为调用方换发 token，得用新凭据重新登录。
func TestChangeAccountChangesPasswordAndRevokesAllSessions(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	other, id := e.loginAs(t, "admin", "secret123456")
	current, _ := e.loginAs(t, "admin", "secret123456")

	username, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{
		Username: "  boss ", OldPassword: "secret123456", NewPassword: "brand-new-pass",
	})
	if err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	if username != "boss" {
		t.Fatalf("username = %q, want boss（应去掉首尾空白）", username)
	}

	for name, tok := range map[string]string{"其他会话": other, "发起修改的会话": current} {
		if _, _, err := e.svc.Authenticate(ctx, tok); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("%s 应已失效, err = %v", name, err)
		}
	}
	// 要在下面重新登录之前数：此刻只该剩改之前登录的那两个，不能悄悄为调用方换发一个。
	if n := e.tokenKeys(t); n != 2 {
		t.Errorf("会话数 = %d, want 2（改之前登录的两个，不该换发新会话）", n)
	}
	if _, err := e.svc.Login(ctx, "boss", "brand-new-pass"); err != nil {
		t.Fatalf("新账号密码应能登录: %v", err)
	}
	for _, c := range [][2]string{{"boss", "secret123456"}, {"admin", "brand-new-pass"}} {
		if _, err := e.svc.Login(ctx, c[0], c[1]); !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("Login(%q, %q) err = %v, want ErrInvalidCredential", c[0], c[1], err)
		}
	}
}

func TestChangeAccountWithoutNewPasswordOnlyRenames(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	if _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "root", OldPassword: "secret123456"}); err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	if _, err := e.svc.Login(ctx, "root", "secret123456"); err != nil {
		t.Fatalf("只改登录名时密码应保持不变: %v", err)
	}
}

func TestChangeAccountValidatesInput(t *testing.T) {
	e := newAdminTestEnv(t)
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	cases := []struct {
		name string
		in   service.ChangeAccountInput
	}{
		{"登录名为空白", service.ChangeAccountInput{Username: "  ", OldPassword: "secret123456"}},
		{"登录名超过 64 字符", service.ChangeAccountInput{Username: strings.Repeat("a", 65), OldPassword: "secret123456"}},
		{"新密码超过 72 字节", service.ChangeAccountInput{Username: "admin", OldPassword: "secret123456", NewPassword: strings.Repeat("a", 73)}},
		// NUL 与非法 UTF-8 会让 PG 报 22021（500）；换行、零宽空格是人打不出来的字符，会让管理员自己登不进去。
		{"登录名含 NUL", service.ChangeAccountInput{Username: "a\x00b", OldPassword: "secret123456"}},
		{"登录名含非法 UTF-8", service.ChangeAccountInput{Username: "a\xffb", OldPassword: "secret123456"}},
		{"登录名含换行", service.ChangeAccountInput{Username: "a\nb", OldPassword: "secret123456"}},
		{"登录名含零宽空格", service.ChangeAccountInput{Username: "a\u200bb", OldPassword: "secret123456"}},
		// 行/段分隔符（Zl、Zp）与私用区（Co）既不是控制符也不是格式符，只排除 Cc 与 Cf 的谓词会放过它们。
		{"登录名含行分隔符", service.ChangeAccountInput{Username: "a\u2028b", OldPassword: "secret123456"}},
		{"登录名含段分隔符", service.ChangeAccountInput{Username: "a\u2029b", OldPassword: "secret123456"}},
		{"登录名含私用区字符", service.ChangeAccountInput{Username: "a\ue000b", OldPassword: "secret123456"}},
	}
	for _, c := range cases {
		if _, err := e.svc.ChangeAccount(context.Background(), id, c.in); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want ErrInvalidArgument", c.name, err)
		}
	}
}

// 控制字符的校验不能误伤正常的名字：中文、名字内部的空格、组合附加符号都是人能打出来的。
// U+2EBF0（CJK 扩展 I 的首字）是合法字符，但 Go 的 Unicode 表（15.0）还不认识它：按"未分配码位"
// 拒绝，就会把凡是晚于 Go 表版本才收录的新字都误拒掉。
func TestChangeAccountAcceptsOrdinaryUsernames(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	for _, name := range []string{"管理员", "ops team", "e\u0301", "a-b_c.d@e", "\U0002EBF0"} {
		if got, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: name, OldPassword: "secret123456"}); err != nil || got != name {
			t.Errorf("ChangeAccount(%q) = (%q, %v)，应当照常通过", name, got, err)
		}
	}
}

// 会话 payload 是 id|username|epoch，登录名里的 | 不能把它切歪。改账号不再签发会话，
// 会话由用新名字重新登录得到。
func TestChangeAccountAllowsPipeInUsername(t *testing.T) {
	e := newAdminTestEnv(t)
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	if _, err := e.svc.ChangeAccount(context.Background(), id, service.ChangeAccountInput{Username: "a|b", OldPassword: "secret123456"}); err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	token, _ := e.loginAs(t, "a|b", "secret123456")
	if _, name, err := e.svc.Authenticate(context.Background(), token); err != nil || name != "a|b" {
		t.Fatalf("name = %q, err = %v, want a|b", name, err)
	}
}

// 改成已被另一行占用的名字：唯一约束冲突要翻译成 400 而不是 500；失败的修改既不动纪元，
// 也不签发会话，原账号保持不变。
func TestChangeAccountRejectsUsernameTakenByAnotherRow(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")
	e.insertAdminRow(t, "other", "other-password", "ACTIVE", time.Now())
	epochBefore := e.epoch(t)

	_, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "other", OldPassword: "secret123456"})
	if !errors.Is(err, domain.ErrInvalidArgument) || adminErrCode(err) != domain.CodeInvalidArgument || err.Error() != "登录名已被占用" {
		t.Fatalf("err = %v (code %q), want ErrInvalidArgument / INVALID_ARGUMENT / 登录名已被占用", err, adminErrCode(err))
	}
	if got := e.epoch(t); got != epochBefore {
		t.Errorf("纪元 %d -> %d，失败的修改不该作废会话", epochBefore, got)
	}
	if n := e.tokenKeys(t); n != 1 {
		t.Errorf("会话数 = %d, want 1（只有登录时签发的那个）", n)
	}
	if _, err := e.svc.Login(ctx, "admin", "secret123456"); err != nil {
		t.Errorf("改名失败后原账号应保持不变: %v", err)
	}
}

// 会话指向的管理员行已不存在（库被清、行被删）：按登录过期处理，让前端回登录页，不是 500。
func TestChangeAccountForUnknownAdminIsSessionInvalid(t *testing.T) {
	e := newAdminTestEnv(t)
	_, err := e.svc.ChangeAccount(context.Background(), uuid.New(), service.ChangeAccountInput{Username: "boss", OldPassword: "secret123456"})
	if !errors.Is(err, domain.ErrUnauthorized) || adminErrCode(err) != domain.CodeAdminSessionInvalid {
		t.Fatalf("err = %v (code %q), want ErrUnauthorized / ADMIN_SESSION_INVALID", err, adminErrCode(err))
	}
}

// 校验旧密码到写库之间隔着一次 bcrypt，其间另一次改密或重置若已提交，无条件的
// UPDATE 会用开头读到的旧哈希把它盖回去：那次改密或重置白做了。
//
// 时序用第二个事务的行锁钉死，不靠 sleep：先锁住管理员行，ChangeAccount 开头的
// 普通 SELECT 不受影响、读到旧哈希，随后它的 UPDATE 确定地卡在锁上；确认卡住后
// 在锁内改密并提交。READ COMMITTED 下被唤醒的 UPDATE 会重新求值 WHERE，
// 条件里带着开头读到的哈希，才会一行都不命中。
func TestChangeAccountDoesNotOverwriteConcurrentPasswordChange(t *testing.T) {
	const (
		oldPassword   = "secret123456"
		otherPassword = "written-by-other-tx"
	)
	cases := []struct {
		name string
		in   service.ChangeAccountInput
	}{
		{"改登录名与密码", service.ChangeAccountInput{Username: "boss", OldPassword: oldPassword, NewPassword: "from-change-account"}},
		// 只改登录名时 UPDATE 写回的是开头读到的旧哈希，盖掉的恰是别人刚写的新哈希。
		{"只改登录名", service.ChangeAccountInput{Username: "boss", OldPassword: oldPassword}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAdminTestEnv(t)
			ctx := context.Background()
			e.bootstrap(t, "admin", oldPassword)
			_, id := e.loginAs(t, "admin", oldPassword)
			epochBefore := e.epoch(t)
			otherHash, err := bcrypt.GenerateFromPassword([]byte(otherPassword), bcrypt.MinCost)
			if err != nil {
				t.Fatalf("bcrypt: %v", err)
			}

			holder, err := e.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("开启持锁事务: %v", err)
			}
			var wg sync.WaitGroup
			defer func() {
				// 中途失败也要放锁，并等卡住的 UPDATE 走完，免得它的写入落进下一个用例。
				_ = holder.Rollback(ctx)
				wg.Wait()
			}()
			var holderPID int32
			if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
				t.Fatalf("取持锁连接的 pid: %v", err)
			}
			if _, err := holder.Exec(ctx, `SELECT 1 FROM admin WHERE id = $1 FOR UPDATE`, id); err != nil {
				t.Fatalf("锁住管理员行: %v", err)
			}

			var changeErr error
			wg.Add(1)
			go func() {
				defer wg.Done()
				callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				_, changeErr = e.svc.ChangeAccount(callCtx, id, c.in)
			}()
			waitUntilBlockedBy(t, e.pool, holderPID)

			if _, err := holder.Exec(ctx, `UPDATE admin SET password_hash = $2 WHERE id = $1`, id, string(otherHash)); err != nil {
				t.Fatalf("另一个事务改密: %v", err)
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatalf("提交另一个事务: %v", err)
			}
			wg.Wait()

			if !errors.Is(changeErr, domain.ErrInvalidArgument) || adminErrCode(changeErr) != domain.CodeAdminOldPasswordWrong {
				t.Errorf("err = %v (code %q), want ErrInvalidArgument / ADMIN_OLD_PASSWORD_WRONG", changeErr, adminErrCode(changeErr))
			}
			// 失败的修改既不能作废会话，也不能签发新会话。
			if got := e.epoch(t); got != epochBefore {
				t.Errorf("纪元 %d -> %d，失败的修改不该作废会话", epochBefore, got)
			}
			if n := e.tokenKeys(t); n != 1 {
				t.Errorf("会话数 = %d, want 1（只有登录时签发的那个）", n)
			}
			// 另一个事务写入的密码与原登录名原样保留。
			if _, err := e.svc.Login(ctx, "admin", otherPassword); err != nil {
				t.Errorf("另一个事务写入的密码应原样保留: %v", err)
			}
			// 必须用 ChangeAccount 想改成的登录名：保护被去掉时 UPDATE 会落库，新名字加新密码
			// 才登得进去。仍用 admin 的话，名字已被改走，保护在不在都是 ErrInvalidCredential。
			if c.in.NewPassword != "" {
				if _, err := e.svc.Login(ctx, c.in.Username, c.in.NewPassword); !errors.Is(err, domain.ErrInvalidCredential) {
					t.Errorf("ChangeAccount 想设的新账号不该生效, err = %v", err)
				}
			}
		})
	}
}

// 登录必须先读纪元、再查库校验凭据：读到新纪元，就保证随后的 SELECT 已看得见新哈希。
// 顺序写反（先校验、后读纪元）时，bcrypt 期间提交的重置拦不住这个在途登录：它拿旧密码
// 通过校验，再领一个新纪元的 token，而 Authenticate 每次访问都会顺延它。
//
// 用 Redis hook 把时序钉死：登录发出"读纪元"之前，先模拟一次并发重置——提交一个别的
// 密码哈希，再 INCR 纪元（与服务里"先提交、后 INCR"同序）——之后才放行这条 GET。
// 顺序正确时 GET 先于 SELECT，SELECT 读到新哈希，旧密码被拒；写反时校验早已用旧哈希
// 通过，随后的 GET 读到被 INCR 过的纪元，签出的 token 在重置之后依然有效。
//
// 另外数纪元 GET 的次数，被拒的与成功的登录都必须恰好一次：只有被 hook 扰动的头一次读
// 取"在栅栏之前"，"先探读一次、签发前再读一次"的写法能让上面的断言照样通过，而后一次读到
// 的恰是已被 INCR 过的值。被拒的那次走不到签发，所以成功登录要单独再跑一遍。
func TestLoginReadsEpochBeforeCheckingCredentials(t *testing.T) {
	const (
		oldPassword   = "secret123456"
		otherPassword = "reset-by-someone-else"
	)
	ctx := context.Background()
	pool := testsupport.NewTestDB(t)
	base := testsupport.NewTestRedis(t)
	otherHash, err := bcrypt.GenerateFromPassword([]byte(otherPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}

	var fired, reset atomic.Bool
	var epochGets atomic.Int32
	svc := service.NewAdminService(pool, hookedRedis(t, &redisCmdHook{
		before: func(ctx context.Context, cmd redis.Cmder) {
			if !isEpochCmd(cmd, "get") {
				return
			}
			epochGets.Add(1)
			if !fired.CompareAndSwap(false, true) {
				return
			}
			if _, err := pool.Exec(ctx, `UPDATE admin SET password_hash = $1`, string(otherHash)); err != nil {
				t.Errorf("模拟重置，写库: %v", err)
				return
			}
			// INCR 走没挂 hook 的 base，免得递归进同一个 hook。
			if err := base.Incr(ctx, "fp:admin:epoch").Err(); err != nil {
				t.Errorf("模拟重置，INCR: %v", err)
				return
			}
			reset.Store(true)
		},
	}))
	if err := svc.EnsureBootstrap(ctx, "admin", oldPassword); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	token, err := svc.Login(ctx, "admin", oldPassword)
	if !reset.Load() {
		t.Fatal("hook 没有执行模拟的重置：Login 没有读纪元？")
	}
	if n := epochGets.Load(); n != 1 {
		t.Fatalf("被拒的登录读了 %d 次纪元, want 1", n)
	}
	switch {
	case err == nil:
		// 若签出了 token，它必须已被那次重置作废。
		if _, _, aerr := svc.Authenticate(ctx, token); aerr == nil {
			t.Fatal("旧密码的登录在重置提交之后拿到了有效会话：纪元读得太晚")
		}
	case !errors.Is(err, domain.ErrInvalidCredential):
		t.Fatalf("Login err = %v, want ErrInvalidCredential（SELECT 应已读到新哈希）", err)
	}

	// 用重置后的密码再登录一次：这条路径会走到签发，同样只能读一次纪元。
	epochGets.Store(0)
	token, err = svc.Login(ctx, "admin", otherPassword)
	if err != nil {
		t.Fatalf("重置后的新密码应能登录: %v", err)
	}
	if n := epochGets.Load(); n != 1 {
		t.Fatalf("成功的登录读了 %d 次纪元, want 1", n)
	}
	if _, _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("重置之后签发的 token 应有效: %v", err)
	}
}

// ChangeAccount 与 ResetPassword 一样必须"先提交、后 INCR"，理由见
// TestResetPasswordCommitsBeforeBumpingEpoch；这里直接钉成功路径上的先后。
//
// hook 在纪元 INCR 发出之前，用另一条连接读管理员行：此刻改账号必须已经提交。要连密码一起改：
// 只改登录名时哈希不变，看不出提交与否。
func TestChangeAccountCommitsBeforeBumpingEpoch(t *testing.T) {
	const oldPassword = "secret123456"
	ctx := context.Background()
	pool := testsupport.NewTestDB(t)
	base := testsupport.NewTestRedis(t)

	var hashBeforeChange string
	var fired, committed atomic.Bool
	hook := &redisCmdHook{
		before: func(ctx context.Context, cmd redis.Cmder) {
			if !isEpochCmd(cmd, "incr") || !fired.CompareAndSwap(false, true) {
				return
			}
			var h string
			if err := pool.QueryRow(ctx, `SELECT password_hash FROM admin`).Scan(&h); err != nil {
				t.Errorf("INCR 之前读管理员行: %v", err)
				return
			}
			committed.Store(h != hashBeforeChange)
		},
	}
	e := adminTestEnv{svc: service.NewAdminService(pool, hookedRedis(t, hook)), pool: pool, rdb: base}
	e.bootstrap(t, "admin", oldPassword)
	_, id := e.loginAs(t, "admin", oldPassword)
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM admin`).Scan(&hashBeforeChange); err != nil {
		t.Fatalf("读改账号前的哈希: %v", err)
	}

	if _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{
		Username: "boss", OldPassword: oldPassword, NewPassword: "brand-new-pass",
	}); err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	if !fired.Load() {
		t.Fatal("hook 没有触发：ChangeAccount 没有 INCR 纪元？")
	}
	if !committed.Load() {
		t.Fatal("纪元 INCR 发出时改账号还没有提交：读到新纪元的登录仍可能读到旧哈希")
	}
}

// reset-password 对旧库里非保留的行只置 DISABLED、不动哈希。ChangeAccount 若只比哈希，就看不见
// 这次停用：旧库里的 admin/admin 靠一个在途的 PUT /me（在重置的 INCR 之前通过鉴权）照样改得动
// 已被停用的行，它的 INCR 还会作废运维刚签发的新会话。已停用的行与"行不存在"走同一条路：回会话
// 失效，不必先付一次 bcrypt，旧密码错也一样（所以下面要有"旧密码错误"这一条，它才钉得住
// "在校验旧密码之前"）。
func TestChangeAccountRefusesDisabledAdmin(t *testing.T) {
	cases := []struct{ name, oldPassword string }{
		{"旧密码正确", "admin"},
		{"旧密码错误", "not-the-password"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAdminTestEnv(t)
			ctx := context.Background()
			id := e.insertLegacyAdmins(t)
			e.loginAs(t, "admin", "admin") // 停用前就持有的会话
			if _, err := e.pool.Exec(ctx, `UPDATE admin SET status = 'DISABLED' WHERE id = $1`, id); err != nil {
				t.Fatalf("停用管理员: %v", err)
			}
			epochBefore := e.epoch(t)

			_, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "renamed", OldPassword: c.oldPassword})
			if !errors.Is(err, domain.ErrUnauthorized) || adminErrCode(err) != domain.CodeAdminSessionInvalid {
				t.Errorf("err = %v (code %q), want ErrUnauthorized / ADMIN_SESSION_INVALID", err, adminErrCode(err))
			}
			if got := e.epoch(t); got != epochBefore {
				t.Errorf("纪元 %d -> %d，被拒的修改不该作废会话", epochBefore, got)
			}
			if n := e.tokenKeys(t); n != 1 {
				t.Errorf("会话数 = %d, want 1（只有登录时签发的那个）", n)
			}
			var name string
			if err := e.pool.QueryRow(ctx, `SELECT username FROM admin WHERE id = $1`, id).Scan(&name); err != nil {
				t.Fatalf("读管理员行: %v", err)
			}
			if name != "admin" {
				t.Errorf("登录名 = %q，被停用的行不该被改名", name)
			}
		})
	}
}

// 停用发生在 ChangeAccount 读行之后、UPDATE 之前。UPDATE 若不带 status 条件，哈希没变就照样
// 命中：被停用的行被改名，随后的 INCR 还会作废运维刚签发的新会话。落空之后要再查一次才分得清
// 原因：行已停用是会话失效（401），不是旧密码不对（400）。
//
// 时序同 TestChangeAccountDoesNotOverwriteConcurrentPasswordChange，用行锁钉死：持锁事务在
// UPDATE 卡住之后停用该行并提交，被唤醒的 UPDATE 重新求值 WHERE。
func TestChangeAccountDoesNotUpdateAdminDisabledWhileBlocked(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	id := e.insertLegacyAdmins(t)
	e.loginAs(t, "admin", "admin")
	epochBefore := e.epoch(t)

	holder, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开启持锁事务: %v", err)
	}
	var wg sync.WaitGroup
	defer func() {
		// 中途失败也要放锁，并等卡住的 UPDATE 走完，免得它的写入落进下一个用例。
		_ = holder.Rollback(ctx)
		wg.Wait()
	}()
	var holderPID int32
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatalf("取持锁连接的 pid: %v", err)
	}
	if _, err := holder.Exec(ctx, `SELECT 1 FROM admin WHERE id = $1 FOR UPDATE`, id); err != nil {
		t.Fatalf("锁住管理员行: %v", err)
	}

	var changeErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, changeErr = e.svc.ChangeAccount(callCtx, id, service.ChangeAccountInput{Username: "renamed", OldPassword: "admin"})
	}()
	waitUntilBlockedBy(t, e.pool, holderPID)

	// reset-password 的第一步：停用该行，哈希不动。
	if _, err := holder.Exec(ctx, `UPDATE admin SET status = 'DISABLED' WHERE id = $1`, id); err != nil {
		t.Fatalf("另一个事务停用该行: %v", err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("提交另一个事务: %v", err)
	}
	wg.Wait()

	if !errors.Is(changeErr, domain.ErrUnauthorized) || adminErrCode(changeErr) != domain.CodeAdminSessionInvalid {
		t.Errorf("err = %v (code %q), want ErrUnauthorized / ADMIN_SESSION_INVALID（行已停用，不是旧密码错）", changeErr, adminErrCode(changeErr))
	}
	var name, status string
	if err := e.pool.QueryRow(ctx, `SELECT username, status FROM admin WHERE id = $1`, id).Scan(&name, &status); err != nil {
		t.Fatalf("读管理员行: %v", err)
	}
	if name != "admin" || status != "DISABLED" {
		t.Errorf("该行 = (%q, %q), want (admin, DISABLED)：被停用的行不该被改名", name, status)
	}
	if got := e.epoch(t); got != epochBefore {
		t.Errorf("纪元 %d -> %d，被拒的修改不该作废会话", epochBefore, got)
	}
	if n := e.tokenKeys(t); n != 1 {
		t.Errorf("会话数 = %d, want 1（只有登录时签发的那个）", n)
	}
}

var generatedPasswordRE = regexp.MustCompile(`^[a-km-zA-HJ-NP-Z2-9]{16}$`)

func TestResetPasswordRestoresAdminWithRandomPassword(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")
	if _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "root", OldPassword: "secret123456"}); err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	staleToken, _ := e.loginAs(t, "root", "secret123456") // 重置前签发的会话，重置后必须失效
	if _, err := e.pool.Exec(ctx, `UPDATE admin SET status = 'DISABLED'`); err != nil {
		t.Fatalf("停用管理员: %v", err)
	}

	username, password, disabled, err := e.svc.ResetPassword(ctx)
	if err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if username != "admin" {
		t.Fatalf("username = %q, want admin", username)
	}
	if disabled != 0 {
		t.Fatalf("disabled = %d, want 0（只有一行时没有旧账号可停用）", disabled)
	}
	if !generatedPasswordRE.MatchString(password) {
		t.Fatalf("password = %q，应为 16 位且不含 0 O 1 l I", password)
	}
	if _, err := e.svc.Login(ctx, "admin", password); err != nil {
		t.Fatalf("打印出来的账号密码应能登录（含被停用的账号被重新启用）: %v", err)
	}
	if _, err := e.svc.Login(ctx, "root", password); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("用户名应已恢复成 admin, err = %v", err)
	}
	if n := e.adminRows(t); n != 1 {
		t.Fatalf("admin 行数 = %d, want 1", n)
	}
	if _, _, err := e.svc.Authenticate(ctx, staleToken); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("重置前签发的会话应已失效, err = %v", err)
	}
}

func TestResetPasswordCreatesAdminWhenTableEmpty(t *testing.T) {
	e := newAdminTestEnv(t)
	_, password, _, err := e.svc.ResetPassword(context.Background())
	if err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if n := e.adminRows(t); n != 1 {
		t.Fatalf("admin 行数 = %d, want 1", n)
	}
	if _, err := e.svc.Login(context.Background(), "admin", password); err != nil {
		t.Fatalf("Login: %v", err)
	}
}

// ResetPassword 与 ChangeAccount 一样必须"先提交、后 INCR"：INCR 若先于提交，读到新纪元的
// 登录仍可能读到旧哈希（READ COMMITTED），Login"先读纪元再查库"的栅栏就破了。ChangeAccount
// 的那条见 TestChangeAccountCommitsBeforeBumpingEpoch。
//
// hook 在纪元 INCR 发出之前，用另一条连接读管理员行：此刻重置必须已经提交。
func TestResetPasswordCommitsBeforeBumpingEpoch(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewTestDB(t)
	base := testsupport.NewTestRedis(t)

	var hashBeforeReset string
	var fired, committed atomic.Bool
	hook := &redisCmdHook{
		before: func(ctx context.Context, cmd redis.Cmder) {
			if !isEpochCmd(cmd, "incr") || !fired.CompareAndSwap(false, true) {
				return
			}
			var h string
			if err := pool.QueryRow(ctx, `SELECT password_hash FROM admin`).Scan(&h); err != nil {
				t.Errorf("INCR 之前读管理员行: %v", err)
				return
			}
			committed.Store(h != hashBeforeReset)
		},
	}
	e := adminTestEnv{svc: service.NewAdminService(pool, hookedRedis(t, hook)), pool: pool, rdb: base}
	e.bootstrap(t, "admin", "secret123456")
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM admin`).Scan(&hashBeforeReset); err != nil {
		t.Fatalf("读重置前的哈希: %v", err)
	}

	if _, _, _, err := e.svc.ResetPassword(ctx); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if !fired.Load() {
		t.Fatal("hook 没有触发：ResetPassword 没有 INCR 纪元？")
	}
	if !committed.Load() {
		t.Fatal("纪元 INCR 发出时重置还没有提交：读到新纪元的登录仍可能读到旧哈希")
	}
}

// 旧版 EnsureBootstrap 是 ON CONFLICT (username) DO NOTHING：引导用户名不存在就补插一行，
// 老库里因此可能有多行（例如升级后系统配置为空，兜底的 admin/admin 被补插成较晚的一行）。
// 只改最早一行的话：admin 这个名字被较晚的行占着时 UPDATE 撞唯一约束，唯一的恢复路径失效；
// 否则其余行（可能正是 admin/admin）在"所有会话已作废"之后照样能登录。
func TestResetPasswordRetiresLegacyAdminRows(t *testing.T) {
	cases := []struct {
		name         string
		lateName     string
		lateStatus   string
		wantDisabled int
		wantRenamed  bool
	}{
		{"较晚的一行占着 admin", "admin", "ACTIVE", 1, true},
		// 改名与停用互相独立：早已停用的行照样占着名字，不腾出来保留行就改不回 admin。
		{"占着 admin 的那一行早已停用", "admin", "DISABLED", 0, true},
		{"较晚的一行是别的名字", "legacy", "ACTIVE", 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAdminTestEnv(t)
			ctx := context.Background()
			now := time.Now()
			e.insertAdminRow(t, "root", "root-password", "ACTIVE", now.Add(-48*time.Hour))
			lateID := e.insertAdminRow(t, c.lateName, "admin", c.lateStatus, now.Add(-24*time.Hour))

			username, password, disabled, err := e.svc.ResetPassword(ctx)
			if err != nil {
				t.Fatalf("ResetPassword: %v", err)
			}
			if username != "admin" || disabled != c.wantDisabled {
				t.Fatalf("username = %q, disabled = %d, want admin / %d", username, disabled, c.wantDisabled)
			}
			if _, err := e.svc.Login(ctx, "admin", password); err != nil {
				t.Fatalf("返回的新密码应能以 admin 登录: %v", err)
			}
			if _, err := e.svc.Login(ctx, "admin", "admin"); !errors.Is(err, domain.ErrInvalidCredential) {
				t.Fatalf("旧的 admin/admin 不该再能登录, err = %v", err)
			}

			if n := e.adminRows(t); n != 2 {
				t.Fatalf("admin 行数 = %d, want 2（不删数据）", n)
			}
			wantName := c.lateName
			if c.wantRenamed {
				wantName = "admin#" + lateID.String()
			}
			var gotName, gotStatus string
			if err := e.pool.QueryRow(ctx, `SELECT username, status FROM admin WHERE id = $1`, lateID).Scan(&gotName, &gotStatus); err != nil {
				t.Fatalf("读较晚的一行: %v", err)
			}
			if gotName != wantName || gotStatus != "DISABLED" {
				t.Fatalf("较晚的一行 = (%q, %q), want (%q, DISABLED)", gotName, gotStatus, wantName)
			}
			// 停用的账号即使口令还是众所周知的 admin，也拿不到会话。
			if _, err := e.svc.Login(ctx, wantName, "admin"); !errors.Is(err, domain.ErrForbidden) || adminErrCode(err) != domain.CodeAdminDisabled {
				t.Fatalf("停用账号登录 err = %v (code %q), want ErrForbidden / ADMIN_DISABLED", err, adminErrCode(err))
			}

			// 幂等：可安全重跑，第二次没有新的行要停用。
			_, again, disabled, err := e.svc.ResetPassword(ctx)
			if err != nil {
				t.Fatalf("第二次 ResetPassword: %v", err)
			}
			if disabled != 0 {
				t.Fatalf("第二次 disabled = %d, want 0", disabled)
			}
			if n := e.adminRows(t); n != 2 {
				t.Fatalf("第二次之后 admin 行数 = %d, want 2", n)
			}
			if _, err := e.svc.Login(ctx, "admin", again); err != nil {
				t.Fatalf("第二次重置的密码应能登录: %v", err)
			}
		})
	}
}

func TestResetPasswordIsRandomEachTime(t *testing.T) {
	e := newAdminTestEnv(t)
	_, first, _, err := e.svc.ResetPassword(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, second, _, err := e.svc.ResetPassword(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("两次重置得到同一个密码 %q", first)
	}
}
