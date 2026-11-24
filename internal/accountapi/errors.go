package accountapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/basicfu/fp/internal/domain"
)

// errTable 整张照搬业务方原实现(设备、分组类错误码留在业务方)。客户端把 404 当"接口未上线",
// 所以业务码一个都不用 404。
var errTable = map[string]struct {
	status int
	msg    string
}{
	"bad_request":  {http.StatusBadRequest, "请求参数不正确"},
	"login_failed": {http.StatusUnauthorized, "账号或验证码不正确"},

	"captcha_failed":  {http.StatusBadRequest, "验证失败，请重试"},
	"captcha_expired": {http.StatusBadRequest, "验证已过期，请重试"},
	// me 发出的这个码是客户端清除本地登录态的唯一依据; 写接口上它表示没带有效 token。
	"unauthorized":     {http.StatusUnauthorized, "登录已过期，请重新登录"},
	"rate_limited":     {http.StatusTooManyRequests, "操作过于频繁，请稍后再试"},
	"unavailable":      {http.StatusServiceUnavailable, "登录服务暂不可用，请稍后重试"},
	"internal":         {http.StatusInternalServerError, "服务器开小差了，请稍后重试"},
	"nickname_invalid": {http.StatusBadRequest, "昵称不符合要求"},
	"avatar_invalid":   {http.StatusBadRequest, "头像图片格式不支持"},
	"avatar_too_large": {http.StatusRequestEntityTooLarge, "头像图片不能超过 256 KB"},
	"not_found":        {http.StatusNotFound, "头像不存在"},
	// 不用 401: 那会让客户端以为登录过期而清掉登录态。客户端按服务端时间校正、重签后自动重试一次。
	"device_unverified": {http.StatusForbidden, "设备校验失败，请检查系统时间后重试"},
	// 会话被别的设备"从账号移除"时作废了。客户端据此清掉登录态并弹专门的提示。
	"kicked":           {http.StatusUnauthorized, "该账号已被其他设备操作退出，如需登录，请重新验证。"},
	"wechat_expired":   {http.StatusBadRequest, "二维码已过期，请刷新后重新扫码"},
	"wechat_cancelled": {http.StatusBadRequest, "已取消微信登录"},
	// 不带设备头的客户端绑不了设备,让它升级而不是报一句时钟问题。
	"version_too_low": {http.StatusBadRequest, "客户端版本过低，请升级到最新版本后再登录"},
	"forbidden":       {http.StatusForbidden, "当前账号状态不支持注销"},

	"code_invalid":   {http.StatusBadRequest, "验证码不正确或已过期"},
	"no_phone":       {http.StatusBadRequest, "请先绑定手机号"},
	"no_email":       {http.StatusBadRequest, "请先绑定邮箱"},
	"phone_taken":    {http.StatusConflict, "该手机号已注册"},
	"email_taken":    {http.StatusConflict, "该邮箱已注册"},
	"ticket_invalid": {http.StatusBadRequest, "验证已过期，请重新验证身份"},
	// 换绑填的就是自己当前的号:白白消耗一次验证码和发送额度,发码时就拦下。
	"phone_same": {http.StatusBadRequest, "新手机号不能与当前手机号相同"},
	"email_same": {http.StatusBadRequest, "新邮箱不能与当前邮箱相同"},
	// 收件方明确拒收(阿里云邮件推送 559 无效地址等):多半是地址填错了,文案由 fp 按渠道给。
	"recipient_rejected": {http.StatusBadRequest, "收件地址无法接收验证码，请检查是否填写正确"},
	"wechat_taken":       {http.StatusConflict, "该微信已绑定其他账号"},
	"wechat_failed":      {http.StatusBadRequest, "微信绑定失败，请重试"},
	"last_identity":      {http.StatusConflict, "无法解绑微信，请先绑定手机号"},
	"password_invalid":   {http.StatusBadRequest, "密码需为 8–32 位，且至少包含大写字母、小写字母、数字、特殊字符中的两种"},
}

const msgServiceUnavailable = "服务暂不可用，请稍后重试"

func asDomain(err error) (code, msg string, de *domain.Error) {
	if errors.As(err, &de) {
		return de.Code, de.Msg, de
	}
	return "", "", nil
}

// internalish 是"不是调用方的问题":领域里归 Internal 的(平台没配好、上游挂了),或者压根不是领域错误
// (Redis、PG 故障)。原实现经 gRPC 拿到的同类错误都落在 SDK 的 ErrUnavailable 上。
func internalish(err error) bool {
	var de *domain.Error
	return !errors.As(err, &de) || errors.Is(err, domain.ErrInternal)
}

func canceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// loginErr 对应原实现 writeFPErr:登录、发登录短信。结构化错误自带的文案是给用户看的(限流剩余时间、账号冻结),优先透传。
func (h *handler) loginErr(w http.ResponseWriter, op string, err error) {
	code, msg, de := asDomain(err)
	switch {
	// 应用没开这种登录方式:按"禁止"报会让用户以为是自己输错了。
	case code == domain.CodeNotifyRecipientRejected:
		h.writeErr(w, "recipient_rejected", msg)
	case code == domain.CodeConnectorDisabled, code == domain.CodeNotifySendFailed:
		slog.Warn("accountapi: 平台侧未就绪", "op", op, "code", code, "err", err)
		h.writeErr(w, "unavailable", "")
	case internalish(err) && !canceled(err):
		slog.Warn("accountapi: 服务不可用", "op", op, "code", code, "err", err)
		h.writeErr(w, "unavailable", "")
	case errors.Is(err, domain.ErrRateLimited):
		h.writeErr(w, "rate_limited", msg)
	case errors.Is(err, domain.ErrInvalidArgument):
		h.writeErr(w, "bad_request", msg)
	case de != nil:
		h.writeErr(w, "login_failed", msg)
	default:
		h.internal(w, op, err)
	}
}

// wechatErr 先认扫码特有的几种,其余同 loginErr。
func (h *handler) wechatErr(w http.ResponseWriter, op string, err error) {
	code, _, _ := asDomain(err)
	switch code {
	case domain.CodeOAuthStateInvalid, domain.CodeOAuthTicketInvalid:
		h.writeErr(w, "wechat_expired", "")
	case domain.CodeConnectorDisabled, domain.CodeReturnURLNotAllowed, domain.CodePublicURLMissing, domain.CodeDesktopModeDisabled:
		// 都是控制台没配好:用户看到的只是"暂未开放",要配什么只在日志里。
		slog.Warn("accountapi: 微信登录未就绪", "op", op, "code", code, "err", err)
		h.writeErr(w, "unavailable", msgWechatOff)
	default:
		h.loginErr(w, op, err)
	}
}

// usersErr 对应原实现 writeUsersErr:改资料、传头像。
func (h *handler) usersErr(w http.ResponseWriter, op string, err error) {
	code, msg, _ := asDomain(err)
	switch {
	case code == domain.CodeNicknameInvalid:
		h.writeErr(w, "nickname_invalid", msg)
	case code == domain.CodeAvatarInvalid:
		h.writeErr(w, "avatar_invalid", msg)
	case code == domain.CodeAvatarNotFound:
		h.writeErr(w, "not_found", "")
	case code == domain.CodeUserNotFound:
		// token 有效而用户查不到: 账号刚被删。不回 unauthorized —— 那是 me 的事, 这里只说这次没成。
		h.writeErr(w, "not_found", "用户不存在")
	case internalish(err) && !canceled(err):
		slog.Warn("accountapi: 服务不可用", "op", op, "code", code, "err", err)
		h.writeErr(w, "unavailable", msgServiceUnavailable)
	case errors.Is(err, domain.ErrInvalidArgument):
		h.writeErr(w, "bad_request", msg)
	case errors.Is(err, domain.ErrNotFound):
		h.writeErr(w, "not_found", "")
	default:
		h.internal(w, op, err)
	}
}

// manageErr 对应原实现 writeManageErr:头像页账号管理那组。taken 是"标识已属别人"在这个接口上的业务码。
func (h *handler) manageErr(w http.ResponseWriter, op, taken string, err error) {
	code, msg, _ := asDomain(err)
	switch code {
	case domain.CodeCodeInvalid:
		h.writeErr(w, "code_invalid", "")
		return
	case domain.CodeIdentityTaken:
		if taken == "" {
			taken = "internal"
		}
		h.writeErr(w, taken, "")
		return
	case domain.CodeLastIdentity:
		h.writeErr(w, "last_identity", "")
		return
	case domain.CodeIdentityNotFound:
		// 只有发到 / 校验"当前手机号 / 当前邮箱"会走到这里;op 以渠道结尾(code-verify-email 等)。
		if strings.HasSuffix(op, "-"+channelEmail) {
			h.writeErr(w, "no_email", "")
		} else {
			h.writeErr(w, "no_phone", "")
		}
		return
	case domain.CodeNotifyRecipientRejected:
		h.writeErr(w, "recipient_rejected", msg)
		return
	case domain.CodeIdentityUnchanged:
		if taken == "" {
			h.writeErr(w, "bad_request", "")
		} else {
			h.writeErr(w, strings.TrimSuffix(taken, "_taken")+"_same", "")
		}
		return
	case domain.CodePasswordTooShort, domain.CodePasswordTooLong:
		h.writeErr(w, "password_invalid", "")
		return
	case domain.CodeOAuthStateInvalid, domain.CodeOAuthTicketInvalid:
		h.writeErr(w, "wechat_expired", "")
		return
	case domain.CodeConnectorDisabled, domain.CodeReturnURLNotAllowed, domain.CodeDesktopModeDisabled:
		slog.Warn("accountapi: 平台侧未就绪", "op", op, "code", code, "err", err)
		h.writeErr(w, "unavailable", msgServiceUnavailable)
		return
	case domain.CodeUserNotFound:
		// token 有效而用户没了: 只能是刚被删, 同注销按未登录处理。
		h.writeErr(w, "unauthorized", "")
		return
	}
	switch {
	case internalish(err) && !canceled(err):
		slog.Warn("accountapi: 服务不可用", "op", op, "code", code, "err", err)
		h.writeErr(w, "unavailable", msgServiceUnavailable)
	case errors.Is(err, domain.ErrRateLimited):
		h.writeErr(w, "rate_limited", msg)
	case errors.Is(err, domain.ErrInvalidArgument):
		h.writeErr(w, "bad_request", msg)
	case errors.Is(err, domain.ErrForbidden):
		if msg == "" {
			msg = "当前账号状态不支持此操作"
		}
		h.writeErr(w, "forbidden", msg)
	case errors.Is(err, domain.ErrNotFound):
		h.writeErr(w, "unauthorized", "")
	default:
		h.internal(w, op, err)
	}
}

// deleteErr 对应原实现 writeDeleteErr。
func (h *handler) deleteErr(w http.ResponseWriter, err error) {
	switch {
	case internalish(err) && !canceled(err):
		slog.Warn("accountapi: 注销不可用", "err", err)
		h.writeErr(w, "unavailable", "注销服务暂不可用，请稍后重试")
	case errors.Is(err, domain.ErrForbidden):
		h.writeErr(w, "forbidden", "")
	case errors.Is(err, domain.ErrNotFound):
		h.writeErr(w, "unauthorized", "")
	default:
		h.internal(w, "delete", err)
	}
}

func (h *handler) internal(w http.ResponseWriter, op string, err error) {
	if canceled(err) {
		slog.Debug("accountapi: 调用方已取消", "op", op, "err", err)
	} else {
		slog.Error("accountapi: internal error", "op", op, "err", err)
	}
	h.writeErr(w, "internal", "")
}
