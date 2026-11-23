package accountapi

import (
	"log/slog"
	"net/http"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// 票据是不透明串, 限个长度只为别把超大请求往下传。
const maxTicketLen = 512

type loginReq struct {
	Type       string `json:"type"`
	Phone      string `json:"phone"`
	Code       string `json:"code"`
	Account    string `json:"account"`
	Password   string `json:"password"`
	Ticket     string `json:"ticket"`
	BindTicket string `json:"bind_ticket"`
}

// loginOutcome 二选一:签了会话(res),或首次扫码的微信要先绑手机号(bindTicket)。
type loginOutcome struct {
	res                  *service.LoginResult
	bindTicket, nickname string
	// entered 是用户这次登录用的标识, 资料查不到时拿它当显示名。
	entered string
}

type loginResp struct {
	User  accountUser `json:"user"`
	Token string      `json:"token"`
}

type bindPhoneResp struct {
	BindTicket string `json:"bind_ticket"`
	Nickname   string `json:"nickname"`
}

func (h *handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if !decodeBody(w, r, &req, maxBody) {
		h.writeErr(w, "bad_request", "")
		return
	}
	out, done := h.login(w, r, req)
	if !done {
		return
	}
	if out.res == nil {
		ok(w, bindPhoneResp{BindTicket: out.bindTicket, Nickname: out.nickname})
		return
	}
	ok(w, loginResp{User: h.loginUser(r, out), Token: out.res.Session.Token})
}

// loginUser 登录已经成功,资料查不到不能让它失败:退回登录结果自带的几项。
func (h *handler) loginUser(r *http.Request, out *loginOutcome) accountUser {
	p, err := h.d.Users.GetProfile(r.Context(), out.res.User.ID)
	if err != nil {
		slog.Warn("accountapi: 登录后取资料失败", "err", err)
		u := out.res.User
		name := u.Nickname
		if name == "" {
			name = out.entered
		}
		return accountUser{ID: u.ID.String(), Name: name, Nickname: u.Nickname, HasPassword: u.PasswordHash != ""}
	}
	return h.accountUser(r.Context(), p)
}

// login 是四种登录方式的同一段:校验参数 → 认设备 → 登录。失败时已写好响应。
// 格式不对的请求不往下走:短信那边按手机号计次,畸形号码也会白白消耗一次额度。
func (h *handler) login(w http.ResponseWriter, r *http.Request, req loginReq) (*loginOutcome, bool) {
	app := appOf(r)
	if !app.Account.HasLoginType(req.Type) {
		h.writeErr(w, "bad_request", "")
		return nil, false
	}
	switch req.Type {
	case domain.AccountLoginSMS, domain.AccountLoginWechatPhone:
		if req.Type == domain.AccountLoginWechatPhone && (req.BindTicket == "" || len(req.BindTicket) > maxTicketLen) {
			h.writeErr(w, "bad_request", "")
			return nil, false
		}
		if !validPhone(req.Phone) {
			h.writeErr(w, "bad_request", "请输入正确的手机号")
			return nil, false
		}
		if !digits(req.Code, 6) {
			h.writeErr(w, "bad_request", "请输入 6 位数字验证码")
			return nil, false
		}
	case domain.AccountLoginPassword:
		if req.Account == "" || len(req.Account) > maxAccountLen || req.Password == "" || len(req.Password) > maxPasswordLen {
			h.writeErr(w, "bad_request", "")
			return nil, false
		}
	case domain.AccountLoginWechat:
		if req.Ticket == "" || len(req.Ticket) > maxTicketLen {
			h.writeErr(w, "bad_request", "")
			return nil, false
		}
	}
	// 先认设备再登录: 认不出设备的登录绑不了设备, 签发出去的 token 也就没处用;扫码票据也是用一次就没。
	devicePub, clientRef, passed := h.loginDevice(w, r)
	if !passed {
		return nil, false
	}
	ctx := r.Context()
	client := service.OAuthClient{AppID: app.AppID, IP: ipOf(r), UA: r.UserAgent(), DevicePub: devicePub, ClientRef: clientRef}
	switch req.Type {
	case domain.AccountLoginSMS, domain.AccountLoginPassword:
		in := service.LoginInput{AppID: app.AppID, IP: client.IP, UA: client.UA, DevicePub: devicePub, ClientRef: clientRef}
		entered := req.Account
		if req.Type == domain.AccountLoginSMS {
			in.ConnectorType = connector.TypeSMSCode
			in.Credentials = connector.Credentials{"phone": req.Phone, "code": req.Code}
			entered = maskPhone(req.Phone)
		} else {
			in.ConnectorType = connector.TypePassword
			in.Credentials = connector.Credentials{"account": req.Account, "password": req.Password}
		}
		res, err := h.d.Auth.Login(ctx, in)
		if err != nil {
			h.loginErr(w, "login", err)
			return nil, false
		}
		return &loginOutcome{res: res, entered: entered}, true
	case domain.AccountLoginWechatPhone:
		res, err := h.d.Auth.BindPhone(ctx, service.BindPhoneInput{OAuthClient: client, BindTicket: req.BindTicket,
			Phone: req.Phone, Code: req.Code})
		if err != nil {
			h.wechatErr(w, "login-wechat-phone", err)
			return nil, false
		}
		return &loginOutcome{res: res, entered: maskPhone(req.Phone)}, true
	default: // wechat
		out, err := h.d.Auth.ExchangeOAuth(ctx, client, req.Ticket)
		if err != nil {
			h.wechatErr(w, "login-wechat", err)
			return nil, false
		}
		switch out.Status {
		case service.OAuthStatusSuccess:
			if out.Login == nil {
				slog.Error("accountapi: 微信登录成功却没有会话")
				h.writeErr(w, "internal", "")
				return nil, false
			}
			return &loginOutcome{res: out.Login, entered: anonymousName}, true
		case service.OAuthStatusBindPhone:
			return &loginOutcome{bindTicket: out.BindTicket, nickname: out.Nickname}, true
		case service.OAuthStatusCancelled:
			h.writeErr(w, "wechat_cancelled", "")
		default:
			slog.Warn("accountapi: 微信登录未成功", "status", out.Status)
			h.writeErr(w, "login_failed", "微信登录失败，请重试")
		}
		return nil, false
	}
}

// handleLogout 总是 200:客户端此时已经清掉了本地登录态,这里报错它也无事可做。
// 绑设备的会话要设备头对得上才撤:不然拿到 token、没有设备私钥的人也能把真设备踢下线。
func (h *handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if token != "" && h.logoutAllowed(r, token) {
		if err := h.d.Auth.Logout(r.Context(), appOf(r).AppID, token); err != nil {
			slog.Warn("accountapi: 撤销 token 失败", "err", err)
		}
	}
	ok(w, empty{})
}

func (h *handler) logoutAllowed(r *http.Request, token string) bool {
	app := appOf(r)
	if !app.Account.DeviceBinding {
		return true
	}
	sess, err := h.d.Sessions.SessionByToken(r.Context(), token)
	if err != nil || sess.AppID != app.ID || sess.DevicePub == "" {
		// 查不到就交给 Logout 自己判(重复登出、已过期都是无事可做)。没绑设备的会话本就用不了,登出照常撤掉。
		return true
	}
	if err := h.d.Guard.Check(r.Context(), app, sess, r.Header.Get(app.Account.DeviceHeader), h.now()); err != nil {
		slog.Info("accountapi: 登出的设备头与会话不符, 不撤销", "sessionId", sess.ID, "err", err)
		return false
	}
	return true
}
