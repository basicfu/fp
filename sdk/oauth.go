package fpsdk

import (
	"context"
	"time"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// ConnectorWechatWeb 是微信开放平台「网站应用」扫码登录。网页端和桌面 App 都用它。
const ConnectorWechatWeb = "wechat_web"

// 扫码登录的发起模式，由业务方按自己的运行环境判断。
const (
	// OAuthModeWeb：网页，或桌面 App 内嵌的 webview。登录后浏览器回到 ReturnURL?fpTicket=…。
	OAuthModeWeb = "web"
	// OAuthModeDesktop：桌面 App 拉起系统浏览器。结果经自定义协议跳回，或按 state 轮询。
	OAuthModeDesktop = "desktop"
)

// 扫码登录的结论（OAuthResult.Status）。
const (
	OAuthSuccess   = "SUCCESS"
	OAuthBindPhone = "BIND_PHONE"
	OAuthPending   = "PENDING"
	OAuthCancelled = "CANCELLED"
	OAuthFailed    = "FAILED"
)

// 扫码登录可能返回的错误码（*Error 的 Code）。
const (
	// CodeOAuthStateInvalid：state 过期、不存在，或不是按轮询发起的。让用户重新发起。
	CodeOAuthStateInvalid = "OAUTH_STATE_INVALID"
	// CodeOAuthTicketInvalid：fpTicket / bindTicket 过期或已用过。让用户重新扫码。
	CodeOAuthTicketInvalid = "OAUTH_TICKET_INVALID"
	// CodeReturnURLNotAllowed：ReturnURL 不在控制台配置的回跳白名单里。
	CodeReturnURLNotAllowed = "RETURN_URL_NOT_ALLOWED"
	// CodeDesktopModeDisabled：应用既没配桌面协议也没开桌面轮询。
	CodeDesktopModeDisabled = "DESKTOP_MODE_DISABLED"
	// CodePublicURLMissing：fp 系统配置没填 public_url。
	CodePublicURLMissing = "PUBLIC_URL_MISSING"
)

// OAuthStartInput 是一次扫码登录的发起。
type OAuthStartInput struct {
	// Connector 为空时取 ConnectorWechatWeb。
	Connector string
	Mode      string
	// ReturnURL 仅 web 模式需要。
	ReturnURL string
}

// OAuthStart 是发起结果。前端跳转 AuthURL；想把二维码嵌进自己页面的，
// 拿 ProviderAppID / RedirectURI / State 交给微信官方的 wxLogin.js。
type OAuthStart struct {
	State         string
	AuthURL       string
	ProviderAppID string
	RedirectURI   string
	ExpiresIn     time.Duration
}

// OAuthClient 是终端信息，签发会话时写进审计与会话，同 LoginInput 的三项。
type OAuthClient struct {
	IP        string
	UserAgent string
	Mobile    bool
}

// OAuthResult 是换票据 / 轮询的结论。
type OAuthResult struct {
	Status string
	// Login 仅 OAuthSuccess。
	Login *LoginResult
	// BindTicket 仅 OAuthBindPhone：首次登录要绑手机号，拿它调 BindPhone。
	// Nickname / AvatarURL 是微信昵称头像，绑手机页面可以展示。
	BindTicket string
	Nickname   string
	AvatarURL  string
}

// BindPhoneInput 是一次绑手机登录。验证码用 SendLoginCode 发。
type BindPhoneInput struct {
	OAuthClient
	BindTicket string
	Phone      string
	Code       string
}

// StartOAuth 发起扫码登录。
func (a *Auth) StartOAuth(ctx context.Context, in OAuthStartInput) (*OAuthStart, error) {
	if in.Connector == "" {
		in.Connector = ConnectorWechatWeb
	}
	res, err := a.c.rpc.StartOAuthLogin(ctx, &fpv1.StartOAuthLoginRequest{
		ConnectorType: in.Connector, Mode: in.Mode, ReturnUrl: in.ReturnURL,
	})
	if err != nil {
		return nil, translate(err)
	}
	return &OAuthStart{
		State: res.GetState(), AuthURL: res.GetAuthUrl(), ProviderAppID: res.GetProviderAppId(),
		RedirectURI: res.GetRedirectUri(), ExpiresIn: time.Duration(res.GetExpiresInSec()) * time.Second,
	}, nil
}

// ExchangeOAuth 用 fpTicket 换结论。网页端从回跳地址的 fpTicket 参数取，桌面端从自定义协议链接里取。
func (a *Auth) ExchangeOAuth(ctx context.Context, ticket string, c OAuthClient) (*OAuthResult, error) {
	res, err := a.c.rpc.ExchangeOAuthTicket(ctx, &fpv1.ExchangeOAuthTicketRequest{
		Ticket: ticket, Ip: c.IP, UserAgent: c.UserAgent, Mobile: c.Mobile,
	})
	if err != nil {
		return nil, translate(err)
	}
	return oauthResultFrom(res.GetResult()), nil
}

// PollOAuth 是桌面端按 state 轮询，还没扫码时返回 OAuthPending。建议 1.5 秒一次。
func (a *Auth) PollOAuth(ctx context.Context, state string, c OAuthClient) (*OAuthResult, error) {
	res, err := a.c.rpc.PollOAuthLogin(ctx, &fpv1.PollOAuthLoginRequest{
		State: state, Ip: c.IP, UserAgent: c.UserAgent, Mobile: c.Mobile,
	})
	if err != nil {
		return nil, translate(err)
	}
	return oauthResultFrom(res.GetResult()), nil
}

// BindPhone 用绑手机票据加短信验证码完成首次扫码登录。手机号已有账号就绑到那个账号上
// （那个账号原来绑的微信会被换下），没有就新建。验证码错误时票据仍可重试。
func (a *Auth) BindPhone(ctx context.Context, in BindPhoneInput) (*LoginResult, error) {
	res, err := a.c.rpc.BindPhoneLogin(ctx, &fpv1.BindPhoneLoginRequest{
		BindTicket: in.BindTicket, Phone: in.Phone, Code: in.Code,
		Ip: in.IP, UserAgent: in.UserAgent, Mobile: in.Mobile,
	})
	if err != nil {
		return nil, translate(err)
	}
	return loginResultFrom(res.GetLogin()), nil
}

func oauthResultFrom(r *fpv1.OAuthLoginResult) *OAuthResult {
	out := &OAuthResult{
		Status: r.GetStatus(), BindTicket: r.GetBindTicket(), Nickname: r.GetNickname(), AvatarURL: r.GetAvatarUrl(),
	}
	if r.GetLogin() != nil {
		out.Login = loginResultFrom(r.GetLogin())
	}
	return out
}

func loginResultFrom(r *fpv1.LoginResponse) *LoginResult {
	return &LoginResult{Token: r.GetToken(), SessionID: r.GetSessionId(), User: r.GetUser()}
}
