# fp 第一阶段（一）：服务端核心 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建成 fp 服务端核心——能通过管理 API 创建应用、用两种方式（密码 / 短信验证码）注册登录、签发与校验 opaque token、延期轮换撤销，全部有集成测试覆盖。

**Architecture:** 分层：`domain`（纯类型与错误，不依赖任何传输与存储）→ `store`（PostgreSQL + Redis）→ `service`（业务逻辑，传输无关）→ `httpapi`（管理 UI 的薄传输层）。会话主存 Redis，PostgreSQL 只持久化账号数据与登录日志。登录方式通过 `Connector` 接口 + 注册表实现，新增一种登录方式不改动编排逻辑。

**Tech Stack:** Go 1.24 · chi v5 · pgx/v5 · sqlc · goose · go-redis/v9 · PostgreSQL 18 · log/slog · bcrypt

**范围：** 设计大纲的 M1–M7。本计划**不含** gRPC 服务、Go SDK、Demo 服务（见计划二）与管理 UI（见计划三）。

**上游文档：** [2026-08-24-fp-foundation-platform-design.md](../specs/2026-08-24-fp-foundation-platform-design.md)

---

## Global Constraints

- Go 版本 **1.24**，module path `github.com/basicfu/fp`
- 数据库 **PostgreSQL 18**，主键统一 `uuid PRIMARY KEY DEFAULT uuidv7()`（PG 18 内置函数）
- **禁止引入** `iris`、`github.com/basicfu/gf`、MongoDB
- **禁止**在 `app_user` 或 `user_application` 上添加任何 `role` 字段（设计文档 5.5 硬约束）
- `service` 包**不得** import `httpapi`、`net/http` 或任何传输层类型
- 所有时间戳在 Go 侧统一用 **毫秒 int64**（`time.Now().UnixMilli()`），PG 侧用 `timestamptz`
- 所有对外 ID 用 `uuid.UUID`（`github.com/google/uuid`）
- 每个任务结束必须 `git commit`
- 测试依赖通过 `docker-compose.yml` 提供；测试从 `FP_TEST_POSTGRES_URL` / `FP_TEST_REDIS_URL` 读取连接串，**未设置时测试直接失败并打印指引**，不得静默跳过

---

## 对设计文档的两处细化

实施中确定的细节，与设计文档 4.1 的表述有出入，理由记录在此：

**① `identity` 表是所有登录标识的唯一真相源，`app_user` 不放 `phone` / `email` 列。**

设计文档 4.1 写 `user` 表含"手机、邮箱"。改为全部落在 `identity`（`type='phone'` / `type='email'` / `type='username'` / `type='wechat_mp'`…），`app_user` 只保留 `password_hash` 这个**凭据**。理由：手机号既是登录标识又要支持换绑，放两处必然产生同步 bug；`identity` 的 `UNIQUE(type, subject)` 已经提供唯一性约束；按手机号搜索走 `identity` 索引 join，成本可忽略。

**② 表名用 `app_user` 而非 `user`。** `user` 是 PostgreSQL 保留字，用它会导致每处 SQL 都要加双引号。

**③ 第一阶段用手写 pgx 查询，暂不引入 sqlc。**

设计文档技术栈写的是 `pgx/v5 + sqlc`。第一阶段查询数量约 30 条，手写 pgx 完全可控，且计划二马上要引入 protobuf 代码生成工具链——同时上两套 codegen 会显著抬高上手成本。**所有 SQL 集中在 `internal/service/*.go` 的查询常量里**，查询数量增长后迁移到 sqlc 是机械替换，不影响 service 的对外签名。

---

## 文件结构

```
fp/
├── go.mod
├── docker-compose.yml               PG 18 + Redis（开发与测试依赖）
├── sqlc.yaml
├── Makefile
├── cmd/
│   └── fp/main.go                   入口：配置→连接→迁移→起 HTTP→优雅关闭
└── internal/
    ├── config/config.go             环境变量加载与校验
    ├── logging/logging.go           slog 初始化
    ├── domain/
    │   ├── errors.go                领域错误（哨兵错误 + 错误码）
    │   ├── application.go           Application 类型与会话参数
    │   ├── user.go                  User / UserStatus 状态机
    │   ├── identity.go              Identity / IdentityType
    │   └── session.go               Session 类型
    ├── store/
    │   ├── postgres.go              pgx pool + goose 自动迁移
    │   ├── redis.go                 go-redis client
    │   ├── migrations/              *.sql + embed.go
    │   ├── queries/                 sqlc 输入 *.sql
    │   ├── gen/                     sqlc 输出（不手改）
    │   ├── session.go               会话的 Redis 读写
    │   └── ratelimit.go             Redis 频率限制
    ├── testsupport/
    │   ├── db.go                    NewTestDB(t)
    │   └── redis.go                 NewTestRedis(t)
    ├── connector/
    │   ├── connector.go             Connector 接口 + Registry + ConfigSchema
    │   ├── password.go              password connector
    │   └── smscode.go               sms_code connector
    ├── notify/
    │   ├── notify.go                Provider 接口 + Sender 编排
    │   ├── code.go                  验证码生成、存储、校验
    │   ├── fake.go                  测试用 Provider
    │   └── aliyun.go                阿里云短信 Provider
    ├── service/
    │   ├── admin.go                 平台管理员
    │   ├── application.go           应用管理
    │   ├── user.go                  用户与身份、账号归并
    │   ├── session.go               令牌签发/校验/延期/轮换/撤销
    │   ├── loginlog.go              登录日志
    │   └── auth.go                  登录编排
    └── httpapi/
        ├── router.go                路由装配
        ├── respond.go               统一响应与错误映射
        ├── middleware.go            管理员认证中间件
        ├── admin.go                 管理员登录
        ├── application.go           应用管理 API
        └── user.go                  用户管理 API
```

---

## Task 1: 项目骨架与配置加载

**Files:**
- Create: `go.mod`, `docker-compose.yml`, `Makefile`, `.gitignore`
- Create: `cmd/fp/main.go`
- Create: `internal/config/config.go`
- Create: `internal/logging/logging.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `config.Config` 结构体，字段：`Env string`、`HTTPAddr string`、`GRPCAddr string`、`PostgresURL string`、`RedisURL string`、`LogLevel string`、`BootstrapAdminUser string`、`BootstrapAdminPassword string`
  - `func config.Load() (*Config, error)`
  - `func logging.Setup(level string) *slog.Logger`

- [ ] **Step 1: 初始化模块与依赖**

```bash
cd /d/fp
go mod init github.com/basicfu/fp
go get github.com/google/uuid@latest
go get github.com/jackc/pgx/v5@latest
go get github.com/redis/go-redis/v9@latest
go get github.com/go-chi/chi/v5@latest
go get github.com/pressly/goose/v3@latest
go get golang.org/x/crypto/bcrypt@latest
go get golang.org/x/sync/singleflight@latest
```

- [ ] **Step 2: 写 `.gitignore` 与 `docker-compose.yml`**

`.gitignore`：

```
/fp
/fp.exe
/tmp/
.env
```

`docker-compose.yml`：

```yaml
services:
  postgres:
    image: postgres:18-alpine
    environment:
      POSTGRES_USER: fp
      POSTGRES_PASSWORD: fp
      POSTGRES_DB: fp
    ports:
      - "5433:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U fp"]
      interval: 2s
      timeout: 3s
      retries: 20

  redis:
    image: redis:7-alpine
    ports:
      - "6380:6379"
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 2s
      timeout: 3s
      retries: 20
```

> 端口用 5433 / 6380 避开本机可能已有的 PG / Redis。

`Makefile`：

```makefile
.PHONY: up down test run

up:
	docker compose up -d --wait

down:
	docker compose down -v

test: up
	FP_TEST_POSTGRES_URL=postgres://fp:fp@localhost:5433/fp?sslmode=disable \
	FP_TEST_REDIS_URL=redis://localhost:6380/1 \
	go test ./... -count=1

run: up
	FP_POSTGRES_URL=postgres://fp:fp@localhost:5433/fp?sslmode=disable \
	FP_REDIS_URL=redis://localhost:6380/0 \
	FP_BOOTSTRAP_ADMIN_USER=admin \
	FP_BOOTSTRAP_ADMIN_PASSWORD=admin123456 \
	go run ./cmd/fp
```

- [ ] **Step 3: 写失败的测试**

`internal/config/config_test.go`：

```go
package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("FP_POSTGRES_URL", "postgres://x/y")
	t.Setenv("FP_REDIS_URL", "redis://localhost:6379/0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != "DEV" {
		t.Errorf("Env = %q, want DEV", cfg.Env)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.GRPCAddr != ":9090" {
		t.Errorf("GRPCAddr = %q, want :9090", cfg.GRPCAddr)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("FP_ENV", "PROD")
	t.Setenv("FP_HTTP_ADDR", ":18080")
	t.Setenv("FP_POSTGRES_URL", "postgres://x/y")
	t.Setenv("FP_REDIS_URL", "redis://localhost:6379/0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != "PROD" {
		t.Errorf("Env = %q, want PROD", cfg.Env)
	}
	if cfg.HTTPAddr != ":18080" {
		t.Errorf("HTTPAddr = %q, want :18080", cfg.HTTPAddr)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	t.Setenv("FP_POSTGRES_URL", "")
	t.Setenv("FP_REDIS_URL", "redis://localhost:6379/0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for missing FP_POSTGRES_URL")
	}
}
```

- [ ] **Step 4: 运行测试确认失败**

Run: `go test ./internal/config/ -v`
Expected: 编译失败，`undefined: Load`

- [ ] **Step 5: 实现 config**

`internal/config/config.go`：

```go
// Package config 从环境变量加载 fp 的启动配置。
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config 是 fp 进程的全部启动配置。所有字段来自环境变量，前缀 FP_。
type Config struct {
	Env         string // DEV / PROD
	HTTPAddr    string // 管理 UI 的 HTTP 监听地址
	GRPCAddr    string // SDK 的 gRPC 监听地址（计划二使用）
	PostgresURL string
	RedisURL    string
	LogLevel    string // debug / info / warn / error

	// 首次启动时创建的平台管理员。已存在同名管理员时跳过。
	BootstrapAdminUser     string
	BootstrapAdminPassword string
}

// IsProd 报告当前是否为生产环境。
func (c *Config) IsProd() bool { return strings.EqualFold(c.Env, "PROD") }

// Load 读取环境变量并校验必填项。
func Load() (*Config, error) {
	c := &Config{
		Env:                    envOr("FP_ENV", "DEV"),
		HTTPAddr:               envOr("FP_HTTP_ADDR", ":8080"),
		GRPCAddr:               envOr("FP_GRPC_ADDR", ":9090"),
		PostgresURL:            os.Getenv("FP_POSTGRES_URL"),
		RedisURL:               os.Getenv("FP_REDIS_URL"),
		LogLevel:               envOr("FP_LOG_LEVEL", "info"),
		BootstrapAdminUser:     os.Getenv("FP_BOOTSTRAP_ADMIN_USER"),
		BootstrapAdminPassword: os.Getenv("FP_BOOTSTRAP_ADMIN_PASSWORD"),
	}

	var missing []string
	if c.PostgresURL == "" {
		missing = append(missing, "FP_POSTGRES_URL")
	}
	if c.RedisURL == "" {
		missing = append(missing, "FP_REDIS_URL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: 缺少必填环境变量 %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 6: 运行测试确认通过**

Run: `go test ./internal/config/ -v`
Expected: PASS（3 个测试）

- [ ] **Step 7: 实现 logging**

`internal/logging/logging.go`：

```go
// Package logging 初始化进程级结构化日志。
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Setup 按 level 创建 JSON 格式的 slog.Logger，并设为全局默认。
// level 无法识别时回退到 info。
func Setup(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv})
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}
```

- [ ] **Step 8: 实现最小入口**

`cmd/fp/main.go`：

```go
// Command fp 是 Foundation Platform 的服务端进程。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/basicfu/fp/internal/config"
	"github.com/basicfu/fp/internal/logging"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fp 启动失败", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.LogLevel)
	log.Info("fp 启动", "env", cfg.Env, "http", cfg.HTTPAddr, "grpc", cfg.GRPCAddr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	log.Info("fp 收到退出信号，正在关闭")
	return nil
}
```

- [ ] **Step 9: 验证可启动**

Run: `FP_POSTGRES_URL=postgres://x/y FP_REDIS_URL=redis://localhost:6379/0 go run ./cmd/fp`
Expected: 打印一行 JSON 启动日志后挂起；`Ctrl+C` 后打印退出日志并正常返回。

- [ ] **Step 10: 提交**

```bash
git add go.mod go.sum .gitignore docker-compose.yml Makefile cmd internal
git commit -m "feat: 项目骨架、配置加载与结构化日志"
```

---

## Task 2: 数据层（PostgreSQL + goose 迁移 + Redis + 测试支撑）

**Files:**
- Create: `internal/store/postgres.go`, `internal/store/redis.go`
- Create: `internal/store/migrations/embed.go`, `internal/store/migrations/00001_baseline.sql`
- Create: `internal/testsupport/db.go`, `internal/testsupport/redis.go`
- Modify: `cmd/fp/main.go`
- Test: `internal/store/postgres_test.go`, `internal/store/redis_test.go`

**Interfaces:**
- Consumes: `config.Config`
- Produces:
  - `func store.OpenPostgres(ctx context.Context, url string) (*pgxpool.Pool, error)` — 建池并 Ping
  - `func store.Migrate(ctx context.Context, pool *pgxpool.Pool) error` — 执行嵌入的迁移
  - `func store.OpenRedis(ctx context.Context, url string) (*redis.Client, error)`
  - `func testsupport.NewTestDB(t *testing.T) *pgxpool.Pool` — 已迁移；每次调用先清空所有业务表
  - `func testsupport.NewTestRedis(t *testing.T) *redis.Client` — 已 FLUSHDB

- [ ] **Step 1: 写失败的测试**

`internal/store/postgres_test.go`：

```go
package store_test

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/testsupport"
)

func TestMigrateCreatesGooseVersionTable(t *testing.T) {
	pool := testsupport.NewTestDB(t)

	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = 'goose_db_version'`).Scan(&n)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("goose_db_version 表数量 = %d, want 1", n)
	}
}

func TestUUIDv7Available(t *testing.T) {
	pool := testsupport.NewTestDB(t)

	var id string
	if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		t.Fatalf("uuidv7() 不可用，确认 PostgreSQL 版本 >= 18: %v", err)
	}
	if len(id) != 36 {
		t.Fatalf("uuidv7() = %q, 长度 want 36", id)
	}
}
```

`internal/store/redis_test.go`：

```go
package store_test

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/testsupport"
)

func TestRedisRoundTrip(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	ctx := context.Background()

	if err := rdb.Set(ctx, "k", "v", 0).Err(); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := rdb.Get(ctx, "k").Result()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "v" {
		t.Fatalf("Get = %q, want v", got)
	}
}

func TestRedisFlushedBetweenTests(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	ctx := context.Background()

	n, err := rdb.Exists(ctx, "k").Result()
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if n != 0 {
		t.Fatal("上一个测试的 key 未被清理，NewTestRedis 应当 FLUSHDB")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/ -v`
Expected: 编译失败，`no required module provides package .../internal/testsupport`

- [ ] **Step 3: 实现 baseline 迁移**

`internal/store/migrations/00001_baseline.sql`：

```sql
-- +goose Up
-- baseline：只放公共扩展与约定，业务表由后续迁移各自添加。
-- PostgreSQL 18 内置 uuidv7()，无需扩展。
CREATE TABLE IF NOT EXISTS schema_note (
    key   text PRIMARY KEY,
    value text NOT NULL
);
INSERT INTO schema_note (key, value)
VALUES ('baseline', 'fp phase 1')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS schema_note;
```

`internal/store/migrations/embed.go`：

```go
// Package migrations 以 go:embed 打包 SQL 迁移文件，使 fp 成为单二进制自迁移。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 4: 实现 postgres.go**

`internal/store/postgres.go`：

```go
// Package store 提供 fp 的持久化访问：PostgreSQL 与 Redis。
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/basicfu/fp/internal/store/migrations"
)

// OpenPostgres 建立连接池并验证连通性。
func OpenPostgres(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: 解析 postgres url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: 建立连接池: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping postgres: %w", err)
	}
	return pool, nil
}

// Migrate 把数据库升级到嵌入迁移的最新版本。可重复调用。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("store: goose dialect: %w", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("store: 执行迁移: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: 实现 redis.go**

`internal/store/redis.go`：

```go
package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// OpenRedis 建立 Redis 客户端并验证连通性。
func OpenRedis(ctx context.Context, url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("store: 解析 redis url: %w", err)
	}
	c := redis.NewClient(opt)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("store: ping redis: %w", err)
	}
	return c, nil
}
```

- [ ] **Step 6: 实现测试支撑**

`internal/testsupport/db.go`：

```go
// Package testsupport 提供集成测试所需的真实 PostgreSQL / Redis 连接。
package testsupport

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/store"
)

const missingPGHint = `
未设置 FP_TEST_POSTGRES_URL。fp 的测试需要真实的 PostgreSQL 18。

  make up
  FP_TEST_POSTGRES_URL=postgres://fp:fp@localhost:5433/fp?sslmode=disable \
  FP_TEST_REDIS_URL=redis://localhost:6380/1 \
  go test ./...

或直接执行 make test。
`

var (
	dbOnce sync.Once
	dbPool *pgxpool.Pool
	dbErr  error
)

// NewTestDB 返回一个已完成迁移的连接池，并清空所有业务表。
// 连接池在整个测试二进制内复用；每次调用只做数据清理。
func NewTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("FP_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal(missingPGHint)
	}

	dbOnce.Do(func() {
		ctx := context.Background()
		dbPool, dbErr = store.OpenPostgres(ctx, url)
		if dbErr != nil {
			return
		}
		dbErr = store.Migrate(ctx, dbPool)
	})
	if dbErr != nil {
		t.Fatalf("testsupport: 准备测试库失败: %v", dbErr)
	}

	truncateAll(t, dbPool)
	return dbPool
}

// truncateAll 清空除 goose 版本表与 schema_note 之外的所有表。
func truncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	rows, err := pool.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public'
		  AND tablename NOT IN ('goose_db_version', 'schema_note')`)
	if err != nil {
		t.Fatalf("testsupport: 列出表失败: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("testsupport: 扫描表名失败: %v", err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("testsupport: 遍历表名失败: %v", err)
	}
	if len(tables) == 0 {
		return
	}

	stmt := "TRUNCATE TABLE "
	for i, name := range tables {
		if i > 0 {
			stmt += ", "
		}
		stmt += `"` + name + `"`
	}
	stmt += " RESTART IDENTITY CASCADE"

	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("testsupport: 清空表失败: %v", err)
	}
}
```

`internal/testsupport/redis.go`：

```go
package testsupport

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/store"
)

const missingRedisHint = `
未设置 FP_TEST_REDIS_URL。fp 的测试需要真实的 Redis。

  make up
  FP_TEST_REDIS_URL=redis://localhost:6380/1 go test ./...

注意：测试会对该 Redis DB 执行 FLUSHDB，务必使用独立的 DB index。
`

var (
	rdbOnce sync.Once
	rdb     *redis.Client
	rdbErr  error
)

// NewTestRedis 返回一个已清空的 Redis 客户端。
func NewTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	url := os.Getenv("FP_TEST_REDIS_URL")
	if url == "" {
		t.Fatal(missingRedisHint)
	}

	rdbOnce.Do(func() {
		rdb, rdbErr = store.OpenRedis(context.Background(), url)
	})
	if rdbErr != nil {
		t.Fatalf("testsupport: 连接测试 Redis 失败: %v", rdbErr)
	}

	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("testsupport: FLUSHDB 失败: %v", err)
	}
	return rdb
}
```

- [ ] **Step 7: 运行测试确认通过**

```bash
make test
```

Expected: `internal/store` 4 个测试全部 PASS。若 `TestUUIDv7Available` 失败，说明 compose 拉起的不是 PostgreSQL 18。

- [ ] **Step 8: 接入 main**

`cmd/fp/main.go` 的 `run()` 替换为：

```go
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := store.OpenPostgres(ctx, cfg.PostgresURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	log.Info("数据库迁移完成")

	rdb, err := store.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	log.Info("fp 启动", "env", cfg.Env, "http", cfg.HTTPAddr, "grpc", cfg.GRPCAddr)

	<-ctx.Done()
	log.Info("fp 收到退出信号，正在关闭")
	return nil
}
```

同时把 import 补上 `"github.com/basicfu/fp/internal/store"`。

- [ ] **Step 9: 验证端到端启动**

Run: `make run`
Expected: 依次打印「数据库迁移完成」「fp 启动」两行 JSON 日志。

- [ ] **Step 10: 提交**

```bash
git add internal/store internal/testsupport cmd/fp/main.go
git commit -m "feat: PostgreSQL 连接与嵌入式迁移、Redis 连接、集成测试支撑"
```

---

## Task 3: 平台管理员与管理端认证

**Files:**
- Create: `internal/store/migrations/00002_admin.sql`
- Create: `internal/domain/errors.go`
- Create: `internal/service/admin.go`
- Create: `internal/httpapi/respond.go`, `internal/httpapi/middleware.go`, `internal/httpapi/admin.go`, `internal/httpapi/router.go`
- Modify: `cmd/fp/main.go`
- Test: `internal/service/admin_test.go`, `internal/httpapi/admin_test.go`

**Interfaces:**
- Consumes: `testsupport.NewTestDB`、`testsupport.NewTestRedis`
- Produces:
  - `domain` 哨兵错误：`ErrNotFound`、`ErrInvalidCredential`、`ErrUnauthorized`、`ErrConflict`、`ErrInvalidArgument`；`func domain.Errorf(base error, format string, a ...any) error`
  - `type service.AdminService struct{}`；`func service.NewAdminService(pool *pgxpool.Pool, rdb *redis.Client) *AdminService`
  - `func (*AdminService) EnsureBootstrap(ctx context.Context, username, password string) error`
  - `func (*AdminService) Login(ctx context.Context, username, password string) (token string, err error)`
  - `func (*AdminService) Authenticate(ctx context.Context, token string) (adminID uuid.UUID, username string, err error)`
  - `func (*AdminService) Logout(ctx context.Context, token string) error`
  - `func httpapi.NewRouter(admin *service.AdminService) http.Handler`
  - HTTP：`POST /admin/api/login`、`POST /admin/api/logout`、`GET /admin/api/me`

- [ ] **Step 1: 写迁移**

`internal/store/migrations/00002_admin.sql`：

```sql
-- +goose Up
CREATE TABLE admin (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    display_name  text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT 'ACTIVE',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE admin;
```

- [ ] **Step 2: 写领域错误**

`internal/domain/errors.go`：

```go
// Package domain 定义 fp 的领域类型与错误，不依赖任何传输层或存储实现。
package domain

import (
	"errors"
	"fmt"
)

// 哨兵错误。service 层返回这些错误的包装，传输层用 errors.Is 判定并映射成协议错误。
var (
	ErrNotFound          = errors.New("not found")
	ErrInvalidCredential = errors.New("invalid credential")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrConflict          = errors.New("conflict")
	ErrInvalidArgument   = errors.New("invalid argument")
	ErrRateLimited       = errors.New("rate limited")
	ErrForbidden         = errors.New("forbidden")
)

// Errorf 用 base 哨兵错误包装一条带上下文的消息。
// 返回值满足 errors.Is(err, base)，同时携带面向用户的说明。
func Errorf(base error, format string, a ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, a...), base)
}
```

- [ ] **Step 3: 写失败的 service 测试**

`internal/service/admin_test.go`：

```go
package service_test

import (
	"context"
	"errors"
	"testing"

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
	if id.String() == "" {
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
```

- [ ] **Step 4: 运行测试确认失败**

Run: `make test` 或 `go test ./internal/service/ -v`
Expected: 编译失败，`undefined: service.NewAdminService`

- [ ] **Step 5: 实现 AdminService**

`internal/service/admin.go`：

```go
// Package service 承载 fp 的业务逻辑。本包不依赖任何传输层类型。
package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/basicfu/fp/internal/domain"
)

// adminSessionTTL 是平台管理员会话的空闲有效期。
// 管理端是高权限入口，窗口刻意设得比业务侧短。
const adminSessionTTL = 2 * time.Hour

const adminTokenPrefix = "fp:admin:tok:"

// bcryptCost 是密码哈希代价。10 是 bcrypt 的常用生产取值。
const bcryptCost = 10

// AdminService 管理平台管理员账号与管理端会话。
// 管理员与业务用户使用完全独立的表，不共享任何数据（设计文档 12.1）。
type AdminService struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

// NewAdminService 构造 AdminService。
func NewAdminService(pool *pgxpool.Pool, rdb *redis.Client) *AdminService {
	return &AdminService{pool: pool, rdb: rdb}
}

// EnsureBootstrap 在管理员不存在时创建引导管理员。
// username 或 password 为空时静默跳过；管理员已存在时不改写其密码。
func (s *AdminService) EnsureBootstrap(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("service: 计算管理员密码哈希: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO admin (username, password_hash, display_name)
		VALUES ($1, $2, $1)
		ON CONFLICT (username) DO NOTHING`, username, string(hash))
	if err != nil {
		return fmt.Errorf("service: 创建引导管理员: %w", err)
	}
	return nil
}

// Login 校验用户名密码并签发一个管理端会话 token。
func (s *AdminService) Login(ctx context.Context, username, password string) (string, error) {
	var (
		id     uuid.UUID
		hash   string
		status string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, password_hash, status FROM admin WHERE username = $1`, username).
		Scan(&id, &hash, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		// 与密码错误返回同一错误，避免用户名枚举。
		return "", domain.Errorf(domain.ErrInvalidCredential, "用户名或密码不正确")
	}
	if err != nil {
		return "", fmt.Errorf("service: 查询管理员: %w", err)
	}
	if status != "ACTIVE" {
		return "", domain.Errorf(domain.ErrForbidden, "管理员账号已停用")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return "", domain.Errorf(domain.ErrInvalidCredential, "用户名或密码不正确")
	}

	token, err := randomToken()
	if err != nil {
		return "", err
	}
	payload := id.String() + "|" + username
	if err := s.rdb.Set(ctx, adminTokenPrefix+token, payload, adminSessionTTL).Err(); err != nil {
		return "", fmt.Errorf("service: 写入管理端会话: %w", err)
	}
	return token, nil
}

// Authenticate 校验管理端 token，返回管理员 ID 与用户名，并顺延会话有效期。
func (s *AdminService) Authenticate(ctx context.Context, token string) (uuid.UUID, string, error) {
	if token == "" {
		return uuid.Nil, "", domain.Errorf(domain.ErrUnauthorized, "缺少管理端凭据")
	}
	key := adminTokenPrefix + token
	payload, err := s.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return uuid.Nil, "", domain.Errorf(domain.ErrUnauthorized, "管理端凭据无效或已过期")
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("service: 读取管理端会话: %w", err)
	}

	var idStr, username string
	if n, _ := fmt.Sscanf(payload, "%36s|%s", &idStr, &username); n != 2 {
		return uuid.Nil, "", domain.Errorf(domain.ErrUnauthorized, "管理端凭据损坏")
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, "", domain.Errorf(domain.ErrUnauthorized, "管理端凭据损坏")
	}

	// 管理端会话数量极少，每次访问直接顺延，无需降频。
	if err := s.rdb.Expire(ctx, key, adminSessionTTL).Err(); err != nil {
		return uuid.Nil, "", fmt.Errorf("service: 顺延管理端会话: %w", err)
	}
	return id, username, nil
}

// Logout 作废一个管理端 token。token 不存在时也返回 nil。
func (s *AdminService) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.rdb.Del(ctx, adminTokenPrefix+token).Err(); err != nil {
		return fmt.Errorf("service: 删除管理端会话: %w", err)
	}
	return nil
}

// randomToken 生成 32 字节的密码学随机 token，base64url 编码后为 43 字符。
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("service: 生成随机 token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
```

> `fmt.Sscanf` 的 `%36s` 会连同 `|` 一起吞掉，改用手工分割更稳妥。实现时把解析部分替换为：
> ```go
> idStr, username, ok := strings.Cut(payload, "|")
> if !ok {
> 	return uuid.Nil, "", domain.Errorf(domain.ErrUnauthorized, "管理端凭据损坏")
> }
> ```
> 并把 import 里的 `fmt.Sscanf` 用法删掉、补上 `"strings"`。

- [ ] **Step 6: 运行测试确认通过**

Run: `go test ./internal/service/ -v`
Expected: 6 个测试全部 PASS

- [ ] **Step 7: 实现 HTTP 响应helper**

`internal/httpapi/respond.go`：

```go
// Package httpapi 是管理 UI 的 HTTP 传输层。它只做协议转换，不含业务逻辑。
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/basicfu/fp/internal/domain"
)

type errorBody struct {
	Error string `json:"error"`
}

// writeJSON 以 status 写出 JSON 响应。v 为 nil 时只写状态码。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httpapi: 写出响应失败", "err", err)
	}
}

// writeError 把领域错误映射为 HTTP 状态码。
// 未识别的错误一律 500，且不把内部错误信息泄露给客户端。
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrInvalidCredential):
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrUnauthorized):
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrForbidden):
		writeJSON(w, http.StatusForbidden, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrConflict):
		writeJSON(w, http.StatusConflict, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrInvalidArgument):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
	case errors.Is(err, domain.ErrRateLimited):
		writeJSON(w, http.StatusTooManyRequests, errorBody{Error: err.Error()})
	default:
		slog.Error("httpapi: 未处理的内部错误", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}

// decodeJSON 读取并解析请求体。解析失败返回 domain.ErrInvalidArgument 的包装。
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "请求体解析失败: %v", err)
	}
	return nil
}
```

- [ ] **Step 8: 实现认证中间件与管理员路由**

`internal/httpapi/middleware.go`：

```go
package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/service"
)

type ctxKey int

const (
	ctxKeyAdminID ctxKey = iota
	ctxKeyAdminName
	ctxKeyAdminToken
)

// adminTokenCookie 是管理端会话 cookie 名。与业务侧的 token 完全分离。
const adminTokenCookie = "fp_admin"

// requireAdmin 校验管理端凭据，通过后把管理员信息放进请求 context。
// 凭据优先取 Authorization: Bearer，其次取 cookie。
func requireAdmin(admin *service.AdminService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				if c, err := r.Cookie(adminTokenCookie); err == nil {
					token = c.Value
				}
			}
			id, name, err := admin.Authenticate(r.Context(), token)
			if err != nil {
				writeError(w, err)
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyAdminID, id)
			ctx = context.WithValue(ctx, ctxKeyAdminName, name)
			ctx = context.WithValue(ctx, ctxKeyAdminToken, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func adminIDFrom(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(ctxKeyAdminID).(uuid.UUID)
	return id
}

func adminNameFrom(ctx context.Context) string {
	name, _ := ctx.Value(ctxKeyAdminName).(string)
	return name
}

func adminTokenFrom(ctx context.Context) string {
	token, _ := ctx.Value(ctxKeyAdminToken).(string)
	return token
}
```

`internal/httpapi/admin.go`：

```go
package httpapi

import (
	"net/http"

	"github.com/basicfu/fp/internal/service"
)

type adminHandler struct {
	svc *service.AdminService
}

type adminLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type adminLoginResponse struct {
	Token    string `json:"token"`
	Username string `json:"username"`
}

func (h *adminHandler) login(w http.ResponseWriter, r *http.Request) {
	var req adminLoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	token, err := h.svc.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminTokenCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, adminLoginResponse{Token: token, Username: req.Username})
}

func (h *adminHandler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), adminTokenFrom(r.Context())); err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: adminTokenCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
	writeJSON(w, http.StatusNoContent, nil)
}

type adminMeResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func (h *adminHandler) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, adminMeResponse{
		ID:       adminIDFrom(r.Context()).String(),
		Username: adminNameFrom(r.Context()),
	})
}
```

`internal/httpapi/router.go`：

```go
package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/basicfu/fp/internal/service"
)

// NewRouter 装配管理 UI 的 HTTP 路由。
// 后续任务会往这里追加 application、user 等路由组。
func NewRouter(admin *service.AdminService) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	ah := &adminHandler{svc: admin}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/admin/api", func(r chi.Router) {
		r.Post("/login", ah.login)

		r.Group(func(r chi.Router) {
			r.Use(requireAdmin(admin))
			r.Post("/logout", ah.logout)
			r.Get("/me", ah.me)
		})
	})

	return r
}
```

需要 `go get github.com/go-chi/chi/v5`（Task 1 已装）。

- [ ] **Step 9: 写 HTTP 层测试**

`internal/httpapi/admin_test.go`：

```go
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func newAdminServer(t *testing.T) (http.Handler, *service.AdminService) {
	t.Helper()
	svc := service.NewAdminService(testsupport.NewTestDB(t), testsupport.NewTestRedis(t))
	if err := svc.EnsureBootstrap(context.Background(), "admin", "secret123456"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	return httpapi.NewRouter(svc), svc
}

func TestHealthz(t *testing.T) {
	h, _ := newAdminServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestAdminLoginThenMe(t *testing.T) {
	h, _ := newAdminServer(t)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"username":"admin","password":"secret123456"}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/api/login", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatalf("解析登录响应: %v", err)
	}

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("me status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), `"username":"admin"`) {
		t.Fatalf("me body = %s", rec2.Body.String())
	}
}

func TestMeRequiresAuth(t *testing.T) {
	h, _ := newAdminServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/api/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAdminLoginWrongPassword(t *testing.T) {
	h, _ := newAdminServer(t)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"username":"admin","password":"nope"}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/api/login", body))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
```

- [ ] **Step 10: 运行测试确认通过**

Run: `make test`
Expected: `internal/httpapi` 4 个测试 PASS，`internal/service` 6 个 PASS

- [ ] **Step 11: 接入 main**

在 `cmd/fp/main.go` 的 `run()` 里，`store.OpenRedis` 之后、`<-ctx.Done()` 之前插入：

```go
	adminSvc := service.NewAdminService(pool, rdb)
	if err := adminSvc.EnsureBootstrap(ctx, cfg.BootstrapAdminUser, cfg.BootstrapAdminPassword); err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: httpapi.NewRouter(adminSvc),
	}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP 服务异常退出", "err", err)
			stop()
		}
	}()
	log.Info("fp 启动", "env", cfg.Env, "http", cfg.HTTPAddr, "grpc", cfg.GRPCAddr)

	<-ctx.Done()
	log.Info("fp 收到退出信号，正在关闭")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("HTTP 优雅关闭超时", "err", err)
	}
	return nil
}
```

补齐 import：`"errors"`、`"net/http"`、`"time"`、`"github.com/basicfu/fp/internal/httpapi"`、`"github.com/basicfu/fp/internal/service"`；删除原先重复的 `log.Info("fp 启动", ...)` 与 `return nil`。

- [ ] **Step 12: 手工验证**

```bash
make run
```

另开一个终端：

```bash
curl -s -X POST localhost:8080/admin/api/login -d '{"username":"admin","password":"admin123456"}'
```

Expected: 返回含 `token` 的 JSON。用该 token 调 `/admin/api/me` 应返回用户名。

- [ ] **Step 13: 提交**

```bash
git add internal cmd
git commit -m "feat: 平台管理员账号、管理端会话与 HTTP 认证中间件"
```

---

## Task 4: 应用（Application）领域模型与服务

**Files:**
- Create: `internal/store/migrations/00003_application.sql`
- Create: `internal/domain/application.go`
- Create: `internal/service/application.go`
- Test: `internal/domain/application_test.go`, `internal/service/application_test.go`

**Interfaces:**
- Consumes: `domain.Errorf` 与哨兵错误、`service.randomToken`、`service.bcryptCost`
- Produces:
  - `type domain.SessionPolicy struct{ IdleTimeoutSeconds, IdleTimeoutMobileSeconds, MaxLifetimeSeconds, RotateIntervalSeconds, ExtendIntervalSeconds, TokenCacheTTLSeconds int32 }`
  - `func domain.DefaultSessionPolicy() SessionPolicy`
  - `func (SessionPolicy) Validate() error`
  - `func (SessionPolicy) IdleTimeoutFor(mobile bool) time.Duration`
  - `func (SessionPolicy) MaxLifetime() time.Duration`
  - `func (SessionPolicy) RotateInterval() time.Duration`
  - `func (SessionPolicy) ExtendInterval() time.Duration`
  - `type domain.Application struct{ ID uuid.UUID; Name, Slug, AppID, Status, CookieDomain string; Session SessionPolicy; RedirectURIs, GrantTypes []string; CreatedAt, UpdatedAt int64 }`
  - `type domain.ApplicationConnector struct{ Type string; Enabled bool; Config map[string]any }`
  - 常量 `domain.ApplicationStatusActive = "ACTIVE"`、`domain.ApplicationStatusDisabled = "DISABLED"`
  - `func service.NewApplicationService(pool *pgxpool.Pool) *ApplicationService`
  - `func (*ApplicationService) Create(ctx context.Context, name, slug string) (*domain.Application, string, error)` — 第二个返回值是**仅此一次**可见的明文 appSecret
  - `func (*ApplicationService) List(ctx context.Context) ([]domain.Application, error)`
  - `func (*ApplicationService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Application, error)`
  - `func (*ApplicationService) GetByAppID(ctx context.Context, appID string) (*domain.Application, error)`
  - `func (*ApplicationService) VerifySecret(ctx context.Context, appID, plainSecret string) (*domain.Application, error)`
  - `func (*ApplicationService) UpdateSessionPolicy(ctx context.Context, id uuid.UUID, p domain.SessionPolicy) (*domain.Application, error)`
  - `func (*ApplicationService) SetConnector(ctx context.Context, appID uuid.UUID, connectorType string, enabled bool, config map[string]any) error`
  - `func (*ApplicationService) GetConnector(ctx context.Context, appID uuid.UUID, connectorType string) (*domain.ApplicationConnector, error)`
  - `func (*ApplicationService) ListConnectors(ctx context.Context, appID uuid.UUID) ([]domain.ApplicationConnector, error)`
  - `type service.rowScanner interface{ Scan(dest ...any) error }`（包内共用，后续任务的 scan 函数复用）
  - 常量 `service.pgUniqueViolation = "23505"`

- [ ] **Step 1: 写迁移**

`internal/store/migrations/00003_application.sql`：

```sql
-- +goose Up
CREATE TABLE application (
    id                          uuid PRIMARY KEY DEFAULT uuidv7(),
    name                        text NOT NULL,
    slug                        text NOT NULL UNIQUE,
    app_id                      text NOT NULL UNIQUE,
    app_secret_hash             text NOT NULL,
    status                      text NOT NULL DEFAULT 'ACTIVE',

    -- 会话三参数 + 缓存与降频窗口（设计文档 4.4 / 4.5.2）
    idle_timeout_seconds        int NOT NULL DEFAULT 604800,   -- 7d
    idle_timeout_mobile_seconds int NOT NULL DEFAULT 2592000,  -- 30d
    max_lifetime_seconds        int NOT NULL DEFAULT 7776000,  -- 90d
    rotate_interval_seconds     int NOT NULL DEFAULT 86400,    -- 24h
    extend_interval_seconds     int NOT NULL DEFAULT 600,      -- 10min
    token_cache_ttl_seconds     int NOT NULL DEFAULT 30,

    cookie_domain               text NOT NULL DEFAULT '',

    -- OIDC 预留（设计文档十四章：暂不实现，但留位置）
    redirect_uris               text[] NOT NULL DEFAULT '{}',
    grant_types                 text[] NOT NULL DEFAULT '{}',

    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE application_connector (
    application_id uuid NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    connector_type text NOT NULL,
    enabled        boolean NOT NULL DEFAULT true,
    config         jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, connector_type)
);

-- +goose Down
DROP TABLE application_connector;
DROP TABLE application;
```

- [ ] **Step 2: 写失败的 domain 测试**

`internal/domain/application_test.go`：

```go
package domain_test

import (
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

func TestDefaultSessionPolicyIsValid(t *testing.T) {
	if err := domain.DefaultSessionPolicy().Validate(); err != nil {
		t.Fatalf("默认策略应当合法: %v", err)
	}
}

func TestSessionPolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.SessionPolicy)
		wantErr bool
	}{
		{"默认值", func(*domain.SessionPolicy) {}, false},
		{"空闲超时为零", func(p *domain.SessionPolicy) { p.IdleTimeoutSeconds = 0 }, true},
		{"缓存 TTL 为零", func(p *domain.SessionPolicy) { p.TokenCacheTTLSeconds = 0 }, true},
		{"绝对上限为零", func(p *domain.SessionPolicy) { p.MaxLifetimeSeconds = 0 }, true},
		{"延期间隔不小于空闲超时", func(p *domain.SessionPolicy) {
			p.ExtendIntervalSeconds = p.IdleTimeoutSeconds
		}, true},
		{"轮换间隔大于绝对上限", func(p *domain.SessionPolicy) {
			p.RotateIntervalSeconds = p.MaxLifetimeSeconds + 1
		}, true},
		{"缓存 TTL 大于空闲超时", func(p *domain.SessionPolicy) {
			p.TokenCacheTTLSeconds = p.IdleTimeoutSeconds + 1
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := domain.DefaultSessionPolicy()
			tt.mutate(&p)
			err := p.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}

func TestIdleTimeoutFor(t *testing.T) {
	p := domain.DefaultSessionPolicy()

	if got, want := p.IdleTimeoutFor(false), 7*24*time.Hour; got != want {
		t.Errorf("web = %v, want %v", got, want)
	}
	if got, want := p.IdleTimeoutFor(true), 30*24*time.Hour; got != want {
		t.Errorf("mobile = %v, want %v", got, want)
	}

	// mobile 值为 0 时回落到 web 值
	p.IdleTimeoutMobileSeconds = 0
	if got, want := p.IdleTimeoutFor(true), 7*24*time.Hour; got != want {
		t.Errorf("mobile 回落 = %v, want %v", got, want)
	}
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/domain/ -v`
Expected: 编译失败，`undefined: domain.DefaultSessionPolicy`

- [ ] **Step 4: 实现 domain/application.go**

```go
package domain

import (
	"time"

	"github.com/google/uuid"
)

// SessionPolicy 是一个应用的会话参数：设计文档 4.4 的会话三参数
// （idle_timeout / max_lifetime / rotate_interval）加上 4.5 的缓存与降频窗口。
type SessionPolicy struct {
	// IdleTimeoutSeconds 多久不访问就失效，即滑动过期的窗口。
	IdleTimeoutSeconds int32
	// IdleTimeoutMobileSeconds 移动端的空闲超时。为 0 时回落到 IdleTimeoutSeconds。
	IdleTimeoutMobileSeconds int32
	// MaxLifetimeSeconds 从首次认证起最长活多久，到期必须重新认证。
	// 轮换不能替代它：轮换只换 token 的值，会话仍是同一个。
	MaxLifetimeSeconds int32
	// RotateIntervalSeconds 多久换一次 token 值。
	RotateIntervalSeconds int32
	// ExtendIntervalSeconds 延期写的时间窗降频间隔（设计文档 4.5.2）。
	ExtendIntervalSeconds int32
	// TokenCacheTTLSeconds SDK 侧缓存校验结果的上限秒数。
	// fp 下发的 cache_ttl = min(本值, token 剩余有效期)。
	TokenCacheTTLSeconds int32
}

// DefaultSessionPolicy 返回新建应用的默认会话策略。
func DefaultSessionPolicy() SessionPolicy {
	return SessionPolicy{
		IdleTimeoutSeconds:       7 * 24 * 60 * 60,
		IdleTimeoutMobileSeconds: 30 * 24 * 60 * 60,
		MaxLifetimeSeconds:       90 * 24 * 60 * 60,
		RotateIntervalSeconds:    24 * 60 * 60,
		ExtendIntervalSeconds:    10 * 60,
		TokenCacheTTLSeconds:     30,
	}
}

// Validate 校验策略的内部一致性。
func (p SessionPolicy) Validate() error {
	if p.IdleTimeoutSeconds <= 0 {
		return Errorf(ErrInvalidArgument, "idle_timeout 必须大于 0")
	}
	if p.IdleTimeoutMobileSeconds < 0 {
		return Errorf(ErrInvalidArgument, "idle_timeout_mobile 不能为负")
	}
	if p.MaxLifetimeSeconds <= 0 {
		return Errorf(ErrInvalidArgument, "max_lifetime 必须大于 0")
	}
	if p.RotateIntervalSeconds <= 0 {
		return Errorf(ErrInvalidArgument, "rotate_interval 必须大于 0")
	}
	if p.ExtendIntervalSeconds <= 0 {
		return Errorf(ErrInvalidArgument, "extend_interval 必须大于 0")
	}
	if p.TokenCacheTTLSeconds <= 0 {
		return Errorf(ErrInvalidArgument, "token_cache_ttl 必须大于 0")
	}
	if p.RotateIntervalSeconds > p.MaxLifetimeSeconds {
		return Errorf(ErrInvalidArgument, "rotate_interval 不能大于 max_lifetime")
	}
	// 降频间隔必须远小于空闲超时，否则用户会因为"少延"而意外掉线。
	if p.ExtendIntervalSeconds >= p.IdleTimeoutSeconds {
		return Errorf(ErrInvalidArgument, "extend_interval 必须小于 idle_timeout")
	}
	// 缓存窗口大于空闲超时意味着 token 过期后仍可能被 SDK 放行。
	if p.TokenCacheTTLSeconds > p.IdleTimeoutSeconds {
		return Errorf(ErrInvalidArgument, "token_cache_ttl 不能大于 idle_timeout")
	}
	return nil
}

// IdleTimeoutFor 返回该端类型适用的空闲超时。
func (p SessionPolicy) IdleTimeoutFor(mobile bool) time.Duration {
	if mobile && p.IdleTimeoutMobileSeconds > 0 {
		return time.Duration(p.IdleTimeoutMobileSeconds) * time.Second
	}
	return time.Duration(p.IdleTimeoutSeconds) * time.Second
}

// MaxLifetime 返回绝对上限时长。
func (p SessionPolicy) MaxLifetime() time.Duration {
	return time.Duration(p.MaxLifetimeSeconds) * time.Second
}

// RotateInterval 返回 token 轮换间隔。
func (p SessionPolicy) RotateInterval() time.Duration {
	return time.Duration(p.RotateIntervalSeconds) * time.Second
}

// ExtendInterval 返回延期写的降频间隔。
func (p SessionPolicy) ExtendInterval() time.Duration {
	return time.Duration(p.ExtendIntervalSeconds) * time.Second
}

// 应用状态。
const (
	ApplicationStatusActive   = "ACTIVE"
	ApplicationStatusDisabled = "DISABLED"
)

// Application 是一个接入端。多个应用挂在同一个 fp 部署下即共享同一套用户体系。
type Application struct {
	ID           uuid.UUID
	Name         string
	Slug         string
	AppID        string
	Status       string
	Session      SessionPolicy
	CookieDomain string
	RedirectURIs []string // OIDC 预留
	GrantTypes   []string // OIDC 预留
	CreatedAt    int64
	UpdatedAt    int64
}

// ApplicationConnector 是某个应用对某种登录方式的启用状态与配置。
type ApplicationConnector struct {
	Type    string
	Enabled bool
	Config  map[string]any
}
```

- [ ] **Step 5: 运行 domain 测试确认通过**

Run: `go test ./internal/domain/ -v`
Expected: 3 个测试 PASS

- [ ] **Step 6: 写失败的 service 测试**

`internal/service/application_test.go`：

```go
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
```

- [ ] **Step 7: 运行测试确认失败**

Run: `go test ./internal/service/ -v`
Expected: 编译失败，`undefined: service.NewApplicationService`

- [ ] **Step 8: 实现 ApplicationService**

`internal/service/application.go`：

```go
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/basicfu/fp/internal/domain"
)

// pgUniqueViolation 是 PostgreSQL 唯一约束冲突的 SQLSTATE。
const pgUniqueViolation = "23505"

// rowScanner 同时被 pgx.Row 与 pgx.Rows 满足，让扫描逻辑只写一遍。
type rowScanner interface {
	Scan(dest ...any) error
}

// applicationColumns 是所有读取 application 的查询共用的列清单，保证 scanApplication 能复用。
// 时间统一转成毫秒，与 Go 侧的 int64 约定一致。
const applicationColumns = `
	id, name, slug, app_id, status,
	idle_timeout_seconds, idle_timeout_mobile_seconds, max_lifetime_seconds,
	rotate_interval_seconds, extend_interval_seconds, token_cache_ttl_seconds,
	cookie_domain, redirect_uris, grant_types,
	(extract(epoch from created_at) * 1000)::bigint,
	(extract(epoch from updated_at) * 1000)::bigint`

// ApplicationService 管理接入端应用及其登录方式配置。
type ApplicationService struct {
	pool *pgxpool.Pool
}

// NewApplicationService 构造 ApplicationService。
func NewApplicationService(pool *pgxpool.Pool) *ApplicationService {
	return &ApplicationService{pool: pool}
}

// Create 新建应用，返回应用与仅此一次可见的明文 appSecret。
// 明文 secret 不落库，只存 bcrypt 哈希。
func (s *ApplicationService) Create(ctx context.Context, name, slug string) (*domain.Application, string, error) {
	if name == "" || slug == "" {
		return nil, "", domain.Errorf(domain.ErrInvalidArgument, "name 与 slug 不能为空")
	}

	appID, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	secret, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcryptCost)
	if err != nil {
		return nil, "", fmt.Errorf("service: 计算 appSecret 哈希: %w", err)
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO application (name, slug, app_id, app_secret_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING `+applicationColumns, name, slug, appID, string(hash))

	app, err := scanApplication(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return nil, "", domain.Errorf(domain.ErrConflict, "slug %q 已被占用", slug)
		}
		return nil, "", fmt.Errorf("service: 创建应用: %w", err)
	}
	return app, secret, nil
}

// List 返回全部应用，按创建时间倒序。永不返回 nil 切片。
func (s *ApplicationService) List(ctx context.Context) ([]domain.Application, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+applicationColumns+` FROM application ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("service: 查询应用列表: %w", err)
	}
	defer rows.Close()

	out := []domain.Application{}
	for rows.Next() {
		app, err := scanApplication(rows)
		if err != nil {
			return nil, fmt.Errorf("service: 扫描应用: %w", err)
		}
		out = append(out, *app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历应用: %w", err)
	}
	return out, nil
}

// GetByID 按内部 ID 查应用。
func (s *ApplicationService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Application, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+applicationColumns+` FROM application WHERE id = $1`, id)
	app, err := scanApplication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "应用不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询应用: %w", err)
	}
	return app, nil
}

// GetByAppID 按对外的 appId 查应用。
func (s *ApplicationService) GetByAppID(ctx context.Context, appID string) (*domain.Application, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+applicationColumns+` FROM application WHERE app_id = $1`, appID)
	app, err := scanApplication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "应用不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 按 appId 查询应用: %w", err)
	}
	return app, nil
}

// VerifySecret 校验 appId + appSecret，成功返回对应应用。
// appId 不存在与 secret 错误返回同一错误，避免 appId 枚举。
func (s *ApplicationService) VerifySecret(ctx context.Context, appID, plainSecret string) (*domain.Application, error) {
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT app_secret_hash FROM application WHERE app_id = $1`, appID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrInvalidCredential, "appId 或 appSecret 不正确")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 读取 appSecret 哈希: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plainSecret)); err != nil {
		return nil, domain.Errorf(domain.ErrInvalidCredential, "appId 或 appSecret 不正确")
	}
	return s.GetByAppID(ctx, appID)
}

// UpdateSessionPolicy 更新应用的会话策略。策略非法时不写库。
func (s *ApplicationService) UpdateSessionPolicy(ctx context.Context, id uuid.UUID, p domain.SessionPolicy) (*domain.Application, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE application SET
			idle_timeout_seconds        = $2,
			idle_timeout_mobile_seconds = $3,
			max_lifetime_seconds        = $4,
			rotate_interval_seconds     = $5,
			extend_interval_seconds     = $6,
			token_cache_ttl_seconds     = $7,
			updated_at                  = now()
		WHERE id = $1
		RETURNING `+applicationColumns,
		id,
		p.IdleTimeoutSeconds, p.IdleTimeoutMobileSeconds, p.MaxLifetimeSeconds,
		p.RotateIntervalSeconds, p.ExtendIntervalSeconds, p.TokenCacheTTLSeconds)

	app, err := scanApplication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "应用不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 更新会话策略: %w", err)
	}
	return app, nil
}

// SetConnector 写入或覆盖某个应用的某种登录方式配置。
func (s *ApplicationService) SetConnector(ctx context.Context, appID uuid.UUID, connectorType string, enabled bool, config map[string]any) error {
	if connectorType == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "connector 类型不能为空")
	}
	if config == nil {
		config = map[string]any{}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "connector 配置无法序列化: %v", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO application_connector (application_id, connector_type, enabled, config)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (application_id, connector_type)
		DO UPDATE SET enabled = EXCLUDED.enabled, config = EXCLUDED.config, updated_at = now()`,
		appID, connectorType, enabled, raw)
	if err != nil {
		return fmt.Errorf("service: 写入 connector 配置: %w", err)
	}
	return nil
}

// GetConnector 返回单个 connector 配置。未配置时返回 domain.ErrNotFound。
func (s *ApplicationService) GetConnector(ctx context.Context, appID uuid.UUID, connectorType string) (*domain.ApplicationConnector, error) {
	var (
		c   domain.ApplicationConnector
		raw []byte
	)
	err := s.pool.QueryRow(ctx, `
		SELECT connector_type, enabled, config
		FROM application_connector
		WHERE application_id = $1 AND connector_type = $2`, appID, connectorType).
		Scan(&c.Type, &c.Enabled, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "该应用未配置 %s 登录方式", connectorType)
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询 connector 配置: %w", err)
	}
	if err := json.Unmarshal(raw, &c.Config); err != nil {
		return nil, fmt.Errorf("service: 解析 connector 配置: %w", err)
	}
	return &c, nil
}

// ListConnectors 返回某个应用已配置的全部登录方式。永不返回 nil 切片。
func (s *ApplicationService) ListConnectors(ctx context.Context, appID uuid.UUID) ([]domain.ApplicationConnector, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT connector_type, enabled, config
		FROM application_connector
		WHERE application_id = $1
		ORDER BY connector_type`, appID)
	if err != nil {
		return nil, fmt.Errorf("service: 查询 connector 配置: %w", err)
	}
	defer rows.Close()

	out := []domain.ApplicationConnector{}
	for rows.Next() {
		var (
			c   domain.ApplicationConnector
			raw []byte
		)
		if err := rows.Scan(&c.Type, &c.Enabled, &raw); err != nil {
			return nil, fmt.Errorf("service: 扫描 connector 配置: %w", err)
		}
		if err := json.Unmarshal(raw, &c.Config); err != nil {
			return nil, fmt.Errorf("service: 解析 connector 配置: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历 connector 配置: %w", err)
	}
	return out, nil
}

func scanApplication(r rowScanner) (*domain.Application, error) {
	var app domain.Application
	err := r.Scan(
		&app.ID, &app.Name, &app.Slug, &app.AppID, &app.Status,
		&app.Session.IdleTimeoutSeconds, &app.Session.IdleTimeoutMobileSeconds, &app.Session.MaxLifetimeSeconds,
		&app.Session.RotateIntervalSeconds, &app.Session.ExtendIntervalSeconds, &app.Session.TokenCacheTTLSeconds,
		&app.CookieDomain, &app.RedirectURIs, &app.GrantTypes,
		&app.CreatedAt, &app.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &app, nil
}
```

- [ ] **Step 9: 运行测试确认通过**

Run: `make test`
Expected: `internal/domain` 3 个 PASS、`internal/service` 15 个 PASS（管理员 6 + 应用 9）

- [ ] **Step 10: 提交**

```bash
git add internal
git commit -m "feat: 应用领域模型、会话策略校验与应用管理服务"
```

---

## Task 5: 用户、身份与账号归并

`identity` 是所有登录标识的唯一真相源（见「对设计文档的细化 ①」）。**账号归并规则**是多登录方式最容易出错的地方，本任务把它显式化并用测试锁死。

**Files:**
- Create: `internal/store/migrations/00004_user.sql`
- Create: `internal/domain/user.go`, `internal/domain/identity.go`
- Create: `internal/service/user.go`
- Test: `internal/domain/user_test.go`, `internal/service/user_test.go`

**Interfaces:**
- Consumes: `domain.Errorf`、`service.rowScanner`、`service.pgUniqueViolation`、`service.bcryptCost`
- Produces:
  - 常量 `domain.UserStatusActive/Frozen/PendingDelete/Deleted = "ACTIVE"/"FROZEN"/"PENDING_DELETE"/"DELETED"`
  - `func domain.CanTransitionUserStatus(from, to string) bool`
  - `type domain.User struct{ ID uuid.UUID; PasswordHash, Nickname, AvatarURL, Gender, Status string; DeleteSubmittedAt, CreatedAt, UpdatedAt int64 }`
  - `func (User) CanLogin() bool`
  - 常量 `domain.IdentityTypePhone/Username/Email/WechatMP = "phone"/"username"/"email"/"wechat_mp"`
  - `func domain.IsMergeableIdentityType(t string) bool` — phone/username/email 属于本地标识，可按 subject 归并
  - `type domain.Identity struct{ ID, UserID uuid.UUID; Type, Subject, UnionKey, Credential string; LastLoginAt, CreatedAt int64 }`
  - `func service.NewUserService(pool *pgxpool.Pool) *UserService`
  - `func (*UserService) FindByIdentity(ctx context.Context, identityType, subject string) (*domain.User, *domain.Identity, error)`
  - `func (*UserService) FindByUnionKey(ctx context.Context, unionKey string) (*domain.User, error)`
  - `func (*UserService) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)`
  - `func (*UserService) EnsureUserWithIdentity(ctx context.Context, in service.EnsureIdentityInput) (*domain.User, *domain.Identity, bool, error)` — 第三个返回值为 `created`
  - `type service.EnsureIdentityInput struct{ Type, Subject, UnionKey, Credential, Nickname string }`
  - `func (*UserService) AttachIdentity(ctx context.Context, userID uuid.UUID, in service.EnsureIdentityInput) (*domain.Identity, error)`
  - `func (*UserService) SetPassword(ctx context.Context, userID uuid.UUID, plain string) error`
  - `func (*UserService) VerifyPassword(ctx context.Context, userID uuid.UUID, plain string) error`
  - `func (*UserService) SetStatus(ctx context.Context, userID uuid.UUID, status string) (*domain.User, error)`
  - `func (*UserService) TouchIdentityLogin(ctx context.Context, identityID uuid.UUID) error`
  - `func (*UserService) EnsureRegistration(ctx context.Context, userID, appID uuid.UUID) error`
  - `func (*UserService) ListIdentities(ctx context.Context, userID uuid.UUID) ([]domain.Identity, error)`

- [ ] **Step 1: 写迁移**

`internal/store/migrations/00004_user.sql`：

```sql
-- +goose Up
CREATE TABLE app_user (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    -- password_hash 是凭据而非标识：用户名/邮箱/手机号三种标识共用同一个密码。
    -- 未设置密码的用户（仅短信登录）此列为空串。
    password_hash       text NOT NULL DEFAULT '',
    nickname            text NOT NULL DEFAULT '',
    avatar_url          text NOT NULL DEFAULT '',
    gender              text NOT NULL DEFAULT '',
    status              text NOT NULL DEFAULT 'ACTIVE',
    delete_submitted_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- identity 是所有登录标识的唯一真相源。
-- type='phone'    subject=手机号
-- type='username' subject=用户名
-- type='email'    subject=邮箱
-- type='wechat_mp' subject=openid  union_key=unionid
CREATE TABLE identity (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id       uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    type          text NOT NULL,
    subject       text NOT NULL,
    -- union_key 用于跨 type 归并（微信 unionId）。本地标识留空串。
    union_key     text NOT NULL DEFAULT '',
    -- credential 存第三方 token 等。密码不存这里，存 app_user.password_hash。
    credential    text NOT NULL DEFAULT '',
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (type, subject)
);
CREATE INDEX identity_user_id_idx ON identity (user_id);
-- 部分索引：只对非空 union_key 建唯一约束，本地标识的空串不参与。
CREATE UNIQUE INDEX identity_union_key_idx ON identity (union_key) WHERE union_key <> '';

-- 用户在某个应用下的注册关系。
-- 严禁添加 role 列（设计文档 5.5）：角色由 casbin 的 grouping policy 承载，
-- 在授权模块交付前不留任何过渡态。
CREATE TABLE user_application (
    user_id        uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    application_id uuid NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    nickname       text NOT NULL DEFAULT '',
    status         text NOT NULL DEFAULT 'ACTIVE',
    extra          jsonb NOT NULL DEFAULT '{}'::jsonb,
    registered_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, application_id)
);

-- +goose Down
DROP TABLE user_application;
DROP TABLE identity;
DROP TABLE app_user;
```

- [ ] **Step 2: 写失败的 domain 测试**

`internal/domain/user_test.go`：

```go
package domain_test

import (
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

func TestCanTransitionUserStatus(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{domain.UserStatusActive, domain.UserStatusFrozen, true},
		{domain.UserStatusFrozen, domain.UserStatusActive, true},
		{domain.UserStatusActive, domain.UserStatusPendingDelete, true},
		// 保护期内可撤销注销
		{domain.UserStatusPendingDelete, domain.UserStatusActive, true},
		{domain.UserStatusPendingDelete, domain.UserStatusDeleted, true},
		// 已注销是终态
		{domain.UserStatusDeleted, domain.UserStatusActive, false},
		{domain.UserStatusDeleted, domain.UserStatusFrozen, false},
		// 冻结状态不能直接跳到注销中
		{domain.UserStatusFrozen, domain.UserStatusPendingDelete, false},
		// 未知状态
		{"WHATEVER", domain.UserStatusActive, false},
		{domain.UserStatusActive, "WHATEVER", false},
	}
	for _, tt := range tests {
		if got := domain.CanTransitionUserStatus(tt.from, tt.to); got != tt.want {
			t.Errorf("CanTransitionUserStatus(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestUserCanLogin(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{domain.UserStatusActive, true},
		// 注销保护期内允许登录——登录行为本身会撤销注销申请
		{domain.UserStatusPendingDelete, true},
		{domain.UserStatusFrozen, false},
		{domain.UserStatusDeleted, false},
	}
	for _, tt := range tests {
		u := domain.User{Status: tt.status}
		if got := u.CanLogin(); got != tt.want {
			t.Errorf("status %q CanLogin() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestIsMergeableIdentityType(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{domain.IdentityTypePhone, true},
		{domain.IdentityTypeUsername, true},
		{domain.IdentityTypeEmail, true},
		{domain.IdentityTypeWechatMP, false},
		{"unknown", false},
	}
	for _, tt := range tests {
		if got := domain.IsMergeableIdentityType(tt.typ); got != tt.want {
			t.Errorf("IsMergeableIdentityType(%q) = %v, want %v", tt.typ, got, tt.want)
		}
	}
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/domain/ -v`
Expected: 编译失败，`undefined: domain.UserStatusActive`

- [ ] **Step 4: 实现 domain/user.go 与 domain/identity.go**

`internal/domain/user.go`：

```go
package domain

import "github.com/google/uuid"

// 用户状态。状态机见 CanTransitionUserStatus。
const (
	UserStatusActive        = "ACTIVE"
	UserStatusFrozen        = "FROZEN"
	UserStatusPendingDelete = "PENDING_DELETE" // 已提交注销，处于保护期
	UserStatusDeleted       = "DELETED"        // 终态
)

// userTransitions 是允许的状态迁移。缺省即不允许。
var userTransitions = map[string]map[string]bool{
	UserStatusActive: {
		UserStatusFrozen:        true,
		UserStatusPendingDelete: true,
	},
	UserStatusFrozen: {
		UserStatusActive: true,
	},
	UserStatusPendingDelete: {
		// 保护期内任意登录行为都会撤销注销申请
		UserStatusActive:  true,
		UserStatusDeleted: true,
	},
	// UserStatusDeleted 是终态，无出边。
}

// CanTransitionUserStatus 报告 from → to 是否为允许的状态迁移。
func CanTransitionUserStatus(from, to string) bool {
	return userTransitions[from][to]
}

// User 是一个全局唯一的用户。登录标识全部落在 Identity 上，
// 本结构只持有跨标识共享的资料与凭据。
type User struct {
	ID uuid.UUID
	// PasswordHash 是 password connector 的凭据，未设置时为空串。
	PasswordHash      string
	Nickname          string
	AvatarURL         string
	Gender            string
	Status            string
	DeleteSubmittedAt int64 // 0 表示未提交注销
	CreatedAt         int64
	UpdatedAt         int64
}

// CanLogin 报告该状态的用户是否允许登录。
// 注销保护期内允许登录，登录本身即撤销注销申请。
func (u User) CanLogin() bool {
	return u.Status == UserStatusActive || u.Status == UserStatusPendingDelete
}
```

`internal/domain/identity.go`：

```go
package domain

import "github.com/google/uuid"

// 登录标识类型。新增登录方式时在此登记。
const (
	IdentityTypePhone    = "phone"
	IdentityTypeUsername = "username"
	IdentityTypeEmail    = "email"
	IdentityTypeWechatMP = "wechat_mp"
)

// mergeableIdentityTypes 是"本地标识"集合。
// 这类标识由 fp 自己校验（短信验证码 / 密码），同一个 subject 必然是同一个人，
// 因此可以直接按 (type, subject) 归并到同一个 User。
// 第三方标识（微信等）的 subject 是对方系统的 openid，只能通过 union_key 归并。
var mergeableIdentityTypes = map[string]bool{
	IdentityTypePhone:    true,
	IdentityTypeUsername: true,
	IdentityTypeEmail:    true,
}

// IsMergeableIdentityType 报告该类型是否为可按 subject 归并的本地标识。
func IsMergeableIdentityType(t string) bool {
	return mergeableIdentityTypes[t]
}

// Identity 是一条登录凭据记录。一个 User 可以有多条。
type Identity struct {
	ID      uuid.UUID
	UserID  uuid.UUID
	Type    string
	Subject string
	// UnionKey 用于跨 Type 归并（微信 unionId）。本地标识为空串。
	UnionKey string
	// Credential 存第三方 token 等。密码存在 User.PasswordHash。
	Credential  string
	LastLoginAt int64
	CreatedAt   int64
}
```

- [ ] **Step 5: 运行 domain 测试确认通过**

Run: `go test ./internal/domain/ -v`
Expected: 6 个测试 PASS

- [ ] **Step 6: 写失败的 service 测试**

`internal/service/user_test.go`：

```go
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

func newUserService(t *testing.T) *service.UserService {
	t.Helper()
	return service.NewUserService(testsupport.NewTestDB(t))
}

func TestEnsureUserWithIdentityCreatesThenReuses(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	in := service.EnsureIdentityInput{
		Type:     domain.IdentityTypePhone,
		Subject:  "13800138000",
		Nickname: "用户8000",
	}

	u1, id1, created, err := svc.EnsureUserWithIdentity(ctx, in)
	if err != nil {
		t.Fatalf("首次 EnsureUserWithIdentity: %v", err)
	}
	if !created {
		t.Fatal("首次调用 created 应为 true")
	}
	if u1.Nickname != "用户8000" {
		t.Fatalf("Nickname = %q", u1.Nickname)
	}
	if id1.Type != domain.IdentityTypePhone || id1.Subject != "13800138000" {
		t.Fatalf("identity = %+v", id1)
	}

	u2, id2, created, err := svc.EnsureUserWithIdentity(ctx, in)
	if err != nil {
		t.Fatalf("再次 EnsureUserWithIdentity: %v", err)
	}
	if created {
		t.Fatal("已存在时 created 应为 false")
	}
	if u2.ID != u1.ID {
		t.Fatalf("UserID 不一致: %v vs %v", u2.ID, u1.ID)
	}
	if id2.ID != id1.ID {
		t.Fatalf("IdentityID 不一致: %v vs %v", id2.ID, id1.ID)
	}
}

// 归并规则的核心用例：同一手机号在 sms_code 与 password 两种登录方式下
// 必须落到同一个 user。两者共用 identity(type='phone')。
func TestSamePhoneAcrossConnectorsMergesToOneUser(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	// 短信验证码首次登录，创建用户
	u1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("短信登录建号: %v", err)
	}

	// 该用户设置密码后，用手机号+密码登录，查到的必须是同一个 user
	if err := svc.SetPassword(ctx, u1.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	u2, _, err := svc.FindByIdentity(ctx, domain.IdentityTypePhone, "13800138000")
	if err != nil {
		t.Fatalf("FindByIdentity: %v", err)
	}
	if u2.ID != u1.ID {
		t.Fatalf("同一手机号归并失败: %v vs %v", u2.ID, u1.ID)
	}
	if err := svc.VerifyPassword(ctx, u2.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
}

// 不同类型的标识各自独立：同一个字符串作为 username 和 phone 是两个人。
func TestDifferentIdentityTypesDoNotMerge(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("phone: %v", err)
	}
	u2, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeUsername, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("username: %v", err)
	}
	if u1.ID == u2.ID {
		t.Fatal("不同 type 的同名 subject 不应归并到同一用户")
	}
}

// 微信这类第三方标识通过 union_key 归并：同一个人的多个 openid 落到同一 user。
func TestWechatMergesByUnionKey(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeWechatMP, Subject: "openid-mp", UnionKey: "union-1",
	})
	if err != nil {
		t.Fatalf("首个 openid: %v", err)
	}

	// 同一 unionId 下的另一个 openid（模拟小程序端）
	u2, id2, created, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: "wechat_mini", Subject: "openid-mini", UnionKey: "union-1",
	})
	if err != nil {
		t.Fatalf("第二个 openid: %v", err)
	}
	if created {
		t.Fatal("同 unionKey 不应创建新用户")
	}
	if u2.ID != u1.ID {
		t.Fatalf("unionKey 归并失败: %v vs %v", u2.ID, u1.ID)
	}
	if id2.Subject != "openid-mini" {
		t.Fatalf("应为新建的 identity 行, got %+v", id2)
	}

	ids, err := svc.ListIdentities(ctx, u1.ID)
	if err != nil {
		t.Fatalf("ListIdentities: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("identity 数量 = %d, want 2", len(ids))
	}
}

func TestAttachIdentityRejectsSubjectOwnedByAnotherUser(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("u1: %v", err)
	}
	u2, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13900139000",
	})
	if err != nil {
		t.Fatalf("u2: %v", err)
	}

	// 把 u1 已占用的手机号绑到 u2 上必须失败，且必须是可识别的冲突错误。
	_, err = svc.AttachIdentity(ctx, u2.ID, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	_ = u1
}

func TestFindByIdentityNotFound(t *testing.T) {
	svc := newUserService(t)
	_, _, err := svc.FindByIdentity(context.Background(), domain.IdentityTypePhone, "13800138000")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestVerifyPasswordWithoutPasswordSet(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	// 未设置密码的用户，任何密码都不应通过，且不能 panic。
	if err := svc.VerifyPassword(ctx, u.ID, ""); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("空密码 err = %v, want ErrInvalidCredential", err)
	}
	if err := svc.VerifyPassword(ctx, u.ID, "anything"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("任意密码 err = %v, want ErrInvalidCredential", err)
	}
}

func TestSetPasswordRejectsTooShort(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if err := svc.SetPassword(ctx, u.ID, "short"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestSetStatusEnforcesStateMachine(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}

	frozen, err := svc.SetStatus(ctx, u.ID, domain.UserStatusFrozen)
	if err != nil {
		t.Fatalf("冻结: %v", err)
	}
	if frozen.Status != domain.UserStatusFrozen {
		t.Fatalf("Status = %q", frozen.Status)
	}
	if frozen.CanLogin() {
		t.Fatal("冻结用户不应可登录")
	}

	// FROZEN → PENDING_DELETE 不是合法迁移
	if _, err := svc.SetStatus(ctx, u.ID, domain.UserStatusPendingDelete); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}

	if _, err := svc.SetStatus(ctx, u.ID, domain.UserStatusActive); err != nil {
		t.Fatalf("解冻: %v", err)
	}
}

func TestEnsureRegistrationIsIdempotent(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	users := service.NewUserService(pool)
	apps := service.NewApplicationService(pool)
	ctx := context.Background()

	app, _, err := apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}
	u, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := users.EnsureRegistration(ctx, u.ID, app.ID); err != nil {
			t.Fatalf("第 %d 次 EnsureRegistration: %v", i+1, err)
		}
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM user_application WHERE user_id = $1 AND application_id = $2`,
		u.ID, app.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("注册关系数量 = %d, want 1", n)
	}
}

func TestGetByIDNotFound(t *testing.T) {
	svc := newUserService(t)
	if _, err := svc.GetByID(context.Background(), uuid.Nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestTouchIdentityLogin(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	_, id, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if id.LastLoginAt != 0 {
		t.Fatalf("新建 identity 的 LastLoginAt = %d, want 0", id.LastLoginAt)
	}

	if err := svc.TouchIdentityLogin(ctx, id.ID); err != nil {
		t.Fatalf("TouchIdentityLogin: %v", err)
	}

	_, got, err := svc.FindByIdentity(ctx, domain.IdentityTypePhone, "13800138000")
	if err != nil {
		t.Fatalf("FindByIdentity: %v", err)
	}
	if got.LastLoginAt == 0 {
		t.Fatal("LastLoginAt 未更新")
	}
}
```

- [ ] **Step 7: 运行测试确认失败**

Run: `go test ./internal/service/ -v`
Expected: 编译失败，`undefined: service.NewUserService`

- [ ] **Step 8: 实现 UserService**

`internal/service/user.go`：

```go
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/basicfu/fp/internal/domain"
)

// minPasswordLength 是密码最小长度。第一阶段只做长度校验，
// 完整密码策略随 password connector 的配置项在后续阶段落地。
const minPasswordLength = 8

const userColumns = `
	id, password_hash, nickname, avatar_url, gender, status,
	coalesce((extract(epoch from delete_submitted_at) * 1000)::bigint, 0),
	(extract(epoch from created_at) * 1000)::bigint,
	(extract(epoch from updated_at) * 1000)::bigint`

const identityColumns = `
	id, user_id, type, subject, union_key, credential,
	coalesce((extract(epoch from last_login_at) * 1000)::bigint, 0),
	(extract(epoch from created_at) * 1000)::bigint`

// UserService 管理用户、登录标识与应用注册关系。
type UserService struct {
	pool *pgxpool.Pool
}

// NewUserService 构造 UserService。
func NewUserService(pool *pgxpool.Pool) *UserService {
	return &UserService{pool: pool}
}

// EnsureIdentityInput 描述一次"确保某个登录标识存在"的请求。
type EnsureIdentityInput struct {
	Type    string
	Subject string
	// UnionKey 非空时参与跨 Type 归并（微信 unionId）。
	UnionKey string
	// Credential 存第三方 token 等。密码不走这里。
	Credential string
	// Nickname 仅在需要新建用户时用作初始昵称。
	Nickname string
}

func (in EnsureIdentityInput) validate() error {
	if in.Type == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "identity 类型不能为空")
	}
	if in.Subject == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "identity subject 不能为空")
	}
	return nil
}

// EnsureUserWithIdentity 按归并规则找到或创建用户，并确保该登录标识挂在其名下。
//
// 归并规则（设计文档 4.3）：
//  1. (type, subject) 已存在 → 直接复用其 user，不新建任何行
//  2. UnionKey 非空且已有同 union_key 的 identity → 复用该 user，新建一行 identity
//  3. 否则新建 user + identity
//
// 返回的 created 表示是否新建了用户。
func (s *UserService) EnsureUserWithIdentity(ctx context.Context, in EnsureIdentityInput) (*domain.User, *domain.Identity, bool, error) {
	if err := in.validate(); err != nil {
		return nil, nil, false, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, false, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 提交成功后 Rollback 是 no-op

	// 规则 1：标识已存在
	user, identity, err := findByIdentityTx(ctx, tx, in.Type, in.Subject)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, false, fmt.Errorf("service: 提交事务: %w", err)
		}
		return user, identity, false, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, nil, false, err
	}

	// 规则 2：unionKey 归并
	var userID uuid.UUID
	createdUser := false
	if in.UnionKey != "" {
		err := tx.QueryRow(ctx,
			`SELECT user_id FROM identity WHERE union_key = $1 LIMIT 1`, in.UnionKey).Scan(&userID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, false, fmt.Errorf("service: 按 unionKey 查询: %w", err)
		}
	}

	// 规则 3：新建用户
	if userID == uuid.Nil {
		row := tx.QueryRow(ctx,
			`INSERT INTO app_user (nickname) VALUES ($1) RETURNING `+userColumns, in.Nickname)
		u, err := scanUser(row)
		if err != nil {
			return nil, nil, false, fmt.Errorf("service: 创建用户: %w", err)
		}
		userID = u.ID
		createdUser = true
	}

	newIdentity, err := insertIdentityTx(ctx, tx, userID, in)
	if err != nil {
		return nil, nil, false, err
	}

	finalUser, err := getUserTx(ctx, tx, userID)
	if err != nil {
		return nil, nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, false, fmt.Errorf("service: 提交事务: %w", err)
	}
	return finalUser, newIdentity, createdUser, nil
}

// AttachIdentity 给已存在的用户挂上一个新的登录标识。
// 该标识已被别的用户占用时返回 domain.ErrConflict。
func (s *UserService) AttachIdentity(ctx context.Context, userID uuid.UUID, in EnsureIdentityInput) (*domain.Identity, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	id, err := insertIdentityTx(ctx, tx, userID, in)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("service: 提交事务: %w", err)
	}
	return id, nil
}

// FindByIdentity 按 (type, subject) 查用户与该标识。
func (s *UserService) FindByIdentity(ctx context.Context, identityType, subject string) (*domain.User, *domain.Identity, error) {
	return findByIdentityTx(ctx, s.pool, identityType, subject)
}

// FindByUnionKey 按 union_key 查用户。
func (s *UserService) FindByUnionKey(ctx context.Context, unionKey string) (*domain.User, error) {
	if unionKey == "" {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "unionKey 不能为空")
	}
	var userID uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT user_id FROM identity WHERE union_key = $1 LIMIT 1`, unionKey).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "未找到该 unionKey 对应的用户")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 按 unionKey 查询: %w", err)
	}
	return s.GetByID(ctx, userID)
}

// GetByID 按用户 ID 查用户。
func (s *UserService) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return getUserTx(ctx, s.pool, id)
}

// ListIdentities 返回某个用户的全部登录标识。永不返回 nil 切片。
func (s *UserService) ListIdentities(ctx context.Context, userID uuid.UUID) ([]domain.Identity, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+identityColumns+` FROM identity WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("service: 查询 identity 列表: %w", err)
	}
	defer rows.Close()

	out := []domain.Identity{}
	for rows.Next() {
		id, err := scanIdentity(rows)
		if err != nil {
			return nil, fmt.Errorf("service: 扫描 identity: %w", err)
		}
		out = append(out, *id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历 identity: %w", err)
	}
	return out, nil
}

// SetPassword 设置或重置用户密码。
func (s *UserService) SetPassword(ctx context.Context, userID uuid.UUID, plain string) error {
	if len([]rune(plain)) < minPasswordLength {
		return domain.Errorf(domain.ErrInvalidArgument, "密码长度不能少于 %d 位", minPasswordLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return fmt.Errorf("service: 计算密码哈希: %w", err)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE app_user SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, string(hash))
	if err != nil {
		return fmt.Errorf("service: 更新密码: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Errorf(domain.ErrNotFound, "用户不存在")
	}
	return nil
}

// VerifyPassword 校验用户密码。未设置密码的用户一律返回 ErrInvalidCredential。
func (s *UserService) VerifyPassword(ctx context.Context, userID uuid.UUID, plain string) error {
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT password_hash FROM app_user WHERE id = $1`, userID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Errorf(domain.ErrInvalidCredential, "账号或密码不正确")
	}
	if err != nil {
		return fmt.Errorf("service: 读取密码哈希: %w", err)
	}
	if hash == "" {
		// 未设置密码。bcrypt 对空哈希会直接报错，这里显式短路以保证错误一致。
		return domain.Errorf(domain.ErrInvalidCredential, "账号或密码不正确")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)); err != nil {
		return domain.Errorf(domain.ErrInvalidCredential, "账号或密码不正确")
	}
	return nil
}

// SetStatus 迁移用户状态，非法迁移返回 domain.ErrInvalidArgument。
func (s *UserService) SetStatus(ctx context.Context, userID uuid.UUID, status string) (*domain.User, error) {
	current, err := s.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if current.Status == status {
		return current, nil
	}
	if !domain.CanTransitionUserStatus(current.Status, status) {
		return nil, domain.Errorf(domain.ErrInvalidArgument,
			"不允许的状态迁移 %s → %s", current.Status, status)
	}

	// 进入注销保护期时记录提交时间；离开时清空。
	var deleteSubmitted any
	if status == domain.UserStatusPendingDelete {
		deleteSubmitted = "now()"
	}
	var row pgx.Row
	if deleteSubmitted != nil {
		row = s.pool.QueryRow(ctx,
			`UPDATE app_user SET status = $2, delete_submitted_at = now(), updated_at = now()
			 WHERE id = $1 RETURNING `+userColumns, userID, status)
	} else {
		row = s.pool.QueryRow(ctx,
			`UPDATE app_user SET status = $2, delete_submitted_at = NULL, updated_at = now()
			 WHERE id = $1 RETURNING `+userColumns, userID, status)
	}
	u, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("service: 更新用户状态: %w", err)
	}
	return u, nil
}

// TouchIdentityLogin 记录该登录标识的最后使用时间。
func (s *UserService) TouchIdentityLogin(ctx context.Context, identityID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `UPDATE identity SET last_login_at = now() WHERE id = $1`, identityID)
	if err != nil {
		return fmt.Errorf("service: 更新 identity 登录时间: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Errorf(domain.ErrNotFound, "identity 不存在")
	}
	return nil
}

// EnsureRegistration 确保用户在该应用下有注册关系。可重复调用。
//
// 注意：user_application 上没有也不允许有 role 列（设计文档 5.5）。
func (s *UserService) EnsureRegistration(ctx context.Context, userID, appID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_application (user_id, application_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id, application_id) DO NOTHING`, userID, appID)
	if err != nil {
		return fmt.Errorf("service: 写入注册关系: %w", err)
	}
	return nil
}

// querier 抽象 pgxpool.Pool 与 pgx.Tx 的公共查询能力，让辅助函数在事务内外都能用。
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func findByIdentityTx(ctx context.Context, q querier, identityType, subject string) (*domain.User, *domain.Identity, error) {
	row := q.QueryRow(ctx,
		`SELECT `+identityColumns+` FROM identity WHERE type = $1 AND subject = $2`,
		identityType, subject)
	identity, err := scanIdentity(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, domain.Errorf(domain.ErrNotFound, "登录标识不存在")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("service: 查询 identity: %w", err)
	}
	user, err := getUserTx(ctx, q, identity.UserID)
	if err != nil {
		return nil, nil, err
	}
	return user, identity, nil
}

func getUserTx(ctx context.Context, q querier, id uuid.UUID) (*domain.User, error) {
	row := q.QueryRow(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = $1`, id)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "用户不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询用户: %w", err)
	}
	return u, nil
}

func insertIdentityTx(ctx context.Context, q querier, userID uuid.UUID, in EnsureIdentityInput) (*domain.Identity, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO identity (user_id, type, subject, union_key, credential)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+identityColumns,
		userID, in.Type, in.Subject, in.UnionKey, in.Credential)

	id, err := scanIdentity(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return nil, domain.Errorf(domain.ErrConflict,
				"登录标识 %s:%s 已被其他账号占用", in.Type, in.Subject)
		}
		return nil, fmt.Errorf("service: 创建 identity: %w", err)
	}
	return id, nil
}

func scanUser(r rowScanner) (*domain.User, error) {
	var u domain.User
	err := r.Scan(&u.ID, &u.PasswordHash, &u.Nickname, &u.AvatarURL, &u.Gender, &u.Status,
		&u.DeleteSubmittedAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func scanIdentity(r rowScanner) (*domain.Identity, error) {
	var i domain.Identity
	err := r.Scan(&i.ID, &i.UserID, &i.Type, &i.Subject, &i.UnionKey, &i.Credential,
		&i.LastLoginAt, &i.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &i, nil
}
```

> `SetStatus` 里 `deleteSubmitted` 那段用 `any` 只是为了走两条分支，实现时直接写成两个 `if` 分支即可，不需要该变量。

- [ ] **Step 9: 运行测试确认通过**

Run: `make test`
Expected: `internal/service` 全部 PASS（管理员 6 + 应用 9 + 用户 12）

- [ ] **Step 10: 提交**

```bash
git add internal
git commit -m "feat: 用户与登录标识模型、账号归并规则与状态机"
```

---

## Task 6: Connector 接口、注册表与 password 登录方式

「新增一种登录方式 = 新增一个实现 + 注册一行，不改编排逻辑」是设计文档的核心要求。本任务把这个契约立起来，并用 password 作为第一个实现验证它。

**关键约定：Connector 只负责「校验凭据并给出登录标识」，不负责建号。** 建号与归并由 `AuthService`（Task 16）依据 `Result.AllowCreate` 统一处理，这样归并规则只有一处实现。

**Files:**
- Create: `internal/domain/field.go`
- Create: `internal/connector/connector.go`, `internal/connector/password.go`
- Test: `internal/connector/connector_test.go`, `internal/connector/password_test.go`

**Interfaces:**
- Consumes: `domain` 哨兵错误、`domain.IdentityType*`、`service.UserService`（通过窄接口注入）
- Produces:
  - `type domain.FieldType string`；常量 `domain.FieldTypeString/FieldTypeInt/FieldTypeBool/FieldTypeSecret = "string"/"int"/"bool"/"secret"`
  - `type domain.Field struct{ Key, Label string; Type FieldType; Required bool; Default any; Help string }`
  - `type connector.Credentials map[string]string`；`func (Credentials) Get(key string) string`
  - `type connector.Result struct{ IdentityType, Subject, UnionKey, Credential, Nickname string; AllowCreate bool }`
  - `type connector.Connector interface{ Type() string; ConfigSchema() []domain.Field; Authenticate(ctx context.Context, cfg map[string]any, creds Credentials) (*Result, error) }`
  - `type connector.Registry struct{}`；`func connector.NewRegistry() *Registry`
  - `func (*Registry) Register(c Connector) error`
  - `func (*Registry) Get(typ string) (Connector, error)`
  - `func (*Registry) Types() []string`
  - `func (*Registry) Schemas() map[string][]domain.Field`
  - `type connector.UserLookup interface{ FindByIdentity(ctx context.Context, identityType, subject string) (*domain.User, *domain.Identity, error); VerifyPassword(ctx context.Context, userID uuid.UUID, plain string) error }`
  - `func connector.NewPassword(lookup UserLookup) *PasswordConnector`
  - `func connector.DetectIdentityType(account string) string`
  - 常量 `connector.TypePassword = "password"`
  - 配置读取助手：`func connector.ConfigBool(cfg map[string]any, key string, def bool) bool`、`func connector.ConfigInt(cfg map[string]any, key string, def int) int`、`func connector.ConfigString(cfg map[string]any, key, def string) string`

- [ ] **Step 1: 实现共享的表单 schema 类型**

`Field` 放在 `domain` 而非 `connector`，因为通知中心的 Provider 配置（Task 7）与后续的配置中心都要用同一套元数据驱动管理 UI 的动态表单。

`internal/domain/field.go`：

```go
package domain

// FieldType 决定管理 UI 用什么控件渲染该配置项。
type FieldType string

const (
	FieldTypeString FieldType = "string"
	FieldTypeInt    FieldType = "int"
	FieldTypeBool   FieldType = "bool"
	// FieldTypeSecret 与 string 相同，但 UI 上脱敏显示、落库加密。
	FieldTypeSecret FieldType = "secret"
)

// Field 是一个配置项的元数据。管理 UI 靠它自动生成表单，
// 因此新增登录方式或通知供应商都不需要改动前端代码。
type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required"`
	Default  any       `json:"default,omitempty"`
	Help     string    `json:"help,omitempty"`
}
```

- [ ] **Step 2: 写失败的 registry 测试**

`internal/connector/connector_test.go`：

```go
package connector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

// stubConnector 是只为测试注册表而存在的最小实现。
type stubConnector struct{ typ string }

func (s stubConnector) Type() string { return s.typ }
func (s stubConnector) ConfigSchema() []domain.Field {
	return []domain.Field{{Key: "k", Label: "K", Type: domain.FieldTypeString}}
}
func (s stubConnector) Authenticate(context.Context, map[string]any, connector.Credentials) (*connector.Result, error) {
	return &connector.Result{IdentityType: "stub", Subject: "s"}, nil
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := connector.NewRegistry()

	if err := r.Register(stubConnector{typ: "a"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Get("a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Type() != "a" {
		t.Fatalf("Type = %q, want a", got.Type())
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	r := connector.NewRegistry()
	if err := r.Register(stubConnector{typ: "a"}); err != nil {
		t.Fatalf("首次 Register: %v", err)
	}
	if err := r.Register(stubConnector{typ: "a"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestRegistryRejectsEmptyType(t *testing.T) {
	r := connector.NewRegistry()
	if err := r.Register(stubConnector{typ: ""}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	r := connector.NewRegistry()
	if _, err := r.Get("nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRegistryTypesIsSorted(t *testing.T) {
	r := connector.NewRegistry()
	for _, typ := range []string{"c", "a", "b"} {
		if err := r.Register(stubConnector{typ: typ}); err != nil {
			t.Fatalf("Register %s: %v", typ, err)
		}
	}
	got := r.Types()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("Types() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Types() = %v, want %v", got, want)
		}
	}
}

func TestRegistrySchemas(t *testing.T) {
	r := connector.NewRegistry()
	if err := r.Register(stubConnector{typ: "a"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	schemas := r.Schemas()
	if len(schemas["a"]) != 1 || schemas["a"][0].Key != "k" {
		t.Fatalf("Schemas() = %+v", schemas)
	}
}

func TestCredentialsGet(t *testing.T) {
	c := connector.Credentials{"a": " x "}
	if got := c.Get("a"); got != "x" {
		t.Fatalf("Get 应当去除首尾空白, got %q", got)
	}
	if got := c.Get("missing"); got != "" {
		t.Fatalf("缺失键应返回空串, got %q", got)
	}
}

func TestConfigHelpers(t *testing.T) {
	// 从 JSONB 读出的数字是 float64，助手必须能处理。
	cfg := map[string]any{
		"n":    float64(10),
		"nInt": 7,
		"b":    true,
		"s":    "hello",
	}
	if got := connector.ConfigInt(cfg, "n", 1); got != 10 {
		t.Errorf("ConfigInt(float64) = %d, want 10", got)
	}
	if got := connector.ConfigInt(cfg, "nInt", 1); got != 7 {
		t.Errorf("ConfigInt(int) = %d, want 7", got)
	}
	if got := connector.ConfigInt(cfg, "absent", 3); got != 3 {
		t.Errorf("ConfigInt(缺省) = %d, want 3", got)
	}
	if got := connector.ConfigInt(cfg, "s", 3); got != 3 {
		t.Errorf("ConfigInt(类型不符) = %d, want 3", got)
	}
	if got := connector.ConfigBool(cfg, "b", false); !got {
		t.Error("ConfigBool = false, want true")
	}
	if got := connector.ConfigBool(cfg, "absent", true); !got {
		t.Error("ConfigBool(缺省) = false, want true")
	}
	if got := connector.ConfigString(cfg, "s", "x"); got != "hello" {
		t.Errorf("ConfigString = %q, want hello", got)
	}
	if got := connector.ConfigString(nil, "s", "x"); got != "x" {
		t.Errorf("ConfigString(nil cfg) = %q, want x", got)
	}
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/connector/ -v`
Expected: 编译失败，`undefined: connector.NewRegistry`

- [ ] **Step 4: 实现 connector.go**

`internal/connector/connector.go`：

```go
// Package connector 定义登录方式的统一契约。
//
// 新增一种登录方式只需要：实现 Connector 接口，然后在启动时 Register 一行。
// 管理 UI 的配置表单由 ConfigSchema() 自动渲染，无需改动前端。
//
// Connector 只负责「校验凭据并给出登录标识」。建号与账号归并由 AuthService
// 依据 Result.AllowCreate 统一处理，保证归并规则只有一处实现。
package connector

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

// Credentials 是一次登录请求携带的凭据，键名由各 Connector 自行定义。
type Credentials map[string]string

// Get 返回去除首尾空白后的值，键不存在时返回空串。
func (c Credentials) Get(key string) string {
	return strings.TrimSpace(c[key])
}

// Result 是校验成功后 Connector 给出的登录标识。
type Result struct {
	// IdentityType / Subject 唯一确定一个 identity 行。
	IdentityType string
	Subject      string
	// UnionKey 非空时参与跨类型归并（微信 unionId）。
	UnionKey string
	// Credential 存第三方 token 等，密码类不使用。
	Credential string
	// Nickname 仅在需要新建用户时用作初始昵称。
	Nickname string
	// AllowCreate 表示标识不存在时是否允许自动建号。
	// 短信验证码登录为 true（验证码本身证明了手机号归属）；
	// 密码登录为 false（连账号都不存在，谈不上密码正确）。
	AllowCreate bool
}

// Connector 是一种登录方式。
type Connector interface {
	// Type 是该登录方式的稳定标识，同时是 application_connector.connector_type 的值。
	Type() string
	// ConfigSchema 描述该登录方式的可配置项，供管理 UI 渲染表单。
	ConfigSchema() []domain.Field
	// Authenticate 校验凭据。cfg 是该应用为本登录方式保存的配置。
	// 校验失败必须返回 domain.ErrInvalidCredential 的包装。
	Authenticate(ctx context.Context, cfg map[string]any, creds Credentials) (*Result, error)
}

// Registry 是进程内的登录方式注册表。构造后即只读，无需加锁。
type Registry struct {
	m map[string]Connector
}

// NewRegistry 返回空注册表。
func NewRegistry() *Registry {
	return &Registry{m: make(map[string]Connector)}
}

// Register 登记一个登录方式。类型重复或为空时报错。
func (r *Registry) Register(c Connector) error {
	typ := c.Type()
	if typ == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "connector 类型不能为空")
	}
	if _, ok := r.m[typ]; ok {
		return domain.Errorf(domain.ErrConflict, "connector %q 已注册", typ)
	}
	r.m[typ] = c
	return nil
}

// Get 按类型取登录方式。
func (r *Registry) Get(typ string) (Connector, error) {
	c, ok := r.m[typ]
	if !ok {
		return nil, domain.Errorf(domain.ErrNotFound, "未知的登录方式 %q", typ)
	}
	return c, nil
}

// Types 返回已注册的全部类型，按字典序排列。
func (r *Registry) Types() []string {
	out := make([]string, 0, len(r.m))
	for typ := range r.m {
		out = append(out, typ)
	}
	sort.Strings(out)
	return out
}

// Schemas 返回全部登录方式的配置元数据，供管理 UI 一次性拉取。
func (r *Registry) Schemas() map[string][]domain.Field {
	out := make(map[string][]domain.Field, len(r.m))
	for typ, c := range r.m {
		out[typ] = c.ConfigSchema()
	}
	return out
}

// UserLookup 是 password connector 需要的最小用户查询能力。
// 用窄接口而非直接依赖 *service.UserService，避免 connector 包反向依赖 service 包。
type UserLookup interface {
	FindByIdentity(ctx context.Context, identityType, subject string) (*domain.User, *domain.Identity, error)
	VerifyPassword(ctx context.Context, userID uuid.UUID, plain string) error
}

// ConfigInt 从配置里读整数。JSONB 反序列化出的数字是 float64，两种都要接受。
func ConfigInt(cfg map[string]any, key string, def int) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return def
	}
}

// ConfigBool 从配置里读布尔值。
func ConfigBool(cfg map[string]any, key string, def bool) bool {
	if v, ok := cfg[key].(bool); ok {
		return v
	}
	return def
}

// ConfigString 从配置里读字符串。
func ConfigString(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return def
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/connector/ -v`
Expected: 7 个测试 PASS

- [ ] **Step 6: 写失败的 password 测试**

`internal/connector/password_test.go`：

```go
package connector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

// fakeLookup 用内存数据模拟 UserService 的两个查询方法。
type fakeLookup struct {
	// byIdentity 的键是 type + "|" + subject
	byIdentity map[string]uuid.UUID
	passwords  map[uuid.UUID]string
	statuses   map[uuid.UUID]string
}

func newFakeLookup() *fakeLookup {
	return &fakeLookup{
		byIdentity: map[string]uuid.UUID{},
		passwords:  map[uuid.UUID]string{},
		statuses:   map[uuid.UUID]string{},
	}
}

func (f *fakeLookup) add(identityType, subject, password, status string) uuid.UUID {
	id := uuid.New()
	f.byIdentity[identityType+"|"+subject] = id
	f.passwords[id] = password
	f.statuses[id] = status
	return id
}

func (f *fakeLookup) FindByIdentity(_ context.Context, identityType, subject string) (*domain.User, *domain.Identity, error) {
	id, ok := f.byIdentity[identityType+"|"+subject]
	if !ok {
		return nil, nil, domain.Errorf(domain.ErrNotFound, "登录标识不存在")
	}
	return &domain.User{ID: id, Status: f.statuses[id]},
		&domain.Identity{UserID: id, Type: identityType, Subject: subject}, nil
}

func (f *fakeLookup) VerifyPassword(_ context.Context, userID uuid.UUID, plain string) error {
	want, ok := f.passwords[userID]
	if !ok || want == "" || want != plain {
		return domain.Errorf(domain.ErrInvalidCredential, "账号或密码不正确")
	}
	return nil
}

func TestDetectIdentityType(t *testing.T) {
	tests := []struct {
		account string
		want    string
	}{
		{"13800138000", domain.IdentityTypePhone},
		{"18612345678", domain.IdentityTypePhone},
		{"a@b.com", domain.IdentityTypeEmail},
		{"alice", domain.IdentityTypeUsername},
		{"alice123", domain.IdentityTypeUsername},
		// 11 位但不以 1 开头，不是手机号
		{"23800138000", domain.IdentityTypeUsername},
		// 10 位数字不是手机号
		{"1380013800", domain.IdentityTypeUsername},
	}
	for _, tt := range tests {
		if got := connector.DetectIdentityType(tt.account); got != tt.want {
			t.Errorf("DetectIdentityType(%q) = %q, want %q", tt.account, got, tt.want)
		}
	}
}

func TestPasswordType(t *testing.T) {
	c := connector.NewPassword(newFakeLookup())
	if c.Type() != connector.TypePassword {
		t.Fatalf("Type = %q, want %q", c.Type(), connector.TypePassword)
	}
	if len(c.ConfigSchema()) == 0 {
		t.Fatal("ConfigSchema 不应为空")
	}
}

func TestPasswordAuthenticateSuccess(t *testing.T) {
	lookup := newFakeLookup()
	lookup.add(domain.IdentityTypePhone, "13800138000", "hunter2hunter2", domain.UserStatusActive)
	c := connector.NewPassword(lookup)

	res, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"account":  "13800138000",
		"password": "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if res.IdentityType != domain.IdentityTypePhone || res.Subject != "13800138000" {
		t.Fatalf("res = %+v", res)
	}
	if res.AllowCreate {
		t.Fatal("密码登录不应允许自动建号")
	}
}

func TestPasswordAuthenticateWrongPassword(t *testing.T) {
	lookup := newFakeLookup()
	lookup.add(domain.IdentityTypePhone, "13800138000", "hunter2hunter2", domain.UserStatusActive)
	c := connector.NewPassword(lookup)

	_, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"account": "13800138000", "password": "wrong",
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

// 账号不存在必须返回与密码错误相同的错误，避免账号枚举。
func TestPasswordAuthenticateUnknownAccountLooksLikeWrongPassword(t *testing.T) {
	c := connector.NewPassword(newFakeLookup())

	_, err := c.Authenticate(context.Background(), nil, connector.Credentials{
		"account": "13800138000", "password": "whatever",
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

func TestPasswordAuthenticateRequiresBothFields(t *testing.T) {
	c := connector.NewPassword(newFakeLookup())
	ctx := context.Background()

	if _, err := c.Authenticate(ctx, nil, connector.Credentials{"password": "x"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("缺 account err = %v, want ErrInvalidArgument", err)
	}
	if _, err := c.Authenticate(ctx, nil, connector.Credentials{"account": "a"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("缺 password err = %v, want ErrInvalidArgument", err)
	}
}

// 配置里关掉某种标识类型后，该类型的账号不能用密码登录。
func TestPasswordAuthenticateRespectsAllowedIdentityTypes(t *testing.T) {
	lookup := newFakeLookup()
	lookup.add(domain.IdentityTypeUsername, "alice", "hunter2hunter2", domain.UserStatusActive)
	c := connector.NewPassword(lookup)
	ctx := context.Background()

	cfg := map[string]any{"allowUsername": false}
	_, err := c.Authenticate(ctx, cfg, connector.Credentials{
		"account": "alice", "password": "hunter2hunter2",
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}

	// 打开后可以登录
	cfg["allowUsername"] = true
	if _, err := c.Authenticate(ctx, cfg, connector.Credentials{
		"account": "alice", "password": "hunter2hunter2",
	}); err != nil {
		t.Fatalf("允许后仍失败: %v", err)
	}
}

// 邮箱默认关闭，需要显式打开。
func TestPasswordEmailDisabledByDefault(t *testing.T) {
	lookup := newFakeLookup()
	lookup.add(domain.IdentityTypeEmail, "a@b.com", "hunter2hunter2", domain.UserStatusActive)
	c := connector.NewPassword(lookup)
	ctx := context.Background()

	if _, err := c.Authenticate(ctx, nil, connector.Credentials{
		"account": "a@b.com", "password": "hunter2hunter2",
	}); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("默认应关闭邮箱登录, err = %v", err)
	}
	if _, err := c.Authenticate(ctx, map[string]any{"allowEmail": true}, connector.Credentials{
		"account": "a@b.com", "password": "hunter2hunter2",
	}); err != nil {
		t.Fatalf("打开后仍失败: %v", err)
	}
}
```

- [ ] **Step 7: 运行测试确认失败**

Run: `go test ./internal/connector/ -run Password -v`
Expected: 编译失败，`undefined: connector.NewPassword`

- [ ] **Step 8: 实现 password.go**

`internal/connector/password.go`：

```go
package connector

import (
	"context"
	"strings"

	"github.com/basicfu/fp/internal/domain"
)

// TypePassword 是密码登录方式的类型标识。
const TypePassword = "password"

// PasswordConnector 用「账号 + 密码」校验身份。
// 账号可以是手机号、用户名或邮箱，具体开放哪几种由应用配置决定。
type PasswordConnector struct {
	lookup UserLookup
}

// NewPassword 构造密码登录方式。
func NewPassword(lookup UserLookup) *PasswordConnector {
	return &PasswordConnector{lookup: lookup}
}

// Type 实现 Connector。
func (c *PasswordConnector) Type() string { return TypePassword }

// ConfigSchema 实现 Connector。
func (c *PasswordConnector) ConfigSchema() []domain.Field {
	return []domain.Field{
		{Key: "allowPhone", Label: "允许手机号登录", Type: domain.FieldTypeBool, Default: true},
		{Key: "allowUsername", Label: "允许用户名登录", Type: domain.FieldTypeBool, Default: true},
		{Key: "allowEmail", Label: "允许邮箱登录", Type: domain.FieldTypeBool, Default: false},
	}
}

// Authenticate 实现 Connector。
//
// 凭据键：account（手机号/用户名/邮箱）、password。
//
// 账号不存在、账号类型未开放、密码错误三种情况**返回同一个错误**，
// 避免攻击者据此枚举已注册账号。
func (c *PasswordConnector) Authenticate(ctx context.Context, cfg map[string]any, creds Credentials) (*Result, error) {
	account := creds.Get("account")
	password := creds.Get("password")
	if account == "" {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "account 不能为空")
	}
	if password == "" {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "password 不能为空")
	}

	// 统一的失败错误，三种失败原因共用，防止账号枚举。
	invalid := domain.Errorf(domain.ErrInvalidCredential, "账号或密码不正确")

	identityType := DetectIdentityType(account)
	if !identityTypeAllowed(cfg, identityType) {
		return nil, invalid
	}

	user, _, err := c.lookup.FindByIdentity(ctx, identityType, account)
	if err != nil {
		return nil, invalid
	}
	if err := c.lookup.VerifyPassword(ctx, user.ID, password); err != nil {
		return nil, invalid
	}

	return &Result{
		IdentityType: identityType,
		Subject:      account,
		// 密码登录不建号：账号都不存在，谈不上密码正确。
		AllowCreate: false,
	}, nil
}

func identityTypeAllowed(cfg map[string]any, identityType string) bool {
	switch identityType {
	case domain.IdentityTypePhone:
		return ConfigBool(cfg, "allowPhone", true)
	case domain.IdentityTypeUsername:
		return ConfigBool(cfg, "allowUsername", true)
	case domain.IdentityTypeEmail:
		return ConfigBool(cfg, "allowEmail", false)
	default:
		return false
	}
}

// DetectIdentityType 从账号字符串推断标识类型。
//
//	11 位、以 1 开头的纯数字 → phone
//	含 @                    → email
//	其余                    → username
func DetectIdentityType(account string) string {
	if strings.Contains(account, "@") {
		return domain.IdentityTypeEmail
	}
	if isChineseMobile(account) {
		return domain.IdentityTypePhone
	}
	return domain.IdentityTypeUsername
}

func isChineseMobile(s string) bool {
	if len(s) != 11 || s[0] != '1' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
```

- [ ] **Step 9: 运行测试确认通过**

Run: `go test ./internal/connector/ -v`
Expected: 全部 PASS（注册表 7 + password 7）

- [ ] **Step 10: 验证 UserService 满足 UserLookup**

在 `internal/service/user.go` 末尾加一行编译期断言。因为 `service` 不能 import `connector`（会形成循环），断言反向写在 `connector` 包的测试里：

`internal/connector/interface_assert_test.go`：

```go
package connector_test

import (
	"testing"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/service"
)

// TestUserServiceSatisfiesUserLookup 保证 service.UserService 的方法签名
// 与 connector.UserLookup 保持一致。签名漂移会在这里编译失败。
func TestUserServiceSatisfiesUserLookup(t *testing.T) {
	var _ connector.UserLookup = (*service.UserService)(nil)
}
```

- [ ] **Step 11: 运行全部测试**

Run: `make test`
Expected: 全部 PASS

- [ ] **Step 12: 提交**

```bash
git add internal
git commit -m "feat: Connector 契约与注册表、password 登录方式"
```

---

## Task 7: 通知中心（Provider 抽象、频率限制、验证码收发）

第一阶段只要求「能发验证码」，但**多供应商顺序降级**只需十几行，且正是设计文档点名的痛点（3s 切供应商靠改 `SendSms` 里注释哪一行），因此一并做掉。不做的是：模板管理 UI、邮件与站内信通道、发送记录查询界面。

**Files:**
- Create: `internal/store/ratelimit.go`
- Create: `internal/store/migrations/00005_notify.sql`
- Create: `internal/notify/notify.go`, `internal/notify/code.go`, `internal/notify/fake.go`
- Test: `internal/store/ratelimit_test.go`, `internal/notify/notify_test.go`, `internal/notify/code_test.go`

**Interfaces:**
- Consumes: `store.OpenRedis` 产出的 `*redis.Client`、`domain.Field`、`domain` 哨兵错误
- Produces:
  - `func store.NewRateLimiter(rdb *redis.Client) *RateLimiter`
  - `func (*RateLimiter) Allow(ctx context.Context, key string, window time.Duration, limit int) (allowed bool, retryAfter time.Duration, err error)`
  - `type notify.Channel string`；常量 `notify.ChannelSMS = "sms"`、`notify.ChannelEmail = "email"`
  - `type notify.Message struct{ Channel Channel; To, Template string; Params map[string]string }`
  - `type notify.Provider interface{ Name() string; Channel() Channel; ConfigSchema() []domain.Field; Send(ctx context.Context, msg Message) error }`
  - `type notify.RateRule struct{ Name string; Window time.Duration; Limit int }`
  - `func notify.DefaultSMSRateRules() []RateRule`
  - `func notify.NewSender(pool *pgxpool.Pool, limiter *store.RateLimiter, rules []RateRule) *Sender`
  - `func (*Sender) AddProvider(p Provider)`
  - `func (*Sender) Send(ctx context.Context, msg Message) error`
  - `func notify.NewCodeService(rdb *redis.Client) *CodeService`
  - `func (*CodeService) Issue(ctx context.Context, purpose, target string) (string, error)`
  - `func (*CodeService) Verify(ctx context.Context, purpose, target, code string) error`
  - 常量 `notify.PurposeLogin = "login"`
  - `func notify.NewFakeProvider(ch Channel, name string) *FakeProvider`
  - `func (*FakeProvider) Sent() []Message`、`func (*FakeProvider) FailNext(err error)`、`func (*FakeProvider) LastParam(key string) string`

- [ ] **Step 1: 写失败的限流测试**

`internal/store/ratelimit_test.go`：

```go
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestRateLimiterAllowsUpToLimit(t *testing.T) {
	rl := store.NewRateLimiter(testsupport.NewTestRedis(t))
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		allowed, _, err := rl.Allow(ctx, "k", time.Minute, 3)
		if err != nil {
			t.Fatalf("第 %d 次 Allow: %v", i, err)
		}
		if !allowed {
			t.Fatalf("第 %d 次应当放行", i)
		}
	}

	allowed, retryAfter, err := rl.Allow(ctx, "k", time.Minute, 3)
	if err != nil {
		t.Fatalf("第 4 次 Allow: %v", err)
	}
	if allowed {
		t.Fatal("第 4 次应当拒绝")
	}
	if retryAfter <= 0 || retryAfter > time.Minute {
		t.Fatalf("retryAfter = %v, 应在 (0, 1m] 内", retryAfter)
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	rl := store.NewRateLimiter(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if allowed, _, _ := rl.Allow(ctx, "a", time.Minute, 1); !allowed {
		t.Fatal("a 首次应放行")
	}
	if allowed, _, _ := rl.Allow(ctx, "b", time.Minute, 1); !allowed {
		t.Fatal("b 首次应放行")
	}
	if allowed, _, _ := rl.Allow(ctx, "a", time.Minute, 1); allowed {
		t.Fatal("a 第二次应拒绝")
	}
}

func TestRateLimiterWindowExpires(t *testing.T) {
	rl := store.NewRateLimiter(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if allowed, _, _ := rl.Allow(ctx, "k", 300*time.Millisecond, 1); !allowed {
		t.Fatal("首次应放行")
	}
	if allowed, _, _ := rl.Allow(ctx, "k", 300*time.Millisecond, 1); allowed {
		t.Fatal("窗口内第二次应拒绝")
	}

	time.Sleep(400 * time.Millisecond)

	if allowed, _, _ := rl.Allow(ctx, "k", 300*time.Millisecond, 1); !allowed {
		t.Fatal("窗口过期后应重新放行")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/ -run RateLimiter -v`
Expected: 编译失败，`undefined: store.NewRateLimiter`

- [ ] **Step 3: 实现限流器**

`internal/store/ratelimit.go`：

```go
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// allowScript 是固定窗口计数器。
// 第一次计数时设置窗口 TTL，之后只累加，因此窗口不会被后续请求延长。
// 返回 {当前计数, 剩余毫秒}。
var allowScript = redis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if c == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return {c, redis.call('PTTL', KEYS[1])}
`)

// RateLimiter 是基于 Redis 的固定窗口频率限制。
type RateLimiter struct {
	rdb *redis.Client
}

// NewRateLimiter 构造 RateLimiter。
func NewRateLimiter(rdb *redis.Client) *RateLimiter {
	return &RateLimiter{rdb: rdb}
}

// Allow 在 window 内最多放行 limit 次。
// 被拒绝时 retryAfter 是当前窗口的剩余时间。
func (rl *RateLimiter) Allow(ctx context.Context, key string, window time.Duration, limit int) (bool, time.Duration, error) {
	res, err := allowScript.Run(ctx, rl.rdb, []string{"fp:rl:" + key}, window.Milliseconds()).Slice()
	if err != nil {
		return false, 0, fmt.Errorf("store: 执行限流脚本: %w", err)
	}
	if len(res) != 2 {
		return false, 0, fmt.Errorf("store: 限流脚本返回 %d 个值, want 2", len(res))
	}
	count, _ := res[0].(int64)
	pttl, _ := res[1].(int64)

	if count > int64(limit) {
		retryAfter := time.Duration(pttl) * time.Millisecond
		if retryAfter < 0 {
			retryAfter = 0
		}
		return false, retryAfter, nil
	}
	return true, 0, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/store/ -run RateLimiter -v`
Expected: 3 个测试 PASS

- [ ] **Step 5: 写迁移**

`internal/store/migrations/00005_notify.sql`：

```sql
-- +goose Up
-- 发送记录。注意：绝不写入验证码本身，params 只留非敏感的模板变量名。
CREATE TABLE notify_log (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    channel    text NOT NULL,
    target     text NOT NULL,
    template   text NOT NULL,
    provider   text NOT NULL DEFAULT '',
    success    boolean NOT NULL,
    error      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notify_log_target_idx ON notify_log (target, created_at DESC);

-- +goose Down
DROP TABLE notify_log;
```

- [ ] **Step 6: 写失败的验证码测试**

`internal/notify/code_test.go`：

```go
package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestIssueAndVerify(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	code, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("code = %q, 长度 want 6", code)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			t.Fatalf("code = %q, 应为纯数字", code)
		}
	}

	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", code); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// 验证码必须是一次性的。
func TestVerifyIsOneShot(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	code, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", code); err != nil {
		t.Fatalf("首次 Verify: %v", err)
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", code); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("二次 Verify err = %v, want ErrInvalidCredential", err)
	}
}

func TestVerifyWrongCode(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if _, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", "000000"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

func TestVerifyWithoutIssue(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	if err := svc.Verify(context.Background(), notify.PurposeLogin, "13800138000", "123456"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

// 连续猜错达到上限后，验证码立即作废，正确的码也不再有效——防爆破。
func TestVerifyAttemptLimit(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	code, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	for i := 0; i < notify.MaxVerifyAttempts; i++ {
		err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", "000000")
		if !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("第 %d 次猜错 err = %v", i+1, err)
		}
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", code); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("超限后正确的码也应无效, err = %v", err)
	}
}

// 不同用途 / 不同号码之间互不干扰。
func TestCodeScopedByPurposeAndTarget(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	codeA, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
	if err != nil {
		t.Fatalf("Issue A: %v", err)
	}
	if _, err := svc.Issue(ctx, notify.PurposeLogin, "13900139000"); err != nil {
		t.Fatalf("Issue B: %v", err)
	}

	if err := svc.Verify(ctx, notify.PurposeLogin, "13900139000", codeA); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("A 的码不应能验 B, err = %v", err)
	}
	if err := svc.Verify(ctx, "other", "13800138000", codeA); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("换用途不应通过, err = %v", err)
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", codeA); err != nil {
		t.Fatalf("原用途原号码应通过: %v", err)
	}
}
```

- [ ] **Step 7: 运行测试确认失败**

Run: `go test ./internal/notify/ -v`
Expected: 编译失败，`no required module provides package .../internal/notify`

- [ ] **Step 8: 实现验证码服务**

`internal/notify/code.go`：

```go
package notify

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
)

// 验证码用途。同一号码在不同用途下的验证码互相独立。
const (
	PurposeLogin = "login"
)

const (
	// codeTTL 是验证码有效期。
	codeTTL = 5 * time.Minute
	// MaxVerifyAttempts 是同一个验证码允许的最大校验次数，超过即作废。
	MaxVerifyAttempts = 5
	// codeLength 是验证码位数。
	codeLength = 6
)

// verifyScript 原子地完成「比对 + 计次 + 消费」。
// 拆成多条命令会在并发下产生「同一个码被用两次」的窗口。
//
// 返回值：1 成功；0 码不匹配；-1 码不存在或已过期；-2 尝试次数超限（码已作废）。
var verifyScript = redis.NewScript(`
local stored = redis.call('GET', KEYS[1])
if not stored then
  return -1
end
local tries = redis.call('INCR', KEYS[2])
if tries == 1 then
  redis.call('PEXPIRE', KEYS[2], ARGV[2])
end
if tries > tonumber(ARGV[3]) then
  redis.call('DEL', KEYS[1], KEYS[2])
  return -2
end
if stored == ARGV[1] then
  redis.call('DEL', KEYS[1], KEYS[2])
  return 1
end
return 0
`)

// CodeService 负责验证码的签发与一次性校验。
type CodeService struct {
	rdb *redis.Client
}

// NewCodeService 构造 CodeService。
func NewCodeService(rdb *redis.Client) *CodeService {
	return &CodeService{rdb: rdb}
}

// Issue 生成并存储一个验证码，覆盖该 (purpose, target) 下已有的验证码。
func (s *CodeService) Issue(ctx context.Context, purpose, target string) (string, error) {
	if purpose == "" || target == "" {
		return "", domain.Errorf(domain.ErrInvalidArgument, "purpose 与 target 不能为空")
	}
	code, err := randomDigits(codeLength)
	if err != nil {
		return "", err
	}
	// 重新签发时一并清掉旧的尝试计数，否则上一轮的失败次数会拖累新码。
	if err := s.rdb.Del(ctx, tryKey(purpose, target)).Err(); err != nil {
		return "", fmt.Errorf("notify: 清理验证码尝试计数: %w", err)
	}
	if err := s.rdb.Set(ctx, codeKey(purpose, target), code, codeTTL).Err(); err != nil {
		return "", fmt.Errorf("notify: 写入验证码: %w", err)
	}
	return code, nil
}

// Verify 校验验证码。成功后该验证码立即作废。
// 码错误、码不存在、尝试超限一律返回 domain.ErrInvalidCredential，不向调用方区分。
func (s *CodeService) Verify(ctx context.Context, purpose, target, code string) error {
	if code == "" {
		return domain.Errorf(domain.ErrInvalidCredential, "验证码不正确")
	}
	res, err := verifyScript.Run(ctx, s.rdb,
		[]string{codeKey(purpose, target), tryKey(purpose, target)},
		code, codeTTL.Milliseconds(), MaxVerifyAttempts).Int64()
	if err != nil {
		return fmt.Errorf("notify: 执行验证码校验脚本: %w", err)
	}
	if res == 1 {
		return nil
	}
	return domain.Errorf(domain.ErrInvalidCredential, "验证码不正确或已过期")
}

func codeKey(purpose, target string) string { return "fp:code:" + purpose + ":" + target }
func tryKey(purpose, target string) string  { return "fp:code:try:" + purpose + ":" + target }

// randomDigits 生成 n 位密码学随机数字串。
func randomDigits(n int) (string, error) {
	const digits = "0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", fmt.Errorf("notify: 生成随机验证码: %w", err)
		}
		b[i] = digits[idx.Int64()]
	}
	return string(b), nil
}
```

- [ ] **Step 9: 运行测试确认通过**

Run: `go test ./internal/notify/ -v`
Expected: 6 个测试 PASS

- [ ] **Step 10: 写失败的 Sender 测试**

`internal/notify/notify_test.go`：

```go
package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func newSender(t *testing.T, rules []notify.RateRule) (*notify.Sender, *notify.FakeProvider) {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	limiter := store.NewRateLimiter(testsupport.NewTestRedis(t))
	s := notify.NewSender(pool, limiter, rules)
	p := notify.NewFakeProvider(notify.ChannelSMS, "fake")
	s.AddProvider(p)
	return s, p
}

func msg(to string) notify.Message {
	return notify.Message{
		Channel:  notify.ChannelSMS,
		To:       to,
		Template: "login_code",
		Params:   map[string]string{"code": "123456"},
	}
}

func TestSenderDelivers(t *testing.T) {
	s, p := newSender(t, nil)

	if err := s.Send(context.Background(), msg("13800138000")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := p.Sent()
	if len(sent) != 1 {
		t.Fatalf("发送数量 = %d, want 1", len(sent))
	}
	if sent[0].To != "13800138000" || sent[0].Template != "login_code" {
		t.Fatalf("sent = %+v", sent[0])
	}
	if p.LastParam("code") != "123456" {
		t.Fatalf("code 参数 = %q", p.LastParam("code"))
	}
}

func TestSenderRejectsUnknownChannel(t *testing.T) {
	s, _ := newSender(t, nil)
	m := msg("13800138000")
	m.Channel = notify.ChannelEmail

	if err := s.Send(context.Background(), m); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSenderEnforcesRateRules(t *testing.T) {
	rules := []notify.RateRule{{Name: "burst", Window: time.Minute, Limit: 2}}
	s, p := newSender(t, rules)
	ctx := context.Background()

	for i := 1; i <= 2; i++ {
		if err := s.Send(ctx, msg("13800138000")); err != nil {
			t.Fatalf("第 %d 次 Send: %v", i, err)
		}
	}
	if err := s.Send(ctx, msg("13800138000")); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("第 3 次 err = %v, want ErrRateLimited", err)
	}
	if len(p.Sent()) != 2 {
		t.Fatalf("被限流的请求不应到达 provider, sent = %d", len(p.Sent()))
	}

	// 限流按号码隔离
	if err := s.Send(ctx, msg("13900139000")); err != nil {
		t.Fatalf("另一号码 Send: %v", err)
	}
}

// 主供应商失败时自动降级到下一个——对症 3s 靠改注释切供应商的问题。
func TestSenderFallsBackToNextProvider(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	limiter := store.NewRateLimiter(testsupport.NewTestRedis(t))
	s := notify.NewSender(pool, limiter, nil)

	primary := notify.NewFakeProvider(notify.ChannelSMS, "primary")
	backup := notify.NewFakeProvider(notify.ChannelSMS, "backup")
	s.AddProvider(primary)
	s.AddProvider(backup)

	primary.FailNext(errors.New("供应商余额不足"))

	if err := s.Send(context.Background(), msg("13800138000")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(primary.Sent()) != 0 {
		t.Fatalf("primary 不应成功发出, sent = %d", len(primary.Sent()))
	}
	if len(backup.Sent()) != 1 {
		t.Fatalf("backup 应接手, sent = %d", len(backup.Sent()))
	}
}

func TestSenderReturnsErrorWhenAllProvidersFail(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	limiter := store.NewRateLimiter(testsupport.NewTestRedis(t))
	s := notify.NewSender(pool, limiter, nil)

	p := notify.NewFakeProvider(notify.ChannelSMS, "only")
	p.FailNext(errors.New("网络不可达"))
	s.AddProvider(p)

	if err := s.Send(context.Background(), msg("13800138000")); err == nil {
		t.Fatal("全部供应商失败时应返回错误")
	}
}

// 发送记录必须落库，但绝不能写入验证码本身。
func TestSenderWritesLogWithoutCode(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	limiter := store.NewRateLimiter(testsupport.NewTestRedis(t))
	s := notify.NewSender(pool, limiter, nil)
	s.AddProvider(notify.NewFakeProvider(notify.ChannelSMS, "fake"))
	ctx := context.Background()

	if err := s.Send(ctx, msg("13800138000")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var (
		n        int
		provider string
	)
	if err := pool.QueryRow(ctx,
		`SELECT count(*), coalesce(max(provider), '') FROM notify_log WHERE target = $1`,
		"13800138000").Scan(&n, &provider); err != nil {
		t.Fatalf("查询发送记录: %v", err)
	}
	if n != 1 {
		t.Fatalf("发送记录数 = %d, want 1", n)
	}
	if provider != "fake" {
		t.Fatalf("provider = %q, want fake", provider)
	}

	// 全表扫一遍，确认没有任何列泄露了验证码。
	var leaked int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notify_log
		 WHERE channel LIKE '%123456%' OR target LIKE '%123456%'
		    OR template LIKE '%123456%' OR provider LIKE '%123456%'
		    OR error LIKE '%123456%'`).Scan(&leaked); err != nil {
		t.Fatalf("检查验证码泄露: %v", err)
	}
	if leaked != 0 {
		t.Fatal("发送记录中出现了验证码明文")
	}
}
```

测试文件需要 `import "time"`（`RateRule.Window` 用到）。

- [ ] **Step 11: 运行测试确认失败**

Run: `go test ./internal/notify/ -v`
Expected: 编译失败，`undefined: notify.NewSender`

- [ ] **Step 12: 实现 Sender 与 Provider 契约**

`internal/notify/notify.go`：

```go
// Package notify 是 fp 的通知中心。
//
// 它把「发什么」（Message）与「谁来发」（Provider）分开：
// 业务只声明模板与参数，具体走哪家供应商由配置决定，主供应商失败自动降级到下一家。
// 这是对 3s 中切换供应商靠改代码注释、模板 ID 硬编码在 if-else 里的直接修正。
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
)

// Channel 是通知通道。
type Channel string

const (
	ChannelSMS   Channel = "sms"
	ChannelEmail Channel = "email"
)

// Message 是一条待发送的通知。Template 是 fp 内部的模板 key，
// 由各 Provider 映射到自己那边的模板 ID。
type Message struct {
	Channel  Channel
	To       string
	Template string
	Params   map[string]string
}

// Provider 是一家通知供应商。
type Provider interface {
	// Name 是供应商标识，写入发送记录用于排障。
	Name() string
	// Channel 是该供应商负责的通道。
	Channel() Channel
	// ConfigSchema 描述该供应商的可配置项，供管理 UI 渲染表单。
	ConfigSchema() []domain.Field
	// Send 发送一条通知。失败时 Sender 会降级到下一家。
	Send(ctx context.Context, msg Message) error
}

// RateRule 是一条频率限制规则，按接收方（手机号 / 邮箱）计数。
type RateRule struct {
	Name   string
	Window time.Duration
	Limit  int
}

// DefaultSMSRateRules 是短信的默认频率限制，取自 3s 的 risk:smssend 策略。
func DefaultSMSRateRules() []RateRule {
	return []RateRule{
		{Name: "30s", Window: 30 * time.Second, Limit: 1},
		{Name: "1h", Window: time.Hour, Limit: 5},
		{Name: "1d", Window: 24 * time.Hour, Limit: 10},
	}
}

// Sender 编排一次发送：频率限制 → 按顺序尝试供应商 → 落发送记录。
type Sender struct {
	pool      *pgxpool.Pool
	limiter   *store.RateLimiter
	rules     []RateRule
	providers map[Channel][]Provider
}

// NewSender 构造 Sender。rules 为 nil 表示不做频率限制。
func NewSender(pool *pgxpool.Pool, limiter *store.RateLimiter, rules []RateRule) *Sender {
	return &Sender{
		pool:      pool,
		limiter:   limiter,
		rules:     rules,
		providers: make(map[Channel][]Provider),
	}
}

// AddProvider 追加一家供应商。同通道内按加入顺序尝试，先加入的是主供应商。
func (s *Sender) AddProvider(p Provider) {
	ch := p.Channel()
	s.providers[ch] = append(s.providers[ch], p)
}

// Send 发送一条通知。
//
// 频率超限返回 domain.ErrRateLimited；该通道没有可用供应商返回 domain.ErrNotFound；
// 全部供应商都失败时返回最后一次的错误。
func (s *Sender) Send(ctx context.Context, msg Message) error {
	if msg.To == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "接收方不能为空")
	}
	providers := s.providers[msg.Channel]
	if len(providers) == 0 {
		return domain.Errorf(domain.ErrNotFound, "通道 %s 没有配置供应商", msg.Channel)
	}

	// 频率限制先于供应商调用，避免被限流的请求也消耗供应商额度。
	for _, rule := range s.rules {
		key := fmt.Sprintf("notify:%s:%s:%s", msg.Channel, rule.Name, msg.To)
		allowed, retryAfter, err := s.limiter.Allow(ctx, key, rule.Window, rule.Limit)
		if err != nil {
			return err
		}
		if !allowed {
			return domain.Errorf(domain.ErrRateLimited,
				"发送过于频繁，请 %d 秒后重试", int(retryAfter.Seconds())+1)
		}
	}

	var lastErr error
	for _, p := range providers {
		err := p.Send(ctx, msg)
		s.writeLog(ctx, msg, p.Name(), err)
		if err == nil {
			return nil
		}
		lastErr = err
		slog.Warn("notify: 供应商发送失败，尝试降级",
			"provider", p.Name(), "channel", msg.Channel, "err", err)
	}
	return fmt.Errorf("notify: 全部供应商发送失败: %w", lastErr)
}

// writeLog 写发送记录。
//
// 只记录通道、接收方、模板 key、供应商与结果——**绝不写入 Params**，
// 因为验证码就在里面。记录失败不影响发送结果，只打日志。
func (s *Sender) writeLog(ctx context.Context, msg Message, provider string, sendErr error) {
	errText := ""
	if sendErr != nil {
		errText = sendErr.Error()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO notify_log (channel, target, template, provider, success, error)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		string(msg.Channel), msg.To, msg.Template, provider, sendErr == nil, errText)
	if err != nil {
		slog.Error("notify: 写入发送记录失败", "err", err)
	}
}
```

- [ ] **Step 13: 实现测试用 FakeProvider**

`internal/notify/fake.go`：

```go
package notify

import (
	"context"
	"sync"

	"github.com/basicfu/fp/internal/domain"
)

// FakeProvider 是内存中的假供应商，用于测试与本地开发。
// 它把发出的消息留在内存里，测试可以直接读出验证码，无需真实短信通道。
//
// 生产部署绝不应注册它。
type FakeProvider struct {
	name string
	ch   Channel

	mu       sync.Mutex
	sent     []Message
	failNext error
}

// NewFakeProvider 构造一个假供应商。
func NewFakeProvider(ch Channel, name string) *FakeProvider {
	return &FakeProvider{name: name, ch: ch}
}

// Name 实现 Provider。
func (p *FakeProvider) Name() string { return p.name }

// Channel 实现 Provider。
func (p *FakeProvider) Channel() Channel { return p.ch }

// ConfigSchema 实现 Provider。假供应商没有可配置项。
func (p *FakeProvider) ConfigSchema() []domain.Field { return nil }

// Send 实现 Provider。若已通过 FailNext 预置错误，则消费掉该错误并返回。
func (p *FakeProvider) Send(_ context.Context, msg Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.failNext != nil {
		err := p.failNext
		p.failNext = nil
		return err
	}
	p.sent = append(p.sent, msg)
	return nil
}

// FailNext 让下一次 Send 返回 err。
func (p *FakeProvider) FailNext(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failNext = err
}

// Sent 返回已成功发出的全部消息的副本。
func (p *FakeProvider) Sent() []Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Message, len(p.sent))
	copy(out, p.sent)
	return out
}

// LastParam 返回最后一条消息里指定参数的值，没有消息时返回空串。
func (p *FakeProvider) LastParam(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sent) == 0 {
		return ""
	}
	return p.sent[len(p.sent)-1].Params[key]
}
```

- [ ] **Step 14: 运行测试确认通过**

Run: `make test`
Expected: `internal/notify` 12 个测试 PASS（验证码 6 + Sender 6），其余包保持 PASS

- [ ] **Step 15: 提交**

```bash
git add internal
git commit -m "feat: 通知中心 Provider 抽象、供应商降级、频率限制与验证码收发"
```
