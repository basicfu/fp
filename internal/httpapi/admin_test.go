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

// 改账号成功后所有会话作废（含发起修改的这一个），响应不再换发 token，并清掉 fp_admin cookie：
// 前端据此回登录页，用新凭据重新登录。清除用的 cookie 要与登录写入的属性一致，两种 Secure 取值
// 都跑一遍，免得清除时的属性写歪，生产里清不掉登录时设下的 Secure cookie。
func TestChangeAccountRevokesAllSessionsAndClearsCookie(t *testing.T) {
	for _, secure := range []bool{true, false} {
		label := "Secure"
		if !secure {
			label = "非 Secure"
		}
		t.Run(label, func(t *testing.T) {
			_, token, deps := newAdminEnv(t)
			deps.SecureCookies = secure
			h := httpapi.NewRouter(deps)
			other, err := deps.Admin.Login(context.Background(), "admin", "secret123456")
			if err != nil {
				t.Fatalf("Login: %v", err)
			}

			rec := do(t, h, token, http.MethodPut, "/admin/api/me",
				`{"username":"boss","oldPassword":"secret123456","newPassword":"brand-new-pass"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			// 只回显登录名：没有 token（不换发），也没有 defaultPassword（只属于登录响应）。
			var out map[string]any
			decode(t, rec, &out)
			if len(out) != 1 || out["username"] != "boss" {
				t.Fatalf("响应 = %s, want 只有 username", rec.Body.String())
			}

			var cookies []*http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == "fp_admin" {
					cookies = append(cookies, c)
				}
			}
			if len(cookies) != 1 {
				t.Fatalf("fp_admin cookie 有 %d 个, want 1（清除用的那个）", len(cookies))
			}
			c := cookies[0]
			if c.Value != "" || c.MaxAge >= 0 {
				t.Fatalf("应清除 fp_admin cookie（空值且 MaxAge < 0）, got %+v", c)
			}
			if c.Path != "/" || !c.HttpOnly || c.Secure != secure || c.SameSite != http.SameSiteLaxMode {
				t.Fatalf("清除用的 cookie 属性要与登录时一致（Path=/、HttpOnly、Secure=%v、SameSite=Lax）, got %+v", secure, c)
			}

			for name, old := range map[string]string{"发起修改的会话": token, "其他会话": other} {
				if rec := do(t, h, old, http.MethodGet, "/admin/api/me", ""); rec.Code != http.StatusUnauthorized {
					t.Fatalf("%s 应已失效, status = %d", name, rec.Code)
				}
			}

			// 用新凭据重新登录：拿到的是一个能用的新会话；defaultPassword 在登录响应里报。
			login := do(t, h, "", http.MethodPost, "/admin/api/login", `{"username":"boss","password":"brand-new-pass"}`)
			if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `"defaultPassword":false`) {
				t.Fatalf("新凭据登录 status = %d, body = %s", login.Code, login.Body.String())
			}
			var relogin struct {
				Token string `json:"token"`
			}
			decode(t, login, &relogin)
			if me := do(t, h, relogin.Token, http.MethodGet, "/admin/api/me", ""); me.Code != http.StatusOK ||
				!strings.Contains(me.Body.String(), `"username":"boss"`) {
				t.Fatalf("重新登录后的 /me: %d %s", me.Code, me.Body.String())
			}
		})
	}
}

// 只有成功才清 cookie：旧密码错（400）时会话还在，浏览器得继续带着它重试，清掉的话
// 下一次请求就是 401，前端会把用户踢回登录页。
func TestChangeAccountFailureLeavesCookieAlone(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	rec := do(t, h, token, http.MethodPut, "/admin/api/me",
		`{"username":"boss","oldPassword":"nope","newPassword":"brand-new-pass"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "fp_admin" {
			t.Fatalf("失败的修改不该动 fp_admin cookie, got %+v", c)
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
