package fpsdk

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path"
)

// OAuthHandlerOptions 配置 OAuthHandler。零值只差 ReturnURL（网页端要用）。
type OAuthHandlerOptions struct {
	// Connector 为空时取 ConnectorWechatWeb。
	Connector string
	// ReturnURL 是网页端登录后回到的业务页面，必须在控制台配的回跳白名单里。
	// 写死在服务端而不是由前端传：前端可控的回跳地址就是开放重定向。
	ReturnURL string

	// OnLogin 决定登录成功后怎么把会话交给前端。为 nil 时写 cookie（与 Middleware 默认的
	// cookie 名一致，下面三项决定其属性）并返回 JSON {status, token, sessionId, user}——
	// 带 token 是给桌面 App 这类不用 cookie 的客户端。
	OnLogin      func(w http.ResponseWriter, r *http.Request, res *LoginResult)
	CookieName   string
	CookieDomain string
	// CookieSecure 同 MiddlewareOptions.CookieSecure：生产必须设为 true。
	CookieSecure bool

	// ClientIP 取终端 IP，为 nil 时用 RemoteAddr。业务方在反向代理后面时要自己从 X-Forwarded-For 取。
	ClientIP func(*http.Request) string
	// Mobile 判断是否移动端（决定空闲超时档位），为 nil 时一律 false。
	Mobile func(*http.Request) bool
}

// OAuthHandler 一次性提供扫码登录要的全部后端接口，挂在任意前缀下：
//
//	mux.Handle("/auth/wechat/", client.Auth().OAuthHandler(fpsdk.OAuthHandlerOptions{
//		ReturnURL: "https://app.example.com/login/wechat",
//	}))
//
// 得到（路径按最后一段区分，请求与响应都是 JSON）：
//
//	POST start      {mode}                   → {state, authUrl, appId, redirectUri, expiresIn}
//	POST exchange   {ticket}                 → 结论
//	GET  poll?state=                         → 结论
//	POST send-code  {phone}                  → 204
//	POST bind       {bindTicket, phone, code} → 登录成功
//
// 结论是 {status, bindTicket?, nickname?, avatarUrl?}；status 为 SUCCESS 时交给 OnLogin。
// 失败统一是 {code, msg}，msg 可以直接展示给用户，HTTP 状态码同 WriteError。
func (a *Auth) OAuthHandler(opts OAuthHandlerOptions) http.Handler {
	if opts.Connector == "" {
		opts.Connector = ConnectorWechatWeb
	}
	if opts.CookieName == "" {
		opts.CookieName = DefaultCookieName
	}
	if opts.OnLogin == nil {
		opts.OnLogin = defaultOAuthOnLogin(opts)
	}
	if opts.ClientIP == nil {
		opts.ClientIP = remoteIP
	}
	h := &oauthHandler{a: a, opts: opts}
	return http.HandlerFunc(h.serve)
}

type oauthHandler struct {
	a    *Auth
	opts OAuthHandlerOptions
}

func (h *oauthHandler) serve(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	switch op := path.Base(r.URL.Path); {
	case op == "poll" && r.Method == http.MethodGet:
		h.poll(w, r)
	case r.Method != http.MethodPost:
		writeOAuthJSON(w, http.StatusMethodNotAllowed, oauthErrorBody{Msg: "method not allowed"})
	case op == "start":
		h.start(w, r)
	case op == "exchange":
		h.exchange(w, r)
	case op == "send-code":
		h.sendCode(w, r)
	case op == "bind":
		h.bind(w, r)
	default:
		writeOAuthJSON(w, http.StatusNotFound, oauthErrorBody{Msg: "not found"})
	}
}

func (h *oauthHandler) start(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if !decodeOAuthBody(w, r, &req) {
		return
	}
	in := OAuthStartInput{Connector: h.opts.Connector, Mode: req.Mode}
	if req.Mode == OAuthModeWeb {
		in.ReturnURL = h.opts.ReturnURL
	}
	st, err := h.a.StartOAuth(r.Context(), in)
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	writeOAuthJSON(w, http.StatusOK, map[string]any{
		"state": st.State, "authUrl": st.AuthURL, "appId": st.ProviderAppID,
		"redirectUri": st.RedirectURI, "expiresIn": int64(st.ExpiresIn.Seconds()),
	})
}

func (h *oauthHandler) exchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	if !decodeOAuthBody(w, r, &req) {
		return
	}
	res, err := h.a.ExchangeOAuth(r.Context(), req.Ticket, h.client(r))
	h.writeResult(w, r, res, err)
}

func (h *oauthHandler) poll(w http.ResponseWriter, r *http.Request) {
	res, err := h.a.PollOAuth(r.Context(), r.URL.Query().Get("state"), h.client(r))
	h.writeResult(w, r, res, err)
}

func (h *oauthHandler) sendCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
	}
	if !decodeOAuthBody(w, r, &req) {
		return
	}
	if err := h.a.SendLoginCode(r.Context(), req.Phone); err != nil {
		writeOAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *oauthHandler) bind(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BindTicket string `json:"bindTicket"`
		Phone      string `json:"phone"`
		Code       string `json:"code"`
	}
	if !decodeOAuthBody(w, r, &req) {
		return
	}
	res, err := h.a.BindPhone(r.Context(), BindPhoneInput{
		OAuthClient: h.client(r), BindTicket: req.BindTicket, Phone: req.Phone, Code: req.Code,
	})
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	h.opts.OnLogin(w, r, res)
}

func (h *oauthHandler) writeResult(w http.ResponseWriter, r *http.Request, res *OAuthResult, err error) {
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	if res.Status == OAuthSuccess && res.Login != nil {
		h.opts.OnLogin(w, r, res.Login)
		return
	}
	body := map[string]any{"status": res.Status}
	if res.Status == OAuthBindPhone {
		body["bindTicket"], body["nickname"], body["avatarUrl"] = res.BindTicket, res.Nickname, res.AvatarURL
	}
	writeOAuthJSON(w, http.StatusOK, body)
}

func (h *oauthHandler) client(r *http.Request) OAuthClient {
	c := OAuthClient{IP: h.opts.ClientIP(r), UserAgent: r.UserAgent()}
	if h.opts.Mobile != nil {
		c.Mobile = h.opts.Mobile(r)
	}
	return c
}

func defaultOAuthOnLogin(opts OAuthHandlerOptions) func(http.ResponseWriter, *http.Request, *LoginResult) {
	return func(w http.ResponseWriter, _ *http.Request, res *LoginResult) {
		http.SetCookie(w, &http.Cookie{
			Name: opts.CookieName, Value: res.Token, Path: "/", Domain: opts.CookieDomain,
			HttpOnly: true, Secure: opts.CookieSecure, SameSite: http.SameSiteLaxMode,
		})
		user := map[string]any{}
		if u := res.User; u != nil {
			user = map[string]any{"id": u.GetId(), "nickname": u.GetNickname(), "avatarUrl": u.GetAvatarUrl(), "avatarHash": u.GetAvatarHash()}
		}
		writeOAuthJSON(w, http.StatusOK, map[string]any{
			"status": OAuthSuccess, "token": res.Token, "sessionId": res.SessionID, "user": user,
		})
	}
}

type oauthErrorBody struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

// writeOAuthError 只输出 fp 结构化错误的 Code 与 Msg（Msg 本就是给终端用户看的），
// 其余错误只给分类文案，理由同 WriteError：底层错误可能带着敏感信息。
func writeOAuthError(w http.ResponseWriter, err error) {
	status, text := classifyError(err)
	body := oauthErrorBody{Msg: text}
	var fe *Error
	if errors.As(err, &fe) && fe.Code != "" {
		body = oauthErrorBody{Code: fe.Code, Msg: fe.Msg}
	}
	writeOAuthJSON(w, status, body)
}

func decodeOAuthBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Code: "INVALID_ARGUMENT", Msg: "请求体不是合法的 JSON"})
		return false
	}
	return true
}

func writeOAuthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func remoteIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
