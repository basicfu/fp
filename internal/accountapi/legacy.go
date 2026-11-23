package accountapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// 老路径别名层:已发布客户端仍在调业务方原来的 23 个路径(网关把 /app/account/* 改写成 /api/account/* 再转来)。
// 每个老路径把请求翻成新接口的用途 / 类型后进同一段处理,响应原样是老格式——老客户端按字段类型严格解析,
// 字段名、类型、省略规则都不能变。老版本被版本闸拦掉后整个文件删除。
//
// 与老格式的唯一差别:账号 ID(display_id)整个功能删了,老格式里它本来就是"发号失败时省略",老客户端按省略处理。

// legacySignHeader 是已发版老客户端发设备签名用的头名。只有老路径(不带 X-Account-Api)在没有 X-Sign 时才认它,
// 随本文件一起删。
const legacySignHeader = "X-KKdesk-Device"

func (h *handler) legacyRoutes(r chi.Router) {
	r.Post("/sms-code", h.legacySMSCode)
	r.Post("/wechat/login", h.legacyWechatLogin)
	r.Post("/wechat/bind", h.legacyWechatPhone)
	r.Post("/phone/verify", h.legacyPhoneVerify)
	r.Post("/phone/bind", h.legacyPhoneBind)
	r.Post("/email/bind", h.legacyEmailBind)
	r.Post("/wechat/bind-start", h.legacyWechatBindStart)
	r.Post("/wechat/bind-confirm", h.legacyWechatBindConfirm)
	r.Post("/wechat/unbind", h.legacyWechatUnbind)
	r.Post("/notify/qr", h.handleNotifyQR)
	r.Get("/notify/state", h.legacyNotifyState)
	r.Get("/avatar/{hash}", h.legacyAvatarByHash)
}

// legacyUser 是老登录、改资料接口回的 user。
type legacyUser struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Nickname   string `json:"nickname"`
	AvatarHash string `json:"avatar_hash"`
}

// legacyAccountUser 是老 /me 与账号管理那组回的 user。
type legacyAccountUser struct {
	legacyUser
	Phone       string `json:"phone"`
	Email       string `json:"email"`
	Wechat      string `json:"wechat"`
	HasPassword bool   `json:"has_password"`
	Notify      *bool  `json:"notify,omitempty"`
}

type legacyLoginResp struct {
	Token string     `json:"token"`
	User  legacyUser `json:"user"`
}

type legacyMeResp struct {
	UserID string `json:"user_id"`
	Token  string `json:"token,omitempty"`
	// User 取不到资料时省略: 客户端保持本地那份不动。
	User *legacyAccountUser `json:"user,omitempty"`
}

type legacyUserChangeResp struct {
	User  legacyUser `json:"user"`
	Token string     `json:"token,omitempty"`
}

type legacyAccountChangeResp struct {
	User  legacyAccountUser `json:"user"`
	Token string            `json:"token,omitempty"`
}

type legacyTokenResp struct {
	Token string `json:"token,omitempty"`
}

type legacyNotifyStateResp struct {
	Notify bool   `json:"notify"`
	Token  string `json:"token,omitempty"`
}

// legacyWechatLoginResp 二选一: 老用户直接是 token+user; 首次扫码的微信要先绑手机号, 回 bind_ticket+nickname。
type legacyWechatLoginResp struct {
	Token      string      `json:"token,omitempty"`
	User       *legacyUser `json:"user,omitempty"`
	BindTicket string      `json:"bind_ticket,omitempty"`
	Nickname   string      `json:"nickname,omitempty"`
}

func legacyUserFrom(p *service.UserProfile, fallback string) legacyUser {
	return legacyUser{ID: p.User.ID.String(), Name: displayName(p, fallback), Nickname: p.User.Nickname, AvatarHash: p.User.AvatarHash}
}

func (h *handler) legacyAccountUser(r *http.Request, p *service.UserProfile) legacyAccountUser {
	u := h.accountUser(r.Context(), p)
	return legacyAccountUser{legacyUser: legacyUserFrom(p, anonymousName), Phone: u.Phone, Email: u.Email,
		Wechat: u.Wechat, HasPassword: u.HasPassword, Notify: u.Notify}
}

// legacyLoginUser 同老实现的 finishLogin:资料查不到用登录结果自带的几项,显示名退到这次登录用的标识。
func (h *handler) legacyLoginUser(r *http.Request, out *loginOutcome) legacyUser {
	if p, err := h.d.Users.GetProfile(r.Context(), out.res.User.ID); err == nil {
		return legacyUserFrom(p, out.entered)
	} else {
		slog.Warn("accountapi: 登录后取资料失败", "err", err)
	}
	u := out.res.User
	lu := legacyUser{ID: u.ID.String(), Name: u.Nickname, Nickname: u.Nickname, AvatarHash: u.AvatarHash}
	if lu.Name == "" {
		lu.Name = out.entered
	}
	return lu
}

// ---- 登录 ----

type legacySMSReq struct {
	Phone     string   `json:"phone"`
	CaptchaID string   `json:"captcha_id"`
	X         *int     `json:"x"`
	Track     [][3]int `json:"track"`
}

func (h *handler) legacySMSCode(w http.ResponseWriter, r *http.Request) {
	var req legacySMSReq
	if !decodeBody(w, r, &req, maxCodeBody) {
		h.writeErr(w, "bad_request", "")
		return
	}
	// 原实现在应用没开短信登录时回 unavailable,老客户端据此提示"暂不可用"而不是"参数错误"。
	if !h.codeOn(w, r, purposeLogin, "unavailable") {
		return
	}
	if h.sendLoginCode(w, r, codeReq{Purpose: purposeLogin, Channel: channelSMS, Target: req.Phone,
		CaptchaID: req.CaptchaID, X: req.X, Track: req.Track}) {
		ok(w, empty{})
	}
}

func (h *handler) legacyLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if !decodeBody(w, r, &req, maxBody) || (req.Type != domain.AccountLoginSMS && req.Type != domain.AccountLoginPassword) {
		h.writeErr(w, "bad_request", "")
		return
	}
	if out, done := h.login(w, r, req, "unavailable", ""); done {
		ok(w, legacyLoginResp{Token: out.res.Session.Token, User: h.legacyLoginUser(r, out)})
	}
}

func (h *handler) legacyWechatLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	if !decodeBody(w, r, &req, maxBody) || req.Ticket == "" || len(req.Ticket) > maxTicketLen {
		h.writeErr(w, "bad_request", "")
		return
	}
	out, done := h.login(w, r, loginReq{Type: domain.AccountLoginWechat, Ticket: req.Ticket}, "unavailable", msgWechatOff)
	if !done {
		return
	}
	if out.res == nil {
		ok(w, legacyWechatLoginResp{BindTicket: out.bindTicket, Nickname: out.nickname})
		return
	}
	ok(w, legacyLoginResp{Token: out.res.Session.Token, User: h.legacyLoginUser(r, out)})
}

func (h *handler) legacyWechatPhone(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if !decodeBody(w, r, &req, maxBody) || req.BindTicket == "" || len(req.BindTicket) > maxTicketLen {
		h.writeErr(w, "bad_request", "")
		return
	}
	out, done := h.login(w, r, loginReq{Type: domain.AccountLoginWechatPhone, BindTicket: req.BindTicket, Phone: req.Phone, Code: req.Code},
		"unavailable", msgWechatOff)
	if done {
		ok(w, legacyLoginResp{Token: out.res.Session.Token, User: h.legacyLoginUser(r, out)})
	}
}

func (h *handler) legacyWechatStart(w http.ResponseWriter, r *http.Request) {
	if !h.loginOn(w, r, domain.AccountLoginWechat, "unavailable", msgWechatOff) {
		return
	}
	if st, done := h.startWechatLogin(w, r); done {
		ok(w, startResp(st))
	}
}

// ---- 资料 ----

func (h *handler) legacyMe(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	out := legacyMeResp{UserID: c.UserID.String(), Token: c.Rotated}
	// 老实现里资料取不到只是"没有资料":me 的 unauthorized 是客户端清登录态的唯一依据,不能因此报错。
	if p, err := h.d.Users.GetProfile(r.Context(), c.UserID); err == nil {
		u := h.legacyAccountUser(r, p)
		out.User = &u
	} else {
		slog.Warn("accountapi: 取用户资料失败, 按没有资料处理", "err", err)
	}
	ok(w, out)
}

func (h *handler) legacyProfile(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	var req struct {
		Nickname *string `json:"nickname"`
	}
	if !decodeBody(w, r, &req, maxBody) || req.Nickname == nil {
		h.writeErr(w, "bad_request", "")
		return
	}
	if p, done := h.updateNickname(w, r, c, *req.Nickname); done {
		ok(w, legacyUserChangeResp{User: legacyUserFrom(p, anonymousName), Token: c.Rotated})
	}
}

func (h *handler) legacyAvatar(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	if p, done := h.updateAvatar(w, r, c); done {
		ok(w, legacyUserChangeResp{User: legacyUserFrom(p, anonymousName), Token: c.Rotated})
	}
}

// legacyAvatarByHash 不要登录: 头像是公开内容。新地址在对象存储上,302 过去;
// 还没迁到对象存储的(迁移跑完之前)照老样子直接回图片。
func (h *handler) legacyAvatarByHash(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	if !validHash(hash) {
		h.writeErr(w, "bad_request", "")
		return
	}
	url, err := h.d.Users.AvatarURLByHash(r.Context(), hash)
	if err == nil {
		// 跳转本身不缓存:迁移回滚或对象地址变了时,缓存住的 302 会一直指向旧地址。
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, url, http.StatusFound)
		return
	}
	if code, _, _ := asDomain(err); code != domain.CodeAvatarNotFound && code != domain.CodeObjectStoreUnavailable {
		h.usersErr(w, "avatar-get", err)
		return
	}
	contentType, image, err := h.d.Users.GetAvatar(r.Context(), hash)
	if err != nil {
		h.usersErr(w, "avatar-get", err)
		return
	}
	switch contentType {
	case "image/jpeg", "image/png", "image/webp":
	default:
		// 类型按文件头认定, 不会是别的; 真出现了也不能把它当可执行内容交给浏览器。
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(image)
}

// ---- 账号管理 ----

// legacyCodePurposes 把老 /code 的用途翻成新的 purpose + channel。
var legacyCodePurposes = map[string][2]string{
	"verify_phone": {purposeVerify, channelSMS},
	"password":     {purposePassword, channelSMS},
	"new_phone":    {purposeBind, channelSMS},
	"email":        {purposeBind, channelEmail},
}

func (h *handler) legacyCode(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	var req struct {
		Purpose string `json:"purpose"`
		Target  string `json:"target"`
	}
	if !decodeBody(w, r, &req, maxBody) {
		h.writeErr(w, "bad_request", "")
		return
	}
	m, known := legacyCodePurposes[req.Purpose]
	if !known || !codeAllowed(m[0], m[1]) {
		h.writeErr(w, "bad_request", "")
		return
	}
	if !h.codeOn(w, r, m[0], "bad_request") {
		return
	}
	if h.sendAccountCode(w, r, c, m[0], m[1], req.Target) {
		ok(w, codeResp{CooldownSec: codeCooldownSec, Token: c.Rotated})
	}
}

func (h *handler) legacyPhoneVerify(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !decodeBody(w, r, &req, maxBody) {
		h.writeErr(w, "bad_request", "")
		return
	}
	if t, done := h.verifyPhone(w, r, c, req.Code); done {
		ok(w, ticketResp{Ticket: t, Token: c.Rotated})
	}
}

func (h *handler) okLegacyAccount(w http.ResponseWriter, r *http.Request, c *caller, p *service.UserProfile) {
	ok(w, legacyAccountChangeResp{User: h.legacyAccountUser(r, p), Token: c.Rotated})
}

func (h *handler) legacyPhoneBind(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	var req identityReq
	if !decodeBody(w, r, &req, maxBody) {
		h.writeErr(w, "bad_request", "")
		return
	}
	if p, done := h.bindPhone(w, r, c, req.Phone, req.Code, req.Ticket); done {
		h.okLegacyAccount(w, r, c, p)
	}
}

func (h *handler) legacyEmailBind(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	var req identityReq
	if !decodeBody(w, r, &req, maxBody) {
		h.writeErr(w, "bad_request", "")
		return
	}
	if p, done := h.bindEmail(w, r, c, req.Email, req.Code); done {
		h.okLegacyAccount(w, r, c, p)
	}
}

// legacyWechatBindStart 老格式不带 token(老实现就没带),轮换出的新 token 留给下一个接口告知。
func (h *handler) legacyWechatBindStart(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	if st, done := h.startWechatBind(w, r, c); done {
		ok(w, startResp(st))
	}
}

func (h *handler) legacyWechatBindConfirm(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	var req struct {
		Ticket string `json:"ticket"`
	}
	if !decodeBody(w, r, &req, maxBody) {
		h.writeErr(w, "bad_request", "")
		return
	}
	if p, done := h.bindWechat(w, r, c, req.Ticket); done {
		h.okLegacyAccount(w, r, c, p)
	}
}

func (h *handler) legacyWechatUnbind(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	if p, done := h.unbindWechat(w, r, c); done {
		h.okLegacyAccount(w, r, c, p)
	}
}

func (h *handler) legacyPassword(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	if h.setPassword(w, r, c) {
		ok(w, legacyTokenResp{Token: c.Rotated})
	}
}

func (h *handler) legacyNotifyState(w http.ResponseWriter, r *http.Request) {
	c, authed := h.notifier(w, r)
	if !authed {
		return
	}
	sub, err := h.d.WechatMP.Subscription(r.Context(), c.UserID)
	if err != nil {
		h.manageErr(w, "notify-state", "", err)
		return
	}
	ok(w, legacyNotifyStateResp{Notify: sub.Bound && sub.Subscribed, Token: c.Rotated})
}

func (h *handler) legacyNotifyOff(w http.ResponseWriter, r *http.Request) {
	c, authed := h.notifier(w, r)
	if authed && h.notifyOff(w, r, c) {
		ok(w, legacyTokenResp{Token: c.Rotated})
	}
}
