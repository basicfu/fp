# fp 配置中心实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让业务方在浏览器里改配置、Go 服务不重新编译发版就能读到新值。

**Architecture:** 一张 `config` 表按 `(应用, 分区)` 存版本快照，每行 `fields` 是 `{key: {type, desc, value}}` 的 jsonb map，一次保存 = 一行 = 一个完整版本。SDK 反射业务方的 struct 推导出 key 与类型，启动时拉当前版本填进去，缺值就报错；变更经 Redis 广播、复用已有的 gRPC Watch 长流推给 SDK，SDK 拉全量、比较、原子换指针。

**Tech Stack:** Go 1.25 / PostgreSQL 18 / Redis / pgx v5 / chi v5 / gRPC + buf / React + Vite + shadcn

## Global Constraints

- **设计文档**：`docs/superpowers/specs/2026-09-05-fp-config-center-design.md`。每个任务开始前读一遍对应章节。
- **跑测试只用 `./scripts/test.sh`**，它从 `.env.local` 载入 `FP_TEST_POSTGRES_URL` / `FP_TEST_REDIS_URL` 并加 `-p 1`。直接 `go test` 会因为缺环境变量而 `t.Fatal`。
- **生成 protobuf 只用 `./scripts/gen.sh`**（buf lint + buf generate + gofmt 检查）。产物提交进仓库。
- **`sdk/` 不得 import `internal/`，`sdk/` 里不得出现 `panic`**。由 `sdk/arch_test.go` 守护，违反会变红。
- **不引入任何新依赖。** 反射与 `encoding/json` 都在标准库。
- **注释和错误信息一律中文**，与仓库既有风格一致。
- **`decodeJSON` 开了 `DisallowUnknownFields`**：请求 DTO 字段名拼错是 400 而不是静默丢弃。
- **fp 类型的取值恒为这六个**：`bool` / `int` / `float` / `string` / `array` / `object`。分区取值恒为 `DEFAULT` / `WEB`。
- **每条标「辨别力」的测试，实现后必须做一次变异验证**：把实现改坏跑一遍确认真的变红，再改回来。这是第三阶段定下的实践，不是可选项。
- **提交粒度**：每个 Task 的每个 TDD 循环结束就 commit，不攒。

---

## 文件结构

**新建：**

| 文件 | 职责 |
|---|---|
| `internal/store/migrations/00007_config.sql` | `config` 表的建表与回滚 |
| `internal/domain/config.go` | `Config` / `ConfigField` 类型、分区与类型常量、值的弱转换 |
| `internal/domain/config_test.go` | 弱转换与 `IsSet` 的表驱动测试 |
| `internal/store/config.go` | `ConfigPublisher`：`fp:config` 频道的发布与订阅 |
| `internal/store/config_test.go` | 发布/订阅往返 |
| `internal/service/config.go` | `ConfigService`：读当前版本、读指定版本、列版本、保存、回滚、修剪 |
| `internal/service/config_test.go` | 服务层全部行为 |
| `internal/grpcapi/config_hub.go` | 把一份 Redis 配置订阅扇出给进程内所有 Watch 流 |
| `internal/grpcapi/config_hub_test.go` | 扇出、缓冲满时的丢弃、Close |
| `internal/grpcapi/config_service.go` | `ConfigService` 的 gRPC 实现（`GetConfig`） |
| `internal/grpcapi/config_service_test.go` | `GetConfig` 的分区隔离与未配置项过滤 |
| `internal/httpapi/config.go` | 控制台的配置中心路由与 DTO |
| `internal/httpapi/config_test.go` | 路由层测试 |
| `proto/fp/v1/config.proto` | `ConfigService` 与 `ConfigChanged` |
| `sdk/configspec.go` | 反射：struct → `[]fieldSpec`（key、fp 类型、字段路径） |
| `sdk/configspec_test.go` | key 推导与类型映射的表驱动测试 |
| `sdk/config.go` | `Bind` / `Binding[T]` / `MissingConfigError` / 热更新 / 回调 |
| `sdk/config_test.go` | 绑定、缺值、热更新、回调 |
| `sdk/bindtype.go` | `BindType` / `TypeBinding` |
| `sdk/bindtype_test.go` | 分区过滤与 JSON 类型解析 |
| `web/src/pages/ConfigCenter.tsx` | 配置中心页（分区 tab、列表、新建、删除、保存） |
| `web/src/pages/ConfigCenter.test.tsx` | 页面测试 |
| `web/src/pages/ConfigVersions.tsx` | 版本历史与回滚 |
| `web/src/pages/ConfigVersions.test.tsx` | 版本历史测试 |
| `internal/integration/config_test.go` | 端到端穿透：控制台改值 → SDK 出口变了 |

**修改：**

| 文件 | 改动 |
|---|---|
| `proto/fp/v1/auth.proto` | `WatchResponse` 的 oneof 加 `config_changed` 分支 |
| `internal/grpcapi/auth_service.go` | `Watch` 里增订配置事件，select 多一个 case |
| `internal/grpcapi/server.go` | `Deps` 加 `ConfigPub`；装配 `ConfigHub` 与 `configServer` |
| `internal/httpapi/router.go` | `Deps` 加 `Configs`；注册配置中心路由 |
| `cmd/fp/main.go` | 构造 `store.NewConfigPublisher` / `service.NewConfigService`，注入两个传输层 |
| `sdk/client.go` | `Client` 持有绑定注册表；`watchOnce` 处理 `ready` 与 `ConfigChanged` |
| `web/src/routes.tsx` | 加两条路由 |
| `web/src/lib/types.ts` | 配置相关 DTO 的手工镜像 |

---

## Task 1: 数据模型与领域类型

**Files:**
- Create: `internal/store/migrations/00007_config.sql`
- Create: `internal/domain/config.go`
- Test: `internal/domain/config_test.go`

**Interfaces:**
- Consumes: 无（第一个任务）
- Produces:
  - `domain.ConfigTypeDefault = "DEFAULT"`、`domain.ConfigTypeWeb = "WEB"`
  - `domain.ConfigValueBool/Int/Float/String/Array/Object`（值分别为 `"bool"` `"int"` `"float"` `"string"` `"array"` `"object"`）
  - `type domain.ConfigField struct { Type string; Desc string; Value json.RawMessage }`（json tag：`type` / `desc` / `value`）
  - `type domain.Config struct { ApplicationID uuid.UUID; Type string; Seq int64; Fields map[string]ConfigField; CreatedAt int64 }`
  - `func (f ConfigField) IsSet() bool`
  - `func IsConfigType(s string) bool`
  - `func IsConfigValueType(s string) bool`
  - `func CoerceConfigValue(valueType string, raw json.RawMessage) (json.RawMessage, error)`
  - `domain.CodeConfigValueInvalid = "CONFIG_VALUE_INVALID"`、`domain.CodeConfigTypeInvalid = "CONFIG_TYPE_INVALID"`

- [ ] **Step 1: 写迁移**

创建 `internal/store/migrations/00007_config.sql`：

```sql
-- +goose Up
-- 配置的版本快照。一次保存一行，自包含：fields 里装该分区**全部**配置项的
-- 类型、备注与值。当前配置 = 该分区 seq 最大的那一行。
--
-- 为什么不是"只存变更点"的时态行：回滚在这里是复制一行，没有可以写错的
-- 地方；时态行的回滚要靠 seq <= N 加 DISTINCT ON 加一层删除过滤全都写对
-- 才对。而且只有整版快照能安全修剪——时态行按 seq 删老行会把"某个字段
-- 最后一次修改恰好落在老版本里"的那行删掉，那个字段就凭空消失了。
--
-- type 是分区不是标记：主键含它，所以同名 key 在 DEFAULT 与 WEB 下是两个
-- 独立的配置项，各有各的值与版本序列。WEB 分区会被下发到浏览器。
CREATE TABLE config (
    application_id uuid        NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    type           text        NOT NULL,
    seq            bigint      NOT NULL,
    fields         jsonb       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, type, seq)
);

-- +goose Down
DROP TABLE config;
```

- [ ] **Step 2: 写失败的测试**

创建 `internal/domain/config_test.go`：

```go
package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

func TestCoerceConfigValue(t *testing.T) {
	cases := []struct {
		name      string
		valueType string
		in        string
		want      string // 期望的规范 JSON；wantErr 为 true 时忽略
		wantErr   bool
	}{
		// 原生类型直接过
		{"bool 原生", domain.ConfigValueBool, `true`, `true`, false},
		{"int 原生", domain.ConfigValueInt, `3`, `3`, false},
		{"float 原生", domain.ConfigValueFloat, `0.02`, `0.02`, false},
		{"string 原生", domain.ConfigValueString, `"商城"`, `"商城"`, false},
		{"array 原生", domain.ConfigValueArray, `[1,2,3]`, `[1,2,3]`, false},
		{"object 原生", domain.ConfigValueObject, `{"a":1}`, `{"a":1}`, false},

		// 弱转换：字符串形式的值也要能进
		{"字符串进 int", domain.ConfigValueInt, `"3"`, `3`, false},
		{"字符串进 bool", domain.ConfigValueBool, `"true"`, `true`, false},
		{"字符串进 float", domain.ConfigValueFloat, `"0.02"`, `0.02`, false},
		{"字符串进 array", domain.ConfigValueArray, `"[1,2]"`, `[1,2]`, false},
		{"字符串进 object", domain.ConfigValueObject, `"{\"a\":1}"`, `{"a":1}`, false},
		{"数字进 string", domain.ConfigValueString, `3`, `"3"`, false},

		// 转不过去才报错
		{"abc 进 int", domain.ConfigValueInt, `"abc"`, "", true},
		{"小数进 int", domain.ConfigValueInt, `3.7`, "", true},
		{"对象进 array", domain.ConfigValueArray, `{"a":1}`, "", true},
		{"数组进 object", domain.ConfigValueObject, `[1,2]`, "", true},

		// null 一律是"未配置"，任何类型都接受
		{"null 进 int", domain.ConfigValueInt, `null`, `null`, false},
		{"null 进 object", domain.ConfigValueObject, `null`, `null`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.CoerceConfigValue(c.valueType, json.RawMessage(c.in))
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if string(got) != c.want {
				t.Fatalf("得到 %s，期望 %s", got, c.want)
			}
		})
	}
}

func TestConfigFieldIsSet(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"已配置", `3`, true},
		{"配成了 false", `false`, true},   // false 是有效值，不是未配置
		{"配成了 0", `0`, true},           // 0 同理
		{"配成了空串", `""`, true},
		{"JSON null 是未配置", `null`, false},
		{"nil 是未配置", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := domain.ConfigField{Type: domain.ConfigValueInt, Value: json.RawMessage(c.raw)}
			if got := f.IsSet(); got != c.want {
				t.Fatalf("IsSet() = %v，期望 %v", got, c.want)
			}
		})
	}
}

func TestIsConfigTypeAndValueType(t *testing.T) {
	for _, s := range []string{domain.ConfigTypeDefault, domain.ConfigTypeWeb} {
		if !domain.IsConfigType(s) {
			t.Fatalf("%q 应当是合法分区", s)
		}
	}
	for _, s := range []string{"", "default", "web", "MOBILE"} {
		if domain.IsConfigType(s) {
			t.Fatalf("%q 不应当是合法分区", s)
		}
	}
	for _, s := range []string{"bool", "int", "float", "string", "array", "object"} {
		if !domain.IsConfigValueType(s) {
			t.Fatalf("%q 应当是合法值类型", s)
		}
	}
	for _, s := range []string{"", "duration", "secret", "Int"} {
		if domain.IsConfigValueType(s) {
			t.Fatalf("%q 不应当是合法值类型", s)
		}
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/domain -run 'TestCoerceConfigValue|TestConfigFieldIsSet|TestIsConfigType' -v`
Expected: 编译失败，`undefined: domain.CoerceConfigValue` 等

- [ ] **Step 4: 写实现**

创建 `internal/domain/config.go`：

```go
package domain

import (
	"bytes"
	"encoding/json"

	"github.com/google/uuid"
)

// 配置分区。type 是分区不是标记：同名 key 在两个分区下是两个独立的配置项，
// 各有各的值与版本序列（设计文档第三节）。
const (
	// ConfigTypeDefault 是后端服务读的分区。密钥类**必须**建在这里——
	// 没有 secret 标记之后，分区是阻止密钥被下发到浏览器的唯一的闸。
	ConfigTypeDefault = "DEFAULT"
	// ConfigTypeWeb 会被业务方转发给浏览器。
	ConfigTypeWeb = "WEB"
)

// 配置项的值类型。刻意不带任何一门语言的特性——没有 duration，因为其他
// 语言没这个概念；Go 的 time.Duration 是 SDK 按毫秒当 int 处理的私事。
const (
	ConfigValueBool   = "bool"
	ConfigValueInt    = "int"
	ConfigValueFloat  = "float"
	ConfigValueString = "string"
	ConfigValueArray  = "array"
	ConfigValueObject = "object"
)

// IsConfigType 报告 s 是否是合法分区。大小写敏感。
func IsConfigType(s string) bool {
	return s == ConfigTypeDefault || s == ConfigTypeWeb
}

// IsConfigValueType 报告 s 是否是合法值类型。大小写敏感。
func IsConfigValueType(s string) bool {
	switch s {
	case ConfigValueBool, ConfigValueInt, ConfigValueFloat,
		ConfigValueString, ConfigValueArray, ConfigValueObject:
		return true
	}
	return false
}

// ConfigField 是 config.fields 里的一项。
type ConfigField struct {
	Type  string          `json:"type"`
	Desc  string          `json:"desc"`
	Value json.RawMessage `json:"value"`
}

// IsSet 报告这一项是否已配置。
//
// 判据只有一条：值不是 JSON null。false / 0 / "" 都是**有效值**，
// 不是"未配置"——把它们也算作未配置的话，一个刻意关掉的开关会在每次
// 启动时被 SDK 报成缺失。
func (f ConfigField) IsSet() bool {
	v := bytes.TrimSpace(f.Value)
	return len(v) > 0 && !bytes.Equal(v, []byte("null"))
}

// Config 是一个分区的一个版本快照。
type Config struct {
	ApplicationID uuid.UUID
	Type          string
	Seq           int64
	Fields        map[string]ConfigField
	CreatedAt     int64
}

// CoerceConfigValue 把提交上来的值按 valueType 转成规范 JSON。
//
// 弱约束（设计文档 6.3）：`"3"` 填进 int 字段照样过，**只有转不过去才报错**。
// 这里不做范围与枚举校验——那交给业务方在 Bind 之后自己判。
//
// JSON null 表示"未配置"，对任何类型都合法，原样返回。
func CoerceConfigValue(valueType string, raw json.RawMessage) (json.RawMessage, error) {
	if !IsConfigValueType(valueType) {
		return nil, Fail(ErrInvalidArgument, CodeConfigTypeInvalid, "配置项类型不合法").
			WithDesc("未知类型 %q", valueType)
	}

	s := bytes.TrimSpace(raw)
	if len(s) == 0 || bytes.Equal(s, []byte("null")) {
		return json.RawMessage("null"), nil
	}

	// 先看它是不是一个 JSON 字符串。是的话把字符串**内容**拿出来再解析一遍，
	// 这就是"弱"的全部含义：控制台上人手填的东西经常是 "3" 而不是 3。
	// string 类型例外——它要的就是这个字符串本身。
	if valueType != ConfigValueString {
		var unquoted string
		if json.Unmarshal(s, &unquoted) == nil {
			s = bytes.TrimSpace([]byte(unquoted))
		}
	}

	fail := func(err error) (json.RawMessage, error) {
		return nil, Fail(ErrInvalidArgument, CodeConfigValueInvalid, "配置值与类型不匹配").
			WithDesc("无法把 %s 解析成 %s: %v", raw, valueType, err)
	}

	switch valueType {
	case ConfigValueBool:
		var v bool
		if err := json.Unmarshal(s, &v); err != nil {
			return fail(err)
		}
		return json.Marshal(v)
	case ConfigValueInt:
		// int64 而不是 float64：3.7 进 int 必须报错，不能静默截断。
		var v int64
		if err := json.Unmarshal(s, &v); err != nil {
			return fail(err)
		}
		return json.Marshal(v)
	case ConfigValueFloat:
		var v float64
		if err := json.Unmarshal(s, &v); err != nil {
			return fail(err)
		}
		return json.Marshal(v)
	case ConfigValueString:
		var v string
		if err := json.Unmarshal(s, &v); err == nil {
			return json.Marshal(v)
		}
		// 不是 JSON 字符串（比如填了个裸数字 3），把原文当字符串收下。
		return json.Marshal(string(s))
	case ConfigValueArray:
		var v []any
		if err := json.Unmarshal(s, &v); err != nil {
			return fail(err)
		}
		return json.Marshal(v)
	default: // ConfigValueObject
		var v map[string]any
		if err := json.Unmarshal(s, &v); err != nil {
			return fail(err)
		}
		return json.Marshal(v)
	}
}
```

在 `internal/domain/codes.go` 的常量块里加两行（放在 `CodeConnectorConfigInvalid` 附近的参数错误一组）：

```go
	// CodeConfigValueInvalid 是配置值按声明类型转换失败。Detail 里带原值与目标类型。
	CodeConfigValueInvalid = "CONFIG_VALUE_INVALID"
	// CodeConfigTypeInvalid 覆盖分区与值类型两处的取值不合法。
	CodeConfigTypeInvalid = "CONFIG_TYPE_INVALID"
```

- [ ] **Step 5: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/domain -run 'TestCoerceConfigValue|TestConfigFieldIsSet|TestIsConfigType' -v`
Expected: PASS，全部子用例绿

- [ ] **Step 6: 跑迁移确认建表成功**

Run: `./scripts/test.sh ./internal/store -run TestMigrate -v`
Expected: PASS。若仓库没有这条测试，改跑 `./scripts/test.sh ./internal/service -run TestApplication -v`——任何用 `testsupport.NewTestDB` 的测试都会执行全部迁移，迁移写错会在这里炸。

- [ ] **Step 7: 提交**

```bash
git add internal/store/migrations/00007_config.sql internal/domain/config.go internal/domain/config_test.go internal/domain/codes.go
git commit -m "feat(config): 配置中心的表结构与领域类型"
```

---

## Task 2: ConfigService 读

**Files:**
- Create: `internal/service/config.go`
- Test: `internal/service/config_test.go`

**Interfaces:**
- Consumes: Task 1 的 `domain.Config` / `domain.ConfigField` / `domain.ConfigTypeDefault` / `domain.ConfigTypeWeb`
- Produces:
  - `func service.NewConfigService(pool *pgxpool.Pool, pub ConfigPublisher) *ConfigService`
    （`pub` 是接口，Task 5 才有真实现，本任务传 `nil`）
  - `type service.ConfigPublisher interface { Publish(ctx context.Context, appID uuid.UUID, typ string, seq int64) error }`
  - `func (s *ConfigService) Current(ctx context.Context, appID uuid.UUID, typ string) (domain.Config, error)`
    —— 该分区一个版本都没有时返回 `Seq: 0` 与**非 nil 的空 Fields**，不是 `ErrNotFound`
  - `func (s *ConfigService) Version(ctx context.Context, appID uuid.UUID, typ string, seq int64) (domain.Config, error)`
    —— 版本不存在返回 `domain.ErrNotFound` 的包装
  - `func (s *ConfigService) ListVersions(ctx context.Context, appID uuid.UUID, typ string, limit int) ([]domain.Config, error)`
    —— 按 `seq` 降序，元素的 `Fields` 为 nil（列表页不需要）

- [ ] **Step 1: 写失败的测试**

创建 `internal/service/config_test.go`：

```go
package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

// newConfigFixture 建一个应用并返回它的 id 与一个 ConfigService。
// pub 传 nil：本任务只测读，推送在 Task 5。
func newConfigFixture(t *testing.T) (*service.ConfigService, uuid.UUID) {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	apps := service.NewApplicationService(pool, nil)
	app, err := apps.Create(context.Background(), "商城", "shop")
	if err != nil {
		t.Fatalf("建应用失败: %v", err)
	}
	return service.NewConfigService(pool, nil), app.ID
}

func field(typ, desc, value string) domain.ConfigField {
	return domain.ConfigField{Type: typ, Desc: desc, Value: json.RawMessage(value)}
}

func TestCurrentReturnsEmptyWhenNoVersion(t *testing.T) {
	svc, appID := newConfigFixture(t)

	got, err := svc.Current(context.Background(), appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if got.Seq != 0 {
		t.Fatalf("Seq = %d，期望 0", got.Seq)
	}
	// 必须是非 nil 的空 map：调用方会直接 range 它，返回 nil 会让
	// "还没有任何版本"和"这一版是空的"在下游产生不同的分支。
	if got.Fields == nil {
		t.Fatal("Fields 是 nil，期望非 nil 的空 map")
	}
	if len(got.Fields) != 0 {
		t.Fatalf("Fields 有 %d 项，期望 0", len(got.Fields))
	}
}

func TestVersionNotFound(t *testing.T) {
	svc, appID := newConfigFixture(t)

	_, err := svc.Version(context.Background(), appID, domain.ConfigTypeDefault, 7)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，期望包装了 domain.ErrNotFound", err)
	}
}

func TestRejectsUnknownPartition(t *testing.T) {
	svc, appID := newConfigFixture(t)

	_, err := svc.Current(context.Background(), appID, "MOBILE")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v，期望包装了 domain.ErrInvalidArgument", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/service -run 'TestCurrentReturnsEmpty|TestVersionNotFound|TestRejectsUnknownPartition' -v`
Expected: 编译失败，`undefined: service.NewConfigService`

- [ ] **Step 3: 写实现**

创建 `internal/service/config.go`：

```go
// Package service 的配置中心部分：配置项由人在控制台创建，SDK 只读。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
)

// ConfigPublisher 是 ConfigService 对推送通道的全部依赖。
//
// 拆成接口而不是直接吃 *store.ConfigPublisher：保存逻辑的测试不需要
// Redis，而"选了仅落库就一次都不发"这条断言恰恰需要一个能数调用次数的
// 假实现（见 Task 3）。
type ConfigPublisher interface {
	Publish(ctx context.Context, appID uuid.UUID, typ string, seq int64) error
}

// ConfigService 管理配置的版本快照。
type ConfigService struct {
	pool *pgxpool.Pool
	pub  ConfigPublisher // 可为 nil：不推送，只落库
}

// NewConfigService 构造 ConfigService。
func NewConfigService(pool *pgxpool.Pool, pub ConfigPublisher) *ConfigService {
	return &ConfigService{pool: pool, pub: pub}
}

// checkType 校验分区取值。
func checkConfigType(typ string) error {
	if !domain.IsConfigType(typ) {
		return domain.Fail(domain.ErrInvalidArgument, domain.CodeConfigTypeInvalid, "配置分区不合法").
			WithDesc("未知分区 %q", typ)
	}
	return nil
}

// Current 返回该分区当前版本。
//
// 一个版本都没有时返回 Seq=0 与空 Fields，**不是** ErrNotFound：
// "这个应用还没配过任何东西"是正常状态，不是错误。控制台第一次打开、
// SDK 第一次 Bind 走的都是这条路径。
func (s *ConfigService) Current(ctx context.Context, appID uuid.UUID, typ string) (domain.Config, error) {
	if err := checkConfigType(typ); err != nil {
		return domain.Config{}, err
	}
	c, err := s.scanOne(ctx, `
		SELECT seq, fields, extract(epoch FROM created_at)::bigint
		FROM config WHERE application_id = $1 AND type = $2
		ORDER BY seq DESC LIMIT 1`, appID, typ)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Config{
			ApplicationID: appID,
			Type:          typ,
			Seq:           0,
			Fields:        map[string]domain.ConfigField{},
		}, nil
	}
	return c, err
}

// Version 返回指定版本。不存在返回 domain.ErrNotFound 的包装。
func (s *ConfigService) Version(ctx context.Context, appID uuid.UUID, typ string, seq int64) (domain.Config, error) {
	if err := checkConfigType(typ); err != nil {
		return domain.Config{}, err
	}
	c, err := s.scanOne(ctx, `
		SELECT seq, fields, extract(epoch FROM created_at)::bigint
		FROM config WHERE application_id = $1 AND type = $2 AND seq = $3`, appID, typ, seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Config{}, domain.Fail(domain.ErrNotFound, domain.CodeConfigVersionNotFound, "配置版本不存在").
			WithDesc("分区 %s 没有第 %d 版", typ, seq)
	}
	return c, err
}

// ListVersions 返回最近 limit 个版本的元信息。元素的 Fields 恒为 nil——
// 列表页只需要 seq 与时间，一次把 100 份完整快照读出来纯属浪费。
func (s *ConfigService) ListVersions(ctx context.Context, appID uuid.UUID, typ string, limit int) ([]domain.Config, error) {
	if err := checkConfigType(typ); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT seq, extract(epoch FROM created_at)::bigint
		FROM config WHERE application_id = $1 AND type = $2
		ORDER BY seq DESC LIMIT $3`, appID, typ, limit)
	if err != nil {
		return nil, fmt.Errorf("service: 查询配置版本列表: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Config, 0, limit)
	for rows.Next() {
		c := domain.Config{ApplicationID: appID, Type: typ}
		if err := rows.Scan(&c.Seq, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("service: 扫描配置版本: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历配置版本: %w", err)
	}
	return out, nil
}

// scanOne 跑一条只返回 (seq, fields, created_at) 的查询。
// 没有行时原样返回 pgx.ErrNoRows，由调用方决定那是错误还是正常状态。
func (s *ConfigService) scanOne(ctx context.Context, sql string, args ...any) (domain.Config, error) {
	var (
		seq       int64
		raw       []byte
		createdAt int64
	)
	if err := s.pool.QueryRow(ctx, sql, args...).Scan(&seq, &raw, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Config{}, err
		}
		return domain.Config{}, fmt.Errorf("service: 查询配置: %w", err)
	}
	fields := map[string]domain.ConfigField{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return domain.Config{}, fmt.Errorf("service: 解析配置 fields: %w", err)
	}
	appID, _ := args[0].(uuid.UUID)
	typ, _ := args[1].(string)
	return domain.Config{
		ApplicationID: appID, Type: typ, Seq: seq,
		Fields: fields, CreatedAt: createdAt,
	}, nil
}
```

在 `internal/domain/codes.go` 的"未找到"一组里加一行：

```go
	CodeConfigVersionNotFound = "CONFIG_VERSION_NOT_FOUND"
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/service -run 'TestCurrentReturnsEmpty|TestVersionNotFound|TestRejectsUnknownPartition' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/service/config.go internal/service/config_test.go internal/domain/codes.go
git commit -m "feat(config): ConfigService 读当前版本与指定版本"
```

---

## Task 3: ConfigService 保存与修剪

**Files:**
- Modify: `internal/service/config.go`
- Test: `internal/service/config_test.go`

**Interfaces:**
- Consumes: Task 2 的 `ConfigService` / `Current` / `Version` / `ConfigPublisher`
- Produces:
  - `func (s *ConfigService) Save(ctx context.Context, appID uuid.UUID, typ string, fields map[string]domain.ConfigField, push bool) (int64, error)` —— 返回新版本的 seq
  - `service.ConfigMaxVersions = 100`（导出常量，测试与控制台共用）

**保存的语义（读之前先记住）：** `fields` 是该分区的**全量**替换。新建、改值、改类型、改备注、删除配置项，全都是"控制台把完整的 fields 交上来、服务端存成新的一版"。没有局部更新——一次保存 = 一个版本这条规则不允许中间态。

- [ ] **Step 1: 写失败的测试（保存与全量替换语义）**

追加到 `internal/service/config_test.go`：

```go
func TestSaveCreatesSequentialVersions(t *testing.T) {
	svc, appID := newConfigFixture(t)
	ctx := context.Background()

	seq1, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"fee_rate": field(domain.ConfigValueFloat, "手续费率", `0.006`),
	}, false)
	if err != nil {
		t.Fatalf("第一次保存失败: %v", err)
	}
	if seq1 != 1 {
		t.Fatalf("首个版本 seq = %d，期望 1", seq1)
	}

	seq2, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"fee_rate": field(domain.ConfigValueFloat, "手续费率", `0.02`),
	}, false)
	if err != nil {
		t.Fatalf("第二次保存失败: %v", err)
	}
	if seq2 != 2 {
		t.Fatalf("第二个版本 seq = %d，期望 2", seq2)
	}

	cur, err := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("读当前版本失败: %v", err)
	}
	if got := string(cur.Fields["fee_rate"].Value); got != "0.02" {
		t.Fatalf("当前值 = %s，期望 0.02", got)
	}

	// 旧版本必须原样还在——回滚全靠它。
	old, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, 1)
	if err != nil {
		t.Fatalf("读 v1 失败: %v", err)
	}
	if got := string(old.Fields["fee_rate"].Value); got != "0.006" {
		t.Fatalf("v1 的值 = %s，期望 0.006", got)
	}
}

// 删除配置项就是"新版本的 fields 里没有它"。这条同时验证 Save 是全量替换
// 而不是合并——如果实现写成了 merge，被删的 key 会留在新版本里。
func TestSaveIsFullReplacement(t *testing.T) {
	svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"a": field(domain.ConfigValueInt, "", `1`),
		"b": field(domain.ConfigValueInt, "", `2`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"a": field(domain.ConfigValueInt, "", `1`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	cur, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if _, ok := cur.Fields["b"]; ok {
		t.Fatal("b 已被删除，不该出现在当前版本里")
	}
	old, _ := svc.Version(ctx, appID, domain.ConfigTypeDefault, 1)
	if _, ok := old.Fields["b"]; !ok {
		t.Fatal("b 必须留在 v1 里，否则回滚恢复不了它")
	}
}

// 未配置（value 是 JSON null）与已删除（key 不在 map 里）是两件事。
// 【辨别力】两种情形必须同时造出来：只造一种的话，把两者混为一谈的实现
// （比如保存时顺手丢掉 value 为 null 的项）照样会绿。
func TestSaveKeepsUnsetFieldsDistinctFromDeleted(t *testing.T) {
	svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"unset":   field(domain.ConfigValueString, "还没配", `null`),
		"deleted": field(domain.ConfigValueInt, "马上删", `1`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"unset": field(domain.ConfigValueString, "还没配", `null`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	cur, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	f, ok := cur.Fields["unset"]
	if !ok {
		t.Fatal("未配置的项必须仍然存在——它是控制台上待填的那一行")
	}
	if f.IsSet() {
		t.Fatal("unset 不该被判成已配置")
	}
	if _, ok := cur.Fields["deleted"]; ok {
		t.Fatal("已删除的项不该出现")
	}
}

// 保存时按类型转换，转不过去才报错（弱约束）。
func TestSaveCoercesValues(t *testing.T) {
	svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"n": field(domain.ConfigValueInt, "", `"3"`),
	}, false); err != nil {
		t.Fatalf("字符串 3 应当能存进 int 项: %v", err)
	}
	cur, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if got := string(cur.Fields["n"].Value); got != "3" {
		t.Fatalf("存进去的值 = %s，期望规范化成 3", got)
	}

	_, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"n": field(domain.ConfigValueInt, "", `"abc"`),
	}, false)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v，期望包装了 domain.ErrInvalidArgument", err)
	}
}

// 分区隔离：两个分区各有各的 seq，改一个不影响另一个。
// 【辨别力】两个分区的值必须**不同**，否则"分区对了"和"压根没分区"
// 产出同样的结果。
func TestSaveIsolatesPartitions(t *testing.T) {
	svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"site.title": field(domain.ConfigValueString, "", `"后端看到的"`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := svc.Save(ctx, appID, domain.ConfigTypeWeb, map[string]domain.ConfigField{
		"site.title": field(domain.ConfigValueString, "", `"前端看到的"`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	def, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	web, _ := svc.Current(ctx, appID, domain.ConfigTypeWeb)

	if got := string(def.Fields["site.title"].Value); got != `"后端看到的"` {
		t.Fatalf("DEFAULT 分区的值 = %s", got)
	}
	if got := string(web.Fields["site.title"].Value); got != `"前端看到的"` {
		t.Fatalf("WEB 分区的值 = %s", got)
	}
	// seq 是**分区内**自增：两个分区各写了一次，各自都该是 1。
	if def.Seq != 1 || web.Seq != 1 {
		t.Fatalf("DEFAULT seq=%d, WEB seq=%d，期望各自都是 1", def.Seq, web.Seq)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/service -run TestSave -v`
Expected: 编译失败，`svc.Save undefined`

- [ ] **Step 3: 写实现**

在 `internal/service/config.go` 里追加（import 补 `"log/slog"`）：

```go
// ConfigMaxVersions 是每个分区保留的版本数上限，超出的从最老的开始删。
//
// 整版快照的代价是每次保存都抄一遍全部配置项（50 项约 7KB），这个上限
// 把它兜住：100 版约 700KB，一个应用两个分区 1.4MB。修剪在整版快照下是
// 安全的——每一行自包含，删掉老版本的后果就一句话：那些版本回滚不了，
// 其余一切照常。（换成"只存变更点"的时态行就不成立了：按 seq 删老行会
// 把"某个字段最后一次修改恰好落在老版本里"的那行删掉，那个字段会凭空
// 消失。这是当初选整版快照的两条理由之一。）
const ConfigMaxVersions = 100

// Save 用 fields 生成该分区的一个新版本，返回新版本的 seq。
//
// fields 是**全量替换**不是合并：新建、改值、改类型、改备注、删除配置项
// 全都走这一个入口。删除就是"新的 fields 里没有那个 key"。
//
// push 为 true 时保存后广播一次 ConfigChanged；为 false 就是控制台上的
// 「仅落库，实例重启后生效」——它专门解决"发布前必须先改值、但一改旧实例
// 立刻就会拿到"这个矛盾（设计文档第七节）。
func (s *ConfigService) Save(
	ctx context.Context, appID uuid.UUID, typ string,
	fields map[string]domain.ConfigField, push bool,
) (int64, error) {
	if err := checkConfigType(typ); err != nil {
		return 0, err
	}

	// 先把每一项按自己声明的类型规范化。任何一项转不过去就整批拒绝——
	// 一次保存是一个版本，不能出现"一半字段生效了"的版本。
	normalized := make(map[string]domain.ConfigField, len(fields))
	for k, f := range fields {
		if !domain.IsConfigValueType(f.Type) {
			return 0, domain.Fail(domain.ErrInvalidArgument, domain.CodeConfigTypeInvalid, "配置项类型不合法").
				WithDesc("配置项 %q 的类型 %q 未知", k, f.Type)
		}
		v, err := domain.CoerceConfigValue(f.Type, f.Value)
		if err != nil {
			return 0, err
		}
		normalized[k] = domain.ConfigField{Type: f.Type, Desc: f.Desc, Value: v}
	}

	raw, err := json.Marshal(normalized)
	if err != nil {
		return 0, fmt.Errorf("service: 序列化配置 fields: %w", err)
	}

	// seq 在事务里取 MAX+1，靠主键约束兜并发。管理操作低频，冲突了让调用方
	// 重试即可，不值得为它引入序列或咨询锁。
	var seq int64
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(seq), 0) + 1 FROM config
			WHERE application_id = $1 AND type = $2`, appID, typ).Scan(&seq); err != nil {
			return fmt.Errorf("service: 取下一个配置版本号: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO config (application_id, type, seq, fields)
			VALUES ($1, $2, $3, $4)`, appID, typ, seq, raw); err != nil {
			return fmt.Errorf("service: 写入配置版本: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM config
			WHERE application_id = $1 AND type = $2 AND seq <= $3`,
			appID, typ, seq-ConfigMaxVersions); err != nil {
			return fmt.Errorf("service: 修剪配置版本: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	// 推送失败不回滚保存：值已经落库了，那是权威事实；推送只是把生效延迟
	// 从"下次重启"压到近乎实时的加速手段（与 store.RevokePublisher 同一逻辑）。
	if push && s.pub != nil {
		if err := s.pub.Publish(ctx, appID, typ, seq); err != nil {
			slog.Warn("service: 广播配置变更失败，新值要等实例重启才生效",
				"appID", appID, "type", typ, "seq", seq, "err", err)
		}
	}
	return seq, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/service -run TestSave -v`
Expected: PASS，五个测试全绿

- [ ] **Step 5: 变异验证「未配置 vs 已删除」**

把 `Save` 里构造 `normalized` 的循环临时改坏——加一行跳过未配置项：

```go
	for k, f := range fields {
		if !f.IsSet() { continue }   // 故意改坏
		...
	}
```

Run: `./scripts/test.sh ./internal/service -run TestSaveKeepsUnsetFieldsDistinctFromDeleted -v`
Expected: **FAIL**，报"未配置的项必须仍然存在"。确认变红后删掉这一行，重跑确认恢复 PASS。

- [ ] **Step 6: 写修剪的测试**

追加到 `internal/service/config_test.go`（import 补 `"fmt"`）：

```go
// 造满上限再多 5 版，断言最老的 5 版被删、其余都在、当前配置不受影响。
func TestSavePrunesOldVersions(t *testing.T) {
	svc, appID := newConfigFixture(t)
	ctx := context.Background()

	total := service.ConfigMaxVersions + 5
	for i := 1; i <= total; i++ {
		if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
			"n": field(domain.ConfigValueInt, "", fmt.Sprintf("%d", i)),
		}, false); err != nil {
			t.Fatalf("第 %d 次保存失败: %v", i, err)
		}
	}

	for seq := int64(1); seq <= 5; seq++ {
		if _, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, seq); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("v%d 应当已被修剪，err = %v", seq, err)
		}
	}
	if _, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, 6); err != nil {
		t.Fatalf("v6 应当还在: %v", err)
	}
	cur, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if cur.Seq != int64(total) {
		t.Fatalf("当前版本 seq = %d，期望 %d", cur.Seq, total)
	}
	if got := string(cur.Fields["n"].Value); got != fmt.Sprintf("%d", total) {
		t.Fatalf("当前值 = %s，期望 %d", got, total)
	}
}
```

- [ ] **Step 7: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/service -run TestSavePrunesOldVersions -v`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add internal/service/config.go internal/service/config_test.go
git commit -m "feat(config): 保存新版本、全量替换语义与版本修剪"
```

---

## Task 4: ConfigService 回滚

**Files:**
- Modify: `internal/service/config.go`
- Test: `internal/service/config_test.go`

**Interfaces:**
- Consumes: Task 3 的 `Save`、Task 2 的 `Version`
- Produces: `func (s *ConfigService) Rollback(ctx context.Context, appID uuid.UUID, typ string, seq int64, push bool) (int64, error)` —— 返回新生成的版本号

- [ ] **Step 1: 写失败的测试**

追加到 `internal/service/config_test.go`：

```go
// 回滚生成新版本，不删历史。这条同时覆盖设计文档测试策略第 9 条的
// "版本可精确还原"：v1 的完整快照（含类型、备注、值）必须逐字段复现。
func TestRollbackCopiesVersionForward(t *testing.T) {
	svc, appID := newConfigFixture(t)
	ctx := context.Background()

	// v1：两项
	mustSave(t, svc, appID, map[string]domain.ConfigField{
		"a": field(domain.ConfigValueInt, "甲", `1`),
		"b": field(domain.ConfigValueString, "乙", `"x"`),
	})
	// v2：删掉 b
	mustSave(t, svc, appID, map[string]domain.ConfigField{
		"a": field(domain.ConfigValueInt, "甲", `1`),
	})
	// v3：把 a 改成 object 类型（改类型也只是普通的一次保存）
	mustSave(t, svc, appID, map[string]domain.ConfigField{
		"a": field(domain.ConfigValueObject, "甲改了类型", `{"k":1}`),
	})

	newSeq, err := svc.Rollback(ctx, appID, domain.ConfigTypeDefault, 1, false)
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if newSeq != 4 {
		t.Fatalf("回滚生成的版本 = %d，期望 4", newSeq)
	}

	// v1..v3 必须原样都在——回滚是往前追加，不是往回删。
	for seq := int64(1); seq <= 3; seq++ {
		if _, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, seq); err != nil {
			t.Fatalf("v%d 应当仍在: %v", seq, err)
		}
	}

	v1, _ := svc.Version(ctx, appID, domain.ConfigTypeDefault, 1)
	v4, _ := svc.Version(ctx, appID, domain.ConfigTypeDefault, 4)
	if len(v4.Fields) != len(v1.Fields) {
		t.Fatalf("v4 有 %d 项，v1 有 %d 项", len(v4.Fields), len(v1.Fields))
	}
	for k, want := range v1.Fields {
		got, ok := v4.Fields[k]
		if !ok {
			t.Fatalf("v4 缺少 %q", k)
		}
		// 类型和备注也要跟着回去——它们和值一样都在 fields 里、都随版本走。
		// 这正是"改类型可被回滚撤销"那条设计的执行点。
		if got.Type != want.Type || got.Desc != want.Desc || string(got.Value) != string(want.Value) {
			t.Fatalf("v4[%q] = %+v，期望 %+v", k, got, want)
		}
	}
}

func TestRollbackToMissingVersion(t *testing.T) {
	svc, appID := newConfigFixture(t)
	_, err := svc.Rollback(context.Background(), appID, domain.ConfigTypeDefault, 9, false)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，期望包装了 domain.ErrNotFound", err)
	}
}

func mustSave(t *testing.T, svc *service.ConfigService, appID uuid.UUID, fields map[string]domain.ConfigField) {
	t.Helper()
	if _, err := svc.Save(context.Background(), appID, domain.ConfigTypeDefault, fields, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/service -run TestRollback -v`
Expected: 编译失败，`svc.Rollback undefined`

- [ ] **Step 3: 写实现**

在 `internal/service/config.go` 里追加：

```go
// Rollback 把 seq 那一版的 fields 复制成一个新版本，返回新版本号。
//
// 复制而不是删除：v7 出了问题回滚到 v6，产出的是 v8，v7 原样留在历史里。
// 这让"回滚本身"也可被回滚，审计链完整。
//
// 走 Save 而不是直接 INSERT ... SELECT：修剪、推送、以及"一次保存 = 一个
// 版本"这套语义只该有一处实现。多出来的代价只是把 fields 在进程内绕一圈，
// 一份几十项的 map，可以忽略。
func (s *ConfigService) Rollback(
	ctx context.Context, appID uuid.UUID, typ string, seq int64, push bool,
) (int64, error) {
	old, err := s.Version(ctx, appID, typ, seq)
	if err != nil {
		return 0, err
	}
	return s.Save(ctx, appID, typ, old.Fields, push)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/service -run TestRollback -v`
Expected: PASS

- [ ] **Step 5: 跑整个 service 包，确认没碰坏既有测试**

Run: `./scripts/test.sh ./internal/service`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/service/config.go internal/service/config_test.go
git commit -m "feat(config): 回滚——复制旧版本往前追加，不删历史"
```

---

## Task 5: 配置变更的 Redis 广播

**Files:**
- Create: `internal/store/config.go`
- Test: `internal/store/config_test.go`

**Interfaces:**
- Consumes: Task 3 的 `service.ConfigPublisher` 接口（本任务提供它的生产实现）
- Produces:
  - `type store.ConfigSignal struct { Gap bool; AppID uuid.UUID; Type string; Seq int64 }`
  - `func store.NewConfigPublisher(rdb *redis.Client) *ConfigPublisher`
  - `func (p *ConfigPublisher) Publish(ctx context.Context, appID uuid.UUID, typ string, seq int64) error` —— 签名与 `service.ConfigPublisher` 接口一致
  - `func (p *ConfigPublisher) Subscribe(ctx context.Context) (<-chan ConfigSignal, func(), error)`

**照着 `internal/store/revoke.go` 写。** 它已经把 go-redis 订阅的两个坑处理干净了：`sub.Receive(ctx)` 阻塞到订阅确认（否则确认前发布的消息会丢）、`ctx` 取消必须显式转成 `sub.Close()`（reader goroutine 跑在 `context.TODO()` 上，只有 `Close()` 能让它退出）。**照抄那两段，连注释一起。**

`ConfigSignal.Gap` 对应 `RevokeSignal` 里的 `RevokeSignalGap`：go-redis 会静默重连并重发 `SUBSCRIBE`，那个窗口里发布的事件对本实例永久丢失。撤销那边的处理是让 SDK 丢弃全部缓存；配置这边只要让 SDK **重拉**即可——它本来就是拉全量，不需要"丢弃"这个动作。

- [ ] **Step 1: 写失败的测试**

创建 `internal/store/config_test.go`：

```go
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestConfigPublishSubscribe(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	pub := store.NewConfigPublisher(rdb)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, closeSub, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	defer closeSub()

	appID := uuid.New()
	if err := pub.Publish(ctx, appID, domain.ConfigTypeWeb, 7); err != nil {
		t.Fatalf("广播失败: %v", err)
	}

	select {
	case sig := <-ch:
		if sig.Gap {
			t.Fatal("这是一条正常事件，不该带 Gap")
		}
		if sig.AppID != appID {
			t.Fatalf("AppID = %s，期望 %s", sig.AppID, appID)
		}
		if sig.Type != domain.ConfigTypeWeb {
			t.Fatalf("Type = %q，期望 %q", sig.Type, domain.ConfigTypeWeb)
		}
		if sig.Seq != 7 {
			t.Fatalf("Seq = %d，期望 7", sig.Seq)
		}
	case <-ctx.Done():
		t.Fatal("等待事件超时")
	}
}

// 没有订阅者不算错误：推送只是把生效延迟从"下次重启"压到近乎实时的加速
// 手段，值已经落库了，那才是权威事实。与 RevokePublisher 同一逻辑。
func TestConfigPublishNoSubscriberIsNotAnError(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	pub := store.NewConfigPublisher(rdb)

	if err := pub.Publish(context.Background(), uuid.New(), domain.ConfigTypeDefault, 1); err != nil {
		t.Fatalf("无订阅者时广播不该报错: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/store -run TestConfigPublish -v`
Expected: 编译失败，`undefined: store.NewConfigPublisher`

- [ ] **Step 3: 写实现**

创建 `internal/store/config.go`。**先打开 `internal/store/revoke.go`，把 `Subscribe` 那段的结构与注释照搬过来**（订阅确认、ctx→Close 的看门狗、重订阅转 Gap），只把载荷类型换掉：

```go
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// configChannel 是配置变更的 Redis 广播频道。
// fp 的每个实例都订阅它，再通过各自持有的 gRPC Watch 流推给 SDK。
const configChannel = "fp:config"

// ConfigSignal 是订阅流上的一条信号。
type ConfigSignal struct {
	// Gap 为 true 表示事件流出现了缺口：订阅刚刚重建，期间发布的事件已
	// 永久丢失，且无法知道丢了哪些。此时 AppID / Type / Seq 全部无意义，
	// 消费方应当让**所有** SDK 重拉配置。
	//
	// 这是悲观判断不是确知：重建时未必真的丢了东西。但 fp 无从分辨，
	// 只能按最坏情况处理。与 RevokeSignalGap 是同一件事的两种后果——
	// 撤销那边要求丢弃全部缓存，配置这边只要求重拉。
	Gap bool

	AppID uuid.UUID `json:"appId"`
	// Type 是分区，取值见 domain.ConfigType*。
	Type string `json:"type"`
	// Seq 是新版本号，仅用于日志与排障。SDK 收到信号后拉的是"当前版本"
	// 而不是"第 Seq 版"——这让丢失一条信号的后果被下一条信号自动修复。
	Seq int64 `json:"seq"`
}

// ConfigPublisher 广播配置变更。
type ConfigPublisher struct {
	rdb *redis.Client
}

// NewConfigPublisher 构造 ConfigPublisher。
func NewConfigPublisher(rdb *redis.Client) *ConfigPublisher {
	return &ConfigPublisher{rdb: rdb}
}

// Publish 广播一次配置变更。
//
// 没有订阅者时不算错误：值已经落库，那是权威事实；推送只是把生效延迟从
// "下次重启"压到近乎实时的加速手段。
func (p *ConfigPublisher) Publish(ctx context.Context, appID uuid.UUID, typ string, seq int64) error {
	raw, err := json.Marshal(ConfigSignal{AppID: appID, Type: typ, Seq: seq})
	if err != nil {
		return fmt.Errorf("store: 序列化配置变更事件: %w", err)
	}
	if err := p.rdb.Publish(ctx, configChannel, raw).Err(); err != nil {
		return fmt.Errorf("store: 广播配置变更事件: %w", err)
	}
	return nil
}

// Subscribe 订阅配置变更。返回的 channel 在 ctx 取消或调用 close 时关闭。
//
// 实现结构与 RevokePublisher.Subscribe 完全一致（订阅确认、ctx→Close 的
// 看门狗、把重订阅转成 Gap），照着它写，两处的坑是同一批。
func (p *ConfigPublisher) Subscribe(ctx context.Context) (<-chan ConfigSignal, func(), error) {
	// —— 照搬 revoke.go 的 Subscribe，把 RevokeSignal 换成 ConfigSignal、
	//    RevokeSignalGap 换成 ConfigSignal{Gap: true}、revokeChannel 换成
	//    configChannel。解析失败时打 WARN 并跳过那一条，不要中断订阅。
	_ = slog.Default
	panic("按 revoke.go 的 Subscribe 实现")
}
```

**实现者注意**：上面那个 `panic` 是占位，实现时必须替换成真正照搬过来的代码。`internal/store` 不受 `sdk/arch_test.go` 的"不得 panic"约束，但留一个 panic 在生产代码里显然不行——**这一步没删干净的话，Step 4 的测试会直接 panic 而不是失败**，很好认。

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/store -run TestConfigPublish -v`
Expected: PASS

- [ ] **Step 5: 补 Gap 的测试**

照着 `internal/store/revoke_test.go` 里的 `TestSubscribeSurfacesResubscribeAsGap` 写一条 `TestConfigSubscribeSurfacesResubscribeAsGap`——用同样的手法触发重订阅，断言收到 `ConfigSignal{Gap: true}`。

Run: `./scripts/test.sh ./internal/store -run TestConfigSubscribeSurfacesResubscribeAsGap -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/store/config.go internal/store/config_test.go
git commit -m "feat(config): 配置变更的 Redis 广播与订阅"
```

---

## Task 6: proto 与 gRPC GetConfig

**Files:**
- Create: `proto/fp/v1/config.proto`
- Modify: `proto/fp/v1/auth.proto`
- Create: `internal/grpcapi/config_service.go`
- Test: `internal/grpcapi/config_service_test.go`

**Interfaces:**
- Consumes: Task 2/3 的 `service.ConfigService`（`Current`）
- Produces:
  - `fpv1.ConfigServiceClient` / `fpv1.ConfigServiceServer`，`GetConfig(GetConfigRequest{Type}) → GetConfigResponse{Version, Values}`
  - `fpv1.WatchResponse_ConfigChanged` 分支与 `fpv1.ConfigChanged{Type, Version}`
  - `func grpcapi.newConfigServer(cfgs *service.ConfigService, apps appLookup) fpv1.ConfigServiceServer`（包内构造，由 `New` 装配）

- [ ] **Step 1: 写 proto**

创建 `proto/fp/v1/config.proto`：

```protobuf
syntax = "proto3";

package fp.v1;

option go_package = "github.com/basicfu/fp/sdk/gen/fp/v1;fpv1";

// ConfigService 是 SDK 读取配置的全部契约。
//
// 只有一个 RPC：配置项由人在控制台创建，SDK **不上报** schema
// （设计文档 4.3）。将来要加上报是纯增量，这里再添一个 rpc 即可。
//
// 认证方式与 AuthService 相同：metadata 带 fp-app-id 与 fp-app-secret，
// 由 internal/grpcapi/auth_interceptor.go 统一校验。
service ConfigService {
  // GetConfig 拉取该应用某个分区当前的全部配置值。
  rpc GetConfig(GetConfigRequest) returns (GetConfigResponse);
}

message GetConfigRequest {
  // type 是分区，取值 "DEFAULT" 或 "WEB"。
  //
  // 必填且不接受空串：分区之间同名 key 是**不同的配置项**，没有"不传就是
  // 全部"这种语义——那会逼调用方回答"撞了算谁的"，而这个问题不该存在。
  string type = 1;
}

message GetConfigResponse {
  // version 是该分区的 config.seq。0 表示该分区还没有任何版本。
  int64 version = 1;

  // values 是一个 JSON 对象 {key: value}，**只含已配置的项**——
  // fields 里 value 为 JSON null 的（"未配置"）不出现在这里，
  // 这样 SDK 算 missing 就是"struct 里有、values 里没有"，一句话。
  //
  // 刻意用 JSON 字符串而不是 map<string,string>：值是 JSON 原生类型
  // （数字、布尔、数组、对象），拆成字符串 map 会把它们全退化成待解析的
  // 字符串，SDK 就得自己实现一遍类型转换——而 encoding/json 本来就会。
  string values = 2;
}
```

在 `proto/fp/v1/auth.proto` 的 `WatchResponse` 里加一个分支（注意 oneof 的字段号接着往下排，现有最大是 3）：

```protobuf
    // config_changed 表示某个分区的配置变了，SDK 应当重拉。
    ConfigChanged config_changed = 4;
```

并在同一个文件末尾（或 config.proto 里，二选一——放 auth.proto 更省一次 import）加：

```protobuf
// ConfigChanged 是一次配置变更的通知。
//
// 只推信号不推内容：SDK 收到后拉全量。配置项就几十条，拉全量比处理增量的
// 乱序、丢失、部分应用简单得多——与 PolicyChanged 同一范式。
message ConfigChanged {
  // type 指出哪个分区变了。一次保存只动一个分区（版本序列是分区内自增的），
  // 所以正常情况下这里是单值。
  //
  // **空串表示"分区未知，请重拉全部绑定"**：fp 侧的 Redis 订阅重建时会漏读
  // 事件且不知道漏了哪些（见 store.ConfigSignal.Gap），只能让 SDK 全部重拉。
  // 这是配置侧对应 WatchPurge 的那条兜底，只是配置不需要"丢弃全部"。
  string type = 1;
  // version 仅用于日志与排障。SDK 拉的是"当前版本"而不是"第 version 版"——
  // 这让丢失一条通知的后果被下一条自动修复。
  int64 version = 2;
}
```

- [ ] **Step 2: 生成代码**

Run: `./scripts/gen.sh`
Expected: `生成完成。`，`sdk/gen/fp/v1/config.pb.go`、`config_grpc.pb.go` 出现，`auth.pb.go` 里多出 `WatchResponse_ConfigChanged`

- [ ] **Step 3: 写失败的测试**

创建 `internal/grpcapi/config_service_test.go`。照着 `internal/grpcapi/auth_service_test.go` 的方式起一个真实的 gRPC 服务端与客户端（复用那里已有的测试脚手架，不要另起一套）：

```go
package grpcapi_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// GetConfig 只返回已配置的项：value 为 JSON null 的"未配置"项不出现，
// SDK 因此可以把 missing 算成"struct 里有、values 里没有"。
//
// 【辨别力】必须同时造出"已配置"和"未配置"两种项：只造一种的话，
// 一个不做过滤、把 null 也吐出去的实现照样会绿。
func TestGetConfigOmitsUnsetFields(t *testing.T) {
	env := newTestEnv(t) // 复用 auth_service_test.go 里的脚手架
	ctx := context.Background()

	if _, err := env.configs.Save(ctx, env.appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"fee_rate": {Type: domain.ConfigValueFloat, Value: json.RawMessage(`0.02`)},
		"api_key":  {Type: domain.ConfigValueString, Value: json.RawMessage(`null`)},
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	resp, err := env.configClient.GetConfig(env.authedCtx(ctx), &fpv1.GetConfigRequest{
		Type: domain.ConfigTypeDefault,
	})
	if err != nil {
		t.Fatalf("GetConfig 失败: %v", err)
	}
	if resp.GetVersion() != 1 {
		t.Fatalf("version = %d，期望 1", resp.GetVersion())
	}

	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resp.GetValues()), &values); err != nil {
		t.Fatalf("values 不是合法 JSON 对象: %v", err)
	}
	if got := string(values["fee_rate"]); got != "0.02" {
		t.Fatalf("fee_rate = %s，期望 0.02", got)
	}
	if _, ok := values["api_key"]; ok {
		t.Fatal("未配置的 api_key 不该出现在 values 里")
	}
}

// 分区隔离穿到 gRPC 出口。
// 【辨别力】两个分区的值必须不同，否则"分区对了"和"压根没分区"同结果。
func TestGetConfigIsolatesPartitions(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	mustSaveTyped(t, env, domain.ConfigTypeDefault, `"后端"`)
	mustSaveTyped(t, env, domain.ConfigTypeWeb, `"前端"`)

	for _, c := range []struct{ typ, want string }{
		{domain.ConfigTypeDefault, `"后端"`},
		{domain.ConfigTypeWeb, `"前端"`},
	} {
		resp, err := env.configClient.GetConfig(env.authedCtx(ctx), &fpv1.GetConfigRequest{Type: c.typ})
		if err != nil {
			t.Fatalf("%s: GetConfig 失败: %v", c.typ, err)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal([]byte(resp.GetValues()), &values); err != nil {
			t.Fatalf("%s: values 不是合法 JSON: %v", c.typ, err)
		}
		if got := string(values["site.title"]); got != c.want {
			t.Fatalf("%s: site.title = %s，期望 %s", c.typ, got, c.want)
		}
	}
}

func TestGetConfigRejectsUnknownPartition(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.configClient.GetConfig(env.authedCtx(context.Background()),
		&fpv1.GetConfigRequest{Type: "MOBILE"})
	if err == nil {
		t.Fatal("未知分区应当被拒绝")
	}
	// 状态码走既有的 StatusFrom 映射，与 HTTP 层的 400 一一对应。
	assertStatusCode(t, err, codes.InvalidArgument)
}

// 该分区一个版本都没有时返回 version=0 与空对象，不是错误——
// "还没配过任何东西"是正常状态，SDK 会据此把全部字段算进 missing。
func TestGetConfigEmptyPartition(t *testing.T) {
	env := newTestEnv(t)
	resp, err := env.configClient.GetConfig(env.authedCtx(context.Background()),
		&fpv1.GetConfigRequest{Type: domain.ConfigTypeDefault})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if resp.GetVersion() != 0 {
		t.Fatalf("version = %d，期望 0", resp.GetVersion())
	}
	if resp.GetValues() != "{}" {
		t.Fatalf("values = %q，期望 {}", resp.GetValues())
	}
}
```

**实现者注意**：`newTestEnv` / `authedCtx` / `assertStatusCode` / `mustSaveTyped` 这几个辅助——先读 `internal/grpcapi/auth_service_test.go`，那里已经有等价的脚手架（起服务端、带 app 凭据的 ctx、断言状态码）。**扩展它而不是另写一套**，并给 env 加上 `configs *service.ConfigService` 与 `configClient fpv1.ConfigServiceClient` 两个字段。`mustSaveTyped` 自己写一个三行的辅助：按给定分区存一个 `site.title`。

- [ ] **Step 4: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/grpcapi -run TestGetConfig -v`
Expected: 编译失败（`env.configClient` 不存在）

- [ ] **Step 5: 写实现**

创建 `internal/grpcapi/config_service.go`：

```go
package grpcapi

import (
	"context"
	"encoding/json"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// configServer 实现 fpv1.ConfigServiceServer。
type configServer struct {
	fpv1.UnimplementedConfigServiceServer
	cfgs *service.ConfigService
	apps appLookup
}

func newConfigServer(cfgs *service.ConfigService, apps appLookup) *configServer {
	return &configServer{cfgs: cfgs, apps: apps}
}

// GetConfig 返回该应用某分区当前的全部**已配置**的值。
//
// 与 Watch 一样走 GetActiveByAppID：停用的应用不该还能拉到配置，
// 否则 status 又变成一个没人读的死开关（Watch 那里踩过这个）。
func (s *configServer) GetConfig(ctx context.Context, req *fpv1.GetConfigRequest) (*fpv1.GetConfigResponse, error) {
	appIDStr, err := callerAppID(ctx)
	if err != nil {
		return nil, err
	}
	app, err := s.apps.GetActiveByAppID(ctx, appIDStr)
	if err != nil {
		return nil, StatusFrom(err)
	}

	cfg, err := s.cfgs.Current(ctx, app.ID, req.GetType())
	if err != nil {
		return nil, StatusFrom(err)
	}

	// 只装已配置的项。未配置（value 为 JSON null）留在库里是为了让控制台
	// 有那一行待填，但对 SDK 来说它等于不存在——SDK 的 missing 就是
	// "struct 里有、这里没有"。
	values := make(map[string]json.RawMessage, len(cfg.Fields))
	for k, f := range cfg.Fields {
		if f.IsSet() {
			values[k] = f.Value
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.GetConfigResponse{Version: cfg.Seq, Values: string(raw)}, nil
}

// 编译期断言：分区常量必须与 domain 的一致。这两处分处 proto 注释与 Go
// 常量，改一处忘另一处不会有任何编译错误。
var _ = [...]string{domain.ConfigTypeDefault, domain.ConfigTypeWeb}
```

**`appLookup`**：`auth_service.go` 里已经有一个只含 `GetActiveByAppID` 的接口（见它对 `*service.ApplicationService` 的用法）。复用那个接口名，不要新定义一个同形状的。

在 `internal/grpcapi/server.go` 的 `Deps` 里加 `Configs *service.ConfigService`，并在 `New` 里注册：

```go
	fpv1.RegisterConfigServiceServer(s, newConfigServer(d.Configs, d.Apps))
```

- [ ] **Step 6: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/grpcapi -run TestGetConfig -v`
Expected: PASS，四条全绿

- [ ] **Step 7: 变异验证"只返回已配置的项"**

把 `GetConfig` 里的 `if f.IsSet()` 判断去掉（无条件装进 values）。

Run: `./scripts/test.sh ./internal/grpcapi -run TestGetConfigOmitsUnsetFields -v`
Expected: **FAIL**，报"未配置的 api_key 不该出现在 values 里"。确认后改回来。

- [ ] **Step 8: 提交**

```bash
git add proto/ sdk/gen/ internal/grpcapi/config_service.go internal/grpcapi/config_service_test.go internal/grpcapi/server.go
git commit -m "feat(config): ConfigService 的 proto 与 gRPC GetConfig"
```

---

## Task 7: 推送接入 Watch 长流

**Files:**
- Create: `internal/grpcapi/config_hub.go`
- Test: `internal/grpcapi/config_hub_test.go`
- Modify: `internal/grpcapi/auth_service.go`（`Watch` 增订配置事件）
- Modify: `internal/grpcapi/server.go`（`Deps` 与生命周期）
- Modify: `cmd/fp/main.go`（装配）

**Interfaces:**
- Consumes: Task 5 的 `store.ConfigSignal` / `store.ConfigPublisher`
- Produces:
  - `type grpcapi.ConfigEvent struct { Type string; Seq int64 }` —— `Type` 为空串表示"分区未知，重拉全部"
  - `func grpcapi.NewConfigHub(pub *store.ConfigPublisher) *ConfigHub`
  - `func (h *ConfigHub) Run(ctx context.Context) error` / `Ready() <-chan struct{}` / `Subscribe(appID uuid.UUID) (<-chan ConfigEvent, func())` / `Close()`
  - `grpcapi.Deps` 多两个字段：`Configs *service.ConfigService`（Task 6 已加）、`ConfigPub *store.ConfigPublisher`

**先读 `internal/grpcapi/watch.go` 的 `RevokeHub`。** `ConfigHub` 是它的简化版——同样的"一份 Redis 订阅扇出给进程内所有流"，同样的 `ready` 语义（订阅真正建立后才关闭，光靠"建流没报错"不够），但**不需要 `Purge`**：配置的缺口靠"SDK 重连后重拉"自动补上。

**缓冲满时摘掉订阅者，与 `RevokeHub` 一致。** 这不是偷懒——摘掉 → 流结束 → SDK 重连 → 收到 `ready` → 重拉全部配置（SDK 侧的约束 5）。丢事件的后果被这条链路完整兜住，所以这里可以放心摘。反过来若选"丢弃这一条信号"就有洞了：待发的信号可能是 `WEB` 分区、被丢的是 `DEFAULT`，SDK 只会重拉 WEB。

- [ ] **Step 1: 写失败的测试**

创建 `internal/grpcapi/config_hub_test.go`：

```go
package grpcapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
)

// 一条信号只送给它自己那个应用的订阅者。
// 【辨别力】必须有第二个应用的订阅者在场并断言它**没收到**，
// 否则一个"广播给所有人"的实现照样会绿。
func TestConfigHubRoutesByApp(t *testing.T) {
	h, signals := newTestConfigHub(t)

	appA, appB := uuid.New(), uuid.New()
	chA, stopA := h.Subscribe(appA)
	defer stopA()
	chB, stopB := h.Subscribe(appB)
	defer stopB()

	signals <- store.ConfigSignal{AppID: appA, Type: domain.ConfigTypeWeb, Seq: 3}

	select {
	case ev := <-chA:
		if ev.Type != domain.ConfigTypeWeb || ev.Seq != 3 {
			t.Fatalf("A 收到 %+v，期望 {WEB 3}", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("A 没收到事件")
	}

	select {
	case ev := <-chB:
		t.Fatalf("B 不该收到任何事件，却收到了 %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// Gap 广播给全部订阅者，且 Type 为空串——语义是"分区未知，重拉全部"。
func TestConfigHubFansOutGapToEveryone(t *testing.T) {
	h, signals := newTestConfigHub(t)

	chA, stopA := h.Subscribe(uuid.New())
	defer stopA()
	chB, stopB := h.Subscribe(uuid.New())
	defer stopB()

	signals <- store.ConfigSignal{Gap: true}

	for name, ch := range map[string]<-chan ConfigEvent{"A": chA, "B": chB} {
		select {
		case ev := <-ch:
			if ev.Type != "" {
				t.Fatalf("%s 收到的 Type = %q，Gap 事件必须是空串（重拉全部）", name, ev.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s 没收到 Gap 事件", name)
		}
	}
}

// 缓冲满时摘掉订阅者：channel 被关闭，Watch handler 因此正常结束，
// SDK 重连后走 ready 重拉，不会漏配置。
func TestConfigHubDropsSaturatedSubscriber(t *testing.T) {
	h, signals := newTestConfigHub(t)
	appID := uuid.New()
	ch, stop := h.Subscribe(appID)
	defer stop()

	// 灌满缓冲再多推几条，且**不消费**。
	for i := 0; i < configBufferSize+5; i++ {
		signals <- store.ConfigSignal{AppID: appID, Type: domain.ConfigTypeDefault, Seq: int64(i)}
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel 已关闭，正是期望
			}
		case <-deadline:
			t.Fatal("缓冲满之后订阅者仍未被摘掉")
		}
	}
}

// newTestConfigHub 返回一个只吃假信号的 hub，完全绕开 Redis。
// 与 watch.go 的 newTestHubWithFakeSignals 同一手法：测的是分发逻辑本身，
// 不是 go-redis 的重连行为——后者既慢又不稳定。
func newTestConfigHub(t *testing.T) (*ConfigHub, chan store.ConfigSignal) {
	t.Helper()
	h := newConfigHub()
	signals := make(chan store.ConfigSignal, 64)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); h.Close() })
	go h.run(ctx, signals)
	return h, signals
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/grpcapi -run TestConfigHub -v`
Expected: 编译失败，`undefined: newConfigHub`

- [ ] **Step 3: 写实现**

创建 `internal/grpcapi/config_hub.go`，照着 `watch.go` 的 `RevokeHub` 写：

```go
package grpcapi

import (
	"context"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/store"
)

// configBufferSize 是每个订阅者的缓冲深度。
//
// 配置变更是极低频事件（人点保存），缓冲只用来吸收 gRPC 写入的瞬时抖动。
// 满了就摘掉订阅者——见 fanout 的说明。
const configBufferSize = 8

// ConfigEvent 是推给一条 Watch 流的配置事件。
type ConfigEvent struct {
	// Type 是变了的分区。**空串表示"分区未知，请重拉全部绑定"**，
	// 来源是 store.ConfigSignal.Gap（Redis 订阅重建，漏读且不知道漏了哪些）。
	Type string
	// Seq 仅用于日志与排障：SDK 拉的是"当前版本"不是"第 Seq 版"。
	Seq int64
}

// configSubscriber 是 ConfigHub 对信号源的全部依赖。拆出接口是为了测试
// 能换上一个可精确控制的假实现（同 revokeSubscriber）。
type configSubscriber interface {
	Subscribe(ctx context.Context) (<-chan store.ConfigSignal, func(), error)
}

// ConfigHub 把一份 Redis 配置订阅扇出给进程内所有 Watch 流。
type ConfigHub struct {
	pub configSubscriber

	mu     sync.RWMutex
	next   uint64
	subs   map[uint64]*configSub
	closed bool

	ready chan struct{}
}

type configSub struct {
	appID uuid.UUID
	ch    chan ConfigEvent
}

func newConfigHub() *ConfigHub {
	return &ConfigHub{subs: make(map[uint64]*configSub), ready: make(chan struct{})}
}

// NewConfigHub 构造 ConfigHub。
func NewConfigHub(pub *store.ConfigPublisher) *ConfigHub {
	h := newConfigHub()
	h.pub = pub
	return h
}

// Run / Ready / Subscribe / Close 的结构与 RevokeHub 完全一致，照着写：
//   - Run：订阅 → 关闭 ready → run(ctx, signals)
//   - Ready：订阅**真正建立**后才关闭。光靠"建流没报错"不够——
//     流建立了但服务端还没订上的那段时间里，配置变更会丢，而 SDK 却以为
//     推送可用。
//   - Subscribe：登记一个订阅者，返回 channel 与摘除函数（用 sync.Once 包住）
//   - Close：写锁下关闭全部 channel，让所有 Watch handler 返回；
//     它存在的唯一理由是让 grpc.Server.GracefulStop 能返回。

// fanout 把一条信号分发给相关订阅者。
//
// **缓冲满就摘掉订阅者**（关掉它的 channel 并从表里删除），不是丢弃这一条
// 信号。丢弃是有洞的：待发的可能是 WEB 分区、被丢的是 DEFAULT，SDK 只会
// 重拉 WEB。而摘掉的后果被完整兜住——流结束 → SDK 重连 → 收到 ready →
// 重拉全部配置。与 RevokeHub 缓冲满时的处理同一逻辑。
func (h *ConfigHub) fanout(sig store.ConfigSignal) {
	// 实现要点：
	//   1. Gap 信号发给**全部**订阅者，ConfigEvent{Type: ""}
	//   2. 普通信号只发给 sig.AppID 匹配的订阅者
	//   3. 非阻塞发送；default 分支里关闭 channel、从 subs 删除，并打一条
	//      WARN（带 appID），说明这条流被摘掉了、SDK 会重连
	//   4. 摘除要在写锁下做，与读锁互斥，避免"向已关闭的 channel 发送"
	_ = slog.Default
	panic("按 RevokeHub.fanout 实现，注意上面四点")
}
```

**实现者注意**：那个 `panic` 是占位，必须替换。`internal/grpcapi` 不受 `sdk/` 的"不得 panic"约束，但留在生产代码里显然不行——没删干净的话 Step 4 的测试会直接 panic，很好认。

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/grpcapi -run TestConfigHub -v`
Expected: PASS，三条全绿

- [ ] **Step 5: 提交 hub**

```bash
git add internal/grpcapi/config_hub.go internal/grpcapi/config_hub_test.go
git commit -m "feat(config): ConfigHub 把 Redis 配置订阅扇出给 Watch 流"
```

- [ ] **Step 6: 写 Watch 分支的失败测试**

追加到 `internal/grpcapi/config_service_test.go`：

```go
// 保存并选择推送 → Watch 流上收到 ConfigChanged。
func TestWatchDeliversConfigChanged(t *testing.T) {
	env := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := env.authClient.Watch(env.authedCtx(ctx))
	if err != nil {
		t.Fatalf("建流失败: %v", err)
	}
	// 先吃掉 ready，确保服务端已经订上，之后的变更不会漏推。
	if msg, err := stream.Recv(); err != nil || msg.GetReady() == nil {
		t.Fatalf("首条消息应当是 ready，得到 %v / %v", msg, err)
	}

	if _, err := env.configs.Save(ctx, env.appID, domain.ConfigTypeWeb, map[string]domain.ConfigField{
		"site.title": {Type: domain.ConfigValueString, Value: json.RawMessage(`"商城"`)},
	}, true); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("收流失败: %v", err)
		}
		cc := msg.GetConfigChanged()
		if cc == nil {
			continue // 可能先来别的事件类型
		}
		if cc.GetType() != domain.ConfigTypeWeb {
			t.Fatalf("Type = %q，期望 WEB", cc.GetType())
		}
		return
	}
}

// 「仅落库」不推送。
// 【辨别力】两条腿都要断言——这里断言"没推"，Task 11 的 SDK 测试断言
// "新起一次 Bind 能拿到新值"。只断言前者的话，一个根本没存的实现也会绿。
func TestWatchSilentWhenPushDisabled(t *testing.T) {
	env := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := env.authClient.Watch(env.authedCtx(ctx))
	if err != nil {
		t.Fatalf("建流失败: %v", err)
	}
	if msg, err := stream.Recv(); err != nil || msg.GetReady() == nil {
		t.Fatalf("首条消息应当是 ready，得到 %v / %v", msg, err)
	}

	if _, err := env.configs.Save(ctx, env.appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"n": {Type: domain.ConfigValueInt, Value: json.RawMessage(`1`)},
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 保存后不该有任何 ConfigChanged。用一个短超时来观测"什么都没发生"。
	done := make(chan *fpv1.WatchResponse, 1)
	go func() {
		msg, err := stream.Recv()
		if err == nil {
			done <- msg
		}
		close(done)
	}()
	select {
	case msg := <-done:
		if msg != nil && msg.GetConfigChanged() != nil {
			t.Fatal("选了「仅落库」却推送了 ConfigChanged")
		}
	case <-time.After(800 * time.Millisecond):
		// 什么都没来，正是期望
	}
}
```

- [ ] **Step 7: 改 Watch handler**

在 `internal/grpcapi/auth_service.go` 的 `Watch` 里，紧挨着 `s.hub.Subscribe(app.ID)` 增订配置事件，**并且在 `stream.Send(ready)` 之前完成订阅**——理由与撤销那条注释一模一样：ready 一发，客户端就认为推送通道健康，此刻还没订上的话这段时间的变更全丢。

```go
	events, unsubscribe := s.hub.Subscribe(app.ID)
	defer unsubscribe()
	configEvents, unsubscribeConfig := s.configHub.Subscribe(app.ID)
	defer unsubscribeConfig()
```

select 里加一个 case：

```go
		case ev, ok := <-configEvents:
			if !ok {
				// 缓冲满被摘掉，或 hub 关闭。都是正常结束——SDK 重连后
				// 会在收到 ready 时重拉配置，不会漏。
				return nil
			}
			if err := stream.Send(&fpv1.WatchResponse{
				Event: &fpv1.WatchResponse_ConfigChanged{
					ConfigChanged: &fpv1.ConfigChanged{Type: ev.Type, Version: ev.Seq},
				},
			}); err != nil {
				return err
			}
```

`authServer` 结构体加 `configHub *ConfigHub` 字段，构造处一并传入。

- [ ] **Step 8: 装配**

`internal/grpcapi/server.go`：
- `Deps` 加 `ConfigPub *store.ConfigPublisher`
- `New` 里 `h := NewConfigHub(d.ConfigPub)`，存进 `Server`
- `ServeWhenReady`（或等价的生命周期方法）里，**与 `RevokeHub` 并列地** 跑 `configHub.Run(ctx)` 并等它的 `Ready()`。照着已有的编排写，那里的注释解释了为什么顺序不能反
- `Server.Stop` / `GracefulStop` 路径上调 `configHub.Close()`

`cmd/fp/main.go`：

```go
	configPub := store.NewConfigPublisher(rdb)
	configSvc := service.NewConfigService(pool, configPub)
```

分别注入 `grpcapi.Deps{Configs: configSvc, ConfigPub: configPub, ...}` 与 `httpapi.Deps{Configs: configSvc, ...}`（HTTP 那半在 Task 8）。

- [ ] **Step 9: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/grpcapi`
Expected: PASS，含既有的全部 Watch 测试

- [ ] **Step 10: 提交**

```bash
git add internal/grpcapi/ cmd/fp/main.go
git commit -m "feat(config): ConfigChanged 接入 Watch 长流并完成装配"
```

---

## Task 8: 控制台的 HTTP 路由

**Files:**
- Create: `internal/httpapi/config.go`
- Test: `internal/httpapi/config_test.go`
- Modify: `internal/httpapi/router.go`

**Interfaces:**
- Consumes: Task 2/3/4 的 `service.ConfigService`
- Produces（前端的手工镜像照这个写）：
  - `GET  /admin/api/applications/{id}/config?type=DEFAULT` → `{"seq":1,"fields":{"k":{"type":"int","desc":"","value":3}}}`
  - `PUT  /admin/api/applications/{id}/config` ← `{"type":"DEFAULT","fields":{...},"push":true}` → `{"seq":2}`
  - `GET  /admin/api/applications/{id}/config/versions?type=DEFAULT` → `[{"seq":2,"createdAt":1757000000}]`
  - `GET  /admin/api/applications/{id}/config/versions/{seq}?type=DEFAULT` → 同第一条的形状
  - `POST /admin/api/applications/{id}/config/rollback` ← `{"type":"DEFAULT","seq":1,"push":false}` → `{"seq":3}`

**PUT 而不是 PATCH**：这个接口是该分区配置的**全量替换**，与 `/applications/{id}/session` 同一语义。新建、改值、改类型、删除全走它——用 PATCH 命名等于承诺了一个做不到的局部更新语义（`router.go` 里那条注释记着上次踩的坑）。

- [ ] **Step 1: 写失败的测试**

创建 `internal/httpapi/config_test.go`。**先读 `internal/httpapi/application_test.go`**，复用那里已有的建 router、带管理员 cookie 发请求的辅助，不要另起一套。

```go
package httpapi_test

import (
	"net/http"
	"testing"
)

// 往返：PUT 存进去的东西，GET 能原样拿回来。
func TestConfigRoundTrip(t *testing.T) {
	env := newAdminEnv(t) // 复用 application_test.go 的脚手架
	appID := env.createApp(t, "商城", "shop")

	body := `{"type":"DEFAULT","push":false,"fields":{
		"fee_rate":{"type":"float","desc":"手续费率","value":0.02},
		"api_key":{"type":"string","desc":"上游密钥","value":null}
	}}`
	res := env.do(t, http.MethodPut, "/admin/api/applications/"+appID+"/config", body)
	assertStatus(t, res, http.StatusOK)

	got := env.getJSON(t, "/admin/api/applications/"+appID+"/config?type=DEFAULT")
	fields := got["fields"].(map[string]any)
	fee := fields["fee_rate"].(map[string]any)
	if fee["value"] != 0.02 {
		t.Fatalf("fee_rate.value = %v，期望 0.02", fee["value"])
	}
	if fee["desc"] != "手续费率" {
		t.Fatalf("desc = %v，期望 手续费率", fee["desc"])
	}
	// 未配置的项必须**保留在响应里**——它就是控制台上待填的那一行。
	api := fields["api_key"].(map[string]any)
	if api["value"] != nil {
		t.Fatalf("api_key.value = %v，期望 null", api["value"])
	}
}

// 弱转换在路由层也成立：字符串 "3" 存进 int 项返回 200，"abc" 返回 400。
func TestConfigSaveCoercionAndRejection(t *testing.T) {
	env := newAdminEnv(t)
	appID := env.createApp(t, "商城", "shop")
	url := "/admin/api/applications/" + appID + "/config"

	ok := env.do(t, http.MethodPut, url,
		`{"type":"DEFAULT","push":false,"fields":{"n":{"type":"int","desc":"","value":"3"}}}`)
	assertStatus(t, ok, http.StatusOK)

	bad := env.do(t, http.MethodPut, url,
		`{"type":"DEFAULT","push":false,"fields":{"n":{"type":"int","desc":"","value":"abc"}}}`)
	assertStatus(t, bad, http.StatusBadRequest)
}

// 未知分区 400；未知字段（DisallowUnknownFields）也 400。
func TestConfigRejectsBadRequests(t *testing.T) {
	env := newAdminEnv(t)
	appID := env.createApp(t, "商城", "shop")
	url := "/admin/api/applications/" + appID + "/config"

	assertStatus(t, env.do(t, http.MethodPut, url,
		`{"type":"MOBILE","push":false,"fields":{}}`), http.StatusBadRequest)

	assertStatus(t, env.do(t, http.MethodPut, url,
		`{"type":"DEFAULT","push":false,"fields":{},"typo":1}`), http.StatusBadRequest)
}

// 版本列表与回滚。
func TestConfigVersionsAndRollback(t *testing.T) {
	env := newAdminEnv(t)
	appID := env.createApp(t, "商城", "shop")
	url := "/admin/api/applications/" + appID + "/config"

	env.do(t, http.MethodPut, url, `{"type":"DEFAULT","push":false,"fields":{"n":{"type":"int","desc":"","value":1}}}`)
	env.do(t, http.MethodPut, url, `{"type":"DEFAULT","push":false,"fields":{"n":{"type":"int","desc":"","value":2}}}`)

	versions := env.getJSONArray(t, url+"/versions?type=DEFAULT")
	if len(versions) != 2 {
		t.Fatalf("版本数 = %d，期望 2", len(versions))
	}
	// 降序：最新的在最前
	if versions[0].(map[string]any)["seq"] != float64(2) {
		t.Fatalf("第一条 seq = %v，期望 2", versions[0].(map[string]any)["seq"])
	}

	res := env.do(t, http.MethodPost, url+"/rollback", `{"type":"DEFAULT","seq":1,"push":false}`)
	assertStatus(t, res, http.StatusOK)

	cur := env.getJSON(t, url+"?type=DEFAULT")
	if cur["seq"] != float64(3) {
		t.Fatalf("回滚后 seq = %v，期望 3", cur["seq"])
	}
	n := cur["fields"].(map[string]any)["n"].(map[string]any)
	if n["value"] != float64(1) {
		t.Fatalf("回滚后 n = %v，期望 1", n["value"])
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/httpapi -run TestConfig -v`
Expected: 404（路由未注册）或编译失败

- [ ] **Step 3: 写实现**

创建 `internal/httpapi/config.go`：

```go
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type configHandler struct {
	svc *service.ConfigService
}

// configFieldDTO 是 fields 里的一项。
//
// Value 用 json.RawMessage：值可能是数字、布尔、数组、对象中的任何一种，
// 转成 any 再转回来会把整数变成 float64、把字段顺序打乱。原样透传。
type configFieldDTO struct {
	Type  string          `json:"type"`
	Desc  string          `json:"desc"`
	Value json.RawMessage `json:"value"`
}

type configDTO struct {
	Seq    int64                     `json:"seq"`
	Fields map[string]configFieldDTO `json:"fields"`
}

type configVersionDTO struct {
	Seq       int64 `json:"seq"`
	CreatedAt int64 `json:"createdAt"`
}

type saveConfigRequest struct {
	Type   string                    `json:"type"`
	Fields map[string]configFieldDTO `json:"fields"`
	// Push 为 true 立即推送；为 false 就是「仅落库，实例重启后生效」。
	Push bool `json:"push"`
}

type rollbackConfigRequest struct {
	Type string `json:"type"`
	Seq  int64  `json:"seq"`
	Push bool   `json:"push"`
}

type saveConfigResponse struct {
	Seq int64 `json:"seq"`
}

func toConfigDTO(c domain.Config) configDTO {
	// 必须是非 nil 的空 map：nil 会被 encoding/json 编码成 null，
	// 前端 Object.entries(null) 直接抛异常。
	fields := make(map[string]configFieldDTO, len(c.Fields))
	for k, f := range c.Fields {
		fields[k] = configFieldDTO{Type: f.Type, Desc: f.Desc, Value: f.Value}
	}
	return configDTO{Seq: c.Seq, Fields: fields}
}

func toDomainFields(in map[string]configFieldDTO) map[string]domain.ConfigField {
	out := make(map[string]domain.ConfigField, len(in))
	for k, f := range in {
		out[k] = domain.ConfigField{Type: f.Type, Desc: f.Desc, Value: f.Value}
	}
	return out
}

// appIDParam 解析路径里的应用 id。
func appIDParam(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "应用 id 不合法")
	}
	return id, nil
}

func (h *configHandler) get(w http.ResponseWriter, r *http.Request) {
	appID, err := appIDParam(r)
	if err != nil {
		writeError(w, err)
		return
	}
	cfg, err := h.svc.Current(r.Context(), appID, r.URL.Query().Get("type"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toConfigDTO(cfg))
}

func (h *configHandler) save(w http.ResponseWriter, r *http.Request) {
	appID, err := appIDParam(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req saveConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	seq, err := h.svc.Save(r.Context(), appID, req.Type, toDomainFields(req.Fields), req.Push)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saveConfigResponse{Seq: seq})
}

func (h *configHandler) listVersions(w http.ResponseWriter, r *http.Request) {
	appID, err := appIDParam(r)
	if err != nil {
		writeError(w, err)
		return
	}
	vs, err := h.svc.ListVersions(r.Context(), appID, r.URL.Query().Get("type"), 20)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]configVersionDTO, 0, len(vs))
	for _, v := range vs {
		out = append(out, configVersionDTO{Seq: v.Seq, CreatedAt: v.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *configHandler) getVersion(w http.ResponseWriter, r *http.Request) {
	appID, err := appIDParam(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var seq int64
	if _, err := fmt.Sscanf(chi.URLParam(r, "seq"), "%d", &seq); err != nil {
		writeError(w, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "版本号不合法"))
		return
	}
	cfg, err := h.svc.Version(r.Context(), appID, r.URL.Query().Get("type"), seq)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toConfigDTO(cfg))
}

func (h *configHandler) rollback(w http.ResponseWriter, r *http.Request) {
	appID, err := appIDParam(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req rollbackConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	seq, err := h.svc.Rollback(r.Context(), appID, req.Type, req.Seq, req.Push)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saveConfigResponse{Seq: seq})
}
```

import 里补 `"fmt"`。

`internal/httpapi/router.go`：`Deps` 加 `Configs *service.ConfigService`，在 `requireAdmin` 那个 Group 里注册：

```go
			cfgH := &configHandler{svc: d.Configs}
			r.Get("/applications/{id}/config", cfgH.get)
			// PUT 而不是 PATCH：这是该分区配置的**全量替换**（新建、改值、
			// 改类型、删除都走它），与 /applications/{id}/session 同一语义。
			r.Put("/applications/{id}/config", cfgH.save)
			r.Get("/applications/{id}/config/versions", cfgH.listVersions)
			r.Get("/applications/{id}/config/versions/{seq}", cfgH.getVersion)
			r.Post("/applications/{id}/config/rollback", cfgH.rollback)
```

（`cfgH` 与其他 handler 一样在 `NewRouter` 顶部构造一次。）

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/httpapi -run TestConfig -v`
Expected: PASS，四条全绿

- [ ] **Step 5: 跑全量 Go 测试**

Run: `./scripts/test.sh`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/httpapi/ cmd/fp/main.go
git commit -m "feat(config): 控制台的配置中心 HTTP 路由"
```
