package fpsdk

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// capLoginCheck 是 WatchHello 里声明回答登录校验的能力名，与服务端 grpcapi.CapLoginCheck 相同。
const capLoginCheck = "login_check"

// loginCheckTimeout 与 fp 侧等应答的上限一致：过了这个时间 fp 已经判登录失败，再答也没用。
const loginCheckTimeout = 3 * time.Second

// CodeLoginCheckUnavailable 是 OnLoginCheck 返回 error 时回给 fp 的拒绝码。
const CodeLoginCheckUnavailable = "unavailable"

// LoginCheckRequest 是 fp 在签发会话前问的一次登录校验，见 Options.OnLoginCheck。
type LoginCheckRequest struct {
	// DeviceHeader 是客户端设备头原文（应用配置的请求头，如 kkdesk 的 X-KKdesk-Device），fp 已验过签名与时间窗。
	DeviceHeader string
	// IP 是客户端 IP。
	IP string
}

// LoginCheckResult 是应用的回答。
type LoginCheckResult struct {
	Allow bool
	// ClientRef 是应用给这台设备的标识，放行时必填。
	ClientRef string
	// Code、Msg 仅拒绝时：原样作为登录错误返回给客户端（如 version_too_low）。
	Code string
	Msg  string
}

func (c *Client) answerLoginCheck(ctx context.Context, stream grpc.BidiStreamingClient[fpv1.WatchRequest, fpv1.WatchResponse],
	sendMu *sync.Mutex, m *fpv1.LoginCheck) {
	cctx, cancel := context.WithTimeout(ctx, loginCheckTimeout)
	defer cancel()
	res, err := c.opts.OnLoginCheck(cctx, LoginCheckRequest{DeviceHeader: m.GetDeviceHeader(), IP: m.GetIp()})
	if err != nil {
		c.opts.Logger.Warn("fpsdk: 登录校验回调失败，按拒绝回答", "err", err)
		res = LoginCheckResult{Code: CodeLoginCheckUnavailable}
	}
	reply := &fpv1.LoginCheckReply{CallId: m.GetCallId(), Allow: res.Allow, ClientRef: res.ClientRef, Code: res.Code, Msg: res.Msg}
	sendMu.Lock()
	err = stream.Send(&fpv1.WatchRequest{Msg: &fpv1.WatchRequest_Reply{Reply: reply}})
	sendMu.Unlock()
	if err != nil {
		// 流断了：fp 那边这次校验会超时，客户端重试登录即可。
		c.opts.Logger.Warn("fpsdk: 登录校验应答没发出去", "err", err)
	}
}
