package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// builtConsole 模拟一份构建好的前端产物。
func builtConsole() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/index-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/index-def456.css": {Data: []byte("body{}")},
		"favicon.svg":             {Data: []byte("<svg/>")},
	}
}

// 未构建：只有 .gitkeep，没有 index.html。
func unbuiltConsole() fstest.MapFS {
	return fstest.MapFS{".gitkeep": {Data: []byte{}}}
}

func TestStaticServesIndexAtRoot(t *testing.T) {
	h := newStaticHandler(builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "id=root") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Fatalf("index.html 的 Cache-Control = %q，必须含 no-cache", cc)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("index.html 应当带 ETag，供条件请求比对")
	}
}

// SPA 回退：前端路由（/users/xxx）在服务端不存在对应文件，必须回 index.html，
// 否则刷新页面就 404。
func TestStaticFallsBackToIndexForClientRoutes(t *testing.T) {
	h := newStaticHandler(builtConsole())
	for _, p := range []string{"/users", "/users/123", "/applications/abc/connectors"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "id=root") {
			t.Fatalf("%s: code = %d body = %q", p, rec.Code, rec.Body.String())
		}
	}
}

// 带 If-None-Match 且跟当前 ETag 相同时，回 304、不带 body——这是"内容没变
// 不用整份重传"这条优化的核心断言。
func TestStaticIndexReturnsNotModifiedWhenETagMatches(t *testing.T) {
	h := newStaticHandler(builtConsole())

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("第一次请求应当带 ETag")
	}

	second := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	h.ServeHTTP(second, req)

	if second.Code != http.StatusNotModified {
		t.Fatalf("code = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 不该带 body，实际 %q", second.Body.String())
	}
}

// If-None-Match 跟当前 ETag 不一致（比如上一次部署留下的旧值）时，必须
// 正常返回整份 index.html，不能被误判成"没变"。
func TestStaticIndexReturnsFullBodyWhenETagDoesNotMatch(t *testing.T) {
	h := newStaticHandler(builtConsole())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", `"stale-etag-from-previous-deploy"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "id=root") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// 静态资源现在挂在 /static/ 前缀下——CDN 按 static.xxzj.com/fp/ 回源到源站
// 的 /static/，Vite 构建时 base 写死的就是这个前缀，两边对得上。
func TestStaticServesFileUnderPrefix(t *testing.T) {
	h := newStaticHandler(builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/assets/index-abc123.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if rec.Body.String() != "console.log(1)" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// 缓存策略完全交给 CDN 自己配置，源站不替它做决定——/static/ 下的文件
// 不管带不带内容哈希，源站都不设 Cache-Control。跟 index.html（有专属
// 的 ETag/no-cache 逻辑）刻意不同，见 TestStaticServesIndexAtRoot。
func TestStaticDoesNotSetCacheControlUnderPrefix(t *testing.T) {
	h := newStaticHandler(builtConsole())
	for _, p := range []string{"/static/assets/index-abc123.js", "/static/favicon.svg"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", p, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "" {
			t.Fatalf("%s: Cache-Control = %q，期望源站不设置任何缓存头", p, cc)
		}
	}
}

// 【辨别力】/static/ 下**不存在**的文件必须 404，不能回退到 index.html。
//
// 没有这条的话，一个"任何找不到的路径都回 index.html"的实现会通过上面
// 所有测试，而线上表现是：某个 JS 文件名写错时，浏览器拿到一份 HTML 并
// 以 text/html 执行它，报出一个和真实原因毫无关系的语法错误。
func TestStaticDoesNotFallBackForMissingAssets(t *testing.T) {
	h := newStaticHandler(builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/assets/does-not-exist.js", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404; body = %q", rec.Code, rec.Body.String())
	}
}

// 没有 /static/ 前缀的旧式资源路径（迁移前的写法）现在应当落到 SPA 回退，
// 而不是意外还能访问到——确认前缀是真的生效了，不是摆设。
func TestStaticUnprefixedAssetPathFallsBackToIndex(t *testing.T) {
	h := newStaticHandler(builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "id=root") {
		t.Fatalf("code = %d body = %q，期望落到 SPA 回退（index.html）", rec.Code, rec.Body.String())
	}
}

// 未构建时给一句人话，而不是空白页或 panic。
func TestStaticReportsNotBuilt(t *testing.T) {
	h := newStaticHandler(unbuiltConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "build-web.sh") {
		t.Fatalf("提示里应当告诉人怎么构建，实际 body = %q", rec.Body.String())
	}
}

func TestStaticNilFSReportsNotBuilt(t *testing.T) {
	h := newStaticHandler(nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}
