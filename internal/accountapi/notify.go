package accountapi

import (
	"net/http"
)

type notifyQRResp struct {
	Content   string `json:"content"`
	ExpiresIn int    `json:"expires_in"`
	Token     string `json:"token,omitempty"`
}

// notifier 是 /notify/* 共同的开头。失败时已写好响应。
func (h *handler) notifier(w http.ResponseWriter, r *http.Request) (*caller, bool) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return nil, false
	}
	if h.d.WechatMP == nil {
		h.writeErr(w, "unavailable", msgServiceUnavailable)
		return nil, false
	}
	return c, true
}

// handleNotifyQR:订阅要等用户在微信里关注才生效,这里只是取一张临时二维码,所以不能被缓存。
func (h *handler) handleNotifyQR(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	c, authed := h.notifier(w, r)
	if !authed {
		return
	}
	qr, err := h.d.WechatMP.CreateSubscribeQR(r.Context(), appOf(r).AppID, c.UserID)
	if err != nil {
		h.manageErr(w, "notify-qr", "", err)
		return
	}
	ok(w, notifyQRResp{Content: qr.Content, ExpiresIn: int(qr.ExpiresIn.Seconds()), Token: c.Rotated})
}

func (h *handler) handleNotifyOff(w http.ResponseWriter, r *http.Request) {
	c, authed := h.notifier(w, r)
	if !authed || !h.notifyOff(w, r, c) {
		return
	}
	if p, done := h.profileOf(w, r, c); done {
		h.okUser(w, r, c, p)
	}
}

func (h *handler) notifyOff(w http.ResponseWriter, r *http.Request, c *caller) bool {
	if err := h.d.WechatMP.Unbind(r.Context(), c.UserID); err != nil {
		h.manageErr(w, "notify-off", "", err)
		return false
	}
	return true
}
