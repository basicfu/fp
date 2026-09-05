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
//
// **两个顺序陷阱**：
//  1. testsupport.NewTestDB 每次调用都会 TRUNCATE 全部业务表。所以拿 pool
//     和调 newAppService（它内部又调一次 NewTestDB）都必须发生在建应用
//     **之前**——反过来的话，刚建好的应用会被下一次 TRUNCATE 冲掉，
//     而报错会是一句与真实原因毫无关系的外键失败。
//  2. ApplicationService.Create 返回**三个**值 (*domain.Application, secret, error)，
//     明文密钥只在创建时返回这一次。
//
// newAppService 是 application_test.go 里已有的辅助（同属 package service_test），
// 直接用，不要另建一个 registry。
func newConfigFixture(t *testing.T) (*service.ConfigService, uuid.UUID) {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	apps := newAppService(t)
	app, _, err := apps.Create(context.Background(), "商城", "shop")
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
func (p *ConfigPublisher) Subscribe(ctx context.Context) (<-chan ConfigSignal, func(), error) {
	sub := p.rdb.Subscribe(ctx, configChannel)
	// Receive 会阻塞到订阅确认返回，确保这之后发布的消息不会丢。
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("store: 订阅配置频道: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	// ctx 取消时主动关掉底层订阅，这才是让 reader goroutine 退出的唯一途径：
	// go-redis 的 reader 跑在 context.TODO() 上，它的输出 channel 只在
	// PubSub.Close() 时才关。光靠下面 select 里的 <-ctx.Done() 不够——
	// 完全没有消息流入时，goroutine 会一直阻塞在 range 上。
	go func() {
		<-ctx.Done()
		_ = sub.Close()
	}()

	out := make(chan ConfigSignal, 64)
	go func() {
		defer close(out)

		// resubscribeCount 数的是本循环观察到的 SUBSCRIBE 确认回包，也就是
		// go-redis 重连的次数。这里能看到的每一条都必然来自重连——真正
		// "初次订阅"的那条确认已经被上面 sub.Receive(ctx) 同步读走了。
		resubscribeCount := 0

		for msg := range sub.ChannelWithSubscriptions() {
			var sig ConfigSignal
			switch m := msg.(type) {
			case *redis.Subscription:
				if m.Kind != "subscribe" {
					continue
				}
				resubscribeCount++
				slog.Warn("store: Redis 订阅已重建，期间的配置变更事件已丢失",
					"resubscribeCount", resubscribeCount)
				sig = ConfigSignal{Gap: true}
			case *redis.Message:
				if err := json.Unmarshal([]byte(m.Payload), &sig); err != nil {
					// 跳过这一条，不中断订阅：一条坏消息不该让整个实例
					// 从此收不到任何配置变更。
					slog.Error("store: 解析配置变更事件失败", "err", err)
					continue
				}
			default:
				continue
			}

			select {
			case out <- sig:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, func() { cancel(); _ = sub.Close() }, nil
}
```

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

// Run 订阅 Redis 并把信号扇出，直到 ctx 取消。
func (h *ConfigHub) Run(ctx context.Context) error {
	signals, closeSub, err := h.pub.Subscribe(ctx)
	if err != nil {
		return err
	}
	defer closeSub()
	close(h.ready)
	h.run(ctx, signals)
	return nil
}

// Ready 在订阅**真正建立**之后关闭。
//
// 光靠"建流没报错"是不够的：流建立了、但服务端还没订上 Redis 的那段时间里，
// 配置变更会丢，而 SDK 却以为推送可用。与 RevokeHub.Ready 同一理由。
func (h *ConfigHub) Ready() <-chan struct{} { return h.ready }

func (h *ConfigHub) run(ctx context.Context, signals <-chan store.ConfigSignal) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			h.fanout(sig)
		}
	}
}

// Subscribe 登记一个订阅者，返回它的事件 channel 与摘除函数。
func (h *ConfigHub) Subscribe(appID uuid.UUID) (<-chan ConfigEvent, func()) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		ch := make(chan ConfigEvent)
		close(ch)
		return ch, func() {}
	}
	h.next++
	id := h.next
	sub := &configSub{appID: appID, ch: make(chan ConfigEvent, configBufferSize)}
	h.subs[id] = sub
	h.mu.Unlock()

	// once 包住：Watch handler 里是 defer 调的，而 fanout 摘除时也会删同一个
	// id，两边都不该因为重复操作而 panic 或误删后来复用的 id。
	var once sync.Once
	return sub.ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, id)
			h.mu.Unlock()
		})
	}
}

// Close 关闭全部订阅者 channel，让所有 Watch handler 返回。
//
// 它存在的唯一理由是让 grpc.Server.GracefulStop 能够返回：Watch 是永不
// 主动结束的长流，不关掉订阅就没有任何机制能让那些 handler 退出。
func (h *ConfigHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.subs {
		close(sub.ch)
	}
	h.subs = nil
	h.closed = true
}

// fanout 把一条信号分发给相关订阅者。
//
// **缓冲满就摘掉订阅者**（关掉 channel 并从表里删除），而不是丢弃这一条
// 信号。丢弃是有洞的：待发的可能是 WEB 分区、被丢的是 DEFAULT，SDK 只会
// 重拉 WEB。而摘掉的后果被完整兜住——流结束 → SDK 重连 → 收到 ready →
// 重拉全部配置。与 RevokeHub 缓冲满时的处理同一逻辑。
//
// 全程持写锁：摘除要删 map、关 channel，与发送必须互斥，否则会出现
// "向已关闭的 channel 发送"。配置变更是极低频事件，写锁的代价可以忽略。
func (h *ConfigHub) fanout(sig store.ConfigSignal) {
	ev := ConfigEvent{Type: sig.Type, Seq: sig.Seq}
	if sig.Gap {
		// 订阅重建，漏读且不知道漏了哪些——空串让 SDK 重拉全部绑定。
		ev = ConfigEvent{}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for id, sub := range h.subs {
		// Gap 发给所有人；普通信号只发给它自己那个应用。
		if !sig.Gap && sub.appID != sig.AppID {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			slog.Warn("grpcapi: 配置事件缓冲已满，摘掉该订阅者；SDK 会重连并重拉配置",
				"appID", sub.appID)
			close(sub.ch)
			delete(h.subs, id)
		}
	}
}
```

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

---

## Task 9: SDK 反射——从 struct 推导 key 与类型

**Files:**
- Create: `sdk/configspec.go`
- Test: `sdk/configspec_test.go`
- Create: `internal/integration/configtype_test.go`

**Interfaces:**
- Consumes: 无（纯本地反射，不碰网络）
- Produces（包内，不导出）：
  - `type fieldSpec struct { Key string; Type string; Index []int; Duration bool }`
  - `func specsOf(t reflect.Type) ([]fieldSpec, error)` —— 按 key 字典序返回
  - `func toSnake(s string) string`
  - 常量 `cfgTypeBool/Int/Float/String/Array/Object`

**这是本模块唯一有真实算法的地方，必须测死。** 它同时是 `MissingConfigError` 那份清单的来源——人照着它在控制台建配置项，推错一个类型，人就建错一个。

- [ ] **Step 1: 写失败的测试**

创建 `sdk/configspec_test.go`：

```go
package fpsdk

import (
	"reflect"
	"testing"
	"time"
)

func TestToSnake(t *testing.T) {
	cases := []struct{ in, want string }{
		{"FeeRate", "fee_rate"},
		{"APIKey", "api_key"},       // 连续大写的缩写不能拆成 a_p_i_key
		{"UserID", "user_id"},       // 结尾的缩写
		{"HTTPSProxy", "https_proxy"},
		{"ID", "id"},
		{"Timeout", "timeout"},
		{"MaxConns2", "max_conns2"},
	}
	for _, c := range cases {
		if got := toSnake(c.in); got != c.want {
			t.Errorf("toSnake(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

type upstreamCfg struct {
	Timeout time.Duration
	APIKey  string
}

type providerCfg struct {
	Name string
	Rate float64
}

type specCfg struct {
	FeeRate   float64
	Enabled   bool
	Name      string
	Limits    []int
	Extra     map[string]string
	Upstream  upstreamCfg            // 嵌套 struct = 分组
	Providers []providerCfg          // 切片一律 array，不递归展开
	Raw       providerCfg `fp:"json"` // 标了 json 就整体当 object
	unexported int                    //nolint:unused // 必须被跳过
}

func TestSpecsOf(t *testing.T) {
	specs, err := specsOf(reflect.TypeOf(specCfg{}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}

	got := map[string]fieldSpec{}
	for _, s := range specs {
		got[s.Key] = s
	}

	want := map[string]string{
		"fee_rate":          cfgTypeFloat,
		"enabled":           cfgTypeBool,
		"name":              cfgTypeString,
		"limits":            cfgTypeArray,
		"extra":             cfgTypeObject,
		"upstream.timeout":  cfgTypeInt, // Duration 走 int（毫秒）
		"upstream.api_key":  cfgTypeString,
		"providers":         cfgTypeArray,
		"raw":               cfgTypeObject,
	}
	if len(got) != len(want) {
		t.Fatalf("推导出 %d 项：%v，期望 %d 项", len(got), keysOf(got), len(want))
	}
	for k, wantType := range want {
		s, ok := got[k]
		if !ok {
			t.Fatalf("缺少 key %q", k)
		}
		if s.Type != wantType {
			t.Errorf("%q 的类型 = %q，期望 %q", k, s.Type, wantType)
		}
	}

	// 【辨别力】Duration 必须被标出来，且它的 fp 类型是 int。
	// 只断言"类型是 int"是不够的——一个先判 Kind（int64）后判具体类型的
	// 实现同样会给出 int，却丢掉了毫秒换算，值会变成纳秒。
	if !got["upstream.timeout"].Duration {
		t.Fatal("upstream.timeout 必须标记为 Duration，否则毫秒换算会丢")
	}
	if got["fee_rate"].Duration {
		t.Fatal("fee_rate 不是 Duration")
	}

	// 排序稳定：MissingConfigError 的清单靠它才不会每次启动顺序都不同。
	for i := 1; i < len(specs); i++ {
		if specs[i-1].Key >= specs[i].Key {
			t.Fatalf("specs 未按 key 升序：%q 在 %q 之前", specs[i-1].Key, specs[i].Key)
		}
	}
}

func TestSpecsOfRejectsUnsupported(t *testing.T) {
	type badPtr struct{ P *int }
	type badChan struct{ C chan int }
	type badFunc struct{ F func() }
	type badIface struct{ I any }

	for _, v := range []any{badPtr{}, badChan{}, badFunc{}, badIface{}} {
		if _, err := specsOf(reflect.TypeOf(v)); err == nil {
			t.Errorf("%T 应当被拒绝，不能静默跳过", v)
		}
	}
}

func TestSpecsOfRejectsNonStruct(t *testing.T) {
	if _, err := specsOf(reflect.TypeOf(42)); err == nil {
		t.Fatal("非 struct 应当被拒绝")
	}
}

func keysOf(m map[string]fieldSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./sdk -run 'TestToSnake|TestSpecsOf' -v`
Expected: 编译失败，`undefined: toSnake`

- [ ] **Step 3: 写实现**

创建 `sdk/configspec.go`：

```go
package fpsdk

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
)

// fp 的配置类型。**不带任何一门语言的特性**——没有 duration，因为其他
// 语言没这个概念；Go 的 time.Duration 是本 SDK 按毫秒当 int 处理的私事。
//
// 这几个字符串是**线上契约**，必须与服务端 internal/domain 的
// ConfigValue* 逐字相同。两处分处 sdk/ 与 internal/（sdk 不得 import
// internal），任何一边单独看都只是几个孤立的字符串常量，改错了
// go build / vet / 全量测试照样全绿。配对由 internal/integration 里的
// TestConfigValueTypesMatch 守护——那是唯一能同时看到两个包的地方。
const (
	cfgTypeBool   = "bool"
	cfgTypeInt    = "int"
	cfgTypeFloat  = "float"
	cfgTypeString = "string"
	cfgTypeArray  = "array"
	cfgTypeObject = "object"
)

// durationType 缓存 time.Duration 的反射类型，供 fpTypeOf 做**具体类型**
// 判断——它必须发生在 Kind 判断之前，见 fpTypeOf 的注释。
var durationType = reflect.TypeOf(time.Duration(0))

// fieldSpec 是 struct 里一个可绑定字段的规格。
type fieldSpec struct {
	// Key 是配置项在 fp 上的键，如 "upstream.timeout"。
	Key string
	// Type 是 fp 类型，取值见上面的 cfgType* 常量。
	Type string
	// Index 是 reflect.Value.FieldByIndex 用的字段索引路径。
	Index []int
	// Duration 为 true 时，拉到的整数按**毫秒**解释，填进字段前乘
	// time.Millisecond。
	Duration bool
}

// specsOf 反射 t 推导出全部可绑定字段，按 key 升序返回。
//
// 排序不是审美：MissingConfigError 的清单直接来自它，不排序的话 Go 的
// map 遍历顺序会让每次启动打出的清单顺序都不同，人对着控制台一项项建的
// 时候极易漏项。
func specsOf(t reflect.Type) ([]fieldSpec, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("fpsdk: 只能绑定 struct，得到 %s", t)
	}
	var out []fieldSpec
	if err := collectSpecs(t, "", nil, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func collectSpecs(t reflect.Type, prefix string, index []int, out *[]fieldSpec) error {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// 未导出字段跳过：反射既读不到也写不进，它不可能是配置项。
		if !f.IsExported() {
			continue
		}
		key := prefix + toSnake(f.Name)
		// 拷一份再 append：append 可能复用底层数组，
		// 兄弟字段的路径会互相踩。
		path := append(append([]int{}, index...), i)

		// 标了 fp:"json" 的 struct 整体当一个 object，不展开成分组。
		// 供应商列表那种拆不动的结构走这条。
		if f.Tag.Get("fp") == "json" {
			*out = append(*out, fieldSpec{Key: key, Type: cfgTypeObject, Index: path})
			continue
		}

		// 未标 tag 的嵌套 struct 是**分组**，递归展开成 父.子 的 key。
		// time.Duration 不是 struct，落不到这里；time.Time 会——但它不在
		// 支持的类型里，展开后它的字段全是未导出的，会得到一个空分组。
		// 这属于"用了不该用的类型"，交给下面的 fpTypeOf 报错更清楚，
		// 所以这里先排除掉标准库里那个唯一常见的误用。
		if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(time.Time{}) {
			if err := collectSpecs(f.Type, key+".", path, out); err != nil {
				return err
			}
			continue
		}

		typ, isDur, err := fpTypeOf(f.Type)
		if err != nil {
			return fmt.Errorf("fpsdk: 字段 %s: %w", key, err)
		}
		*out = append(*out, fieldSpec{Key: key, Type: typ, Index: path, Duration: isDur})
	}
	return nil
}

// fpTypeOf 把 Go 类型映射成 fp 类型。
func fpTypeOf(t reflect.Type) (string, bool, error) {
	// **必须先判具体类型再判 Kind。** time.Duration 底层是 int64，
	// 顺序反了它会被当成普通整数，毫秒换算整个丢掉——配的 3000 会变成
	// 3 微秒，而且不报任何错。
	if t == durationType {
		return cfgTypeInt, true, nil
	}
	switch t.Kind() {
	case reflect.Bool:
		return cfgTypeBool, false, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return cfgTypeInt, false, nil
	case reflect.Float32, reflect.Float64:
		return cfgTypeFloat, false, nil
	case reflect.String:
		return cfgTypeString, false, nil
	case reflect.Slice, reflect.Array:
		return cfgTypeArray, false, nil
	case reflect.Map:
		return cfgTypeObject, false, nil
	default:
		// 指针、interface、chan、func 一律报错，不静默跳过——静默跳过会让
		// 一个本该被配置的字段永远停在零值，且没有任何迹象。
		return "", false, fmt.Errorf("不支持的类型 %s", t)
	}
}

// toSnake 把 Go 字段名转成 snake_case 的 key。
//
// 连续大写要当成一个缩写整体处理：APIKey → api_key 而不是 a_p_i_key，
// UserID → user_id，HTTPSProxy → https_proxy。
func toSnake(s string) string {
	r := []rune(s)
	var b strings.Builder
	b.Grow(len(r) + 4)
	for i, c := range r {
		if unicode.IsUpper(c) {
			// 在两种位置插下划线：① 前一个字符不是大写（词边界）；
			// ② 前一个是大写但后一个是小写（缩写结束，如 HTTPSProxy 的 P）。
			if i > 0 && (!unicode.IsUpper(r[i-1]) ||
				(i+1 < len(r) && unicode.IsLower(r[i+1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(c))
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./sdk -run 'TestToSnake|TestSpecsOf' -v`
Expected: PASS

- [ ] **Step 5: 变异验证 Duration 那条**

把 `fpTypeOf` 里的 `if t == durationType` 整块删掉（让它落到 `reflect.Int64` 分支）。

Run: `./scripts/test.sh ./sdk -run TestSpecsOf -v`
Expected: **FAIL**，报"upstream.timeout 必须标记为 Duration"。确认后改回来。

- [ ] **Step 6: 写跨包常量配对测试**

创建 `internal/integration/configtype_test.go`：

```go
package integration_test

import (
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/sdk"
)

// fp 类型的取值是线上契约，服务端（internal/domain）与 SDK（sdk/）各存了
// 一份字符串常量。sdk 不得 import internal，两边单独看都只是孤立常量，
// 改错一个 go build / vet / 全量测试照样全绿——这里是唯一能同时看到两个
// 包的地方。与 TestKeepaliveTimingIsCompatible 是同一类守护。
func TestConfigValueTypesMatch(t *testing.T) {
	for _, s := range fpsdk.ExportedConfigTypes() {
		if !domain.IsConfigValueType(s) {
			t.Errorf("SDK 的类型 %q 服务端不认", s)
		}
	}
	// 反向也要查：服务端多出一个类型而 SDK 不认，控制台上能建、
	// SDK 拉下来解析不了。
	for _, s := range []string{
		domain.ConfigValueBool, domain.ConfigValueInt, domain.ConfigValueFloat,
		domain.ConfigValueString, domain.ConfigValueArray, domain.ConfigValueObject,
	} {
		if !containsString(fpsdk.ExportedConfigTypes(), s) {
			t.Errorf("服务端的类型 %q SDK 不认", s)
		}
	}
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
```

在 `sdk/export_test.go`（若无则新建）里导出给测试用的入口。**注意**：`internal/integration` 是外部包，`export_test.go` 只对 `sdk` 包自己的测试可见——所以这个导出得放在**非测试文件**里。放 `sdk/configspec.go` 末尾：

```go
// ExportedConfigTypes 返回 SDK 认识的全部 fp 类型。
//
// 导出它只为一个目的：让 internal/integration 能核对它与服务端
// domain.IsConfigValueType 的取值集合一致（见 TestConfigValueTypesMatch）。
// 业务方用不到这个函数。
func ExportedConfigTypes() []string {
	return []string{cfgTypeBool, cfgTypeInt, cfgTypeFloat,
		cfgTypeString, cfgTypeArray, cfgTypeObject}
}
```

- [ ] **Step 7: 跑配对测试**

Run: `./scripts/test.sh ./internal/integration -run TestConfigValueTypesMatch -v`
Expected: PASS

- [ ] **Step 8: 确认分层约束没被破坏**

Run: `./scripts/test.sh ./sdk -run TestArch -v`
Expected: PASS（`sdk/` 仍未 import `internal/`，仍无 `panic`）

- [ ] **Step 9: 提交**

```bash
git add sdk/configspec.go sdk/configspec_test.go internal/integration/configtype_test.go
git commit -m "feat(config): SDK 反射——从 struct 推导 key 与 fp 类型"
```

---

## Task 10: SDK Bind 与 MissingConfigError

**Files:**
- Create: `sdk/config.go`
- Test: `sdk/config_test.go`
- Modify: `sdk/client.go`（持有 `ConfigServiceClient`）

**Interfaces:**
- Consumes: Task 9 的 `specsOf` / `fieldSpec`；Task 6 的 `fpv1.ConfigServiceClient`
- Produces:
  - `func Bind[T any](c *Client) (*Binding[T], error)`
  - `func (b *Binding[T]) Load() *T`
  - `type MissingConfigError struct { Keys []string; Types map[string]string }` + `Error() string`
  - 包内：`func fillStruct[T any](specs []fieldSpec, values map[string]json.RawMessage) (*T, []string, error)`

- [ ] **Step 1: 写失败的测试**

创建 `sdk/config_test.go`：

```go
package fpsdk

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

type bindCfg struct {
	FeeRate  float64
	Enabled  bool
	Upstream bindUpstream
	Limits   []int
}

type bindUpstream struct {
	Timeout time.Duration
	APIKey  string
}

func TestFillStructAppliesValues(t *testing.T) {
	specs, err := specsOf(reflect.TypeOf(bindCfg{}))
	if err != nil {
		t.Fatalf("specsOf 失败: %v", err)
	}
	values := map[string]json.RawMessage{
		"fee_rate":         json.RawMessage(`0.02`),
		"enabled":          json.RawMessage(`true`),
		"upstream.timeout": json.RawMessage(`3000`),
		"upstream.api_key": json.RawMessage(`"sk-live"`),
		"limits":           json.RawMessage(`[1,2,3]`),
	}

	got, missing, err := fillStruct[bindCfg](specs, values)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("不该有缺失项: %v", missing)
	}
	if got.FeeRate != 0.02 || !got.Enabled || got.Upstream.APIKey != "sk-live" {
		t.Fatalf("填充结果不对: %+v", got)
	}
	// 【辨别力】3000 必须变成 3 秒而不是 3000 纳秒。断言具体时长，
	// 不要只断言"非零"——丢掉毫秒换算的实现同样是非零。
	if got.Upstream.Timeout != 3*time.Second {
		t.Fatalf("Timeout = %v，期望 3s（3000 毫秒）", got.Upstream.Timeout)
	}
	if !reflect.DeepEqual(got.Limits, []int{1, 2, 3}) {
		t.Fatalf("Limits = %v", got.Limits)
	}
}

// 缺失项一次列全，且缺失的字段留 Go 零值。
// 【辨别力】必须缺**两项**：只缺一项的话，"报第一个就 return"的实现也会绿。
func TestFillStructReportsAllMissing(t *testing.T) {
	specs, _ := specsOf(reflect.TypeOf(bindCfg{}))
	values := map[string]json.RawMessage{
		"fee_rate": json.RawMessage(`0.02`),
		"enabled":  json.RawMessage(`true`),
		"limits":   json.RawMessage(`[]`),
	}

	got, missing, err := fillStruct[bindCfg](specs, values)
	if err != nil {
		t.Fatalf("缺值不是错误，应当由调用方决定: %v", err)
	}
	want := []string{"upstream.api_key", "upstream.timeout"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %v，期望 %v（升序、两项都在）", missing, want)
	}
	if got.FeeRate != 0.02 {
		t.Fatal("已配置的字段仍应被填上")
	}
	if got.Upstream.Timeout != 0 || got.Upstream.APIKey != "" {
		t.Fatal("缺失的字段应当留 Go 零值")
	}
}

// 类型对不上是错误，不是缺失——旧代码遇到"有人把 int 改成了 object"
// 必须报错，不能当成没配。
func TestFillStructRejectsMismatchedType(t *testing.T) {
	specs, _ := specsOf(reflect.TypeOf(bindCfg{}))
	values := map[string]json.RawMessage{
		"fee_rate":         json.RawMessage(`{"a":1}`), // 该是数字
		"enabled":          json.RawMessage(`true`),
		"upstream.timeout": json.RawMessage(`3000`),
		"upstream.api_key": json.RawMessage(`"x"`),
		"limits":           json.RawMessage(`[]`),
	}
	if _, _, err := fillStruct[bindCfg](specs, values); err == nil {
		t.Fatal("类型对不上必须报错")
	}
}

func TestMissingConfigErrorMessageListsEveryKeyWithType(t *testing.T) {
	err := &MissingConfigError{
		Keys:  []string{"fee_rate", "upstream.api_key"},
		Types: map[string]string{"fee_rate": cfgTypeFloat, "upstream.api_key": cfgTypeString},
	}
	msg := err.Error()
	for _, want := range []string{"fee_rate", "float", "upstream.api_key", "string"} {
		if !contains(msg, want) {
			t.Errorf("错误信息里缺少 %q：\n%s", want, msg)
		}
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && strings.Contains(s, sub) }
```

（测试文件 import 补 `"strings"`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./sdk -run 'TestFillStruct|TestMissingConfigError' -v`
Expected: 编译失败，`undefined: fillStruct`

- [ ] **Step 3: 写实现**

创建 `sdk/config.go`：

```go
package fpsdk

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MissingConfigError 表示有配置项在 fp 上还没有值。
//
// **它是人在控制台建配置项的唯一依据**（本模块不做 SDK 上报），所以必须
// 一次列全、且带上每一项该建成什么类型——漏一项就要多跑一轮"起→失败"。
type MissingConfigError struct {
	// Keys 是全部缺失的配置项，升序。
	Keys []string
	// Types 是每个 key 对应的 fp 类型，人照着它在控制台选类型。
	Types map[string]string
}

func (e *MissingConfigError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "fpsdk: %d 个配置项尚未在 fp 上配置，请到控制台创建并填值：", len(e.Keys))
	for _, k := range e.Keys {
		fmt.Fprintf(&b, "\n  %-24s (%s)", k, e.Types[k])
	}
	return b.String()
}

// Binding 是一份绑定到 T 的配置快照。并发安全。
type Binding[T any] struct {
	c     *Client
	typ   string
	specs []fieldSpec

	// snap 是当前快照。用 atomic.Pointer 换指针而不是就地改字段：
	// Load() 拿到的必须是**跨字段一致**的一份，一次请求内不会出现
	// A 字段是新值、B 字段是旧值。
	snap atomic.Pointer[T]

	mu       sync.Mutex
	onChange func(old, new *T)
	onError  func(error)
}

// Load 返回当前快照。返回的指针指向的内容**不得修改**——它被所有
// goroutine 共享。
func (b *Binding[T]) Load() *T { return b.snap.Load() }

// Bind 拉取 DEFAULT 分区的配置并填进 T。
//
// 任意字段在 fp 上没有值时返回 *MissingConfigError，但 **binding 照样返回**，
// 缺失的字段留 Go 零值——SDK 不替业务方决定能不能带伤启动。
func Bind[T any](c *Client) (*Binding[T], error) {
	var zero T
	specs, err := specsOf(reflect.TypeOf(zero))
	if err != nil {
		return nil, err
	}
	b := &Binding[T]{c: c, typ: ConfigTypeDefault, specs: specs}
	if err := b.reload(); err != nil {
		var miss *MissingConfigError
		if !asMissing(err, &miss) {
			return nil, err
		}
		c.registerBinding(b)
		return b, err
	}
	c.registerBinding(b)
	return b, nil
}

// fillStruct 按 specs 把 values 填进一个新的 T。
//
// 返回的 missing 是**升序**的全部缺失 key，一次给全。缺值不是 error——
// 是不是致命由调用方判断。类型对不上才是 error。
func fillStruct[T any](specs []fieldSpec, values map[string]json.RawMessage) (*T, []string, error) {
	out := new(T)
	v := reflect.ValueOf(out).Elem()

	var missing []string
	for _, s := range specs {
		raw, ok := values[s.Key]
		if !ok {
			missing = append(missing, s.Key)
			continue
		}
		field := v.FieldByIndex(s.Index)

		if s.Duration {
			// 拉到的整数按**毫秒**解释。fp 侧不知道 duration 这回事，
			// 单位约定只活在这一行和文档里。
			var ms int64
			if err := json.Unmarshal(raw, &ms); err != nil {
				return nil, nil, fmt.Errorf("fpsdk: 配置项 %s 解析失败: %w", s.Key, err)
			}
			field.SetInt(int64(time.Duration(ms) * time.Millisecond))
			continue
		}
		if err := json.Unmarshal(raw, field.Addr().Interface()); err != nil {
			return nil, nil, fmt.Errorf("fpsdk: 配置项 %s 解析失败: %w", s.Key, err)
		}
	}
	// specs 已经是升序的（specsOf 保证），missing 因此天然升序。
	return out, missing, nil
}
```

`reload()` / `registerBinding` / `asMissing` 在 Task 11 补齐。本任务先给一个只做"拉一次并填充"的 `reload`：

```go
// reload 拉一次当前配置并替换快照。Task 11 会给它加上变更比较与回调。
func (b *Binding[T]) reload() error {
	values, err := b.c.fetchConfig(b.typ)
	if err != nil {
		return err
	}
	snap, missing, err := fillStruct[T](b.specs, values)
	if err != nil {
		return err
	}
	b.snap.Store(snap)
	if len(missing) > 0 {
		types := make(map[string]string, len(missing))
		for _, s := range b.specs {
			types[s.Key] = s.Type
		}
		return &MissingConfigError{Keys: missing, Types: types}
	}
	return nil
}
```

`sdk/client.go`：
- `Client` 加 `cfgRPC fpv1.ConfigServiceClient`，`New` 里 `cfgRPC: fpv1.NewConfigServiceClient(conn)`
- 加 `ConfigTypeDefault = "DEFAULT"` / `ConfigTypeWeb = "WEB"` 两个导出常量（业务方调 `BindType` 要用）
- 加 `fetchConfig(typ string) (map[string]json.RawMessage, error)`：调 `GetConfig`，把 `Values` 这个 JSON 字符串解成 map
- 加 `registerBinding(r reloadable)` 与 `reloadable` 接口（Task 11 用到，这里先立起来）：

```go
// reloadable 是 Client 对各份绑定的全部依赖。Binding[T] 是泛型，
// 没法直接存进一个切片，只能靠这个非泛型接口。
type reloadable interface {
	reloadFromPush()
}
```

`asMissing` 是 `errors.As` 的一层包装，放 `sdk/config.go`。

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./sdk -run 'TestFillStruct|TestMissingConfigError' -v`
Expected: PASS

- [ ] **Step 5: 变异验证 missing 列全**

把 `fillStruct` 里的 `missing = append(missing, s.Key); continue` 改成 `return out, []string{s.Key}, nil`。

Run: `./scripts/test.sh ./sdk -run TestFillStructReportsAllMissing -v`
Expected: **FAIL**，报 missing 只有一项。确认后改回来。

- [ ] **Step 6: 变异验证 Duration 换算**

把 `field.SetInt(int64(time.Duration(ms) * time.Millisecond))` 改成 `field.SetInt(ms)`。

Run: `./scripts/test.sh ./sdk -run TestFillStructAppliesValues -v`
Expected: **FAIL**，报 `Timeout = 3µs，期望 3s`。确认后改回来。

- [ ] **Step 7: 提交**

```bash
git add sdk/config.go sdk/config_test.go sdk/client.go
git commit -m "feat(config): SDK Bind 与一次列全的 MissingConfigError"
```

---

## Task 11: SDK 热更新、OnChange 与 OnError

**Files:**
- Modify: `sdk/config.go`
- Modify: `sdk/client.go`
- Test: `sdk/config_test.go`

**Interfaces:**
- Consumes: Task 10 的 `Binding[T]` / `fillStruct`；Task 7 推来的 `fpv1.ConfigChanged`
- Produces:
  - `func (b *Binding[T]) OnChange(fn func(old, new *T))`
  - `func (b *Binding[T]) OnError(fn func(error))`
  - 包内：`func applyValues[T any](base *T, specs []fieldSpec, values map[string]json.RawMessage) (*T, []string, error)`
  - `Client` 的重载编排：`cfgReload chan struct{}`（缓冲 1）+ 一个消费 goroutine

**重载的编排（先看这段，它决定了下面所有代码的形状）：**

`Client` 开**一个** goroutine 消费一个**缓冲为 1** 的信号 channel，每次唤醒就重载**全部**绑定。三个理由：

1. **不能在 `watchOnce` 的收流循环里同步重载**——`GetConfig` 是一次网络往返，会把撤销事件的投递一起卡住。
2. **不能每收到一条就起一个 goroutine**——两次重载并发跑，先发起的可能后返回，把旧快照盖到新快照上。单 goroutine 天然串行。
3. **缓冲满就丢弃信号是安全的**：待处理的那次重载拉的是"**当前**版本"而不是"第 N 版"，它一定会带上被丢掉那条信号对应的变更。

**为什么无差别重载全部绑定而不按分区分派**：一个进程最多两份绑定（`DEFAULT` + `WEB`），多拉一次 `GetConfig` 的代价可以忽略；而"快照没变就不触发 `OnChange`"这条规则保证了不相关的那份绑定不会产生任何回调。换来的是彻底不用处理 `ConfigChanged.Type` 为空串（订阅缺口）这种分支。

- [ ] **Step 1: 写失败的测试**

追加到 `sdk/config_test.go`：

```go
// 快照没变就不换指针、不触发 OnChange。
// 同分区里别人改了你不关心的 key 也会推给你——不比较的话，别人配置一次
// 你的连接池就重建一次。
func TestReloadWithoutChangeDoesNotFire(t *testing.T) {
	b := newTestBinding(t, map[string]json.RawMessage{
		"fee_rate":         json.RawMessage(`0.02`),
		"enabled":          json.RawMessage(`true`),
		"upstream.timeout": json.RawMessage(`3000`),
		"upstream.api_key": json.RawMessage(`"k"`),
		"limits":           json.RawMessage(`[]`),
	})
	before := b.Load()

	var calls int
	b.OnChange(func(_, _ *bindCfg) { calls++ })

	// 推一份内容完全相同的配置。
	b.applyForTest(t, sameValues(before))

	if calls != 0 {
		t.Fatalf("OnChange 被调用了 %d 次，内容没变时应当是 0 次", calls)
	}
	if b.Load() != before {
		t.Fatal("内容没变时不该换指针")
	}
}

func TestReloadFiresOnChangeWithOldAndNew(t *testing.T) {
	b := newTestBinding(t, baseValues())

	var gotOld, gotNew *bindCfg
	b.OnChange(func(o, n *bindCfg) { gotOld, gotNew = o, n })

	v := baseValues()
	v["fee_rate"] = json.RawMessage(`0.05`)
	b.applyForTest(t, v)

	if gotOld == nil || gotNew == nil {
		t.Fatal("OnChange 没被调用")
	}
	if gotOld.FeeRate != 0.02 || gotNew.FeeRate != 0.05 {
		t.Fatalf("old=%v new=%v，期望 0.02 → 0.05", gotOld.FeeRate, gotNew.FeeRate)
	}
	if b.Load().FeeRate != 0.05 {
		t.Fatal("快照没被替换")
	}
}

// key 消失（被删或回滚导致）→ 保持旧值 + OnError，**绝不清成零值**。
// 清零值是危险的：fee_rate=0 就是免手续费。
func TestReloadKeepsOldValueWhenKeyDisappears(t *testing.T) {
	b := newTestBinding(t, baseValues())

	var errs int
	b.OnError(func(error) { errs++ })

	v := baseValues()
	delete(v, "fee_rate")
	b.applyForTest(t, v)

	if got := b.Load().FeeRate; got != 0.02 {
		t.Fatalf("FeeRate = %v，key 消失时必须保持旧值 0.02", got)
	}
	if errs != 1 {
		t.Fatalf("OnError 被调用 %d 次，期望 1 次", errs)
	}
}

// 解析失败（有人把 int 改成了 object）→ 保持**整份**旧快照 + OnError，
// 进程不崩、也不半解析。
//
// 【辨别力】必须断言"值还是旧的"而不只是"报了错"——一个先逐字段写入、
// 遇到错误再返回的实现同样会报错，却已经把前面几个字段换掉了，
// 快照的跨字段一致性已经破了。
func TestReloadKeepsWholeSnapshotOnParseFailure(t *testing.T) {
	b := newTestBinding(t, baseValues())
	before := b.Load()

	var errs int
	b.OnError(func(error) { errs++ })

	v := baseValues()
	v["enabled"] = json.RawMessage(`true`)
	v["fee_rate"] = json.RawMessage(`{"a":1}`) // 类型对不上
	v["upstream.api_key"] = json.RawMessage(`"changed"`)
	b.applyForTest(t, v)

	if b.Load() != before {
		t.Fatal("解析失败时必须保持整份旧快照，指针都不该换")
	}
	if b.Load().Upstream.APIKey != "k" {
		t.Fatal("同一批里合法的字段也不该被写进去——那会破坏跨字段一致性")
	}
	if errs != 1 {
		t.Fatalf("OnError 被调用 %d 次，期望 1 次", errs)
	}
}

// 没挂 OnError 时不静默：打 ERROR 日志。
func TestReloadLogsWhenNoErrorHandler(t *testing.T) {
	var logged int
	b := newTestBindingWithLogger(t, baseValues(), func(msg string) { logged++ })

	v := baseValues()
	v["fee_rate"] = json.RawMessage(`{"a":1}`)
	b.applyForTest(t, v)

	if logged == 0 {
		t.Fatal("没挂 OnError 时必须打 ERROR 日志，不能静默")
	}
}

func baseValues() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"fee_rate":         json.RawMessage(`0.02`),
		"enabled":          json.RawMessage(`true`),
		"upstream.timeout": json.RawMessage(`3000`),
		"upstream.api_key": json.RawMessage(`"k"`),
		"limits":           json.RawMessage(`[]`),
	}
}
```

**实现者注意**：`newTestBinding` / `newTestBindingWithLogger` / `applyForTest` / `sameValues` 是本任务要写的测试辅助。它们**绕开网络**——直接构造一个 `Binding[bindCfg]`（`specs` 由 `specsOf` 得到、`snap` 预置好），`applyForTest` 直接调那个"拿到 values 之后做的全部事情"的内部方法。把那段逻辑从 `reload()` 里拆成一个独立方法（比如 `applySnapshot(values map[string]json.RawMessage)`），`reload()` 只负责取数再调它——这样测试完全不需要 gRPC，跑得快也稳。

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./sdk -run TestReload -v`
Expected: 编译失败

- [ ] **Step 3: 写实现**

`sdk/config.go`：

```go
// applyValues 在 base 的副本上按 specs 应用 values，返回新快照与缺失的 key。
//
// 从 base 出发而不是从零值出发，是"key 消失时保持旧值"这条规则的执行点：
// 重载时 base 是当前快照，values 里没有的 key 就原样保留旧值；首次绑定时
// base 是零值，效果与"填不上就留零值"一致。
//
// 任何一个字段解析失败就整体返回 error，**绝不返回半成品**——调用方拿到
// error 后保持整份旧快照，跨字段一致性因此不会被破坏。
func applyValues[T any](base *T, specs []fieldSpec, values map[string]json.RawMessage) (*T, []string, error) {
	out := new(T)
	if base != nil {
		*out = *base // 浅拷贝：配置里只有值类型、切片与 map，写入时整体替换，
		             // 不会出现两份快照共享同一个被就地修改的底层数组。
	}
	v := reflect.ValueOf(out).Elem()

	var missing []string
	for _, s := range specs {
		raw, ok := values[s.Key]
		if !ok {
			missing = append(missing, s.Key)
			continue
		}
		field := v.FieldByIndex(s.Index)
		if s.Duration {
			var ms int64
			if err := json.Unmarshal(raw, &ms); err != nil {
				return nil, nil, fmt.Errorf("fpsdk: 配置项 %s 解析失败: %w", s.Key, err)
			}
			field.SetInt(int64(time.Duration(ms) * time.Millisecond))
			continue
		}
		if err := json.Unmarshal(raw, field.Addr().Interface()); err != nil {
			return nil, nil, fmt.Errorf("fpsdk: 配置项 %s 解析失败: %w", s.Key, err)
		}
	}
	return out, missing, nil
}

// fillStruct 是首次绑定用的入口：从零值出发。
func fillStruct[T any](specs []fieldSpec, values map[string]json.RawMessage) (*T, []string, error) {
	return applyValues[T](nil, specs, values)
}

// OnChange 注册变更回调。只在快照**真的变了**时触发，old 与 new 都非 nil。
// 重复调用会替换掉上一个回调。
func (b *Binding[T]) OnChange(fn func(old, new *T)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onChange = fn
}

// OnError 注册错误回调。触发条件是 4.4 那两条：某个 key 消失、或解析失败。
// 两种情况下快照都保持不变——所以这些事**不**走 OnChange。
//
// 没注册时 SDK 打 ERROR 日志，不静默。
func (b *Binding[T]) OnError(fn func(error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onError = fn
}

// applySnapshot 是拿到 values 之后做的全部事情：应用、比较、必要时换指针
// 并回调。测试直接调它，绕开网络。
func (b *Binding[T]) applySnapshot(values map[string]json.RawMessage) {
	old := b.snap.Load()
	next, missing, err := applyValues(old, b.specs, values)
	if err != nil {
		// 解析失败：整份旧快照原样留着，一个字段都不动。
		b.raise(err)
		return
	}
	if len(missing) > 0 {
		// key 消失：上面的 applyValues 已经让它们保持了旧值，这里只报错。
		b.raise(&MissingConfigError{Keys: missing, Types: b.typeMap()})
	}
	// 内容没变就不换指针、不回调——同分区里别人改了你不关心的 key
	// 也会推给你，不比较的话别人配置一次你的连接池就重建一次。
	if old != nil && reflect.DeepEqual(*old, *next) {
		return
	}
	b.snap.Store(next)

	b.mu.Lock()
	fn := b.onChange
	b.mu.Unlock()
	if fn != nil && old != nil {
		fn(old, next)
	}
}

// raise 把错误交给 OnError；没注册就打 ERROR 日志，绝不静默。
func (b *Binding[T]) raise(err error) {
	b.mu.Lock()
	fn := b.onError
	b.mu.Unlock()
	if fn != nil {
		fn(err)
		return
	}
	b.c.opts.Logger.Error("fpsdk: 配置重载出错且未注册 OnError", "type", b.typ, "err", err)
}

// reloadFromPush 实现 reloadable，让 Client 的重载 goroutine 统一驱动各份
// 绑定。刻意不给 partition()——重载是无差别的（见本任务开头的编排说明），
// 一个只会被写进接口却没人调的方法只会误导下一个人。
func (b *Binding[T]) reloadFromPush() {
	values, err := b.c.fetchConfig(b.typ)
	if err != nil {
		b.raise(err)
		return
	}
	b.applySnapshot(values)
}
```

`sdk/client.go`：

```go
// cfgReload 是配置重载的唤醒信号，缓冲为 1。
//
// 缓冲满就丢弃是**安全的**：待处理的那次重载拉的是"当前版本"而不是
// "第 N 版"，它一定会带上被丢掉那条信号对应的变更。
//
// 用单 goroutine 消费而不是每条信号起一个：两次重载并发跑的话，先发起的
// 可能后返回，把旧快照盖到新快照上。
```

在 `Client` 上加：

```go
	bindMu    sync.Mutex
	bindings  []reloadable
	cfgReload chan struct{}
```

`New` 里初始化 `cfgReload: make(chan struct{}, 1)`，并再起一个 goroutine：

```go
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.runConfigReload(ctx)
	}()
```

```go
// runConfigReload 串行地重载全部绑定，直到 ctx 取消。
func (c *Client) runConfigReload(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.cfgReload:
			c.bindMu.Lock()
			bs := append([]reloadable(nil), c.bindings...)
			c.bindMu.Unlock()
			for _, b := range bs {
				b.reloadFromPush()
			}
		}
	}
}

// requestConfigReload 请求一次重载。非阻塞——已经有待处理的信号就直接返回。
func (c *Client) requestConfigReload() {
	select {
	case c.cfgReload <- struct{}{}:
	default:
	}
}

func (c *Client) registerBinding(r reloadable) {
	c.bindMu.Lock()
	c.bindings = append(c.bindings, r)
	c.bindMu.Unlock()
}
```

`watchOnce` 的两处：

```go
		case msg.GetReady() != nil:
			// ...既有的 purge 与 streamUp 逻辑保持不变...

			// 重拉一次配置。断线期间发布的 ConfigChanged 一条都收不到，
			// 光靠"下一次变更"来补会让配置无限期停在旧值——这是配置侧
			// 对应 WatchPurge 的兜底，只是配置不需要"丢弃全部"，重拉即可。
			// 首次连接也走这条，此时各绑定刚拉过一次，重载会发现内容没变、
			// 不触发任何回调，无害。
			c.requestConfigReload()

		case msg.GetConfigChanged() != nil:
			// 无差别重载全部绑定，不按 Type 分派：一个进程最多两份绑定，
			// 多拉一次 GetConfig 的代价可以忽略，而"快照没变就不触发
			// OnChange"保证了不相关的那份不会产生回调。换来的是彻底不用
			// 处理 Type 为空串（fp 侧订阅缺口）这个分支。
			c.requestConfigReload()
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./sdk -run TestReload -v`
Expected: PASS，五条全绿

- [ ] **Step 5: 变异验证"解析失败保持整份旧快照"**

把 `applyValues` 改成遇错时返回已经填了一半的 `out`（即 `return out, missing, err`），并把 `applySnapshot` 改成出错时也 `b.snap.Store(next)`。

Run: `./scripts/test.sh ./sdk -run TestReloadKeepsWholeSnapshotOnParseFailure -v`
Expected: **FAIL**，报"解析失败时必须保持整份旧快照"。确认后改回来。

- [ ] **Step 6: 变异验证"内容没变不回调"**

把 `applySnapshot` 里的 `reflect.DeepEqual` 那个 early-return 删掉。

Run: `./scripts/test.sh ./sdk -run TestReloadWithoutChangeDoesNotFire -v`
Expected: **FAIL**，报 `OnChange 被调用了 1 次`。确认后改回来。

- [ ] **Step 7: 提交**

```bash
git add sdk/config.go sdk/client.go sdk/config_test.go
git commit -m "feat(config): SDK 热更新、OnChange 与 OnError"
```

---

## Task 12: SDK BindType

**Files:**
- Create: `sdk/bindtype.go`
- Test: `sdk/bindtype_test.go`

**Interfaces:**
- Consumes: Task 11 的 `reloadable` / `Client.fetchConfig` / `Client.registerBinding`
- Produces:
  - `func BindType(c *Client, typ string) (*TypeBinding, error)`
  - `func (b *TypeBinding) Load() map[string]any`
  - `func (b *TypeBinding) OnChange(fn func(old, new map[string]any))`
  - `func (b *TypeBinding) OnError(fn func(error))`

- [ ] **Step 1: 写失败的测试**

创建 `sdk/bindtype_test.go`：

```go
package fpsdk

import (
	"encoding/json"
	"reflect"
	"testing"
)

// 值解析成真正的 JSON 类型，不是一堆待解析的字符串——业务方
// json.Encode 出去直接就是 {"feature.new": true, "limits": [1,2,3]}。
func TestTypeBindingDecodesNativeJSON(t *testing.T) {
	b := newTestTypeBinding(t, map[string]json.RawMessage{
		"feature.new": json.RawMessage(`true`),
		"limits":      json.RawMessage(`[1,2,3]`),
		"site":        json.RawMessage(`{"title":"商城"}`),
		"title":       json.RawMessage(`"商城"`),
		"n":           json.RawMessage(`3`),
	})

	got := b.Load()
	if got["feature.new"] != true {
		t.Fatalf("feature.new = %#v，期望 bool true 而不是字符串", got["feature.new"])
	}
	if !reflect.DeepEqual(got["limits"], []any{float64(1), float64(2), float64(3)}) {
		t.Fatalf("limits = %#v，期望 []any", got["limits"])
	}
	site, ok := got["site"].(map[string]any)
	if !ok || site["title"] != "商城" {
		t.Fatalf("site = %#v，期望 map[string]any", got["site"])
	}
	if got["title"] != "商城" {
		t.Fatalf("title = %#v", got["title"])
	}
	if got["n"] != float64(3) {
		t.Fatalf("n = %#v", got["n"])
	}
}

// 内容没变不回调，与 Binding[T] 同一条规则。
func TestTypeBindingWithoutChangeDoesNotFire(t *testing.T) {
	v := map[string]json.RawMessage{"a": json.RawMessage(`1`)}
	b := newTestTypeBinding(t, v)

	var calls int
	b.OnChange(func(_, _ map[string]any) { calls++ })
	b.applyForTest(t, map[string]json.RawMessage{"a": json.RawMessage(`1`)})

	if calls != 0 {
		t.Fatalf("OnChange 被调用 %d 次，内容没变时应当是 0 次", calls)
	}
}

func TestBindTypeRejectsEmptyPartition(t *testing.T) {
	// 分区必填：分区之间同名 key 是不同的配置项，没有"不传就是全部"
	// 这种语义——那会逼调用方回答"撞了算谁的"。
	if _, err := BindType(nil, ""); err == nil {
		t.Fatal("空分区应当被拒绝")
	}
}
```

**实现者注意**：`newTestTypeBinding` / `applyForTest` 与 Task 11 的同名辅助同构，直接构造 `TypeBinding` 并调它的 `applySnapshot`，不走网络。

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./sdk -run 'TestTypeBinding|TestBindType' -v`
Expected: 编译失败，`undefined: BindType`

- [ ] **Step 3: 写实现**

创建 `sdk/bindtype.go`：

```go
package fpsdk

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
)

// TypeBinding 是某个分区的全量配置，不绑定任何 struct。
//
// 它的用途是**转发给前端**：拿到 map 之后怎么送出去是业务方的事——
// 挂个 handler 吐 Load()、或挂 OnChange 用 WebSocket 推，都行。
// SDK 刻意不提供 HTTP handler，也不让前端直连 fp（fp 是全局单点，
// 前端流量的量级与 SDK 完全不同）。
type TypeBinding struct {
	c   *Client
	typ string

	snap atomic.Pointer[map[string]any]

	mu       sync.Mutex
	onChange func(old, new map[string]any)
	onError  func(error)
}

// BindType 拉取指定分区的全部配置值。
//
// 分区**必填**，没有"不传就是全部"的重载：分区之间同名 key 是不同的
// 配置项，合并成一个 map 就得回答"撞了算谁的"，而这个问题不该存在。
func BindType(c *Client, typ string) (*TypeBinding, error) {
	if typ == "" {
		return nil, fmt.Errorf("fpsdk: BindType 必须指定分区，如 fpsdk.ConfigTypeWeb")
	}
	b := &TypeBinding{c: c, typ: typ}
	values, err := c.fetchConfig(typ)
	if err != nil {
		return nil, err
	}
	b.applySnapshot(values)
	c.registerBinding(b)
	return b, nil
}

// Load 返回当前快照。返回的 map **不得修改**——它被所有 goroutine 共享。
func (b *TypeBinding) Load() map[string]any {
	m := b.snap.Load()
	if m == nil {
		return map[string]any{}
	}
	return *m
}

// OnChange 注册变更回调。只在内容真的变了时触发。
func (b *TypeBinding) OnChange(fn func(old, new map[string]any)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onChange = fn
}

// OnError 注册错误回调。没注册时打 ERROR 日志，不静默。
func (b *TypeBinding) OnError(fn func(error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onError = fn
}

func (b *TypeBinding) applySnapshot(values map[string]json.RawMessage) {
	// 解析成 JSON 原生类型再交出去，而不是原样转发字符串——否则业务方
	// json.Encode 出来的是 {"feature.new": "true"}，前端还要再转一次。
	next := make(map[string]any, len(values))
	for k, raw := range values {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			b.raise(fmt.Errorf("fpsdk: 配置项 %s 解析失败: %w", k, err))
			return // 与 Binding[T] 一致：宁可整份保持旧的，也不给半成品
		}
		next[k] = v
	}

	old := b.snap.Load()
	if old != nil && reflect.DeepEqual(*old, next) {
		return
	}
	b.snap.Store(&next)

	b.mu.Lock()
	fn := b.onChange
	b.mu.Unlock()
	if fn != nil && old != nil {
		fn(*old, next)
	}
}

func (b *TypeBinding) raise(err error) {
	b.mu.Lock()
	fn := b.onError
	b.mu.Unlock()
	if fn != nil {
		fn(err)
		return
	}
	b.c.opts.Logger.Error("fpsdk: 配置重载出错且未注册 OnError", "type", b.typ, "err", err)
}

func (b *TypeBinding) reloadFromPush() {
	values, err := b.c.fetchConfig(b.typ)
	if err != nil {
		b.raise(err)
		return
	}
	b.applySnapshot(values)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./sdk -run 'TestTypeBinding|TestBindType' -v`
Expected: PASS

- [ ] **Step 5: 跑整个 sdk 包并确认分层约束**

Run: `./scripts/test.sh ./sdk`
Expected: PASS，含 `TestArch*`（`sdk/` 仍未 import `internal/`、仍无 `panic`）

- [ ] **Step 6: 提交**

```bash
git add sdk/bindtype.go sdk/bindtype_test.go
git commit -m "feat(config): SDK BindType——按分区拉全量，转发给前端"
```

---

## Task 13: 控制台——配置中心页

**Files:**
- Create: `web/src/pages/ConfigCenter.tsx`
- Test: `web/src/pages/ConfigCenter.test.tsx`
- Modify: `web/src/lib/types.ts`（DTO 手工镜像）
- Modify: `web/src/lib/api.ts`（如需新增方法）
- Modify: `web/src/routes.tsx`（加路由）

**Interfaces:**
- Consumes: Task 8 的五条 HTTP 路由
- Produces: 路由 `/applications/:id/config`

**先读 `docs/console.md`** 里那六条与官方文档冲突的前端工具链坑（tsconfig 不许有 `baseUrl`、shadcn 组件名必须带 `@shadcn/` 命名空间、shadcn v4 没有 form 组件、`defineConfig` 要从 `vitest/config` 导入等）。改前端工程配置之前必须读。

**两条硬约束：**

1. **不复用 `DynamicForm`。** 结构差异太大（那是 connector 的单个动态表单，这是带分组、批量保存的列表），而且第三阶段终审查出它的 DOM id 没加命名空间会让同名字段串台——配置项的 key 带点、撞得更狠。新组件**从第一行起就给每个 input 的 id 加上 `cfg-${partition}-${key}` 这样的命名空间**。
2. **`web/src/lib/types.ts` 是手工镜像，`api.get<T>` 只是类型断言**——字段名与 Go 的 json tag 对不上不会有编译错误，只会在运行时变成 `undefined`。逐字对着 Task 8 的 DTO 抄。

- [ ] **Step 1: 加类型镜像**

`web/src/lib/types.ts` 追加：

```ts
/** 配置分区。同名 key 在两个分区下是两个独立的配置项。 */
export type ConfigPartition = 'DEFAULT' | 'WEB'

/** 配置项的值类型。与后端 domain.ConfigValue* 逐字一致。 */
export type ConfigValueType = 'bool' | 'int' | 'float' | 'string' | 'array' | 'object'

export interface ConfigField {
  type: ConfigValueType
  desc: string
  /** null 表示"未配置"——它仍然是列表上待填的一行，不是不存在。 */
  value: unknown
}

export interface ConfigSnapshot {
  /** 0 表示该分区还没有任何版本。 */
  seq: number
  fields: Record<string, ConfigField>
}

export interface ConfigVersion {
  seq: number
  createdAt: number
}

export interface SaveConfigResponse {
  seq: number
}
```

- [ ] **Step 2: 写失败的测试**

创建 `web/src/pages/ConfigCenter.test.tsx`。**先读 `web/src/pages/ApplicationDetail.test.tsx`**，照它的方式 stub `fetch`（`vi.stubGlobal`）并渲染。注意第三阶段那条教训：vitest 关掉了 `globals`，`@testing-library/react` 的自动 DOM 清理靠裸标识符探测全局 `afterEach`——**每个测试文件必须自己写 `afterEach(cleanup)`**，否则组件测试之间 DOM 互相污染、断言会假红。

```tsx
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

afterEach(cleanup)

describe('ConfigCenter', () => {
  it('未配置的项标出来并计数', async () => {
    stubConfig({
      seq: 1,
      fields: {
        fee_rate: { type: 'float', desc: '手续费率', value: 0.02 },
        api_key: { type: 'string', desc: '上游密钥', value: null },
      },
    })
    renderPage()

    // 顶部提示必须给出数量——人是照着它决定还要不要继续配的。
    expect(await screen.findByText(/1 项未配置/)).toBeTruthy()
    // 未配置的项本身仍然在列表上（它就是待填的那一行）。
    expect(screen.getByLabelText('api_key')).toBeTruthy()
  })

  it('保存时把完整的 fields 全量提交', async () => {
    const calls: Array<{ method: string; body: unknown }> = []
    stubConfig(
      {
        seq: 1,
        fields: {
          a: { type: 'int', desc: '', value: 1 },
          b: { type: 'int', desc: '', value: 2 },
        },
      },
      calls,
    )
    renderPage()

    const input = await screen.findByLabelText('a')
    await userEvent.clear(input)
    await userEvent.type(input, '9')
    await userEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const put = calls.find((c) => c.method === 'PUT')!
    const body = put.body as { type: string; push: boolean; fields: Record<string, unknown> }
    expect(body.type).toBe('DEFAULT')
    // 【辨别力】没被改的 b 也必须在提交里。接口是全量替换——只提交被改过的
    // 字段的话，b 会在新版本里凭空消失（等于被删了）。
    expect(Object.keys(body.fields).sort()).toEqual(['a', 'b'])
  })

  it('生效方式默认是立即推送，可切成仅落库', async () => {
    const calls: Array<{ method: string; body: unknown }> = []
    stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } }, calls)
    renderPage()

    await screen.findByLabelText('a')
    await userEvent.click(screen.getByLabelText(/仅落库/))
    await userEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    expect((calls.find((c) => c.method === 'PUT')!.body as { push: boolean }).push).toBe(false)
  })

  it('切换分区会重新拉取', async () => {
    const urls: string[] = []
    stubConfigCapturingUrls(urls)
    renderPage()

    await screen.findByRole('tab', { name: 'WEB' })
    await userEvent.click(screen.getByRole('tab', { name: 'WEB' }))

    await waitFor(() => expect(urls.some((u) => u.includes('type=WEB'))).toBe(true))
    // 【辨别力】要断言 DEFAULT 也被拉过——只断言 WEB 的话，
    // 一个把 type 写死成 WEB 的实现同样会绿。
    expect(urls.some((u) => u.includes('type=DEFAULT'))).toBe(true)
  })

  it('删除配置项要二次确认，并说明没有机制能确认它是否还被读取', async () => {
    stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } })
    renderPage()

    await userEvent.click(await screen.findByRole('button', { name: '删除 a' }))
    expect(screen.getByText(/没有机制能确认它是否还被代码读取/)).toBeTruthy()
  })
})
```

`stubConfig` / `stubConfigCapturingUrls` / `renderPage` 是本文件的辅助，照 `ApplicationDetail.test.tsx` 的写法实现（`vi.stubGlobal('fetch', ...)` + `MemoryRouter` 定位到 `/applications/app-1/config`）。

- [ ] **Step 3: 跑测试确认失败**

Run: `cd web && npx vitest run src/pages/ConfigCenter.test.tsx`
Expected: 模块不存在

- [ ] **Step 4: 写实现**

创建 `web/src/pages/ConfigCenter.tsx`。要点（**照着 `ApplicationDetail.tsx` 的结构与既有 shadcn 组件写**）：

- 组件状态：`partition`（默认 `'DEFAULT'`）、`snapshot`、`draft`（本地编辑中的 fields）、`push`（默认 `true`）
- `useEffect` 依赖 `[appId, partition]` 拉 `GET .../config?type=${partition}`
- 分区 tab 用 `role="tab"`，名字就是 `DEFAULT` / `WEB`
- 顶部提示：`draft` 里 `value === null` 的项数，非 0 时渲染 `{n} 项未配置`（红色）
- 列表按 key 的**第一段点前缀**分组折叠（`upstream.timeout` 与 `upstream.api_key` 归 `upstream` 组；无点的归"未分组"）
- 每行按 `type` 渲染控件：`bool` 开关、`int`/`float` 数字输入、`string` 文本框、`array`/`object` 多行文本（内容是 JSON，失焦时 `JSON.parse` 校验，不合法就标红并禁用保存）
- **每个 input 的 `id` 与 `htmlFor` 都是 `cfg-${partition}-${key}`**，`aria-label` 就是 key（测试靠它定位）
- 改类型的下拉要二次确认，文案含"旧实例若收到推送会解析失败"
- `[新建配置项]` 弹窗：key + 类型 + 值 + 备注，**值必填**（没有代码强制它，空着没意义）
- `[删除]` 用既有的 `ConfirmDialog`，文案含"没有机制能确认它是否还被代码读取；删错了运行中的实例会保持旧值并报错，新起的实例会起不来"
- `[保存]`：`PUT .../config`，body 是 `{ type: partition, push, fields: draft }`——**永远提交完整的 draft**，接口是全量替换

- [ ] **Step 5: 加路由**

`web/src/routes.tsx` 在 `RequireAuth` 那个 Route 里加：

```tsx
        <Route path="/applications/:id/config" element={<ConfigCenter />} />
```

并在 `ApplicationDetail.tsx` 里加一个跳过去的入口链接。

- [ ] **Step 6: 跑测试确认通过**

Run: `cd web && npx vitest run src/pages/ConfigCenter.test.tsx`
Expected: PASS，五条全绿

- [ ] **Step 7: 变异验证"全量提交"**

把保存时的 body 改成只带被修改过的字段。

Run: `cd web && npx vitest run src/pages/ConfigCenter.test.tsx -t 全量提交`
Expected: **FAIL**。确认后改回来。

- [ ] **Step 8: 类型检查与构建**

Run: `cd web && npx tsc -b && npm run build`
Expected: 无错误，`web/dist/` 产出且 `.gitkeep` 仍在（`package.json` 的 build 脚本末尾会重建它）

- [ ] **Step 9: 提交**

```bash
git add web/src/pages/ConfigCenter.tsx web/src/pages/ConfigCenter.test.tsx web/src/lib/types.ts web/src/routes.tsx web/src/pages/ApplicationDetail.tsx
git commit -m "feat(config): 控制台配置中心页"
```

---

## Task 14: 控制台——版本历史与回滚

**Files:**
- Create: `web/src/pages/ConfigVersions.tsx`
- Test: `web/src/pages/ConfigVersions.test.tsx`
- Modify: `web/src/routes.tsx`

**Interfaces:**
- Consumes: Task 8 的 `/config/versions`、`/config/versions/{seq}`、`/config/rollback`
- Produces: 路由 `/applications/:id/config/versions`

- [ ] **Step 1: 写失败的测试**

创建 `web/src/pages/ConfigVersions.test.tsx`：

```tsx
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

afterEach(cleanup)

describe('ConfigVersions', () => {
  it('列出版本并标出每版改了哪些 key', async () => {
    stubVersions(
      [{ seq: 2, createdAt: 1757000000 }, { seq: 1, createdAt: 1756000000 }],
      {
        1: { seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } },
        2: {
          seq: 2,
          fields: {
            a: { type: 'int', desc: '', value: 1 },
            b: { type: 'int', desc: '', value: 2 },
          },
        },
      },
    )
    renderPage()

    // "改了哪些"是相邻两版 diff 出来的，后端不存这个字段。
    // 【辨别力】v2 里没变的 a 不能出现在改动清单里，否则一个"把整份 fields
    // 都列成改动"的实现同样会绿。
    //
    // 断言落在**改动清单这个容器**上，不是整行的 textContent——整行还带着
    // 版本号、时间戳、按钮文案，用 not.toContain('a') 去查一个单字母会被
    // 那些文字里任意一个 a 误伤，测试会因为无关的文案改动而假红。
    // 清单渲染成 <ul data-testid="changed-2">，每个 key 一个 <li>。
    const changed = await screen.findByTestId('changed-2')
    const keys = Array.from(changed.querySelectorAll('li')).map((li) => li.textContent)
    expect(keys).toEqual(['b'])
  })

  it('回滚前提示哪些项将变成未配置', async () => {
    stubVersions(
      [{ seq: 2, createdAt: 2 }, { seq: 1, createdAt: 1 }],
      {
        1: { seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } },
        2: {
          seq: 2,
          fields: {
            a: { type: 'int', desc: '', value: 1 },
            b: { type: 'int', desc: '', value: 2 },
          },
        },
      },
    )
    renderPage()

    await userEvent.click(await screen.findByRole('button', { name: '回滚到 v1' }))
    // v2 才新增的 b 在 v1 里没有——回滚后它会变成未配置，运行中的实例
    // 保持旧值并报错，新起的实例会缺值起不来。这条提示必须出现。
    expect(screen.getByText(/回滚后以下配置项将变成未配置/)).toBeTruthy()
    expect(screen.getByText(/\bb\b/)).toBeTruthy()
  })

  it('回滚同样要选生效方式', async () => {
    const calls: Array<{ method: string; body: unknown }> = []
    stubVersions([{ seq: 1, createdAt: 1 }], { 1: { seq: 1, fields: {} } }, calls)
    renderPage()

    await userEvent.click(await screen.findByRole('button', { name: '回滚到 v1' }))
    await userEvent.click(screen.getByLabelText(/仅落库/))
    await userEvent.click(screen.getByRole('button', { name: '确认回滚' }))

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const body = calls.find((c) => c.method === 'POST')!.body as { seq: number; push: boolean }
    expect(body).toEqual({ type: 'DEFAULT', seq: 1, push: false })
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npx vitest run src/pages/ConfigVersions.test.tsx`
Expected: 模块不存在

- [ ] **Step 3: 写实现**

创建 `web/src/pages/ConfigVersions.tsx`。要点：

- 分区 tab 同 Task 13
- `GET .../config/versions?type=X` 拉列表，每行 `data-testid="version-${seq}"`
- **改了哪些 key = 相邻两版的 `fields` 在前端 diff**（后端不存这个字段）：拉 `seq` 与 `seq-1` 两份快照，比较 key 集合与每个 key 的 `{type, desc, value}`；最老的一版（前一版不存在或已被修剪）标成"初始版本"
- `[回滚到 vN]` → 弹窗：
  - 先算出"当前有、vN 没有"的 key 集合，非空时渲染 `回滚后以下配置项将变成未配置：...`
  - 生效方式单选（默认立即推送）
  - `[确认回滚]` → `POST .../config/rollback`，body `{ type, seq, push }`
- 回滚成功后跳回配置中心页

- [ ] **Step 4: 跑测试确认通过**

Run: `cd web && npx vitest run src/pages/ConfigVersions.test.tsx`
Expected: PASS

- [ ] **Step 5: 加路由并跑全部前端测试**

`web/src/routes.tsx` 加 `<Route path="/applications/:id/config/versions" element={<ConfigVersions />} />`。

Run: `cd web && npx vitest run && npx tsc -b`
Expected: 全绿、类型检查无错

- [ ] **Step 6: 提交**

```bash
git add web/src/pages/ConfigVersions.tsx web/src/pages/ConfigVersions.test.tsx web/src/routes.tsx
git commit -m "feat(config): 控制台版本历史与回滚"
```

---

## Task 15: 端到端穿透

**Files:**
- Create: `internal/integration/config_test.go`

**Interfaces:**
- Consumes: 前面全部任务

**这条对应第三阶段最要命的那个教训**：服务端有信息、传输层丢了，而所有测试照绿——因为没有一条测试跨越边界。下面每条都必须穿到 **SDK 出口**（`Load()` 的返回值），断言服务端返回值是不够的。

- [ ] **Step 1: 写失败的测试**

创建 `internal/integration/config_test.go`。**先读同目录下既有的集成测试**，复用那里"起一个真 fp（HTTP + gRPC）+ 一个真 SDK Client"的脚手架。

```go
package integration_test

import (
	"context"
	"testing"
	"time"
)

// 改值 → 推送 → SDK 的 Load() 真的变了。
func TestConfigChangePropagatesToSDK(t *testing.T) {
	env := newFullEnv(t) // 真 fp + 真 SDK Client
	ctx := context.Background()

	env.saveConfig(t, "DEFAULT", `{"fee_rate":{"type":"float","desc":"","value":0.02}}`, true)

	type shopCfg struct{ FeeRate float64 }
	cfg, err := fpsdk.Bind[shopCfg](env.client)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}
	if cfg.Load().FeeRate != 0.02 {
		t.Fatalf("初始值 = %v，期望 0.02", cfg.Load().FeeRate)
	}

	changed := make(chan float64, 1)
	cfg.OnChange(func(_, n *shopCfg) { changed <- n.FeeRate })

	// 走**控制台的 HTTP 接口**改值，不要直接调 service——这条测试的价值
	// 就在于穿过 HTTP → PG → Redis → gRPC → SDK 这一整条链路。
	env.putConfig(t, "DEFAULT", `{"type":"DEFAULT","push":true,"fields":{
		"fee_rate":{"type":"float","desc":"","value":0.05}}}`)

	select {
	case v := <-changed:
		if v != 0.05 {
			t.Fatalf("推送后的值 = %v，期望 0.05", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待配置推送超时——变更没能穿到 SDK 出口")
	}
	if cfg.Load().FeeRate != 0.05 {
		t.Fatalf("Load() = %v，期望 0.05", cfg.Load().FeeRate)
	}
}

// 「仅落库」的另一条腿：不推送，但新起一次 Bind 能拿到新值。
// 【辨别力】必须同时断言这两件事。只断言"没推送"的话，一个根本没存的
// 实现也会绿；只断言"新 Bind 拿到了"的话，一个照样推送的实现也会绿。
func TestSaveWithoutPushIsInvisibleUntilRebind(t *testing.T) {
	env := newFullEnv(t)

	env.saveConfig(t, "DEFAULT", `{"n":{"type":"int","desc":"","value":1}}`, true)

	type cfgT struct{ N int }
	cfg, err := fpsdk.Bind[cfgT](env.client)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}
	fired := make(chan struct{}, 1)
	cfg.OnChange(func(_, _ *cfgT) { fired <- struct{}{} })

	env.putConfig(t, "DEFAULT", `{"type":"DEFAULT","push":false,"fields":{
		"n":{"type":"int","desc":"","value":2}}}`)

	select {
	case <-fired:
		t.Fatal("选了「仅落库」，运行中的绑定不该收到变更")
	case <-time.After(time.Second):
	}
	if got := cfg.Load().N; got != 1 {
		t.Fatalf("运行中的绑定 N = %d，期望仍是 1", got)
	}

	// 另一条腿：新起一次 Bind（模拟 pod 重启）必须拿到新值。
	fresh, err := fpsdk.Bind[cfgT](env.newClient(t))
	if err != nil {
		t.Fatalf("重新 Bind 失败: %v", err)
	}
	if got := fresh.Load().N; got != 2 {
		t.Fatalf("新绑定 N = %d，期望 2——值必须真的落库了", got)
	}
}

// 改类型的两条路，穿到 SDK 出口（设计文档测试策略第 5 条）。
//
// 场景：v2 代码把某项从 int 改成了 object，发版前得先把控制台上的值改成
// JSON，而这时 v1 还在跑、要的还是那个 int。
func TestTypeChangeBothPaths(t *testing.T) {
	type v1Cfg struct{ Timeout int }

	// —— 路径一：选「仅落库」，v1 完全不受影响
	env := newFullEnv(t)
	env.saveConfig(t, "DEFAULT", `{"timeout":{"type":"int","desc":"","value":3000}}`, true)

	cfg, err := fpsdk.Bind[v1Cfg](env.client)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}
	var errs int32
	cfg.OnError(func(error) { atomic.AddInt32(&errs, 1) })

	env.putConfig(t, "DEFAULT", `{"type":"DEFAULT","push":false,"fields":{
		"timeout":{"type":"object","desc":"","value":{"ms":5000}}}}`)
	time.Sleep(time.Second)

	if got := cfg.Load().Timeout; got != 3000 {
		t.Fatalf("「仅落库」路径：v1 的 Timeout = %d，期望仍是 3000", got)
	}
	if n := atomic.LoadInt32(&errs); n != 0 {
		t.Fatalf("「仅落库」路径：OnError 被调用 %d 次，期望 0 次", n)
	}

	// —— 路径二：误选「立即推送」，v1 保持旧值 + OnError，**不崩**
	env2 := newFullEnv(t)
	env2.saveConfig(t, "DEFAULT", `{"timeout":{"type":"int","desc":"","value":3000}}`, true)

	cfg2, err := fpsdk.Bind[v1Cfg](env2.client)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}
	raised := make(chan struct{}, 1)
	cfg2.OnError(func(error) {
		select {
		case raised <- struct{}{}:
		default:
		}
	})

	env2.putConfig(t, "DEFAULT", `{"type":"DEFAULT","push":true,"fields":{
		"timeout":{"type":"object","desc":"","value":{"ms":5000}}}}`)

	select {
	case <-raised:
	case <-time.After(5 * time.Second):
		t.Fatal("「立即推送」路径：解析失败必须触发 OnError")
	}
	// 【辨别力】必须断言"值还是旧的"而不只是"报了错"——一个逐字段写入、
	// 遇错才返回的实现同样会报错，却已经把快照改坏了。
	if got := cfg2.Load().Timeout; got != 3000 {
		t.Fatalf("「立即推送」路径：Timeout = %d，解析失败时必须保持旧值 3000", got)
	}

	// —— 两条路都要能让新版本代码拿到新值
	type v2Cfg struct {
		Timeout struct{ Ms int }
	}
	fresh, err := fpsdk.Bind[v2Cfg](env2.newClient(t))
	if err != nil {
		t.Fatalf("v2 Bind 失败: %v", err)
	}
	if fresh.Load().Timeout.Ms != 5000 {
		t.Fatalf("v2 拿到 %d，期望 5000", fresh.Load().Timeout.Ms)
	}
}

// 断线期间的变更，靠"收到 ready 就重拉"补上。
// 【辨别力】变更必须发生在断线**期间**：断线前改（重连前就拉到了）
// 或重连后改（有 ConfigChanged 推送）都测不到这个缺口。
func TestReadyRepullsConfigMissedWhileDisconnected(t *testing.T) {
	env := newFullEnv(t)
	env.saveConfig(t, "DEFAULT", `{"n":{"type":"int","desc":"","value":1}}`, true)

	type cfgT struct{ N int }
	cfg, err := fpsdk.Bind[cfgT](env.client)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}

	// 掐断 gRPC 服务端，让 SDK 的 Watch 流断开。
	env.stopGRPC(t)
	// 断线**期间**改值：这次的 ConfigChanged 谁也收不到。
	env.putConfig(t, "DEFAULT", `{"type":"DEFAULT","push":true,"fields":{
		"n":{"type":"int","desc":"","value":42}}}`)
	// 重新起服务端，SDK 会退避重连并收到 ready。
	env.startGRPC(t)

	deadline := time.Now().Add(15 * time.Second) // 退避最长 30s，这里给足重试窗口
	for time.Now().Before(deadline) {
		if cfg.Load().N == 42 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("重连后 N = %d，期望 42——收到 ready 时必须重拉一次配置", cfg.Load().N)
}

// 分区隔离穿到 SDK 出口。
// 【辨别力】两个分区的值必须不同，否则"分区对了"和"压根没分区"同结果。
func TestPartitionIsolationAtSDKBoundary(t *testing.T) {
	env := newFullEnv(t)
	env.saveConfig(t, "DEFAULT", `{"site.title":{"type":"string","desc":"","value":"后端"}}`, false)
	env.saveConfig(t, "WEB", `{"site.title":{"type":"string","desc":"","value":"前端"}}`, false)

	type cfgT struct{ Site siteT }
	type siteT struct{ Title string }

	cfg, err := fpsdk.Bind[cfgT](env.client)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}
	if got := cfg.Load().Site.Title; got != "后端" {
		t.Fatalf("Bind 拿到 %q，期望 后端", got)
	}

	web, err := fpsdk.BindType(env.client, "WEB")
	if err != nil {
		t.Fatalf("BindType 失败: %v", err)
	}
	if got := web.Load()["site.title"]; got != "前端" {
		t.Fatalf("BindType 拿到 %v，期望 前端", got)
	}
}
```

**实现者注意**：`newFullEnv` / `saveConfig` / `putConfig` / `newClient` / `stopGRPC` / `startGRPC` 要写。前四个照既有集成测试的脚手架扩展；后两个需要能停掉再起一个监听同一端口的 gRPC 服务端——照 `internal/grpcapi/server_test.go` 里起服务端的方式，把 listener 的地址记下来复用。

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/integration -run 'TestConfig|TestSaveWithoutPush|TestReadyRepulls|TestPartitionIsolation' -v`
Expected: 编译失败或断言失败

- [ ] **Step 3: 补齐脚手架直到测试通过**

这一步不写新的产品代码——前 14 个任务已经把功能做完了。这里只写测试脚手架。**如果某条测试暴露出真实缺陷，回到对应的任务修，不要在集成测试里绕过去。**

Run: `./scripts/test.sh ./internal/integration -v`
Expected: PASS

- [ ] **Step 4: 跑全量测试**

Run: `./scripts/test.sh`
Expected: PASS

Run: `cd web && npx vitest run && npx tsc -b && npm run build`
Expected: 全绿

- [ ] **Step 5: 确认 `web/dist/.gitkeep` 仍被版本库跟踪**

Run: `git ls-files web/dist/.gitkeep`
Expected: 输出 `web/dist/.gitkeep`。它是 `//go:embed all:dist` 在前端未构建时能通过编译的唯一依托——`internal/integration/console_test.go` 有一条测试查的是**索引不是磁盘**，因为危险情形恰恰是"从版本库删了但本地还在"。

- [ ] **Step 6: 提交**

```bash
git add internal/integration/config_test.go
git commit -m "test(config): 端到端穿透——控制台改值直到 SDK 出口"
```

---

## 收尾清单

全部任务完成后逐条确认：

- [ ] `./scripts/test.sh` 全绿
- [ ] `cd web && npx vitest run && npx tsc -b` 全绿
- [ ] `./scripts/gen.sh` 跑过且产物已提交
- [ ] 17 处标「辨别力」的测试全部做过变异验证——计划里已经显式写出变异步骤的有 6 处（Task 3/6/9/10/11/13），其余 11 处实现者自己照同样方式做一遍
- [ ] `sdk/` 仍未 import `internal/`、仍无 `panic`（`./scripts/test.sh ./sdk -run TestArch`）
- [ ] `docs/superpowers/specs/2026-08-24-fp-foundation-platform-design.md` 第六节已按本模块的四条反转更新（环境维度、本地快照、secret、默认值与校验规则），否则下一个人会读到两份互相矛盾的设计
- [ ] 写一份交接记录追加到设计文档末尾，照前三个阶段的格式：**没做完的事**、**留给下一阶段的待办**、**计划本身被实现者挑出的缺陷**、**新确立的技术约定**
