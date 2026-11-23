package accountapi

import (
	"log/slog"
	"net/http"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type identityReq struct {
	Type   string `json:"type"`
	Phone  string `json:"phone"`
	Email  string `json:"email"`
	Code   string `json:"code"`
	Ticket string `json:"ticket"`
}

func (h *handler) handleIdentityBind(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	var req identityReq
	if !decodeBody(w, r, &req, maxBody) {
		h.writeErr(w, "bad_request", "")
		return
	}
	var (
		p    *service.UserProfile
		done bool
	)
	switch req.Type {
	case domain.IdentityTypePhone:
		p, done = h.bindPhone(w, r, c, req.Phone, req.Code, req.Ticket)
	case domain.IdentityTypeEmail:
		p, done = h.bindEmail(w, r, c, req.Email, req.Code)
	case service.UnbindKindWechat:
		p, done = h.bindWechat(w, r, c, req.Ticket)
	default:
		h.writeErr(w, "bad_request", "")
	}
	if done {
		h.okUser(w, r, c, p)
	}
}

// bindPhone 已有手机号的是换绑: 必须先验过旧号(/code/verify 发的票据), 否则拿到别人登录态就能把号换走。
func (h *handler) bindPhone(w http.ResponseWriter, r *http.Request, c *caller, phone, code, ticket string) (*service.UserProfile, bool) {
	if len(ticket) > maxPhoneTicketLen {
		h.writeErr(w, "bad_request", "")
		return nil, false
	}
	if !validPhone(phone) {
		h.writeErr(w, "bad_request", "请输入正确的手机号")
		return nil, false
	}
	if !digits(code, 6) {
		h.writeErr(w, "bad_request", "请输入 6 位数字验证码")
		return nil, false
	}
	ctx := r.Context()
	cur, done := h.profileOf(w, r, c)
	if !done {
		return nil, false
	}
	var held *heldTicket
	if identity(cur, domain.IdentityTypePhone) != "" {
		if ticket == "" {
			h.writeErr(w, "ticket_invalid", "")
			return nil, false
		}
		var err error
		if held, err = h.tickets.Take(ctx, c.UserID.String(), ticket); err != nil {
			slog.Error("accountapi: 取换绑票据失败", "err", err)
			h.writeErr(w, "internal", "")
			return nil, false
		}
		if held == nil {
			h.writeErr(w, "ticket_invalid", "")
			return nil, false
		}
	}
	p, err := h.d.Auth.BindIdentity(ctx, appOf(r).AppID, c.UserID, domain.IdentityTypePhone, phone, code)
	if err != nil {
		// 新号的码输错不该逼用户回头重验旧号: 票据放回去, 剩余有效期不变。
		h.tickets.Restore(ctx, held)
		h.manageErr(w, "phone-bind", "phone_taken", err)
		return nil, false
	}
	return p, true
}

func (h *handler) bindEmail(w http.ResponseWriter, r *http.Request, c *caller, email, code string) (*service.UserProfile, bool) {
	if !validEmail(email) {
		h.writeErr(w, "bad_request", "请输入正确的邮箱")
		return nil, false
	}
	if !digits(code, 6) {
		h.writeErr(w, "bad_request", "请输入 6 位数字验证码")
		return nil, false
	}
	p, err := h.d.Auth.BindIdentity(r.Context(), appOf(r).AppID, c.UserID, domain.IdentityTypeEmail, email, code)
	if err != nil {
		h.manageErr(w, "email-bind", "email_taken", err)
		return nil, false
	}
	return p, true
}

// bindWechat 用 /wechat/start purpose=bind 换来的票据。票据只认发起人,登录票据也不能拿来绑。
func (h *handler) bindWechat(w http.ResponseWriter, r *http.Request, c *caller, ticket string) (*service.UserProfile, bool) {
	if ticket == "" || len(ticket) > maxTicketLen {
		h.writeErr(w, "bad_request", "")
		return nil, false
	}
	res, err := h.d.Auth.ExchangeBindOAuth(r.Context(), appOf(r).AppID, c.UserID, ticket)
	if err != nil {
		h.manageErr(w, "wechat-bind", "wechat_taken", err)
		return nil, false
	}
	switch res.Status {
	case service.OAuthStatusSuccess:
	case service.OAuthStatusCancelled:
		h.writeErr(w, "wechat_cancelled", "已取消微信绑定")
		return nil, false
	default:
		slog.Warn("accountapi: 微信绑定未成功", "status", res.Status)
		h.writeErr(w, "wechat_failed", "")
		return nil, false
	}
	if res.Profile != nil {
		return res.Profile, true
	}
	return h.profileOf(w, r, c)
}

// handleIdentityUnbind 现在只开放微信:手机号是主登录方式不可解绑,邮箱解绑没有需求。
func (h *handler) handleIdentityUnbind(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	var req identityReq
	if !decodeBody(w, r, &req, maxBody) || req.Type != service.UnbindKindWechat {
		h.writeErr(w, "bad_request", "")
		return
	}
	if p, done := h.unbindWechat(w, r, c); done {
		h.okUser(w, r, c, p)
	}
}

func (h *handler) unbindWechat(w http.ResponseWriter, r *http.Request, c *caller) (*service.UserProfile, bool) {
	p, err := h.d.Auth.UnbindIdentity(r.Context(), appOf(r).AppID, c.UserID, service.UnbindKindWechat)
	if code, _, _ := asDomain(err); code == domain.CodeIdentityNotFound {
		// 本来就没绑(比如另一台电脑刚解绑): 结果就是用户要的, 按成功回。
		return h.profileOf(w, r, c)
	}
	if err != nil {
		h.manageErr(w, "wechat-unbind", "", err)
		return nil, false
	}
	return p, true
}
