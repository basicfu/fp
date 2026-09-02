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
	Accounts *service.AccountService
	Sessions *service.SessionService
	Logs     *service.LoginLogService
	Registry *connector.Registry

	// SecureCookies 决定管理端会话 cookie 是否带 Secure 属性。
	//
	// 生产必须为 true（cmd/fp 用 config.Config.IsProd() 填），否则那个有效期
	// 两小时、能开关应用/冻结账号/重置密码的平台管理员凭据会在任何一段明文
	// HTTP 上原样出现——反代前面掉了 TLS、内网跳板、有人手滑访问 http:// 都算。
	// 本地开发拿不到 TLS，所以不能无脑写死 true，只能由启动配置决定。
	SecureCookies bool
}

// NewRouter 装配管理 UI 的 HTTP 路由。
//
// 这套 API 只服务管理控制台。SDK 走 gRPC（计划二），终端用户的登录接口
// 在第一阶段也由 SDK 代理，fp 暂不直接对终端用户暴露 HTTP。
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	ah := &adminHandler{svc: d.Admin, secureCookies: d.SecureCookies}
	appH := &applicationHandler{svc: d.Apps}
	userH := &userHandler{users: d.Users, accounts: d.Accounts, sessions: d.Sessions, logs: d.Logs}
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
			// PATCH 而不是 PUT：这两个接口都是**局部更新**，只碰自己那几列。
			// 与下面 /session 的全量替换语义刻意不同。
			r.Patch("/applications/{id}", appH.update)
			r.Patch("/applications/{id}/status", appH.setStatus)
			// PUT 而不是 PATCH：这个接口是整份会话策略的**全量替换**。
			// decodeJSON 开了 DisallowUnknownFields、sessionPolicyDTO 六个字段
			// 都是非指针、SessionPolicy.Validate 又要求六项全部有值——只发其中
			// 一两项的"局部更新"必然 400。用 PATCH 命名等于承诺了一个做不到的
			// 语义。趁还没有任何消费方，先把动词改对。
			r.Put("/applications/{id}/session", appH.updateSession)
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
