# fpsdk —— fp 接入 SDK（Go）

fp（Foundation Platform）是账号与会话中心；fp-im 是配套的 WebSocket 网关。
本 SDK 是接入两者的 Go 客户端，开源在 `github.com/basicfu/fp`，直接 import
仓库路径即可使用，不需要单独发布的包。

| 子包 | import 路径 | 用途 |
|---|---|---|
| 根包 | `github.com/basicfu/fp/sdk` | 认证、鉴权、配置中心。业务方必接 |
| `fpim` | `github.com/basicfu/fp/sdk/im` | 接入 fp-im WebSocket 网关。只有做长连接推送才需要 |
| `fpchi` | `github.com/basicfu/fp/sdk/fpchi` | chi 路由框架的鉴权适配器。用别的框架或裸 `net/http` 不需要 |

SDK 跑在业务方进程里，**不使用 panic**，任何错误都经返回值传递；也**不
import `internal/` 下的任何包**，公开面完全独立于 fp 服务端实现。

## 安装

```bash
go get github.com/basicfu/fp@latest
```

```go
import fpsdk "github.com/basicfu/fp/sdk"
```

## 快速开始

```go
client, err := fpsdk.New(fpsdk.Options{
    Addr:      "fp.internal:9090",
    AppID:     os.Getenv("FP_APP_ID"),
    AppSecret: os.Getenv("FP_APP_SECRET"),
})
if err != nil {
    log.Fatal(err)
}
defer client.Close()

auth := client.Auth()

mux := http.NewServeMux()
mux.Handle("GET /api/me", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    id, _ := fpsdk.IdentityFrom(r.Context())
    fmt.Fprintf(w, "userId=%s", id.UserID)
})))
log.Fatal(http.ListenAndServe(":8090", mux))
```

一个可以直接跑起来的完整版本（含登录、发验证码、健康检查）见
[`examples/demo`](../examples/demo)。

## 目录

1. [连接（Client）](#1-连接client)
2. [认证（Auth）](#2-认证auth)
3. [鉴权（Authz / RBAC）](#3-鉴权authz--rbac)
4. [配置中心](#4-配置中心)
5. [WebSocket 网关（fp-im）](#5-websocket-网关fp-im)
6. [生产环境清单](#6-生产环境清单)
7. [常见问题](#7-常见问题)

## 1. 连接（Client）

```go
client, err := fpsdk.New(fpsdk.Options{ /* ... */ })
```

`New` 建立 gRPC 长连接并立即启动一条撤销推送流，进程内建一个 `*Client`
即可，并发安全。用完调 `client.Close()`——它会停掉后台 goroutine 并关闭
连接。

### Options

| 字段 | 说明 | 默认值 |
|---|---|---|
| `Addr` | fp 的 gRPC 地址，如 `"fp.internal:9090"` | 必填 |
| `AppID` / `AppSecret` | 应用凭据，来自 fp 控制台创建应用 | 必填 |
| `CallerType` | **业务方不要用**，见下方说明 | `""` |
| `Insecure` | 允许明文连接 | `false` |
| `TLSConfig` | 自定义 TLS 配置 | `nil`（用系统根证书） |
| `ValidateTimeout` | 单次校验回源的超时 | 2 秒 |
| `CacheSize` | 本地校验结果缓存的容量（条） | 10000 |
| `DegradedCacheTTL` | 推送流断开时缓存的有效期上限 | 5 秒 |
| `AllowStaleOnOutage` | fp 不可达时能否使用已过期的缓存条目 | `false` |
| `MaxStaleness` | `AllowStaleOnOutage` 开启时，过期条目最多能延用多久 | 5 分钟 |
| `Logger` | SDK 内部日志 | `slog.Default()` |
| `OnRevoke` | 收到撤销事件时的回调 | `nil` |

**`Insecure` 生产环境绝不能开**——`AppSecret` 随每个 RPC 的 metadata 发送，
明文连接等于把它印在网线上。只用于本地开发。

> **`CallerType` 是给 fp-im 网关自己用的，业务方不要设。** 设成
> `fpsdk.CallerTypeIM` 之后 fp 开放的是一组**收窄过**的接口：`Login` /
> `Logout` / `SendLoginCode` / `GetPolicy` / `ReportPermissions` /
> `GetConfig` 一律 `PermissionDenied`，只剩下 `Auth().Validate` 和网关专用
> 的 `IMGateway()`；作用域也不再钉在连接上，`AppID` 允许留空，改由
> `fpsdk.WithAppID(ctx, app)` 逐调用附上。业务方设了它只会得到一堆
> `PermissionDenied`。

### 连接健康与降级

`client.StreamHealthy()` 报告撤销推送流是否可用。fp 短暂不可达时，SDK 靠
本地缓存 + 推送式撤销继续提供服务，不会让所有已登录用户瞬间掉线：

- 推送流健康：缓存条目按 fp 下发的完整 TTL 生效，撤销靠推送**主动**通知，
  无需等 TTL 到期。
- 推送流断开：新查询与**已经缓存的旧条目**的有效期一起被收紧到
  `DegradedCacheTTL`（默认 5 秒）——因为撤销事件送不到本地了，只能靠更短
  的 TTL 兜底。
- fp 彻底不可达且缓存已过期：默认直接拒绝（`ErrUnavailable`）。只有显式
  开了 `AllowStaleOnOutage`，才会继续沿用"刚验证过的身份"，且最多延用
  `MaxStaleness`（默认 5 分钟）。完全没有缓存条目时，无论这个开关如何都
  必须拒绝——这不是常见意义上的 fail-open，放行却拿不出一个可信身份是
  讲不通的。

## 2. 认证（Auth）

`client.Auth()` 返回认证入口，多次调用是同一个实例。

### 中间件接入（推荐）

```go
mux.Handle("GET /api/orders", auth.Middleware(handler))
```

默认行为：从 `Authorization: Bearer <token>` 或名为 `fp_token`
（`fpsdk.DefaultCookieName`）的 cookie 里取 token；校验失败写 401/503/…
（见下方错误处理）；token 需要轮换时自动回写新 cookie 并带上
`X-Fp-New-Token` 响应头。

需要定制时用 `MiddlewareWith`：

```go
mw := auth.MiddlewareWith(fpsdk.MiddlewareOptions{
    CookieSecure: true, // 生产环境必须显式开
    AllowGuest:   true, // 允许未登录访客，见下文
})
mux.Handle("GET /api/orders", mw(handler))
```

| 字段 | 说明 |
|---|---|
| `TokenFrom` | 自定义 token 提取，默认 Bearer 头 + cookie |
| `CookieName` | 会话 cookie 名，默认 `fp_token` |
| `CookieSecure` | 回写 cookie 是否带 `Secure`。**生产必须设 `true`**，默认 `false` 只为不破坏本地 HTTP 开发 |
| `CookiePath` / `CookieDomain` / `CookieSameSite` | 回写 cookie 的其余属性 |
| `OnRotate` | 覆盖 token 轮换时的交付方式 |
| `OnError` | 覆盖失败响应，默认写状态码、不回显错误内容 |
| `AllowGuest` | 见下文"访客模式" |

请求处理函数里取身份：

```go
id, ok := fpsdk.IdentityFrom(r.Context())
```

`Identity` 字段：

| 字段 | 说明 |
|---|---|
| `UserID` / `SessionID` | 用户与会话标识 |
| `Roles` | 该用户在本应用的有效角色，鉴权判定用它 |
| `RotatedTo` | 非空表示 token 已轮换，中间件已自动处理；手动调 `Validate` 时需要自己交付 |
| `Stale` | `true` 表示这是 fp 不可达期间返回的陈旧结果，高危操作应拒绝 |
| `GuestID` | 非空表示这是访客身份（`IsGuest()` 判断），与 `UserID` 互斥 |

### 手动校验

不经过中间件时直接调用：

```go
id, err := auth.Validate(ctx, token)
```

命中本地缓存时同步返回、不产生网络调用；未命中时回源，同一 token 的并发
请求会被合并成一次 RPC。

### 登录 / 登出

```go
if err := auth.SendLoginCode(ctx, phone); err != nil { ... }

res, err := auth.Login(ctx, fpsdk.LoginInput{
    ConnectorType: "sms_code",
    Credentials:   map[string]string{"phone": phone, "code": code},
    IP:            r.RemoteAddr,
    UserAgent:     r.UserAgent(),
})
// res.Token / res.SessionID / res.User

err = auth.Logout(ctx, token)
```

`ConnectorType` 与 `Credentials` 的取值由控制台里给该应用启用的登录方式
决定（如 `sms_code` 需要 `phone` + `code`）。

### 访客模式

`MiddlewareOptions.AllowGuest = true` 时，请求没带 token 但带了合法的
`X-Guest-Id`（`fpsdk.GuestIDHeader`，前端生成并持久化的 uuid v4）会被当
访客放行，`Identity.GuestID` 非空、`UserID` 为空。访客标识不经过 fp
签发也不做签名验证，只校验格式；真正需要区分权限时按 `IsGuest()` 自行
处理。

### 错误处理

```go
var fe *fpsdk.Error
if errors.As(err, &fe) {
    // fe.Code   机器可读，稳定契约，用它分支
    // fe.Msg    可直接展示给终端用户
    // fe.Detail JSON 字符串，排查用
}
if errors.Is(err, fpsdk.ErrUnauthorized) { ... } // 哨兵判断依然有效
```

自己写 HTTP handler（不经过 `Middleware`）时，用 `fpsdk.WriteError(w, err)`
把 SDK 的哨兵错误映射成正确的状态码——**不要自己再写一遍分类逻辑**，写反
方向的代价很不对称：

| 哨兵错误 | HTTP 状态码 | 说明 |
|---|---|---|
| `ErrNoToken` / `ErrUnauthorized` | 401 | 未登录 / token 无效过期 |
| `ErrUnavailable` | 503 | **fp 不可达，不是鉴权失败**。回 401 会让客户端清掉一个其实有效的 token，把一次 fp 抖动放大成全体用户被迫重新登录 |
| `ErrInvalidArgument` | 400 | 参数不合法（手机号格式等），不是凭据问题 |
| `ErrRateLimited` | 429 | 被限流（验证码发送过频），凭据本身没问题 |

## 3. 鉴权（Authz / RBAC）

`client.Authz()` 返回鉴权入口。**判定完全在本地完成，不走网络**——策略
由 fp 推送并缓存在进程内，用户角色随身份校验一起回来。

### 判定

```go
allowed, err := client.Authz().Allow(ctx, r.Method, "/orders/{id}")
```

- `pattern` 必须是**匹配到的路由模式**（`/orders/{id}`），不是原始 URL
  （`/orders/123`），否则每个 id 会变成一个独立的权限点。
- 返回值语义：`(false, nil)` 是明确拒绝，业务方应回 403；`(_, err)` 是
  "没能判定"（本地策略还没就绪等），按业务方自己的降级策略处理，不要
  当成拒绝。
- 不使用 fpsdk 认证中间件（角色从别处取）时用 `AllowRoles(roles, method,
  pattern)`。

### 权限点上报

```go
_ = client.ReportPermissions(ctx, []fpsdk.PermissionPoint{
    {Key: fpsdk.PermissionKey("GET", "/orders/{id}")},
})
```

启动时上报一次即可；上报快照里没有的权限点不会被自动删除，只会在控制台
标记"过渡中"，由人决定是否清理——滚动发布时新旧版本同时上报是安全的。
**上报与判定必须用同一个 `PermissionKey` 函数拼 key**，格式一旦漂移，
鉴权会静默全部拒绝（本地查不到条目就是默认拒绝），没有任何报错指向真实
原因。

### chi 框架适配器

用 chi 时不需要手写路由枚举，`sdk/fpchi` 从路由树里自动收集：

```go
import fpchi "github.com/basicfu/fp/sdk/fpchi"

a := fpchi.New(client.Authz(), fpchi.StripPrefix("/api/v1"))
r.Use(authMiddleware) // 必须先认证，鉴权靠 context 里的身份
r.Use(a.Middleware())
_ = client.ReportPermissions(context.Background(), a.Collect(r)) // 传顶层路由器
```

`StripPrefix` 影响上报与判定两侧的 key（同一个 `Adapter` 实例保证一致）。
没挂 `a.Middleware()` 的路由 fp 一概不管；挂了默认拒绝。

其他框架（gin / echo / 裸 `net/http`）没有现成适配器，按同样的模式自己
接：认证中间件之后，从 context 取路由模式，调 `Authz().Allow`。

## 4. 配置中心

把 fp 控制台里维护的配置拉进本地 struct，支持推送热更新。

```go
type UpstreamConfig struct {
    Timeout    time.Duration // fp 上按毫秒配置的整数，见下方特殊类型说明
    RetryCount int
    APIKey     string
}

binding, err := fpsdk.Bind[UpstreamConfig](client)
var missing *fpsdk.MissingConfigError
if errors.As(err, &missing) {
    // 有配置项在控制台还没建值：missing.Keys / missing.Types
    // binding 依然可用，缺失字段是 Go 零值，SDK 不替业务方决定能不能带伤启动
} else if err != nil {
    log.Fatal(err)
}

cfg := binding.Load() // 当前快照，*T，不得修改
binding.OnChange(func(old, new *UpstreamConfig) { /* 热更新回调 */ })
binding.OnError(func(err error) { /* key 被删 / 解析失败 */ })
```

### 字段到 key 的映射规则

- 字段名转 `snake_case`：`APIKey` → `api_key`，`UserID` → `user_id`。
- 未标 tag 的嵌套 struct 是**分组**，递归展开成 `父.子` 的 key。
- 标了 `` fp:"json" `` 的字段整体当一个 JSON 对象存，不展开（拆不动的结构，
  如供应商列表）。
- `time.Duration` 字段：fp 上按**毫秒**的整数配置，SDK 自动换算，不需要
  也不支持在控制台标记"这是 duration"。
- 支持的标量类型：`bool`、各种 `int`/`uint`/`float`、`string`；此外是
  slice/array（对应 fp 的 array）与 map（对应 object）。指针、interface、
  chan、func 一律报错——避免一个本该被配置的字段永远停在零值却没有任何
  提示。

### `Bind[T]` vs `BindType`

| | `Bind[T]` | `BindType` |
|---|---|---|
| 用途 | 绑定到 Go struct，类型安全 | 拉整个分区，原样转发给前端 |
| 分区 | 固定 `ConfigTypeDefault` | 调用方指定，如 `ConfigTypeWeb` |
| 缺值语义 | 有——`MissingConfigError` 提示该去控制台建哪些 key | 无——没配就是 map 里没有这个 key |
| 返回 | `*Binding[T]`，`Load() *T` | `*TypeBinding`，`Load() map[string]any` |

`ConfigTypeDefault`（后端读的分区，密钥类配置建这里）与
`ConfigTypeWeb`（业务方转发给浏览器的分区）是 SDK 导出的两个内置分区名，
也可以在控制台建自定义分区名传给 `BindType`。

`OnChange`/`OnError` 回调在 Client 唯一的重载 goroutine 里同步执行，
**回调必须快**，慢操作自己另起 goroutine。

## 5. WebSocket 网关（fp-im）

只有需要长连接推送（IM、通知、实时状态）时才需要这个子包，与根包 `Auth`
用的是同一对 `AppID`/`AppSecret`。

```go
import fpim "github.com/basicfu/fp/sdk/im"
```

两个角色：

- **`fpim.Server`**——业务后端连 fp-im 的 gRPC，负责推送消息、踢人、查
  在线会话，并接收 client 发上来的消息与连接事件。
- **`fpim.Client`**——Go 编写的设备端/程序化客户端连 fp-im 的 WebSocket。
  浏览器前端走 JS SDK，不使用这个类型。

### 前置：在控制台把这个应用的 IM 接入打开

fp-im 自己不存任何应用配置，连接策略、访客开关、是否允许接入全部来自
fp。所以接入前必须先在 fp 控制台的「应用 → IM 设置」里打开 IM 接入，
否则业务后端与设备端**都连不上**，凭据再正确也一样。

这带来两条运行期后果，接入前值得先知道：

- **业务后端连 fp-im 时，fp 必须可达。** fp-im 拿不到任何应用的
  `AppSecret`（fp 只存 bcrypt 哈希），只能把凭据转给 fp 核实。校验成功的
  结果在 fp-im 侧缓存 5 分钟，fp 的短暂抖动因此不会打断已经接入过的业务
  后端重连；但**冷启动**时 fp 不可达就是接不进来。
- **控制台把 IM 接入关掉时，已经在线的连接不会被踢**，但新的握手一律被
  拒——与应用被停用（`status=disabled`）的语义一致。重新打开即恢复。

### Server（业务后端）

```go
srv, err := fpim.NewServer(fpim.ServerConfig{
    Addr: "fp-im.internal:9090", AppID: appID, AppSecret: appSecret,
})
defer srv.Close()

srv.OnMessage(func(ctx context.Context, in fpim.Inbound) error {
    // 可以在回调里直接同步调用 Push 等方法等待应答，不会死锁
    _, err := srv.Push(ctx, in.Subject, in.Payload)
    return err
})
srv.OnEvent(func(ctx context.Context, ev fpim.Event) {
    // ev.Kind: EventConnected / EventDisconnected
})

result, err := srv.Push(ctx, fpim.User(userID), []byte(`{"type":"notify"}`))
results, err := srv.PushMany(ctx, []fpim.Subject{fpim.User(id1), fpim.Guest(id2)}, payload)
sessions, err := srv.Sessions(ctx, fpim.User(userID))
err = srv.Kick(ctx, fpim.User(userID)) // 不传 connID 表示踢掉该用户全部连接
```

**`NewServer` 不会因为凭据不对而报错。** 它只做参数校验，gRPC 是懒连接，
真正的接入校验发生在后台那条流上。所以凭据错、IM 接入没开、fp 不可达这
三种情况的表现都一样：`NewServer` 正常返回，`srv.StreamHealthy()` 一直是
`false`，`Push`/`Kick`/`Sessions` 全部返回 `fpim.ErrUnavailable`，日志里
反复出现 `fpim: 流断开`。**区别在那条日志带的 gRPC 状态码上**：

| 状态码 | 含义 | 怎么办 |
|---|---|---|
| `FailedPrecondition`「该应用未在 fp 控制台启用 IM 接入」 | 凭据是对的，开关没开 | 去控制台打开 IM 接入，不用查 secret |
| `Unauthenticated`「应用凭据无效」 | `AppID`/`AppSecret` 不对，或 fp 不可达 | 先核对凭据，再看 fp 是否健康 |

这两种刻意分开：能拿到 `FailedPrecondition` 的调用方已经证明自己持有那份
`AppSecret`，告诉它真实原因不泄露任何东西；压成"凭据无效"只会让人去查一
个根本没错的 secret。反过来 `Unauthenticated` 刻意不区分"应用不存在"和
"secret 不对"——那个区别会泄露某个 appId 是否存在。

上线时把 `StreamHealthy()` 接到健康检查里：这是唯一能把上面这些情况和
"一切正常但暂时没消息"区分开的信号。

`Subject` 用 `fpim.User(id)` / `fpim.Guest(id)` / `fpim.Biz(id)` 构造，
分别对应登录用户、访客、业务方自有认证体系的用户，三者在网关侧的存储
天然隔离。

`OnMessage`/`OnEvent` 由 Server 内部单独一个协程按投递顺序串行调用，慢
操作会拖慢排在后面的消息，需要另起 goroutine。

### Client（Go 设备端）

```go
c, err := fpim.Dial(ctx, fpim.ClientConfig{
    URL: "wss://gateway/ws", App: appID, Token: token, // 或 Guest: uuidv4
})
defer c.Close()

c.OnMessage(func(payload []byte) { /* 收到推送 */ })
c.OnClose(func(code int) { /* 连接结束，见下表 */ })
err = c.Send(ctx, []byte(`{"hi":1}`))
```

`Dial` 同步完成握手，失败立即返回错误。握手成功后断线自动按退避重连，
除非收到下列不可重连的关闭码（`c.GaveUp()` 可查询是否已放弃）：

| 关闭码 | 含义 | 是否自动重连 |
|---|---|---|
| `CloseAuthFailed`（4001） | token 无效/过期 | 否，需重新登录换凭据 |
| `ClosePolicyRejected`（4002） | 被应用的连接策略拒绝——最常见的是控制台没打开 IM 接入，其次是超过 `ConnLimit`、访客被禁 | 否 |
| `CloseKicked`（4003） | 被顶替或业务方主动踢下线 | 否 |
| `CloseUnavailable`（4004） | 网关依赖的后端暂时不可用 | 是，退避重连 |
| `CloseIdleTimeout`（4005） | 空闲太久被网关清理，一切正常 | 是，立即重连不退避 |
| `CloseBackpressure`（1013） | 发送队列堆积，网关主动断开 | 是，退避重连；期间消息可能有丢失，需向业务接口拉历史补漏 |

`Send` 只表示写入了本机发送缓冲区，不代表对端已收到——可靠投递（去重、
超时重发）需要业务自己在 payload 里带唯一 id 实现。

## 6. 生产环境清单

- `Insecure` 保持 `false`，配好证书。
- `MiddlewareOptions.CookieSecure` 显式设 `true`。
- `AllowGuest` 只在真的需要匿名访问时开启。
- 传入自己的 `Logger`（默认打到 `slog.Default()`）。
- 启动时调用一次 `ReportPermissions`（或用 `fpchi.Collect`），让控制台能
  看到并管理这个应用的权限点。
- 需要连接 fp-im 时，`ServerConfig`/`ClientConfig` 同样把 `Insecure` 保持
  `false`。
- 用到 fp-im 的应用，先在控制台打开 IM 接入，并确认 fp-im 到 fp 的网络
  可达——fp-im 的每一次凭据校验都要回源 fp（成功结果缓存 5 分钟）。
- 把 `srv.StreamHealthy()` 接进业务后端的健康检查；它是"接入流真的通了"
  的唯一信号，`NewServer` 返回成功并不代表接进去了。
- 需要"fp 挂了也不完全瘫痪"的降级能力时，评估是否开启
  `AllowStaleOnOutage`，并明确 `MaxStaleness` 的业务含义（延用的是登录时
  验证过的旧身份，不是重新鉴权）。

## 7. 常见问题

**为什么受保护接口偶尔返回 503 而不是 401？**
fp 短暂不可达且本地没有可用缓存。这是有意设计——见 [错误处理](#错误处理)。
不要在业务代码里把 503 当成"鉴权失败"处理。

**鉴权判定一直返回拒绝，权限点在控制台却存在？**
八成是上报与判定两侧算出的 `PermissionKey` 不一致（framework 前缀没对齐、
或手写的 pattern 与实际路由模式不同）。本地策略表查不到条目就是默认
拒绝，不会报错指出原因，出现大面积拒绝先检查这个。

**`Load()` 拿到的配置/身份指针能不能改？**
不能。它们被所有 goroutine 共享，下一次热更新会整体替换指针而不是就地
改字段，就地修改会产生竞态。需要独立副本自己深拷贝。

**业务后端连 fp-im 一直不通，`NewServer` 却没报错？**
它是懒连接，不通只体现在 `StreamHealthy()` 为 `false` 和日志里的
`fpim: 流断开`。先看那条日志的状态码：`FailedPrecondition` 是控制台没打开
IM 接入（凭据没问题），`Unauthenticated` 才是凭据不对或 fp 不可达。见
[Server（业务后端）](#server业务后端)。

**一个进程要建几个 `Client`？**
一个。`Client`/`Auth`/`Authz` 都是并发安全的，多个 `Client` 只会浪费连接
和缓存。
