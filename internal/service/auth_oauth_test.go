package service_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

const oauthReturnURL = "https://app.example.com/login/wechat"

type oauthEnv struct {
	auth  *service.AuthService
	apps  *service.ApplicationService
	users *service.UserService
	sms   *testsupport.FakeLoginSMS
	app   string // 对外 appId
	appID string // 内部 UUID 字符串
}

// newOAuthEnv 只启用 wechat_web，不启用 sms_code：绑手机发码靠的是放宽后的 SendLoginCode。
func newOAuthEnv(t *testing.T, extra map[string]any) *oauthEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	wx := testsupport.NewFakeWechat(t)

	users := service.NewUserService(pool)
	codes := service.NewLoginCodeService(rdb, service.WithLoginCodeRateRules())
	reg := connector.NewRegistry()
	for _, c := range []connector.Connector{connector.NewSMSCode(codes), connector.NewWechatWeb(connector.WithWechatAPIBase(wx.URL))} {
		if err := reg.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	apps := service.NewApplicationService(pool, reg)
	ctx := context.Background()
	app, _, err := apps.Create(ctx, "测试应用", "test-app")
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"appId": "wxapp", "appSecret": "s3cret", "returnUrls": oauthReturnURL}
	for k, v := range extra {
		cfg[k] = v
	}
	if err := apps.SetConnector(ctx, app.ID, connector.TypeWechatWeb, true, cfg); err != nil {
		t.Fatal(err)
	}
	sms := &testsupport.FakeLoginSMS{}
	sessions := service.NewSessionService(store.NewSessionStore(rdb), store.NewRevokePublisher(rdb), store.NewEpochStore(rdb))
	return &oauthEnv{
		auth: service.NewAuthService(service.AuthDeps{
			Apps: apps, Users: users, Sessions: sessions, Logs: service.NewLoginLogService(pool), Registry: reg,
			Codes: codes, Notifier: sms, OAuth: service.NewOAuthStore(rdb), PublicURL: "https://fp.example.com",
		}),
		apps: apps, users: users, sms: sms, app: app.AppID, appID: app.ID.String(),
	}
}

func (e *oauthEnv) client() service.OAuthClient { return service.OAuthClient{AppID: e.app, IP: "1.2.3.4"} }

func (e *oauthEnv) start(t *testing.T, mode string) *service.OAuthStart {
	t.Helper()
	st, err := e.auth.StartOAuth(context.Background(), service.OAuthStartInput{
		AppID: e.app, ConnectorType: connector.TypeWechatWeb, Mode: mode, ReturnURL: oauthReturnURL,
	})
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	return st
}

// webLogin 走完 web 模式的发起 → 回调，返回 fpTicket。
func (e *oauthEnv) webTicket(t *testing.T, code string) string {
	t.Helper()
	st := e.start(t, service.OAuthModeWeb)
	cb := e.auth.HandleOAuthCallback(context.Background(), st.State, code)
	u, err := url.Parse(cb.Redirect)
	if err != nil || !strings.HasPrefix(cb.Redirect, oauthReturnURL+"?") {
		t.Fatalf("redirect = %q", cb.Redirect)
	}
	ticket := u.Query().Get("fpTicket")
	if ticket == "" {
		t.Fatalf("redirect 没带 fpTicket：%q", cb.Redirect)
	}
	return ticket
}

func (e *oauthEnv) bind(t *testing.T, bindTicket, phone string) *service.LoginResult {
	t.Helper()
	ctx := context.Background()
	if err := e.auth.SendLoginCode(ctx, e.app, phone); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	res, err := e.auth.BindPhone(ctx, service.BindPhoneInput{OAuthClient: e.client(), BindTicket: bindTicket, Phone: phone, Code: e.sms.LastCode()})
	if err != nil {
		t.Fatalf("BindPhone: %v", err)
	}
	return res
}

func wantOAuthCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err = %v, want 码 %s", err, code)
	}
}

func TestOAuthStartWeb(t *testing.T) {
	e := newOAuthEnv(t, nil)
	st := e.start(t, service.OAuthModeWeb)
	if st.RedirectURI != "https://fp.example.com/oauth/callback" || st.ProviderAppID != "wxapp" ||
		!strings.Contains(st.AuthURL, "state="+st.State) || st.ExpiresInSec != 300 {
		t.Fatalf("start = %+v", st)
	}
}

func TestOAuthFirstLoginBindsPhoneThenLogsInDirectly(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()

	out, err := e.auth.ExchangeOAuth(ctx, e.client(), e.webTicket(t, "o1:u1:小明"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != service.OAuthStatusBindPhone || out.BindTicket == "" || out.Nickname != "小明" || out.AvatarURL != "https://img/o1" {
		t.Fatalf("out = %+v", out)
	}

	// 验证码错了不消费票据，可以重试。
	if err := e.auth.SendLoginCode(ctx, e.app, "13800138000"); err != nil {
		t.Fatal(err)
	}
	_, err = e.auth.BindPhone(ctx, service.BindPhoneInput{OAuthClient: e.client(), BindTicket: out.BindTicket, Phone: "13800138000", Code: "000000x"})
	wantOAuthCode(t, err, domain.CodeCodeInvalid)

	res := e.bind(t, out.BindTicket, "13800138000")
	if res.Session == nil || res.User.Nickname != "小明" {
		t.Fatalf("res = %+v", res)
	}
	// 票据已用。
	_, err = e.auth.BindPhone(ctx, service.BindPhoneInput{OAuthClient: e.client(), BindTicket: out.BindTicket, Phone: "13800138000", Code: e.sms.LastCode()})
	wantOAuthCode(t, err, domain.CodeOAuthTicketInvalid)

	again, err := e.auth.ExchangeOAuth(ctx, e.client(), e.webTicket(t, "o1:u1:小明"))
	if err != nil || again.Status != service.OAuthStatusSuccess || again.Login.User.ID != res.User.ID {
		t.Fatalf("again = %+v, err = %v", again, err)
	}
}

func TestOAuthTicketSingleUseAndBoundToApp(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	ticket := e.webTicket(t, "o1:u1")

	other, _, err := e.apps.Create(ctx, "别的应用", "other-app")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.auth.ExchangeOAuth(ctx, service.OAuthClient{AppID: other.AppID}, ticket)
	wantOAuthCode(t, err, domain.CodeOAuthTicketInvalid)

	ticket = e.webTicket(t, "o1:u1")
	if _, err := e.auth.ExchangeOAuth(ctx, e.client(), ticket); err != nil {
		t.Fatal(err)
	}
	_, err = e.auth.ExchangeOAuth(ctx, e.client(), ticket)
	wantOAuthCode(t, err, domain.CodeOAuthTicketInvalid)
}

// web 模式的 state 不能拿来轮询：否则发起人能取走完成扫码的人的会话。
func TestOAuthWebStateCannotBePolled(t *testing.T) {
	e := newOAuthEnv(t, map[string]any{"desktopPoll": true})
	ctx := context.Background()
	st := e.start(t, service.OAuthModeWeb)
	e.auth.HandleOAuthCallback(ctx, st.State, "o1:u1")
	_, err := e.auth.PollOAuth(ctx, e.client(), st.State)
	wantOAuthCode(t, err, domain.CodeOAuthStateInvalid)
}

func TestOAuthDesktopDisabledByDefault(t *testing.T) {
	e := newOAuthEnv(t, nil)
	_, err := e.auth.StartOAuth(context.Background(), service.OAuthStartInput{AppID: e.app, ConnectorType: connector.TypeWechatWeb, Mode: service.OAuthModeDesktop})
	wantOAuthCode(t, err, domain.CodeDesktopModeDisabled)
}

func TestOAuthDesktopPoll(t *testing.T) {
	e := newOAuthEnv(t, map[string]any{"desktopPoll": true})
	ctx := context.Background()
	st := e.start(t, service.OAuthModeDesktop)

	out, err := e.auth.PollOAuth(ctx, e.client(), st.State)
	if err != nil || out.Status != service.OAuthStatusPending {
		t.Fatalf("回调前 out = %+v, err = %v", out, err)
	}
	cb := e.auth.HandleOAuthCallback(ctx, st.State, "o1:u1")
	if cb.Redirect != "" || cb.AppLink != "" || cb.Status != "ok" {
		t.Fatalf("cb = %+v", cb)
	}
	out, err = e.auth.PollOAuth(ctx, e.client(), st.State)
	if err != nil || out.Status != service.OAuthStatusBindPhone {
		t.Fatalf("回调后 out = %+v, err = %v", out, err)
	}
	_, err = e.auth.PollOAuth(ctx, e.client(), st.State)
	wantOAuthCode(t, err, domain.CodeOAuthStateInvalid)
}

// 配了自定义协议就只走协议，轮询开关再开也不让轮询。
func TestOAuthDesktopSchemeWinsOverPoll(t *testing.T) {
	e := newOAuthEnv(t, map[string]any{"desktopPoll": true, "desktopScheme": "myapp"})
	ctx := context.Background()
	st := e.start(t, service.OAuthModeDesktop)
	cb := e.auth.HandleOAuthCallback(ctx, st.State, "o1:u1")
	if !strings.HasPrefix(cb.AppLink, "myapp://oauth?fpTicket=") {
		t.Fatalf("cb = %+v", cb)
	}
	_, err := e.auth.PollOAuth(ctx, e.client(), st.State)
	wantOAuthCode(t, err, domain.CodeOAuthStateInvalid)

	ticket, _ := url.Parse(cb.AppLink)
	out, err := e.auth.ExchangeOAuth(ctx, e.client(), ticket.Query().Get("fpTicket"))
	if err != nil || out.Status != service.OAuthStatusBindPhone {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}

func TestOAuthReturnURLWhitelist(t *testing.T) {
	e := newOAuthEnv(t, nil)
	_, err := e.auth.StartOAuth(context.Background(), service.OAuthStartInput{
		AppID: e.app, ConnectorType: connector.TypeWechatWeb, Mode: service.OAuthModeWeb, ReturnURL: "https://evil.com/",
	})
	wantOAuthCode(t, err, domain.CodeReturnURLNotAllowed)
}

func TestOAuthCallbackReplayAndCancel(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()

	st := e.start(t, service.OAuthModeWeb)
	e.auth.HandleOAuthCallback(ctx, st.State, "o1:u1")
	if cb := e.auth.HandleOAuthCallback(ctx, st.State, "o1:u1"); cb.Status != "expired" || cb.Redirect != "" {
		t.Fatalf("重放 cb = %+v", cb)
	}

	st = e.start(t, service.OAuthModeWeb)
	cb := e.auth.HandleOAuthCallback(ctx, st.State, "")
	if !strings.Contains(cb.Redirect, "fpError=cancelled") || strings.Contains(cb.Redirect, "fpTicket") {
		t.Fatalf("取消 cb = %+v", cb)
	}

	st = e.start(t, service.OAuthModeWeb)
	cb = e.auth.HandleOAuthCallback(ctx, st.State, "bad")
	if !strings.Contains(cb.Redirect, "fpError=failed") {
		t.Fatalf("失败 cb = %+v", cb)
	}
}

// 绑到一个已经有别的微信的手机号：自动换绑，登录日志留下换绑记录。
func TestOAuthBindReplacesOldWechat(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	first, _ := e.auth.ExchangeOAuth(ctx, e.client(), e.webTicket(t, "old:u-old"))
	u := e.bind(t, first.BindTicket, "13800138000").User

	second, _ := e.auth.ExchangeOAuth(ctx, e.client(), e.webTicket(t, "new:u-new"))
	if second.Status != service.OAuthStatusBindPhone {
		t.Fatalf("second = %+v", second)
	}
	res := e.bind(t, second.BindTicket, "13800138000")
	if res.User.ID != u.ID {
		t.Fatalf("绑到了 %s, want %s", res.User.ID, u.ID)
	}

	// 旧微信再扫：找不到账号，回到绑手机。
	old, _ := e.auth.ExchangeOAuth(ctx, e.client(), e.webTicket(t, "old:u-old"))
	if old.Status != service.OAuthStatusBindPhone {
		t.Fatalf("old = %+v", old)
	}
}

func TestOAuthPublicURLMissing(t *testing.T) {
	e := newOAuthEnv(t, nil)
	// 不给 PublicURL 的 AuthService，只关心 StartOAuth 的拒绝。
	auth := service.NewAuthService(service.AuthDeps{Apps: e.apps, Users: e.users, Registry: connectorRegistryWithWechat(t)})
	_, err := auth.StartOAuth(context.Background(), service.OAuthStartInput{AppID: e.app, ConnectorType: connector.TypeWechatWeb, Mode: service.OAuthModeWeb, ReturnURL: oauthReturnURL})
	wantOAuthCode(t, err, domain.CodePublicURLMissing)
}

func connectorRegistryWithWechat(t *testing.T) *connector.Registry {
	t.Helper()
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewWechatWeb()); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestWechatDirectLoginRejected(t *testing.T) {
	e := newOAuthEnv(t, nil)
	_, err := e.auth.Login(context.Background(), service.LoginInput{
		AppID: e.app, ConnectorType: connector.TypeWechatWeb, Credentials: connector.Credentials{"code": "o1:u1"},
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
}
