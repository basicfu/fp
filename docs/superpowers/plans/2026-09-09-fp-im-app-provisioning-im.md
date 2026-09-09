# fp-im app 配置下发 · 第二阶段（fp-im 侧）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**前置**：第一阶段（`2026-09-09-fp-im-app-provisioning-fp.md`）必须已经发布，控制台已生成 IM 凭据，需要接入的应用已打开 `im_enabled` 并配好参数。

**Goal:** fp-im 删掉本地 apps 文件，不再持有任何业务应用的 `app_secret`，app 的准入与策略全部向 fp 实时索取。

**Architecture:** fp-im 到 fp 收敛成**一个** `fpsdk.Client`（凭据是 IM secret，`CallerType=im`，连接级不带 app_id），app 作用域由每次调用从 client 的 ws 握手帧取、`fpsdk.WithAppID` 附上。`internal/im/appcfg`（读本地文件）换成 `internal/im/fpappcfg`（向 fp 拉 + 进程内缓存 + Watch 流刷新）。业务 server 的连入凭据转给 fp 的 `VerifyAppCredential` 核实。

**Tech Stack:** Go 1.25 / Redis / gRPC / coder/websocket

## Global Constraints

- **设计文档**：`docs/superpowers/specs/2026-09-09-fp-im-app-provisioning-design.md`。每个任务开始前读一遍对应章节。
- **跑测试只用 `./scripts/test.sh`**。
- **`internal/im/**` 不得 import `fpsdk` 的手写部分，唯一例外是 `internal/im/fpauth`**（`internal/im/arch_test.go` 断言）。生成代码 `sdk/gen` 谁都可以用。新增的 `fpappcfg` 若要碰 `fpsdk`，**必须**把那部分收进 `fpauth` 或走接口注入——不要去改 arch_test 放宽约束。
- **`sdk/` 不得 import `internal/`**。
- **不引入任何新依赖。**
- **注释和错误信息一律中文。**
- **每条标「辨别力」的测试，实现后必须做一次变异验证。**
- **提交粒度**：每个 TDD 循环结束就 commit。
- **绝不 `git add -A`。**

---

## 文件结构

**新建：**

| 文件 | 职责 |
|---|---|
| `internal/im/fpappcfg/source.go` | `auth.AppConfigSource` 的 fp 实现：拉取、缓存、失效 |
| `internal/im/fpappcfg/source_test.go` | 缓存命中/未命中、拉取失败、失效 |

**改：**

| 文件 | 改什么 |
|---|---|
| `internal/im/auth/auth.go` | `AppConfigSource` 加 `Load(ctx, app) error`；`Get` 保持纯内存读 |
| `internal/im/model/appconfig.go` | 删 `AppSecret` 字段与相关校验 |
| `internal/im/fpauth/fpauth.go` | N 个 client → 1 个；`OnRevoke` 按 `ev.AppID` 分派，空串扇给所有 app |
| `internal/im/imgrpc/appauth.go` | 本地明文比对 → `VerifyAppCredential` + 只缓存成功 |
| `internal/im/config/config.go` | `FPSDK` 加 `Secret`，删 `AppsFile` |
| `cmd/fp-im/main.go` | 装配换成新的 source；新 app 加载后 `TrackApp` + 同步 `Refresh` |
| `config-im.example.yaml` | `fpsdk.secret`，删 `apps_file` |
| `docs/im.md` | 启动与运维章节重写 |

**删：** `internal/im/appcfg/`（整个包）、`tmp/im-apps.json`

---

## Task 1: AppConfigSource 接口容纳 I/O

**Files:**
- Modify: `internal/im/auth/auth.go`
- Modify: `internal/im/hub/hubtest/fakes.go`（测试替身跟上新接口）

**Interfaces:**
- Produces:
  ```go
  type AppConfigSource interface {
      // Load 确保 app 的配置已在本地缓存里。可能有网络 I/O，只在握手与
      // 业务 server 接入这类冷路径调用。
      Load(ctx context.Context, app string) error
      // Get 是纯内存读：握手与 Push 的热路径都会调它。
      Get(app string) (model.AppConfig, bool)
      // Apps 返回已缓存的 app 列表。
      Apps() []string
  }
  ```

- [ ] **Step 1: 改接口与替身**

`internal/im/auth/auth.go`：

```go
// AppConfigSource 提供 app 级配置。
//
// 拆成 Load 与 Get 两个方法，是因为配置的来源从本地文件变成了 fp：
// 拉取有网络 I/O，而 Get 在握手与 Push 的热路径上被调用，必须是纯内存读。
// 调用约定是"冷路径先 Load，之后热路径随便 Get"——握手一定先于该 app 的
// 任何消息投递，所以这个顺序天然成立。
type AppConfigSource interface {
	Load(ctx context.Context, app string) error
	Get(app string) (model.AppConfig, bool)
	Apps() []string
}
```

`internal/im/hub/hubtest/fakes.go` 里的 `Apps` 替身补一个空实现的 `Load`。

- [ ] **Step 2: 编译**

```bash
go build ./... && ./scripts/test.sh ./internal/im/...
```

预期：`internal/im/appcfg` 会因为缺 `Load` 而编译失败——给它补一个直接返回 nil 的
`Load`（本地文件已经全量在内存里，没有"按需加载"这回事）。这个包 Task 4 会整个删掉，
现在只是让它继续能编译。

- [ ] **Step 3: 提交**

```bash
git add internal/im/auth/auth.go internal/im/hub/hubtest/fakes.go internal/im/appcfg/file.go
git commit -m "refactor(im/auth): AppConfigSource 拆出 Load，为 I/O 来源让路"
```

---

## Task 2: fpappcfg —— 向 fp 拉配置的 AppConfigSource

**Files:**
- Create: `internal/im/fpappcfg/source.go`, `internal/im/fpappcfg/source_test.go`

**Interfaces:**
- Consumes: 第一阶段的 `fpsdk.AppIMConfig`（通过下面的窄接口注入，**本包不 import fpsdk**）
- Produces:
  - `fpappcfg.Fetcher` 接口：`Fetch(ctx context.Context, app string) (model.AppConfig, error)`
  - `fpappcfg.New(f Fetcher, onLoad func(app string)) *Source`
  - `(*Source)` 实现 `auth.AppConfigSource`
  - `(*Source).Invalidate(app string)` —— 收到变更推送时丢掉缓存

**为什么要 `Fetcher` 这层接口**：`internal/im/**` 只有 `fpauth` 允许 import `fpsdk`
（`arch_test.go` 断言）。本包只认这个窄接口，真正的 `fpsdk` 调用在 `fpauth` 里实现并
注入进来。这不是为测试凭空造的抽象——它是那条架构约束的直接后果。

- [ ] **Step 1: 写失败的测试**

`internal/im/fpappcfg/source_test.go`：

```go
package fpappcfg

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/basicfu/fp/internal/im/model"
)

type fakeFetcher struct {
	mu    sync.Mutex
	calls map[string]int
	cfg   map[string]model.AppConfig
	err   error
}

func (f *fakeFetcher) Fetch(_ context.Context, app string) (model.AppConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[app]++
	if f.err != nil {
		return model.AppConfig{}, f.err
	}
	c, ok := f.cfg[app]
	if !ok {
		return model.AppConfig{}, errors.New("no such app")
	}
	return c, nil
}

func (f *fakeFetcher) count(app string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[app]
}

func newFake(cfg map[string]model.AppConfig) *fakeFetcher {
	return &fakeFetcher{calls: map[string]int{}, cfg: cfg}
}

// TestLoadCachesAndGetIsPureMemory：Get 在握手与 Push 的热路径上，
// 必须是纯内存读——第二次 Load 不能再回源。
func TestLoadCachesAndGetIsPureMemory(t *testing.T) {
	f := newFake(map[string]model.AppConfig{"a1": {AppID: "a1", ConnPolicy: model.PolicyReplace, ConnLimit: 5}})
	s := New(f, nil)
	ctx := context.Background()

	if _, ok := s.Get("a1"); ok {
		t.Fatal("Load 之前 Get 不该命中")
	}
	if err := s.Load(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("a1"); !ok {
		t.Fatal("Load 之后 Get 必须命中")
	}
	if err := s.Load(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if n := f.count("a1"); n != 1 {
		t.Fatalf("回源了 %d 次，缓存命中时不该回源", n)
	}
}

// TestLoadFailureIsNotCached：拉取失败不能进缓存。
//
// 缓存键来自 client 的握手帧（未认证输入），缓存失败等于把 map 的大小交给
// 攻击者——与 fp 的 appVerifier 只缓存成功是同一条推理。
func TestLoadFailureIsNotCached(t *testing.T) {
	f := newFake(nil)
	f.err = errors.New("fp 不可达")
	s := New(f, nil)

	for i := 0; i < 3; i++ {
		if err := s.Load(context.Background(), "ghost"); err == nil {
			t.Fatal("拉取失败必须返回错误")
		}
	}
	if _, ok := s.Get("ghost"); ok {
		t.Fatal("失败的拉取不能留下缓存条目")
	}
	if n := f.count("ghost"); n != 3 {
		t.Fatalf("回源 %d 次，失败不缓存意味着每次都要回源", n)
	}
}

// TestInvalidateForcesRefetch：收到 fp 的变更推送后，下一次 Load 必须回源。
func TestInvalidateForcesRefetch(t *testing.T) {
	f := newFake(map[string]model.AppConfig{"a1": {AppID: "a1", ConnPolicy: model.PolicyReplace, ConnLimit: 5}})
	s := New(f, nil)
	ctx := context.Background()
	if err := s.Load(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	s.Invalidate("a1")
	if _, ok := s.Get("a1"); ok {
		t.Fatal("Invalidate 之后 Get 不该再命中")
	}
	if err := s.Load(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if n := f.count("a1"); n != 2 {
		t.Fatalf("回源 %d 次，Invalidate 之后应当重新拉一次", n)
	}
}

// TestOnLoadFiresForNewAppOnly：onLoad 是给调用方同步派生状态用的
// （cmd/fp-im 要在这里把新 app 加进 srv 追踪集合并强制刷新一次）。
// 缓存命中时不该触发——那会让每次握手都多做一次无谓的 Redis 刷新。
func TestOnLoadFiresForNewAppOnly(t *testing.T) {
	f := newFake(map[string]model.AppConfig{"a1": {AppID: "a1", ConnPolicy: model.PolicyReplace, ConnLimit: 5}})
	var got []string
	s := New(f, func(app string) { got = append(got, app) })
	ctx := context.Background()
	_ = s.Load(ctx, "a1")
	_ = s.Load(ctx, "a1")
	if len(got) != 1 || got[0] != "a1" {
		t.Fatalf("onLoad 触发情况 = %v，期望只在首次加载时触发一次", got)
	}
}

// TestConcurrentLoadFetchesOnce：一个 app 的第一批 client 往往同时握手，
// 不合并的话 fp 会被打上 N 次同样的请求。
func TestConcurrentLoadFetchesOnce(t *testing.T) {
	f := newFake(map[string]model.AppConfig{"a1": {AppID: "a1", ConnPolicy: model.PolicyReplace, ConnLimit: 5}})
	s := New(f, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Load(context.Background(), "a1") }()
	}
	wg.Wait()
	if n := f.count("a1"); n != 1 {
		t.Fatalf("并发 Load 回源了 %d 次，应当合并成 1 次", n)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/im/fpappcfg
```

预期：`undefined: New`。

- [ ] **Step 3: 实现**

`internal/im/fpappcfg/source.go`：

```go
// Package fpappcfg 是 AppConfigSource 的 fp 实现：app 的准入与策略向 fp
// 实时索取，进程内缓存，靠 fp 的推送失效。
//
// 它取代了早期的 internal/im/appcfg（读本地 JSON 文件）。那份文件里存着
// 每个 app 的 app_secret 明文，而 fp 只存 bcrypt 哈希——两边天然会漂移，
// 且 fp-im 根本不需要持有那些 secret。
package fpappcfg

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/basicfu/fp/internal/im/model"
)

// Fetcher 是本包对 fp 的全部依赖。
//
// 用窄接口而不是直接 import fpsdk：internal/im/** 里只有 fpauth 允许碰
// fpsdk 的手写部分（internal/im/arch_test.go 断言）。真正的调用在 fpauth
// 里实现并注入进来。
type Fetcher interface {
	Fetch(ctx context.Context, app string) (model.AppConfig, error)
}

type Source struct {
	f Fetcher
	// onLoad 在**首次**成功加载某个 app 之后调用，让调用方同步自己那份派生
	// 状态。唯一的用途见 cmd/fp-im：新 app 必须立刻加进 srv 表的追踪集合并
	// 强制刷新一次，否则它在本节点的第一条上行消息或连接事件会因为转发候选
	// 列表为空而被静默丢弃、零日志。
	onLoad func(app string)

	// sf 合并同一个 app 的并发拉取。一个 app 的第一批 client 往往同时握手，
	// 不合并的话 fp 会被打上 N 次同样的请求。
	sf singleflight.Group

	mu   sync.RWMutex
	apps map[string]model.AppConfig
}

func New(f Fetcher, onLoad func(app string)) *Source {
	return &Source{f: f, onLoad: onLoad, apps: map[string]model.AppConfig{}}
}

// Load 确保 app 的配置已在缓存里。有网络 I/O，只在冷路径调用。
//
// **失败不入缓存**：缓存键来自 client 的握手帧（未认证输入），缓存失败等于
// 把这个 map 的大小交给攻击者——与 fp 的 appVerifier 同一条推理。副作用是
// 不存在的 app 每次握手都会回源一次，由 fp 侧的限流兜底。
func (s *Source) Load(ctx context.Context, app string) error {
	s.mu.RLock()
	_, ok := s.apps[app]
	s.mu.RUnlock()
	if ok {
		return nil
	}
	_, err, _ := s.sf.Do(app, func() (any, error) {
		// 双检：等在 singleflight 上的那批协程醒来时，赢家可能已经写好了。
		s.mu.RLock()
		_, ok := s.apps[app]
		s.mu.RUnlock()
		if ok {
			return nil, nil
		}
		cfg, err := s.f.Fetch(ctx, app)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.apps[app] = cfg
		fn := s.onLoad
		s.mu.Unlock()
		// 回调在锁外调用：它是调用方给的任意代码，拿着 s.mu 调用它意味着
		// 回调里任何一次 Get/Apps（都要 RLock）都会自锁死。
		if fn != nil {
			fn(app)
		}
		return nil, nil
	})
	return err
}

func (s *Source) Get(app string) (model.AppConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.apps[app]
	return c, ok
}

func (s *Source) Apps() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.apps))
	for k := range s.apps {
		out = append(out, k)
	}
	return out
}

// Invalidate 丢掉某个 app 的缓存，下一次 Load 会重新回源。
// 由 fp 的 AppIMConfigChanged 推送触发。
//
// 丢弃而不是就地重拉：重拉要网络 I/O，而这里是推送流的读循环，一个慢回调
// 等于拖慢整条推送流。丢掉之后由下一次握手按需拉，代价只是那一次握手多一次
// 往返。
func (s *Source) Invalidate(app string) {
	s.mu.Lock()
	delete(s.apps, app)
	s.mu.Unlock()
}
```

`golang.org/x/sync` 已经是登记在案的直接依赖（`sdk` 的 singleflight 在用），不需要
改白名单。

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/im/fpappcfg -v
```

预期：五条全 PASS。

- [ ] **Step 5: 变异验证**

把 `Load` 里失败分支改成也写进缓存（存零值），确认 `TestLoadFailureIsNotCached` 变红；
把 `singleflight` 去掉，确认 `TestConcurrentLoadFetchesOnce` 变红。都改回来。

- [ ] **Step 6: 提交**

```bash
git add internal/im/fpappcfg/
git commit -m "feat(im/fpappcfg): 向 fp 索取 app 配置的 AppConfigSource"
```

---

## Task 3: fpauth 收敛成一个 client

**Files:**
- Modify: `internal/im/fpauth/fpauth.go`
- Test: `internal/im/fpauth/fpauth_test.go`

**Interfaces:**
- Consumes: 第一阶段的 `fpsdk.Options.CallerType`、`fpsdk.WithAppID`、`(*Client).IMGateway()`；Task 2 的 `fpappcfg.Fetcher`
- Produces:
  - `fpauth.Config{FPAddr, Secret string; Insecure bool; OnRevoke func(app string, tokens []string); AllApps func() []string; Logger *slog.Logger}`
  - `(*Authenticator)` 仍实现 `auth.Authenticator`
  - `(*Authenticator).Fetch(ctx, app) (model.AppConfig, error)` —— 满足 `fpappcfg.Fetcher`
  - `(*Authenticator).VerifyAppCredential(ctx, app, secret) error` —— 给 `imgrpc` 用

**注意**：`Config.Apps` 字段删除——fpauth 不再需要 app 配置源（它以前用它取 secret）。
新增 `AllApps func() []string`，用于跨应用撤销的扇出（见 Step 1 的测试）。

- [ ] **Step 1: 写失败的测试**

追加到 `internal/im/fpauth/fpauth_test.go`：

```go
// TestCrossAppRevokeFansOutToAllApps 是本阶段最重要的一条测试。
//
// RevokeEvent.AppID 为空串表示**跨全部应用**的撤销（改密、冻结）。
//
// 以前 fp-im 每个 app 一个 client，这条事件会被 N 个客户端各收一份，每个
// 用自己闭包里的 app 调 OnRevoke——扇出是靠连接数量天然做到的。收敛成一条
// 流之后只收到一份，照抄旧写法（OnRevoke(ev.AppID, ...)）会拿空串去查
// hub.byToken（键是 app + "\x00" + token），一条也查不到——**改密码、冻结
// 用户之后 ws 全都不会被关，而且零报错、零日志**。
//
// 这属于"删掉也不会让任何现有测试变红"的那类，必须显式钉住。
func TestCrossAppRevokeFansOutToAllApps(t *testing.T) {
	var mu sync.Mutex
	var got []string
	a := newTestAuthenticator(t, Config{
		FPAddr:   "127.0.0.1:1",
		Secret:   "im-secret",
		AllApps:  func() []string { return []string{"a1", "a2", "a3"} },
		OnRevoke: func(app string, _ []string) { mu.Lock(); got = append(got, app); mu.Unlock() },
	})

	a.dispatchRevoke(fpsdk.RevokeEvent{AppID: "", Tokens: []string{"t1"}})

	mu.Lock()
	defer mu.Unlock()
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"a1", "a2", "a3"}) {
		t.Fatalf("跨应用撤销扇出到 %v，必须覆盖本节点持有连接的每个 app", got)
	}
}

// TestScopedRevokeGoesToOneApp：带 app 的撤销只投给那一个 app，
// 不能因为补了扇出就把所有 app 都扫一遍。
func TestScopedRevokeGoesToOneApp(t *testing.T) {
	var mu sync.Mutex
	var got []string
	a := newTestAuthenticator(t, Config{
		FPAddr:   "127.0.0.1:1",
		Secret:   "im-secret",
		AllApps:  func() []string { return []string{"a1", "a2", "a3"} },
		OnRevoke: func(app string, _ []string) { mu.Lock(); got = append(got, app); mu.Unlock() },
	})

	a.dispatchRevoke(fpsdk.RevokeEvent{AppID: "a2", Tokens: []string{"t1"}})

	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(got, []string{"a2"}) {
		t.Fatalf("带 app 的撤销扇出到 %v，期望只有 a2", got)
	}
}

// TestEmptyRevokeIsIgnored：没有 token 的事件不该惊动任何人。
func TestEmptyRevokeIsIgnored(t *testing.T) {
	var calls int
	a := newTestAuthenticator(t, Config{
		FPAddr:   "127.0.0.1:1",
		Secret:   "im-secret",
		AllApps:  func() []string { return []string{"a1"} },
		OnRevoke: func(string, []string) { calls++ },
	})
	a.dispatchRevoke(fpsdk.RevokeEvent{AppID: "", Tokens: nil})
	if calls != 0 {
		t.Fatalf("空 token 列表触发了 %d 次回调", calls)
	}
}

// TestNewRequiresSecret：IM secret 为空必须在装配阶段就失败，与既有的
// FPAddr 检查同理——client 是启动时建的，配错了不该等到第一个用户握手。
func TestNewRequiresSecret(t *testing.T) {
	if _, err := New(Config{FPAddr: "127.0.0.1:1"}); err == nil {
		t.Fatal("Secret 为空必须报错")
	}
}
```

`dispatchRevoke` 是从 `OnRevoke` 回调体里提出来的包内方法，让扇出逻辑可以脱离
真实 gRPC 流被测试。`newTestAuthenticator` 构造一个不真正拨号的 `Authenticator`
（`New` 本身不拨号——`grpc.NewClient` 是惰性的）。

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/im/fpauth
```

预期：`unknown field Secret` / `undefined: a.dispatchRevoke`。

- [ ] **Step 3: 实现**

`internal/im/fpauth/fpauth.go` 大改：

```go
// Package fpauth 用 fpsdk 实现 auth.Authenticator，并把 fp 的 IM 网关接口
// 包给 fp-im 的其余部分用。
//
// **全进程只有一个 fpsdk.Client。** 早期是每个 app 一个——因为那时凭据是
// 各 app 自己的 appSecret。现在凭据是 IM secret，连接级不带 app_id，app
// 作用域由每次调用附上（fpsdk.WithAppID），所以一条连接服务所有 app。
//
// 这是 internal/im 里唯一允许 import github.com/basicfu/fp/sdk 的包
// （arch_test 断言）。
package fpauth

type Config struct {
	FPAddr   string
	Secret   string // IM secret，来自 config-im.yaml 的 fpsdk.secret
	Insecure bool
	// OnRevoke 在 token 被撤销时调用，fp-im 用它关 ws。
	OnRevoke func(app string, tokens []string)
	// AllApps 返回本节点当前持有连接的全部 app。
	//
	// 只为一件事存在：RevokeEvent.AppID 为空串表示跨全部应用的撤销
	// （改密、冻结），而 hub 的连接表按 app 分桶，必须逐个投递。
	// 见 dispatchRevoke。
	AllApps func() []string
	Logger  *slog.Logger
}

type Authenticator struct {
	cfg    Config
	client *fpsdk.Client
}

func New(cfg Config) (*Authenticator, error) {
	if cfg.FPAddr == "" {
		return nil, errors.New("fpauth: FPAddr 必填")
	}
	if cfg.Secret == "" {
		return nil, errors.New("fpauth: Secret（IM 凭据）必填")
	}
	a := &Authenticator{cfg: cfg}
	c, err := fpsdk.New(fpsdk.Options{
		Addr:       cfg.FPAddr,
		AppSecret:  cfg.Secret,
		CallerType: fpsdk.CallerTypeIM,
		Insecure:   cfg.Insecure,
		Logger:     cfg.Logger,
		OnRevoke:   a.dispatchRevoke,
	})
	if err != nil {
		return nil, fmt.Errorf("fpauth: 连接 fp: %w", err)
	}
	a.client = c
	return a, nil
}

// dispatchRevoke 把一条撤销事件投给 hub。
//
// **AppID 为空串表示跨全部应用的撤销**（改密、冻结）——proto 的 RevokeEvent
// 注释里写着这个语义。以前 fp-im 每个 app 一个 client，这条事件被 N 个客户端
// 各收一份，扇出靠连接数量天然做到；收敛成一条流之后只收到一份，必须在这里
// 显式扇开。
//
// 漏了这一步的后果是**静默的**：拿空串去查 hub.byToken（键是
// app + "\x00" + token）一条也查不到，改密码/冻结用户之后 ws 全都不会被关，
// 没有任何报错或日志。TestCrossAppRevokeFansOutToAllApps 钉住它。
func (a *Authenticator) dispatchRevoke(ev fpsdk.RevokeEvent) {
	if a.cfg.OnRevoke == nil || len(ev.Tokens) == 0 {
		return
	}
	if ev.AppID != "" {
		a.cfg.OnRevoke(ev.AppID, ev.Tokens)
		return
	}
	if a.cfg.AllApps == nil {
		return
	}
	for _, app := range a.cfg.AllApps() {
		a.cfg.OnRevoke(app, ev.Tokens)
	}
}

func (a *Authenticator) Verify(ctx context.Context, req auth.VerifyRequest) (model.Subject, error) {
	// app 作用域逐调用附上：连接级凭据里没有 app_id。
	id, err := a.client.Auth().Validate(fpsdk.WithAppID(ctx, req.App), req.Token)
	if err != nil {
		return model.Subject{}, translate(err)
	}
	return model.User(id.UserID), nil
}

// Fetch 满足 fpappcfg.Fetcher：拉一个 app 的 IM 配置并转成 fp-im 的模型。
func (a *Authenticator) Fetch(ctx context.Context, app string) (model.AppConfig, error) {
	c, err := a.client.IMGateway().GetAppIMConfig(ctx, app)
	if err != nil {
		return model.AppConfig{}, translate(err)
	}
	cfg := model.AppConfig{
		AppID:       app,
		ConnPolicy:  model.Policy(c.ConnPolicy),
		ConnLimit:   int(c.ConnLimit),
		AllowGuest:  c.AllowGuest,
		GuestIPRate: int(c.GuestIPRate),
	}
	if b := c.BizAuth; b != nil {
		cfg.BizAuth = &model.BizAuth{
			VerifyURL: b.VerifyURL,
			Timeout:   model.Duration(time.Duration(b.TimeoutMs) * time.Millisecond),
			CacheSize: int(b.CacheSize),
		}
	}
	// fp 侧保存时已经校验过一遍，这里再校验一次不是不信任它，而是让
	// "线上传下来一份 fp-im 认为非法的配置"在加载时就暴露，而不是等到
	// 某条握手走到那个字段才诡异地失败。
	if err := cfg.Validate(); err != nil {
		return model.AppConfig{}, err
	}
	return cfg, nil
}

// VerifyAppCredential 核实业务 server 连入 fp-im 时递上来的凭据。
func (a *Authenticator) VerifyAppCredential(ctx context.Context, app, secret string) error {
	return translate(a.client.IMGateway().VerifyAppCredential(ctx, app, secret))
}

func (a *Authenticator) Close() error { return a.client.Close() }
```

`clients map`、`mu`、`closed`、`client(app)` 全部删除——`closed` 那套是为了防止
"已关闭的 Authenticator 又悄悄建一条新连接"，而现在根本没有懒建这回事。

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/im/fpauth -v
```

预期：全 PASS。

- [ ] **Step 5: 变异验证**

把 `dispatchRevoke` 的空串分支改成直接 `a.cfg.OnRevoke(ev.AppID, ev.Tokens)`
（即照抄旧写法），确认 `TestCrossAppRevokeFansOutToAllApps` 变红。改回来。

- [ ] **Step 6: 提交**

```bash
git add internal/im/fpauth/
git commit -m "feat(im/fpauth): 收敛成一个 client，跨应用撤销显式扇出"
```

---

## Task 4: imgrpc 的凭据校验转给 fp

**Files:**
- Modify: `internal/im/imgrpc/appauth.go`, `internal/im/imgrpc/server.go`
- Test: `internal/im/imgrpc/appauth_test.go`

**Interfaces:**
- Consumes: Task 3 的 `(*Authenticator).VerifyAppCredential`
- Produces: `imgrpc.CredentialVerifier` 接口 `Verify(ctx, app, secret) error`；`imgrpc.Deps.Creds`

- [ ] **Step 1: 写失败的测试**

```go
// TestStreamAuthCachesSuccessOnly：业务 server 每条流校验一次，凭据要转给
// fp，所以 fp 不可达时业务 server 就接不进来——这是本改动新增的依赖。
//
// 缓存成功结果把 fp 的短暂抖动挡在外面。**只缓存成功**：缓存键有一半来自
// 调用方，缓存失败等于把 map 的大小交给攻击者，与 fp 的 appVerifier 同一
// 条推理。
func TestStreamAuthCachesSuccessOnly(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{"a1": "s1"}}
	auth := newCredCache(v, time.Minute)

	for i := 0; i < 3; i++ {
		if err := auth.verify(context.Background(), "a1", "s1"); err != nil {
			t.Fatal(err)
		}
	}
	if v.calls != 1 {
		t.Fatalf("成功结果回源了 %d 次，应当只有 1 次", v.calls)
	}

	v.calls = 0
	for i := 0; i < 3; i++ {
		if err := auth.verify(context.Background(), "a1", "wrong"); err == nil {
			t.Fatal("错误凭据必须被拒")
		}
	}
	if v.calls != 3 {
		t.Fatalf("失败结果回源了 %d 次，失败不缓存意味着每次都要回源", v.calls)
	}
}

// TestStreamAuthRejectsUnknownApp：没有本地文件之后，未知 app 由 fp 判定。
func TestStreamAuthRejectsUnknownApp(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{"a1": "s1"}}
	auth := newCredCache(v, time.Minute)
	if err := auth.verify(context.Background(), "ghost", "s"); err == nil {
		t.Fatal("未知 app 必须被拒")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/im/imgrpc
```

- [ ] **Step 3: 实现**

`streamAuth` 的签名从收 `auth.AppConfigSource` 改成收一个带缓存的校验器：

```go
// CredentialVerifier 是 imgrpc 对"核实业务 server 凭据"的全部依赖。
// *fpauth.Authenticator 满足它。
type CredentialVerifier interface {
	Verify(ctx context.Context, app, secret string) error
}

// credCache 给凭据校验加一层只缓存成功的缓存。
//
// 校验从"比对本地明文"变成了"转给 fp"，于是 fp 不可达时业务 server 接不
// 进来——这是本改动新增的依赖。缓存把 fp 的短暂抖动挡在外面：业务 server
// 重连时命中缓存即可，不必等 fp 恢复。
//
// **只缓存成功。** 键的一半（secret）来自调用方，缓存失败结果等于把这个
// map 的大小交给攻击者：一百万个不同的错误 secret 就能把进程 OOM 掉。
// 只收成功结果的话，键的数量被真实应用数钉死。与 fp 的 appVerifier 是同
// 一段推理，那边的注释写得更全。
type credCache struct {
	v   CredentialVerifier
	ttl time.Duration

	mu    sync.RWMutex
	cache map[string]int64 // app + ":" + sha256(secret) → 过期时刻（UnixMilli）
}
```

`verify` 的形状照抄 `internal/grpcapi/auth_interceptor.go` 的 `appVerifier.verify`
（含 `maxCacheEntries` 那段清理），**键同样用 secret 的 SHA-256 而不是明文**——
明文常驻堆内存的话，崩溃 dump / 调试器 / 内存分析都能读到。

`streamAuth` 里：

```go
		id, secret := first(md, MDAppID), first(md, MDAppSecret)
		if err := c.verify(ss.Context(), id, secret); err != nil {
			return status.Error(codes.Unauthenticated, "应用凭据无效")
		}
```

`imgrpc.Deps` 的 `Apps auth.AppConfigSource` 换成 `Creds CredentialVerifier`
（若 `Apps` 在本包还有别的用途则两个都留）。

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/im/imgrpc -v
```

- [ ] **Step 5: 变异验证**

把 `verify` 改成失败也入缓存，确认 `TestStreamAuthCachesSuccessOnly` 的后半段变红。
改回来。

- [ ] **Step 6: 提交**

```bash
git add internal/im/imgrpc/
git commit -m "feat(im/imgrpc): 业务 server 凭据转给 fp 核实，只缓存成功"
```

---

## Task 5: 装配、删包、配置

**Files:**
- Modify: `cmd/fp-im/main.go`, `cmd/fp-im/main_test.go`
- Modify: `internal/im/config/config.go`, `internal/im/config/config_test.go`
- Modify: `internal/im/model/appconfig.go`, `internal/im/model/appconfig_test.go`
- Modify: `config-im.example.yaml`
- Delete: `internal/im/appcfg/`

- [ ] **Step 1: 删 `model.AppConfig.AppSecret`**

删字段与 `Validate` 里那句 `c.AppSecret == ""` 的检查。`AppID` 的检查保留。
`internal/im/model/appconfig_test.go` 里对应的用例删掉。

顺带更新 `AppConfig` 的文档注释——它现在说"AppID/AppSecret 同时用于 fp-im 调 fp
验 token，和校验业务 server 连 fp-im 的凭据"，两条都不成立了：

```go
// AppConfig 是一个接入应用在 fp-im 里的配置，全部来自 fp。
//
// **没有 AppSecret**：fp 只存 bcrypt 哈希，明文只在创建应用时返回一次，
// fp-im 拿不到也不需要——验 token 用的是 IM 凭据（fpauth），核实业务
// server 的凭据是转给 fp 做的（imgrpc）。
```

- [ ] **Step 2: 改 config**

`internal/im/config/config.go`：`FPSDK` 加 `Secret string \`yaml:"secret"\``，
删掉 `AppsFile` 字段与它的必填校验。`Secret` **不加**必填校验——沿用上一份 spec
定下的"谁用谁校验"：由 `fpauth.New` 在装配阶段挡（Task 3 已有测试钉住）。

`config_test.go` 里 `minimal` 去掉 `apps_file`，加一条断言 `fpsdk.secret` 被读到。

`config-im.example.yaml`：

```yaml
# fp SDK 的连接参数。addr 是 fp 的 gRPC 地址，不是 HTTP。
#
# secret 是 IM 凭据，在 fp 控制台生成（应用管理页的 IM 凭据卡片），
# 明文只显示一次。轮换后旧凭据最多再活 10 秒。
#
# 没有 insecure 开关：传输安全由上面的 env 推导——dev 明文，prod 走 TLS。
fpsdk:
  addr: localhost:9090
  secret: ""

# apps_file 已删除：接入应用的准入与策略全部来自 fp，在控制台的应用管理页
# 配置（先打开 im_enabled）。fp-im 不再持有任何应用的 app_secret。
```

- [ ] **Step 3: 改装配**

`cmd/fp-im/main.go` 的 `serve`：

```go
	// 先建 Authenticator：fpappcfg 要用它当 Fetcher，而它自己不依赖 app 配置。
	authr, err := fpauth.New(fpauth.Config{
		FPAddr:   cfg.FPSDK.Addr,
		Secret:   cfg.FPSDK.Secret,
		Insecure: cfg.Insecure(),
		Logger:   log,
		// AllApps 与 OnRevoke 在 hub 建好之后回填，见下面。
	})
	if err != nil {
		return err
	}
	defer authr.Close()

	// 新 app 首次加载时，必须立刻加进 srv 表的追踪集合并**同步**刷新一次。
	//
	// 早期这件事在装配时对本地文件里的每个 app 做一遍（「甲一」）。现在没有
	// 本地文件，fp-im 启动时不知道任何 app，只能在按需加载的那一刻补。
	//
	// 同步 Refresh 不能省：TrackApp 只是把 app 加进待刷新集合，真正的内容要
	// 等下一次周期刷新（默认 3 秒）。握手完成后 client 可能立刻发消息，那条
	// 消息在需要跨节点转发时会因为候选列表为空而被**静默丢弃、零日志**。
	apps := fpappcfg.New(authr, func(app string) {
		live.TrackApp(app)
		if err := live.Refresh(ctx); err != nil {
			log.Error("刷新 srv 表失败，该 app 的首条消息可能被丢弃", "app", app, "err", err)
		}
	})
```

`OnRevoke` / `AllApps` 的回填：`fpauth.Config` 里这两个是函数字段，`hub` 建好之后
用 setter 补上（`authr.SetHooks(onRevoke, allApps)`），或者把 `hub` 的构造提到
`fpauth.New` 之前。**选后者**——setter 会让 `Authenticator` 多出一个"半构造好"的
中间状态，而 `Watch` 流在 `New` 返回时就已经在跑了，那个窗口里来的撤销会丢。

所以顺序是：`hub` → `fpauth.New`（带上 `OnRevoke`/`AllApps`）→ `fpappcfg.New`。
`AllApps` 传 `apps.Apps`——但 `apps` 还没建。用一个闭包捕获后面赋值的变量：

```go
	var apps *fpappcfg.Source
	authr, err := fpauth.New(fpauth.Config{
		// …
		OnRevoke: h.OnRevoked,
		AllApps:  func() []string { return apps.Apps() },
	})
	// …
	apps = fpappcfg.New(authr, func(app string) { /* … */ })
```

闭包在第一条撤销事件到达之前一定已经赋值——`apps` 的赋值就在 `New` 的下一条语句，
中间没有任何 I/O 或调度点。

握手路径上，`wsapi` 在读取 app 配置之前先调 `apps.Load(ctx, app)`；失败按
"不可用"处理（关闭码 4002 / 4004，按错误类型分）。

`AppIMConfigChanged` 推送的处理：`fpsdk` 的 `OnConfigChanged`（或等价钩子）里调
`apps.Invalidate(app)`。

- [ ] **Step 4: 删包**

```bash
git rm -r internal/im/appcfg
rm -f tmp/im-apps.json
```

- [ ] **Step 5: 编译、格式、全量测试**

```bash
go build ./... && go vet ./... && gofmt -l cmd internal sdk
./scripts/test.sh
```

预期：全绿。`internal/im/arch_test.go` 会验证 `fpappcfg` 没有直接 import `fpsdk`。

- [ ] **Step 6: 提交**

```bash
git add cmd/fp-im/ internal/im/config/ internal/im/model/ config-im.example.yaml
git rm -r --cached internal/im/appcfg
git commit -m "feat(im): app 配置改从 fp 拉取，删掉本地 apps 文件"
```

---

## Task 6: 集成测试与文档

**Files:**
- Create: `internal/integration/im_provisioning_test.go`
- Modify: `docs/im.md`

- [ ] **Step 1: 写集成测试**

三条，都要起真实的 fp + fp-im：

```go
// TestCrossAppRevokeClosesConnectionsInAllApps 是第八节那个洞的端到端护栏。
//
// 起两个 app 的 client 连接，触发一条跨应用撤销（AppID 为空），断言两边的
// ws 都被关掉。fpauth 那条单测钉的是扇出逻辑本身，这条钉的是整条链路——
// fp 的通配扇出 + SDK 的事件透传 + fp-im 的分派，任何一环断了都在这里红。
func TestCrossAppRevokeClosesConnectionsInAllApps(t *testing.T) { /* … */ }

// TestIMDisabledAppRejectsHandshake：控制台关掉 im_enabled 之后，
// 在线连接保持、新握手被拒（4002）。
func TestIMDisabledAppRejectsHandshake(t *testing.T) { /* … */ }

// TestAppIMConfigChangePropagates：控制台改 conn_policy，fp-im 在下一次
// 握手时用上新值，不需要重启。
func TestAppIMConfigChangePropagates(t *testing.T) { /* … */ }
```

- [ ] **Step 2: 跑全量**

```bash
gofmt -l cmd internal sdk && go vet ./... && ./scripts/test.sh
```

- [ ] **Step 3: 文档**

`docs/im.md` 改：

- **启动**一节：删掉 `apps_file` 与那段 JSON 示例，改成"在 fp 控制台打开应用的
  `im_enabled` 并配好参数"，`config-im.yaml` 只剩 `fpsdk.addr` + `fpsdk.secret`
- 新增一段**运维性质**：
  - fp 不可达时，业务 server 接不进来（新增的依赖），但已缓存凭据的重连不受影响
  - IM 凭据轮换后旧凭据最多再活 10 秒
  - 关掉 `im_enabled` 不断开在线连接，只拒新握手
- **已知不足**里补一条：不存在的 app 每次握手都会回源一次（失败不缓存），
  依赖 fp 侧限流兜底

- [ ] **Step 4: 提交**

```bash
git add internal/integration/im_provisioning_test.go docs/im.md
git commit -m "test(integration): app 配置下发端到端；docs: im 运维说明重写"
```

---

## 第二阶段完成标准

- [ ] `./scripts/test.sh` 全绿
- [ ] `internal/im/appcfg` 已删除，仓库里搜不到 `apps_file`、`AppSecret`（fp-im 侧）
- [ ] `config-im.yaml` 只需 `fpsdk.addr` + `fpsdk.secret`
- [ ] 跨应用撤销有单测 + 集成测试双重护栏（这是最容易静默失效的一处）
