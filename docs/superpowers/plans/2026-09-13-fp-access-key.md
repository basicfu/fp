# fp 访问密钥（AccessKey）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 第三方程序用 AK/SK 签名调用业务方接口；匿名请求按内置 GUEST 角色鉴权；补发 `PolicyChanged`。

**Architecture:** fp 存全局 key（绑定一个角色）；业务方 SDK 中间件本地验签（SK 从 fp 拉取后缓存），之后复用现有「角色 → 权限点」策略判定。key 变更与策略变更经现有 `fp:revoke` 频道推送。

**Tech Stack:** Go 1.25（pgx v5、go-redis v9、gRPC + buf）；React 19 + base-ui + vitest。

**Spec:** `docs/superpowers/specs/2026-09-13-fp-access-key-design.md`。行为以 spec 为准，本计划只写实现。

## Global Constraints

- **工作区有未提交的 IM 改动，且与本计划的文件重叠**（`proto/fp/v1/auth.proto`、`sdk/gen/fp/v1/auth_grpc.pb.go`、`sdk/client.go`、`sdk/client_test.go`、`sdk/options.go`、`internal/integration/phase2_env_test.go`、`sdk/README.md`、`docs/console.md`、`examples/demo/*`）。开工前必须先确认这些改动已提交，或在独立 worktree 中执行。提交时只 `git add` 本任务列出的文件，禁止 `git add -A`、`git commit -a`。
- Go 测试：`./scripts/test.sh ./路径 -run '正则'`（Git Bash）。**同一时间只跑一个 go test 进程**（共用测试库，会互相 TRUNCATE）。报 `could not import context` 时先 `go clean -cache`。
- 测试库每次清空所有表（含 `role`），迁移插入的 GUEST 行在测试里不存在：需要 GUEST 的测试自己 `CreateRole(ctx, "GUEST", "访客", nil)`。
- 前端：`cd web && npx vitest run 文件路径`；改完前端跑 `npm run build`（含 `tsc -b`）。
- 改 proto 后跑 `./scripts/gen.sh`，生成产物一并提交。
- 不新增第三方依赖；`sdk/` 不得 import `internal/`、不得 `panic`；`sdk/aksign` 只用标准库。
- 新错误码必须登记进 `internal/domain/codes.go` 的 `codeSentinels`。
- 批量改文件用 sed/perl，本机 python 是空壳。
- 注释用中文，风格与周边一致。提交信息形如 `feat(sdk): …`，末尾加 `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`。
- 固定值：AK = `FPAK` + 20 位 `[A-Z0-9]`；SK = 32 字节随机数的 base64url；时间窗 ±15 分钟；nonce `[A-Za-z0-9_-]{1,64}`；签名 body 上限默认 10MB；nonce 容量默认 200000；IP 白名单 ≤ 50 条；使用时间上报间隔 60 秒；策略兜底重拉 5 分钟；内置角色 key `GUEST`。

## 文件结构

| 文件 | 职责 |
|---|---|
| `sdk/aksign/aksign.go` | 签名规则的唯一实现：拼待签名串、算签名、给请求签名 |
| `sdk/authzcore/authzcore.go` | `GuestRoleKey`、`Snapshot.AllowWith` |
| `proto/fp/v1/auth.proto` | 两个 RPC、`AccessKeyChanged` 事件 |
| `internal/store/migrations/00011_access_key.sql` | `access_key` 表、GUEST 角色 |
| `internal/domain/accesskey.go` | `AccessKey` 与展示状态 |
| `internal/domain/session.go` | `RevokeEvent` 加 `Kind`、`AccessKeyID` |
| `internal/service/events.go` | `EventPublisher` 接口与发布辅助 |
| `internal/service/authz.go`、`permission.go` | GUEST 限制、`ROLE_IN_USE`、发布 `PolicyChanged`、按应用列权限 |
| `internal/service/accesskey.go` | key 的增删改查、给 SDK 的校验材料、记录使用时间 |
| `internal/grpcapi/auth_service.go`、`server.go` | 两个 RPC、Watch 转发新事件 |
| `internal/httpapi/accesskey.go`、`router.go` | 控制台接口 |
| `sdk/auth.go`、`middleware.go`、`authz.go` | 身份字段、匿名请求、GUEST 并入 |
| `sdk/accesskey.go`、`nonce.go`、`accesskey_error.go` | 访问密钥校验 |
| `sdk/usage.go`、`sdk/client.go` | 使用时间上报、策略定时重拉、`AccessKeyChanged` |
| `sdk/fpchi/fpchi.go` | 匿名请求被拒回 401 |
| `web/src/pages/AccessKeys.tsx`、`AccessKeyDetail.tsx` | 控制台页面 |
| `docs/access-key.md` | 对接文档与测试向量 |

---

### Task 1: 签名包 `sdk/aksign` 与 authzcore 的 GUEST 判定

**Files:**
- Create: `sdk/aksign/aksign.go`、`sdk/aksign/aksign_test.go`
- Modify: `sdk/authzcore/authzcore.go`、`sdk/authzcore/authzcore_test.go`、`sdk/arch_test.go`

**Interfaces:**
- Produces:
  - `aksign.HeaderAccessKey`、`HeaderTimestamp`、`HeaderNonce`、`HeaderSignature`（`"X-Fp-Access-Key"` 等）
  - `aksign.StringToSign(method, rawPath, rawQuery string, timestamp int64, nonce string, body []byte) string`
  - `aksign.Signature(secret, stringToSign string) string`（小写十六进制）
  - `aksign.SplitRequestURI(requestURI string) (rawPath, rawQuery string)`
  - `aksign.ValidNonce(s string) bool`
  - `aksign.Sign(req *http.Request, accessKeyID, secret string) error`
  - `aksign.SignAt(req *http.Request, accessKeyID, secret string, timestamp int64, nonce string) error`
  - `authzcore.GuestRoleKey = "GUEST"`
  - `(*authzcore.Snapshot).AllowWith(roleKeys []string, extraRole, permissionKey string) bool`

- [ ] **Step 1: 写失败测试** `sdk/aksign/aksign_test.go`

```go
package aksign

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

const vecSecret = "ZmFrZS1zZWNyZXQtZm9yLWRvY3MtZXhhbXBsZS0wMDE"

// vectors 同时写进 docs/access-key.md，Task 14 的测试核对两边一致。
var vectors = []struct {
	name, method, path, query, nonce, body, sts, sig string
}{
	{"POST 带 query 与 body", "POST", "/api/v1/orders", "source=partner",
		"3f2b8c1e-7d4a-4e1b-9c55-2a6f0d8e9b17", `{"sku":"A100","qty":2}`,
		"POST\n/api/v1/orders\nsource=partner\n1789200000\n3f2b8c1e-7d4a-4e1b-9c55-2a6f0d8e9b17\neac3608933ecc3a8767f2d0966e808202214fa80644296ed3b7a0286dae3c383",
		"a3d533065c49e9a9888c0f6e8c1b41c9d70316891411e001ec73ef12f464f3c4"},
	{"GET 编码斜杠、无 query、无 body", "GET", "/api/v1/orders/a%2Fb", "", "n-0001", "",
		"GET\n/api/v1/orders/a%2Fb\n\n1789200000\nn-0001\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"46b32c81d96b8e44e56e152af7bb2e6a6485968fc9bac34df180b2fa299ba8f8"},
}

func TestVectors(t *testing.T) {
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			sts := StringToSign(v.method, v.path, v.query, 1789200000, v.nonce, []byte(v.body))
			if sts != v.sts {
				t.Fatalf("StringToSign = %q, want %q", sts, v.sts)
			}
			if got := Signature(vecSecret, sts); got != v.sig {
				t.Fatalf("Signature = %s, want %s", got, v.sig)
			}
		})
	}
}

// 【辨别力】六个字段逐个改一处，签名都必须变。
func TestEveryFieldParticipates(t *testing.T) {
	base := func() (string, string, string, int64, string, []byte) {
		return "POST", "/a", "x=1", 100, "n1", []byte("b")
	}
	m, p, q, ts, n, b := base()
	want := Signature("s", StringToSign(m, p, q, ts, n, b))
	mutations := map[string]func(){
		"method": func() { m = "PUT" }, "path": func() { p = "/b" }, "query": func() { q = "x=2" },
		"timestamp": func() { ts = 101 }, "nonce": func() { n = "n2" }, "body": func() { b = []byte("c") },
	}
	for name, mutate := range mutations {
		m, p, q, ts, n, b = base()
		mutate()
		if Signature("s", StringToSign(m, p, q, ts, n, b)) == want {
			t.Errorf("改了 %s，签名却没变", name)
		}
	}
}

func TestSignAtMatchesVectorAndKeepsBody(t *testing.T) {
	v := vectors[0]
	req, _ := http.NewRequest(v.method, "http://biz.example"+v.path+"?"+v.query, strings.NewReader(v.body))
	if err := SignAt(req, "FPAK7Q2M9X4K1D8R3T6W", vecSecret, 1789200000, v.nonce); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get(HeaderSignature) != v.sig || req.Header.Get(HeaderTimestamp) != "1789200000" {
		t.Fatalf("签名头不对: %v", req.Header)
	}
	if b, _ := io.ReadAll(req.Body); string(b) != v.body {
		t.Fatalf("签名后 body 被读空了: %q", b)
	}
}

func TestSplitRequestURI(t *testing.T) {
	cases := []struct{ in, path, query string }{
		{"/a?b=1&c=2", "/a", "b=1&c=2"},
		{"/a", "/a", ""},
		{"/a%2Fb?x", "/a%2Fb", "x"},
		{"http://h:8080/a?x=1", "/a", "x=1"},
		{"http://h", "/", ""},
	}
	for _, c := range cases {
		if p, q := SplitRequestURI(c.in); p != c.path || q != c.query {
			t.Errorf("SplitRequestURI(%q) = (%q, %q), want (%q, %q)", c.in, p, q, c.path, c.query)
		}
	}
}

func TestValidNonce(t *testing.T) {
	for _, ok := range []string{"a", "A-z_0", strings.Repeat("x", 64)} {
		if !ValidNonce(ok) {
			t.Errorf("%q 应当合法", ok)
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 65), "a b", "a\nb", "中"} {
		if ValidNonce(bad) {
			t.Errorf("%q 应当不合法", bad)
		}
	}
}
```

- [ ] **Step 2:** `./scripts/test.sh ./sdk/aksign` → 编译失败（包不存在）。

- [ ] **Step 3: 实现** `sdk/aksign/aksign.go`

```go
// Package aksign 是 fp 访问密钥签名规则的唯一实现：第三方用它签名，fpsdk 用它验签。
//
// 只依赖标准库：用 Go 的第三方 import 它，不会被带进 gRPC 等依赖。
package aksign

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderAccessKey = "X-Fp-Access-Key"
	HeaderTimestamp = "X-Fp-Timestamp"
	HeaderNonce     = "X-Fp-Nonce"
	HeaderSignature = "X-Fp-Signature"
)

// StringToSign 拼出 6 行待签名串。rawPath、rawQuery 必须是请求行里的原样字节。
func StringToSign(method, rawPath, rawQuery string, timestamp int64, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	return method + "\n" + rawPath + "\n" + rawQuery + "\n" +
		strconv.FormatInt(timestamp, 10) + "\n" + nonce + "\n" + hex.EncodeToString(sum[:])
}

// Signature 返回小写十六进制的 HMAC-SHA256。密钥是 secret 字符串本身的字节，不做 base64 解码。
func Signature(secret, stringToSign string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(stringToSign))
	return hex.EncodeToString(m.Sum(nil))
}

// SplitRequestURI 把 request-target 拆成原样 path 与 query。绝对形式只取 path 部分。
func SplitRequestURI(requestURI string) (rawPath, rawQuery string) {
	if i := strings.Index(requestURI, "://"); i >= 0 {
		rest := requestURI[i+3:]
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return "/", ""
		}
		requestURI = rest[j:]
	}
	rawPath, rawQuery, _ = strings.Cut(requestURI, "?")
	return rawPath, rawQuery
}

// ValidNonce 报告 nonce 是否符合 [A-Za-z0-9_-]{1,64}。
func ValidNonce(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Sign 用当前时间和随机 nonce 给即将发出的请求加上 4 个签名头。
func Sign(req *http.Request, accessKeyID, secret string) error {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	return SignAt(req, accessKeyID, secret, time.Now().Unix(), hex.EncodeToString(b[:]))
}

// SignAt 与 Sign 相同，但时间戳和 nonce 由调用方给出。会读完 req.Body 并放回。
func SignAt(req *http.Request, accessKeyID, secret string, timestamp int64, nonce string) error {
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.ContentLength = int64(len(b))
		body = b
	}
	// 客户端发出的请求行就是 URL.RequestURI()，签的必须是这串字节。
	path, query := SplitRequestURI(req.URL.RequestURI())
	req.Header.Set(HeaderAccessKey, accessKeyID)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, Signature(secret, StringToSign(req.Method, path, query, timestamp, nonce, body)))
	return nil
}
```

- [ ] **Step 4: authzcore**。`sdk/authzcore/authzcore.go` 加：

```go
// GuestRoleKey 是内置访客角色：匿名请求只有它，登录用户判定时并入它，访问密钥不拥有它。
const GuestRoleKey = "GUEST"

// AllowWith 与 Allow 规则相同，但把 extraRole 也当作持有的角色（空串表示没有）。
// 用它并入 GUEST，不必为每个请求分配新的角色切片。
func (s *Snapshot) AllowWith(roleKeys []string, extraRole, permissionKey string) bool {
	if s == nil {
		return false
	}
	allowed := false
	check := func(k string) (denied bool) {
		sets, ok := s.roles[k]
		if !ok {
			return false
		}
		if _, d := sets.deny[permissionKey]; d {
			return true
		}
		if _, a := sets.allow[permissionKey]; a {
			allowed = true
		}
		return false
	}
	for _, k := range roleKeys {
		if check(k) {
			return false
		}
	}
	if extraRole != "" && check(extraRole) {
		return false
	}
	return allowed
}
```

把现有 `(*Snapshot).Allow` 的函数体改成 `return s.AllowWith(roleKeys, "", permissionKey)`。`authzcore_test.go` 追加：

```go
func TestAllowWithExtraRole(t *testing.T) {
	snap := Compile([]RolePolicy{
		{RoleKey: GuestRoleKey, Allow: []string{"GET:/pub"}},
		{RoleKey: "blocked", Deny: []string{"GET:/pub"}},
		{RoleKey: "user", Allow: []string{"GET:/me"}},
	})
	cases := []struct {
		name, extra, key string
		roles            []string
		want             bool
	}{
		{"并入 GUEST 后放行", GuestRoleKey, "GET:/pub", []string{"user"}, true},
		{"不并入就拒绝", "", "GET:/pub", []string{"user"}, false},
		{"显式角色的 deny 盖过 GUEST 的 allow", GuestRoleKey, "GET:/pub", []string{"blocked"}, false},
		{"extra 为空不影响原有判定", "", "GET:/me", []string{"user"}, true},
	}
	for _, c := range cases {
		if got := snap.AllowWith(c.roles, c.extra, c.key); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 5: 架构测试**。`sdk/arch_test.go` 追加：

```go
// TestAksignUsesOnlyStdlib 守住"sdk/aksign 只依赖标准库"：第三方 import 它不能被带进 gRPC。
func TestAksignUsesOnlyStdlib(t *testing.T) {
	root := filepath.Join(archTestDir(t), "aksign")
	walkNonTestGoFiles(t, root, nil, func(path string, file *ast.File, _ *token.FileSet) {
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if first, _, _ := strings.Cut(p, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %q：sdk/aksign 只能依赖标准库", path, p)
			}
		}
	})
}
```

- [ ] **Step 6:** `./scripts/test.sh ./sdk/aksign ./sdk/authzcore`，再 `./scripts/test.sh ./sdk -run 'TestAksignUsesOnlyStdlib|TestSDK'` → PASS。

- [ ] **Step 7: 提交**

```bash
git add sdk/aksign sdk/authzcore/authzcore.go sdk/authzcore/authzcore_test.go sdk/arch_test.go
git commit -m "feat(sdk): 访问密钥签名包 aksign；authzcore 支持并入 GUEST"
```

---

### Task 2: proto 与生成代码

**Files:**
- Modify: `proto/fp/v1/auth.proto`、`sdk/contract_test.go`
- Regenerate: `sdk/gen/fp/v1/auth.pb.go`、`sdk/gen/fp/v1/auth_grpc.pb.go`

**Interfaces:**
- Produces（生成类型）：`fpv1.GetAccessKeyRequest{AccessKeyId}`、`fpv1.GetAccessKeyResponse{Secret, Remark, Roles, AllowedIps, CacheTtlMs}`、`fpv1.ReportAccessKeyUsageRequest{Usages}`、`fpv1.AccessKeyUsage{AccessKeyId, LastUsedAtMs}`、`fpv1.ReportAccessKeyUsageResponse`、`fpv1.AccessKeyChanged{AccessKeyId}`、`fpv1.WatchResponse_AccessKeyChanged`；`AuthServiceClient` / `AuthServiceServer` 多出 `GetAccessKey`、`ReportAccessKeyUsage`。

- [ ] **Step 1: 改契约测试**。`sdk/contract_test.go` 里 `TestAuthServiceSurface` 的 `expected` 加两行：

```go
		// 访问密钥：取校验材料、上报使用时间。
		"GetAccessKey":         {false, false},
		"ReportAccessKeyUsage": {false, false},
```

并追加：

```go
// TestWatchResponseCarriesAccessKeyChanged 钉住事件的字段号：老 SDK 靠 oneof 的未知分支忽略它。
func TestWatchResponseCarriesAccessKeyChanged(t *testing.T) {
	f := fpv1.File_fp_v1_auth_proto.Messages().ByName("WatchResponse").Fields().ByName("access_key_changed")
	if f == nil || f.Number() != 8 {
		t.Fatalf("WatchResponse.access_key_changed 缺失或字段号不是 8: %v", f)
	}
}
```

- [ ] **Step 2:** `./scripts/test.sh ./sdk -run 'TestAuthServiceSurface|TestWatchResponseCarriesAccessKeyChanged'` → FAIL。

- [ ] **Step 3: 改 proto**。`service AuthService` 末尾加：

```protobuf
  // GetAccessKey 取一把访问密钥的校验材料（含 SK）。SDK 本地缓存未命中时调用。
  rpc GetAccessKey(GetAccessKeyRequest) returns (GetAccessKeyResponse);

  // ReportAccessKeyUsage 批量上报签名校验通过的时间，fp 据此更新最后使用时间。
  rpc ReportAccessKeyUsage(ReportAccessKeyUsageRequest) returns (ReportAccessKeyUsageResponse);
```

`WatchResponse` 的 `oneof event` 加：

```protobuf
    // access_key_changed 表示某把访问密钥被修改、停用或删除，SDK 应丢掉它的缓存。
    AccessKeyChanged access_key_changed = 8;
```

文件末尾加：

```protobuf
message AccessKeyChanged {
  string access_key_id = 1;
}

message GetAccessKeyRequest {
  string access_key_id = 1;
}

message GetAccessKeyResponse {
  string secret = 1;
  string remark = 2;
  // roles 是绑定的角色，当前 0 或 1 个。
  repeated string roles = 3;
  // allowed_ips 是 IP 白名单（CIDR），空表示不校验。
  repeated string allowed_ips = 4;
  // cache_ttl_ms = min(应用的 token_cache_ttl, 到期剩余时间)，0 表示不缓存。
  int64 cache_ttl_ms = 5;
}

message ReportAccessKeyUsageRequest {
  repeated AccessKeyUsage usages = 1;
}

message AccessKeyUsage {
  string access_key_id = 1;
  int64 last_used_at_ms = 2;
}

message ReportAccessKeyUsageResponse {}
```

- [ ] **Step 4:** `./scripts/gen.sh`，然后 `go build ./...`。
- [ ] **Step 5:** 重跑 Step 2 → PASS。
- [ ] **Step 6: 提交**

```bash
git add proto/fp/v1/auth.proto sdk/gen/fp/v1/auth.pb.go sdk/gen/fp/v1/auth_grpc.pb.go sdk/contract_test.go
git commit -m "feat(proto): 访问密钥的两个 RPC 与 AccessKeyChanged 事件"
```

---

### Task 3: 迁移、领域类型、错误码

**Files:**
- Create: `internal/store/migrations/00011_access_key.sql`、`internal/domain/accesskey.go`、`internal/domain/accesskey_test.go`
- Modify: `internal/domain/session.go`、`internal/domain/codes.go`

**Interfaces:**
- Produces:
  - `domain.AccessKeyStatusActive = "ACTIVE"`、`domain.AccessKeyStatusDisabled = "DISABLED"`
  - `domain.AccessKeyState`：`AccessKeyStateActive = "active"`、`AccessKeyStateDisabled = "disabled"`、`AccessKeyStateExpired = "expired"`
  - `domain.MaxAllowedIPs = 50`
  - `domain.AccessKey{ID uuid.UUID; AccessKeyID, Secret, Remark, RoleKey string; AllowedIPs []netip.Prefix; Status string; ExpiresAt, LastUsedAt, CreatedAt, UpdatedAt int64}`（时间为毫秒，0 表示没有）
  - `(domain.AccessKey).State(now time.Time) domain.AccessKeyState`
  - `domain.RevokeEvent` 新增 `Kind string`、`AccessKeyID string`；`domain.EventKindAccessKeyChanged = "access_key_changed"`、`domain.EventKindPolicyChanged = "policy_changed"`（空串表示撤销）
  - 错误码：`CodeAccessKeyInvalid`、`CodeAccessKeyDisabled`、`CodeAccessKeyExpired`、`CodeAccessKeyNotFound`、`CodeRoleInUse`、`CodeRoleBuiltin`

- [ ] **Step 1: 写失败测试** `internal/domain/accesskey_test.go`

```go
package domain_test

import (
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

func TestAccessKeyState(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	cases := []struct {
		name string
		k    domain.AccessKey
		want domain.AccessKeyState
	}{
		{"正常、永不过期", domain.AccessKey{Status: domain.AccessKeyStatusActive}, domain.AccessKeyStateActive},
		{"未到期", domain.AccessKey{Status: domain.AccessKeyStatusActive, ExpiresAt: 1_000_001}, domain.AccessKeyStateActive},
		{"恰好到期算过期", domain.AccessKey{Status: domain.AccessKeyStatusActive, ExpiresAt: 1_000_000}, domain.AccessKeyStateExpired},
		{"停用优先于过期", domain.AccessKey{Status: domain.AccessKeyStatusDisabled, ExpiresAt: 1}, domain.AccessKeyStateDisabled},
	}
	for _, c := range cases {
		if got := c.k.State(now); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2:** `./scripts/test.sh ./internal/domain -run TestAccessKeyState` → 编译失败。

- [ ] **Step 3: 迁移** `internal/store/migrations/00011_access_key.sql`

```sql
-- +goose Up

-- 访问密钥：第三方程序调用业务方接口的凭据。全局，不属于应用。
-- secret 暂时明文存储（spec「已接受的风险」第 3 条），以后加密时 SK 的值不变。
CREATE TABLE access_key (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    access_key_id text NOT NULL UNIQUE,
    secret        text NOT NULL,
    remark        text NOT NULL,
    -- 可为空。RESTRICT：被绑定的角色必须先改绑才能删，静默摘掉会让合作方突然全部 403。
    role_key      text REFERENCES role(key) ON DELETE RESTRICT,
    allowed_ips   cidr[] NOT NULL DEFAULT '{}',
    status        text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED')),
    expires_at    timestamptz,
    last_used_at  timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX access_key_role_idx ON access_key (role_key);

-- 内置访客角色：匿名请求只有它，登录用户判定时并入它。库里已有同名角色就沿用。
INSERT INTO role (key, name) VALUES ('GUEST', '访客') ON CONFLICT (key) DO NOTHING;

-- +goose Down
-- GUEST 行不删：它可能在迁移之前就被人建过。
DROP TABLE access_key;
```

- [ ] **Step 4: 领域类型** `internal/domain/accesskey.go`

```go
package domain

import (
	"net/netip"
	"time"

	"github.com/google/uuid"
)

const (
	AccessKeyStatusActive   = "ACTIVE"
	AccessKeyStatusDisabled = "DISABLED"
)

// AccessKeyState 是展示状态，由 Status 与 ExpiresAt 算出，不存库——过期不需要定时任务去改 status。
type AccessKeyState string

const (
	AccessKeyStateActive   AccessKeyState = "active"
	AccessKeyStateDisabled AccessKeyState = "disabled"
	AccessKeyStateExpired  AccessKeyState = "expired"
)

// MaxAllowedIPs 是单把 key 的 IP 白名单条数上限。
const MaxAllowedIPs = 50

// AccessKey 是一把访问密钥。时间字段为毫秒，0 表示没有（永不过期 / 从未使用）。
type AccessKey struct {
	ID          uuid.UUID
	AccessKeyID string
	// Secret 只在创建与 SDK 取校验材料时有值，其余查询清空。
	Secret     string
	Remark     string
	RoleKey    string // 空串表示未绑定
	AllowedIPs []netip.Prefix
	Status     string
	ExpiresAt  int64
	LastUsedAt int64
	CreatedAt  int64
	UpdatedAt  int64
}

// State 按 now 算出展示状态，停用优先于过期。
func (k AccessKey) State(now time.Time) AccessKeyState {
	if k.Status == AccessKeyStatusDisabled {
		return AccessKeyStateDisabled
	}
	if k.ExpiresAt > 0 && now.UnixMilli() >= k.ExpiresAt {
		return AccessKeyStateExpired
	}
	return AccessKeyStateActive
}
```

- [ ] **Step 5: 事件种类**。`internal/domain/session.go` 在 `RevokeEvent` 上方加常量，并在结构体末尾加两个字段：

```go
// 撤销频道上承载的事件种类。空串是撤销本身；另两种复用这条频道，
// 这样 Redis 订阅重建时现有的 Purge 兜底对它们同样生效。
const (
	EventKindAccessKeyChanged = "access_key_changed"
	EventKindPolicyChanged    = "policy_changed"
)
```

```go
	// Kind 为空表示撤销；非空时 Tokens、UserIDs、Reason 无意义。
	Kind string `json:"kind,omitempty"`
	// AccessKeyID 只在 Kind 为 EventKindAccessKeyChanged 时有值。
	AccessKeyID string `json:"accessKeyId,omitempty"`
```

- [ ] **Step 6: 错误码**。`internal/domain/codes.go` 常量块在「授权模块」之后加：

```go
	// 访问密钥。

	CodeAccessKeyInvalid  = "ACCESS_KEY_INVALID"
	CodeAccessKeyDisabled = "ACCESS_KEY_DISABLED"
	CodeAccessKeyExpired  = "ACCESS_KEY_EXPIRED"
	// CodeAccessKeyNotFound 是控制台按 id 找不到 key；SDK 侧的"AK 不存在"用 CodeAccessKeyInvalid。
	CodeAccessKeyNotFound = "ACCESS_KEY_NOT_FOUND"
	// CodeRoleInUse：删除仍被访问密钥绑定的角色。
	CodeRoleInUse = "ROLE_IN_USE"
	// CodeRoleBuiltin：对内置 GUEST 做了不允许的操作。
	CodeRoleBuiltin = "ROLE_BUILTIN"
```

`codeSentinels` 加：

```go
	CodeAccessKeyInvalid:  ErrUnauthorized,
	CodeAccessKeyDisabled: ErrForbidden,
	CodeAccessKeyExpired:  ErrForbidden,
	CodeAccessKeyNotFound: ErrNotFound,
	CodeRoleInUse:         ErrConflict,
	CodeRoleBuiltin:       ErrInvalidArgument,
```

- [ ] **Step 7:** 依次跑 `./scripts/test.sh ./internal/domain`、`./scripts/test.sh ./internal/store`（会跑迁移）、`./scripts/test.sh ./internal/integration -run TestTransportsAgreeOnEveryCode` → PASS。

- [ ] **Step 8: 提交**

```bash
git add internal/store/migrations/00011_access_key.sql internal/domain/accesskey.go internal/domain/accesskey_test.go internal/domain/session.go internal/domain/codes.go
git commit -m "feat(domain): access_key 表、内置 GUEST 角色、访问密钥错误码"
```

---

### Task 4: 授权服务：GUEST 限制、ROLE_IN_USE、推送 PolicyChanged

**Files:**
- Create: `internal/service/events.go`、`internal/service/events_test.go`、`internal/service/authz_accesskey_test.go`
- Modify: `internal/service/authz.go`、`internal/service/permission.go`、`internal/service/policy.go`

**Interfaces:**
- Consumes: Task 1 `authzcore.GuestRoleKey`；Task 3 的事件种类与错误码。
- Produces:
  - `service.EventPublisher interface { Publish(ctx context.Context, ev domain.RevokeEvent) error }`（`*store.RevokePublisher` 满足）
  - 包内辅助 `publishEvent(ctx context.Context, pub EventPublisher, ev domain.RevokeEvent)`（Task 5 复用）
  - `service.AuthzOption`、`service.WithAuthzPublisher(pub EventPublisher) AuthzOption`、`service.NewAuthzService(pool *pgxpool.Pool, opts ...AuthzOption) *AuthzService`（已有调用点不用改）
  - `service.PermissionRef{Key, Name string}`、`service.AppPermissions{AppID uuid.UUID; AppName string; Points []PermissionRef}`
  - `(*AuthzService).RolePermissionsByApp(ctx context.Context, roleKey string) ([]AppPermissions, error)`
  - 测试辅助（`package service_test`）：`recordingPublisher`（`Publish`、`last(t)`）、`wantDomainCode(t, err, code)`，Task 5 复用

- [ ] **Step 1: 测试辅助** `internal/service/events_test.go`

```go
package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

// recordingPublisher 记下发布的每一条事件，供断言推送。
type recordingPublisher struct {
	mu     sync.Mutex
	events []domain.RevokeEvent
}

func (p *recordingPublisher) Publish(_ context.Context, ev domain.RevokeEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return nil
}

func (p *recordingPublisher) last(t *testing.T) domain.RevokeEvent {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.events) == 0 {
		t.Fatal("没有发布任何事件")
	}
	return p.events[len(p.events)-1]
}

func wantDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}
```

- [ ] **Step 2: 写失败测试** `internal/service/authz_accesskey_test.go`

```go
package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/sdk/authzcore"
)

func TestGuestRoleRestrictions(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	guest := e.mustRole(t, authzcore.GuestRoleKey, nil)
	normal := e.mustRole(t, "普通用户", nil)
	perm := e.mustPerm(t, "GET:/pub")

	_, err := e.svc.UpdateRole(ctx, guest.ID, "访客", &normal.ID)
	wantDomainCode(t, err, domain.CodeRoleBuiltin)
	wantDomainCode(t, e.svc.SetRolePermission(ctx, guest.ID, perm.ID, domain.EffectDeny), domain.CodeRoleBuiltin)
	if err := e.svc.SetRolePermission(ctx, guest.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatalf("GUEST 配「允许」应当成功: %v", err)
	}
	wantDomainCode(t, e.svc.DeleteRole(ctx, guest.ID), domain.CodeRoleBuiltin)
	if _, err := e.svc.UpdateRole(ctx, normal.ID, "普通用户", &guest.ID); err != nil {
		t.Fatalf("普通角色继承 GUEST 不受限制: %v", err)
	}
}

func TestDeleteRoleBoundToAccessKey(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	partner := e.mustRole(t, "合作方A", nil)
	for i := 0; i < 2; i++ {
		if _, err := e.pool.Exec(ctx,
			`INSERT INTO access_key (access_key_id, secret, remark, role_key) VALUES ($1, 's', 'r', $2)`,
			fmt.Sprintf("FPAKTEST%016d", i), partner.Key); err != nil {
			t.Fatalf("插入 key: %v", err)
		}
	}
	err := e.svc.DeleteRole(ctx, partner.ID)
	wantDomainCode(t, err, domain.CodeRoleInUse)
	if !strings.Contains(err.Error(), "2 把") {
		t.Fatalf("提示里应带绑定数量: %v", err)
	}
	// 【辨别力】直接删行也必须被外键拦下，而不只靠应用层判断。
	_, err = e.pool.Exec(ctx, `DELETE FROM role WHERE id = $1`, partner.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("直接删除被绑定的角色应违反外键，err = %v", err)
	}
}

func TestAuthzWritesPublishPolicyChanged(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	pub := &recordingPublisher{}
	svc := service.NewAuthzService(e.pool, service.WithAuthzPublisher(pub))
	role := e.mustRole(t, "角色", nil)
	perm := e.mustPerm(t, "GET:/x")

	check := func(step string, wantApp uuid.UUID) {
		t.Helper()
		if ev := pub.last(t); ev.Kind != domain.EventKindPolicyChanged || ev.AppID != wantApp {
			t.Fatalf("%s: 事件 = %+v，want kind=%s app=%v", step, ev, domain.EventKindPolicyChanged, wantApp)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(svc.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow))
	check("授权", e.app.ID)
	must(svc.SetRolePermission(ctx, role.ID, perm.ID, ""))
	check("收回", e.app.ID)
	_, err := svc.UpdatePermission(ctx, perm.ID, "GET:/y", "y")
	must(err)
	check("改权限点", e.app.ID)
	_, err = svc.UpdateRole(ctx, role.ID, "角色2", nil)
	must(err)
	check("改角色", uuid.Nil)
	must(svc.DeletePermission(ctx, perm.ID))
	check("删权限点", e.app.ID)
	must(svc.DeleteRole(ctx, role.ID))
	check("删角色", uuid.Nil)
}

func TestRolePermissionsByApp(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	base := e.mustRole(t, "基线", nil)
	child := e.mustRole(t, "合作方", &base.ID)
	view := e.mustPerm(t, "GET:/orders/{id}")
	del := e.mustPerm(t, "DELETE:/orders/{id}")
	e.grant(t, base, view, domain.EffectAllow)
	e.grant(t, base, del, domain.EffectAllow)
	e.grant(t, child, del, domain.EffectDeny)

	got, err := e.svc.RolePermissionsByApp(ctx, child.Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AppID != e.app.ID || len(got[0].Points) != 1 || got[0].Points[0].Key != "GET:/orders/{id}" {
		t.Fatalf("got %+v，want 只有该应用的 GET:/orders/{id}（继承来的允许在，被子角色拒绝的不在）", got)
	}
	if empty, _ := e.svc.RolePermissionsByApp(ctx, ""); len(empty) != 0 {
		t.Fatalf("未绑定角色应返回空: %+v", empty)
	}
}
```

- [ ] **Step 3:** `./scripts/test.sh ./internal/service -run 'TestGuestRoleRestrictions|TestDeleteRoleBoundToAccessKey|TestAuthzWritesPublishPolicyChanged|TestRolePermissionsByApp'` → 编译失败。

- [ ] **Step 4: 实现**

`internal/service/events.go`：

```go
package service

import (
	"context"
	"log/slog"

	"github.com/basicfu/fp/internal/domain"
)

// EventPublisher 广播推送事件。*store.RevokePublisher 满足它。
type EventPublisher interface {
	Publish(ctx context.Context, ev domain.RevokeEvent) error
}

// publishEvent 发布失败只记日志：推送只是加速手段，SDK 另有缓存 TTL 与定时重拉兜底。
func publishEvent(ctx context.Context, pub EventPublisher, ev domain.RevokeEvent) {
	if pub == nil {
		return
	}
	if err := pub.Publish(ctx, ev); err != nil {
		slog.Error("service: 广播事件失败", "err", err, "kind", ev.Kind)
	}
}
```

`internal/service/authz.go`：
- `AuthzService` 加字段 `pub EventPublisher`；加 `type AuthzOption func(*AuthzService)` 与 `WithAuthzPublisher`；`NewAuthzService(pool, opts ...AuthzOption)` 构造后逐个应用选项。
- 加辅助：

```go
// announcePolicy 通知 SDK 重拉策略。appID 为 uuid.Nil 表示所有应用。
func (s *AuthzService) announcePolicy(ctx context.Context, appID uuid.UUID) {
	publishEvent(ctx, s.pub, domain.RevokeEvent{
		Kind: domain.EventKindPolicyChanged, AppID: appID, At: time.Now().UnixMilli(),
	})
}

// roleKeyByID 取角色 key，不存在返回 ROLE_NOT_FOUND。
func (s *AuthzService) roleKeyByID(ctx context.Context, id uuid.UUID) (string, error) {
	var key string
	err := s.pool.QueryRow(ctx, `SELECT key FROM role WHERE id = $1`, id).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.Fail(domain.ErrNotFound, domain.CodeRoleNotFound, "角色不存在")
	}
	if err != nil {
		return "", fmt.Errorf("service: 查询角色: %w", err)
	}
	return key, nil
}

// pgForeignKeyViolation 是 PostgreSQL 外键约束冲突的 SQLSTATE。
const pgForeignKeyViolation = "23503"

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation
}
```

- `UpdateRole`：在环检查之前，`parentID != nil` 时用 `roleKeyByID` 取 key，是 GUEST 就返回 `domain.Fail(domain.ErrInvalidArgument, domain.CodeRoleBuiltin, "内置角色 GUEST 不能设置父角色")`；更新成功后调 `s.announcePolicy(ctx, uuid.Nil)`。
- `DeleteRole` 整体替换为（两条连带清理原样保留）：

```go
func (s *AuthzService) DeleteRole(ctx context.Context, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("service: 开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var key string
	if err := tx.QueryRow(ctx, `SELECT key FROM role WHERE id = $1 FOR UPDATE`, id).Scan(&key); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Fail(domain.ErrNotFound, domain.CodeRoleNotFound, "角色不存在")
		}
		return fmt.Errorf("service: 查询角色: %w", err)
	}
	if key == authzcore.GuestRoleKey {
		return domain.Fail(domain.ErrInvalidArgument, domain.CodeRoleBuiltin, "内置角色 GUEST 不能删除")
	}
	var bound int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM access_key WHERE role_key = $1`, key).Scan(&bound); err != nil {
		return fmt.Errorf("service: 统计绑定该角色的访问密钥: %w", err)
	}
	if bound > 0 {
		return domain.Failf(domain.ErrConflict, domain.CodeRoleInUse,
			"有 %d 把访问密钥绑定了该角色，请先改绑或删除这些密钥", bound).WithField("count", bound)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM role WHERE id = $1`, id); err != nil {
		// 外键兜底：计数之后、删除之前有人给这个角色绑了新 key。
		if isForeignKeyViolation(err) {
			return domain.Fail(domain.ErrConflict, domain.CodeRoleInUse, "有访问密钥绑定了该角色，请先改绑或删除这些密钥")
		}
		return fmt.Errorf("service: 删除角色: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE user_role SET roles = array_remove(roles, $1), updated_at = now()
		WHERE roles @> ARRAY[$1::text]`, key); err != nil {
		return fmt.Errorf("service: 清理用户角色: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE application SET default_role_key = '' WHERE default_role_key = $1`, key); err != nil {
		return fmt.Errorf("service: 清理应用默认角色: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("service: 提交删除角色: %w", err)
	}
	s.announcePolicy(ctx, uuid.Nil)
	return nil
}
```

`internal/service/permission.go`：
- `SetRolePermission`：开头当 `effect == domain.EffectDeny` 时用 `roleKeyByID` 取 key，是 GUEST 返回 `domain.Fail(domain.ErrInvalidArgument, domain.CodeRoleBuiltin, "GUEST 只能配置「允许」")`；授予、收回两条路径成功后都调 `s.announcePolicy(ctx, s.permissionApp(ctx, permissionID))`。
- `UpdatePermission` 成功后调 `s.announcePolicy(ctx, p.ApplicationID)`。
- `DeletePermission` 改成 `DELETE FROM permission WHERE id = $1 RETURNING application_id`，`pgx.ErrNoRows` 返回 `PERMISSION_NOT_FOUND`，成功后推该应用。
- 加：

```go
// permissionApp 取权限点所属应用；查不到时返回 uuid.Nil，推给所有应用，宁多勿漏。
func (s *AuthzService) permissionApp(ctx context.Context, permissionID uuid.UUID) uuid.UUID {
	var appID uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT application_id FROM permission WHERE id = $1`, permissionID).Scan(&appID); err != nil {
		return uuid.Nil
	}
	return appID
}
```

`internal/service/policy.go` 追加（import 加 `sort`）：

```go
// PermissionRef 是一个权限点的 key 与显示名。
type PermissionRef struct {
	Key  string
	Name string
}

// AppPermissions 是某个角色在一个应用里最终允许调用的接口（继承已展开）。
type AppPermissions struct {
	AppID   uuid.UUID
	AppName string
	Points  []PermissionRef
}

// RolePermissionsByApp 按应用列出 roleKey 当前能调用的接口，供访问密钥详情页展示。
// 没有任何允许的应用不出现；roleKey 为空返回空切片。
func (s *AuthzService) RolePermissionsByApp(ctx context.Context, roleKey string) ([]AppPermissions, error) {
	out := []AppPermissions{}
	if roleKey == "" {
		return out, nil
	}
	type app struct {
		id   uuid.UUID
		name string
	}
	rows, err := s.pool.Query(ctx, `SELECT id, name FROM application ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("service: 查询应用列表: %w", err)
	}
	var apps []app
	for rows.Next() {
		var a app
		if err := rows.Scan(&a.id, &a.name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("service: 扫描应用: %w", err)
		}
		apps = append(apps, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历应用: %w", err)
	}

	for _, a := range apps {
		pol, err := s.CompilePolicy(ctx, a.id)
		if err != nil {
			return nil, err
		}
		var allow []string
		for _, rp := range pol {
			if rp.RoleKey == roleKey {
				allow = rp.Allow
			}
		}
		if len(allow) == 0 {
			continue
		}
		names := map[string]string{}
		nrows, err := s.pool.Query(ctx, `SELECT key, name FROM permission WHERE application_id = $1`, a.id)
		if err != nil {
			return nil, fmt.Errorf("service: 查询权限点名称: %w", err)
		}
		for nrows.Next() {
			var k, n string
			if err := nrows.Scan(&k, &n); err != nil {
				nrows.Close()
				return nil, fmt.Errorf("service: 扫描权限点名称: %w", err)
			}
			names[k] = n
		}
		nrows.Close()
		sort.Strings(allow)
		ap := AppPermissions{AppID: a.id, AppName: a.name}
		for _, k := range allow {
			ap.Points = append(ap.Points, PermissionRef{Key: k, Name: names[k]})
		}
		out = append(out, ap)
	}
	return out, nil
}
```

- [ ] **Step 5:** 重跑 Step 3 → PASS；再跑 `./scripts/test.sh ./internal/service`，确认原有授权测试不受影响。

- [ ] **Step 6: 提交**

```bash
git add internal/service/events.go internal/service/events_test.go internal/service/authz_accesskey_test.go internal/service/authz.go internal/service/permission.go internal/service/policy.go
git commit -m "feat(service): GUEST 内置限制、删除被绑定角色返回 ROLE_IN_USE、授权变更推送 PolicyChanged"
```

---

### Task 5: 访问密钥服务 `AccessKeyService`

**Files:**
- Create: `internal/service/accesskey.go`、`internal/service/accesskey_test.go`

**Interfaces:**
- Consumes: Task 3 的 `domain.AccessKey` 与错误码；Task 4 的 `EventPublisher`、`publishEvent`、`isForeignKeyViolation`、测试辅助 `recordingPublisher`、`wantDomainCode`；已有的 `randomToken()`（`admin.go`）、`rowScanner`、`isUniqueViolation`、`newAppServiceWith`。
- Produces:
  - `service.CreateAccessKeyInput{Remark, RoleKey string; ValidDays int; AllowedIPs []string}`
  - `service.UpdateAccessKeyInput{Remark, RoleKey *string; ValidDays *int; AllowedIPs *[]string}`（nil 表示不改）
  - `service.AccessKeyMaterial{Key domain.AccessKey; CacheTTL time.Duration}`
  - `service.NewAccessKeyService(pool *pgxpool.Pool, pub EventPublisher) *AccessKeyService`
  - `service.NewAccessKeyServiceWithClock(pool *pgxpool.Pool, pub EventPublisher, now func() time.Time) *AccessKeyService`
  - 方法：`Create(ctx, CreateAccessKeyInput) (*domain.AccessKey, error)`（唯一返回 Secret 的写方法）、`List(ctx, roleKey string) ([]domain.AccessKey, error)`、`Get(ctx, id uuid.UUID) (*domain.AccessKey, error)`、`Update(ctx, id uuid.UUID, UpdateAccessKeyInput) (*domain.AccessKey, error)`、`SetStatus(ctx, id uuid.UUID, status string) (*domain.AccessKey, error)`、`Delete(ctx, id uuid.UUID) error`、`Resolve(ctx, app *domain.Application, accessKeyID string) (*AccessKeyMaterial, error)`、`RecordUsage(ctx, usages map[string]time.Time) error`

- [ ] **Step 1: 写失败测试** `internal/service/accesskey_test.go`

```go
package service_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
	"github.com/basicfu/fp/sdk/authzcore"
)

type akEnv struct {
	svc   *service.AccessKeyService
	authz *service.AuthzService
	app   *domain.Application
	pub   *recordingPublisher
	now   time.Time
}

func newAKEnv(t *testing.T) *akEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	app, _, err := newAppServiceWith(t, pool).Create(context.Background(), "商城", "mall")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	e := &akEnv{authz: service.NewAuthzService(pool), app: app, pub: &recordingPublisher{},
		now: time.UnixMilli(1_800_000_000_000)}
	e.svc = service.NewAccessKeyServiceWithClock(pool, e.pub, func() time.Time { return e.now })
	return e
}

func TestCreateAccessKey(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	role, err := e.authz.CreateRole(ctx, "合作方A", "合作方A", nil)
	if err != nil {
		t.Fatal(err)
	}
	k, err := e.svc.Create(ctx, service.CreateAccessKeyInput{
		Remark: " 顺丰 ", RoleKey: role.Key, ValidDays: 7,
		AllowedIPs: []string{"10.0.0.5/24", "1.2.3.4", "", "::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^FPAK[A-Z0-9]{20}$`).MatchString(k.AccessKeyID) || len(k.Secret) != 43 {
		t.Fatalf("凭据格式不对: %q / %q", k.AccessKeyID, k.Secret)
	}
	if k.Remark != "顺丰" || k.RoleKey != role.Key {
		t.Fatalf("字段不对: %+v", k)
	}
	want := []string{"10.0.0.0/24", "1.2.3.4/32", "::1/128"}
	if len(k.AllowedIPs) != len(want) {
		t.Fatalf("IP 白名单 = %v, want %v", k.AllowedIPs, want)
	}
	for i, p := range k.AllowedIPs {
		if p.String() != want[i] {
			t.Fatalf("IP 白名单 = %v, want %v", k.AllowedIPs, want)
		}
	}
	if w := e.now.Add(7 * 24 * time.Hour).UnixMilli(); k.ExpiresAt != w {
		t.Fatalf("ExpiresAt = %d, want %d", k.ExpiresAt, w)
	}
	if list, _ := e.svc.List(ctx, ""); len(list) != 1 || list[0].Secret != "" {
		t.Fatalf("列表不应带 SK: %+v", list)
	}
}

func TestCreateAccessKeyValidation(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	tooMany := make([]string, domain.MaxAllowedIPs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("10.0.0.%d", i)
	}
	cases := []struct {
		name string
		in   service.CreateAccessKeyInput
		code string
	}{
		{"备注为空", service.CreateAccessKeyInput{Remark: "  "}, domain.CodeInvalidArgument},
		{"天数为负", service.CreateAccessKeyInput{Remark: "r", ValidDays: -1}, domain.CodeInvalidArgument},
		{"绑定 GUEST", service.CreateAccessKeyInput{Remark: "r", RoleKey: authzcore.GuestRoleKey}, domain.CodeRoleBuiltin},
		{"角色不存在", service.CreateAccessKeyInput{Remark: "r", RoleKey: "不存在"}, domain.CodeRoleNotFound},
		{"IP 不合法", service.CreateAccessKeyInput{Remark: "r", AllowedIPs: []string{"1.2.3"}}, domain.CodeInvalidArgument},
		{"IP 超过上限", service.CreateAccessKeyInput{Remark: "r", AllowedIPs: tooMany}, domain.CodeInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.svc.Create(ctx, c.in)
			wantDomainCode(t, err, c.code)
		})
	}
}

func TestResolveAccessKey(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	k, err := e.svc.Create(ctx, service.CreateAccessKeyInput{Remark: "r", ValidDays: 1})
	if err != nil {
		t.Fatal(err)
	}

	m, err := e.svc.Resolve(ctx, e.app, k.AccessKeyID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Key.Secret == "" || m.CacheTTL != time.Duration(e.app.Session.TokenCacheTTLSeconds)*time.Second {
		t.Fatalf("材料不对: secret=%q ttl=%v", m.Key.Secret, m.CacheTTL)
	}

	e.now = e.now.Add(24*time.Hour - 10*time.Second)
	if m, err := e.svc.Resolve(ctx, e.app, k.AccessKeyID); err != nil || m.CacheTTL != 10*time.Second {
		t.Fatalf("快到期时缓存时长应取剩余时间: ttl=%v err=%v", m.CacheTTL, err)
	}

	e.now = e.now.Add(10 * time.Second)
	_, err = e.svc.Resolve(ctx, e.app, k.AccessKeyID)
	wantDomainCode(t, err, domain.CodeAccessKeyExpired)

	if _, err := e.svc.SetStatus(ctx, k.ID, domain.AccessKeyStatusDisabled); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Resolve(ctx, e.app, k.AccessKeyID)
	wantDomainCode(t, err, domain.CodeAccessKeyDisabled)

	_, err = e.svc.Resolve(ctx, e.app, "FPAKNOTEXIST000000000000")
	wantDomainCode(t, err, domain.CodeAccessKeyInvalid)
}

// 【辨别力】只改备注时，到期时间与白名单都不能被顺手改掉。
func TestUpdateAccessKeyOnlyTouchesGivenFields(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	k, err := e.svc.Create(ctx, service.CreateAccessKeyInput{Remark: "旧", ValidDays: 7, AllowedIPs: []string{"1.2.3.4"}})
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	remark := "新"
	got, err := e.svc.Update(ctx, k.ID, service.UpdateAccessKeyInput{Remark: &remark})
	if err != nil {
		t.Fatal(err)
	}
	if got.Remark != "新" || got.ExpiresAt != k.ExpiresAt || len(got.AllowedIPs) != 1 || got.Secret != "" {
		t.Fatalf("只改备注却动了别的字段: before=%+v after=%+v", k, got)
	}
	zero := 0
	if got, _ = e.svc.Update(ctx, k.ID, service.UpdateAccessKeyInput{ValidDays: &zero}); got.ExpiresAt != 0 {
		t.Fatalf("ValidDays=0 应改为永不过期，got %d", got.ExpiresAt)
	}
	_, err = e.svc.Update(ctx, uuid.New(), service.UpdateAccessKeyInput{Remark: &remark})
	wantDomainCode(t, err, domain.CodeAccessKeyNotFound)
}

func TestRecordUsage(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	k, err := e.svc.Create(ctx, service.CreateAccessKeyInput{Remark: "r"})
	if err != nil {
		t.Fatal(err)
	}
	t1 := e.now.Add(-2 * time.Minute).Truncate(time.Minute)
	if err := e.svc.RecordUsage(ctx, map[string]time.Time{k.AccessKeyID: t1}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordUsage(ctx, map[string]time.Time{k.AccessKeyID: t1.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(ctx, k.ID); got.LastUsedAt != t1.UnixMilli() {
		t.Fatalf("更旧的时间不应覆盖: got %d want %d", got.LastUsedAt, t1.UnixMilli())
	}
	if err := e.svc.RecordUsage(ctx, map[string]time.Time{k.AccessKeyID: e.now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(ctx, k.ID); got.LastUsedAt != e.now.UnixMilli() {
		t.Fatalf("未来时间应按当前时间记: got %d want %d", got.LastUsedAt, e.now.UnixMilli())
	}
}

func TestAccessKeyWritesPublishChanged(t *testing.T) {
	e := newAKEnv(t)
	ctx := context.Background()
	k, err := e.svc.Create(ctx, service.CreateAccessKeyInput{Remark: "r"})
	if err != nil {
		t.Fatal(err)
	}
	remark := "x"
	steps := []struct {
		name string
		run  func() error
	}{
		{"改备注", func() error { _, err := e.svc.Update(ctx, k.ID, service.UpdateAccessKeyInput{Remark: &remark}); return err }},
		{"停用", func() error { _, err := e.svc.SetStatus(ctx, k.ID, domain.AccessKeyStatusDisabled); return err }},
		{"删除", func() error { return e.svc.Delete(ctx, k.ID) }},
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if ev := e.pub.last(t); ev.Kind != domain.EventKindAccessKeyChanged || ev.AccessKeyID != k.AccessKeyID || ev.AppID != uuid.Nil {
			t.Fatalf("%s: 事件 = %+v", s.name, ev)
		}
	}
}
```

- [ ] **Step 2:** `./scripts/test.sh ./internal/service -run 'AccessKey|TestRecordUsage'` → 编译失败。

- [ ] **Step 3: 实现** `internal/service/accesskey.go`

```go
package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/sdk/authzcore"
)

// AccessKeyService 管理访问密钥：控制台的增删改查，以及给业务方 SDK 的校验材料。
type AccessKeyService struct {
	pool *pgxpool.Pool
	// pub 推送 AccessKeyChanged。为 nil 时不推送。
	pub EventPublisher
	now func() time.Time
}

func NewAccessKeyService(pool *pgxpool.Pool, pub EventPublisher) *AccessKeyService {
	return NewAccessKeyServiceWithClock(pool, pub, time.Now)
}

func NewAccessKeyServiceWithClock(pool *pgxpool.Pool, pub EventPublisher, now func() time.Time) *AccessKeyService {
	return &AccessKeyService{pool: pool, pub: pub, now: now}
}

type CreateAccessKeyInput struct {
	Remark     string
	RoleKey    string // 空串表示不绑定
	ValidDays  int    // 0 表示永不过期
	AllowedIPs []string
}

// UpdateAccessKeyInput 里为 nil 的字段不修改。ValidDays 非 nil 时从现在起重新计算，0 表示永不过期。
type UpdateAccessKeyInput struct {
	Remark     *string
	RoleKey    *string
	ValidDays  *int
	AllowedIPs *[]string
}

// AccessKeyMaterial 是给业务方 SDK 的校验材料。
type AccessKeyMaterial struct {
	Key      domain.AccessKey
	CacheTTL time.Duration
}

// Create 新建一把 key。返回值带 SK，这是它唯一一次出现。
func (s *AccessKeyService) Create(ctx context.Context, in CreateAccessKeyInput) (*domain.AccessKey, error) {
	remark, err := checkRemark(in.Remark)
	if err != nil {
		return nil, err
	}
	roleKey, err := checkRoleKey(in.RoleKey)
	if err != nil {
		return nil, err
	}
	expires, err := s.expiresAt(in.ValidDays)
	if err != nil {
		return nil, err
	}
	ips, err := normalizeAllowedIPs(in.AllowedIPs)
	if err != nil {
		return nil, err
	}
	secret, err := randomToken()
	if err != nil {
		return nil, err
	}
	// AK 撞车的概率可以忽略，重试三次只是不让唯一约束冲突变成一次 500。
	for attempt := 0; attempt < 3; attempt++ {
		akID, err := newAccessKeyID()
		if err != nil {
			return nil, err
		}
		k, err := scanAccessKey(s.pool.QueryRow(ctx, `
			INSERT INTO access_key (access_key_id, secret, remark, role_key, allowed_ips, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING `+accessKeyColumns, akID, secret, remark, roleKey, ips, expires))
		switch {
		case isUniqueViolation(err):
			continue
		case isForeignKeyViolation(err):
			return nil, domain.Fail(domain.ErrNotFound, domain.CodeRoleNotFound, "角色不存在")
		case err != nil:
			return nil, fmt.Errorf("service: 创建访问密钥: %w", err)
		}
		return k, nil
	}
	return nil, errors.New("service: 连续生成的 AccessKey ID 都已存在")
}

// List 返回全部 key，roleKey 非空时只返回绑定该角色的。不含 SK。
func (s *AccessKeyService) List(ctx context.Context, roleKey string) ([]domain.AccessKey, error) {
	q, args := `SELECT `+accessKeyColumns+` FROM access_key`, []any{}
	if roleKey != "" {
		q += ` WHERE role_key = $1`
		args = append(args, roleKey)
	}
	rows, err := s.pool.Query(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("service: 查询访问密钥列表: %w", err)
	}
	defer rows.Close()
	out := []domain.AccessKey{}
	for rows.Next() {
		k, err := scanAccessKey(rows)
		if err != nil {
			return nil, fmt.Errorf("service: 扫描访问密钥: %w", err)
		}
		k.Secret = ""
		out = append(out, *k)
	}
	return out, rows.Err()
}

// Get 按主键取一把 key。不含 SK。
func (s *AccessKeyService) Get(ctx context.Context, id uuid.UUID) (*domain.AccessKey, error) {
	k, err := scanAccessKey(s.pool.QueryRow(ctx, `SELECT `+accessKeyColumns+` FROM access_key WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errAccessKeyNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询访问密钥: %w", err)
	}
	k.Secret = ""
	return k, nil
}

// Update 只改给出的字段，成功后推送 AccessKeyChanged。
func (s *AccessKeyService) Update(ctx context.Context, id uuid.UUID, in UpdateAccessKeyInput) (*domain.AccessKey, error) {
	var sets []string
	args := []any{id}
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if in.Remark != nil {
		r, err := checkRemark(*in.Remark)
		if err != nil {
			return nil, err
		}
		set("remark", r)
	}
	if in.RoleKey != nil {
		rk, err := checkRoleKey(*in.RoleKey)
		if err != nil {
			return nil, err
		}
		set("role_key", rk)
	}
	if in.ValidDays != nil {
		exp, err := s.expiresAt(*in.ValidDays)
		if err != nil {
			return nil, err
		}
		set("expires_at", exp)
	}
	if in.AllowedIPs != nil {
		ips, err := normalizeAllowedIPs(*in.AllowedIPs)
		if err != nil {
			return nil, err
		}
		set("allowed_ips", ips)
	}
	if len(sets) == 0 {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "至少要提供一个要修改的字段")
	}
	k, err := scanAccessKey(s.pool.QueryRow(ctx,
		`UPDATE access_key SET `+strings.Join(sets, ", ")+`, updated_at = now() WHERE id = $1 RETURNING `+accessKeyColumns,
		args...))
	return s.afterWrite(ctx, k, err)
}

// SetStatus 启用或停用，成功后推送 AccessKeyChanged。
func (s *AccessKeyService) SetStatus(ctx context.Context, id uuid.UUID, status string) (*domain.AccessKey, error) {
	if status != domain.AccessKeyStatusActive && status != domain.AccessKeyStatusDisabled {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "未知的状态 %q", status)
	}
	k, err := scanAccessKey(s.pool.QueryRow(ctx,
		`UPDATE access_key SET status = $2, updated_at = now() WHERE id = $1 RETURNING `+accessKeyColumns, id, status))
	return s.afterWrite(ctx, k, err)
}

// Delete 真删，成功后推送 AccessKeyChanged。
func (s *AccessKeyService) Delete(ctx context.Context, id uuid.UUID) error {
	var akID string
	err := s.pool.QueryRow(ctx, `DELETE FROM access_key WHERE id = $1 RETURNING access_key_id`, id).Scan(&akID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errAccessKeyNotFound()
	}
	if err != nil {
		return fmt.Errorf("service: 删除访问密钥: %w", err)
	}
	s.announce(ctx, akID)
	return nil
}

// Resolve 给业务方 SDK 取校验材料。不存在、已停用、已过期分别返回对应错误码，不返回 SK。
func (s *AccessKeyService) Resolve(ctx context.Context, app *domain.Application, accessKeyID string) (*AccessKeyMaterial, error) {
	k, err := scanAccessKey(s.pool.QueryRow(ctx,
		`SELECT `+accessKeyColumns+` FROM access_key WHERE access_key_id = $1`, accessKeyID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Fail(domain.ErrUnauthorized, domain.CodeAccessKeyInvalid, "AccessKey 无效")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 查询访问密钥: %w", err)
	}
	now := s.now()
	switch k.State(now) {
	case domain.AccessKeyStateDisabled:
		return nil, domain.Fail(domain.ErrForbidden, domain.CodeAccessKeyDisabled, "AccessKey 已停用")
	case domain.AccessKeyStateExpired:
		return nil, domain.Fail(domain.ErrForbidden, domain.CodeAccessKeyExpired, "AccessKey 已过期")
	}
	// 与 token 共用应用的缓存时长；快到期时不超过剩余有效期。
	ttl := time.Duration(app.Session.TokenCacheTTLSeconds) * time.Second
	if k.ExpiresAt > 0 {
		ttl = min(ttl, time.UnixMilli(k.ExpiresAt).Sub(now))
	}
	return &AccessKeyMaterial{Key: *k, CacheTTL: max(ttl, 0)}, nil
}

// RecordUsage 写入最后使用时间：只写比库里新的，晚于当前时间的按当前时间记。
func (s *AccessKeyService) RecordUsage(ctx context.Context, usages map[string]time.Time) error {
	if len(usages) == 0 {
		return nil
	}
	now := s.now()
	ids := make([]string, 0, len(usages))
	times := make([]time.Time, 0, len(usages))
	for id, t := range usages {
		ids = append(ids, id)
		times = append(times, minTime(t, now))
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE access_key AS k SET last_used_at = u.t
		FROM unnest($1::text[], $2::timestamptz[]) AS u(id, t)
		WHERE k.access_key_id = u.id AND (k.last_used_at IS NULL OR k.last_used_at < u.t)`,
		ids, times); err != nil {
		return fmt.Errorf("service: 记录访问密钥使用时间: %w", err)
	}
	return nil
}

func (s *AccessKeyService) afterWrite(ctx context.Context, k *domain.AccessKey, err error) (*domain.AccessKey, error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, errAccessKeyNotFound()
	case isForeignKeyViolation(err):
		return nil, domain.Fail(domain.ErrNotFound, domain.CodeRoleNotFound, "角色不存在")
	case err != nil:
		return nil, fmt.Errorf("service: 更新访问密钥: %w", err)
	}
	s.announce(ctx, k.AccessKeyID)
	k.Secret = ""
	return k, nil
}

// announce 让所有应用的 SDK 丢掉这把 key 的缓存。新建不需要推：还没有任何缓存。
func (s *AccessKeyService) announce(ctx context.Context, accessKeyID string) {
	publishEvent(ctx, s.pub, domain.RevokeEvent{
		Kind: domain.EventKindAccessKeyChanged, AccessKeyID: accessKeyID, AppID: uuid.Nil, At: s.now().UnixMilli(),
	})
}

const accessKeyIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// newAccessKeyID 生成 FPAK + 20 位 [A-Z0-9]。拒绝采样保证每个字符等概率。
func newAccessKeyID() (string, error) {
	out := []byte("FPAK")
	var buf [32]byte
	for len(out) < 24 {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", fmt.Errorf("service: 生成 AccessKey ID: %w", err)
		}
		for _, b := range buf {
			if b >= 252 { // 252 = 36 × 7，丢掉尾部才能均匀取模
				continue
			}
			out = append(out, accessKeyIDAlphabet[b%36])
			if len(out) == 24 {
				break
			}
		}
	}
	return string(out), nil
}

func checkRemark(remark string) (string, error) {
	r := strings.TrimSpace(remark)
	if r == "" {
		return "", domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "备注不能为空").WithField("field", "remark")
	}
	return r, nil
}

// checkRoleKey 空串返回 nil（写 NULL）。角色是否存在由外键判断。
func checkRoleKey(roleKey string) (*string, error) {
	if roleKey == "" {
		return nil, nil
	}
	if roleKey == authzcore.GuestRoleKey {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeRoleBuiltin, "访问密钥不能绑定 GUEST")
	}
	return &roleKey, nil
}

func (s *AccessKeyService) expiresAt(days int) (*time.Time, error) {
	if days < 0 {
		return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "有效期天数不能为负").WithField("field", "validDays")
	}
	if days == 0 {
		return nil, nil
	}
	t := s.now().Add(time.Duration(days) * 24 * time.Hour)
	return &t, nil
}

// normalizeAllowedIPs 解析 IP 或网段：单个 IP 存为 /32 或 /128，网段去掉主机位，空行跳过。
func normalizeAllowedIPs(in []string) ([]netip.Prefix, error) {
	out := []netip.Prefix{}
	for i, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		bad := domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
			"IP 白名单第 %d 行 %q 不是合法的 IP 或网段", i+1, raw).WithField("field", "allowedIps")
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, bad
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, bad
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	if len(out) > domain.MaxAllowedIPs {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
			"IP 白名单最多 %d 条", domain.MaxAllowedIPs).WithField("field", "allowedIps")
	}
	return out, nil
}

func minTime(a, b time.Time) time.Time {
	if a.After(b) {
		return b
	}
	return a
}

func errAccessKeyNotFound() error {
	return domain.Fail(domain.ErrNotFound, domain.CodeAccessKeyNotFound, "访问密钥不存在")
}

const accessKeyColumns = `id, access_key_id, secret, remark, coalesce(role_key, ''), allowed_ips, status,
	coalesce((extract(epoch from expires_at) * 1000)::bigint, 0),
	coalesce((extract(epoch from last_used_at) * 1000)::bigint, 0),
	(extract(epoch from created_at) * 1000)::bigint,
	(extract(epoch from updated_at) * 1000)::bigint`

func scanAccessKey(row rowScanner) (*domain.AccessKey, error) {
	var k domain.AccessKey
	if err := row.Scan(&k.ID, &k.AccessKeyID, &k.Secret, &k.Remark, &k.RoleKey, &k.AllowedIPs, &k.Status,
		&k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.UpdatedAt); err != nil {
		return nil, err
	}
	return &k, nil
}
```

- [ ] **Step 4:** 重跑 Step 2 → PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/service/accesskey.go internal/service/accesskey_test.go
git commit -m "feat(service): 访问密钥的增删改查、SDK 校验材料与使用时间记录"
```

---

### Task 6: gRPC 接口与推送转发

**Files:**
- Modify: `internal/grpcapi/auth_service.go`、`internal/grpcapi/server.go`、`internal/integration/phase2_env_test.go`
- Create: `internal/integration/accesskey_rpc_test.go`

**Interfaces:**
- Consumes: Task 2 生成类型；Task 3 事件种类；Task 4 `WithAuthzPublisher`；Task 5 `AccessKeyService.Resolve`、`RecordUsage`。
- Produces:
  - `grpcapi.Deps.AccessKeys *service.AccessKeyService`、`grpcapi.AuthServerDeps.AccessKeys *service.AccessKeyService`
  - `phase2Services.accessKeys *service.AccessKeyService`；phase2 环境里的 `authz` 带上推送（Task 11 复用）
  - 集成测试辅助：`rawAuthClient(t, e) (fpv1.AuthServiceClient, context.Context)`、`wantErrorCode(t, err, codes.Code, code string)`

- [ ] **Step 1: 装配测试环境**。`internal/integration/phase2_env_test.go`：
  - `phase2Services` 加字段 `accessKeys *service.AccessKeyService`。
  - `wireServices` 里 `authz := service.NewAuthzService(pool, service.WithAuthzPublisher(revokePub))`，新增 `accessKeys := service.NewAccessKeyService(pool, revokePub)` 并放进返回值。
  - `startServer` 的 `grpcapi.Deps` 加 `AccessKeys: e.accessKeys`（此时字段还不存在，下一步测试会编译失败）。

- [ ] **Step 2: 写失败测试** `internal/integration/accesskey_rpc_test.go`

```go
package integration_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// rawAuthClient 不经 SDK、直接用应用凭据调 AuthService：本文件测的是传输层契约。
func rawAuthClient(t *testing.T, e *phase2Env) (fpv1.AuthServiceClient, context.Context) {
	t.Helper()
	conn, err := grpc.NewClient(e.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := metadata.AppendToOutgoingContext(context.Background(), "fp-app-id", e.appID, "fp-app-secret", e.appSecret)
	return fpv1.NewAuthServiceClient(conn), ctx
}

func wantErrorCode(t *testing.T, err error, grpcCode codes.Code, code string) {
	t.Helper()
	st, _ := status.FromError(err)
	if st.Code() != grpcCode {
		t.Fatalf("gRPC code = %v, want %v（err=%v）", st.Code(), grpcCode, err)
	}
	for _, d := range st.Details() {
		if ed, ok := d.(*fpv1.ErrorDetail); ok && ed.GetCode() == code {
			return
		}
	}
	t.Fatalf("缺少错误码 %s: %v", code, err)
}

func TestGetAccessKeyRPC(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	role, err := e.authz.CreateRole(ctx, "合作方", "合作方", nil)
	if err != nil {
		t.Fatal(err)
	}
	k, err := e.accessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "顺丰", RoleKey: role.Key, AllowedIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	client, rpcCtx := rawAuthClient(t, e)

	res, err := client.GetAccessKey(rpcCtx, &fpv1.GetAccessKeyRequest{AccessKeyId: k.AccessKeyID})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetSecret() != k.Secret || res.GetRemark() != "顺丰" ||
		len(res.GetRoles()) != 1 || res.GetRoles()[0] != role.Key ||
		len(res.GetAllowedIps()) != 1 || res.GetAllowedIps()[0] != "127.0.0.1/32" ||
		res.GetCacheTtlMs() != int64(e.app.Session.TokenCacheTTLSeconds)*1000 {
		t.Fatalf("响应不对: %+v", res)
	}

	_, err = client.GetAccessKey(rpcCtx, &fpv1.GetAccessKeyRequest{AccessKeyId: "FPAKNOTEXIST000000000000"})
	wantErrorCode(t, err, codes.Unauthenticated, domain.CodeAccessKeyInvalid)

	if _, err := e.accessKeys.SetStatus(ctx, k.ID, domain.AccessKeyStatusDisabled); err != nil {
		t.Fatal(err)
	}
	_, err = client.GetAccessKey(rpcCtx, &fpv1.GetAccessKeyRequest{AccessKeyId: k.AccessKeyID})
	wantErrorCode(t, err, codes.PermissionDenied, domain.CodeAccessKeyDisabled)
}

func TestReportAccessKeyUsageRPC(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	k, err := e.accessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "r"})
	if err != nil {
		t.Fatal(err)
	}
	client, rpcCtx := rawAuthClient(t, e)
	at := time.Now().Add(-time.Minute).Truncate(time.Minute)
	if _, err := client.ReportAccessKeyUsage(rpcCtx, &fpv1.ReportAccessKeyUsageRequest{
		Usages: []*fpv1.AccessKeyUsage{{AccessKeyId: k.AccessKeyID, LastUsedAtMs: at.UnixMilli()}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.accessKeys.Get(ctx, k.ID); got.LastUsedAt != at.UnixMilli() {
		t.Fatalf("LastUsedAt = %d, want %d", got.LastUsedAt, at.UnixMilli())
	}
}

// 【辨别力】从 Watch 流的出口断言：服务层发布的两种事件真的变成了对应的推送消息。
func TestWatchForwardsAccessKeyAndPolicyEvents(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	client, rpcCtx := rawAuthClient(t, e)
	stream, err := client.Watch(rpcCtx)
	if err != nil {
		t.Fatal(err)
	}
	// 只起一个读流的 goroutine；next 读到第一条满足条件的推送为止，中途的其他事件忽略。
	msgs := make(chan *fpv1.WatchResponse, 16)
	go func() {
		defer close(msgs)
		for {
			m, err := stream.Recv()
			if err != nil {
				return
			}
			msgs <- m
		}
	}()
	next := func(what string, match func(*fpv1.WatchResponse) bool) {
		t.Helper()
		timeout := time.After(defaultWaitTimeout)
		for {
			select {
			case m, ok := <-msgs:
				if !ok {
					t.Fatalf("等 %s 时推送流断了", what)
				}
				if match(m) {
					return
				}
			case <-timeout:
				t.Fatalf("等 %s 超时", what)
			}
		}
	}
	next("ready", func(m *fpv1.WatchResponse) bool { return m.GetReady() != nil })

	k, err := e.accessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.accessKeys.SetStatus(ctx, k.ID, domain.AccessKeyStatusDisabled); err != nil {
		t.Fatal(err)
	}
	next("AccessKeyChanged", func(m *fpv1.WatchResponse) bool {
		return m.GetAccessKeyChanged().GetAccessKeyId() == k.AccessKeyID
	})

	role, _ := e.authz.CreateRole(ctx, "角色", "角色", nil)
	perm, _ := e.authz.CreatePermission(ctx, e.app.ID, "GET:/x", "x", domain.PermissionKindAPI)
	if err := e.authz.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatal(err)
	}
	next("PolicyChanged", func(m *fpv1.WatchResponse) bool { return m.GetPolicyChanged() != nil })
}
```

- [ ] **Step 3:** `./scripts/test.sh ./internal/integration -run 'AccessKey.*RPC|TestWatchForwardsAccessKeyAndPolicyEvents'` → 编译失败。

- [ ] **Step 4: 实现**

`internal/grpcapi/server.go`：`Deps` 加

```go
	// AccessKeys 提供访问密钥的校验材料与使用时间记录。为 nil 时两个 RPC 返回 Unimplemented。
	AccessKeys *service.AccessKeyService
```

`New` 里 `NewAuthServer(AuthServerDeps{…, AccessKeys: d.AccessKeys})`。

`internal/grpcapi/auth_service.go`：
- `AuthServerDeps` 与 `authServer` 各加 `AccessKeys` / `accessKeys` 字段，`NewAuthServer` 赋值。
- Watch 循环里构造撤销消息那一段（`msg = &fpv1.WatchResponse{Event: &fpv1.WatchResponse_Revoke{…}}`）改为 `msg = eventMessage(ev.Revoke)`，并加：

```go
// eventMessage 把撤销频道上的事件翻译成对应的推送消息。
func eventMessage(ev domain.RevokeEvent) *fpv1.WatchResponse {
	switch ev.Kind {
	case domain.EventKindAccessKeyChanged:
		return &fpv1.WatchResponse{Event: &fpv1.WatchResponse_AccessKeyChanged{
			AccessKeyChanged: &fpv1.AccessKeyChanged{AccessKeyId: ev.AccessKeyID},
		}}
	case domain.EventKindPolicyChanged:
		return &fpv1.WatchResponse{Event: &fpv1.WatchResponse_PolicyChanged{
			PolicyChanged: &fpv1.PolicyChanged{Version: ev.At},
		}}
	default:
		return &fpv1.WatchResponse{Event: &fpv1.WatchResponse_Revoke{Revoke: revokeEvent(ev)}}
	}
}
```

- 两个 RPC：

```go
// GetAccessKey 给业务方 SDK 取访问密钥的校验材料。
func (s *authServer) GetAccessKey(ctx context.Context, req *fpv1.GetAccessKeyRequest) (*fpv1.GetAccessKeyResponse, error) {
	if err := requireNotIM(ctx); err != nil {
		return nil, err
	}
	if s.accessKeys == nil {
		return nil, status.Error(codes.Unimplemented, "该 fp 部署未启用访问密钥")
	}
	app, err := s.callerApp(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.accessKeys.Resolve(ctx, app, req.GetAccessKeyId())
	if err != nil {
		return nil, StatusFrom(err)
	}
	ips := make([]string, 0, len(m.Key.AllowedIPs))
	for _, p := range m.Key.AllowedIPs {
		ips = append(ips, p.String())
	}
	var roles []string
	if m.Key.RoleKey != "" {
		roles = []string{m.Key.RoleKey}
	}
	return &fpv1.GetAccessKeyResponse{
		Secret: m.Key.Secret, Remark: m.Key.Remark, Roles: roles, AllowedIps: ips,
		CacheTtlMs: m.CacheTTL.Milliseconds(),
	}, nil
}

// ReportAccessKeyUsage 记录 SDK 上报的最后使用时间。同一把 key 出现多次时取最新。
func (s *authServer) ReportAccessKeyUsage(ctx context.Context, req *fpv1.ReportAccessKeyUsageRequest) (*fpv1.ReportAccessKeyUsageResponse, error) {
	if err := requireNotIM(ctx); err != nil {
		return nil, err
	}
	if s.accessKeys == nil {
		return nil, status.Error(codes.Unimplemented, "该 fp 部署未启用访问密钥")
	}
	if _, err := s.callerApp(ctx); err != nil {
		return nil, err
	}
	usages := map[string]time.Time{}
	for _, u := range req.GetUsages() {
		if u.GetAccessKeyId() == "" || u.GetLastUsedAtMs() <= 0 {
			continue
		}
		at := time.UnixMilli(u.GetLastUsedAtMs())
		if cur, ok := usages[u.GetAccessKeyId()]; !ok || at.After(cur) {
			usages[u.GetAccessKeyId()] = at
		}
	}
	if err := s.accessKeys.RecordUsage(ctx, usages); err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.ReportAccessKeyUsageResponse{}, nil
}
```

（`auth_service.go` 的 import 加 `time`。）

- [ ] **Step 5:** 重跑 Step 3 → PASS；再跑 `./scripts/test.sh ./internal/grpcapi` 与 `./scripts/test.sh ./internal/integration -run 'TestAuthz|TestRevoke'`，确认撤销推送不受影响。

- [ ] **Step 6: 提交**

```bash
git add internal/grpcapi/auth_service.go internal/grpcapi/server.go internal/integration/phase2_env_test.go internal/integration/accesskey_rpc_test.go
git commit -m "feat(grpcapi): GetAccessKey 与 ReportAccessKeyUsage；Watch 转发 AccessKeyChanged 与 PolicyChanged"
```

---

### Task 7: 控制台 HTTP 接口与进程装配

**Files:**
- Create: `internal/httpapi/accesskey.go`、`internal/httpapi/accesskey_test.go`
- Modify: `internal/httpapi/router.go`、`internal/httpapi/env_test.go`、`cmd/fp/main.go`

**Interfaces:**
- Consumes: Task 4 `RolePermissionsByApp`；Task 5 `AccessKeyService`。
- Produces（Task 12 前端依赖的 JSON 契约）：
  - 访问密钥对象：`{id, accessKeyId, remark, roleKey, allowedIps: string[], status: "ACTIVE"|"DISABLED", state: "active"|"disabled"|"expired", expiresAt, lastUsedAt, createdAt, updatedAt}`（时间为毫秒，0 表示没有）
  - `GET /admin/api/access-keys?roleKey=` → 对象数组
  - `POST /admin/api/access-keys` body `{remark, roleKey, validDays, allowedIps}` → 201 `{accessKey, secret}`
  - `GET /admin/api/access-keys/{id}` → 对象
  - `GET /admin/api/access-keys/{id}/permissions` → `[{appId, appName, points: [{key, name}]}]`
  - `PATCH /admin/api/access-keys/{id}` body 里省略或为 null 的字段不改 → 对象
  - `PATCH /admin/api/access-keys/{id}/status` body `{status}` → 对象
  - `DELETE /admin/api/access-keys/{id}` → 204
  - `httpapi.Deps.AccessKeys *service.AccessKeyService`

- [ ] **Step 1: 测试环境**。`internal/httpapi/env_test.go` 的 `newAdminEnv` 里：

```go
	authz := service.NewAuthzService(pool)
```

并在 `deps` 里加 `Authz: authz, AccessKeys: service.NewAccessKeyService(pool, nil),`。

- [ ] **Step 2: 写失败测试** `internal/httpapi/accesskey_test.go`

```go
package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type accessKeyJSON struct {
	ID          string   `json:"id"`
	AccessKeyID string   `json:"accessKeyId"`
	Remark      string   `json:"remark"`
	AllowedIPs  []string `json:"allowedIps"`
	State       string   `json:"state"`
	ExpiresAt   int64    `json:"expiresAt"`
}

func TestAccessKeyCRUDOverHTTP(t *testing.T) {
	h, token, _ := newAdminEnv(t)

	rec := do(t, h, token, http.MethodPost, "/admin/api/access-keys",
		`{"remark":"顺丰","roleKey":"","validDays":7,"allowedIps":["1.2.3.4"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		AccessKey accessKeyJSON `json:"accessKey"`
		Secret    string        `json:"secret"`
	}
	decode(t, rec, &created)
	if created.Secret == "" || created.AccessKey.State != "active" ||
		len(created.AccessKey.AllowedIPs) != 1 || created.AccessKey.AllowedIPs[0] != "1.2.3.4/32" {
		t.Fatalf("创建响应不对: %s", rec.Body.String())
	}
	base := "/admin/api/access-keys/" + created.AccessKey.ID

	rec = do(t, h, token, http.MethodGet, "/admin/api/access-keys", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.Secret) {
		t.Fatalf("列表不能带 SK: %d %s", rec.Code, rec.Body.String())
	}

	// 【辨别力】PATCH 里省略的字段不改。
	rec = do(t, h, token, http.MethodPatch, base, `{"remark":"顺丰速运"}`)
	var updated accessKeyJSON
	decode(t, rec, &updated)
	if updated.Remark != "顺丰速运" || updated.ExpiresAt != created.AccessKey.ExpiresAt || len(updated.AllowedIPs) != 1 {
		t.Fatalf("只改备注却动了别的字段: %s", rec.Body.String())
	}

	rec = do(t, h, token, http.MethodPatch, base+"/status", `{"status":"DISABLED"}`)
	var disabled accessKeyJSON
	decode(t, rec, &disabled)
	if disabled.State != "disabled" {
		t.Fatalf("停用后 state = %q", disabled.State)
	}

	if rec = do(t, h, token, http.MethodDelete, base, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, token, http.MethodGet, base, "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), domain.CodeAccessKeyNotFound) {
		t.Fatalf("删除后 get = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteRoleBoundToAccessKeyReturns409(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	rec := do(t, h, token, http.MethodPost, "/admin/api/roles", `{"key":"合作方","name":"合作方","parentId":""}`)
	var role struct {
		ID string `json:"id"`
	}
	decode(t, rec, &role)
	if rec = do(t, h, token, http.MethodPost, "/admin/api/access-keys",
		`{"remark":"r","roleKey":"合作方","validDays":0,"allowedIps":[]}`); rec.Code != http.StatusCreated {
		t.Fatalf("create key = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, token, http.MethodDelete, "/admin/api/roles/"+role.ID, "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), domain.CodeRoleInUse) {
		t.Fatalf("删除被绑定的角色 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAccessKeyPermissionsGroupedByApp(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	ctx := context.Background()
	appID := createAppID(t, h, token)
	role, err := deps.Authz.CreateRole(ctx, "合作方", "合作方", nil)
	if err != nil {
		t.Fatal(err)
	}
	perm, err := deps.Authz.CreatePermission(ctx, uuid.MustParse(appID), "GET:/orders/{id}", "查看订单", domain.PermissionKindAPI)
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.Authz.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatal(err)
	}
	k, err := deps.AccessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "r", RoleKey: role.Key})
	if err != nil {
		t.Fatal(err)
	}

	rec := do(t, h, token, http.MethodGet, "/admin/api/access-keys/"+k.ID.String()+"/permissions", "")
	var got []struct {
		AppID  string `json:"appId"`
		Points []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"points"`
	}
	decode(t, rec, &got)
	if len(got) != 1 || got[0].AppID != appID || len(got[0].Points) != 1 ||
		got[0].Points[0].Key != "GET:/orders/{id}" || got[0].Points[0].Name != "查看订单" {
		t.Fatalf("按应用分组的接口不对: %s", rec.Body.String())
	}
}
```

- [ ] **Step 3:** `./scripts/test.sh ./internal/httpapi -run 'AccessKey'` → 编译失败。

- [ ] **Step 4: 实现** `internal/httpapi/accesskey.go`

```go
package httpapi

import (
	"net/http"
	"time"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type accessKeyHandler struct {
	svc   *service.AccessKeyService
	authz *service.AuthzService
}

type accessKeyDTO struct {
	ID          string   `json:"id"`
	AccessKeyID string   `json:"accessKeyId"`
	Remark      string   `json:"remark"`
	RoleKey     string   `json:"roleKey"`
	AllowedIPs  []string `json:"allowedIps"`
	Status      string   `json:"status"`
	// State 是算出来的展示状态：active / disabled / expired，停用优先于过期。
	State      string `json:"state"`
	ExpiresAt  int64  `json:"expiresAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

func toAccessKeyDTO(k domain.AccessKey, now time.Time) accessKeyDTO {
	ips := make([]string, 0, len(k.AllowedIPs))
	for _, p := range k.AllowedIPs {
		ips = append(ips, p.String())
	}
	return accessKeyDTO{
		ID: k.ID.String(), AccessKeyID: k.AccessKeyID, Remark: k.Remark, RoleKey: k.RoleKey,
		AllowedIPs: ips, Status: k.Status, State: string(k.State(now)),
		ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt, CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt,
	}
}

func (h *accessKeyHandler) list(w http.ResponseWriter, r *http.Request) {
	keys, err := h.svc.List(r.Context(), r.URL.Query().Get("roleKey"))
	if err != nil {
		writeError(w, err)
		return
	}
	now := time.Now()
	out := make([]accessKeyDTO, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAccessKeyDTO(k, now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *accessKeyHandler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Remark     string   `json:"remark"`
		RoleKey    string   `json:"roleKey"`
		ValidDays  int      `json:"validDays"`
		AllowedIPs []string `json:"allowedIps"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	k, err := h.svc.Create(r.Context(), service.CreateAccessKeyInput{
		Remark: req.Remark, RoleKey: req.RoleKey, ValidDays: req.ValidDays, AllowedIPs: req.AllowedIPs,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	// SK 只在创建响应里出现这一次。
	writeJSON(w, http.StatusCreated, map[string]any{"accessKey": toAccessKeyDTO(*k, time.Now()), "secret": k.Secret})
}

func (h *accessKeyHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	k, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccessKeyDTO(*k, time.Now()))
}

// update 是局部更新：省略或为 null 的字段不改。roleKey 传空串表示解绑。
func (h *accessKeyHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		Remark     *string   `json:"remark"`
		RoleKey    *string   `json:"roleKey"`
		ValidDays  *int      `json:"validDays"`
		AllowedIPs *[]string `json:"allowedIps"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	k, err := h.svc.Update(r.Context(), id, service.UpdateAccessKeyInput{
		Remark: req.Remark, RoleKey: req.RoleKey, ValidDays: req.ValidDays, AllowedIPs: req.AllowedIPs,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccessKeyDTO(*k, time.Now()))
}

func (h *accessKeyHandler) setStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	k, err := h.svc.SetStatus(r.Context(), id, req.Status)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccessKeyDTO(*k, time.Now()))
}

func (h *accessKeyHandler) remove(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

type permissionRefDTO struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type appPermissionsDTO struct {
	AppID   string             `json:"appId"`
	AppName string             `json:"appName"`
	Points  []permissionRefDTO `json:"points"`
}

// permissions 列出这把 key 当前能调用的接口，按应用分组、继承已展开。
// 给"key 全局可用"配的可见性：角色在别的应用多了授权，这里一眼能看到。
func (h *accessKeyHandler) permissions(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	k, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	groups, err := h.authz.RolePermissionsByApp(r.Context(), k.RoleKey)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]appPermissionsDTO, 0, len(groups))
	for _, g := range groups {
		pts := make([]permissionRefDTO, 0, len(g.Points))
		for _, p := range g.Points {
			pts = append(pts, permissionRefDTO{Key: p.Key, Name: p.Name})
		}
		out = append(out, appPermissionsDTO{AppID: g.AppID.String(), AppName: g.AppName, Points: pts})
	}
	writeJSON(w, http.StatusOK, out)
}
```

`internal/httpapi/router.go`：
- `Deps` 加 `AccessKeys *service.AccessKeyService`（注释：为 nil 时整组路由不挂载）。
- 构造 `akH := &accessKeyHandler{svc: d.AccessKeys, authz: d.Authz}`，在管理员分组里（`/im-credential` 那段之后）加：

```go
			// 访问密钥。全局，不带应用 id。AccessKeys 为 nil 时整组不挂载。
			if d.AccessKeys != nil {
				r.Get("/access-keys", akH.list)
				r.Post("/access-keys", akH.create)
				r.Get("/access-keys/{id}", akH.get)
				r.Get("/access-keys/{id}/permissions", akH.permissions)
				r.Patch("/access-keys/{id}", akH.update)
				r.Patch("/access-keys/{id}/status", akH.setStatus)
				r.Delete("/access-keys/{id}", akH.remove)
			}
```

`cmd/fp/main.go`：

```go
	authzSvc := service.NewAuthzService(pool, service.WithAuthzPublisher(revokePub))
	accessKeySvc := service.NewAccessKeyService(pool, revokePub)
```

并在 `httpapi.Deps` 与 `grpcapi.Deps` 里各加 `AccessKeys: accessKeySvc`。

- [ ] **Step 5:** 重跑 Step 3 → PASS；`go build ./cmd/fp`；`./scripts/test.sh ./internal/httpapi`。

- [ ] **Step 6: 提交**

```bash
git add internal/httpapi/accesskey.go internal/httpapi/accesskey_test.go internal/httpapi/router.go internal/httpapi/env_test.go cmd/fp/main.go
git commit -m "feat(httpapi): 访问密钥的控制台接口；fp 进程装配访问密钥与策略推送"
```

---

### Task 8: SDK 身份字段、匿名请求与 GUEST 并入

**Files:**
- Modify: `sdk/auth.go`、`sdk/middleware.go`、`sdk/authz.go`、`sdk/fpchi/fpchi.go`、`sdk/middleware_test.go`、`sdk/fpchi/fpchi_test.go`
- Create: `sdk/authz_guest_test.go`

**Interfaces:**
- Consumes: Task 1 `authzcore.GuestRoleKey`、`Snapshot.AllowWith`。
- Produces:
  - `Identity.AccessKeyID string`、`Identity.AccessKeyRemark string`
  - `(*Identity).IsAccessKey() bool`、`(*Identity).IsAnonymous() bool`（`UserID` 与 `AccessKeyID` 都为空；访客也算匿名）
  - `fpsdk.WithIdentity(ctx context.Context, id *Identity) context.Context`

- [ ] **Step 1: 写失败测试** `sdk/authz_guest_test.go`

```go
package fpsdk

import (
	"context"
	"testing"

	"github.com/basicfu/fp/sdk/authzcore"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

func TestAllowMergesGuest(t *testing.T) {
	a := &Authz{}
	a.setPolicy(&fpv1.AppPolicy{Version: 1, Roles: []*fpv1.RolePolicy{
		{RoleKey: authzcore.GuestRoleKey, Allow: []string{"GET:/pub"}},
		{RoleKey: "partner", Allow: []string{"GET:/orders/{id}"}},
	}})
	cases := []struct {
		name    string
		id      *Identity
		pattern string
		want    bool
	}{
		{"匿名请求能调 GUEST 的接口", &Identity{}, "/pub", true},
		{"匿名请求调不了别的接口", &Identity{}, "/orders/{id}", false},
		// 【辨别力】会话里的角色不含 GUEST，登录用户照样拥有它。
		{"登录用户并入 GUEST", &Identity{UserID: "u1", Roles: []string{"普通用户"}}, "/pub", true},
		// 【辨别力】访问密钥不拥有 GUEST。
		{"没绑角色的访问密钥调不了 GUEST 的接口", &Identity{AccessKeyID: "FPAK1"}, "/pub", false},
		{"访问密钥按绑定的角色放行", &Identity{AccessKeyID: "FPAK1", Roles: []string{"partner"}}, "/orders/{id}", true},
	}
	for _, c := range cases {
		got, err := a.Allow(WithIdentity(context.Background(), c.id), "GET", c.pattern)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v err %v, want %v", c.name, got, err, c.want)
		}
	}
}

func TestIdentityKinds(t *testing.T) {
	if !(&Identity{}).IsAnonymous() || !(&Identity{GuestID: "g"}).IsAnonymous() {
		t.Fatal("没有用户也不是访问密钥的身份应算匿名（访客也算）")
	}
	if (&Identity{UserID: "u"}).IsAnonymous() || (&Identity{AccessKeyID: "k"}).IsAnonymous() {
		t.Fatal("登录用户与访问密钥不算匿名")
	}
	if !(&Identity{AccessKeyID: "k"}).IsAccessKey() || (&Identity{UserID: "u"}).IsAccessKey() {
		t.Fatal("IsAccessKey 判断不对")
	}
}
```

`sdk/middleware_test.go`：删掉 `TestMiddlewareRejectsMissingToken` 与 `TestMiddlewareAllowGuestOffRejectsGuestHeader`（行为已按 spec 改变），换成下面三条（import 加 `sync/atomic`）：

```go
// 没带任何凭据的请求按匿名放行，鉴权时只有 GUEST；不产生回源。
func TestMiddlewarePassesAnonymousWithoutCredentials(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", CacheTtlMs: 30_000}, nil
	})
	var got *Identity
	h := env.auth.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusOK || got == nil || !got.IsAnonymous() {
		t.Fatalf("code=%d identity=%+v，期望匿名放行", rec.Code, got)
	}
	if calls.Load() != 0 {
		t.Fatal("匿名请求不应回源 fp")
	}
}

// AllowGuest 关闭时忽略访客头：与没带凭据一样按匿名处理，GuestID 为空。
func TestMiddlewareAllowGuestOffIgnoresGuestHeader(t *testing.T) {
	env := newStubEnv(t, okValidate("u1", 30_000))
	var got *Identity
	h := env.auth.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(GuestIDHeader, "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || got == nil || !got.IsAnonymous() || got.GuestID != "" {
		t.Fatalf("code=%d identity=%+v，期望匿名且不采信访客头", rec.Code, got)
	}
}

// 【辨别力】带了 token 但无效：直接 401，绝不降级成匿名。
func TestMiddlewareInvalidTokenIsNotDowngradedToAnonymous(t *testing.T) {
	env := newStubEnv(t, failValidate())
	called := false
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer bad")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("code=%d called=%v，期望 401 且不进 handler", rec.Code, called)
	}
}
```

`sdk/fpchi/fpchi_test.go` 追加：

```go
// 鉴权被拒时：匿名请求回 401（该去登录），登录用户与访问密钥回 403。
func TestDeniedStatusDependsOnIdentity(t *testing.T) {
	r := chi.NewRouter()
	r.Use(New(&fakeDecider{}).Middleware()) // 空表：一律拒绝
	r.Get("/orders/{id}", func(http.ResponseWriter, *http.Request) {})

	cases := []struct {
		name string
		id   *fpsdk.Identity
		want int
	}{
		{"匿名", &fpsdk.Identity{}, http.StatusUnauthorized},
		{"访客", &fpsdk.Identity{GuestID: "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"}, http.StatusUnauthorized},
		{"登录用户", &fpsdk.Identity{UserID: "u1"}, http.StatusForbidden},
		{"访问密钥", &fpsdk.Identity{AccessKeyID: "FPAK1"}, http.StatusForbidden},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
		req = req.WithContext(fpsdk.WithIdentity(req.Context(), c.id))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: code = %d, want %d", c.name, rec.Code, c.want)
		}
	}
}
```

- [ ] **Step 2:** `./scripts/test.sh ./sdk ./sdk/fpchi` → 编译失败。

- [ ] **Step 3: 实现**

`sdk/auth.go`：`Identity` 末尾加字段与方法：

```go
	// AccessKeyID 非空表示调用方是第三方程序（访问密钥签名），与 UserID、GuestID 互斥。
	AccessKeyID string
	// AccessKeyRemark 是控制台上填的备注（哪个合作方），供业务方打日志。
	AccessKeyRemark string
}

// IsAccessKey 报告这个身份是否来自访问密钥签名。
func (id *Identity) IsAccessKey() bool { return id != nil && id.AccessKeyID != "" }

// IsAnonymous 报告这个请求既没有登录用户、也不是访问密钥（访客也算匿名）。
// 匿名请求鉴权时只有 GUEST。
func (id *Identity) IsAnonymous() bool { return id == nil || (id.UserID == "" && id.AccessKeyID == "") }
```

`sdk/middleware.go`：
- 加：

```go
// WithIdentity 把身份放进 context。供框架适配器与测试使用，业务代码通常不需要。
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}
```

- `MiddlewareWith` 的处理函数里，把"`token == "" && opts.AllowGuest` 访客分支 + 随后的 `a.Validate`"改成：

```go
			token := opts.TokenFrom(r)
			if token == "" {
				// 完全没带凭据：按匿名放行，鉴权时只有 GUEST。带了凭据但无效的走不到这里，
				// 那种必须 401——降级成匿名会让登录过期的用户悄悄变成访客，不会被提示重新登录。
				id := &Identity{}
				if opts.AllowGuest {
					if gid := r.Header.Get(GuestIDHeader); gid != "" {
						// 只做格式校验；格式错说明调用方有 bug，直接拒而不是混进匿名流量。
						if !isUUIDv4(gid) {
							opts.OnError(w, r, ErrUnauthorized)
							return
						}
						id.GuestID = gid
					}
				}
				next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
				return
			}

			id, err := a.Validate(r.Context(), token)
```

  后面放行处的 `context.WithValue(r.Context(), identityCtxKey{}, id)` 改用 `WithIdentity`。
- `MiddlewareOptions.AllowGuest` 的注释里"不开的时候，带访客头的请求必须和现在完全一样地被拒"改为"不开时忽略访客头，与没带凭据一样按匿名处理（`GuestID` 为空）"。

`sdk/authz.go` 的 `Allow` 改为：

```go
func (a *Authz) Allow(ctx context.Context, method, pattern string) (bool, error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return false, ErrNoIdentity
	}
	a.mu.RLock()
	snap, ready := a.compiled, a.ready
	a.mu.RUnlock()
	if !ready {
		return false, ErrPolicyUnavailable
	}
	// 匿名请求与登录用户都拥有内置的 GUEST；访问密钥只有绑定的角色。
	extra := authzcore.GuestRoleKey
	if id.IsAccessKey() {
		extra = ""
	}
	return snap.AllowWith(id.Roles, extra, authzcore.PermissionKey(method, pattern)), nil
}
```

`AllowRoles` 不变（只用调用方显式传入的角色），在它的注释里补一句"不会自动并入 GUEST"。

`sdk/fpchi/fpchi.go` 的 `Middleware` 里，`if !allowed` 分支改为：

```go
			if !allowed {
				// 匿名请求被拒回 401：前端据此去登录。登录用户与访问密钥被拒才是 403。
				if id, ok := fpsdk.IdentityFrom(req.Context()); ok && id.IsAnonymous() {
					http.Error(w, "未登录", http.StatusUnauthorized)
					return
				}
				http.Error(w, "没有权限", http.StatusForbidden)
				return
			}
```

- [ ] **Step 4:** `./scripts/test.sh ./sdk ./sdk/fpchi ./sdk/authzcore` → PASS。再跑 `grep -rn "Middleware" --include=*_test.go internal examples`，把断言"没带凭据回 401"的测试按新语义改掉，然后 `./scripts/test.sh ./internal/integration -run 'Guest|Parity|Authz'` → PASS。

- [ ] **Step 5: 提交**

```bash
git add sdk/auth.go sdk/middleware.go sdk/authz.go sdk/authz_guest_test.go sdk/middleware_test.go sdk/fpchi/fpchi.go sdk/fpchi/fpchi_test.go
git commit -m "feat(sdk): 匿名请求按 GUEST 鉴权；身份区分访问密钥；fpchi 匿名被拒回 401"
```

---

### Task 9: SDK 访问密钥校验

**Files:**
- Create: `sdk/accesskey.go`、`sdk/accesskey_error.go`、`sdk/nonce.go`、`sdk/nonce_test.go`、`sdk/accesskey_test.go`
- Modify: `sdk/auth.go`、`sdk/cache.go`、`sdk/middleware.go`、`sdk/options.go`、`sdk/client.go`、`sdk/client_test.go`

**Interfaces:**
- Consumes: Task 1 `aksign`；Task 2 生成的 `GetAccessKey`；Task 8 `WithIdentity`、`Identity.AccessKeyID`。
- Produces:
  - 哨兵 `fpsdk.ErrForbidden`（403）、`fpsdk.ErrBodyTooLarge`（413）
  - `fpsdk.AccessKeyError{Code, Msg, StringToSign string}`（`Unwrap` 返回哨兵）
  - 错误码常量：`CodeSignatureInvalid`、`CodeTimestampExpired`、`CodeAccessKeyInvalid`、`CodeAccessKeyDisabled`、`CodeAccessKeyExpired`、`CodeSignatureMismatch`、`CodeNonceUsed`、`CodeIPDenied`、`CodeBodyTooLarge`
  - `Options.MaxSignedBodyBytes int64`（默认 `10 << 20`）、`Options.NonceCapacity int`（默认 200000）
  - 包内：`accessKeyCacheKey(id string) string`、`entry.accessKey *accessKeyEntry`、`Client.nonces *nonceStore`、`(*Auth).verifyAccessKey(r *http.Request) (*Identity, error)`（Task 10 在它里面加使用记录）
  - 测试桩：`stubServer.getAccessKey`、`reportUsage`、`getPolicy` 三个钩子（Task 10 用后两个）；测试辅助 `akStub`、`signedRequest`、`okAccessKey`

- [ ] **Step 1: 桩服务端钩子**。`sdk/client_test.go` 的 `stubServer` 加三个字段与方法（未赋值时按 Unimplemented 处理，与 login/logout 同一约定）：

```go
	getAccessKey func(*fpv1.GetAccessKeyRequest) (*fpv1.GetAccessKeyResponse, error)
	reportUsage  func(*fpv1.ReportAccessKeyUsageRequest) (*fpv1.ReportAccessKeyUsageResponse, error)
	getPolicy    func(*fpv1.GetPolicyRequest) (*fpv1.GetPolicyResponse, error)
```

```go
func (s *stubServer) GetAccessKey(ctx context.Context, req *fpv1.GetAccessKeyRequest) (*fpv1.GetAccessKeyResponse, error) {
	if s.getAccessKey == nil {
		return s.UnimplementedAuthServiceServer.GetAccessKey(ctx, req)
	}
	return s.getAccessKey(req)
}

func (s *stubServer) ReportAccessKeyUsage(ctx context.Context, req *fpv1.ReportAccessKeyUsageRequest) (*fpv1.ReportAccessKeyUsageResponse, error) {
	if s.reportUsage == nil {
		return s.UnimplementedAuthServiceServer.ReportAccessKeyUsage(ctx, req)
	}
	return s.reportUsage(req)
}

func (s *stubServer) GetPolicy(ctx context.Context, req *fpv1.GetPolicyRequest) (*fpv1.GetPolicyResponse, error) {
	if s.getPolicy == nil {
		return s.UnimplementedAuthServiceServer.GetPolicy(ctx, req)
	}
	return s.getPolicy(req)
}
```

- [ ] **Step 2: 写失败测试** `sdk/nonce_test.go`

```go
package fpsdk

import (
	"errors"
	"testing"
)

func TestNonceStoreRejectsReuseUntilExpiry(t *testing.T) {
	s := newNonceStore(10)
	if err := s.add("ak:n1", 100, 50); err != nil {
		t.Fatal(err)
	}
	if err := s.add("ak:n1", 100, 60); !errors.Is(err, errNonceUsed) {
		t.Fatalf("窗口内重复应拒绝，got %v", err)
	}
	if err := s.add("ak:n1", 200, 101); err != nil {
		t.Fatalf("过期后应能再用，got %v", err)
	}
}

// 【辨别力】满了就拒绝新的，不挤掉仍有效的旧记录——挤掉等于关掉防重放。
func TestNonceStoreFullRejectsInsteadOfEvicting(t *testing.T) {
	s := newNonceStore(2)
	_ = s.add("a", 100, 0)
	_ = s.add("b", 100, 0)
	if err := s.add("c", 100, 1); !errors.Is(err, errNonceFull) {
		t.Fatalf("满了应返回 errNonceFull，got %v", err)
	}
	if err := s.add("a", 100, 1); !errors.Is(err, errNonceUsed) {
		t.Fatalf("旧记录不能被挤掉，got %v", err)
	}
	if err := s.add("c", 200, 101); err != nil {
		t.Fatalf("旧记录过期后应腾出位置，got %v", err)
	}
}
```

`sdk/accesskey_test.go`

```go
package fpsdk

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/sdk/aksign"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

const (
	testAK = "FPAKTEST0000000000000000"
	testSK = "test-secret"
)

func okAccessKey(ips ...string) *fpv1.GetAccessKeyResponse {
	return &fpv1.GetAccessKeyResponse{Secret: testSK, Remark: "顺丰", Roles: []string{"partner"}, AllowedIps: ips, CacheTtlMs: 30_000}
}

// akStub 起一个桩服务端：GetAccessKey 返回 res 或 err，并统计调用次数。等推送流就绪后才返回，
// 否则 ready 触发的 purge 会冲掉测试刚写进去的缓存。
func akStub(t *testing.T, res *fpv1.GetAccessKeyResponse, err error, opt ...func(*Options)) (*stubEnv, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	stub := &stubServer{
		validate:   okValidate("u1", 30_000),
		watchReady: make(chan struct{}, 1),
		events:     make(chan *fpv1.WatchResponse, 16),
		getAccessKey: func(*fpv1.GetAccessKeyRequest) (*fpv1.GetAccessKeyResponse, error) {
			calls.Add(1)
			return res, err
		},
	}
	addr, stop := startStub(t, "", stub)
	opts := Options{Addr: addr, AppID: "t", AppSecret: "t"}
	for _, o := range opt {
		o(&opts)
	}
	client, nerr := New(opts)
	if nerr != nil {
		stop()
		t.Fatalf("New: %v", nerr)
	}
	t.Cleanup(func() {
		_ = client.Close()
		stop()
	})
	env := &stubEnv{stub: stub, client: client, auth: client.Auth(), addr: addr, stop: stop}
	env.waitUntil(t, client.StreamHealthy, "推送流没有就绪")
	return env, calls
}

// signedRequest 造一个按规则签好名的服务端请求（RemoteAddr 是 httptest 默认的 192.0.2.1）。
func signedRequest(t *testing.T, method, target, body string, ts int64, nonce, secret string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	path, query := aksign.SplitRequestURI(r.RequestURI)
	r.Header.Set(aksign.HeaderAccessKey, testAK)
	r.Header.Set(aksign.HeaderTimestamp, strconv.FormatInt(ts, 10))
	r.Header.Set(aksign.HeaderNonce, nonce)
	r.Header.Set(aksign.HeaderSignature,
		aksign.Signature(secret, aksign.StringToSign(method, path, query, ts, nonce, []byte(body))))
	return r
}

func TestAccessKeyRequestPasses(t *testing.T) {
	env, calls := akStub(t, okAccessKey(), nil)
	var got *Identity
	var body string
	h := env.auth.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFrom(r.Context())
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	now := time.Now().Unix()
	for i, nonce := range []string{"n1", "n2"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, signedRequest(t, "POST", "/orders?x=1", `{"a":1}`, now, nonce, testSK))
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次: code=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	if got == nil || got.AccessKeyID != testAK || got.AccessKeyRemark != "顺丰" || len(got.Roles) != 1 || got.UserID != "" {
		t.Fatalf("身份不对: %+v", got)
	}
	if body != `{"a":1}` {
		t.Fatalf("handler 读到的 body = %q", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("key 信息应被缓存，GetAccessKey 调了 %d 次", calls.Load())
	}
}

func TestAccessKeyRejections(t *testing.T) {
	now := time.Now().Unix()
	fpDenied := func(c codes.Code, code string) error {
		st, _ := status.New(c, code).WithDetails(&fpv1.ErrorDetail{Code: code, Msg: code})
		return st.Err()
	}
	sign := func(method, body string, ts int64, nonce, secret string) func(*testing.T) *http.Request {
		return func(t *testing.T) *http.Request { return signedRequest(t, method, "/x", body, ts, nonce, secret) }
	}
	cases := []struct {
		name     string
		res      *fpv1.GetAccessKeyResponse
		fpErr    error
		opt      func(*Options)
		req      func(*testing.T) *http.Request
		wantHTTP int
		wantCode string
	}{
		{"缺签名头", okAccessKey(), nil, nil, func(t *testing.T) *http.Request {
			r := signedRequest(t, "GET", "/x", "", now, "n", testSK)
			r.Header.Del(aksign.HeaderSignature)
			return r
		}, 401, CodeSignatureInvalid},
		{"nonce 含非法字符", okAccessKey(), nil, nil, sign("GET", "", now, "a b", testSK), 401, CodeSignatureInvalid},
		{"时间戳早于窗口", okAccessKey(), nil, nil, sign("GET", "", now-16*60, "n", testSK), 401, CodeTimestampExpired},
		{"时间戳晚于窗口", okAccessKey(), nil, nil, sign("GET", "", now+16*60, "n", testSK), 401, CodeTimestampExpired},
		{"AK 不存在", nil, fpDenied(codes.Unauthenticated, CodeAccessKeyInvalid), nil, sign("GET", "", now, "n", testSK), 401, CodeAccessKeyInvalid},
		{"key 已停用", nil, fpDenied(codes.PermissionDenied, CodeAccessKeyDisabled), nil, sign("GET", "", now, "n", testSK), 403, CodeAccessKeyDisabled},
		{"key 已过期", nil, fpDenied(codes.PermissionDenied, CodeAccessKeyExpired), nil, sign("GET", "", now, "n", testSK), 403, CodeAccessKeyExpired},
		{"签名不匹配", okAccessKey(), nil, nil, sign("GET", "", now, "n", "wrong"), 401, CodeSignatureMismatch},
		{"body 超过上限", okAccessKey(), nil, func(o *Options) { o.MaxSignedBodyBytes = 4 }, sign("POST", "12345", now, "n", testSK), 413, CodeBodyTooLarge},
		{"IP 不在白名单", okAccessKey("10.0.0.0/8"), nil, nil, sign("GET", "", now, "n", testSK), 403, CodeIPDenied},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var opts []func(*Options)
			if c.opt != nil {
				opts = append(opts, c.opt)
			}
			env, _ := akStub(t, c.res, c.fpErr, opts...)
			called := false
			h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, c.req(t))
			if rec.Code != c.wantHTTP || called || !strings.Contains(rec.Body.String(), c.wantCode) {
				t.Fatalf("code=%d called=%v body=%s，want %d %s", rec.Code, called, rec.Body.String(), c.wantHTTP, c.wantCode)
			}
		})
	}
}

// 签名不匹配时附上 SDK 算出的待签名串，供第三方逐行对照；响应里不能出现 SK。
func TestSignatureMismatchIncludesStringToSign(t *testing.T) {
	env, _ := akStub(t, okAccessKey(), nil)
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	now := time.Now().Unix()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, "GET", "/x?b=2", "", now, "n1", "wrong"))
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if want := aksign.StringToSign("GET", "/x", "b=2", now, "n1", nil); body["code"] != CodeSignatureMismatch || body["stringToSign"] != want {
		t.Fatalf("body = %v, want stringToSign %q", body, want)
	}
	if strings.Contains(rec.Body.String(), testSK) {
		t.Fatal("响应里出现了 SK")
	}
}

// 【辨别力】nonce 在签名通过后才记录：伪造请求用过的 nonce，正确请求仍能用。
func TestNonceRecordedOnlyAfterSignaturePasses(t *testing.T) {
	env, _ := akStub(t, okAccessKey(), nil)
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	now := time.Now().Unix()
	serve := func(secret string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, signedRequest(t, "GET", "/x", "", now, "same-nonce", secret))
		return rec
	}
	if rec := serve("wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("错签名应 401，got %d", rec.Code)
	}
	if rec := serve(testSK); rec.Code != http.StatusOK {
		t.Fatalf("正确签名应放行，got %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(testSK); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), CodeNonceUsed) {
		t.Fatalf("重放应 401 NONCE_USED，got %d %s", rec.Code, rec.Body.String())
	}
}

func TestClientIPFromXForwardedFor(t *testing.T) {
	env, _ := akStub(t, okAccessKey("10.0.0.0/8"), nil)
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r := signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK)
	r.Header.Set("X-Forwarded-For", "10.1.2.3, 192.0.2.9") // RemoteAddr 192.0.2.1 不在白名单
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("应取 X-Forwarded-For 的第一个地址，got %d %s", rec.Code, rec.Body.String())
	}
}

// 【辨别力】带了访问密钥头就只走访问密钥：签名错时，即使同时带着 token 也不放行。
func TestAccessKeyHeaderNeverFallsBackToToken(t *testing.T) {
	env, _ := akStub(t, okAccessKey(), nil)
	called := false
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", "wrong")
	r.Header.Set("Authorization", "Bearer valid-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("code=%d called=%v", rec.Code, called)
	}
}

func TestAccessKeyFpUnavailableIs503(t *testing.T) {
	env, _ := akStub(t, nil, status.Error(codes.Unavailable, "down"))
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fp 不可达应 503 而不是 401，got %d", rec.Code)
	}
}
```

- [ ] **Step 3:** `./scripts/test.sh ./sdk -run 'Nonce|AccessKey|Signature|ClientIP'` → 编译失败。

- [ ] **Step 4: 实现**

`sdk/nonce.go`：

```go
package fpsdk

import (
	"errors"
	"sync"
)

var (
	errNonceUsed = errors.New("fpsdk: nonce 已使用过")
	errNonceFull = errors.New("fpsdk: nonce 表已满")
)

// nonceStore 记住本进程见过的 (AK, nonce)，直到对应请求的时间戳超出窗口。
// 只在单个进程内去重：重放到另一台实例能通过，这是 spec 里已接受的限制。
type nonceStore struct {
	mu   sync.Mutex
	cap  int
	seen map[string]int64 // 键 → 失效时刻（Unix 秒）
}

func newNonceStore(capacity int) *nonceStore {
	return &nonceStore{cap: capacity, seen: make(map[string]int64)}
}

// add 记录一个 nonce。仍在有效期内的重复返回 errNonceUsed；满了且清掉过期记录后
// 仍然满，返回 errNonceFull——不挤掉旧记录，挤掉等于关掉防重放。
func (s *nonceStore) add(key string, expireAt, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if exp, ok := s.seen[key]; ok && exp > now {
		return errNonceUsed
	}
	if len(s.seen) >= s.cap {
		for k, exp := range s.seen {
			if exp <= now {
				delete(s.seen, k)
			}
		}
		if len(s.seen) >= s.cap {
			return errNonceFull
		}
	}
	s.seen[key] = expireAt
	return nil
}
```

`sdk/accesskey_error.go`：

```go
package fpsdk

import (
	"encoding/json"
	"errors"
	"net/http"
)

// 访问密钥请求被拒的错误码。ACCESS_KEY_* 与 fp 服务端的码逐字相同。
const (
	CodeSignatureInvalid  = "SIGNATURE_INVALID"
	CodeTimestampExpired  = "TIMESTAMP_EXPIRED"
	CodeAccessKeyInvalid  = "ACCESS_KEY_INVALID"
	CodeAccessKeyDisabled = "ACCESS_KEY_DISABLED"
	CodeAccessKeyExpired  = "ACCESS_KEY_EXPIRED"
	CodeSignatureMismatch = "SIGNATURE_MISMATCH"
	CodeNonceUsed         = "NONCE_USED"
	CodeIPDenied          = "IP_DENIED"
	CodeBodyTooLarge      = "BODY_TOO_LARGE"
)

// AccessKeyError 是访问密钥请求被拒的原因。OnError 里可以用 errors.As 取出它自定义响应。
type AccessKeyError struct {
	Code string
	Msg  string
	// StringToSign 只在 SIGNATURE_MISMATCH 时非空：SDK 算出的待签名串，全部来自请求本身，不含 SK。
	StringToSign string

	sentinel error
}

func (e *AccessKeyError) Error() string { return e.Msg }
func (e *AccessKeyError) Unwrap() error { return e.sentinel }

func akErr(sentinel error, code, msg string) *AccessKeyError {
	return &AccessKeyError{Code: code, Msg: msg, sentinel: sentinel}
}

// writeAccessKeyError 写出错误码与固定文案。唯一回显的请求内容是待签名串，里面没有秘密。
func writeAccessKeyError(w http.ResponseWriter, e *AccessKeyError) {
	status := http.StatusUnauthorized
	switch {
	case errors.Is(e, ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(e, ErrBodyTooLarge):
		status = http.StatusRequestEntityTooLarge
	}
	body := map[string]string{"code": e.Code, "msg": e.Msg}
	if e.StringToSign != "" {
		body["stringToSign"] = e.StringToSign
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```

`sdk/auth.go`：哨兵块加

```go
	// ErrForbidden 凭据有效但不被允许：访问密钥已停用、已过期，或来源 IP 不在白名单。
	ErrForbidden = errors.New("fpsdk: 禁止访问")
	// ErrBodyTooLarge 签名请求的 body 超过 Options.MaxSignedBodyBytes。
	ErrBodyTooLarge = errors.New("fpsdk: 请求体过大")
```

`Auth` 结构体加 `akSF singleflight.Group`（注释：访问密钥回源单独合并，键空间与 token 分开）。

`sdk/middleware.go`：
- `WriteError` 开头加：

```go
	var ake *AccessKeyError
	if errors.As(err, &ake) {
		writeAccessKeyError(w, ake)
		return
	}
```

  并在 switch 里补 `ErrForbidden` → 403 `"forbidden"`、`ErrBodyTooLarge` → 413 `"request entity too large"` 两个分支。
- `MiddlewareWith` 的处理函数最前面加：

```go
			if r.Header.Get(aksign.HeaderAccessKey) != "" {
				// 带了访问密钥就只走访问密钥：校验失败直接拒，不降级成 token 或匿名。
				id, err := a.verifyAccessKey(r)
				if err != nil {
					opts.OnError(w, r, err)
					return
				}
				next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
				return
			}
```

`sdk/cache.go`：`entry` 加字段，并新增类型与函数：

```go
	// accessKey 非 nil 表示这是访问密钥的缓存条目（键见 accessKeyCacheKey），roles 同样有效。
	accessKey *accessKeyEntry
```

```go
// accessKeyEntry 是一把访问密钥的校验材料。
type accessKeyEntry struct {
	id, secret, remark string
	allowed            []netip.Prefix
}

// accessKeyCacheKey 给访问密钥条目加前缀：token 是 base64url，不含冒号，两类键不会相撞。
func accessKeyCacheKey(id string) string { return "ak:" + id }
```

`sdk/options.go`：`Options` 加

```go
	// MaxSignedBodyBytes 是访问密钥签名请求的 body 上限，超出回 413。默认 10MB。
	MaxSignedBodyBytes int64
	// NonceCapacity 是本进程记住的 nonce 条数上限。满了拒绝新的签名请求（503），
	// 不挤掉旧记录——挤掉等于关掉防重放。默认 200000。
	NonceCapacity int
```

`applyDefaults` 补 `defaultMaxSignedBodyBytes = 10 << 20`、`defaultNonceCapacity = 200_000`；`validate` 补两项不能为负。

`sdk/client.go`：`Client` 加字段 `nonces *nonceStore`，`New` 里在启动 goroutine 之前 `c.nonces = newNonceStore(opts.NonceCapacity)`。

`sdk/accesskey.go`：

```go
package fpsdk

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/sdk/aksign"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// signatureWindow 是请求时间戳与服务器时间允许的最大偏差。
const signatureWindow = 15 * time.Minute

// verifyAccessKey 校验带 X-Fp-Access-Key 的请求，顺序见 spec 第七节。
// 通过时 r.Body 已换成读过的副本，handler 能读到原字节。
func (a *Auth) verifyAccessKey(r *http.Request) (*Identity, error) {
	akID := r.Header.Get(aksign.HeaderAccessKey)
	nonce := r.Header.Get(aksign.HeaderNonce)
	ts, tsErr := strconv.ParseInt(r.Header.Get(aksign.HeaderTimestamp), 10, 64)
	sig, sigErr := hex.DecodeString(r.Header.Get(aksign.HeaderSignature))
	if tsErr != nil || sigErr != nil || len(sig) != sha256.Size || !aksign.ValidNonce(nonce) {
		return nil, akErr(ErrUnauthorized, CodeSignatureInvalid, "签名请求头缺失或格式不正确")
	}
	now := a.cache.now()
	if d := now.Sub(time.Unix(ts, 0)); d > signatureWindow || d < -signatureWindow {
		return nil, akErr(ErrUnauthorized, CodeTimestampExpired, "时间戳与服务器时间相差超过 15 分钟")
	}

	info, roles, stale, err := a.accessKey(r.Context(), akID)
	if err != nil {
		return nil, err
	}

	body, err := readSignedBody(r, a.c.opts.MaxSignedBodyBytes)
	if err != nil {
		return nil, err
	}
	requestURI := r.RequestURI
	if requestURI == "" {
		requestURI = r.URL.RequestURI()
	}
	path, query := aksign.SplitRequestURI(requestURI)
	sts := aksign.StringToSign(r.Method, path, query, ts, nonce, body)
	want, _ := hex.DecodeString(aksign.Signature(info.secret, sts))
	if !hmac.Equal(want, sig) {
		e := akErr(ErrUnauthorized, CodeSignatureMismatch, "签名不匹配")
		e.StringToSign = sts
		return nil, e
	}

	// 签名通过之后才记 nonce：伪造请求不该占内存，也不该让真实请求被误判成重放。
	switch err := a.c.nonces.add(akID+":"+nonce, ts+int64(signatureWindow/time.Second), now.Unix()); {
	case errors.Is(err, errNonceUsed):
		return nil, akErr(ErrUnauthorized, CodeNonceUsed, "nonce 已经使用过")
	case err != nil:
		return nil, errors.Join(ErrUnavailable, err)
	}

	if len(info.allowed) > 0 {
		ip, ok := clientIP(r)
		if !ok || !prefixesContain(info.allowed, ip) {
			a.c.opts.Logger.Warn("fpsdk: 访问密钥的来源 IP 不在白名单", "accessKeyId", akID, "ip", ip.String())
			return nil, akErr(ErrForbidden, CodeIPDenied, "来源 IP 不在白名单内")
		}
	}
	return &Identity{AccessKeyID: info.id, AccessKeyRemark: info.remark, Roles: roles, Stale: stale}, nil
}

// accessKey 取 key 的校验材料：本地缓存优先，未命中回源 GetAccessKey。缓存规则与 Validate 相同。
func (a *Auth) accessKey(ctx context.Context, akID string) (*accessKeyEntry, []string, bool, error) {
	var maxTTL, maxStale time.Duration
	if !a.c.StreamHealthy() {
		maxTTL = a.c.opts.DegradedCacheTTL
	}
	if a.c.opts.AllowStaleOnOutage {
		maxStale = a.c.opts.MaxStaleness
	}
	key := accessKeyCacheKey(akID)
	if e, st := a.cache.get(key, maxTTL, maxStale); st == cacheFresh && e.accessKey != nil {
		return e.accessKey, e.roles, false, nil
	}

	v, err, _ := a.akSF.Do(key, func() (any, error) {
		gen := a.cache.generation()
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.c.opts.ValidateTimeout)
		defer cancel()
		res, err := a.c.rpc.GetAccessKey(callCtx, &fpv1.GetAccessKeyRequest{AccessKeyId: akID})
		if err != nil {
			return nil, err
		}
		allowed := make([]netip.Prefix, 0, len(res.GetAllowedIps()))
		for _, s := range res.GetAllowedIps() {
			p, perr := netip.ParsePrefix(s)
			if perr != nil {
				return nil, fmt.Errorf("fpsdk: fp 下发的 IP 白名单 %q 无法解析: %w", s, perr)
			}
			allowed = append(allowed, p)
		}
		e := entry{roles: res.GetRoles(), accessKey: &accessKeyEntry{
			id: akID, secret: res.GetSecret(), remark: res.GetRemark(), allowed: allowed,
		}}
		a.cache.putIfGen(key, e, time.Duration(res.GetCacheTtlMs())*time.Millisecond, gen)
		return e, nil
	})
	if err == nil {
		e := v.(entry)
		return e.accessKey, e.roles, false, nil
	}

	// fp 给出的确定答案按错误码分 401 / 403，不走 gRPC code（translate 会把 PermissionDenied 归成 401）。
	var fe *Error
	if errors.As(translate(err), &fe) {
		switch fe.Code {
		case CodeAccessKeyDisabled:
			return nil, nil, false, akErr(ErrForbidden, fe.Code, "AccessKey 已停用")
		case CodeAccessKeyExpired:
			return nil, nil, false, akErr(ErrForbidden, fe.Code, "AccessKey 已过期")
		}
	}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound:
		return nil, nil, false, akErr(ErrUnauthorized, CodeAccessKeyInvalid, "AccessKey 无效")
	}
	if maxStale > 0 {
		if e, st := a.cache.get(key, maxTTL, maxStale); st == cacheStale && e.accessKey != nil {
			return e.accessKey, e.roles, true, nil
		}
	}
	return nil, nil, false, errors.Join(ErrUnavailable, err)
}

// readSignedBody 读出 body 用于算摘要，并把副本放回 r.Body。
func readSignedBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	_ = r.Body.Close()
	if err != nil {
		return nil, akErr(ErrUnauthorized, CodeSignatureInvalid, "读取请求体失败")
	}
	if int64(len(body)) > limit {
		return nil, akErr(ErrBodyTooLarge, CodeBodyTooLarge, "请求体超过签名校验的大小上限")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// clientIP 取来源 IP：X-Forwarded-For 的第一个地址，没有这个头时取 RemoteAddr。
// 部署要求最外层代理用真实客户端地址覆盖 X-Forwarded-For，见 docs/access-key.md。
func clientIP(r *http.Request) (netip.Addr, bool) {
	raw := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		raw = strings.TrimSpace(first)
	} else if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func prefixesContain(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5:** 重跑 Step 3 → PASS；再跑 `./scripts/test.sh ./sdk`（含 `TestSDKHasNoPanic`、`TestWriteError*`）。

- [ ] **Step 6: 提交**

```bash
git add sdk/accesskey.go sdk/accesskey_error.go sdk/nonce.go sdk/nonce_test.go sdk/accesskey_test.go sdk/auth.go sdk/cache.go sdk/middleware.go sdk/options.go sdk/client.go sdk/client_test.go
git commit -m "feat(sdk): 中间件校验访问密钥签名：本地缓存 SK、nonce 去重、IP 白名单、结构化错误响应"
```

---

### Task 10: SDK 后台任务：使用时间上报、策略定时重拉、AccessKeyChanged

**Files:**
- Create: `sdk/usage.go`、`sdk/usage_test.go`
- Modify: `sdk/client.go`、`sdk/options.go`、`sdk/accesskey.go`、`sdk/accesskey_test.go`

**Interfaces:**
- Consumes: Task 9 的 `verifyAccessKey`、`accessKeyCacheKey`、桩钩子、`akStub`、`signedRequest`、`okAccessKey`。
- Produces:
  - `usageRecorder`：`record(akID string, at time.Time)`、`take() map[string]int64`、`restore(batch map[string]int64)`
  - `(*Client).flushUsage(ctx context.Context)`；`Client.usage *usageRecorder`
  - `Options` 的两个未导出字段 `usageFlushInterval`（默认 60 秒）、`policyRefreshInterval`（默认 5 分钟），只供包内测试缩短周期
  - 测试辅助 `akStubWith(t, customize func(*stubServer), res, err, opt...)`

- [ ] **Step 1: 测试辅助**。把 `sdk/accesskey_test.go` 里的 `akStub` 抽成 `akStubWith`：多一个 `customize func(*stubServer)` 参数，在 `startStub` 之前 `if customize != nil { customize(stub) }`；`akStub` 改为 `return akStubWith(t, nil, res, err, opt...)`。

- [ ] **Step 2: 写失败测试** `sdk/usage_test.go`

```go
package fpsdk

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

func TestUsageRecorderKeepsLatestMinute(t *testing.T) {
	u := newUsageRecorder()
	base := time.Date(2026, 9, 13, 10, 30, 45, 0, time.UTC)
	want := base.Truncate(time.Minute).UnixMilli()
	u.record("k", base)
	u.record("k", base.Add(-5*time.Minute))
	batch := u.take()
	if batch["k"] != want {
		t.Fatalf("应保留较新的那一分钟: %v", batch)
	}
	if len(u.take()) != 0 {
		t.Fatal("take 之后应清空")
	}
	u.record("k", base.Add(-time.Hour))
	u.restore(batch)
	if got := u.take()["k"]; got != want {
		t.Fatalf("restore 应保留较新的时间，got %d", got)
	}
}

func reportCollector(ch chan *fpv1.ReportAccessKeyUsageRequest, failFirst bool) func(*stubServer) {
	var failed atomic.Bool
	return func(s *stubServer) {
		s.reportUsage = func(req *fpv1.ReportAccessKeyUsageRequest) (*fpv1.ReportAccessKeyUsageResponse, error) {
			if failFirst && !failed.Swap(true) {
				return nil, status.Error(codes.Unavailable, "down")
			}
			ch <- req
			return &fpv1.ReportAccessKeyUsageResponse{}, nil
		}
	}
}

func TestUsageReportedAfterSuccessfulRequestOnly(t *testing.T) {
	reports := make(chan *fpv1.ReportAccessKeyUsageRequest, 8)
	env, _ := akStubWith(t, reportCollector(reports, false), okAccessKey(), nil,
		func(o *Options) { o.usageFlushInterval = 50 * time.Millisecond })
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	h.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n0", "wrong"))
	select {
	case r := <-reports:
		t.Fatalf("校验失败的请求不应上报: %v", r)
	case <-time.After(200 * time.Millisecond):
	}

	h.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK))
	select {
	case r := <-reports:
		u := r.GetUsages()
		if len(u) != 1 || u[0].GetAccessKeyId() != testAK || u[0].GetLastUsedAtMs()%60_000 != 0 {
			t.Fatalf("上报内容不对: %v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("校验通过的请求应在一个周期内上报")
	}
}

func TestUsageRetriedAfterReportFailure(t *testing.T) {
	reports := make(chan *fpv1.ReportAccessKeyUsageRequest, 8)
	env, _ := akStubWith(t, reportCollector(reports, true), okAccessKey(), nil,
		func(o *Options) { o.usageFlushInterval = 50 * time.Millisecond })
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK))
	select {
	case r := <-reports:
		if len(r.GetUsages()) != 1 {
			t.Fatalf("重试的批次不对: %v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("第一次上报失败后，应并入下一批重试")
	}
}

func TestCloseFlushesPendingUsage(t *testing.T) {
	reports := make(chan *fpv1.ReportAccessKeyUsageRequest, 8)
	env, _ := akStubWith(t, reportCollector(reports, false), okAccessKey(), nil) // 默认 60 秒周期，只有 Close 会触发上报
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK))
	_ = env.client.Close()
	select {
	case <-reports:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 应把还没上报的使用时间报掉")
	}
}

// 【辨别力】缓存时长给到 1 小时：第二次回源只能由推送解释。
func TestAccessKeyChangedAndPurgeDropCache(t *testing.T) {
	for _, tc := range []struct {
		name string
		push func(*stubEnv)
	}{
		{"AccessKeyChanged", func(e *stubEnv) {
			e.stub.events <- &fpv1.WatchResponse{Event: &fpv1.WatchResponse_AccessKeyChanged{
				AccessKeyChanged: &fpv1.AccessKeyChanged{AccessKeyId: testAK}}}
		}},
		{"Purge", func(e *stubEnv) { e.pushPurge(t, "测试") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := okAccessKey()
			res.CacheTtlMs = time.Hour.Milliseconds()
			env, calls := akStub(t, res, nil)
			h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			serve := func() {
				nonce := "n" + strconv.FormatInt(time.Now().UnixNano(), 10)
				h.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "GET", "/x", "", time.Now().Unix(), nonce, testSK))
			}
			serve()
			serve()
			if calls.Load() != 1 {
				t.Fatalf("推送前应命中缓存，GetAccessKey 调了 %d 次", calls.Load())
			}
			tc.push(env)
			env.waitUntil(t, func() bool { serve(); return calls.Load() == 2 }, "收到推送后应重新向 fp 取")
		})
	}
}

func TestPolicyRefreshedPeriodically(t *testing.T) {
	var polls atomic.Int32
	akStubWith(t, func(s *stubServer) {
		s.getPolicy = func(*fpv1.GetPolicyRequest) (*fpv1.GetPolicyResponse, error) {
			polls.Add(1)
			return &fpv1.GetPolicyResponse{Policy: &fpv1.AppPolicy{Version: 1}}, nil
		}
	}, okAccessKey(), nil, func(o *Options) { o.policyRefreshInterval = 50 * time.Millisecond })
	waitUntilTimeout(t, 3*time.Second, func() bool { return polls.Load() >= 3 },
		"除了 ready 触发的那次，策略还应按周期重拉")
}
```

- [ ] **Step 3:** `./scripts/test.sh ./sdk -run 'Usage|TestCloseFlushesPendingUsage|TestAccessKeyChangedAndPurgeDropCache|TestPolicyRefreshedPeriodically'` → 编译失败。

- [ ] **Step 4: 实现**

`sdk/usage.go`：

```go
package fpsdk

import (
	"context"
	"sync"
	"time"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// usageRecorder 按分钟记录每把 key 最后一次校验通过的时间，由 Client 定期批量上报。
type usageRecorder struct {
	mu   sync.Mutex
	last map[string]int64 // AK → 按分钟截断的 Unix 毫秒
}

func newUsageRecorder() *usageRecorder { return &usageRecorder{last: make(map[string]int64)} }

func (u *usageRecorder) record(akID string, at time.Time) {
	m := at.Truncate(time.Minute).UnixMilli()
	u.mu.Lock()
	if m > u.last[akID] {
		u.last[akID] = m
	}
	u.mu.Unlock()
}

// take 取走当前这一批。
func (u *usageRecorder) take() map[string]int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	batch := u.last
	u.last = make(map[string]int64)
	return batch
}

// restore 把上报失败的批次并回去，保留较新的时间。
func (u *usageRecorder) restore(batch map[string]int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for id, ms := range batch {
		if ms > u.last[id] {
			u.last[id] = ms
		}
	}
}

// flushUsage 上报一批使用时间；失败的并入下一批。
func (c *Client) flushUsage(ctx context.Context) {
	batch := c.usage.take()
	if len(batch) == 0 {
		return
	}
	req := &fpv1.ReportAccessKeyUsageRequest{Usages: make([]*fpv1.AccessKeyUsage, 0, len(batch))}
	for id, ms := range batch {
		req.Usages = append(req.Usages, &fpv1.AccessKeyUsage{AccessKeyId: id, LastUsedAtMs: ms})
	}
	if _, err := c.rpc.ReportAccessKeyUsage(ctx, req); err != nil {
		c.usage.restore(batch)
		c.opts.Logger.Warn("fpsdk: 上报访问密钥使用时间失败，并入下一批", "err", err)
	}
}

// runEvery 每隔 every 调一次 fn，直到 ctx 取消。
func runEvery(ctx context.Context, every time.Duration, fn func(context.Context)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}
```

`sdk/options.go`：`Options` 末尾加

```go
	// usageFlushInterval、policyRefreshInterval 只供包内测试缩短周期，零值取默认。
	usageFlushInterval    time.Duration
	policyRefreshInterval time.Duration
```

`applyDefaults` 里：零值时分别设为 `time.Minute`、`5 * time.Minute`。

`sdk/client.go`：
- `Client` 加字段 `usage *usageRecorder`；`New` 里 `c.usage = newUsageRecorder()`，并在现有两个 goroutine 之后再起两个：

```go
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		runEvery(ctx, opts.usageFlushInterval, c.flushUsage)
	}()
	go func() {
		defer c.wg.Done()
		// 兜底：推送的 PolicyChanged 丢了，也能在一个周期内追上。
		runEvery(ctx, opts.policyRefreshInterval, c.refreshPolicy)
	}()
```

- `Close` 开头先报最后一批（连接一关就发不出去了）：

```go
	flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	c.flushUsage(flushCtx)
	cancel()
```

- `watchOnce` 的 switch 加：

```go
		case msg.GetAccessKeyChanged() != nil:
			// 丢掉这把 key 的缓存，下次请求重新向 fp 取：停用、删除、改角色、改白名单都靠它立即生效。
			c.auth.cache.drop(accessKeyCacheKey(msg.GetAccessKeyChanged().GetAccessKeyId()))
```

`sdk/accesskey.go`：`verifyAccessKey` 最后的 `return &Identity{…}` 之前加 `a.c.usage.record(akID, now)`。

- [ ] **Step 5:** 重跑 Step 3 → PASS；再跑 `./scripts/test.sh ./sdk`（`TestCloseStopsWatchLoop` 等 goroutine 相关测试要保持通过）。

- [ ] **Step 6: 提交**

```bash
git add sdk/usage.go sdk/usage_test.go sdk/client.go sdk/options.go sdk/accesskey.go sdk/accesskey_test.go
git commit -m "feat(sdk): 上报访问密钥使用时间、策略每 5 分钟兜底重拉、处理 AccessKeyChanged"
```

---

### Task 11: 端到端集成测试

**Files:**
- Create: `internal/integration/accesskey_e2e_test.go`

**Interfaces:**
- Consumes: Task 6 的 `phase2Env.accessKeys`；Task 8~10 的 SDK 行为；`aksign.Sign`；`fpchi`；已有的 `waitUntil`、`waitUntilTimeout`、`waitPolicy`、`e.dial`。

- [ ] **Step 1: 写测试** `internal/integration/accesskey_e2e_test.go`

```go
package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpsdk "github.com/basicfu/fp/sdk"
	"github.com/basicfu/fp/sdk/aksign"
	"github.com/basicfu/fp/sdk/authzcore"
	"github.com/basicfu/fp/sdk/fpchi"
)

// newBizServer 起一个"业务服务"：SDK 认证 + fpchi 鉴权，所有路由都挂鉴权（spec 的部署约定）。
func newBizServer(t *testing.T, client *fpsdk.Client) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	r.Use(client.Auth().Middleware)
	r.Use(fpchi.New(client.Authz()).Middleware())
	r.Get("/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := fpsdk.IdentityFrom(r.Context())
		_, _ = io.WriteString(w, id.AccessKeyID)
	})
	r.Post("/orders", func(http.ResponseWriter, *http.Request) {})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// callBiz 发一个请求并返回状态码；ak 非空时用 aksign 签名。
func callBiz(t *testing.T, srv *httptest.Server, method, path, ak, sk string) int {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if ak != "" {
		if err := aksign.Sign(req, ak, sk); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// partnerKey 建角色「合作方」、授权 GET:/orders/{id}，再建一把绑定它、白名单为本机的 key。
func partnerKey(t *testing.T, e *phase2Env) (*domain.Role, *domain.Permission, *domain.AccessKey) {
	t.Helper()
	ctx := context.Background()
	role, err := e.authz.CreateRole(ctx, "合作方", "合作方", nil)
	if err != nil {
		t.Fatal(err)
	}
	perm, err := e.authz.CreatePermission(ctx, e.app.ID, "GET:/orders/{id}", "查看订单", domain.PermissionKindAPI)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.authz.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatal(err)
	}
	k, err := e.accessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "顺丰", RoleKey: role.Key, AllowedIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	return role, perm, k
}

func TestAccessKeyEndToEnd(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	role, perm, k := partnerKey(t, e)
	srv := newBizServer(t, e.sdk)
	get := func() int { return callBiz(t, srv, http.MethodGet, "/orders/1", k.AccessKeyID, k.Secret) }

	waitUntil(t, func() bool { return get() == http.StatusOK }, "授权后应能调通")

	// 【辨别力】收回授权不重启就生效：只有 PolicyChanged 推送能解释。
	if err := e.authz.SetRolePermission(ctx, role.ID, perm.ID, ""); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return get() == http.StatusForbidden }, "收回授权后应 403")
	if err := e.authz.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return get() == http.StatusOK }, "重新授权后应恢复")

	// 【辨别力】停用在 30 秒缓存过期之前就生效：只有 AccessKeyChanged 推送能解释。
	if _, err := e.accessKeys.SetStatus(ctx, k.ID, domain.AccessKeyStatusDisabled); err != nil {
		t.Fatal(err)
	}
	waitUntilTimeout(t, 5*time.Second, func() bool { return get() == http.StatusForbidden }, "停用后应立即 403")
}

func TestAccessKeyLastUsedReportedOnClose(t *testing.T) {
	e := newPhase2Env(t)
	_, _, k := partnerKey(t, e)
	client := e.dial(t) // 单独一个客户端，Close 时会把使用时间报掉
	waitPolicy(t, client.Authz())
	srv := newBizServer(t, client)
	waitUntil(t, func() bool {
		return callBiz(t, srv, http.MethodGet, "/orders/1", k.AccessKeyID, k.Secret) == http.StatusOK
	}, "应能调通")
	_ = client.Close()

	got, err := e.accessKeys.Get(context.Background(), k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastUsedAt == 0 || got.LastUsedAt%60_000 != 0 {
		t.Fatalf("最后使用时间应已上报且精确到分钟，got %d", got.LastUsedAt)
	}
}

func TestGuestAndAccessKeyBoundaries(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	// 测试库每次清空，迁移插入的 GUEST 不在，需要自己建。
	guest, err := e.authz.CreateRole(ctx, authzcore.GuestRoleKey, "访客", nil)
	if err != nil {
		t.Fatal(err)
	}
	view, err := e.authz.CreatePermission(ctx, e.app.ID, "GET:/orders/{id}", "查看订单", domain.PermissionKindAPI)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.authz.SetRolePermission(ctx, guest.ID, view.ID, domain.EffectAllow); err != nil {
		t.Fatal(err)
	}
	noRole, err := e.accessKeys.Create(ctx, service.CreateAccessKeyInput{Remark: "没绑角色"})
	if err != nil {
		t.Fatal(err)
	}
	srv := newBizServer(t, e.sdk)

	waitUntil(t, func() bool {
		return callBiz(t, srv, http.MethodGet, "/orders/1", "", "") == http.StatusOK
	}, "匿名请求应能调 GUEST 授权的接口")
	if code := callBiz(t, srv, http.MethodPost, "/orders", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("匿名调未授权接口应 401，got %d", code)
	}
	// 【辨别力】GUEST 开放的接口：没绑角色的 key 是 403，签错的 key 是 401，都不降级成匿名。
	if code := callBiz(t, srv, http.MethodGet, "/orders/1", noRole.AccessKeyID, noRole.Secret); code != http.StatusForbidden {
		t.Fatalf("没绑角色的 key 应 403，got %d", code)
	}
	if code := callBiz(t, srv, http.MethodGet, "/orders/1", noRole.AccessKeyID, "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("签错的 key 应 401，got %d", code)
	}
}
```

- [ ] **Step 2:** `./scripts/test.sh ./internal/integration -run 'TestAccessKeyEndToEnd|TestAccessKeyLastUsedReportedOnClose|TestGuestAndAccessKeyBoundaries'` → PASS。若失败，按失败信息回到对应任务修实现，不改测试断言。

- [ ] **Step 3: 变异验证**（spec 要求）：临时注释掉 `sdk/client.go` 里处理 `AccessKeyChanged` 的分支，确认 `TestAccessKeyEndToEnd` 在停用那一步失败；恢复。临时让 `AuthzService.announcePolicy` 直接 return，确认收回授权那一步失败；恢复。

- [ ] **Step 4: 全量回归**：`./scripts/test.sh`（只跑这一个进程）。

- [ ] **Step 5: 提交**

```bash
git add internal/integration/accesskey_e2e_test.go
git commit -m "test(integration): 访问密钥与 GUEST 的端到端验收"
```

---

### Task 12: 控制台：访问密钥列表、新建、详情

**Files:**
- Create: `web/src/lib/roles.ts`、`web/src/components/AccessKeyFields.tsx`、`web/src/components/AccessKeyDialogs.tsx`、`web/src/pages/AccessKeys.tsx`、`web/src/pages/AccessKeys.test.tsx`、`web/src/pages/AccessKeyDetail.tsx`、`web/src/pages/AccessKeyDetail.test.tsx`
- Modify: `web/src/lib/types.ts`、`web/src/lib/labels.ts`、`web/src/lib/format.ts`、`web/src/lib/breadcrumb.ts`、`web/src/lib/breadcrumb.test.ts`、`web/src/routes.tsx`、`web/src/components/Layout.tsx`

**Interfaces:**
- Consumes: Task 7 的 JSON 契约。
- Produces:
  - 类型 `AccessKeyStatus`、`AccessKeyState`、`AccessKey`、`CreateAccessKeyResponse`、`AppPermissions`
  - `accessKeyStateLabels`、`formatMinute(ms: number, fallback?: string): string`
  - `GUEST_ROLE_KEY = 'GUEST'`（Task 13 复用）
  - `AccessKeyFields.tsx`：`selectableRoles(roles: Role[]): Role[]`、`parseIps(text: string): string[]`、`RoleSelect`、`IpTextarea`
  - `AccessKeyDialogs.tsx`：`StatusConfirm`、`DeleteConfirm`（props：`target: AccessKey | null; onClose(); onDone()`）

- [ ] **Step 1: 类型与工具**

`web/src/lib/types.ts` 末尾追加：

```ts
// --- 访问密钥 -------------------------------------------------------------

export type AccessKeyStatus = 'ACTIVE' | 'DISABLED'
/** 算出来的展示状态：停用优先于过期。 */
export type AccessKeyState = 'active' | 'disabled' | 'expired'

export interface AccessKey {
  id: string
  accessKeyId: string
  remark: string
  /** 空串表示未绑定角色。 */
  roleKey: string
  allowedIps: string[]
  status: AccessKeyStatus
  state: AccessKeyState
  /** 0 表示永不过期。 */
  expiresAt: number
  /** 0 表示从未使用，精确到分钟。 */
  lastUsedAt: number
  createdAt: number
  updatedAt: number
}

export interface CreateAccessKeyResponse {
  accessKey: AccessKey
  /** SK 明文，只在创建时返回这一次。 */
  secret: string
}

/** 某个应用里这把 key 能调用的接口（角色继承已展开）。 */
export interface AppPermissions {
  appId: string
  appName: string
  points: { key: string; name: string }[]
}
```

`web/src/lib/labels.ts` 追加（import 加 `AccessKeyState`）：

```ts
/** 访问密钥展示状态的中文文案。 */
export const accessKeyStateLabels: Record<AccessKeyState, string> = {
  active: '正常',
  disabled: '已停用',
  expired: '已过期',
}
```

`web/src/lib/format.ts` 追加：

```ts
/** formatMinute 把毫秒时间戳格式化到分钟；为 0 时显示 fallback。 */
export function formatMinute(ms: number, fallback = '-'): string {
  if (!ms) return fallback
  return new Date(ms).toLocaleString(undefined, {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  })
}
```

`web/src/lib/roles.ts`：

```ts
/** 内置访客角色的 key：匿名请求只有它，登录用户判定时并入它，访问密钥不拥有它。 */
export const GUEST_ROLE_KEY = 'GUEST'
```

`web/src/lib/breadcrumb.ts` 在 `users` 分支之后加：

```ts
  if (parts[0] === 'access-keys') {
    if (parts.length === 1) return [{ label: '访问密钥' }]
    return [{ label: '访问密钥', to: '/access-keys' }, { label: '密钥详情' }]
  }
```

`web/src/lib/breadcrumb.test.ts` 追加（沿用文件已有的 import）：

```ts
test('访问密钥的面包屑', () => {
  expect(buildBreadcrumb('/access-keys')).toEqual([{ label: '访问密钥' }])
  expect(buildBreadcrumb('/access-keys/k1')).toEqual([
    { label: '访问密钥', to: '/access-keys' },
    { label: '密钥详情' },
  ])
})
```

- [ ] **Step 2: 共享组件**

`web/src/components/AccessKeyFields.tsx`：

```tsx
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { GUEST_ROLE_KEY } from '@/lib/roles'
import type { Role } from '@/lib/types'

/** 「不绑定角色」在 Select 里的占位值。base-ui 的 SelectItem 不接受空串。 */
const NO_ROLE = '__none__'

/** selectableRoles 去掉 GUEST：访问密钥不能绑定它（后端同样拒绝）。 */
export function selectableRoles(roles: Role[]): Role[] {
  return roles.filter((r) => r.key !== GUEST_ROLE_KEY)
}

/** parseIps 把多行文本拆成 IP 列表，空行忽略。 */
export function parseIps(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
}

export function RoleSelect({
  id,
  roles,
  value,
  onChange,
}: {
  id: string
  roles: Role[]
  value: string
  onChange: (v: string) => void
}) {
  return (
    <>
      <Select value={value || NO_ROLE} onValueChange={(v) => onChange(v === NO_ROLE || v === null ? '' : v)}>
        <SelectTrigger id={id} className="w-full">
          {/* 显式给出显示文字：拿不到 item 标签时 base-ui 会直接渲染 value。 */}
          <SelectValue placeholder="不绑定">{value || '不绑定'}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={NO_ROLE}>不绑定</SelectItem>
          {selectableRoles(roles).map((r) => (
            <SelectItem key={r.id} value={r.key}>
              {r.key}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="text-xs text-muted-foreground">不绑定角色的 key 调任何接口都是 403；访问密钥不拥有 GUEST。</p>
    </>
  )
}

export function IpTextarea({ id, value, onChange }: { id: string; value: string; onChange: (v: string) => void }) {
  return (
    <>
      <textarea
        id={id}
        rows={4}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={'每行一个 IP 或网段，例如\n203.0.113.7\n10.0.0.0/8'}
        className="w-full rounded-md border bg-transparent px-3 py-2 font-mono text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
      />
      <p className="text-xs text-muted-foreground">留空表示不校验 IP；最多 50 条。</p>
    </>
  )
}
```

`web/src/components/AccessKeyDialogs.tsx`：

```tsx
import { toast } from 'sonner'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/useResource'
import { formatMinute } from '@/lib/format'
import type { AccessKey } from '@/lib/types'

interface Props {
  target: AccessKey | null
  onClose: () => void
  onDone: () => void
}

/** StatusConfirm 停用或启用前的确认。显示最后使用时间，方便判断还有没有人在用。 */
export function StatusConfirm({ target, onClose, onDone }: Props) {
  const disabling = target?.status === 'ACTIVE'
  async function run(k: AccessKey) {
    try {
      await api.patch(`/access-keys/${k.id}/status`, { status: k.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE' })
      toast.success('已生效，正在推送到业务方')
      onDone()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }
  return (
    <ConfirmDialog
      open={target !== null}
      onOpenChange={(v) => !v && onClose()}
      title={disabling ? '停用访问密钥' : '启用访问密钥'}
      description={
        target
          ? `「${target.remark}」最后使用：${formatMinute(target.lastUsedAt, '从未使用')}。` +
            (disabling ? '停用后，使用这把 key 的请求会立即被拒绝。' : '启用后立即可以调用。')
          : ''
      }
      confirmLabel={disabling ? '确认停用' : '确认启用'}
      onConfirm={() => {
        const k = target
        onClose()
        if (k) void run(k)
      }}
    />
  )
}

/** DeleteConfirm 删除前的确认。删除是真删，不可恢复。 */
export function DeleteConfirm({ target, onClose, onDone }: Props) {
  async function run(k: AccessKey) {
    try {
      await api.del(`/access-keys/${k.id}`)
      toast.success('已删除')
      onDone()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }
  return (
    <ConfirmDialog
      open={target !== null}
      onOpenChange={(v) => !v && onClose()}
      title="删除访问密钥"
      description={
        target
          ? `「${target.remark}」最后使用：${formatMinute(target.lastUsedAt, '从未使用')}。` +
            '删除后，使用这把 key 的请求立即失败，且不可恢复。'
          : ''
      }
      confirmLabel="确认删除"
      onConfirm={() => {
        const k = target
        onClose()
        if (k) void run(k)
      }}
    />
  )
}
```

- [ ] **Step 3: 写失败测试** `web/src/pages/AccessKeys.test.tsx`

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import AccessKeys from './AccessKeys'
import { selectableRoles } from '@/components/AccessKeyFields'
import type { AccessKey, Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const key: AccessKey = {
  id: 'k1', accessKeyId: 'FPAK7Q2M9X4K1D8R3T6W0000', remark: '顺丰', roleKey: '合作方', allowedIps: [],
  status: 'ACTIVE', state: 'active', expiresAt: 0, lastUsedAt: 0, createdAt: 1700000000000, updatedAt: 1700000000000,
}
const roles: Role[] = [
  { id: 'r1', key: '合作方', name: '合作方', parentId: '', createdAt: 1 },
  { id: 'r2', key: 'GUEST', name: '访客', parentId: '', createdAt: 1 },
]

function stubFetch(onWrite?: (url: string, method: string, body: unknown) => unknown) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      if (method !== 'GET') {
        const out = onWrite?.(url, method, init?.body ? JSON.parse(String(init.body)) : undefined)
        return Promise.resolve(
          out === undefined ? new Response(null, { status: 204 }) : new Response(JSON.stringify(out), { status: 201 }),
        )
      }
      if (url === '/admin/api/access-keys') return Promise.resolve(new Response(JSON.stringify([key]), { status: 200 }))
      if (url === '/admin/api/roles') return Promise.resolve(new Response(JSON.stringify(roles), { status: 200 }))
      return Promise.reject(new Error(`没准备 ${method} ${url}`))
    }),
  )
}

const renderPage = () =>
  render(
    <MemoryRouter>
      <AccessKeys />
    </MemoryRouter>,
  )

async function openCreateAndSubmit(remark: string) {
  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '新建访问密钥' }))
  fireEvent.change(await waitFor(() => screen.getByLabelText('备注')), { target: { value: remark } })
}

test('列表显示状态、未使用、永不过期与不限制 IP', async () => {
  stubFetch()
  renderPage()
  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())
  const row = screen.getByText('顺丰').closest('tr')!
  for (const text of ['正常', '从未使用', '永不过期', '不限制', '合作方']) {
    expect(row.textContent).toContain(text)
  }
})

test('新建提交备注、有效期与按行拆开的 IP，并展示一次性密钥', async () => {
  const bodies: unknown[] = []
  stubFetch((_u, _m, body) => {
    bodies.push(body)
    return { accessKey: { ...key, id: 'k2' }, secret: 'SECRET-ONCE' }
  })
  renderPage()
  await openCreateAndSubmit('圆通')
  fireEvent.change(screen.getByLabelText('有效期（天）'), { target: { value: '30' } })
  fireEvent.change(screen.getByLabelText('IP 白名单'), { target: { value: '1.2.3.4\n\n10.0.0.0/8\n' } })
  fireEvent.click(screen.getByRole('button', { name: '创建' }))

  await waitFor(() => expect(bodies.length).toBe(1))
  expect(bodies[0]).toEqual({ remark: '圆通', roleKey: '', validDays: 30, allowedIps: ['1.2.3.4', '10.0.0.0/8'] })
  await waitFor(() => expect(screen.getByText('SECRET-ONCE')).toBeTruthy())
})

// 【辨别力】一次性密钥弹窗按 Esc 关不掉，只有「我已保存」能关——关掉 SK 就再也找不回来了。
test('一次性密钥弹窗只能点「我已保存」关闭', async () => {
  stubFetch(() => ({ accessKey: { ...key, id: 'k2' }, secret: 'SECRET-ONCE' }))
  renderPage()
  await openCreateAndSubmit('圆通')
  fireEvent.click(screen.getByRole('button', { name: '创建' }))
  await waitFor(() => expect(screen.getByText('SECRET-ONCE')).toBeTruthy())

  fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
  await new Promise((r) => setTimeout(r, 50))
  expect(screen.queryByText('SECRET-ONCE')).toBeTruthy()

  fireEvent.click(screen.getByRole('button', { name: '我已保存' }))
  await waitFor(() => expect(screen.queryByText('SECRET-ONCE')).toBeNull())
})

test('角色下拉不提供 GUEST', () => {
  expect(selectableRoles(roles).map((r) => r.key)).toEqual(['合作方'])
})
```

`web/src/pages/AccessKeyDetail.test.tsx`

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import AccessKeyDetail from './AccessKeyDetail'
import type { AccessKey, AppPermissions, Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const key: AccessKey = {
  id: 'k1', accessKeyId: 'FPAK7Q2M9X4K1D8R3T6W0000', remark: '顺丰', roleKey: '合作方', allowedIps: ['1.2.3.4/32'],
  status: 'ACTIVE', state: 'active', expiresAt: 1800000000000, lastUsedAt: 1790000000000,
  createdAt: 1700000000000, updatedAt: 1700000000000,
}
const perms: AppPermissions[] = [{ appId: 'a1', appName: '商城', points: [{ key: 'GET:/orders/{id}', name: '查看订单' }] }]
const roles: Role[] = [{ id: 'r1', key: '合作方', name: '合作方', parentId: '', createdAt: 1 }]

function stubFetch(onWrite?: (url: string, method: string, body: Record<string, unknown>) => void) {
  const data: Record<string, unknown> = {
    '/admin/api/access-keys/k1': key,
    '/admin/api/access-keys/k1/permissions': perms,
    '/admin/api/roles': roles,
  }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      if (method !== 'GET') {
        onWrite?.(url, method, init?.body ? JSON.parse(String(init.body)) : {})
        return Promise.resolve(new Response(null, { status: 204 }))
      }
      if (url in data) return Promise.resolve(new Response(JSON.stringify(data[url]), { status: 200 }))
      return Promise.reject(new Error(`没准备 ${method} ${url}`))
    }),
  )
}

const renderDetail = () =>
  render(
    <MemoryRouter initialEntries={['/access-keys/k1']}>
      <Routes>
        <Route path="/access-keys/:id" element={<AccessKeyDetail />} />
      </Routes>
    </MemoryRouter>,
  )

test('按应用分组展示可调用的接口', async () => {
  stubFetch()
  renderDetail()
  await waitFor(() => expect(screen.getByText('商城')).toBeTruthy())
  expect(screen.getByText('GET:/orders/{id}')).toBeTruthy()
  expect(screen.getByText('查看订单')).toBeTruthy()
})

// 【辨别力】有效期留空时 PATCH 请求体里没有 validDays；填了才带上。
test('编辑时有效期留空不发送 validDays', async () => {
  const bodies: Record<string, unknown>[] = []
  stubFetch((_u, method, body) => {
    if (method === 'PATCH') bodies.push(body)
  })
  renderDetail()
  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '编辑' }))
  fireEvent.change(await waitFor(() => screen.getByLabelText('备注')), { target: { value: '顺丰速运' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(bodies.length).toBe(1))
  expect(bodies[0]).not.toHaveProperty('validDays')
  expect(bodies[0].remark).toBe('顺丰速运')

  fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '编辑' })))
  fireEvent.change(await waitFor(() => screen.getByLabelText('重新设置有效期（天）')), { target: { value: '30' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(bodies.length).toBe(2))
  expect(bodies[1].validDays).toBe(30)
})

test('删除确认框显示最后使用时间', async () => {
  stubFetch()
  renderDetail()
  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '删除' }))
  const desc = await waitFor(() => screen.getByText(/最后使用：/))
  expect(desc.textContent).not.toContain('从未使用')
})
```

- [ ] **Step 4:** `cd web && npx vitest run src/pages/AccessKeys.test.tsx src/pages/AccessKeyDetail.test.tsx src/lib/breadcrumb.test.ts` → FAIL（页面不存在）。

- [ ] **Step 5: 实现页面**

`web/src/pages/AccessKeys.tsx`：

```tsx
import { useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { IpTextarea, RoleSelect, parseIps } from '@/components/AccessKeyFields'
import { DeleteConfirm, StatusConfirm } from '@/components/AccessKeyDialogs'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatMinute } from '@/lib/format'
import { accessKeyStateLabels } from '@/lib/labels'
import type { AccessKey, CreateAccessKeyResponse, Role } from '@/lib/types'

export default function AccessKeys() {
  const [sp, setSp] = useSearchParams()
  const role = sp.get('role') ?? ''
  const keys = useResource(
    () => api.get<AccessKey[]>(role ? `/access-keys?roleKey=${encodeURIComponent(role)}` : '/access-keys'),
    [role],
  )
  const roles = useResource(() => api.get<Role[]>('/roles'), [])
  const navigate = useNavigate()
  const [keyword, setKeyword] = useState('')
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<CreateAccessKeyResponse | null>(null)
  const [toggling, setToggling] = useState<AccessKey | null>(null)
  const [deleting, setDeleting] = useState<AccessKey | null>(null)

  const kw = keyword.trim().toLowerCase()
  const list = (keys.data ?? []).filter(
    (k) => !kw || k.accessKeyId.toLowerCase().includes(kw) || k.remark.toLowerCase().includes(kw),
  )

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">访问密钥</h1>
        <Button onClick={() => setCreating(true)}>新建访问密钥</Button>
      </div>
      <p className="text-sm text-muted-foreground">
        给第三方程序签名调用业务方接口。key 是<strong>全局</strong>的，能调哪些接口完全由绑定的角色决定。
      </p>

      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          placeholder="按 AccessKey 或备注筛选"
          className="w-72"
        />
        {role && (
          <Button variant="outline" size="sm" onClick={() => setSp(new URLSearchParams())}>
            只看角色「{role}」 ✕
          </Button>
        )}
      </div>

      {keys.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {keys.error && <p className="text-sm text-destructive">{keys.error}</p>}
      {keys.data && (
        <div className="overflow-x-auto rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>AccessKey</TableHead>
                <TableHead>备注</TableHead>
                <TableHead>角色</TableHead>
                <TableHead>IP 白名单</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>到期时间</TableHead>
                <TableHead>最后使用</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.length === 0 && (
                <TableRow>
                  <TableCell colSpan={8} className="text-center text-muted-foreground">
                    没有访问密钥
                  </TableCell>
                </TableRow>
              )}
              {list.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-mono text-xs">
                    <Link to={`/access-keys/${k.id}`} className="underline-offset-4 hover:underline">
                      {k.accessKeyId}
                    </Link>
                  </TableCell>
                  <TableCell>{k.remark}</TableCell>
                  <TableCell className="text-muted-foreground">{k.roleKey || '未绑定'}</TableCell>
                  <TableCell className="text-muted-foreground">
                    {k.allowedIps.length === 0 ? '不限制' : `${k.allowedIps.length} 条`}
                  </TableCell>
                  <TableCell>
                    <Badge variant={k.state === 'active' ? 'default' : 'secondary'}>
                      {accessKeyStateLabels[k.state] ?? k.state}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{formatMinute(k.expiresAt, '永不过期')}</TableCell>
                  <TableCell className="text-muted-foreground">{formatMinute(k.lastUsedAt, '从未使用')}</TableCell>
                  <TableCell className="space-x-2 text-right">
                    <Button variant="outline" size="sm" onClick={() => navigate(`/access-keys/${k.id}`)}>
                      编辑
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => setToggling(k)}>
                      {k.status === 'ACTIVE' ? '停用' : '启用'}
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => setDeleting(k)}>
                      删除
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <CreateDialog
        open={creating}
        onOpenChange={setCreating}
        roles={roles.data ?? []}
        onCreated={(res) => {
          setCreating(false)
          setCreated(res)
          keys.reload()
        }}
      />
      <SecretDialog value={created} onClose={() => setCreated(null)} />
      <StatusConfirm target={toggling} onClose={() => setToggling(null)} onDone={keys.reload} />
      <DeleteConfirm target={deleting} onClose={() => setDeleting(null)} onDone={keys.reload} />
    </div>
  )
}

function CreateDialog({
  open,
  onOpenChange,
  roles,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  roles: Role[]
  onCreated: (res: CreateAccessKeyResponse) => void
}) {
  const [remark, setRemark] = useState('')
  const [roleKey, setRoleKey] = useState('')
  const [validDays, setValidDays] = useState('0')
  const [ips, setIps] = useState('')
  const [saving, setSaving] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!remark.trim()) {
      toast.error('请输入备注')
      return
    }
    const days = Number(validDays)
    if (!Number.isInteger(days) || days < 0) {
      toast.error('有效期必须是不小于 0 的整数')
      return
    }
    setSaving(true)
    try {
      const res = await api.post<CreateAccessKeyResponse>('/access-keys', {
        remark: remark.trim(),
        roleKey,
        validDays: days,
        allowedIps: parseIps(ips),
      })
      setRemark('')
      setRoleKey('')
      setValidDays('0')
      setIps('')
      onCreated(res)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建访问密钥</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="ak-remark">备注</Label>
            <Input id="ak-remark" value={remark} onChange={(e) => setRemark(e.target.value)} placeholder="哪个合作方在用" />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ak-role">角色</Label>
            <RoleSelect id="ak-role" roles={roles} value={roleKey} onChange={setRoleKey} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ak-days">有效期（天）</Label>
            <Input id="ak-days" type="number" min={0} value={validDays} onChange={(e) => setValidDays(e.target.value)} />
            <p className="text-xs text-muted-foreground">0 表示永不过期。</p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="ak-ips">IP 白名单</Label>
            <IpTextarea id="ak-ips" value={ips} onChange={setIps} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={saving}>
              创建
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * SecretDialog 展示刚创建的 AK 与 SK。SK 只在创建响应里出现这一次，所以与
 * Applications.tsx 的同名弹窗一样：onOpenChange 不响应任何内部关闭请求（Esc、X 按钮），
 * disablePointerDismissal 拦掉点遮罩，只能点「我已保存」关闭。
 */
function SecretDialog({ value, onClose }: { value: CreateAccessKeyResponse | null; onClose: () => void }) {
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      toast.success('已复制')
    } catch {
      toast.error('复制失败，请手动选中复制')
    }
  }
  return (
    <Dialog open={value !== null} onOpenChange={() => {}} disablePointerDismissal>
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>访问密钥已创建</DialogTitle>
        </DialogHeader>
        {value && (
          <div className="space-y-3">
            {[
              ['AccessKey ID', value.accessKey.accessKeyId],
              ['AccessKey Secret', value.secret],
            ].map(([label, text]) => (
              <div key={label} className="space-y-1">
                <Label>{label}</Label>
                <div className="flex items-center gap-2">
                  <div className="flex-1 rounded-md border bg-muted/40 p-2 font-mono text-sm break-all">{text}</div>
                  <Button variant="outline" size="sm" onClick={() => void copy(text)}>
                    复制
                  </Button>
                </div>
              </div>
            ))}
            <p className="text-sm text-destructive">Secret 只显示这一次，关闭后无法再查看。请立刻复制并交给合作方。</p>
            <p className="text-xs text-muted-foreground">签名规则见仓库里的 docs/access-key.md。</p>
          </div>
        )}
        <DialogFooter>
          <Button onClick={onClose}>我已保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
```

`web/src/pages/AccessKeyDetail.tsx`：

```tsx
import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { IpTextarea, RoleSelect, parseIps } from '@/components/AccessKeyFields'
import { DeleteConfirm, StatusConfirm } from '@/components/AccessKeyDialogs'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatMinute } from '@/lib/format'
import { accessKeyStateLabels } from '@/lib/labels'
import type { AccessKey, AppPermissions, Role } from '@/lib/types'

export default function AccessKeyDetail() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const key = useResource(() => api.get<AccessKey>(`/access-keys/${id}`), [id])
  const perms = useResource(() => api.get<AppPermissions[]>(`/access-keys/${id}/permissions`), [id])
  const roles = useResource(() => api.get<Role[]>('/roles'), [])
  const [editing, setEditing] = useState(false)
  const [toggling, setToggling] = useState<AccessKey | null>(null)
  const [deleting, setDeleting] = useState<AccessKey | null>(null)

  if (key.loading && !key.data) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (key.error) return <p className="text-sm text-destructive">{key.error}</p>
  const k = key.data
  if (!k) return null
  const roleId = roles.data?.find((r) => r.key === k.roleKey)?.id

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <Link to="/access-keys" className="text-sm text-muted-foreground underline-offset-4 hover:underline">
          ← 访问密钥
        </Link>
        <h1 className="font-mono text-lg font-semibold">{k.accessKeyId}</h1>
        <Badge variant={k.state === 'active' ? 'default' : 'secondary'}>{accessKeyStateLabels[k.state] ?? k.state}</Badge>
        <div className="ml-auto space-x-2">
          <Button variant="outline" onClick={() => setEditing(true)}>
            编辑
          </Button>
          <Button variant="outline" onClick={() => setToggling(k)}>
            {k.status === 'ACTIVE' ? '停用' : '启用'}
          </Button>
          <Button variant="outline" onClick={() => setDeleting(k)}>
            删除
          </Button>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">基本信息</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid grid-cols-[8rem_1fr] gap-y-2 text-sm">
            <dt className="text-muted-foreground">备注</dt>
            <dd>{k.remark}</dd>
            <dt className="text-muted-foreground">角色</dt>
            <dd>
              {!k.roleKey ? (
                '未绑定'
              ) : roleId ? (
                <Link to={`/roles/${roleId}`} className="underline-offset-4 hover:underline">
                  {k.roleKey}
                </Link>
              ) : (
                k.roleKey
              )}
            </dd>
            <dt className="text-muted-foreground">IP 白名单</dt>
            <dd className="font-mono text-xs">{k.allowedIps.length === 0 ? '不限制' : k.allowedIps.join('、')}</dd>
            <dt className="text-muted-foreground">到期时间</dt>
            <dd>{formatMinute(k.expiresAt, '永不过期')}</dd>
            <dt className="text-muted-foreground">最后使用</dt>
            <dd>{formatMinute(k.lastUsedAt, '从未使用')}</dd>
            <dt className="text-muted-foreground">创建时间</dt>
            <dd>{formatMinute(k.createdAt)}</dd>
          </dl>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">可调用的接口</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-xs text-muted-foreground">
            按应用列出这把 key 当前能调用的接口（角色继承已展开）。给绑定的角色在任何应用增加授权，都会出现在这里。
          </p>
          {perms.error && <p className="text-sm text-destructive">{perms.error}</p>}
          {perms.data?.length === 0 && (
            <p className="text-sm text-muted-foreground">没有可调用的接口（未绑定角色，或角色没有任何授权）。</p>
          )}
          {perms.data?.map((g) => (
            <div key={g.appId} className="space-y-1">
              <h3 className="text-sm font-medium">{g.appName}</h3>
              <ul className="space-y-1">
                {g.points.map((p) => (
                  <li key={p.key} className="flex gap-3 text-sm">
                    <span className="font-mono text-xs">{p.key}</span>
                    {p.name && <span className="text-muted-foreground">{p.name}</span>}
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </CardContent>
      </Card>

      {editing && (
        <EditDialog
          k={k}
          roles={roles.data ?? []}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            key.reload()
            perms.reload()
          }}
        />
      )}
      <StatusConfirm target={toggling} onClose={() => setToggling(null)} onDone={key.reload} />
      <DeleteConfirm target={deleting} onClose={() => setDeleting(null)} onDone={() => navigate('/access-keys')} />
    </div>
  )
}

function EditDialog({
  k,
  roles,
  onClose,
  onSaved,
}: {
  k: AccessKey
  roles: Role[]
  onClose: () => void
  onSaved: () => void
}) {
  const [remark, setRemark] = useState(k.remark)
  const [roleKey, setRoleKey] = useState(k.roleKey)
  const [ips, setIps] = useState(k.allowedIps.join('\n'))
  const [validDays, setValidDays] = useState('')
  const [saving, setSaving] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const body: Record<string, unknown> = { remark: remark.trim(), roleKey, allowedIps: parseIps(ips) }
    // 有效期留空表示不改：只改备注时不能把到期时间顺手重算。
    if (validDays.trim() !== '') {
      const days = Number(validDays)
      if (!Number.isInteger(days) || days < 0) {
        toast.error('有效期必须是不小于 0 的整数')
        return
      }
      body.validDays = days
    }
    setSaving(true)
    try {
      await api.patch(`/access-keys/${k.id}`, body)
      toast.success('已生效，正在推送到业务方')
      onSaved()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>编辑访问密钥</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="edit-remark">备注</Label>
            <Input id="edit-remark" value={remark} onChange={(e) => setRemark(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-role">角色</Label>
            <RoleSelect id="edit-role" roles={roles} value={roleKey} onChange={setRoleKey} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-days">重新设置有效期（天）</Label>
            <Input
              id="edit-days"
              type="number"
              min={0}
              value={validDays}
              onChange={(e) => setValidDays(e.target.value)}
              placeholder="留空表示不修改"
            />
            <p className="text-xs text-muted-foreground">
              当前到期时间：{formatMinute(k.expiresAt, '永不过期')}。填 0 改为永不过期，填 N 从现在起算 N 天。
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-ips">IP 白名单</Label>
            <IpTextarea id="edit-ips" value={ips} onChange={setIps} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              取消
            </Button>
            <Button type="submit" disabled={saving}>
              保存
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
```

`web/src/routes.tsx`：import `AccessKeys`、`AccessKeyDetail`，在 `/users/:id` 之后加

```tsx
        <Route path="/access-keys" element={<AccessKeys />} />
        <Route path="/access-keys/:id" element={<AccessKeyDetail />} />
```

`web/src/components/Layout.tsx`：lucide import 加 `Key`，`nav` 在「用户管理」之后插入 `{ to: '/access-keys', label: '访问密钥', icon: Key }`。

- [ ] **Step 6:** 重跑 Step 4 → PASS；`cd web && npx vitest run && npm run build`。

- [ ] **Step 7: 提交**

```bash
git add web/src/lib/types.ts web/src/lib/labels.ts web/src/lib/format.ts web/src/lib/roles.ts web/src/lib/breadcrumb.ts web/src/lib/breadcrumb.test.ts web/src/components/AccessKeyFields.tsx web/src/components/AccessKeyDialogs.tsx web/src/pages/AccessKeys.tsx web/src/pages/AccessKeys.test.tsx web/src/pages/AccessKeyDetail.tsx web/src/pages/AccessKeyDetail.test.tsx web/src/routes.tsx web/src/components/Layout.tsx
git commit -m "feat(web): 访问密钥列表、新建（一次性展示密钥）与详情页"
```

---

### Task 13: 控制台：角色页与用户角色卡片

**Files:**
- Modify: `web/src/pages/Roles.tsx`、`web/src/pages/Roles.test.tsx`、`web/src/pages/RoleDetail.tsx`、`web/src/components/UserRolesCard.tsx`
- Create: `web/src/pages/RoleDetail.guest.test.tsx`、`web/src/components/UserRolesCard.guest.test.tsx`

**Interfaces:**
- Consumes: Task 12 的 `GUEST_ROLE_KEY`、`AccessKey` 类型；Task 7 的 `GET /admin/api/access-keys?roleKey=`；Task 4 的 `ROLE_IN_USE` 文案。

- [ ] **Step 1: 写失败测试**

`web/src/pages/Roles.test.tsx` 追加（沿用文件里的 `stubFetch`、`renderRoles`、`roles`；import 加 `import { Toaster } from 'sonner'` 与 `Roles`）：

```tsx
test('GUEST 标「内置」且删除按钮不可用', async () => {
  stubFetch([...roles, { id: 'g', key: 'GUEST', name: '访客', parentId: '', createdAt: 1700000000000 }])
  renderRoles()
  const row = (await waitFor(() => screen.getByRole('link', { name: 'GUEST' }))).closest('tr')!
  expect(row.textContent).toContain('内置')
  const del = Array.from(row.querySelectorAll('button')).find((b) => b.textContent === '删除') as HTMLButtonElement
  expect(del.disabled).toBe(true)
})

test('删除被访问密钥绑定的角色时提示绑定数量', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn((_url: string, init?: RequestInit) =>
      Promise.resolve(
        (init?.method ?? 'GET') === 'DELETE'
          ? new Response(JSON.stringify({ code: 'ROLE_IN_USE', msg: '有 2 把访问密钥绑定了该角色，请先改绑或删除这些密钥' }), { status: 409 })
          : new Response(JSON.stringify(roles), { status: 200 }),
      ),
    ),
  )
  render(
    <MemoryRouter>
      <Roles />
      <Toaster />
    </MemoryRouter>,
  )
  const row = (await waitFor(() => screen.getByRole('link', { name: '商城管理员' }))).closest('tr')!
  fireEvent.click(Array.from(row.querySelectorAll('button')).find((b) => b.textContent === '删除')!)
  fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '确认删除' })))
  expect(await screen.findByText(/2 把访问密钥/)).toBeTruthy()
})
```

`web/src/pages/RoleDetail.guest.test.tsx`：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import RoleDetail from './RoleDetail'
import { disabledIMConfig } from '@/lib/testFixtures'
import type { AccessKey, Application, PermissionPoint, Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const app: Application = {
  id: 'a1', name: '商城', slug: 'mall', appId: 'app1', status: 'ACTIVE', cookieDomain: '', defaultRoleKey: '',
  session: { idleTimeoutSeconds: 1, idleTimeoutMobileSeconds: 1, maxLifetimeSeconds: 1, rotateIntervalSeconds: 1, extendIntervalSeconds: 1, tokenCacheTtlSeconds: 1 },
  im: disabledIMConfig, createdAt: 1, updatedAt: 1,
}
const perm: PermissionPoint = {
  id: 'p1', key: 'GET:/orders/{id}', name: '查看订单', kind: 'api', source: 'app', status: 'normal',
  staleForMs: 0, lastSeenAt: 1, createdAt: 1,
}
const guest: Role = { id: 'g', key: 'GUEST', name: '访客', parentId: '', createdAt: 1 }
const partner: Role = { id: 'r1', key: '合作方', name: '合作方', parentId: '', createdAt: 1 }

function stub(keys: AccessKey[]) {
  const data: Record<string, unknown> = {
    '/admin/api/roles': [guest, partner],
    '/admin/api/applications': [app],
    '/admin/api/applications/a1/permissions': [perm],
  }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (/^\/admin\/api\/roles\/[^/]+\/permissions$/.test(url)) {
        return Promise.resolve(new Response(JSON.stringify({ grants: [] }), { status: 200 }))
      }
      if (url.startsWith('/admin/api/access-keys?roleKey=')) {
        return Promise.resolve(new Response(JSON.stringify(keys), { status: 200 }))
      }
      if (url in data) return Promise.resolve(new Response(JSON.stringify(data[url]), { status: 200 }))
      return Promise.reject(new Error(`没准备 ${url}`))
    }),
  )
}

const renderAt = (id: string) =>
  render(
    <MemoryRouter initialEntries={[`/roles/${id}?app=a1`]}>
      <Routes>
        <Route path="/roles/:id" element={<RoleDetail />} />
      </Routes>
    </MemoryRouter>,
  )

test('绑定了访问密钥的角色提示数量并链接到筛选后的列表', async () => {
  const k = { id: 'k1' } as AccessKey
  stub([k, { ...k, id: 'k2' }])
  renderAt('r1')
  expect(await screen.findByText(/绑定了 2 把访问密钥/)).toBeTruthy()
  expect(screen.getByRole('link', { name: '查看这些密钥' }).getAttribute('href')).toBe(
    `/access-keys?role=${encodeURIComponent('合作方')}`,
  )
})

test('GUEST 的授权只有「未授权 / 允许」，普通角色有「拒绝」', async () => {
  stub([])
  const { unmount } = renderAt('g')
  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  expect(screen.queryByRole('button', { name: '拒绝' })).toBeNull()
  expect(screen.getByText(/GUEST 是内置角色/)).toBeTruthy()
  unmount()

  renderAt('r1')
  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  expect(screen.getByRole('button', { name: '拒绝' })).toBeTruthy()
})
```

`web/src/components/UserRolesCard.guest.test.tsx`：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import UserRolesCard from './UserRolesCard'

afterEach(() => vi.unstubAllGlobals())

test('说明所有用户自动拥有 GUEST', async () => {
  const data: Record<string, unknown> = {
    '/admin/api/users/u1/roles': { roles: [] },
    '/admin/api/roles': [],
    '/admin/api/applications': [],
  }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) =>
      url in data ? Promise.resolve(new Response(JSON.stringify(data[url]), { status: 200 })) : Promise.reject(new Error(url)),
    ),
  )
  render(<UserRolesCard userId="u1" />)
  expect(await screen.findByText(/自动拥有内置角色/)).toBeTruthy()
})
```

- [ ] **Step 2:** `cd web && npx vitest run src/pages/Roles.test.tsx src/pages/RoleDetail.guest.test.tsx src/components/UserRolesCard.guest.test.tsx` → FAIL。

- [ ] **Step 3: 实现**

`web/src/pages/Roles.tsx`（import `Badge` 与 `GUEST_ROLE_KEY`）：
- 标识列的 `<Link>` 之后加 `{r.key === GUEST_ROLE_KEY && <Badge variant="secondary" className="ml-2">内置</Badge>}`（放在 Link 外面，链接名保持为 key）。
- 删除按钮加 `disabled={r.key === GUEST_ROLE_KEY}`。
- `EditDialog` 的「继承自」`Select` 加 `disabled={role.key === GUEST_ROLE_KEY}`，GUEST 时在下方显示 `<p className="text-xs text-muted-foreground">内置角色 GUEST 不能设置父角色。</p>`。
- 删除确认框 `description` 末尾补一句：`有访问密钥绑定时无法删除，需要先改绑或删除这些密钥。`

`web/src/pages/RoleDetail.tsx`（import `GUEST_ROLE_KEY`、`AccessKey` 类型）：
- 在所有提前 `return` 之前加：

```tsx
  const roleKey = role.data?.find((x) => x.id === id)?.key ?? ''
  const bound = useResource(
    () =>
      roleKey
        ? api.get<AccessKey[]>(`/access-keys?roleKey=${encodeURIComponent(roleKey)}`)
        : Promise.resolve([] as AccessKey[]),
    [roleKey],
  )
```

- 标题区之后加两段提示：

```tsx
      {(bound.data?.length ?? 0) > 0 && (
        <p className="rounded-md border bg-muted/30 p-3 text-sm">
          这个角色绑定了 {bound.data!.length} 把访问密钥，这里的授权改动会立即作用到这些第三方。{' '}
          <Link to={`/access-keys?role=${encodeURIComponent(r.key)}`} className="underline underline-offset-4">
            查看这些密钥
          </Link>
        </p>
      )}
      {r.key === GUEST_ROLE_KEY && (
        <p className="rounded-md border bg-muted/30 p-3 text-sm text-muted-foreground">
          GUEST 是内置角色：未登录的请求和所有登录用户都拥有它，访问密钥不拥有它。只能配置「允许」——配「拒绝」会连带拒掉所有登录用户。
        </p>
      )}
```

- `GrantTable` 加 prop `allowDeny: boolean` 并传给 `EffectPicker`；`EffectPicker` 渲染 `options.filter((o) => allowDeny || o.value !== 'deny')`；`RoleDetail` 里传 `allowDeny={r.key !== GUEST_ROLE_KEY}`。

`web/src/components/UserRolesCard.tsx`（import `GUEST_ROLE_KEY`）：
- `available` 过滤掉 GUEST：`.filter((r) => !current.includes(r.key) && r.key !== GUEST_ROLE_KEY)`。
- 默认角色说明之后加：

```tsx
        <p className="text-xs text-muted-foreground">
          所有用户还自动拥有内置角色 <span className="font-mono">GUEST</span>（未登录的请求也有它），不需要分配。
        </p>
```

- [ ] **Step 4:** 重跑 Step 2 → PASS；`cd web && npx vitest run && npm run build`。

- [ ] **Step 5: 提交**

```bash
git add web/src/pages/Roles.tsx web/src/pages/Roles.test.tsx web/src/pages/RoleDetail.tsx web/src/pages/RoleDetail.guest.test.tsx web/src/components/UserRolesCard.tsx web/src/components/UserRolesCard.guest.test.tsx
git commit -m "feat(web): GUEST 内置角色的展示与限制；角色详情提示绑定的访问密钥"
```

---

### Task 14: 对接文档、SDK 文档与 demo

**Files:**
- Create: `docs/access-key.md`、`sdk/aksign/doc_vector_test.go`、`examples/demo/partner/main.go`
- Modify: `sdk/README.md`、`docs/console.md`、`examples/demo/main.go`、`examples/demo/README.md`

**Interfaces:**
- Consumes: Task 1 的 `vectors`、`vecSecret`；Task 8~10 的 SDK 行为。

- [ ] **Step 1: 写失败测试** `sdk/aksign/doc_vector_test.go`

```go
package aksign

import (
	"os"
	"strings"
	"testing"
)

// TestDocVectorsMatch 核对 docs/access-key.md 里的测试向量与实现一致：文档和代码不能各说各的。
func TestDocVectorsMatch(t *testing.T) {
	raw, err := os.ReadFile("../../docs/access-key.md")
	if err != nil {
		t.Fatalf("读取对接文档: %v", err)
	}
	doc := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.Contains(doc, vecSecret) {
		t.Error("文档里缺少测试向量用的 SK")
	}
	for _, v := range vectors {
		if !strings.Contains(doc, v.sts) || !strings.Contains(doc, v.sig) {
			t.Errorf("文档里缺少或写错了向量「%s」的待签名串或签名", v.name)
		}
	}
}
```

- [ ] **Step 2:** `./scripts/test.sh ./sdk/aksign -run TestDocVectorsMatch` → FAIL（文档不存在）。

- [ ] **Step 3: 对接文档** `docs/access-key.md`，内容如下：

~~~markdown
# 访问密钥（AccessKey）对接文档

第三方程序调用业务方接口时不登录账号，改用 fp 控制台发放的 AccessKey ID（AK）与 AccessKey Secret（SK）给请求签名。

## 1. 拿到密钥

业务方管理员在 fp 控制台「访问密钥」里新建：填备注，绑定角色，设置有效期与 IP 白名单。创建成功的弹窗里显示 AK 与 SK，**SK 只显示这一次**。

## 2. 每个请求带 4 个请求头

| 请求头 | 值 |
|---|---|
| `X-Fp-Access-Key` | AK |
| `X-Fp-Timestamp` | 当前 Unix 时间（秒），与服务器相差不超过 15 分钟 |
| `X-Fp-Nonce` | 每个请求都不同的随机串，只能包含字母、数字、`-`、`_`，最长 64 位，建议用 UUID |
| `X-Fp-Signature` | 签名，小写十六进制 |

## 3. 计算签名

把下面 6 项用换行符 `\n` 连起来（最后一项后面没有换行）：

1. HTTP 方法，如 `POST`
2. 路径：与请求行里发出去的**完全一致**，不解码、不重新编码
3. query：`?` 后面的部分，原样，不排序；没有 query 就是空串
4. 与 `X-Fp-Timestamp` 相同的时间戳
5. 与 `X-Fp-Nonce` 相同的 nonce
6. 请求体的 SHA-256（小写十六进制）；没有请求体时是 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`

签名 = `hex(HMAC-SHA256(SK, 拼出的字符串))`。HMAC 的密钥就是 SK 字符串本身的字节，**不做 base64 解码**。请求头不参与签名。

## 4. 测试向量

SK：`ZmFrZS1zZWNyZXQtZm9yLWRvY3MtZXhhbXBsZS0wMDE`，时间戳：`1789200000`。

**向量 1**：`POST /api/v1/orders?source=partner`，nonce `3f2b8c1e-7d4a-4e1b-9c55-2a6f0d8e9b17`，请求体 `{"sku":"A100","qty":2}`。

待签名串：

```
POST
/api/v1/orders
source=partner
1789200000
3f2b8c1e-7d4a-4e1b-9c55-2a6f0d8e9b17
eac3608933ecc3a8767f2d0966e808202214fa80644296ed3b7a0286dae3c383
```

签名：`a3d533065c49e9a9888c0f6e8c1b41c9d70316891411e001ec73ef12f464f3c4`

**向量 2**：`GET /api/v1/orders/a%2Fb`，没有 query、没有请求体，nonce `n-0001`。

待签名串（第 3 行是空行）：

```
GET
/api/v1/orders/a%2Fb

1789200000
n-0001
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

签名：`46b32c81d96b8e44e56e152af7bb2e6a6485968fc9bac34df180b2fa299ba8f8`

## 5. Go 示例

`github.com/basicfu/fp/sdk/aksign` 只依赖标准库：

```go
req, _ := http.NewRequest(http.MethodPost, "https://biz.example.com/api/v1/orders?source=partner", body)
req.Header.Set("Content-Type", "application/json")
if err := aksign.Sign(req, accessKeyID, secret); err != nil {
	return err
}
resp, err := http.DefaultClient.Do(req)
```

## 6. 错误码

失败时响应体为 `{"code": "...", "msg": "..."}`。

| 状态码 | code | 含义与处理 |
|---|---|---|
| 401 | `SIGNATURE_INVALID` | 缺少签名请求头，或格式不对 |
| 401 | `TIMESTAMP_EXPIRED` | 时间戳与服务器相差超过 15 分钟，检查本机时间 |
| 401 | `ACCESS_KEY_INVALID` | AK 不存在 |
| 401 | `SIGNATURE_MISMATCH` | 签名不对。响应体的 `stringToSign` 是服务端算出的待签名串，逐行对照找差异 |
| 401 | `NONCE_USED` | nonce 已经用过；每个请求（包括重试）都要换新的 nonce |
| 403 | `ACCESS_KEY_DISABLED` / `ACCESS_KEY_EXPIRED` | key 已停用 / 已过期，联系业务方 |
| 403 | `IP_DENIED` | 来源 IP 不在白名单 |
| 403 | 无 | 这把 key 的角色没有这个接口的权限 |
| 413 | `BODY_TOO_LARGE` | 请求体超过上限（默认 10MB） |
| 503 | 无 | 业务方暂时无法校验，稍后重试 |

## 7. 业务方的部署要求

- 最外层代理必须用真实客户端地址**覆盖** `X-Forwarded-For`（nginx：`proxy_set_header X-Forwarded-For $remote_addr;`），不能追加。SDK 取它的第一个地址判断 IP 白名单。
- 中间网关不能改写路径与 query，否则签名必然失败。
- 所有路由都要挂鉴权：认证中间件会放行签名正确的访问密钥请求与匿名请求，能调什么由鉴权决定。
- 先发布 fp，再升级业务方的 SDK。

## 8. 已接受的限制

- nonce 只在业务方单个实例内去重，同一个请求重放到另一个实例能通过。
- SK 缓存在业务方 SDK 的进程内存里；fp 数据库里的 SK 目前是明文存储。
- 请求头不在签名范围内，业务逻辑不要依赖第三方传来的自定义请求头做安全判断。
~~~

- [ ] **Step 4: SDK README**（`sdk/README.md`）
  - 「快速开始」与「中间件接入」里的 `/api/me` 示例，取身份后加：

```go
    if id.IsAnonymous() {
        fpsdk.WriteError(w, fpsdk.ErrNoToken)
        return
    }
```

  - `Identity` 字段表补 `AccessKeyID` / `AccessKeyRemark`（访问密钥请求才有值，与 `UserID` 互斥）；`Options` 表补 `MaxSignedBodyBytes`（默认 10MB）、`NonceCapacity`（默认 200000，满了回 503）。
  - 「访客模式」一节改为：没开 `AllowGuest` 时访客头被忽略，请求按匿名处理。
  - 第 3 节「鉴权」末尾加两小节：

```markdown
### 匿名请求与 GUEST

没带任何凭据的请求会被认证中间件放行为匿名身份（`id.IsAnonymous()` 为 true），鉴权时只有内置角色 `GUEST`；登录用户判定时也自动拥有 `GUEST`。控制台给 GUEST 授权的接口未登录也能调，其余接口匿名请求回 401。带了 token 但无效的请求仍然是 401，不会被当成匿名。**所有路由都要挂鉴权**（`fpchi` 在顶层 `r.Use(a.Middleware())` 即可）。

### 访问密钥（第三方程序调用）

第三方程序用控制台发放的 AccessKey 签名调用，认证中间件自动校验，不需要额外配置：身份里 `id.IsAccessKey()` 为 true，鉴权只按 key 绑定的角色判定、不拥有 GUEST。来源 IP 取 `X-Forwarded-For` 的第一个地址，部署时最外层代理必须覆盖这个头。签名规则、测试向量与错误码见 [`docs/access-key.md`](../docs/access-key.md)。
```

- [ ] **Step 5: 控制台文档**（`docs/console.md`）页面表加两行：

```markdown
| `/access-keys` | 访问密钥列表：新建（Secret 只显示一次）、停用、删除，可按角色筛选 |
| `/access-keys/:id` | 访问密钥详情：基本信息、按应用分组的可调用接口、编辑 |
```

  「授权相关的三处刻意分开放」一节后加一段：`GUEST` 是内置角色——未登录请求和所有登录用户都拥有它，访问密钥不拥有；不能删除、不能设父角色、只能配「允许」。

- [ ] **Step 6: demo**

`examples/demo/main.go`（import 加 `context`）：
- `/api/me` 的 handler 取身份后加上面 Step 4 的匿名判断。
- 健康检查之前加：

```go
	// 第三方签名调用：访问密钥请求同样经过 auth.Middleware，再按角色判定能不能调。
	// ServeMux 取不到路由模式，权限点 key 手写，并在启动时上报给 fp。
	const partnerPattern = "/api/partner/ping"
	if err := client.ReportPermissions(context.Background(), []fpsdk.PermissionPoint{
		{Key: fpsdk.PermissionKey(http.MethodGet, partnerPattern), Name: "第三方连通性检查"},
	}); err != nil {
		log.Printf("上报权限点失败: %v", err)
	}
	mux.Handle("GET "+partnerPattern, auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, err := client.Authz().Allow(r.Context(), http.MethodGet, partnerPattern)
		id, _ := fpsdk.IdentityFrom(r.Context())
		switch {
		case err != nil:
			fpsdk.WriteError(w, fpsdk.ErrUnavailable)
		case !ok && id.IsAnonymous():
			fpsdk.WriteError(w, fpsdk.ErrNoToken)
		case !ok:
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			writeJSON(w, map[string]any{"accessKeyId": id.AccessKeyID, "remark": id.AccessKeyRemark})
		}
	})))
```

`examples/demo/partner/main.go`：

```go
// Command partner 模拟第三方程序：用 AccessKey 签名调用 demo 的 /api/partner/ping。
//
//	go run ./examples/demo/partner -ak FPAK... -sk ...
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/basicfu/fp/sdk/aksign"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8090/api/partner/ping", "要调用的接口")
	ak := flag.String("ak", "", "AccessKey ID")
	sk := flag.String("sk", "", "AccessKey Secret")
	flag.Parse()

	req, err := http.NewRequest(http.MethodGet, *url, nil)
	if err != nil {
		log.Fatal(err)
	}
	if err := aksign.Sign(req, *ak, *sk); err != nil {
		log.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("%d %s\n", resp.StatusCode, body)
}
```

`examples/demo/README.md`：路由表加 `GET /api/partner/ping`（第三方签名调用示例），并加一节「第三方签名调用」：
1. 启动 demo（它会上报 `GET:/api/partner/ping`）。
2. 控制台建角色「合作方」，在「角色管理 → 授权」里勾上 `GET:/api/partner/ping`。
3. 控制台「访问密钥」新建一把绑定「合作方」的 key，记下 AK 与 SK。
4. `go run ./examples/demo/partner -ak <AK> -sk <SK>` → `200 {"accessKeyId":…}`。
5. 在控制台停用这把 key，再跑一次 → `403 {"code":"ACCESS_KEY_DISABLED",…}`。

- [ ] **Step 7:** `./scripts/test.sh ./sdk/aksign -run TestDocVectorsMatch` → PASS；`go build ./examples/...`；`./scripts/test.sh ./sdk -run TestExamplesDoNotImportInternal` → PASS。

- [ ] **Step 8: 提交**

```bash
git add docs/access-key.md sdk/aksign/doc_vector_test.go sdk/README.md docs/console.md examples/demo/main.go examples/demo/README.md examples/demo/partner/main.go
git commit -m "docs: 访问密钥对接文档与测试向量；SDK 文档补匿名/GUEST 与访问密钥；demo 加第三方签名调用示例"
```
