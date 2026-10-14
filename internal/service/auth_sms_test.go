package service_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
)

// smsLogin 发码、从假通知中心取回验证码、用 sms_code 登录。
func (e *authEnv) smsLogin(t *testing.T, phone string) *service.LoginResult {
	t.Helper()
	ctx := context.Background()
	if err := e.auth.SendLoginCode(ctx, e.app.AppID, phone); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	code := e.sms.LastCode()
	if code == "" {
		t.Fatal("通知中心没有收到验证码")
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
}

// 端到端验证账号归并：短信建的号，设完密码后用手机号+密码登录，必须是同一个人。
func TestPasswordAndSMSLoginResolveToSameUser(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	first := e.smsLogin(t, "13800138000")
	if err := e.users.SetPassword(ctx, first.User.ID, authTestPassword); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	second, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "13800138000", "password": authTestPassword},
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

// 关掉「首次登录自动注册」后，没注册过的手机号即使验证码正确也进不来，也不会被建号。
func TestSMSCodeLoginWithoutAutoRegisterRejectsUnknownPhone(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, true, map[string]any{"autoRegister": false}); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	_, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": e.sms.LastCode()},
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
	if _, _, err := e.users.FindByIdentity(ctx, domain.IdentityTypePhone, "13800138000"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("不允许自动注册时不该建号: err = %v", err)
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
	code := e.sms.LastCode()
	if code == "" {
		t.Fatal("未取到验证码")
	}

	// 再停用该登录方式
	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, false, nil); err != nil {
		t.Fatalf("停用 sms_code: %v", err)
	}

	in := service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": code},
	}
	if _, err := e.auth.Login(ctx, in); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}

	// 关键断言：验证码必须**没有被消费**。重新启用后它应当仍然可用——若已被消费，
	// 说明 Authenticate 被调用了，也就是说开关检查发生在凭据校验之后。
	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, true, nil); err != nil {
		t.Fatalf("重新启用 sms_code: %v", err)
	}
	if _, err := e.auth.Login(ctx, in); err != nil {
		t.Fatalf("验证码在被拒绝的那次登录中已被消费——说明应用级开关检查发生在 Authenticate 之后，顺序错了: %v", err)
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
	if len(e.sms.Sent()) != 0 {
		t.Fatal("登录方式未启用时不该发短信")
	}
}

func TestSendLoginCodeRejectsMalformedPhone(t *testing.T) {
	e := newAuthEnv(t, service.WithLoginCodeRateRules(service.DefaultLoginCodeRateRules()...))
	ctx := context.Background()

	// 连发两次：格式校验若排在限流之后，第二次得到的会是 ErrRateLimited，畸形串还会在 Redis 里占出键来。
	for i := 1; i <= 2; i++ {
		if err := e.auth.SendLoginCode(ctx, e.app.AppID, "12345"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("第 %d 次 err = %v, want ErrInvalidArgument", i, err)
		}
	}
	if len(e.sms.Sent()) != 0 {
		t.Fatal("格式非法时不应真的发短信")
	}
}

// 应用停用、登录方式没启用都得先于限流判断：被拒绝的请求不占发码额度，
// 也不会因为额度用完而换成另一种报错。
func TestSendLoginCodeChecksAppAndConnectorBeforeRateLimit(t *testing.T) {
	e := newAuthEnv(t, service.WithLoginCodeRateRules(service.DefaultLoginCodeRateRules()...))
	ctx := context.Background()

	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, false, nil); err != nil {
		t.Fatalf("停用 sms_code: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("登录方式未启用，第 %d 次 err = %v, want ErrForbidden", i, err)
		}
	}
	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, true, nil); err != nil {
		t.Fatalf("启用 sms_code: %v", err)
	}
	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("被拒绝的请求占了发码额度: %v", err)
	}

	if _, err := e.pool.Exec(ctx, `UPDATE application SET status = $2 WHERE id = $1`,
		e.app.ID, domain.ApplicationStatusDisabled); err != nil {
		t.Fatalf("停用应用: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13900139000"); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("应用已停用，第 %d 次 err = %v, want ErrForbidden", i, err)
		}
	}
	if n := len(e.sms.Sent()); n != 1 {
		t.Fatalf("发送次数 = %d, want 1", n)
	}
}

// 猜错到上限之后点重发，收到的必须是一个能用的新码，而不是同一个注定作废的码。
func TestResendAfterAttemptsExhaustedDeliversUsableCode(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	const phone = "13800138000"
	login := func(code string) error {
		_, err := e.auth.Login(ctx, service.LoginInput{
			AppID:         e.app.AppID,
			ConnectorType: connector.TypeSMSCode,
			Credentials:   connector.Credentials{"phone": phone, "code": code},
		})
		return err
	}

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, phone); err != nil {
		t.Fatalf("首次发送: %v", err)
	}
	wrong := "000000"
	if e.sms.LastCode() == wrong {
		wrong = "000001"
	}
	for i := 0; i < service.MaxVerifyAttempts; i++ {
		if err := login(wrong); !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("第 %d 次猜错 err = %v", i+1, err)
		}
	}

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, phone); err != nil {
		t.Fatalf("重发: %v", err)
	}
	if err := login(e.sms.LastCode()); err != nil {
		t.Fatalf("猜错用满之后重发的验证码登录不了: %v", err)
	}
}

// 钉住与通知中心之间的约定：模板 code 是 verify_code，收件人是手机号，变量只有 code，
// 不带幂等键（每次点"发送"都是一次新的尝试，由发码限流兜住重复）。
func TestSendLoginCodeAsksNotifyCenterForTheLoginTemplate(t *testing.T) {
	e := newAuthEnv(t)
	if err := e.auth.SendLoginCode(context.Background(), e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	sent := e.sms.Sent()
	if len(sent) != 1 {
		t.Fatalf("发送次数 = %d, want 1", len(sent))
	}
	got := sent[0]
	if got.Code != "verify_code" {
		t.Errorf("模板 code = %q, want verify_code", got.Code)
	}
	if got.To != "13800138000" || got.AppID != e.app.AppID || got.IdempotencyKey != "" {
		t.Errorf("请求 = %+v", got)
	}
	if len(got.Params) != 1 || !regexp.MustCompile(`^\d{6}$`).MatchString(got.Params["code"]) {
		t.Errorf("变量 = %v, want 只有一个 6 位数字的 code", got.Params)
	}
}

// 限流只拦发码，被拦下的请求既不能再发一条短信，也不能作废用户手里已经收到的验证码。
//
// 这是个用户侧的死锁：如果被限流的重发顶掉了旧码，而新码又因限流发不出去，
// 用户手上的码作废了、补发没到，整个窗口内都登录不了。
func TestRateLimitedResendDoesNotInvalidateDeliveredCode(t *testing.T) {
	e := newAuthEnv(t, service.WithLoginCodeRateRules(service.LoginCodeRateRule{Name: "30s", Window: 30 * time.Second, Limit: 1}))
	ctx := context.Background()

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("首次发送: %v", err)
	}
	delivered := e.sms.LastCode()

	// 窗口内再点一次，必然被限流
	err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000")
	if !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("二次发送 err = %v, want ErrRateLimited", err)
	}
	if n := len(e.sms.Sent()); n != 1 {
		t.Fatalf("被限流的请求不该再发短信: 发送次数 = %d", n)
	}

	// 关键断言：用户手里那个码必须仍然可用
	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": delivered},
	}); err != nil {
		t.Fatalf("被限流的重发作废了用户已收到的验证码，用户被锁在窗口外: %v", err)
	}
}

func TestSendLoginCodeIsRateLimitedPerPhone(t *testing.T) {
	// 不传规则选项走默认：30 秒 1 条。
	e := newAuthEnv(t, service.WithLoginCodeRateRules(service.DefaultLoginCodeRateRules()...))
	ctx := context.Background()

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("首次: %v", err)
	}
	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("二次 err = %v, want ErrRateLimited", err)
	}
	// 按手机号计：别的号码不受影响。
	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13900139000"); err != nil {
		t.Fatalf("别的号码被误限流: %v", err)
	}
}

// 通知中心没配好是平台侧的问题，不能透传：模板缺失 / 停用会以 NotFound / PermissionDenied 出去，
// SDK 把它们归成 ErrUnauthorized，业务方的登录页就把"通知中心没配好"显示成了"未授权"。
// 但要分清是"模板没配好"（去控制台配）还是"供应商没发出去"（查供应商）。
func TestSendLoginCodeHidesNotifyCenterFailures(t *testing.T) {
	type tc struct {
		cause error
		want  string
	}
	tpl, sent := domain.CodeVerifyTemplateUnavailable, domain.CodeNotifySendFailed
	for name, c := range map[string]tc{
		"模板不存在":  {domain.Fail(domain.ErrNotFound, domain.CodeNotifyTemplateNotFound, "no template"), tpl},
		"模板被停用":  {domain.Fail(domain.ErrForbidden, domain.CodeNotifyTemplateDisabled, "disabled"), tpl},
		"变量对不上":  {domain.Fail(domain.ErrInvalidArgument, domain.CodeNotifyParamsInvalid, "params"), tpl},
		"没关联供应商": {domain.Fail(domain.ErrNotFound, domain.CodeNotifyProviderMissing, "no provider"), tpl},
		"供应商全挂":  {domain.Fail(domain.ErrInternal, domain.CodeNotifySendFailed, "all failed"), sent},
		"未知错误":   {errors.New("boom"), sent},
	} {
		cause := c.cause
		t.Run(name, func(t *testing.T) {
			e := newAuthEnv(t)
			e.sms.FailWith(cause)

			err := e.auth.SendLoginCode(context.Background(), e.app.AppID, "13800138000")
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != c.want || !errors.Is(err, domain.ErrInternal) {
				t.Fatalf("err = %v, want %s / ErrInternal", err, c.want)
			}
			if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("不该把平台侧的配置问题伪装成调用方的错误: %v", err)
			}
		})
	}
}

// 真的通知中心 + 假供应商：发码 → 供应商收到 → 用收到的码登录 → 发送记录里有这一条。
// 钉住 AuthService 与通知模板之间的约定（code 是 verify_code，变量是 code）：
// 任何一边改了名，这条先红。
func TestSMSCodeLoginThroughRealNotifyCenter(t *testing.T) {
	ne := newNotifyEnv(t)
	ne.smsTemplate(t, service.VerifyCodeTemplate)
	pid := ne.provider(t, "fake_sms", "sms1")
	ne.link(t, service.VerifyCodeTemplate, pid, "SMS_LOGIN_1", 0)
	auth, appID := newSMSAuth(t, ne, service.NewLoginCodeService(ne.rdb))
	ctx := context.Background()

	if err := auth.SendLoginCode(ctx, appID, "13800138000"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	d := ne.fake("sms1").lastDelivery()
	if d.To != "13800138000" || d.ProviderTemplateID != "SMS_LOGIN_1" {
		t.Fatalf("供应商收到的 = %+v", d)
	}
	code := d.Params["code"]
	if !regexp.MustCompile(`^\d{6}$`).MatchString(code) {
		t.Fatalf("验证码 = %q", code)
	}

	res, err := auth.Login(ctx, service.LoginInput{
		AppID:         appID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": code},
	})
	if err != nil || res.User == nil {
		t.Fatalf("Login: %v", err)
	}

	logs, total, err := ne.svc.ListLogs(ctx, service.NotifyLogFilter{Code: service.VerifyCodeTemplate})
	if err != nil || total != 1 || !logs[0].Success || logs[0].AppID != appID || logs[0].Target != "13800138000" {
		t.Fatalf("发送记录 = %+v, total = %d, err = %v", logs, total, err)
	}
}

// 管理员还没在通知中心建 verify_code 模板时，发码失败得说清是模板的事，而不是 NotFound。
func TestSendLoginCodeWithoutTemplateInNotifyCenter(t *testing.T) {
	ne := newNotifyEnv(t)
	auth, appID := newSMSAuth(t, ne, service.NewLoginCodeService(ne.rdb))

	err := auth.SendLoginCode(context.Background(), appID, "13800138000")
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeVerifyTemplateUnavailable {
		t.Fatalf("err = %v, want VERIFY_TEMPLATE_UNAVAILABLE", err)
	}
}

// newSMSAuth 在 ne 的库和 Redis 上装一套只启用 sms_code 的登录栈，通知走 ne 里真的 NotifyService。
func newSMSAuth(t *testing.T, ne *notifyEnv, codes *service.LoginCodeService) (*service.AuthService, string) {
	t.Helper()
	users := service.NewUserService(ne.pool)
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}
	apps := service.NewApplicationService(ne.pool, reg)
	ctx := context.Background()
	app, _, err := apps.Create(ctx, "短信应用", "sms-app")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	if err := apps.SetConnector(ctx, app.ID, connector.TypeSMSCode, true, nil); err != nil {
		t.Fatalf("启用 sms_code: %v", err)
	}
	auth := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users,
		Sessions: service.NewSessionService(store.NewSessionStore(ne.rdb), store.NewRevokePublisher(ne.rdb), store.NewEpochStore(ne.rdb)),
		Logs:     service.NewLoginLogService(ne.pool),
		Registry: reg, Codes: codes, Notifier: ne.svc,
	})
	return auth, app.AppID
}
