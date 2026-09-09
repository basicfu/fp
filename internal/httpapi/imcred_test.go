package httpapi_test

import (
	"strings"
	"testing"
)

// TestIMCredentialRotateReturnsSecretOnce：明文只在轮换时返回一次，之后
// 任何读接口都拿不回来——与应用的 appSecret 同一纪律。
func TestIMCredentialRotateReturnsSecretOnce(t *testing.T) {
	h, token, _ := newAdminEnv(t)

	rec := do(t, h, token, "GET", "/admin/api/im-credential", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"exists":false`) {
		t.Fatalf("初始状态应为 exists:false，got %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, "POST", "/admin/api/im-credential/rotate", "")
	if rec.Code != 200 {
		t.Fatalf("轮换失败：%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"secret"`) {
		t.Fatalf("轮换必须返回明文 secret：%s", rec.Body.String())
	}

	rec = do(t, h, token, "GET", "/admin/api/im-credential", "")
	if !strings.Contains(rec.Body.String(), `"exists":true`) {
		t.Fatalf("生成之后 exists 应为 true：%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"secret"`) {
		t.Fatalf("读接口绝不能回显 secret：%s", rec.Body.String())
	}
}

// TestIMCredentialRequiresAdmin：这条路由能换掉整个 fp-im 网关的凭据，
// 未登录必须拿不到。
func TestIMCredentialRequiresAdmin(t *testing.T) {
	h, _, _ := newAdminEnv(t)
	if rec := do(t, h, "", "POST", "/admin/api/im-credential/rotate", ""); rec.Code != 401 {
		t.Fatalf("未登录轮换 IM 凭据应当 401，got %d", rec.Code)
	}
}
