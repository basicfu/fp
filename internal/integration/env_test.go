// Package integration_test 把 fp 的各层串起来做端到端验证。
// 它只使用各包的公开 API——若这里需要访问私有细节，说明分层出了问题。
package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

const (
	adminUser = "admin"
	adminPass = "secret123456"
)

// env 是一套完整装配好的 fp。
type env struct {
	t      *testing.T
	server *httptest.Server

	auth     *service.AuthService
	apps     *service.ApplicationService
	users    *service.UserService
	sessions *service.SessionService
	logs     *service.LoginLogService
	sms      *notify.FakeProvider
	revoke   *store.RevokePublisher

	adminToken string
}

// newEnv 按 cmd/fp/main.go 的方式装配全部依赖，只把短信通道换成假供应商。
func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	ctx := context.Background()

	apps := service.NewApplicationService(pool)
	users := service.NewUserService(pool)
	logs := service.NewLoginLogService(pool)
	codes := notify.NewCodeService(rdb)

	sessionStore := store.NewSessionStore(rdb)
	revokePub := store.NewRevokePublisher(rdb)
	sessions := service.NewSessionService(sessionStore, revokePub)

	registry := connector.NewRegistry()
	if err := registry.Register(connector.NewPassword(users)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	if err := registry.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}

	sms := notify.NewFakeProvider(notify.ChannelSMS, "fake")
	sender := notify.NewSender(pool, store.NewRateLimiter(rdb), nil)
	sender.AddProvider(sms)

	admin := service.NewAdminService(pool, rdb)
	if err := admin.EnsureBootstrap(ctx, adminUser, adminPass); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{
		Admin: admin, Apps: apps, Users: users,
		Accounts: service.NewAccountService(users, sessions),
		Sessions: sessions, Logs: logs, Registry: registry,
	}))
	t.Cleanup(srv.Close)

	return &env{
		t: t, server: srv,
		auth: service.NewAuthService(service.AuthDeps{
			Apps: apps, Users: users, Sessions: sessions, Logs: logs,
			Registry: registry, Notifier: sender, Codes: codes,
		}),
		apps: apps, users: users, sessions: sessions, logs: logs,
		sms: sms, revoke: revokePub,
	}
}

// adminLogin 通过 HTTP 登录管理控制台，并把 token 记在 env 上。
func (e *env) adminLogin() string {
	e.t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	e.request(http.MethodPost, "/admin/api/login",
		`{"username":"`+adminUser+`","password":"`+adminPass+`"}`, http.StatusOK, &out)
	e.adminToken = out.Token
	return out.Token
}

// request 发一个管理 API 请求，断言状态码，并把响应体解析进 out（out 为 nil 时跳过）。
func (e *env) request(method, path, body string, wantStatus int, out any) {
	e.t.Helper()

	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req *http.Request
	var err error
	if reader != nil {
		req, err = http.NewRequest(method, e.server.URL+path, reader)
	} else {
		req, err = http.NewRequest(method, e.server.URL+path, nil)
	}
	if err != nil {
		e.t.Fatalf("构造请求 %s %s: %v", method, path, err)
	}
	if e.adminToken != "" {
		req.Header.Set("Authorization", "Bearer "+e.adminToken)
	}

	resp, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatalf("请求 %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	// 先把 body 读完再判断状态码：失败时要把它打进错误信息，
	// 成功时还要解析，一次读取两处都能用。
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("读取 %s %s 响应: %v", method, path, err)
	}
	if resp.StatusCode != wantStatus {
		e.t.Fatalf("%s %s status = %d, want %d, body = %s",
			method, path, resp.StatusCode, wantStatus, string(raw))
	}
	if out == nil {
		return
	}
	if err := json.Unmarshal(raw, out); err != nil {
		e.t.Fatalf("解析 %s %s 响应: %v, body = %s", method, path, err, string(raw))
	}
}

// smsLogin 走完整的「发码 → 取码 → 登录」流程。
func (e *env) smsLogin(appID, phone string) *service.LoginResult {
	e.t.Helper()
	ctx := context.Background()

	if err := e.auth.SendLoginCode(ctx, appID, phone); err != nil {
		e.t.Fatalf("SendLoginCode: %v", err)
	}
	code := e.sms.LastParam("code")
	if code == "" {
		e.t.Fatal("未从假供应商取到验证码")
	}
	res, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         appID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": phone, "code": code},
		IP:            "203.0.113.7", UA: "integration-test",
	})
	if err != nil {
		e.t.Fatalf("Login(sms_code): %v", err)
	}
	return res
}

// createApp 通过管理 API 建应用并启用两种登录方式，返回 appId 与明文 secret。
func (e *env) createApp(name, slug string) (internalID, appID, secret string) {
	e.t.Helper()

	var created struct {
		Application struct {
			ID    string `json:"id"`
			AppID string `json:"appId"`
		} `json:"application"`
		AppSecret string `json:"appSecret"`
	}
	e.request(http.MethodPost, "/admin/api/applications",
		`{"name":"`+name+`","slug":"`+slug+`"}`, http.StatusCreated, &created)

	base := "/admin/api/applications/" + created.Application.ID + "/connectors"
	e.request(http.MethodPut, base+"/password", `{"enabled":true,"config":{}}`, http.StatusNoContent, nil)
	e.request(http.MethodPut, base+"/sms_code", `{"enabled":true,"config":{}}`, http.StatusNoContent, nil)

	return created.Application.ID, created.Application.AppID, created.AppSecret
}

// waitFor 每 20ms 检查一次 cond，直到成立或超时。
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}
