# fp-im 双认证实施计划：fp 令牌与业务方令牌并存

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让同一个应用同时接受 fp 签发的令牌和业务方自己签发的令牌，后者通过 HTTP 回调业务方的接口验证。

**Architecture:** 握手帧新增 `kind` 字段决定送往哪个验证器。认证接口从"收令牌字符串"改成"收请求结构"，因为业务方的实现需要原始帧字节。新增两个包：`bizauth` 做 HTTP 回调加缓存，`multiauth` 做分派。接入层仍然只持有一个认证器，形状不变。

**Tech Stack:** Go 1.25、标准库 `net/http`、`github.com/hashicorp/golang-lru/v2`（仓库已有依赖）、标准库 `testing`。

## Global Constraints

- **只用标准库 `testing`**，不要 testify。**不得调用 `t.Parallel()`**（全仓 `-p 1`，测试共享同一个 Redis 与 PG）。
- **断言不用 `time.Sleep` 等条件成立**，用轮询或 channel 加超时。制造文件修改时间差异那种用途除外。
- **注释与测试失败信息一律用中文**，非平凡注释要解释"为什么不是另一种做法"。
- **不得新增模块依赖。** `github.com/hashicorp/golang-lru/v2` 已在 `go.mod` 且已登记在 `internal/integration/dependency_whitelist_test.go`。
- **`sdk/` 不得 import `github.com/basicfu/fp/internal`，不得出现 `panic`**（`sdk/arch_test.go` 遍历整个 `sdk/` 目录强制）。
- **`internal/im/**` 不得 import `internal/{store,service,domain,httpapi,grpcapi,connector,notify}`**；只有 `internal/im/fpauth` 可以 import 手写的 `sdk`（`internal/im/arch_test.go` 强制）。生成代码 `sdk/gen` 谁都能用。
- **时间戳一律毫秒 `int64`。** 文件 LF 换行（`.gitattributes` 强制）。
- **每个任务结束必须 `git commit`**，Conventional Commits，中文主题，消息体末尾空一行后加：
  `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`
- **环境**：`scripts/test.sh` 会 source `scripts/env.sh`，要求仓库根有 `.env.local`（已有，未跟踪）。缺了从别处复制，不要改也不要提交。
- **已知的环境噪音**：`TestRedisSubscriptionBlipDoesNotSilentlyLoseRevocations` 会失败，原因是共享 Redis 上有外部进程持着 pub/sub 连接（多余那条在 db=0 而测试库是 db=1，且用不同 Go 版本构建）。它与本计划无关，不要试图修它。
- **回退验证是硬要求**：每条关键性质写完后，把实现改成缺陷版本，确认对应测试变红，再恢复。把过程和真实输出写进报告。本项目前一批任务里反复出现过"测试名字承诺的性质其实没被断言"。

---

## 一、文件结构

**新建**

| 文件 | 职责 |
|---|---|
| `internal/im/bizauth/bizauth.go` | HTTP 回调业务方验证接口 |
| `internal/im/bizauth/cache.go` | 按应用隔离的验证结果缓存 |
| `internal/im/multiauth/multiauth.go` | 按令牌类型分派到下游认证器 |

**修改**

| 文件 | 改动 |
|---|---|
| `internal/im/auth/auth.go` | 接口改收 `VerifyRequest` |
| `internal/im/model/subject.go` | 加 `b:` 前缀、加用户标识校验 |
| `internal/im/model/frame.go` | 握手帧加 `Kind`，加两个类型常量 |
| `internal/im/model/appconfig.go` | 加 `BizAuth` 与校验、加 `Duration` 类型 |
| `internal/im/fpauth/fpauth.go` | 适配新签名 |
| `internal/im/wsapi/handler.go` | 握手读原始字节、主体解析按类型分派 |
| `internal/im/appcfg/file.go` | `biz_auth` 的默认值填充 |
| `cmd/fp-im/main.go` | 装配两个认证器 |
| `sdk/im/subject.go` | 加 `b:` 前缀，与网关侧逐字一致 |
| `sdk/im/client.go` | 客户端配置加令牌类型 |
| `internal/integration/im_parity_test.go` | 配对断言覆盖新前缀 |
| `docs/im.md` | 协议、配置、运维要点 |

## 二、跨任务接口总表

后面每个任务的 **Interfaces** 只写本任务相关的部分，这里是全貌，名字以此为准：

```go
// internal/im/model
const KindBiz SubjectKind = "b"
func Biz(id string) Subject
var ErrBadUserID error
func ValidateUserID(id string) error          // 非空、无 \x00、<= 128 字节

const TokenKindFP, TokenKindBiz = "fp", "biz"
type AuthFrame struct{ …; Kind string `json:"kind,omitempty"` }

type Duration time.Duration
func (d Duration) Std() time.Duration
func (d *Duration) UnmarshalJSON(b []byte) error
func (d Duration) MarshalJSON() ([]byte, error)

type BizAuth struct {
    VerifyURL string   `json:"verify_url"`
    Timeout   Duration `json:"timeout"`
    CacheSize int      `json:"cache_size"`
}
type AppConfig struct{ …; BizAuth *BizAuth `json:"biz_auth,omitempty"` }

// internal/im/auth
type VerifyRequest struct{ App, Kind, Token string; Raw []byte }
type Authenticator interface {
    Verify(ctx context.Context, req VerifyRequest) (model.Subject, error)
}

// internal/im/bizauth
type Config struct {
    Apps   auth.AppConfigSource
    Client *http.Client      // nil 则用带超时的默认客户端
    Logger *slog.Logger      // nil 则用 slog.Default()
    Now    func() time.Time  // nil 则用 time.Now，测试注入假时钟
}
func New(cfg Config) (*Authenticator, error)
func (a *Authenticator) Verify(ctx context.Context, req auth.VerifyRequest) (model.Subject, error)

// internal/im/bizauth（cache.go，包内可见）
type cacheKey struct{ app, token string }
type cacheEntry struct{ sub model.Subject; expiresAt time.Time }
type cacheSet struct{ … }
func newCacheSet() *cacheSet
func (s *cacheSet) get(app, token string, now time.Time) (model.Subject, bool)
func (s *cacheSet) put(app, token string, sub model.Subject, ttl time.Duration, size int, now time.Time)

// internal/im/multiauth
func New(fp, biz auth.Authenticator) auth.Authenticator

// sdk/im
const KindBiz SubjectKind = "b"
func Biz(id string) Subject
const TokenKindFP, TokenKindBiz = "fp", "biz"
type ClientConfig struct{ …; Kind string }
```

---

## Task 1: model 层的三处扩展

主体前缀、握手帧字段、应用配置，都在 `internal/im/model`，都是没有 I/O 的纯类型。后面每个任务都依赖它们。

**Files:**
- Modify: `internal/im/model/subject.go`、`internal/im/model/frame.go`、`internal/im/model/appconfig.go`
- Test: `internal/im/model/subject_test.go`、`internal/im/model/appconfig_test.go`

**Interfaces:**
- Produces: `KindBiz`、`Biz()`、`ErrBadUserID`、`ValidateUserID()`、`TokenKindFP`、`TokenKindBiz`、`AuthFrame.Kind`、`Duration`、`BizAuth`、`AppConfig.BizAuth`。

- [ ] **Step 1: 写主体前缀与用户标识校验的失败测试**

追加到 `internal/im/model/subject_test.go`：

```go
func TestParseSubjectBiz(t *testing.T) {
	got, err := ParseSubject("b:1001")
	if err != nil {
		t.Fatalf("合法的业务方主体被拒：%v", err)
	}
	if got != (Subject{KindBiz, "1001"}) {
		t.Fatalf("ParseSubject(\"b:1001\")=%+v", got)
	}
	if got.String() != "b:1001" {
		t.Fatalf("String() 必须还原输入，实际 %q", got.String())
	}
	// 业务方主体的 id 不做格式约束（业务方的用户体系我们不理解），
	// 但空 id 必须拒绝，否则会生成 "b:" 这种没有主体的 Redis key。
	if _, err := ParseSubject("b:"); err == nil {
		t.Fatal("空的业务方 id 必须被拒")
	}
}

func TestValidateUserID(t *testing.T) {
	if err := ValidateUserID("1001"); err != nil {
		t.Fatalf("普通 id 被拒：%v", err)
	}
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"空串", ""},
		{"含空字节", "10\x0001"},
		{"超长", strings.Repeat("x", 129)},
	} {
		if err := ValidateUserID(tc.id); err == nil {
			t.Errorf("%s 必须被拒绝：user_id 会成为 Redis key 的一部分，空字节还会撞上路由表内存键的分隔符", tc.name)
		}
	}
	// 恰好 128 字节是允许的，边界不能少一个
	if err := ValidateUserID(strings.Repeat("x", 128)); err != nil {
		t.Fatalf("128 字节应当放行：%v", err)
	}
}
```

`subject_test.go` 的 import 要加 `"strings"`。

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/im/model -run 'TestParseSubjectBiz|TestValidateUserID' -v`
Expected: 编译失败，`KindBiz`、`ValidateUserID` 未定义。

- [ ] **Step 3: 实现主体前缀与校验**

`internal/im/model/subject.go` 的常量块加一个：

```go
const (
	KindUser  SubjectKind = "u"
	KindGuest SubjectKind = "g"
	// KindBiz 是业务方自己的认证体系里的用户。与 KindUser 分开，是为了让
	// fp 的用户 1001 和业务方的用户 1001 在 Redis key 上天然隔离——
	// 合成一个前缀的话，两个毫不相干的人会共用同一张连接表，互相顶号、
	// 互相收到对方的消息。
	KindBiz SubjectKind = "b"
)
```

加构造函数，放在 `Guest` 后面：

```go
func Biz(id string) Subject { return Subject{Kind: KindBiz, ID: id} }
```

`ParseSubject` 的 switch 加一个分支，放在 `KindGuest` 之后：

```go
	case KindBiz:
		if err := ValidateUserID(id); err != nil {
			return Subject{}, fmt.Errorf("%w: %v", ErrBadSubject, err)
		}
		return Biz(id), nil
```

文件末尾加校验函数：

```go
// MaxUserIDLen 是业务方用户标识的长度上限。
// 它会成为 Redis key 的一部分（fp:im:{app:b:1001}:conn），
// 没有上限的话，一个恶意或有 bug 的业务方能造出超长 key 撑爆 Redis 内存。
const MaxUserIDLen = 128

var ErrBadUserID = errors.New("model: 用户标识非法")

// ValidateUserID 校验业务方回调返回的用户标识。
// 不约束字符集：业务方的用户体系我们不理解，可能是 uuid、雪花 id、邮箱。
// 只拦三种一定会出事的情况。
func ValidateUserID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: 不能为空", ErrBadUserID)
	}
	if len(id) > MaxUserIDLen {
		return fmt.Errorf("%w: 超过 %d 字节", ErrBadUserID, MaxUserIDLen)
	}
	// 路由器的内存键用 \x00 拼接 app 与 subject，id 里含它会让两个
	// 不同的 (app, subject) 组合切分成同一个键。
	if strings.ContainsRune(id, 0) {
		return fmt.Errorf("%w: 不能含空字节", ErrBadUserID)
	}
	return nil
}
```

import 要加 `"strings"`。

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/im/model -run 'TestParseSubjectBiz|TestValidateUserID' -v`
Expected: PASS

- [ ] **Step 5: 握手帧加令牌类型**

`internal/im/model/frame.go`，在帧类型常量之后加：

```go
// 握手帧里的令牌类型。缺省（空串）视为 fp，所以老客户端一行不用改。
const (
	TokenKindFP  = "fp"
	TokenKindBiz = "biz"
)
```

`AuthFrame` 加字段，放在 `Guest` 之后：

```go
	// Kind 决定这个 token 送去哪个验证器。空或 "fp" 走 fp，"biz" 走业务方回调。
	// 客户端可以乱填，但那不构成安全边界：类型只决定送到哪儿验，不决定是否放行。
	Kind string `json:"kind,omitempty"`
```

- [ ] **Step 6: 写配置的失败测试**

追加到 `internal/im/model/appconfig_test.go`：

```go
func TestDurationJSONRoundTrip(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`"2s"`), &d); err != nil {
		t.Fatalf("解析 \"2s\" 失败：%v", err)
	}
	if d.Std() != 2*time.Second {
		t.Fatalf("解析结果 %v，期望 2s", d.Std())
	}
	b, err := json.Marshal(d)
	if err != nil || string(b) != `"2s"` {
		t.Fatalf("序列化得 %s (%v)，期望 \"2s\"", b, err)
	}
	if json.Unmarshal([]byte(`"不是时长"`), &d) == nil {
		t.Fatal("非法时长必须报错，而不是静默变成 0——0 超时意味着每次验证都立刻失败")
	}
	if json.Unmarshal([]byte(`2`), &d) == nil {
		t.Fatal("裸数字必须报错：配置里写 2 是想表达 2 秒还是 2 纳秒，没人说得清")
	}
}

func TestAppConfigValidateBizAuth(t *testing.T) {
	base := AppConfig{AppID: "a1", AppSecret: "s", ConnPolicy: PolicyReplace, ConnLimit: 5, GuestIPRate: 20}

	ok := base
	ok.BizAuth = &BizAuth{VerifyURL: "https://x/verify", Timeout: Duration(2 * time.Second), CacheSize: 10}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法的 biz_auth 被拒：%v", err)
	}

	for _, tc := range []struct {
		name string
		biz  BizAuth
	}{
		{"缺地址", BizAuth{Timeout: Duration(time.Second), CacheSize: 10}},
		{"明文 HTTP", BizAuth{VerifyURL: "http://x/verify", Timeout: Duration(time.Second), CacheSize: 10}},
		{"超时为零", BizAuth{VerifyURL: "https://x/verify", CacheSize: 10}},
		{"超时为负", BizAuth{VerifyURL: "https://x/verify", Timeout: Duration(-time.Second), CacheSize: 10}},
		{"缓存容量为零", BizAuth{VerifyURL: "https://x/verify", Timeout: Duration(time.Second)}},
	} {
		bad := base
		bad.BizAuth = &tc.biz
		if bad.Validate() == nil {
			t.Errorf("%s 必须被拒绝", tc.name)
		}
	}

	// 不配 biz_auth 是合法的：那表示这个 app 不支持业务方令牌
	none := base
	if err := none.Validate(); err != nil {
		t.Fatalf("不配 biz_auth 应当合法：%v", err)
	}
}
```

import 要加 `"encoding/json"` 和 `"time"`。

- [ ] **Step 7: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/im/model -run 'TestDuration|TestAppConfigValidateBizAuth' -v`
Expected: 编译失败，`Duration`、`BizAuth` 未定义。

- [ ] **Step 8: 实现配置类型与校验**

`internal/im/model/appconfig.go` 顶部 import 加 `"encoding/json"`、`"strings"`、`"time"`。

在 `AppConfig` 之前加：

```go
// Duration 是 JSON 里写成 "2s" 这种人类可读时长的配置项。
// 不直接用 time.Duration：它的 JSON 表示是纳秒整数，配置文件里写 2000000000
// 既难读又容易错一个数量级。
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		// 明确拒绝裸数字：写 2 是 2 秒还是 2 纳秒，没人说得清，
		// 与其猜一个不如让配置加载直接失败。
		return fmt.Errorf("model: 时长必须是带单位的字符串，例如 \"2s\"：%w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("model: 无法解析时长 %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// BizAuth 是业务方自有认证的回调配置。整组为空表示这个 app 不支持业务方令牌。
// 用嵌套而不是铺平成三个字段，是为了让"支不支持"能整块判断，
// 铺平之后就得靠"地址是不是空串"这种间接判断。
type BizAuth struct {
	VerifyURL string   `json:"verify_url"`
	Timeout   Duration `json:"timeout"`
	CacheSize int      `json:"cache_size"`
}
```

`AppConfig` 加字段：

```go
	BizAuth *BizAuth `json:"biz_auth,omitempty"`
```

`Validate()` 在原有的访客那条规则之后、`return nil` 之前，加：

```go
	if c.BizAuth != nil {
		if c.BizAuth.VerifyURL == "" {
			return fmt.Errorf("model: app %q 配了 biz_auth 但缺 verify_url", c.AppID)
		}
		// 必须 HTTPS：client 的令牌明文走在请求体里，明文传输等于把
		// 所有业务方令牌交给中间人。
		if !strings.HasPrefix(c.BizAuth.VerifyURL, "https://") {
			return fmt.Errorf("model: app %q 的 verify_url 必须是 https", c.AppID)
		}
		if c.BizAuth.Timeout <= 0 {
			return fmt.Errorf("model: app %q 的 biz_auth.timeout 必须大于 0", c.AppID)
		}
		if c.BizAuth.CacheSize <= 0 {
			return fmt.Errorf("model: app %q 的 biz_auth.cache_size 必须大于 0", c.AppID)
		}
	}
```

- [ ] **Step 9: 跑整包测试**

Run: `./scripts/test.sh ./internal/im/model -v`
Expected: 全部 PASS

- [ ] **Step 10: 回退验证**

三条各做一次：把 `ParseSubject` 里 `KindBiz` 分支的 `ValidateUserID` 调用去掉，确认 `TestParseSubjectBiz` 的空 id 断言变红；把 `Validate` 里 HTTPS 那条去掉，确认 `TestAppConfigValidateBizAuth` 的"明文 HTTP"子用例变红；把 `UnmarshalJSON` 里拒绝裸数字那条改成接受，确认 `TestDurationJSONRoundTrip` 变红。每次确认后立刻恢复。把三次的真实输出写进报告。

- [ ] **Step 11: 提交**

```bash
git add internal/im/model
git commit -m "$(cat <<'EOF'
feat(im/model): 加业务方主体前缀、握手帧令牌类型、业务方认证配置

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: 认证接口改签名，fpauth 适配

接口从收令牌字符串改成收请求结构。`fpauth` 是当前唯一实现，必须同批改，否则编译不过。

**Files:**
- Modify: `internal/im/auth/auth.go`、`internal/im/fpauth/fpauth.go`、`internal/im/fpauth/fpauth_test.go`、`internal/im/wsapi/handler.go`（仅调用点）
- Test: `internal/im/fpauth/fpauth_test.go`

**Interfaces:**
- Consumes: Task 1 的 `model.Subject`。
- Produces: `auth.VerifyRequest`、改签名后的 `auth.Authenticator`。

- [ ] **Step 1: 改接口**

`internal/im/auth/auth.go`，把 `Authenticator` 换成：

```go
// VerifyRequest 是一次身份验证的全部输入。
// 用结构体而不是继续往参数列表里加：不同实现用到的字段不同，
// fp 侧只需要 App 与 Token，业务方侧只需要 App 与 Raw。
// 以后加第三种认证方式时，这里加字段不会波及已有实现的签名。
type VerifyRequest struct {
	App   string
	Kind  string // "" 或 model.TokenKindFP 或 model.TokenKindBiz
	Token string
	// Raw 是 client 发上来的原始握手帧字节。业务方回调把它原样转发，
	// 这样 client 塞的自定义字段（设备指纹之类）也能到业务方手里。
	Raw []byte
}

// Authenticator 验证 client 的凭证，返回它对应的主体。
type Authenticator interface {
	Verify(ctx context.Context, req VerifyRequest) (model.Subject, error)
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/im/... -v`
Expected: `fpauth` 与 `wsapi` 编译失败，`Verify` 签名不匹配。

- [ ] **Step 3: 改 fpauth**

`internal/im/fpauth/fpauth.go` 的 `Verify` 改成：

```go
func (a *Authenticator) Verify(ctx context.Context, req auth.VerifyRequest) (model.Subject, error) {
	c, err := a.client(req.App)
	if err != nil {
		return model.Subject{}, err
	}
	id, err := c.Auth().Validate(ctx, req.Token)
	if err != nil {
		return model.Subject{}, translate(err)
	}
	return model.User(id.UserID), nil
}
```

`fpauth_test.go` 里所有 `a.Verify(ctx, "app", "token")` 形式的调用改成 `a.Verify(ctx, auth.VerifyRequest{App: "app", Token: "token"})`。

- [ ] **Step 4: 改 wsapi 的调用点**

`internal/im/wsapi/handler.go` 的 `resolveSubject` 里那一行：

```go
		sub, err := s.Auth.Verify(ctx, auth.VerifyRequest{App: af.App, Kind: af.Kind, Token: af.Token})
```

Task 5 会把原始字节也传进去，这一步先让它编译通过。

- [ ] **Step 5: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/im/... -v`
Expected: 全部 PASS

- [ ] **Step 6: 提交**

```bash
git add internal/im/auth internal/im/fpauth internal/im/wsapi
git commit -m "$(cat <<'EOF'
refactor(im/auth): Authenticator 改收 VerifyRequest，为多实现分派让路

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: bizauth 包——HTTP 回调与缓存

新包，业务方令牌的验证实现。它是这个计划里唯一有网络 I/O 的部分。

**Files:**
- Create: `internal/im/bizauth/bizauth.go`、`internal/im/bizauth/cache.go`
- Test: `internal/im/bizauth/bizauth_test.go`、`internal/im/bizauth/cache_test.go`

**Interfaces:**
- Consumes: Task 1 的 `model.BizAuth`/`model.Biz`/`model.ValidateUserID`，Task 2 的 `auth.VerifyRequest`/`auth.Authenticator`/`auth.ErrUnauthorized`/`auth.ErrUnavailable`；`auth.AppConfigSource`（已有，`Get(app) (model.AppConfig, bool)`）。
- Produces: `bizauth.Config`、`bizauth.New`、`(*Authenticator).Verify`。

- [ ] **Step 1: 写缓存的失败测试**

`internal/im/bizauth/cache_test.go`：

```go
package bizauth

import (
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/model"
)

func TestCacheHitAndExpiry(t *testing.T) {
	s := newCacheSet()
	now := time.UnixMilli(1_000_000)
	s.put("a1", "tok", model.Biz("u1"), 60*time.Second, 10, now)

	got, ok := s.get("a1", "tok", now.Add(59*time.Second))
	if !ok || got != model.Biz("u1") {
		t.Fatalf("未过期时应命中，实际 %+v ok=%v", got, ok)
	}
	if _, ok := s.get("a1", "tok", now.Add(61*time.Second)); ok {
		t.Fatal("过期后不能再命中：撤销的令牌不该无限期地继续放行")
	}
}

func TestCacheIsolatesApps(t *testing.T) {
	s := newCacheSet()
	now := time.UnixMilli(1_000_000)
	s.put("a1", "tok", model.Biz("u1"), time.Minute, 10, now)

	// 同一个 token 串在另一个 app 下必须是未命中：两个 app 的令牌体系毫不相干，
	// 串了就意味着 A 应用的令牌能在 B 应用下登录成另一个人。
	if _, ok := s.get("a2", "tok", now); ok {
		t.Fatal("不同 app 的同名令牌不能互相命中")
	}
}

func TestCacheKeyHasNoConcatenationAmbiguity(t *testing.T) {
	s := newCacheSet()
	now := time.UnixMilli(1_000_000)
	// 拼接成字符串的话，("a", "1:b") 与 ("a:1", "b") 会拼出同一个键。
	s.put("a", "1:b", model.Biz("first"), time.Minute, 10, now)
	s.put("a:1", "b", model.Biz("second"), time.Minute, 10, now)

	got1, _ := s.get("a", "1:b", now)
	got2, _ := s.get("a:1", "b", now)
	if got1 != model.Biz("first") || got2 != model.Biz("second") {
		t.Fatalf("两个键被混成了一个：got1=%+v got2=%+v", got1, got2)
	}
}

func TestCacheEvictsBeyondCapacity(t *testing.T) {
	s := newCacheSet()
	now := time.UnixMilli(1_000_000)
	for i := 0; i < 5; i++ {
		s.put("a1", string(rune('a'+i)), model.Biz("u"), time.Minute, 3, now)
	}
	// 容量 3，写了 5 条，最早的两条应当被淘汰
	live := 0
	for i := 0; i < 5; i++ {
		if _, ok := s.get("a1", string(rune('a'+i)), now); ok {
			live++
		}
	}
	if live != 3 {
		t.Fatalf("容量上限没生效，存活 %d 条，期望 3。无界缓存会在令牌高基数时吃光内存", live)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/im/bizauth -v`
Expected: 包不存在，编译失败。

- [ ] **Step 3: 实现缓存**

`internal/im/bizauth/cache.go`：

```go
package bizauth

import (
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/basicfu/fp/internal/im/model"
)

// cacheKey 用结构体而不是拼接字符串。拼接会引入歧义：
// app "a" 加令牌 "1:b" 与 app "a:1" 加令牌 "b" 拼出来是同一个串，
// 两个应用的令牌就串了。
type cacheKey struct {
	app   string
	token string
}

type cacheEntry struct {
	sub       model.Subject
	expiresAt time.Time
}

// cacheSet 按 app 各持一个 LRU：容量是 app 级配置，合成一个全局缓存的话
// 一个高流量应用会把其它应用的条目挤光。
type cacheSet struct {
	mu   sync.Mutex
	byApp map[string]*lru.Cache[cacheKey, cacheEntry]
}

func newCacheSet() *cacheSet {
	return &cacheSet{byApp: map[string]*lru.Cache[cacheKey, cacheEntry]{}}
}

func (s *cacheSet) get(app, token string, now time.Time) (model.Subject, bool) {
	s.mu.Lock()
	c, ok := s.byApp[app]
	s.mu.Unlock()
	if !ok {
		return model.Subject{}, false
	}
	e, ok := c.Get(cacheKey{app, token})
	if !ok || !now.Before(e.expiresAt) {
		return model.Subject{}, false
	}
	return e.sub, true
}

// put 在 ttl 大于零时写入。size 是该 app 配置的容量上限，
// 首次为该 app 建缓存时生效；之后改配置不会缩容，重启才生效——
// 缓存容量不值得为热更新引入重建逻辑。
func (s *cacheSet) put(app, token string, sub model.Subject, ttl time.Duration, size int, now time.Time) {
	if ttl <= 0 {
		return
	}
	s.mu.Lock()
	c, ok := s.byApp[app]
	if !ok {
		var err error
		c, err = lru.New[cacheKey, cacheEntry](size)
		if err != nil {
			// size 已在配置校验里保证大于零，走不到这里。
			s.mu.Unlock()
			return
		}
		s.byApp[app] = c
	}
	s.mu.Unlock()
	c.Add(cacheKey{app, token}, cacheEntry{sub: sub, expiresAt: now.Add(ttl)})
}
```

- [ ] **Step 4: 跑缓存测试确认通过**

Run: `./scripts/test.sh ./internal/im/bizauth -run TestCache -v`
Expected: 四条全部 PASS

- [ ] **Step 5: 写回调的失败测试**

`internal/im/bizauth/bizauth_test.go`：

```go
package bizauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
)

type apps map[string]model.AppConfig

func (a apps) Get(app string) (model.AppConfig, bool) { c, ok := a[app]; return c, ok }
func (a apps) Apps() []string                          { return nil }

// newEnv 起一个假的业务方验证服务，返回配好的认证器与请求计数器。
// handler 里可以断言收到的请求，也可以按需返回不同响应。
func newEnv(t *testing.T, h http.HandlerFunc) (*Authenticator, *httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	// 配置校验要求 https，但测试用的是 httptest 的 http 地址，
	// 所以这里直接构造 AppConfig 而不经过 Validate——被测的是回调行为，
	// 不是配置校验（那条由 model 包的测试守着）。
	a, err := New(Config{
		Apps: apps{"a1": {
			AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace,
			BizAuth: &model.BizAuth{VerifyURL: srv.URL, Timeout: model.Duration(2 * time.Second), CacheSize: 100},
		}},
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, srv, &calls
}

func req(app, token string, raw string) auth.VerifyRequest {
	return auth.VerifyRequest{App: app, Kind: model.TokenKindBiz, Token: token, Raw: []byte(raw)}
}

func TestVerifyForwardsRawFrameVerbatim(t *testing.T) {
	const raw = `{"t":"auth","app":"a1","token":"tok","kind":"biz","custom":{"deviceId":"d-1"}}`
	var got string
	a, _, _ := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Write([]byte(`{"user_id":"u1"}`))
	})
	if _, err := a.Verify(context.Background(), req("a1", "tok", raw)); err != nil {
		t.Fatal(err)
	}
	// 逐字节相同：client 塞的自定义字段必须原样到业务方手里，
	// 网关不解析后重新序列化（那会丢掉未知字段）。
	if got != raw {
		t.Fatalf("转发的不是原始字节：\n收到 %s\n期望 %s", got, raw)
	}
}

func TestVerifySuccessMapsToBizSubject(t *testing.T) {
	a, _, _ := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user_id":"1001"}`))
	})
	sub, err := a.Verify(context.Background(), req("a1", "tok", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if sub != model.Biz("1001") {
		t.Fatalf("主体应是 b:1001，实际 %s", sub)
	}
}

func TestVerifyErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{"401 无效令牌", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }, auth.ErrUnauthorized},
		{"200 但 user_id 为空", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"user_id":""}`)) }, auth.ErrUnauthorized},
		{"200 但 user_id 含空字节", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{\"user_id\":\"a\\u0000b\"}")) }, auth.ErrUnauthorized},
		{"500", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, auth.ErrUnavailable},
		{"200 但响应体不是 JSON", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`not json`)) }, auth.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := newEnv(t, tc.handler)
			_, err := a.Verify(context.Background(), req("a1", "tok", `{}`))
			if !errors.Is(err, tc.want) {
				t.Fatalf("错误映射不对：得到 %v，期望 %v", err, tc.want)
			}
		})
	}
}

func TestVerifyUnreachableIsUnavailable(t *testing.T) {
	a, srv, _ := newEnv(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close() // 关掉服务端，制造连不上
	_, err := a.Verify(context.Background(), req("a1", "tok", `{}`))
	if !errors.Is(err, auth.ErrUnavailable) {
		t.Fatalf("连不上必须是 ErrUnavailable（client 应退避重连），实际 %v", err)
	}
}

func TestVerifyAppWithoutBizAuthIsUnauthorized(t *testing.T) {
	a, err := New(Config{Apps: apps{"a1": {AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace}}})
	if err != nil {
		t.Fatal(err)
	}
	_, verr := a.Verify(context.Background(), req("a1", "tok", `{}`))
	if !errors.Is(verr, auth.ErrUnauthorized) {
		t.Fatalf("app 没配 biz_auth 时必须拒绝，实际 %v", verr)
	}
}

func TestVerifyCachesWhenServerAsksFor(t *testing.T) {
	a, _, calls := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user_id":"u1","cache_seconds":60}`))
	})
	for i := 0; i < 3; i++ {
		if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("业务方要求缓存 60 秒，三次握手应当只调一次，实际调了 %d 次", n)
	}
}

func TestVerifyDoesNotCacheWithoutTTL(t *testing.T) {
	a, _, calls := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user_id":"u1"}`))
	})
	for i := 0; i < 3; i++ {
		if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("不带 cache_seconds 就不该缓存，三次握手应当调三次，实际 %d 次", n)
	}
}

func TestVerifyReCallsAfterCacheExpires(t *testing.T) {
	// 假时钟：缓存过期这条性质靠等真实时间来测的话，要么让测试跑几十秒，
	// 要么把 TTL 调到毫秒级而变得不稳定。注入时钟是唯一可靠的办法。
	now := time.UnixMilli(1_000_000)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"user_id":"u1","cache_seconds":60}`))
	}))
	t.Cleanup(srv.Close)
	a, err := New(Config{
		Apps: apps{"a1": {
			AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace,
			BizAuth: &model.BizAuth{VerifyURL: srv.URL, Timeout: model.Duration(2 * time.Second), CacheSize: 10},
		}},
		Client: srv.Client(),
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Second) // 还没过期
	if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("59 秒时缓存仍应命中，实际调了 %d 次", n)
	}

	now = now.Add(2 * time.Second) // 越过 60 秒
	if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("过期后必须重新调用业务方，实际总共调了 %d 次——不重新调意味着撤销的令牌能无限期继续放行", n)
	}
}

func TestVerifyDoesNotCacheFailures(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	a, _, calls := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte(`{"user_id":"u1","cache_seconds":60}`))
	})
	if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err == nil {
		t.Fatal("第一次应当失败")
	}
	fail.Store(false)
	// 业务方接口恢复之后，client 应当立刻能连上，而不是等一个负缓存过期
	if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
		t.Fatalf("接口恢复后应当立刻成功，实际 %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("失败不该进缓存，应当调两次，实际 %d 次", n)
	}
}

func TestVerifyRespectsTimeout(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // 一直不返回
	}))
	t.Cleanup(srv.Close)
	a, err := New(Config{
		Apps: apps{"a1": {
			AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace,
			BizAuth: &model.BizAuth{VerifyURL: srv.URL, Timeout: model.Duration(200 * time.Millisecond), CacheSize: 10},
		}},
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, verr := a.Verify(context.Background(), req("a1", "tok", `{}`))
	el := time.Since(start)
	if !errors.Is(verr, auth.ErrUnavailable) {
		t.Fatalf("超时必须是 ErrUnavailable，实际 %v", verr)
	}
	// 上界放宽到 2 秒是为了容忍慢机器；关键是它必须明显小于握手的 5 秒上限，
	// 否则 client 会先被握手超时踢掉，拿到的关闭码是错的。
	if el > 2*time.Second {
		t.Fatalf("超时没生效，耗时 %v", el)
	}
}
```

- [ ] **Step 6: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/im/bizauth -v`
Expected: FAIL，`New`、`Config`、`Authenticator` 未定义。

- [ ] **Step 7: 实现回调**

`internal/im/bizauth/bizauth.go`：

```go
// Package bizauth 用 HTTP 回调业务方自己的接口来验证他们签发的令牌。
// 与 fpauth 并列，两者都实现 auth.Authenticator，由 multiauth 按令牌类型分派。
package bizauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
)

// maxRespBody 是响应体的读取上限。业务方接口异常时可能吐出一个巨大的
// HTML 错误页，不设上限的话每次握手都会把它整个读进内存。
const maxRespBody = 64 << 10

type Config struct {
	Apps   auth.AppConfigSource
	Client *http.Client     // nil 则用默认客户端；超时由每次请求的 ctx 控制
	Logger *slog.Logger     // nil 则用 slog.Default()
	Now    func() time.Time // nil 则用 time.Now，测试注入假时钟
}

type Authenticator struct {
	apps   auth.AppConfigSource
	client *http.Client
	log    *slog.Logger
	now    func() time.Time
	cache  *cacheSet
}

func New(cfg Config) (*Authenticator, error) {
	if cfg.Apps == nil {
		return nil, errors.New("bizauth: Apps 必填")
	}
	a := &Authenticator{
		apps:   cfg.Apps,
		client: cfg.Client,
		log:    cfg.Logger,
		now:    cfg.Now,
		cache:  newCacheSet(),
	}
	if a.client == nil {
		a.client = &http.Client{}
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a, nil
}

// verifyResponse 是业务方接口的响应约定。
// CacheSeconds 由业务方决定：只有它知道自己的令牌撤销有多频繁、
// 能容忍多长的失效窗口。不带就不缓存，默认安全。
type verifyResponse struct {
	UserID       string `json:"user_id"`
	CacheSeconds int    `json:"cache_seconds"`
}

func (a *Authenticator) Verify(ctx context.Context, req auth.VerifyRequest) (model.Subject, error) {
	cfg, ok := a.apps.Get(req.App)
	if !ok || cfg.BizAuth == nil {
		// app 不存在，或者存在但没开业务方认证。对 client 是同一件事：
		// 你这个令牌在这里验不了。不区分是为了不泄露"哪些 app 开了什么"。
		return model.Subject{}, auth.ErrUnauthorized
	}
	if sub, hit := a.cache.get(req.App, req.Token, a.now()); hit {
		return sub, nil
	}

	rctx, cancel := context.WithTimeout(ctx, cfg.BizAuth.Timeout.Std())
	defer cancel()
	httpReq, err := http.NewRequestWithContext(rctx, http.MethodPost, cfg.BizAuth.VerifyURL, bytes.NewReader(req.Raw))
	if err != nil {
		return model.Subject{}, fmt.Errorf("%w: 构造请求失败: %v", auth.ErrUnavailable, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		a.log.Warn("bizauth: 调业务方验证接口失败", "app", req.App, "err", err)
		return model.Subject{}, fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// 业务方明确说这个令牌不对，client 该去重新登录而不是重连。
		return model.Subject{}, auth.ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		a.log.Warn("bizauth: 业务方验证接口返回异常状态", "app", req.App, "status", resp.StatusCode)
		return model.Subject{}, fmt.Errorf("%w: 业务方返回 %d", auth.ErrUnavailable, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return model.Subject{}, fmt.Errorf("%w: 读响应失败: %v", auth.ErrUnavailable, err)
	}
	var vr verifyResponse
	if err := json.Unmarshal(body, &vr); err != nil {
		// 响应体坏了是业务方的接口出问题，不是 client 的令牌有问题，
		// 所以归 Unavailable 让 client 退避重连，等业务方修好。
		a.log.Warn("bizauth: 业务方响应不是合法 JSON", "app", req.App, "err", err)
		return model.Subject{}, fmt.Errorf("%w: 响应不是合法 JSON", auth.ErrUnavailable)
	}
	if err := model.ValidateUserID(vr.UserID); err != nil {
		// user_id 会成为 Redis key 的一部分，非法值必须在这里拦住。
		a.log.Warn("bizauth: 业务方返回了非法的 user_id", "app", req.App, "err", err)
		return model.Subject{}, fmt.Errorf("%w: %v", auth.ErrUnauthorized, err)
	}

	sub := model.Biz(vr.UserID)
	// 只缓存成功的结果。失败不缓存：业务方接口恢复之后 client 应当立刻能连上，
	// 而不是等一个负缓存过期。
	a.cache.put(req.App, req.Token, sub, time.Duration(vr.CacheSeconds)*time.Second, cfg.BizAuth.CacheSize, a.now())
	return sub, nil
}
```

- [ ] **Step 8: 跑整包测试**

Run: `./scripts/test.sh ./internal/im/bizauth -v`
Expected: 全部 PASS

- [ ] **Step 9: 回退验证**

三条各做一次，每次确认后立刻恢复：

1. 把转发的 body 从 `req.Raw` 改成重新序列化一个只含 token 的 JSON，确认 `TestVerifyForwardsRawFrameVerbatim` 变红。
2. 把 `ValidateUserID` 那段去掉，确认 `TestVerifyErrorMapping` 里"user_id 为空"和"含空字节"两个子用例变红。
3. 把失败路径也写进缓存（在返回错误前调 `a.cache.put`），确认 `TestVerifyDoesNotCacheFailures` 变红。
4. 把 `cacheSet.get` 里的过期判断去掉（永远返回命中），确认 `TestVerifyReCallsAfterCacheExpires` 与 `TestCacheHitAndExpiry` 都变红。

把四次的真实输出写进报告。

- [ ] **Step 10: 提交**

```bash
git add internal/im/bizauth
git commit -m "$(cat <<'EOF'
feat(im): bizauth 用 HTTP 回调验证业务方令牌，缓存时长由业务方决定

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: multiauth 分派器

按令牌类型把请求送到 fp 或业务方的实现。它自己也实现 `auth.Authenticator`，所以接入层仍然只持有一个认证器。

**Files:**
- Create: `internal/im/multiauth/multiauth.go`
- Test: `internal/im/multiauth/multiauth_test.go`

**Interfaces:**
- Consumes: `auth.Authenticator`、`auth.VerifyRequest`、`auth.ErrUnauthorized`、`model.TokenKindFP`、`model.TokenKindBiz`。
- Produces: `multiauth.New(fp, biz auth.Authenticator) auth.Authenticator`。

- [ ] **Step 1: 写失败测试**

`internal/im/multiauth/multiauth_test.go`：

```go
package multiauth

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
)

// fake 记录自己被调用过，并返回一个可辨认的主体。
type fake struct {
	called bool
	sub    model.Subject
}

func (f *fake) Verify(_ context.Context, _ auth.VerifyRequest) (model.Subject, error) {
	f.called = true
	return f.sub, nil
}

func TestDispatchByKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    string
		wantFP  bool
		wantBiz bool
	}{
		{"空串走 fp", "", true, false},
		{"显式 fp", model.TokenKindFP, true, false},
		{"biz 走业务方", model.TokenKindBiz, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := &fake{sub: model.User("1")}
			biz := &fake{sub: model.Biz("2")}
			a := New(fp, biz)
			if _, err := a.Verify(context.Background(), auth.VerifyRequest{App: "a1", Kind: tc.kind, Token: "t"}); err != nil {
				t.Fatal(err)
			}
			if fp.called != tc.wantFP || biz.called != tc.wantBiz {
				t.Fatalf("分派错了：fp.called=%v biz.called=%v", fp.called, biz.called)
			}
		})
	}
}

func TestUnknownKindIsRejected(t *testing.T) {
	fp := &fake{sub: model.User("1")}
	biz := &fake{sub: model.Biz("2")}
	a := New(fp, biz)
	_, err := a.Verify(context.Background(), auth.VerifyRequest{App: "a1", Kind: "wechat", Token: "t"})
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("未知类型必须拒绝，实际 %v", err)
	}
	// 未知类型不能被兜底送给任何一个下游：一个拼错的类型说明调用方有 bug，
	// 静默降级会让它更难被发现。
	if fp.called || biz.called {
		t.Fatal("未知类型不该被送给任何下游")
	}
}

func TestNilBizRejectsBizKind(t *testing.T) {
	fp := &fake{sub: model.User("1")}
	a := New(fp, nil)
	_, err := a.Verify(context.Background(), auth.VerifyRequest{App: "a1", Kind: model.TokenKindBiz, Token: "t"})
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("没有业务方认证器时 biz 必须拒绝，实际 %v", err)
	}
	if fp.called {
		t.Fatal("不能因为业务方认证器缺席就退回 fp 验：那会让业务方令牌被当成 fp 令牌，报错信息还会误导排查")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/im/multiauth -v`
Expected: 包不存在，编译失败。

- [ ] **Step 3: 实现**

`internal/im/multiauth/multiauth.go`：

```go
// Package multiauth 按握手帧里的令牌类型把验证请求分派给对应的实现。
// 它自己也实现 auth.Authenticator，所以接入层只持有一个认证器，
// 将来加第三种认证方式时接入层一行都不用动。
package multiauth

import (
	"context"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
)

type dispatcher struct {
	fp  auth.Authenticator
	biz auth.Authenticator // 可以为 nil：没有任何 app 配业务方认证时
}

// New 组合两个认证器。biz 为 nil 表示本节点没有业务方认证能力，
// 此时带 biz 类型的握手一律被拒。
func New(fp, biz auth.Authenticator) auth.Authenticator {
	return &dispatcher{fp: fp, biz: biz}
}

func (d *dispatcher) Verify(ctx context.Context, req auth.VerifyRequest) (model.Subject, error) {
	switch req.Kind {
	case "", model.TokenKindFP:
		return d.fp.Verify(ctx, req)
	case model.TokenKindBiz:
		if d.biz == nil {
			return model.Subject{}, auth.ErrUnauthorized
		}
		return d.biz.Verify(ctx, req)
	}
	// 未知类型直接拒，不兜底送给某一个下游：拼错的类型说明调用方有 bug，
	// 静默降级会让它更难被发现，而且报出来的错会指向错误的方向。
	return model.Subject{}, auth.ErrUnauthorized
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/im/multiauth -v`
Expected: 全部 PASS

- [ ] **Step 5: 回退验证**

把 `Verify` 最后那条 `return` 改成 `return d.fp.Verify(ctx, req)`（也就是未知类型兜底给 fp），确认 `TestUnknownKindIsRejected` 变红。恢复后再确认变绿。把输出写进报告。

- [ ] **Step 6: 提交**

```bash
git add internal/im/multiauth
git commit -m "$(cat <<'EOF'
feat(im): multiauth 按令牌类型分派到 fp 或业务方认证器

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: wsapi 接入——握手读原始字节、按类型分派

握手层现在把原始帧字节丢掉了，业务方回调需要它。同时主体解析要能把令牌类型传下去。

**Files:**
- Modify: `internal/im/wsapi/handler.go`
- Test: `internal/im/wsapi/handler_test.go`

**Interfaces:**
- Consumes: Task 2 的 `auth.VerifyRequest`、Task 1 的 `model.AuthFrame.Kind`。
- Produces: 无新导出符号，`wsapi.Deps` 与 `Config` 不变。

- [ ] **Step 1: 写失败测试**

先读 `internal/im/wsapi/handler_test.go` 里现有的 `fakeAuth` 与 `newEnv`，本任务要扩展它们。现在的 `fakeAuth` 是一个 `map[string]model.Subject`，按令牌查表；改成一个能记录收到的完整请求的结构：

```go
// 替换现有的 fakeAuth。除了按 token 查表，还要记下最后一次收到的请求，
// 这样测试才能断言原始字节与令牌类型确实被传下来了。
type fakeAuth struct {
	byToken map[string]model.Subject
	last    auth.VerifyRequest
}

func (a *fakeAuth) Verify(_ context.Context, req auth.VerifyRequest) (model.Subject, error) {
	a.last = req
	if s, ok := a.byToken[req.Token]; ok {
		return s, nil
	}
	return model.Subject{}, auth.ErrUnauthorized
}
```

`newEnv` 里构造它的地方相应改成 `&fakeAuth{byToken: map[string]model.Subject{"tok-1": model.User("1")}}`。

现有的 `env` 结构体没有暴露这个认证器，测试读不到 `last`，所以要给它加一个字段：

```go
type env struct {
	srv    *httptest.Server
	h      *hub.Hub
	stream *hubtest.Stream
	hs     *fakeHandshaker
	apps   hubtest.Apps
	auth   *fakeAuth   // 新增：让测试能断言认证器收到了什么
}
```

`newEnv` 里把构造出来的那个指针同时传给 `Deps.Auth` 和 `env.auth`，两处是同一个对象。字段名以现有文件为准，上面只列出新增的那一行。

然后追加两条测试：

```go
func TestHandshakePassesRawFrameAndKind(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	// 帧里故意带一个网关不认识的字段，它必须原样出现在 Raw 里
	raw := `{"t":"auth","app":"a1","token":"tok-1","kind":"biz","custom":{"deviceId":"d-1"}}`
	if err := c.Write(context.Background(), websocket.MessageText, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return e.auth.last.Token != "" }, "认证器应当被调用")

	if e.auth.last.Kind != "biz" {
		t.Fatalf("令牌类型没传下去，实际 %q", e.auth.last.Kind)
	}
	if string(e.auth.last.Raw) != raw {
		t.Fatalf("原始帧字节不是逐字节转发的：\n收到 %s\n期望 %s", e.auth.last.Raw, raw)
	}
	if e.auth.last.App != "a1" {
		t.Fatalf("app 没传下去，实际 %q", e.auth.last.App)
	}
}

func TestHandshakeDefaultsKindToEmpty(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	// 不带 kind 的老客户端，Kind 应当是空串，由 multiauth 当成 fp 处理
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "tok-1"})
	if f, err := read(t, c); err != nil || f.T != "hello" {
		t.Fatalf("不带 kind 的握手应当成功，实际 %+v %v", f, err)
	}
	if e.auth.last.Kind != "" {
		t.Fatalf("缺省的令牌类型应当是空串，实际 %q——老客户端一行不用改是这次改动的前提", e.auth.last.Kind)
	}
}
```

`handler_test.go` 的 import 要加 `"github.com/basicfu/fp/internal/im/auth"`（如果还没有）。

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/im/wsapi -run TestHandshake -v`
Expected: FAIL，`e.auth.last.Raw` 为空（原始字节没被传下去）。

- [ ] **Step 3: 让握手保留原始字节**

`readAuthFrame` 的返回值加一个原始字节：

```go
// readAuthFrame 读第一帧并解析。第二个返回值是原始字节：
// 业务方的验证回调要把它逐字节转发过去，client 塞的自定义字段
// （设备指纹之类）才能到业务方手里。解析后重新序列化会丢掉未知字段。
func (s *server) readAuthFrame(ctx context.Context, ws *websocket.Conn) (model.AuthFrame, []byte, bool) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		_, data, err := ws.Read(ctx)
		done <- result{data, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return model.AuthFrame{}, nil, false
		}
		var af model.AuthFrame
		// 握手帧必须带 app：fp 签发的 token 不透明，没有 claim，网关要先知道
		// 用哪个 app 的凭据去验它，缺 app 直接拒。
		if json.Unmarshal(r.data, &af) != nil || af.T != model.FrameAuth || af.App == "" {
			return model.AuthFrame{}, nil, false
		}
		return af, r.data, true
	case <-time.After(s.Cfg.AuthTimeout):
		return model.AuthFrame{}, nil, false
	}
}
```

`ServeHTTP` 的调用点改成三返回值：

```go
	af, rawFrame, ok := s.readAuthFrame(ctx, ws)
	if !ok {
		_ = ws.Close(websocket.StatusCode(model.CloseAuthFailed), "auth frame")
		return
	}
```

`resolveSubject` 加一个参数并构造完整的请求：

```go
func (s *server) resolveSubject(ctx context.Context, af model.AuthFrame, raw []byte, cfg model.AppConfig, ip string) (model.Subject, int) {
	switch {
	case af.Token != "":
		sub, err := s.Auth.Verify(ctx, auth.VerifyRequest{
			App:   af.App,
			Kind:  af.Kind,
			Token: af.Token,
			Raw:   raw,
		})
		if errors.Is(err, auth.ErrUnavailable) {
			return model.Subject{}, model.CloseUnavailable
		}
		if err != nil {
			return model.Subject{}, model.CloseAuthFailed
		}
		return sub, 0
	case af.Guest != "":
		if !cfg.AllowGuest || !model.IsUUIDv4(af.Guest) {
			return model.Subject{}, model.CloseAuthFailed
		}
		if !s.Guests.Allow(af.App, ip, af.Guest, cfg.GuestIPRate, time.Now()) {
			return model.Subject{}, model.CloseAuthFailed
		}
		return model.Guest(af.Guest), 0
	}
	return model.Subject{}, model.CloseAuthFailed
}
```

`ServeHTTP` 里的调用点相应加上 `rawFrame` 参数。

**注意访客那条分支不变**：访客不经过认证器，不需要原始字节。

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/im/wsapi -v`
Expected: 全部 PASS，包括原有的十条

- [ ] **Step 5: 回退验证**

把 `resolveSubject` 里的 `Raw: raw` 改成 `Raw: nil`，确认 `TestHandshakePassesRawFrameAndKind` 变红；再把 `Kind: af.Kind` 改成 `Kind: ""`，确认同一条测试因为类型不对而变红。两次都恢复。把输出写进报告。

- [ ] **Step 6: 提交**

```bash
git add internal/im/wsapi
git commit -m "$(cat <<'EOF'
feat(im/wsapi): 握手保留原始帧字节并把令牌类型传给认证器

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: 配置默认值与进程装配

配置加载时填 `biz_auth` 的默认值，进程启动时把两个认证器装进分派器。

**Files:**
- Modify: `internal/im/appcfg/file.go`、`cmd/fp-im/main.go`
- Test: `internal/im/appcfg/file_test.go`、`cmd/fp-im/main_test.go`

**Interfaces:**
- Consumes: Task 1 的 `model.BizAuth`、Task 3 的 `bizauth.New`、Task 4 的 `multiauth.New`。
- Produces: 无新导出符号。

- [ ] **Step 1: 写配置默认值的失败测试**

追加到 `internal/im/appcfg/file_test.go`：

```go
func TestLoadFileFillsBizAuthDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "apps.json")
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1","biz_auth":{"verify_url":"https://x/verify"}}]}`)
	src, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := src.Get("a1")
	if a.BizAuth == nil {
		t.Fatal("biz_auth 应当被保留")
	}
	if a.BizAuth.Timeout.Std() != 2*time.Second {
		t.Fatalf("timeout 默认值应为 2s，实际 %v", a.BizAuth.Timeout.Std())
	}
	if a.BizAuth.CacheSize != 10000 {
		t.Fatalf("cache_size 默认值应为 10000，实际 %d", a.BizAuth.CacheSize)
	}
}

func TestLoadFileRejectsPlaintextVerifyURL(t *testing.T) {
	p := filepath.Join(t.TempDir(), "apps.json")
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1","biz_auth":{"verify_url":"http://x/verify"}}]}`)
	// 明文 http 会让所有业务方令牌暴露给中间人，必须在加载时就拒绝，
	// 而不是等到第一次握手才发现。
	if _, err := LoadFile(p); err == nil {
		t.Fatal("明文 http 的 verify_url 必须在加载时被拒")
	}
}

func TestLoadFileKeepsExplicitBizAuthValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "apps.json")
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1","biz_auth":{"verify_url":"https://x/v","timeout":"5s","cache_size":7}}]}`)
	src, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := src.Get("a1")
	if a.BizAuth.Timeout.Std() != 5*time.Second || a.BizAuth.CacheSize != 7 {
		t.Fatalf("显式值不能被默认值覆盖，实际 timeout=%v cache_size=%d", a.BizAuth.Timeout.Std(), a.BizAuth.CacheSize)
	}
}
```

import 要加 `"time"`。

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/im/appcfg -run TestLoadFile -v`
Expected: FAIL，默认值为零

- [ ] **Step 3: 填默认值**

`internal/im/appcfg/file.go` 的 `Reload()` 里，在现有三条默认值填充之后、`Validate()` 之前，加：

```go
		// biz_auth 是可选的；配了才填默认值。
		// 填充必须在 Validate 之前：Validate 要求 timeout 与 cache_size 大于零，
		// 顺序反了的话不写这两项的配置会被自己的校验拒掉。
		if a.BizAuth != nil {
			if a.BizAuth.Timeout <= 0 {
				a.BizAuth.Timeout = model.Duration(2 * time.Second)
			}
			if a.BizAuth.CacheSize == 0 {
				a.BizAuth.CacheSize = 10000
			}
		}
```

import 要加 `"time"`。

**注意 `a` 是循环变量的副本，而 `BizAuth` 是指针**：改 `a.BizAuth.Timeout` 会改到底层那个结构。这在这里是想要的效果（副本和原对象指向同一个 BizAuth），但要在注释里点明，免得后来人以为改的是副本。

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/im/appcfg -v`
Expected: 全部 PASS

- [ ] **Step 5: 写装配的失败测试**

追加到 `cmd/fp-im/main_test.go`。先读该文件里现有的 `serve` 测试怎么起进程、怎么造配置文件，沿用同一套。

**这条测试的构造有个真实的障碍**：假的验证服务用自签证书，而配置校验要求 HTTPS，进程侧的 HTTP 客户端默认不信任自签证书。三条出路：

1. 断言弱一点：只验证"带 `kind=biz` 的握手会去调那个地址"，用调用计数判断，握手最终因为证书不被信任而拿到 4004。这仍然证明了装配是通的。
2. 给 `bizauth.Config` 加一个 `Client` 注入点（它已经有了），在 `cmd` 里从环境变量读一个"跳过证书校验"的开关。**不要这么做**：给生产代码开一个绕过 TLS 校验的口子，为了测试而降低安全性，代价远大于收益。
3. 把这条端到端断言放到 `internal/integration`，那里可以直接构造 `bizauth.Authenticator` 并注入 `verify.Client()`。

**选第一条。** 在 `cmd` 层只验证装配是通的：配置带 `biz_auth` 时进程能正常启动，带 `kind=biz` 的握手确实发起了一次外部调用。真正的回调行为由 Task 3 的包内测试覆盖，那里可以自由注入客户端。

按这个思路写：

```go
func TestServeWithBizAuthStartsAndDispatches(t *testing.T) {
	var calls atomic.Int64
	verify := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"user_id":"biz-42"}`))
	}))
	defer verify.Close()

	env := startServe(t, withApps(`{"apps":[{"app_id":"a1","app_secret":"s1","allow_guest":true,
		"biz_auth":{"verify_url":"`+verify.URL+`"}}]}`))

	c := env.dialWS(t)
	defer c.CloseNow()
	sendFrame(t, c, map[string]any{"t": "auth", "app": "a1", "token": "whatever", "kind": "biz"})

	// 证书不被信任，所以握手最终会失败并拿到 4004。但请求确实发出去了，
	// 这证明 cmd 里的装配把 kind=biz 路由到了 bizauth 而不是 fpauth。
	_, err := readFrame(t, c)
	if websocket.CloseStatus(err) != model.CloseUnavailable {
		t.Fatalf("回调失败应当以 4004 关闭，实际 %v", err)
	}
	waitUntil(t, func() bool { return calls.Load() > 0 }, "带 kind=biz 的握手必须打到业务方的验证地址；打不到说明装配把它错误地路由给了 fp 认证器")
}
```

`startServe`、`withApps`、`dialWS`、`sendFrame`、`readFrame` 这些辅助函数在现有的 `main_test.go` 里已经有对应物，名字以现有文件为准，不要另起一套。

- [ ] **Step 6: 跑测试确认失败**

Run: `./scripts/test.sh ./cmd/fp-im -run TestServeWithBizAuth -v`
Expected: FAIL，调用计数为零（装配还没有把 biz 路由出去）

- [ ] **Step 7: 装配**

`cmd/fp-im/main.go`，在构造 `fpauth` 之后、构造 `hub` 之前，加：

```go
	// 业务方认证器与 fp 认证器并列，由 multiauth 按握手帧里的令牌类型分派。
	// 即使没有任何 app 配了 biz_auth 也照常构造：判断"这个 app 支不支持"
	// 在 bizauth 内部按 app 配置做，比在这里按全局有无来决定更精确。
	bizAuthn, err := bizauth.New(bizauth.Config{Apps: apps, Logger: log})
	if err != nil {
		return err
	}
	authn := multiauth.New(fpAuthn, bizAuthn)
```

原来传给 `wsapi.Deps` 的那个认证器换成 `authn`。注意原有变量名可能是 `authn`，如果是，把 `fpauth.New` 的结果改名为 `fpAuthn`，避免遮蔽。

import 加两个新包。

- [ ] **Step 8: 跑测试确认通过**

Run: `./scripts/test.sh ./cmd/fp-im -v`
Expected: 全部 PASS

- [ ] **Step 9: 回退验证**

把装配改回只用 `fpAuthn`（不经过分派器），确认 `TestServeWithBizAuthStartsAndDispatches` 变红。恢复后确认变绿。把输出写进报告。

- [ ] **Step 10: 提交**

```bash
git add internal/im/appcfg cmd/fp-im
git commit -m "$(cat <<'EOF'
feat(cmd): 装配业务方认证器，配置补 biz_auth 默认值

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: 客户端 SDK 的业务方主体与令牌类型

`sdk/im` 里主体解析要认 `b:` 前缀，客户端配置要能声明令牌类型。这份代码与网关侧是有意的重复（`sdk/` 不能 import `internal/`），行为必须逐字一致。

**Files:**
- Modify: `sdk/im/subject.go`、`sdk/im/client.go`
- Test: `sdk/im/subject_test.go`、`sdk/im/client_test.go`

**Interfaces:**
- Produces: `fpim.KindBiz`、`fpim.Biz()`、`fpim.TokenKindFP`、`fpim.TokenKindBiz`、`ClientConfig.Kind`。

- [ ] **Step 1: 写失败测试**

追加到 `sdk/im/subject_test.go`：

```go
func TestParseBizSubject(t *testing.T) {
	got, err := Parse("b:1001")
	if err != nil {
		t.Fatalf("合法的业务方主体被拒：%v", err)
	}
	if got != Biz("1001") || got.String() != "b:1001" {
		t.Fatalf("Parse(\"b:1001\")=%+v String()=%q", got, got.String())
	}
	if _, err := Parse("b:"); err == nil {
		t.Fatal("空的业务方 id 必须被拒")
	}
}
```

追加到 `sdk/im/client_test.go`：

```go
func TestDialSendsTokenKind(t *testing.T) {
	f, url := newFake(t)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok", Kind: TokenKindBiz})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if len(f.auths) != 1 || f.auths[0]["kind"] != "biz" {
		t.Fatalf("握手帧里的令牌类型不对：%v", f.auths)
	}
}

func TestDialOmitsKindWhenEmpty(t *testing.T) {
	f, url := newFake(t)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// 不设类型时不发这个字段，让服务端按缺省当成 fp。
	// 发一个空串也能工作，但会让抓包和日志里多一个无意义的键。
	if _, present := f.auths[0]["kind"]; present {
		t.Fatalf("没设类型时不该发 kind 字段，实际 %v", f.auths[0])
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./sdk/im -run 'TestParseBizSubject|TestDial.*Kind' -v`
Expected: FAIL，`Biz`、`TokenKindBiz`、`ClientConfig.Kind` 未定义

- [ ] **Step 3: 实现**

`sdk/im/subject.go`，常量块加：

```go
	// KindBiz 是业务方自己的认证体系里的用户。与 KindUser 分开，是为了让
	// fp 的用户 1001 和业务方的用户 1001 在网关的 Redis key 上天然隔离。
	// 这份定义与 internal/im/model 的那份必须逐字一致，由
	// internal/integration 的配对测试守着——sdk 不能 import internal，
	// 所以这个重复是架构约束下的必然。
	KindBiz SubjectKind = "b"
```

加构造函数与解析分支：

```go
func Biz(id string) Subject { return Subject{Kind: KindBiz, ID: id} }
```

`Parse` 的 switch 加：

```go
	case KindBiz:
		if id == "" {
			return Subject{}, fmt.Errorf("%w: 业务方 id 不能为空", ErrBadSubject)
		}
		return Biz(id), nil
```

**注意这里只校验非空，不校验长度与空字节。** 网关侧的 `ValidateUserID` 还查那两项，但那是给"业务方回调返回的值"用的入口校验；客户端侧解析的是网关已经发下来的主体串，网关不会发出非法值。两侧对同一个输入的**接受与拒绝结论**必须一致，配对测试断言的正是这一点，而 128 字节以内且不含空字节的输入在两边行为相同。**配对测试的输入集不要包含超长或含空字节的业务方主体**，否则会暴露这个有意的差异。

`sdk/im/client.go`，加令牌类型常量：

```go
// 握手帧里的令牌类型。空表示 fp，与服务端的缺省一致。
const (
	TokenKindFP  = "fp"
	TokenKindBiz = "biz"
)
```

`ClientConfig` 加字段：

```go
	// Kind 声明 Token 是哪一种。留空表示 fp 签发的令牌。
	// 业务方自己签发的令牌填 TokenKindBiz，网关会回调该应用配置里的验证地址。
	Kind string
```

`connect` 里构造握手帧的地方加：

```go
	if c.cfg.Kind != "" {
		auth["kind"] = c.cfg.Kind
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `./scripts/test.sh ./sdk/... -v`
Expected: 全部 PASS，包括架构断言

- [ ] **Step 5: 回退验证**

把 `connect` 里那个条件改成无条件写入（`auth["kind"] = c.cfg.Kind`），确认 `TestDialOmitsKindWhenEmpty` 变红。恢复后确认变绿。把输出写进报告。

- [ ] **Step 6: 提交**

```bash
git add sdk/im
git commit -m "$(cat <<'EOF'
feat(sdk/im): 加业务方主体前缀与握手帧令牌类型

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: 配对断言与文档

主体解析有两份实现，配对断言是防止它们漂移的唯一防线。文档要写清新协议、新配置和那条"失败一律拒绝"的运维语义。

**Files:**
- Modify: `internal/integration/im_parity_test.go`、`docs/im.md`
- Test: `internal/integration/im_parity_test.go`

**Interfaces:**
- Consumes: Task 1 与 Task 7 的两份主体解析。

- [ ] **Step 1: 扩充配对断言**

`internal/integration/im_parity_test.go` 里 `TestSubjectFormatParity` 的输入集加上业务方主体。现有输入覆盖了合法用户、合法访客、大写标识、无连字符、版本位错误、未知前缀、空串，加这三条：

```go
		"b:1001",   // 合法的业务方主体
		"b:",       // 空 id，两边都要拒
		"b:a-b_c",  // 业务方的 id 不约束字符集，两边都要接受
```

**不要加超长或含空字节的业务方主体。** 网关侧的解析会额外查这两项（那是给业务方回调的返回值用的入口校验），客户端侧不查，两边对这类输入的结论不同，加进配对断言会让它红，而那个差异是有意的。这条限制要写进测试注释。

- [ ] **Step 2: 跑测试确认通过**

Run: `./scripts/test.sh ./internal/integration -run TestSubjectFormatParity -v`
Expected: PASS

- [ ] **Step 3: 回退验证**

把 `sdk/im/subject.go` 的 `Parse` 里 `KindBiz` 那个分支删掉，确认配对断言变红（一边接受一边拒绝）。恢复后确认变绿。把输出写进报告。

- [ ] **Step 4: 更新交接文档**

`docs/im.md` 要加三处。

**握手协议那一节**，在现有的帧说明后面加：

```markdown
握手帧的 `kind` 字段决定这个令牌送去哪里验证：

| kind | 验证方 | 主体前缀 |
|---|---|---|
| 不写或 `fp` | fp，走 SDK | `u:` |
| `biz` | 回调该应用配置里的 `verify_url` | `b:` |

不写 `kind` 的老客户端行为不变。带 `guest` 的访客路径也不变。

业务方的用户 1001 是 `b:1001`，与 fp 的 `u:1001` 在 Redis 键上天然隔离，
两者不会互相顶号、不会收到对方的消息。
```

**配置那一节**，在 apps 文件的例子里加上 `biz_auth`，并说明：

```markdown
`biz_auth` 整组为空表示这个应用不支持业务方令牌。`verify_url` 必填且必须是
https（令牌明文走在请求体里）；`timeout` 缺省 2 秒，是整个请求的超时；
`cache_size` 缺省 10000。
```

**新增一节讲业务方验证接口的约定**：

```markdown
## 业务方验证接口

网关收到 `kind` 为 `biz` 的握手时，把 client 发上来的**握手帧原始字节**
原样 POST 到你配置的地址，不添加任何自己的东西，也不带凭据。

请求：

    POST <verify_url>
    Content-Type: application/json

    {"t":"auth","app":"a1","token":"...","kind":"biz","ua":"..."}

client 塞的自定义字段会原样到你手里，可以直接用。

响应：

| 状态码 | body | 网关动作 |
|---|---|---|
| 200 | `{"user_id":"1001"}` | 主体 `b:1001`，放行 |
| 200 | `user_id` 为空、含空字节或超过 128 字节 | 拒绝，关闭码 4001 |
| 200 | body 不是合法 JSON | 拒绝，关闭码 4004 |
| 401 | 任意 | 拒绝，关闭码 4001 |
| 其它、超时、连不上 | 任意 | 拒绝，关闭码 4004 |

**验证失败一律拒绝，网关不会放行。** 你的验证接口挂了，就是这个应用的
业务方令牌全都连不上。这是有意的：验不了就放行等于任何字符串都能当令牌。

**缓存由你决定。** 在响应里带 `cache_seconds`，网关就按它缓存这个令牌的
验证结果；不带就每次握手都调你一次。

    {"user_id":"1001","cache_seconds":60}

强烈建议带上。网关某个节点崩溃时，它上面的所有连接会在同一时刻重连到
其它节点，你的验证接口会在几秒内被打上成千上万次。这是网关的固有行为，
不是异常情况。

缓存只对成功的结果生效，失败不缓存，所以你的接口恢复之后 client 立刻
就能连上。

**这个接口对公网是裸的**，网关不带任何凭据，没有东西证明请求来自网关。
实际风险有限（攻击者要先有令牌才能拿去验，而有了有效令牌本来就能直接
连网关），但建议把它放在内网或做网络层隔离。
```

**运维要点那一节**加一句：`biz_auth` 的验证接口是该应用业务方令牌的强依赖，它不可用时对应的新连接会全部被拒（fp 令牌与访客不受影响）。

- [ ] **Step 5: 跑全仓测试**

Run: `./scripts/test.sh ./... -v`
Expected: 除已知的环境噪音外全绿

- [ ] **Step 6: 提交**

```bash
git add internal/integration docs/im.md
git commit -m "$(cat <<'EOF'
test(integration): 配对断言覆盖业务方主体；docs: 补双认证协议与配置

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## 三、验收对照表

| # | 验收项（设计文档章节） | 由谁保证 |
|---|---|---|
| 1 | 三种身份靠帧字段组合区分，未知类型被拒（三） | `multiauth` 的分派测试、`wsapi` 的握手测试 |
| 2 | 缺省视为 fp，老客户端不用改（3.1） | `wsapi` 的缺省测试、`sdk/im` 的省略字段测试 |
| 3 | 接口改收请求结构（四） | 编译期保证，`fpauth` 与 `bizauth` 两个实现 |
| 4 | 原始字节逐字节转发（5.1） | `bizauth` 与 `wsapi` 各一条，都做回退验证 |
| 5 | 六条响应路径映射到正确的关闭码（5.2） | `bizauth` 的错误映射表驱动测试 |
| 6 | 用户标识校验（5.3） | `model` 的校验测试、`bizauth` 的两个非法子用例 |
| 7 | 缓存由业务方决定、失败不缓存、容量上限（六） | `bizauth` 的四条缓存测试 |
| 8 | 缓存键无拼接歧义（6.3） | `bizauth` 的键歧义测试 |
| 9 | 业务方主体前缀隔离（七） | `model` 与 `sdk/im` 各一条，加配对断言 |
| 10 | 配置校验与默认值（八） | `model` 的校验测试、`appcfg` 的三条默认值测试 |
| 11 | HTTPS 强制（8.2） | `model` 与 `appcfg` 各一条 |
| 12 | 装配把 biz 路由到业务方认证器（十） | `cmd/fp-im` 的装配测试 |

## 四、给执行者的最后几句

**最容易写成"看起来对"的地方**

- **原始字节必须是 `readAuthFrame` 读到的那一份**，不能在别处重新序列化。解析后再 `json.Marshal` 会丢掉客户端塞的未知字段，而那正是这个设计要保留的东西。两条测试都做了回退验证要求，不要跳过。
- **失败不进缓存。** 写成"先缓存再判断错误"会让业务方接口恢复之后客户端还要等一个负缓存过期。
- **未知令牌类型不要兜底给 fp。** 兜底会让拼错的类型静默走错路径，报出来的错还指向错误的方向。
- **配置默认值必须填在校验之前**，顺序反了的话，不写超时和容量的合法配置会被自己的校验拒掉。
- **两份主体解析对同一输入的接受与拒绝结论必须一致。** 网关侧多查长度与空字节是有意的，所以配对断言的输入集不能包含那两类。

**数值约束**：验证超时默认 2 秒且必须明显小于握手的 5 秒上限；缓存容量默认 10000；用户标识上限 128 字节；响应体读取上限 64 KB。改任何一个都要同时改设计文档第八节。

### 本计划自身的已知不足

| 处 | 情况 | 要求 |
|---|---|---|
| Task 6 的装配测试 | 只验证"请求发出去了"，不验证完整的成功路径，因为假验证服务用自签证书而生产代码不该为测试开跳过校验的口子 | 完整的回调行为由 Task 3 的包内测试覆盖，那里能注入客户端 |
| 业务方接口的鉴权 | 本计划不做，设计文档 5.1 记了取舍 | 接入方自己做网络层隔离 |
| 缓存容量的热更新 | 首次为某应用建缓存时定容，之后改配置不缩容，重启才生效 | 已写进 `cache.go` 注释，不值得为它引入重建逻辑 |

**断言点本身是硬要求。**
