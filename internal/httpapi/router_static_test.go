package httpapi_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/web"
)

// builtConsole 模拟一份构建好的前端产物。
//
// 与 static_test.go 里的同名 fixture 内容一致，但这里必须独立定义一份：
// 本文件是 package httpapi_test（黑盒测试，只能看见 httpapi 导出的
// API），而 static_test.go 是 package httpapi（白盒测试，直接调
// newStaticHandler）。两个测试包在 Go 里是相互不可见的独立包，即便同在
// 一个目录，也无法共享未导出的测试辅助。
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
//
// 装配用的是零值 Deps（除 Console 外全部留空/nil），不经过 newAdminEnv、
// 不连库。这条测试守护的性质——/admin/api 子路由自己的 NotFound
// （router.go 里 r.NotFound(...)，注册在 requireAdmin 所在的 r.Group
// 之外，参见该文件 r.Route("/admin/api", ...) 的开头几行）——从头到尾
// 不涉及任何 service：chi 对某个路径判定"在 /admin/api 下找不到匹配
// 路由"发生在路由树匹配阶段，早于且独立于 requireAdmin 中间件与任何
// handler，所以 GET /admin/api/no-such-endpoint 根本不会走到会解引用
// d.Admin/d.Apps 等字段的代码。
//
// 这个结论不是照搬 TestRouterServesRealEmbeddedConsoleAtRoot 里"GET /
// 不会 panic"的推导——那条测试命中的是完全不同的一条路径（兜底静态
// handler），这里实测的是 /admin/api 分支本身。已经实际跑通（不 panic、
// 断言全部通过），不是纸面假设。
func TestRouterAPINotFoundStaysJSON(t *testing.T) {
	h := httpapi.NewRouter(httpapi.Deps{Console: builtConsole()})

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
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	// 断言 code 而不只是"有内容"：路由层的 404 与"资源不存在"是两回事，
	// 调用方要能据此区分"URL 写错了"和"这个 id 查不到"。
	if body.Code != domain.CodeRouteNotFound {
		t.Fatalf("code = %q, want %q", body.Code, domain.CodeRouteNotFound)
	}
	if body.Msg == "" {
		t.Fatal("msg 字段为空")
	}
}

// 【终审必须修】管理控制台在这条分支之前没有任何浏览器 UI，缺
// X-Frame-Options 之前是纯理论问题；现在控制台能被 <iframe> 嵌入的话，
// 已登录管理员访问一个恶意页面就可能被 clickjack 成一次"停用应用"之类的
// 点击。这条断言响应头在三类路径上都存在：/healthz（不经过 /admin/api
// 分组）、/admin/api 下的 404（经过 /admin/api 分组但不经过
// requireAdmin，因为路由树匹配阶段就没找到）、静态兜底（/* 通配，走的是
// newStaticHandler）——覆盖 r.Use 中间件链能触达的全部三条分支，
// 不只测其中一条侥幸过关的路径。
func TestRouterSetsXFrameOptionsDenyOnAllPaths(t *testing.T) {
	h := httpapi.NewRouter(httpapi.Deps{Console: builtConsole()})

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/healthz", nil),
		httptest.NewRequest(http.MethodGet, "/admin/api/no-such-endpoint", nil),
		httptest.NewRequest(http.MethodGet, "/", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Fatalf("%s: X-Frame-Options = %q, want %q——控制台可以被 iframe 嵌套，已登录管理员能被 clickjack", req.URL.Path, got, "DENY")
		}
	}
}

// /healthz 是显式注册的路由，不能被 /* 通配吃掉。
//
// 同样用零值 Deps 装配：/healthz 的 handler
// （router.go: r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {...})）
// 是一个不捕获任何依赖的纯闭包，只是直接写一个固定的 JSON 响应，与
// d.Admin/d.Apps 等字段完全无关，实测确认零值 Deps 不影响这条路径。
func TestRouterHealthzNotShadowedByStatic(t *testing.T) {
	h := httpapi.NewRouter(httpapi.Deps{Console: builtConsole()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "id=root") {
		t.Fatal("/healthz 返回了 index.html")
	}
}

// 【终审】静态兜底 r.Handle("/*", ...) 之前对所有 HTTP 方法都生效。
//
// 失败场景：前端某处漏写 /admin/api 前缀，DELETE /users/123 本该打到
// /admin/api/users/123，写错后落到这条 /* 通配，兜底 handler 不管方法一律
// 回 200 + index.html——前端 api.ts 的 request() 看到 200 会继续
// res.json()，jsdom/浏览器把这段 HTML 当 JSON 解析会抛一个和"URL 前缀写
// 错"毫无关系的语法错误，几乎没法从错误消息联想到真实原因。改成只对
// GET/HEAD 回退之后，非法方法应当拿到 404 或 405（chi 默认行为），而不是
// 200——用两个都断言，不管 chi 具体选哪个都算过，但绝不能是 200。
func TestStaticOnlyFallsBackForGetAndHead(t *testing.T) {
	h := httpapi.NewRouter(httpapi.Deps{Console: builtConsole()})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/some/spa/path", nil))

	if rec.Code == http.StatusOK {
		t.Fatalf("DELETE /some/spa/path 返回了 200，body = %q——非 GET/HEAD 方法不应该拿到 SPA 回退", rec.Body.String())
	}
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE /some/spa/path code = %d，想要 404 或 405", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "id=root") {
		t.Fatalf("DELETE /some/spa/path 的响应体里出现了 index.html 的内容：%q", rec.Body.String())
	}
}

// TestRouterServesRealEmbeddedConsoleAtRoot 用真正嵌进二进制的 web.Dist()
// 装配路由，而不是本文件其余用例里 builtConsole() 造的假 fstest.MapFS，
// 确认"go:embed 真的把前端产物嵌进来了"，而不仅仅是 newStaticHandler /
// NewRouter 的路由逻辑本身是对的。
//
// 这条测试要补的缺口：builtConsole() 系列的假产物内容恒定，就算
// web/embed.go 的 //go:embed 指令路径写错、或者构建脚本没跑，上面那些
// 用假 fs 的测试也会照样全绿——线上表现却是打开控制台看到 503。只有
// 直接问 web.Dist() 本身，才能抓住这类问题。
//
// Deps 除 Console 外全部留空（零值/nil）：NewRouter 构造时只是把这些
// 指针存进 adminHandler/applicationHandler/userHandler/connectorHandler
// 的结构体字段，requireAdmin(nil) 返回的也只是一个捕获了 nil 的闭包，
// 全部推迟到请求真正落进 /admin/api 分组、调用某个 handler 方法时才会
// 解引用。本测试只发一个 GET /，被 chi 路由到最后兜底的
// r.Handle("/*", newStaticHandler(...))，从未经过 /admin/api 分组，
// 所以不会 panic——这是先读过 router.go 与 middleware.go 的
// requireAdmin 得出的假设，并且已经用本测试的实际通过验证过，不是纸面
// 推导；如果这个假设错了，chi 的 middleware.Recoverer 也只会把 panic
// 转成 500 而不是让测试进程崩溃，但下面的断言会因为 code != 200 而失败，
// 从而暴露出来。
//
// 前端未构建（web/dist 只有 .gitkeep）时优雅跳过，理由与
// internal/integration 包里的 TestEmbeddedConsoleIsPresentOrClearlyAbsent
// 相同：仓库干净 clone 出来 web/dist 就是空的，硬性要求会让全量测试在
// 干净 clone 上必红。
func TestRouterServesRealEmbeddedConsoleAtRoot(t *testing.T) {
	dist := web.Dist()
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		t.Skip("前端未构建（web/dist 为空）——跑 ./scripts/build-web.sh 后本测试才有意义")
	}

	h := httpapi.NewRouter(httpapi.Deps{Console: dist})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200；body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="root"`) {
		t.Fatalf("响应体里没有 id=\"root\"，看起来不是真实构建出的控制台 index.html：%q", body)
	}
	if strings.Contains(body, "尚未构建") {
		t.Fatal("拿到了「前端尚未构建」的 503 提示页，而不是真实 index.html")
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Fatalf("index.html 的 Cache-Control = %q，必须含 no-cache", cc)
	}
}
