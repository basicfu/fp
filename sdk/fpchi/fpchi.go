// Package fpchi 是 fpsdk 授权模块的 chi 适配器。
//
// **它是独立的包，不是 fpsdk 的一部分**——这样只有真正用 chi 的接入方才会
// 把 chi 连进自己的二进制。用 gin、echo 或裸 net/http 的人 import fpsdk
// 时不会被迫拖上一个用不到的路由库（sdk/arch_test.go 的
// TestSDKDoesNotImportWebFrameworks 守着这条）。
//
// 适配器只做两件事：把路由树翻译成权限点（上报），以及从请求里取出匹配到的
// 路由模式（判定）。判定本身在 fpsdk 里，与框架无关。
//
// 用法——上报与判定共用同一个 Adapter：
//
//	a := fpchi.New(client.Authz(), fpchi.StripPrefix("/api/v1"))
//	r.Use(a.Middleware())
//	_ = client.ReportPermissions(ctx, a.Collect(r))
package fpchi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	fpsdk "github.com/basicfu/fp/sdk"
)

// Decider 是本适配器对判定方的全部要求。*fpsdk.Authz 满足它。
//
// 收窄成接口而不是直接写 *fpsdk.Authz：适配器要验的是"取出的模式对不对、
// 各类错误映射到哪个状态码"，用假的判定方就能测完，不必在这里拼装真策略。
type Decider interface {
	Allow(ctx context.Context, method, pattern string) (bool, error)
}

// Adapter 同时提供上报与判定，**这是它存在的理由**。
//
// 两侧算出来的 key 必须一字不差，否则鉴权会静默全拒——本地策略表里查不到
// 条目就是默认拒绝，没有任何报错指向真实原因。把 stripPrefix 这类会影响
// key 的配置放在一个对象上，"只在一侧配了"这种错法就不存在了。早先
// Collect 与中间件各自独立、前缀只有 Collect 认，正是这个洞。
type Adapter struct {
	d           Decider
	stripPrefix string
	onError     func(http.ResponseWriter, *http.Request, error)
}

// Option 配置 Adapter。
type Option func(*Adapter)

// StripPrefix 从权限点的 key 前面去掉一段固定前缀，上报与判定同时生效。
//
// 给出它是为了让 key 有意义：`/api/v1/orders/{id}` 不加处理的话，将来做
// 分组时会被归到 "api" 组里去。
func StripPrefix(prefix string) Option {
	return func(a *Adapter) { a.stripPrefix = prefix }
}

// OnError 接管"没能判定"（fp 不可达、本地策略还没拉到）。默认回 503。
//
// 这一层刻意交给业务方：有的服务宁可在 fp 不可用时放行内部接口，有的宁可
// 全拒——SDK 不替他们决定。
func OnError(f func(http.ResponseWriter, *http.Request, error)) Option {
	return func(a *Adapter) { a.onError = f }
}

// New 构造适配器。d 通常是 client.Authz()。
func New(d Decider, opts ...Option) *Adapter {
	a := &Adapter{
		d: d,
		onError: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "服务暂时不可用", http.StatusServiceUnavailable)
		},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Collect 枚举 chi 路由树里的全部路由，作为权限点上报给 fp。
//
// 业务方**一行注解都不用写**——这正是设计文档里"权限路径从代码变成注册
// 数据"要解的东西（3s 那 200 行硬编码路径数组，加个接口要改数组再发版）。
//
// **传顶层路由器**。判定时取模式用的是 chi 记在 RouteContext 里的顶层
// 路由器，所以上报也必须从同一棵树出发，否则两侧的 key 差一截挂载前缀。
func (a *Adapter) Collect(r chi.Routes) []fpsdk.PermissionPoint {
	var out []fpsdk.PermissionPoint
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, fpsdk.PermissionPoint{
			Key:  fpsdk.PermissionKey(method, a.key(normalizePattern(route))),
			Kind: fpsdk.PermissionKindAPI,
		})
		return nil
	})
	return out
}

// Middleware 返回 chi 中间件，对经过它的请求做鉴权。
//
// **"哪些路由归 fp 管"就是"你把它挂在哪"**——没挂的路由 fp 一概不管（公开
// 接口、健康检查、回调本来就该这样）；挂了的默认拒绝：你既然显式说了这组
// 要管，那就该管住。
//
// 身份从 context 里取（fpsdk 的认证中间件放进去的），所以本中间件必须挂在
// 认证中间件**之后**——鉴权的前提是知道你是谁。
func (a *Adapter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			pattern, ok := routePattern(req)
			if !ok {
				// 路由本身不存在，交给 chi 的 404/405，不要在这里回 403——
				// 那会让"URL 写错了"看起来像"没权限"，排障方向直接跑偏。
				//
				// 这条放行是整个适配器唯一的失败开放路径，它的正确性完全
				// 依赖"试匹配匹不上 ⟺ chi 也匹不上"。守卫见
				// TestHandlerNeverRunsWithoutDecision。
				next.ServeHTTP(w, req)
				return
			}
			allowed, err := a.d.Allow(req.Context(), req.Method, a.key(pattern))
			if errors.Is(err, fpsdk.ErrNoIdentity) {
				// 没过认证中间件。回 401 而不是走 onError：这是装配问题，
				// 不是可用性问题，混进降级路径会掩盖真实原因。
				http.Error(w, "未登录", http.StatusUnauthorized)
				return
			}
			if err != nil {
				a.onError(w, req, err)
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

// key 把路由模式化成权限点里的那一段。上报与判定都走这里。
func (a *Adapter) key(pattern string) string {
	return trimPrefix(pattern, a.stripPrefix)
}

// routePattern 取出请求匹配到的完整路由模式。
//
// **不能直接读 RouteContext().RoutePattern()**：在顶层中间件里它是空串
// （路由还没匹配），在子路由中间件里只到 "/orders/*"，完整的 "/orders/{id}"
// 要到 handler 里才有——而鉴权必须在 handler 之前拦下。
//
// 所以这里主动做一次试匹配。**RESTful 匹配是 chi 自己做的**：用的就是分发
// 该请求的那个匹配器，不可能出现"路由这么匹、鉴权那么匹"的分歧，判定本身
// 退化成精确字符串比较（对比 casbin 需要 keyMatch2 这类自带的匹配函数）。
func routePattern(req *http.Request) (string, bool) {
	rctx := chi.RouteContext(req.Context())
	if rctx == nil || rctx.Routes == nil {
		return "", false
	}
	probe := chi.NewRouteContext()
	if !rctx.Routes.Match(probe, req.Method, routingPath(req)) {
		return "", false
	}
	return probe.RoutePattern(), true
}

// routingPath 给出该拿去匹配 rctx.Routes 的那个路径。
//
// **必须与 chi 自己用的路径一致，否则是鉴权绕过**，不是普通的判定错误：
// 试匹配失败时中间件会放行给 chi 去回 404，可如果 chi 用的是另一个路径
// 并且匹配上了，那个请求就直接进了 handler，**一次判定都没做**。
//
// 两条实测出来的坑：
//
//  1. 用 req.URL.Path（解码过的）。chi 在 RawPath 非空时用 RawPath：
//     GET /orders/a%2Fb 的 Path 是 "/orders/a/b"（两段，匹配不上），
//     RawPath 是 "/orders/a%2Fb"（一段，匹配 /orders/{id}）。于是任何带
//     百分号编码的 URL 都能绕开鉴权。
//
//  2. 用 rctx.RoutePath。它是 Mount 之后的**剩余**路径，配的是**内层**
//     路由器；而 rctx.Routes 只在最外层被赋值一次（chi mux.go 的
//     ServeHTTP：有父 context 时直接复用，不重设 Routes），永远是**顶层**
//     路由器。拿内层路径去匹配顶层路由器，同样匹不上、同样绕过。
//
// 所以固定用"顶层路由器 + 完整路径"这一对。代价是 Mount 进去的子路由，
// 判定拿到的模式含挂载前缀（/api/v1/orders/{id}）——而 chi.Walk 顶层路由器
// 给出的也正是这个，两侧仍然一致。
func routingPath(req *http.Request) string {
	if req.URL.RawPath != "" {
		return req.URL.RawPath
	}
	if req.URL.Path == "" {
		return "/"
	}
	return req.URL.Path
}

// normalizePattern 抹平 chi.Walk 与试匹配的路由模式差异。
//
// **两侧必须一字不差，否则鉴权会静默全拒**——本地策略表里查不到对应条目
// 就是默认拒绝，没有任何报错指向真实原因。
//
// 差异是单向的，六种路由形态实测下来结论一致（见
// TestWalkAndMatchAgreeAcrossShapes）：**chi.Walk 会加尾斜杠，试匹配从不加**。
// 命中的是注册在子路由根上的接口——r.Route("/orders", ...) 里的
// r.Get("/", ...)，也就是 REST 里最常见的列表与创建接口：
//
//	chi.Walk（上报） → "/orders/"    带尾斜杠
//	试匹配（判定）   → "/orders"     不带
//
// 所以只在上报侧调用它。判定侧加上去是一句测不出差别的死代码。
// 根路由 "/" 是唯一要留意的：去掉尾斜杠就成空串，不再是能辨认的路径。
func normalizePattern(pattern string) string {
	if len(pattern) > 1 && pattern[len(pattern)-1] == '/' {
		return pattern[:len(pattern)-1]
	}
	return pattern
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
