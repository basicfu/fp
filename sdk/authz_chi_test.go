package fpsdk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
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
	for _, p := range CollectChi(r, "") {
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
			key := PermissionKey(c.method, pattern)
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
func TestChiRoutePattern(t *testing.T) {
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

func TestCollectChiStripsPrefix(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/orders/{id}", func(http.ResponseWriter, *http.Request) {})
	})

	got := CollectChi(r, "/api/v1")
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
			pattern, ok = chiRoutePattern(req)
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
func TestChiAuthzMiddlewareEndToEnd(t *testing.T) {
	az := &Authz{}
	az.setPolicy(&fpv1.AppPolicy{Roles: []*fpv1.RolePolicy{
		{RoleKey: "普通用户", Allow: []string{"GET:/orders/{id}", "GET:/orders"}},
	}})

	r := chi.NewRouter()
	// 把身份塞进 context，模拟认证中间件。
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := context.WithValue(req.Context(), identityCtxKey{},
				&Identity{UserID: "u1", Roles: []string{"普通用户"}})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Use(ChiAuthz(az, nil))
	r.Route("/orders", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		r.Get("/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		r.Delete("/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	tests := []struct {
		method, url string
		want        int
	}{
		// 具体 id 归到 /orders/{id}，授权命中
		{"GET", "/orders/123", http.StatusOK},
		{"GET", "/orders/99999", http.StatusOK},
		// 子路由根上的列表接口——就是尾斜杠那个 bug 的现场
		{"GET", "/orders/", http.StatusOK},
		// 没授权的动作
		{"DELETE", "/orders/123", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.url, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.url, nil))
			if rec.Code != tt.want {
				t.Fatalf("状态码 = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// 没过认证中间件时回 401 而不是 403：那是装配问题，不是权限问题。
func TestChiAuthzWithoutIdentity(t *testing.T) {
	az := &Authz{}
	az.setPolicy(&fpv1.AppPolicy{})

	r := chi.NewRouter()
	r.Use(ChiAuthz(az, nil))
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
func TestChiAuthzPolicyUnavailableGoesToOnError(t *testing.T) {
	az := &Authz{} // 没有策略

	called := false
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := context.WithValue(req.Context(), identityCtxKey{},
				&Identity{UserID: "u1", Roles: []string{"普通用户"}})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Use(ChiAuthz(az, func(w http.ResponseWriter, _ *http.Request, err error) {
		called = true
		if !errors.Is(err, ErrPolicyUnavailable) {
			t.Errorf("onError 收到的是 %v, want ErrPolicyUnavailable", err)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
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
