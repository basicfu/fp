package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

// LoginCheckTimeout 是一次登录校验的全程上限，含跨实例转发。
const LoginCheckTimeout = 3 * time.Second

// LoginCheckCall 是发给应用的一次登录校验。
type LoginCheckCall struct {
	CallID       string
	AppID        uuid.UUID
	DeviceHeader string
	IP           string
}

// LoginCheckAnswer 是应用的回答。
type LoginCheckAnswer struct {
	Allow     bool
	ClientRef string
	Code      string
	Msg       string
}

// LoginCheckTransport 是登录校验对 Watch 流与跨实例转发的全部依赖，*grpcapi.LoginCheckRelay 满足它。
//
// handled 为假表示「这一处没有该应用的流」：本实例没有，或目标实例不在线（订阅数为 0）、回了没有流。
// 调用方据此立刻换下一处，而不是等满超时。Candidates 是本实例之外持有该应用流的实例，按 callID 稳定排好先后。
type LoginCheckTransport interface {
	Candidates(ctx context.Context, appID uuid.UUID, callID string) []string
	AskLocal(ctx context.Context, call LoginCheckCall) (ans LoginCheckAnswer, handled bool, err error)
	AskRemote(ctx context.Context, node string, call LoginCheckCall) (ans LoginCheckAnswer, handled bool, err error)
}

// LoginCheckOptions 是可调参数，零值取默认。
type LoginCheckOptions struct {
	// Timeout 默认 LoginCheckTimeout。
	Timeout time.Duration
	// NewCallID 默认 uuid.NewString；测试固定它以控制候选实例的先后。
	NewCallID func() string
}

// LoginCheck 在签发会话前经 Watch 流同步问应用认不认这台设备。
type LoginCheck struct {
	t    LoginCheckTransport
	opts LoginCheckOptions
}

// NewLoginCheck 构造 LoginCheck。
func NewLoginCheck(t LoginCheckTransport, opts LoginCheckOptions) *LoginCheck {
	if opts.Timeout <= 0 {
		opts.Timeout = LoginCheckTimeout
	}
	if opts.NewCallID == nil {
		opts.NewCallID = uuid.NewString
	}
	return &LoginCheck{t: t, opts: opts}
}

// Ask 问应用，放行时返回应用给的 client_ref。
//
// 本实例有流就直接发、不碰 Redis；没有就按 call_id 稳定排序其余持流实例逐个转发。
// 超时、没有可用的流、应用放行却没给 client_ref，都是 CodeLoginCheckUnavailable；
// 应用拒绝时它给的 code / msg 原样作为错误（如 version_too_low），由接口层照搬给客户端。
func (s *LoginCheck) Ask(ctx context.Context, app *domain.Application, deviceHeader, clientIP string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	call := LoginCheckCall{CallID: s.opts.NewCallID(), AppID: app.ID, DeviceHeader: deviceHeader, IP: clientIP}

	ans, handled, err := s.t.AskLocal(ctx, call)
	if err == nil && !handled {
		for _, n := range s.t.Candidates(ctx, app.ID, call.CallID) {
			ans, handled, err = s.t.AskRemote(ctx, n, call)
			if err != nil || handled {
				break
			}
		}
	}
	switch {
	case err != nil:
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			slog.Warn("service: 登录校验失败", "err", err, "appId", app.AppID)
		}
		return "", loginCheckUnavailable()
	case !handled:
		return "", loginCheckUnavailable()
	case !ans.Allow && ans.Code == loginCheckAppUnavailable:
		return "", loginCheckUnavailable()
	case !ans.Allow:
		if ans.Code == "" {
			return "", domain.Fail(domain.ErrForbidden, domain.CodeLoginCheckRejected, "当前设备不允许登录")
		}
		msg := ans.Msg
		if msg == "" {
			msg = "当前设备不允许登录"
		}
		return "", domain.Fail(domain.ErrForbidden, ans.Code, msg)
	case ans.ClientRef == "":
		slog.Warn("service: 应用放行了登录却没给 client_ref", "appId", app.AppID)
		return "", loginCheckUnavailable()
	}
	return ans.ClientRef, nil
}

// loginCheckAppUnavailable 是 SDK 在业务方回调出错时回的码（fpsdk.CodeLoginCheckUnavailable）：
// 应用自己没能判定，与「没问到」同样处理，不当成应用拒绝。
const loginCheckAppUnavailable = "unavailable"

func loginCheckUnavailable() error {
	return domain.Fail(domain.ErrInternal, domain.CodeLoginCheckUnavailable, "服务暂时不可用，请稍后重试")
}
