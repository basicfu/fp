package httpapi

import (
	"net/http"

	"github.com/basicfu/fp/internal/service"
)

type adminHandler struct {
	svc *service.AdminService
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
	http.SetCookie(w, &http.Cookie{
		Name:     adminTokenCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, adminLoginResponse{Token: token, Username: req.Username})
}

func (h *adminHandler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), adminTokenFrom(r.Context())); err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: adminTokenCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
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
