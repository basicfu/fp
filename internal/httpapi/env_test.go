package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

// newAdminEnv 装配一套完整的管理 API 环境，并返回一个已登录管理员的 token。
func newAdminEnv(t *testing.T) (http.Handler, string, httpapi.Deps) {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	users := service.NewUserService(pool)
	codes := notify.NewCodeService(rdb)
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(users)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	if err := reg.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}

	sessions := service.NewSessionService(store.NewSessionStore(rdb), store.NewRevokePublisher(rdb))
	logs := service.NewLoginLogService(pool)
	deps := httpapi.Deps{
		Admin:    service.NewAdminService(pool, rdb),
		Apps:     service.NewApplicationService(pool),
		Users:    users,
		Accounts: service.NewAccountService(users, sessions, logs),
		Sessions: sessions,
		Logs:     logs,
		Registry: reg,
	}

	ctx := context.Background()
	if err := deps.Admin.EnsureBootstrap(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	token, err := deps.Admin.Login(ctx, "admin", "secret123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return httpapi.NewRouter(deps), token, deps
}

// do 发一个请求并返回响应记录器。token 为空时不带鉴权头。
func do(t *testing.T, h http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// decode 把响应体解析进 v，失败时终止测试。
func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("解析响应失败: %v, body = %s", err, rec.Body.String())
	}
}
