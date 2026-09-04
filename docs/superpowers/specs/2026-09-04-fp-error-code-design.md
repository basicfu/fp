# fp 统一错误码体系设计

**日期：** 2026-09-04
**状态：** 已确认，待编写实施计划

## 一、问题

第三阶段验收时发现：登录失败时接入方只看到一个 401，看不出原因——账号被冻结、验证码错误、应用被停用，在 SDK 那侧长得一模一样。

排查后确认这是**两个独立的问题**，容易被混为一谈：

**问题一：文本已经有了，是 SDK 把它扔掉的。** 服务端每个失败分支都有具体的中文消息（"账号已被冻结或注销"、"验证码不正确或已过期"、"该应用未开放 xx 登录"），`grpcapi.statusFrom` 也把 `err.Error()` 原样透传了。但 `sdk/auth.go` 的 `translate()` 与 `Auth.Validate()` 把 `Unauthenticated` / `PermissionDenied` / `NotFound` **一律压成扁平的 `ErrUnauthorized` 哨兵**，消息在这一步丢失。

**问题二：任何一层都没有机器可读的错误码。** 文本是给人看的、随时可能改词，接入方无法据此分支。HTTP 管理端返回的 `{"error": "..."}` 同样只有文本。

## 二、目标与非目标

**目标**

1. 所有错误同时返回 `code`（机器可读）、`msg`（面向终端用户，可直接展示）、`detail`（面向接入方开发者，排查用）三个字段。
2. 登录失败能区分具体原因：账号冻结、验证码错误、应用停用、登录方式未开放、限流。
3. 三层（HTTP 管理端 / gRPC / Go SDK）表达同一件事时给出同一个 `code`。
4. 现有 SDK 接入方零改动仍可运行——`errors.Is(err, fpsdk.ErrUnauthorized)` 继续成立。

**非目标（本次明确不做）**

- **账号临时封禁机制。** 用户最初提到"被临时封禁 xx 小时"，排查确认这个机制**整个不存在**：仓库里只有短信发送的限流器（`store.RateLimiter`，已能算出 `retryAfter`）和 `notify.MaxVerifyAttempts = 5`（同一个验证码试 5 次作废），没有任何"账号被锁定 N 小时"的存储、判定与解锁。它需要独立设计封禁维度（账号还是 IP）、阈值与时长、递增还是固定、管理端解封入口、解封审计——是独立的一块，另起阶段做。本设计中 `detail` 的可扩展 JSON 结构为它预留了位置，届时加 `bannedUntil` 之类字段不需要改协议。
- 错误消息的多语言。当前只有简体中文。
- 把 `msg` 做成可配置文案。

## 三、两条刻意不拆的错误

这两处**保持现状的合并**，不要在实施时"顺手拆细"：

1. **`password.go` 的"账号或密码不正确"** 同时覆盖账号不存在、账号类型未开放、密码错误三种情况。拆开等于给攻击者一个账号枚举预言机。该文件既有注释已写明这条理由。
2. **"验证码不正确或已过期"** 合并了两种情况。拆开会泄露"这个手机号有没有被发过验证码"。

**一条相关的安全结论（实施时不要因此改动执行顺序）：** `CanLogin()` 的判断位于 `conn.Authenticate()` 成功**之后**（`service/auth.go` Login 主流程）。也就是说，能看到"账号已被冻结"的调用方，已经先证明了自己知道密码或持有有效验证码——因此暴露冻结原因**不构成账号枚举泄露**。这个顺序是安全性的前提，不能调换。

## 四、错误模型

`internal/domain` 新增：

```go
// Error 是 fp 对外的统一错误。所有面向调用方的错误都必须是它。
//
// 三个字段的分工：
//   Code   机器可读，稳定契约，调用方据此分支
//   Msg    面向终端用户，接入方可以直接展示，不含任何内部标识
//   Detail 面向接入方开发者，排查用；序列化成 JSON 字符串上线
type Error struct {
	Code   string
	Msg    string
	Detail map[string]any

	// sentinel 决定 HTTP 状态码与 gRPC code，并让 errors.Is 继续可用。
	sentinel error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.sentinel }

// DetailJSON 把 Detail 序列化成上线用的字符串。Detail 为空时返回空串
// （而不是 "{}"），让"没有细节"在线上是一个明确的空值。
func (e *Error) DetailJSON() string
```

构造与链式扩展：

```go
// Fail 构造一个领域错误。三个参数都是必填。
func Fail(sentinel error, code, msg string) *Error

// WithDesc 写入 Detail["desc"]，即默认的自由文本详情字段。
func (e *Error) WithDesc(format string, a ...any) *Error

// WithField 写入任意扩展字段，例如 retryAfterMs。
func (e *Error) WithField(key string, value any) *Error
```

用例：

```go
domain.Fail(domain.ErrRateLimited, "RATE_LIMITED", "操作过于频繁，请稍后再试").
	WithDesc("短信发送限流：target=%s 窗口内已发 %d 条", masked, n).
	WithField("retryAfterMs", retryAfter.Milliseconds())
```

**默认详情字段叫 `desc` 而不是 `detail`**，避免出现 `detail.detail` 这种嵌套。

**`domain.Errorf` 迁移完毕后整个删除**，并由架构测试禁止它复活（见第八节）。这样"每个对外错误都有码"由编译器保证，而不是靠约定——这是全量迁移相对于"只迁移登录路径、其余靠哨兵兜底"的唯一实质收益，也是选择全量迁移的理由。

**哨兵新增一个。** `ErrNotFound` / `ErrInvalidCredential` / `ErrUnauthorized` / `ErrConflict` / `ErrInvalidArgument` / `ErrRateLimited` / `ErrForbidden` 七个照旧，另加 `ErrInternal`——传输层此前用 switch 的 `default` 分支处理内部错误，给它一个显式哨兵是为了让"每个码都有登记的哨兵"在注册表里成立、可被测试遍历；`default` 分支仍然保留，接的是真正未识别、不带码的错误。八个哨兵，仍然是状态码映射的唯一依据。`Error.Code` 是在哨兵之上的细分，不替代它。

## 五、完整错误码清单

下表覆盖全部 86 处 `domain.Errorf` 调用点。同一类情形共用一个码，靠 `msg` 与 `detail` 区分——码的数量收敛到 31 个，每个都有调用方真正会分支判断的意义。

**本节的表格就是完整的码注册表**：实施时第八节第 1 条那张 `code → (哨兵, HTTP 状态码, gRPC code)` 的表要与这里逐行对齐，不允许出现只在正文里提到、表格里没有的码。

行内的文件:行号是 2026-09-04 时的位置，实施时以函数与行为为准，行号可能已漂移。

### 登录与认证（`msg` 可直接展示给终端用户）

| code | 哨兵 | HTTP | gRPC | msg | 调用点 |
|---|---|---|---|---|---|
| `CREDENTIAL_INVALID` | ErrInvalidCredential | 401 | Unauthenticated | 账号或密码不正确 | connector/password.go:58；service/user.go:344,347；service/auth.go:356 |
| `CODE_INVALID` | ErrInvalidCredential | 401 | Unauthenticated | 验证码不正确或已过期 | notify/code.go:101,112 |
| `ACCOUNT_FROZEN` | ErrForbidden | 403 | PermissionDenied | 账号已被冻结 | service/auth.go:118,228 |
| `TOKEN_INVALID` | ErrUnauthorized | 401 | Unauthenticated | 登录已过期，请重新登录 | service/auth.go:232,270；service/session.go:140,145,152,167,174 |
| `APP_DISABLED` | ErrForbidden | 403 | PermissionDenied | 应用已停用 | service/application.go:163 |
| `APP_NOT_FOUND` | ErrNotFound | 404 | NotFound | 应用不存在 | service/application.go:123,136,207,247,277 |
| `APP_CREDENTIAL_INVALID` | ErrInvalidCredential | 401 | Unauthenticated | appId 或 appSecret 不正确 | service/application.go:174,180；grpcapi/auth_interceptor.go:79,113 |
| `CONNECTOR_DISABLED` | ErrForbidden | 403 | PermissionDenied | 该应用未开放此登录方式 | service/auth.go:328,334 |
| `CONNECTOR_UNKNOWN` | ErrNotFound | 404 | NotFound | 未知的登录方式 | connector/connector.go:86；service/application.go:324 |
| `RATE_LIMITED` | ErrRateLimited | 429 | ResourceExhausted | 操作过于频繁，请稍后再试 | notify/notify.go:128 |
| `ACCOUNT_UNAVAILABLE` | ErrForbidden | 403 | PermissionDenied | 账号当前不可用 | service/auth.go:118,228 的兜底分支，见下 |

`ACCOUNT_FROZEN` 的 `detail` 带 `desc`（含 userID 与 status）。`RATE_LIMITED` 的 `detail` 带 `retryAfterMs`——现在这个秒数是拼进文本的（"发送过于频繁，请 N 秒后重试"），接入方想做倒计时只能正则抠数字。

**`CanLogin()` 为假但状态不是 `FROZEN` 时的兜底**：当前 `UserStatusDeleted` 没有任何生产代码会写入（状态机允许 PENDING_DELETE → DELETED，但没有任何地方执行该迁移；`PENDING_DELETE` 在登录时会被复活成 `ACTIVE`），所以实践中 `CanLogin()` 为假只可能是 `FROZEN`。实施时**按 `user.Status` 判定**：`FROZEN` → `ACCOUNT_FROZEN`；其余非可登录状态 → `ACCOUNT_UNAVAILABLE`（ErrForbidden / 403 / PermissionDenied / "账号当前不可用"）。加这个兜底码不是为不存在的功能做设计，而是保证将来真做了注销任务时**不会谎报成"已冻结"**。

### 管理端

| code | 哨兵 | HTTP | msg | 调用点 |
|---|---|---|---|---|
| `ADMIN_CREDENTIAL_INVALID` | ErrInvalidCredential | 401 | 用户名或密码不正确 | service/admin.go:81,90 |
| `ADMIN_DISABLED` | ErrForbidden | 403 | 管理员账号已停用 | service/admin.go:87 |
| `ADMIN_SESSION_INVALID` | ErrUnauthorized | 401 | 管理端登录已过期，请重新登录 | service/admin.go:107,112,120,124 |

`admin.go:120,124` 现在的文案是"管理端凭据损坏"，对使用者没有意义（那是 cookie 被篡改或存储损坏），统一并入 `ADMIN_SESSION_INVALID`，具体差异进 `detail.desc`。

### 参数与校验

| code | 哨兵 | HTTP | msg | 调用点 |
|---|---|---|---|---|
| `INVALID_ARGUMENT` | ErrInvalidArgument | 400 | 随调用点，见下 | connector/connector.go:73；connector/password.go:51,54；connector/smscode.go:59；httpapi/application.go:241；httpapi/respond.go:58；notify/code.go:74；notify/notify.go:113；service/application.go:64,230,233；service/user.go:70,73,248 |
| `CONNECTOR_CONFIG_INVALID` | ErrInvalidArgument | 400 | 登录方式配置不合法 | service/application.go:288,333,340,360,366,370,375,378 |
| `PHONE_INVALID` | ErrInvalidArgument | 400 | 手机号格式不正确 | service/auth.go:75；connector/smscode.go:56 |
| `PASSWORD_TOO_SHORT` | ErrInvalidArgument | 400 | 密码长度不能少于 N 位 | service/user.go:293 |
| `PASSWORD_TOO_LONG` | ErrInvalidArgument | 400 | 密码过长 | service/user.go:296；service/admin.go:52 |
| `SLUG_TAKEN` | ErrConflict | 409 | slug 已被占用 | service/application.go:89 |
| `UNION_KEY_CONFLICT` | ErrConflict | 409 | 该 unionKey 已归属其他账号 | service/user.go:222,580 |
| `CONNECTOR_ALREADY_REGISTERED` | ErrConflict | 409 | 登录方式已注册 | connector/connector.go:76 |

`INVALID_ARGUMENT` 是通用码，各调用点保留自己现有的具体文案作为 `msg`（"name 与 slug 不能为空"、"路径参数 %s 不是合法 UUID" 等）。这些消费方是控制台和 SDK 的参数校验，接入方不会按码细分，共用一个码是恰当的。

`CONNECTOR_CONFIG_INVALID` 的 `detail` 带 `field`（出问题的配置项键）与 `desc`（具体原因），让控制台将来能把错误定位到具体表单字段上。

### 资源不存在

| code | 哨兵 | HTTP | msg | 调用点 |
|---|---|---|---|---|
| `USER_NOT_FOUND` | ErrNotFound | 404 | 用户不存在 | service/user.go:309,561 |
| `IDENTITY_NOT_FOUND` | ErrNotFound | 404 | 登录标识不存在 | service/user.go:392,545 |
| `UNION_KEY_NOT_FOUND` | ErrNotFound | 404 | 未找到该 unionKey 对应的用户 | service/user.go:254 |
| `SESSION_NOT_FOUND` | ErrNotFound | 404 | 会话不存在或已过期 | store/session.go:81 |
| `CONNECTOR_NOT_CONFIGURED` | ErrNotFound | 404 | 该应用未配置此登录方式 | service/application.go:400 |
| `NOTIFY_PROVIDER_MISSING` | ErrNotFound | 404 | 通道未配置供应商 | notify/notify.go:117 |
| `SMS_TEMPLATE_MISSING` | ErrNotFound | 404 | 短信模板未配置 | notify/aliyun.go:125 |
| `ROUTE_NOT_FOUND` | ErrNotFound | 404 | 接口不存在 | httpapi/router.go 的 `r.NotFound`——路由层的 404，与"资源不存在"不同：它意味着调用方把 URL 写错了，而不是某个 id 查不到 |

### 内部错误与不变式违背

| code | 哨兵 | HTTP | msg | 调用点 |
|---|---|---|---|---|
| `INTERNAL` | ErrInternal | 500 | 服务器内部错误 | 未识别的错误兜底；以及下列不变式违背 |

不变式违背的调用点：`service/session.go:58,137`（"签发/校验会话缺少应用信息"）、`store/session.go:54,238`（ttl 必须为正）、`service/application.go:298`（配置无法序列化）、`notify/aliyun.go:48,51`（阿里云凭据缺失）、`service/user.go:362`。

这些是**编程错误或启动配置错误**，接入方分支判断没有意义。它们仍然用 `Fail(...)` 显式构造（保持"每个错误都有码"），但共用 `INTERNAL`，且**详细信息只进服务端日志、不进 `detail`**——与现有的"未识别错误丢掉原始消息"策略一致，理由见 `grpcapi/errors.go` 既有注释：那些消息可能带连接串、SQL、表名。

## 六、三层传输契约

### HTTP（管理端）

`internal/httpapi/respond.go` 的 `errorBody` 从 `{error}` 改为：

```go
type errorBody struct {
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	Detail string `json:"detail,omitempty"` // JSON 字符串，无细节时省略
}
```

线上形态：

```json
{"code":"RATE_LIMITED","msg":"操作过于频繁，请稍后再试","detail":"{\"desc\":\"短信发送限流：target=138****0001 窗口内已发 3 条\",\"retryAfterMs\":42000}"}
```

`writeError` 从错误里取 `*domain.Error`（`errors.As`）；取不到时（真正未识别的内部错误）用 `INTERNAL` + "服务器内部错误" + 空 detail，原始错误只进服务端日志。

**这是破坏性变更**：响应体不再有 `error` 字段。唯一的消费方是我们自己的控制台——`web/src/lib/api.ts` 的 `readErrorMessage` 需要改成读 `msg`（并保留"响应体不是 JSON 时退回状态码兜底"那条既有逻辑）。没有外部调用方。

### gRPC

`proto/fp/v1/common.proto` 新增（纯增量，通过 buf breaking 检查）：

```protobuf
// ErrorDetail 随 gRPC status 一起返回，承载 fp 的统一错误三元组。
message ErrorDetail {
  string code = 1;
  string msg = 2;
  // detail 是 JSON 字符串，可为空。至少包含 desc 字段，
  // 视错误类型可能带 retryAfterMs、field 等扩展字段。
  string detail = 3;
}
```

`grpcapi.statusFrom` 改为：

```go
st := status.New(grpcCode, msg)
if ds, err := st.WithDetails(&fpv1.ErrorDetail{...}); err == nil {
    return ds.Err()
}
return st.Err()   // 附加 details 失败时退回纯 status，不因此丢掉整个错误
```

**gRPC code 的映射一条都不改。** 老接入方拿到的状态码与现在完全一致，只是多了可选的 details——不升级 SDK 也不会坏。

**不引入 `google.rpc.ErrorInfo`**：`buf.yaml` 目前只有本地 `proto` 模块，用它要加 buf 依赖，还会让 `google.golang.org/genproto/googleapis/rpc` 从 indirect 变成 direct，需要进 `internal/integration/dependency_whitelist_test.go` 的白名单。对一个自用平台，这份标准化换不来相应的好处。自定义 message 零新增 Go 模块依赖。

### Go SDK

```go
// Error 是 fp 返回的结构化错误。用 errors.As 取出它来读 Code。
type Error struct {
	Code   string
	Msg    string
	Detail string // JSON 字符串，可能为空
	sentinel error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.sentinel }
```

`translate()` 与 `Auth.Validate()` 改为：先从 gRPC status 里取 `ErrorDetail`，取到就构造 `*fpsdk.Error`，其 `sentinel` 仍按现有的 code→哨兵映射选取；取不到（老服务端、或非 fp 返回的传输层错误）就退回现有的扁平哨兵，行为与今天完全一致。

接入方两种用法并存：

```go
// 老写法，继续成立
if errors.Is(err, fpsdk.ErrUnauthorized) { ... }

// 新写法
var fe *fpsdk.Error
if errors.As(err, &fe) && fe.Code == "ACCOUNT_FROZEN" {
    // 直接把 fe.Msg 展示给终端用户
}
```

**`Auth.Validate` 里"不缓存失败结果"这条必须保留。** 现有注释说明了理由：缓存有容量上限，把失败也塞进去的话，攻击者用海量随机 token 就能把真实条目挤出 LRU，逼得每个正常请求都回源。新增结构化错误不改变这一点。

**SDK 现有的哨兵全部保留**（`ErrNoToken` / `ErrUnauthorized` / `ErrUnavailable` / `ErrInvalidArgument` / `ErrRateLimited`），它们是第二阶段就发布的公开 API。

## 七、`msg` 是对外契约

`msg` 面向终端用户、接入方会直接展示，因此**改动 `msg` 的措辞等同于对外行为变更**，要当作 API 变动对待，不能当成"顺手改个错别字"。`code` 一经发布不得更名或复用于其他语义。

`detail` 不承担这个约束——它是诊断信息，字段可以增删。但接入方可能把它写进日志，所以**不要往里放会变动的大对象**。

## 八、测试策略

第三阶段的教训是"服务端有文本、SDK 丢了，而所有测试照绿"——因为没有一条测试跨越传输层边界。本设计的测试重点就是补上这一层。

1. **码表完整性（表驱动）。** 建一张 `code → (哨兵, HTTP 状态码, gRPC code)` 的注册表，测试断言：每个码都在表里；每个码的哨兵映射与 `httpapi.writeError`、`grpcapi.statusFrom` 两处实际行为一致。现在这两处是两份手写 switch，靠注释约定对齐——这条测试把约定变成断言。

2. **HTTP 与 gRPC 必须给出同一个 code。** 遍历全部哨兵与代表性错误，断言同一个 `*domain.Error` 经两条路径出来的 `code` 相同。防的是"同一个失败经 HTTP 是 403、经 gRPC 是 NotFound"这类漂移（`grpcapi/errors.go` 既有注释点名担心过这件事，此前没有测试守住）。

3. **端到端穿透测试（本设计的核心）。** 对第五节"登录与认证"那 10 个码，每个都要有一条从 service 层穿到**传输层出口**的测试，断言 `code` 与 `msg` 都正确抵达。**只测 service 层返回值是不够的**——这次的缺陷正是服务端正确、传输层丢失。

4. **冻结账号全链路。** 管理端冻结用户 → 用正确密码登录 → 断言 SDK 侧 `errors.As` 拿到的 `Code == "ACCOUNT_FROZEN"`，而不是笼统的 401。这条直接对应本次发现的问题，是验收的标志性用例。

5. **向后兼容。** 断言 `errors.Is(err, fpsdk.ErrUnauthorized)` 对新的结构化错误仍然成立；并断言当服务端**不返回** `ErrorDetail` 时（模拟老服务端），SDK 退回扁平哨兵、行为与今天一致。

6. **`detail` 不泄露内部信息。** 断言未识别的内部错误（构造一个 pgx 风格的错误）经两个传输层出来时，`detail` 为空、`msg` 是通用文案，原始消息不出现在响应里。

7. **架构测试禁止 `domain.Errorf` 复活。** 迁移完成后用 `go/parser` 断言全仓不存在 `domain.Errorf` 调用。仓库里已有同类做法（`sdk/arch_test.go`、`internal/service/arch_test.go`）。

8. **辨别力要求。** 上述每条测试在实施时都要做一次变异验证——把实现改成错误版本，确认该测试真的变红，再改回。第三阶段有一条测试正是靠这个步骤才被发现是假绿的（测试辅助函数只按 URL 过滤不看 HTTP 方法，把 `DELETE /sessions` 误计成一次列表拉取，导致 8/8 假通过）。

## 九、改动范围

**新增**
- `internal/domain/error.go`（`Error` 类型、`Fail`、`WithDesc`、`WithField`、`DetailJSON`）
- `internal/domain/codes.go`（码常量与注册表）
- `proto/fp/v1/common.proto` 的 `ErrorDetail` message
- `sdk/error.go`（SDK 侧 `Error` 类型）
- 各层测试文件

**修改**
- 86 处 `domain.Errorf` 调用点（分布：service 42、application/user/auth/session/admin 为主；connector 8；notify 8；httpapi 2；grpcapi 2；store 3）
- `internal/domain/errors.go`：删除 `Errorf`
- `internal/httpapi/respond.go`：`errorBody`、`writeError`
- `internal/grpcapi/errors.go`：`statusFrom`
- `sdk/auth.go`：`translate`、`Auth.Validate` 的错误分支
- `web/src/lib/api.ts`：`readErrorMessage` 读 `msg`
- `web/src/lib/api.test.ts`：对应断言

**不改动**
- 七个哨兵错误本身
- gRPC code 与 HTTP 状态码的映射关系
- `CanLogin()` 与 `Authenticate()` 的执行顺序（安全前提，见第三节）
- SDK 现有公开哨兵
- `Auth.Validate` 的"不缓存失败结果"策略

## 十、实施顺序建议

1. `domain.Error` 与码常量表（无外部依赖，可独立测试）
2. 两个传输层（`writeError` / `statusFrom`）+ 码表一致性测试
3. proto 与 SDK（含向后兼容测试）
4. 86 处调用点迁移，按包分批：domain → store → notify → connector → service → httpapi/grpcapi
5. 删除 `Errorf` + 架构测试
6. 控制台 `api.ts` 跟进
7. 端到端穿透测试与冻结账号全链路验收
