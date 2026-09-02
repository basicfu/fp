package httpapi_test

import (
	"context"
	"encoding/json"
	"io/fs"
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

	epochs := store.NewEpochStore(rdb)
	sessions := service.NewSessionService(store.NewSessionStore(rdb), store.NewRevokePublisher(rdb), epochs)
	logs := service.NewLoginLogService(pool)
	deps := httpapi.Deps{
		Admin:    service.NewAdminService(pool, rdb),
		Apps:     service.NewApplicationService(pool, reg),
		Users:    users,
		Accounts: service.NewAccountService(users, sessions, epochs, logs),
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

// newTestRouterWithConsole 复用 newAdminEnv 装配好的依赖，额外挂上一份
// 前端静态资源，用于测试 /admin/api 与静态兜底路由（/*）之间的路由优先级。
//
// 不单独拼一套依赖：newAdminEnv 已经把 Admin/Apps/Users 等服务和真实的
// Postgres/Redis 接好了，这里只是在其基础上多设置 Console 字段后重新
// 装一次 router——与 TestAdminSessionCookieAttributes 里"改一个字段、
// 重新 NewRouter"的写法一致。
func newTestRouterWithConsole(t *testing.T, fsys fs.FS) http.Handler {
	t.Helper()
	_, _, deps := newAdminEnv(t)
	deps.Console = fsys
	return httpapi.NewRouter(deps)
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
