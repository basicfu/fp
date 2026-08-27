package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func newAdminService(t *testing.T) *service.AdminService {
	t.Helper()
	return service.NewAdminService(testsupport.NewTestDB(t), testsupport.NewTestRedis(t))
}

func TestEnsureBootstrapCreatesAdminOnce(t *testing.T) {
	svc := newAdminService(t)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("首次 EnsureBootstrap: %v", err)
	}
	// 重复调用不应报错，也不应改写密码。
	if err := svc.EnsureBootstrap(ctx, "admin", "another-password"); err != nil {
		t.Fatalf("重复 EnsureBootstrap: %v", err)
	}

	if _, err := svc.Login(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("原密码应仍然有效: %v", err)
	}
	if _, err := svc.Login(ctx, "admin", "another-password"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("新密码不应生效, err = %v", err)
	}
}

func TestEnsureBootstrapSkipsWhenEmpty(t *testing.T) {
	svc := newAdminService(t)
	if err := svc.EnsureBootstrap(context.Background(), "", ""); err != nil {
		t.Fatalf("未配置引导管理员时应静默跳过: %v", err)
	}
}

// bcrypt 的 72 字节上限对引导管理员密码同样适用；必须在启动路径上给出
// 清晰的 400，而不是把 bcrypt 的原始报错一路捅到进程退出。
func TestEnsureBootstrapRejectsTooLongPassword(t *testing.T) {
	svc := newAdminService(t)
	long := strings.Repeat("a", 73)
	if err := svc.EnsureBootstrap(context.Background(), "admin", long); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("73 字节 err = %v, want ErrInvalidArgument", err)
	}
}

func TestLoginAndAuthenticate(t *testing.T) {
	svc := newAdminService(t)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	token, err := svc.Login(ctx, "admin", "secret123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if len(token) < 32 {
		t.Fatalf("token 长度 = %d, 太短", len(token))
	}

	id, username, err := svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if username != "admin" {
		t.Fatalf("username = %q, want admin", username)
	}
	// 必须比 uuid.Nil，不能比 id.String() == ""：零值 uuid.UUID 会被
	// 格式化成一串全零（00000000-0000-...），永远不是空串，那个断言恒为真。
	if id == uuid.Nil {
		t.Fatal("adminID 为空")
	}
}

func TestAuthenticateRejectsUnknownToken(t *testing.T) {
	svc := newAdminService(t)
	_, _, err := svc.Authenticate(context.Background(), "not-a-real-token")
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestLogoutInvalidatesToken(t *testing.T) {
	svc := newAdminService(t)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	token, err := svc.Login(ctx, "admin", "secret123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, _, err := svc.Authenticate(ctx, token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("登出后 err = %v, want ErrUnauthorized", err)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	svc := newAdminService(t)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	if _, err := svc.Login(ctx, "admin", "wrong"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
	if _, err := svc.Login(ctx, "nobody", "secret123456"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("未知用户名 err = %v, want ErrInvalidCredential", err)
	}
}
