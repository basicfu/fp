package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/service"
)

// 没有 state 存储时回调一律按过期处理，渲染结果页而不是报错。
func TestOAuthCallbackExpiredPage(t *testing.T) {
	h := NewRouter(Deps{Auth: service.NewAuthService(service.AuthDeps{})})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/callback?state=x&code=y", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "登录已过期") {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

func TestSafeAppLink(t *testing.T) {
	for link, want := range map[string]bool{
		"myapp://oauth?fpTicket=abc-_123":     true,
		"myapp://oauth?fpError=cancelled":     true,
		"javascript://oauth?x=1":              false,
		"javascript:alert(1)":                 false,
		"myapp://oauth?fpTicket=a\"><script>": false,
	} {
		if got := safeAppLink(link); got != want {
			t.Errorf("%q: got %v, want %v", link, got, want)
		}
	}
}
