package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func newAppService(t *testing.T) *service.ApplicationService {
	t.Helper()
	return service.NewApplicationService(testsupport.NewTestDB(t))
}

func TestCreateApplicationReturnsPlainSecretOnce(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, secret, err := svc.Create(ctx, "新项目前台", "newproj-web")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if app.AppID == "" {
		t.Fatal("AppID 为空")
	}
	if len(secret) < 32 {
		t.Fatalf("secret 长度 = %d, 太短", len(secret))
	}
	if app.Session != domain.DefaultSessionPolicy() {
		t.Fatalf("新应用应使用默认会话策略, got %+v", app.Session)
	}
	if app.Status != domain.ApplicationStatusActive {
		t.Fatalf("Status = %q, want ACTIVE", app.Status)
	}

	got, err := svc.GetByID(ctx, app.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.AppID != app.AppID {
		t.Fatalf("AppID = %q, want %q", got.AppID, app.AppID)
	}
}

func TestCreateApplicationRejectsDuplicateSlug(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	if _, _, err := svc.Create(ctx, "A", "same-slug"); err != nil {
		t.Fatalf("首次 Create: %v", err)
	}
	if _, _, err := svc.Create(ctx, "B", "same-slug"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestCreateApplicationRejectsEmptyFields(t *testing.T) {
	svc := newAppService(t)
	if _, _, err := svc.Create(context.Background(), "", "slug"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestVerifySecret(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, secret, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.VerifySecret(ctx, app.AppID, secret)
	if err != nil {
		t.Fatalf("VerifySecret: %v", err)
	}
	if got.ID != app.ID {
		t.Fatalf("ID = %v, want %v", got.ID, app.ID)
	}

	if _, err := svc.VerifySecret(ctx, app.AppID, "wrong"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("错误 secret err = %v, want ErrInvalidCredential", err)
	}
	// 未知 appId 必须返回与密钥错误相同的错误，避免 appId 枚举。
	if _, err := svc.VerifySecret(ctx, "no-such-app", secret); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("未知 appID err = %v, want ErrInvalidCredential", err)
	}
}

func TestUpdateSessionPolicy(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	bad := domain.DefaultSessionPolicy()
	bad.ExtendIntervalSeconds = bad.IdleTimeoutSeconds // 违反 extend < idle
	if _, err := svc.UpdateSessionPolicy(ctx, app.ID, bad); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}

	good := domain.DefaultSessionPolicy()
	good.TokenCacheTTLSeconds = 60
	updated, err := svc.UpdateSessionPolicy(ctx, app.ID, good)
	if err != nil {
		t.Fatalf("UpdateSessionPolicy: %v", err)
	}
	if updated.Session.TokenCacheTTLSeconds != 60 {
		t.Fatalf("TokenCacheTTLSeconds = %d, want 60", updated.Session.TokenCacheTTLSeconds)
	}
}

func TestApplicationNotFound(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	if _, err := svc.GetByID(ctx, uuid.Nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByID err = %v, want ErrNotFound", err)
	}
	if _, err := svc.GetByAppID(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByAppID err = %v, want ErrNotFound", err)
	}
}

func TestListApplicationsReturnsEmptySlice(t *testing.T) {
	svc := newAppService(t)
	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list == nil {
		t.Fatal("List 返回 nil，应返回空切片以便 JSON 序列化为 []")
	}
	if len(list) != 0 {
		t.Fatalf("len = %d, want 0", len(list))
	}
}

func TestConnectorConfigRoundTrip(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	list, err := svc.ListConnectors(ctx, app.ID)
	if err != nil {
		t.Fatalf("ListConnectors: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("len = %d, want 0", len(list))
	}

	if _, err := svc.GetConnector(ctx, app.ID, "password"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("未配置时 err = %v, want ErrNotFound", err)
	}

	cfg := map[string]any{"minLength": float64(8)}
	if err := svc.SetConnector(ctx, app.ID, "password", true, cfg); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	// 重复设置应为 upsert 而非报错
	cfg["minLength"] = float64(10)
	if err := svc.SetConnector(ctx, app.ID, "password", true, cfg); err != nil {
		t.Fatalf("SetConnector upsert: %v", err)
	}

	got, err := svc.GetConnector(ctx, app.ID, "password")
	if err != nil {
		t.Fatalf("GetConnector: %v", err)
	}
	if !got.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if got.Config["minLength"] != float64(10) {
		t.Fatalf("minLength = %v, want 10", got.Config["minLength"])
	}

	list, err = svc.ListConnectors(ctx, app.ID)
	if err != nil {
		t.Fatalf("ListConnectors: %v", err)
	}
	if len(list) != 1 || list[0].Type != "password" {
		t.Fatalf("list = %+v", list)
	}
}

func TestSetConnectorRejectsEmptyType(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.SetConnector(ctx, app.ID, "", true, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}
