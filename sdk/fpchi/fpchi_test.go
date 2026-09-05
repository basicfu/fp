package fpchi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"

	fpsdk "github.com/basicfu/fp/sdk"
)

// buildRouter 造一棵覆盖各种形态的路由树。
func buildRouter() chi.Router {
	r := chi.NewRouter()
	h := func(http.ResponseWriter, *http.Request) {}

	r.Get("/health", h)
	r.Route("/orders", func(r chi.Router) {
		r.Get("/", h)
		r.Post("/", h)
		r.Get("/{id}", h)
		r.Delete("/{id}", h)
		r.Route("/{id}/items", func(r chi.Router) {
			r.Get("/", h)
			r.Delete("/{itemId}", h)
		})
	})
	return r
}

// 【最要紧的一条】上报出来的 key，必须与请求时试匹配出来的 key 一字不差。
//
// 授权模块不自己做 RESTful 匹配（不像 casbin 的 keyMatch2）：存的是路由模式，
// 请求进来先用 chi 把具体 URL 还原成模式，再精确比较。这么做的好处是用的
// 就是分发该请求的那个匹配器，不可能出现"路由这么匹、鉴权那么匹"的分歧。
//
// 代价是引入了这条不变式：**CollectChi 与 chiRoutePattern 必须产出同一个
// 字符串**。差一个斜杠、多一个 /* 后缀，鉴权就会**静默地全部拒绝**——本地
// 查不到对应条目就是默认拒绝，没有任何报错指向真实原因。
//
// 这条测试遍历真实路由树的每一条，两边各算一次再比对，是这个不变式唯一的守卫。
func TestCollectAndMatchProduceSameKey(t *testing.T) {
	r := buildRouter()

	// 上报侧
	reported := map[string]bool{}
	for _, p := range New(nil).Collect(r) {
		reported[p.Key] = true
	}
	if len(reported) == 0 {
		t.Fatal("CollectChi 一条都没收集到，测试构造失效")
	}

	// 判定侧：对每条路由构造一个具体 URL，走试匹配
	cases := []struct{ method, url string }{
		{"GET", "/health"},
		{"GET", "/orders/"},
		{"POST", "/orders/"},
		{"GET", "/orders/123"},
		{"DELETE", "/orders/123"},
		{"GET", "/orders/9/items/"},
		{"DELETE", "/orders/9/items/7"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.url, func(t *testing.T) {
			pattern, ok := matchThrough(r, c.method, c.url)
			if !ok {
				t.Fatalf("试匹配失败——这条路由存在却匹不上")
			}
			key := fpsdk.PermissionKey(c.method, pattern)
			if !reported[key] {
				var all []string
				for k := range reported {
					all = append(all, k)
				}
				sort.Strings(all)
				t.Fatalf("判定侧算出 %q，但上报侧没有这个 key。\n"+
					"两者必须一字不差，否则鉴权会静默全拒。\n上报的是：\n  %v", key, all)
			}
		})
	}
}

// 试匹配对各种路径形态的行为。第五节的手工实测固化成这条。
func TestRoutePattern(t *testing.T) {
	r := buildRouter()

	tests := []struct {
		method, url string
		want        string
		wantOK      bool
	}{
		{"GET", "/health", "/health", true},
		{"GET", "/orders/123", "/orders/{id}", true},
		{"DELETE", "/orders/456", "/orders/{id}", true},
		// 多个路径参数、嵌套子路由
		{"DELETE", "/orders/9/items/7", "/orders/{id}/items/{itemId}", true},
		// 路径存在但方法不对：匹配不上，应当交给 chi 回 405，
		// 而不是在鉴权这里当成"没权限"回 403——那会把"方法用错了"
		// 伪装成权限问题，排障方向直接跑偏。
		{"PUT", "/orders/123", "", false},
		// 路径压根不存在
		{"GET", "/nope", "", false},
		{"GET", "/orders/1/items/2/extra", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.url, func(t *testing.T) {
			got, ok := matchThrough(r, tt.method, tt.url)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v（模式 %q）", ok, tt.wantOK, got)
			}
			if ok && got != tt.want {
				t.Fatalf("模式 = %q, want %q", got, tt.want)
			}
		})
	}
}

// 【辨别力】不同的 id 必须归到同一个权限点。
//
// 这是"存路由模式而不是存 URL"的全部意义。一个直接拿 r.URL.Path 当 key 的
// 实现会让每个 id 变成一个独立的权限点——控制台上瞬间几万条，而且没有任何
// 一条能被授权到。
func TestDifferentIDsMapToSamePermission(t *testing.T) {
	r := buildRouter()

	first, ok1 := matchThrough(r, "GET", "/orders/1")
	second, ok2 := matchThrough(r, "GET", "/orders/99999")
	if !ok1 || !ok2 {
		t.Fatal("试匹配失败")
	}
	if first != second {
		t.Fatalf("不同 id 归到了不同权限点: %q vs %q", first, second)
	}
}

func TestCollectStripsPrefix(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/orders/{id}", func(http.ResponseWriter, *http.Request) {})
	})

	got := New(nil, StripPrefix("/api/v1")).Collect(r)
	if len(got) != 1 {
		t.Fatalf("收集到 %d 条, want 1", len(got))
	}
	if got[0].Key != "GET:/orders/{id}" {
		t.Fatalf("key = %q, want GET:/orders/{id}——前缀没有被去掉", got[0].Key)
	}
}

// 前缀恰好等于整条路由时不能产出空串——那会变成一个无法辨认的权限点。
func TestTrimPrefixEdgeCases(t *testing.T) {
	tests := []struct{ route, prefix, want string }{
		{"/api/v1/orders", "/api/v1", "/orders"},
		{"/api/v1", "/api/v1", "/"},
		{"/orders", "/api/v1", "/orders"}, // 前缀对不上，原样返回
		{"/orders", "", "/orders"},
		{"/api", "/api/v1", "/api"}, // 路由比前缀短
	}
	for _, tt := range tests {
		if got := trimPrefix(tt.route, tt.prefix); got != tt.want {
			t.Errorf("trimPrefix(%q, %q) = %q, want %q", tt.route, tt.prefix, got, tt.want)
		}
	}
}

// matchThrough 把一个请求真正送进路由器，在顶层中间件里取出试匹配的结果。
//
// 刻意走真实的 ServeHTTP 而不是直接调 chiRoutePattern：要测的正是"在中间件
// 这个时机能不能拿到完整模式"。直接构造 RouteContext 的话，测的就不是那个
// 时机了——而那个时机恰恰是问题所在（顶层中间件里 RoutePattern() 是空串，
// 子路由里只到 /orders/*，完整值要到 handler 才有，可鉴权必须在 handler 之前）。
func matchThrough(r chi.Router, method, url string) (string, bool) {
	var pattern string
	var ok bool

	probe := chi.NewRouter()
	probe.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			pattern, ok = routePattern(req)
			next.ServeHTTP(w, req)
		})
	})
	probe.Mount("/", r)

	probe.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, url, nil))
	return pattern, ok
}

// 走完整的中间件：真实 HTTP 请求 → 试匹配 → 判定 → 状态码。
//
// 前面的测试分别验了"模式提取对"与"判定逻辑对"，这条验的是**两者接起来
// 之后仍然对**——第三阶段的教训正是：各层自己的测试都绿，接缝处却漏了。

// 六种路由形态下，chi.Walk 与试匹配的一致性。
//
// 上一条测试用的是一棵具体的路由树；这条换个角度，把 chi 里能产生模式差异
// 的形态逐个过一遍——Route / Mount / 显式尾斜杠 / 根路由 / 通配。差异是单向
// 的（Walk 加尾斜杠，Match 从不加），normalizePattern 只挂在上报侧就够了，
// 这条测试是那个判断的依据。哪天升级 chi 把方向改了，这里会先红。
func TestWalkAndMatchAgreeAcrossShapes(t *testing.T) {
	h := func(http.ResponseWriter, *http.Request) {}

	shapes := []struct {
		name  string
		build func() chi.Router
		// probe 是一个会命中的具体 URL
		method, url string
	}{
		{"顶层显式带尾斜杠", func() chi.Router {
			r := chi.NewRouter()
			r.Get("/orders/", h)
			return r
		}, "GET", "/orders/"},
		{"Route 子路由根", func() chi.Router {
			r := chi.NewRouter()
			r.Route("/orders", func(r chi.Router) { r.Get("/", h) })
			return r
		}, "GET", "/orders/"},
		{"Mount 子路由根", func() chi.Router {
			sub := chi.NewRouter()
			sub.Get("/", h)
			r := chi.NewRouter()
			r.Mount("/orders", sub)
			return r
		}, "GET", "/orders/"},
		{"Mount 带路径参数", func() chi.Router {
			sub := chi.NewRouter()
			sub.Get("/{id}", h)
			r := chi.NewRouter()
			r.Mount("/orders", sub)
			return r
		}, "GET", "/orders/9"},
		{"根路由", func() chi.Router {
			r := chi.NewRouter()
			r.Get("/", h)
			return r
		}, "GET", "/"},
		{"通配后缀", func() chi.Router {
			r := chi.NewRouter()
			r.Get("/files/*", h)
			return r
		}, "GET", "/files/a/b"},
	}

	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			r := s.build()

			reported := map[string]bool{}
			for _, p := range New(nil).Collect(r) {
				reported[p.Key] = true
			}

			pattern, ok := matchThrough(r, s.method, s.url)
			if !ok {
				t.Fatalf("%s %s 匹配不上，测试构造失效", s.method, s.url)
			}
			key := fpsdk.PermissionKey(s.method, pattern)
			if !reported[key] {
				var all []string
				for k := range reported {
					all = append(all, k)
				}
				sort.Strings(all)
				t.Fatalf("判定侧算出 %q，上报侧只有 %v——两侧对不上，这种形态的接口会被静默全拒", key, all)
			}
		})
	}
}

// 【辨别力】路由压根不存在时，不能回 403。
//
// 交给 chi 去回它的 404。在鉴权这里拦下来回 403，会把"URL 写错了"伪装成
// "没有权限"——排查的人会去翻角色配置，而问题在请求方那边。方法用错（405）
// 同理。

// fakeDecider 是一个可控的判定方。
//
// 适配器的职责只有两条：把请求翻译成路由模式，以及把判定结果/各类错误
// 映射成状态码。判定逻辑本身是 authzcore 的事，它有自己的穷举测试；在这里
// 拼装真策略只会让失败信息含糊——分不清是模式取错了还是判定算错了。
type fakeDecider struct {
	allow map[string]bool
	err   error
	// seen 记下适配器实际传下来的 key，用来断言传的是模式不是原始 URL。
	seen []string
}

func (f *fakeDecider) Allow(_ context.Context, method, pattern string) (bool, error) {
	f.seen = append(f.seen, fpsdk.PermissionKey(method, pattern))
	if f.err != nil {
		return false, f.err
	}
	return f.allow[fpsdk.PermissionKey(method, pattern)], nil
}

// 走完整的中间件：真实 HTTP 请求 → 试匹配 → 判定 → 状态码。
//
// 前面的测试分别验了"模式提取对"与"判定逻辑对"，这条验的是**两者接起来
// 之后仍然对**——第三阶段的教训正是：各层自己的测试都绿，接缝处却漏了。
func TestAuthzMiddlewareEndToEnd(t *testing.T) {
	d := &fakeDecider{allow: map[string]bool{
		"GET:/orders/{id}": true,
		"GET:/orders":      true,
	}}

	r := chi.NewRouter()
	r.Use(New(d).Middleware())
	r.Route("/orders", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		r.Get("/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		r.Delete("/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	tests := []struct {
		method, url string
		want        int
		wantKey     string
	}{
		// 具体 id 归到 /orders/{id}，授权命中
		{"GET", "/orders/123", http.StatusOK, "GET:/orders/{id}"},
		{"GET", "/orders/99999", http.StatusOK, "GET:/orders/{id}"},
		// 子路由根上的列表接口——就是尾斜杠那个 bug 的现场
		{"GET", "/orders/", http.StatusOK, "GET:/orders"},
		// 没授权的动作
		{"DELETE", "/orders/123", http.StatusForbidden, "DELETE:/orders/{id}"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.url, func(t *testing.T) {
			d.seen = nil
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.url, nil))
			if rec.Code != tt.want {
				t.Fatalf("状态码 = %d, want %d", rec.Code, tt.want)
			}
			if len(d.seen) != 1 || d.seen[0] != tt.wantKey {
				t.Fatalf("传给判定方的是 %v, want [%s]——适配器传下去的必须是路由模式",
					d.seen, tt.wantKey)
			}
		})
	}
}

// 没过认证中间件时回 401 而不是 403：那是装配问题，不是权限问题。
func TestAuthzWithoutIdentity(t *testing.T) {
	d := &fakeDecider{err: fpsdk.ErrNoIdentity}

	r := chi.NewRouter()
	r.Use(New(d).Middleware())
	r.Get("/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401——没有身份被当成了没权限", rec.Code)
	}
}

// 【辨别力】本地策略还没拉到时走 onError，不是 403。
//
// 调用方要分得清"没权限"（业务照常）和"没能判定"（可用性事件）。一个把
// 两者都回 403 的实现，会让 fp 刚启动那几秒里所有请求看起来像权限配错了。
func TestAuthzPolicyUnavailableGoesToOnError(t *testing.T) {
	d := &fakeDecider{err: fpsdk.ErrPolicyUnavailable}

	called := false
	r := chi.NewRouter()
	r.Use(New(d, OnError(func(w http.ResponseWriter, _ *http.Request, err error) {
		called = true
		if !errors.Is(err, fpsdk.ErrPolicyUnavailable) {
			t.Errorf("onError 收到的是 %v, want ErrPolicyUnavailable", err)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})).Middleware())
	r.Get("/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if !called {
		t.Fatal("策略未就绪时没有走 onError")
	}
	if rec.Code == http.StatusForbidden {
		t.Fatal("策略未就绪被当成了没权限（403）")
	}
}

// 【辨别力】路由压根不存在时，不能回 403。
//
// 交给 chi 去回它的 404。在鉴权这里拦下来回 403，会把"URL 写错了"伪装成
// "没有权限"——排查的人会去翻角色配置，而问题在请求方那边。方法用错（405）
// 同理。
func TestAuthzUnmatchedRouteIsNotForbidden(t *testing.T) {
	d := &fakeDecider{} // 空表：任何走到判定的请求都会被拒

	r := chi.NewRouter()
	r.Use(New(d).Middleware())
	r.Get("/orders/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	tests := []struct {
		name, method, url string
		want              int
		wantDecided       bool
	}{
		{"路径不存在 → 404", "GET", "/nope", http.StatusNotFound, false},
		{"方法不对 → 405", "PUT", "/orders/1", http.StatusMethodNotAllowed, false},
		// 对照组：路由存在且匹配上了，空表下确实该 403
		{"路由存在但没授权 → 403", "GET", "/orders/1", http.StatusForbidden, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d.seen = nil
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.url, nil))
			if rec.Code != tt.want {
				t.Fatalf("状态码 = %d, want %d", rec.Code, tt.want)
			}
			if decided := len(d.seen) > 0; decided != tt.wantDecided {
				t.Fatalf("是否走到判定 = %v, want %v——匹配不上的请求不该消耗一次判定",
					decided, tt.wantDecided)
			}
		})
	}
}

// 【最要紧的一条】只要 handler 跑了，就必须先做过一次判定。
//
// 这是整个适配器的安全不变式。中间件在试匹配失败时会放行（好让 chi 去回
// 404/405），所以"试匹配失败"与"chi 也匹不上"必须是同一件事。一旦不是，
// 那个请求就直接进 handler，**一次判定都没做**——不是判错，是压根没判。
//
// 这个洞真实存在过：routePattern 当时用 req.URL.Path，而 chi 用 RawPath。
//
//	GET /orders/a%2Fb
//	req.URL.Path    = "/orders/a/b"     试匹配拿它 → 匹配不上 → 放行
//	req.URL.RawPath = "/orders/a%2Fb"   chi 拿它   → 匹配上 → handler 执行
//
// 任何带百分号编码的 URL 都能绕开鉴权。所以这条不去断言"该匹配成什么"，
// 只断言"handler 跑了就一定判定过"——不管路径长什么样。
func TestHandlerNeverRunsWithoutDecision(t *testing.T) {
	urls := []string{
		// 普通路径
		"/orders/123",
		"/orders/",
		"/orders/9/items/7",
		// 百分号编码：编码的斜杠会改变解码后的段结构
		"/orders/a%2Fb",
		"/orders/a%2Fb/items/1",
		"/orders/9/items/a%2Fb",
		// 编码的点号，解码后是 ..
		"/orders/%2E%2E",
		"/orders/%2e%2e/items/1",
		// 编码的普通字符：不改段结构，但一样会让 RawPath 非空
		"/orders/a%20b",
		"/orders/%E8%AE%A2%E5%8D%95",
		// 重复斜杠与尾部变体
		"//orders/1",
		"/orders/1/",
		"/orders//items/1",
		// 匹配不上的（这些确实该 404/405，不该判定）
		"/nope",
		"/orders/1/items/2/extra",
	}
	methods := []string{"GET", "POST", "DELETE", "PUT", "PATCH", "HEAD", "OPTIONS"}

	for _, u := range urls {
		for _, m := range methods {
			t.Run(m+" "+u, func(t *testing.T) {
				d := &fakeDecider{} // 空表：判定一律拒绝
				ran := false
				h := func(w http.ResponseWriter, _ *http.Request) { ran = true }

				r := chi.NewRouter()
				r.Use(New(d).Middleware())
				r.Route("/orders", func(r chi.Router) {
					r.Get("/", h)
					r.Post("/", h)
					r.Get("/{id}", h)
					r.Delete("/{id}", h)
					r.Get("/{id}/items/{itemId}", h)
				})

				r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(m, u, nil))

				if ran && len(d.seen) == 0 {
					t.Fatalf("handler 执行了，但一次判定都没做——鉴权被绕过")
				}
				if ran {
					t.Fatalf("判定方一律拒绝，handler 不该执行（判定过 %v）", d.seen)
				}
			})
		}
	}
}

// 中间件挂在 Mount 进去的子路由里时，仍然要做判定，且两侧 key 一致。
//
// chi 只在最外层给 RouteContext.Routes 赋值（mux.go 的 ServeHTTP：已有父
// context 时直接复用），而 RoutePath 是 Mount 之后的剩余路径、配的是内层
// 路由器。把这两个配成一对就会匹配失败——而匹配失败等于放行，是一次鉴权
// 绕过（曾经如此：DELETE 直接回 200，一次判定都没做）。
//
// 固定用"顶层路由器 + 完整路径"之后，判定拿到的模式含挂载前缀。这不是
// 将就：chi.Walk 顶层路由器给出的也正是含前缀的那个，两侧依然一致——这条
// 测试同时断言这一点。
func TestAuthzInsideMountedSubrouter(t *testing.T) {
	const fullKey = "/api/v1/orders/{id}"
	d := &fakeDecider{allow: map[string]bool{"GET:" + fullKey: true}}

	sub := chi.NewRouter()
	sub.Use(New(d).Middleware())
	sub.Get("/orders/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	sub.Delete("/orders/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	r := chi.NewRouter()
	r.Mount("/api/v1", sub)

	// 上报侧从顶层路由器出发，算出来的必须是同一个 key
	reported := map[string]bool{}
	for _, p := range New(nil).Collect(r) {
		reported[p.Key] = true
	}

	tests := []struct {
		method string
		want   int
	}{
		{"GET", http.StatusOK},
		{"DELETE", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			d.seen = nil
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tt.method, "/api/v1/orders/7", nil))
			if rec.Code != tt.want {
				t.Fatalf("状态码 = %d, want %d（判定过 %v）", rec.Code, tt.want, d.seen)
			}
			key := tt.method + ":" + fullKey
			if len(d.seen) != 1 || d.seen[0] != key {
				t.Fatalf("判定用的 key = %v, want [%s]", d.seen, key)
			}
			if !reported[key] {
				var all []string
				for k := range reported {
					all = append(all, k)
				}
				sort.Strings(all)
				t.Fatalf("判定侧用 %q，上报侧只有 %v——两侧对不上", key, all)
			}
		})
	}
}

// StripPrefix 必须同时作用于上报与判定。
//
// 【辨别力】只在一侧生效的实现会让所有请求被静默拒绝：上报的是
// GET:/orders/{id}，判定要的是 GET:/api/v1/orders/{id}，本地策略表查不到
// 就是默认拒绝，而且没有任何报错指向真实原因。这正是把前缀收进 Adapter、
// 不让两侧各配一次的理由。
func TestStripPrefixAppliesToBothSides(t *testing.T) {
	a := New(&fakeDecider{allow: map[string]bool{"GET:/orders/{id}": true}},
		StripPrefix("/api/v1"))

	r := chi.NewRouter()
	r.Use(a.Middleware())
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/orders/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	got := a.Collect(r)
	if len(got) != 1 || got[0].Key != "GET:/orders/{id}" {
		t.Fatalf("上报 = %v, want [GET:/orders/{id}]", got)
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/orders/7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200——前缀只在上报侧生效，判定侧没去掉，"+
			"于是查不到条目、静默全拒", rec.Code)
	}
}

// 正则参数与段内后缀：chi 支持，两侧必须仍然一致。
func TestCollectAndMatchAgreeOnAdvancedPatterns(t *testing.T) {
	r := chi.NewRouter()
	h := func(http.ResponseWriter, *http.Request) {}
	r.Get("/orders/{id:[0-9]+}", h) // 正则约束
	r.Get("/orders/{slug}", h)      // 无约束，兜底
	r.Get("/files/{name}.json", h)  // 段内后缀
	r.Get("/v{ver:[0-9]+}/ping", h) // 段内前缀 + 正则

	reported := map[string]bool{}
	for _, p := range New(nil).Collect(r) {
		reported[p.Key] = true
	}

	cases := []struct{ url, want string }{
		{"/orders/123", "/orders/{id:[0-9]+}"},
		{"/orders/abc", "/orders/{slug}"},
		{"/files/a.json", "/files/{name}.json"},
		{"/v2/ping", "/v{ver:[0-9]+}/ping"},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			pattern, ok := matchThrough(r, "GET", c.url)
			if !ok {
				t.Fatal("试匹配失败")
			}
			if pattern != c.want {
				t.Fatalf("模式 = %q, want %q", pattern, c.want)
			}
			if key := fpsdk.PermissionKey("GET", pattern); !reported[key] {
				t.Fatalf("判定侧算出 %q，上报侧没有——两侧对不上", key)
			}
		})
	}
}
