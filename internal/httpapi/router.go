package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/basicfu/fp/internal/service"
)

// NewRouter 装配管理 UI 的 HTTP 路由。
// 后续任务会往这里追加 application、user 等路由组。
func NewRouter(admin *service.AdminService) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	ah := &adminHandler{svc: admin}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/admin/api", func(r chi.Router) {
		r.Post("/login", ah.login)

		r.Group(func(r chi.Router) {
			r.Use(requireAdmin(admin))
			r.Post("/logout", ah.logout)
			r.Get("/me", ah.me)
		})
	})

	return r
}
