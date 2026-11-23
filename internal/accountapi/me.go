package accountapi

import (
	"net/http"

	"github.com/basicfu/fp/internal/service"
)

// userResp 是新接口里登录之后所有改资料接口的统一返回:改完的完整资料, 外加可能轮换出的新 token。
type userResp struct {
	User  accountUser `json:"user"`
	Token string      `json:"token,omitempty"`
}

func (h *handler) handleMe(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	if p, done := h.profileOf(w, r, c); done {
		h.okUser(w, r, c, p)
	}
}

// profileOf 取当前用户资料。token 有效而用户没了只能是刚被删,按未登录回。
func (h *handler) profileOf(w http.ResponseWriter, r *http.Request, c *caller) (*service.UserProfile, bool) {
	p, err := h.d.Users.GetProfile(r.Context(), c.UserID)
	if err != nil {
		h.manageErr(w, "profile-get", "", err)
		return nil, false
	}
	return p, true
}

func (h *handler) okUser(w http.ResponseWriter, r *http.Request, c *caller, p *service.UserProfile) {
	ok(w, userResp{User: h.accountUser(r.Context(), p), Token: c.Rotated})
}
