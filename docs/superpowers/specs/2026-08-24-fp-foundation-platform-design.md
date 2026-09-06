# fp（Foundation Platform）设计大纲

**日期**：2026-08-24
**状态**：方向已定，待细化为分期实施计划

---

## 一、背景与目标

### 1.1 现状问题

现有两个平台 `3s`（最早）和 `xxzj`（基于 3s 实现）。每新建一个项目都要移植一大堆用户 / 配置 / 权限 / 短信代码，尤其是用户体系中的多种登录方式。

代码层面的实证：

**拷贝后必然漂移。** xxzj 把 3s 的 `core/` 整个拷进 `server/core/`，砍掉一半包，然后两边再也合不回来：

| 维度 | 3s | xxzj |
|---|---|---|
| 认证模型 | `User` + 独立 `UserAuth` 集合 | `AuthPhone`/`AuthUsername`/`AuthWechat` 内嵌进 `User` |
| Mongo 驱动 | v1 | v2 |
| Token 结构 | `Uid`/`Role`/`Type`/`Token` | 增加 `Tid`/`IsMobile`/`Expiry` |
| 权限匹配 | `gstr.InArray` 精确匹配 | RESTful 路径匹配 |

**权限硬编码。** `3s/core/middleware/common.go` 从第 33 行起是 `guestPath` / `normalPath` / `servicePath` 三个硬编码字符串数组，累计 200+ 行路径。加接口要改代码、重新编译、发版。

**配置编译期固化。** `3s/core/config/config.go` 335 行 Go 变量，费率、Redis key 前缀、业务常量、base64 logo 全混在一起。改一个费率要重新编译发版。

**短信供应商切换靠改代码。** `SendSmsZt` / `SendSmsAliyun` / `SendSmsTcloud` 三份实现，切换靠改 `SendSms` 里注释哪一行；模板 ID 硬编码在 if-else 分支里，历史 ID 以注释形式层层堆积。

### 1.2 目标

建立通用基础服务平台 fp，后续新项目通过 SDK 直接对接，不再移植代码。同时提供管理 UI，通过 UI 完成配置。

### 1.3 非目标

- **不迁移 3s。** 3s 保持现状，不做任何兼容妥协，仅作为设计借鉴来源。
- **xxzj 后续迁入。** 是设计约束（数据模型不要堵死），不是当前交付目标。
- **不做多租户。** 一个 fp 部署 = 一套用户体系。需要隔离就再部署一套。
- **不做业务逻辑。** 订单、资金、支付密码、IM 业务层等不进 fp。

---

## 二、设计原则

**原则 1：配置化优先。**
登录方式、权限、配置项、通知模板全部 UI 可配。改配置不需要改代码、不需要重新发版。

**原则 2：fp 提供配置和数据，不提供每次请求的计算。**
SDK 本地判定，fp 挂了业务不挂。这条原则决定了 SDK 的能力边界（见第十章）和令牌校验方案（见 4.5）。

**原则 3：模块可独立启用。**
接入方只用身份、不用配置中心，也应该能跑。

**原则 4：fp 是全局单点，必须又小又稳。**
任何会放大故障域的东西（长连接、高频计算、有状态服务）都拆到 fp 之外。

---

## 三、系统组成

### 3.1 部署形态

```
        fp Server（单二进制，go:embed 内嵌管理 UI）
   ┌──────┬────────┬──────────┬──────┬──────┐
   │ IAM  │ 授权    │ 配置中心  │ 通知 │ 实名 │
   │      │(casbin)│          │      │      │
   └──────┴────────┴──────────┴──────┴──────┘
              service 层（业务逻辑）
   ┌────────────────────┬───────────────────┐
   │ HTTP/JSON  :8080   │  gRPC  :9090      │
   └────────────────────┴───────────────────┘
            PostgreSQL 18 + Redis
         ▲                          ▲
   管理 UI (SPA)              Go SDK（业务项目）
                            一条 gRPC 双向流
                            承载回源 + 推送

        fp-im（独立服务，可选，多实例）
   client ──ws──► im 节点 ◄──gRPC──► 业务 server
```

**一个 fp 部署 = 一套用户体系 + N 个接入应用。**

多个业务平台共享用户体系 = 同一个 fp 部署下建多个 Application。
需要隔离 = 再部署一套 fp。

这个决策是可逆的，且是 UI 上的一个下拉框，不需要事先想清楚。

### 3.2 服务划分

| 服务 | 职责 | 部署 |
|---|---|---|
| **fp** | IAM / 授权 / 配置中心 / 通知 / 实名 + 管理 UI | 单二进制，可单实例起步 |
| **fp-im** | 连接网关 + 消息路由，只转发不管业务 | 多实例，独立扩容，**可选** |

fp-im 独立的三个理由：

1. **运行时特性相反。** fp 是无状态请求响应，加机器即可扩容；im 是有状态长连接，需要连接亲和性和节点路由。塞一起就无法独立扩容。
2. **故障域隔离。** im 连接数打满不能拖垮身份认证——身份认证一挂所有接入方全挂。
3. **违反原则 2。** fp 提供配置和数据，im 恰恰是"每条消息的实时计算"。

**fp 不依赖 fp-im。** fp 自带 gRPC 双向流用于控制面推送（配置变更 / 策略变更 / 撤销通知），不复用 fp-im，避免 fp↔fp-im 循环依赖，保证 fp 能独立部署运行。

### 3.3 四条通信链路

| 链路 | 协议 | 理由 |
|---|---|---|
| SDK ↔ fp | **gRPC 双向流** | 一条流同时承载回源与推送；连接永不空闲；与 fp-im 保持同一套通信范式 |
| 管理 UI ↔ fp | **HTTP/JSON** | SPA 调不了 gRPC |
| client ↔ fp-im | **WebSocket** | 双向；设备友好；无浏览器同域连接数限制 |
| fp-im ↔ 业务 server | **gRPC 双向流** | 高频（每条消息一次）；服务间内部调用；需要背压与连接复用 |
| fp-im ↔ fp-im | **Redis pub/sub 起步** | 沿用现有做法；接口预留，量大了换 gRPC 节点直连 |

### 为什么 SDK↔fp 用 gRPC

**① 一条双向流统一请求与推送**

```
SDK ←──── 一条 gRPC 双向流 ────→ fp
     上行：ValidateToken(tid)
     下行：RevokeEvent / ConfigChanged / PolicyChanged
```

SDK 侧只维护一套连接生命周期与重连退避，不需要"HTTP 连接池 + 独立推送连接"两套。

**② 连接永不空闲——这是延迟的真实来源**

回源的延迟差异**不在编解码上**：回源响应体约 100 字节，JSON 与 protobuf 的编解码差异约 2 微秒，占单次回源（~0.7 ms）的 0.3%，再被 >93% 的缓存命中率稀释后不可测量。

真正的差异在**冷连接**：

| | 明文 | TLS 1.3 | TLS 1.2 |
|---|---|---|---|
| TCP 握手 | 1 RTT | 1 RTT | 1 RTT |
| TLS 握手 | — | 1 RTT + 密码学开销 | 2 RTT + 开销 |
| **额外成本（同机房）** | 0.1–0.5 ms | **2–5 ms** | **3–8 ms** |

HTTP 连接池会空闲回收：Go `http.DefaultTransport` 的 `IdleConnTimeout` 为 90 秒，nginx `keepalive_timeout` 默认 75 秒，云 LB 空闲超时普遍 60 秒。**几分钟没有回源，下一次必然付冷连接成本——相比热连接是 3–7 倍。**

这不是均匀分摊的延迟，是尾延迟：低流量实例每次回源都是冷的；低峰期所有实例变冷；部署重启、autoscaling 扩容后全冷；fp 重启会引发所有 SDK 实例同时重连的连接风暴。

gRPC 解决这个问题是**结构性的**，不靠"连接池碰巧还在"：

- `keepalive.ClientParameters` 用 HTTP/2 PING 帧主动保活
- 内建指数退避重连，天然处理连接风暴
- **双向流本身承载推送，这条流永远是活的，连接根本不会进入空闲状态**

①和②互相加强：因为推送走同一条流，连接永不空闲；因为连接永不空闲，回源永远是热的。

**③ 与 fp-im 保持同一套通信范式**

`fpsdk.IM` 要连 fp-im，grpc-go 这个依赖跑不掉。fp 也用 gRPC 不算新增依赖，反而是只维护一套通信范式。

**④ 强类型契约**：proto 的约束比 OpenAPI 更硬。

### 传输层组织方式

```
fp Server
├── gRPC 服务（:9090）  ← SDK
├── HTTP/JSON（:8080）  ← 管理 UI
└── service 层          ← 业务逻辑，两个传输层共用
```

两个端口，不用 cmux 多路复用——省一个端口不值得那份复杂度。

**不采用 grpc-gateway 从 proto 生成 REST。** 管理 UI 的 API 与 SDK 的 API 诉求本就不同（UI 要分页 / 搜索 / 批量，SDK 要单点高频），强行用一份 proto 表达两者会互相将就，且生成的 REST 在 field mask、oneof 场景下形状别扭。

### gRPC 相关设计点

| 项 | 说明 |
|---|---|
| **流认证** | 建流时 metadata 带 `appId` + 签名，服务端验一次，之后整条流已认证，不必每个 RPC 重复验 |
| **keepalive 参数** | 客户端 `Time` / `Timeout` / `PermitWithoutStream`；服务端 `EnforcementPolicy.MinTime` 防客户端 ping 过频被误断 |
| **重连退避** | 使用 gRPC 内建退避，覆盖 fp 重启时的连接风暴 |
| **流断开时的降级** | 流断开 = 收不到撤销推送 → SDK 自动收紧本地 `cache_ttl`（如降至 5 秒），恢复后还原 |

最后一条值得单独说明：推送通道断开时撤销的"加速"能力消失，只剩 TTL 兜底。SDK 感知到流断开后主动收紧缓存窗口，把安全性拉回来。**因为 gRPC 的流状态是明确可感知的，这个策略才做得了**——这是 SSE 方案给不了的。

---

## 四、IAM 模块

> 业界参考：Keycloak（Realm/Client）、Auth0（Tenant/Application/Connection）、Casdoor（Organization/Application）、FusionAuth（Tenant/Application/Registration）

### 4.1 核心实体

| 表 | 归属 | 内容 |
|---|---|---|
| `application` | 部署级 | 接入端。启用的登录方式、会话三参数、cookie domain、appId/appSecret、OIDC 预留字段（`redirect_uris`/`grant_types`/`client_secret`） |
| `user` | 部署级 | 全局唯一用户。手机、邮箱、密码、昵称、头像、性别、状态 |
| `identity` | user 一对多 | 登录凭据。`type` + 唯一标识 + `union_id` + 凭据数据 + 最后登录时间 |
| `user_application` | user × application | 应用内注册关系。应用内昵称 / 状态 / 扩展字段 / 注册时间 |
| `user_field_def` / `user_field_value` | 部署级 / user | 用户扩展字段 schema 化 |
| `session` | user | 全局登录态。设备信息、IP、UA、创建 / 最后活跃时间 |
| `login_log` | — | IP、归属地、UA、结果、失败原因 |

**关键设计决策：**

**① `identity` 用独立表，不用内嵌结构。**
xxzj 现在把 `AuthPhone`/`AuthUsername`/`AuthWechat` 内嵌进 `User`，每加一种登录方式就要改 struct。fp 的登录方式必须能在 UI 上动态增删，因此采用 3s 的独立表方向。

**② 角色归属 `user_application`，不归属 `user`。**
3s 现在 `User.Role = ADMIN/MERCHANT/NORMAL/SERVICE` 是全局字段。一旦两个平台共享用户体系，角色立刻串台。角色关系实际由 casbin 的 grouping policy 承载（见第五章），`user_application` 只存应用内的注册状态与扩展字段。

**③ 用户扩展字段 schema 化。**
池级定义字段名 / 类型 / 必填 / 可见性，用户上存值。新项目加业务字段不需要改 fp 的表结构。

**用户状态机：**

```
ACTIVE ⇄ FROZEN
ACTIVE → PENDING_DELETE（保护期内可撤销）→ DELETED
```

保护期设计沿用 3s 的 `SUBMIT_DELETE` 机制：提交注销后进入保护期，期内任意登录行为自动撤销注销。

### 4.2 登录方式（Connector）

首批五种，**平级实现**，后续按需增加：

| Connector | 说明 |
|---|---|
| `sms_code` | 手机号 + 短信验证码（依赖通知中心） |
| `password` | 用户名 / 邮箱 / 手机号 + 密码 |
| `email_code` | 邮箱 + 验证码（依赖通知中心） |
| `wechat_mp` | 微信公众号网页授权 |
| `qrcode` | PC 出码 → 已登录端扫码确认 |

统一接口：

```go
type Connector interface {
    Type() string                          // "sms_code" / "password" / "wechat_mp" ...
    ConfigSchema() []Field                 // UI 表单由此自动渲染
    Authenticate(ctx, req) (*Identity, error)
}
```

新增登录方式 = 新增一个实现 + `Register()` 一行。UI 表单靠 `ConfigSchema()` 自动生成，不需要改前端。

各 Connector 的 secret 加密存库，通过 UI 配置。**登录方式的启用与否是 application 级配置**，不同应用可启用不同组合。

**微信建模注意**：当前只做公众号，但数据模型按 `union_id` 建。将来加小程序 / 开放平台 / App 时，同一个人的多个 `open_id` 靠 `union_id` 归并到同一个 `user`。3s 的 `bindWechatWithOpenIdAndUnionId` 已在处理这件事，但逻辑是散的，fp 里统一到用户中心处理。

### 4.3 账号归并规则

多登录方式最容易出错的地方，必须显式定义：

- 同一手机号在 `sms_code` 和 `password` 下归并为同一个 `user`
- 同一 `union_id` 下的多个 `open_id` 归并为同一个 `user`
- 归并冲突（两个已存在的 user 因新绑定而需要合并）的处理策略需在实现阶段明确

### 4.4 会话与令牌

**会话三参数**（application 级配置）：

> 业界参考：Keycloak 的 SSO Session Idle / SSO Session Max；Auth0 的 Inactivity Lifetime / Absolute Lifetime

| 参数 | 含义 | 3s 现状 |
|---|---|---|
| `idle_timeout` | 多久不访问就失效（= 延期窗口） | ✅ 7 天 / 30 天（`ExpireLoginFun` 按 UA 区分） |
| `max_lifetime` | 从首次登录起最长活多久，到期必须**重新认证** | ❌ **缺失**，需补上 |
| `rotate_interval` | 多久换一次 token 值 | ✅ 24 小时 |

**`rotate_interval` 不能替代 `max_lifetime`**：轮换只换 token 的值，会话本身仍是同一个，攻击者拿到旧 token 后跟着轮换走可以一直有效。`max_lifetime` 要求的是重新认证。

**不同应用的典型配置：**

| 应用类型 | idle_timeout | max_lifetime | 延期 |
|---|---|---|---|
| C 端 App | 30 天 | 90 天 | ✅ |
| C 端 Web | 7 天 | 90 天 | ✅ |
| 后台管理 | 2 小时 | 12 小时 | ✅（闲置登出，合规要求） |
| 设备通道 | 长期 | — | ❌ 用设备凭据，不是用户会话 |
| OIDC 对外 | — | — | ❌ 绝对过期 + refresh token |

**延期（滑动过期）的价值**：活跃用户永不掉线，不活跃用户快速失效——在体验和安全之间取动态平衡。绝对过期只能在两头选一个：7 天则全体每周重登，90 天则泄露的 token 稳稳有效 90 天。

**跨应用单点登录（SSO）：**

```
Session（fp 全局登录态，一个人一个设备一条）
  └── 各 Application 凭 session 换取各自的访问凭据
       └── 撤销时按 session 批量作废
```

用户在应用 A 登录后访问应用 B 无需重新登录。改密 / 封号 / 踢下线可一次性作用到所有应用。这层结构也是将来接 OIDC 时所需，一步到位。

**其他会话能力**：在线设备列表 / 强制下线 / 同类设备互斥。

**存储分工**：session 主存 Redis（沿用 3s 的 `TokenPrefix + tid` 与 `TokenUidPrefix + uid` 的 Set 结构），回源只查 Redis 不碰 PG；PG 只持久化 `login_log` 等需要长期审计的数据。Redis 丢失 = 全体重登，这是 3s 现在就接受的风险。若将来需要 session 持久化再补 PG 副本。

**Token 轮换过渡期**：轮换出新 token 的瞬间，可能有并发请求还带着旧 token 在路上。旧 token 保留 `max(15s, cache_ttl)` 缓冲后再失效——3s 和 xxzj 都有这一手（`redis.SetEx(TokenPrefix+tid, tokenStr, 15*time.Second)`），fp 下还需覆盖 SDK 本地缓存中的旧条目，故取两者较大值。

### 4.5 令牌校验方案

> 业界参考：RFC 7662 Token Introspection + Keycloak Push Revocation Policy + AWS API Gateway Lambda Authorizer 缓存模型

**方案：opaque token + SDK 本地缓存 + 撤销推送。**

```
1. 请求带 opaque token（tid，32 字节随机串）
2. SDK 查本地内存缓存 → 命中且未超 cache_ttl → 放行（0 网络）
3. 未命中 → 调 fp 校验一次 → 按返回的 cache_ttl 缓存
4. fp 通过 gRPC 双向流推送撤销事件 → SDK 立即删除对应缓存项
```

**为什么不用纯 JWT 本地验签**（已列出的代价）：

| # | 缺点 |
|---|---|
| ① | **撤销延迟**。JWT 签发后自证有效，踢下线 / 改密 / 封号后仍合法。撤销名单推送**是唯一防线，丢了就没了**；且 SDK 冷启动时名单为空，必须先全量拉取，之前的窗口不安全；名单无限增长，只能靠 `exp` 淘汰，反过来逼短有效期 |
| ② | **有效期两难**。长则撤销窗口长、泄露危害大；短则频繁刷新，本地验签省下的网络调用又回来。且"访问时延期"做不到——JWT 不可变，延期等于重新签发 |
| ③ | **权限数据不实时**。JWT 里的 roles 在签发时冻结，改角色后 token 里还是旧的 |
| ④ | **体积**。300–800 字节 vs opaque 的 32 字节，cookie 有 4KB 上限且每请求都带 |
| ⑤ | **拿不到活跃状态**。3s 每请求 `redis.HSet(UserLastOnlineTime, uid, now)` 用于在线时间显示；本地验签不经过 fp，fp 不知道用户还活着。"同账号只允许一个设备在线"同理需要中心化状态 |
| ⑥ | **密钥轮换麻烦**。JWKS 多密钥并存，旧公钥必须保留到所有旧 token 过期 |
| ⑦ | **排障困难**。"这个 token 为什么还有效"要翻各 SDK 实例本地状态，而非查一个中心 Redis |

**opaque + 缓存方案的关键优势**：撤销推送只是**加速**而非唯一防线。推送丢失时最长 `cache_ttl` 后自然回源被拒；SDK 冷启动无需全量拉取，回源即可。安全性上限更高。

**为什么必须做本地缓存：**

3s 现在每请求查一次 Redis，但那是**同进程直连**（`redis.GetString(TokenPrefix + tid)`，<1ms）。fp 化之后业务方要校验 token 就得**跨服务调 fp**，变成一次网络往返（热连接 ~0.7 ms，冷连接 2–5 ms），且 fp 会成为每个请求路径上的强依赖——**fp 一挂，所有接入方立刻全挂**。本地缓存同时解决延迟和故障域两个问题。

**回源量估算：**

准确公式不是"用户数 / cache_ttl"，而是：

```
回源 QPS = Σ 每个用户的 min(他的请求频率, 1/cache_ttl) × 业务方实例数
```

- 持续活跃用户（每几秒一个请求）：每 cache_ttl 回源 1 次
- 低频用户（每 5 分钟一个请求）：缓存早已过期甚至被 LRU 淘汰，**每 5 分钟才回源 1 次**，不是每 30 秒

所以"用户数 / cache_ttl"是**上限**，只在全部用户持续高频访问时成立。实际按同时在线且持续操作的人数算：10 万 DAU → 同时在线约 10% → 其中持续操作约一半 → 5,000 / 30s ≈ 167 QPS。乘以业务方实例数（4 个实例各自缓存、各自回源）≈ 670 QPS。

**回源的实际成本是一次 Redis GET**（`token → session`），不碰 PG。670 QPS 的纯 Redis 读不构成瓶颈。

**四个可调手段：**

| 手段 | 效果 |
|---|---|
| 调大 `cache_ttl`（应用级配置） | 与回源量成反比。C 端普通业务 60–120 秒可接受，后台管理 10–30 秒，高危操作可配成不缓存 |
| SDK 二级缓存用业务方共享 Redis | 第一个实例回源后写入共享 Redis，其他实例读它 → **消掉多实例放大因子**，代价是多一跳 Redis（<1ms） |
| `singleflight` 合并 | 同实例内同一 token 的并发 miss 只发一次回源，防缓存过期时的惊群 |
| LRU 容量上限 | 防止缓存无界增长 |

**方案对比：**

| | 3s 现状（每次查 Redis） | 纯 JWT + 撤销名单 | **opaque + 缓存 + 推送** |
|---|---|---|---|
| 每请求网络 | 1 次（跨服务后 0.7–5 ms） | 0 | ~0（命中率 >93%） |
| 撤销延迟 | 立即 | 推送延迟 | 推送延迟 |
| 推送失败时 | — | ❌ 一直有效到过期 | ✅ cache_ttl 后回源被拒 |
| 冷启动 | 无问题 | ❌ 要全量拉名单 | ✅ 无问题 |
| 权限实时性 | 实时 | 差 | 好（每 cache_ttl 回源刷新） |
| 访问延期 | ✅ | ❌ 做不到 | ✅ 回源时顺便延期 |
| token 体积 | 32B | 300–800B | 32B |
| fp 短暂故障 | ❌ 业务全挂 | ✅ 无影响 | ✅ 缓存内无影响，可配 fail-open/closed |

**JWT 保留给 OIDC 对外场景**：第三方系统没有 fp 的 SDK，做不了缓存和撤销订阅，只能给 JWT。因此最终是双轨——内部 SDK 走 opaque，对外 OIDC 走 JWT。

#### 4.5.1 一致性规则：SDK 永远不判断过期

**竞态场景：**

```
t=0    SDK-A 回源，fp 返回 {uid, expiry: t+20}，SDK 缓存 30 秒
t=10   用户在 SDK-B 上发请求，fp 把 token 延期到 t+3600
t=20   SDK-A 缓存还剩 10 秒，但缓存里记的 expiry 是 t+20
       → SDK-A 判定「过期」→ 清 cookie，误踢用户
                                    ❌ fp 侧 token 明明有效
```

**根因**：SDK 缓存了一个会被 fp 侧改变的值（`expiry`），还拿它做否定判断。

**规则：fp 返回 `cache_ttl`，不返回 `expiry`。**

```
cache_ttl = min(应用配置的缓存时长, token 剩余有效期)
```

```json
{ "uid": "...", "session_id": "...", "cache_ttl": 20 }
```

SDK 逻辑退化为两行，不含任何推断：

```
缓存命中且未超 cache_ttl  → 放行
否则                      → 回源
```

**过期判断只由 fp 做，SDK 只是个遵守 TTL 的缓存**——与 DNS TTL、HTTP `Cache-Control: max-age`、AWS Lambda Authorizer 同一个模式：权威源决定缓存多久，缓存端不自作主张。

副作用正是想要的：token 越接近过期，`cache_ttl` 越短，回源越频繁。token 只剩 2 秒时 `cache_ttl=2`，绝不会误放行。

**竞态全量检查：**

| 场景 | 结果 |
|---|---|
| 撤销推送到达 → SDK 删缓存 → 下次回源被拒 | ✅ |
| 撤销推送丢失 → 最长 cache_ttl 后回源被拒 | ✅ 有兜底 |
| SDK 冷启动、推送流未连上 | ✅ 回源是主路径，推送只是加速；流未连上时 SDK 自动收紧 `cache_ttl` |
| fp 短暂不可用 | ✅ 缓存内不受影响；过期后按配置 fail-open / fail-closed |
| 并发延期写打爆 fp | ⚠️ 需降频，见 4.5.2 |

#### 4.5.2 延期写降频

"访问延期"意味着每次校验都要把过期时间往后推——**这是写操作**。

按 4.5 的估算，10 万 DAU 场景下回源约 670 QPS（上限 3,300）。**如果每次回源都写一次延期，读压力就原样变成了写压力。**

写 QPS 对 Redis 无所谓，但一旦涉及 PostgreSQL 代价完全不同：每次 `UPDATE` 产生 WAL、产生 dead tuple 触发 autovacuum、同一行并发 `UPDATE` 造成行锁排队。而这些写业务上毫无价值（把过期时间从 `t+3600` 改成 `t+3630`）。且 fp 是所有接入方共享的单点，写压力是全部业务方的总和。

**降频规则（沿用 3s 的做法）：**

3s `core/middleware/common.go:381` 已是标准答案，两层：

```go
} else if nowTime-token.UpdateTime >= 2*60*60*1000 {   // ① 时间窗降频
    lua := redis.Lua(config.ExistsLua, config.UpdateToken+tid, 10)  // ② 分布式锁去重
    if gconv.String(lua) == "0" {
        token.UpdateTime = nowTime
        redis.SetEx(config.TokenPrefix+tid, json.String(token), ...)
    }
}
```

fp 采纳同样的两层，并明确为三条规则：

1. **时间窗降频**：距上次延期不足 `extend_interval`（默认 10 分钟，可配）则不写
2. **分布式锁去重**：防止并发请求同时触发同一行的写
3. **轮换过渡期** `max(15s, cache_ttl)`

**叠加效果：**

```
原始请求量        10 万 DAU × 每人若干次请求
      ↓ SDK 本地缓存（cache_ttl=30s）+ 共享 Redis 二级缓存 + singleflight
回源 fp（读）      ~670 QPS
      ↓ 时间窗降频（10 分钟）
延期写            ~33 QPS          ← 降 20 倍
      ↓ 分布式锁去重
实际写            ≤33 QPS，且无同行并发
```

**降频不影响用户**：只要用户在任意一个延期窗口内有过访问，token 就会被延期。降频只影响延期的精度，不影响是否掉线。前提是 `extend_interval << idle_timeout`（10 分钟 vs 7 天，比例可忽略）。

### 4.6 登录风控

可插拔环节，挂在登录流程上：

- 登录失败次数锁定
- 异地登录提醒
- IP 黑白名单（对应 3s 的 `risk:*` 系列）
- 同号码 / 同 IP 发送频率限制
- 设备指纹

规则在 UI 配置，判定在 SDK / fp 服务端本地执行。

### 4.7 账号管理

注册 · 改密 · 忘记密码 · **换绑手机（旧手机验证 / 实名验证双路径，沿用 3s 的 `UserReBind`）** · 绑定 / 解绑第三方 · 注销与保护期 · MFA（TOTP + 短信二次验证）

原"账号安全"独立模块打散到各归属模块：

| 能力 | 归属 |
|---|---|
| 密码策略、加密算法（bcrypt / argon2） | `password` Connector |
| MFA | 登录流程 |
| 登录失败锁定、异地提醒、IP 黑白名单 | 登录风控（4.6） |
| 注销与保护期 | 用户生命周期（4.1） |
| 换绑手机 | 账号管理（4.7） |
| ~~支付密码~~ | ❌ 不进 fp——属于资金业务，只有 3s 需要 |

---

## 五、授权模块

> 业界参考：Casdoor 的 Role / Permission → casbin policy 编译模型

**分层：**

```
UI 层:      Role（角色，可继承）   Permission（资源 + 动作 + allow/deny）
              ↓ 编译
casbin 层:  p, role, app_id, resource, action, effect
            g, user, role, app_id
```

这层间接很关键——**不能让人在 UI 上直接编辑 casbin 原始规则**。

**`domain` = `application_id`**，天然支持一人在多应用不同角色：

```
g, user_123, ADMIN,  app_新项目后台
g, user_123, NORMAL, app_xxzj
```

同一个人两个应用两个角色，一条 policy 解决，不需要额外的关系表。

**model.conf 采用 RBAC with domains + deny-override。**

### 5.1 权限点注册

**业务方 SDK 启动时上报**自己的接口路径 / 菜单 / 按钮清单，fp 存下供 UI 勾选。

这是 3s 那 200 行硬编码路径数组的解药——**路径从代码变成注册数据**。

### 5.2 鉴权执行

策略变更 → 推送 SDK → 本地缓存刷新 → 业务进程内 `Enforce()`，**不走网络**（原则 2）。

### 5.3 预留能力

组织树 + 数据范围（本人 / 本部门 / 全部）。一开始就上 casbin 并预留结构，避免 `MERCHANT` 这类场景来了再改。是否启用留待实现阶段判断。

### 5.4 前端权限

登录后一次性下发菜单树 + 按钮权限点。

### 5.5 一条硬约束：不引入过渡态的角色字段

在授权模块交付之前，**不得在 `user` 或 `user_application` 上放任何简化的 `role` 字段**。

`user_application` 表提前建好，授权模块交付时直接接 casbin，中间不留过渡态。3s 的 `User.Role = ADMIN/MERCHANT/NORMAL/SERVICE` 就是这样产生的——一个当初"先简单区分一下"的字段，现在成了共享用户体系的最大障碍。

---

## 六、配置中心

> 业界参考：Nacos / Apollo

对症：3s `config.go` 335 行编译期变量，改费率要重新编译发版。

> **以下五条已被 `2026-09-05-fp-config-center-design.md` 推翻，本节按最终实现更新；完整推导见该文档第二节「明确不做」。**

- **层级**：应用 × 分区（`DEFAULT` / `WEB`），**没有环境维度**——一个 fp 部署 = 一套用户体系（见 §1.3「不做多租户」），环境需要隔离就整套重新部署一次，不靠配置里加一个维度解决
- **配置项只带类型与说明**（类型 / `desc`）。**没有默认值**——密钥类必然要人配，启动本来就卡在这一步，且有默认值时改字段名会静默回落成默认值，事故无声；**没有 min/max/oneof 校验规则**——只校验类型转换，范围与枚举由业务方在 `Bind` 之后自己判；**没有 secret 类型、不加密落库**——配套纪律是密钥类必须建在 `DEFAULT` 分区，否则会随 `WEB` 分区一起下发到浏览器
- **SDK 流程**：启动拉取 → 绑定到 Go struct → 长连接接收变更。**没有本地快照文件兜底**——代价是 fp 不可达期间业务方进程无法重启（详见 §10.3）
- 版本历史 + 一键回滚
- **不做变更审计**：今天只有一个管理员（`AdminService` 没有创建第二个的路径），"谁改的"这一列恒为同值，记了也没有信息量
- **不做加密配置项**：与上面「没有 secret 类型」同一条纪律，密钥类必须建在 `DEFAULT` 分区

配置项由人在控制台创建，SDK 不上报 schema——`Bind` 失败时给出的缺失字段清单（含类型）代替 schema 上报，告诉人"该建哪些 key"。

---

## 七、通知中心

对症：`sms.go` 里改注释切供应商、模板 ID 硬编码在 if-else、历史 ID 以注释堆积。

- **通道**：短信 / 邮件 / 站内信（预留语音、App Push、微信模板消息）
- **供应商抽象 + 路由与自动降级**：主供应商失败自动切备用（现有阿里 / 腾讯 / 助通三家改造成 provider 实现）
- **模板管理**：fp 内部模板 key ↔ 各供应商模板 ID 映射
- **频率限制**：同号 30s / 1h / 1d、同内容、同 IP（对应 3s 的 `RiskSmsSend`）
- 发送记录 + 回执 + 失败重试
- 变量渲染

---

## 八、实名认证

- **通道**：身份证二 / 三要素、银行卡四要素、人脸核身
- 供应商抽象 + UI 配置
- 认证状态机：未认证 / 认证中 / 已认证 / 认证失败
- **合规要点**：姓名 / 身份证号加密存储，明文不出库；查询接口默认脱敏；访问留审计；**实名信息不进 token**
- 与 IAM 的关系：`user.realname_status` + 独立 `realname` 表

---

## 九、fp-im

### 9.1 定位

**独立服务，只负责转发，不实现具体业务。**

> 业界参考：Centrifugo（开源实时消息服务器，定位就是"只转发不管业务"）

现有两个项目的 "im" 根本不是同一个东西，共性只在传输层：

| | 3s 的 im | xxzj 的 im |
|---|---|---|
| 传输 | SSE | WebSocket |
| 对象 | 人 ↔ 人 | 服务端 ↔ 设备 |
| 分组键 | `orderNo` / `chatId` | `mac` |
| 核心 API | `ChatJoin(orderNo, chatType, uid, role)`、`SendKf` | `StreamServerMsg(cmd, mac, data)`、`Connect(client, id)` |
| 本质 | 订单聊天 + 客服工单 | 设备控制通道 |

fp-im 提取的正是那个共性：**连接管理 + 路由表 + 跨节点投递**——每个项目都要重写一遍的基础设施。

### 9.2 架构

```
                     ┌────────┐
  client ──ws──────► │  im-1  │◄─┐
  client ──ws──────► │        │  │
                     └────────┘  │
                     ┌────────┐  │ gRPC 双向流
  client ──ws──────► │  im-2  │◄─┤
                     └────────┘  │
                          ▲      ▼
              路由表      │    ┌─────┐    server-1
             (Redis)      │    │ LB  │───►server-2
   conn:{clientId}→im节点 ┘    └─────┘    server-3
```

**核心抽象是 Channel**，不是"会话"也不是"聊天"：

```
业务 server → fp-im:  publish(channel, payload)
client      → fp-im:  subscribe(channel)
fp-im:                把 payload 原样投给该 channel 的在线订阅者
```

频道命名由业务方定：

| 业务 | channel |
|---|---|
| 3s 订单聊天 | `order:{orderNo}` |
| 3s 客服会话 | `kf:{chatId}` |
| xxzj 设备控制 | `device:{mac}` |
| 任意私人推送 | `user:{uid}` |

**三件事：**

**① 连接注册表**（Redis，带 TTL，心跳续期）

```
conn:{clientId}  → {imNodeId, connId, connectedAt}
node:{imNodeId}  → {addr, lastHeartbeat}
```

im 节点崩溃后 TTL 自动清理脏数据，client 自动重连到其他节点。

**② 上行**：client → im → 通过 gRPC 打到 LB 后任一 server（server 无状态）

**③ 下行**：server 调 `Push(clientId, payload)` → fp-im 查注册表定位节点 → 本节点直投 / 转发到目标节点

**业务方永远不需要知道 client 连在哪个 im 节点上。** 这是 fp-im 的核心价值。

### 9.3 三条红线

守住这三条，fp-im 就永远是转发器：

1. **不解析 payload**——对 fp-im 是不透明字节
2. **不存消息**——离线消息由业务方存库，client 重连后向业务方拉增量
3. **不理解 channel 的业务含义**——命名规则完全由业务方定

**业务处理全部在业务 server**：client 上行 → im 转发 → server 存库 / 敏感词过滤 / 风控 → server 下发 → im 转发 → 目标 client。im 只是条透明管道。

### 9.4 关键设计决策

| 项 | 决策 | 理由 |
|---|---|---|
| **投递语义** | 默认**最多一次**（client 不在线就丢） | 做至少一次要 ack + 重试 + 持久化，im 立刻有状态。离线消息由业务方存库。可选开启有界离线队列（Redis + TTL）给设备控制场景 |
| **顺序** | 单连接天然有序；跨节点转发加序号，client 侧丢弃乱序或重排 | |
| **背压** | 每连接有界发送队列，满了直接断连接 | 让 client 重连并向业务方拉历史，比无限堆积拖垮节点好 |
| **鉴权** | 连接时验 fp token；**订阅 channel 由业务方签发短期订阅凭证**，im 只验签不判业务 | 业务规则（这单是不是你的）只有业务方知道。内置默认免签规则 `user:{自己的uid}` |

---

## 十、Go SDK

一个 Go 包，模块化，用不到的不初始化：

```go
fpsdk.Auth      // 认证中间件（本地缓存 + 撤销订阅）
fpsdk.Perm      // 鉴权中间件（本地 casbin + 策略同步 + 权限点上报）
fpsdk.Config    // 配置绑定 + 热更新
fpsdk.Notify    // 短信 / 邮件 / 站内信
fpsdk.Realname  // 实名认证
fpsdk.IM        // fp-im 客户端
fpsdk.Local     // 本地工具（不走网络）
```

**连接层**：SDK 与 fp 之间维持一条 gRPC 双向流，上行承载回源等 RPC，下行接收撤销 / 配置变更 / 策略变更事件。流的生命周期、keepalive、退避重连由 SDK 统一管理，各功能模块共用。

**缓存层**（`fpsdk.Auth` 的核心）：

| 机制 | 作用 |
|---|---|
| 进程内 LRU + TTL | 主缓存，遵守 fp 下发的 `cache_ttl`，有容量上限 |
| 共享 Redis 二级缓存（可选） | 业务方多实例共用，消掉多实例回源放大因子 |
| `singleflight` | 同一 token 的并发 miss 只发一次回源，防惊群 |
| 流断开时收紧 `cache_ttl` | 推送不可用时主动降低缓存窗口（如 5 秒），把安全性拉回来 |

### 10.1 能力边界

按原则 2 划分：

| 走 fp（远程） | 不走 fp（纯本地 Go 包） |
|---|---|
| 认证中间件（本地缓存 + 撤销订阅） | 限流（本地 Redis Lua） |
| 鉴权中间件（本地 casbin Enforce） | 防重复提交 |
| 权限点上报 | ID 生成（业务方进程内生成 uuidv7） |
| 用户查询与管理 | 敏感词过滤 |
| 配置绑定 + 热更新 | 参数校验 |
| 通知发送、实名发起 | |

**右列的能力 fp 服务端一行代码都不用写**——它们只是 SDK 里的普通 Go 包，跑在业务方进程里、用业务方自己的 Redis。3s 的 `core/limit`、`core/repeat`、`core/id`、`core/sensitive`、`core/validator` 清理后直接搬。

**反例说明**：限流若做成调 fp 的 `/limit/check`，每请求 +1 次 HTTP 往返（5–20ms vs 本地 <1ms），fp 挂则所有业务方全挂，且 fp 要扛所有业务方的总 QPS。

### 10.2 IM 封装

server 侧：

```go
im := fpsdk.IM.Server(cfg)

im.OnMessage(func(ctx context.Context, msg fpim.Inbound) error {
    // msg.ClientId / msg.Channel / msg.Payload
    // 业务处理：存库、敏感词、风控
    return nil
})

im.Push(clientId, payload)              // 不用管 client 在哪个 im 节点
im.PushChannel("order:123", payload)    // 推给频道所有订阅者
im.Broadcast(appId, payload)
```

client 侧（Go / TS 同构）：

```go
c := fpsdk.IM.Client(token)
c.Subscribe("device:aa:bb:cc")
c.OnMessage(func(msg) { ... })
c.Send(channel, payload)                // 上行
```

### 10.3 降级行为

fp 不可用时：

- token 校验：缓存内的请求不受影响；缓存过期后按配置 fail-open / fail-closed
- 权限：使用缓存策略继续判定
- 配置：fp 不可达期间**无法启动新进程**（已在跑的进程不受影响，值仍在内存里）——没有本地快照文件兜底，见 §6 配置中心节内推翻说明

**token 校验与权限判定不中断；配置不在此列。**

---

## 十一、管理控制台

**前后端分离开发，`go:embed` 单二进制部署**（同 Casdoor / Grafana）。

分离的理由：后端本来就要给 SDK 提供 API，管理 UI 复用同一套 API，能顺便验证 API 设计是否合理；Go 模板渲染做配置密集的后台会非常痛苦。

**页面清单（约 18 个）：**

登录 · 概览 · 应用管理（列表 + 详情：登录方式勾选 / 会话三参数 / 密钥）· 登录方式配置（每种 connector 一个动态表单）· 用户列表 · 用户详情（资料 / 凭据 / 角色 / 登录日志 / 在线设备 / 封禁 / 改密 / 踢下线）· 角色管理 · 权限点树 · 配置中心（应用 + 分区（DEFAULT/WEB）切换 / 配置表单 / 版本历史 / 回滚）· 通知供应商 · 消息模板 · 发送记录 · 实名配置 · 实名记录 · 审计日志 · 平台管理员 · 系统设置

**三个真正的难点**（其余 70% 是常规 CRUD）：

1. **动态表单**——connector 配置和配置中心的配置项都是"后端给 schema，前端渲染"。做好后加登录方式 / 加配置项都不用动前端
2. **权限树**——多选、半选、父子联动
3. **配置版本 diff**

---

## 十二、平台自身

### 12.1 fp 管理员账号

**采用独立的管理员表**，与业务 `user` 表完全隔离，登录方式只支持用户名 + 密码（后续可加 MFA）。

考虑过的另一方案是让 fp 自己也成为一个 Application（`fp-console`），管理员即该应用下的 user，靠 `user_application` 隔离——这是 Casdoor 的做法，更优雅且能 dogfooding。**不采用的原因**：它的隔离边界依赖"每个业务查询都记得加 application 过滤"，靠纪律保证的安全边界很脆弱，一处遗漏管理员就泄露到业务侧。

Keycloak 用 master realm 提供这个隔离边界，但 **fp 砍掉了多用户池，就没有这个天然边界了**——这是"不做多租户"决策的一个具体后果。管理员数量少、功能需求简单，多维护一套表换取结构性的隔离，值得。
### 12.2 其他

- **全局审计日志**——谁在什么时候改了配置 / 权限 / 用户
- 健康检查、Prometheus 指标、`log/slog` 结构化日志
- 数据库迁移（goose，`go:embed` 打包 SQL，启动时自动升级到最新版）
- 多环境部署、备份

---

## 十三、技术栈

| 层 | 选型 | 理由 |
|---|---|---|
| 语言 | Go | |
| HTTP（管理 UI） | `net/http` + `chi` | Go 1.22+ 的 ServeMux 已支持 `GET /users/{id}`，chi 只补中间件链和路由组，约 1500 行，完全兼容标准库 |
| gRPC（SDK） | `grpc-go` + `buf` + `protoc-gen-go` / `protoc-gen-go-grpc` | 双向流承载回源与推送；与 fp-im 同一套范式 |
| DB | **PostgreSQL 18** | RBAC 是重关联查询；用内置 `uuidv7()` 做主键（时间有序、索引友好），一次性解决 ObjectID / snowflake / nanoid 三套 ID 并存 |
| DB 驱动 | `pgx/v5` + `sqlc` | sqlc 从 SQL 生成类型安全 Go 代码，零 ORM 魔法。IAM 关系复杂，手写 SQL 更可控 |
| 迁移 | `goose` | 能作为库嵌进 fp 二进制，配合 `go:embed` 实现单二进制自迁移 |
| 缓存 / 会话 | `redis/go-redis/v9` | 沿用 |
| 权限 | `casbin/v2` + pgx adapter | |
| 日志 | `log/slog` | 标准库 |
| 校验 | `go-playground/validator` | 沿用 |
| SDK 契约 | protobuf（`buf` 管理） | 强类型，代码生成，多语言客户端 |
| 管理 UI 契约 | **第一阶段手写 chi handler** | UI 的 API 在早期频繁变形，OpenAPI 契约反而拖慢迭代；避免同时维护两套代码生成工具链。稳定后再补 `oapi-codegen` |
| fp-im 内部通信 | `grpc-go`（im ↔ server）+ Redis pub/sub（im ↔ im） | |
| SDK 缓存 | LRU + `golang.org/x/sync/singleflight` | 防惊群、防无界增长 |
| 前端 | **待定** | 大纲阶段不做技术选型 |

**明确抛弃**：`iris`、`github.com/basicfu/gf`、MongoDB。整体依赖数量约为现有 `core/go.mod` 的三分之一。

---

## 十四、明确不做的事

| 项 | 原因 |
|---|---|
| 多租户 / 多用户池 | 一个部署一套用户体系，要隔离就再部署一套 |
| 文件 / 对象存储 | 本轮取舍排除（是四个候选里最轻的，将来要加成本很低） |
| IM 业务层（会话模型 / 消息存储 / 已读回执 / 客服分配 / 订单绑定） | 各项目自己实现，fp-im 只转发 |
| 支付密码 | 属于资金业务 |
| 订单 / 资金 / 交易 | 业务逻辑 |
| OIDC / OAuth2 Provider | **暂不实现，但留位置**：`application` 表预留 `redirect_uris` / `grant_types` / `client_secret`；对外场景走 JWT + JWKS。将来加 OIDC 是加一层 endpoint，不是重构 |
| 迁移 3s | 3s 保持现状 |

**OIDC 的价值**（供将来决策参考）：

| 场景 | 没有 OIDC | 有 OIDC |
|---|---|---|
| Grafana / Jenkins / GitLab / Nacos 接入 | 改人家源码，或像 3s 那样给 `/grafana` 开 basic auth 后门 | 填 issuer URL + client_id/secret，5 分钟接完 |
| Python / Java / Node 项目接入 | 每种语言写一个 SDK | 各语言都有成熟 OIDC 库 |
| 合作方"用你的账号登录他们系统" | 自己发明协议 + 文档 | 标准流程，对方照着接 |
| SPA / App 登录安全性 | 自己设计自担风险 | PKCE，业界验证过 |

---

## 十五、待定事项

以下问题不阻塞方向，留待实现阶段决策：

1. **前端框架选型**（Vue 3 + Element Plus / React + Ant Design）
2. **微信登录且无手机号时的策略**：直接创建用户，还是强制走绑定手机流程
3. **账号归并冲突处理**：两个已存在的 user 因新绑定而需要合并时的策略
4. **组织树 + 数据权限是否启用**：预留结构已定，是否在首版实现待定
5. **fp-im 离线队列是否需要**：默认最多一次投递，设备控制场景可能需要有界离线队列
6. **分期实施顺序**：本文档只定目标形态。第一阶段（用户体系 + SDK 跑通）的范围与实施计划见同目录的实施计划文档

---

## 附：设计决策索引

| 决策 | 位置 | 一句话理由 |
|---|---|---|
| 不做多租户 | 1.3 | 要隔离就再部署一套，省掉每张表的租户列 |
| 保留 Application 层 | 3.1 / 4.1 | 没有它就无法实现"多平台共享用户体系" |
| 角色归属 application 而非 user | 4.1 | 否则共享用户体系时角色串台 |
| identity 用独立表 | 4.1 | 登录方式要能 UI 动态增删，内嵌结构每加一种就改 schema |
| 会话三参数 | 4.4 | 3s 缺 `max_lifetime`，token 可被无限延期 |
| opaque token 而非 JWT | 4.5 | 撤销推送是加速而非唯一防线，有 TTL 兜底 |
| fp 返回 cache_ttl 而非 expiry | 4.5.1 | SDK 不做本地过期推断，消除延期竞态 |
| 延期写降频 | 4.5.2 | fp 是共享单点，写压力是全部业务方的总和 |
| 权限点由 SDK 上报 | 5.1 | 路径从代码变成注册数据 |
| **SDK↔fp 用 gRPC 双向流** | 3.3 | 一条流统一回源与推送；连接永不空闲，规避冷连接的 3–7 倍延迟；与 fp-im 同一套范式 |
| fp 保留独立 HTTP 层给管理 UI | 3.3 | SPA 调不了 gRPC；UI 与 SDK 的 API 诉求不同，不用 grpc-gateway 强行合一 |
| 流断开时 SDK 收紧 cache_ttl | 3.3 / 10 | 推送不可用时靠缩短窗口把安全性拉回来，SSE 方案做不到 |
| im↔server 用 gRPC | 3.3 / 9.2 | 每条消息一次，高频服务间调用，需要背压 |
| fp 不依赖 fp-im | 3.2 | 避免循环依赖，保证 fp 能独立部署 |
| session 主存 Redis | 4.4 | 回源只查 Redis 不碰 PG；沿用 3s 成熟做法 |
| fp 管理员用独立表 | 12.1 | 砍掉多用户池后没有天然隔离边界，安全边界不能靠纪律保证 |
| 本地能力不做成远程调用 | 10.1 | fp 提供配置和数据，不提供每次请求的计算 |

---

## 附：第一阶段实施后的交接事项（写给计划二/三）

第一阶段实现完毕（14 个任务、55+ 提交、222 个测试）。下面这些是实施过程中确定或发现的、
**会直接影响后续阶段实现**的事实，不看会踩坑。

### 计划二（gRPC + SDK）必须遵守的契约

| 契约 | 后果 |
|---|---|
| **`cache_ttl == 0` 表示"不要缓存"，不是"未设置、用默认值"** | 会话剩余不足一毫秒时 fp 就返回 0。SDK 若当成缺省值去套配置，会缓存一个马上过期的判定 |
| **`cache_ttl` 上线时必须向下取整，不能四舍五入** | 它是 `time.Duration`，可能是亚秒。把 800ms 进位成 1s 会重新引入 `min()` 要防的过度缓存 |
| **`Rotated == true` 时必须把 `NewToken` 回传客户端** | 忘记回传的话，每个发生轮换的会话都会在过渡期（默认 30 秒）结束后被登出 |
| **轮换的 `NewToken` 只会送达并发请求中的一个** | 其余并发请求返回 `Rotated=false` 且看起来完全正常。客户端若丢掉了那一个响应（请求取消、网络中断、后台请求忽略响应体），90 天的会话会在 30 秒后无声死亡。SDK 需要处理这个交接，或考虑把过渡期做成策略项 |
| **`RevokePublisher.Subscribe` 的 ctx 与 `closeFn` 必须配对使用** | 已在 `Subscribe` 内部派生可取消子 ctx，`closeFn` 会 cancel。但仍应传可取消的 ctx |

### 已知的、留给计划二解决的问题

- **冻结/改密与登录之间仍有窄竞态。** `AuthService.Login` 在签发会话后会重查用户状态与密码哈希，
  把「静默数天的失陷」收敛成一个窄窗口，但**不是围栏**。对密码登录还有一段更窄的缝：
  connector 在 `resolveUser` 读快照**之前**校验凭据，此间的改密会让快照已持有新哈希。
  真正的解法是撤销版本号（revocation epoch）在签发时比对——属于计划二。
- **`Sender.rules` 是跨通道共用的一张表。** 只有短信通道时无害，**邮件通道上线前必须改成按通道分表**。
- **`SetConnector` 不校验 connector 类型。** `PUT .../connectors/typo` 会返回 204 并写入一条永远不会被读的行，
  运营会以为自己启用了一个不存在的登录方式。修复应放在 `ApplicationService`（构造器接 `*connector.Registry`），
  **不要放在 handler 里**——那会重建 Task 13 花了整轮预算才拆掉的模式。
- **未解析到账号的失败登录写得进、读不出。** `login_log` 刻意记录 `user_id` 为空的失败（这正是枚举探测的全部形态），
  但唯一的读路径是 `GET /users/{id}/login-logs`，按 `user_id` 过滤。需要一个带时间范围的
  `GET /admin/api/login-logs`。索引 `login_log_created_idx` 已经为它建好了。
- **`ApplicationService` 没有任何更新方法**（只有 `Create` / `UpdateSessionPolicy` / `SetConnector`）。
  控制台无法改应用名、无法编辑 `redirect_uris`。计划三若需要就得先加接口。
- **会话列表的去重逻辑在 HTTP 层。** `SessionService.ListByUser` 返回的是每个存活 token 一条，
  轮换过渡期内同一会话有两条。计划二若基于它做「我的设备」，需要自己去重，
  且要留 `IdleExpiresAt` 更大的那条（`SMEMBERS` 返回插入顺序，陈旧的那条在前）。

### 实施中确立的、与设计大纲有出入的决定

- `identity` 是所有登录标识的唯一真相源，`app_user` **不含** `phone` / `email` 列。
- 表名 `app_user`（`user` 是 PostgreSQL 保留字）。
- 第一阶段用手写 pgx 查询，未引入 sqlc。
- `identity(union_key)` 上的索引**不是** UNIQUE——同一 unionId 允许多条 identity 是归并规则 2 的前提。
  「一个 union_key 只属于一个用户」由应用层的 `lockUnionKeyTx` + 归属校验维持。
- 测试必须串行（`-p 1`，禁用 `t.Parallel()`）：所有包共用同一个 `fp_test` 库，而每次 `NewTestDB` 都会清空全表。

---

## 附：第二阶段实施后的交接事项（写给计划三）

第二阶段实现完毕（13 个任务、42 个提交、355 个测试）。gRPC 服务端与 Go SDK 已可用，
demo 的四步手工验收全部实跑通过（含"杀掉 fp 后业务不中断"）。

### 合入后应优先处理的三项

| # | 事项 | 位置 | 理由 |
|---|---|---|---|
| 1 | `Validate` 不再响应调用方 ctx 的取消与 deadline | `sdk/auth.go` | 为修 singleflight 共享调用被领导者取消拖垮的问题，改成了 `context.WithoutCancel`。代价是调用方自带的 200ms deadline 会被拉长到 `ValidateTimeout`（默认 2 秒）。**有界，但要在 doc comment 里写明**，别让接入方以为传 ctx 还能控延迟 |
| 2 | gRPC 与 HTTP 共用一个 10 秒关闭预算 | `cmd/fp/main.go` | 前者用满会让后者拿到已过期的 ctx，误报"HTTP 优雅关闭超时" |
| 3 | SDK 未导出 `WriteError(w, err)` | `sdk/` | `examples/demo/main.go` 的 `writeAuthError` 重写了一遍 503/401 分类（`SendLoginCode`/`Login` 不经过 `Middleware`）。每个接入方都会重写一遍，且可能写反 |

### 两条硬约束仍无测试守护

分层约束已由 `sdk/arch_test.go` 与 `internal/service/arch_test.go` 用 `go/parser` 守住，
但下面两条还没有：

- **依赖白名单**（本阶段只允许 grpc / protobuf / golang-lru / x/sync）
- **Redis 键统一 `fp:` 前缀**

两者违反时都会在 `go.mod` 或 diff 里显形，不属于"可静默打破"，优先级低于分层约束。

### 已知的、留给后续的设计问题

- **全局撤销事件把各应用的 token 互相摊开。** `RevokeUsers`（改密/冻结）与 `RevokeSession`
  传的都是 `AppID: uuid.Nil`，而 `revokeUserTokens` 会把该用户**跨全部应用**的 token
  收进同一条事件。结果是 A 应用改一次密码，B 应用的 SDK 会收到 A 那些会话的 token 字符串。
  token 广播时已从 Redis 删除，**不构成认证绕过**，但确是跨应用信息泄露。
  收口方式：删除前记下每个会话的 `AppID`，全局撤销按 app 切分成多条事件——
  每条仍是 `Nil` 语义（要求全体清缓存），但 `Tokens` 只带本 app 的
- **SDK 把 `Internal` / `NotFound` / `ResourceExhausted` 都归入"fp 不可达"。**
  `sdk/auth.go` 只把 `Unauthenticated`/`PermissionDenied` 判成鉴权失败，其余一律走陈旧兜底。
  后果举例：管理端删掉一个 application 后回 `NotFound`，开了 `AllowStaleOnOutage` 的接入方
  会继续用陈旧身份放行 `MaxStaleness`（默认 5 分钟）而不是立刻失败关闭。
  这个 fail-soft 方向可能就是想要的，但注释与行为不一致，**值得显式定夺一次**
- **退避复位没有"健康持续多久"的门槛。** 条件是"本次连接是否收到过 ready"。
  fp 若出现"连上→吐一次 ready→立刻断开"式抖动，SDK 会以 200ms 高频冲击一个本就不稳的
  服务端（每实例约 4.8 次/秒，有界）。补法三行："本次连接存活 < N 秒则不复位"
- **`GetActiveByAppID` 每次回源一次 PG 查询。** 刻意不缓存（缓存 `Application` 快照会让
  "停用应用"在 TTL 内形同虚设），单次约 1ms 而非 bcrypt 的 50–100ms。但它与上一条叠加时
  会放大：fp 抖动 → SDK 200ms 重连 → 每次一次 PG 查询
- **`MaxConnectionAge` 每 30 分钟触发一次全量缓存 purge。** 良性（默认 `cache_ttl` 30 秒
  远短于 30 分钟），但配了 `TokenCacheTTLSeconds = 600` 的部署会每 30 分钟吃一次回源浪潮。
  将来调大 `cache_ttl` 时要一起看
- **验收表第 16 项是配置断言而非行为验证。** `keepalive_pairing_test.go` 断言了
  `MaxConnectionAge > 0`，但"扩容后存量连接会重新分摊"这个行为需要等 30 分钟或把值做成
  可注入才能真验。**不要把它记成已被行为覆盖**
- **`Logout` 留下一个 token 存在性 oracle。** 跨应用登出返回 `Unauthenticated`，
  未知 token 返回成功——两者可区分，比 `ValidateToken`（两种情况都返回 `Unauthenticated`）
  泄露多一点。这是"不符时拒绝"与"查不到时静默成功"两条要求逻辑上无法同时满足的结果。
  利用前提是攻击者已持有该 token 字符串（高熵不可枚举），实际价值很低
- **`putIfGen` 的核对与写入之间仍有纳秒级 check-then-act 窗口。** 原窗口是整个 RPC 往返
  （毫秒到秒），现在缩小约六个数量级。完全封死需要额外加锁，会打破 `cache`
  "无需额外同步"的既有性质

### 执行过程中总结出的一条方法论

第二阶段的实施计划在执行中被修正了 13 次。这些修正里，实现者在计划里找出的真实缺陷分成四类：

- **A 类：测试存在但守不住它名字声称的性质**——`Advance(2000)` 实际是 0 毫秒、
  `TestRotationInheritsEpoch` 的目标路径结构上不可达、
  `TestShutdownCompletesWithOpenWatchStream` 被 `Stop()` 兜底掩盖、以及最要命的
  那条：为"让丢事件可观测"设计的验收测试在把 `broadcastPurge` 整个废掉后仍
  100% 通过
- **B 类：要求写了但没有任何测试**——退避复位、`put` 的 `ttl<=0` guard、`get`
  里的 `Remove`（且原注释低估了后果：`golang-lru` 的 `Get` 会把命中的键提升为
  最近使用，少了 `Remove` 会让过期条目反过来挤走新鲜条目）、`ErrUnavailable`
  →503
- **C 类：逻辑错**——Gap 检测 off-by-one（第一次真实重连一条 Gap 都不发）、
  purge-on-ready 竞态（`New()` 在首个 ready 前就返回了）、`cache_ttl` 恒等证明
  依赖一条并不存在的校验
- **D 类：简报自身写错**——测试自带竞态、"生产不能用 FakeProvider"措辞有歧义
  导致所有环境都被要求配阿里云凭据、`cache_ttl` 配 0 会被 `Validate` 拒绝、
  几处 import 块漏包

**写计划时"强调了"不等于"守住了"。** 每写一条"这是必须的"，都要同时问：
哪一条测试会因为违反它而变红？答不上来就说明那条要求还没被守住。

这也是分层硬约束最终要用 `go/parser` 写架构断言的原因——那六条约束此前全部可以被
静默打破：违反任何一条，`go build` / `go vet` / `gofmt` / 全量测试**全绿**。

---

## 附：第三阶段（管理控制台）实施后的交接事项

第三阶段共 21 个 commit，手写约 4646 行 / 66 个文件（不含 npm lock 与 shadcn 生成组件）。十个任务各自通过独立审查，触发 6 轮任务级修复；全分支终审判定「可合并，需修」，无 Critical，修复波次 4 个 commit 后复审全部核销。

### 一、本阶段**没有**完成的事（最要紧的一条）

**计划自己写的验收标准 —— Task 10 Step 7 那十步端到端人工验收，完全没做。**

原因：局域网 Postgres（10.9.1.2:15432）与 Redis（10.9.1.2:4379）从执行中途（Task 6）起就一直拒绝连接，至今未恢复。`fp` 进程起不来，全量 Go 测试 248 条因连接被拒而失败。

由此产生的具体后果，不要含糊过去：

- **控制台与后端之间的真实 HTTP 往返，本阶段从头到尾一次都没有发生过。** 所有前端测试都是 `vi.stubGlobal('fetch', ...)` 的进程内桩，所有后端路由测试都是 `httptest`。
- 「在控制台上点停用，应用真的被停用」这件事**没有任何形式的验证**。它的三条验证腿当时全断：后端测试跑不了、前端当时无测试（已在修复波次补上）、人工验收未做。
- 后端逻辑本身有**间接**证据：终审逐 commit 核实过，最后一个改动"依赖数据库的生产代码"的提交是 `96af88d`（Task 2），而 Postgres 是 Task 6 才断的——也就是说 Task 1/2 的后端改动当年是在真库上验过绿的，且此后未被触碰。这是间接证据，不是验收。

**数据库恢复后必须做的事**：走完 Task 10 Step 7 的十步，尤其第 3、4、10 步（配登录方式 / 调会话策略 / 停用应用）——正好是自动化测试最薄的三处。**跑完之前，不要对外声称第三阶段验收通过。**

### 二、留给下一阶段的待办（按优先级）

1. **三个新增的变更操作没有任何审计记录。** `ApplicationService` 的 `Update` / `SetStatus` / `SetConnector` 都不写日志。有人把生产上全部应用停用、或把某个应用的短信登录悄悄关掉，数据库里没有任何痕迹——既不知道发生过，更不知道是谁。全仓唯一的管理侧审计写入是 `service/account.go` 的 `writeRevokeLog`。
   另一半问题是基线遗留：`login_log` 表没有操作者列，`internal/httpapi/user.go` 里几处 handler 拿到 `adminIDFrom(ctx)` 却直接丢掉。补 actor 列涉及迁移。

2. **陈旧的 connector 配置键会让启停开关卡住。** Task 2 给 `SetConnector` 加了"拒绝未知配置键"的校验，但**没有配套迁移**。本阶段之前任意键都能落库（这正是 Task 2 的立项理由），所以既有部署里很可能存在带陈旧键的行。而 `ConnectorsPanel` 的启停开关会把已存配置**原样回传**——管理员点开关关闭一个登录方式会得到 400「不支持配置项 "xxx"」，而正确操作（先在下面的表单点一次保存把配置洗干净）完全不可发现。同理，未来给某个 connector 加**必填**字段也会让启停开关先坏掉。
   修法二选一：开关只提交 `enabled`（需要后端支持局部更新），或提交前用 schema 过滤一遍 config。

3. **secret 字段的脱敏与落库加密仍未实现。** `domain.Field` 那条注释已在本阶段补上"尚未实现"的说明，但行为没变：`SetConnector` 明文入 JSONB、`ListConnectors` 原样回传，前端渲染成 `type="password"` **只是视觉遮挡**。当前两个 connector 一个 secret 字段都没有，所以现在没有实际泄露。**真到加微信 AppSecret 那天，它会明文出现在 `GET /applications/{id}/connectors` 的响应里——必须先补上这条链路。**

4. **一批基线遗留的安全问题，本阶段一行都没碰，但本阶段第一次给 fp 装上浏览器界面，把其中几条从"理论问题"变成了"可被点击利用的问题"：**
   - `/admin/api/login` 没有限流（`store.RateLimiter` 是现成的）。
   - 登录响应体里回显了 token，抵消了 HttpOnly cookie 的意义。
   - `FP_ENV` 的取值不做校验，拼错会让 `IsProd()` 静默返回 false，进而让管理端 cookie 的 `Secure` 属性静默关闭。
   - 用户列表把身份手机号明文下发，而登录日志里的同一个手机号是脱敏的（`service.MaskSubject`）——同一份数据两套标准。

### 三、这份计划自身被实现者挑出的缺陷（方法论记录）

九处。每一处都是"我写计划时以为写对了、实际跑起来才发现不对"：

1. `PATCH /applications/{id}` 的 `cookieDomain` 是非指针，只发 `name` 会把它静默清空——而路由注释写着"局部更新"。
2. 测试名写错：`TestConnectorCRUD` 实际叫 `TestConnectorConfigRoundTrip`。
3. 外部测试包（`package service_test`）里漏了 `service.` 包限定符，照抄编译不过。
4. 把 8 条测试放进一个 `package httpapi` 文件，但既有测试是 `package httpapi_test`，两者互不可见。
5. `ApiError` 用了 TypeScript 构造函数参数属性，在脚手架开启的 `erasableSyntaxOnly` 下编译不过。
6. 关掉了 vitest 的 `globals`（为 TypeScript 的正确理由），却没想到 `@testing-library/react` 的自动 DOM 清理靠裸标识符探测全局 `afterEach`——于是清理失效、组件测试之间 DOM 互相污染，计划给的 3 条测试里有 2 条会**假红**。
7. `Select.onValueChange` 在当前 base-ui 下参数类型是 `string | null`，原文编译不过。
8. `CardHeader` 写了 `flex-row` 却没写 `flex`，被基类的 `grid` 压住，实际是纵向堆叠。
9. `TestEmbeddedConsoleIsPresentOrClearlyAbsent` 的 skip 条件只看 `index.html` 在不在，而注释声称"两者都没有才跳过"——"index.html 缺失但 assets 有残留"这个半吊子状态会被直接 skip 掉，**而那正是这条测试点名要抓的东西**。

### 四、方法论：第二阶段那条教训的现场复现

第二阶段记的是：

> 写计划时"强调了"不等于"守住了"。每写一条"这是必须的"，都要同时问：**哪一条测试会因为违反它而变红？**
> 补充：还要再问一句——**这条测试的前置数据里，有没有某种巧合，使得正确实现和错误实现产出同样的结果？**

第三阶段有两个案例把这条教训坐实了，都值得记住：

**案例一（Task 9）：变异测试逼出了一条假绿的测试。** 实现者给"全部下线"补测试时做变异验证——删掉 `sessions.reload()`，本该变红，结果 8/8 全绿。他没有放过，查出根因是测试辅助函数 `sessionsListCallCount` **只按 URL 过滤、不看 HTTP 方法**，而 `DELETE /users/{id}/sessions` 与拉取列表的 `GET /users/{id}/sessions` 是**同一个 URL**，那次 DELETE 被误计成了一次"列表被拉取"。
连带结论更值得记：**既有的"踢单个设备"那条测试之所以一直有效，纯属路径巧合**（`.../sessions/{sid}` 恰好不同于 `.../sessions`），不是设计上的严谨。
**没有变异测试，这条假绿会一直当成保护伞挂在那里。**

**案例二（终审）：一条被记为"当前不冲突"的 Minor，实测后升级成必修。** `DynamicForm` 的 DOM id 没加命名空间，任务级审查记的是"当前 password/sms_code 的字段 key 不冲突，推迟"。终审写了个临时测试实跑，发现后果根本不是"重复 id"：两个 connector 有同名字段时，**点 B 的开关文案会翻掉 A 的开关**，且 B 的开关无法按可访问名定位。而这恰好发生在本阶段押注的那个场景——"后端加登录方式、前端零改动"。
**教训：判断一条缺陷的严重性时，"当前不会触发"和"在这个项目的核心场景下会不会触发"是两个问题。**

第三阶段自己新增的一条实践，建议延续：**每条标注「辨别力」的测试，实现者都要做一次变异验证**——把实现改成错误版本，确认这条测试真的变红，再改回来。本阶段十个任务全部执行了这条，直接产出了案例一。

### 五、本阶段确立、后续应当延续的技术约定

- 前端工具链有六条与官方文档冲突的坑（tsconfig 不许有 `baseUrl`、shadcn 组件名必须带 `@shadcn/` 命名空间否则静默失败、shadcn v4 没有 form 组件、TanStack Table v9 是彻底重写故未引入、`go:embed` 必须用 `all:` 前缀、`defineConfig` 要从 `vitest/config` 导入）。全部记在 `docs/console.md`，改前端工程配置前先读那一节。
- `web/dist/.gitkeep` 是 `//go:embed all:dist` 能在前端未构建时通过编译的唯一依托。`web/package.json` 的 build 脚本末尾有一段专门重建它的逻辑（Vite 的 `emptyOutDir` 会清空目录），`internal/integration/console_test.go` 有一条测试用 `git ls-files` 断言它仍被版本库跟踪——**查的是索引不是磁盘**，因为危险情形恰恰是"从版本库删了但本地还在"，那时破坏者一切正常、只有下一个 clone 的人 `go build` 会炸。
- 前后端 DTO 契约靠两个后端护栏而非前端自觉：`decodeJSON` 开了 `DisallowUnknownFields`（请求方向字段名拼错 = 400 而不是静默丢弃），`validateConnectorConfig` 拒绝未知配置键。`web/src/lib/types.ts` 是手工镜像，`api.get<T>` 只是类型断言，字段名对不上不会有编译错误、只会在运行时变成 undefined。
- 破坏性操作（停用应用、冻结账号、全部下线）有二次确认；「下线单设备」刻意没有（破坏半径最小、高频重复动作）。确认框允许 Escape/点遮罩关闭——语义是"取消 = 不执行"，与 appSecret 那个"不许被误关"的弹窗刚好相反。
