# fp 系统配置迁入配置中心 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `POSTGRES_URL`/`REDIS_URL` 两个环境变量成为 fp 启动的唯一强制项，其余启动配置（`env`、监听地址、首次管理员、阿里云短信）挪进数据库里一张有版本历史的「系统配置」表，通过控制台新增的页面维护，保存后重启生效。

**Architecture:** 后端新增 `system_config` 表 + `SystemConfigService`（`ConfigService` 去掉 `application_id`/`type`/`push` 三个维度后的形状）+ 一组 `/admin/api/system-config*` 路由；`internal/config.Config` 去掉 `Postgres`/`Redis` 字段，`Load(path)` 改名 `Parse(yamlText)` 直接吃系统配置表里的 YAML；`cmd/fp/main.go` 启动顺序改成"环境变量 → 连库 → 读系统配置 → 装配其余服务"。前端新增「系统配置」+「系统配置版本历史」两个页面，复用配置中心页面抽出来的 YAML 编辑器 hook。

**Tech Stack:** Go（pgx/v5、chi、yaml.v3）、React + TypeScript（vitest、@testing-library/react）、PostgreSQL 18（goose 迁移）。

## Global Constraints

- 系统配置的 YAML 处理规则（顶层必须是映射、冒号漏空格自动补全、整版快照不做时态存储）与业务方配置中心完全一致，直接复用 `domain.ParseConfigYAML` / `domain.NormalizeConfigYAML`，不新写一套。
- 系统配置没有 `push`：只在进程启动时读一次，不广播 Redis。
- `bootstrap_admin` 两项都为空时落到内置默认值 `admin`/`admin`（不再是"跳过创建"）——首次启动系统配置表必为空，需要有账号能登进控制台。
- 每个新增/改动的 Go 文件里的注释风格、错误信息措辞、测试断言风格，照抄本仓库已有的 `internal/service/config.go`、`internal/httpapi/config.go`、`internal/config/config.go` 那一套，不引入新风格。
- 不删除本机可能已存在、未受版本控制的 `D:\fp\config.yaml`——它此后只是一个不再被读取的死文件，交给用户自己按需清理；本计划只删仓库里被 git 追踪的 `config.example.yaml`。
- 参考设计文档：`docs/superpowers/specs/2026-09-21-fp-system-config-design.md`。

---

### Task 1: `internal/config` 改造——环境变量必填项 + Load 改 Parse

**Files:**
- Create: `internal/config/env.go`
- Create: `internal/config/env_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`（整份重写）

**Interfaces:**
- Produces: `config.RequireEnv(name string) (string, error)`；`config.Parse(yamlText string) (*Config, error)`；`Config` 结构体去掉 `Postgres`、`Redis`、`Endpoint` 三者（`Endpoint` 类型整个删除，删除字段后它没有别的使用方）。`DefaultPath` 常量删除。

- [ ] **Step 1: 写 `RequireEnv` 的失败测试**

`internal/config/env_test.go`：

```go
package config

import (
	"strings"
	"testing"
)

func TestRequireEnvReturnsValue(t *testing.T) {
	t.Setenv("FP_TEST_REQUIRE_ENV", "postgres://x/y")
	v, err := RequireEnv("FP_TEST_REQUIRE_ENV")
	if err != nil {
		t.Fatalf("RequireEnv() error = %v", err)
	}
	if v != "postgres://x/y" {
		t.Errorf("v = %q, want postgres://x/y", v)
	}
}

func TestRequireEnvErrorsWhenMissing(t *testing.T) {
	t.Setenv("FP_TEST_REQUIRE_ENV_MISSING", "")
	_, err := RequireEnv("FP_TEST_REQUIRE_ENV_MISSING")
	if err == nil {
		t.Fatal("环境变量为空必须报错")
	}
	if !strings.Contains(err.Error(), "FP_TEST_REQUIRE_ENV_MISSING") {
		t.Errorf("错误信息 %q 里应当出现变量名", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd internal/config && go test ./... -run TestRequireEnv -v`
Expected: 编译失败（`RequireEnv` 未定义）

- [ ] **Step 3: 实现 `RequireEnv`**

`internal/config/env.go`：

```go
package config

import (
	"fmt"
	"os"
)

// RequireEnv 读取 name 指向的环境变量，为空时返回一个点明是哪个变量缺失
// 的错误。postgres/redis 连接串必须来自环境变量而不是这个包解析的
// YAML——读系统配置表本身就要先连上数据库，见
// docs/superpowers/specs/2026-09-21-fp-system-config-design.md 第三节。
func RequireEnv(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("config: 环境变量 %s 未设置", name)
	}
	return v, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd internal/config && go test ./... -run TestRequireEnv -v`
Expected: PASS

- [ ] **Step 5: 写 `Parse` 的失败测试（整份重写 config_test.go）**

`internal/config/config_test.go`：

```go
package config

import (
	"strings"
	"testing"
)

// TestParseReadsEveryField 逐字段断言，是 yaml tag 的护栏：漏写 tag 的
// 多词字段会被 yaml.v3 按"字段名整个小写"的默认规则映射错位，
// KnownFields(true) 会把它报成"未知键"，只有逐字段断言才抓得住。
func TestParseReadsEveryField(t *testing.T) {
	cfg, err := Parse(`
env: prod
log:
  level: debug
http:
  addr: ":18080"
grpc:
  addr: ":19090"
bootstrap_admin:
  user: root
  password: s3cret
sms:
  aliyun:
    access_key_id: ak
    access_key_secret: sk
    sign_name: 测试签名
    template_login_code: SMS_0001
    endpoint: dysmsapi.example.com
`)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"env", cfg.Env, "prod"},
		{"log.level", cfg.Log.Level, "debug"},
		{"http.addr", cfg.HTTP.Addr, ":18080"},
		{"grpc.addr", cfg.GRPC.Addr, ":19090"},
		{"bootstrap_admin.user", cfg.BootstrapAdmin.User, "root"},
		{"bootstrap_admin.password", cfg.BootstrapAdmin.Password, "s3cret"},
		{"sms.aliyun.access_key_id", cfg.SMS.Aliyun.AccessKeyID, "ak"},
		{"sms.aliyun.access_key_secret", cfg.SMS.Aliyun.AccessKeySecret, "sk"},
		{"sms.aliyun.sign_name", cfg.SMS.Aliyun.SignName, "测试签名"},
		{"sms.aliyun.template_login_code", cfg.SMS.Aliyun.TemplateLoginCode, "SMS_0001"},
		{"sms.aliyun.endpoint", cfg.SMS.Aliyun.Endpoint, "dysmsapi.example.com"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestParseEmptyTextUsesAllDefaults 钉住"系统配置表还没有任何版本"这个
// 首次启动的正常状态：空文本不是错误，全部用零值默认。
func TestParseEmptyTextUsesAllDefaults(t *testing.T) {
	cfg, err := Parse("")
	if err != nil {
		t.Fatalf(`Parse("") error = %v`, err)
	}
	if cfg.Env != "dev" {
		t.Errorf("Env = %q, want dev", cfg.Env)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want info", cfg.Log.Level)
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("HTTP.Addr = %q, want :8080", cfg.HTTP.Addr)
	}
	if cfg.GRPC.Addr != ":9090" {
		t.Errorf("GRPC.Addr = %q, want :9090", cfg.GRPC.Addr)
	}
}

// TestParseRejectsUnknownField 是从文件迁到数据库依然保留的能力：拼错的
// 键当场报错，不静默回落到默认值。
func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse("log:\n  lvel: debug\n")
	if err == nil {
		t.Fatal("拼错的键必须报错，不能静默用默认值")
	}
	if !strings.Contains(err.Error(), "lvel") {
		t.Errorf("错误信息 %q 里应当出现拼错的那个键名 lvel", err)
	}
}

// TestIsProd 钉住大小写不敏感与默认值。IsProd 决定管理端 cookie 带不带
// Secure，是个真正有安全后果的判定。
func TestIsProd(t *testing.T) {
	for _, tt := range []struct {
		env  string
		want bool
	}{
		{"prod", true},
		{"PROD", true},
		{"Prod", true},
		{"dev", false},
		{"", false},
		{"production", false}, // 只认 prod，不做前缀匹配
	} {
		if got := (&Config{Env: tt.env}).IsProd(); got != tt.want {
			t.Errorf("Env=%q IsProd() = %v, want %v", tt.env, got, tt.want)
		}
	}
}

func TestParseMissingAliyunRequiredInProd(t *testing.T) {
	_, err := Parse(`
env: prod
sms:
  aliyun:
    access_key_secret: sk
    sign_name: 签名
    template_login_code: SMS_0001
`)
	if err == nil {
		t.Fatal("生产环境缺 sms.aliyun.access_key_id 必须报错")
	}
	if !strings.Contains(err.Error(), "sms.aliyun.access_key_id") {
		t.Errorf("错误信息 %q 里应当出现 sms.aliyun.access_key_id", err)
	}
}

func TestParseAllowsMissingAliyunOutsideProd(t *testing.T) {
	cfg, err := Parse("")
	if err != nil {
		t.Fatalf("Parse() error = %v，非生产环境阿里云凭据允许留空", err)
	}
	if cfg.SMS.Aliyun.AccessKeyID != "" {
		t.Errorf("SMS.Aliyun.AccessKeyID = %q, want empty", cfg.SMS.Aliyun.AccessKeyID)
	}
}
```

- [ ] **Step 6: 跑测试确认失败**

Run: `cd internal/config && go test ./... -v`
Expected: 编译失败（`Parse` 未定义，`Load` 仍在但测试已不再调用它）

- [ ] **Step 7: 重写 `internal/config/config.go`**

```go
// Package config 解析 fp 的系统配置——存在数据库 system_config 表里的
// YAML 原文，字段树与 YAML 的字段树 1:1 同构，中间没有映射层——加了字段
// 却忘了映射是这类加载器最常见的漏，同构就没有这个漏可犯。代价是每个
// 字段都必须显式写 yaml tag：yaml.v3 的默认规则是把字段名整个小写，
// BootstrapAdmin 会变成 bootstrapadmin 而不是 bootstrap_admin。
package config

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 是 fp 进程的系统配置（不含 postgres/redis——那两项必须来自
// 环境变量，读系统配置本身就要先连上数据库，见
// docs/superpowers/specs/2026-09-21-fp-system-config-design.md 第三节）。
type Config struct {
	Env            string         `yaml:"env"` // dev / prod，大小写不敏感
	Log            Log            `yaml:"log"`
	HTTP           Listen         `yaml:"http"` // 管理控制台
	GRPC           Listen         `yaml:"grpc"` // SDK 接入
	BootstrapAdmin BootstrapAdmin `yaml:"bootstrap_admin"`
	SMS            SMS            `yaml:"sms"`
}

type Log struct {
	Level string `yaml:"level"` // debug / info / warn / error
}

type Listen struct {
	Addr string `yaml:"addr"`
}

// BootstrapAdmin 是首次启动时创建的平台管理员。两项都填才生效——
// AdminService.EnsureBootstrap 在任一为空时直接跳过；cmd/fp/main.go 在
// 两项都为空时会落到内置默认值 admin/admin，见该文件里的
// resolveBootstrapAdmin。
type BootstrapAdmin struct {
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

type SMS struct {
	Aliyun Aliyun `yaml:"aliyun"`
}

// Aliyun 是阿里云短信供应商的配置（见 cmd/fp/main.go）。
//
// 只在生产环境（IsProd）强制要求前四项非空——非生产环境缺任一项时，
// cmd/fp/main.go 会退化成 notify.NewLoggingFakeProvider 并打一条醒目的
// WARN——不静默，只是不强制。
//
// Endpoint 任何环境下都可以留空，notify.NewAliyunSMS 会套用它自己的默认
// 接入点，不参与必填校验。
type Aliyun struct {
	AccessKeyID       string `yaml:"access_key_id"`
	AccessKeySecret   string `yaml:"access_key_secret"`
	SignName          string `yaml:"sign_name"`
	TemplateLoginCode string `yaml:"template_login_code"` // 映射 service.LoginCodeTemplate 到阿里云侧模板 ID
	Endpoint          string `yaml:"endpoint"`
}

// IsProd 报告当前是否为生产环境。
func (c *Config) IsProd() bool { return strings.EqualFold(c.Env, "prod") }

// Parse 解析系统配置表里存的 YAML 原文，填默认值并校验必填项。yamlText
// 为空（系统配置表还没有任何版本、或者存过一份空文本）按"什么都没覆盖"
// 处理，返回全部默认值——不是错误：首次启动系统配置表必然是空的，这是
// 正常状态。
func Parse(yamlText string) (*Config, error) {
	// 先填默认值再解码：yaml.v3 只写文档里出现过的字段，没出现的原样
	// 保留，于是"默认值"这件事不需要任何额外的 applyDefaults 逻辑。
	c := &Config{
		Env:  "dev",
		Log:  Log{Level: "info"},
		HTTP: Listen{Addr: ":8080"},
		GRPC: Listen{Addr: ":9090"},
	}

	dec := yaml.NewDecoder(strings.NewReader(yamlText))
	// 拼错的键当场报错，不静默回落到默认值——与 internal/httpapi 的
	// decodeJSON 开 DisallowUnknownFields 同一条纪律。
	dec.KnownFields(true)
	// 空文本解码返回 io.EOF，不是错误——它只是"什么都没覆盖"。
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: 解析系统配置: %w", err)
	}

	var missing []string
	// 阿里云短信凭据只在生产环境强制必填，理由见 Aliyun 上方的注释。
	if c.IsProd() {
		for _, kv := range []struct{ path, v string }{
			{"sms.aliyun.access_key_id", c.SMS.Aliyun.AccessKeyID},
			{"sms.aliyun.access_key_secret", c.SMS.Aliyun.AccessKeySecret},
			{"sms.aliyun.sign_name", c.SMS.Aliyun.SignName},
			{"sms.aliyun.template_login_code", c.SMS.Aliyun.TemplateLoginCode},
		} {
			if kv.v == "" {
				missing = append(missing, kv.path)
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: 系统配置缺少必填项 %s", strings.Join(missing, ", "))
	}
	return c, nil
}
```

- [ ] **Step 8: 跑测试确认通过**

Run: `cd internal/config && go test ./... -v`
Expected: PASS（包括 Step 1 的 `RequireEnv` 用例）

- [ ] **Step 9: 提交**

```bash
git add internal/config/env.go internal/config/env_test.go internal/config/config.go internal/config/config_test.go
git commit -m "$(cat <<'EOF'
refactor(config): 启动配置改从系统配置表解析，postgres/redis 走环境变量

Load(path) 改名 Parse(yamlText)，不再打开文件；Config 去掉 Postgres/
Redis 字段，两者从此只能来自 POSTGRES_URL/REDIS_URL 环境变量（新增
RequireEnv）。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `system_config` 表 + `SystemConfigService`

**Files:**
- Create: `internal/store/migrations/00014_system_config.sql`
- Create: `internal/domain/system_config.go`
- Create: `internal/service/system_config.go`
- Create: `internal/service/system_config_test.go`

**Interfaces:**
- Consumes: `domain.Fail`/`domain.Failf`/`ErrNotFound`/`ErrConflict`（`internal/domain/error.go`）；`domain.CodeConfigVersionNotFound`、`domain.CodeConfigVersionConflict`（`internal/domain/codes.go`，两个码都是分区无关的通用码，直接复用不新增）；`domain.ParseConfigYAML`/`domain.NormalizeConfigYAML`（`internal/domain/config.go`）；`pgUniqueViolation`（`internal/service/application.go:22`，同包常量）；`testsupport.NewTestDB`（`internal/testsupport/db.go`）。
- Produces: `domain.SystemConfig{Seq int64, Value string, CreatedAt int64}`；`service.SystemConfigMaxVersions = 100`；`service.NewSystemConfigService(pool *pgxpool.Pool) *SystemConfigService`；方法 `Current(ctx) (domain.SystemConfig, error)`、`Version(ctx, seq int64) (domain.SystemConfig, error)`、`ListVersions(ctx, limit int) ([]domain.SystemConfig, error)`、`Save(ctx, value string) (int64, error)`、`Rollback(ctx, seq int64) (int64, error)`。

- [ ] **Step 1: 建迁移**

`internal/store/migrations/00014_system_config.sql`：

```sql
-- +goose Up
-- fp 自身启动配置的版本快照：一次保存一行，Value 是 YAML 原文，原样
-- 存储——与 config 表同一条"整版快照"纪律（见
-- docs/superpowers/specs/2026-09-05-fp-config-center-design.md 第三节）。
-- 没有 application_id / type：fp 自己只有一份系统配置，不需要这两个维度。
CREATE TABLE system_config (
    seq        bigint      NOT NULL PRIMARY KEY,
    value      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE system_config;
```

- [ ] **Step 2: domain 类型**

`internal/domain/system_config.go`：

```go
package domain

// SystemConfig 是 fp 自身启动配置的一个版本快照。Value 是管理端提交的
// YAML 原文，原样存储——语义与 Config.Value 相同，见
// internal/domain/config.go 顶部注释。
type SystemConfig struct {
	Seq       int64
	Value     string
	CreatedAt int64
}
```

- [ ] **Step 3: 写 `SystemConfigService` 的失败测试**

`internal/service/system_config_test.go`：

```go
package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func newSystemConfigFixture(t *testing.T) *service.SystemConfigService {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	return service.NewSystemConfigService(pool)
}

func TestSystemConfigCurrentReturnsEmptyWhenNoVersion(t *testing.T) {
	svc := newSystemConfigFixture(t)
	got, err := svc.Current(context.Background())
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if got.Seq != 0 {
		t.Fatalf("Seq = %d，期望 0", got.Seq)
	}
	if got.Value != "" {
		t.Fatalf("Value = %q，期望空字符串", got.Value)
	}
}

func TestSystemConfigVersionNotFound(t *testing.T) {
	svc := newSystemConfigFixture(t)
	_, err := svc.Version(context.Background(), 7)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，期望 domain.ErrNotFound", err)
	}
}

func TestSystemConfigSaveIncrementsSeqAndIsFullReplacement(t *testing.T) {
	svc := newSystemConfigFixture(t)
	ctx := context.Background()

	seq1, err := svc.Save(ctx, "a: 1\nb: 2\n")
	if err != nil {
		t.Fatalf("首次保存: %v", err)
	}
	if seq1 != 1 {
		t.Fatalf("seq1 = %d，期望 1", seq1)
	}

	seq2, err := svc.Save(ctx, "a: 1\n")
	if err != nil {
		t.Fatalf("二次保存: %v", err)
	}
	if seq2 != 2 {
		t.Fatalf("seq2 = %d，期望 2", seq2)
	}

	cur, err := svc.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur.Value != "a: 1\n" {
		t.Fatalf("当前值 = %q，期望 %q（全量替换，b 应当消失）", cur.Value, "a: 1\n")
	}
}

func TestSystemConfigSaveRejectsInvalidYAML(t *testing.T) {
	svc := newSystemConfigFixture(t)
	_, err := svc.Save(context.Background(), "- a\n- b\n")
	if err == nil {
		t.Fatal("顶层不是映射应当被拒")
	}
}

func TestSystemConfigRollback(t *testing.T) {
	svc := newSystemConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, "a: 1\n"); err != nil {
		t.Fatalf("v1: %v", err)
	}
	if _, err := svc.Save(ctx, "a: 2\n"); err != nil {
		t.Fatalf("v2: %v", err)
	}

	newSeq, err := svc.Rollback(ctx, 1)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if newSeq != 3 {
		t.Fatalf("回滚后 seq = %d，期望 3（回滚是往前追加，不是往回删）", newSeq)
	}

	cur, err := svc.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur.Value != "a: 1\n" {
		t.Fatalf("回滚后值 = %q，期望 %q", cur.Value, "a: 1\n")
	}
}

// 回滚到内容与当前完全一致的版本不该凭空多出一个版本号。
func TestSystemConfigRollbackToSameContentProducesNoNewVersion(t *testing.T) {
	svc := newSystemConfigFixture(t)
	ctx := context.Background()

	seq1, err := svc.Save(ctx, "a: 1\n")
	if err != nil {
		t.Fatalf("v1: %v", err)
	}

	newSeq, err := svc.Rollback(ctx, seq1)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if newSeq != seq1 {
		t.Fatalf("newSeq = %d，期望原样返回 %d（内容没变，不该产生新版本）", newSeq, seq1)
	}
}

func TestSystemConfigListVersionsDescending(t *testing.T) {
	svc := newSystemConfigFixture(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if _, err := svc.Save(ctx, fmt.Sprintf("n: %d\n", i)); err != nil {
			t.Fatalf("保存第 %d 版: %v", i, err)
		}
	}

	vs, err := svc.ListVersions(ctx, 20)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("版本数 = %d，期望 3", len(vs))
	}
	if vs[0].Seq != 3 {
		t.Fatalf("第一条 seq = %d，期望 3（降序）", vs[0].Seq)
	}
}

// 超过 SystemConfigMaxVersions 版之后，最老的会被修剪掉。
func TestSystemConfigPrunesOldVersions(t *testing.T) {
	svc := newSystemConfigFixture(t)
	ctx := context.Background()

	for i := 1; i <= service.SystemConfigMaxVersions+5; i++ {
		if _, err := svc.Save(ctx, fmt.Sprintf("n: %d\n", i)); err != nil {
			t.Fatalf("保存第 %d 版: %v", i, err)
		}
	}

	if _, err := svc.Version(ctx, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("第 1 版应该已被修剪掉，err = %v", err)
	}
	if _, err := svc.Version(ctx, 6); err != nil {
		t.Fatalf("第 6 版不该被修剪: %v", err)
	}
}
```

- [ ] **Step 4: 跑测试确认失败**

Run: `cd internal/service && go test ./... -run TestSystemConfig -v`
Expected: 编译失败（`service.NewSystemConfigService` 未定义）

- [ ] **Step 5: 实现 `SystemConfigService`**

`internal/service/system_config.go`：

```go
// Package service 的系统配置部分：fp 自身启动配置的版本化存取，形状是
// ConfigService 去掉 application_id/type/push 三个维度之后的样子——系统
// 配置只有 fp 自己一个消费者，不需要分区；只在进程启动时读一次，不广播。
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
)

// SystemConfigMaxVersions 是保留的版本数上限，超出的从最老的开始删。
// 与 ConfigMaxVersions 同一条纪律，各自独立维护：两张表的主键结构不同
// （(application_id, type, seq) vs (seq)），修剪 SQL 本来就不是一份。
const SystemConfigMaxVersions = 100

// SystemConfigService 管理 fp 自身启动配置的版本快照。
type SystemConfigService struct {
	pool *pgxpool.Pool
}

// NewSystemConfigService 构造 SystemConfigService。
func NewSystemConfigService(pool *pgxpool.Pool) *SystemConfigService {
	return &SystemConfigService{pool: pool}
}

// Current 返回当前版本。一个版本都没有时返回 Seq=0、空 Value，不是
// 错误——首次启动前系统配置表必然是空的，这是正常状态。
func (s *SystemConfigService) Current(ctx context.Context) (domain.SystemConfig, error) {
	seq, value, createdAt, err := s.scanOne(ctx, `
		SELECT seq, value, (extract(epoch FROM created_at) * 1000)::bigint
		FROM system_config ORDER BY seq DESC LIMIT 1`)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SystemConfig{Seq: 0, Value: ""}, nil
	}
	if err != nil {
		return domain.SystemConfig{}, err
	}
	return domain.SystemConfig{Seq: seq, Value: value, CreatedAt: createdAt}, nil
}

// Version 返回指定版本。不存在返回 domain.ErrNotFound 的包装。
func (s *SystemConfigService) Version(ctx context.Context, seq int64) (domain.SystemConfig, error) {
	retSeq, value, createdAt, err := s.scanOne(ctx, `
		SELECT seq, value, (extract(epoch FROM created_at) * 1000)::bigint
		FROM system_config WHERE seq = $1`, seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SystemConfig{}, domain.Fail(domain.ErrNotFound, domain.CodeConfigVersionNotFound, "配置版本不存在").
			WithDesc("系统配置没有第 %d 版", seq)
	}
	if err != nil {
		return domain.SystemConfig{}, err
	}
	return domain.SystemConfig{Seq: retSeq, Value: value, CreatedAt: createdAt}, nil
}

func (s *SystemConfigService) scanOne(ctx context.Context, sql string, args ...any) (int64, string, int64, error) {
	var (
		seq       int64
		value     string
		createdAt int64
	)
	if err := s.pool.QueryRow(ctx, sql, args...).Scan(&seq, &value, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", 0, err
		}
		return 0, "", 0, fmt.Errorf("service: 查询系统配置: %w", err)
	}
	return seq, value, createdAt, nil
}

// ListVersions 返回最近 limit 个版本的元信息。元素的 Value 恒为空字符串——
// 列表页只需要 seq 与时间。
func (s *SystemConfigService) ListVersions(ctx context.Context, limit int) ([]domain.SystemConfig, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT seq, (extract(epoch FROM created_at) * 1000)::bigint
		FROM system_config ORDER BY seq DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("service: 查询系统配置版本列表: %w", err)
	}
	defer rows.Close()

	out := make([]domain.SystemConfig, 0, limit)
	for rows.Next() {
		var c domain.SystemConfig
		if err := rows.Scan(&c.Seq, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("service: 扫描系统配置版本: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历系统配置版本: %w", err)
	}
	return out, nil
}

// Save 把 value（管理端提交的 YAML 原文）存成一个新版本，返回新版本号。
// 全量替换语义、NormalizeConfigYAML/ParseConfigYAML 的校验规则都与
// ConfigService.Save 一致，见其注释。没有 push 参数——系统配置只在进程
// 启动时读一次，不广播。
func (s *SystemConfigService) Save(ctx context.Context, value string) (int64, error) {
	value = domain.NormalizeConfigYAML(value)
	if _, err := domain.ParseConfigYAML(value); err != nil {
		return 0, err
	}

	var seq int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(seq), 0) + 1 FROM system_config`).Scan(&seq); err != nil {
			return fmt.Errorf("service: 取下一个系统配置版本号: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO system_config (seq, value) VALUES ($1, $2)`, seq, value); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
				return domain.Failf(domain.ErrConflict, domain.CodeConfigVersionConflict, "配置版本冲突，请重试")
			}
			return fmt.Errorf("service: 写入系统配置版本: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM system_config WHERE seq <= $1`, seq-SystemConfigMaxVersions); err != nil {
			return fmt.Errorf("service: 修剪系统配置版本: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// Rollback 把 seq 那一版的 value 复制成一个新版本，返回新版本号。与目标
// 版本内容完全一致时不产生新版本，原样返回当前版本号——理由与
// ConfigService.Rollback 相同：回滚本身也该是可回滚、可审计的一次
// "保存"，但内容没变就不该凭空多出一个版本号。
func (s *SystemConfigService) Rollback(ctx context.Context, seq int64) (int64, error) {
	old, err := s.Version(ctx, seq)
	if err != nil {
		return 0, err
	}
	current, err := s.Current(ctx)
	if err != nil {
		return 0, err
	}
	if current.Value == old.Value {
		return current.Seq, nil
	}
	return s.Save(ctx, old.Value)
}
```

- [ ] **Step 6: 跑测试确认通过**

Run: `cd internal/service && go test ./... -run TestSystemConfig -v`
Expected: PASS（这条会真的连测试库跑 105 次 Save，耐心等几秒）

- [ ] **Step 7: 提交**

```bash
git add internal/store/migrations/00014_system_config.sql internal/domain/system_config.go internal/service/system_config.go internal/service/system_config_test.go
git commit -m "$(cat <<'EOF'
feat(config): 新增 system_config 表与 SystemConfigService

fp 自身启动配置的版本化存取，形状是 ConfigService 去掉
application_id/type/push 三个维度之后的样子——不广播，只在进程启动时
读一次。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `/admin/api/system-config*` HTTP 路由

**Files:**
- Create: `internal/httpapi/system_config.go`
- Create: `internal/httpapi/system_config_test.go`
- Modify: `internal/httpapi/router.go`
- Modify: `internal/httpapi/env_test.go`

**Interfaces:**
- Consumes: `service.SystemConfigService`（Task 2）；`writeJSON`/`writeError`/`decodeJSON`（`internal/httpapi/respond.go`）；`domain.Fail`/`domain.ErrInvalidArgument`/`domain.CodeInvalidArgument`。
- Produces: 路由 `GET /admin/api/system-config`、`PUT /admin/api/system-config`、`GET /admin/api/system-config/versions`、`GET /admin/api/system-config/versions/{seq}`、`POST /admin/api/system-config/rollback`；`httpapi.Deps.SystemConfigs *service.SystemConfigService` 字段。

- [ ] **Step 1: 先接线 `Deps` 与路由（让 Step 2 的测试能编译到"接口存在但未实现"这一步）**

`internal/httpapi/router.go`，在 `Deps` 结构体里 `Configs *service.ConfigService` 那一行后面加：

```go
	Configs *service.ConfigService
	// SystemConfigs 管理 fp 自身启动配置的版本化存取，供控制台「系统配置」
	// 页面使用。
	SystemConfigs *service.SystemConfigService
```

`NewRouter` 里，`cfgH := &configHandler{svc: d.Configs}` 那一行后面加：

```go
	cfgH := &configHandler{svc: d.Configs}
	sysCfgH := &systemConfigHandler{svc: d.SystemConfigs}
```

`r.Get("/applications/{id}/config/versions/{seq}", cfgH.getVersion)` 与 `r.Post("/applications/{id}/config/rollback", cfgH.rollback)` 那两行后面（IM 网关凭据那段之前）加：

```go
			r.Get("/system-config", sysCfgH.get)
			// PUT 而不是 PATCH：整份系统配置的全量替换，与
			// /applications/{id}/config 同一语义。
			r.Put("/system-config", sysCfgH.save)
			r.Get("/system-config/versions", sysCfgH.listVersions)
			r.Get("/system-config/versions/{seq}", sysCfgH.getVersion)
			r.Post("/system-config/rollback", sysCfgH.rollback)
```

- [ ] **Step 2: 写 handler 的失败测试**

`internal/httpapi/system_config_test.go`：

```go
package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

// 往返：PUT 存进去的 YAML 原文，GET 能逐字节原样拿回来。
func TestSystemConfigRoundTrip(t *testing.T) {
	h, token, _ := newAdminEnv(t)

	const yamlText = "env: dev # 开发环境\nhttp:\n  addr: \":8080\"\n"
	rec := do(t, h, token, http.MethodPut, "/admin/api/system-config", `{"value":`+jsonString(yamlText)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, "/admin/api/system-config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Seq   int64  `json:"seq"`
		Value string `json:"value"`
	}
	decode(t, rec, &got)
	if got.Value != yamlText {
		t.Fatalf("value = %q，期望逐字节等于 %q", got.Value, yamlText)
	}
}

func TestSystemConfigSaveRejectsInvalidYAML(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	url := "/admin/api/system-config"

	rec := do(t, h, token, http.MethodPut, url, `{"value":"n: 3\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法 YAML 应当能存，status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodPut, url, `{"value":"- a\n- b\n"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("顶层不是映射应当被拒，status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// PUT 是全量替换：第二次省略掉的 key 必须从当前值里消失。
func TestSystemConfigPutIsFullReplacement(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	url := "/admin/api/system-config"

	do(t, h, token, http.MethodPut, url, `{"value":"a: 1\nb: 2\n"}`)
	do(t, h, token, http.MethodPut, url, `{"value":"a: 1\n"}`)

	rec := do(t, h, token, http.MethodGet, url, "")
	var cur struct {
		Value string `json:"value"`
	}
	decode(t, rec, &cur)
	if strings.Contains(cur.Value, "b:") {
		t.Fatalf("b 应当从当前值消失，value = %q", cur.Value)
	}
}

func TestSystemConfigVersionsAndRollback(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	url := "/admin/api/system-config"

	for _, v := range []string{"1", "2"} {
		rec := do(t, h, token, http.MethodPut, url, `{"value":"n: `+v+`\n"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("保存 %s status = %d, body = %s", v, rec.Code, rec.Body.String())
		}
	}

	rec := do(t, h, token, http.MethodGet, url+"/versions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("版本列表 status = %d", rec.Code)
	}
	var versions []struct {
		Seq       int64 `json:"seq"`
		CreatedAt int64 `json:"createdAt"`
	}
	decode(t, rec, &versions)
	if len(versions) != 2 {
		t.Fatalf("版本数 = %d，期望 2", len(versions))
	}
	if versions[0].Seq != 2 {
		t.Fatalf("第一条 seq = %d，期望 2（降序）", versions[0].Seq)
	}

	rec = do(t, h, token, http.MethodPost, url+"/rollback", `{"seq":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("回滚 status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, url, "")
	var cur struct {
		Seq   int64  `json:"seq"`
		Value string `json:"value"`
	}
	decode(t, rec, &cur)
	if cur.Seq != 3 {
		t.Fatalf("回滚后 seq = %d，期望 3", cur.Seq)
	}
	if cur.Value != "n: 1\n" {
		t.Fatalf("回滚后 value = %q，期望 %q", cur.Value, "n: 1\n")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd internal/httpapi && go test ./... -run TestSystemConfig -v`
Expected: 编译失败（`systemConfigHandler` 未定义，`newAdminEnv` 还没传 `SystemConfigs`）

- [ ] **Step 4: 实现 handler**

`internal/httpapi/system_config.go`：

```go
package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type systemConfigHandler struct {
	svc *service.SystemConfigService
}

// systemConfigDTO 是系统配置的一份快照——Value 是 YAML 原文，原样透传。
type systemConfigDTO struct {
	Seq   int64  `json:"seq"`
	Value string `json:"value"`
}

type systemConfigVersionDTO struct {
	Seq       int64 `json:"seq"`
	CreatedAt int64 `json:"createdAt"`
}

type saveSystemConfigRequest struct {
	Value string `json:"value"`
}

type rollbackSystemConfigRequest struct {
	Seq int64 `json:"seq"`
}

type saveSystemConfigResponse struct {
	Seq int64 `json:"seq"`
}

func toSystemConfigDTO(c domain.SystemConfig) systemConfigDTO {
	return systemConfigDTO{Seq: c.Seq, Value: c.Value}
}

func (h *systemConfigHandler) get(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.svc.Current(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSystemConfigDTO(cfg))
}

func (h *systemConfigHandler) save(w http.ResponseWriter, r *http.Request) {
	var req saveSystemConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	seq, err := h.svc.Save(r.Context(), req.Value)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saveSystemConfigResponse{Seq: seq})
}

func (h *systemConfigHandler) listVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := h.svc.ListVersions(r.Context(), 20)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]systemConfigVersionDTO, 0, len(vs))
	for _, v := range vs {
		out = append(out, systemConfigVersionDTO{Seq: v.Seq, CreatedAt: v.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *systemConfigHandler) getVersion(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(chi.URLParam(r, "seq"), 10, 64)
	if err != nil {
		writeError(w, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "版本号不合法"))
		return
	}
	cfg, err := h.svc.Version(r.Context(), seq)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSystemConfigDTO(cfg))
}

func (h *systemConfigHandler) rollback(w http.ResponseWriter, r *http.Request) {
	var req rollbackSystemConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	seq, err := h.svc.Rollback(r.Context(), req.Seq)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saveSystemConfigResponse{Seq: seq})
}
```

- [ ] **Step 5: 把 `SystemConfigs` 接进测试环境**

`internal/httpapi/env_test.go`，`configs := service.NewConfigService(...)` 那一行后面加：

```go
	configs := service.NewConfigService(pool, store.NewConfigPublisher(rdb))
	systemConfigs := service.NewSystemConfigService(pool)
```

`httpapi.Deps{...}` 字面量里 `Configs: configs,` 后面加一行：

```go
		Configs:       configs,
		SystemConfigs: systemConfigs,
```

（`gofmt` 会处理对齐，不用手动数空格。）

- [ ] **Step 6: 跑测试确认通过**

Run: `cd internal/httpapi && go test ./... -v`
Expected: PASS（全量跑一遍 httpapi 包，确认没有连带弄坏别的测试）

- [ ] **Step 7: 提交**

```bash
git add internal/httpapi/system_config.go internal/httpapi/system_config_test.go internal/httpapi/router.go internal/httpapi/env_test.go
git commit -m "$(cat <<'EOF'
feat(httpapi): 系统配置的 HTTP 路由

GET/PUT /admin/api/system-config、版本列表/单版本/回滚，形状是
/applications/{id}/config 那组去掉 id 与 push 之后的样子。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `cmd/fp/main.go` 启动顺序改造

**Files:**
- Create: `cmd/fp/main_test.go`
- Modify: `cmd/fp/main.go`

**Interfaces:**
- Consumes: `config.RequireEnv`、`config.Parse`（Task 1）；`service.NewSystemConfigService`（Task 2）。
- Produces: `resolveBootstrapAdmin(cfg config.BootstrapAdmin) config.BootstrapAdmin`（包内私有，`main_test.go` 用同包直接测）。

- [ ] **Step 1: 写 `resolveBootstrapAdmin` 的失败测试**

`cmd/fp/main_test.go`：

```go
package main

import (
	"testing"

	"github.com/basicfu/fp/internal/config"
)

func TestResolveBootstrapAdminDefaultsWhenEmpty(t *testing.T) {
	got := resolveBootstrapAdmin(config.BootstrapAdmin{})
	if got.User != "admin" || got.Password != "admin" {
		t.Fatalf("got = %+v，期望两项都空时落到内置默认 admin/admin", got)
	}
}

func TestResolveBootstrapAdminKeepsExplicitValue(t *testing.T) {
	want := config.BootstrapAdmin{User: "root", Password: "s3cret"}
	got := resolveBootstrapAdmin(want)
	if got != want {
		t.Fatalf("got = %+v，期望原样返回 %+v", got, want)
	}
}

// 只填了一项也不该被当成"两项都空"去覆盖——沿用 EnsureBootstrap 自己
// "任一为空就跳过"的判断，这里只负责"两项都空"这一种情况的默认值。
func TestResolveBootstrapAdminKeepsPartiallyFilledValue(t *testing.T) {
	want := config.BootstrapAdmin{User: "root"}
	got := resolveBootstrapAdmin(want)
	if got != want {
		t.Fatalf("got = %+v，期望原样返回 %+v", got, want)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd cmd/fp && go test ./... -run TestResolveBootstrapAdmin -v`
Expected: 编译失败（`resolveBootstrapAdmin` 未定义）

- [ ] **Step 3: 重写 `cmd/fp/main.go`**

完整替换文件内容为：

```go
// Command fp 是 Foundation Platform 的服务端进程。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/basicfu/fp/internal/config"
	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/grpcapi"
	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/internal/logging"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fp 启动失败", "err", err)
		os.Exit(1)
	}
}

// resolveBootstrapAdmin 两项都为空时落到内置默认值 admin/admin。系统
// 配置表首次启动必然是空的，不给默认值会导致数据库里一个管理员都没有、
// 谁都登不进控制台去创建这张表的第一条记录——见
// docs/superpowers/specs/2026-09-21-fp-system-config-design.md 第六节。
// EnsureBootstrap 本身是 ON CONFLICT DO NOTHING，这个默认值只在"库里
// 还没有任何管理员"时才真正生效。
func resolveBootstrapAdmin(cfg config.BootstrapAdmin) config.BootstrapAdmin {
	if cfg.User == "" && cfg.Password == "" {
		return config.BootstrapAdmin{User: "admin", Password: "admin"}
	}
	return cfg
}

func run() error {
	pgURL, err := config.RequireEnv("POSTGRES_URL")
	if err != nil {
		return err
	}
	redisURL, err := config.RequireEnv("REDIS_URL")
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := store.OpenPostgres(ctx, pgURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}

	rdb, err := store.OpenRedis(ctx, redisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	// 系统配置必须在 Postgres 连上之后才能读——它本身就存在数据库里。
	// 见 docs/superpowers/specs/2026-09-21-fp-system-config-design.md 第三节。
	systemConfigs := service.NewSystemConfigService(pool)
	sysCfg, err := systemConfigs.Current(ctx)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(sysCfg.Value)
	if err != nil {
		return err
	}
	cfg.BootstrapAdmin = resolveBootstrapAdmin(cfg.BootstrapAdmin)

	log := logging.Setup(cfg.Log.Level)
	log.Info("数据库迁移完成")

	sessionStore := store.NewSessionStore(rdb)
	revokePub := store.NewRevokePublisher(rdb)
	epochStore := store.NewEpochStore(rdb)
	// configPub 要在 appSvc 之前构造：应用的 IM 接入配置变更也走这条广播
	// （见 service.WithIMConfigPublisher）。
	configPub := store.NewConfigPublisher(rdb)

	userSvc := service.NewUserService(pool)
	codeSvc := notify.NewCodeService(rdb)

	registry := connector.NewRegistry()
	if err := registry.Register(connector.NewPassword(userSvc)); err != nil {
		return err
	}
	if err := registry.Register(connector.NewSMSCode(codeSvc)); err != nil {
		return err
	}

	appSvc := service.NewApplicationService(pool, registry, service.WithIMConfigPublisher(configPub))

	sessionSvc := service.NewSessionService(sessionStore, revokePub, epochStore)

	adminSvc := service.NewAdminService(pool, rdb)
	if err := adminSvc.EnsureBootstrap(ctx, cfg.BootstrapAdmin.User, cfg.BootstrapAdmin.Password); err != nil {
		return err
	}

	logSvc := service.NewLoginLogService(pool)
	authzSvc := service.NewAuthzService(pool, service.WithAuthzPublisher(revokePub))

	imCredSvc := service.NewIMCredentialService(pool)
	accessKeySvc := service.NewAccessKeyService(pool, revokePub)

	configSvc := service.NewConfigService(pool, configPub)

	// 短信供应商：四项阿里云凭据齐全就用真实供应商——不管是不是生产环境，
	// 有人就是想在本机联调真实短信通道。凭据不全时：
	//   - 生产环境：config.Parse 已经把这四项收进必填校验，走不到这里；
	//     留着下面这个分支纯属防御性代码。
	//   - 非生产环境：退化成 notify.NewLoggingFakeProvider（验证码只进内存，
	//     不会真的发短信，但发送成功后会以 WARN 级别把整条消息——含验证码
	//     ——打进日志）并打一条 WARN。
	smsSender := notify.NewSender(pool, store.NewRateLimiter(rdb), nil)
	aliyunConfigured := cfg.SMS.Aliyun.AccessKeyID != "" && cfg.SMS.Aliyun.AccessKeySecret != "" &&
		cfg.SMS.Aliyun.SignName != "" && cfg.SMS.Aliyun.TemplateLoginCode != ""
	switch {
	case aliyunConfigured:
		aliyunSMS, err := notify.NewAliyunSMS(notify.AliyunConfig{
			AccessKeyID:     cfg.SMS.Aliyun.AccessKeyID,
			AccessKeySecret: cfg.SMS.Aliyun.AccessKeySecret,
			Endpoint:        cfg.SMS.Aliyun.Endpoint,
			SignName:        cfg.SMS.Aliyun.SignName,
			Templates: map[string]string{
				service.LoginCodeTemplate: cfg.SMS.Aliyun.TemplateLoginCode,
			},
		})
		if err != nil {
			return err
		}
		smsSender.AddProvider(aliyunSMS)
	case cfg.IsProd():
		// 理论上到不了这里：config.Parse 已经保证生产环境下 aliyunConfigured
		// 必为 true。留作防御性兜底。
		return errors.New("生产环境缺少阿里云短信凭据")
	default:
		log.Warn("阿里云短信未配置，短信通道使用内存假供应商——验证码不会真的发送，" +
			"仅限本地开发/测试使用；发送成功的验证码会以 WARN 级别打进本进程日志")
		smsSender.AddProvider(notify.NewLoggingFakeProvider(notify.ChannelSMS, "fake"))
	}

	authSvc := service.NewAuthService(service.AuthDeps{
		Apps:     appSvc,
		Users:    userSvc,
		Sessions: sessionSvc,
		Logs:     logSvc,
		Registry: registry,
		Notifier: smsSender,
		Codes:    codeSvc,
		Authz:    authzSvc,
	})

	httpSrv := &http.Server{
		Addr: cfg.HTTP.Addr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Admin:         adminSvc,
			Apps:          appSvc,
			Users:         userSvc,
			Accounts:      service.NewAccountService(userSvc, sessionSvc, epochStore, logSvc),
			Sessions:      sessionSvc,
			Logs:          logSvc,
			Registry:      registry,
			Authz:         authzSvc,
			Configs:       configSvc,
			SystemConfigs: systemConfigs,
			IMCreds:       imCredSvc,
			AccessKeys:    accessKeySvc,
			// 生产环境的管理端 cookie 必须带 Secure。
			SecureCookies: cfg.IsProd(),
			Console:       web.Dist(),
		}),
	}

	// fatalErr 收后台服务（HTTP、gRPC）的致命错误。带缓冲，保证两个
	// goroutine 都不会因为没人接收而卡在发送上；容量给到二者各投一次。
	fatalErr := make(chan error, 2)

	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatalErr <- fmt.Errorf("HTTP 服务异常退出: %w", err)
			stop()
		}
	}()

	grpcSrv := grpcapi.New(grpcapi.Deps{
		Auth:       authSvc,
		Apps:       appSvc,
		IMCreds:    imCredSvc,
		Pub:        revokePub,
		Authz:      authzSvc,
		Configs:    configSvc,
		ConfigPub:  configPub,
		AccessKeys: accessKeySvc,
	})

	grpcLis, err := net.Listen("tcp", cfg.GRPC.Addr)
	if err != nil {
		return fmt.Errorf("监听 gRPC 地址 %s: %w", cfg.GRPC.Addr, err)
	}

	// ServeWhenReady 内部会先等撤销中继订阅上 Redis 才开始接受连接——这条
	// 编排本身连同"为什么顺序不能反"的完整推导见 grpcapi.Server.ServeWhenReady
	// 的注释。
	go func() {
		if err := grpcSrv.ServeWhenReady(ctx, grpcLis, 10*time.Second); err != nil {
			fatalErr <- fmt.Errorf("gRPC 服务异常退出: %w", err)
			stop()
		}
	}()
	log.Info("fp 启动", "env", cfg.Env, "http", cfg.HTTP.Addr, "grpc", cfg.GRPC.Addr)

	<-ctx.Done()
	log.Info("fp 收到退出信号，正在关闭")

	// gRPC 与 HTTP 各给独立的 10 秒关闭预算，不共用一个 ctx：gRPC 那边要等
	// 存活的 Watch 长流退出，关闭复杂度比 HTTP（只需等存量请求跑完）更高，
	// 更容易把预算用满。
	grpcShutdownCtx, grpcCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer grpcCancel()
	grpcSrv.Shutdown(grpcShutdownCtx)

	httpShutdownCtx, httpCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer httpCancel()
	if err := httpSrv.Shutdown(httpShutdownCtx); err != nil {
		log.Error("HTTP 优雅关闭超时", "err", err)
	}

	// 区分"收到外部信号的正常退出"与"后台服务把自己搞挂了、靠 stop()
	// 间接触发的退出"：前者返回 nil（退出码 0），后者把错误传出去。
	// 用非阻塞 select 排空，而不是 close(fatalErr) 再 range，理由见
	// git 历史（本次改动未触碰这段逻辑）。
	var errs []error
drain:
	for {
		select {
		case err := <-fatalErr:
			errs = append(errs, err)
		default:
			break drain
		}
	}
	return errors.Join(errs...)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd cmd/fp && go build ./... && go test ./... -v`
Expected: `go build` 成功；`TestResolveBootstrapAdmin*` 三条 PASS

- [ ] **Step 5: 提交**

```bash
git add cmd/fp/main.go cmd/fp/main_test.go
git commit -m "$(cat <<'EOF'
feat(fp): 启动顺序改为环境变量→连库→读系统配置

POSTGRES_URL/REDIS_URL 两个环境变量替代 -c 指定的 config.yaml；
连上 Postgres 后从 system_config 表读取其余启动配置。
bootstrap_admin 两项皆空时落到内置默认 admin/admin。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `cmd/fp-dbclean` 跟进环境变量

**Files:**
- Modify: `cmd/fp-dbclean/main.go`

**Interfaces:**
- Consumes: `config.RequireEnv`（Task 1）。

`cmd/fp-dbclean` 没有测试文件（之前也没有），这个任务是纯手工改动 + 手动验证，不走 TDD 的红绿循环。

- [ ] **Step 1: 改 `run()` 里的连接串来源**

把：

```go
	var (
		cfgPath   = flag.String("c", config.DefaultPath, "配置文件路径")
		assumeYes = flag.Bool("y", false, "跳过确认（供 CI 使用）")
		skipRedis = flag.Bool("no-redis", false, "只清 Postgres，不动 Redis")
	)
	flag.Usage = usage
	flag.Parse()

	mode := flag.Arg(0)
	if mode != "truncate" && mode != "reset" {
		usage()
		return errors.New("请指定模式：truncate 或 reset")
	}

	// 清的是 config.yaml 指向的那套库，也就是**开发库**——语义与改成读配置
	// 文件之前完全一致，只是来源从环境变量换成了 config.yaml。config.Load
	// 已经保证 postgres.url 与 redis.url 非空，所以这里不用再判空。
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	pgCfg, err := pgxpool.ParseConfig(cfg.Postgres.URL)
	if err != nil {
		return fmt.Errorf("解析 %s 的 postgres.url: %w", *cfgPath, err)
	}
	conn := pgCfg.ConnConfig
	dbName := conn.Database

	var redisOpt *redis.Options
	if !*skipRedis {
		// 不静默跳过：只清 Postgres 会留下指向已删数据的会话、撤销
		// epoch 与配置推送信号，是个很难查的中间态。
		if redisOpt, err = redis.ParseURL(cfg.Redis.URL); err != nil {
			return fmt.Errorf("解析 %s 的 redis.url: %w", *cfgPath, err)
		}
	}
```

换成：

```go
	var (
		assumeYes = flag.Bool("y", false, "跳过确认（供 CI 使用）")
		skipRedis = flag.Bool("no-redis", false, "只清 Postgres，不动 Redis")
	)
	flag.Usage = usage
	flag.Parse()

	mode := flag.Arg(0)
	if mode != "truncate" && mode != "reset" {
		usage()
		return errors.New("请指定模式：truncate 或 reset")
	}

	// 清的是 POSTGRES_URL/REDIS_URL 指向的那套库，与 fp 本身读的是同一对
	// 环境变量，也就是**开发库**。
	pgURL, err := config.RequireEnv("POSTGRES_URL")
	if err != nil {
		return err
	}
	pgCfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		return fmt.Errorf("解析 POSTGRES_URL: %w", err)
	}
	conn := pgCfg.ConnConfig
	dbName := conn.Database

	var redisURL string
	var redisOpt *redis.Options
	if !*skipRedis {
		// 不静默跳过：只清 Postgres 会留下指向已删数据的会话、撤销
		// epoch 与配置推送信号，是个很难查的中间态。
		if redisURL, err = config.RequireEnv("REDIS_URL"); err != nil {
			return err
		}
		if redisOpt, err = redis.ParseURL(redisURL); err != nil {
			return fmt.Errorf("解析 REDIS_URL: %w", err)
		}
	}
```

下面 `pool, err := store.OpenPostgres(ctx, cfg.Postgres.URL)` 改成 `pool, err := store.OpenPostgres(ctx, pgURL)`；`rdb, err := store.OpenRedis(ctx, cfg.Redis.URL)` 改成 `rdb, err := store.OpenRedis(ctx, redisURL)`。

结尾那句提示语：

```go
	fmt.Println("引导管理员已被清除，重启 fp 时会按 config.yaml 的 bootstrap_admin 重新创建。")
```

改成：

```go
	fmt.Println("引导管理员已被清除，重启 fp 时若系统配置里没有 bootstrap_admin，会用内置默认账号 admin/admin 重新创建。")
```

`usage()` 里的说明文字：

```go
连接串取自配置文件的 postgres.url / redis.url，与 fp 本身读的是同一份，
默认 ./config.yaml——也就是说清的是**开发库**，不是测试库（测试库由
testsupport 在每次跑测试时自己清）。
```

改成：

```go
连接串取自环境变量 POSTGRES_URL / REDIS_URL，与 fp 本身读的是同一对，
清的是**开发库**，不是测试库（测试库由 testsupport 在每次跑测试时自己清）。
```

顶部 `[-c 配置文件]` 的用法提示同步去掉。

- [ ] **Step 2: 编译确认**

Run: `cd cmd/fp-dbclean && go build ./...`
Expected: 编译通过（`flag`、`config`、`pgxpool`、`redis`、`store`、`fmt`、`errors` 几个 import 都还在用，不会有 unused import）

- [ ] **Step 3: 提交**

```bash
git add cmd/fp-dbclean/main.go
git commit -m "$(cat <<'EOF'
refactor(fp-dbclean): 连接串改从 POSTGRES_URL/REDIS_URL 环境变量读取

跟随 cmd/fp 的启动配置改造，去掉 -c 参数与 config.yaml 依赖。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 仓库清理——删旧模板、改脚本与文档

**Files:**
- Delete: `config.example.yaml`
- Modify: `.gitignore`
- Modify: `scripts/run.sh`
- Modify: `scripts/db-clean.sh`
- Modify: `docs/console.md`

无自动化测试（都是脚本与文档），靠手动检查。

- [ ] **Step 1: 删模板、改 `.gitignore`**

```bash
git rm config.example.yaml
```

`.gitignore` 第 10 行 `/config.yaml` 删掉（第 11 行 `/config-im.yaml` 保留，fp-im 的配置文件不在本次范围内）。

- [ ] **Step 2: 改 `scripts/run.sh`**

完整替换为：

```bash
#!/usr/bin/env bash
# 起 fp。POSTGRES_URL / REDIS_URL 必须在环境变量里，其余启动配置都在
# 数据库里的系统配置表，通过控制台「系统配置」页面维护——不再有
# config.yaml。本机开发图省事可以把这两条连接串写进 .env.local，这里顺手
# source 一下；CI / 生产环境直接在进程环境里传，不依赖这个文件。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if [ -f .env.local ]; then
  set -a
  # shellcheck disable=SC1091
  . .env.local
  set +a
fi
if [ -z "${POSTGRES_URL:-}" ] || [ -z "${REDIS_URL:-}" ]; then
  echo "缺少 POSTGRES_URL 或 REDIS_URL。写进 $ROOT/.env.local，或在环境变量里传。" >&2
  exit 1
fi
go run ./cmd/fp "$@"
```

- [ ] **Step 3: 改 `scripts/db-clean.sh`**

完整替换为：

```bash
#!/usr/bin/env bash
# 清空 fp 的数据库。破坏性操作，会要求把库名敲一遍确认。
#
#   ./scripts/db-clean.sh truncate   # 清数据，保表结构（不重跑迁移）
#   ./scripts/db-clean.sh reset      # 删掉全部表，下次启动重跑迁移
#
# 两种模式共用同一套目标打印与确认逻辑，所以合成一个脚本带模式参数，
# 而不是两个各自复制一遍确认代码的脚本——确认这一步正是最不该有分叉的地方。
#
# 清的是 POSTGRES_URL / REDIS_URL 指向的库，与 fp 本身读的是同一对环境
# 变量，也就是**开发库**，不是测试库。测试库由 testsupport 在每次跑测试时
# 自己清，不需要这个脚本。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if [ -f .env.local ]; then
  set -a
  # shellcheck disable=SC1091
  . .env.local
  set +a
fi
exec go run ./cmd/fp-dbclean "$@"
```

- [ ] **Step 4: 改 `docs/console.md`**

把第 6～33 行（"## 构建与运行"整节）替换为：

```markdown
## 构建与运行

    ./scripts/build-web.sh                # 构建前端，产物落在 web/dist/
    export POSTGRES_URL=postgres://postgres:password@127.0.0.1:5432/fp?sslmode=disable
    export REDIS_URL=redis://:password@127.0.0.1:6379/0
    ./scripts/run.sh                      # 起 fp，浏览器打开 http://localhost:8080/

`fp` 只要求这两个环境变量，缺一不启动：

    ERROR fp 启动失败 err="config: 环境变量 POSTGRES_URL 未设置"

其余启动配置（`env`、监听地址、首次管理员、阿里云短信）不在环境变量或
文件里，存在数据库的「系统配置」表中，通过控制台「系统配置」页面维护——
改完需要重启 fp 才会生效。系统配置表还没有任何版本时（全新库）全部用
零值默认启动，`env` 默认 `dev`、监听地址默认 `:8080`/`:9090`。

`scripts/run.sh` 会顺手 source 一下仓库根目录的 `.env.local`（如果存在），
本机开发可以把这两条连接串写在那里，不用每次手动 `export`。

    go build -o fp ./cmd/fp && POSTGRES_URL=... REDIS_URL=... ./fp

**控制台的登录账号**：系统配置里的 `bootstrap_admin.user` /
`bootstrap_admin.password`，两项都为空时（包括全新库、从未配置过）落到
内置默认值 `admin` / `admin`。注意 `EnsureBootstrap` 是
`ON CONFLICT DO NOTHING`——账号一旦建过，改系统配置里的这两项不会改
密码，得直接改库或在控制台里改密码。

**没跑过 `build-web.sh` 也能 `go build`**（`web/dist/` 里提交了一个
`.gitkeep`，embed 指令用的是 `all:` 前缀），只是打开控制台会看到一句
"管理控制台前端尚未构建"。
```

在"## 页面"那张表（约第 82～83 行，`/config/versions` 那一行之后）追加两行：

```markdown
| `/system-config` | fp 自身的系统配置：env、监听地址、首次管理员、阿里云短信，保存后重启生效 |
| `/system-config/versions` | 系统配置版本历史：查看某一版内容、回滚到某一版 |
```

- [ ] **Step 5: 手动检查**

```bash
grep -rn "config\.yaml\|config\.example\.yaml\|FP_POSTGRES_URL\|FP_REDIS_URL" docs/ scripts/ README.md 2>/dev/null
```

Expected: 除 `config-im.yaml`（fp-im 的，不受影响）外没有残留引用；如果 `README.md` 也提到 `config.yaml`，一并按同样思路改掉（环境变量 + 系统配置页面）。

- [ ] **Step 6: 提交**

```bash
git add -A -- config.example.yaml .gitignore scripts/run.sh scripts/db-clean.sh docs/console.md
git commit -m "$(cat <<'EOF'
docs: 系统配置改从数据库读取，config.yaml 相关文档与脚本跟进

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: 前端——从 `ConfigCenter.tsx` 抽出 `useYamlEditor` hook

**Files:**
- Create: `web/src/lib/useYamlEditor.ts`
- Modify: `web/src/pages/ConfigCenter.tsx`

**Interfaces:**
- Produces: `normalizeYAML(text: string): string`、`validateYAML(text: string): string`、`useYamlEditor(remote: ConfigSnapshot | null): { draft: string; setDraft: (v: string) => void; validationError: string; dirty: boolean; handleTextareaKeyDown: (e: KeyboardEvent<HTMLTextAreaElement>) => void }`（都是 `web/src/lib/useYamlEditor.ts` 的具名导出）。
- Consumes: `ConfigSnapshot`（`web/src/lib/types.ts`，已存在，`{seq: number, value: string}`）。

这是纯重构：不改变任何行为，`ConfigCenter.test.tsx` 不改动也必须全绿——它是这个任务唯一的回归网。

- [ ] **Step 1: 跑一遍现有测试，确认重构前是绿的基线**

Run: `cd web && npm test -- ConfigCenter.test.tsx`
Expected: 全部 PASS（重构前的基线）

- [ ] **Step 2: 新建 `useYamlEditor.ts`**

`web/src/lib/useYamlEditor.ts`：

```ts
import { useEffect, useState, type KeyboardEvent } from 'react'
import { load as loadYAML } from 'js-yaml'
import type { ConfigSnapshot } from '@/lib/types'

/**
 * normalizeYAML 给"key:value"这种冒号后漏了空格的行补上那个空格（列表
 * 项前缀、缩进都保留，注释行不动）——跟后端 domain.NormalizeConfigYAML
 * 是同一条启发式规则的前端版本，只用来在打字时就让校验通过，真正落库的
 * 补全动作由后端做一遍权威的。
 */
export function normalizeYAML(text: string): string {
  return text
    .split('\n')
    .map((line) => {
      const trimmed = line.trimStart()
      if (trimmed === '' || trimmed.startsWith('#')) return line
      return line.replace(/^(\s*(?:-\s+)?[^:\s][^:]*):(\S)/, '$1: $2')
    })
    .join('\n')
}

/**
 * validateYAML 只做一件事：这段文本（补完冒号空格之后）能不能被解析、且
 * 顶层是不是一个映射。不做任何值级别的校验——YAML 自己的字面量语法就是
 * 类型信息。跟后端 domain.ParseConfigYAML 是同一条校验规则的前端版本，
 * 只是提前到打字时就告诉人，不用等点保存才知道。
 */
export function validateYAML(text: string): string {
  const normalized = normalizeYAML(text)
  if (normalized.trim() === '') return ''
  let parsed: unknown
  try {
    parsed = loadYAML(normalized)
  } catch (e) {
    return e instanceof Error ? e.message : '不是合法的 YAML'
  }
  if (parsed === null || parsed === undefined) return ''
  if (typeof parsed !== 'object' || Array.isArray(parsed)) {
    return '顶层必须是一个映射（key: value 的形式），不能是列表或裸标量'
  }
  return ''
}

/**
 * useYamlEditor 是 ConfigCenter（应用配置中心）与 SystemConfig（fp 自身
 * 系统配置）共用的 YAML 编辑框状态。draft 是编辑框里的原文；remote 一到
 * （首次加载、切分区/重新拉取、保存成功后的 reload）就用它整体覆盖
 * draft——不做"合并本地改动"的尝试，YAML 编辑框本来就是"整份替换"的
 * 心智模型，不是逐字段增量编辑。
 *
 * 依赖的是 remote 这个对象引用本身（不是拆出来的字符串）：调用方每次
 * reload 都会拿到一个新对象，即使内容没变，这样才能保证"保存成功后
 * dirty 归零"在内容意外没变时依然生效。
 */
export function useYamlEditor(remote: ConfigSnapshot | null) {
  const [draft, setDraft] = useState('')

  useEffect(() => {
    if (!remote) return
    setDraft(remote.value)
  }, [remote])

  const validationError = validateYAML(draft)
  const dirty = remote !== null && draft !== remote.value

  /** Tab 在这个编辑框里是缩进，不是切到下一个控件——YAML 靠缩进表达层级，浏览器默认的"移走焦点"在这没用。 */
  function handleTextareaKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key !== 'Tab') return
    e.preventDefault()
    const el = e.currentTarget
    const start = el.selectionStart
    const end = el.selectionEnd
    setDraft(draft.slice(0, start) + '  ' + draft.slice(end))
    requestAnimationFrame(() => {
      el.selectionStart = el.selectionEnd = start + 2
    })
  }

  return { draft, setDraft, validationError, dirty, handleTextareaKeyDown }
}
```

- [ ] **Step 3: 改 `ConfigCenter.tsx` 接入 hook**

删掉这些顶层函数（整段移到了 `useYamlEditor.ts`）：`normalizeYAML`、`validateYAML`。

删掉这些局部状态与函数：

```ts
  const [draft, setDraft] = useState('')
  ...
  useEffect(() => {
    if (!snapshot.data) return
    setDraft(snapshot.data.value)
  }, [snapshot.data])

  ...
  const validationError = validateYAML(draft)
  const dirty = snapshot.data !== null && draft !== snapshot.data.value
```

以及 `handleTextareaKeyDown` 整个函数定义。

换成：

```ts
  const { draft, setDraft, validationError, dirty, handleTextareaKeyDown } = useYamlEditor(snapshot.data)
```

放在 `const snapshot = useResource(...)` 定义之后、`const [saving, setSaving] = useState(false)` 之前。

顶部 import 加一行、删两行：

```ts
import { useYamlEditor, validateYAML } from '@/lib/useYamlEditor'
```

（`handleSave` 里还直接调用 `validateYAML(draft)` 做提交前的最终校验，所以要从 hook 文件里把它也 import 进来；`useEffect`、`type KeyboardEvent` 这两个从 `react` 的具名导入如果 `ConfigCenter.tsx` 里已经没有其他地方用到，就从 import 里删掉，`load as loadYAML` 的导入同理整行删掉。）

- [ ] **Step 4: 跑测试确认仍然全绿**

Run: `cd web && npm test -- ConfigCenter.test.tsx`
Expected: PASS，且用例数量与 Step 1 完全一致（纯重构，一条都不该少）

- [ ] **Step 5: 跑一遍类型检查**

Run: `cd web && npx tsc -b`
Expected: 无错误（确认没有留下未使用的 import）

- [ ] **Step 6: 提交**

```bash
git add web/src/lib/useYamlEditor.ts web/src/pages/ConfigCenter.tsx
git commit -m "$(cat <<'EOF'
refactor(web): 从 ConfigCenter 抽出 useYamlEditor hook

供即将新增的 SystemConfig 页面复用，纯重构不改变 ConfigCenter 行为
（ConfigCenter.test.tsx 未改动且全绿）。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: 前端——「系统配置」编辑页

**Files:**
- Create: `web/src/pages/SystemConfig.tsx`
- Create: `web/src/pages/SystemConfig.test.tsx`

**Interfaces:**
- Consumes: `useYamlEditor`、`validateYAML`（Task 7）；`api`（`web/src/lib/api.ts`）；`useResource`/`errorMessage`（`web/src/lib/useResource.ts`）；`ConfigSnapshot`/`SaveConfigResponse`（`web/src/lib/types.ts`，复用，不新增类型）。
- Produces: 默认导出 `SystemConfig` 组件，`GET /system-config` 拉取、`PUT /system-config`（body `{value}`）保存、链接到 `/system-config/versions`。

- [ ] **Step 1: 写失败的测试**

`web/src/pages/SystemConfig.test.tsx`：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { toast } from 'sonner'
import SystemConfig from './SystemConfig'
import type { ConfigSnapshot } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/** stubSystemConfig 是个有状态的假后端，支持这个页面用到的两个接口。 */
function stubSystemConfig(initial: ConfigSnapshot, calls: Array<{ method: string; body: unknown }> = []) {
  let state: ConfigSnapshot = { ...initial }
  const fn = vi.fn(async (_url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const body = init?.body ? (JSON.parse(init.body as string) as unknown) : undefined
    calls.push({ method, body })
    if (method === 'PUT') {
      const put = body as { value: string }
      state = { seq: state.seq + 1, value: put.value }
      return jsonResponse({ seq: state.seq })
    }
    return jsonResponse(state)
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function yamlBox(): HTMLTextAreaElement {
  return screen.getByRole('textbox') as HTMLTextAreaElement
}

function renderPage() {
  return render(
    <MemoryRouter>
      <SystemConfig />
    </MemoryRouter>,
  )
}

test('加载出来的 YAML 原文原样显示在编辑框里', async () => {
  stubSystemConfig({ seq: 1, value: 'env: dev\n' })
  renderPage()
  await waitFor(() => expect(yamlBox().value).toBe('env: dev\n'))
})

test('没有改动时保存按钮禁用', async () => {
  stubSystemConfig({ seq: 1, value: 'env: dev\n' })
  renderPage()
  await waitFor(() => expect(yamlBox().value).toBe('env: dev\n'))
  expect((screen.getByRole('button', { name: '保存' }) as HTMLButtonElement).disabled).toBe(true)
})

test('点保存提交 PUT，body 只有 value 字段', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubSystemConfig({ seq: 1, value: 'env: dev\n' }, calls)
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('env: dev\n'))
  fireEvent.change(yamlBox(), { target: { value: 'env: prod\n' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
  const put = calls.find((c) => c.method === 'PUT')!.body
  expect(put).toEqual({ value: 'env: prod\n' })
})

test('YAML 不合法时点保存被 toast 挡下，不提交', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubSystemConfig({ seq: 1, value: 'env: dev\n' }, calls)
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('env: dev\n'))
  fireEvent.change(yamlBox(), { target: { value: '- a\n- b\n' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(errorSpy).toHaveBeenCalled())
  expect(calls.some((c) => c.method === 'PUT')).toBe(false)
})

test('提供入口跳到版本历史页', async () => {
  stubSystemConfig({ seq: 1, value: 'env: dev\n' })
  renderPage()
  await waitFor(() => expect(yamlBox().value).toBe('env: dev\n'))
  const link = screen.getByRole('link', { name: '版本历史' })
  expect(link.getAttribute('href')).toBe('/system-config/versions')
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm test -- SystemConfig.test.tsx`
Expected: 找不到模块 `./SystemConfig`

- [ ] **Step 3: 实现页面**

`web/src/pages/SystemConfig.tsx`：

```tsx
import { useState } from 'react'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { useYamlEditor, validateYAML } from '@/lib/useYamlEditor'
import { cn } from '@/lib/utils'
import type { ConfigSnapshot, SaveConfigResponse } from '@/lib/types'

const PLACEHOLDER = [
  'env: dev                # dev / prod',
  'log:',
  '  level: info',
  'http:',
  '  addr: ":8080"',
  'grpc:',
  '  addr: ":9090"',
  'bootstrap_admin:',
  '  user: admin',
  '  password: admin',
  'sms:',
  '  aliyun:',
  '    access_key_id: ""',
].join('\n')

export default function SystemConfig() {
  const snapshot = useResource(() => api.get<ConfigSnapshot>('/system-config'), [])
  const { draft, setDraft, validationError, dirty, handleTextareaKeyDown } = useYamlEditor(snapshot.data)
  const [saving, setSaving] = useState(false)

  async function handleSave() {
    const err = validateYAML(draft)
    if (err) {
      toast.error(err)
      return
    }
    setSaving(true)
    try {
      const res = await api.put<SaveConfigResponse>('/system-config', { value: draft })
      toast.success(`已保存，seq=${res.seq}，重启 fp 后生效`)
      snapshot.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  if (snapshot.error) return <p className="text-sm text-destructive">{snapshot.error}</p>

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-2">
        <h1 className="text-lg font-medium">系统配置</h1>
        <div className="flex-1" />
        <Button variant="outline" render={<Link to="/system-config/versions" />}>
          版本历史
        </Button>
      </div>

      <p className="text-sm text-muted-foreground">
        fp 进程自身的启动配置——env、监听地址、首次启动的管理员账号、阿里云短信凭据。保存后需要重启 fp 才会生效。
      </p>

      {snapshot.data === null && snapshot.loading && <p className="text-sm text-muted-foreground">加载中…</p>}

      {snapshot.data !== null && (
        <>
          <Label htmlFor="system-config-yaml" className="sr-only">
            系统配置（YAML）
          </Label>
          <textarea
            id="system-config-yaml"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={handleTextareaKeyDown}
            spellCheck={false}
            placeholder={PLACEHOLDER}
            className={cn(
              'h-[60vh] max-h-[60vh] w-full resize-y overflow-y-auto rounded-lg border bg-transparent p-3 font-mono text-sm leading-relaxed outline-none',
              validationError ? 'border-destructive' : 'border-input',
            )}
          />

          <div className="flex items-center gap-2">
            <Button onClick={() => void handleSave()} disabled={saving || !dirty}>
              保存
            </Button>
          </div>
        </>
      )}
    </div>
  )
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd web && npm test -- SystemConfig.test.tsx`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add web/src/pages/SystemConfig.tsx web/src/pages/SystemConfig.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): 新增「系统配置」编辑页

fp 自身启动配置的 YAML 编辑框，复用 useYamlEditor；保存只调用
PUT /system-config，没有「推送」——系统配置只在进程启动时读一次。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: 前端——「系统配置版本历史」页

**Files:**
- Create: `web/src/pages/SystemConfigVersions.tsx`
- Create: `web/src/pages/SystemConfigVersions.test.tsx`

**Interfaces:**
- Consumes: `api`、`useResource`/`errorMessage`、`ConfigSnapshot`/`ConfigVersion`/`SaveConfigResponse`（复用已有类型）、`formatTime`（`web/src/lib/format.ts`）、`ConfirmDialog`（`web/src/components/ConfirmDialog.tsx`）。
- Produces: 默认导出 `SystemConfigVersions` 组件：版本列表（`GET /system-config/versions`）、查看某一版内容（`GET /system-config/versions/{seq}`，弹窗展示原文，不做逐 key diff——YAGNI，系统配置改动频率低，不值得复刻 `ConfigVersions.tsx` 的 diff 引擎）、回滚（`POST /system-config/rollback`，二次确认）。

- [ ] **Step 1: 写失败的测试**

`web/src/pages/SystemConfigVersions.test.tsx`：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { toast } from 'sonner'
import SystemConfigVersions from './SystemConfigVersions'
import type { ConfigVersion, ConfigSnapshot } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

const versions: ConfigVersion[] = [
  { seq: 2, createdAt: 2000 },
  { seq: 1, createdAt: 1000 },
]

const snapshots: Record<number, ConfigSnapshot> = {
  1: { seq: 1, value: 'env: dev\n' },
  2: { seq: 2, value: 'env: prod\n' },
}

function stub(calls: Array<{ method: string; url: string; body: unknown }> = []) {
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const body = init?.body ? (JSON.parse(init.body as string) as unknown) : undefined
    calls.push({ method, url, body })

    if (url.endsWith('/system-config/versions')) return jsonResponse(versions)
    if (method === 'POST' && url.endsWith('/rollback')) {
      const seq = (body as { seq: number }).seq
      return jsonResponse({ seq: seq === 2 ? 2 : 3 })
    }
    const m = /\/system-config\/versions\/(\d+)$/.exec(url)
    if (m) return jsonResponse(snapshots[Number(m[1])])
    return jsonResponse({})
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function renderPage() {
  return render(
    <MemoryRouter>
      <SystemConfigVersions />
    </MemoryRouter>,
  )
}

test('列出版本，最新一条标为当前版本', async () => {
  stub()
  renderPage()
  await waitFor(() => expect(screen.getByTestId('version-2')).toBeTruthy())
  expect(screen.getByTestId('version-2').textContent).toContain('当前版本')
  expect(screen.getByTestId('version-1').textContent).not.toContain('当前版本')
})

test('点查看弹出该版本的原文', async () => {
  stub()
  renderPage()
  await waitFor(() => expect(screen.getByTestId('version-1')).toBeTruthy())

  const row = screen.getByTestId('version-1')
  fireEvent.click(within(row).getByRole('button', { name: '查看' }))

  await waitFor(() => expect(screen.getByText('env: dev')).toBeTruthy())
})

test('当前版本没有回滚按钮，其余版本有', async () => {
  stub()
  renderPage()
  await waitFor(() => expect(screen.getByTestId('version-2')).toBeTruthy())
  expect(within(screen.getByTestId('version-2')).queryByRole('button', { name: /回滚到/ })).toBeNull()
  expect(within(screen.getByTestId('version-1')).getByRole('button', { name: '回滚到 v1' })).toBeTruthy()
})

test('点回滚后二次确认，确认后提交 POST /rollback', async () => {
  const calls: Array<{ method: string; url: string; body: unknown }> = []
  stub(calls)
  renderPage()
  await waitFor(() => expect(screen.getByTestId('version-1')).toBeTruthy())

  fireEvent.click(within(screen.getByTestId('version-1')).getByRole('button', { name: '回滚到 v1' }))
  const dialog = await screen.findByRole('dialog')
  fireEvent.click(within(dialog).getByRole('button', { name: '确认回滚' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/rollback'))).toBe(true))
  const rollback = calls.find((c) => c.method === 'POST')!.body
  expect(rollback).toEqual({ seq: 1 })
})
```

（这个测试文件顶部还需要 `import { within } from '@testing-library/react'`，与其余 `render, screen, waitFor, fireEvent` 放在同一行 import 里。）

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm test -- SystemConfigVersions.test.tsx`
Expected: 找不到模块 `./SystemConfigVersions`

- [ ] **Step 3: 实现页面**

`web/src/pages/SystemConfigVersions.tsx`：

```tsx
import { useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { formatTime } from '@/lib/format'
import { useResource, errorMessage } from '@/lib/useResource'
import type { ConfigSnapshot, ConfigVersion, SaveConfigResponse } from '@/lib/types'

export default function SystemConfigVersions() {
  const navigate = useNavigate()
  const versions = useResource(() => api.get<ConfigVersion[]>('/system-config/versions'), [])

  const [viewing, setViewing] = useState<number | null>(null)
  const [viewingValue, setViewingValue] = useState('')
  const [viewingLoading, setViewingLoading] = useState(false)

  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)

  async function openView(seq: number) {
    setViewing(seq)
    setViewingLoading(true)
    try {
      const snap = await api.get<ConfigSnapshot>(`/system-config/versions/${seq}`)
      setViewingValue(snap.value)
    } catch (e) {
      toast.error(errorMessage(e))
      setViewing(null)
    } finally {
      setViewingLoading(false)
    }
  }

  async function confirmRollback() {
    if (rollbackTarget === null) return
    const latestSeqBefore = versions.data?.[0]?.seq
    try {
      const res = await api.post<SaveConfigResponse>('/system-config/rollback', { seq: rollbackTarget })
      toast.success(
        res.seq === latestSeqBefore
          ? '这一版与当前内容完全一致，没有产生新版本'
          : `已回滚，当前版本 seq=${res.seq}，重启 fp 后生效`,
      )
      navigate('/system-config')
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  if (versions.error) return <p className="text-sm text-destructive">{versions.error}</p>
  if (versions.loading && !versions.data) return <p className="text-sm text-muted-foreground">加载中…</p>

  return (
    <div className="space-y-6">
      <h1 className="text-lg font-medium">系统配置版本历史</h1>

      <div className="space-y-3">
        {versions.data?.length === 0 && <p className="text-sm text-muted-foreground">还没有任何版本。</p>}
        {versions.data?.map((v, idx) => {
          const isCurrent = idx === 0
          return (
            <div
              key={v.seq}
              data-testid={`version-${v.seq}`}
              className="flex flex-wrap items-center justify-between gap-3 rounded-md border p-3"
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-sm font-medium">v{v.seq}</span>
                <span className="text-xs text-muted-foreground">{formatTime(v.createdAt)}</span>
                {isCurrent && <Badge variant="secondary">当前版本</Badge>}
              </div>
              <div className="flex items-center gap-2">
                <Button variant="outline" size="sm" onClick={() => void openView(v.seq)}>
                  查看
                </Button>
                {!isCurrent && (
                  <Button variant="outline" size="sm" onClick={() => setRollbackTarget(v.seq)}>
                    回滚到 v{v.seq}
                  </Button>
                )}
              </div>
            </div>
          )
        })}
      </div>

      <Dialog open={viewing !== null} onOpenChange={(v) => !v && setViewing(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>v{viewing} 的内容</DialogTitle>
          </DialogHeader>
          {viewingLoading ? (
            <p className="text-sm text-muted-foreground">加载中…</p>
          ) : (
            <pre className="max-h-[50vh] overflow-auto rounded-md bg-muted/40 p-3 font-mono text-xs leading-relaxed">
              {viewingValue}
            </pre>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={rollbackTarget !== null}
        onOpenChange={(v) => !v && setRollbackTarget(null)}
        title={`回滚到 v${rollbackTarget}`}
        description="回滚只落库，需要重启 fp 才会生效。"
        confirmLabel="确认回滚"
        onConfirm={() => void confirmRollback()}
      />
    </div>
  )
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd web && npm test -- SystemConfigVersions.test.tsx`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add web/src/pages/SystemConfigVersions.tsx web/src/pages/SystemConfigVersions.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): 新增「系统配置版本历史」页

列版本、查看某一版原文、回滚（二次确认）。不做逐 key diff——系统配置
改动频率低，不值得复刻 ConfigVersions.tsx 的 diff 引擎。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: 前端——路由与导航接线

**Files:**
- Modify: `web/src/routes.tsx`
- Modify: `web/src/components/Layout.tsx`
- Modify: `web/src/components/Layout.test.tsx`

**Interfaces:**
- Consumes: `SystemConfig`（Task 8）、`SystemConfigVersions`（Task 9）。

- [ ] **Step 1: 先改测试，钉住"六个导航项"这个新预期**

`web/src/components/Layout.test.tsx`，把：

```tsx
test('渲染五个导航项，当前用户名、面包屑和页面内容都显示', async () => {
```

改成：

```tsx
test('渲染六个导航项，当前用户名、面包屑和页面内容都显示', async () => {
```

在 `expect(within(navMenu).getByRole('link', { name: /配置中心/ })).toBeTruthy()` 那一行后面加：

```tsx
  expect(within(navMenu).getByRole('link', { name: /系统配置/ })).toBeTruthy()
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm test -- Layout.test.tsx`
Expected: 新加的那一断言失败（导航里还没有「系统配置」）

- [ ] **Step 3: 接入路由**

`web/src/routes.tsx`，`import ConfigVersions from '@/pages/ConfigVersions'` 后面加两行 import：

```tsx
import SystemConfig from '@/pages/SystemConfig'
import SystemConfigVersions from '@/pages/SystemConfigVersions'
```

`<Route path="/config/versions" element={<ConfigVersions />} />` 后面加：

```tsx
        <Route path="/system-config" element={<SystemConfig />} />
        <Route path="/system-config/versions" element={<SystemConfigVersions />} />
```

- [ ] **Step 4: 接入导航**

`web/src/components/Layout.tsx`，import 列表里 `Settings` 后面加 `Server`：

```tsx
import {
  AppWindow,
  Key,
  KeyRound,
  LogOut,
  Server,
  Settings,
  ShieldCheck,
  Users as UsersIcon,
} from 'lucide-react'
```

`nav` 数组里 `{ to: '/config', label: '配置中心', icon: Settings },` 后面加一行：

```ts
  { to: '/system-config', label: '系统配置', icon: Server },
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd web && npm test -- Layout.test.tsx`
Expected: PASS

- [ ] **Step 6: 全量跑一遍前端测试与类型检查，确认没有连带弄坏别的东西**

Run: `cd web && npx tsc -b && npm test`
Expected: 全部 PASS

- [ ] **Step 7: 提交**

```bash
git add web/src/routes.tsx web/src/components/Layout.tsx web/src/components/Layout.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): 「系统配置」接入路由与侧边栏导航

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## 收尾检查

全部任务完成后跑一遍：

```bash
cd /d/fp
go build ./...
./scripts/test.sh
cd web && npx tsc -b && npm test && cd ..
```

手动验证（见 `preview_tools`——启动本地 fp 后在浏览器里过一遍）：

1. 不设 `POSTGRES_URL`/`REDIS_URL` 直接起 `./fp`，确认报错信息点名缺的是哪个变量。
2. 设好两个环境变量、指向一个全新数据库启动，确认能用 `admin`/`admin` 登进控制台。
3. 打开「系统配置」页面，编辑框应为空（全新库还没有任何版本），改点内容保存，toast 提示"重启后生效"。
4. 打开「系统配置版本历史」，能看到刚保存的那一版，点「查看」能看到原文，点「回滚」有二次确认。
5. 重启 `fp` 进程，确认新的系统配置（比如改过的 `http.addr`）真的生效。
