package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestConnectorSchemasExposed(t *testing.T) {
	h, token, _ := newAdminEnv(t)

	rec := do(t, h, token, http.MethodGet, "/admin/api/connectors", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var schemas []struct {
		Type   string `json:"type"`
		Fields []struct {
			Key  string `json:"key"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	decode(t, rec, &schemas)
	if len(schemas) != 2 {
		t.Fatalf("登录方式数 = %d, want 2", len(schemas))
	}
	// Registry.Types() 按字典序，password 在 sms_code 之前
	if schemas[0].Type != "password" || schemas[1].Type != "sms_code" {
		t.Fatalf("顺序不对: %+v", schemas)
	}
	if len(schemas[0].Fields) == 0 {
		t.Fatal("password 的 fields 为空，动态表单将渲染不出任何控件")
	}
}

func TestApplicationCRUDOverHTTP(t *testing.T) {
	h, token, _ := newAdminEnv(t)

	rec := do(t, h, token, http.MethodGet, "/admin/api/applications", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("空列表 body = %s, want []", rec.Body.String())
	}

	rec = do(t, h, token, http.MethodPost, "/admin/api/applications",
		`{"name":"新项目前台","slug":"newproj-web"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Application struct {
			ID    string `json:"id"`
			AppID string `json:"appId"`
		} `json:"application"`
		AppSecret string `json:"appSecret"`
	}
	decode(t, rec, &created)
	if created.AppSecret == "" || created.Application.AppID == "" {
		t.Fatalf("创建响应缺字段: %s", rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, "/admin/api/applications/"+created.Application.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"idleTimeoutSeconds":604800`) {
		t.Fatalf("默认会话策略未返回: %s", rec.Body.String())
	}
	// 详情响应绝不能包含密钥
	if strings.Contains(rec.Body.String(), created.AppSecret) {
		t.Fatal("详情响应泄露了 appSecret")
	}
}

func TestUpdateSessionPolicyValidation(t *testing.T) {
	h, token, _ := newAdminEnv(t)

	rec := do(t, h, token, http.MethodPost, "/admin/api/applications", `{"name":"A","slug":"a"}`)
	var created struct {
		Application struct {
			ID string `json:"id"`
		} `json:"application"`
	}
	decode(t, rec, &created)
	path := "/admin/api/applications/" + created.Application.ID + "/session"

	// extend_interval 不小于 idle_timeout：非法
	rec = do(t, h, token, http.MethodPut, path,
		`{"idleTimeoutSeconds":604800,"idleTimeoutMobileSeconds":2592000,"maxLifetimeSeconds":7776000,"rotateIntervalSeconds":86400,"extendIntervalSeconds":604800,"tokenCacheTtlSeconds":30}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法策略 status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodPut, path,
		`{"idleTimeoutSeconds":604800,"idleTimeoutMobileSeconds":2592000,"maxLifetimeSeconds":7776000,"rotateIntervalSeconds":86400,"extendIntervalSeconds":600,"tokenCacheTtlSeconds":60}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法策略 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"tokenCacheTtlSeconds":60`) {
		t.Fatalf("策略未生效: %s", rec.Body.String())
	}
}

func TestApplicationConnectorOverHTTP(t *testing.T) {
	h, token, _ := newAdminEnv(t)

	rec := do(t, h, token, http.MethodPost, "/admin/api/applications", `{"name":"A","slug":"a"}`)
	var created struct {
		Application struct {
			ID string `json:"id"`
		} `json:"application"`
	}
	decode(t, rec, &created)
	base := "/admin/api/applications/" + created.Application.ID + "/connectors"

	rec = do(t, h, token, http.MethodPut, base+"/password", `{"enabled":true,"config":{"allowEmail":true}}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("put status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, base, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"password"`) ||
		!strings.Contains(rec.Body.String(), `"allowEmail":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// 每一条受保护路由都要覆盖，尤其是会改状态的那些。
//
// 只测四条 GET 是不够的：DELETE /users/{id}/sessions、PUT /users/{id}/password、
// PATCH /users/{id}/status、PUT /applications/{id}/connectors/{type} 全都可以
// 被挪到 requireAdmin 组之外而没有任何测试变红——而它们恰恰是危险的那几条。
func TestAdminAPIRequiresAuth(t *testing.T) {
	h, _, _ := newAdminEnv(t)

	const someUUID = "00000000-0000-0000-0000-000000000001"
	routes := []struct{ method, path string }{
		{http.MethodGet, "/admin/api/me"},
		{http.MethodPost, "/admin/api/logout"},
		{http.MethodGet, "/admin/api/connectors"},
		{http.MethodGet, "/admin/api/applications"},
		{http.MethodPost, "/admin/api/applications"},
		{http.MethodGet, "/admin/api/applications/" + someUUID},
		{http.MethodPut, "/admin/api/applications/" + someUUID + "/session"},
		{http.MethodGet, "/admin/api/applications/" + someUUID + "/connectors"},
		{http.MethodPut, "/admin/api/applications/" + someUUID + "/connectors/password"},
		{http.MethodGet, "/admin/api/users"},
		{http.MethodGet, "/admin/api/users/" + someUUID},
		{http.MethodPatch, "/admin/api/users/" + someUUID + "/status"},
		{http.MethodPut, "/admin/api/users/" + someUUID + "/password"},
		{http.MethodGet, "/admin/api/users/" + someUUID + "/sessions"},
		{http.MethodDelete, "/admin/api/users/" + someUUID + "/sessions"},
		{http.MethodDelete, "/admin/api/users/" + someUUID + "/sessions/sid"},
		{http.MethodGet, "/admin/api/users/" + someUUID + "/login-logs"},
	}
	for _, r := range routes {
		rec := do(t, h, "", r.method, r.path, `{}`)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", r.method, r.path, rec.Code)
		}
	}
}

func TestBadUUIDReturns400(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	rec := do(t, h, token, http.MethodGet, "/admin/api/applications/not-a-uuid", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
