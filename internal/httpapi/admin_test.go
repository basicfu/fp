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
	svc := service.NewAdminService(testsupport.NewTestDB(t), testsupport.NewTestRedis(t))
	if err := svc.EnsureBootstrap(context.Background(), "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	return httpapi.NewRouter(svc), svc
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
