package fpsdk

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// CollectChi 枚举 chi 路由树里的全部路由，作为权限点上报给 fp。
//
// 业务方**一行注解都不用写**——这正是设计文档里"权限路径从代码变成注册
// 数据"要解的东西（3s 那 200 行硬编码路径数组，加个接口要改数组再发版）。
//
// stripPrefix 会从路由模式前面去掉一段固定前缀。给出它是为了让权限点的
// key 有意义：`/api/v1/orders/{id}` 不加处理的话，将来做分组时会被归到
// "api" 组里去。本期不做分组，但前缀配置现在就留，免得以后要求所有已接入
// 方改初始化代码。
func CollectChi(r chi.Routes, stripPrefix string) []PermissionPoint {
	var out []PermissionPoint
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		pattern := trimPrefix(route, stripPrefix)
		out = append(out, PermissionPoint{Key: PermissionKey(method, pattern), Kind: PermissionKindAPI})
		return nil
	})
	return out
}

// ChiAuthz 返回一个 chi 中间件，对经过它的请求做鉴权。
//
// **"哪些路由归 fp 管"就是"你把它挂在哪"**——没挂的路由 fp 一概不管（公开
// 接口、健康检查、回调本来就该这样）；挂了的默认拒绝：你既然显式说了这组
// 要管，那就该管住。
//
// 身份从 context 里取（fpsdk 的认证中间件放进去的），所以本中间件必须挂在
// 认证中间件**之后**——鉴权的前提是知道你是谁。
//
// onError 处理"没能判定"（fp 不可达、本地策略还没拉到）。传 nil 时默认 503。
// 这一层刻意交给业务方：有的服务宁可在 fp 不可用时放行内部接口，有的宁可
// 全拒——SDK 不替他们决定。
func ChiAuthz(a *Authz, onError func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	if onError == nil {
		onError = func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "服务暂时不可用", http.StatusServiceUnavailable)
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			pattern, ok := chiRoutePattern(req)
			if !ok {
				// 路由本身不存在，交给 chi 的 404，不要在这里回 403——
				// 那会让"URL 写错了"看起来像"没权限"，排障方向直接跑偏。
				next.ServeHTTP(w, req)
				return
			}
			allowed, err := a.Allow(req.Context(), req.Method, pattern)
			if errors.Is(err, ErrNoIdentity) {
				// 没过认证中间件。回 401 而不是走 onError：这是装配问题，
				// 不是可用性问题，混进降级路径会掩盖真实原因。
				http.Error(w, "未登录", http.StatusUnauthorized)
				return
			}
			if err != nil {
				onError(w, req, err)
				return
			}
			if !allowed {
				http.Error(w, "没有权限", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

// chiRoutePattern 取出请求匹配到的完整路由模式。
//
// **不能直接读 RouteContext().RoutePattern()**：在顶层中间件里它是空串
// （路由还没匹配），在子路由中间件里只到 "/orders/*"，完整的 "/orders/{id}"
// 要到 handler 里才有——而鉴权必须在 handler 之前拦下。
//
// 所以这里主动做一次试匹配。已实测嵌套路由与多路径参数都正确：
// DELETE /orders/9/items/7 → /orders/{id}/items/{itemId}。
func chiRoutePattern(req *http.Request) (string, bool) {
	rctx := chi.RouteContext(req.Context())
	if rctx == nil || rctx.Routes == nil {
		return "", false
	}
	probe := chi.NewRouteContext()
	if !rctx.Routes.Match(probe, req.Method, req.URL.Path) {
		return "", false
	}
	return probe.RoutePattern(), true
}

func trimPrefix(route, prefix string) string {
	if prefix == "" || len(route) < len(prefix) || route[:len(prefix)] != prefix {
		return route
	}
	trimmed := route[len(prefix):]
	if trimmed == "" {
		return "/"
	}
	return trimmed
}
