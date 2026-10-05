package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func newAdminServer(t *testing.T) (http.Handler, *service.AdminService) {
	t.Helper()
	h, _, deps := newAdminEnv(t)
	return h, deps.Admin
}

func TestHealthz(t *testing.T) {
	h, _ := newAdminServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestHealth 钉住 /health 是纯文本 "ok"，不是 JSON——给探活探针用的，
// 与面向人/脚本的 /healthz 并存，不是它的替代品。
func TestHealth(t *testing.T) {
	h, _ := newAdminServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "ok")
	}
}

func TestAdminLoginThenMe(t *testing.T) {
	h, _ := newAdminServer(t)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"username":"admin","password":"secret123456"}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/api/login", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatalf("解析登录响应: %v", err)
	}

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("me status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), `"username":"admin"`) {
		t.Fatalf("me body = %s", rec2.Body.String())
	}
}

// 管理端会话 cookie 在生产必须带 Secure。
//
// 这是一个有效期两小时、能停用应用/冻结账号/重置任意用户密码的凭据。
// 没有 Secure，它会在任何一段明文 HTTP 上原样出现——反代前面掉了 TLS、
// 走内网跳板、用户手滑打了 http://，都足以让它被旁路抓走。
// 同时必须保留 HttpOnly（挡 XSS 读取）和 SameSite=Lax（挡跨站 CSRF）。
func TestAdminSessionCookieAttributes(t *testing.T) {
	_, _, deps := newAdminEnv(t)

	login := func(secure bool) *http.Cookie {
		t.Helper()
		deps.SecureCookies = secure
		h := httpapi.NewRouter(deps)

		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"username":"admin","password":"secret123456"}`)
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/api/login", body))
		if rec.Code != http.StatusOK {
			t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == "fp_admin" {
				return c
			}
		}
		t.Fatal("登录响应里没有 fp_admin cookie")
		return nil
	}

	got := login(true)
	if !got.Secure {
		t.Fatal("SecureCookies=true 时 cookie 没有 Secure——管理员凭据会走明文")
	}
	if !got.HttpOnly {
		t.Fatal("cookie 丢了 HttpOnly")
	}
	if got.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", got.SameSite)
	}

	// 本地开发拿不到 TLS，所以这个开关必须真的能关掉，不能写死。
	if got := login(false); got.Secure {
		t.Fatal("SecureCookies=false 时不该带 Secure——本地开发会登不进去")
	}
}

func TestMeRequiresAuth(t *testing.T) {
	h, _ := newAdminServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/api/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAdminLoginWrongPassword(t *testing.T) {
	h, _ := newAdminServer(t)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"username":"admin","password":"nope"}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/api/login", body))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAdminLoginReportsDefaultPassword(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	svc := service.NewAdminService(pool, rdb)
	if err := svc.EnsureBootstrap(context.Background(), "admin", "admin"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	h := httpapi.NewRouter(httpapi.Deps{Admin: svc})

	rec := do(t, h, "", http.MethodPost, "/admin/api/login", `{"username":"admin","password":"admin"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"defaultPassword":true`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminLoginDoesNotReportDefaultPasswordForCustomPassword(t *testing.T) {
	h, _, _ := newAdminEnv(t) // 引导密码是 secret123456
	rec := do(t, h, "", http.MethodPost, "/admin/api/login", `{"username":"admin","password":"secret123456"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"defaultPassword":false`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestChangeAccountKeepsCurrentSessionAndRevokesOthers(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	other, err := deps.Admin.Login(context.Background(), "admin", "secret123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	rec := do(t, h, token, http.MethodPut, "/admin/api/me",
		`{"username":"boss","oldPassword":"secret123456","newPassword":"brand-new-pass"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token    string `json:"token"`
		Username string `json:"username"`
	}
	decode(t, rec, &out)
	if out.Username != "boss" || out.Token == "" {
		t.Fatalf("响应 = %+v", out)
	}
	// defaultPassword 只属于登录响应：这里恒为 false，新密码设成 admin 时还是错的。
	if strings.Contains(rec.Body.String(), "defaultPassword") {
		t.Fatalf("改账号的响应不该带 defaultPassword: %s", rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "fp_admin" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != out.Token {
		t.Fatalf("应写入换新后的 fp_admin cookie, got %+v", cookie)
	}

	if me := do(t, h, out.Token, http.MethodGet, "/admin/api/me", ""); me.Code != http.StatusOK ||
		!strings.Contains(me.Body.String(), `"username":"boss"`) {
		t.Fatalf("新会话 /me: %d %s", me.Code, me.Body.String())
	}
	for name, old := range map[string]string{"发起修改的旧会话": token, "其他会话": other} {
		if rec := do(t, h, old, http.MethodGet, "/admin/api/me", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 应已失效, status = %d", name, rec.Code)
		}
	}
}

// 【辨别力】旧密码错必须是 400 而不是 401：前端对任何 401 都清登录态并跳登录页。
func TestChangeAccountWrongOldPasswordIs400AndKeepsSession(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	rec := do(t, h, token, http.MethodPut, "/admin/api/me",
		`{"username":"admin","oldPassword":"nope","newPassword":"x"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ADMIN_OLD_PASSWORD_WRONG") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if me := do(t, h, token, http.MethodGet, "/admin/api/me", ""); me.Code != http.StatusOK {
		t.Fatalf("失败的修改不该让会话失效, status = %d", me.Code)
	}
}

func TestChangeAccountRequiresAuthAndRejectsUnknownFields(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	// 未登录的 body 用 {}：路由若被挪出鉴权组，空登录名只会得到 400 而不是 401，才分得出来。
	if rec := do(t, h, "", http.MethodPut, "/admin/api/me", `{}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 status = %d, want 401", rec.Code)
	}
	// 旧密码给对：解析若被放宽，这次修改会成功（200）并让该 token 失效；
	// 只有严格解析才会在到达服务层之前就 400。旧密码给错的话，放宽后同样是 400，分不出来。
	if rec := do(t, h, token, http.MethodPut, "/admin/api/me", `{"username":"a","oldPassword":"secret123456","extra":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("未知字段 status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	me := do(t, h, token, http.MethodGet, "/admin/api/me", "")
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"username":"admin"`) {
		t.Fatalf("被拒绝的请求不该改动账号或让会话失效: %d %s", me.Code, me.Body.String())
	}
}
