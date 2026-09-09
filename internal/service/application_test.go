package service_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

// stubSchemas 让 SetConnector 的校验测试不依赖真实 registry，
// 从而能覆盖到"必填字段"这条——线上两个 connector 恰好都没有必填字段。
type stubSchemas map[string][]domain.Field

func (s stubSchemas) Schemas() map[string][]domain.Field { return s }

func newAppService(t *testing.T) *service.ApplicationService {
	t.Helper()
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(nil)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	if err := reg.Register(connector.NewSMSCode(nil)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}
	return service.NewApplicationService(testsupport.NewTestDB(t), reg)
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

	cfg := map[string]any{"allowPhone": true}
	if err := svc.SetConnector(ctx, app.ID, "password", true, cfg); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	// 重复设置应为 upsert 而非报错
	cfg["allowPhone"] = false
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
	if got.Config["allowPhone"] != false {
		t.Fatalf("allowPhone = %v, want false", got.Config["allowPhone"])
	}

	list, err = svc.ListConnectors(ctx, app.ID)
	if err != nil {
		t.Fatalf("ListConnectors: %v", err)
	}
	if len(list) != 1 || list[0].Type != "password" {
		t.Fatalf("list = %+v", list)
	}
}

func TestSetConnectorRejectsUnregisteredType(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// wechat 没有注册。写成功的话，库里会多出一行永远不会被使用的配置，
	// 而管理员以为自己开通了微信登录。
	if err := svc.SetConnector(ctx, app.ID, "wechat", true, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	list, err := svc.ListConnectors(ctx, app.ID)
	if err != nil {
		t.Fatalf("ListConnectors: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("被拒绝的配置仍然落库了: %+v", list)
	}
}

func TestSetConnectorRejectsUnknownConfigKey(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// minLength 不在 password 的 ConfigSchema 里。前后端字段名漂移时，
	// 这条校验是唯一会喊出声的地方。
	err = svc.SetConnector(ctx, app.ID, "password", true, map[string]any{"minLength": float64(8)})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestSetConnectorRejectsWrongValueType(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// allowPhone 是 bool。前端如果把开关序列化成字符串 "true"，
	// connector.ConfigBool 读到的会是 false —— 开关看起来开着，实际关着。
	err = svc.SetConnector(ctx, app.ID, "password", true, map[string]any{"allowPhone": "true"})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestSetConnectorRejectsMissingRequiredField(t *testing.T) {
	// 线上两个 connector 都没有必填字段，只能用 stub 覆盖这条分支。
	svc := service.NewApplicationService(testsupport.NewTestDB(t), stubSchemas{
		"demo": {
			{Key: "apiKey", Label: "API Key", Type: domain.FieldTypeString, Required: true},
			{Key: "note", Label: "备注", Type: domain.FieldTypeString},
		},
	})
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 缺 apiKey
	if err := svc.SetConnector(ctx, app.ID, "demo", true, map[string]any{"note": "x"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("缺必填 err = %v, want ErrInvalidArgument", err)
	}
	// 必填给了空串同样不算数
	if err := svc.SetConnector(ctx, app.ID, "demo", true, map[string]any{"apiKey": ""}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("必填为空串 err = %v, want ErrInvalidArgument", err)
	}
	// 给全了就该成功；可选字段不给也没问题
	if err := svc.SetConnector(ctx, app.ID, "demo", true, map[string]any{"apiKey": "k"}); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
}

// 未注入元数据时必须**失败关闭**，不能退化成"跳过校验"。
// 如果 nil 意味着放行，那么任何一个忘了传 registry 的调用点都会静默地
// 失去全部校验，而所有测试照绿——这正是第二阶段反复栽跟头的那类缺陷。
func TestSetConnectorFailsClosedWithoutSchemas(t *testing.T) {
	svc := service.NewApplicationService(testsupport.NewTestDB(t), nil)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.SetConnector(ctx, app.ID, "password", true, nil); err == nil {
		t.Fatal("未注入 connector 元数据时 SetConnector 竟然成功了")
	}
}

func TestSetConnectorAcceptsIntFromJSONFloat(t *testing.T) {
	// JSONB 反序列化出来的数字一律是 float64（见 connector.ConfigInt 的注释）。
	// int 字段的校验必须接受整数值的 float64，否则任何走过一次 JSON 的
	// 配置都会被自己的校验拒掉。
	svc := service.NewApplicationService(testsupport.NewTestDB(t), stubSchemas{
		"demo": {{Key: "ttl", Label: "TTL", Type: domain.FieldTypeInt}},
	})
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.SetConnector(ctx, app.ID, "demo", true, map[string]any{"ttl": float64(30)}); err != nil {
		t.Fatalf("整数值的 float64 被拒: %v", err)
	}
	// 但小数不是整数
	if err := svc.SetConnector(ctx, app.ID, "demo", true, map[string]any{"ttl": float64(1.5)}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("小数 err = %v, want ErrInvalidArgument", err)
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

// newAppServiceWith 在指定 pool 上构造 ApplicationService，供需要共用同一个
// pool 的测试使用（例如授权测试要在同一个库里建用户与角色）。
func newAppServiceWith(t *testing.T, pool *pgxpool.Pool) *service.ApplicationService {
	t.Helper()
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(nil)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	if err := reg.Register(connector.NewSMSCode(nil)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}
	return service.NewApplicationService(pool, reg)
}

func TestSetIMConfigRoundTrip(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()
	app, _, err := svc.Create(ctx, "im 应用", "im-app")
	if err != nil {
		t.Fatal(err)
	}
	// 新建的应用 IM 必须是关的——这是"发布后对现网零影响"的依据。
	if app.IM.Enabled {
		t.Fatal("新建应用的 im_enabled 必须默认为 false")
	}

	want := domain.IMConfig{
		Enabled:     true,
		ConnPolicy:  domain.IMConnPolicyLimit,
		ConnLimit:   3,
		AllowGuest:  true,
		GuestIPRate: 30,
		BizAuth:     &domain.IMBizAuth{VerifyURL: "https://biz/v", TimeoutMs: 1500, CacheSize: 100},
	}
	got, err := svc.SetIMConfig(ctx, app.ID, want)
	if err != nil {
		t.Fatalf("SetIMConfig: %v", err)
	}
	if !reflect.DeepEqual(got.IM, want) {
		t.Fatalf("写回的配置 = %+v，want %+v", got.IM, want)
	}
	// 重新读一次：确认真的落库了，而不是只在返回值里对。
	reread, err := svc.GetByAppID(ctx, app.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reread.IM, want) {
		t.Fatalf("重新读出的配置 = %+v，want %+v", reread.IM, want)
	}
}

// TestSetIMConfigClearsBizAuth 钉住"整组置空"这条路径：biz_auth 是可空
// jsonb，从"配过"改成"没配"必须真的写 NULL，而不是留一份旧值或写成
// "null" 字面量——scanApplication 靠 NULL 判定"不支持业务方令牌"。
func TestSetIMConfigClearsBizAuth(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()
	app, _, err := svc.Create(ctx, "im 应用", "im-app2")
	if err != nil {
		t.Fatal(err)
	}
	cfg := domain.DefaultIMConfig()
	cfg.Enabled = true
	cfg.BizAuth = &domain.IMBizAuth{VerifyURL: "https://biz/v", TimeoutMs: 1500, CacheSize: 100}
	if _, err := svc.SetIMConfig(ctx, app.ID, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.BizAuth = nil
	got, err := svc.SetIMConfig(ctx, app.ID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.IM.BizAuth != nil {
		t.Fatalf("BizAuth = %+v，want nil", got.IM.BizAuth)
	}
}

func TestSetIMConfigRejectsInvalid(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()
	app, _, err := svc.Create(ctx, "im 应用", "im-app3")
	if err != nil {
		t.Fatal(err)
	}
	bad := domain.DefaultIMConfig()
	bad.Enabled = true
	bad.BizAuth = &domain.IMBizAuth{VerifyURL: "http://biz/v", TimeoutMs: 1500, CacheSize: 100}
	if _, err := svc.SetIMConfig(ctx, app.ID, bad); err == nil {
		t.Fatal("非法配置必须在写库之前被拒")
	}
	reread, err := svc.GetByAppID(ctx, app.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.IM.Enabled {
		t.Fatal("被拒的配置不能有任何一部分落库")
	}
}
