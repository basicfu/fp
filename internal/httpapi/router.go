package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/service"
)

// Deps 是管理 API 需要的全部依赖。
type Deps struct {
	Admin    *service.AdminService
	Apps     *service.ApplicationService
	Users    *service.UserService
	Sessions *service.SessionService
	Logs     *service.LoginLogService
	Registry *connector.Registry
}

// NewRouter 装配管理 UI 的 HTTP 路由。
//
// 这套 API 只服务管理控制台。SDK 走 gRPC（计划二），终端用户的登录接口
// 在第一阶段也由 SDK 代理，fp 暂不直接对终端用户暴露 HTTP。
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	ah := &adminHandler{svc: d.Admin}
	appH := &applicationHandler{svc: d.Apps}
	userH := &userHandler{users: d.Users, sessions: d.Sessions, logs: d.Logs}
	connH := &connectorHandler{registry: d.Registry}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/admin/api", func(r chi.Router) {
		r.Post("/login", ah.login)

		r.Group(func(r chi.Router) {
			r.Use(requireAdmin(d.Admin))

			r.Post("/logout", ah.logout)
			r.Get("/me", ah.me)

			r.Get("/connectors", connH.list)

			r.Get("/applications", appH.list)
			r.Post("/applications", appH.create)
			r.Get("/applications/{id}", appH.get)
			r.Patch("/applications/{id}/session", appH.updateSession)
			r.Get("/applications/{id}/connectors", appH.listConnectors)
			r.Put("/applications/{id}/connectors/{type}", appH.putConnector)

			r.Get("/users", userH.list)
			r.Get("/users/{id}", userH.get)
			r.Patch("/users/{id}/status", userH.setStatus)
			r.Put("/users/{id}/password", userH.setPassword)
			r.Get("/users/{id}/sessions", userH.listSessions)
			r.Delete("/users/{id}/sessions", userH.revokeAllSessions)
			r.Delete("/users/{id}/sessions/{sid}", userH.revokeSession)
			r.Get("/users/{id}/login-logs", userH.listLoginLogs)
		})
	})

	return r
}
