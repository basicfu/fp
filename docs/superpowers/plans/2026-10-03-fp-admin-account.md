# 管理端账号 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 管理端补齐默认密码提示、自助改账号（登录名 + 密码）、`fp reset-password` 恢复路径，并修掉"改名后重启会重新造出 `admin/admin`"的漏洞。

**Architecture:** 会话作废用单个 Redis 计数器 `fp:admin:epoch`（token payload 带 epoch，`Authenticate` 比对）。改账号与重置都在 `AdminService`；HTTP 层只加 `PUT /admin/api/me` 与登录响应里的 `defaultPassword`；`reset-password` 是 `cmd/fp` 的子命令，直接调 `AdminService.ResetPassword`。前端：登录页弹可关闭的 toast，右上角菜单加「修改密码」对话框。

**Tech Stack:** Go 1.25、pgx、go-redis、bcrypt；React 19、react-hook-form、zod、sonner、vitest。

**Spec:** `docs/superpowers/specs/2026-10-03-fp-admin-account-design.md`

## Global Constraints

- Go 测试一律用 `./scripts/test.sh [./pkg] [-run Name]`，不要直接 `go test`（需要 `.env.local` 里的 `FP_TEST_POSTGRES_URL` / `FP_TEST_REDIS_URL`，脚本带 `-p 1`）。**不要并行跑两个测试进程**：测试库共用，会互相 `TRUNCATE` / `FLUSHDB`，失败信号会指向错的地方。
- 前端测试 `cd web && npm test`；类型与构建检查 `cd web && npm run build`。
- `internal/service` 不得 import `net/http` / grpc / `internal/httpapi` / `sdk/gen`（`arch_test.go` 守着）。
- 新错误码登记在 `internal/domain/codes.go` 的常量与 `codeSentinels` 两处；HTTP 与 gRPC 都从哨兵推导状态码。
- 旧密码错误**不能**用 `ErrInvalidCredential`：它映射 401，前端 `api.ts` 对任何 401 都清登录态并跳登录页。
- 默认账号固定 `admin` / `admin`；`reset-password` 用户名固定 `admin`、密码随机 16 位（字符集去掉 `0 O 1 l I`）。
- 注释少而精，只写 Why。
- 每个 Task 结束 commit 并 push（用户全局规则：改完代码直接提交推送）。提交信息用中文 conventional commits，末尾按当前会话的署名要求加 `Co-Authored-By` 行。本机 git 报 "dubious ownership" 时用 `git -c safe.directory=D:/fp ...` 单次覆盖，不改全局配置。

---

### Task 1: AdminService —— 引导修复、会话纪元、改账号、重置

**Files:**
- Modify: `internal/domain/codes.go`（新增 `CodeAdminOldPasswordWrong`）
- Modify: `internal/service/admin.go`（整体替换，下面给全文）
- Test: `internal/service/admin_test.go`

**Interfaces:**
- Produces（Task 2、3 依赖）：
  - `service.DefaultAdminUsername = "admin"`、`service.DefaultAdminPassword = "admin"`
  - `type ChangeAccountInput struct{ Username, OldPassword, NewPassword string }`
  - `func (s *AdminService) ChangeAccount(ctx, id uuid.UUID, in ChangeAccountInput) (token, username string, err error)`
  - `func (s *AdminService) ResetPassword(ctx) (username, password string, err error)`
  - `domain.CodeAdminOldPasswordWrong = "ADMIN_OLD_PASSWORD_WRONG"`
  - `EnsureBootstrap` 语义变化：只在 `admin` 表为空时创建。`Login` / `Authenticate` / `Logout` 签名不变。

- [ ] **Step 1: 写失败的测试**

在 `internal/service/admin_test.go` 顶部 import 块补上 `"regexp"`、`"time"`、`"github.com/jackc/pgx/v5/pgxpool"`、`"github.com/redis/go-redis/v9"`，文件末尾追加：

```go
type adminTestEnv struct {
	svc  *service.AdminService
	pool *pgxpool.Pool
	rdb  *redis.Client
}

// 只调一次 NewTestDB / NewTestRedis：它们每次调用都会清库，二次调用会冲掉前面造的数据。
func newAdminTestEnv(t *testing.T) adminTestEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	return adminTestEnv{svc: service.NewAdminService(pool, rdb), pool: pool, rdb: rdb}
}

func (e adminTestEnv) bootstrap(t *testing.T, username, password string) {
	t.Helper()
	if err := e.svc.EnsureBootstrap(context.Background(), username, password); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
}

// loginAs 登录并返回 token 与管理员 ID。
func (e adminTestEnv) loginAs(t *testing.T, username, password string) (string, uuid.UUID) {
	t.Helper()
	token, err := e.svc.Login(context.Background(), username, password)
	if err != nil {
		t.Fatalf("Login(%q): %v", username, err)
	}
	id, _, err := e.svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	return token, id
}

func (e adminTestEnv) adminRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM admin`).Scan(&n); err != nil {
		t.Fatalf("统计 admin 行数: %v", err)
	}
	return n
}

func adminErrCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// 登录名可改之后，"用户名冲突"不再能代表"管理员已存在"：改名后重启，
// admin 这个名字空出来，旧实现会再插入一个已知密码的 admin/admin。
func TestEnsureBootstrapDoesNotRecreateDefaultAdminAfterRename(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "admin")
	_, id := e.loginAs(t, "admin", "admin")
	if _, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "root", OldPassword: "admin"}); err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}

	e.bootstrap(t, "admin", "admin") // 模拟重启
	if n := e.adminRows(t); n != 1 {
		t.Fatalf("admin 行数 = %d, want 1", n)
	}
	if _, err := e.svc.Login(ctx, "admin", "admin"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("改名后 admin/admin 不该再能登录, err = %v", err)
	}
}

func TestAuthenticateRejectsLegacyTokenWithoutEpoch(t *testing.T) {
	e := newAdminTestEnv(t)
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")
	// 升级前签发的 token payload 只有 id|username。
	if err := e.rdb.Set(context.Background(), "fp:admin:tok:legacy", id.String()+"|admin", time.Hour).Err(); err != nil {
		t.Fatalf("写旧格式 token: %v", err)
	}
	if _, _, err := e.svc.Authenticate(context.Background(), "legacy"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestChangeAccountRejectsWrongOldPassword(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	_, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{
		Username: "admin", OldPassword: "nope", NewPassword: "new-password",
	})
	if !errors.Is(err, domain.ErrInvalidArgument) || adminErrCode(err) != domain.CodeAdminOldPasswordWrong {
		t.Fatalf("err = %v (code %q), want ErrInvalidArgument / ADMIN_OLD_PASSWORD_WRONG", err, adminErrCode(err))
	}
	if _, err := e.svc.Login(ctx, "admin", "secret123456"); err != nil {
		t.Fatalf("失败的修改不该动旧密码: %v", err)
	}
}

func TestChangeAccountChangesPasswordAndRevokesOtherSessions(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	other, id := e.loginAs(t, "admin", "secret123456")
	current, _ := e.loginAs(t, "admin", "secret123456")

	newToken, username, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{
		Username: "  boss ", OldPassword: "secret123456", NewPassword: "brand-new-pass",
	})
	if err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	if username != "boss" {
		t.Fatalf("username = %q, want boss（应去掉首尾空白）", username)
	}

	for name, tok := range map[string]string{"其他会话": other, "发起修改的旧会话": current} {
		if _, _, err := e.svc.Authenticate(ctx, tok); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("%s 应已失效, err = %v", name, err)
		}
	}
	if _, name, err := e.svc.Authenticate(ctx, newToken); err != nil || name != "boss" {
		t.Fatalf("新签发的 token: name = %q, err = %v", name, err)
	}
	if _, err := e.svc.Login(ctx, "boss", "brand-new-pass"); err != nil {
		t.Fatalf("新账号密码应能登录: %v", err)
	}
	for _, c := range [][2]string{{"boss", "secret123456"}, {"admin", "brand-new-pass"}} {
		if _, err := e.svc.Login(ctx, c[0], c[1]); !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("Login(%q, %q) err = %v, want ErrInvalidCredential", c[0], c[1], err)
		}
	}
}

func TestChangeAccountWithoutNewPasswordOnlyRenames(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	if _, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "root", OldPassword: "secret123456"}); err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	if _, err := e.svc.Login(ctx, "root", "secret123456"); err != nil {
		t.Fatalf("只改登录名时密码应保持不变: %v", err)
	}
}

func TestChangeAccountValidatesInput(t *testing.T) {
	e := newAdminTestEnv(t)
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	cases := []struct {
		name string
		in   service.ChangeAccountInput
	}{
		{"登录名为空白", service.ChangeAccountInput{Username: "  ", OldPassword: "secret123456"}},
		{"登录名超过 64 字符", service.ChangeAccountInput{Username: strings.Repeat("a", 65), OldPassword: "secret123456"}},
		{"新密码超过 72 字节", service.ChangeAccountInput{Username: "admin", OldPassword: "secret123456", NewPassword: strings.Repeat("a", 73)}},
	}
	for _, c := range cases {
		if _, _, err := e.svc.ChangeAccount(context.Background(), id, c.in); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want ErrInvalidArgument", c.name, err)
		}
	}
}

// 会话 payload 是 id|username|epoch，登录名里的 | 不能把它切歪。
func TestChangeAccountAllowsPipeInUsername(t *testing.T) {
	e := newAdminTestEnv(t)
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")

	token, _, err := e.svc.ChangeAccount(context.Background(), id, service.ChangeAccountInput{Username: "a|b", OldPassword: "secret123456"})
	if err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	if _, name, err := e.svc.Authenticate(context.Background(), token); err != nil || name != "a|b" {
		t.Fatalf("name = %q, err = %v, want a|b", name, err)
	}
}

var generatedPasswordRE = regexp.MustCompile(`^[a-km-zA-HJ-NP-Z2-9]{16}$`)

func TestResetPasswordRestoresAdminWithRandomPassword(t *testing.T) {
	e := newAdminTestEnv(t)
	ctx := context.Background()
	e.bootstrap(t, "admin", "secret123456")
	_, id := e.loginAs(t, "admin", "secret123456")
	if _, _, err := e.svc.ChangeAccount(ctx, id, service.ChangeAccountInput{Username: "root", OldPassword: "secret123456"}); err != nil {
		t.Fatalf("ChangeAccount: %v", err)
	}
	staleToken, _ := e.loginAs(t, "root", "secret123456") // 重置前签发的会话，重置后必须失效
	if _, err := e.pool.Exec(ctx, `UPDATE admin SET status = 'DISABLED'`); err != nil {
		t.Fatalf("停用管理员: %v", err)
	}

	username, password, err := e.svc.ResetPassword(ctx)
	if err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if username != "admin" {
		t.Fatalf("username = %q, want admin", username)
	}
	if !generatedPasswordRE.MatchString(password) {
		t.Fatalf("password = %q，应为 16 位且不含 0 O 1 l I", password)
	}
	if _, err := e.svc.Login(ctx, "admin", password); err != nil {
		t.Fatalf("打印出来的账号密码应能登录（含被停用的账号被重新启用）: %v", err)
	}
	if _, err := e.svc.Login(ctx, "root", password); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("用户名应已恢复成 admin, err = %v", err)
	}
	if n := e.adminRows(t); n != 1 {
		t.Fatalf("admin 行数 = %d, want 1", n)
	}
	if _, _, err := e.svc.Authenticate(ctx, staleToken); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("重置前签发的会话应已失效, err = %v", err)
	}
}

func TestResetPasswordCreatesAdminWhenTableEmpty(t *testing.T) {
	e := newAdminTestEnv(t)
	_, password, err := e.svc.ResetPassword(context.Background())
	if err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if n := e.adminRows(t); n != 1 {
		t.Fatalf("admin 行数 = %d, want 1", n)
	}
	if _, err := e.svc.Login(context.Background(), "admin", password); err != nil {
		t.Fatalf("Login: %v", err)
	}
}

func TestResetPasswordIsRandomEachTime(t *testing.T) {
	e := newAdminTestEnv(t)
	_, first, err := e.svc.ResetPassword(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := e.svc.ResetPassword(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("两次重置得到同一个密码 %q", first)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/service -run "TestEnsureBootstrap|TestChangeAccount|TestResetPassword|TestAuthenticateRejectsLegacy"`
Expected: 编译失败，`undefined: service.ChangeAccountInput` / `ResetPassword` / `domain.CodeAdminOldPasswordWrong`。

- [ ] **Step 3: 登记错误码**

`internal/domain/codes.go`：在 `CodeAdminSessionInvalid = ...` 后面加常量，并在 `codeSentinels` 里加映射：

```go
	// CodeAdminOldPasswordWrong 是改账号时旧密码不对。映射 ErrInvalidArgument（400）
	// 而不是 ErrInvalidCredential（401）：前端对任何 401 都清登录态跳登录页，
	// 输错旧密码不该把人踢出去。
	CodeAdminOldPasswordWrong = "ADMIN_OLD_PASSWORD_WRONG"
```

```go
	CodeAdminOldPasswordWrong:  ErrInvalidArgument,
```

- [ ] **Step 4: 整体替换 `internal/service/admin.go`**

```go
// Package service 承载 fp 的业务逻辑。本包不依赖任何传输层类型。
package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

// adminEpochKey 是管理端会话的全局纪元。改账号与重置密码时 INCR；token payload
// 带着签发时的纪元，Authenticate 比对不一致即失效。不给 token 按管理员建索引、
// 也不 SCAN：共享 Redis 里还有终端用户会话，扫前缀要遍历整个键空间。
const adminEpochKey = "fp:admin:epoch"

// bcryptCost 是密码哈希代价。10 是 bcrypt 的常用生产取值。
const bcryptCost = 10

const (
	// DefaultAdminUsername / DefaultAdminPassword 是空库首次启动时的内置账号，
	// 登录响应据此判断"仍在使用默认密码"。
	DefaultAdminUsername = "admin"
	DefaultAdminPassword = "admin"

	maxAdminUsernameRunes = 64
	generatedPasswordLen  = 16
	// 去掉易混字符 0 O 1 l I：口述、抄写时不会认错。
	generatedPasswordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

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

// EnsureBootstrap 在 admin 表为空时创建引导管理员。
// username 或 password 为空时静默跳过；表里已有任何管理员时什么都不做。
//
// 判断依据必须是"表为空"而不是"用户名冲突"：登录名可改之后，管理员把 admin
// 改成 root，重启时 admin 这个名字不再冲突，按冲突判断会再造出一个已知密码的账号。
func (s *AdminService) EnsureBootstrap(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return nil
	}
	// 同 UserService.SetPassword 的理由：bcrypt 超过 72 字节直接报错，
	// 不拦住就会在启动时炸出一个不知所云的 bcrypt 错误。
	if len(password) > maxPasswordBytes {
		return domain.Failf(domain.ErrInvalidArgument, domain.CodePasswordTooLong,
			"引导管理员密码过长（超过 %d 字节，约 %d 个汉字）", maxPasswordBytes, maxPasswordBytes/3)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("service: 计算管理员密码哈希: %w", err)
	}
	// 末尾的 ON CONFLICT 兜住两个实例同时首次启动的竞态。
	_, err = s.pool.Exec(ctx, `
		INSERT INTO admin (username, password_hash, display_name)
		SELECT $1::text, $2::text, $1::text WHERE NOT EXISTS (SELECT 1 FROM admin)
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
		return "", domain.Failf(domain.ErrInvalidCredential, domain.CodeAdminCredentialInvalid, "用户名或密码不正确")
	}
	if err != nil {
		return "", fmt.Errorf("service: 查询管理员: %w", err)
	}
	if status != "ACTIVE" {
		return "", domain.Failf(domain.ErrForbidden, domain.CodeAdminDisabled, "管理员账号已停用")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return "", domain.Failf(domain.ErrInvalidCredential, domain.CodeAdminCredentialInvalid, "用户名或密码不正确")
	}
	return s.issueToken(ctx, id, username)
}

// issueToken 签发一个带当前纪元的会话 token。
func (s *AdminService) issueToken(ctx context.Context, id uuid.UUID, username string) (string, error) {
	epoch, err := s.currentEpoch(ctx)
	if err != nil {
		return "", err
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	payload := id.String() + "|" + username + "|" + strconv.FormatInt(epoch, 10)
	if err := s.rdb.Set(ctx, adminTokenPrefix+token, payload, adminSessionTTL).Err(); err != nil {
		return "", fmt.Errorf("service: 写入管理端会话: %w", err)
	}
	return token, nil
}

// Authenticate 校验管理端 token，返回管理员 ID 与用户名，并顺延会话有效期。
func (s *AdminService) Authenticate(ctx context.Context, token string) (uuid.UUID, string, error) {
	invalid := func() (uuid.UUID, string, error) {
		return uuid.Nil, "", domain.Failf(domain.ErrUnauthorized, domain.CodeAdminSessionInvalid, "管理端登录已过期，请重新登录")
	}
	if token == "" {
		return invalid()
	}
	key := adminTokenPrefix + token
	payload, err := s.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return invalid()
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("service: 读取管理端会话: %w", err)
	}

	idStr, rest, ok := strings.Cut(payload, "|")
	if !ok {
		return invalid()
	}
	// 登录名可以含 "|"，所以纪元从最后一个 "|" 切。升级前签发的旧格式
	// （id|username，没有纪元段）切不出来，按失效处理，管理员重新登录一次。
	i := strings.LastIndex(rest, "|")
	if i < 0 {
		return invalid()
	}
	username := rest[:i]
	tokenEpoch, err := strconv.ParseInt(rest[i+1:], 10, 64)
	if err != nil {
		return invalid()
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return invalid()
	}
	epoch, err := s.currentEpoch(ctx)
	if err != nil {
		return uuid.Nil, "", err
	}
	if tokenEpoch != epoch {
		return invalid()
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

// ChangeAccountInput 是改账号的入参。NewPassword 为空表示不改密码。
type ChangeAccountInput struct {
	Username    string
	OldPassword string
	NewPassword string
}

// ChangeAccount 修改登录名与（可选的）密码。旧密码必须匹配。
//
// 成功后作废全部管理端会话（含发起修改的这一个），并为调用方重新签发一个
// 带新纪元与新登录名的 token——其他浏览器被踢下线，当前浏览器保持登录。
func (s *AdminService) ChangeAccount(ctx context.Context, id uuid.UUID, in ChangeAccountInput) (token, username string, err error) {
	username = strings.TrimSpace(in.Username)
	if n := utf8.RuneCountInString(username); n < 1 || n > maxAdminUsernameRunes {
		return "", "", domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
			"登录名需为 1–%d 个字符", maxAdminUsernameRunes)
	}
	if len(in.NewPassword) > maxPasswordBytes {
		return "", "", domain.Failf(domain.ErrInvalidArgument, domain.CodePasswordTooLong,
			"新密码过长（超过 %d 字节，约 %d 个汉字）", maxPasswordBytes, maxPasswordBytes/3)
	}

	var hash string
	err = s.pool.QueryRow(ctx, `SELECT password_hash FROM admin WHERE id = $1`, id).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", domain.Failf(domain.ErrUnauthorized, domain.CodeAdminSessionInvalid, "管理端登录已过期，请重新登录")
	}
	if err != nil {
		return "", "", fmt.Errorf("service: 查询管理员: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.OldPassword)) != nil {
		return "", "", domain.Fail(domain.ErrInvalidArgument, domain.CodeAdminOldPasswordWrong, "旧密码不正确")
	}

	newHash := hash
	if in.NewPassword != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcryptCost)
		if err != nil {
			return "", "", fmt.Errorf("service: 计算管理员密码哈希: %w", err)
		}
		newHash = string(h)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE admin SET username = $2, password_hash = $3, display_name = $2, updated_at = now()
		WHERE id = $1`, id, username, newHash)
	if isUniqueViolation(err) {
		return "", "", domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "登录名已被占用")
	}
	if err != nil {
		return "", "", fmt.Errorf("service: 更新管理员: %w", err)
	}
	if err := s.bumpEpoch(ctx); err != nil {
		return "", "", err
	}
	token, err = s.issueToken(ctx, id, username)
	if err != nil {
		return "", "", err
	}
	return token, username, nil
}

// ResetPassword 把唯一的管理员恢复成用户名 admin + 随机密码，并作废全部会话。
// 表为空时创建。供 `fp reset-password` 使用：这是唯一的重置路径，不依赖通知渠道。
//
// 返回的明文密码只有这一次，库里只存 bcrypt 哈希。
func (s *AdminService) ResetPassword(ctx context.Context) (username, password string, err error) {
	password, err = randomPassword(generatedPasswordLen)
	if err != nil {
		return "", "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", "", fmt.Errorf("service: 计算管理员密码哈希: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", fmt.Errorf("service: 开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM admin ORDER BY created_at, id LIMIT 1 FOR UPDATE`).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err = tx.Exec(ctx, `
			INSERT INTO admin (username, password_hash, display_name) VALUES ($1, $2, $1)`,
			DefaultAdminUsername, string(hash))
	case err == nil:
		_, err = tx.Exec(ctx, `
			UPDATE admin SET username = $2, password_hash = $3, display_name = $2,
			                 status = 'ACTIVE', updated_at = now()
			WHERE id = $1`, id, DefaultAdminUsername, string(hash))
	}
	if err != nil {
		return "", "", fmt.Errorf("service: 重置管理员: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", fmt.Errorf("service: 提交重置: %w", err)
	}
	if err := s.bumpEpoch(ctx); err != nil {
		return "", "", err
	}
	return DefaultAdminUsername, password, nil
}

func (s *AdminService) currentEpoch(ctx context.Context) (int64, error) {
	v, err := s.rdb.Get(ctx, adminEpochKey).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("service: 读取管理端会话纪元: %w", err)
	}
	return v, nil
}

func (s *AdminService) bumpEpoch(ctx context.Context) error {
	if err := s.rdb.Incr(ctx, adminEpochKey).Err(); err != nil {
		return fmt.Errorf("service: 作废管理端会话: %w", err)
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

// randomPassword 从去掉易混字符的字母表里取 n 位密码学随机字符。
func randomPassword(n int) (string, error) {
	max := big.NewInt(int64(len(generatedPasswordAlphabet)))
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("service: 生成随机密码: %w", err)
		}
		b[i] = generatedPasswordAlphabet[idx.Int64()]
	}
	return string(b), nil
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/service ./internal/domain`
Expected: PASS（含原有的 `TestEnsureBootstrapCreatesAdminOnce` 等）。

- [ ] **Step 6: Commit**

```bash
git add internal/domain/codes.go internal/service/admin.go internal/service/admin_test.go
git commit -m "feat(admin): 管理端会话纪元、改账号与重置密码；引导管理员只在表为空时创建"
git push
```

---

### Task 2: HTTP —— 登录响应带 `defaultPassword`，新增 `PUT /admin/api/me`

**Files:**
- Modify: `internal/httpapi/admin.go`
- Modify: `internal/httpapi/router.go`（在 `r.Get("/me", ah.me)` 下一行加路由）
- Test: `internal/httpapi/admin_test.go`

**Interfaces:**
- Consumes: Task 1 的 `AdminService.ChangeAccount`、`service.DefaultAdminPassword`。
- Produces（Task 4 前端依赖）：
  - `POST /admin/api/login` 响应 `{ token, username, defaultPassword }`
  - `PUT /admin/api/me`，请求 `{ username, oldPassword, newPassword }`，成功 200 返回 `{ token, username }` 并写新 cookie；旧密码错返回 400 + `code: "ADMIN_OLD_PASSWORD_WRONG"`

- [ ] **Step 1: 写失败的测试**

`internal/httpapi/admin_test.go` import 块补 `"context"`、`"github.com/basicfu/fp/internal/testsupport"`，文件末尾追加：

```go
func TestAdminLoginReportsDefaultPassword(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	svc := service.NewAdminService(pool, rdb)
	if err := svc.EnsureBootstrap(context.Background(), "admin", "admin"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	h := httpapi.NewRouter(httpapi.Deps{Admin: svc})

	rec := do(t, h, "", http.MethodPost, "/admin/api/login", `{"username":"admin","password":"admin"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"defaultPassword":true`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminLoginDoesNotReportDefaultPasswordForCustomPassword(t *testing.T) {
	h, _, _ := newAdminEnv(t) // 引导密码是 secret123456
	rec := do(t, h, "", http.MethodPost, "/admin/api/login", `{"username":"admin","password":"secret123456"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"defaultPassword":false`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestChangeAccountKeepsCurrentSessionAndRevokesOthers(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	other, err := deps.Admin.Login(context.Background(), "admin", "secret123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	rec := do(t, h, token, http.MethodPut, "/admin/api/me",
		`{"username":"boss","oldPassword":"secret123456","newPassword":"brand-new-pass"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token    string `json:"token"`
		Username string `json:"username"`
	}
	decode(t, rec, &out)
	if out.Username != "boss" || out.Token == "" {
		t.Fatalf("响应 = %+v", out)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "fp_admin" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != out.Token {
		t.Fatalf("应写入换新后的 fp_admin cookie, got %+v", cookie)
	}

	if me := do(t, h, out.Token, http.MethodGet, "/admin/api/me", ""); me.Code != http.StatusOK ||
		!strings.Contains(me.Body.String(), `"username":"boss"`) {
		t.Fatalf("新会话 /me: %d %s", me.Code, me.Body.String())
	}
	for name, old := range map[string]string{"发起修改的旧会话": token, "其他会话": other} {
		if rec := do(t, h, old, http.MethodGet, "/admin/api/me", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 应已失效, status = %d", name, rec.Code)
		}
	}
}

// 【辨别力】旧密码错必须是 400 而不是 401：前端对任何 401 都清登录态并跳登录页。
func TestChangeAccountWrongOldPasswordIs400AndKeepsSession(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	rec := do(t, h, token, http.MethodPut, "/admin/api/me",
		`{"username":"admin","oldPassword":"nope","newPassword":"x"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ADMIN_OLD_PASSWORD_WRONG") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if me := do(t, h, token, http.MethodGet, "/admin/api/me", ""); me.Code != http.StatusOK {
		t.Fatalf("失败的修改不该让会话失效, status = %d", me.Code)
	}
}

func TestChangeAccountRequiresAuthAndRejectsUnknownFields(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	if rec := do(t, h, "", http.MethodPut, "/admin/api/me", `{"username":"a","oldPassword":"b"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 status = %d, want 401", rec.Code)
	}
	if rec := do(t, h, token, http.MethodPut, "/admin/api/me", `{"username":"a","oldPassword":"b","extra":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("未知字段 status = %d, want 400", rec.Code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/httpapi -run "TestAdminLogin|TestChangeAccount"`
Expected: FAIL（登录响应没有 `defaultPassword`；`PUT /admin/api/me` 返回 404/405）。

- [ ] **Step 3: 实现**

`internal/httpapi/admin.go`：`adminLoginResponse` 加字段、`login` 里填值、新增 `changeAccount`：

```go
type adminLoginResponse struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	// DefaultPassword 为 true 表示这次登录用的仍是内置默认密码，前端据此弹一条
	// 可忽略的提示。拿登录时的明文判断，不需要库里加标志位，也不会过期失真。
	DefaultPassword bool `json:"defaultPassword"`
}
```

```go
	writeJSON(w, http.StatusOK, adminLoginResponse{
		Token:           token,
		Username:        req.Username,
		DefaultPassword: req.Password == service.DefaultAdminPassword,
	})
```

```go
type adminChangeAccountRequest struct {
	Username    string `json:"username"`
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

func (h *adminHandler) changeAccount(w http.ResponseWriter, r *http.Request) {
	var req adminChangeAccountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	token, username, err := h.svc.ChangeAccount(r.Context(), adminIDFrom(r.Context()), service.ChangeAccountInput{
		Username: req.Username, OldPassword: req.OldPassword, NewPassword: req.NewPassword,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, h.sessionCookie(token, 0))
	writeJSON(w, http.StatusOK, adminLoginResponse{Token: token, Username: username})
}
```

`internal/httpapi/router.go`，在 `r.Get("/me", ah.me)` 后一行加：

```go
			r.Put("/me", ah.changeAccount)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/httpapi ./internal/grpcapi`
Expected: PASS（含 `TestCodeRegistryMatchesTransports` 之类校验新错误码两端一致的测试）。

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/admin.go internal/httpapi/router.go internal/httpapi/admin_test.go
git commit -m "feat(admin): 登录响应带 defaultPassword，新增 PUT /admin/api/me 改账号"
git push
```

---

### Task 3: `fp reset-password` 子命令 + 文档

**Files:**
- Create: `cmd/fp/subcommand.go`
- Modify: `cmd/fp/main.go`（`main()` 开头加分发）
- Test: `cmd/fp/main_test.go`
- Modify: `docs/console.md`

**Interfaces:**
- Consumes: Task 1 的 `AdminService.ResetPassword`。
- Produces: `fp reset-password`（容器内是 `./bootstrap reset-password`）；其他参数退出码 2。

- [ ] **Step 1: 写失败的测试**

`cmd/fp/main_test.go` import 块补 `"bytes"`、`"context"`、`"regexp"`、`"github.com/basicfu/fp/internal/service"`、`"github.com/basicfu/fp/internal/testsupport"`，文件末尾追加：

```go
func TestRunSubcommandRejectsUnknownArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSubcommand([]string{"reset-pasword"}, &stdout, &stderr); code != 2 {
		t.Fatalf("退出码 = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "用法") || stdout.Len() != 0 {
		t.Fatalf("stderr = %q, stdout = %q", stderr.String(), stdout.String())
	}
}

// 登录名被改过的实例：重置必须把用户名一并恢复，打印出来的账号密码必须真的能登录。
func TestResetPasswordPrintsCredentialsThatWork(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	svc := service.NewAdminService(pool, rdb)
	ctx := context.Background()
	if err := svc.EnsureBootstrap(ctx, "root", "some-password"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	var out bytes.Buffer
	if err := resetPassword(ctx, svc, &out); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}
	m := regexp.MustCompile(`用户名：(\S+)\n\s*密码：(\S+)`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("输出里找不到用户名与密码: %q", out.String())
	}
	if m[1] != "admin" {
		t.Fatalf("用户名 = %q, want admin", m[1])
	}
	if _, err := svc.Login(ctx, m[1], m[2]); err != nil {
		t.Fatalf("打印出来的账号密码登录不了: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./cmd/fp`
Expected: 编译失败，`undefined: runSubcommand` / `resetPassword`。

- [ ] **Step 3: 实现**

新建 `cmd/fp/subcommand.go`：

```go
package main

import (
	"context"
	"fmt"
	"io"

	"github.com/basicfu/fp/internal/config"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
)

const usage = `用法：
  fp                  启动服务
  fp reset-password   重置管理员账号：用户名恢复为 admin，密码随机生成并打印，作废全部管理端会话
`

// runSubcommand 返回进程退出码。多余参数一律报错而不是静默忽略：
// 敲错子命令却把服务起起来，比报错更糟。
func runSubcommand(args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case "reset-password":
		if err := runResetPassword(context.Background(), stdout); err != nil {
			fmt.Fprintf(stderr, "重置失败：%v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "未知参数 %q\n\n%s", args[0], usage)
		return 2
	}
}

// runResetPassword 只依赖 Postgres 与 Redis（与服务进程同一组环境变量），不读系统配置：
// 系统配置 YAML 写坏了也不该妨碍找回账号。先 Migrate，保证新版本二进制对旧库也能跑。
func runResetPassword(ctx context.Context, out io.Writer) error {
	pgURL, err := config.RequireEnv("FP_POSTGRES_URL")
	if err != nil {
		return err
	}
	redisURL, err := config.RequireEnv("FP_REDIS_URL")
	if err != nil {
		return err
	}
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
	return resetPassword(ctx, service.NewAdminService(pool, rdb), out)
}

func resetPassword(ctx context.Context, svc *service.AdminService, out io.Writer) error {
	username, password, err := svc.ResetPassword(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "管理员账号已重置，所有管理端会话已作废：\n  用户名：%s\n  密码：%s\n", username, password)
	return err
}
```

`cmd/fp/main.go`，`main()` 改为：

```go
func main() {
	if len(os.Args) > 1 {
		os.Exit(runSubcommand(os.Args[1:], os.Stdout, os.Stderr))
	}
	if err := run(); err != nil {
		slog.Error("fp 启动失败", "err", err)
		os.Exit(1)
	}
}
```

`docs/console.md`：把"**控制台的登录账号**：……得直接改库或在控制台里改密码。"这一段整段替换为：

```
**控制台的登录账号**：全新库首次启动时创建 `admin` / `admin`（系统配置里
`bootstrap_admin` 两项都为空时的内置默认值）。账号存在数据库 `admin` 表里，
之后改系统配置不会改已有账号。登录名与密码都可以在控制台右上角「修改密码」
里改；登录时若仍是默认密码，会弹一条可忽略的提示。

**忘记密码**：只有一条路——在服务器上执行 `./fp reset-password`。它把用户名
恢复成 `admin`、密码重置为随机值并打印出来（只显示这一次），同时作废全部
管理端会话。容器里二进制叫 `bootstrap`：

    docker exec <容器名> ./bootstrap reset-password
```

- [ ] **Step 4: 跑测试确认通过，并手动验证命令**

Run: `./scripts/test.sh ./cmd/fp`
Expected: PASS。

再手动验一次真命令（需要 `.env.local` 里的 `FP_POSTGRES_URL` / `FP_REDIS_URL`，用开发库）：

Run: `set -a; . ./.env.local; set +a; go run ./cmd/fp reset-password`
Expected: 输出 `用户名：admin` 和一个 16 位密码；再跑 `go run ./cmd/fp bogus` 退出码 2 并打印用法。

- [ ] **Step 5: Commit**

```bash
git add cmd/fp/subcommand.go cmd/fp/main.go cmd/fp/main_test.go docs/console.md
git commit -m "feat(admin): fp reset-password 子命令，重置为 admin + 随机密码"
git push
```

---

### Task 4: 前端 —— 默认密码提示与「修改密码」对话框

**Files:**
- Modify: `web/src/lib/auth.tsx`、`web/src/pages/Login.tsx`、`web/src/components/Layout.tsx`
- Create: `web/src/components/ChangeAccountDialog.tsx`
- Test: `web/src/lib/auth.test.tsx`、`web/src/components/Layout.test.tsx`、新建 `web/src/pages/Login.test.tsx`、新建 `web/src/components/ChangeAccountDialog.test.tsx`

**Interfaces:**
- Consumes: Task 2 的 `POST /login`（带 `defaultPassword`）与 `PUT /me`。
- Produces: `useAuth()` 新增 `renameUser(username)`、`accountDialogOpen`、`setAccountDialogOpen(open)`；`login()` 现在 resolve `{ defaultPassword: boolean }`。

- [ ] **Step 1: 写失败的测试**

新建 `web/src/components/ChangeAccountDialog.test.tsx`：

```tsx
import { test, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { ChangeAccountDialog } from './ChangeAccountDialog'

const mocks = vi.hoisted(() => ({
  setAccountDialogOpen: vi.fn(),
  renameUser: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))
vi.mock('@/lib/auth', () => ({
  useAuth: () => ({
    username: 'admin',
    accountDialogOpen: true,
    setAccountDialogOpen: mocks.setAccountDialogOpen,
    renameUser: mocks.renameUser,
  }),
}))
vi.mock('sonner', () => ({ toast: { success: mocks.success, error: mocks.error } }))

beforeEach(() => vi.clearAllMocks())
afterEach(() => vi.unstubAllGlobals())

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

test('登录名预填当前值', () => {
  render(<ChangeAccountDialog />)
  expect((screen.getByLabelText('登录名') as HTMLInputElement).value).toBe('admin')
})

test('两次新密码不一致时不发请求并提示', async () => {
  const fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
  render(<ChangeAccountDialog />)
  fill('旧密码', 'admin')
  fill('新密码', 'aaa')
  fill('确认新密码', 'bbb')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.error).toHaveBeenCalled())
  expect(String(mocks.error.mock.calls[0][0])).toContain('不一致')
  expect(fetchMock).not.toHaveBeenCalled()
})

test('提交后同步登录名并关闭对话框', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ token: 't', username: 'boss' }), { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  render(<ChangeAccountDialog />)
  fill('登录名', 'boss')
  fill('旧密码', 'admin')
  fill('新密码', 'n3w-pass')
  fill('确认新密码', 'n3w-pass')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.renameUser).toHaveBeenCalledWith('boss'))
  const [url, init] = fetchMock.mock.calls[0]
  expect(url).toBe('/admin/api/me')
  expect(init.method).toBe('PUT')
  expect(JSON.parse(String(init.body))).toEqual({ username: 'boss', oldPassword: 'admin', newPassword: 'n3w-pass' })
  expect(mocks.setAccountDialogOpen).toHaveBeenCalledWith(false)
  expect(mocks.success).toHaveBeenCalled()
})

test('新密码留空时只改登录名', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ token: 't', username: 'root' }), { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  render(<ChangeAccountDialog />)
  fill('登录名', 'root')
  fill('旧密码', 'admin')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(fetchMock).toHaveBeenCalled())
  expect(JSON.parse(String(fetchMock.mock.calls[0][1].body))).toEqual({ username: 'root', oldPassword: 'admin', newPassword: '' })
})

// 旧密码错是 400，对话框必须留着让人重填；若被当成 401 全局处理会被踢回登录页。
test('旧密码错误时提示原因并保持对话框打开', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ code: 'ADMIN_OLD_PASSWORD_WRONG', msg: '旧密码不正确' }), { status: 400 }),
  ))
  render(<ChangeAccountDialog />)
  fill('旧密码', 'nope')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith('旧密码不正确'))
  expect(mocks.renameUser).not.toHaveBeenCalled()
  expect(mocks.setAccountDialogOpen).not.toHaveBeenCalledWith(false)
})
```

新建 `web/src/pages/Login.test.tsx`：

```tsx
import { test, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import Login from './Login'

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
  setAccountDialogOpen: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
}))
vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ login: mocks.login, setAccountDialogOpen: mocks.setAccountDialogOpen }),
}))
vi.mock('sonner', () => ({ toast: { warning: mocks.warning, error: mocks.error } }))

beforeEach(() => vi.clearAllMocks())

function submit() {
  render(<Login />)
  fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'admin' } })
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'admin' } })
  fireEvent.click(screen.getByRole('button', { name: '登录' }))
}

test('仍是默认密码时弹出可忽略的提示，"去修改"打开改密对话框', async () => {
  mocks.login.mockResolvedValue({ defaultPassword: true })
  submit()
  await waitFor(() => expect(mocks.warning).toHaveBeenCalled())
  const [message, options] = mocks.warning.mock.calls[0]
  expect(String(message)).toContain('默认密码')
  expect(options.action.label).toBe('去修改')
  options.action.onClick()
  expect(mocks.setAccountDialogOpen).toHaveBeenCalledWith(true)
})

test('不是默认密码时不弹提示', async () => {
  mocks.login.mockResolvedValue({ defaultPassword: false })
  submit()
  await waitFor(() => expect(mocks.login).toHaveBeenCalled())
  expect(mocks.warning).not.toHaveBeenCalled()
})
```

`web/src/lib/auth.test.tsx` 末尾追加：

```tsx
function LoginProbe({ holder }: { holder: { login?: (u: string, p: string) => Promise<{ defaultPassword: boolean }> } }) {
  const { status, username, login } = useAuth()
  useEffect(() => {
    holder.login = login
  }, [holder, login])
  return <div data-testid="probe">{status}:{username ?? '-'}</div>
}

test('login 返回后端给的 defaultPassword 并进入 authed', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ msg: '未登录' }), { status: 401 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ token: 't', username: 'admin', defaultPassword: true }), { status: 200 })),
  )
  const holder: { login?: (u: string, p: string) => Promise<{ defaultPassword: boolean }> } = {}
  render(<AuthProvider><LoginProbe holder={holder} /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('anon:-'))

  await expect(holder.login!('admin', 'admin')).resolves.toEqual({ defaultPassword: true })
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('authed:admin'))
})
```

`web/src/components/Layout.test.tsx`：把文件顶部的 `useAuth` mock 改成

```tsx
const authMock = vi.hoisted(() => ({
  username: 'alice',
  logout: vi.fn(),
  accountDialogOpen: false,
  setAccountDialogOpen: vi.fn(),
  renameUser: vi.fn(),
}))
vi.mock('@/lib/auth', () => ({ useAuth: () => authMock }))
```

并在文件末尾追加：

```tsx
test('用户菜单里有「修改密码」，点它会打开改密对话框', async () => {
  authMock.setAccountDialogOpen.mockClear()
  renderLayout(['/applications'])
  const trigger = screen.getByText('alice').closest('button')!
  fireEvent.pointerDown(trigger)
  fireEvent.click(trigger)
  fireEvent.click(await screen.findByRole('menuitem', { name: /修改密码/ }))
  expect(authMock.setAccountDialogOpen).toHaveBeenCalledWith(true)
})

test('accountDialogOpen 为 true 时渲染改密对话框', async () => {
  authMock.accountDialogOpen = true
  try {
    renderLayout(['/applications'])
    expect(await screen.findByRole('dialog', { name: '修改账号' })).toBeTruthy()
  } finally {
    authMock.accountDialogOpen = false
  }
})
```

（下拉菜单在 base-ui 里要先 `pointerDown` 再 `click` 才会打开，与 `ThemeColorSwitcher.test.tsx` 的做法相同。这第一条测试同时是下面"现有 bug"的回归测试：菜单一打开就会渲染 `DropdownMenuLabel`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npm test`
Expected: FAIL（`ChangeAccountDialog` 不存在；`login` 不返回对象；Layout 没有「修改密码」）。

- [ ] **Step 3: 改 `web/src/lib/auth.tsx`**

```tsx
interface AuthValue {
  status: Status
  username: string | null
  /** resolve 后端给的 defaultPassword：这次登录用的是否仍是内置默认密码。 */
  login: (username: string, password: string) => Promise<{ defaultPassword: boolean }>
  logout: () => Promise<void>
  /** 改账号成功后同步顶栏显示的登录名；会话本身由后端在响应里换新。 */
  renameUser: (username: string) => void
  /** 「修改密码」对话框的开关放在这里：登录页的默认密码提示要能从别处把它打开。 */
  accountDialogOpen: boolean
  setAccountDialogOpen: (open: boolean) => void
}
```

`AuthProvider` 内：

```tsx
  const [accountDialogOpen, setAccountDialogOpen] = useState(false)
```

`setUnauthorizedHandler` 回调和 `logout` 的 `finally` 里各加一行 `setAccountDialogOpen(false)`。

把原来的 `login` 替换成下面两个（`logout` 仍在它们后面，顺序不变）：

```tsx
  const login = useCallback(async (u: string, p: string) => {
    const res = await api.post<{ username: string; defaultPassword: boolean }>('/login', { username: u, password: p })
    setUsername(res.username)
    setStatus('authed')
    return { defaultPassword: res.defaultPassword }
  }, [])

  const renameUser = useCallback((u: string) => setUsername(u), [])
```

把文件末尾原来的 `value` 替换成（它必须留在 `logout` 之后，否则会先用后声明）：

```tsx
  const value = useMemo<AuthValue>(
    () => ({ status, username, login, logout, renameUser, accountDialogOpen, setAccountDialogOpen }),
    [status, username, login, logout, renameUser, accountDialogOpen],
  )
```

- [ ] **Step 4: 新建 `web/src/components/ChangeAccountDialog.tsx`**

```tsx
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { api } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { toastFormErrors } from '@/lib/formErrors'
import { errorMessage } from '@/lib/useResource'

const schema = z
  .object({
    username: z.string().trim().min(1, '请输入登录名').max(64, '登录名最长 64 个字符'),
    oldPassword: z.string().min(1, '请输入旧密码'),
    newPassword: z.string(),
    confirmPassword: z.string(),
  })
  .refine((v) => v.newPassword === v.confirmPassword, {
    message: '两次输入的新密码不一致',
    path: ['confirmPassword'],
  })
type Values = z.infer<typeof schema>

/** 右上角「修改密码」：登录名与密码在同一个对话框里改，新密码留空表示只改登录名。 */
export function ChangeAccountDialog() {
  const { username, accountDialogOpen, setAccountDialogOpen, renameUser } = useAuth()
  const { register, handleSubmit, reset, formState } = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { username: username ?? '', oldPassword: '', newPassword: '', confirmPassword: '' },
  })

  // 每次打开都重置：上次填的旧密码不该留在表单里，登录名要跟着最新值走。
  useEffect(() => {
    if (accountDialogOpen) {
      reset({ username: username ?? '', oldPassword: '', newPassword: '', confirmPassword: '' })
    }
  }, [accountDialogOpen, username, reset])

  async function onSubmit(v: Values) {
    try {
      const res = await api.put<{ username: string }>('/me', {
        username: v.username,
        oldPassword: v.oldPassword,
        newPassword: v.newPassword,
      })
      renameUser(res.username)
      toast.success('账号信息已修改')
      setAccountDialogOpen(false)
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open={accountDialogOpen} onOpenChange={setAccountDialogOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>修改账号</DialogTitle>
        </DialogHeader>
        <form
          id="change-account-form"
          onSubmit={handleSubmit(onSubmit, toastFormErrors)}
          className="space-y-4"
          noValidate
        >
          <div className="space-y-2">
            <Label htmlFor="ca-username">登录名</Label>
            <Input id="ca-username" autoComplete="username" {...register('username')} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ca-old">旧密码</Label>
            <Input id="ca-old" type="password" autoComplete="current-password" {...register('oldPassword')} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ca-new">新密码</Label>
            <Input
              id="ca-new"
              type="password"
              autoComplete="new-password"
              placeholder="留空表示不改密码"
              {...register('newPassword')}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ca-confirm">确认新密码</Label>
            <Input id="ca-confirm" type="password" autoComplete="new-password" {...register('confirmPassword')} />
          </div>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => setAccountDialogOpen(false)}>
            取消
          </Button>
          <Button type="submit" form="change-account-form" disabled={formState.isSubmitting}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
```

- [ ] **Step 5: 改 `Login.tsx` 与 `Layout.tsx`**

`web/src/pages/Login.tsx`：

```tsx
  const { login, setAccountDialogOpen } = useAuth()
```

```tsx
  async function onSubmit(v: Values) {
    try {
      const { defaultPassword } = await login(v.username, v.password)
      if (defaultPassword) {
        toast.warning('当前仍在使用默认密码，建议修改', {
          duration: 15000,
          action: { label: '去修改', onClick: () => setAccountDialogOpen(true) },
        })
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '登录失败')
    }
  }
```

`web/src/components/Layout.tsx`：lucide 导入里加 `Lock`；`import { ChangeAccountDialog } from '@/components/ChangeAccountDialog'`；从 `@/components/ui/dropdown-menu` 的导入里加 `DropdownMenuGroup`；

```tsx
  const { username, logout, setAccountDialogOpen } = useAuth()
```

**顺带修一个现有的 bug**：用户菜单里的 `<DropdownMenuLabel>{username}</DropdownMenuLabel>` 是 base-ui 的 `Menu.GroupLabel`，1.7 起要求必须在 `Menu.Group` 里，否则菜单一打开就抛 `MenuGroupContext is missing`（上面的"点开用户菜单"测试会直接暴露它）。把它包进 group：

```tsx
                <DropdownMenuGroup>
                  <DropdownMenuLabel>{username}</DropdownMenuLabel>
                </DropdownMenuGroup>
```

然后在 `<DropdownMenuItem variant="destructive" ...>退出</DropdownMenuItem>` 之前加：

```tsx
                <DropdownMenuItem onClick={() => setAccountDialogOpen(true)}>
                  <Lock />
                  修改密码
                </DropdownMenuItem>
```

在 `Layout` 返回的 `</SidebarInset>` 之后、`</SidebarProvider>` 之前加 `<ChangeAccountDialog />`。

- [ ] **Step 6: 跑测试与构建，再在浏览器里验证**

Run: `cd web && npm test && npm run build`
Expected: 全部 PASS，`tsc -b` 无报错。

浏览器验证（用 `.claude/launch.json` 起 fp 与前端开发服务器，或 `./scripts/run.sh` + `cd web && npm run dev`）：

1. 用 `admin` / `admin` 登录 → 右下角弹出"当前仍在使用默认密码，建议修改"，点关闭后继续使用不受影响；点"去修改"打开对话框。
2. 右上角用户菜单 →「修改密码」→ 旧密码填错，提示"旧密码不正确"，**没有**被踢回登录页。
3. 改登录名为 `boss`、新密码两次一致 → 提示成功，顶栏显示 `boss`，页面仍保持登录；换另一个浏览器里的旧会话，刷新后回到登录页。
4. 用 `boss` + 新密码重新登录，不再弹默认密码提示。
5. 服务器上执行 `go run ./cmd/fp reset-password` → 用打印的 `admin` + 随机密码登录成功；原来的 `boss` 会话刷新后回到登录页。

- [ ] **Step 7: Commit**

```bash
git add web/src
git commit -m "feat(web): 登录页默认密码提示与右上角「修改密码」对话框"
git push
```

---

## Self-Review

- **Spec 覆盖**：默认账号固定 `admin/admin` 与密码存库（Task 1 `EnsureBootstrap` 沿用、Task 3 文档）；默认密码非强制提示（Task 2 `defaultPassword` + Task 4 toast）；自助改账号含登录名、无二次确认、新密码留空只改登录名（Task 1/2/4）；旧密码错 400 不复用 401 码（Task 1 错误码 + Task 2 辨别力测试）；`reset-password` 随机密码 + 同时打印用户名密码 + 作废会话 + 容器内命令（Task 1/3）；`EnsureBootstrap` 改名后门（Task 1 首个测试）；epoch 会话作废（Task 1）；`docs/console.md`（Task 3）。
- **类型一致**：`ChangeAccountInput` / `ChangeAccount` 三返回值 / `ResetPassword` 两返回值 / `DefaultAdminPassword` 在 Task 1 定义，Task 2、3 照用；前端 `login` 返回 `{ defaultPassword }`、`renameUser`、`accountDialogOpen`、`setAccountDialogOpen` 在 Task 4 定义并被 `Login.tsx` / `Layout.tsx` / `ChangeAccountDialog.tsx` 一致使用。
