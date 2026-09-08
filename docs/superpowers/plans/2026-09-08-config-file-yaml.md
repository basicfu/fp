# fp / fp-im 启动配置 YAML 化实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 fp 与 fp-im 各读一份自己的 YAML 启动配置文件（`config.yaml` / `config-im.yaml`），彻底不再从环境变量读产品配置。

**Architecture:** 两个 config 包各自把 `Load()` 从"读一堆 `os.Getenv`"改成"读一个 YAML 文件"。Go 结构体的字段树与 YAML 的字段树 1:1 同构，每个字段显式写 `yaml` tag，解码开 `KnownFields(true)` 让拼错的键当场报错。默认值靠"先填好一个带默认值的 `Config`、再让 yaml 覆盖它上面出现过的字段"实现。fp-im 额外引入一个 `Duration` 类型（`UnmarshalYAML` 走 `time.ParseDuration`、拒绝裸数字），并删掉 `FP_IM_FP_INSECURE` 旋钮、改由 `env` 推导传输安全。

**Tech Stack:** Go 1.25 / gopkg.in/yaml.v3 / PostgreSQL 18 / Redis / gRPC

## Global Constraints

- **设计文档**：`docs/superpowers/specs/2026-09-08-config-file-yaml-design.md`。每个任务开始前读一遍对应章节。
- **跑测试只用 `./scripts/test.sh`**，它从 `.env.local` 载入 `FP_TEST_POSTGRES_URL` / `FP_TEST_REDIS_URL` 并加 `-p 1`。直接 `go test` 会因为缺环境变量而 `t.Fatal`。
- **不引入 yaml.v3 之外的任何新依赖。** 新增直接依赖必须先登记进 `internal/integration/dependency_whitelist_test.go` 的 `allowedDirectDependencies`。
- **注释和错误信息一律中文**，与仓库既有风格一致。
- **`sdk/` 不得 import `internal/`**，由 `sdk/arch_test.go` 守护。本计划不碰 `sdk/`，但改 `internal/im/*` 时不要顺手违反。
- **错误信息里一律用 YAML 路径**（`postgres.url`、`node.dead_after`），不用环境变量名；文件名取自实际加载的路径，不是硬编码字面量。
- **每个字段都必须显式写 `yaml` tag。** yaml.v3 的默认规则是把字段名整个小写，`DeadAfter` 会变成 `deadafter` 而不是 `dead_after`。
- **提交粒度**：每个 Task 的每个 TDD 循环结束就 commit，不攒。
- **绝不 `git add -A`。** 这个分支上带着一批与本计划无关的、未提交的配置中心改动（`internal/domain/config.go`、`web/**`、`internal/httpapi/config*` 等 44 个文件）。每次提交都显式列出文件路径。

---

## 文件结构

**新建：**

| 文件 | 职责 |
|---|---|
| `config.example.yaml` | fp 配置模板，含全部项与注释；进 git |
| `config-im.example.yaml` | fp-im 配置模板；进 git |
| `config.yaml` | 本机实际配置，含密钥；**不进 git** |
| `config-im.yaml` | 同上 |

**改：**

| 文件 | 改什么 |
|---|---|
| `go.mod` / `go.sum` | 把 `gopkg.in/yaml.v3` 落成直接依赖 |
| `internal/integration/dependency_whitelist_test.go` | 白名单补 `gopkg.in/yaml.v3` 的理由 |
| `internal/config/config.go` | 从 env 读改成从 YAML 读；具名嵌套类型 |
| `internal/config/config_test.go` | `t.Setenv` 改成写临时 YAML 文件 |
| `internal/im/config/config.go` | 同上；加 `Duration`；删 `FPInsecure`，加 `Insecure()` |
| `internal/im/config/config_test.go` | 同上 |
| `internal/im/fpauth/fpauth_test.go` | 补一条守住"`FPAddr` 为空必须报错"的测试 |
| `cmd/fp/main.go` | 加 `-c` 标志；字段改名 |
| `cmd/fp-im/main.go` | 加 `-c` 标志；字段改名；`Insecure()` |
| `cmd/fp-im/main_test.go` | `testConfig` 改用具名类型的复合字面量 |
| `cmd/fp-dbclean/main.go` | 改成走 `internal/config` |
| `scripts/run.sh` / `run-im.sh` / `db-clean.sh` | 不再 source `env.sh` |
| `scripts/env.sh` | 缩成只载入 `.env.local` |
| `.gitignore` | 加 `/config.yaml`、`/config-im.yaml` |
| `docs/console.md` / `docs/im.md` | 配置章节重写 |

**删：** `.env.example`

---

## Task 1: 把 gopkg.in/yaml.v3 落成登记在案的直接依赖

`gopkg.in/yaml.v3` 眼下只存在于**未提交**的工作区改动里（配置中心那批改动引入的），
`git show HEAD:go.mod` 里没有它。本计划的所有代码都要用它，所以先把它连同白名单
条目一起提交，让这个分支自身能编译。

**Files:**
- Modify: `go.mod`、`go.sum`
- Modify: `internal/integration/dependency_whitelist_test.go:39`

**Interfaces:**
- Produces: `gopkg.in/yaml.v3` 可被 `internal/config` 与 `internal/im/config` import

- [ ] **Step 1: 确认 yaml.v3 已经在工作区的 go.mod 顶层 require 块里**

```bash
grep -n "gopkg.in/yaml.v3" go.mod
go mod why -m github.com/rogpeppe/go-internal
```

预期：`go.mod` 第 20 行左右有 `gopkg.in/yaml.v3 v3.0.1`；`go mod why` 显示
`go-internal` 是 yaml.v3 测试依赖链上的 indirect（`yaml.v3.test → check.v1 → kr/pretty → go-internal/fmtsort`），
也就是说整个 `go.mod` diff 都是"加 yaml.v3"这一件事，没有夹带别的依赖。

- [ ] **Step 2: 把白名单的理由改写成同时覆盖两个使用方**

`internal/integration/dependency_whitelist_test.go` 里现在这两行：

```go
	"gopkg.in/yaml.v3",           // 配置中心改成整份 YAML 存储：domain.ParseConfigYAML 用它把管理端
	// 提交的 YAML 原文解析成 map[string]any，交给 GetConfig 序列化成 JSON 吐给 SDK。
```

改成：

```go
	"gopkg.in/yaml.v3",           // 两个使用方：(1) 配置中心改成整份 YAML 存储，domain.ParseConfigYAML
	// 用它把管理端提交的 YAML 原文解析成 map[string]any，交给 GetConfig 序列化成 JSON 吐给 SDK；
	// (2) 启动配置本身也是 YAML，internal/config 与 internal/im/config 用它读 config.yaml /
	// config-im.yaml，并开 KnownFields(true) 让拼错的键当场报错。
```

- [ ] **Step 3: 跑白名单测试**

```bash
./scripts/test.sh ./internal/integration -run TestGoModDirectDependenciesAreWhitelisted -v
```

预期：PASS。

- [ ] **Step 4: 提交**

```bash
git add go.mod go.sum internal/integration/dependency_whitelist_test.go
git commit -m "build: 把 gopkg.in/yaml.v3 落成登记在案的直接依赖"
```

---

## Task 2: internal/config 改读 YAML

**Files:**
- Modify: `internal/config/config.go`（整份重写）
- Test: `internal/config/config_test.go`（整份重写）

**Interfaces:**
- Produces:
  - `config.DefaultPath = "config.yaml"`
  - `config.Load(path string) (*Config, error)`
  - `Config{Env string; Log Log; HTTP, GRPC Listen; Postgres, Redis Endpoint; BootstrapAdmin BootstrapAdmin; SMS SMS}`
  - `Log{Level string}`、`Listen{Addr string}`、`Endpoint{URL string}`
  - `BootstrapAdmin{User, Password string}`
  - `SMS{Aliyun Aliyun}`、`Aliyun{AccessKeyID, AccessKeySecret, SignName, TemplateLoginCode, Endpoint string}`
  - `(*Config).IsProd() bool`

- [ ] **Step 1: 写失败的测试**

整份替换 `internal/config/config_test.go`：

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig 把 body 写成一个临时的 config.yaml 并返回它的路径。
// 每条测试一个独立的 t.TempDir()，互不干扰；也不再有旧版 t.Setenv 那种
// "本机 .env.local 恰好设了同名变量"的污染问题——文件的内容完全由测试决定。
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("写临时配置文件：%v", err)
	}
	return p
}

// minimal 是只写了必填项的最小配置，供只关心默认值或某一项的测试复用。
const minimal = `
postgres:
  url: postgres://x/y
redis:
  url: redis://localhost:6379/0
`

// TestLoadReadsEveryField 逐字段断言，是 yaml tag 的护栏。
//
// 漏写 tag 的多词字段（比如 BootstrapAdmin 上漏了 `yaml:"bootstrap_admin"`）
// 会被 yaml.v3 按"字段名整个小写"的默认规则映射到 bootstrapadmin，于是配置
// 文件里的 bootstrap_admin 变成未知键——KnownFields(true) 会把它报成"配置
// 文件写错了"，指错方向。只有逐字段断言读到的值才能把这类错误钉在加载器上。
func TestLoadReadsEveryField(t *testing.T) {
	p := writeConfig(t, `
env: prod
log:
  level: debug
http:
  addr: ":18080"
grpc:
  addr: ":19090"
postgres:
  url: postgres://u:p@h:5432/fp?sslmode=disable
redis:
  url: redis://:pw@h:6379/0
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
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"env", cfg.Env, "prod"},
		{"log.level", cfg.Log.Level, "debug"},
		{"http.addr", cfg.HTTP.Addr, ":18080"},
		{"grpc.addr", cfg.GRPC.Addr, ":19090"},
		{"postgres.url", cfg.Postgres.URL, "postgres://u:p@h:5432/fp?sslmode=disable"},
		{"redis.url", cfg.Redis.URL, "redis://:pw@h:6379/0"},
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

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
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

// TestLoadRejectsUnknownField 是这次从环境变量迁到文件净新增的能力：
// 环境变量那个介质压根没有"这个键我不认识"的概念，拼错只会静默回落到
// 默认值；文件有，所以必须报错。
func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+`
log:
  lvel: debug
`))
	if err == nil {
		t.Fatal("拼错的键必须报错，不能静默用默认值")
	}
	if !strings.Contains(err.Error(), "lvel") {
		t.Errorf("错误信息 %q 里应当出现拼错的那个键名 lvel", err)
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "不存在.yaml")
	if _, err := Load(p); err == nil {
		t.Fatal("文件不存在必须报错，不能回落到一整套默认值")
	}
}

// TestLoadMissingRequired 同时钉住"报错"和"错误信息用 YAML 路径而不是
// 环境变量名"——后者才是这次迁移对排障的实际改善。
func TestLoadMissingRequired(t *testing.T) {
	_, err := Load(writeConfig(t, "redis:\n  url: redis://localhost:6379/0\n"))
	if err == nil {
		t.Fatal("缺 postgres.url 必须报错")
	}
	if !strings.Contains(err.Error(), "postgres.url") {
		t.Errorf("错误信息 %q 里应当出现 YAML 路径 postgres.url", err)
	}
	if strings.Contains(err.Error(), "FP_POSTGRES_URL") {
		t.Errorf("错误信息 %q 里不该再出现环境变量名", err)
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

// TestLoadMissingAliyunRequiredInProd 钉住阿里云短信凭据在生产环境是必填项。
//
// 没有它的话，Load 会允许生产环境下阿里云凭据全部留空——cmd/fp/main.go 会
// 因此在生产环境悄悄退化成假供应商，验证码只进内存、没有任何真实用户能收到，
// 且不会有任何启动期报错，只会在运营发现"用户投诉收不到验证码"时才暴露。
func TestLoadMissingAliyunRequiredInProd(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+`
env: prod
sms:
  aliyun:
    access_key_secret: sk
    sign_name: 签名
    template_login_code: SMS_0001
`))
	if err == nil {
		t.Fatal("生产环境缺 sms.aliyun.access_key_id 必须报错")
	}
	if !strings.Contains(err.Error(), "sms.aliyun.access_key_id") {
		t.Errorf("错误信息 %q 里应当出现 sms.aliyun.access_key_id", err)
	}
}

// TestLoadAllowsMissingAliyunOutsideProd 钉住阿里云短信凭据在非生产环境
// 允许留空——Load 本身不该因为缺它们而失败。本机开发、CI 跑 cmd/fp 二进制，
// 都不该被要求先备齐一份连假的都算不上的阿里云凭据才能启动。真正装配假
// 供应商并打 WARN 的逻辑在 cmd/fp/main.go 里，这里只钉住"Load 不替它做
// 这个决定"。
func TestLoadAllowsMissingAliyunOutsideProd(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatalf("Load() error = %v，非生产环境阿里云凭据允许留空", err)
	}
	if cfg.SMS.Aliyun.AccessKeyID != "" {
		t.Errorf("SMS.Aliyun.AccessKeyID = %q, want empty", cfg.SMS.Aliyun.AccessKeyID)
	}
}
```

- [ ] **Step 2: 跑测试确认它失败**

```bash
./scripts/test.sh ./internal/config
```

预期：编译失败，`undefined: Load` 的签名不匹配（`Load()` 现在不收参数）、
`cfg.Log undefined` 等。

- [ ] **Step 3: 整份重写 internal/config/config.go**

```go
// Package config 从 YAML 文件加载 fp 的启动配置。
//
// 字段树与 config.yaml 的字段树 1:1 同构，中间没有映射层——加了字段却忘了
// 映射是这类加载器最常见的漏，同构就没有这个漏可犯。代价是每个字段都必须
// 显式写 yaml tag：yaml.v3 的默认规则是把字段名整个小写，BootstrapAdmin 会
// 变成 bootstrapadmin 而不是 bootstrap_admin。
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath 是 -c 未指定时读的配置文件。
const DefaultPath = "config.yaml"

// Config 是 fp 进程的全部启动配置。
type Config struct {
	Env            string         `yaml:"env"` // dev / prod，大小写不敏感
	Log            Log            `yaml:"log"`
	HTTP           Listen         `yaml:"http"` // 管理控制台
	GRPC           Listen         `yaml:"grpc"` // SDK 接入
	Postgres       Endpoint       `yaml:"postgres"`
	Redis          Endpoint       `yaml:"redis"`
	BootstrapAdmin BootstrapAdmin `yaml:"bootstrap_admin"`
	SMS            SMS            `yaml:"sms"`
}

type Log struct {
	Level string `yaml:"level"` // debug / info / warn / error
}

type Listen struct {
	Addr string `yaml:"addr"`
}

type Endpoint struct {
	URL string `yaml:"url"`
}

// BootstrapAdmin 是首次启动时创建的平台管理员。两项都填才生效——
// AdminService.EnsureBootstrap 在任一为空时直接跳过。
type BootstrapAdmin struct {
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

type SMS struct {
	Aliyun Aliyun `yaml:"aliyun"`
}

// Aliyun 是阿里云短信供应商的配置（见 cmd/fp/main.go）。
//
// 只在生产环境（IsProd）强制要求前四项非空——notify.FakeProvider 的文档
// 注释本身就写着"用于测试与本地开发"，逼所有人在本机跑 ./scripts/run.sh
// 或 CI 跑 cmd/fp 二进制都先备齐（哪怕是假的）阿里云凭据，是把一条只该管
// 生产的约束错误地套到了所有环境头上。非生产环境缺任何一项时，
// cmd/fp/main.go 会退化成 notify.NewLoggingFakeProvider 并打一条醒目的
// WARN——不静默，只是不强制。四项在非生产环境下也齐全时仍然装配真实供应商，
// 方便有人就是想在本机联调真实短信通道。
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

// Load 读 path 指向的 YAML 文件，填默认值并校验必填项。
func Load(path string) (*Config, error) {
	// 先填默认值再解码：yaml.v3 只写文档里出现过的字段，没出现的原样保留，
	// 于是"默认值"这件事不需要任何额外的 applyDefaults 逻辑。
	c := &Config{
		Env:  "dev",
		Log:  Log{Level: "info"},
		HTTP: Listen{Addr: ":8080"},
		GRPC: Listen{Addr: ":9090"},
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: 打开配置文件 %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	// 拼错的键当场报错，而不是静默回落到默认值。这是从环境变量迁到文件
	// 净新增的能力：env 那个介质压根没有"这个键我不认识"的概念。
	// 与 internal/httpapi 的 decodeJSON 开 DisallowUnknownFields 同一条纪律。
	dec.KnownFields(true)
	// 空文件（或整份只有注释）解码返回 io.EOF，不是错误——它只是"什么都
	// 没覆盖"，默认值原样留着，随后必填校验会告诉调用方到底缺什么。
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: 解析 %s: %w", path, err)
	}

	var missing []string
	if c.Postgres.URL == "" {
		missing = append(missing, "postgres.url")
	}
	if c.Redis.URL == "" {
		missing = append(missing, "redis.url")
	}
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
		return nil, fmt.Errorf("config: %s 缺少必填项 %s", path, strings.Join(missing, ", "))
	}
	return c, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/config -v
```

预期：8 条测试全 PASS。此时 `go build ./...` 仍会因为 `cmd/fp` 还在用旧字段名
而失败，这是预期的，Task 3 解决。

- [ ] **Step 5: 变异验证**

把 `Config.BootstrapAdmin` 的 tag 从 `yaml:"bootstrap_admin"` 临时改成
`yaml:"bootstrapadmin"`，重跑 `./scripts/test.sh ./internal/config`，确认
`TestLoadReadsEveryField` 与 `TestLoadRejectsUnknownField` 之一真的变红
（前者读到空串，或后者不再报未知键）。确认后改回来。

再把 `dec.KnownFields(true)` 临时注释掉，确认 `TestLoadRejectsUnknownField`
变红。确认后改回来。

- [ ] **Step 6: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): fp 的启动配置改从 config.yaml 读"
```

---

## Task 3: cmd/fp 接上新 Config，配上示例与本机配置文件

**Files:**
- Modify: `cmd/fp/main.go`
- Create: `config.example.yaml`
- Create: `config.yaml`（本机文件，不进 git）
- Modify: `.gitignore`
- Modify: `scripts/run.sh`

**Interfaces:**
- Consumes: Task 2 的 `config.Load(path)`、`config.DefaultPath`、`Config` 各字段

- [ ] **Step 1: 写 config.example.yaml**

```yaml
# fp 启动配置。复制成 config.yaml 后填入真实值——config.yaml 含密钥，
# 已在 .gitignore 里，不会被提交。
#
# 二进制默认读 ./config.yaml，用 -c 指定别的路径：
#   ./fp -c /etc/fp/config.yaml

env: dev                       # dev / prod，大小写不敏感

log:
  level: info                  # debug / info / warn / error

http:
  addr: ":8080"                # 管理控制台
grpc:
  addr: ":9090"                # SDK 接入

postgres:
  url: postgres://postgres:password@127.0.0.1:5432/fp?sslmode=disable
redis:
  url: redis://:password@127.0.0.1:6379/0

# 首次启动创建的平台管理员。两项都填才生效——EnsureBootstrap 在任一为空时
# 直接跳过，不报错也不提示，结果是库里没有任何管理员、控制台登不进去。
# 已存在同名账号时不会覆盖（ON CONFLICT DO NOTHING），改密码要直接改库。
bootstrap_admin:
  user: admin
  password: admin

# 阿里云短信。只在 env: prod 时前四项强制必填；非 prod 缺任一项会退化成
# 内存假供应商（验证码不会真的发送，而是以 WARN 级别打进 fp 自己的日志），
# 启动日志会打一条 WARN 提示这一点，不会静默降级。四项在非生产环境下也
# 填齐的话，仍然会使用真实的阿里云供应商。
sms:
  aliyun:
    access_key_id: ""
    access_key_secret: ""
    sign_name: ""
    template_login_code: ""    # fp 模板 key "login_code" 对应的阿里云模板 ID
    endpoint: ""               # 留空则用阿里云短信默认接入点，任何环境都不必填
```

- [ ] **Step 2: 把 config.yaml 加进 .gitignore**

在 `.gitignore` 的 `.env.local` 那一行之后插入：

```
# 启动配置：含 PG/Redis 密码与阿里云凭据。仓库里只留 *.example.yaml。
/config.yaml
/config-im.yaml
```

- [ ] **Step 3: 生成本机的 config.yaml**

**凭据从本机 `.env.local` 现有的零件拼，不要从本计划文档抄** —— 这份文档进 git，
里面不能有真实口令。先把零件读进 shell 变量，再让脚本替你填：

```bash
set -a; . ./.env.local; set +a
cat > config.yaml <<YAML
env: dev
log:
  level: info
http:
  addr: ":8080"
grpc:
  addr: ":9090"
postgres:
  url: postgres://${FP_PG_USER}:${FP_PG_PASSWORD}@${FP_PG_HOST}:${FP_PG_PORT}/fp?sslmode=disable
redis:
  url: redis://:${FP_REDIS_PASSWORD}@${FP_REDIS_HOST}:${FP_REDIS_PORT}/0
bootstrap_admin:
  user: admin
  password: admin
sms:
  aliyun:
    access_key_id: ${FP_ALIYUN_ACCESS_KEY_ID}
    access_key_secret: ${FP_ALIYUN_ACCESS_KEY_SECRET}
    sign_name: ${FP_ALIYUN_SMS_SIGN_NAME}
    template_login_code: ${FP_ALIYUN_SMS_TEMPLATE_LOGIN_CODE}
    endpoint: ""
YAML
git check-ignore -v config.yaml
```

（注意这里的 heredoc 定界符是不带引号的 `YAML`，`${}` 才会被展开；上面
`config.example.yaml` 那个用的是带引号的 `'YAML'`，原样写入不展开。）

预期：`git check-ignore` 输出 `.gitignore:<行号>:/config.yaml	config.yaml`，
确认它真的被忽略了。随后 `grep -c . config.yaml` 应当看到各字段都填上了值，
没有留下未展开的 `${...}`。

- [ ] **Step 4: 改 cmd/fp/main.go**

`import` 块加 `"flag"`（放在 `"errors"` 之后、`"fmt"` 之前，保持字母序）。

`run()` 开头由：

```go
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.LogLevel)
```

改成：

```go
func run() error {
	cfgPath := flag.String("c", config.DefaultPath, "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.Log.Level)
```

其余按下表逐处替换（全文件共 11 处）：

| 原 | 新 |
|---|---|
| `cfg.PostgresURL` | `cfg.Postgres.URL` |
| `cfg.RedisURL` | `cfg.Redis.URL` |
| `cfg.BootstrapAdminUser` | `cfg.BootstrapAdmin.User` |
| `cfg.BootstrapAdminPassword` | `cfg.BootstrapAdmin.Password` |
| `cfg.AliyunAccessKeyID` | `cfg.SMS.Aliyun.AccessKeyID` |
| `cfg.AliyunAccessKeySecret` | `cfg.SMS.Aliyun.AccessKeySecret` |
| `cfg.AliyunSMSSignName` | `cfg.SMS.Aliyun.SignName` |
| `cfg.AliyunSMSTemplateLoginCode` | `cfg.SMS.Aliyun.TemplateLoginCode` |
| `cfg.AliyunEndpoint` | `cfg.SMS.Aliyun.Endpoint` |
| `cfg.HTTPAddr` | `cfg.HTTP.Addr` |
| `cfg.GRPCAddr` | `cfg.GRPC.Addr` |

`cfg.Env`、`cfg.IsProd()` 不变。

- [ ] **Step 5: 改 scripts/run.sh**

整份替换：

```bash
#!/usr/bin/env bash
# 起 fp。配置全部来自 ./config.yaml（从 config.example.yaml 复制一份填），
# 不再需要 scripts/env.sh —— 那里现在只剩测试与 examples/demo 用的环境变量。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if [ ! -f config.yaml ]; then
  echo "缺少 $ROOT/config.yaml。请从 config.example.yaml 复制一份并填入凭据。" >&2
  exit 1
fi
go run ./cmd/fp "$@"
```

- [ ] **Step 6: 编译与静态检查**

```bash
go build ./... && go vet ./cmd/fp ./internal/config
```

预期：都通过。（`cmd/fp-im` 此时还没改，但它用的是 `internal/im/config`，
不受影响，所以 `go build ./...` 应当整体通过。）

- [ ] **Step 7: 冒烟——真的起一次 fp**

```bash
./scripts/run.sh &
sleep 8
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/
curl -s -X POST http://127.0.0.1:8080/api/admin/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin"}' | head -c 200
kill %1
```

预期：首个 curl 返回 `200`；登录返回带 token 的 JSON（或前端未构建时的提示页，
但登录接口必须成功）。启动日志里应当有 `fp 启动 env=dev http=:8080 grpc=:9090`。

再验一次"配置文件缺失/写错就该失败"：

```bash
go run ./cmd/fp -c 不存在.yaml 2>&1 | head -3
```

预期：`ERROR fp 启动失败 err="config: 打开配置文件 不存在.yaml: ..."`，退出码非 0。

- [ ] **Step 8: 提交**

```bash
git add cmd/fp/main.go config.example.yaml .gitignore scripts/run.sh
git commit -m "feat(cmd/fp): 读 config.yaml，加 -c 标志"
```

---

## Task 4: cmd/fp-dbclean 接上新 Config

**Files:**
- Modify: `cmd/fp-dbclean/main.go:70-105`
- Modify: `scripts/db-clean.sh`

**Interfaces:**
- Consumes: Task 2 的 `config.Load(path)`、`config.DefaultPath`

- [ ] **Step 1: 改 cmd/fp-dbclean/main.go**

`import` 块里删掉 `"os"`（如果 `os` 在文件别处还有用就保留），加
`"github.com/basicfu/fp/internal/config"`。

`run()` 里的标志声明加一个 `-c`：

```go
	var (
		cfgPath   = flag.String("c", config.DefaultPath, "配置文件路径")
		assumeYes = flag.Bool("y", false, "跳过确认（供 CI 使用）")
		skipRedis = flag.Bool("no-redis", false, "只清 Postgres，不动 Redis")
	)
```

把这两段：

```go
	pgURL := os.Getenv("FP_POSTGRES_URL")
	if pgURL == "" {
		return errors.New("未设置 FP_POSTGRES_URL（用 ./scripts/db-clean.sh 跑，它会从 .env.local 载入）")
	}
	pgCfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		return fmt.Errorf("解析 FP_POSTGRES_URL: %w", err)
	}
```

```go
	redisURL := os.Getenv("FP_REDIS_URL")
	var redisOpt *redis.Options
	if !*skipRedis {
		if redisURL == "" {
			// 不静默跳过：只清 Postgres 会留下指向已删数据的会话、撤销
			// epoch 与配置推送信号，是个很难查的中间态。
			return errors.New("未设置 FP_REDIS_URL。确实只想清 Postgres 请显式加 -no-redis")
		}
		if redisOpt, err = redis.ParseURL(redisURL); err != nil {
			return fmt.Errorf("解析 FP_REDIS_URL: %w", err)
		}
	}
```

改成：

```go
	// 清的是 config.yaml 指向的那套库，也就是开发库——语义与改成读环境变量
	// 之前完全一致，只是来源换成了配置文件。config.Load 已经保证
	// postgres.url 与 redis.url 非空，所以这里不用再判空。
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	pgCfg, err := pgxpool.ParseConfig(cfg.Postgres.URL)
	if err != nil {
		return fmt.Errorf("解析 %s 的 postgres.url: %w", *cfgPath, err)
	}
```

```go
	var redisOpt *redis.Options
	if !*skipRedis {
		// 不静默跳过：只清 Postgres 会留下指向已删数据的会话、撤销
		// epoch 与配置推送信号，是个很难查的中间态。
		if redisOpt, err = redis.ParseURL(cfg.Redis.URL); err != nil {
			return fmt.Errorf("解析 %s 的 redis.url: %w", *cfgPath, err)
		}
	}
```

（`conn := pgCfg.ConnConfig` 与 `dbName := conn.Database` 两行保持不变。）

- [ ] **Step 2: 改 scripts/db-clean.sh**

把开头注释里这一段：

```bash
# 清的是 FP_POSTGRES_URL / FP_REDIS_URL，也就是**开发库**（env.sh 里的 fp / DB 0），
# 不是测试库。测试库由 testsupport 在每次跑测试时自己清，不需要这个脚本。
```

改成：

```bash
# 清的是 ./config.yaml 里的 postgres.url / redis.url，也就是**开发库**，
# 不是测试库。测试库由 testsupport 在每次跑测试时自己清，不需要这个脚本。
```

并把这两行：

```bash
# shellcheck disable=SC1091
. "$ROOT/scripts/env.sh"
```

删掉。

- [ ] **Step 3: 编译与冒烟**

```bash
go build ./... && go vet ./cmd/fp-dbclean
./scripts/db-clean.sh 2>&1 | head -12
```

预期：`go build`/`go vet` 通过；`db-clean.sh` 不带模式参数时打出 usage 并
以"请指定模式：truncate 或 reset"退出——**不要**真的执行 truncate 或 reset。

再验一次目标打印读的是 config.yaml：

```bash
echo n | ./scripts/db-clean.sh truncate 2>&1 | head -12
```

预期：打出 `即将清理：` 与 `Postgres  postgres@127.0.0.1:5432/fp`（也就是
`config.yaml` 里那套开发库），随后因为确认输入不匹配而中止，**不会**真的清库。

- [ ] **Step 4: 提交**

```bash
git add cmd/fp-dbclean/main.go scripts/db-clean.sh
git commit -m "refactor(cmd/fp-dbclean): 目标库改从 config.yaml 读"
```

---

## Task 5: internal/im/config 改读 YAML

**Files:**
- Modify: `internal/im/config/config.go`（整份重写）
- Test: `internal/im/config/config_test.go`（整份重写）

**Interfaces:**
- Produces:
  - `config.DefaultPath = "config-im.yaml"`
  - `config.Load(path string) (*Config, error)`
  - `config.MinIdleTimeout`（**保持 `time.Duration`**，`internal/integration/im_parity_test.go:196` 拿它跟 `fpim.PingInterval` 比）
  - `Duration`（底层 `time.Duration`）及其 `Std() time.Duration`、`String() string`
  - `Config{Env string; Log Log; HTTP HTTP; GRPC Listen; Redis Endpoint; FPSDK FPSDK; AppsFile string; Node Node; Conn Conn; Pipeline Pipeline}`
  - `Log{Level string}`、`Listen{Addr string}`、`HTTP{Addr string; TrustProxy bool}`、`Endpoint{URL string}`、`FPSDK{Addr string}`
  - `Node{Heartbeat, DeadAfter Duration}`
  - `Conn{FieldTTL, FieldRenew, IdleTimeout, AuthTimeout Duration; SendQueue int}`
  - `Pipeline{FlushInterval Duration; FlushSize int}`
  - `(*Config).IsProd() bool`、`(*Config).Insecure() bool`
- **不再有** `Config.FPAddr`、`Config.FPInsecure`、`Config.HTTPAddr`、`Config.GRPCAddr`、`Config.RedisURL`、`Config.LogLevel`、`Config.TrustProxy`

- [ ] **Step 1: 写失败的测试**

整份替换 `internal/im/config/config_test.go`：

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config-im.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("写临时配置文件：%v", err)
	}
	return p
}

// minimal 只写必填项，供只关心默认值或某一项的测试复用。
const minimal = `
redis:
  url: redis://localhost:6379/0
apps_file: apps.json
`

// TestLoadReadsEveryField 逐字段断言，是 yaml tag 的护栏——多词字段
// （dead_after、field_ttl、trust_proxy、flush_interval…）漏写 tag 会被
// yaml.v3 按"字段名整个小写"映射成 deadafter 之类，配置文件里的正确写法
// 反而变成未知键。
func TestLoadReadsEveryField(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
env: prod
log:
  level: debug
http:
  addr: ":18081"
  trust_proxy: true
grpc:
  addr: ":19091"
redis:
  url: redis://:pw@h:6379/2
fpsdk:
  addr: fp.internal:9090
apps_file: ./tmp/im-apps.json
node:
  heartbeat: 1s
  dead_after: 4s
conn:
  field_ttl: 20m
  field_renew: 5m
  idle_timeout: 90s
  auth_timeout: 3s
  send_queue: 128
pipeline:
  flush_interval: 5ms
  flush_size: 64
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != "prod" || cfg.Log.Level != "debug" {
		t.Errorf("env/log.level = %q/%q, want prod/debug", cfg.Env, cfg.Log.Level)
	}
	if cfg.HTTP.Addr != ":18081" || !cfg.HTTP.TrustProxy {
		t.Errorf("http = %+v, want addr :18081 且 trust_proxy true", cfg.HTTP)
	}
	if cfg.GRPC.Addr != ":19091" {
		t.Errorf("grpc.addr = %q, want :19091", cfg.GRPC.Addr)
	}
	if cfg.Redis.URL != "redis://:pw@h:6379/2" {
		t.Errorf("redis.url = %q", cfg.Redis.URL)
	}
	if cfg.FPSDK.Addr != "fp.internal:9090" {
		t.Errorf("fpsdk.addr = %q, want fp.internal:9090", cfg.FPSDK.Addr)
	}
	if cfg.AppsFile != "./tmp/im-apps.json" {
		t.Errorf("apps_file = %q", cfg.AppsFile)
	}
	if cfg.Node.Heartbeat.Std() != time.Second || cfg.Node.DeadAfter.Std() != 4*time.Second {
		t.Errorf("node = %+v, want heartbeat 1s / dead_after 4s", cfg.Node)
	}
	if cfg.Conn.FieldTTL.Std() != 20*time.Minute || cfg.Conn.FieldRenew.Std() != 5*time.Minute ||
		cfg.Conn.IdleTimeout.Std() != 90*time.Second || cfg.Conn.AuthTimeout.Std() != 3*time.Second ||
		cfg.Conn.SendQueue != 128 {
		t.Errorf("conn = %+v", cfg.Conn)
	}
	if cfg.Pipeline.FlushInterval.Std() != 5*time.Millisecond || cfg.Pipeline.FlushSize != 64 {
		t.Errorf("pipeline = %+v", cfg.Pipeline)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != "dev" || cfg.Log.Level != "info" {
		t.Errorf("env/log.level = %q/%q, want dev/info", cfg.Env, cfg.Log.Level)
	}
	if cfg.HTTP.Addr != ":8081" || cfg.HTTP.TrustProxy {
		t.Errorf("http = %+v, want addr :8081 且 trust_proxy false", cfg.HTTP)
	}
	if cfg.GRPC.Addr != ":9091" {
		t.Errorf("grpc.addr = %q, want :9091", cfg.GRPC.Addr)
	}
	if cfg.Node.Heartbeat.Std() != 3*time.Second || cfg.Node.DeadAfter.Std() != 10*time.Second ||
		cfg.Conn.FieldTTL.Std() != 30*time.Minute || cfg.Conn.FieldRenew.Std() != 10*time.Minute ||
		cfg.Conn.IdleTimeout.Std() != 60*time.Second || cfg.Conn.AuthTimeout.Std() != 5*time.Second ||
		cfg.Conn.SendQueue != 256 || cfg.Pipeline.FlushInterval != 0 || cfg.Pipeline.FlushSize != 1 {
		t.Fatalf("默认值与设计文档第四节不符：%+v", cfg)
	}
}

// TestLoadDoesNotRequireFPSDKAddr 钉住"谁用谁校验"：config 包不知道谁会用
// 这个地址，不该替使用方决定它是不是必需。空值仍会在启动时炸，但那是
// fpauth.New 的职责（见 internal/im/fpauth 的 TestNewRequiresFPAddr）。
func TestLoadDoesNotRequireFPSDKAddr(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatalf("Load() error = %v，fpsdk.addr 不该是 config 的必填项", err)
	}
	if cfg.FPSDK.Addr != "" {
		t.Errorf("fpsdk.addr = %q, want empty", cfg.FPSDK.Addr)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+`
conn:
  idle_timout: 90s
`))
	if err == nil {
		t.Fatal("拼错的键必须报错，不能静默用默认值")
	}
	if !strings.Contains(err.Error(), "idle_timout") {
		t.Errorf("错误信息 %q 里应当出现拼错的那个键名", err)
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "不存在.yaml")); err == nil {
		t.Fatal("文件不存在必须报错")
	}
}

// TestLoadRequiresRedisAndAppsFile 两个必填项各自单独缺失都要报错，
// 且错误信息用 YAML 路径而不是环境变量名。
func TestLoadRequiresRedisAndAppsFile(t *testing.T) {
	_, err := Load(writeConfig(t, "apps_file: apps.json\n"))
	if err == nil || !strings.Contains(err.Error(), "redis.url") {
		t.Fatalf("缺 redis.url 必须报错且信息里含 redis.url，实际 %v", err)
	}
	if strings.Contains(err.Error(), "FP_IM_") {
		t.Errorf("错误信息 %q 里不该再出现环境变量名", err)
	}
	_, err = Load(writeConfig(t, "redis:\n  url: redis://localhost:6379/0\n"))
	if err == nil || !strings.Contains(err.Error(), "apps_file") {
		t.Fatalf("缺 apps_file 必须报错且信息里含 apps_file，实际 %v", err)
	}
}

// TestDurationRejectsBareNumber：配置里写 2 是想表达 2 秒还是 2 纳秒，
// 没人说得清，与其猜一个不如让配置加载直接失败。与 model.Duration 同一纪律。
func TestDurationRejectsBareNumber(t *testing.T) {
	if _, err := Load(writeConfig(t, minimal+"node:\n  heartbeat: 2\n")); err == nil {
		t.Fatal("裸数字时长必须报错")
	}
	if _, err := Load(writeConfig(t, minimal+"node:\n  heartbeat: 不是时长\n")); err == nil {
		t.Fatal("非法时长必须报错，而不是静默变成 0")
	}
	// 0s 是合法的：pipeline.flush_interval 的"不合批"就是它。
	if _, err := Load(writeConfig(t, minimal+"pipeline:\n  flush_interval: 0s\n")); err != nil {
		t.Fatalf("0s 应当合法：%v", err)
	}
}

// TestInsecureDerivesFromEnv 钉住第六节：删掉 insecure 旋钮之后，fp-im 连 fp
// 走不走明文完全由 env 决定。这条判定有真实的安全后果——appSecret 随每个
// RPC 的 metadata 发送，明文传输等于把一个能签发任意用户会话的凭据印在网线上。
func TestInsecureDerivesFromEnv(t *testing.T) {
	for _, tt := range []struct {
		env  string
		want bool
	}{
		{"dev", true},
		{"", true},
		{"prod", false},
		{"PROD", false},
		{"Prod", false},
		{"production", true}, // 只认 prod，不做前缀匹配
	} {
		if got := (&Config{Env: tt.env}).Insecure(); got != tt.want {
			t.Errorf("Env=%q Insecure() = %v, want %v", tt.env, got, tt.want)
		}
	}
}

func TestLoadRejectsBadPipelineAndRenew(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+"pipeline:\n  flush_size: 64\n"))
	if err == nil || !strings.Contains(err.Error(), "pipeline.flush_interval") {
		t.Fatalf("flush_size>1 且无 flush_interval 必须报错，实际 %v", err)
	}
	_, err = Load(writeConfig(t, minimal+"conn:\n  field_renew: 20m\n"))
	if err == nil || !strings.Contains(err.Error(), "conn.field_renew") {
		t.Fatalf("field_renew 必须小于 field_ttl 的一半，实际 %v", err)
	}
}

// TestLoadRejectsNonPositiveCounts 覆盖旧 num() 助手曾经承担的"必须是正整数"
// 校验：换成 YAML 之后 yaml.v3 只管把整数解出来，下界得自己判，否则
// send_queue: 0 会一路走到 hub 里变成一个零容量发送队列。
func TestLoadRejectsNonPositiveCounts(t *testing.T) {
	if _, err := Load(writeConfig(t, minimal+"conn:\n  send_queue: 0\n")); err == nil {
		t.Fatal("conn.send_queue 必须 >= 1")
	}
	if _, err := Load(writeConfig(t, minimal+"pipeline:\n  flush_size: 0\n")); err == nil {
		t.Fatal("pipeline.flush_size 必须 >= 1")
	}
}

func TestLoadRejectsDeadAfterNotGreaterThanHeartbeat(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+"node:\n  heartbeat: 10s\n  dead_after: 10s\n"))
	if err == nil || !strings.Contains(err.Error(), "node.dead_after") {
		t.Fatalf("dead_after 必须大于 heartbeat，相等也要报错，实际 %v", err)
	}
	if _, err := Load(writeConfig(t, minimal+"node:\n  heartbeat: 10s\n  dead_after: 9s\n")); err == nil {
		t.Fatal("dead_after 小于 heartbeat 更加不合理，必须报错")
	}
}

// TestLoadRejectsFieldTTLBelowOneSecond：conn.field_ttl 会被
// registry.Conns.Handshake 换算成 int64(seconds) 传给 Redis 的 HEXPIRE，
// 小于一秒的值会被截断成 0，而 HEXPIRE 传 0 的语义是"立刻让这个 field 过期"
// ——把它配成几百毫秒，效果不是"缩短存活时长"，而是每条连接刚握手登记就被
// Redis 判定过期删除，client 无法查到自己是谁在线。必须在配置加载阶段就
// 拒绝，不能等到线上排查"连接注册了但查不到"这种诡异现象。
//
// field_renew 默认 10m 而这里的 field_ttl 都远小于 1s：不显式把 renew 也
// 调小的话，renew*2 >= ttl 那条既有校验会先一步报错，就验证不到本条。
func TestLoadRejectsFieldTTLBelowOneSecond(t *testing.T) {
	for _, ttl := range []string{"500ms", "999ms"} {
		body := minimal + "conn:\n  field_renew: 100ms\n  field_ttl: " + ttl + "\n"
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Fatalf("conn.field_ttl = %s 小于 1s，换算成整秒会被截断为 0，必须报错", ttl)
		}
	}
	body := minimal + "conn:\n  field_renew: 100ms\n  field_ttl: 1s\n"
	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("conn.field_ttl 恰好等于 1s 应该通过：%v", err)
	}
}

// TestLoadRejectsIdleTimeoutBelowClientHeartbeat 是给空闲超时加下界的那一半。
//
// 另一半（下界这个数字确实等于 client SDK 心跳间隔的两倍）在
// internal/integration 的 TestClientPingAndIdleTimeoutPairing 里，那里同时
// 看得见 sdk/im 与本包；本包看不见 sdk/im，只能守住"下界被真的执行了"。
func TestLoadRejectsIdleTimeoutBelowClientHeartbeat(t *testing.T) {
	if _, err := Load(writeConfig(t, minimal+"conn:\n  idle_timeout: 20s\n")); err == nil {
		t.Fatal("空闲超时低于 client 心跳间隔的两倍必须报错：" +
			"配成 20s 会让全网每个 client 每 20 秒被踢一次并立即重连（4005 的契约就是不退避），形成稳定的重连风暴")
	}
	// 恰好等于下界要放行：下界是"允许的最小值"，不是"必须严格大于"。
	body := minimal + "conn:\n  idle_timeout: " + MinIdleTimeout.String() + "\n"
	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("空闲超时恰好等于下界应放行，实际报错：%v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认它失败**

```bash
./scripts/test.sh ./internal/im/config
```

预期：编译失败（`Load()` 签名不匹配、`cfg.HTTP undefined`、`Duration undefined` 等）。

- [ ] **Step 3: 整份重写 internal/im/config/config.go**

```go
// Package config 从 YAML 文件加载 fp-im 的启动配置。风格与 internal/config
// 一致：字段树与 config-im.yaml 1:1 同构，每个字段显式写 yaml tag，解码开
// KnownFields(true)，必填项缺失直接报错。
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath 是 -c 未指定时读的配置文件。
const DefaultPath = "config-im.yaml"

// Duration 是配置里写成 "3s" 这种人类可读时长的配置项。
//
// 不直接用 time.Duration：yaml.v3 会把它当成一个 int64 纳秒数，配置文件里
// 写 3000000000 既难读又容易错一个数量级。与 internal/im/model.Duration 是
// 同一件事的 YAML 版本；两者不合并，因为那个服务的是 apps 文件（JSON），
// 而 internal/im/config 不该为了复用一个十行的类型去 import internal/im/model。
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }
func (d Duration) String() string     { return time.Duration(d).String() }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	// 明确拒绝裸数字：写 2 是 2 秒还是 2 纳秒，没人说得清，与其猜一个
	// 不如让配置加载直接失败。yaml.v3 解一个 !!int 节点进 string 会报错，
	// 这里正是要那个错误。
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("config: 时长必须是带单位的字符串，例如 \"3s\"：%w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: 无法解析时长 %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

type Config struct {
	Env      string   `yaml:"env"` // dev / prod，大小写不敏感
	Log      Log      `yaml:"log"`
	HTTP     HTTP     `yaml:"http"` // client 的 WebSocket 接入
	GRPC     Listen   `yaml:"grpc"` // 业务 server 接入
	Redis    Endpoint `yaml:"redis"`
	FPSDK    FPSDK    `yaml:"fpsdk"`
	AppsFile string   `yaml:"apps_file"`
	Node     Node     `yaml:"node"`
	Conn     Conn     `yaml:"conn"`
	Pipeline Pipeline `yaml:"pipeline"`
}

type Log struct {
	Level string `yaml:"level"`
}

type Listen struct {
	Addr string `yaml:"addr"`
}

type HTTP struct {
	Addr string `yaml:"addr"`
	// TrustProxy 决定访客限流的 IP 取不取 X-Forwarded-For 的最右一跳。
	// 前面确实有可信反代时才开——开在裸奔的服务上等于让客户端自己声明 IP。
	TrustProxy bool `yaml:"trust_proxy"`
}

type Endpoint struct {
	URL string `yaml:"url"`
}

// FPSDK 是 fp SDK 的连接参数。Addr 是 fp 的 **gRPC** 地址，不是 HTTP。
//
// 这里没有 insecure 开关：传输安全由 Config.Insecure() 从 env 推导。一个
// 默认值为 true 的 insecure 旋钮，最可能的失效方式就是有人把它连同整份 dev
// 配置抄到生产上，而 appSecret 随每个 RPC 的 metadata 明文发送，抓到包就
// 等于拿到一个能签发任意用户会话的凭据。
//
// Addr 为空**不由本包校验**——config 不知道谁会用这个地址，"谁用谁校验"。
// 空值仍然会在启动装配阶段炸掉，那是 fpauth.New 的职责。
type FPSDK struct {
	Addr string `yaml:"addr"`
}

type Node struct {
	Heartbeat Duration `yaml:"heartbeat"`
	DeadAfter Duration `yaml:"dead_after"`
}

type Conn struct {
	FieldTTL    Duration `yaml:"field_ttl"`
	FieldRenew  Duration `yaml:"field_renew"`
	IdleTimeout Duration `yaml:"idle_timeout"`
	AuthTimeout Duration `yaml:"auth_timeout"`
	SendQueue   int      `yaml:"send_queue"`
}

type Pipeline struct {
	FlushInterval Duration `yaml:"flush_interval"`
	FlushSize     int      `yaml:"flush_size"`
}

// MinIdleTimeout 是 conn.idle_timeout 的下界：client SDK 心跳间隔的两倍。
//
// 25 秒这个数字是硬编码抄过来的，对应 sdk/im 的 PingInterval——client SDK
// 在一条已建立的连接上每 25 秒发一次心跳帧。这里不能 import 那个常量：
// internal/ 不该反过来依赖 sdk 的实现细节，而 sdk/ 也不得 import
// internal/（见 sdk/arch_test.go），两边只能各写一份。真正把这两个数字
// 钉在一起的是 internal/integration 里的
// TestClientPingAndIdleTimeoutPairing——只有那里同时看得见两个包。
//
// 为什么要有下界：空闲超时一旦小于等于心跳间隔，全网每个 client 都会被
// 周期性地空闲超时踢下线，而 4005 的契约恰恰是"立即重连、不退避"，于是
// 形成一场稳定的重连风暴——配成 20 秒就够了。取两倍而不是刚好一倍，是
// 为了留出至少一个心跳周期的余量：网络抖动、client 忙、时钟漂移都可能让
// 某一次心跳晚到，只留一倍余量的话这些正常抖动就会变成断线。
//
// 类型保持 time.Duration（而不是本包的 Duration）：
// internal/integration/im_parity_test.go 拿它直接跟 fpim.PingInterval 比。
const MinIdleTimeout = 2 * 25 * time.Second

// IsProd 报告当前是否为生产环境。
func (c *Config) IsProd() bool { return strings.EqualFold(c.Env, "prod") }

// Insecure 报告 fp-im 连 fp 的 gRPC 是否走明文。
//
// **前提**：fp 的 gRPC 服务端目前没有传 grpc.Creds，只服务明文，所以
// env: prod 下这条 TLS 必须由前面的反代 / 网关终结。见 docs/im.md。
func (c *Config) Insecure() bool { return !c.IsProd() }

// Load 读 path 指向的 YAML 文件，填默认值并校验。
func Load(path string) (*Config, error) {
	// 先填默认值再解码：yaml.v3 只写文档里出现过的字段，没出现的原样保留。
	c := &Config{
		Env:  "dev",
		Log:  Log{Level: "info"},
		HTTP: HTTP{Addr: ":8081"},
		GRPC: Listen{Addr: ":9091"},
		Node: Node{
			Heartbeat: Duration(3 * time.Second),
			DeadAfter: Duration(10 * time.Second),
		},
		Conn: Conn{
			FieldTTL:    Duration(30 * time.Minute),
			FieldRenew:  Duration(10 * time.Minute),
			IdleTimeout: Duration(60 * time.Second),
			AuthTimeout: Duration(5 * time.Second),
			SendQueue:   256,
		},
		Pipeline: Pipeline{FlushSize: 1},
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: 打开配置文件 %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: 解析 %s: %w", path, err)
	}

	var missing []string
	// fpsdk.addr 刻意不在这个清单里，理由见 FPSDK 的注释。
	for _, kv := range []struct{ path, v string }{
		{"redis.url", c.Redis.URL},
		{"apps_file", c.AppsFile},
	} {
		if kv.v == "" {
			missing = append(missing, kv.path)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: %s 缺少必填项 %s", path, strings.Join(missing, ", "))
	}

	// 计数类的下界：旧版靠 num() 助手统一挡，换成 YAML 之后 yaml.v3 只管把
	// 整数解出来，下界得自己判——send_queue: 0 会一路走到 hub 里变成一个
	// 零容量发送队列。
	if c.Conn.SendQueue < 1 {
		return nil, fmt.Errorf("config: %s 里 conn.send_queue 必须是正整数，当前为 %d", path, c.Conn.SendQueue)
	}
	if c.Pipeline.FlushSize < 1 {
		return nil, fmt.Errorf("config: %s 里 pipeline.flush_size 必须是正整数，当前为 %d", path, c.Pipeline.FlushSize)
	}
	if c.Pipeline.FlushSize > 1 && c.Pipeline.FlushInterval <= 0 {
		return nil, fmt.Errorf("config: %s 里 pipeline.flush_size > 1 时必须设置 pipeline.flush_interval", path)
	}
	if c.Node.DeadAfter <= c.Node.Heartbeat {
		return nil, fmt.Errorf("config: %s 里 node.dead_after 必须大于 node.heartbeat", path)
	}
	if c.Conn.FieldRenew*2 >= c.Conn.FieldTTL {
		return nil, fmt.Errorf("config: %s 里 conn.field_renew 必须小于 conn.field_ttl 的一半", path)
	}
	if c.Conn.IdleTimeout.Std() < MinIdleTimeout {
		return nil, fmt.Errorf("config: %s 里 conn.idle_timeout 必须 >= %s（client SDK 每 25 秒发一次心跳，"+
			"空闲超时低于这个量级会让全网 client 被周期性踢下线，而 4005 的契约是立即重连不退避，形成重连风暴），当前为 %s",
			path, MinIdleTimeout, c.Conn.IdleTimeout)
	}
	// registry.Conns.Handshake 把 field_ttl 换算成 int64(seconds) 传给 Redis
	// 的 HEXPIRE。小于一秒的值转换成整秒会被截断为 0，而 HEXPIRE 的字段 TTL
	// 传 0 的语义是"让这个字段立刻过期"——不是"几乎不过期"，效果是每条连接
	// 刚握手登记就被 Redis 删除，client 查不到自己是谁在线，现象极难定位到
	// 是这里的配置问题。所以这里直接拒绝，而不是留给运行期悄悄截断。
	if c.Conn.FieldTTL.Std() < time.Second {
		return nil, fmt.Errorf("config: %s 里 conn.field_ttl 必须 >= 1s"+
			"（会被换算成整秒传给 Redis HEXPIRE，小于一秒会截断为 0，语义是立刻删除），当前为 %s",
			path, c.Conn.FieldTTL)
	}
	return c, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/im/config -v
```

预期：12 条测试全 PASS。`go build ./...` 此时会因为 `cmd/fp-im` 还在用旧字段名
而失败，这是预期的，Task 6 解决。

- [ ] **Step 5: 变异验证**

把 `Conn.IdleTimeout` 的 tag 从 `yaml:"idle_timeout"` 临时改成 `yaml:"idletimeout"`，
重跑，确认 `TestLoadReadsEveryField` 变红。改回来。

把 `Duration.UnmarshalYAML` 里的 `n.Decode(&s)` 临时换成
`n.Decode((*time.Duration)(d))`，确认 `TestDurationRejectsBareNumber` 变红。改回来。

- [ ] **Step 6: 提交**

```bash
git add internal/im/config/config.go internal/im/config/config_test.go
git commit -m "feat(im/config): fp-im 的启动配置改从 config-im.yaml 读"
```

---

## Task 6: cmd/fp-im 接上新 Config，补 fpauth 的守护测试

**Files:**
- Modify: `cmd/fp-im/main.go`
- Modify: `cmd/fp-im/main_test.go:67-97`
- Modify: `internal/im/fpauth/fpauth_test.go`
- Create: `config-im.example.yaml`
- Create: `config-im.yaml`（本机文件，不进 git）
- Modify: `scripts/run-im.sh`

**Interfaces:**
- Consumes: Task 5 的 `config.Load(path)`、`config.DefaultPath`、`Duration.Std()`、`(*Config).Insecure()`

- [ ] **Step 1: 先补 fpauth 的守护测试**

`fpsdk.addr` 从 config 的必填清单里拿掉之后，`fpauth.New` 里那句
`cfg.FPAddr == ""` 检查成了唯一挡住"地址没填"的东西，但眼下没有任何测试
钉住它。往 `internal/im/fpauth/fpauth_test.go` 末尾追加：

```go
// TestNewRequiresFPAddr 钉住"fpsdk.addr 为空必须在装配阶段就失败"。
//
// internal/im/config 按"谁用谁校验"把这一项从必填清单里拿掉了，这句检查
// 因此成了唯一的挡板。不能指望 fpsdk.New 自己报——client 是懒建的（每个
// app 一个，首次握手才建），那样"地址配错了"会被推迟到第一个真实用户握手
// 的那一刻才暴露。
func TestNewRequiresFPAddr(t *testing.T) {
	if _, err := New(Config{Apps: apps{}}); err == nil {
		t.Fatal("FPAddr 为空必须报错：config 已不再校验这一项，这里是唯一的挡板")
	}
	if _, err := New(Config{FPAddr: "127.0.0.1:1"}); err == nil {
		t.Fatal("Apps 为 nil 必须报错")
	}
}
```

跑：

```bash
./scripts/test.sh ./internal/im/fpauth -run TestNewRequiresFPAddr -v
```

预期：PASS（`fpauth.New` 里已有这个检查，这条测试是把既有行为钉住，不是新增行为）。

变异验证：把 `fpauth.New` 里的 `if cfg.FPAddr == "" || cfg.Apps == nil` 临时改成
`if cfg.Apps == nil`，确认这条测试变红。改回来。

- [ ] **Step 2: 提交守护测试**

```bash
git add internal/im/fpauth/fpauth_test.go
git commit -m "test(im/fpauth): 钉住 FPAddr 为空必须在装配阶段失败"
```

- [ ] **Step 3: 写 config-im.example.yaml**

```yaml
# fp-im 启动配置。复制成 config-im.yaml 后填入真实值——config-im.yaml 含
# 密钥，已在 .gitignore 里，不会被提交。
#
# 二进制默认读 ./config-im.yaml，用 -c 指定别的路径：
#   ./fp-im -c /etc/fp/config-im.yaml
#
# fp-im 强制依赖 fp（它必须连 fp 的 gRPC 验 token），但这份配置完全独立于
# config.yaml：不引用、不继承它的任何默认值。想跟 fp 共用一个 Redis，就把
# 同一条 URL 抄到下面 redis.url。

env: dev                       # dev / prod，大小写不敏感

log:
  level: info                  # debug / info / warn / error

http:
  addr: ":8081"                # client 的 WebSocket 接入
  # 前面确实有可信反代时才开：开启后访客限流的 IP 取 X-Forwarded-For 的
  # 最右一跳。开在裸奔的服务上等于让客户端自己声明 IP。
  trust_proxy: false
grpc:
  addr: ":9091"                # 业务 server 接入

redis:
  url: redis://:password@127.0.0.1:6379/0

# fp SDK 的连接参数。addr 是 fp 的 gRPC 地址，不是 HTTP。
#
# 没有 insecure 开关：传输安全由上面的 env 推导——dev 明文，prod 走 TLS。
# 【部署硬要求】fp 的 gRPC 服务端目前只服务明文，所以 env: prod 下这条 TLS
# 必须由 fp 前面的反代 / 网关终结。
fpsdk:
  addr: localhost:9090

# 接入应用清单。除 app_id/app_secret 外都有默认值，10 秒检测 mtime 热更新，
# 坏文件保留旧配置。格式见 docs/im.md。
apps_file: ./tmp/im-apps.json

node:
  heartbeat: 3s
  dead_after: 10s              # 必须大于 heartbeat

conn:
  field_ttl: 30m               # 必须 >= 1s（会被换算成整秒传给 Redis HEXPIRE）
  field_renew: 10m             # 必须小于 field_ttl 的一半
  idle_timeout: 60s            # 必须 >= 50s（client SDK 每 25 秒发一次心跳）
  auth_timeout: 5s             # 握手帧的等待上限
  send_queue: 256

pipeline:
  flush_interval: 0s           # 0s = 不合批
  flush_size: 1                # > 1 时必须同时设 flush_interval
```

- [ ] **Step 4: 生成本机的 config-im.yaml**

同样从 `.env.local` 的零件拼，不要从本文档抄真实口令：

```bash
set -a; . ./.env.local; set +a
cat > config-im.yaml <<YAML
env: dev
log:
  level: info
http:
  addr: ":8081"
  trust_proxy: false
grpc:
  addr: ":9091"
redis:
  url: redis://:${FP_REDIS_PASSWORD}@${FP_REDIS_HOST}:${FP_REDIS_PORT}/0
fpsdk:
  addr: localhost:9090
apps_file: ./tmp/im-apps.json
node:
  heartbeat: 3s
  dead_after: 10s
conn:
  field_ttl: 30m
  field_renew: 10m
  idle_timeout: 60s
  auth_timeout: 5s
  send_queue: 256
pipeline:
  flush_interval: 0s
  flush_size: 1
YAML
git check-ignore -v config-im.yaml
```

预期：`git check-ignore` 确认它被忽略。

- [ ] **Step 5: 改 cmd/fp-im/main.go**

`import` 块加 `"flag"`。

`run()` 由：

```go
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.LogLevel)
```

改成：

```go
func run() error {
	cfgPath := flag.String("c", config.DefaultPath, "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.Log.Level)
```

`serve()` 里按下表逐处替换（行号以改动前的文件为准）：

| 行 | 原 | 新 |
|---|---|---|
| 94 | `redisx.Open(ctx, cfg.RedisURL)` | `redisx.Open(ctx, cfg.Redis.URL)` |
| 99 | `cfg.Pipeline.FlushInterval, cfg.Pipeline.FlushSize` | `cfg.Pipeline.FlushInterval.Std(), cfg.Pipeline.FlushSize` |
| 110 | `cfg.Node.Heartbeat, cfg.Node.DeadAfter` | `cfg.Node.Heartbeat.Std(), cfg.Node.DeadAfter.Std()` |
| 111 | `cfg.Conn.FieldTTL` | `cfg.Conn.FieldTTL.Std()` |
| 139 | `FPAddr: cfg.FPAddr, Insecure: cfg.FPInsecure,` | `FPAddr: cfg.FPSDK.Addr, Insecure: cfg.Insecure(),` |
| 185 | `cfg.Conn.FieldRenew` | `cfg.Conn.FieldRenew.Std()` |
| 192 | `AuthTimeout: cfg.Conn.AuthTimeout, IdleTimeout: cfg.Conn.IdleTimeout, SendQueue: cfg.Conn.SendQueue, TrustProxy: cfg.TrustProxy` | `AuthTimeout: cfg.Conn.AuthTimeout.Std(), IdleTimeout: cfg.Conn.IdleTimeout.Std(), SendQueue: cfg.Conn.SendQueue, TrustProxy: cfg.HTTP.TrustProxy` |
| 195 | `&http.Server{Addr: cfg.HTTPAddr, ...}` | `&http.Server{Addr: cfg.HTTP.Addr, ...}` |
| 202 | `net.Listen("tcp", cfg.HTTPAddr)` | `net.Listen("tcp", cfg.HTTP.Addr)` |
| 206 | `net.Listen("tcp", cfg.GRPCAddr)` | `net.Listen("tcp", cfg.GRPC.Addr)` |

`cfg.AppsFile`（105 行）保持不变。

- [ ] **Step 6: 改 cmd/fp-im/main_test.go 的 testConfig**

具名类型让整个构造能写成一个复合字面量，不必再"先建空 Config 再逐字段赋值"。
把 `testConfig`（67–97 行）整段替换成：

```go
func testConfig(t *testing.T, appsFile string) *config.Config {
	t.Helper()
	url := os.Getenv("FP_TEST_REDIS_URL")
	if url == "" {
		t.Fatal("缺少 FP_TEST_REDIS_URL，请用 ./scripts/test.sh 跑测试")
	}
	return &config.Config{
		Env:      "dev",
		Log:      config.Log{Level: "warn"},
		HTTP:     config.HTTP{Addr: "127.0.0.1:0"},
		GRPC:     config.Listen{Addr: "127.0.0.1:0"},
		Redis:    config.Endpoint{URL: url},
		AppsFile: appsFile,
		// 访客握手不会走到 Authenticator，这个地址永远不会被真的拨号；
		// 但 fpauth.New 要求非空，所以给一个必然不通的地址，万一哪天真的
		// 被拨了，失败会立刻暴露而不是悄悄连上别的东西。
		FPSDK: config.FPSDK{Addr: "127.0.0.1:1"},
		// 心跳 200ms（而不是生产的 3 秒）：测试里要等的传播延迟就是心跳周期
		// 本身，取生产值只会把每条测试拖慢十几倍。
		Node: config.Node{
			Heartbeat: config.Duration(200 * time.Millisecond),
			DeadAfter: config.Duration(2 * time.Second),
		},
		Conn: config.Conn{
			FieldTTL:    config.Duration(30 * time.Minute),
			FieldRenew:  config.Duration(10 * time.Minute),
			IdleTimeout: config.Duration(60 * time.Second),
			AuthTimeout: config.Duration(2 * time.Second),
			SendQueue:   64,
		},
		Pipeline: config.Pipeline{FlushSize: 1},
	}
}
```

注意 `Env` 用 `"dev"` 而不是原来的 `"TEST"`：`Insecure()` 现在从它推导，
`"TEST"` 同样会落到"非 prod ⇒ 明文"这一侧，但写 `dev` 才是在说人话。

- [ ] **Step 7: 改 scripts/run-im.sh**

整份替换：

```bash
#!/usr/bin/env bash
# 起 fp-im。配置全部来自 ./config-im.yaml（从 config-im.example.yaml 复制
# 一份填），与 fp 的 config.yaml 完全独立。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if [ ! -f config-im.yaml ]; then
  echo "缺少 $ROOT/config-im.yaml。请从 config-im.example.yaml 复制一份并填入凭据。" >&2
  exit 1
fi
exec go run ./cmd/fp-im "$@"
```

- [ ] **Step 8: 编译、静态检查、跑全量测试**

```bash
go build ./... && go vet ./... && gofmt -l cmd internal
./scripts/test.sh
```

预期：`go build`/`go vet` 通过，`gofmt -l` 无输出，全部测试包 `ok`
（对照基线：26 个 `ok`、0 个 `FAIL`）。

- [ ] **Step 9: 冒烟——真的起一次 fp-im**

先确保 `tmp/im-apps.json` 存在（`tmp/` 是 gitignore 的，可能是空的）：

```bash
mkdir -p tmp
cat > tmp/im-apps.json <<'JSON'
{"apps":[{"app_id":"smoke","app_secret":"smoke-secret","allow_guest":true}]}
JSON
./scripts/run-im.sh &
sleep 6
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/healthz || true
kill %1
```

预期：进程起来，日志里没有配置相关的报错。（`/healthz` 若不存在则返回 404
也算通过——这一步验的是"能带着 config-im.yaml 起来"，不是 HTTP 路由。）

再验一次校验真的生效：

```bash
printf 'redis:\n  url: redis://x\napps_file: a.json\nconn:\n  idle_timeout: 20s\n' > /tmp/bad-im.yaml
go run ./cmd/fp-im -c /tmp/bad-im.yaml 2>&1 | head -3
```

预期：`config: /tmp/bad-im.yaml 里 conn.idle_timeout 必须 >= 50s（...）`，退出码非 0。

- [ ] **Step 10: 提交**

```bash
git add cmd/fp-im/main.go cmd/fp-im/main_test.go config-im.example.yaml scripts/run-im.sh
git commit -m "feat(cmd/fp-im): 读 config-im.yaml，传输安全改由 env 推导"
```

---

## Task 7: 收尾 scripts/env.sh 与 .env

**Files:**
- Modify: `scripts/env.sh`
- Delete: `.env.example`
- Modify: `.env.local`（本机文件，不进 git）

**Interfaces:**
- Consumes: 无
- Produces: `scripts/test.sh` 与 `scripts/demo.sh` source `env.sh` 后仍能拿到
  `FP_TEST_POSTGRES_URL`、`FP_TEST_REDIS_URL`、`FP_APP_ID`、`FP_APP_SECRET`

- [ ] **Step 1: 改写 .env.local（本机文件）**

`.env.local` 从此只放"测试与示例的环境变量"，不再是 fp 的配置。产品配置全在
`config.yaml` / `config-im.yaml` 里。

**凭据仍然从现有 `.env.local` 的零件拼，不要从本文档抄真实口令**——这份计划
文档进 git。旧文件里的 `FP_APP_ID` / `FP_APP_SECRET` 原样保留，只是把
`FP_PG_*` / `FP_REDIS_*` 零件换成拼好的两条测试库连接串：

```bash
set -a; . ./.env.local; set +a
{
  cat <<'ENV'
# 本机测试与 examples/ 用的环境变量。fp / fp-im 自己的配置在 config.yaml
# 与 config-im.yaml 里，不在这个文件。

# 测试库连接串。开发库与测试库必须分开：测试会 TRUNCATE 全部业务表 /
# FLUSHDB，绝不能跑在开发库上（internal/testsupport 直读这两个变量）。
ENV
  echo "FP_TEST_POSTGRES_URL=postgres://${FP_PG_USER}:${FP_PG_PASSWORD}@${FP_PG_HOST}:${FP_PG_PORT}/fp_test?sslmode=disable"
  echo "FP_TEST_REDIS_URL=redis://:${FP_REDIS_PASSWORD}@${FP_REDIS_HOST}:${FP_REDIS_PORT}/1"
  echo
  echo "# examples/demo：手工验收时创建的应用凭据（见 examples/demo/README.md 第 1 步）。"
  echo "FP_APP_ID=${FP_APP_ID}"
  echo "FP_APP_SECRET=${FP_APP_SECRET}"
} > .env.local.new
mv .env.local.new .env.local
```

写完 `grep -c '\${' .env.local` 应当是 0——没有未展开的变量残留。
`git check-ignore -v .env.local` 应当确认它仍然被忽略。

- [ ] **Step 2: 改写 scripts/env.sh**

整份替换：

```bash
#!/usr/bin/env bash
# 载入 .env.local 里的环境变量。被 test.sh 与 demo.sh 复用。
#
# 这里只剩两类东西：测试库连接串（internal/testsupport 直读
# FP_TEST_POSTGRES_URL / FP_TEST_REDIS_URL）和 examples/demo 的应用凭据。
# fp 与 fp-im 自己的启动配置在 config.yaml / config-im.yaml 里，不经过这里
# ——run.sh、run-im.sh、db-clean.sh 都不再 source 本文件。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ ! -f "$ROOT/.env.local" ]; then
  echo "缺少 $ROOT/.env.local。它只放测试库连接串与 examples/demo 的应用凭据；" >&2
  echo "fp / fp-im 的配置在 config.yaml / config-im.yaml（见 *.example.yaml）。" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1091
. "$ROOT/.env.local"
set +a
```

- [ ] **Step 3: 删掉 .env.example**

```bash
git rm .env.example
```

- [ ] **Step 4: 验证测试与 demo 链路仍然通**

```bash
./scripts/test.sh ./internal/config ./internal/im/config ./internal/testsupport
bash -c '. ./scripts/env.sh; echo "PG=${FP_TEST_POSTGRES_URL:0:20}… APP=${FP_APP_ID:0:8}…"'
```

预期：测试 PASS；第二条打出两个非空的前缀，说明 `env.sh` 仍然把这几个变量
带出来了。

- [ ] **Step 5: 跑一次全量测试**

```bash
./scripts/test.sh
```

预期：26 个包全 `ok`、0 个 `FAIL`。

- [ ] **Step 6: 提交**

```bash
git add scripts/env.sh .env.example
git commit -m "chore(scripts): env.sh 缩成只载入测试与示例的环境变量，删 .env.example"
```

---

## Task 8: 文档

**Files:**
- Modify: `docs/console.md:9-28`
- Modify: `docs/im.md:8-18`、`docs/im.md:159`、`docs/im.md:170`、`docs/im.md:190`

- [ ] **Step 1: 改 docs/console.md 的"构建与运行"节**

把 `## 构建与运行` 下从"**必须走 `scripts/run.sh`**"到"改这两个环境变量不会改密码。"
这一整段（第 9–28 行）替换成：

```markdown
    ./scripts/build-web.sh      # 构建前端，产物落在 web/dist/
    cp config.example.yaml config.yaml   # 首次：填入 PG / Redis 连接串
    ./scripts/run.sh            # 起 fp，浏览器打开 http://localhost:8080/

`fp` 读 `./config.yaml`（用 `-c` 可以指定别的路径），文件不存在直接启动失败：

    ERROR fp 启动失败 err="config: 打开配置文件 config.yaml: ..."

拼错的键也会当场报错，不会静默回落到默认值——`log: {lvel: debug}` 启动不了。
这是从环境变量迁到配置文件换来的：环境变量那个介质压根没有"这个键我不认识"
的概念。

`scripts/run.sh` 只是个"检查 config.yaml 在不在，然后 `go run ./cmd/fp`"的
包装，所以直接跑构建出来的二进制也完全可以：

    go build -o fp ./cmd/fp && ./fp

**控制台的登录账号**：`config.yaml` 里的 `bootstrap_admin.user` /
`bootstrap_admin.password`，`config.example.yaml` 里给的是 `admin` / `admin`。
两项都填才生效——`EnsureBootstrap` 在任一为空时直接跳过，不报错也不提示，
结果是库里没有任何管理员、控制台登不进去。注意它是 `ON CONFLICT DO NOTHING`
——账号一旦建过，改这两项不会改密码，得直接改库。
```

- [ ] **Step 2: 改 docs/im.md 的"启动"节**

把第 8–18 行（```bash 代码块起，到"必须自己显式传全这三个变量。"止）替换成：

````markdown
```bash
cp config-im.example.yaml config-im.yaml   # 首次：填入 Redis 连接串与 fp 地址
./scripts/run-im.sh
```

`fp-im` 读 `./config-im.yaml`（`-c` 可指定别的路径），与 fp 的 `config.yaml`
**完全独立**：不引用、不继承它的任何默认值。想跟 fp 共用一个 Redis，就把同一条
URL 抄进 `config-im.yaml` 的 `redis.url`——旧版那条"`FP_IM_REDIS_URL` 未设时
复用 `FP_REDIS_URL`"的 shell 回退没有了，隐式继承比多抄一行难懂得多。

`redis.url` 与 `apps_file` 是必填项，缺一个直接启动失败。`fpsdk.addr`（fp 的
**gRPC** 地址，不是 HTTP）不由 `internal/im/config` 校验——"谁用谁校验"，它由
`fpauth.New` 在装配阶段挡住，报错时机一样是启动时。

**传输安全没有开关，由 `env` 推导**：`env: dev` 明文，`env: prod` 走 TLS。
一个默认为 true 的 `insecure` 旋钮最可能的失效方式就是被连同整份 dev 配置抄到
生产上，而 `appSecret` 是随每个 RPC 的 metadata 明文发的。

> **【部署硬要求】** fp 的 gRPC 服务端目前没有传 `grpc.Creds`，只服务明文。
> 所以 `env: prod` 下 fp-im 连 fp 的那条 TLS **必须**由 fp 前面的反代 / 网关
> 终结。在给 fp 的 gRPC 补上 TLS 之前，这不是可选项。
````

- [ ] **Step 3: 把 docs/im.md 里剩下的环境变量名换成 YAML 路径**

| 行 | 原 | 新 |
|---|---|---|
| 159 | `` `FP_IM_CONN_IDLE_TIMEOUT` 有 50 秒下界 `` | `` `conn.idle_timeout` 有 50 秒下界 `` |
| 170 | `` **`FP_IM_TRUST_PROXY`**：开启后…… `` | `` **`http.trust_proxy`**：开启后…… `` |
| 190 | `` **`FP_IM_CONN_IDLE_TIMEOUT` 现在有 50 秒下界……** `` | `` **`conn.idle_timeout` 现在有 50 秒下界……** `` |

用一次全文替换扫干净，确认没有遗漏：

```bash
grep -n "FP_IM_\|FP_POSTGRES_URL\|FP_REDIS_URL\|FP_BOOTSTRAP\|FP_ALIYUN\|FP_HTTP_ADDR\|FP_GRPC_ADDR\|FP_ENV\|FP_LOG_LEVEL" docs/*.md
```

预期：无输出。（`FP_TEST_*`、`FP_APP_ID`、`FP_APP_SECRET`、`FP_ADDR` 仍然是
环境变量，如果文档里提到它们是对的，上面的 grep 模式刻意不匹配它们。）

- [ ] **Step 4: 全仓库扫一遍残留**

```bash
grep -rn "config.Load()" --include=*.go . | grep -v '/.claude/'
grep -rn "Getenv(\"FP_" --include=*.go . | grep -v '/.claude/'
```

预期：第一条无输出；第二条只剩 `internal/testsupport/db.go`、
`internal/testsupport/redis.go`、`examples/demo/main.go`、`examples/im-demo/main.go`
和 `sdk/doc.go` 的注释——这些都在"明确不做"里。

- [ ] **Step 5: 最后跑一次全量测试与格式检查**

```bash
gofmt -l cmd internal sdk
go vet ./...
./scripts/test.sh
```

预期：`gofmt -l` 无输出，`go vet` 通过，26 个包全 `ok`。

- [ ] **Step 6: 提交**

```bash
git add docs/console.md docs/im.md
git commit -m "docs: 配置章节改写成 config.yaml / config-im.yaml"
```
