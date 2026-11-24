package accountapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/accountapi"
	"github.com/basicfu/fp/sdk/resp"
)

func doV2(t *testing.T, e *env, method, path string, opts ...reqOpt) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Host = testHost
	r.Header.Set(accountapi.APIHeader, accountapi.APIVersion)
	r.Header.Set(deviceHeader, e.dev.header(t, time.Now()))
	r.Header.Set(resp.HeaderFormat, "2")
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	resp.Middleware(e.h, resp.Options{}).ServeHTTP(w, r)
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON: %v %q", err, w.Body.String())
	}
	return w.Code, m
}

// 新格式: 成功 200 只有 data/rid/time; 没带 token 是 401 + unauthorized + data.relogin(客户端据此清登录态)。
func TestRespFormatV2Account(t *testing.T) {
	e := newEnv(t)
	_, _, tok := e.loggedIn(t)

	code, m := doV2(t, e, "GET", "/me", withToken(tok))
	if code != http.StatusOK || m["data"] == nil || m["rid"] == nil || m["time"] == nil || m["code"] != nil || m["msg"] != nil {
		t.Fatalf("成功响应: %d %v", code, m)
	}

	code, m = doV2(t, e, "GET", "/me")
	d, _ := m["data"].(map[string]any)
	if code != http.StatusUnauthorized || m["code"] != "unauthorized" || d["relogin"] != true {
		t.Fatalf("没带 token: %d %v", code, m)
	}

	// 旧格式(不带头)仍是原来的 {code,msg,time}, 状态码也不变。
	legacyW := e.do(t, "GET", "/me", "")
	if legacyW.Code != http.StatusUnauthorized {
		t.Fatalf("旧格式状态码不变: %d", legacyW.Code)
	}
	var lm map[string]any
	_ = json.Unmarshal(legacyW.Body.Bytes(), &lm)
	if lm["code"] != "unauthorized" || lm["time"] == nil || lm["rid"] != nil || lm["data"] != nil {
		t.Fatalf("旧格式响应体应原样: %v", lm)
	}
}
