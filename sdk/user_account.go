package fpsdk

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// 账号管理方法可能返回的错误码（*Error 的 Code），另见上面 Users 的那一组与 CodeRateLimited 等。
const (
	// CodeIdentityTaken：要绑定的手机号、邮箱或微信已属于别的用户。哨兵 ErrConflict。
	CodeIdentityTaken = "IDENTITY_TAKEN"
	// CodeLastIdentity：解绑后账号就没有能登录的方式了。哨兵 ErrConflict。
	CodeLastIdentity = "LAST_IDENTITY"
	// CodeIdentityNotFound：用户没有绑定这种标识（发码时 target 为空却没有手机号 / 邮箱，或解绑的本来就没有）。哨兵 ErrNotFound。
	CodeIdentityNotFound = "IDENTITY_NOT_FOUND"
	// CodeCodeInvalid：验证码不对、过期或尝试次数用完。哨兵 ErrInvalidArgument——不是会话失效，别让客户端清登录态。
	CodeCodeInvalid = "CODE_INVALID"
	// CodeRateLimited：发码过于频繁，Detail 里有 retryAfterMs。哨兵 ErrRateLimited。
	CodeRateLimited = "RATE_LIMITED"
	// CodePhoneInvalid：手机号不是 11 位中国大陆号码。哨兵 ErrInvalidArgument。
	CodePhoneInvalid = "PHONE_INVALID"
	// CodePasswordTooShort / CodePasswordTooLong：密码不足 8 个字符 / 超过 72 字节。哨兵 ErrInvalidArgument。
	CodePasswordTooShort = "PASSWORD_TOO_SHORT"
	CodePasswordTooLong  = "PASSWORD_TOO_LONG"
	// CodeVerifyTemplateUnavailable：通知中心里验证码模板没配好。哨兵 ErrUnavailable。
	CodeVerifyTemplateUnavailable = "VERIFY_TEMPLATE_UNAVAILABLE"
	// CodeNotifySendFailed：模板配好了但供应商没发出去。哨兵 ErrUnavailable。
	CodeNotifySendFailed = "NOTIFY_SEND_FAILED"
)

// ErrConflict 操作与现状冲突：标识已被别人占用，或解绑后无法登录。用 *Error 的 Code 区分。
var ErrConflict = errors.New("fpsdk: 与现状冲突")

// 发码渠道（SendVerifyCode / VerifyCode 的 channel）。
const (
	VerifyChannelSMS   = "sms"
	VerifyChannelEmail = "email"
)

// 登录标识类型（BindIdentity / UnbindIdentity 的 typ）。
const (
	IdentityPhone = "phone"
	IdentityEmail = "email"
	// IdentityWechat 只用于 UnbindIdentity：一次删掉全部微信身份（网站应用、移动应用、服务号 openid）。
	IdentityWechat = "wechat"
)

// BindOAuthResult 是扫码绑微信的结论。Status 取 OAuthSuccess / OAuthPending / OAuthCancelled / OAuthFailed。
type BindOAuthResult struct {
	Status string
	// User 仅 OAuthSuccess：绑完的完整资料。
	User *UserProfile
	// Nickname / AvatarURL 仅 OAuthSuccess：这次扫码的微信昵称与头像。fp 不存，要展示请自己存。
	Nickname  string
	AvatarURL string
}

// SendVerifyCode 给用户发一条验证码，用于换绑、设密码之前的身份校验。
//
// target 为空发到当前绑定的手机号（VerifyChannelSMS）或邮箱（VerifyChannelEmail），没有则 CodeIdentityNotFound；
// 非空是要换绑的新标识，已属别人则 CodeIdentityTaken。频率限制与 SendLoginCode 共用按收件人的计数。
// 短信用通知模板 verify_code，邮件用 fp 系统配置 verify_code.email_template（默认 verify_code_email）。
//
// 这里的码只对这个 userID 有效，不能拿去登录。
func (u *Users) SendVerifyCode(ctx context.Context, userID, channel, target string) error {
	_, err := u.c.userRPC.SendVerifyCode(ctx, &fpv1.SendVerifyCodeRequest{UserId: userID, Channel: channel, Target: target})
	return translateAccount(err)
}

// VerifyCode 校验发到 target（空 = 当前绑定的那个）的码，成功即作废，不改任何资料。失败为 CodeCodeInvalid。
func (u *Users) VerifyCode(ctx context.Context, userID, channel, target, code string) error {
	_, err := u.c.userRPC.VerifyCode(ctx, &fpv1.VerifyCodeRequest{UserId: userID, Channel: channel, Target: target, Code: code})
	return translateAccount(err)
}

// BindIdentity 校验发到新 subject 的码后，把用户的手机号（IdentityPhone）或邮箱（IdentityEmail）换成它，
// 返回改完的完整资料。已属别人返回 CodeIdentityTaken。
//
// 要求「先验旧手机号再换新的」之类的流程由业务方自己编排：先 VerifyCode 旧的，再 BindIdentity 新的。
func (u *Users) BindIdentity(ctx context.Context, userID, typ, subject, code string) (*UserProfile, error) {
	p, err := u.c.userRPC.BindIdentity(ctx, &fpv1.BindIdentityRequest{UserId: userID, Type: typ, Subject: subject, Code: code})
	if err != nil {
		return nil, translateAccount(err)
	}
	return userProfileFrom(p), nil
}

// UnbindIdentity 删掉用户某一类登录标识（IdentityPhone / IdentityEmail / IdentityWechat），返回改完的完整资料。
// 删完没有可登录的方式返回 CodeLastIdentity，本来就没有返回 CodeIdentityNotFound。
func (u *Users) UnbindIdentity(ctx context.Context, userID, typ string) (*UserProfile, error) {
	p, err := u.c.userRPC.UnbindIdentity(ctx, &fpv1.UnbindIdentityRequest{UserId: userID, Type: typ})
	if err != nil {
		return nil, translateAccount(err)
	}
	return userProfileFrom(p), nil
}

// StartBindOAuth 发起一次扫码，把微信绑到这个用户上。参数、返回值与 Auth.StartOAuth 相同，
// 之后用 ExchangeBindOAuth（web 模式、桌面协议）或 PollBindOAuth（桌面轮询）取结论。
func (u *Users) StartBindOAuth(ctx context.Context, userID string, in OAuthStartInput) (*OAuthStart, error) {
	conn := in.Connector
	if conn == "" {
		conn = ConnectorWechatWeb
	}
	res, err := u.c.userRPC.StartBindOAuth(ctx, &fpv1.StartBindOAuthRequest{
		UserId: userID, ConnectorType: conn, Mode: in.Mode, ReturnUrl: in.ReturnURL,
	})
	if err != nil {
		return nil, translateAccount(err)
	}
	return &OAuthStart{
		State: res.GetState(), AuthURL: res.GetAuthUrl(), ProviderAppID: res.GetProviderAppId(),
		RedirectURI: res.GetRedirectUri(), ExpiresIn: time.Duration(res.GetExpiresInSec()) * time.Second,
	}, nil
}

// ExchangeBindOAuth 用回调带回的 fpTicket 换绑定结论。微信已属别人返回 CodeIdentityTaken；
// 票据无效、过期、不是为这个用户发起的绑定返回 CodeOAuthTicketInvalid。
func (u *Users) ExchangeBindOAuth(ctx context.Context, userID, ticket string) (*BindOAuthResult, error) {
	res, err := u.c.userRPC.ExchangeBindOAuthTicket(ctx, &fpv1.ExchangeBindOAuthTicketRequest{UserId: userID, Ticket: ticket})
	if err != nil {
		return nil, translateAccount(err)
	}
	return bindOAuthResultFrom(res.GetResult()), nil
}

// PollBindOAuth 按 state 轮询绑定结论，还没扫码时 Status 为 OAuthPending。错误同 ExchangeBindOAuth，
// state 无效为 CodeOAuthStateInvalid。
func (u *Users) PollBindOAuth(ctx context.Context, userID, state string) (*BindOAuthResult, error) {
	res, err := u.c.userRPC.PollBindOAuth(ctx, &fpv1.PollBindOAuthRequest{UserId: userID, State: state})
	if err != nil {
		return nil, translateAccount(err)
	}
	return bindOAuthResultFrom(res.GetResult()), nil
}

// SetPassword 设置或重置密码：8 个字符起、72 字节封顶，更严的规则由业务方自己先校验。不撤销任何会话。
func (u *Users) SetPassword(ctx context.Context, userID, password string) error {
	_, err := u.c.userRPC.SetPassword(ctx, &fpv1.SetPasswordRequest{UserId: userID, Password: password})
	return translateAccount(err)
}

func bindOAuthResultFrom(r *fpv1.BindOAuthResult) *BindOAuthResult {
	out := &BindOAuthResult{Status: r.GetStatus(), Nickname: r.GetNickname(), AvatarURL: r.GetAvatarUrl()}
	if r.GetUser() != nil {
		out.User = userProfileFrom(r.GetUser())
	}
	return out
}

// translateAccount 在 translateUser 之上再收两类：AlreadyExists 归 ErrConflict、ResourceExhausted 归 ErrRateLimited；
// Unauthenticated 在这里只可能是验证码 / 扫码票据不对，归 ErrInvalidArgument——归成 ErrUnauthorized 的话，
// 业务方套 WriteError 回 401，客户端会以为登录态失效而把用户踢回登录页。应用凭据错不在此列，仍是 ErrUnauthorized。
func translateAccount(err error) error {
	if err == nil {
		return nil
	}
	var sentinel error
	switch status.Code(err) {
	case codes.AlreadyExists:
		sentinel = ErrConflict
	case codes.ResourceExhausted:
		sentinel = ErrRateLimited
	case codes.Unauthenticated:
		if fe := errorFrom(err, nil); fe != nil && fe.Code != "APP_CREDENTIAL_INVALID" {
			sentinel = ErrInvalidArgument
		}
	}
	if sentinel == nil {
		return translateUser(err)
	}
	if fe := errorFrom(err, sentinel); fe != nil {
		return fe
	}
	return errors.Join(sentinel, err)
}
