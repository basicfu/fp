package resp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(h http.Handler, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/x", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	Middleware(h, Options{}).ServeHTTP(rec, req)
	return rec
}

func body(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body 不是 JSON: %q (%v)", rec.Body.String(), err)
	}
	return m
}

var v2 = map[string]string{HeaderFormat: "2"}

func TestOKv2HasOnlyDataRidTime(t *testing.T) {
	rec := serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { OK(w, map[string]int{"n": 1}) }), v2)
	if rec.Code != 200 || rec.Header().Get(HeaderFormat) != "2" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	m := body(t, rec)
	if _, has := m["code"]; has {
		t.Fatal("200 不该有 code")
	}
	if _, has := m["msg"]; has {
		t.Fatal("200 不该有 msg")
	}
	if m["data"] == nil || m["rid"] == "" || m["time"] == nil {
		t.Fatalf("缺 data/rid/time: %v", m)
	}
	if m["rid"] != rec.Header().Get(HeaderRequestID) {
		t.Fatal("body 的 rid 应与响应头一致")
	}
}

func TestOKv2NilDataIsEmptyObject(t *testing.T) {
	rec := serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { OK(w, nil) }), v2)
	if d, ok := body(t, rec)["data"].(map[string]any); !ok || len(d) != 0 {
		t.Fatalf("无返回内容时 data 应为 {}: %s", rec.Body.String())
	}
}

func TestFailBusiness400(t *testing.T) {
	rec := serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		Fail(w, "param_missing", "必填项不能为空", map[string]any{"fields": []string{"phone"}}, 400)
	}), v2)
	m := body(t, rec)
	if rec.Code != 400 || m["code"] != "param_missing" || m["msg"] != "必填项不能为空" || m["data"] == nil || m["rid"] == "" || m["time"] == nil {
		t.Fatalf("%d %v", rec.Code, m)
	}
}

func TestFailBusinessWithoutDataOmitsData(t *testing.T) {
	rec := serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { Fail(w, "peer_offline", "该设备不在线", nil, 409) }), v2)
	m := body(t, rec)
	if rec.Code != 400 {
		t.Fatalf("旧 409 应归 400: %d", rec.Code)
	}
	if _, has := m["data"]; has {
		t.Fatal("没有业务详情时不该返回 data")
	}
}

func TestFailAuthClassAndRelogin(t *testing.T) {
	cases := []struct {
		code   string
		legacy int
		relog  bool
	}{
		{"unauthorized", 401, true},
		{"login_required", 401, true},
		{"kicked", 401, true},
		{"device_unverified", 403, false}, // 客户端校时重签后重试, 绝不能清登录态
		{"forbidden", 403, false},
		{"auth_failed", 401, false}, // 设备签名失败: 授权类但不是登录态问题
	}
	for _, c := range cases {
		rec := serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { Fail(w, c.code, "m", nil, c.legacy) }), v2)
		m := body(t, rec)
		if rec.Code != 401 || m["code"] != c.code {
			t.Fatalf("%s: %d %v", c.code, rec.Code, m)
		}
		d, _ := m["data"].(map[string]any)
		if got := d != nil && d["relogin"] == true; got != c.relog {
			t.Fatalf("%s: relogin = %v, 想要 %v (%v)", c.code, got, c.relog, m)
		}
	}
}

func TestLoginFailedIsBusiness(t *testing.T) {
	rec := serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		Fail(w, "login_failed", "账号或密码错误", nil, 401)
	}), v2)
	m := body(t, rec)
	if rec.Code != 400 {
		t.Fatalf("账号或密码错误是业务错误, 应 400: %d", rec.Code)
	}
	if d, _ := m["data"].(map[string]any); d != nil && d["relogin"] == true {
		t.Fatal("输错密码不该带 relogin")
	}
}

func TestFailSystem500HasFixedMsgAndNoData(t *testing.T) {
	for _, legacy := range []int{500, 502, 503, 504} {
		rec := serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			Fail(w, "unavailable", "连接 10.0.0.5:5432 失败", map[string]any{"x": 1}, legacy)
		}), v2)
		m := body(t, rec)
		if rec.Code != 500 || m["msg"] != SystemMsg {
			t.Fatalf("%d: %d %v", legacy, rec.Code, m)
		}
		if _, has := m["data"]; has {
			t.Fatal("500 不带 data")
		}
		if strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Fatal("系统错误不能把内部原因返回给用户")
		}
	}
}

func TestLegacyRequestsUnaffected(t *testing.T) {
	rec := serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if IsV2(w) {
			t.Error("没带头不该是新格式")
		}
		w.WriteHeader(204)
	}), nil)
	if rec.Code != 204 || rec.Header().Get(HeaderFormat) != "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get(HeaderRequestID) == "" {
		t.Fatal("旧格式的响应头也带 X-Request-Id")
	}
}

func TestRequestIDReusedWhenValid(t *testing.T) {
	rec := serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { OK(w, nil) }), map[string]string{HeaderFormat: "2", HeaderRequestID: "abc12345-XYZ"})
	if body(t, rec)["rid"] != "abc12345-XYZ" {
		t.Fatalf("合法的请求带来的 rid 应沿用: %v", body(t, rec))
	}
	rec = serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { OK(w, nil) }), map[string]string{HeaderFormat: "2", HeaderRequestID: "bad id!!"})
	if rid, _ := body(t, rec)["rid"].(string); len(rid) != 16 {
		t.Fatalf("非法的应重新生成 16 位: %q", rid)
	}
}

func TestStdlib404And405And413BecomeBusiness400InV2(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /only-post", func(http.ResponseWriter, *http.Request) {})
	h := Middleware(mux, Options{})
	for _, c := range []struct {
		method, path, code string
	}{{"GET", "/nope", "not_found"}, {"GET", "/only-post", "method_not_allowed"}} {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set(HeaderFormat, "2")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		m := body(t, rec)
		if rec.Code != 400 || m["code"] != c.code {
			t.Fatalf("%s %s: %d %v", c.method, c.path, rec.Code, m)
		}
	}
	// 旧格式: 标准库的纯文本 404 不动。
	req := httptest.NewRequest("GET", "/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "404 page not found") {
		t.Fatalf("旧格式应保持标准库响应: %d %q", rec.Code, rec.Body.String())
	}
}

func TestPanicBecomes500InBothFormats(t *testing.T) {
	var reported int
	h := Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { var m map[int]int; m[1] = 1 }),
		Options{OnPanic: func(*http.Request, any) { reported++ }})
	for _, hdr := range []map[string]string{v2, nil} {
		req := httptest.NewRequest("GET", "/x", nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		m := body(t, rec)
		if rec.Code != 500 || m["code"] != "internal" || m["time"] == nil {
			t.Fatalf("%v: %d %v", hdr, rec.Code, m)
		}
		if hdr != nil && m["rid"] == nil {
			t.Fatal("新格式的 500 带 rid")
		}
	}
	if reported != 2 {
		t.Fatalf("OnPanic 应被调用: %d", reported)
	}
}

// ErrAbortHandler 是"静默中止": 不回 500 体, 直接断开连接(客户端读到 EOF)。
func TestPanicAbortHandlerClosesConnection(t *testing.T) {
	srv := httptest.NewServer(Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }), Options{}))
	defer srv.Close()
	if resp, err := http.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatalf("应断开连接而不是回响应: %d", resp.StatusCode)
	}
}

func TestWrapperKeepsFlusherAndUnwrap(t *testing.T) {
	var flushed, unwrapped bool
	serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
			flushed = true
		}
		if _, ok := w.(interface{ Unwrap() http.ResponseWriter }); ok {
			unwrapped = true
		}
	}), v2)
	if !flushed || !unwrapped {
		t.Fatalf("包装后的 writer 要保留 Flush / Unwrap(流式与 ResponseController 依赖): %v %v", flushed, unwrapped)
	}
}
