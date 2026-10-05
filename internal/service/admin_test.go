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

// 用户名不存在时也要跑完一遍 bcrypt，否则几十毫秒的响应时间差会泄露"这个用户名存不存在"。
//
// 判定用"下限"而不是"两者之差"：bcrypt cost 10 要三十毫秒以上，短路掉它的路径只剩
// Redis 与 Postgres 各一次往返（这台机器的局域网库上实测约 10 ms，所以不能照搬
// TestVerifyPasswordEqualizesTiming 的 5 ms）。阈值取在两者之间，不做上限断言。
func TestLoginUnknownUsernamePaysBcryptCost(t *testing.T) {
	svc := newAdminService(t)
	const minBcrypt = 20 * time.Millisecond

	start := time.Now()
	_, err := svc.Login(context.Background(), "nobody", "definitely-wrong-password")
	elapsed := time.Since(start)

	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
	if elapsed < minBcrypt {
		t.Errorf("耗时 %v < %v，说明跳过了 bcrypt，响应时间会泄露用户名是否存在", elapsed, minBcrypt)
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

// redisCmdHook 在每条单命令前后各调一次回调（after 只在命令成功后调），拨号与管道原样放行。
// 回调自己判断是不是要拦的那条命令，并负责只触发一次。
type redisCmdHook struct {
	before, after func(ctx context.Context, cmd redis.Cmder)
}

func (h *redisCmdHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *redisCmdHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.before != nil {
			h.before(ctx, cmd)
		}
		err := next(ctx, cmd)
		if err == nil && h.after != nil {
			h.after(ctx, cmd)
		}
		return err
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
	if _, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "root", OldPassword: "admin"}); err != nil {
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

	_, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{
		Username: "admin", OldPassword: "nope", NewPassword: "new-password",
	})
	if !errors.Is(err, domain.ErrInvalidArgument) || adminErrCode(err) != domain.CodeAdminOldPasswordWrong {
		t.Fatalf("err = %v (code %q), want ErrInvalidArgument / ADMIN_OLD_PASSWORD_WRONG", err, adminErrCode(err))
	}
	if _, err := e.svc.Login(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("失败的修改不该动旧密码: %v", err)
	}
}

func TestChangeAccountChangesPasswordAndRevokesOtherSessions(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	other, id := e.loginAs(t, "admin", "secret123456")
	current, _ := e.loginAs(t, "admin", "secret123456")

	newToken, username, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{
		Username: "  boss ", OldPassword: "secret123456", NewPassword: "brand-new-pass",
	})
	if err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	if username != "boss" {
		t.Fatalf("username = %q, want boss（应去掉首尾空白）", username)
	}

	for name, tok := range map[string]string{"其他会话": other, "发起修改的旧会话": current} {
		if _, _, err := e.svc.Authenticate(ctx, tok); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("%s 应已失效, err = %v", name, err)
		}
	}
	if _, name, err := e.svc.Authenticate(ctx, newToken); err != nil || name != "boss" {
		t.Fatalf("新签发的 token: name = %q, err = %v", name, err)
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

	if _, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "root", OldPassword: "secret123456"}); err != nil {
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
		// NUL 会让 PG 报 22021（500）；换行、零宽空格是人打不出来的字符，会让管理员自己登不进去。
		{"登录名含 NUL", service.ChangeAccountInput{Username: "a\x00b", OldPassword: "secret123456"}},
		{"登录名含换行", service.ChangeAccountInput{Username: "a\nb", OldPassword: "secret123456"}},
		{"登录名含零宽空格", service.ChangeAccountInput{Username: "a​b", OldPassword: "secret123456"}},
	}
	for _, c := range cases {
		if _, _, err := e.svc.ChangeAccount(context.Background(), id, c.in); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want ErrInvalidArgument", c.name, err)
		}
	}
}

// 控制字符的校验不能误伤正常的名字：中文、名字内部的空格、组合附加符号都是人能打出来的。
func TestChangeAccountAcceptsOrdinaryUsernames(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	for _, name := range []string{"管理员", "ops team", "é", "a-b_c.d@e"} {
		if _, got, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: name, OldPassword: "secret123456"}); err != nil || got != name {
			t.Errorf("ChangeAccount(%q) = (%q, %v)，应当照常通过", name, got, err)
		}
	}
}

// 会话 payload 是 id|username|epoch，登录名里的 | 不能把它切歪。
func TestChangeAccountAllowsPipeInUsername(t *testing.T) {
	e := newAdminTestEnv(t)
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	token, _, err := e.svc.ChangeAccount(context.Background(), id, service.ChangeAccountInput{Username: "a|b", OldPassword: "secret123456"})
	if err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
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

	_, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "other", OldPassword: "secret123456"})
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
	_, _, err := e.svc.ChangeAccount(context.Background(), uuid.New(), service.ChangeAccountInput{Username: "boss", OldPassword: "secret123456"})
	if !errors.Is(err, domain.ErrUnauthorized) || adminErrCode(err) != domain.CodeAdminSessionInvalid {
		t.Fatalf("err = %v (code %q), want ErrUnauthorized / ADMIN_SESSION_INVALID", err, adminErrCode(err))
	}
}

// 校验旧密码到写库之间隔着一次 bcrypt，其间另一次改密或重置若已提交，无条件的
// UPDATE 会用开头读到的旧哈希把它盖回去：重置白做，发起人还拿着刚签发的有效会话。
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
				_, _, changeErr = e.svc.ChangeAccount(callCtx, id, c.in)
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

// 改账号签发的 token 必须带自己那次 INCR 的返回值，不能事后再读计数器：两步之间若又有
// 别处的作废落地（另一次改密或重置），读回来的是对方的纪元，token 会越过那次作废。
//
// hook 在 ChangeAccount 的 INCR 返回之后立刻再 INCR 一次，模拟这次作废：此时纪元是
// E+2，而 ChangeAccount 签发的 token 带的是 E+1，必须已经失效。
func TestChangeAccountSignsTokenWithItsOwnEpochBump(t *testing.T) {
	const oldPassword = "secret123456"
	ctx := context.Background()
	pool := testsupport.NewTestDB(t)
	base := testsupport.NewTestRedis(t)

	var fired atomic.Bool
	hook := &redisCmdHook{
		after: func(ctx context.Context, cmd redis.Cmder) {
			if !isEpochCmd(cmd, "incr") || !fired.CompareAndSwap(false, true) {
				return
			}
			// 走没挂 hook 的 base，免得递归进同一个 hook。
			if err := base.Incr(ctx, "fp:admin:epoch").Err(); err != nil {
				t.Errorf("模拟并发作废，INCR: %v", err)
			}
		},
	}
	e := adminTestEnv{svc: service.NewAdminService(pool, hookedRedis(t, hook)), pool: pool, rdb: base}
	e.bootstrap(t, "admin", oldPassword)
	_, id := e.loginAs(t, "admin", oldPassword)
	epochBefore := e.epoch(t)

	token, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "boss", OldPassword: oldPassword})
	if err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	if !fired.Load() {
		t.Fatal("hook 没有触发：ChangeAccount 没有 INCR 纪元？")
	}
	if got := e.epoch(t); got != epochBefore+2 {
		t.Fatalf("纪元 %d -> %d, want +2（ChangeAccount 自己那次加上模拟的并发作废）", epochBefore, got)
	}
	if _, _, err := e.svc.Authenticate(ctx, token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Authenticate err = %v, want ErrUnauthorized：token 应带本次 INCR 的纪元，已被后来的作废盖掉", err)
	}
}

// 改账号"先提交、后 INCR"，两步之间若夹进另一次凭据变更 X 的提交与 INCR（C_A < C_X <
// I_X < I_A），A 用自己那次 INCR 签出的 token 带着最终纪元，会撑过 X：只改登录名时 A 写回
// 的是原哈希，并发改密的条件 UPDATE 照样命中；重置的 UPDATE 本来就不带条件。INCR 之后重读
// 哈希，对不上就不能签发。
//
// hook 在 A 的 INCR 发出之前模拟 X：提交另一个哈希，再经没挂 hook 的 base 做一次 INCR。
func TestChangeAccountRefusesTokenWhenCredentialsChangedBeforeItsIncr(t *testing.T) {
	const oldPassword = "secret123456"
	ctx := context.Background()
	pool := testsupport.NewTestDB(t)
	base := testsupport.NewTestRedis(t)
	otherHash, err := bcrypt.GenerateFromPassword([]byte("written-by-other-change"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}

	var fired atomic.Bool
	hook := &redisCmdHook{
		before: func(ctx context.Context, cmd redis.Cmder) {
			if !isEpochCmd(cmd, "incr") || !fired.CompareAndSwap(false, true) {
				return
			}
			if _, err := pool.Exec(ctx, `UPDATE admin SET password_hash = $1`, string(otherHash)); err != nil {
				t.Errorf("模拟并发改密，写库: %v", err)
				return
			}
			if err := base.Incr(ctx, "fp:admin:epoch").Err(); err != nil {
				t.Errorf("模拟并发改密，INCR: %v", err)
			}
		},
	}
	e := adminTestEnv{svc: service.NewAdminService(pool, hookedRedis(t, hook)), pool: pool, rdb: base}
	e.bootstrap(t, "admin", oldPassword)
	_, id := e.loginAs(t, "admin", oldPassword)
	epochBefore := e.epoch(t)

	// 只改登录名：UPDATE 写回的恰是原哈希，条件 UPDATE 拦不住的就是这种。
	token, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "boss", OldPassword: oldPassword})
	if !fired.Load() {
		t.Fatal("hook 没有触发：ChangeAccount 没有 INCR 纪元？")
	}
	if err == nil {
		_, _, aerr := e.svc.Authenticate(ctx, token)
		t.Fatalf("凭据已被另一次变更换掉，ChangeAccount 仍签发了 token（Authenticate err = %v，nil 即它是有效会话）", aerr)
	}
	if !errors.Is(err, domain.ErrUnauthorized) || adminErrCode(err) != domain.CodeAdminSessionInvalid {
		t.Fatalf("err = %v (code %q), want ErrUnauthorized / ADMIN_SESSION_INVALID", err, adminErrCode(err))
	}
	if n := e.tokenKeys(t); n != 1 {
		t.Errorf("会话数 = %d, want 1（只有登录时签发的那个，不该多出新的）", n)
	}
	if got := e.epoch(t); got != epochBefore+2 {
		t.Errorf("纪元 %d -> %d, want +2（ChangeAccount 自己那次加上模拟的并发变更）", epochBefore, got)
	}
}

var generatedPasswordRE = regexp.MustCompile(`^[a-km-zA-HJ-NP-Z2-9]{16}$`)

func TestResetPasswordRestoresAdminWithRandomPassword(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")
	if _, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "root", OldPassword: "secret123456"}); err != nil {
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
// 的顺序有"失败的修改不动纪元"的断言钉着，ResetPassword 的这里钉。
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
