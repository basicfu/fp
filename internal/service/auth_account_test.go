package service_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// accountUser 建一个只有手机号的用户。
func (e *oauthEnv) accountUser(t *testing.T, phone string) uuid.UUID {
	t.Helper()
	u, _, _, err := e.users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{Type: domain.IdentityTypePhone, Subject: phone})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	return u.ID
}

func (e *oauthEnv) attach(t *testing.T, userID uuid.UUID, typ, subject, union string) {
	t.Helper()
	if _, err := e.users.AttachIdentity(context.Background(), userID, service.EnsureIdentityInput{Type: typ, Subject: subject, UnionKey: union}); err != nil {
		t.Fatalf("挂 %s:%s: %v", typ, subject, err)
	}
}

func (e *oauthEnv) identities(t *testing.T, userID uuid.UUID) map[string]string {
	t.Helper()
	list, err := e.users.ListIdentities(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, i := range list {
		out[i.Type] = i.Subject
	}
	return out
}

func profileTypes(p *service.UserProfile) map[string]string {
	out := map[string]string{}
	for _, i := range p.Identities {
		out[i.Type] = i.Subject
	}
	return out
}

func TestSendVerifyCodeToCurrentPhoneIsSingleUseAndNotALoginCode(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	const phone = "13800138001"
	uid := e.accountUser(t, phone)

	if err := e.auth.SendVerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, ""); err != nil {
		t.Fatalf("SendVerifyCode: %v", err)
	}
	sent := e.sms.Sent()
	if len(sent) != 1 || sent[0].To != phone || sent[0].Code != service.VerifyCodeTemplate || sent[0].AppID != e.app {
		t.Fatalf("sent = %+v", sent)
	}
	code := e.sms.LastCode()

	// 账号用途的码与登录码互不相通。
	if err := e.codes.Verify(ctx, phone, code); err == nil {
		t.Fatal("账号验证码不应能当登录码用")
	}
	wantOAuthCode(t, e.auth.VerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, "", "000000x"), domain.CodeCodeInvalid)
	if err := e.auth.VerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, "", code); err != nil {
		t.Fatalf("VerifyCode: %v", err)
	}
	wantOAuthCode(t, e.auth.VerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, phone, code), domain.CodeCodeInvalid)
	if got := e.identities(t, uid); len(got) != 1 || got[domain.IdentityTypePhone] != phone {
		t.Fatalf("校验不该改资料：%v", got)
	}
}

func TestSendVerifyCodeRejectsBadInput(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	uid := e.accountUser(t, "13800138002")

	cases := []struct {
		name, channel, target, code string
	}{
		{"没有邮箱", service.VerifyChannelEmail, "", domain.CodeIdentityNotFound},
		{"未知渠道", "wechat", "", domain.CodeInvalidArgument},
		{"手机号格式", service.VerifyChannelSMS, "1380013800", domain.CodePhoneInvalid},
		{"邮箱格式", service.VerifyChannelEmail, "a@b", domain.CodeInvalidArgument},
		{"邮箱两个 @", service.VerifyChannelEmail, "a@b@c.com", domain.CodeInvalidArgument},
	}
	for _, c := range cases {
		wantOAuthCode(t, e.auth.SendVerifyCode(ctx, e.app, uid, c.channel, c.target), c.code)
	}
	wantOAuthCode(t, e.auth.SendVerifyCode(ctx, e.app, uuid.New(), service.VerifyChannelSMS, ""), domain.CodeUserNotFound)

	// 只有微信、没有手机号的用户。
	wx, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypeWechatWeb, Subject: "o-only", UnionKey: "u-only"})
	if err != nil {
		t.Fatal(err)
	}
	wantOAuthCode(t, e.auth.SendVerifyCode(ctx, e.app, wx.ID, service.VerifyChannelSMS, ""), domain.CodeIdentityNotFound)
	if n := len(e.sms.Sent()); n != 0 {
		t.Fatalf("校验失败不该发出任何消息，发了 %d 条", n)
	}
}

func TestSendVerifyCodeEmailUsesEmailTemplate(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	uid := e.accountUser(t, "13800138003")

	if err := e.auth.SendVerifyCode(ctx, e.app, uid, service.VerifyChannelEmail, " Foo@Example.COM "); err != nil {
		t.Fatalf("SendVerifyCode: %v", err)
	}
	sent := e.sms.Sent()
	if len(sent) != 1 || sent[0].To != "foo@example.com" || sent[0].Code != service.DefaultEmailCodeTemplate {
		t.Fatalf("sent = %+v", sent)
	}
	p, err := e.auth.BindIdentity(ctx, e.app, uid, domain.IdentityTypeEmail, "FOO@example.com", e.sms.LastCode())
	if err != nil {
		t.Fatalf("BindIdentity: %v", err)
	}
	if got := profileTypes(p); got[domain.IdentityTypeEmail] != "foo@example.com" || got[domain.IdentityTypePhone] != "13800138003" {
		t.Fatalf("identities = %v", got)
	}
}

func TestSendVerifyCodeToSomeoneElsesPhoneIsTaken(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	uid := e.accountUser(t, "13800138004")
	e.accountUser(t, "13800138005")

	wantOAuthCode(t, e.auth.SendVerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, "13800138005"), domain.CodeIdentityTaken)
	wantOAuthCode(t, mustFail(e.auth.BindIdentity(ctx, e.app, uid, domain.IdentityTypePhone, "13800138005", "123456")), domain.CodeIdentityTaken)
	if n := len(e.sms.Sent()); n != 0 {
		t.Fatalf("号码被占用不该发码，发了 %d 条", n)
	}
	// 发到自己当前的号码不算占用。
	if err := e.auth.SendVerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, "13800138004"); err != nil {
		t.Fatalf("发到自己的号码: %v", err)
	}
}

// 限流按收件人计数，与登录发码共用：同一个号码不会因为换了用途就多收一倍短信。
func TestSendVerifyCodeSharesRateLimitWithLoginCode(t *testing.T) {
	e := newOAuthEnvWith(t, nil)
	ctx := context.Background()
	uid := e.accountUser(t, "13800138006")

	if err := e.auth.SendLoginCode(ctx, e.app, "13800138007"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	wantOAuthCode(t, e.auth.SendVerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, "13800138007"), domain.CodeRateLimited)
	if err := e.auth.SendVerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, ""); err != nil {
		t.Fatalf("另一个号码不受影响: %v", err)
	}
	wantOAuthCode(t, e.auth.SendVerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, ""), domain.CodeRateLimited)
}

func TestBindIdentityReplacesPhoneAndCodeIsBoundToUser(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	const oldPhone, newPhone = "13800138010", "13800138011"
	uid := e.accountUser(t, oldPhone)
	other := e.accountUser(t, "13800138012")

	if err := e.auth.SendVerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, newPhone); err != nil {
		t.Fatalf("SendVerifyCode: %v", err)
	}
	code := e.sms.LastCode()
	// 别的用户拿不走这个码。
	wantOAuthCode(t, mustFail(e.auth.BindIdentity(ctx, e.app, other, domain.IdentityTypePhone, newPhone, code)), domain.CodeCodeInvalid)

	p, err := e.auth.BindIdentity(ctx, e.app, uid, domain.IdentityTypePhone, newPhone, code)
	if err != nil {
		t.Fatalf("BindIdentity: %v", err)
	}
	if got := profileTypes(p); len(got) != 1 || got[domain.IdentityTypePhone] != newPhone {
		t.Fatalf("identities = %v, 应只剩新手机号", got)
	}
	if _, _, err := e.users.FindByIdentity(ctx, domain.IdentityTypePhone, oldPhone); err == nil {
		t.Fatal("旧手机号应已释放")
	}
	wantOAuthCode(t, mustFail(e.auth.BindIdentity(ctx, e.app, uid, domain.IdentityTypePhone, newPhone, code)), domain.CodeCodeInvalid)
	wantOAuthCode(t, mustFail(e.auth.BindIdentity(ctx, e.app, uid, "username", "bob", code)), domain.CodeInvalidArgument)
}

func TestUnbindWechatRemovesAllWechatIdentities(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	uid := e.accountUser(t, "13800138020")
	e.attach(t, uid, domain.IdentityTypeWechatWeb, "o-web", "u1")
	e.attach(t, uid, domain.IdentityTypeWechatApp, "o-app", "u1")
	e.attach(t, uid, domain.IdentityTypeWechatMP, "o-mp", "u1")

	p, err := e.auth.UnbindIdentity(ctx, e.app, uid, service.UnbindKindWechat)
	if err != nil {
		t.Fatalf("UnbindIdentity: %v", err)
	}
	if got := profileTypes(p); len(got) != 1 || got[domain.IdentityTypePhone] == "" {
		t.Fatalf("identities = %v, 应只剩手机号", got)
	}
	wantOAuthCode(t, mustFail(e.auth.UnbindIdentity(ctx, e.app, uid, service.UnbindKindWechat)), domain.CodeIdentityNotFound)
	wantOAuthCode(t, mustFail(e.auth.UnbindIdentity(ctx, e.app, uid, "qq")), domain.CodeInvalidArgument)
	wantOAuthCode(t, mustFail(e.auth.UnbindIdentity(ctx, e.app, uid, domain.IdentityTypePhone)), domain.CodeLastIdentity)
}

func TestUnbindKeepsAtLeastOneLoginMethod(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()

	// 只有微信：解掉就没法登录。
	wx, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypeWechatWeb, Subject: "o2", UnionKey: "u2"})
	if err != nil {
		t.Fatal(err)
	}
	wantOAuthCode(t, mustFail(e.auth.UnbindIdentity(ctx, e.app, wx.ID, service.UnbindKindWechat)), domain.CodeLastIdentity)

	// 邮箱 + 微信：没设密码时邮箱登录不了，设了密码就可以解微信。
	e.attach(t, wx.ID, domain.IdentityTypeEmail, "x@example.com", "")
	wantOAuthCode(t, mustFail(e.auth.UnbindIdentity(ctx, e.app, wx.ID, service.UnbindKindWechat)), domain.CodeLastIdentity)
	if err := e.auth.SetPassword(ctx, e.app, wx.ID, "hunter2hunter2"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.auth.UnbindIdentity(ctx, e.app, wx.ID, service.UnbindKindWechat); err != nil {
		t.Fatalf("有邮箱和密码时解微信: %v", err)
	}

	// 手机号 + 微信：手机号可以解，剩下微信照样能扫码登录。
	uid := e.accountUser(t, "13800138021")
	e.attach(t, uid, domain.IdentityTypeWechatWeb, "o3", "u3")
	p, err := e.auth.UnbindIdentity(ctx, e.app, uid, domain.IdentityTypePhone)
	if err != nil {
		t.Fatalf("解手机号: %v", err)
	}
	if got := profileTypes(p); len(got) != 1 || got[domain.IdentityTypeWechatWeb] != "o3" {
		t.Fatalf("identities = %v", got)
	}
}

func TestAccountOpsRejectFrozenUser(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	uid := e.accountUser(t, "13800138030")
	if _, err := e.users.SetStatus(ctx, uid, domain.UserStatusFrozen); err != nil {
		t.Fatal(err)
	}
	wantOAuthCode(t, e.auth.SendVerifyCode(ctx, e.app, uid, service.VerifyChannelSMS, ""), domain.CodeAccountFrozen)
	wantOAuthCode(t, e.auth.SetPassword(ctx, e.app, uid, "hunter2hunter2"), domain.CodeAccountFrozen)
	wantOAuthCode(t, mustFail(e.auth.UnbindIdentity(ctx, e.app, uid, domain.IdentityTypePhone)), domain.CodeAccountFrozen)
}

func TestAccountSetPassword(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	uid := e.accountUser(t, "13800138031")
	wantOAuthCode(t, e.auth.SetPassword(ctx, e.app, uid, "short"), domain.CodePasswordTooShort)
	wantOAuthCode(t, e.auth.SetPassword(ctx, e.app, uid, strings.Repeat("a", 73)), domain.CodePasswordTooLong)
	pw := "Aa1!" + strings.Repeat("x", 28) // kkdesk 的上限 32 位
	if err := e.auth.SetPassword(ctx, e.app, uid, pw); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if err := e.users.VerifyPassword(ctx, uid, pw); err != nil {
		t.Fatalf("新密码校验不过: %v", err)
	}
}

// startBind 走完 web 模式的发起绑定 → 回调，返回 fpTicket 与回调结果。
func (e *oauthEnv) startBind(t *testing.T, uid uuid.UUID, code string) (string, *service.OAuthCallbackResult) {
	t.Helper()
	ctx := context.Background()
	st, err := e.auth.StartBindOAuth(ctx, service.OAuthStartInput{
		AppID: e.app, ConnectorType: connector.TypeWechatWeb, Mode: service.OAuthModeWeb, ReturnURL: oauthReturnURL,
	}, uid)
	if err != nil {
		t.Fatalf("StartBindOAuth: %v", err)
	}
	cb := e.auth.HandleOAuthCallback(ctx, st.State, code)
	u, err := url.Parse(cb.Redirect)
	if err != nil {
		t.Fatalf("redirect = %q", cb.Redirect)
	}
	return u.Query().Get("fpTicket"), cb
}

func TestBindWechatWebAttachesToUserAndLoginUsesIt(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	uid := e.accountUser(t, "13800138040")
	other := e.accountUser(t, "13800138041")

	// 绑定票据不能拿去登录（那就成了用别人扫的码登录），也不能给别的用户用。
	ticket, cb := e.startBind(t, uid, "ob:ub:小红")
	if !cb.Bind || cb.Status != "ok" {
		t.Fatalf("cb = %+v", cb)
	}
	_, err := e.auth.ExchangeOAuth(ctx, e.client(), ticket)
	wantOAuthCode(t, err, domain.CodeOAuthTicketInvalid)
	ticket, _ = e.startBind(t, uid, "ob:ub:小红")
	wantOAuthCode(t, mustFail(e.auth.ExchangeBindOAuth(ctx, e.app, other, ticket)), domain.CodeOAuthTicketInvalid)
	// 登录票据也不能拿来绑定。
	wantOAuthCode(t, mustFail(e.auth.ExchangeBindOAuth(ctx, e.app, uid, e.webTicket(t, "ob:ub"))), domain.CodeOAuthTicketInvalid)

	ticket, _ = e.startBind(t, uid, "ob:ub:小红")
	out, err := e.auth.ExchangeBindOAuth(ctx, e.app, uid, ticket)
	if err != nil {
		t.Fatalf("ExchangeBindOAuth: %v", err)
	}
	if out.Status != service.OAuthStatusSuccess || out.Nickname != "小红" || out.AvatarURL != "https://img/ob" {
		t.Fatalf("out = %+v", out)
	}
	if got := profileTypes(out.Profile); got[domain.IdentityTypeWechatWeb] != "ob" || got[domain.IdentityTypePhone] != "13800138040" {
		t.Fatalf("identities = %v", got)
	}
	login, err := e.auth.ExchangeOAuth(ctx, e.client(), e.webTicket(t, "ob:ub"))
	if err != nil || login.Status != service.OAuthStatusSuccess || login.Login.User.ID != uid {
		t.Fatalf("绑完扫码应直接登录到该用户：%+v, %v", login, err)
	}
}

func TestBindWechatTakenByOtherUser(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	uid := e.accountUser(t, "13800138050")
	owner := e.accountUser(t, "13800138051")
	// 别人只挂了同一个微信的移动应用 openid：按 unionId 同样算占用。
	e.attach(t, owner, domain.IdentityTypeWechatApp, "o-app", "u-shared")

	ticket, _ := e.startBind(t, uid, "o-web:u-shared")
	wantOAuthCode(t, mustFail(e.auth.ExchangeBindOAuth(ctx, e.app, uid, ticket)), domain.CodeIdentityTaken)
	if got := e.identities(t, uid); len(got) != 1 {
		t.Fatalf("被占用时不该改资料：%v", got)
	}
}

func TestBindWechatReplacesOldWechat(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()
	uid := e.accountUser(t, "13800138060")
	e.attach(t, uid, domain.IdentityTypeWechatWeb, "old", "u-old")
	e.attach(t, uid, domain.IdentityTypeWechatMP, "old-mp", "u-old")

	ticket, _ := e.startBind(t, uid, "new:u-new")
	out, err := e.auth.ExchangeBindOAuth(ctx, e.app, uid, ticket)
	if err != nil {
		t.Fatalf("ExchangeBindOAuth: %v", err)
	}
	got := profileTypes(out.Profile)
	if got[domain.IdentityTypeWechatWeb] != "new" || got[domain.IdentityTypeWechatMP] != "" || len(got) != 2 {
		t.Fatalf("identities = %v, 旧微信（含同 unionId 的服务号 openid）应一并换掉", got)
	}
	// 重复绑同一个微信是幂等的。
	ticket, _ = e.startBind(t, uid, "new:u-new")
	if out, err := e.auth.ExchangeBindOAuth(ctx, e.app, uid, ticket); err != nil || out.Status != service.OAuthStatusSuccess {
		t.Fatalf("重复绑定: %+v, %v", out, err)
	}
}

func TestBindWechatDesktopPollAndCancel(t *testing.T) {
	e := newOAuthEnv(t, map[string]any{"desktopPoll": true})
	ctx := context.Background()
	uid := e.accountUser(t, "13800138070")
	start := func() string {
		st, err := e.auth.StartBindOAuth(ctx, service.OAuthStartInput{AppID: e.app, ConnectorType: connector.TypeWechatWeb, Mode: service.OAuthModeDesktop}, uid)
		if err != nil {
			t.Fatalf("StartBindOAuth: %v", err)
		}
		return st.State
	}

	state := start()
	out, err := e.auth.PollBindOAuth(ctx, e.app, uid, state)
	if err != nil || out.Status != service.OAuthStatusPending {
		t.Fatalf("回调前 out = %+v, err = %v", out, err)
	}
	// 绑定的 state 不能用登录轮询取走。
	_, err = e.auth.PollOAuth(ctx, e.client(), state)
	wantOAuthCode(t, err, domain.CodeOAuthStateInvalid)
	if cb := e.auth.HandleOAuthCallback(ctx, state, "op:up"); !cb.Bind || cb.Status != "ok" {
		t.Fatalf("cb = %+v", cb)
	}
	wantOAuthCode(t, mustFail(e.auth.PollBindOAuth(ctx, e.app, e.accountUser(t, "13800138071"), state)), domain.CodeOAuthStateInvalid)
	out, err = e.auth.PollBindOAuth(ctx, e.app, uid, state)
	if err != nil || out.Status != service.OAuthStatusSuccess || profileTypes(out.Profile)[domain.IdentityTypeWechatWeb] != "op" {
		t.Fatalf("回调后 out = %+v, err = %v", out, err)
	}
	wantOAuthCode(t, mustFail(e.auth.PollBindOAuth(ctx, e.app, uid, state)), domain.CodeOAuthStateInvalid)

	state = start()
	e.auth.HandleOAuthCallback(ctx, state, "")
	if out, err := e.auth.PollBindOAuth(ctx, e.app, uid, state); err != nil || out.Status != service.OAuthStatusCancelled {
		t.Fatalf("取消 out = %+v, err = %v", out, err)
	}
}

// mustFail 丢掉成功值，只留错误，让 wantOAuthCode 能直接套在多返回值的调用上。
func mustFail[T any](_ T, err error) error { return err }
