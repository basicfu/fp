package service

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

// 业务方给已登录用户发码的渠道。
const (
	VerifyChannelSMS   = "sms"
	VerifyChannelEmail = "email"
)

// DefaultEmailCodeTemplate 是邮件验证码模板 code 的默认值，与 config.DefaultVerifyEmailTemplate 一致。
const DefaultEmailCodeTemplate = "verify_code_email"

// maxEmailBytes 同 notifyMaxRecipientBytes：超出的地址通知中心也不收。
const maxEmailBytes = 254

// BindOAuthOutcome 是绑微信的结论：Status 取 OAuthStatusSuccess / Pending / Cancelled / Failed。
type BindOAuthOutcome struct {
	Status string
	// Profile、Nickname、AvatarURL 仅 SUCCESS。
	Profile   *UserProfile
	Nickname  string
	AvatarURL string
}

// accountUser 是本文件全部操作共用的前置：应用可用、用户存在且能登录。
// 冻结的账号不许业务方替他改资料：冻结多半是风控，换掉手机号等于帮人转移账号。
func (s *AuthService) accountUser(ctx context.Context, appID string, userID uuid.UUID) (*domain.Application, *domain.User, error) {
	app, err := s.activeApp(ctx, appID)
	if err != nil {
		return nil, nil, err
	}
	u, err := s.deps.Users.GetByID(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if !u.CanLogin() {
		return nil, nil, accountUnavailableError(u)
	}
	return app, u, nil
}

// verifyTarget 把 (channel, target) 解析成规整后的收件人与对应的标识类型。target 为空取用户当前绑定的那个。
func (s *AuthService) verifyTarget(ctx context.Context, userID uuid.UUID, channel, target string) (typ, to string, err error) {
	switch channel {
	case VerifyChannelSMS:
		typ = domain.IdentityTypePhone
	case VerifyChannelEmail:
		typ = domain.IdentityTypeEmail
	default:
		return "", "", domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "channel 只能是 sms 或 email，收到 %q", channel)
	}
	if strings.TrimSpace(target) != "" {
		to, err = normalizeBindSubject(typ, target)
		return typ, to, err
	}
	list, err := s.deps.Users.ListIdentities(ctx, userID)
	if err != nil {
		return "", "", err
	}
	for _, i := range list {
		if i.Type == typ {
			return typ, i.Subject, nil
		}
	}
	if typ == domain.IdentityTypePhone {
		return "", "", domain.Fail(domain.ErrNotFound, domain.CodeIdentityNotFound, "还没有绑定手机号")
	}
	return "", "", domain.Fail(domain.ErrNotFound, domain.CodeIdentityNotFound, "还没有绑定邮箱")
}

// normalizeBindSubject 校验并规整要绑定的手机号 / 邮箱。
func normalizeBindSubject(typ, subject string) (string, error) {
	subject = normalizeSubject(typ, subject)
	switch typ {
	case domain.IdentityTypePhone:
		if connector.DetectIdentityType(subject) != domain.IdentityTypePhone {
			return "", domain.Fail(domain.ErrInvalidArgument, domain.CodePhoneInvalid, "手机号格式不正确")
		}
	case domain.IdentityTypeEmail:
		if !looksLikeEmail(subject) {
			return "", domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "邮箱格式不正确")
		}
	default:
		return "", domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "type 只能是 phone 或 email，收到 %q", typ)
	}
	return subject, nil
}

// looksLikeEmail 只拦明显不是邮箱的输入；地址到底收不收得到，靠验证码本身证明。
func looksLikeEmail(s string) bool {
	if len(s) > maxEmailBytes || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at != strings.LastIndexByte(s, '@') {
		return false
	}
	domainPart := s[at+1:]
	dot := strings.LastIndexByte(domainPart, '.')
	return dot > 0 && dot < len(domainPart)-1
}

// ensureNotTakenByOthers 在发码、校验之前先挡住别人的标识：否则用户收了码、输完了，才被告知号码已被占用。
func (s *AuthService) ensureNotTakenByOthers(ctx context.Context, userID uuid.UUID, typ, subject string) error {
	owner, _, err := s.deps.Users.FindByIdentity(ctx, typ, subject)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return nil
	case err != nil:
		return err
	case owner.ID != userID:
		return identityTaken()
	}
	return nil
}

// SendVerifyCode 给已登录用户发一条验证码。target 非空时是要换绑的新标识，已属别人回 IDENTITY_TAKEN。
// 限流与登录发码共用按收件人计数的那套规则：同一个号码不会因为换了用途就多收一倍短信。
func (s *AuthService) SendVerifyCode(ctx context.Context, appID string, userID uuid.UUID, channel, target string) error {
	app, _, err := s.accountUser(ctx, appID, userID)
	if err != nil {
		return err
	}
	typ, to, err := s.verifyTarget(ctx, userID, channel, target)
	if err != nil {
		return err
	}
	if strings.TrimSpace(target) != "" {
		if err := s.ensureNotTakenByOthers(ctx, userID, typ, to); err != nil {
			return err
		}
	}
	if err := s.deps.Codes.AllowSend(ctx, to); err != nil {
		return err
	}
	code, err := s.deps.Codes.IssueAccount(ctx, userID, to)
	if err != nil {
		return err
	}
	if channel == VerifyChannelSMS {
		return s.sendVerifyCode(ctx, app, VerifyCodeTemplate, "短信", to, code)
	}
	tpl := s.deps.EmailCodeTemplate
	if tpl == "" {
		tpl = DefaultEmailCodeTemplate
	}
	return s.sendVerifyCode(ctx, app, tpl, "邮件", to, code)
}

// VerifyCode 校验 SendVerifyCode 发出的码，成功即作废。
func (s *AuthService) VerifyCode(ctx context.Context, appID string, userID uuid.UUID, channel, target, code string) error {
	if _, _, err := s.accountUser(ctx, appID, userID); err != nil {
		return err
	}
	_, to, err := s.verifyTarget(ctx, userID, channel, target)
	if err != nil {
		return err
	}
	return s.deps.Codes.VerifyAccount(ctx, userID, to, strings.TrimSpace(code))
}

// BindIdentity 校验发到新标识的码后，把用户的手机号 / 邮箱换成它。
func (s *AuthService) BindIdentity(ctx context.Context, appID string, userID uuid.UUID, typ, subject, code string) (*UserProfile, error) {
	if _, _, err := s.accountUser(ctx, appID, userID); err != nil {
		return nil, err
	}
	subject, err := normalizeBindSubject(typ, subject)
	if err != nil {
		return nil, err
	}
	// 先查占用再验码：被占用时不该白白消费掉用户手里的码。事务里还会再查一次，挡并发。
	if err := s.ensureNotTakenByOthers(ctx, userID, typ, subject); err != nil {
		return nil, err
	}
	if err := s.deps.Codes.VerifyAccount(ctx, userID, subject, strings.TrimSpace(code)); err != nil {
		return nil, err
	}
	if _, err := s.deps.Users.ReplaceIdentity(ctx, userID, typ, subject); err != nil {
		return nil, err
	}
	return s.deps.Users.GetProfile(ctx, userID)
}

// UnbindIdentity 删掉用户某一类登录标识（phone / email / wechat），删完没法登录时拒绝。
func (s *AuthService) UnbindIdentity(ctx context.Context, appID string, userID uuid.UUID, kind string) (*UserProfile, error) {
	if _, _, err := s.accountUser(ctx, appID, userID); err != nil {
		return nil, err
	}
	if err := s.deps.Users.RemoveIdentities(ctx, userID, kind); err != nil {
		return nil, err
	}
	return s.deps.Users.GetProfile(ctx, userID)
}

// SetPassword 设置或重置密码，不撤销会话。规则同 UserService.SetPassword。
func (s *AuthService) SetPassword(ctx context.Context, appID string, userID uuid.UUID, plain string) error {
	if _, _, err := s.accountUser(ctx, appID, userID); err != nil {
		return err
	}
	return s.deps.Users.SetPassword(ctx, userID, plain)
}

// StartBindOAuth 发起一次扫码，把三方身份（微信）绑到这个用户上。参数与 StartOAuth 相同。
func (s *AuthService) StartBindOAuth(ctx context.Context, in OAuthStartInput, userID uuid.UUID) (*OAuthStart, error) {
	if _, _, err := s.accountUser(ctx, in.AppID, userID); err != nil {
		return nil, err
	}
	return s.startOAuth(ctx, in, userID)
}

// PollBindOAuth 按 state 轮询绑定结论。只对以轮询方式、为这个用户发起的 state 有效。
func (s *AuthService) PollBindOAuth(ctx context.Context, appID string, userID uuid.UUID, state string) (*BindOAuthOutcome, error) {
	app, _, err := s.accountUser(ctx, appID, userID)
	if err != nil {
		return nil, err
	}
	invalid := domain.Fail(domain.ErrInvalidCredential, domain.CodeOAuthStateInvalid, "绑定已过期，请重新扫码")
	if s.deps.OAuth == nil || state == "" {
		return nil, invalid
	}
	st, err := s.deps.OAuth.getState(ctx, state)
	if err != nil {
		return nil, err
	}
	if st == nil || st.AppID != app.ID || st.Deliver != oauthDeliverPoll || st.BindUser != userID {
		return nil, invalid
	}
	out, err := s.deps.OAuth.takeResult(ctx, state)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return &BindOAuthOutcome{Status: OAuthStatusPending}, nil
	}
	return s.settleBind(ctx, app, userID, st.Connector, *out)
}

// ExchangeBindOAuth 用 fpTicket 换绑定结论。票据必须是为这个用户发起的绑定。
func (s *AuthService) ExchangeBindOAuth(ctx context.Context, appID string, userID uuid.UUID, ticket string) (*BindOAuthOutcome, error) {
	app, _, err := s.accountUser(ctx, appID, userID)
	if err != nil {
		return nil, err
	}
	if s.deps.OAuth == nil || ticket == "" {
		return nil, ticketInvalid()
	}
	t, err := s.deps.OAuth.takeTicket(ctx, ticket)
	if err != nil {
		return nil, err
	}
	if t == nil || t.AppID != app.ID || t.BindUser != userID {
		return nil, ticketInvalid()
	}
	return s.settleBind(ctx, app, userID, t.Connector, t.Outcome)
}

func (s *AuthService) settleBind(ctx context.Context, app *domain.Application, userID uuid.UUID, connType string, out oauthOutcome) (*BindOAuthOutcome, error) {
	switch out.Status {
	case oauthCancelled:
		return &BindOAuthOutcome{Status: OAuthStatusCancelled}, nil
	case oauthOK:
	default:
		return &BindOAuthOutcome{Status: OAuthStatusFailed}, nil
	}
	if _, _, err := s.thirdPartyConnectorFor(ctx, app, connType); err != nil {
		return nil, err
	}
	p := out.Profile
	if _, err := s.deps.Users.BindThirdParty(ctx, userID, EnsureIdentityInput{
		Type: p.IdentityType, Subject: p.Subject, UnionKey: p.UnionKey,
	}); err != nil {
		return nil, err
	}
	u, err := s.deps.Users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	s.fillProfileFrom(ctx, u, p)
	prof, err := s.deps.Users.GetProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &BindOAuthOutcome{Status: OAuthStatusSuccess, Profile: prof, Nickname: p.Nickname, AvatarURL: p.AvatarURL}, nil
}
