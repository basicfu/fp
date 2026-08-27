package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/internal/service"
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
