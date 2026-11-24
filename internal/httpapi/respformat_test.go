package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fetch(h http.Handler, method, path string, v2 bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if v2 {
		req.Header.Set("X-Resp-Format", "2")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// 带 X-Resp-Format: 2 成功响应统一成 {data, rid, time}; 不带则保持原来的 {"status":"ok"}。
func TestRespFormatNegotiation(t *testing.T) {
	h := NewRouter(Deps{})

	old := fetch(h, "GET", "/healthz", false)
	if old.Code != 200 || old.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("旧格式应原样: %d %q", old.Code, old.Body.String())
	}
	if old.Header().Get("X-Request-Id") == "" {
		t.Fatal("旧格式的响应头也带 X-Request-Id")
	}

	nw := fetch(h, "GET", "/healthz", true)
	var m map[string]any
	if err := json.Unmarshal(nw.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	data, _ := m["data"].(map[string]any)
	if nw.Code != 200 || data["status"] != "ok" || m["rid"] == nil || m["time"] == nil || m["code"] != nil || m["msg"] != nil {
		t.Fatalf("新格式 200 只该有 data/rid/time: %d %v", nw.Code, m)
	}
}

// 管理接口的 JSON 404 在新格式下是 400 + not_found; 旧格式仍是 404。
func TestRespFormatUnknownAdminRoute(t *testing.T) {
	h := NewRouter(Deps{})
	if rec := fetch(h, "GET", "/admin/api/no-such", false); rec.Code != 404 {
		t.Fatalf("旧格式: %d", rec.Code)
	}
	rec := fetch(h, "GET", "/admin/api/no-such", true)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if rec.Code != 400 || m["code"] != "not_found" || m["rid"] == nil {
		t.Fatalf("新格式: %d %v", rec.Code, m)
	}
}
