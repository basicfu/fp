# fp-im app 配置下发 · 第一阶段（fp 侧）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 fp 具备"把 app 的 IM 准入与策略下发给 fp-im"的全部能力，并且**发布后对现网零影响**——新列默认关、新 RPC 无人调用、不带 `fp-caller-type` 的调用行为一字不变。

**Architecture:** `application` 加一组 IM 列（`im_enabled` 默认 false）与一张单行的 `im_credential` 表。gRPC 拦截器认一个新的 metadata 键 `fp-caller-type`：空走今天的 app 凭据校验，`im` 走 IM 凭据校验，**只比对一份，不两边都试**。`type=im` 能调 `ValidateToken`/`Watch` 与两个新的 `IMGatewayService` RPC，不能调 `Login` 之类。撤销扇出对 `type=im` 订阅者不按 app 过滤。

**Tech Stack:** Go 1.25 / PostgreSQL 18 / Redis / pgx v5 / gRPC + buf / React + Vite + shadcn

## Global Constraints

- **设计文档**：`docs/superpowers/specs/2026-09-09-fp-im-app-provisioning-design.md`。每个任务开始前读一遍对应章节。
- **跑测试只用 `./scripts/test.sh`**，它从 `.env.local` 载入 `FP_TEST_POSTGRES_URL` / `FP_TEST_REDIS_URL` 并加 `-p 1`。直接 `go test` 会因为缺环境变量而 `t.Fatal`。
- **生成 protobuf 只用 `./scripts/gen.sh`**（buf lint + buf generate + gofmt 检查）。产物提交进仓库。
- **`sdk/` 不得 import `internal/`，`sdk/` 里不得出现 `panic`**。由 `sdk/arch_test.go` 守护。
- **不引入任何新依赖。** 新增直接依赖必须先登记进 `internal/integration/dependency_whitelist_test.go`。
- **注释和错误信息一律中文**，与仓库既有风格一致。
- **零现网影响是硬要求**：不带 `fp-caller-type` 的调用，从 metadata 解析到错误码，行为必须与改动前逐字相同。每个碰拦截器的任务都要有一条测试钉住这一点。
- **`decodeJSON` 开了 `DisallowUnknownFields`**：httpapi 的请求 DTO 字段名拼错是 400 而不是静默丢弃。
- **每条标「辨别力」的测试，实现后必须做一次变异验证**：把实现改坏跑一遍确认真的变红，再改回来。
- **提交粒度**：每个 Task 的每个 TDD 循环结束就 commit，不攒。
- **绝不 `git add -A`。** 这个分支上带着一批与本计划无关的、未提交的配置中心改动（`internal/domain/config.go`、`web/**` 等 46 个文件）。每次提交都显式列出文件路径。
- **`web/` 下的文件与那批未提交改动高度重叠**（`ApplicationSettings.tsx` 就在其中）。Task 12 开始前先跟人确认那批改动的状态，别把它们卷进提交。

---

## 文件结构

**新建：**

| 文件 | 职责 |
|---|---|
| `internal/store/migrations/00010_im.sql` | `im_credential` 表 + `application` 的 6 个 IM 列 |
| `internal/service/imcred.go` | `IMCredentialService`：生成、读哈希、轮换 |
| `internal/service/imcred_test.go` | 生成/轮换/校验的服务层测试 |
| `internal/grpcapi/im_service.go` | `IMGatewayService` 的 gRPC 实现 |
| `internal/grpcapi/im_service_test.go` | 两个 RPC 的行为与权限边界 |
| `internal/httpapi/imcred.go` | 控制台的 IM 凭据路由 |
| `internal/httpapi/imcred_test.go` | 路由层测试 |
| `proto/fp/v1/im.proto` | `IMGatewayService` 与 `AppIMConfigChanged` |
| `sdk/imgateway.go` | SDK 侧两个新 RPC 的包装 |
| `sdk/imgateway_test.go` | 包装层测试 |
| `web/src/components/ApplicationIMSettings.tsx` | 应用管理页的 IM 面板 |

**改：**

| 文件 | 改什么 |
|---|---|
| `internal/domain/application.go` | `Application` 加 `IM IMConfig`；新增 `IMConfig` / `IMBizAuth` 与 `Validate` |
| `internal/service/application.go` | `applicationColumns` / `scanApplication` 加 6 列；新增 `SetIMConfig` |
| `internal/grpcapi/auth_interceptor.go` | 认 `fp-caller-type`；IM 凭据校验与它自己的短 TTL 缓存 |
| `internal/grpcapi/auth_service.go` | `type=im` 拒 `Login`/`SendLoginCode`/`Logout`/`ReportPermissions`/`GetPolicy`；`Watch` 对 im 用通配订阅 |
| `internal/grpcapi/config_service.go` | `type=im` 拒 `GetConfig` |
| `internal/grpcapi/watch.go` | `RevokeHub` 支持通配订阅者；`AppIMConfigChanged` 中继 |
| `internal/grpcapi/server.go` | `Deps` 加 `IMCreds`；注册 `IMGatewayService` |
| `internal/httpapi/application.go` | 应用 IM 配置的读写 DTO 与路由 |
| `internal/httpapi/router.go` | 挂上两组新路由 |
| `sdk/options.go` | `Options.CallerType`；`appCredentials` 按它输出 metadata |
| `sdk/client.go` | 注册 `IMGatewayServiceClient` |
| `web/src/pages/Applications.tsx` | 嵌入 IM 面板 |
| `web/src/lib/types.ts` | IM 配置的前端类型 |

---

## Task 1: 迁移与 domain 类型

**Files:**
- Create: `internal/store/migrations/00010_im.sql`
- Modify: `internal/domain/application.go`
- Test: `internal/domain/application_test.go`

**Interfaces:**
- Produces：
  - `domain.IMConfig{Enabled bool; ConnPolicy string; ConnLimit int32; AllowGuest bool; GuestIPRate int32; BizAuth *IMBizAuth}`
  - `domain.IMBizAuth{VerifyURL string; TimeoutMs int32; CacheSize int32}`
  - `domain.IMConfig.Validate() error`
  - `domain.Application.IM IMConfig`
  - 常量 `domain.IMConnPolicyReplace/Reject/Limit = "replace"/"reject"/"limit"`
  - `domain.DefaultIMConfig() IMConfig`

- [ ] **Step 1: 写迁移**

`internal/store/migrations/00010_im.sql`：

```sql
-- +goose Up
-- ---------------------------------------------------------------------------
-- IM 凭据。单行表：CHECK (id = 1) 把"只能有一条"写进 schema，不靠代码自觉。
--
-- 为什么不复用 application 表加一个 kind 列：那会造出一条"不是应用的应用"
-- ——会话策略、登录方式、配置分区、默认角色对它全是死字段，控制台还要
-- 处处判断"这条要不要展示"。IM 凭据与业务应用是两种东西。
--
-- 只存 bcrypt 哈希，与 application.app_secret_hash 同一纪律：明文只在生成
-- 时返回一次，之后无法读回。
-- ---------------------------------------------------------------------------
CREATE TABLE im_credential (
    id          smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    secret_hash text        NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- 应用的 IM 接入配置。
--
-- im_enabled 默认 false：一个应用在有人明确打开之前连不上 fp-im——业务
-- server 接不进来，client 握手也拒。这条默认值就是"发布后对现网零影响"
-- 的全部依据。
--
-- im_biz_auth 用可空 jsonb 而不是铺平成三列：biz_auth 是"整组有或整组
-- 没有"的东西，铺平之后就得靠"verify_url 是不是空串"这种间接判断，而
-- internal/im/model 的 BizAuth 当初选嵌套正是为了避开这一点。SQL 的 NULL
-- 恰好是同一个语义。
-- ---------------------------------------------------------------------------
ALTER TABLE application
    ADD COLUMN im_enabled       boolean NOT NULL DEFAULT false,
    ADD COLUMN im_conn_policy   text    NOT NULL DEFAULT 'replace'
        CHECK (im_conn_policy IN ('replace', 'reject', 'limit')),
    ADD COLUMN im_conn_limit    integer NOT NULL DEFAULT 5,
    ADD COLUMN im_allow_guest   boolean NOT NULL DEFAULT false,
    ADD COLUMN im_guest_ip_rate integer NOT NULL DEFAULT 20,
    ADD COLUMN im_biz_auth      jsonb;

-- +goose Down
ALTER TABLE application
    DROP COLUMN im_enabled,
    DROP COLUMN im_conn_policy,
    DROP COLUMN im_conn_limit,
    DROP COLUMN im_allow_guest,
    DROP COLUMN im_guest_ip_rate,
    DROP COLUMN im_biz_auth;
DROP TABLE im_credential;
```

- [ ] **Step 2: 写失败的测试**

追加到 `internal/domain/application_test.go`：

```go
func TestIMConfigValidate(t *testing.T) {
	base := DefaultIMConfig()
	base.Enabled = true
	if err := base.Validate(); err != nil {
		t.Fatalf("默认配置被拒：%v", err)
	}

	// 关着的时候不校验其余项：还没配就先拦人，等于逼人一次填全才能存草稿。
	off := IMConfig{Enabled: false, ConnPolicy: "whatever", ConnLimit: -1}
	if err := off.Validate(); err != nil {
		t.Fatalf("im_enabled=false 时不该校验其余项：%v", err)
	}

	for _, tc := range []struct {
		name string
		mut  func(*IMConfig)
	}{
		{"未知策略", func(c *IMConfig) { c.ConnPolicy = "whatever" }},
		{"limit 策略下上限为零", func(c *IMConfig) { c.ConnPolicy = IMConnPolicyLimit; c.ConnLimit = 0 }},
		{"允许访客但限流为零", func(c *IMConfig) { c.AllowGuest = true; c.GuestIPRate = 0 }},
		{"biz_auth 缺地址", func(c *IMConfig) { c.BizAuth = &IMBizAuth{TimeoutMs: 2000, CacheSize: 10} }},
		{"biz_auth 明文 http", func(c *IMConfig) {
			c.BizAuth = &IMBizAuth{VerifyURL: "http://x/v", TimeoutMs: 2000, CacheSize: 10}
		}},
		{"biz_auth 超时为零", func(c *IMConfig) {
			c.BizAuth = &IMBizAuth{VerifyURL: "https://x/v", CacheSize: 10}
		}},
		{"biz_auth 缓存容量为零", func(c *IMConfig) {
			c.BizAuth = &IMBizAuth{VerifyURL: "https://x/v", TimeoutMs: 2000}
		}},
	} {
		c := base
		tc.mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s 必须被拒绝", tc.name)
		}
	}

	ok := base
	ok.BizAuth = &IMBizAuth{VerifyURL: "https://x/v", TimeoutMs: 2000, CacheSize: 10}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法的 biz_auth 被拒：%v", err)
	}
}

// TestDefaultIMConfigIsDisabled 钉住"发布后对现网零影响"的那条依据。
func TestDefaultIMConfigIsDisabled(t *testing.T) {
	if DefaultIMConfig().Enabled {
		t.Fatal("IM 默认必须是关的——这是新版 fp 发布后不影响现网的全部依据")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/domain
```

预期：编译失败，`undefined: DefaultIMConfig` / `IMConfig`。

- [ ] **Step 4: 实现**

追加到 `internal/domain/application.go`：

```go
// IM 连接策略。取值必须与 internal/im/model.Policy 逐字相同——那两个常量
// 分处 internal/domain 与 internal/im/model，任何一边单独看都只是孤立的
// 字符串字面量，配对关系由 internal/integration 的测试守护。
const (
	IMConnPolicyReplace = "replace" // 新连接顶掉同 subject 的旧连接
	IMConnPolicyReject  = "reject"  // 已有连接时拒绝新连接
	IMConnPolicyLimit   = "limit"   // 最多 ConnLimit 条，超出拒新
)

// IMBizAuth 是业务方自有认证的回调配置。整组为 nil 表示这个应用不支持
// 业务方令牌。
//
// 用嵌套（指针）而不是铺平成三个字段：铺平之后"支不支持"就得靠"地址是不是
// 空串"这种间接判断。落库时对应可空的 jsonb 列，SQL 的 NULL 是同一个语义。
type IMBizAuth struct {
	VerifyURL string `json:"verify_url"`
	// TimeoutMs 是整个回调请求的超时。用毫秒而不是 time.Duration：它要原样
	// 落进 jsonb，time.Duration 的 JSON 表示是纳秒整数，既难读又容易错一个
	// 数量级。
	TimeoutMs int32 `json:"timeout_ms"`
	CacheSize int32 `json:"cache_size"`
}

// IMConfig 是一个应用在 fp-im 里的接入配置。
type IMConfig struct {
	// Enabled 关着时这个应用连不上 fp-im：业务 server 接不进来，client
	// 握手也拒。默认 false。
	Enabled     bool       `json:"enabled"`
	ConnPolicy  string     `json:"conn_policy"`
	ConnLimit   int32      `json:"conn_limit"`
	AllowGuest  bool       `json:"allow_guest"`
	GuestIPRate int32      `json:"guest_ip_rate"`
	BizAuth     *IMBizAuth `json:"biz_auth,omitempty"`
}

// DefaultIMConfig 返回新建应用的默认 IM 配置。
func DefaultIMConfig() IMConfig {
	return IMConfig{
		Enabled:     false,
		ConnPolicy:  IMConnPolicyReplace,
		ConnLimit:   5,
		AllowGuest:  false,
		GuestIPRate: 20,
	}
}

// Validate 校验 IM 配置。
//
// Enabled 为 false 时直接放行：还没打开就先拦人，等于逼人一次填全才能存
// 草稿。真正会被 fp-im 读到的只有打开之后的配置。
func (c IMConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	switch c.ConnPolicy {
	case IMConnPolicyReplace, IMConnPolicyReject:
	case IMConnPolicyLimit:
		if c.ConnLimit < 1 {
			return Failf(ErrInvalidArgument, CodeInvalidArgument, "策略为 limit 时 conn_limit 必须 >= 1")
		}
	default:
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "未知的 conn_policy %q", c.ConnPolicy)
	}
	if c.AllowGuest && c.GuestIPRate < 1 {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "允许访客时 guest_ip_rate 必须 >= 1")
	}
	if c.BizAuth != nil {
		if c.BizAuth.VerifyURL == "" {
			return Failf(ErrInvalidArgument, CodeInvalidArgument, "配了 biz_auth 但缺 verify_url")
		}
		// 必须 HTTPS：client 的令牌明文走在请求体里，明文传输等于把所有
		// 业务方令牌交给中间人。
		if !strings.HasPrefix(c.BizAuth.VerifyURL, "https://") {
			return Failf(ErrInvalidArgument, CodeInvalidArgument, "biz_auth.verify_url 必须是 https")
		}
		if c.BizAuth.TimeoutMs <= 0 {
			return Failf(ErrInvalidArgument, CodeInvalidArgument, "biz_auth.timeout_ms 必须大于 0")
		}
		if c.BizAuth.CacheSize <= 0 {
			return Failf(ErrInvalidArgument, CodeInvalidArgument, "biz_auth.cache_size 必须大于 0")
		}
	}
	return nil
}
```

`Application` 结构体加一个字段（放在 `DefaultRoleKey` 之后）：

```go
	// IM 是该应用在 fp-im 里的接入配置。默认关。
	IM IMConfig
```

`import` 块补 `"strings"`。

- [ ] **Step 5: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/domain -run 'TestIMConfig|TestDefaultIMConfig' -v
```

预期：两条 PASS。

- [ ] **Step 6: 变异验证**

把 `Validate` 里 `if !c.Enabled { return nil }` 临时删掉，确认 `TestIMConfigValidate` 的
"im_enabled=false 时不该校验其余项"变红。改回来。

- [ ] **Step 7: 提交**

```bash
git add internal/store/migrations/00010_im.sql internal/domain/application.go internal/domain/application_test.go
git commit -m "feat(domain): application 加 IM 接入配置，新增 im_credential 表"
```

---

## Task 2: ApplicationService 读写 IM 配置

**Files:**
- Modify: `internal/service/application.go`
- Test: `internal/service/application_test.go`

**Interfaces:**
- Consumes: Task 1 的 `domain.IMConfig` / `domain.DefaultIMConfig()`
- Produces: `(*ApplicationService).SetIMConfig(ctx, id uuid.UUID, cfg domain.IMConfig) (*domain.Application, error)`；`GetByAppID` / `GetActiveByAppID` / `List` 返回的 `Application` 上 `IM` 字段已填

- [ ] **Step 1: 写失败的测试**

追加到 `internal/service/application_test.go`：

```go
func TestSetIMConfigRoundTrip(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	svc := NewApplicationService(pool, connector.NewRegistry())
	app, _, err := svc.Create(context.Background(), "im 应用", "im-app")
	if err != nil {
		t.Fatal(err)
	}

	// 新建的应用 IM 必须是关的。
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
	got, err := svc.SetIMConfig(context.Background(), app.ID, want)
	if err != nil {
		t.Fatalf("SetIMConfig: %v", err)
	}
	if !reflect.DeepEqual(got.IM, want) {
		t.Fatalf("写回的配置 = %+v，want %+v", got.IM, want)
	}

	// 重新读一次：确认真的落库了，而不是只在返回值里对。
	reread, err := svc.GetByAppID(context.Background(), app.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reread.IM, want) {
		t.Fatalf("重新读出的配置 = %+v，want %+v", reread.IM, want)
	}
}

// TestSetIMConfigClearsBizAuth 钉住"整组置空"这条路径：biz_auth 是可空
// jsonb，从"配过"改成"没配"必须真的写 NULL，而不是留一份旧值。
func TestSetIMConfigClearsBizAuth(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	svc := NewApplicationService(pool, connector.NewRegistry())
	app, _, err := svc.Create(context.Background(), "im 应用", "im-app2")
	if err != nil {
		t.Fatal(err)
	}
	cfg := domain.DefaultIMConfig()
	cfg.Enabled = true
	cfg.BizAuth = &domain.IMBizAuth{VerifyURL: "https://biz/v", TimeoutMs: 1500, CacheSize: 100}
	if _, err := svc.SetIMConfig(context.Background(), app.ID, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.BizAuth = nil
	got, err := svc.SetIMConfig(context.Background(), app.ID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.IM.BizAuth != nil {
		t.Fatalf("BizAuth = %+v，want nil", got.IM.BizAuth)
	}
}

func TestSetIMConfigRejectsInvalid(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	svc := NewApplicationService(pool, connector.NewRegistry())
	app, _, err := svc.Create(context.Background(), "im 应用", "im-app3")
	if err != nil {
		t.Fatal(err)
	}
	bad := domain.DefaultIMConfig()
	bad.Enabled = true
	bad.BizAuth = &domain.IMBizAuth{VerifyURL: "http://biz/v", TimeoutMs: 1500, CacheSize: 100}
	if _, err := svc.SetIMConfig(context.Background(), app.ID, bad); err == nil {
		t.Fatal("非法配置必须在写库之前被拒")
	}
	reread, err := svc.GetByAppID(context.Background(), app.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.IM.Enabled {
		t.Fatal("被拒的配置不能有任何一部分落库")
	}
}
```

`import` 块按需补 `"reflect"`。

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/service -run TestSetIMConfig
```

预期：`undefined: svc.SetIMConfig`。

- [ ] **Step 3: 实现**

`applicationColumns` 末尾（`updated_at` 那两行**之前**）插入 6 列，保持与
`scanApplication` 的顺序一致：

```go
const applicationColumns = `
	id, name, slug, app_id, status,
	idle_timeout_seconds, idle_timeout_mobile_seconds, max_lifetime_seconds,
	rotate_interval_seconds, extend_interval_seconds, token_cache_ttl_seconds,
	cookie_domain, redirect_uris, grant_types, default_role_key,
	im_enabled, im_conn_policy, im_conn_limit, im_allow_guest, im_guest_ip_rate, im_biz_auth,
	(extract(epoch from created_at) * 1000)::bigint,
	(extract(epoch from updated_at) * 1000)::bigint`
```

`scanApplication` 相应加上——`im_biz_auth` 是可空 jsonb，扫进 `[]byte` 再解：

```go
func scanApplication(r rowScanner) (*domain.Application, error) {
	var app domain.Application
	var bizAuth []byte
	err := r.Scan(
		&app.ID, &app.Name, &app.Slug, &app.AppID, &app.Status,
		&app.Session.IdleTimeoutSeconds, &app.Session.IdleTimeoutMobileSeconds, &app.Session.MaxLifetimeSeconds,
		&app.Session.RotateIntervalSeconds, &app.Session.ExtendIntervalSeconds, &app.Session.TokenCacheTTLSeconds,
		&app.CookieDomain, &app.RedirectURIs, &app.GrantTypes, &app.DefaultRoleKey,
		&app.IM.Enabled, &app.IM.ConnPolicy, &app.IM.ConnLimit, &app.IM.AllowGuest, &app.IM.GuestIPRate, &bizAuth,
		&app.CreatedAt, &app.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	// NULL 表示这个应用不支持业务方令牌，保持 BizAuth 为 nil。
	if len(bizAuth) > 0 {
		var b domain.IMBizAuth
		if err := json.Unmarshal(bizAuth, &b); err != nil {
			return nil, fmt.Errorf("service: 解析 im_biz_auth: %w", err)
		}
		app.IM.BizAuth = &b
	}
	return &app, nil
}
```

新增 `SetIMConfig`（放在 `SetDefaultRole` 附近）：

```go
// SetIMConfig 更新应用的 IM 接入配置。配置非法时不写库。
func (s *ApplicationService) SetIMConfig(ctx context.Context, id uuid.UUID, cfg domain.IMConfig) (*domain.Application, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// BizAuth 为 nil 时写 SQL NULL，而不是 "null" 字面量或空对象：
	// scanApplication 靠 NULL 判定"不支持业务方令牌"。
	var bizAuth any
	if cfg.BizAuth != nil {
		raw, err := json.Marshal(cfg.BizAuth)
		if err != nil {
			return nil, fmt.Errorf("service: 序列化 im_biz_auth: %w", err)
		}
		bizAuth = raw
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE application SET
			im_enabled       = $2,
			im_conn_policy   = $3,
			im_conn_limit    = $4,
			im_allow_guest   = $5,
			im_guest_ip_rate = $6,
			im_biz_auth      = $7,
			updated_at       = now()
		WHERE id = $1
		RETURNING `+applicationColumns,
		id, cfg.Enabled, cfg.ConnPolicy, cfg.ConnLimit, cfg.AllowGuest, cfg.GuestIPRate, bizAuth)

	app, err := scanApplication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Failf(domain.ErrNotFound, domain.CodeAppNotFound, "应用不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 更新 IM 配置: %w", err)
	}
	return app, nil
}
```

`import` 块按需补 `"encoding/json"`。

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/service -run TestSetIMConfig -v
```

预期：三条全 PASS。

- [ ] **Step 5: 跑整包，确认没碰坏既有查询**

```bash
./scripts/test.sh ./internal/service ./internal/httpapi ./internal/grpcapi
```

预期：全绿。`applicationColumns` 被所有读应用的查询共用，列顺序错位会在这里暴露。

- [ ] **Step 6: 变异验证**

把 `SetIMConfig` 里 `bizAuth` 的 nil 分支改成永远 `json.Marshal`（nil 会变成
`"null"` 字节），确认 `TestSetIMConfigClearsBizAuth` 变红。改回来。

- [ ] **Step 7: 提交**

```bash
git add internal/service/application.go internal/service/application_test.go
git commit -m "feat(service): 应用的 IM 配置读写"
```

---

## Task 3: IMCredentialService

**Files:**
- Create: `internal/service/imcred.go`, `internal/service/imcred_test.go`

**Interfaces:**
- Produces:
  - `service.NewIMCredentialService(pool *pgxpool.Pool) *IMCredentialService`
  - `(*IMCredentialService).Rotate(ctx) (plainSecret string, err error)` —— 生成新 secret，落哈希，返回仅此一次可见的明文
  - `(*IMCredentialService).Verify(ctx, plainSecret string) error` —— 成功返回 nil，否则 `domain.ErrInvalidCredential`
  - `(*IMCredentialService).Exists(ctx) (bool, error)` —— 控制台用来展示"还没生成过"

- [ ] **Step 1: 写失败的测试**

`internal/service/imcred_test.go`：

```go
package service

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestIMCredentialRotateAndVerify(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	svc := NewIMCredentialService(pool)
	ctx := context.Background()

	if ok, err := svc.Exists(ctx); err != nil || ok {
		t.Fatalf("初始状态应当是"还没生成过"，got ok=%v err=%v", ok, err)
	}

	secret, err := svc.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) < 32 {
		t.Fatalf("生成的 secret 太短：%d 字符", len(secret))
	}
	if ok, err := svc.Exists(ctx); err != nil || !ok {
		t.Fatalf("生成之后 Exists 应为 true，got ok=%v err=%v", ok, err)
	}
	if err := svc.Verify(ctx, secret); err != nil {
		t.Fatalf("刚生成的 secret 校验失败：%v", err)
	}
	if err := svc.Verify(ctx, secret+"x"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("错误的 secret 必须返回 ErrInvalidCredential，got %v", err)
	}
}

// TestIMCredentialRotateInvalidatesOld 钉住轮换的核心语义：旧 secret 必须
// 立刻在库这一层失效。gRPC 拦截器那层的缓存窗口是另一回事（10 秒），
// 见 auth_interceptor.go。
func TestIMCredentialRotateInvalidatesOld(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	svc := NewIMCredentialService(pool)
	ctx := context.Background()

	old, err := svc.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if old == fresh {
		t.Fatal("两次轮换生成了相同的 secret")
	}
	if err := svc.Verify(ctx, fresh); err != nil {
		t.Fatalf("新 secret 应当有效：%v", err)
	}
	if err := svc.Verify(ctx, old); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("旧 secret 必须失效，got %v", err)
	}
}

// TestIMCredentialVerifyBeforeRotate 钉住"从未生成过"时的行为：必须是
// 凭据无效，不能是内部错误——fp-im 配了个 secret 而 fp 这边还没生成，
// 是运维顺序问题，不是 bug。
func TestIMCredentialVerifyBeforeRotate(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	svc := NewIMCredentialService(pool)
	if err := svc.Verify(context.Background(), "whatever"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("还没生成过 IM 凭据时必须返回 ErrInvalidCredential，got %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/service -run TestIMCredential
```

预期：`undefined: NewIMCredentialService`。

- [ ] **Step 3: 实现**

`internal/service/imcred.go`：

```go
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/basicfu/fp/internal/domain"
)

// IMCredentialService 管理 fp-im 用来连 fp 的那份凭据。
//
// 全库只有一条（im_credential 的 CHECK (id = 1) 保证），因为 fp-im 是一个
// 服务而不是一群应用：多实例 fp-im 共用同一份凭据。
//
// 与 application.app_secret_hash 同一纪律：只存 bcrypt 哈希，明文只在
// Rotate 时返回一次，之后无法读回。
type IMCredentialService struct {
	pool *pgxpool.Pool
}

func NewIMCredentialService(pool *pgxpool.Pool) *IMCredentialService {
	return &IMCredentialService{pool: pool}
}

// Rotate 生成一份新的 IM secret，落哈希，返回仅此一次可见的明文。
//
// 用 upsert 而不是先查后写：单行表上的并发轮换靠主键冲突收敛，
// 不需要额外的事务或锁。
func (s *IMCredentialService) Rotate(ctx context.Context) (string, error) {
	secret, err := generateSecret()
	if err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("service: 计算 IM secret 哈希: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO im_credential (id, secret_hash, updated_at) VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET secret_hash = EXCLUDED.secret_hash, updated_at = now()`,
		string(hash)); err != nil {
		return "", fmt.Errorf("service: 写入 IM 凭据: %w", err)
	}
	return secret, nil
}

// Verify 校验 IM secret。
//
// "还没生成过"与"secret 不对"返回同一个错误：前者是运维顺序问题（fp-im
// 先配好了而 fp 这边还没生成），不是内部故障，让调用方按凭据无效处理即可。
func (s *IMCredentialService) Verify(ctx context.Context, plainSecret string) error {
	if plainSecret == "" {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "IM 凭据无效")
	}
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT secret_hash FROM im_credential WHERE id = 1`).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "IM 凭据无效")
	}
	if err != nil {
		return fmt.Errorf("service: 读取 IM 凭据哈希: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plainSecret)); err != nil {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "IM 凭据无效")
	}
	return nil
}

// Exists 报告是否已经生成过 IM 凭据，供控制台展示状态。
func (s *IMCredentialService) Exists(ctx context.Context) (bool, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*)::int FROM im_credential WHERE id = 1`).Scan(&n); err != nil {
		return false, fmt.Errorf("service: 查询 IM 凭据: %w", err)
	}
	return n > 0, nil
}
```

`generateSecret` 与 `bcryptCost` 复用 `internal/service/application.go` 里既有的那两个
（`Create` 生成 appSecret 用的就是它们）。若 `generateSecret` 当前是内联的，
先把它提成包级函数再复用——**不要复制一份**。

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/service -run TestIMCredential -v
```

预期：三条全 PASS。

- [ ] **Step 5: 变异验证**

把 `Verify` 里 `pgx.ErrNoRows` 那个分支改成返回 `fmt.Errorf(...)`，确认
`TestIMCredentialVerifyBeforeRotate` 变红。改回来。

- [ ] **Step 6: 提交**

```bash
git add internal/service/imcred.go internal/service/imcred_test.go internal/service/application.go
git commit -m "feat(service): IM 凭据的生成、轮换与校验"
```

---

## Task 4: 拦截器认 `fp-caller-type`

**Files:**
- Modify: `internal/grpcapi/auth_interceptor.go`
- Modify: `internal/grpcapi/server.go`（`Deps` 加 `IMCreds`）
- Test: `internal/grpcapi/auth_interceptor_test.go`

**Interfaces:**
- Consumes: Task 3 的 `(*IMCredentialService).Verify(ctx, secret) error`
- Produces:
  - 常量 `grpcapi.MDCallerType = "fp-caller-type"`、`grpcapi.CallerTypeIM = "im"`
  - `grpcapi.callerTypeFrom(ctx) string`（包内）
  - `grpcapi.Deps.IMCreds IMCredentialVerifier`（接口：`Verify(ctx, secret) error`）

- [ ] **Step 1: 写失败的测试**

追加到 `internal/grpcapi/auth_interceptor_test.go`：

```go
// fakeIMCreds 是 IMCredentialVerifier 的测试替身。
type fakeIMCreds struct {
	secret string
	calls  int
}

func (f *fakeIMCreds) Verify(_ context.Context, s string) error {
	f.calls++
	if s != f.secret {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "IM 凭据无效")
	}
	return nil
}

// TestCallerTypeIsAuthoritative 钉住这条设计：fp-caller-type 决定**只**比对
// 哪一份凭据，不是"提示先试哪个、失败再试另一个"。
//
// 声明本身不授予任何东西——声明 im 却拿着 app secret 一样失败——所以让它
// 权威在安全上零损失，却把最坏情况从两次 bcrypt 砍到一次。
func TestCallerTypeIsAuthoritative(t *testing.T) {
	apps := &fakeAppAuth{appID: "app1", secret: "app-secret"}
	imc := &fakeIMCreds{secret: "im-secret"}
	v := newAppVerifier(apps, time.Minute, imc, time.Minute)

	for _, tc := range []struct {
		name       string
		callerType string
		appID      string
		secret     string
		wantErr    bool
	}{
		{"普通调用用 app secret", "", "app1", "app-secret", false},
		{"普通调用拿 IM secret", "", "app1", "im-secret", true},
		{"im 调用用 IM secret", "im", "app1", "im-secret", false},
		{"im 调用拿 app secret", "im", "app1", "app-secret", true},
		{"未知的 caller type", "gateway", "app1", "im-secret", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md := metadata.Pairs(mdAppID, tc.appID, mdAppSecret, tc.secret)
			if tc.callerType != "" {
				md.Set(MDCallerType, tc.callerType)
			}
			ctx := metadata.NewIncomingContext(context.Background(), md)
			_, err := v.authenticate(ctx)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v，wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestCallerTypeOnlyOneBcrypt 钉住"只比对一份"：im 调用不能顺带去验
// app secret，反之亦然。两次 bcrypt 各 50–100ms，热路径上翻倍不可接受。
func TestCallerTypeOnlyOneBcrypt(t *testing.T) {
	apps := &fakeAppAuth{appID: "app1", secret: "app-secret"}
	imc := &fakeIMCreds{secret: "im-secret"}
	v := newAppVerifier(apps, time.Minute, imc, time.Minute)

	md := metadata.Pairs(mdAppID, "app1", mdAppSecret, "im-secret", MDCallerType, CallerTypeIM)
	if _, err := v.authenticate(metadata.NewIncomingContext(context.Background(), md)); err != nil {
		t.Fatal(err)
	}
	if apps.calls != 0 {
		t.Fatalf("im 调用不该触碰应用凭据校验，实际调了 %d 次", apps.calls)
	}
	if imc.calls != 1 {
		t.Fatalf("IM 凭据校验应当恰好一次，实际 %d 次", imc.calls)
	}
}

// TestNoCallerTypeBehavesExactlyAsBefore 钉住"发布后对现网零影响"：
// 不带 fp-caller-type 的调用，从 metadata 解析到错误，行为必须与改动前
// 逐字相同。
func TestNoCallerTypeBehavesExactlyAsBefore(t *testing.T) {
	apps := &fakeAppAuth{appID: "app1", secret: "app-secret"}
	v := newAppVerifier(apps, time.Minute, &fakeIMCreds{secret: "im-secret"}, time.Minute)

	md := metadata.Pairs(mdAppID, "app1", mdAppSecret, "app-secret")
	ctx, err := v.authenticate(metadata.NewIncomingContext(context.Background(), md))
	if err != nil {
		t.Fatalf("既有调用方式必须原样可用：%v", err)
	}
	if got, _ := appIDFrom(ctx); got != "app1" {
		t.Fatalf("appId 未放进 ctx，got %q", got)
	}
	if got := callerTypeFrom(ctx); got != "" {
		t.Fatalf("未声明时 callerType 应为空，got %q", got)
	}
}

// TestIMCallerRequiresAppID：im 调用同样要带 app-id（逐调用附上），
// 四个它能调的 RPC 全都需要 app 作用域。
func TestIMCallerRequiresAppID(t *testing.T) {
	v := newAppVerifier(&fakeAppAuth{appID: "app1", secret: "s"}, time.Minute,
		&fakeIMCreds{secret: "im-secret"}, time.Minute)
	md := metadata.Pairs(mdAppSecret, "im-secret", MDCallerType, CallerTypeIM)
	if _, err := v.authenticate(metadata.NewIncomingContext(context.Background(), md)); err == nil {
		t.Fatal("im 调用缺 fp-app-id 必须报错")
	}
}
```

若 `fakeAppAuth` 当前没有 `calls` 计数，给它加上。

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/grpcapi -run 'TestCallerType|TestNoCallerType|TestIMCaller'
```

预期：编译失败，`newAppVerifier` 参数数量不匹配、`MDCallerType` 未定义。

- [ ] **Step 3: 实现**

`internal/grpcapi/auth_interceptor.go`：

```go
// MDCallerType 是声明调用方类型的 metadata 键。
//
// **它是权威声明，不是提示**：值决定**只**比对哪一份凭据，不会"先试一个
// 失败再试另一个"。这在安全上零损失——声明本身不授予任何东西，声明 im
// 却没有 IM secret 一样失败——却把最坏情况从两次 bcrypt（各 50–100ms）
// 砍到一次。
//
// 空值（不带这个键）就是今天的行为，已接入的 SDK 无需任何改动。
const MDCallerType = "fp-caller-type"

// CallerTypeIM 表示调用方是 fp-im 网关，凭据是 IM secret 而不是某个应用的
// appSecret。
const CallerTypeIM = "im"

// IMCredentialVerifier 是拦截器对 IM 凭据的全部依赖。
// *service.IMCredentialService 满足它。
type IMCredentialVerifier interface {
	Verify(ctx context.Context, plainSecret string) error
}

type callerTypeCtxKey struct{}

// callerTypeFrom 返回本次调用已认证的调用方类型，空串表示普通应用。
func callerTypeFrom(ctx context.Context) string {
	v, _ := ctx.Value(callerTypeCtxKey{}).(string)
	return v
}
```

`appVerifier` 加两个字段与一段独立的 IM 缓存：

```go
type appVerifier struct {
	apps AppAuthenticator
	ttl  time.Duration

	mu    sync.RWMutex
	cache map[string]int64 // key → 过期时刻（UnixMilli）

	// imCreds 与 imTTL 是 IM 凭据那一路，与上面的应用凭据完全独立。
	imCreds IMCredentialVerifier
	imTTL   time.Duration
	imMu    sync.RWMutex
	imHash  string // 已验证通过的 secret 的 SHA-256，空表示无缓存
	imExp   int64
}
```

**IM 缓存的 TTL 取 10 秒，不是应用凭据那 5 分钟。** 理由写进注释：

```go
// DefaultIMSecretCacheTTL 是 IM 凭据校验结果的缓存时长。
//
// 刻意远短于应用凭据的 5 分钟（AppSecretCacheTTL）：IM 凭据全库只有一条，
// bcrypt 每 10 秒一次的开销可以忽略，而换来的是"轮换后旧 secret 最多再
// 活 10 秒"。
//
// 为什么不做成"轮换时主动清缓存"：fp 可以多实例部署，主动清只清得掉本
// 实例的，别的实例照样留满 TTL——要做对就得再走一遍 Redis 广播，为一条
// 凭据引入一整条中继不划算。短 TTL 零新增管道、跨实例天然一致。
const DefaultIMSecretCacheTTL = 10 * time.Second
```

`verifyIM`：

```go
// verifyIM 校验 IM 凭据，缓存成功结果。
//
// 与应用凭据同一纪律：**只缓存成功**。这里的键空间只有一个（IM 凭据全库
// 一条），所以缓存退化成"一份哈希 + 一个过期时刻"，连 map 都不需要。
func (v *appVerifier) verifyIM(ctx context.Context, secret string) error {
	if secret == "" {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "缺少 IM 凭据")
	}
	sum := sha256.Sum256([]byte(secret))
	h := hex.EncodeToString(sum[:])
	now := time.Now().UnixMilli()

	v.imMu.RLock()
	hit := v.imHash == h && v.imExp > now
	v.imMu.RUnlock()
	if hit {
		return nil
	}
	if err := v.imCreds.Verify(ctx, secret); err != nil {
		return err // 失败不入缓存
	}
	v.imMu.Lock()
	v.imHash, v.imExp = h, now+v.imTTL.Milliseconds()
	v.imMu.Unlock()
	return nil
}
```

`authenticate` 按 caller type 分流：

```go
func (v *appVerifier) authenticate(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "缺少 appId 或 appSecret")
	}
	appID := first(md, mdAppID)
	secret := first(md, mdAppSecret)

	switch callerType := first(md, MDCallerType); callerType {
	case "":
		if err := v.verify(ctx, appID, secret); err != nil {
			return nil, err
		}
		return context.WithValue(ctx, appIDCtxKey{}, appID), nil
	case CallerTypeIM:
		// im 调用同样要带 appId：它能调的四个 RPC 全都需要 app 作用域，
		// 只是这个 appId 来自 client 的 ws 握手帧、逐调用附上，而不是
		// 钉在连接的凭据里。
		if appID == "" {
			return nil, domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "缺少 appId")
		}
		if err := v.verifyIM(ctx, secret); err != nil {
			return nil, err
		}
		authed := context.WithValue(ctx, appIDCtxKey{}, appID)
		return context.WithValue(authed, callerTypeCtxKey{}, CallerTypeIM), nil
	default:
		return nil, domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid,
			"未知的调用方类型 %q", callerType)
	}
}
```

`newAppVerifier` 改签名：`newAppVerifier(apps AppAuthenticator, ttl time.Duration, imCreds IMCredentialVerifier, imTTL time.Duration) *appVerifier`。

`internal/grpcapi/server.go` 的 `Deps` 加：

```go
	// IMCreds 校验 fp-im 网关的凭据。为 nil 时任何 fp-caller-type: im 的
	// 调用都会被拒——这是测试里绕开 New 直接构造 Server 的既有用法能继续
	// 工作的前提。
	IMCreds IMCredentialVerifier
	// IMSecretCacheTTL 为 0 时取 DefaultIMSecretCacheTTL（10 秒）。
	IMSecretCacheTTL time.Duration
```

`New` 里相应改造，`IMCreds` 为 nil 时塞一个恒失败的实现：

```go
	imTTL := d.IMSecretCacheTTL
	if imTTL == 0 {
		imTTL = DefaultIMSecretCacheTTL
	}
	imCreds := d.IMCreds
	if imCreds == nil {
		imCreds = deniedIMCreds{}
	}
	verifier := newAppVerifier(d.Apps, ttl, imCreds, imTTL)
```

```go
// deniedIMCreds 在没有配置 IM 凭据校验器时拒绝一切 im 调用。
// 不用 nil 判断散在各处：一个恒失败的实现让调用点只有一条路径。
type deniedIMCreds struct{}

func (deniedIMCreds) Verify(context.Context, string) error {
	return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "IM 凭据无效")
}
```

`cmd/fp/main.go` 装配处补：

```go
	imCredSvc := service.NewIMCredentialService(pool)
	// …grpcapi.Deps 里加 IMCreds: imCredSvc,
```

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/grpcapi -run 'TestCallerType|TestNoCallerType|TestIMCaller' -v
go build ./...
```

预期：全 PASS，编译通过。

- [ ] **Step 5: 跑整包**

```bash
./scripts/test.sh ./internal/grpcapi ./internal/integration
```

预期：全绿。既有的拦截器测试一条都不能红——那正是"零现网影响"的证据。

- [ ] **Step 6: 变异验证**

把 `authenticate` 的 `case CallerTypeIM` 改成"先试 IM，失败再试 app"，确认
`TestCallerTypeIsAuthoritative` 的"im 调用拿 app secret"与 `TestCallerTypeOnlyOneBcrypt`
变红。改回来。

- [ ] **Step 7: 提交**

```bash
git add internal/grpcapi/auth_interceptor.go internal/grpcapi/auth_interceptor_test.go internal/grpcapi/server.go cmd/fp/main.go
git commit -m "feat(grpcapi): 拦截器认 fp-caller-type，IM 凭据单独校验与缓存"
```

---

## Task 5: 双向隔离

**Files:**
- Modify: `internal/grpcapi/auth_service.go`
- Modify: `internal/grpcapi/config_service.go`
- Test: `internal/grpcapi/auth_service_test.go`

**Interfaces:**
- Consumes: Task 4 的 `callerTypeFrom(ctx)`、`CallerTypeIM`
- Produces: `grpcapi.requireNotIM(ctx) error`（包内）

- [ ] **Step 1: 写失败的测试**

追加到 `internal/grpcapi/auth_service_test.go`：

```go
// TestIMCallerCannotMintSessions 是 IM secret 泄露时的爆炸半径边界。
//
// type=im 不是普通 app 的超集。如果它能 Login，一份泄露的 IM secret 就等于
// 能对**任意** app 冒充**任意**用户——那比"读到 app 的 IM 配置"严重一个
// 量级。隔离之后 IM 凭据的能力被压到"只能核实，不能签发"。
func TestIMCallerCannotMintSessions(t *testing.T) {
	s := newTestAuthServer(t)
	ctx := imCallerContext("app1")

	if _, err := s.Login(ctx, &fpv1.LoginRequest{ConnectorType: "password"}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("Login: code = %v, want PermissionDenied", status.Code(err))
	}
	if _, err := s.SendLoginCode(ctx, &fpv1.SendLoginCodeRequest{Phone: "13800000000"}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("SendLoginCode: code = %v, want PermissionDenied", status.Code(err))
	}
	if _, err := s.Logout(ctx, &fpv1.LogoutRequest{Token: "t"}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("Logout: code = %v, want PermissionDenied", status.Code(err))
	}
	if _, err := s.ReportPermissions(ctx, &fpv1.ReportPermissionsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("ReportPermissions: code = %v, want PermissionDenied", status.Code(err))
	}
	if _, err := s.GetPolicy(ctx, &fpv1.GetPolicyRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("GetPolicy: code = %v, want PermissionDenied", status.Code(err))
	}
}

// TestIMCallerCanValidateAndWatch：隔离是双向的，不是把 im 关在门外。
// ValidateToken 与 Watch 正是 fp-im 存在的理由。
func TestIMCallerCanValidateAndWatch(t *testing.T) {
	s := newTestAuthServer(t)
	_, err := s.ValidateToken(imCallerContext("app1"), &fpv1.ValidateTokenRequest{Token: "bad-token"})
	if status.Code(err) == codes.PermissionDenied {
		t.Fatal("ValidateToken 必须对 type=im 开放")
	}
}

// TestIMCallerValidateRequiresIMEnabled 是设计文档 9.4 那条"权威判定"。
//
// fp-im 自己也会在握手前查一次 im_enabled，但它读的是**自己的缓存**；这里
// 读的是**库**。只留前者的话，"关掉 im_enabled 后不再允许新连接"就依赖
// fp-im 缓存的新鲜度——而 fp 自己的设计里就承认推送会漏（WatchPurge 存在
// 的全部理由就是"Redis 订阅重建时会漏读事件且不知道漏了哪些"）。漏一次
// 推送这个开关就悄悄失效，新 client 照连不误且没有任何报错。
//
// 代价是零：activeApp 本来就要读那一行查 status，im_enabled 在同一行上。
//
// 只对 type=im 生效：普通业务方 SDK 调 ValidateToken 与 IM 毫无关系，
// 给它加这条判断会把没开 IM 的应用全部弄挂。
func TestIMCallerValidateRequiresIMEnabled(t *testing.T) {
	s, appID := newTestAuthServerWithIM(t, domain.IMConfig{Enabled: false})

	// type=im 调用：必须拒。
	_, err := s.ValidateToken(imCallerContext(appID), &fpv1.ValidateTokenRequest{Token: "t"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("im 调用方在 im_enabled=false 时 code = %v, want FailedPrecondition", status.Code(err))
	}

	// 普通调用：不受影响。
	normal := context.WithValue(context.Background(), appIDCtxKey{}, appID)
	_, err = s.ValidateToken(normal, &fpv1.ValidateTokenRequest{Token: "t"})
	if status.Code(err) == codes.FailedPrecondition {
		t.Fatal("普通调用方不该被 im_enabled 影响——那会把没开 IM 的应用全部弄挂")
	}
}
```

`imCallerContext(appID)` 是本文件的新助手：

```go
// imCallerContext 造一个"已通过 im 凭据认证"的 ctx，跳过拦截器直接构造。
func imCallerContext(appID string) context.Context {
	ctx := context.WithValue(context.Background(), appIDCtxKey{}, appID)
	return context.WithValue(ctx, callerTypeCtxKey{}, CallerTypeIM)
}
```

`internal/grpcapi/config_service_test.go` 里加一条对称的：

```go
func TestIMCallerCannotReadConfig(t *testing.T) {
	s := newTestConfigServer(t)
	_, err := s.GetConfig(imCallerContext("app1"), &fpv1.GetConfigRequest{Type: "DEFAULT"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("GetConfig: code = %v, want PermissionDenied", status.Code(err))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/grpcapi -run 'TestIMCaller'
```

预期：`Login` 等返回的不是 `PermissionDenied`。

- [ ] **Step 3: 实现**

`internal/grpcapi/auth_service.go` 加一个守卫，并在五个 handler 的第一行调用：

```go
// requireNotIM 拒绝 fp-im 网关调用面向业务应用的接口。
//
// 双向隔离而不是提权：type=im 能做的事是普通应用的一个**不同**子集，不是
// 超集。最关键的是 Login/SendLoginCode——能签发会话意味着一份泄露的 IM
// 凭据可以对任意应用冒充任意用户。ReportPermissions/GetPolicy 与配置读取
// 同理：那些是业务方自己的东西，网关没有任何理由碰。
func requireNotIM(ctx context.Context) error {
	if callerTypeFrom(ctx) == CallerTypeIM {
		return status.Error(codes.PermissionDenied, "IM 网关凭据不能调用该接口")
	}
	return nil
}
```

在 `SendLoginCode` / `Login` / `Logout` / `ReportPermissions` / `GetPolicy` 的开头各加：

```go
	if err := requireNotIM(ctx); err != nil {
		return nil, err
	}
```

`internal/grpcapi/config_service.go` 的 `GetConfig` 同样。

**不要**给 `ValidateToken` 与 `Watch` 加——那两个正是 fp-im 要用的。

但这两个要加另一条判断：**`type=im` 时应用必须打开了 `im_enabled`。**
`authServer.callerApp` 是它俩共用的取应用入口，加在那里：

```go
// callerApp 取出本次调用已认证的那个应用。
//
// type=im 时额外要求 im_enabled——这是"关掉开关后不再允许新连接"的**权威**
// 判定点。fp-im 自己也会在握手前查一次，但它读的是自己的缓存，而缓存靠
// 推送刷新，推送会漏（见 WatchPurge 的注释）。只留 fp-im 那一次的话，
// 漏一次推送这个开关就悄悄失效。
//
// 代价是零：GetActiveByAppID 本来就要读那一行查 status，im_enabled 在同
// 一行上。
//
// 只对 type=im 生效：普通业务方调 ValidateToken 与 IM 毫无关系，给它加这
// 条判断会把所有没开 IM 的应用全部弄挂。
func (s *authServer) callerApp(ctx context.Context) (*domain.Application, error) {
	// …既有的取 appID + GetActiveByAppID…
	if callerTypeFrom(ctx) == CallerTypeIM && !app.IM.Enabled {
		return nil, status.Error(codes.FailedPrecondition, "该应用未启用 IM 接入")
	}
	return app, nil
}
```

`ValidateToken` 当前若是直接调 `s.auth.ValidateToken(ctx, appID, token)` 而没走
`callerApp`，先把它改成先取 app 再调——**不要**把这条判断复制到两个地方。

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/grpcapi -run TestIMCaller -v
```

预期：三条全 PASS。

- [ ] **Step 5: 变异验证**

把 `Login` 里那句 `requireNotIM` 删掉，确认 `TestIMCallerCannotMintSessions` 变红。改回来。

- [ ] **Step 6: 提交**

```bash
git add internal/grpcapi/auth_service.go internal/grpcapi/auth_service_test.go internal/grpcapi/config_service.go internal/grpcapi/config_service_test.go
git commit -m "feat(grpcapi): type=im 与普通应用双向隔离"
```

---

## Task 6: proto —— IMGatewayService 与 AppIMConfigChanged

**Files:**
- Create: `proto/fp/v1/im.proto`
- Modify: `proto/fp/v1/auth.proto`（`WatchResponse` 的 oneof 加一支）

**Interfaces:**
- Produces（`sdk/gen/fp/v1`）：`fpv1.IMGatewayServiceClient/Server`、`fpv1.GetAppIMConfigRequest/Response`、`fpv1.IMBizAuth`、`fpv1.VerifyAppCredentialRequest/Response`、`fpv1.AppIMConfigChanged`、`fpv1.WatchResponse_AppImConfigChanged`

- [ ] **Step 1: 写 proto**

`proto/fp/v1/im.proto`：

```protobuf
syntax = "proto3";

package fp.v1;

option go_package = "github.com/basicfu/fp/sdk/gen/fp/v1;fpv1";

// IMGatewayService 是 fp 与 fp-im 网关之间的全部契约。
//
// **只有携带 fp-caller-type: im 的调用方能用**（凭据是 IM secret，不是某个
// 应用的 appSecret）；普通应用调用一律 PermissionDenied。反过来，im 调用方
// 也不能碰 AuthService 的 Login 系列与 ConfigService——隔离是双向的，
// 见设计文档 3.3。
//
// 作用域一律来自 metadata 的 fp-app-id，请求体里不再重复：两处都有就要
// 回答"不一致算谁的"，而这个问题不该存在。fp-im 的那个 app-id 来自 client
// 的 ws 握手帧，逐调用附上。
service IMGatewayService {
  // GetAppIMConfig 返回 metadata 中那个应用的 IM 接入配置。
  //
  // 应用不存在、被停用、或没打开 im_enabled 时返回 FAILED_PRECONDITION，
  // fp-im 据此给 client 关闭码 4002（策略拒绝，别重连）而不是 4001。
  rpc GetAppIMConfig(GetAppIMConfigRequest) returns (GetAppIMConfigResponse);

  // VerifyAppCredential 核实 secret 是否为 metadata 中那个应用的 appSecret。
  //
  // fp-im 没有数据库、也拿不到任何应用的 secret（fp 只存 bcrypt 哈希），
  // 业务 server 连上来时只能把凭据转给 fp 核实。
  rpc VerifyAppCredential(VerifyAppCredentialRequest) returns (VerifyAppCredentialResponse);
}

message GetAppIMConfigRequest {}

message GetAppIMConfigResponse {
  bool allow_guest = 1;
  // conn_policy 取值 replace / reject / limit，与服务端 domain.IMConnPolicy*
  // 及 fp-im 的 model.Policy 逐字相同。
  string conn_policy = 2;
  int32 conn_limit = 3;
  int32 guest_ip_rate = 4;
  // biz_auth 不设表示这个应用不支持业务方令牌。
  // 用 message 而不是铺平三个字段：这是"整组有或整组没有"的东西。
  IMBizAuth biz_auth = 5;
}

message IMBizAuth {
  // verify_url 必须是 https：client 的令牌明文走在请求体里。
  string verify_url = 1;
  // timeout_ms 是整个回调请求的超时，必须明显小于握手的 5 秒上限。
  int32 timeout_ms = 2;
  int32 cache_size = 3;
}

message VerifyAppCredentialRequest {
  string secret = 1;
}

message VerifyAppCredentialResponse {}
```

`proto/fp/v1/auth.proto` 的 `WatchResponse` oneof 末尾追加（沿用既有编号规律，
取下一个未使用的字段号）：

```protobuf
    // app_im_config_changed 表示某个应用的 IM 接入配置变了，fp-im 应当
    // 重拉一次 GetAppIMConfig。只有 fp-caller-type: im 的流会收到。
    AppIMConfigChanged app_im_config_changed = 7;
```

并在文件末尾加消息：

```protobuf
// AppIMConfigChanged 是一次 IM 配置变更的通知。
//
// 只推信号不推内容，与 ConfigChanged / PolicyChanged 同一范式：fp-im 收到
// 后重拉全量。它替掉了 fp-im 早期那套"每 10 秒看一次 apps 文件 mtime"的
// 轮询。
message AppIMConfigChanged {
  string app_id = 1;
}
```

- [ ] **Step 2: 生成并确认编译**

```bash
./scripts/gen.sh
go build ./...
```

预期：生成成功，`sdk/gen/fp/v1` 下多出 `im.pb.go` / `im_grpc.pb.go`，编译通过。

- [ ] **Step 3: 确认旧 SDK 兼容性没被破坏**

```bash
./scripts/test.sh ./sdk ./internal/grpcapi
```

预期：全绿。oneof 加分支是向后兼容的——proto 注释里本来就写着"旧 SDK 遇到
不认识的分支会落到 default，忽略即可"。

- [ ] **Step 4: 提交**

```bash
git add proto/fp/v1/im.proto proto/fp/v1/auth.proto sdk/gen/
git commit -m "feat(proto): IMGatewayService 与 AppIMConfigChanged"
```

---

## Task 7: IMGatewayService 实现

**Files:**
- Create: `internal/grpcapi/im_service.go`, `internal/grpcapi/im_service_test.go`
- Modify: `internal/grpcapi/server.go`（注册服务）

**Interfaces:**
- Consumes: Task 2 的 `Application.IM`、Task 4 的 `callerTypeFrom`、Task 6 的 gen 类型
- Produces: `grpcapi.newIMServer(apps *service.ApplicationService) fpv1.IMGatewayServiceServer`

- [ ] **Step 1: 写失败的测试**

`internal/grpcapi/im_service_test.go`（关键三条，其余按同样形状补）：

```go
// TestGetAppIMConfigRequiresIMCaller：普通应用不能调这两个 RPC。
func TestGetAppIMConfigRequiresIMCaller(t *testing.T) {
	s := newTestIMServer(t)
	ctx := context.WithValue(context.Background(), appIDCtxKey{}, "app1") // 没有 callerType
	if _, err := s.GetAppIMConfig(ctx, &fpv1.GetAppIMConfigRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
}

// TestGetAppIMConfigRejectsDisabledIM：im_enabled 关着时必须是
// FailedPrecondition——fp-im 据此给 4002 而不是 4001。给错了的话 client
// 会被指去重新登录，登录完还是连不上，变成死循环。
func TestGetAppIMConfigRejectsDisabledIM(t *testing.T) {
	s, appID := newTestIMServerWithApp(t, domain.IMConfig{Enabled: false})
	_, err := s.GetAppIMConfig(imCallerContext(appID), &fpv1.GetAppIMConfigRequest{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition", status.Code(err))
	}
}

// TestVerifyAppCredentialChecksSecretBeforeIMEnabled 钉住检查顺序。
//
// 先 bcrypt 验凭据、再看 im_enabled：顺序反了的话，一个手里没有 appSecret
// 的人也能探出某个应用有没有开 IM。而凭据验过之后再告诉它"IM 没开"是安全
// 的——能走到这一步的人本来就持有那份 secret，且运维需要这个区分才知道去
// 翻开关。
func TestVerifyAppCredentialChecksSecretBeforeIMEnabled(t *testing.T) {
	s, appID, secret := newTestIMServerWithAppSecret(t, domain.IMConfig{Enabled: false})

	// 凭据错 + IM 没开 → Unauthenticated（不泄露 im_enabled）
	_, err := s.VerifyAppCredential(imCallerContext(appID), &fpv1.VerifyAppCredentialRequest{Secret: "wrong"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("secret 错时 code = %v, want Unauthenticated", status.Code(err))
	}
	// 凭据对 + IM 没开 → FailedPrecondition（明确指向那个开关）
	_, err = s.VerifyAppCredential(imCallerContext(appID), &fpv1.VerifyAppCredentialRequest{Secret: secret})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("secret 对但 IM 未启用时 code = %v, want FailedPrecondition", status.Code(err))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/grpcapi -run 'TestGetAppIMConfig|TestVerifyAppCredential'
```

预期：`undefined: newTestIMServer`。

- [ ] **Step 3: 实现**

`internal/grpcapi/im_service.go`：

```go
package grpcapi

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// imServer 实现 IMGatewayService。只对 fp-caller-type: im 开放。
type imServer struct {
	fpv1.UnimplementedIMGatewayServiceServer
	apps *service.ApplicationService
}

func newIMServer(apps *service.ApplicationService) fpv1.IMGatewayServiceServer {
	return &imServer{apps: apps}
}

// requireIM 拒绝普通应用调用网关接口。与 requireNotIM 一起构成双向隔离。
func requireIM(ctx context.Context) error {
	if callerTypeFrom(ctx) != CallerTypeIM {
		return status.Error(codes.PermissionDenied, "该接口只对 IM 网关开放")
	}
	return nil
}

// imApp 取出 metadata 里那个应用，并要求它启用且打开了 IM。
func (s *imServer) imApp(ctx context.Context) (*domain.Application, error) {
	appID, ok := appIDFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "缺少 appId")
	}
	app, err := s.apps.GetActiveByAppID(ctx, appID)
	if err != nil {
		// 应用不存在或已停用：对 fp-im 而言与"没打开 IM"是同一种后果，
		// 统一成 FailedPrecondition，它据此给 client 关闭码 4002。
		return nil, status.Error(codes.FailedPrecondition, "该应用不可用")
	}
	if !app.IM.Enabled {
		return nil, status.Error(codes.FailedPrecondition, "该应用未启用 IM 接入")
	}
	return app, nil
}

func (s *imServer) GetAppIMConfig(ctx context.Context, _ *fpv1.GetAppIMConfigRequest) (*fpv1.GetAppIMConfigResponse, error) {
	if err := requireIM(ctx); err != nil {
		return nil, err
	}
	app, err := s.imApp(ctx)
	if err != nil {
		return nil, err
	}
	resp := &fpv1.GetAppIMConfigResponse{
		AllowGuest:  app.IM.AllowGuest,
		ConnPolicy:  app.IM.ConnPolicy,
		ConnLimit:   app.IM.ConnLimit,
		GuestIpRate: app.IM.GuestIPRate,
	}
	if b := app.IM.BizAuth; b != nil {
		resp.BizAuth = &fpv1.IMBizAuth{
			VerifyUrl: b.VerifyURL,
			TimeoutMs: b.TimeoutMs,
			CacheSize: b.CacheSize,
		}
	}
	return resp, nil
}

func (s *imServer) VerifyAppCredential(ctx context.Context, req *fpv1.VerifyAppCredentialRequest) (*fpv1.VerifyAppCredentialResponse, error) {
	if err := requireIM(ctx); err != nil {
		return nil, err
	}
	appID, ok := appIDFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "缺少 appId")
	}
	// **先验凭据，再看 im_enabled。** 顺序反了的话，一个手里没有 appSecret
	// 的人也能探出某个应用有没有开 IM。凭据验过之后再区分是安全的：能走到
	// 这一步的人本来就持有那份 secret，而运维需要这个区分才知道去翻开关。
	//
	// VerifySecret 内部对"应用不存在"与"secret 错"返回同一个错误，防 appId
	// 枚举——这里原样透传，不要试图区分。
	app, err := s.apps.VerifySecret(ctx, appID, req.GetSecret())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "应用凭据无效")
	}
	if app.Status != domain.ApplicationStatusActive {
		return nil, status.Error(codes.Unauthenticated, "应用凭据无效")
	}
	if !app.IM.Enabled {
		return nil, status.Error(codes.FailedPrecondition, "该应用未启用 IM 接入")
	}
	return &fpv1.VerifyAppCredentialResponse{}, nil
}
```

`internal/grpcapi/server.go` 的 `New` 里注册：

```go
	fpv1.RegisterIMGatewayServiceServer(srv, newIMServer(d.Apps))
```

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/grpcapi -run 'TestGetAppIMConfig|TestVerifyAppCredential' -v
```

预期：全 PASS。

- [ ] **Step 5: 变异验证**

把 `VerifyAppCredential` 里 `im_enabled` 的判断挪到 `VerifySecret` **之前**，确认
`TestVerifyAppCredentialChecksSecretBeforeIMEnabled` 的第一条断言变红。改回来。

- [ ] **Step 6: 提交**

```bash
git add internal/grpcapi/im_service.go internal/grpcapi/im_service_test.go internal/grpcapi/server.go
git commit -m "feat(grpcapi): IMGatewayService 的两个 RPC"
```

---

## Task 8: 撤销对 type=im 通配扇出

**Files:**
- Modify: `internal/grpcapi/watch.go`
- Modify: `internal/grpcapi/auth_service.go`（`Watch` handler）
- Test: `internal/grpcapi/watch_test.go`

**Interfaces:**
- Consumes: Task 4 的 `callerTypeFrom`
- Produces: `(*RevokeHub).SubscribeAll() (<-chan HubEvent, func())` —— 不按 app 过滤的订阅

- [ ] **Step 1: 写失败的测试**

追加到 `internal/grpcapi/watch_test.go`：

```go
// TestSubscribeAllReceivesEveryApp：type=im 的订阅者要收到所有应用的撤销。
//
// fp-im 只有一条到 fp 的连接、一条 Watch 流，服务的却是所有应用。按 app
// 过滤的话它只能收到一个应用的撤销，其余应用被踢下线的用户 ws 会一直挂着。
func TestSubscribeAllReceivesEveryApp(t *testing.T) {
	h := newTestRevokeHub(t)
	events, unsub := h.SubscribeAll()
	defer unsub()

	appA, appB := uuid.New(), uuid.New()
	h.fanout(domain.RevokeEvent{AppID: appA, Tokens: []string{"ta"}})
	h.fanout(domain.RevokeEvent{AppID: appB, Tokens: []string{"tb"}})

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case ev := <-events:
			got[ev.Revoke.Tokens[0]] = true
		case <-time.After(time.Second):
			t.Fatalf("只收到 %d 条事件，期望 2 条", i)
		}
	}
	if !got["ta"] || !got["tb"] {
		t.Fatalf("收到的事件 = %v，两个应用的都要收到", got)
	}
}

// TestSubscribeByAppStillFilters：通配订阅不能把既有的按 app 过滤弄坏。
// 业务方 SDK 绝不能收到别的应用的 token 列表。
func TestSubscribeByAppStillFilters(t *testing.T) {
	h := newTestRevokeHub(t)
	appA, appB := uuid.New(), uuid.New()
	events, unsub := h.Subscribe(appA)
	defer unsub()

	h.fanout(domain.RevokeEvent{AppID: appB, Tokens: []string{"tb"}})
	h.fanout(domain.RevokeEvent{AppID: appA, Tokens: []string{"ta"}})

	select {
	case ev := <-events:
		if ev.Revoke.Tokens[0] != "ta" {
			t.Fatalf("收到了别的应用的事件：%v", ev.Revoke.Tokens)
		}
	case <-time.After(time.Second):
		t.Fatal("本应用的事件没收到")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/grpcapi -run TestSubscribe
```

预期：`undefined: h.SubscribeAll`。

- [ ] **Step 3: 实现**

`revokeSub` 加一个字段，`fanout` 改一行判断：

```go
type revokeSub struct {
	appID uuid.UUID
	// all 为 true 时不按 app 过滤：fp-im 网关只有一条 Watch 流，服务的却是
	// 所有应用。业务方 SDK 永远是 false——它绝不能收到别的应用的 token。
	all bool
	ch  chan HubEvent
}
```

`fanout` 里那句：

```go
		if !sub.all && ev.AppID != uuid.Nil && ev.AppID != sub.appID {
			continue
		}
```

新增：

```go
// SubscribeAll 登记一个不按 app 过滤的订阅者，只给 fp-im 网关用。
//
// 与 Subscribe 共用同一份注册表与同一条 Redis 订阅——多一个订阅者不会多一次
// Redis 订阅，见 subscribeCalls 的注释。
func (h *RevokeHub) SubscribeAll() (<-chan HubEvent, func()) {
	return h.subscribeWith(revokeSub{all: true})
}
```

把 `Subscribe` 与 `SubscribeAll` 的公共部分提成 `subscribeWith(proto revokeSub)`，
**不要复制一遍注册/注销逻辑**。

`internal/grpcapi/auth_service.go` 的 `Watch` handler 分流：

```go
	// fp-im 网关只有一条流，服务所有应用，所以不按 app 过滤。
	// 它多收到几条自己没有连接的应用的撤销是无害的：fp-im 侧按 token 查
	// 本地连接表，查不到就什么都不做。为此在 fp 侧按 im_enabled 过滤要给
	// 每条事件多查一次应用，不划算。
	var events <-chan HubEvent
	var unsubscribe func()
	if callerTypeFrom(ctx) == CallerTypeIM {
		events, unsubscribe = s.hub.SubscribeAll()
	} else {
		events, unsubscribe = s.hub.Subscribe(app.ID)
	}
	defer unsubscribe()
```

配置那条订阅（`s.configHub.Subscribe(app.ID)`）保持不变——`type=im` 不读配置中心。

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/grpcapi -run TestSubscribe -v
./scripts/test.sh ./internal/grpcapi
```

预期：新增两条 PASS，整包全绿。

- [ ] **Step 5: 变异验证**

把 `fanout` 里的 `!sub.all &&` 去掉，确认 `TestSubscribeAllReceivesEveryApp` 变红；
再把它改成恒 `true`（即所有订阅者都不过滤），确认 `TestSubscribeByAppStillFilters`
变红。都改回来。

- [ ] **Step 6: 提交**

```bash
git add internal/grpcapi/watch.go internal/grpcapi/watch_test.go internal/grpcapi/auth_service.go
git commit -m "feat(grpcapi): 撤销对 IM 网关通配扇出"
```

---

## Task 9: AppIMConfigChanged 推送链路

**Files:**
- Modify: `internal/store/config.go`（复用既有的配置广播通道）
- Modify: `internal/grpcapi/config_hub.go`
- Modify: `internal/grpcapi/auth_service.go`（`Watch` 里投递新事件）
- Modify: `internal/service/application.go`（`SetIMConfig` 后发信号）
- Test: `internal/integration/im_config_push_test.go`

**Interfaces:**
- Consumes: Task 2 的 `SetIMConfig`、Task 6 的 `fpv1.AppIMConfigChanged`
- Produces: `SetIMConfig` 成功后经 Redis 广播一条 IM 配置变更，`type=im` 的 Watch 流收到 `AppIMConfigChanged{app_id}`

- [ ] **Step 1: 写失败的集成测试**

`internal/integration/im_config_push_test.go`：

```go
// TestIMConfigChangePushesToIMWatcher 钉住热更新链路：控制台改完 IM 配置，
// fp-im 的 Watch 流上要收到通知，而不是等它自己轮询。
//
// 这条链路替掉了 fp-im 早期那套"每 10 秒看一次 apps 文件 mtime"。
func TestIMConfigChangePushesToIMWatcher(t *testing.T) {
	env := newPhase2Env(t)
	app := env.createApp(t, "im-push")

	stream := env.openIMWatch(t, app.AppID) // 带 fp-caller-type: im 的 Watch 流
	env.awaitReady(t, stream)

	cfg := domain.DefaultIMConfig()
	cfg.Enabled = true
	if _, err := env.apps.SetIMConfig(context.Background(), app.ID, cfg); err != nil {
		t.Fatal(err)
	}

	ev := env.awaitEvent(t, stream, 5*time.Second)
	changed := ev.GetAppImConfigChanged()
	if changed == nil {
		t.Fatalf("收到的事件不是 AppIMConfigChanged：%+v", ev)
	}
	if changed.GetAppId() != app.AppID {
		t.Fatalf("app_id = %q, want %q", changed.GetAppId(), app.AppID)
	}
}

// TestIMConfigChangeNotPushedToNormalWatcher：普通业务方 SDK 不该收到这类
// 事件——它们不认识这个分支，也不该被无关的通知打扰。
func TestIMConfigChangeNotPushedToNormalWatcher(t *testing.T) {
	env := newPhase2Env(t)
	app := env.createApp(t, "im-push-2")

	stream := env.openWatch(t, app.AppID) // 普通流
	env.awaitReady(t, stream)

	cfg := domain.DefaultIMConfig()
	cfg.Enabled = true
	if _, err := env.apps.SetIMConfig(context.Background(), app.ID, cfg); err != nil {
		t.Fatal(err)
	}
	env.expectNoEvent(t, stream, 2*time.Second)
}
```

`newPhase2Env` / `createApp` / `openWatch` / `awaitReady` / `awaitEvent` / `expectNoEvent`
沿用 `internal/integration/phase2_env_test.go` 里既有的助手；`openIMWatch` 是新增的，
与 `openWatch` 的差别只是 metadata 里多一个 `fp-caller-type: im`、secret 用 IM secret。

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/integration -run TestIMConfigChange
```

预期：`undefined: env.openIMWatch` 或收不到事件。

- [ ] **Step 3: 实现**

复用既有的配置广播基础设施，**不新建 Redis 通道**：`store.ConfigPublisher` 已经有
一条 `fp:config` 通道与完整的订阅重建/Gap 处理，IM 配置变更再开一条等于把那套
推理抄第二遍。给它的负载加一个类型标记：

```go
// ConfigSignal 的负载加一个 Kind，区分配置中心的变更与 IM 配置的变更。
// 两者共用同一条 Redis 通道与同一套订阅重建/Gap 处理——那套推理（订阅
// 重建时会漏读且不知道漏了哪些，只能让订阅方全量重拉）对两者完全一样，
// 不值得抄第二遍。
const (
	ConfigKindCenter = "center" // 配置中心的分区变更，空值也按它处理（向后兼容）
	ConfigKindIM     = "im"     // 应用的 IM 接入配置变更
)
```

`ConfigHub.fanout` 按 Kind 分派：`center` 走今天的 `ConfigChanged`；`im` 只投给
`all`（type=im）订阅者，投递成 `AppIMConfigChanged`。

`ConfigHub` 同样需要一个 `SubscribeAll()`，形状与 Task 8 的 `RevokeHub.SubscribeAll`
一致。`Watch` handler 里对 im 调用方改用它。

`ApplicationService.SetIMConfig` 成功后发布：

```go
	// 发布在事务之外、返回之前：失败只记日志不影响写入结果——配置已经
	// 落库，漏推一条通知的后果是 fp-im 的缓存最多陈旧到它下次重启，
	// 而让整个保存失败会让人以为没存上、反复重试。
	if err := s.configPub.PublishIM(ctx, app.AppID); err != nil {
		slog.Error("service: 广播 IM 配置变更失败", "appId", app.AppID, "err", err)
	}
```

`ApplicationService` 因此要多一个 `configPub` 依赖——构造函数加参数，
`cmd/fp/main.go` 与全部测试装配点同步。

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/integration -run TestIMConfigChange -v
./scripts/test.sh ./internal/grpcapi ./internal/service ./internal/store
```

预期：新增两条 PASS，相关包全绿。

- [ ] **Step 5: 变异验证**

把 `ConfigHub.fanout` 里 IM 事件的"只投给 all 订阅者"改成投给所有订阅者，确认
`TestIMConfigChangeNotPushedToNormalWatcher` 变红。改回来。

- [ ] **Step 6: 提交**

```bash
git add internal/store/config.go internal/grpcapi/config_hub.go internal/grpcapi/auth_service.go internal/service/application.go internal/integration/im_config_push_test.go cmd/fp/main.go
git commit -m "feat: IM 配置变更经 Watch 流推给网关"
```

---

## Task 10: SDK —— CallerType 与两个新 RPC

**Files:**
- Modify: `sdk/options.go`, `sdk/client.go`
- Create: `sdk/imgateway.go`, `sdk/imgateway_test.go`
- Test: `sdk/options_test.go`

**Interfaces:**
- Consumes: Task 6 的 `fpv1.IMGatewayServiceClient`
- Produces:
  - `fpsdk.Options.CallerType string`（`""` / `fpsdk.CallerTypeIM`）
  - `fpsdk.CallerTypeIM = "im"`
  - `(*Client).IMGateway() *IMGateway`
  - `(*IMGateway).GetAppIMConfig(ctx, appID string) (*AppIMConfig, error)`
  - `(*IMGateway).VerifyAppCredential(ctx, appID, secret string) error`
  - `fpsdk.AppIMConfig{AllowGuest bool; ConnPolicy string; ConnLimit int32; GuestIPRate int32; BizAuth *AppIMBizAuth}`
  - `fpsdk.AppIMBizAuth{VerifyURL string; TimeoutMs int32; CacheSize int32}`
  - `fpsdk.WithAppID(ctx, appID) context.Context` —— 逐调用附上 app 作用域

- [ ] **Step 1: 写失败的测试**

追加到 `sdk/options_test.go`：

```go
// TestCallerTypeIMAllowsEmptyAppID：fp-im 的连接没有固定 app——它的 appId
// 来自每个 client 的 ws 握手帧，逐调用附上。
func TestCallerTypeIMAllowsEmptyAppID(t *testing.T) {
	o := Options{Addr: "x:1", AppSecret: "s", CallerType: CallerTypeIM}
	if err := o.validate(); err != nil {
		t.Fatalf("CallerType=im 时应允许空 AppID：%v", err)
	}
}

func TestDefaultCallerTypeStillRequiresAppID(t *testing.T) {
	o := Options{Addr: "x:1", AppSecret: "s"}
	if err := o.validate(); err == nil {
		t.Fatal("默认调用方仍然必须填 AppID——这条不能因为新增字段而放松")
	}
}

// TestIMCredentialsOmitAppID：im 凭据不输出 fp-app-id，为逐调用附上的那个
// 让路。两个都出的话 metadata 里会有两个值，取哪个是未定义行为。
func TestIMCredentialsOmitAppID(t *testing.T) {
	c := newAppCredentials(Options{AppID: "ignored", AppSecret: "s", CallerType: CallerTypeIM, Insecure: true})
	md, err := c.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := md["fp-app-id"]; ok {
		t.Fatal("im 凭据不能输出 fp-app-id")
	}
	if md["fp-caller-type"] != "im" {
		t.Fatalf("fp-caller-type = %q, want im", md["fp-caller-type"])
	}
}

// TestDefaultCredentialsUnchanged 钉住"已接入的 SDK 一个字节都不用改"。
func TestDefaultCredentialsUnchanged(t *testing.T) {
	c := newAppCredentials(Options{AppID: "app1", AppSecret: "s", Insecure: true})
	md, err := c.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if md["fp-app-id"] != "app1" || md["fp-app-secret"] != "s" {
		t.Fatalf("既有凭据形状变了：%v", md)
	}
	if _, ok := md["fp-caller-type"]; ok {
		t.Fatal("默认调用方不该出现 fp-caller-type")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./sdk -run 'TestCallerType|TestDefaultCallerType|TestIMCredentials|TestDefaultCredentials'
```

预期：`unknown field CallerType`。

- [ ] **Step 3: 实现**

`sdk/options.go`：

```go
// CallerTypeIM 表示本客户端是 fp-im 网关，凭据是 IM secret 而不是某个应用
// 的 appSecret。业务方**不要**用它——fp 对它开放的是与业务方完全不同的一
// 组接口（不能 Login，不能读配置中心）。
const CallerTypeIM = "im"

// Options 新增字段：
	// CallerType 声明调用方类型，空表示普通业务应用（默认，行为与不设这个
	// 字段完全一致）。设为 CallerTypeIM 时：
	//   - AppID 允许为空，app 作用域改为逐调用附上（见 WithAppID）
	//   - 凭据里输出 fp-caller-type: im，不输出 fp-app-id
	CallerType string
```

`validate()`：

```go
	case o.AppID == "" && o.CallerType != CallerTypeIM:
		return errors.New("fpsdk: Options.AppID 不能为空")
	case o.CallerType != "" && o.CallerType != CallerTypeIM:
		return errors.New("fpsdk: Options.CallerType 只能为空或 " + CallerTypeIM)
```

`appCredentials` 加 `callerType` 字段，`GetRequestMetadata` 分流：

```go
func (c appCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	if c.callerType == CallerTypeIM {
		// 不输出 fp-app-id：作用域由调用方逐调用附上（WithAppID）。
		// 两个都出的话 metadata 里会有两个值，服务端 first() 取哪个是
		// 未定义行为。
		return map[string]string{
			"fp-app-secret":  c.secret,
			"fp-caller-type": CallerTypeIM,
		}, nil
	}
	return map[string]string{
		"fp-app-id":     c.appID,
		"fp-app-secret": c.secret,
	}, nil
}
```

`sdk/imgateway.go`：

```go
package fpsdk

// WithAppID 把 app 作用域附在这一次调用上。
//
// 只有 CallerType 为 CallerTypeIM 的客户端需要它：那种客户端的连接凭据里
// 没有 fp-app-id，作用域来自每个 client 的 ws 握手帧，一条连接上会连着为
// 不同的应用发起调用。
//
// metadata 里的 app-id 之所以看起来"钉死在连接上"，只是因为普通客户端把它
// 放进了 PerRPCCredentials，不代表不能逐调用带。
func WithAppID(ctx context.Context, appID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "fp-app-id", appID)
}

// IMGateway 是 fp-im 网关专用的那组接口。
type IMGateway struct{ c *Client }

// AppIMConfig 是一个应用的 IM 接入配置。
type AppIMConfig struct {
	AllowGuest  bool
	ConnPolicy  string
	ConnLimit   int32
	GuestIPRate int32
	// BizAuth 为 nil 表示这个应用不支持业务方令牌。
	BizAuth *AppIMBizAuth
}

type AppIMBizAuth struct {
	VerifyURL string
	TimeoutMs int32
	CacheSize int32
}

// GetAppIMConfig 拉取 appID 那个应用的 IM 接入配置。
//
// 应用不存在、被停用、或没打开 IM 时返回 ErrUnavailable 之外的明确错误，
// 调用方据此给 client 关闭码 4002 而不是 4001。
func (g *IMGateway) GetAppIMConfig(ctx context.Context, appID string) (*AppIMConfig, error)

// VerifyAppCredential 核实 secret 是否为 appID 那个应用的 appSecret。
func (g *IMGateway) VerifyAppCredential(ctx context.Context, appID, secret string) error
```

两个方法体照 `sdk/auth.go` 里既有 RPC 包装的写法：`WithAppID` 附作用域、调 stub、
`translate(err)` 映射哨兵错误。`Client` 加 `imRPC fpv1.IMGatewayServiceClient` 与
`imGateway *IMGateway`，在 `New` 里与 `cfgRPC` 同处构造。

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./sdk -v
```

预期：全绿，包括既有测试——`CallerType` 为空时行为一字未变。

- [ ] **Step 5: 变异验证**

把 `GetRequestMetadata` 的 im 分支改成也输出 `fp-app-id`，确认
`TestIMCredentialsOmitAppID` 变红。改回来。

- [ ] **Step 6: 提交**

```bash
git add sdk/options.go sdk/options_test.go sdk/client.go sdk/imgateway.go sdk/imgateway_test.go
git commit -m "feat(sdk): CallerType 与 IMGateway 客户端"
```

---

## Task 11: 控制台后端

**Files:**
- Modify: `internal/httpapi/application.go`, `internal/httpapi/router.go`
- Create: `internal/httpapi/imcred.go`, `internal/httpapi/imcred_test.go`
- Test: `internal/httpapi/application_test.go`

**Interfaces:**
- Consumes: Task 2 的 `SetIMConfig`、Task 3 的 `IMCredentialService`
- Produces:
  - `PUT /admin/api/applications/{id}/im` —— 保存 IM 配置
  - `GET /admin/api/im-credential` —— `{"exists": bool, "updatedAt": int64}`
  - `POST /admin/api/im-credential/rotate` —— `{"secret": "..."}`，仅此一次可见
  - 应用详情 DTO 里多一个 `im` 对象

- [ ] **Step 1: 写失败的测试**

追加到 `internal/httpapi/application_test.go`：

```go
// TestPutIMConfigValidatesBeforeSaving：保存时就校验，别留到 fp-im 解析时
// 才报——那时报错的是错的人（运维在控制台点了保存、以为成功了，故障却在
// 网关那边冒出来）。
func TestPutIMConfigValidatesBeforeSaving(t *testing.T) {
	env := newAdminEnv(t)
	app := env.createApp(t, "im-http")

	body := `{"enabled":true,"connPolicy":"replace","connLimit":5,"allowGuest":false,"guestIpRate":20,
		"bizAuth":{"verifyUrl":"http://insecure/v","timeoutMs":2000,"cacheSize":10}}`
	res := env.do(t, "PUT", "/admin/api/applications/"+app.ID.String()+"/im", body)
	if res.Code != 400 {
		t.Fatalf("明文 http 的 verify_url 必须 400，got %d：%s", res.Code, res.Body)
	}
}

// TestPutIMConfigRejectsUnknownField：与仓库既有纪律一致，DTO 字段名拼错
// 是 400 而不是静默丢弃。
func TestPutIMConfigRejectsUnknownField(t *testing.T) {
	env := newAdminEnv(t)
	app := env.createApp(t, "im-http-2")
	res := env.do(t, "PUT", "/admin/api/applications/"+app.ID.String()+"/im",
		`{"enabled":true,"connPolicyy":"replace"}`)
	if res.Code != 400 {
		t.Fatalf("未知字段必须 400，got %d", res.Code)
	}
}
```

`internal/httpapi/imcred_test.go`：

```go
// TestRotateIMCredentialReturnsSecretOnce：明文只在轮换时返回一次，
// 之后任何读接口都拿不回来——与应用的 appSecret 同一纪律。
func TestRotateIMCredentialReturnsSecretOnce(t *testing.T) {
	env := newAdminEnv(t)

	res := env.do(t, "GET", "/admin/api/im-credential", "")
	if res.Code != 200 || !strings.Contains(res.Body, `"exists":false`) {
		t.Fatalf("初始状态应为 exists:false，got %d %s", res.Code, res.Body)
	}

	res = env.do(t, "POST", "/admin/api/im-credential/rotate", "")
	if res.Code != 200 {
		t.Fatalf("轮换失败：%d %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body, `"secret"`) {
		t.Fatalf("轮换必须返回明文 secret：%s", res.Body)
	}

	res = env.do(t, "GET", "/admin/api/im-credential", "")
	if strings.Contains(res.Body, `"secret"`) {
		t.Fatalf("读接口绝不能回显 secret：%s", res.Body)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/httpapi -run 'TestPutIMConfig|TestRotateIMCredential'
```

预期：404 或路由未定义。

- [ ] **Step 3: 实现**

按 `internal/httpapi/application.go` 里既有 handler 的形状写：`decodeJSON` 解请求
（它已经开了 `DisallowUnknownFields`）、转成 `domain.IMConfig`、调 `SetIMConfig`、
`writeJSON` 回应用 DTO。应用 DTO 加 `im` 对象（字段名用 camelCase，与既有 DTO 一致）。

`internal/httpapi/imcred.go` 两个 handler：`GET` 调 `Exists`，`POST /rotate` 调
`Rotate` 并把明文放进响应。**读接口绝不回显 secret**——库里只有哈希，回显也无从谈起，
但 DTO 里不要留这个字段，免得以后有人往里塞。

`router.go` 在 `requireAdmin` 组内挂上三条路由。

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/httpapi -v
```

预期：全绿。

- [ ] **Step 5: 提交**

```bash
git add internal/httpapi/application.go internal/httpapi/application_test.go internal/httpapi/imcred.go internal/httpapi/imcred_test.go internal/httpapi/router.go
git commit -m "feat(httpapi): 应用 IM 配置与 IM 凭据的控制台接口"
```

---

## Task 12: 控制台前端

**Files:**
- Create: `web/src/components/ApplicationIMSettings.tsx`, `web/src/components/ApplicationIMSettings.test.tsx`
- Modify: `web/src/pages/Applications.tsx`, `web/src/lib/types.ts`

> **开始前先确认**：这个分支上带着一批未提交的前端改动（`ApplicationSettings.tsx`、
> `Applications.tsx` 都在其中）。先跟人确认那批改动的状态，别把它们卷进提交，也别
> 在一个正在被改的文件上叠加。

- [ ] **Step 1: 写失败的测试**

`ApplicationIMSettings.test.tsx` 覆盖三条：

1. `enabled` 关着时其余字段折叠不可见——避免"没打开却填了一堆"的误导
2. `connPolicy` 选 `limit` 时 `connLimit` 才出现
3. 保存时 `verifyUrl` 填 `http://` 前端就拦下，不发请求

- [ ] **Step 2: 跑测试确认失败**

```bash
cd web && npm test -- ApplicationIMSettings
```

- [ ] **Step 3: 实现**

按 `ApplicationSettings.tsx` 的既有形状写：一个折叠区，顶上一个 `enabled` 开关，
打开后展开 `connPolicy` / `connLimit` / `allowGuest` / `guestIpRate` 与一组可整块
启用/关闭的 `bizAuth`。保存调 `PUT /admin/api/applications/{id}/im`。

IM 凭据的展示与轮换放在同一页的独立卡片：显示"已生成 / 未生成"与更新时间，
一个「重新生成」按钮，生成后把明文显示在一次性的对话框里并提示"只显示这一次"。

- [ ] **Step 4: 跑测试与构建**

```bash
cd web && npm test && npm run build
```

- [ ] **Step 5: 提交**

```bash
git add web/src/components/ApplicationIMSettings.tsx web/src/components/ApplicationIMSettings.test.tsx web/src/pages/Applications.tsx web/src/lib/types.ts
git commit -m "feat(web): 应用的 IM 接入配置与 IM 凭据面板"
```

---

## Task 13: 第一阶段收尾验证

**Files:**
- Create: `internal/integration/im_gateway_test.go`
- Modify: `docs/console.md`

- [ ] **Step 1: 写端到端测试**

`internal/integration/im_gateway_test.go`，跑在真实 gRPC 监听上：

```go
// TestIMGatewayEndToEnd 走完整链路：生成 IM 凭据 → 用它建一个 CallerType=im
// 的 SDK 客户端 → 对两个不同应用逐调用切换 app 作用域 → 各自拿到正确结果。
//
// 第九条测试要点：同一条连接上连着为两个不同应用验 token，且 A 的 token
// 拿到 B 的应用上验必须失败——那条 sess.AppID != app.ID 的检查仍然在 fp 侧
// 执行，这个方案一寸没挪它。
func TestIMGatewayEndToEnd(t *testing.T) { /* … */ }

// TestIMGatewayCannotLogin 在真实 gRPC 上再钉一次爆炸半径边界。
func TestIMGatewayCannotLogin(t *testing.T) { /* … */ }

// TestNormalSDKUnaffected 钉住零现网影响：不带 CallerType 的客户端，
// 登录、校验、Watch、配置四条路径全部与改动前一致。
func TestNormalSDKUnaffected(t *testing.T) { /* … */ }
```

- [ ] **Step 2: 跑全量**

```bash
gofmt -l cmd internal sdk && go vet ./... && ./scripts/test.sh
cd web && npm test
```

预期：全绿。

- [ ] **Step 3: 文档**

`docs/console.md` 加一节"IM 接入"：怎么生成 IM 凭据、怎么给应用打开 `im_enabled`、
各参数的含义与约束、以及**轮换 IM 凭据后旧凭据最多再活 10 秒**这条运维性质。

- [ ] **Step 4: 提交**

```bash
git add internal/integration/im_gateway_test.go docs/console.md
git commit -m "test(integration): IM 网关端到端；docs: 控制台 IM 接入说明"
```

---

## 第一阶段完成标准

- [ ] `./scripts/test.sh` 全绿，`cd web && npm test` 全绿
- [ ] 不带 `fp-caller-type` 的调用行为与改动前逐字相同（Task 4/13 各有测试钉住）
- [ ] 所有现存应用的 `im_enabled` 为 `false`，新 RPC 无人调用 —— **可以先于 fp-im 单独发布**
- [ ] 第二阶段（fp-im 侧）的计划见 `2026-09-09-fp-im-app-provisioning-im.md`
