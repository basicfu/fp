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
	// DefaultPassword 为 true 表示这次登录用的仍是内置默认密码，前端据此弹一条
	// 可忽略的提示。拿登录时的明文判断，不需要库里加标志位，也不会过期失真。
	DefaultPassword bool `json:"defaultPassword"`
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
	writeJSON(w, http.StatusOK, adminLoginResponse{
		Token:           token,
		Username:        req.Username,
		DefaultPassword: req.Password == service.DefaultAdminPassword,
	})
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

type adminChangeAccountRequest struct {
	Username    string `json:"username"`
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// adminChangeAccountResponse 只回显去掉空白后的登录名。不复用 adminLoginResponse，也不带 token：
// 全部会话已作废、不重发，用户要重新登录；defaultPassword 只对登录有意义，留给登录响应去报。
type adminChangeAccountResponse struct {
	Username string `json:"username"`
}

func (h *adminHandler) changeAccount(w http.ResponseWriter, r *http.Request) {
	var req adminChangeAccountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	username, err := h.svc.ChangeAccount(r.Context(), adminIDFrom(r.Context()), service.ChangeAccountInput{
		Username: req.Username, OldPassword: req.OldPassword, NewPassword: req.NewPassword,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	// 凭据一改所有会话都已作废，清掉 cookie，免得浏览器带着必然 401 的 cookie 继续发请求。
	http.SetCookie(w, h.sessionCookie("", -1))
	writeJSON(w, http.StatusOK, adminChangeAccountResponse{Username: username})
}
