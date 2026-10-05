package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

// recordingProvider 记录收到的每一次发送。
type recordingProvider struct {
	mu    sync.Mutex
	calls []notify.Delivery
}

func (p *recordingProvider) Send(_ context.Context, d notify.Delivery) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, d)
	return nil
}

func (p *recordingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// newNotifyHTTPEnv 装配管理 API，通知服务用两种假的供应商类型（短信与 telegram），返回已登录管理员的 token。
func newNotifyHTTPEnv(t *testing.T) (http.Handler, string, *recordingProvider) {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	rec := &recordingProvider{}
	reg := notify.NewRegistry()
	schema := []domain.Field{
		{Key: "name", Label: "名称", Type: domain.FieldTypeString, Required: true},
		{Key: "token", Label: "令牌", Type: domain.FieldTypeSecret},
	}
	newRec := func(notify.Config) (notify.Provider, error) { return rec, nil }
	for _, s := range []notify.TypeSpec{
		{Type: "fake_sms", Channel: domain.NotifyChannelSMS, ConfigSchema: schema, New: newRec},
		{Type: "fake_tg", Channel: domain.NotifyChannelTelegram, ConfigSchema: schema, New: newRec},
	} {
		if err := reg.Register(s); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}

	admin := service.NewAdminService(pool, rdb)
	if err := admin.EnsureBootstrap(context.Background(), "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	token, err := admin.Login(context.Background(), "admin", "secret123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	h := httpapi.NewRouter(httpapi.Deps{Admin: admin, Notify: service.NewNotifyService(pool, rdb, reg)})
	return h, token, rec
}

type idResp struct {
	ID string `json:"id"`
}

func createProviderOverHTTP(t *testing.T, h http.Handler, token, typ, name, secret string) string {
	t.Helper()
	body := `{"type":"` + typ + `","description":"` + name + `","config":{"name":"` + name + `","token":"` + secret + `"}}`
	rec := do(t, h, token, http.MethodPost, "/admin/api/notify/providers", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建供应商 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out idResp
	decode(t, rec, &out)
	return out.ID
}

const smsTemplateBody = `{"code":"login_sms","channel":"sms","mode":"vendor","description":"登录短信",
	"content":{"content":"您的验证码是${code}","variables":["code"]}}`

func TestNotifyRoutesRequireAuth(t *testing.T) {
	h, _, _ := newNotifyHTTPEnv(t)
	for _, path := range []string{"/admin/api/notify/providers", "/admin/api/notify/templates", "/admin/api/notify/logs", "/admin/api/notify/provider-types"} {
		if rec := do(t, h, "", http.MethodGet, path, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s 未登录 status = %d, want 401", path, rec.Code)
		}
	}
}

func TestNotifyProviderTypes(t *testing.T) {
	h, token, _ := newNotifyHTTPEnv(t)
	rec := do(t, h, token, http.MethodGet, "/admin/api/notify/provider-types", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var types []struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
		Fields  []struct {
			Key  string `json:"key"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	decode(t, rec, &types)
	if len(types) != 2 || types[0].Type != "fake_sms" || types[0].Channel != "sms" || len(types[0].Fields) != 2 || types[0].Fields[1].Type != "secret" {
		t.Fatalf("types = %+v", types)
	}
}

func TestNotifyProviderLifecycleAndSecretMasking(t *testing.T) {
	h, token, _ := newNotifyHTTPEnv(t)
	id := createProviderOverHTTP(t, h, token, "fake_sms", "a", "REAL-SECRET")

	// 任何读取接口都不能把 secret 原文带出来。
	for _, path := range []string{"/admin/api/notify/providers/" + id, "/admin/api/notify/providers"} {
		rec := do(t, h, token, http.MethodGet, path, "")
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "REAL-SECRET") || !strings.Contains(rec.Body.String(), domain.SecretMask) {
			t.Fatalf("%s: status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}

	// 局部更新：掩码原样传回 = 保持原值；不带 config = 只改备注。
	rec := do(t, h, token, http.MethodPatch, "/admin/api/notify/providers/"+id, `{"config":{"name":"a2","token":"********"}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"a2"`) {
		t.Fatalf("PATCH config: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, token, http.MethodPatch, "/admin/api/notify/providers/"+id, `{"description":"主账号","enabled":false}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"description":"主账号"`) || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("PATCH 备注/启停: %d %s", rec.Code, rec.Body.String())
	}

	if rec := do(t, h, token, http.MethodPost, "/admin/api/notify/providers", `{"type":"nope","config":{}}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "NOTIFY_PROVIDER_INVALID") {
		t.Fatalf("未知类型: %d %s", rec.Code, rec.Body.String())
	}

	if rec := do(t, h, token, http.MethodDelete, "/admin/api/notify/providers/"+id, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, token, http.MethodGet, "/admin/api/notify/providers/"+id, ""); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "NOTIFY_PROVIDER_NOT_FOUND") {
		t.Fatalf("删除后 GET: %d %s", rec.Code, rec.Body.String())
	}
}

func TestNotifyTemplateLifecycle(t *testing.T) {
	h, token, _ := newNotifyHTTPEnv(t)

	if rec := do(t, h, token, http.MethodPost, "/admin/api/notify/templates", smsTemplateBody); rec.Code != http.StatusCreated {
		t.Fatalf("创建模板: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, token, http.MethodPost, "/admin/api/notify/templates", smsTemplateBody); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "NOTIFY_TEMPLATE_CODE_TAKEN") {
		t.Fatalf("重复 code: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, token, http.MethodPost, "/admin/api/notify/templates",
		`{"code":"x","channel":"sms","mode":"custom","content":{"content":"x","variables":[]}}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "NOTIFY_TEMPLATE_INVALID") {
		t.Fatalf("短信不支持 custom: %d %s", rec.Code, rec.Body.String())
	}

	rec := do(t, h, token, http.MethodPatch, "/admin/api/notify/templates/login_sms",
		`{"content":{"content":"验证码 ${code}","variables":["code"]},"enabled":false}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("PATCH: %d %s", rec.Code, rec.Body.String())
	}
	// code / channel / mode 创建后不可改：请求体里带它们就是未知字段。
	if rec := do(t, h, token, http.MethodPatch, "/admin/api/notify/templates/login_sms", `{"channel":"email"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("改 channel status = %d, want 400", rec.Code)
	}

	rec = do(t, h, token, http.MethodGet, "/admin/api/notify/templates", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"providerCount":0`) {
		t.Fatalf("列表: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, token, http.MethodDelete, "/admin/api/notify/templates/login_sms", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", rec.Code)
	}
	if rec := do(t, h, token, http.MethodGet, "/admin/api/notify/templates/login_sms", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("删除后 GET status = %d, want 404", rec.Code)
	}
}

func TestNotifyLinkLifecycleAndProviderInUse(t *testing.T) {
	h, token, _ := newNotifyHTTPEnv(t)
	pid := createProviderOverHTTP(t, h, token, "fake_sms", "a", "s")
	other := createProviderOverHTTP(t, h, token, "fake_tg", "t", "s")
	do(t, h, token, http.MethodPost, "/admin/api/notify/templates", smsTemplateBody)

	link := "/admin/api/notify/templates/login_sms/providers/" + pid
	if rec := do(t, h, token, http.MethodPut, link, `{"providerTemplateId":"","enabled":true}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "NOTIFY_LINK_INVALID") {
		t.Fatalf("vendor 缺模板 ID: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, token, http.MethodPut, "/admin/api/notify/templates/login_sms/providers/"+other,
		`{"providerTemplateId":"SMS_1","enabled":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("渠道不一致 status = %d, want 400", rec.Code)
	}
	if rec := do(t, h, token, http.MethodPut, link, `{"providerTemplateId":"SMS_1","enabled":true,"priority":5}`); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT 关联: %d %s", rec.Code, rec.Body.String())
	}

	rec := do(t, h, token, http.MethodGet, "/admin/api/notify/templates/login_sms", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"providerTemplateId":"SMS_1"`) ||
		!strings.Contains(rec.Body.String(), `"priority":5`) || !strings.Contains(rec.Body.String(), `"providerType":"fake_sms"`) {
		t.Fatalf("模板详情: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, token, http.MethodGet, "/admin/api/notify/providers/"+pid+"/templates", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"code":"login_sms"`) {
		t.Fatalf("供应商被引用: %d %s", rec.Code, rec.Body.String())
	}

	if rec := do(t, h, token, http.MethodDelete, "/admin/api/notify/providers/"+pid, ""); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "NOTIFY_PROVIDER_IN_USE") {
		t.Fatalf("被引用时删除: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, token, http.MethodDelete, link, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("解除关联: %d", rec.Code)
	}
	if rec := do(t, h, token, http.MethodDelete, "/admin/api/notify/providers/"+pid, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("解除后删除: %d", rec.Code)
	}
}

func TestNotifyTestSendAndLogs(t *testing.T) {
	h, token, rec := newNotifyHTTPEnv(t)
	pid := createProviderOverHTTP(t, h, token, "fake_sms", "a", "s")
	do(t, h, token, http.MethodPost, "/admin/api/notify/templates", smsTemplateBody)
	do(t, h, token, http.MethodPut, "/admin/api/notify/templates/login_sms/providers/"+pid, `{"providerTemplateId":"SMS_1","enabled":true}`)

	send := "/admin/api/notify/templates/login_sms/test"
	if r := do(t, h, token, http.MethodPost, send, `{"to":"13800138000","params":{"code":"123456"}}`); r.Code != http.StatusNoContent {
		t.Fatalf("测试发送: %d %s", r.Code, r.Body.String())
	}
	if rec.count() != 1 {
		t.Fatalf("供应商调用数 = %d, want 1", rec.count())
	}
	if r := do(t, h, token, http.MethodPost, send, `{"to":"13800138000","params":{}}`); r.Code != http.StatusBadRequest ||
		!strings.Contains(r.Body.String(), "NOTIFY_PARAMS_INVALID") {
		t.Fatalf("缺变量: %d %s", r.Code, r.Body.String())
	}
	if r := do(t, h, token, http.MethodPost, "/admin/api/notify/templates/nope/test", `{"params":{}}`); r.Code != http.StatusNotFound {
		t.Fatalf("未知模板 status = %d, want 404", r.Code)
	}

	r := do(t, h, token, http.MethodGet, "/admin/api/notify/logs?code=login_sms&success=true&limit=10", "")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"total":1`) || !strings.Contains(r.Body.String(), `"target":"13800138000"`) ||
		!strings.Contains(r.Body.String(), `"appId":""`) {
		t.Fatalf("发送记录: %d %s", r.Code, r.Body.String())
	}
	if strings.Contains(r.Body.String(), "123456") {
		t.Fatalf("发送记录里不能有变量取值: %s", r.Body.String())
	}
	if r := do(t, h, token, http.MethodGet, "/admin/api/notify/logs?success=false", ""); !strings.Contains(r.Body.String(), `"total":0`) {
		t.Fatalf("按失败过滤应为空: %s", r.Body.String())
	}
}
