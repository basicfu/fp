package accountapi

import (
	"errors"
	"log/slog"
	"net/http"

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
		if !h.loginOn(w, r, domain.AccountLoginWechat, "bad_request", "") {
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

// msgWechatOff 是微信扫码没开或没配好时给终端用户的话;要配什么只进日志。
const msgWechatOff = "微信登录暂未开放"

// oauthStart 的回跳地址取微信登录方式回跳白名单的第一项:它本就得在白名单里,客户端给的不能信。
// 客户端从不打开它(见文件头),取哪一项都一样,第一项最好预期。
func (h *handler) oauthStart(w http.ResponseWriter, r *http.Request) (service.OAuthStartInput, bool) {
	app := appOf(r)
	returnURL := ""
	ac, err := h.d.Apps.GetConnector(r.Context(), app.ID, connector.TypeWechatWeb)
	switch {
	case errors.Is(err, domain.ErrNotFound):
	case err != nil:
		slog.Warn("accountapi: 读微信登录方式失败", "app", app.AppID, "err", err)
		h.writeErr(w, "unavailable", "")
		return service.OAuthStartInput{}, false
	case ac.Enabled:
		urls := connector.OAuthReturnURLs(ac.Config)
		if len(urls) == 0 {
			slog.Warn("accountapi: 微信登录方式没配允许的网页回跳地址, 扫码不可用", "app", app.AppID)
			h.writeErr(w, "unavailable", msgWechatOff)
			return service.OAuthStartInput{}, false
		}
		returnURL = urls[0]
	}
	// 没开微信登录时照常往下走,由 StartOAuth 报 CONNECTOR_DISABLED,登录与绑定各按自己的映射回。
	return service.OAuthStartInput{AppID: app.AppID, ConnectorType: connector.TypeWechatWeb, Mode: service.OAuthModeWeb,
		ReturnURL: returnURL}, true
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
