package service_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
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

func adminErrCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
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
	}
	for _, c := range cases {
		if _, _, err := e.svc.ChangeAccount(context.Background(), id, c.in); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want ErrInvalidArgument", c.name, err)
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

	username, password, err := e.svc.ResetPassword(ctx)
	if err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if username != "admin" {
		t.Fatalf("username = %q, want admin", username)
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
	_, password, err := e.svc.ResetPassword(context.Background())
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

func TestResetPasswordIsRandomEachTime(t *testing.T) {
	e := newAdminTestEnv(t)
	_, first, err := e.svc.ResetPassword(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := e.svc.ResetPassword(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("两次重置得到同一个密码 %q", first)
	}
}
