package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

// authEnv 是一套完整装配好的登录环境，测试用它跑端到端流程。
type authEnv struct {
	auth  *service.AuthService
	apps  *service.ApplicationService
	users *service.UserService
	sess  *service.SessionService
	logs  *service.LoginLogService
	sms   *notify.FakeProvider
	codes *notify.CodeService
	app   *domain.Application
	// pool 用于少数需要绕过 service 层直接改库的用例（例如制造"应用已停用"这种
	// 第一阶段还没有管理接口可以到达的状态）。
	pool *pgxpool.Pool
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	apps := service.NewApplicationService(pool)
	users := service.NewUserService(pool)
	sessions := service.NewSessionService(store.NewSessionStore(rdb), store.NewRevokePublisher(rdb))
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
	sender := notify.NewSender(pool, store.NewRateLimiter(rdb), nil)
	sender.AddProvider(sms)

	app, _, err := apps.Create(context.Background(), "测试应用", "test-app")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	ctx := context.Background()
	for _, typ := range []string{connector.TypePassword, connector.TypeSMSCode} {
		if err := apps.SetConnector(ctx, app.ID, typ, true, nil); err != nil {
			t.Fatalf("启用 %s: %v", typ, err)
		}
	}

	return &authEnv{
		auth: service.NewAuthService(service.AuthDeps{
			Apps: apps, Users: users, Sessions: sessions, Logs: logs,
			Registry: reg, Notifier: sender, Codes: codes,
		}),
		apps: apps, users: users, sess: sessions, logs: logs,
		sms: sms, codes: codes, app: app, pool: pool,
	}
}

func (e *authEnv) smsLogin(t *testing.T, phone string) *service.LoginResult {
	t.Helper()
	ctx := context.Background()
	if err := e.auth.SendLoginCode(ctx, e.app.AppID, phone); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	code := e.sms.LastParam("code")
	if code == "" {
		t.Fatal("未从假供应商取到验证码")
	}
	res, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": phone, "code": code},
		IP:            "1.2.3.4", UA: "go-test",
	})
	if err != nil {
		t.Fatalf("Login(sms_code): %v", err)
	}
	return res
}

func TestSMSCodeLoginCreatesUser(t *testing.T) {
	e := newAuthEnv(t)
	res := e.smsLogin(t, "13800138000")

	if res.User == nil || res.Session == nil {
		t.Fatal("返回结果不完整")
	}
	if res.Session.Token == "" {
		t.Fatal("token 为空")
	}
	if res.User.Status != domain.UserStatusActive {
		t.Fatalf("Status = %q", res.User.Status)
	}

	// 签发的 token 立刻可用
	vr, err := e.auth.ValidateToken(context.Background(), e.app.AppID, res.Session.Token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if vr.Session.UserID != res.User.ID {
		t.Fatal("token 指向的用户不对")
	}
	if vr.CacheTTL <= 0 {
		t.Fatalf("CacheTTL = %v", vr.CacheTTL)
	}
}

// 端到端验证账号归并：短信建的号，设完密码后用手机号+密码登录，必须是同一个人。
func TestPasswordAndSMSLoginResolveToSameUser(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	first := e.smsLogin(t, "13800138000")
	if err := e.users.SetPassword(ctx, first.User.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	second, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "13800138000", "password": "hunter2hunter2"},
	})
	if err != nil {
		t.Fatalf("Login(password): %v", err)
	}
	if second.User.ID != first.User.ID {
		t.Fatalf("归并失败: %v vs %v", second.User.ID, first.User.ID)
	}
	if second.Session.Token == first.Session.Token {
		t.Fatal("两次登录应签发不同的 token")
	}
}

// 密码登录不建号：账号不存在时必须拒绝，而不是悄悄注册一个。
func TestPasswordLoginDoesNotCreateUser(t *testing.T) {
	e := newAuthEnv(t)
	_, err := e.auth.Login(context.Background(), service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "13800138000", "password": "hunter2hunter2"},
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

// 应用没启用的登录方式必须在校验凭据之前就被拒绝。
func TestLoginRejectsDisabledConnector(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypePassword, false, nil); err != nil {
		t.Fatalf("停用 password: %v", err)
	}
	_, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "13800138000", "password": "x"},
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// 停用登录方式后，一次被拒绝的登录尝试不能消费掉验证码——这个断言比
// "返回 ErrForbidden" 更严格：两种实现（开关检查在 Authenticate 之前或之后）
// 在被拒绝这件事上表现一致，只有验证码是否被消费能区分它们。
func TestDisabledConnectorRejectsBeforeConsumingCode(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	// 先在启用状态下拿到一个真实验证码
	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	code := e.sms.LastParam("code")
	if code == "" {
		t.Fatal("未取到验证码")
	}

	// 再停用该登录方式
	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, false, nil); err != nil {
		t.Fatalf("停用 sms_code: %v", err)
	}

	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": code},
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}

	// 关键断言：验证码必须**没有被消费**。
	// 重新启用后它应当仍然可用——若已被消费，说明 Authenticate 被调用了，
	// 也就是说开关检查发生在凭据校验之后。
	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, true, nil); err != nil {
		t.Fatalf("重新启用 sms_code: %v", err)
	}
	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": code},
	}); err != nil {
		t.Fatalf("验证码在被拒绝的那次登录中已被消费——说明应用级开关检查"+
			"发生在 Authenticate 之后，顺序错了: %v", err)
	}
}

func TestLoginRejectsUnconfiguredConnector(t *testing.T) {
	e := newAuthEnv(t)
	_, err := e.auth.Login(context.Background(), service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: "wechat_mp", // 已注册但该应用未配置
		Credentials:   connector.Credentials{},
	})
	if !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrForbidden 或 ErrNotFound", err)
	}
}

func TestLoginRejectsUnknownApplication(t *testing.T) {
	e := newAuthEnv(t)
	_, err := e.auth.Login(context.Background(), service.LoginInput{
		AppID:         "no-such-app",
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "a", "password": "b"},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestLoginRejectsFrozenUser(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")
	if _, err := e.users.SetStatus(ctx, res.User.ID, domain.UserStatusFrozen); err != nil {
		t.Fatalf("冻结: %v", err)
	}

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	_, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": e.sms.LastParam("code")},
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// 注销保护期内登录会撤销注销申请——沿用 3s 的行为。
func TestLoginCancelsPendingDeletion(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	first := e.smsLogin(t, "13800138000")
	if _, err := e.users.SetStatus(ctx, first.User.ID, domain.UserStatusPendingDelete); err != nil {
		t.Fatalf("提交注销: %v", err)
	}

	second := e.smsLogin(t, "13800138000")
	if second.User.ID != first.User.ID {
		t.Fatal("不是同一个用户")
	}
	if second.User.Status != domain.UserStatusActive {
		t.Fatalf("Status = %q, want ACTIVE（登录应撤销注销申请）", second.User.Status)
	}
	if second.User.DeleteSubmittedAt != 0 {
		t.Fatalf("DeleteSubmittedAt = %d, 应被清零", second.User.DeleteSubmittedAt)
	}
}

// 注册关系必须幂等：同一用户重复登录不能因重复插入而失败。
func TestLoginRecordsRegistrationIdempotently(t *testing.T) {
	e := newAuthEnv(t)
	first := e.smsLogin(t, "13800138000")
	again := e.smsLogin(t, "13800138000")
	if again.User.ID != first.User.ID {
		t.Fatal("两次登录不是同一用户")
	}
}

func TestLoginWritesAuditLog(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")
	list, err := e.logs.ListByUser(ctx, res.User.ID, 10)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("登录成功未写审计日志")
	}
	last := list[0]
	if !last.Success || last.Event != domain.LoginEventLogin {
		t.Fatalf("log = %+v", last)
	}
	if last.IP != "1.2.3.4" || last.UA != "go-test" {
		t.Fatalf("设备信息未记录: %+v", last)
	}
	// 审计日志里必须是脱敏后的手机号
	if last.Subject != "138****8000" {
		t.Fatalf("Subject = %q, want 138****8000", last.Subject)
	}
	if last.SessionID != res.Session.ID {
		t.Fatalf("SessionID = %q, want %q", last.SessionID, res.Session.ID)
	}
}

// 登录失败也要留痕，否则爆破攻击无从追溯。
func TestFailedLoginWritesAuditLog(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	// 先建号并设密码，这样失败记录能挂到 user_id 上，便于按用户查询
	res := e.smsLogin(t, "13800138000")
	if err := e.users.SetPassword(ctx, res.User.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "13800138000", "password": "wrong"},
		IP:            "9.9.9.9",
	}); err == nil {
		t.Fatal("应当登录失败")
	}

	list, err := e.logs.ListByUser(ctx, res.User.ID, 10)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	var found bool
	for _, l := range list {
		if !l.Success && l.IP == "9.9.9.9" && l.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("未找到失败的审计记录: %+v", list)
	}
}

// 应用被停用后，登录、发码、token 校验三条入口必须同时失效。
// 第一阶段没有停用应用的管理接口，因此这里直接改库来制造该状态。
func TestDisabledApplicationBlocksAllAuthEntryPoints(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")

	if _, err := e.pool.Exec(ctx,
		`UPDATE application SET status = $2 WHERE id = $1`,
		e.app.ID, domain.ApplicationStatusDisabled); err != nil {
		t.Fatalf("停用应用: %v", err)
	}

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("SendLoginCode err = %v, want ErrForbidden", err)
	}
	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": "123456"},
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("Login err = %v, want ErrForbidden", err)
	}
	// 已签发的 token 也必须立刻失效
	if _, err := e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("ValidateToken err = %v, want ErrForbidden", err)
	}
}

func TestSendLoginCodeRequiresEnabledConnector(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, false, nil); err != nil {
		t.Fatalf("停用 sms_code: %v", err)
	}
	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestSendLoginCodeRejectsMalformedPhone(t *testing.T) {
	e := newAuthEnv(t)
	if err := e.auth.SendLoginCode(context.Background(), e.app.AppID, "12345"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	if len(e.sms.Sent()) != 0 {
		t.Fatal("格式非法时不应真的发短信")
	}
}

// 命名加 AuthService 前缀以区别于 admin_test.go 里同名但测 AdminService 的用例。
func TestAuthServiceLogoutInvalidatesToken(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")
	if err := e.auth.Logout(ctx, res.Session.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestValidateTokenRejectsWrongApp(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	other, _, err := e.apps.Create(ctx, "另一个应用", "other-app")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	res := e.smsLogin(t, "13800138000")

	if _, err := e.auth.ValidateToken(ctx, other.AppID, res.Session.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestSendLoginCodeIsRateLimitedPerPhone(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	apps := service.NewApplicationService(pool)
	users := service.NewUserService(pool)
	codes := notify.NewCodeService(rdb)
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册: %v", err)
	}
	sms := notify.NewFakeProvider(notify.ChannelSMS, "fake")
	// 启用 30 秒一次的限制
	sender := notify.NewSender(pool, store.NewRateLimiter(rdb),
		[]notify.RateRule{{Name: "30s", Window: 30 * time.Second, Limit: 1}})
	sender.AddProvider(sms)

	app, _, err := apps.Create(context.Background(), "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	ctx := context.Background()
	if err := apps.SetConnector(ctx, app.ID, connector.TypeSMSCode, true, nil); err != nil {
		t.Fatalf("启用: %v", err)
	}

	auth := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users,
		Sessions: service.NewSessionService(store.NewSessionStore(rdb), store.NewRevokePublisher(rdb)),
		Logs:     service.NewLoginLogService(pool),
		Registry: reg, Notifier: sender, Codes: codes,
	})

	if err := auth.SendLoginCode(ctx, app.AppID, "13800138000"); err != nil {
		t.Fatalf("首次: %v", err)
	}
	if err := auth.SendLoginCode(ctx, app.AppID, "13800138000"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("二次 err = %v, want ErrRateLimited", err)
	}
}
