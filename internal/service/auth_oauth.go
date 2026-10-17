package service

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

// OAuthCallbackPath 是三方回调在 fp 上的路径，拼在 public_url 后面。
const OAuthCallbackPath = "/oauth/callback"

// 三方扫码登录的发起模式。
const (
	OAuthModeWeb     = "web"
	OAuthModeDesktop = "desktop"
)

// 三方扫码登录对发起方给出的结论。
const (
	OAuthStatusSuccess   = "SUCCESS"
	OAuthStatusBindPhone = "BIND_PHONE"
	OAuthStatusPending   = "PENDING"
	OAuthStatusCancelled = "CANCELLED"
	OAuthStatusFailed    = "FAILED"
)

// OAuthStartInput 是一次扫码登录的发起。
type OAuthStartInput struct {
	AppID         string
	ConnectorType string
	// Mode 取 OAuthModeWeb / OAuthModeDesktop，由业务方按自己的运行环境判断。
	Mode string
	// ReturnURL 仅 web 模式：登录完成后浏览器回到的业务页面，必须命中白名单。
	ReturnURL string
}

// OAuthStart 是发起结果。业务方要么跳转 AuthURL，要么拿后三项嵌入官方登录组件。
type OAuthStart struct {
	State         string
	AuthURL       string
	ProviderAppID string
	RedirectURI   string
	ExpiresInSec  int64
}

// OAuthClient 是业务后端代为转交的终端信息，签发会话时用。
type OAuthClient struct {
	AppID  string
	IP     string
	UA     string
	Mobile bool
}

// OAuthOutcome 是 Exchange / Poll 的结论。
type OAuthOutcome struct {
	Status string
	// Login 仅 SUCCESS。
	Login *LoginResult
	// BindTicket、Nickname、AvatarURL 仅 BIND_PHONE：昵称头像给绑手机页面展示用。
	BindTicket string
	Nickname   string
	AvatarURL  string
}

// BindPhoneInput 是一次绑手机登录。
type BindPhoneInput struct {
	OAuthClient
	BindTicket string
	Phone      string
	Code       string
}

// OAuthCallbackResult 告诉 HTTP 层浏览器接下来去哪。
type OAuthCallbackResult struct {
	// Redirect 非空时 302 过去（web 模式）。
	Redirect string
	// AppLink 非空时结果页跳桌面 App（自定义协议）。
	AppLink string
	// Status 给结果页选文案：ok / cancelled / failed / expired。
	Status string
}

// StartOAuth 发起一次扫码登录。
func (s *AuthService) StartOAuth(ctx context.Context, in OAuthStartInput) (*OAuthStart, error) {
	app, err := s.activeApp(ctx, in.AppID)
	if err != nil {
		return nil, err
	}
	conn, cfg, err := s.oauthConnectorFor(ctx, app, in.ConnectorType)
	if err != nil {
		return nil, err
	}
	if s.deps.PublicURL == "" || s.deps.OAuth == nil {
		return nil, domain.Fail(domain.ErrInternal, domain.CodePublicURLMissing, "扫码登录暂不可用").
			WithDesc("系统配置没填 public_url，拼不出三方回调地址")
	}

	st := oauthState{AppID: app.ID, Connector: in.ConnectorType}
	switch in.Mode {
	case OAuthModeWeb:
		if !connector.ReturnURLAllowed(connector.OAuthReturnURLs(cfg), in.ReturnURL) {
			return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeReturnURLNotAllowed, "回跳地址不在白名单内").
				WithField("returnUrl", in.ReturnURL)
		}
		st.Deliver, st.ReturnURL = oauthDeliverWeb, in.ReturnURL
	case OAuthModeDesktop:
		// 配了协议就只走协议：轮询有扫码钓鱼风险，有更安全的路就不开它。
		if scheme := connector.ConfigString(cfg, connector.OAuthKeyDesktopScheme, ""); scheme != "" {
			st.Deliver, st.Scheme = oauthDeliverScheme, scheme
		} else if connector.ConfigBool(cfg, connector.OAuthKeyDesktopPoll, false) {
			st.Deliver = oauthDeliverPoll
		} else {
			return nil, domain.Fail(domain.ErrForbidden, domain.CodeDesktopModeDisabled, "该应用未开放桌面端扫码登录")
		}
	default:
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "mode 只能是 web 或 desktop，收到 %q", in.Mode)
	}

	state, err := randomToken()
	if err != nil {
		return nil, err
	}
	if err := s.deps.OAuth.putState(ctx, state, st); err != nil {
		return nil, err
	}
	redirectURI := s.deps.PublicURL + OAuthCallbackPath
	return &OAuthStart{
		State:         state,
		AuthURL:       conn.AuthorizeURL(cfg, redirectURI, state),
		ProviderAppID: conn.ProviderAppID(cfg),
		RedirectURI:   redirectURI,
		ExpiresInSec:  int64(oauthStateTTL.Seconds()),
	}, nil
}

// HandleOAuthCallback 处理三方回调。只换身份、不签会话：会话等发起方来取时才签，
// 那时才有业务后端转交的 IP / UA。错误不往外抛，全部折成结果页状态——对面是终端用户的浏览器。
func (s *AuthService) HandleOAuthCallback(ctx context.Context, state, code string) *OAuthCallbackResult {
	expired := &OAuthCallbackResult{Status: "expired"}
	if state == "" || s.deps.OAuth == nil {
		return expired
	}
	st, err := s.deps.OAuth.getState(ctx, state)
	if err != nil {
		slog.Error("service: 读取 oauth state 失败", "err", err)
		return &OAuthCallbackResult{Status: oauthFailed}
	}
	if st == nil {
		return expired
	}
	if claimed, err := s.deps.OAuth.claimCallback(ctx, state); err != nil || !claimed {
		if err != nil {
			slog.Error("service: 占用 oauth 回调失败", "err", err)
		}
		return expired
	}

	out := s.exchangeCode(ctx, st, code)
	switch st.Deliver {
	case oauthDeliverPoll:
		if err := s.deps.OAuth.putResult(ctx, state, out); err != nil {
			slog.Error("service: 写入 oauth 轮询结果失败", "err", err)
			return &OAuthCallbackResult{Status: oauthFailed}
		}
		return &OAuthCallbackResult{Status: out.Status}
	case oauthDeliverWeb, oauthDeliverScheme:
		s.deps.OAuth.deleteState(ctx, state)
		q := url.Values{}
		if out.Status == oauthOK {
			ticket, err := s.deps.OAuth.putTicket(ctx, oauthTicket{AppID: st.AppID, Connector: st.Connector, Outcome: out})
			if err != nil {
				slog.Error("service: 写入 oauth 票据失败", "err", err)
				out.Status = oauthFailed
			} else {
				q.Set("fpTicket", ticket)
			}
		}
		if out.Status != oauthOK {
			q.Set("fpError", out.Status)
		}
		if st.Deliver == oauthDeliverWeb {
			return &OAuthCallbackResult{Status: out.Status, Redirect: appendQuery(st.ReturnURL, q)}
		}
		return &OAuthCallbackResult{Status: out.Status, AppLink: st.Scheme + "://oauth?" + q.Encode()}
	}
	return &OAuthCallbackResult{Status: oauthFailed}
}

func (s *AuthService) exchangeCode(ctx context.Context, st *oauthState, code string) oauthOutcome {
	// 用户在授权页点了取消，微信回调时不带 code。
	if code == "" {
		return oauthOutcome{Status: oauthCancelled}
	}
	app, err := s.deps.Apps.GetByID(ctx, st.AppID)
	if err != nil {
		slog.Error("service: oauth 回调读取应用失败", "err", err, "appId", st.AppID)
		return oauthOutcome{Status: oauthFailed}
	}
	conn, cfg, err := s.oauthConnectorFor(ctx, app, st.Connector)
	if err != nil {
		slog.Warn("service: oauth 回调时登录方式不可用", "err", err, "appId", app.AppID, "connector", st.Connector)
		return oauthOutcome{Status: oauthFailed}
	}
	p, err := conn.Exchange(ctx, cfg, code)
	if err != nil {
		slog.Error("service: 三方 code 换身份失败", "err", err, "detail", detailOf(err), "appId", app.AppID, "connector", st.Connector)
		return oauthOutcome{Status: oauthFailed}
	}
	return oauthOutcome{Status: oauthOK, Profile: p}
}

// ExchangeOAuth 用 web / 协议模式拿到的 fpTicket 换结论。
func (s *AuthService) ExchangeOAuth(ctx context.Context, in OAuthClient, ticket string) (*OAuthOutcome, error) {
	app, err := s.activeApp(ctx, in.AppID)
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
	if t == nil || t.AppID != app.ID {
		return nil, ticketInvalid()
	}
	return s.settle(ctx, app, in, t.Connector, t.Outcome)
}

// PollOAuth 是桌面端轮询。只对按轮询发起的 state 开放：web 模式开放轮询，
// 发起登录的人就能取走完成扫码的人的会话。
func (s *AuthService) PollOAuth(ctx context.Context, in OAuthClient, state string) (*OAuthOutcome, error) {
	app, err := s.activeApp(ctx, in.AppID)
	if err != nil {
		return nil, err
	}
	invalid := domain.Fail(domain.ErrInvalidCredential, domain.CodeOAuthStateInvalid, "登录已过期，请重新发起")
	if s.deps.OAuth == nil || state == "" {
		return nil, invalid
	}
	st, err := s.deps.OAuth.getState(ctx, state)
	if err != nil {
		return nil, err
	}
	if st == nil || st.AppID != app.ID || st.Deliver != oauthDeliverPoll {
		return nil, invalid
	}
	out, err := s.deps.OAuth.takeResult(ctx, state)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return &OAuthOutcome{Status: OAuthStatusPending}, nil
	}
	return s.settle(ctx, app, in, st.Connector, *out)
}

// settle 把回调结论变成对发起方的结论：老用户且有手机号就签会话，否则发绑手机票据。
func (s *AuthService) settle(ctx context.Context, app *domain.Application, in OAuthClient, connType string, out oauthOutcome) (*OAuthOutcome, error) {
	switch out.Status {
	case oauthCancelled:
		return &OAuthOutcome{Status: OAuthStatusCancelled}, nil
	case oauthOK:
	default:
		return &OAuthOutcome{Status: OAuthStatusFailed}, nil
	}
	// 回调之后管理员可能关掉了这种登录方式。
	if _, _, err := s.oauthConnectorFor(ctx, app, connType); err != nil {
		return nil, err
	}
	p := out.Profile

	user, identity, err := s.deps.Users.FindByIdentity(ctx, p.IdentityType, p.Subject)
	if errors.Is(err, domain.ErrNotFound) && p.UnionKey != "" {
		user, err = s.deps.Users.FindByUnionKey(ctx, p.UnionKey)
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	if user != nil {
		hasPhone, err := s.hasPhone(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		if hasPhone {
			if identity == nil {
				// unionId 归并到的老用户：把这个 openid 也挂上，下次按 openid 直接命中。
				if identity, err = s.deps.Users.AttachIdentity(ctx, user.ID, EnsureIdentityInput{
					Type: p.IdentityType, Subject: p.Subject, UnionKey: p.UnionKey,
				}); err != nil {
					return nil, err
				}
			}
			res, err := s.completeLogin(ctx, app, loginInputFrom(in, connType), profileResult(p), user, identity, false, "")
			if err != nil {
				return nil, err
			}
			return &OAuthOutcome{Status: OAuthStatusSuccess, Login: res}, nil
		}
	}

	ticket, err := s.deps.OAuth.putBind(ctx, oauthTicket{AppID: app.ID, Connector: connType, Outcome: out})
	if err != nil {
		return nil, err
	}
	return &OAuthOutcome{Status: OAuthStatusBindPhone, BindTicket: ticket, Nickname: p.Nickname, AvatarURL: p.AvatarURL}, nil
}

// BindPhone 用绑手机票据加短信验证码完成首次登录。
func (s *AuthService) BindPhone(ctx context.Context, in BindPhoneInput) (*LoginResult, error) {
	app, err := s.activeApp(ctx, in.AppID)
	if err != nil {
		return nil, err
	}
	if s.deps.OAuth == nil || in.BindTicket == "" {
		return nil, ticketInvalid()
	}
	t, err := s.deps.OAuth.peekBind(ctx, in.BindTicket)
	if err != nil {
		return nil, err
	}
	if t == nil || t.AppID != app.ID || t.Outcome.Profile == nil {
		return nil, ticketInvalid()
	}
	if _, _, err := s.oauthConnectorFor(ctx, app, t.Connector); err != nil {
		return nil, err
	}
	login := loginInputFrom(in.OAuthClient, t.Connector)

	phone := strings.TrimSpace(in.Phone)
	if connector.DetectIdentityType(phone) != domain.IdentityTypePhone {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodePhoneInvalid, "手机号格式不正确")
	}
	if err := s.deps.Codes.Verify(ctx, phone, strings.TrimSpace(in.Code)); err != nil {
		s.logFailure(ctx, app, login, domain.IdentityTypePhone, phone, err)
		return nil, err
	}
	if t, err = s.deps.OAuth.takeBind(ctx, in.BindTicket); err != nil {
		return nil, err
	}
	if t == nil {
		return nil, ticketInvalid()
	}

	p := t.Outcome.Profile
	res, err := s.deps.Users.LinkPhone(ctx, LinkPhoneInput{Phone: phone, Identity: EnsureIdentityInput{
		Type: p.IdentityType, Subject: p.Subject, UnionKey: p.UnionKey, Nickname: p.Nickname, AvatarURL: p.AvatarURL,
	}})
	if err != nil {
		s.logFailure(ctx, app, login, p.IdentityType, p.Subject, err)
		return nil, err
	}
	var note string
	if len(res.Replaced) > 0 {
		olds := make([]string, 0, len(res.Replaced))
		for _, old := range res.Replaced {
			olds = append(olds, old.Type+":"+MaskSubject(old.Type, old.Subject))
		}
		note = "绑定新微信，换下 " + strings.Join(olds, "、")
	}
	return s.completeLogin(ctx, app, login, profileResult(p), res.User, res.Identity, res.Created, note)
}

// oauthConnectorFor 取出应用启用的、属于扫码登录一类的登录方式。
func (s *AuthService) oauthConnectorFor(ctx context.Context, app *domain.Application, typ string) (connector.OAuthConnector, map[string]any, error) {
	conn, cfg, err := s.connectorFor(ctx, app, typ)
	if err != nil {
		return nil, nil, err
	}
	oc, ok := conn.(connector.OAuthConnector)
	if !ok {
		return nil, nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "%s 不是扫码登录方式", typ)
	}
	return oc, cfg, nil
}

// phoneBindingEnabled 报告应用是否开了任何一种首次登录要绑手机的登录方式。
func (s *AuthService) phoneBindingEnabled(ctx context.Context, app *domain.Application) bool {
	for _, typ := range s.deps.Registry.Types() {
		conn, err := s.deps.Registry.Get(typ)
		if err != nil {
			continue
		}
		if _, ok := conn.(connector.OAuthConnector); !ok {
			continue
		}
		if _, err := s.enabledConnector(ctx, app, typ); err == nil {
			return true
		}
	}
	return false
}

func (s *AuthService) hasPhone(ctx context.Context, userID uuid.UUID) (bool, error) {
	list, err := s.deps.Users.ListIdentities(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, i := range list {
		if i.Type == domain.IdentityTypePhone {
			return true, nil
		}
	}
	return false, nil
}

func loginInputFrom(in OAuthClient, connType string) LoginInput {
	return LoginInput{AppID: in.AppID, ConnectorType: connType, IP: in.IP, UA: in.UA, Mobile: in.Mobile}
}

func profileResult(p *connector.OAuthProfile) *connector.Result {
	return &connector.Result{IdentityType: p.IdentityType, Subject: p.Subject, UnionKey: p.UnionKey}
}

func ticketInvalid() error {
	return domain.Fail(domain.ErrInvalidCredential, domain.CodeOAuthTicketInvalid, "登录已过期，请重新扫码")
}

// appendQuery 把 q 追加到 raw 的查询串上，保留 raw 原有参数与锚点。
func appendQuery(raw string, q url.Values) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	merged := u.Query()
	for k, vs := range q {
		merged[k] = vs
	}
	u.RawQuery = merged.Encode()
	return u.String()
}

func detailOf(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.DetailJSON()
	}
	return ""
}
