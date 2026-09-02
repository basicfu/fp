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

func TestSetStatusDisabledBlocksGetActive(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 停用前拿得到
	if _, err := svc.GetActiveByAppID(ctx, app.AppID); err != nil {
		t.Fatalf("停用前 GetActiveByAppID: %v", err)
	}

	got, err := svc.SetStatus(ctx, app.ID, domain.ApplicationStatusDisabled)
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got.Status != domain.ApplicationStatusDisabled {
		t.Fatalf("Status = %q, want DISABLED", got.Status)
	}

	// 停用后 GetActiveByAppID 必须拒绝，但 GetByAppID 仍然找得到——
	// 这两者的区别正是"停用"而非"删除"的含义。
	if _, err := svc.GetActiveByAppID(ctx, app.AppID); err == nil {
		t.Fatal("停用后 GetActiveByAppID 仍然成功，停用形同虚设")
	}
	if _, err := svc.GetByAppID(ctx, app.AppID); err != nil {
		t.Fatalf("停用后 GetByAppID 应仍可查到: %v", err)
	}

	// 能重新启用
	if _, err := svc.SetStatus(ctx, app.ID, domain.ApplicationStatusActive); err != nil {
		t.Fatalf("重新启用: %v", err)
	}
	if _, err := svc.GetActiveByAppID(ctx, app.AppID); err != nil {
		t.Fatalf("重新启用后 GetActiveByAppID: %v", err)
	}
}

func TestSetStatusRejectsUnknownStatus(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, bad := range []string{"", "disabled", "ENABLED", "DELETED"} {
		if _, err := svc.SetStatus(ctx, app.ID, bad); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("status=%q err = %v, want ErrInvalidArgument", bad, err)
		}
	}
}

func TestSetStatusOnMissingApplication(t *testing.T) {
	svc := newAppService(t)
	if _, err := svc.SetStatus(context.Background(), uuid.New(), domain.ApplicationStatusDisabled); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// strPtr 是取字符串字面量地址的小工具：Go 不允许 &"字面量"，
// Update 的局部更新语义又必须靠 *string 区分"没传"与"传了空串"，
// 测试里到处需要它。
func strPtr(s string) *string { return &s }

func TestUpdateOnlyTouchesNameAndCookieDomain(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, secret, err := svc.Create(ctx, "旧名", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 先把会话策略改成一组与默认值明显不同的值。必须先改：沿用默认值的话，
	// "策略被清零"与"策略没动"在断言上有可能因为默认值本身含零值而无法区分。
	want := domain.SessionPolicy{
		IdleTimeoutSeconds:       3601,
		IdleTimeoutMobileSeconds: 7202,
		MaxLifetimeSeconds:       86403,
		RotateIntervalSeconds:    604,
		ExtendIntervalSeconds:    305,
		TokenCacheTTLSeconds:     56,
	}
	if _, err := svc.UpdateSessionPolicy(ctx, app.ID, want); err != nil {
		t.Fatalf("UpdateSessionPolicy: %v", err)
	}

	got, err := svc.Update(ctx, app.ID, strPtr("新名"), strPtr("example.com"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != "新名" {
		t.Fatalf("Name = %q, want 新名", got.Name)
	}
	if got.CookieDomain != "example.com" {
		t.Fatalf("CookieDomain = %q, want example.com", got.CookieDomain)
	}
	// slug 与 appId 是身份，改名不许动
	if got.Slug != app.Slug || got.AppID != app.AppID {
		t.Fatalf("slug/appId 被改动: %+v", got)
	}
	// 会话策略必须原封不动
	if got.Session != want {
		t.Fatalf("会话策略被改名连带改动了: got %+v, want %+v", got.Session, want)
	}
	// appSecret 必须仍然有效
	if _, err := svc.VerifySecret(ctx, app.AppID, secret); err != nil {
		t.Fatalf("改名后 appSecret 失效: %v", err)
	}
	// 状态必须仍是启用
	if got.Status != domain.ApplicationStatusActive {
		t.Fatalf("Status = %q, want ACTIVE", got.Status)
	}
}

// TestUpdatePartialNameOnlyKeepsCookieDomain 只传 name 时，cookieDomain
// 必须原封不动。cookie_domain 建表默认就是空串，所以必须先把它改成一个
// 非空值再验证——不然"没被改"和"本来就是空"在断言上分不出来。
func TestUpdatePartialNameOnlyKeepsCookieDomain(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "旧名", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(ctx, app.ID, nil, strPtr("original.example.com")); err != nil {
		t.Fatalf("预置 cookieDomain: %v", err)
	}

	got, err := svc.Update(ctx, app.ID, strPtr("新名"), nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != "新名" {
		t.Fatalf("Name = %q, want 新名", got.Name)
	}
	if got.CookieDomain != "original.example.com" {
		t.Fatalf("CookieDomain = %q, 只传 name 时不该被改动", got.CookieDomain)
	}
}

// TestUpdatePartialCookieDomainOnlyKeepsName 只传 cookieDomain 时，name
// 必须原封不动。
func TestUpdatePartialCookieDomainOnlyKeepsName(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "原名", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.Update(ctx, app.ID, nil, strPtr("example.com"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.CookieDomain != "example.com" {
		t.Fatalf("CookieDomain = %q, want example.com", got.CookieDomain)
	}
	if got.Name != "原名" {
		t.Fatalf("Name = %q, 只传 cookieDomain 时不该被改动", got.Name)
	}
}

// TestUpdateExplicitEmptyCookieDomainClearsIt 证明 nil 与显式空串被区分
// 对待：先把 cookieDomain 设成非空值，再显式传 &""，必须真的被清空——
// 与 TestUpdatePartialNameOnlyKeepsCookieDomain（传 nil，保持不变）对照，
// 才能证明这不是巧合。
func TestUpdateExplicitEmptyCookieDomainClearsIt(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(ctx, app.ID, nil, strPtr("example.com")); err != nil {
		t.Fatalf("预置 cookieDomain: %v", err)
	}

	got, err := svc.Update(ctx, app.ID, nil, strPtr(""))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.CookieDomain != "" {
		t.Fatalf("CookieDomain = %q, 显式传空字符串应该清空为空串", got.CookieDomain)
	}
}

func TestUpdateRejectsEmptyName(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(ctx, app.ID, strPtr(""), strPtr("example.com")); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

// TestUpdateRejectsWhenNoFieldsGiven 两个字段都不传（nil）必须报错，而不是
// 悄悄 no-op——decodeJSON 已经用 DisallowUnknownFields 挡掉了拼错字段名的
// 情况，所以两个都是 nil 就意味着请求体真的什么有效字段都没给。
func TestUpdateRejectsWhenNoFieldsGiven(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(ctx, app.ID, nil, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}
