package httpapi

import (
	"net/http"

	"github.com/basicfu/fp/internal/service"
)

type adminHandler struct {
	svc *service.AdminService
	// secureCookies 见 Deps.SecureCookies。
	secureCookies bool
}

// sessionCookie 组装管理端会话 cookie。
//
// 登录写入和登出清除必须走同一个构造函数：属性（尤其是 Secure）在两处写歪了，
// 就会出现"设的是 Secure cookie、清的是非 Secure cookie"这类只在生产才复现的怪事。
func (h *adminHandler) sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     adminTokenCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
}

type adminLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type adminLoginResponse struct {
	Token    string `json:"token"`
	Username string `json:"username"`
}

func (h *adminHandler) login(w http.ResponseWriter, r *http.Request) {
	var req adminLoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	token, err := h.svc.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, h.sessionCookie(token, 0))
	writeJSON(w, http.StatusOK, adminLoginResponse{Token: token, Username: req.Username})
}

func (h *adminHandler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), adminTokenFrom(r.Context())); err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, h.sessionCookie("", -1))
	writeJSON(w, http.StatusNoContent, nil)
}

type adminMeResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func (h *adminHandler) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, adminMeResponse{
		ID:       adminIDFrom(r.Context()).String(),
		Username: adminNameFrom(r.Context()),
	})
}
