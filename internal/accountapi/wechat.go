package accountapi

import (
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// 微信扫码用 web 模式而不是桌面轮询:轮询模式下谁拿着 state 谁就能取走会话(扫码钓鱼),
// web 模式的结果只交给发起回调请求的那一方。客户端从不真的打开回跳地址,只从 Location 里读票据。

type wechatStartReq struct {
	Purpose string `json:"purpose"`
}

type wechatStartResp struct {
	State       string `json:"state"`
	AuthURL     string `json:"auth_url"`
	RedirectURI string `json:"redirect_uri"`
	ExpiresIn   int    `json:"expires_in"`
	Token       string `json:"token,omitempty"`
}

func startResp(st *service.OAuthStart) wechatStartResp {
	return wechatStartResp{State: st.State, AuthURL: st.AuthURL, RedirectURI: st.RedirectURI, ExpiresIn: int(st.ExpiresInSec)}
}

func (h *handler) handleWechatStart(w http.ResponseWriter, r *http.Request) {
	var req wechatStartReq
	if !decodeBody(w, r, &req, maxBody) {
		h.writeErr(w, "bad_request", "")
		return
	}
	switch req.Purpose {
	case purposeLogin:
		if !appOf(r).Account.HasLoginType(domain.AccountLoginWechat) {
			h.writeErr(w, "bad_request", "")
			return
		}
		if st, done := h.startWechatLogin(w, r); done {
			ok(w, startResp(st))
		}
	case purposeBind:
		c, authed := h.requireUser(w, r)
		if !authed {
			return
		}
		if st, done := h.startWechatBind(w, r, c); done {
			out := startResp(st)
			out.Token = c.Rotated
			ok(w, out)
		}
	default:
		h.writeErr(w, "bad_request", "")
	}
}

// oauthStart 的回跳地址取应用配置:它要进三方的回跳白名单,客户端给的不能信。
func (h *handler) oauthStart(w http.ResponseWriter, r *http.Request) (service.OAuthStartInput, bool) {
	app := appOf(r)
	if app.Account.WechatReturnURL == "" {
		slog.Warn("accountapi: 应用没配扫码回跳地址", "app", app.AppID)
		h.writeErr(w, "unavailable", "微信登录暂未开放")
		return service.OAuthStartInput{}, false
	}
	return service.OAuthStartInput{AppID: app.AppID, ConnectorType: connector.TypeWechatWeb, Mode: service.OAuthModeWeb,
		ReturnURL: app.Account.WechatReturnURL, CallbackBase: callbackBase(r)}, true
}

// callbackBase 是三方回调的前缀：用已认定应用的请求 Host（走到这里 withApp 已按它认出了应用），固定 https——
// 对外只经网关的 TLS。认不出 Host 时返回空，退回 fp 的 public_url。
func callbackBase(r *http.Request) string {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !domain.ValidDomain(host) {
		return ""
	}
	return "https://" + host
}

func (h *handler) startWechatLogin(w http.ResponseWriter, r *http.Request) (*service.OAuthStart, bool) {
	in, passed := h.oauthStart(w, r)
	if !passed {
		return nil, false
	}
	st, err := h.d.Auth.StartOAuth(r.Context(), in)
	if err != nil {
		h.wechatErr(w, "wechat-start", err)
		return nil, false
	}
	return st, true
}

// startWechatBind 把发起人记进扫码状态:换来的票据只能绑到他身上,登录票据也不能拿来绑。
func (h *handler) startWechatBind(w http.ResponseWriter, r *http.Request, c *caller) (*service.OAuthStart, bool) {
	in, passed := h.oauthStart(w, r)
	if !passed {
		return nil, false
	}
	st, err := h.d.Auth.StartBindOAuth(r.Context(), in, c.UserID)
	if err != nil {
		h.manageErr(w, "wechat-bind-start", "", err)
		return nil, false
	}
	return st, true
}
