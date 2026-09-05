package fpchi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// benchRouter 造一棵接近真实项目规模的路由树：nRes 个资源 × 5 个接口。
func benchRouter(nRes int) chi.Router {
	r := chi.NewRouter()
	h := func(http.ResponseWriter, *http.Request) {}
	for i := 0; i < nRes; i++ {
		res := fmt.Sprintf("/res%d", i)
		r.Route(res, func(r chi.Router) {
			r.Get("/", h)
			r.Post("/", h)
			r.Get("/{id}", h)
			r.Put("/{id}", h)
			r.Delete("/{id}", h)
		})
	}
	return r
}

type allowAll struct{}

func (allowAll) Allow(_ context.Context, _, _ string) (bool, error) { return true, nil }

// 只测试匹配这一步的开销。
func BenchmarkRoutePattern(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("%d资源=%d路由", n, n*5), func(b *testing.B) {
			r := benchRouter(n)
			// 打到最后一个资源，避免只测到第一条就命中的乐观情形
			url := fmt.Sprintf("/res%d/12345", n-1)

			var probe *http.Request
			outer := chi.NewRouter()
			outer.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					probe = req
				})
			})
			outer.Mount("/", r)
			outer.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", url, nil))

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, ok := routePattern(probe); !ok {
					b.Fatal("匹配不上")
				}
			}
		})
	}
}

// 整条中间件：试匹配 + 判定 + 分发。
func BenchmarkMiddleware(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("%d资源=%d路由", n, n*5), func(b *testing.B) {
			r := chi.NewRouter()
			r.Use(New(allowAll{}).Middleware())
			h := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }
			for i := 0; i < n; i++ {
				r.Route(fmt.Sprintf("/res%d", i), func(r chi.Router) {
					r.Get("/{id}", h)
					r.Delete("/{id}", h)
				})
			}
			req := httptest.NewRequest("GET", fmt.Sprintf("/res%d/12345", n-1), nil)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.ServeHTTP(httptest.NewRecorder(), req)
			}
		})
	}
}

// 对照：不挂鉴权中间件的同一棵树，差值就是鉴权的净成本。
func BenchmarkMiddlewareBaseline(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("%d资源=%d路由", n, n*5), func(b *testing.B) {
			r := chi.NewRouter()
			h := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }
			for i := 0; i < n; i++ {
				r.Route(fmt.Sprintf("/res%d", i), func(r chi.Router) {
					r.Get("/{id}", h)
					r.Delete("/{id}", h)
				})
			}
			req := httptest.NewRequest("GET", fmt.Sprintf("/res%d/12345", n-1), nil)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.ServeHTTP(httptest.NewRecorder(), req)
			}
		})
	}
}
