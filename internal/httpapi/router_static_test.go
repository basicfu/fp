package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// builtConsole 模拟一份构建好的前端产物。
//
// 与 static_test.go 里的同名 fixture 内容一致，但这里必须独立定义一份：
// 本文件是 package httpapi_test（黑盒测试，需要用到 newTestRouterWithConsole
// 这个只能看见 httpapi 导出 API 的辅助函数），而 static_test.go 是
// package httpapi（白盒测试，直接调 newStaticHandler）。两个测试包在
// Go 里是相互不可见的独立包，即便同在一个目录，也无法共享未导出的
// 测试辅助——这正是本任务 brief 里提到的"以 env_test.go 实际情况为准"
// 需要适配的地方。
func builtConsole() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/index-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/index-def456.css": {Data: []byte("body{}")},
		"favicon.svg":             {Data: []byte("<svg/>")},
	}
}

// 【辨别力】这条是整个任务里最要紧的一条：API 的 404 必须是 JSON，
// 绝不能被 SPA 回退吃掉变成 index.html。
func TestRouterAPINotFoundStaysJSON(t *testing.T) {
	// 用本包既有的路由脚手架装配一个带 Console 的 router。
	h := newTestRouterWithConsole(t, builtConsole())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/api/no-such-endpoint", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON——API 的 404 被 SPA 回退吃掉了", ct)
	}
	// errorBody 是 httpapi 包内部类型，这里是黑盒测试看不到，用等价的本地
	// 结构体解析（其余用例文件里 login/me 之类响应也是这么处理的）。
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if body.Error == "" {
		t.Fatal("error 字段为空")
	}
}

// /healthz 是显式注册的路由，不能被 /* 通配吃掉。
func TestRouterHealthzNotShadowedByStatic(t *testing.T) {
	h := newTestRouterWithConsole(t, builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "id=root") {
		t.Fatal("/healthz 返回了 index.html")
	}
}
