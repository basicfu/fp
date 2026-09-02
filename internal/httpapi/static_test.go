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

// 带内容哈希的资源可以长缓存。
func TestStaticSetsImmutableOnHashedAssets(t *testing.T) {
	h := newStaticHandler(builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") {
		t.Fatalf("Cache-Control = %q，带哈希的资源应当 immutable", cc)
	}
}

// 【辨别力】assets 下**不存在**的文件必须 404，不能回退到 index.html。
//
// 没有这条的话，一个"任何找不到的路径都回 index.html"的实现会通过上面
// 所有测试，而线上表现是：某个 JS 文件名写错时，浏览器拿到一份 HTML 并
// 以 text/html 执行它，报出一个和真实原因毫无关系的语法错误。
func TestStaticDoesNotFallBackForMissingAssets(t *testing.T) {
	h := newStaticHandler(builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/does-not-exist.js", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404; body = %q", rec.Code, rec.Body.String())
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
