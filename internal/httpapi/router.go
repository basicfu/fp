package httpapi

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
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

	// Console 是管理控制台的前端构建产物。为 nil（或其中没有 index.html）
	// 时，非 API 路径统一返回 503 加一句"请先构建前端"，而不是空白页。
	Console fs.FS
}

// NewRouter 装配管理 UI 的 HTTP 路由。
//
// 这套 API 只服务管理控制台。SDK 走 gRPC（计划二），终端用户的登录接口
// 在第一阶段也由 SDK 代理，fp 暂不直接对终端用户暴露 HTTP。
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	// 管理控制台第一次给 fp 带来浏览器 UI：没有这个头，已登录管理员访问
	// 一个恶意页面就可能被 iframe 嵌套后 clickjack 成一次"停用应用"之类的
	// 点击。放在最前面而不是只加在 /admin/api 或静态兜底其中一支，保证
	// 三类响应（/healthz、/admin/api/*、静态兜底）都带上。
	r.Use(xFrameOptionsDeny)

	ah := &adminHandler{svc: d.Admin, secureCookies: d.SecureCookies}
	appH := &applicationHandler{svc: d.Apps}
	userH := &userHandler{users: d.Users, accounts: d.Accounts, sessions: d.Sessions, logs: d.Logs}
	connH := &connectorHandler{registry: d.Registry}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/admin/api", func(r chi.Router) {
		// API 的 404 必须是 JSON。少了这行，chi 会用它默认的纯文本 404，
		// 前端的 res.json() 会抛一个与真实原因无关的解析错误。
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotFound, errorBody{
				Code: domain.CodeRouteNotFound,
				Msg:  "接口不存在",
			})
		})

		r.Post("/login", ah.login)

		r.Group(func(r chi.Router) {
			r.Use(requireAdmin(d.Admin))

			r.Post("/logout", ah.logout)
			r.Get("/me", ah.me)

			r.Get("/connectors", connH.list)

			r.Get("/applications", appH.list)
			r.Post("/applications", appH.create)
			r.Get("/applications/{id}", appH.get)
			// PATCH 而不是 PUT：这两个接口都是**局部更新**，只碰自己那几列，
			// 且 /applications/{id} 允许只传其中一个字段——省略或传 null 表示
			// 不改，显式传空字符串才是把 cookieDomain 清空。与下面 /session
			// 的全量替换语义刻意不同。
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

	// 放在最后：chi 的路由匹配偏好更具体的模式，/healthz 与 /admin/api/*
	// 都比 /* 具体，不会被这条吃掉。
	//
	// 【终审】只对 GET/HEAD 注册，不能用 r.Handle（对全部方法生效）。
	// 前端某处如果漏写 /admin/api 前缀，DELETE/PATCH/PUT 这类写请求会落到
	// 这条通配；如果不管方法一律回 200 + index.html，前端 api.ts 会在
	// res.json() 上抛一个和"URL 前缀写错"毫无关系的解析错误，事故排查
	// 会被带偏。GET/HEAD 之外的方法交给 chi 的默认行为（找不到匹配方法时
	// 405，路径本身也未知时 404），都不是 200。
	console := newStaticHandler(d.Console)
	r.Get("/*", console.ServeHTTP)
	r.Head("/*", console.ServeHTTP)

	return r
}
