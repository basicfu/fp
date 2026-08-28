# fp 第二阶段实施计划：gRPC 服务 + Go SDK

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让业务方通过一个 Go SDK 接入 fp——初始化时给 appId/appSecret，之后登录、鉴权中间件、踢下线实时生效、fp 挂掉时业务不中断，全部开箱可用。

**Architecture:** fp 进程在 `:8080` 的 HTTP（管理 UI）之外，增加 `:9090` 的 gRPC 服务，两者共用同一个 `service` 层。SDK 与 fp 之间维持**一条 gRPC 连接**：一元 RPC 承载登录与回源校验，一条双向流承载撤销推送，流的存在让连接永不空闲，回源始终走热连接。SDK 侧以 LRU+TTL 本地缓存 + singleflight 消化回源量，按 fp 下发的 `cache_ttl_ms` 决定缓存时长，自己不做任何过期推断。

**Tech Stack:** Go 1.26 / gRPC-Go v1.83.2 / protobuf v1.36.12 / buf v1.72.0（纯 Go 编译器，不需要 protoc）/ hashicorp/golang-lru v2.0.7 / golang.org/x/sync（singleflight）

---

## 一、范围

### 本计划做的

| 模块 | 内容 |
|---|---|
| **M8 gRPC 服务端** | proto 契约、代码生成管线、连接认证、一元 RPC（发码/登录/登出/校验）、撤销推送双向流、接进 `main.go` |
| **M9 Go SDK** | 连接层（keepalive/退避/流健康）、缓存层（LRU+TTL+singleflight）、`fpsdk.Auth`（校验/登录/登出）、HTTP 中间件（轮换回传/降级） |
| **交接项** | 撤销纪元与批量撤销（Task 2）、轮换交接幂等化（Task 3） |
| **验收** | 端到端集成测试 + 一个可运行的 demo 业务服务 |

### 本计划不做的

授权/casbin、配置中心、实名认证、fp-im、OIDC、MFA、微信公众号、扫码登录、邮箱验证码、管理 UI 前端（计划三）、`fpsdk.Local` 系列纯本地能力（限流/防重复提交/ID 生成/敏感词，与 fp 无关，可任何时候单独做）。

### 两个交接项为什么在这里

**① Task 2 修的是第一阶段留下的洞**，与 gRPC、SDK 都没有关系，在这里是因为交接清单把它派给了计划二。

**竞态一旦命中，产生的那个会话是永久有效的。** `SessionService.Validate` 只查「会话存在 / 应用匹配 / 未过期」，**不查用户状态**——所以一个在竞态窗口里签发出来的会话，此后每一次校验都会被放行，一直到空闲超时（C 端 7~30 天）。不是"多活一个请求"，是"多活一个月"，而管理界面上显示的是「已冻结」。

命中概率极低（要求冻结操作精确落在 `Login` 的两次读之间，那是 2~3 次 DB 往返的宽度）。**低频但后果不封顶**——所以做。

同一个任务里还有批量撤销：一次踢 500 个用户若产生 500 条广播，每条都要扇出到全部 fp 实例、每个 SDK 都要处理 500 次。两件事都动 `session.go` / `account.go`，放一起。

**② Task 3（轮换交接幂等化）不做就没法交付。** fp 当前的实现里，轮换出的新 token 只会送达并发请求中的一个，其余请求看到的是 `Rotated=false` 且一切正常。那一个响应一旦丢失（请求被取消、页面忽略响应体、网络中断），一个 90 天的会话会在过渡期（默认 30 秒）后无声死亡。这不是"SDK 小心一点"能绕开的。

---

## 二、Global Constraints

沿用第一阶段全部约束，下面重述关键项并补充本阶段新增的。

### 继承自第一阶段（未变）

- module path 是 `github.com/basicfu/fp`
- Go **≥ 1.24**。已安装工具链 1.26.0。`go.mod` 的 `go` 指令由依赖的最低要求决定，**不要为凑数字降级依赖**
- 数据库 **PostgreSQL 18**，主键 `uuid PRIMARY KEY DEFAULT uuidv7()`
- **禁止引入** `iris`、`github.com/basicfu/gf`、MongoDB
- **禁止**在 `app_user` 或 `user_application` 上添加任何 `role` 字段（设计文档 5.5 硬约束）
- **所有时间戳在 Go 侧统一用毫秒 int64**（`time.Now().UnixMilli()`），PG 侧用 `timestamptz`
- 所有对外 ID 用 `uuid.UUID`（`github.com/google/uuid`）
- 每个任务结束必须 `git commit`
- 测试从 `FP_TEST_POSTGRES_URL` / `FP_TEST_REDIS_URL` 读取连接串，**未设置时直接失败并打印指引**，不得静默跳过
- 开发与测试依赖是局域网上已就绪的实例（本机无 Docker）：PostgreSQL `10.9.1.2:15432`（18.6）、Redis `10.9.1.2:4379`（8.2.1）。库 `fp` / `fp_test` 已建。凭据在 git-ignored 的 `.env.local`，**不得提交**
- 本机**没有 `make`**。入口是 `scripts/test.sh` / `scripts/run.sh`（bash，Git Bash 下运行）
- **测试必须串行**：`scripts/test.sh` 带 `-p 1`，任何测试**不得**调用 `t.Parallel()`。原因是所有包共用同一个 `fp_test` 库，而 `testsupport.NewTestDB` 每次调用都 TRUNCATE 全表
- **推进测试时钟一律写单位**：`clk.Advance(2 * time.Second)`，**绝不要**写裸数字。
  `fakeClock.Advance` 的形参是 `time.Duration`，`Advance(2000)` 是 2000 **纳秒**，
  取整成毫秒后是 **0**——时钟根本不动。而这类测试通常还有别的检查会先命中，
  于是测试照常变绿，只是它想验证的那条路径从未被执行。第一阶段的
  `session_rotate_test.go` 全部是 `N * time.Second` 的写法，照它来

### 本阶段新增

- **`service` 包不得 import 任何传输层类型**——第一阶段的约束原文是「不得 import `httpapi`、`net/http`」，本阶段同等适用于 gRPC：`service` 包**不得 import `google.golang.org/grpc`、`sdk/gen/fp/v1` 或任何 `*.pb.go`**。领域错误到 gRPC status 的映射只能发生在 `internal/grpcapi` 里
- **proto 源文件在 `proto/fp/v1/`，生成产物在 `sdk/gen/fp/v1/`，生成产物必须提交进仓库**。理由：接入方 `go get` 时不应被要求安装 buf
- **`sdk/` 包不得 import `internal/`** 下的任何包。Go 的 internal 规则本来就会拦住外部消费者，但 `sdk/` 与 `internal/` 同属一个 module，编译器**不会**拦——必须靠纪律。SDK 只能依赖 `sdk/gen/fp/v1` 与标准库/第三方库
- **`buf lint` 使用 STANDARD 规则集，且必须零告警**。它有硬性命名要求，写 proto 前先看 Task 1 的说明
- **禁止引入 `grpc-gateway`**（设计文档已明确：管理 UI 与 SDK 的 API 诉求不同，不共用一份 proto）
- **禁止在 `sdk/` 里出现 `panic`**——它跑在业务方进程里，SDK 崩掉等于业务方崩掉。构造期的参数错误用 `error` 返回
- 新增依赖只允许这四个：`google.golang.org/grpc`、`google.golang.org/protobuf`、`github.com/hashicorp/golang-lru/v2`、`golang.org/x/sync`

---

## 三、第一阶段交接契约（实现前必读）

这些是第一阶段实现完成后确认的事实，违反其中任何一条都会产生**看起来正常、实际错误**的代码。

| # | 契约 | 违反的后果 |
|---|---|---|
| 1 | **`cache_ttl == 0` 表示"不要缓存"，不是"未设置、用默认值"** | 会话剩余不足一毫秒时 fp 就返回 0。SDK 若当成缺省值去套本地配置，会缓存一个马上就该失效的放行判定 |
| 2 | **`cache_ttl` 必须向下取整** | 它是 `time.Duration`，可能是亚秒。本计划改用**毫秒**传输（见 Task 1）把损失压到 1ms，但转换时仍必须用 `Milliseconds()`（截断），不得四舍五入 |
| 3 | **`Rotated == true` 时必须把 `NewToken` 回传客户端** | 忘记回传，每个发生轮换的会话都会在过渡期结束后被登出 |
| 4 | **轮换的 `NewToken` 只送达并发请求中的一个** | Task 3 专门修这个。修之前不要写任何依赖轮换的 SDK 代码 |
| 5 | **`RevokePublisher.Subscribe` 的 ctx 与 `closeFn` 必须配对** | 已在 `Subscribe` 内部派生可取消子 ctx，`closeFn` 会 cancel。**仍应传可取消的 ctx**。每条泄漏是一个 goroutine + 一条 Redis 连接 |
| 6 | **`domain.RevokeEvent.AppID == uuid.Nil` 表示跨全部应用的撤销** | 服务端按 appID 过滤推送时把 `Nil` 当成"某个应用"，会导致改密/冻结的撤销事件推不到任何 SDK |
| 7 | **`SessionService.ListByUser` 返回的是每个存活 token 一条，不是每个会话一条** | 轮换过渡期内同一会话有两条。做"我的设备"要自己去重，且要留 `IdleExpiresAt` 更大的那条（`SMEMBERS` 返回插入顺序，陈旧的在前） |
| 8 | **`ApplicationService.VerifySecret` 内部是 bcrypt** | 单次约 50–100ms。**每个 RPC 调一次会直接把吞吐打死**。Task 4 必须做验证结果缓存 |

---

## 四、五个设计决策

计划编写阶段发现设计大纲留白或需要修正的地方，先在这里定死，任务里直接按结论实现。

### 4.1 回源走一元 RPC，不走双向流

设计大纲 3.3 画的是「一条双向流，上行 ValidateToken，下行 RevokeEvent」。**本计划改为：一元 `ValidateToken` + 一条独立的 `Watch` 双向流，共用同一个 `grpc.ClientConn`。**

理由是大纲自己给的理由本身指向连接层，而非流层：

- 大纲论证 gRPC 的核心是 §② **「连接永不空闲」**——冷连接比热连接贵 3–7 倍，而 HTTP 连接池会空闲回收。这个收益来自 **ClientConn 保持活跃**，与请求走哪条 HTTP/2 流无关。一元 RPC 与 `Watch` 流在 grpc-go 里复用同一条 TCP/TLS 连接，各自是独立的 HTTP/2 stream。`Watch` 流长期存在 + keepalive PING，连接就是热的，一元回源自然也是热的
- 把回源塞进双向流意味着**手写请求/响应关联**：自增请求 ID、pending map、超时清理、连接断开时唤醒所有等待者。这是在用应用层重新实现 HTTP/2 已经做好的多路复用
- 一元 RPC 白得 per-request deadline、`context` 取消传播、拦截器、标准错误码。流里全部要自己造
- 省下的开销是每请求一个 HPACK 压缩后的 HEADERS 帧，几十字节。相比回源响应体本身可忽略

**这不是推翻「用 gRPC 而不是 HTTP」的决定**——那个决定成立且已落实，冷连接问题由 `Watch` 流 + keepalive 解决。改的只是该决定内部的一个实现选择。如果将来测出一元 RPC 有实际问题，改回流内多路复用不影响 proto 之外的任何代码。

### 4.2 `cache_ttl` 用毫秒传输

proto 字段是 `int64 cache_ttl_ms`，不是秒。

交接契约 2 要求向下取整，根源是 `ValidateResult.CacheTTL` 是 `time.Duration` 而下发用整数秒——`800ms` 取整成 `0`（不缓存，安全但浪费）或进位成 `1s`（**过度缓存，不安全**）。用毫秒把量化误差从 1 秒压到 1 毫秒，同时与 Global Constraints「所有时间戳统一用毫秒 int64」一致。

转换一律 `d.Milliseconds()`——它对 `time.Duration` 是整除，天然向下取整。**不得**写 `int64(d/time.Millisecond + 0.5)` 之类的四舍五入。

### 4.3 应用凭据验证必须缓存

`VerifySecret` 是 bcrypt，约 50–100ms（这是它的设计目的）。回源目标量级是数百 QPS，每次 bcrypt 直接不可用。

方案：`internal/grpcapi` 内一个进程内缓存，键是 `appID + ":" + sha256(secret)`，值是 `{expiresAt int64}`，TTL 5 分钟。首次调用走 bcrypt，之后是 map 查找。

三条硬要求：

- **缓存键里存的是 secret 的 SHA-256，不是 secret 本身。** 进程内存里不留明文凭据——堆转储、崩溃 dump、调试器都能读到 map 的键
- **只缓存成功结果，绝不缓存失败。** 理由是**内存**：缓存键的一半来自调用方，攻击者随手发一百万个不同的错误 secret 就能让这个 map 无界膨胀，把进程 OOM 掉。只缓存成功则键的数量被"真实存在的应用数"钉死——因为只有正确的 secret 才进得来
- **缓存里不放 `*domain.Application` 对象，只放"这对凭据有效"这个事实。** 应用的启用状态必须每次从 `AuthService.activeApp` 重新读——缓存一个 `Status: ACTIVE` 的快照，会让"停用应用"这个动作在 5 分钟内形同虚设

拦截器**只做认证，不做授权**：它回答"你是不是这个 appId"，不回答"这个应用现在能不能用"。后者是 `activeApp` 的唯一职责，保持单一执行点。

TTL 5 分钟是 appSecret 轮换的生效上限，可接受。

### 4.4 SDK 与生成产物的目录布局

```
proto/fp/v1/*.proto          proto 源文件（buf 模块根是 proto/）
sdk/gen/fp/v1/*.pb.go        生成产物，package fpv1
                             import path github.com/basicfu/fp/sdk/gen/fp/v1
sdk/*.go                     SDK 本体，import path github.com/basicfu/fp/sdk
internal/grpcapi/            gRPC 服务端
```

两个路径都不是随便取的，都已实测：

- **生成目录必须是 `sdk/gen`（只放生成产物），不能是 `sdk`。** `buf.gen.yaml` 的
  `clean: true` 会在生成前删掉整个 `out` 目录——`out: sdk` 会把手写的 SDK 代码一起删掉。
  实测 `out: sdk/gen` 时同目录的手写文件不受影响
- **proto 必须放在 `proto/fp/v1/`。** `buf lint` 的 STANDARD 规则集包含 `PACKAGE_DIRECTORY_MATCH`，
  要求 `package fp.v1` 的文件位于 `fp/v1/` 目录下

**本阶段是单 module**（`github.com/basicfu/fp`），SDK 只是其中的一个包。

代价是接入方 `go get github.com/basicfu/fp` 会把 fp 服务端的依赖（pgx、goose、阿里云 SDK……）拉进自己的 module 图——这恰恰是 fp 要消灭的那种痛苦。**将来必须拆成独立 module `sdk/go.mod`。**

之所以现在不拆：拆分是纯 `go.mod` 层面的改动，**import path 一个字都不用变**（`github.com/basicfu/fp/sdk` 在两种布局下完全相同），所以推迟没有任何兼容代价。而现在拆要么引入 `go.work`（`go test ./...` 不再覆盖两个 module，`scripts/test.sh` 得改）、要么引入 `replace` 指令，都是在本阶段目标（跑通）之外的复杂度。

**关键是生成产物从第一天就放在 `sdk/` 之下**，不能放仓库根的 `gen/fp/v1/`——后者在拆分时 import path 会从 `github.com/basicfu/fp/gen/fp/v1` 变成 `github.com/basicfu/fp/sdk/gen/fp/v1`，那才是真正的破坏性变更。

### 4.5 多实例部署下的推送路径

fp 会部署多个实例挂在 LB 后面。SDK 的 `Watch` 流只落在**其中一台**上，而触发撤销的管理操作会落在**另一台**上。这两台之间没有任何直连。

**靠 Redis pub/sub 桥接**（第一阶段已实现，见 `internal/store/revoke.go`）：

```
管理员踢下线 → LB → fp 实例 #5
                        ↓
        ① 删除 Redis 里的 session          ← 权威撤销，此刻 token 已经失效
        ② PUBLISH fp:revoke <事件>          ← 广播，只负责"加速通知"
                        ↓
        ┌───────────────┼───────────────┐
     实例 #1         实例 #2          实例 #5     ← 每个实例进程内一份 SUBSCRIBE
                        ↓
              SDK 的 Watch 流恰好在这台
                        ↓
                 SDK 清掉本地缓存
```

**这套之所以成立，前提是 fp 对会话完全无状态**：session 在 Redis、用户在 PG，`ValidateToken` 只读共享 Redis，撤销的权威动作也是删共享 Redis。因此：

- SDK 连**哪一台**都得到相同答案——**不需要连接亲和，不需要一致性哈希**，LB 随便分
- 撤销在**哪一台**触发都一样——删的是共享状态，广播是给所有实例的
- 某台实例挂掉，SDK 重连到别的实例后行为完全一致

**Task 6 的 `RevokeHub` 就是每个实例的那一份订阅**：一个进程一份 Redis 订阅，扇出给本进程持有的全部 `Watch` 流。绝不能做成"每条流一份订阅"——那会让 Redis 连接数等于全平台 SDK 实例数。

#### 丢事件本身有兜底，但"丢得无声无息"不行

**Redis pub/sub 是 fire-and-forget，没有缓冲、没有重放。** 某个 fp 实例的订阅连接在抖动中重连时，那一瞬间发布的事件对这台实例是**永久丢失**的。

丢事件本身是可接受的——推送只是把撤销延迟从 `cache_ttl` 压到近乎实时的**加速手段**，权威撤销早已通过删除 Redis 会话完成。丢一条的后果是那个 SDK 最长 `cache_ttl` 之后回源被拒，正好退化成没有推送时的行为。

**真正不可接受的是它不可观测。** go-redis 内部自动重连并重发 `SUBSCRIBE`，既不关闭 channel 也不返回错误：`RevokeHub.Run` 一无所知，SDK 那边看到的流也一直健康——**所有人都以为推送在正常工作。** 一个有兜底的故障不要紧，一个查不出来的故障要紧。

所以 Task 6 做了一件小事：把 `Channel()` 换成 `ChannelWithSubscriptions()`，数 `SUBSCRIBE` 确认回包。**第一条是初次订阅，第二条起必然是重连**，也就意味着中间有缺口。检测到就给本实例下所有 SDK 广播一条 `WatchPurge`，让它们清空缓存。

这是**悲观判断**：重连时未必真丢了东西，但 fp 无从分辨，只能按最坏情况处理。代价是一波回源，而且只有真正被使用的 token 才回源。

于是撤销的实际保证变成三段：

| | 机制 | 延迟 |
|---|---|---|
| 正常 | Redis 广播 → 流推送 → SDK 清对应条目 | 毫秒级 |
| **fp 的订阅抖动** | **检测到重订阅 → 广播 Purge → SDK 清全部** | **毫秒级（新增）** |
| SDK↔fp 流断开 | SDK 重连后自行 purge（Task 10） | 重连即恢复 |
| 兜底 | SDK 缓存到期回源 → fp 查 Redis 查不到 → 拒 | ≤ `cache_ttl`（流断开时收紧到 `DegradedCacheTTL`） |

##### 为什么不上 Redis Stream

Stream + 每实例一个消费组能**精准补齐**丢掉的那几条事件，而不是粗暴清空。但衡量下来不划算：

- 它要解决的窗口**本来就被 `cache_ttl` 兜住了**，且触发条件是月级的 Redis 故障转移
- 消费组的唯一额外收益是"跨进程重启的 offset 持久化"，而这里价值为零——fp 进程重启时它持有的 SDK 流全断了，SDK 重连后按 Task 10 会 purge，重放那段历史没有任何意义
- 消费组要求清理死实例遗留的组，而"哪些实例死了"就是**成员管理**——正是选择 Redis 而非 gRPC mesh 时刻意避开的东西
- 代价是新增 `MAXLEN` 调参、`XINFO` 间隙检测、启动顺序约束（`$` 必须在服务开始接受连接前解析）、以及滚动发布期间的双写迁移

重订阅检测关闭的是同一个洞（静默丢失），约 30 行，不新增任何运维项。**两者的差别只在恢复精度：精准补齐 vs 全量清空。** 对一个月级发生的事件，粗暴恢复完全够用。

**什么情况下该改回 Stream：** 撤销频率涨到"一次全量 Purge 的回源浪潮会打疼 fp"的量级（比如常态每秒几十次撤销）。按现在约 0.1 次/秒的量级，差得远。

#### 对 LB 的三条要求

gRPC 是长连接 + HTTP/2 多路复用，套在 LB 后面有几个坑：

1. **LB 的空闲超时必须大于 SDK 的 keepalive `Time`（30 秒）。** 云 LB 普遍默认 60 秒空闲超时——`Watch` 长流加 keepalive PING 本来就不会空闲，这条通常自动满足，但配置里把超时调到 30 秒以下就会周期性掐连接
2. **L4 LB 会把一个 SDK 实例的全部流量钉在一台 fp 上**（所有 RPC 复用同一条连接）。这对正确性无影响，但负载是**按连接**而非按请求分摊的——SDK 实例少的时候会明显不均
3. **扩容 fp 不会自动重新分摊存量连接**，见下面的 `MaxConnectionAge`

用 L7（gRPC-aware）LB 能按请求分摊，但它必须能正确处理长流。本阶段按 L4 设计，够用。

---

## 五、文件结构

### 新建

| 文件 | 职责 |
|---|---|
| `buf.yaml` | buf 模块与 lint 配置 |
| `buf.gen.yaml` | 代码生成配置 |
| `scripts/gen.sh` | 安装插件 + `buf lint` + `buf generate` |
| `proto/fp/v1/common.proto` | 共享消息（撤销事件、错误细节） |
| `proto/fp/v1/auth.proto` | `AuthService`：发码/登录/登出/校验/Watch |
| `sdk/gen/fp/v1/*.pb.go` | 生成产物（提交进仓库，**不手改**） |
| `internal/grpcapi/server.go` | gRPC server 装配与生命周期 |
| `internal/grpcapi/auth_interceptor.go` | appId/appSecret 认证拦截器 + 凭据缓存 |
| `internal/grpcapi/errors.go` | `domain` 错误 → gRPC status 映射 |
| `internal/grpcapi/auth_service.go` | `AuthService` 一元 RPC 实现 |
| `internal/grpcapi/watch.go` | 撤销事件中继（一份 Redis 订阅扇出到 N 条流） |
| `sdk/client.go` | 连接层：dial、keepalive、退避、流健康状态 |
| `sdk/cache.go` | LRU + TTL 缓存 |
| `sdk/auth.go` | `fpsdk.Auth`：校验、登录、登出 |
| `sdk/middleware.go` | `net/http` 中间件：取 token、轮换回传、降级 |
| `sdk/options.go` | 配置项与默认值 |
| `examples/demo/main.go` | 可运行的示例业务服务 |

### 修改

| 文件 | 改动 |
|---|---|
| `internal/domain/session.go` | 加 `Session.Epoch`（Task 2）；加 `RotatedTo`（Task 3） |
| `internal/store/session.go` | 加轮换映射的读写（Task 3） |
| `internal/store/epoch.go` | **新建**：纪元读写（Task 2） |
| `internal/service/session.go` | `Issue`/`Validate` 接入纪元与轮换映射 |
| `internal/service/account.go` | 冻结/改密时递增纪元 |
| `cmd/fp/main.go` | 启动 gRPC server，与 HTTP 并存，一起优雅关闭 |
| `scripts/test.sh` | 无需改动（`./...` 已覆盖新包） |
| `.gitignore` | 无需改动 |

---

## Task 1: proto 契约与代码生成管线

建立 proto 源文件、buf 配置、生成脚本，并把生成产物提交进仓库。本任务不写任何业务逻辑。

**buf lint STANDARD 的硬性要求**（已实测，违反就无法通过 `buf lint`）：

- service 名必须以 `Service` 结尾——`AuthService` 可以，`Auth` 不行
- 每个 RPC 的请求/响应消息必须命名为 `<Rpc>Request` / `<Rpc>Response`
- **同一个消息不能同时作为多个 RPC 的请求或响应**——哪怕两个 RPC 的入参完全一样，也要各建一个消息
- `package fp.v1` 的文件必须位于 `fp/v1/` 目录下
- 每个文件必须有 `option go_package`
- 枚举零值必须以 `_UNSPECIFIED` 结尾，枚举值必须以枚举名的大写蛇形为前缀

**Files:**
- Create: `buf.yaml`, `buf.gen.yaml`, `scripts/gen.sh`
- Create: `proto/fp/v1/common.proto`, `proto/fp/v1/auth.proto`
- Generate: `sdk/gen/fp/v1/common.pb.go`, `sdk/gen/fp/v1/auth.pb.go`, `sdk/gen/fp/v1/auth_grpc.pb.go`
- Test: `sdk/contract_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: 无
- Produces: `fpv1.AuthServiceClient` / `fpv1.AuthServiceServer` 及全部消息类型，import path `github.com/basicfu/fp/sdk/gen/fp/v1`，包名 `fpv1`

---

- [ ] **Step 1: 写 buf 配置**

`buf.yaml`：

```yaml
version: v2
modules:
  - path: proto
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
```

`buf.gen.yaml`：

```yaml
version: v2
# clean 会在生成前删掉下面每个 out 目录。out 必须是只存放生成产物的目录，
# 绝不能指向 sdk/——那会把手写的 SDK 代码一起删掉。
clean: true
plugins:
  - local: protoc-gen-go
    out: sdk/gen
    opt: paths=source_relative
  - local: protoc-gen-go-grpc
    out: sdk/gen
    opt: paths=source_relative
```

- [ ] **Step 2: 写生成脚本**

`scripts/gen.sh`（记得 `git update-index --chmod=+x scripts/gen.sh`，与第一阶段其他脚本一致）：

```bash
#!/usr/bin/env bash
# 生成 protobuf / gRPC 代码。
#
# 产物提交进仓库：接入方 go get 之后应该直接能编译，不该被要求装 buf。
# 本机没有也不需要 protoc——buf 自带纯 Go 的 protobuf 编译器。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GOBIN="$(go env GOPATH)/bin"
export PATH="$GOBIN:$PATH"

# 与 go.mod 里的运行时依赖对齐，升级时两处一起改。
BUF_VERSION=v1.72.0
PROTOC_GEN_GO_VERSION=v1.36.12
PROTOC_GEN_GO_GRPC_VERSION=v1.6.2

# 只检查存在性，不检查版本：已装旧版时请自行 go install 覆盖。
ensure() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "安装 $1 ..."
    GOBIN="$GOBIN" go install "$2"
  fi
}

ensure buf                "github.com/bufbuild/buf/cmd/buf@${BUF_VERSION}"
ensure protoc-gen-go      "google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}"
ensure protoc-gen-go-grpc "google.golang.org/grpc/cmd/protoc-gen-go-grpc@${PROTOC_GEN_GO_GRPC_VERSION}"

buf lint
buf generate

# 生成产物本应已经是 gofmt 干净的；不干净说明插件版本不对。
unformatted="$(gofmt -l sdk/gen)"
if [ -n "$unformatted" ]; then
  echo "生成产物未通过 gofmt：$unformatted" >&2
  exit 1
fi

echo "生成完成。"
```

- [ ] **Step 3: 写 common.proto**

`proto/fp/v1/common.proto`：

```protobuf
syntax = "proto3";

package fp.v1;

option go_package = "github.com/basicfu/fp/sdk/gen/fp/v1;fpv1";

// RevokeEvent 是一次撤销的广播，对应服务端的 domain.RevokeEvent。
//
// 必须携带具体的 token 列表而不能只给 user_id：SDK 是按 token 缓存校验结果的，
// 只给 user_id 的话 SDK 无从知道该清哪些缓存条目。
message RevokeEvent {
  // tokens 是本次被撤销的全部 token，**可能跨多个用户**。
  //
  // SDK 只需要这一个字段：它的缓存按 token 键控，收到事件就是
  // cache.drop(tokens...)。下面的 user_ids 只用于日志。
  //
  // 单条事件的 token 数有上限（服务端 maxTokensPerEvent，1000）。
  // 超出的会被切成多条——一条携带三万个 token 的事件约 1 MB，
  // 广播给 20 个实例就是 20 MB，SDK 侧还要吃一个逼近 gRPC 默认
  // 4 MB 接收上限的消息。
  repeated string tokens = 1;
  // user_ids 是这些 token 所属的用户，**仅用于日志与排障**。
  //
  // 是列表而非单值：批量撤销（一次踢 500 个用户）合并成一条事件，
  // 否则 500 次操作会产生 500 条广播、每条都要扇出到全部实例。
  repeated string user_ids = 2;
  // app_id 为空串表示跨全部应用的撤销（改密、冻结）。
  // 对应服务端的 uuid.Nil，不要当成"某个应用"来过滤。
  //
  // 同一条事件里的所有 token 共享这一个 app_id——批量撤销只在
  // 「相同 reason + 相同 app 范围」内合并，混合范围会被拆成多条。
  string app_id = 3;
  // reason 是撤销原因，取值见服务端 domain.RevokeReason*。
  //
  // 刻意用 string 而不是 enum：SDK 拿到 reason 只用于日志与可观测，
  // 处理动作与原因无关（一律清缓存）。用 enum 的话，服务端将来新增一种原因，
  // 旧 SDK 会把它解码成 UNSPECIFIED，日志里丢掉真实信息却换不来任何好处。
  string reason = 4;
  // at_ms 是撤销发生的毫秒时间戳。
  int64 at_ms = 5;
}
```

- [ ] **Step 4: 写 auth.proto**

`proto/fp/v1/auth.proto`：

```protobuf
syntax = "proto3";

package fp.v1;

import "fp/v1/common.proto";

option go_package = "github.com/basicfu/fp/sdk/gen/fp/v1;fpv1";

// AuthService 是 SDK 与 fp 之间的全部认证契约。
//
// 认证方式：每个 RPC 的 metadata 必须带 fp-app-id 与 fp-app-secret，
// 由服务端拦截器校验（见 internal/grpcapi/auth_interceptor.go）。
service AuthService {
  // SendLoginCode 给手机号发送登录验证码。
  rpc SendLoginCode(SendLoginCodeRequest) returns (SendLoginCodeResponse);

  // Login 用某种登录方式的凭据换取会话 token。
  rpc Login(LoginRequest) returns (LoginResponse);

  // Logout 撤销一个 token。
  rpc Logout(LogoutRequest) returns (LogoutResponse);

  // ValidateToken 是 SDK 回源校验的入口。
  rpc ValidateToken(ValidateTokenRequest) returns (ValidateTokenResponse);

  // Watch 是撤销事件的推送流。
  //
  // 它同时承担第二个职责：让这条 gRPC 连接**永不空闲**。
  // 一元 RPC 与本流复用同一条 TCP/TLS 连接，流长期存在意味着
  // 回源永远走热连接，不会付 2–5ms 的冷连接代价。
  rpc Watch(stream WatchRequest) returns (stream WatchResponse);
}

// SendLoginCodeRequest 是一次发码请求。
message SendLoginCodeRequest {
  // phone 是接收验证码的手机号。
  string phone = 1;
}

// SendLoginCodeResponse 是发码结果。成功即空响应，失败走 gRPC status。
message SendLoginCodeResponse {}

// LoginRequest 是一次登录请求。
message LoginRequest {
  // connector_type 是登录方式，例如 "password" / "sms_code"。
  string connector_type = 1;
  // credentials 是该登录方式所需的凭据，键名由各 connector 定义。
  map<string, string> credentials = 2;
  // ip / user_agent 写入登录审计，由业务方从自己的请求里取。
  string ip = 3;
  string user_agent = 4;
  // mobile 决定适用哪一档空闲超时。
  bool mobile = 5;
}

// LoginResponse 是登录成功的结果。
message LoginResponse {
  // token 是签发的 opaque token。
  string token = 1;
  // user 是登录用户的基本资料。
  UserInfo user = 2;
  // session_id 是会话标识，token 轮换时不变。
  string session_id = 3;
}

// UserInfo 是用户的基本资料。
//
// 刻意不含手机号/邮箱：登录标识全部落在 identity 上，
// 需要时通过独立接口按 user_id 查询，不在每次登录响应里携带。
message UserInfo {
  string id = 1;
  string nickname = 2;
  string avatar_url = 3;
  string gender = 4;
  // status 取值见服务端 domain.UserStatus*。
  string status = 5;
}

// LogoutRequest 是一次登出请求。
message LogoutRequest {
  string token = 1;
}

// LogoutResponse 是登出结果。重复登出也返回成功。
message LogoutResponse {}

// ValidateTokenRequest 是一次回源校验。
message ValidateTokenRequest {
  string token = 1;
}

// ValidateTokenResponse 是校验结果。
//
// 刻意不返回过期时刻：SDK 永远不判断 token 是否过期，只遵守 cache_ttl_ms
// （设计文档 4.5.1）。返回 expiry 会让"一处延期、另一处按旧 expiry 误判"
// 的竞态重新出现。
message ValidateTokenResponse {
  string user_id = 1;
  // session_id 是会话标识，token 轮换时不变。撤销以它为单位。
  string session_id = 2;

  // cache_ttl_ms 是 SDK 可以缓存本次判定的毫秒数，
  // 等于 min(应用配置的 token_cache_ttl, token 剩余有效期)。
  //
  // 0 表示**不要缓存**，不是"未设置、用默认值"。会话剩余不足一毫秒时
  // 服务端就会算出 0。proto3 的隐式存在性让"缺省"与"显式 0"在线上不可区分，
  // 这恰好使"不缓存"成为唯一可表达的行为——任何"没给就套本地默认值"的
  // 实现都是在无中生有。
  //
  // 用毫秒而非秒：它在服务端是 time.Duration，可能是亚秒。用秒会把 800ms
  // 进位成 1s，重新引入 min() 本来要防的过度缓存。
  int64 cache_ttl_ms = 3;

  // rotated 为 true 时 new_token 非空，**必须**把它回传给客户端。
  //
  // 不回传的后果：该会话在过渡期（默认 30 秒）结束后被登出，
  // 哪怕它的 max_lifetime 还有 90 天。
  bool rotated = 4;
  string new_token = 5;
}

// WatchRequest 是建流请求。
//
// 目前无字段：应用身份来自 metadata，不在消息体里重复。
// 保留消息本身是为了将来能在同一条流上订阅更多事件类型。
message WatchRequest {}

// WatchResponse 是一条推送事件。
//
// 用 oneof 是为了将来加 ConfigChanged / PolicyChanged 时不破坏兼容：
// 旧 SDK 遇到不认识的分支会落到 default，忽略即可。
message WatchResponse {
  oneof event {
    // revoke 是一次撤销。
    RevokeEvent revoke = 1;
    // ready 表示服务端已完成订阅，此后的撤销不会漏推。
    WatchReady ready = 2;
    // purge 要求 SDK 丢弃全部缓存。
    WatchPurge purge = 3;
  }
}

// WatchReady 表示服务端已就绪。
//
// 它存在的意义：SDK 只有收到 ready 才能把"推送通道健康"置为 true。
// 光靠"建流没报错"是不够的——流建立了但服务端还没订上 Redis 的那段时间里，
// 撤销事件会丢，而 SDK 却以为推送可用，不会收紧缓存窗口。
message WatchReady {}

// WatchPurge 要求 SDK 丢弃**全部**缓存条目。
//
// fp 在确知自己漏读了撤销事件、却不知道漏了哪些时发出。目前唯一的触发源是
// Redis 订阅重建（见 internal/store/revoke.go 的 RevokeSignalGap）：go-redis
// 会静默重连并重发 SUBSCRIBE，那个窗口里发布的事件对本实例永久丢失。
//
// **这是悲观判断，不是确知。** 重建时未必真的丢了东西——那个窗口里可能压根
// 没人发布过撤销。但 fp 无从分辨，只能按最坏情况处理。不要试图把它"优化"成
// 条件触发：能让它变成条件的那个信息并不存在。
message WatchPurge {
  // reason 只用于日志与排障，SDK 的处理动作与它无关。
  string reason = 1;
}
```

- [ ] **Step 5: 引入依赖并生成**

```bash
go get google.golang.org/grpc@v1.83.2
```

```bash
go get google.golang.org/protobuf@v1.36.12
```

```bash
./scripts/gen.sh
```

`buf lint` 必须零输出。有输出就是命名违反了 STANDARD，按本任务开头的清单改 proto，**不要改 lint 配置**。

- [ ] **Step 6: 写契约测试**

测试放 `sdk/contract_test.go`，**不能**放在 `sdk/gen/` 下——`buf.gen.yaml` 的 `clean: true` 会在每次生成时删掉整个 `sdk/gen` 目录，手写测试放进去会被无声删除。

同时创建 `sdk/doc.go`。只有测试文件、没有任何非测试文件的目录，`go test` 无法确定包名。

**包注释只放这一个文件里**——一个包写两份包注释是 lint 问题，而后续任务还会往 `sdk/` 加好几个文件：

```go
// Package fpsdk 是 fp 的 Go 接入 SDK。
//
// 最小用法：
//
//	client, err := fpsdk.New(fpsdk.Options{
//	    Addr:      "fp.internal:9090",
//	    AppID:     os.Getenv("FP_APP_ID"),
//	    AppSecret: os.Getenv("FP_APP_SECRET"),
//	})
//	defer client.Close()
//	http.Handle("/api/", client.Auth().Middleware(apiHandler))
//
// SDK 跑在业务方进程里，**任何一处 panic 都等于业务方崩溃**，
// 因此本包内不使用 panic，参数错误一律经 error 返回。
//
// 本包不得 import internal/ 下的任何包。Go 的 internal 规则不会拦住
// 同 module 内的引用，编译能过——这条只能靠纪律和评审维持。
package fpsdk
```

> 上面的示例里 `New` / `Auth()` / `Middleware` 要到 Task 8、10、11 才存在。
> 包注释是文档，不参与编译，先写完整版即可。

`sdk/contract_test.go`：

```go
package fpsdk

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// TestCacheTTLSurvives90Days 守住 cache_ttl_ms 的位宽。
//
// 90 天 = 7,776,000,000 毫秒，远超 int32 的上限 2,147,483,647。
// 谁把这个字段改成 int32，这里立刻失败——而线上的表现会隐蔽得多：
// 长会话的 cache_ttl 溢出成负数，SDK 要么永不缓存（回源量暴涨），
// 要么按负 TTL 算出一个已过期的条目，看起来"缓存没生效"却查不出原因。
func TestCacheTTLSurvives90Days(t *testing.T) {
	const ninetyDaysMs int64 = 90 * 24 * 60 * 60 * 1000

	raw, err := proto.Marshal(&fpv1.ValidateTokenResponse{CacheTtlMs: ninetyDaysMs})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out fpv1.ValidateTokenResponse
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := out.GetCacheTtlMs(); got != ninetyDaysMs {
		t.Fatalf("cache_ttl_ms 往返后为 %d，期望 %d", got, ninetyDaysMs)
	}
}

// TestZeroCacheTTLIsIndistinguishableFromAbsent 钉住交接契约 1。
//
// cache_ttl_ms == 0 的语义是"不要缓存"。proto3 的隐式存在性让显式的 0
// 根本不上线，接收端拿到的就是零值——也就是说"0"与"没给"在协议层面
// 无法区分。这不是缺陷，是保护：它让"没给就套本地默认值"这种实现
// 从一开始就无法写对，只能收敛到唯一安全的解释——不缓存。
//
// 本测试固定这个性质。谁把字段改成 optional（显式存在性）或包一层
// wrapper message，这里就会失败，届时必须同步审查 SDK 缓存层的 0 值处理。
func TestZeroCacheTTLIsIndistinguishableFromAbsent(t *testing.T) {
	explicitZero, err := proto.Marshal(&fpv1.ValidateTokenResponse{CacheTtlMs: 0})
	if err != nil {
		t.Fatalf("marshal explicit: %v", err)
	}
	absent, err := proto.Marshal(&fpv1.ValidateTokenResponse{})
	if err != nil {
		t.Fatalf("marshal absent: %v", err)
	}
	if len(explicitZero) != len(absent) {
		t.Fatalf("显式 0 编码 %d 字节，缺省编码 %d 字节——两者已可区分，"+
			"请同步审查 SDK 缓存层对 0 的处理", len(explicitZero), len(absent))
	}
	var out fpv1.ValidateTokenResponse
	if err := proto.Unmarshal(explicitZero, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetCacheTtlMs() != 0 {
		t.Fatalf("显式 0 解码后为 %d", out.GetCacheTtlMs())
	}
}

// TestAuthServiceSurface 钉住 AuthService 的 RPC 清单与流式属性。
//
// 少一个 RPC 会让 SDK 编译失败，那种错误不需要测试来发现。这里真正守的是
// **流式属性**：把 Watch 从双向流改成一元 RPC（或反过来）在 proto 里只是
// 两个关键字的差别，生成的 Go 代码却完全不同，而"连接永不空闲"这个
// 整套延迟论证的前提正建立在 Watch 是长流之上。
func TestAuthServiceSurface(t *testing.T) {
	svc := fpv1.File_fp_v1_auth_proto.Services().ByName("AuthService")
	if svc == nil {
		t.Fatal("auth.proto 里找不到 AuthService")
	}

	type want struct {
		clientStream bool
		serverStream bool
	}
	expected := map[protoreflect.Name]want{
		"SendLoginCode": {false, false},
		"Login":         {false, false},
		"Logout":        {false, false},
		"ValidateToken": {false, false},
		"Watch":         {true, true},
	}

	methods := svc.Methods()
	if methods.Len() != len(expected) {
		t.Fatalf("AuthService 有 %d 个 RPC，期望 %d 个——"+
			"增删 RPC 请同步更新本测试与计划文档", methods.Len(), len(expected))
	}
	for name, w := range expected {
		m := methods.ByName(name)
		if m == nil {
			t.Errorf("缺少 RPC %s", name)
			continue
		}
		if m.IsStreamingClient() != w.clientStream || m.IsStreamingServer() != w.serverStream {
			t.Errorf("RPC %s 的流式属性为 (client=%v, server=%v)，期望 (client=%v, server=%v)",
				name, m.IsStreamingClient(), m.IsStreamingServer(), w.clientStream, w.serverStream)
		}
	}
}

// TestRevokeEventCarriesTokens 钉住撤销事件必须携带 token 列表。
//
// SDK 的缓存是按 token 键控的。谁把 tokens 删掉只留 user_id，
// SDK 就无从知道该清哪些条目——退化的结果不是报错，而是撤销静默失效，
// 被踢的用户在整个 cache_ttl 内继续畅通。
func TestRevokeEventCarriesTokens(t *testing.T) {
	f := fpv1.File_fp_v1_common_proto.Messages().ByName("RevokeEvent").Fields().ByName("tokens")
	if f == nil {
		t.Fatal("RevokeEvent 缺少 tokens 字段")
	}
	if !f.IsList() || f.Kind() != protoreflect.StringKind {
		t.Fatalf("RevokeEvent.tokens 是 %v（list=%v），期望 repeated string", f.Kind(), f.IsList())
	}
}
```

- [ ] **Step 7: 跑测试**

```bash
./scripts/test.sh ./sdk/...
```

期望全部 PASS。

- [ ] **Step 8: 提交**

```bash
git add buf.yaml buf.gen.yaml scripts/gen.sh proto sdk go.mod go.sum
```

```bash
git commit -m "feat(proto): AuthService 契约与 buf 代码生成管线"
```

---

## Task 2: 撤销纪元与批量撤销

两件事都动 `session.go` / `account.go`，放一起做。

**为什么做纪元：** 竞态命中产生的会话是**永久有效**的。`Validate` 只查「会话存在 / 应用匹配 / 未过期」，**不查用户状态**——所以那个会话此后每次校验都被放行，直到空闲超时（C 端 7~30 天）。概率极低，后果不封顶：一个显示为「已冻结」的账号可能还揣着一个能用一个月的会话。

纪元的本质是**用 Redis 上一个整数，代替「每次校验都去读一遍用户表」**。后者是一次 PG 往返，前者便宜一个数量级，而热路径每秒要跑几百次。

**为什么做批量：** 一次踢 500 个用户，若产生 500 条广播，每条都要扇出到全部 fp 实例、每个 SDK 都要处理 500 次。合并成一条（token 列表）之后是 1 条。

> **批量必须是唯一实现，不能是"预留接口"。** 单用户路径要调批量方法、传一个元素的切片。
> 否则批量分支没有任何生产者，就是死代码——第一阶段的 `Application.Status`、
> `LoginEventRotate`、`DefaultSMSRateRules` 全是这么来的：字段有、常量有、
> 测试也断言了，就是没人读。让 n=1 走同一条代码路径，现有每一条测试都在跑它。

### 问题一：签发竞态

`AuthService.Login` 的顺序是：读用户 → 判 `CanLogin()` → 若干次 DB 往返 → `Issue` 签发 → `recheckLoginable` 重查状态。

管理员在「判 `CanLogin()`」与「`Issue`」之间冻结该用户时，冻结的会话清扫扫不到还没签发的会话，于是一个已被冻结的用户拿到了有效会话。第一阶段加的 `recheckLoginable` 把窗口收窄了，但**不是围栏**：冻结的状态写入若发生在 recheck 的读之后，仍然漏。

### 方案

给每个用户维护一个单调递增的**纪元**。签发时把当时的纪元刻进会话，校验时比对当前纪元——不一致即拒。

### 为什么纪元 + recheck 合起来才是围栏

单看纪元并不够。设冻结的三步是 `F1` 状态落库 → `F2` 递增纪元 → `F3` 清扫会话；登录的关键两步是 `L1` 读用户判 `CanLogin` → `L3` 读纪元 → `L4` 写会话 → `L5` recheck 重读状态。

| 情形 | 谁拦住 |
|---|---|
| `F2` 发生在 `L3` 之后 | **纪元**：会话刻的是旧纪元，校验时不匹配 |
| `F2` 发生在 `L3` 之前 | 那么 `F1` 也在 `L3` 之前，更在 `L5` 之前 → **recheck** 读到 FROZEN，撤销 |
| 冻结整体在登录之后 | `F3` 正常清扫 |

**这个论证依赖 `F1` 严格早于 `F2`。** 所以 `AccountService` 里的顺序是死的：先写状态、再递增纪元、最后清扫。谁把递增挪到状态写入之前，围栏就破了一个洞，而所有测试仍然是绿的。

### 问题二：逐用户广播

`revokeMatching` 目前一次只处理一个用户，发一条事件。一次踢 500 个用户 = 500 条广播 × 每条扇出到全部 fp 实例 × 每个 SDK 处理 500 次。

改成一条事件携带多个用户的 token。**SDK 侧一行都不用改**——它的缓存按 token 键控，`onRevoke` 做的是 `cache.drop(ev.GetTokens()...)`，列表长一点而已。`UserIDs` 变成列表纯粹是为了日志。

**两条硬约束：**

1. **单条事件的 token 数必须有上限。** 一万个用户 × 每人 3 个会话 = 三万个 token ≈ 1 MB JSON，广播给 20 个实例就是 20 MB，SDK 侧还要吃一个逼近 gRPC 默认 4 MB 接收上限的消息。超出上限就切成多条
2. **合并只在「相同 reason + 相同 app 范围」内进行。** 一条事件只有一个 `AppID` 字段；把跨应用撤销（`uuid.Nil`）和限定应用的撤销混进同一条，必然有一半的过滤是错的

### 纪元该在哪些地方递增

每加一个递增点，先问一句：**这个动作是否意味着「已经签发出去的会话不该继续有效」？** 不是所有账号变更都该递增——改昵称、换头像就不该。

| 动作 | 递增？ | 理由 |
|---|---|---|
| 冻结 / 注销（`SetStatus` 到不可登录态） | ✅ | 账号本身不该再有活动会话 |
| 重置密码 | ✅ | "密码泄露了赶紧改"若不作废旧会话，等于什么都没做 |
| 踢下线全部设备 | ✅ | 常是"怀疑失陷"的应急动作，要连正在签发路上的那个一起拦 |
| **踢下线单台设备** | ❌ | 纪元是**用户级**的，递增会把手机、电脑、平板一起踢掉 |
| 改昵称 / 头像 / 性别 | ❌ | 与凭据和账号有效性无关 |
| 绑定新的登录方式 | ❌ | 是增加能力，不是削减 |

**还没有、但将来有了必须递增的：**

- **用户自助改密**（第一阶段只有管理员重置，没有自助入口）
- **解绑登录标识**——仅当解绑动机是"这个手机号/微信被盗了"。单纯换绑不必

**将来可能值得做、但现在不要建的：**

- **全局纪元**（`fp:epoch:global`，用于安全事件后强制全员重新登录）。机制上就是多比一个整数，五行代码。**但现在没有任何触发者，建了就是死代码**——等真有"全员下线"这个需求时再加

**Files:**
- Create: `internal/store/epoch.go`, `internal/store/epoch_test.go`
- Create: `internal/service/session_epoch_test.go`, `internal/service/session_batch_test.go`
- Modify: `internal/domain/session.go`（`Session` 加 `Epoch`；`RevokeEvent.UserID` → `UserIDs []uuid.UUID`）
- Modify: `internal/service/session.go`（`Issue` 刻入纪元、`Validate` 比对、`revokeMatching` 支持多用户与切片）
- Modify: `internal/service/account.go`（递增纪元；批量方法）
- Modify: `cmd/fp/main.go`（装配 `EpochStore`）
- Modify: 所有 `NewSessionService` / `NewAccountService` 的调用点（编译错误会全部指出来）

**Interfaces:**
- Produces: `store.NewEpochStore(rdb) *store.EpochStore`，方法 `Current(ctx, uuid.UUID) (int64, error)` / `Bump(ctx, uuid.UUID) (int64, error)`
- Produces: `service.NewSessionService(st *store.SessionStore, pub *store.RevokePublisher, ep service.EpochStore) *SessionService`（**三参数**，注意与第一阶段的两参数版本不同）
- Produces: `service.NewSessionServiceWithClock(st, pub, ep, now func() int64) *SessionService`（**四参数**）
- Produces: `service.NewAccountService(users, sessions, epochs, logs) *AccountService`（**四参数**）
- Produces: `(*SessionService).RevokeUsers(ctx, userIDs []uuid.UUID, reason string) (int, error)`
- Produces: `(*AccountService).RevokeUsersSessions(ctx, userIDs []uuid.UUID) (int, error)`
- Changes: `(*SessionService).RevokeUser` 变成 `RevokeUsers` 的单元素包装
- Changes: `domain.RevokeEvent.UserID uuid.UUID` → `UserIDs []uuid.UUID`
- Produces: `service.maxTokensPerEvent = 1000`（包内常量）

---

- [ ] **Step 1: 写 EpochStore 的失败测试**

`internal/store/epoch_test.go`：

```go
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestEpochStartsAtZero(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	es := store.NewEpochStore(rdb)
	ctx := context.Background()

	got, err := es.Current(ctx, uuid.New())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got != 0 {
		t.Fatalf("从未撤销过的用户纪元为 %d，期望 0", got)
	}
}

func TestBumpIsMonotonic(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	es := store.NewEpochStore(rdb)
	ctx := context.Background()
	uid := uuid.New()

	for want := int64(1); want <= 3; want++ {
		got, err := es.Bump(ctx, uid)
		if err != nil {
			t.Fatalf("Bump: %v", err)
		}
		if got != want {
			t.Fatalf("第 %d 次 Bump 返回 %d", want, got)
		}
		cur, err := es.Current(ctx, uid)
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if cur != want {
			t.Fatalf("Bump 后 Current 为 %d，期望 %d", cur, want)
		}
	}
}

// TestEpochIsPerUser 守住键的作用域。
//
// 谁把键写成常量（漏掉 userID），所有用户会共用一个纪元——
// 冻结任意一个用户会把全站的会话全部作废。那种故障在单用户测试里
// 完全看不出来，只有并排比较两个用户才暴露。
func TestEpochIsPerUser(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	es := store.NewEpochStore(rdb)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()

	if _, err := es.Bump(ctx, a); err != nil {
		t.Fatalf("Bump a: %v", err)
	}
	got, err := es.Current(ctx, b)
	if err != nil {
		t.Fatalf("Current b: %v", err)
	}
	if got != 0 {
		t.Fatalf("递增用户 a 的纪元后，用户 b 的纪元变成了 %d", got)
	}
}

// TestEpochHasNoExpiry 守住"纪元键不设 TTL"。
//
// 设 TTL 会引入一个比它想解决的问题更糟的故障：键过期后，所有携带
// 非零纪元的会话都会与 0 比对失败而被登出——而这些会话本身完全合法。
// 表现是"一批用户在某个时刻集体掉线"，且无法从任何日志里看出原因。
func TestEpochHasNoExpiry(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	es := store.NewEpochStore(rdb)
	ctx := context.Background()
	uid := uuid.New()

	if _, err := es.Bump(ctx, uid); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	ttl, err := rdb.TTL(ctx, "fp:epoch:"+uid.String()).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	// go-redis 对"存在但无 TTL"的键返回 -1。
	if ttl != -1 {
		t.Fatalf("纪元键的 TTL 是 %v，期望无过期（-1）", ttl)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/store/ -run TestEpoch
```

期望：编译失败，`undefined: store.NewEpochStore`。

- [ ] **Step 3: 实现 EpochStore**

`internal/store/epoch.go`：

```go
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// epochKey 返回用户撤销纪元的 Redis 键。
func epochKey(userID uuid.UUID) string {
	return "fp:epoch:" + userID.String()
}

// EpochStore 维护每个用户的撤销纪元。
//
// 纪元关掉的是「冻结/改密」与「登录签发」之间的竞态：撤销时递增纪元，
// 此前读到旧纪元并据此签发的会话，校验时会因纪元不匹配被拒。
//
// **键不设 TTL。** 设 TTL 会引入一个更糟的故障模式：键过期后，所有携带
// 非零纪元的合法会话都会与 0 比对失败而被集体登出。增长量是
// 「曾被撤销过的用户数 × 一个几十字节的键」，可以忽略。
type EpochStore struct {
	rdb *redis.Client
}

// NewEpochStore 构造 EpochStore。
func NewEpochStore(rdb *redis.Client) *EpochStore {
	return &EpochStore{rdb: rdb}
}

// Current 返回用户当前的纪元。从未被撤销过的用户返回 0。
func (s *EpochStore) Current(ctx context.Context, userID uuid.UUID) (int64, error) {
	v, err := s.rdb.Get(ctx, epochKey(userID)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: 读取用户纪元: %w", err)
	}
	return v, nil
}

// Bump 递增用户纪元并返回新值。
func (s *EpochStore) Bump(ctx context.Context, userID uuid.UUID) (int64, error) {
	v, err := s.rdb.Incr(ctx, epochKey(userID)).Result()
	if err != nil {
		return 0, fmt.Errorf("store: 递增用户纪元: %w", err)
	}
	return v, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/store/ -run TestEpoch
```

- [ ] **Step 5: 给 Session 加 Epoch 字段**

`internal/domain/session.go`，在 `Mobile` 之后追加：

```go
	// Epoch 是签发时该用户的撤销纪元。校验时与当前纪元比对，不一致即拒。
	//
	// 它关掉的是「管理员冻结/改密」与「登录签发」之间的竞态：撤销的清扫
	// 只能扫到已存在的会话，扫不到正在签发路上的那一个。轮换时原样继承——
	// 轮换只换 token 的值，不是新的一次认证。
	Epoch int64
```

**轮换必须继承 `Epoch`。** `tryRotate` 里的 `newSess := *sess` 是整体拷贝，天然继承，不要额外赋值——写成 `newSess.Epoch = <重新读一次>` 会让轮换成为洗白纪元的途径：一个本该被拒的会话，只要熬到轮换点就复活了。

- [ ] **Step 6: SessionService 接入纪元**

`internal/service/session.go`。先加窄接口与字段：

```go
// EpochStore 是 SessionService 需要的最小纪元能力。
// 用窄接口而非直接依赖 *store.EpochStore，是为了让测试能注入桩，
// 也让这层的意图一目了然：它只读写一个计数器。
type EpochStore interface {
	Current(ctx context.Context, userID uuid.UUID) (int64, error)
}
```

`SessionService` 结构体加 `epochs EpochStore`，两个构造器都加参数：

```go
// NewSessionService 构造使用真实时钟的 SessionService。
func NewSessionService(st *store.SessionStore, pub *store.RevokePublisher, ep EpochStore) *SessionService {
	return NewSessionServiceWithClock(st, pub, ep, func() int64 { return time.Now().UnixMilli() })
}

// NewSessionServiceWithClock 构造使用自定义时钟的 SessionService。
func NewSessionServiceWithClock(st *store.SessionStore, pub *store.RevokePublisher, ep EpochStore, now func() int64) *SessionService {
	return &SessionService{store: st, pub: pub, epochs: ep, now: now}
}
```

`Issue` 里，在 `randomToken()` 之后、构造 `sess` 之前读纪元：

```go
	// 纪元在这里读、刻进会话。读得越早能拦住的撤销越多，但不能早于登录流程本身；
	// 这个读之前发生的撤销由 AuthService.recheckLoginable 兜住（见本任务开头的论证）。
	epoch, err := s.epochs.Current(ctx, in.UserID)
	if err != nil {
		return nil, err
	}
```

并在 `sess := &domain.Session{...}` 里加上 `Epoch: epoch,`。

`Validate` 里，在「token 与应用必须匹配」检查之后、`now := s.now()` 之前插入：

```go
	// 纪元比对：会话刻的纪元与当前不一致，说明它是在一次撤销的竞态窗口里
	// 签发的，撤销的清扫没扫到它。
	epoch, err := s.epochs.Current(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	if epoch != sess.Epoch {
		// 主动删掉：留着它只会让后续每次校验都白跑两趟 Redis，
		// 而它永远不可能再通过。
		if err := s.store.Delete(ctx, sess.Token); err != nil {
			slog.Error("service: 删除纪元失配的会话失败", "err", err, "sessionId", sess.ID)
		}
		return nil, domain.Errorf(domain.ErrUnauthorized, "token 无效或已过期")
	}
```

**错误信息与其他失败路径完全一致**（"token 无效或已过期"）。区分它们会告诉攻击者"这个 token 曾经存在过"。

> **性能注记：** `Validate` 现在是两次 Redis 往返（会话 + 纪元）。局域网 RTT 约 0.3ms，而 SDK 缓存后回源量在数百 QPS 量级，两次往返完全在预算内。
> **不要**在本任务里用 pipeline 合并它们——那需要改 `SessionStore.Get` 的形状，把一个正确性任务变成一个重构任务。真需要时再单独做。

- [ ] **Step 7: AccountService 递增纪元**

`internal/service/account.go`。加窄接口：

```go
// EpochBumper 是 AccountService 需要的最小纪元能力。
type EpochBumper interface {
	Bump(ctx context.Context, userID uuid.UUID) (int64, error)
}
```

结构体加 `epochs EpochBumper`，构造器变四参数：

```go
func NewAccountService(users *UserService, sessions SessionRevoker, epochs EpochBumper, logs *LoginLogService) *AccountService {
	return &AccountService{users: users, sessions: sessions, epochs: epochs, logs: logs}
}
```

`SetStatus` 的撤销分支改成：

```go
	if !u.CanLogin() {
		// 顺序是死的：状态已在上一步落库，纪元必须在清扫**之前**递增。
		// 挪到状态写入之前会破坏本任务开头论证的围栏，且测试不会变红。
		if _, err := s.epochs.Bump(ctx, userID); err != nil {
			return nil, err
		}
		if _, err := s.sessions.RevokeUser(ctx, userID, domain.RevokeReasonFreeze); err != nil {
			return nil, err
		}
		s.writeRevokeLog(ctx, userID, domain.RevokeReasonFreeze, "")
	}
```

`ResetPassword` 同样，在 `SetPassword` 之后、`RevokeUser` 之前插入 `Bump`。

`RevokeAllSessions`（踢全部设备）也递增：

```go
	// 踢下线常常是"怀疑账号失陷"的应急动作，所以也递增纪元，
	// 把正在签发路上的那个会话一并拦下。
	// 代价：与踢下线并发的一次合法登录会失败一次，用户重登即可——
	// 竞态窗口是微秒级，而漏掉一个失陷会话的代价大得多。
	if _, err := s.epochs.Bump(ctx, userID); err != nil {
		return 0, err
	}
```

**`RevokeSession`（踢单台设备）绝对不能递增纪元。** 纪元是用户级的，递增会把该用户**全部**设备踢掉——一个"踢掉这台平板"的操作会让手机和电脑一起掉线。

- [ ] **Step 8: 写行为测试**

`internal/service/session_epoch_test.go`：

```go
package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// TestSessionIssuedBeforeBumpIsRejected 是纪元的核心性质。
func TestSessionIssuedBeforeBumpIsRejected(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := env.sessions.Validate(ctx, sess.Token, env.app); err != nil {
		t.Fatalf("刚签发的会话就校验失败: %v", err)
	}

	if _, err := env.epochs.Bump(ctx, user.ID); err != nil {
		t.Fatalf("Bump: %v", err)
	}

	_, err = env.sessions.Validate(ctx, sess.Token, env.app)
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("纪元递增后校验返回 %v，期望 ErrUnauthorized", err)
	}
}

// TestSessionIssuedAfterBumpIsAccepted 确认纪元不是单向开关。
//
// 少了它，一个"永远返回不匹配"的实现也能让上一个测试通过——
// 而那意味着任何被冻结过一次的用户从此再也无法登录。
func TestSessionIssuedAfterBumpIsAccepted(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	if _, err := env.epochs.Bump(ctx, user.ID); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := env.sessions.Validate(ctx, sess.Token, env.app); err != nil {
		t.Fatalf("纪元递增后新签发的会话被拒: %v", err)
	}
}

// TestRotationCarriesEpochForward 守住"轮换不丢纪元"。
//
// 轮换出的新会话必须继承旧会话的 Epoch。若 tryRotate 改成从头构造新会话
// 而漏掉 Epoch，新会话的 Epoch 会是零值，与当前纪元不符——用户会在
// token 轮换那一刻被无声登出，而他什么都没做错。
//
// **纪元必须先递增到非零值。** 用零值的话，"继承了旧值"与"被清成零值"
// 根本无法区分，测试对这个 bug 完全失明。
//
// 关于"轮换洗白纪元"（本测试**不**覆盖，也无法覆盖）：那个场景在当前实现下
// 结构上不可达——纪元不匹配的会话在 Validate 里会被提前拒掉，根本进不了
// 轮换分支；就算把 newSess.Epoch 改成重读当前值，读到的也必然等于
// sess.Epoch，行为完全一致。残留的只有一个微秒级竞态（纪元检查通过后、
// tryRotate 执行前恰好发生一次 Bump），没有注入点，不值得为它造 hook。
func TestRotationCarriesEpochForward(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	// 先把纪元推到非零——零值下本测试无法区分"继承"与"清零"。
	for i := 0; i < 2; i++ {
		if _, err := env.epochs.Bump(ctx, user.ID); err != nil {
			t.Fatalf("Bump: %v", err)
		}
	}

	// rotate_interval 设 1 秒，让下一次校验必定触发轮换。
	app := env.appWithPolicy(t, func(p *domain.SessionPolicy) { p.RotateIntervalSeconds = 1 })

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// 必须带单位。Advance 的形参是 time.Duration，裸写 2000 是 2000 纳秒，
	// 取整成毫秒后是 0，时钟纹丝不动，轮换分支根本进不去。
	env.clock.Advance(2 * time.Second)

	// 这一步的断言本身就是护栏：确认轮换真的发生了。缺了它，
	// 本测试会退化成"什么都没测但绿了"。
	res, err := env.sessions.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("旧 token 校验: %v", err)
	}
	if !res.Rotated || res.NewToken == "" {
		t.Fatalf("越过 rotate_interval 后没有触发轮换: rotated=%v newToken=%q",
			res.Rotated, res.NewToken)
	}

	if _, err := env.sessions.Validate(ctx, res.NewToken, app); err != nil {
		t.Fatalf("轮换出的新 token 校验失败: %v——"+
			"新会话的纪元与当前纪元不符，说明轮换过程中把它丢了", err)
	}
}

// TestEpochMismatchDeletesSession 确认失配的会话被清掉。
func TestEpochMismatchDeletesSession(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := env.epochs.Bump(ctx, user.ID); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	if _, err := env.sessions.Validate(ctx, sess.Token, env.app); err == nil {
		t.Fatal("期望校验失败")
	}
	if _, err := env.sessions.SessionByToken(ctx, sess.Token); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("纪元失配的会话仍在存储里: %v", err)
	}
}

// TestFreezeBumpsEpoch / TestResetPasswordBumpsEpoch 确认撤销路径确实递增。
func TestFreezeBumpsEpoch(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	before, _ := env.epochs.Current(ctx, user.ID)
	if _, err := env.accounts.SetStatus(ctx, user.ID, domain.UserStatusFrozen); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	after, _ := env.epochs.Current(ctx, user.ID)
	if after <= before {
		t.Fatalf("冻结后纪元从 %d 变成 %d，期望递增", before, after)
	}
}

func TestResetPasswordBumpsEpoch(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	before, _ := env.epochs.Current(ctx, user.ID)
	if err := env.accounts.ResetPassword(ctx, user.ID, "newpassword123"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	after, _ := env.epochs.Current(ctx, user.ID)
	if after <= before {
		t.Fatalf("改密后纪元从 %d 变成 %d，期望递增", before, after)
	}
}

// TestRevokeSingleSessionDoesNotBumpEpoch 是本任务最重要的一个测试。
//
// 纪元是**用户级**的。给"踢掉这一台设备"加上递增，会把该用户所有设备
// 一起踢下线——手机、电脑、平板全掉。这个错误极易犯（三个撤销方法看起来
// 是一类操作），而症状是"踢一台掉一片"，用户报障时几乎不可能对上因果。
//
// 断言写成"另一台设备仍然可用"，而不是"纪元没变"：前者是我们真正在乎的
// 性质，后者会在实现换成别的机制时误报。
func TestRevokeSingleSessionDoesNotBumpEpoch(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	phone, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app, UA: "phone"})
	if err != nil {
		t.Fatalf("Issue phone: %v", err)
	}
	laptop, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app, UA: "laptop"})
	if err != nil {
		t.Fatalf("Issue laptop: %v", err)
	}

	if _, err := env.accounts.RevokeSession(ctx, user.ID, phone.ID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	if _, err := env.sessions.Validate(ctx, phone.Token, env.app); err == nil {
		t.Fatal("被踢掉的那台设备仍然有效")
	}
	if _, err := env.sessions.Validate(ctx, laptop.Token, env.app); err != nil {
		t.Fatalf("踢掉一台设备后，另一台也失效了——纪元被误用在单设备撤销上: %v", err)
	}
}
```

> **注意：** `newAccountEnv` 是第一阶段 `internal/service/account_test.go` 里已有的装配辅助。
> 本任务需要给它补上 `epochs` 字段、`newActiveUser` / `appWithPolicy` / `clock.Advance` 辅助（若尚不存在）。
> 先读 `account_test.go` 与 `session_test.go` 看现有形状，**复用而不是重建**。

- [ ] **Step 9: 写批量撤销的失败测试**

`internal/service/session_batch_test.go`：

```go
package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// TestRevokeUsersEmitsOneEventForManyUsers 是批量的全部意义。
//
// 逐用户发的话，一次踢 500 个用户 = 500 条广播 × 扇出到每个 fp 实例 ×
// 每个 SDK 处理 500 次。合并之后是 1 条。
//
// 断言"事件条数"而不是"token 都被撤销了"：一个内部 for 循环逐个调
// RevokeUser 的实现，功能完全正确、所有会话也确实失效了，
// 唯独没有减少任何广播——而减少广播正是本改动的唯一目的。
func TestRevokeUsersEmitsOneEventForManyUsers(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()

	var userIDs []uuid.UUID
	var tokens []string
	for i := 0; i < 5; i++ {
		u := env.newActiveUser(t)
		userIDs = append(userIDs, u.ID)
		sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: u.ID, App: env.app})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		tokens = append(tokens, sess.Token)
	}

	events := env.captureEvents(t) // 订阅撤销频道，收集广播

	n, err := env.sessions.RevokeUsers(ctx, userIDs, domain.RevokeReasonKick)
	if err != nil {
		t.Fatalf("RevokeUsers: %v", err)
	}
	if n != len(tokens) {
		t.Fatalf("撤销了 %d 个 token，期望 %d 个", n, len(tokens))
	}

	got := events.collect(t, 1) // 等最多 2 秒，收齐已到达的事件
	if len(got) != 1 {
		t.Fatalf("5 个用户产生了 %d 条广播，期望 1 条——"+
			"实现多半是内部 for 循环逐个调 RevokeUser，功能对但没省下任何广播", len(got))
	}
	if len(got[0].Tokens) != len(tokens) {
		t.Fatalf("事件里带了 %d 个 token，期望 %d 个", len(got[0].Tokens), len(tokens))
	}
	if len(got[0].UserIDs) != len(userIDs) {
		t.Fatalf("事件里带了 %d 个 userID，期望 %d 个", len(got[0].UserIDs), len(userIDs))
	}

	// 所有会话都必须真的失效。
	for _, tok := range tokens {
		if _, err := env.sessions.Validate(ctx, tok, env.app); err == nil {
			t.Fatalf("token %q 仍然有效", tok)
		}
	}
}

// TestRevokeUserDelegatesToBatch 守住"批量是唯一实现"。
//
// 单用户路径必须调批量方法传一个元素，而不是各写一套。两套实现意味着
// 修一个 bug 要改两处，而漏改的那一处不会有任何测试变红——
// 因为两条路径各有各的测试，都是绿的。
//
// 断言写成"单用户撤销产生的事件形状与批量一致"：
// 独立实现的那一版多半还在用旧的单值 UserID 形状。
func TestRevokeUserDelegatesToBatch(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	events := env.captureEvents(t)

	if _, err := env.sessions.RevokeUser(ctx, user.ID, domain.RevokeReasonKick); err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}

	got := events.collect(t, 1)
	if len(got) != 1 {
		t.Fatalf("单用户撤销产生了 %d 条事件", len(got))
	}
	if len(got[0].UserIDs) != 1 || got[0].UserIDs[0] != user.ID {
		t.Fatalf("事件的 UserIDs 是 %v，期望恰好 [%v]", got[0].UserIDs, user.ID)
	}
}

// TestLargeRevocationIsSplitIntoBoundedEvents 守住单条事件的体积上限。
//
// 一万个用户 × 每人 3 个会话 = 三万个 token，序列化后约 1 MB。广播给 20 个
// fp 实例是 20 MB，SDK 侧还要吃一个逼近 gRPC 默认 4 MB 接收上限的消息。
// 不切片的话，"批量"就从优化变成了新的故障源。
//
// 用 maxTokensPerEvent + 1 个 token 触发切片，断言产生 2 条且每条都不超上限。
func TestLargeRevocationIsSplitIntoBoundedEvents(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()

	// 造出 maxTokensPerEvent + 1 个会话。用少量用户各开多个会话最省时间。
	userIDs, total := env.seedSessions(t, service.MaxTokensPerEventForTest+1)

	events := env.captureEvents(t)
	if _, err := env.sessions.RevokeUsers(ctx, userIDs, domain.RevokeReasonKick); err != nil {
		t.Fatalf("RevokeUsers: %v", err)
	}

	got := events.collect(t, 2)
	if len(got) < 2 {
		t.Fatalf("%d 个 token 只产生了 %d 条事件——没有切片，"+
			"单条消息会随撤销规模无界增长", total, len(got))
	}
	sum := 0
	for i, ev := range got {
		if len(ev.Tokens) > service.MaxTokensPerEventForTest {
			t.Fatalf("第 %d 条事件带了 %d 个 token，超过上限 %d",
				i, len(ev.Tokens), service.MaxTokensPerEventForTest)
		}
		sum += len(ev.Tokens)
	}
	if sum != total {
		t.Fatalf("切片后 token 总数为 %d，期望 %d——切片过程中丢了", sum, total)
	}
}

// TestRevokeUsersSkipsUsersWithoutSessions 确认空用户不产生噪声。
//
// 批量踢 500 个用户，其中大多数本来就不在线是常态。给它们各发一条空事件
// 等于把刚省下的广播又加回来。
func TestRevokeUsersSkipsUsersWithoutSessions(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()

	online := env.newActiveUser(t)
	if _, err := env.sessions.Issue(ctx, service.IssueInput{UserID: online.ID, App: env.app}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	offline := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	events := env.captureEvents(t)
	if _, err := env.sessions.RevokeUsers(ctx, append(offline, online.ID), domain.RevokeReasonKick); err != nil {
		t.Fatalf("RevokeUsers: %v", err)
	}

	got := events.collect(t, 1)
	if len(got) != 1 {
		t.Fatalf("产生了 %d 条事件，期望 1 条", len(got))
	}
	if len(got[0].UserIDs) != 1 || got[0].UserIDs[0] != online.ID {
		t.Fatalf("事件的 UserIDs 是 %v，期望只含有会话的那一个用户", got[0].UserIDs)
	}
}

// TestRevokeUsersIsAtomicPerUserForEpoch 确认批量也逐个递增纪元。
//
// 纪元是用户级的，批量撤销必须给**每个**用户都递增，不能只递增第一个
// 或者引入一个"批次纪元"。漏递增的那些用户，正在签发路上的会话拦不住。
func TestRevokeUsersIsAtomicPerUserForEpoch(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()

	var ids []uuid.UUID
	before := map[uuid.UUID]int64{}
	for i := 0; i < 3; i++ {
		u := env.newActiveUser(t)
		ids = append(ids, u.ID)
		before[u.ID], _ = env.epochs.Current(ctx, u.ID)
	}

	if _, err := env.accounts.RevokeUsersSessions(ctx, ids); err != nil {
		t.Fatalf("RevokeUsersSessions: %v", err)
	}
	for _, id := range ids {
		after, _ := env.epochs.Current(ctx, id)
		if after <= before[id] {
			t.Fatalf("用户 %v 的纪元没有递增（%d → %d）", id, before[id], after)
		}
	}
}
```

> **测试辅助：** `captureEvents` 订阅撤销频道并收集广播，`collect(t, want)`
> 等到收满 `want` 条或超时（2 秒）后返回**已收到的全部**——注意不能收满就立刻返回，
> 否则"多发了一条"这类错误检测不到。`seedSessions(t, n)` 造出至少 n 个会话并返回
> 涉及的用户与实际会话数。`MaxTokensPerEventForTest` 是 `maxTokensPerEvent` 的
> 导出别名，仅供测试引用——**不要为了测试把常量本身导出**。

- [ ] **Step 10: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/service/ -run 'TestRevokeUsers|TestRevokeUserDelegates|TestLargeRevocation'
```

期望：`undefined: RevokeUsers`。

- [ ] **Step 11: 实现批量撤销**

先改 `internal/domain/session.go` 的事件形状：

```go
// RevokeEvent 是一次撤销的广播消息。
//
// SDK 按 token 缓存校验结果，因此事件必须携带具体的 token 列表，
// 而不能只给 userID——否则 SDK 无从知道该清哪些缓存条目。
type RevokeEvent struct {
	// Tokens 是本次被撤销的全部 token，可能跨多个用户。
	Tokens []string `json:"tokens"`
	// UserIDs 是这些 token 所属的用户，**仅用于日志与排障**。
	//
	// 是列表而非单值：批量撤销（一次踢 500 个用户）合并成一条事件，
	// 否则 500 次操作会产生 500 条广播、每条都要扇出到全部实例。
	UserIDs []uuid.UUID `json:"userIds"`
	// AppID 为 uuid.Nil 表示跨全部应用的撤销。
	//
	// 一条事件只有一个 AppID：合并只在「相同 reason + 相同 app 范围」内进行。
	// 把跨应用撤销（Nil）与限定应用的撤销混进同一条，必然有一半的过滤是错的。
	AppID  uuid.UUID `json:"appId"`
	Reason string    `json:"reason"`
	At     int64     `json:"at"`
}
```

`internal/service/session.go`：

```go
// maxTokensPerEvent 是单条撤销事件能携带的 token 上限。
//
// 一万个用户 × 每人 3 个会话 = 三万个 token，序列化后约 1 MB；广播给 20 个
// fp 实例就是 20 MB，SDK 侧还要吃一个逼近 gRPC 默认 4 MB 接收上限的消息。
// 超出就切成多条——"批量"是为了减少广播条数，不是为了把单条撑到无界。
const maxTokensPerEvent = 1000

// RevokeUsers 撤销一批用户的全部会话。
//
// 这是**唯一实现**，RevokeUser 是它的单元素包装。两套实现意味着修一个 bug
// 要改两处，而漏改的那一处不会有任何测试变红——两条路径各有各的绿测试。
func (s *SessionService) RevokeUsers(ctx context.Context, userIDs []uuid.UUID, reason string) (int, error) {
	// …逐用户 ListUserTokens + 删除（复用现有的 revokeMatching 内部逻辑），
	// 把结果累积到一个 (tokens, touchedUserIDs) 里，最后统一广播。
	//
	// 没有会话的用户不进 touchedUserIDs——批量踢 500 个用户时大多数本来就
	// 不在线，给它们各发一条空事件等于把刚省下的广播又加回来。
	//
	// 广播用 announceBatch 切片发出。
}

// RevokeUser 撤销单个用户的全部会话。
func (s *SessionService) RevokeUser(ctx context.Context, userID uuid.UUID, reason string) (int, error) {
	return s.RevokeUsers(ctx, []uuid.UUID{userID}, reason)
}

// announceBatch 把一批 token 按 maxTokensPerEvent 切片广播。
//
// 切片时 UserIDs 原样带在每一条上：它只用于日志，精确对应哪一片没有意义，
// 而"这批撤销涉及哪些用户"对排障是有意义的。
func (s *SessionService) announceBatch(ctx context.Context, tokens []string, userIDs []uuid.UUID, appID uuid.UUID, reason string) {
	for start := 0; start < len(tokens); start += maxTokensPerEvent {
		end := min(start+maxTokensPerEvent, len(tokens))
		s.announce(ctx, domain.RevokeEvent{
			Tokens:  tokens[start:end],
			UserIDs: userIDs,
			AppID:   appID,
			Reason:  reason,
			At:      s.now(),
		})
	}
}
```

`internal/service/account.go` 加批量入口，并保持 Step 7 定下的顺序（**状态/凭据写入 → 递增纪元 → 清扫**）：

```go
// RevokeUsersSessions 批量踢下线。逐个递增纪元——纪元是用户级的，
// 不存在"批次纪元"，漏掉谁就拦不住谁正在签发路上的会话。
func (s *AccountService) RevokeUsersSessions(ctx context.Context, userIDs []uuid.UUID) (int, error) {
	for _, id := range userIDs {
		if _, err := s.epochs.Bump(ctx, id); err != nil {
			return 0, err
		}
	}
	n, err := s.sessions.RevokeUsers(ctx, userIDs, domain.RevokeReasonKick)
	if err != nil {
		return n, err
	}
	for _, id := range userIDs {
		s.writeRevokeLog(ctx, id, domain.RevokeReasonKick, "")
	}
	return n, nil
}
```

`RevokeAllSessions`（单用户）改为 `return s.RevokeUsersSessions(ctx, []uuid.UUID{userID})`。

> **本阶段不加批量的管理端 HTTP 接口。** 那是控制台的事（计划三）。
> 服务层先就位，接口来了直接调——但 `RevokeUsersSessions` 现在就有真实调用者
> （单用户路径），不是预留的空壳。

- [ ] **Step 12: 修全部调用点并跑全量测试**

```bash
go build ./... 2>&1 | head -40
```

编译错误会列出所有 `NewSessionService` / `NewAccountService` 的调用点。逐个补参数。`cmd/fp/main.go` 里要新建 `epochStore := store.NewEpochStore(rdb)` 并传给两者。

```bash
./scripts/test.sh
```

期望：全绿。

- [ ] **Step 13: 提交**

```bash
git add -A
```

```bash
git commit -m "feat(session): 撤销纪元与批量撤销"
```

---

## Task 3: 轮换交接幂等化（**不可跳过**）

### 问题

`tryRotate` 用 `rot:<sessionID>` 锁保证一个会话只轮换一次。过渡期内，其余带旧 token 的请求走到 `tryRotate` 拿不到锁，**原样返回"未轮换"**——它们永远不会知道新 token 是什么。

也就是说：新 token 只送达并发请求中的**一个**。那一个响应丢了（请求被取消、页面忽略响应体、后台请求、网络中断），客户端就一直握着旧 token，30 秒后无声登出——哪怕这个会话的 `max_lifetime` 还剩 89 天。

第一阶段没有 SDK，每个请求都实打实回源查 Redis，症状只是"偶发掉线"。有了 SDK 缓存之后，旧 token 会被缓存整整一个 `cache_ttl`，客户端连"再试一次也许能拿到"的机会都变少。

### 方案

轮换时额外写一条 `fp:rot:<旧token> → 新token` 的映射，TTL 与过渡期一致。过渡期内任何一次带旧 token 的校验，只要拿不到轮换锁，就去读这条映射并**再告知一次**。

于是交接从"一次性投递"变成"过渡期内每次请求都重复告知"。

### 两条路径的 `cache_ttl` 不相等，但都安全

改完之后，"重复告知"这条路径手里只有旧会话对象，而"亲自轮换"那条路径手里是新会话。会不会两条路径下发不同的 `cache_ttl`？**会。**

```
grace = max(15s, cfg)                        ⇒ grace ≥ cfg

重复告知：cacheTTL = min(cfg, min(grace, maxRemain)) = min(cfg, maxRemain)
亲自轮换：cacheTTL = min(cfg, min(idle,  maxRemain))
```

两者仅在 `idle ≥ cfg` 时相等。而 `idle` 取自 `IdleTimeoutFor(sess.Mobile)`——
`SessionPolicy.Validate` 只约束了 `TokenCacheTTLSeconds ≤ IdleTimeoutSeconds`（桌面档），
**从未约束 `IdleTimeoutMobileSeconds`**。所以 `idle_mobile=10s、cfg=60s` 是一组合法配置，
此时移动端会话在两条路径下拿到 10 秒与 60 秒两个不同的值。

**但安全性不依赖这个等式：** 调用方手里是**旧 token**，它的剩余寿命是 `grace`；
而两条路径给出的 `cache_ttl` 都是 `min(cfg, …) ≤ cfg ≤ grace`，都不会让 SDK 缓存
超过旧 token 的实际存活时间。不等只意味着移动端在轮换路径下会多回源几次，
是效率问题不是安全问题。

所以本任务**不需要**为此改动 `Validate` 里 `res.Session` 的赋值——保持第一阶段的行为即可。

> 这一节最初写的是"两者恒等"，理由是"`SessionPolicy.Validate` 保证 `cfg ≤ idle`"。
> 那句话只对桌面档成立，Task 3 的评审查出了移动档这个缺口。**证明是假的，结论侥幸是真的。**
> 记在这里当作提醒：把"某处校验保证了 X"写进证明之前，去把那处校验读一遍。

### 一个残留的窄窗口（可接受，但要知道）

线程 A 拿到轮换锁到它写完映射之间，有一次 Redis 往返的空隙。落在这个空隙里的并发请求 B 读不到映射，会返回"未轮换"。

后果是 B 这一次没学到新 token，下一次校验就学到了——而 B 手里的旧 token 至少还能活 `grace ≥ cfg`，也就是至少能撑到它的缓存过期再回源一次。**不会掉线。**

写入顺序因此是死的：**先写新会话，再写映射，最后缩短旧会话**。反过来先写映射的话，新会话写失败会留下一条指向不存在 token 的映射——客户端照着切过去，立刻登出，比现在的问题更糟。

**Files:**
- Modify: `internal/store/session.go`（加 `PutRotation` / `RotatedTo`）
- Modify: `internal/service/session.go`（`tryRotate` 改签名并写映射）
- Create: `internal/store/rotation_test.go`
- Create: `internal/service/session_handoff_test.go`

**Interfaces:**
- Produces: `(*store.SessionStore).PutRotation(ctx, oldToken, newToken string, ttl time.Duration) error`
- Produces: `(*store.SessionStore).RotatedTo(ctx, oldToken string) (string, error)`——无记录时返回**空串加 nil error**，不是 `ErrNotFound`
- Changes: `(*service.SessionService).tryRotate` 变为 `(newToken string, newSess *domain.Session, err error)`，`newToken == ""` 表示未轮换

---

- [ ] **Step 1: 写 store 层的失败测试**

`internal/store/rotation_test.go`：

```go
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestRotatedToReturnsEmptyWhenAbsent(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))

	got, err := st.RotatedTo(context.Background(), "没有记录的token")
	if err != nil {
		t.Fatalf("无记录时返回了错误: %v", err)
	}
	if got != "" {
		t.Fatalf("无记录时返回 %q，期望空串", got)
	}
}

func TestPutRotationRoundTrips(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if err := st.PutRotation(ctx, "old", "new", time.Minute); err != nil {
		t.Fatalf("PutRotation: %v", err)
	}
	got, err := st.RotatedTo(ctx, "old")
	if err != nil {
		t.Fatalf("RotatedTo: %v", err)
	}
	if got != "new" {
		t.Fatalf("RotatedTo 返回 %q，期望 \"new\"", got)
	}
}

// TestPutRotationRejectsNonPositiveTTL 守住一个 go-redis 的陷阱。
//
// SET 的 TTL 参数为 0 或负数时，go-redis 直接不发 EX/PX——写进去的是一个
// **永不过期**的键。轮换映射一旦不朽，一个早已死透的旧 token 会在往后的
// 任意时刻被"告知"成一个同样早已不存在的新 token，客户端照着切过去后登出。
//
// 第一阶段的 Put 已经有同样的防线，这里是同一个陷阱的第二个入口。
func TestPutRotationRejectsNonPositiveTTL(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()

	for _, ttl := range []time.Duration{0, -time.Second} {
		if err := st.PutRotation(ctx, "old", "new", ttl); err == nil {
			t.Fatalf("ttl=%v 时 PutRotation 没有报错", ttl)
		}
		got, err := st.RotatedTo(ctx, "old")
		if err != nil {
			t.Fatalf("RotatedTo: %v", err)
		}
		if got != "" {
			t.Fatalf("ttl=%v 被拒后仍写进了 %q", ttl, got)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/store/ -run 'TestRotat|TestPutRotation'
```

期望：编译失败，`st.PutRotation undefined`。

- [ ] **Step 3: 实现 store 层**

`internal/store/session.go`，在已有的 key 前缀常量旁加：

```go
	// rotationPrefix 是「旧 token → 新 token」映射的键前缀。
	rotationPrefix = "fp:rot:"
```

追加两个方法：

```go
// PutRotation 记下一次轮换的去向：旧 token 换成了哪个新 token。
//
// ttl 必须与轮换过渡期一致。过渡期内每一次带旧 token 的校验都会读这条映射，
// 把新 token 再告知一次——轮换的交接因此从"一次性投递"变成"重复告知"，
// 不再依赖某一个响应必须送达。
//
// 拒绝非正的 ttl：go-redis 在 ttl <= 0 时不发 EX 参数，写进去的是永不过期的键。
// 一条不朽的轮换映射会在旧 token 早已死透之后，继续把客户端指向一个同样
// 不存在的新 token。
func (s *SessionStore) PutRotation(ctx context.Context, oldToken, newToken string, ttl time.Duration) error {
	if ttl <= 0 {
		return domain.Errorf(domain.ErrInvalidArgument, "轮换映射的 ttl 必须为正，得到 %v", ttl)
	}
	if err := s.rdb.Set(ctx, rotationPrefix+oldToken, newToken, ttl).Err(); err != nil {
		return fmt.Errorf("store: 写入轮换映射: %w", err)
	}
	return nil
}

// RotatedTo 返回旧 token 轮换后的新 token。
//
// 没有记录时返回空串与 nil error，而不是 ErrNotFound：调用方问的是
// "这个 token 轮换过吗"，"没有"是一个正常答案，不是异常。
func (s *SessionStore) RotatedTo(ctx context.Context, oldToken string) (string, error) {
	v, err := s.rdb.Get(ctx, rotationPrefix+oldToken).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: 读取轮换映射: %w", err)
	}
	return v, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
./scripts/test.sh ./internal/store/ -run 'TestRotat|TestPutRotation'
```

- [ ] **Step 5: 写 service 层的失败测试**

`internal/service/session_handoff_test.go`：

```go
package service_test

import (
	"context"
	"sync"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// TestRotationIsAnnouncedToEveryValidateDuringGrace 是本任务的核心。
//
// 修复前：第一次校验返回 Rotated=true，之后每一次都返回 Rotated=false，
// 新 token 就此失传，客户端在过渡期结束时被登出。
// 修复后：过渡期内每一次带旧 token 的校验都会被再次告知同一个新 token。
func TestRotationIsAnnouncedToEveryValidateDuringGrace(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()
	app := env.appWithPolicy(t, func(p *domain.SessionPolicy) {
		p.RotateIntervalSeconds = 1
	})

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: env.userID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	env.clock.Advance(2 * time.Second) // 越过 rotate_interval

	first, err := env.sessions.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("首次校验: %v", err)
	}
	if !first.Rotated || first.NewToken == "" {
		t.Fatalf("首次校验没有触发轮换: rotated=%v newToken=%q", first.Rotated, first.NewToken)
	}

	// 过渡期内再校验两次，每次都必须拿到同一个新 token。
	for i := 2; i <= 3; i++ {
		got, err := env.sessions.Validate(ctx, sess.Token, app)
		if err != nil {
			t.Fatalf("第 %d 次校验: %v", i, err)
		}
		if !got.Rotated {
			t.Fatalf("第 %d 次校验返回 Rotated=false——新 token 在过渡期内失传了，"+
				"客户端会在过渡期结束时被登出", i)
		}
		if got.NewToken != first.NewToken {
			t.Fatalf("第 %d 次校验给出的新 token 是 %q，首次是 %q——"+
				"重复告知不能触发第二次轮换", i, got.NewToken, first.NewToken)
		}
	}
}

// TestConcurrentValidatesConvergeOnOneNewToken 确认并发下只轮换一次。
//
// 断言分两段：并发阶段允许有请求落在"锁已拿到、映射未写完"的窄空隙里而
// 学不到新 token（那是可接受的降级，它们下一次就学到了）；但**凡是学到的，
// 必须是同一个 token**。串行阶段则要求全部学到——空隙已经过去了。
func TestConcurrentValidatesConvergeOnOneNewToken(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()
	app := env.appWithPolicy(t, func(p *domain.SessionPolicy) {
		p.RotateIntervalSeconds = 1
	})

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: env.userID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	env.clock.Advance(2 * time.Second)

	const n = 8
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := env.sessions.Validate(ctx, sess.Token, app)
			if err != nil {
				return
			}
			if got.Rotated {
				mu.Lock()
				seen[got.NewToken]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != 1 {
		t.Fatalf("并发校验产生了 %d 个不同的新 token，期望恰好 1 个：%v", len(seen), seen)
	}

	// 空隙已过，此后每一次都必须被告知。
	got, err := env.sessions.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("并发之后的校验: %v", err)
	}
	if !got.Rotated {
		t.Fatal("并发结束后的校验仍未收到告知")
	}
	for token := range seen {
		if got.NewToken != token {
			t.Fatalf("并发阶段的新 token 是 %q，之后拿到的是 %q", token, got.NewToken)
		}
	}
}

// TestExpiredOldTokenIsNotAnnounced 守住"不能复活死 token"。
//
// 过渡期结束后旧 token 的会话已被 Redis 清掉，此时的校验必须直接失败，
// 而不是从残留的映射里读出一个新 token 再告知——那等于给一个早该失效的
// 凭据开了一条后门。
func TestExpiredOldTokenIsNotAnnounced(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()
	app := env.appWithPolicy(t, func(p *domain.SessionPolicy) {
		p.RotateIntervalSeconds = 1
	})

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: env.userID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	env.clock.Advance(2 * time.Second)
	if _, err := env.sessions.Validate(ctx, sess.Token, app); err != nil {
		t.Fatalf("触发轮换: %v", err)
	}

	// 越过过渡期。逻辑时钟推进不会让 Redis 的 TTL 到期，所以显式删掉旧会话，
	// 模拟 TTL 清理后的状态——校验必须止步于"会话不存在"。
	if err := env.store.Delete(ctx, sess.Token); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := env.sessions.Validate(ctx, sess.Token, app); err == nil {
		t.Fatal("会话已清理，校验却仍然放行")
	}
}
```

> **注意：** `newSessionEnv` / `env.clock` / `env.appWithPolicy` 是第一阶段
> `internal/service/session_test.go`、`session_rotate_test.go` 里已有的辅助。
> 先读那两个文件，**复用现有形状**，缺什么补什么，不要另起炉灶。
> `env.store` 若尚未暴露，在 env 里加一个字段即可。

- [ ] **Step 6: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/service/ -run 'TestRotationIsAnnounced|TestConcurrentValidates|TestExpiredOldToken'
```

期望：`TestRotationIsAnnouncedToEveryValidateDuringGrace` 在第 2 次校验时失败并打印
"新 token 在过渡期内失传了"。

- [ ] **Step 7: 改 tryRotate**

`internal/service/session.go`。新签名与实现：

```go
// tryRotate 在拿到去重锁时换发新 token，并把旧 token 缩短到过渡期。
//
// 返回值 newToken 为空串表示本次没有轮换。newSess 仅在本请求**亲自完成**
// 轮换时非空；过渡期内的"重复告知"路径手里只有旧会话，这不影响正确性——
// 新旧会话算出的 cache_ttl 恒等，证明见本函数下方 Validate 的注释。
//
// 新会话继承 ID、FirstAuthAt 与 Epoch：轮换只换 token 的值，会话本身没有变，
// 也不是新的一次认证。特别是 FirstAuthAt 绝不能重置，否则 max_lifetime 会被
// 活跃用户无限续命；Epoch 同理，重读会让轮换成为洗白撤销纪元的途径。
func (s *SessionService) tryRotate(ctx context.Context, sess *domain.Session, app *domain.Application, now int64) (string, *domain.Session, error) {
	grace := GraceDuration(app.Session)

	// 锁的 TTL 取过渡期：过渡期内旧 token 仍可用，但不应再次触发轮换。
	ok, err := s.store.TryLock(ctx, "rot:"+sess.ID, grace)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		// 锁被占：本会话刚被另一个并发请求轮换过。取出当时写下的映射，
		// 把新 token 再告知这一个请求一次。
		//
		// 这是本函数存在的第二个理由，也是交接不再依赖"某一个响应必须送达"
		// 的全部原因。读到空串说明对方还没写完映射（窄空隙），当作未轮换
		// 返回即可——调用方手里的旧 token 至少还能活一个过渡期。
		newToken, err := s.store.RotatedTo(ctx, sess.Token)
		if err != nil {
			return "", nil, err
		}
		return newToken, nil, nil
	}

	newToken, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	idle := app.Session.IdleTimeoutFor(sess.Mobile)

	newSess := *sess
	newSess.Token = newToken
	newSess.IssuedAt = now
	newSess.LastExtendedAt = now
	newSess.IdleExpiresAt = now + idle.Milliseconds()
	if err := s.store.Put(ctx, &newSess, newSess.RemainingAt(now, app.Session)); err != nil {
		return "", nil, err
	}

	// 写映射必须在新会话落地**之后**：反过来的话，新会话写失败会留下一条
	// 指向不存在 token 的映射，客户端照着切过去会立刻登出——比不告知更糟。
	if err := s.store.PutRotation(ctx, sess.Token, newToken, grace); err != nil {
		return "", nil, err
	}

	// 旧 token 缩短到过渡期。
	// 同时把 IdleExpiresAt 一起改小——只改 Redis TTL 的话，会话 JSON 里
	// 仍是很远的过期时间，cache_ttl 会按完整窗口下发，而 Redis key 在过渡期
	// 结束就没了，SDK 会拿着一个已被删除的 token 继续放行。
	//
	// 特意不改 IssuedAt：过渡期内旧 token 再次被校验时，(now-IssuedAt) 依旧
	// 越过 rotate_interval，会继续走 Validate 的 if 分支进入 tryRotate——
	// 这正是"重复告知"生效的前提。一旦落到 else if 的延期分支，
	// LastExtendedAt 早已陈旧，会被判定为"该延期"，从而把刚缩短的
	// IdleExpiresAt 重新拉回一整个空闲窗口，过渡期形同虚设。
	oldSess := *sess
	oldSess.IdleExpiresAt = now + grace.Milliseconds()
	if err := s.store.Put(ctx, &oldSess, grace); err != nil {
		return "", nil, err
	}

	return newToken, &newSess, nil
}
```

- [ ] **Step 8: 改 Validate 的调用处**

把原来的轮换分支替换为：

```go
	// 轮换优先于延期：轮换本身就会给新会话一个完整的空闲窗口。
	if now-sess.IssuedAt >= app.Session.RotateInterval().Milliseconds() {
		newToken, newSess, err := s.tryRotate(ctx, sess, app, now)
		if err != nil {
			return nil, err
		}
		if newToken != "" {
			res.Rotated = true
			res.NewToken = newToken
			// newSess 为 nil 时保持 res.Session 为旧会话——这是过渡期内的
			// 重复告知路径。两条路径算出的 cache_ttl **不保证相等**，但都是安全的：
			//
			//   grace = max(15s, cfg)                      ⇒ grace ≥ cfg
			//   重复告知：min(cfg, min(grace, maxRemain)) = min(cfg, maxRemain)
			//   亲自轮换：min(cfg, min(idle,  maxRemain))
			//
			// 两者仅在 idle ≥ cfg 时相等。idle 取自 IdleTimeoutFor(sess.Mobile)，
			// 而 SessionPolicy.Validate 只约束了 IdleTimeoutSeconds ≥ cfg，
			// **没有约束 IdleTimeoutMobileSeconds**——所以移动端存在 idle < cfg 的
			// 合法配置（例如 idle_mobile=10s、cfg=60s），此时轮换路径给出的值更小。
			//
			// 安全性不依赖这个等式：调用方手里是旧 token，它的剩余寿命是 grace，
			// 而两条路径给出的 cache_ttl 都 ≤ cfg ≤ grace，都不会让 SDK 缓存超过
			// 旧 token 的实际存活时间。不等只意味着移动端在轮换路径下多回源几次。
			if newSess != nil {
				res.Session = newSess
			}
		}
	} else if now-sess.LastExtendedAt >= app.Session.ExtendInterval().Milliseconds() {
```

- [ ] **Step 9: 跑全量测试**

```bash
./scripts/test.sh
```

期望全绿。第一阶段的 `session_rotate_test.go` 里若有断言 `tryRotate` 旧签名的地方，一并修掉。

- [ ] **Step 10: 提交**

```bash
git add -A
```

```bash
git commit -m "fix(session): 轮换交接改为过渡期内重复告知，不再依赖单次投递"
```

---

## Task 4: gRPC 传输层骨架——错误映射与应用认证拦截器

本任务只建传输层的地基：领域错误怎么变成 gRPC status，以及每个调用怎么证明自己是哪个应用。不实现任何 RPC。

**Files:**
- Create: `internal/grpcapi/errors.go`, `internal/grpcapi/auth_interceptor.go`
- Test: `internal/grpcapi/errors_test.go`, `internal/grpcapi/auth_interceptor_test.go`

**Interfaces:**
- Produces: `grpcapi.statusFrom(err error) error`（包内）
- Produces: `grpcapi.newAppVerifier(apps AppAuthenticator, ttl time.Duration) *appVerifier`
- Produces: `(*appVerifier).UnaryInterceptor` / `(*appVerifier).StreamInterceptor`
- Produces: `grpcapi.AppAuthenticator` 接口，`*service.ApplicationService` 满足它
- Produces: `grpcapi.appIDFrom(ctx) (string, bool)`——从已认证的调用上下文里取 appId

---

- [ ] **Step 1: 写错误映射的失败测试**

`internal/grpcapi/errors_test.go`：

```go
package grpcapi

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
)

func TestStatusFromMapsEveryDomainError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"未找到", domain.Errorf(domain.ErrNotFound, "没有这个用户"), codes.NotFound},
		{"凭据错误", domain.Errorf(domain.ErrInvalidCredential, "密码不对"), codes.Unauthenticated},
		{"未授权", domain.Errorf(domain.ErrUnauthorized, "token 无效或已过期"), codes.Unauthenticated},
		{"禁止", domain.Errorf(domain.ErrForbidden, "应用已停用"), codes.PermissionDenied},
		{"冲突", domain.Errorf(domain.ErrConflict, "已存在"), codes.AlreadyExists},
		{"参数非法", domain.Errorf(domain.ErrInvalidArgument, "手机号格式不对"), codes.InvalidArgument},
		{"被限流", domain.Errorf(domain.ErrRateLimited, "发送太频繁"), codes.ResourceExhausted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := statusFrom(c.err)
			if c.want == codes.OK {
				if got != nil {
					t.Fatalf("nil 错误被映射成了 %v", got)
				}
				return
			}
			if status.Code(got) != c.want {
				t.Fatalf("映射成 %v，期望 %v", status.Code(got), c.want)
			}
		})
	}
}

// TestStatusFromHidesInternalDetail 守住"内部错误不外泄"。
//
// service 层的内部错误里带着 SQL 片段、连接串、表名、内部 ID。
// 这些经 gRPC 原样回给业务方（进而可能回给终端用户）就是信息泄露。
// 已识别的领域错误可以原样回——那些消息本来就是写给调用方看的。
func TestStatusFromHidesInternalDetail(t *testing.T) {
	secret := "pgx: connect postgres://postgres:hunter2@10.9.1.2:15432/fp"
	got := statusFrom(fmt.Errorf("service: 读取用户: %w", errors.New(secret)))

	if status.Code(got) != codes.Internal {
		t.Fatalf("未识别的错误映射成 %v，期望 Internal", status.Code(got))
	}
	if strings.Contains(status.Convert(got).Message(), "hunter2") ||
		strings.Contains(status.Convert(got).Message(), "10.9.1.2") {
		t.Fatalf("内部错误细节泄露到了 gRPC 响应里: %q", status.Convert(got).Message())
	}
}

// TestStatusFromKeepsDomainMessage 确认已识别错误的说明没被一起吞掉。
//
// 少了这条，一个"什么都返回 Internal 且消息为空"的实现也能让上面两个测试通过——
// 而那意味着业务方永远分不清"密码错了"和"服务挂了"。
func TestStatusFromKeepsDomainMessage(t *testing.T) {
	got := statusFrom(domain.Errorf(domain.ErrRateLimited, "验证码发送太频繁"))
	if !strings.Contains(status.Convert(got).Message(), "验证码发送太频繁") {
		t.Fatalf("领域错误的说明丢了: %q", status.Convert(got).Message())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/grpcapi/
```

期望：包不存在，编译失败。

- [ ] **Step 3: 实现错误映射**

`internal/grpcapi/errors.go`：

```go
// Package grpcapi 是 SDK 的 gRPC 传输层。它只做协议转换与认证，不含业务逻辑。
package grpcapi

import (
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
)

// statusFrom 把领域错误映射成 gRPC status。
//
// 分支顺序与取值刻意与 httpapi.writeError 一一对应——同一个领域错误在
// 两个传输层必须表达同一件事，否则同一个失败经 HTTP 是 403、经 gRPC 是
// NotFound，排障时没人能把两边的日志对上。
//
// 未识别的错误一律 Internal 且**丢掉原始消息**：它们来自 pgx / redis /
// 标准库，消息里可能带连接串、SQL、表名。服务端日志留全文，线上只回一句
// 通用说明。已识别的领域错误则原样回传——那些消息本来就是写给调用方看的。
func statusFrom(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrInvalidCredential):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, domain.ErrUnauthorized):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, domain.ErrForbidden):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, domain.ErrConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, domain.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, domain.ErrRateLimited):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		slog.Error("grpcapi: 未处理的内部错误", "err", err)
		return status.Error(codes.Internal, "internal error")
	}
}
```

- [ ] **Step 4: 写认证拦截器的失败测试**

`internal/grpcapi/auth_interceptor_test.go`：

```go
package grpcapi

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
)

// countingApps 是 AppAuthenticator 的计数桩。
type countingApps struct {
	calls  atomic.Int32
	appID  string
	secret string
}

func (c *countingApps) VerifySecret(_ context.Context, appID, secret string) (*domain.Application, error) {
	c.calls.Add(1)
	if appID != c.appID || secret != c.secret {
		return nil, domain.Errorf(domain.ErrInvalidCredential, "appId 或 appSecret 不正确")
	}
	return &domain.Application{AppID: appID}, nil
}

func ctxWith(appID, secret string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		mdAppID, appID,
		mdAppSecret, secret,
	))
}

func newTestVerifier() (*appVerifier, *countingApps) {
	apps := &countingApps{appID: "app-1", secret: "s3cret"}
	return newAppVerifier(apps, 5*time.Minute), apps
}

func TestUnaryRejectsMissingMetadata(t *testing.T) {
	v, _ := newTestVerifier()
	_, err := v.UnaryInterceptor(context.Background(), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("无 metadata 时返回 %v，期望 Unauthenticated", status.Code(err))
	}
}

func TestUnaryRejectsWrongSecret(t *testing.T) {
	v, _ := newTestVerifier()
	_, err := v.UnaryInterceptor(ctxWith("app-1", "wrong"), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("错误 secret 返回 %v，期望 Unauthenticated", status.Code(err))
	}
}

func TestUnaryInjectsAppID(t *testing.T) {
	v, _ := newTestVerifier()
	var seen string
	_, err := v.UnaryInterceptor(ctxWith("app-1", "s3cret"), nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, _ any) (any, error) {
			seen, _ = appIDFrom(ctx)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("合法凭据被拒: %v", err)
	}
	if seen != "app-1" {
		t.Fatalf("handler 看到的 appId 是 %q，期望 app-1", seen)
	}
}

// TestVerificationIsCached 是本任务存在的全部理由。
//
// VerifySecret 内部是 bcrypt，单次 50–100ms。回源目标量级是数百 QPS——
// 不缓存的话每个 ValidateToken 都要付一次 bcrypt，吞吐直接归零，
// 而且不会报任何错，只是慢到不可用。
func TestVerificationIsCached(t *testing.T) {
	v, apps := newTestVerifier()
	pass := func(ctx context.Context, _ any) (any, error) { return nil, nil }

	for i := 0; i < 5; i++ {
		if _, err := v.UnaryInterceptor(ctxWith("app-1", "s3cret"), nil, &grpc.UnaryServerInfo{}, pass); err != nil {
			t.Fatalf("第 %d 次调用被拒: %v", i, err)
		}
	}
	if got := apps.calls.Load(); got != 1 {
		t.Fatalf("5 次调用触发了 %d 次 bcrypt 校验，期望 1 次", got)
	}
}

// TestFailedVerificationIsNotCached 守住一条内存安全性质。
//
// 缓存键的一半（secret 的哈希）由调用方控制。缓存失败结果的话，
// 攻击者发一百万个不同的错误 secret 就能让这个 map 无界膨胀，把 fp OOM 掉。
// 只缓存成功，键的数量就被"真实存在的应用数"钉死——因为只有正确的
// secret 才进得来。
//
// 断言写成"底层被调用了 N 次"：失败若进了缓存，第二次起就不会再打到底层。
func TestFailedVerificationIsNotCached(t *testing.T) {
	v, apps := newTestVerifier()
	pass := func(ctx context.Context, _ any) (any, error) { return nil, nil }

	for i := 0; i < 3; i++ {
		if _, err := v.UnaryInterceptor(ctxWith("app-1", "wrong"), nil, &grpc.UnaryServerInfo{}, pass); err == nil {
			t.Fatal("错误 secret 被放行")
		}
	}
	if got := apps.calls.Load(); got != 3 {
		t.Fatalf("3 次错误凭据只打到底层 %d 次——失败被缓存了，"+
			"缓存键可被攻击者任意撑大", got)
	}
}

// TestCacheExpires 确认 TTL 真的生效，appSecret 轮换后旧值不会永远有效。
func TestCacheExpires(t *testing.T) {
	apps := &countingApps{appID: "app-1", secret: "s3cret"}
	v := newAppVerifier(apps, time.Millisecond)
	pass := func(ctx context.Context, _ any) (any, error) { return nil, nil }

	if _, err := v.UnaryInterceptor(ctxWith("app-1", "s3cret"), nil, &grpc.UnaryServerInfo{}, pass); err != nil {
		t.Fatalf("首次: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := v.UnaryInterceptor(ctxWith("app-1", "s3cret"), nil, &grpc.UnaryServerInfo{}, pass); err != nil {
		t.Fatalf("过期后: %v", err)
	}
	if got := apps.calls.Load(); got != 2 {
		t.Fatalf("TTL 过期后仍走缓存，底层只被调用了 %d 次", got)
	}
}

// TestCacheKeyDoesNotContainPlaintextSecret 守住"内存里不留明文凭据"。
//
// 直接拿 secret 当键是最省事的写法，也是最容易过审的——功能完全正确。
// 代价是 appSecret 明文常驻堆内存，崩溃 dump、调试器、内存分析工具都能读到。
func TestCacheKeyDoesNotContainPlaintextSecret(t *testing.T) {
	const secret = "非常独特的明文密钥-9f3a"
	if k := verifierCacheKey("app-1", secret); strings.Contains(k, secret) {
		t.Fatalf("缓存键里含明文 secret: %q", k)
	}
}

// TestStreamInterceptorAlsoInjectsAppID 守住"流没被漏掉"。
//
// 实现一元拦截器时很容易忘了流也要一份——而 Watch 是流。漏掉的表现是
// Watch 完全不做认证：任何人连上 :9090 就能订阅到全部应用的撤销事件，
// 拿到实时的 token 列表。这是本任务里后果最严重的一个疏漏。
func TestStreamInterceptorAlsoInjectsAppID(t *testing.T) {
	v, _ := newTestVerifier()

	var seen string
	err := v.StreamInterceptor(nil, &fakeServerStream{ctx: ctxWith("app-1", "s3cret")},
		&grpc.StreamServerInfo{}, func(_ any, ss grpc.ServerStream) error {
			seen, _ = appIDFrom(ss.Context())
			return nil
		})
	if err != nil {
		t.Fatalf("合法凭据的流被拒: %v", err)
	}
	if seen != "app-1" {
		t.Fatalf("流 handler 看到的 appId 是 %q，期望 app-1", seen)
	}

	err = v.StreamInterceptor(nil, &fakeServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{}, func(any, grpc.ServerStream) error { return nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("无凭据的流返回 %v，期望 Unauthenticated", status.Code(err))
	}
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }
```

- [ ] **Step 5: 实现认证拦截器**

`internal/grpcapi/auth_interceptor.go`：

```go
package grpcapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/basicfu/fp/internal/domain"
)

// metadata 里携带应用凭据的键。gRPC 要求全部小写。
const (
	mdAppID     = "fp-app-id"
	mdAppSecret = "fp-app-secret"
)

// AppAuthenticator 是拦截器需要的最小能力。*service.ApplicationService 满足它。
type AppAuthenticator interface {
	VerifySecret(ctx context.Context, appID, plainSecret string) (*domain.Application, error)
}

type appIDCtxKey struct{}

// appIDFrom 返回本次调用已认证的 appId。
func appIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(appIDCtxKey{}).(string)
	return v, ok
}

// verifierCacheKey 是凭据缓存的键。
//
// 用 secret 的 SHA-256 而不是 secret 本身：直接拿明文当键功能上完全正确，
// 代价是 appSecret 明文常驻堆内存，崩溃 dump / 调试器 / 内存分析都能读到。
// 键里同时含 appID 与哈希，所以"拿 A 的正确 secret 冒充 B"不会命中缓存。
func verifierCacheKey(appID, secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return appID + ":" + hex.EncodeToString(sum[:])
}

// appVerifier 校验 appId/appSecret，并缓存**成功**的校验结果。
//
// 缓存是必需品不是优化：VerifySecret 内部是 bcrypt，单次 50–100ms，
// 而 ValidateToken 的目标量级是数百 QPS。不缓存的话吞吐直接归零，
// 且不会报任何错——只是慢到不可用。
//
// **只缓存成功。** 缓存键的一半来自调用方，缓存失败结果等于把这个 map
// 的大小交给攻击者：一百万个不同的错误 secret 就能把进程 OOM 掉。
// 只收成功结果的话，键的数量被真实应用数钉死。
//
// 缓存里**不存 *domain.Application**，只存"这对凭据有效"这个事实：
// 缓存一份 Status 快照会让"停用应用"在 TTL 内形同虚设。应用是否可用
// 由 AuthService.activeApp 每次重新判定，保持单一执行点。
type appVerifier struct {
	apps AppAuthenticator
	ttl  time.Duration

	mu    sync.RWMutex
	cache map[string]int64 // key → 过期时刻（UnixMilli）
}

// newAppVerifier 构造 appVerifier。
func newAppVerifier(apps AppAuthenticator, ttl time.Duration) *appVerifier {
	return &appVerifier{apps: apps, ttl: ttl, cache: make(map[string]int64)}
}

// maxCacheEntries 是触发清理过期项的阈值。
//
// 正常情况下缓存大小等于应用数，远低于这个值；能长到这里说明有大量
// appSecret 轮换残留。纯属卫生措施，不是安全边界——安全边界是"不缓存失败"。
const maxCacheEntries = 256

func (v *appVerifier) verify(ctx context.Context, appID, secret string) error {
	if appID == "" || secret == "" {
		return domain.Errorf(domain.ErrInvalidCredential, "缺少 appId 或 appSecret")
	}

	key := verifierCacheKey(appID, secret)
	now := time.Now().UnixMilli()

	v.mu.RLock()
	expiresAt, ok := v.cache[key]
	v.mu.RUnlock()
	if ok && expiresAt > now {
		return nil
	}

	if _, err := v.apps.VerifySecret(ctx, appID, secret); err != nil {
		return err // 失败不入缓存
	}

	v.mu.Lock()
	if len(v.cache) >= maxCacheEntries {
		for k, exp := range v.cache {
			if exp <= now {
				delete(v.cache, k)
			}
		}
	}
	v.cache[key] = now + v.ttl.Milliseconds()
	v.mu.Unlock()
	return nil
}

// authenticate 从 metadata 取凭据、校验，并把 appId 放进 ctx。
func (v *appVerifier) authenticate(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, domain.Errorf(domain.ErrInvalidCredential, "缺少 appId 或 appSecret")
	}
	appID := first(md, mdAppID)
	secret := first(md, mdAppSecret)
	if err := v.verify(ctx, appID, secret); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, appIDCtxKey{}, appID), nil
}

func first(md metadata.MD, key string) string {
	if vs := md.Get(key); len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// UnaryInterceptor 校验一元调用的应用凭据。
func (v *appVerifier) UnaryInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	authed, err := v.authenticate(ctx)
	if err != nil {
		return nil, statusFrom(err)
	}
	return handler(authed, req)
}

// StreamInterceptor 校验流式调用的应用凭据。
//
// 一元和流必须都拦。只拦一元的话 Watch 完全不做认证——任何人连上 :9090
// 就能订阅到全部应用的撤销事件，实时拿到 token 列表。
func (v *appVerifier) StreamInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	authed, err := v.authenticate(ss.Context())
	if err != nil {
		return statusFrom(err)
	}
	return handler(srv, &authedStream{ServerStream: ss, ctx: authed})
}

// authedStream 用带 appId 的 ctx 覆盖流的 Context()。
type authedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authedStream) Context() context.Context { return s.ctx }
```

- [ ] **Step 6: 跑测试**

```bash
./scripts/test.sh ./internal/grpcapi/
```

期望全绿。

- [ ] **Step 7: 提交**

```bash
git add internal/grpcapi
```

```bash
git commit -m "feat(grpcapi): 领域错误映射与应用凭据认证拦截器"
```

---

## Task 5: 四个一元 RPC

实现 `SendLoginCode` / `Login` / `Logout` / `ValidateToken`。它们是纯映射层——取出已认证的 appId、转换消息、调 `service`、转换回来。**任何业务判断出现在这一层都是错的。**

**Files:**
- Create: `internal/grpcapi/auth_service.go`
- Create: `internal/grpcapi/env_test.go`（bufconn 测试装配）
- Create: `internal/grpcapi/auth_service_test.go`

**Interfaces:**
- Consumes: Task 4 的 `appIDFrom` / `statusFrom` / `appVerifier`
- Produces: `grpcapi.NewAuthServer(d AuthServerDeps) fpv1.AuthServiceServer`
- Produces: `grpcapi.AuthServerDeps{ Auth *service.AuthService }`

---

- [ ] **Step 1: 写测试装配**

`internal/grpcapi/env_test.go`。**先读 `internal/service/auth_test.go` 里的 `authEnv`**，本文件是它加上一层 bufconn；能复用的装配步骤照抄，不要另发明一套。

```go
package grpcapi

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/basicfu/fp/internal/domain"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// grpcEnv 是一套跑在 bufconn 上的完整 fp 服务端。
//
// 用 bufconn 而不是真监听端口：不占端口、不受防火墙影响、测试并行时不冲突，
// 而且走的是真实的 gRPC 编解码与拦截器链——比直接调 handler 函数有意义得多，
// 因为 metadata 认证、错误码映射这些恰恰只在真实链路上才会被执行到。
type grpcEnv struct {
	client fpv1.AuthServiceClient
	app    *domain.Application
	appID  string
	secret string

	// 下面这些字段照搬 service 层 authEnv 的装配结果，供测试直接操纵服务端状态。
	auth     *service.AuthService
	sessions *service.SessionService
	accounts *service.AccountService
	users    *service.UserService
	sms      *notify.FakeProvider
	codes    *notify.CodeService
}

func newGRPCEnv(t *testing.T) *grpcEnv {
	t.Helper()

	// …按 internal/service/auth_test.go 的 authEnv 装配 pool / rdb / 各 service，
	// 并创建一个启用了 password 与 sms_code 两种登录方式的应用，
	// 记下它的 appID 与明文 secret（ApplicationService.Create 的第二个返回值）。

	verifier := newAppVerifier(apps, 5*time.Minute)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(verifier.UnaryInterceptor),
		grpc.StreamInterceptor(verifier.StreamInterceptor),
	)
	fpv1.RegisterAuthServiceServer(srv, NewAuthServer(AuthServerDeps{Auth: authSvc}))

	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &grpcEnv{client: fpv1.NewAuthServiceClient(conn), /* … */}
}

// authed 返回带本应用凭据的 ctx。
func (e *grpcEnv) authed(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, mdAppID, e.appID, mdAppSecret, e.secret)
}
```

- [ ] **Step 2: 写 RPC 的失败测试**

`internal/grpcapi/auth_service_test.go`：

```go
package grpcapi

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// TestCacheTTLIsInMilliseconds 是本任务最重要的一个测试。
//
// 服务端的 ValidateResult.CacheTTL 是 time.Duration，proto 字段是毫秒。
// 写成 int64(res.CacheTTL) 会下发纳秒（30 秒变成三百亿），写成
// int64(res.CacheTTL.Seconds()) 会下发 30 而字段名说的是毫秒——
// SDK 于是只缓存 30 毫秒，回源量放大一千倍，而**一切功能都正常**，
// 只有 fp 的负载图会莫名其妙地翻一千倍。没有测试能靠"跑通了"发现它。
func TestCacheTTLIsInMilliseconds(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())

	token := env.loginWithPassword(t, ctx)

	res, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token})
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}

	wantMs := int64(env.app.Session.TokenCacheTTLSeconds) * 1000
	if res.GetCacheTtlMs() != wantMs {
		t.Fatalf("cache_ttl_ms = %d，期望 %d（token_cache_ttl = %d 秒）",
			res.GetCacheTtlMs(), wantMs, env.app.Session.TokenCacheTTLSeconds)
	}
}

// TestCacheTTLIsNeverNegative 守住一个下限。
//
// 负的 TTL 经 SDK 会算出一个"已经过期"的缓存条目——最好的情况是每次都回源，
// 最坏的情况是某个 min/max 比较把它当成"很久以后"。
func TestCacheTTLIsNeverNegative(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	token := env.loginWithPassword(t, ctx)

	res, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token})
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if res.GetCacheTtlMs() < 0 {
		t.Fatalf("cache_ttl_ms 为负: %d", res.GetCacheTtlMs())
	}
}

// TestTokenFromAnotherAppIsRejected 守住跨应用隔离。
//
// appId 来自拦截器写进 ctx 的值，proto 里刻意没有 appId 字段。
// 但 handler 仍可能把空串传给 service（比如忘了取 ctx），那样
// activeApp 会失败——也可能有人"顺手"加个回退。这条测试钉死：
// A 应用签发的 token，拿 B 应用的凭据去校验必须失败。
func TestTokenFromAnotherAppIsRejected(t *testing.T) {
	env := newGRPCEnv(t)
	other := env.newApplication(t) // 同一个 fp，另一个应用

	token := env.loginWithPassword(t, env.authed(context.Background()))

	otherCtx := metadata.AppendToOutgoingContext(context.Background(),
		mdAppID, other.appID, mdAppSecret, other.secret)
	_, err := env.client.ValidateToken(otherCtx, &fpv1.ValidateTokenRequest{Token: token})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("跨应用校验返回 %v，期望 Unauthenticated", status.Code(err))
	}
}

func TestRPCsRequireCredentials(t *testing.T) {
	env := newGRPCEnv(t)
	bare := context.Background()

	calls := map[string]func() error{
		"SendLoginCode": func() error {
			_, err := env.client.SendLoginCode(bare, &fpv1.SendLoginCodeRequest{Phone: "13800138000"})
			return err
		},
		"Login": func() error {
			_, err := env.client.Login(bare, &fpv1.LoginRequest{ConnectorType: "password"})
			return err
		},
		"Logout": func() error {
			_, err := env.client.Logout(bare, &fpv1.LogoutRequest{Token: "x"})
			return err
		},
		"ValidateToken": func() error {
			_, err := env.client.ValidateToken(bare, &fpv1.ValidateTokenRequest{Token: "x"})
			return err
		},
	}
	for name, call := range calls {
		if code := status.Code(call()); code != codes.Unauthenticated {
			t.Errorf("%s 在无凭据时返回 %v，期望 Unauthenticated", name, code)
		}
	}
}

// TestSMSCodeLoginRoundTrip 走一遍发码→登录→校验。
func TestSMSCodeLoginRoundTrip(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	const phone = "13800138000"

	if _, err := env.client.SendLoginCode(ctx, &fpv1.SendLoginCodeRequest{Phone: phone}); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	code := env.sms.LastParam("code")
	if code == "" {
		t.Fatal("假短信供应商没有收到验证码")
	}

	login, err := env.client.Login(ctx, &fpv1.LoginRequest{
		ConnectorType: "sms_code",
		Credentials:   map[string]string{"phone": phone, "code": code},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login.GetToken() == "" || login.GetUser().GetId() == "" {
		t.Fatalf("登录响应缺字段: %+v", login)
	}

	res, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: login.GetToken()})
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if res.GetUserId() != login.GetUser().GetId() {
		t.Fatalf("校验返回的 userId %q 与登录返回的 %q 不一致",
			res.GetUserId(), login.GetUser().GetId())
	}
	if res.GetSessionId() != login.GetSessionId() {
		t.Fatalf("校验返回的 sessionId %q 与登录返回的 %q 不一致",
			res.GetSessionId(), login.GetSessionId())
	}
}

// TestLogoutInvalidatesToken 确认登出真的作废 token。
func TestLogoutInvalidatesToken(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	token := env.loginWithPassword(t, ctx)

	if _, err := env.client.Logout(ctx, &fpv1.LogoutRequest{Token: token}); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("登出后校验返回 %v，期望 Unauthenticated", status.Code(err))
	}
}

// TestRotationIsRelayedOverGRPC 确认轮换字段没在映射层丢掉。
//
// Rotated / NewToken 是两个很容易在"写映射代码"时漏掉的字段——漏掉不报错，
// 只是所有会话在轮换过渡期后集体登出，而那要等到 rotate_interval
// （默认 24 小时）之后才在线上显形。
func TestRotationIsRelayedOverGRPC(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())

	// 把应用改成 1 秒轮换，并让服务端时钟前进越过它。
	env.setRotateInterval(t, 1)
	token := env.loginWithPassword(t, ctx)
	env.clock.Advance(2 * time.Second)

	res, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token})
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if !res.GetRotated() {
		t.Fatal("越过 rotate_interval 后 rotated 仍为 false")
	}
	if res.GetNewToken() == "" {
		t.Fatal("rotated=true 但 new_token 为空——客户端将无从切换，过渡期后登出")
	}
	if res.GetNewToken() == token {
		t.Fatal("new_token 与旧 token 相同")
	}
}

// TestDisabledApplicationIsRejected 确认停用应用在 gRPC 入口同样生效。
//
// Task 4 的凭据缓存刻意只缓存"凭据有效"这个事实、不缓存 Application 对象，
// 就是为了让这条断言成立。谁把 *domain.Application 塞进缓存，
// 停用应用会在 TTL（5 分钟）内继续放行，而这条测试会失败。
func TestDisabledApplicationIsRejected(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	token := env.loginWithPassword(t, ctx)

	env.disableApplication(t)

	if _, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("停用应用后校验返回 %v，期望 PermissionDenied", status.Code(err))
	}
}
```

> 辅助方法 `loginWithPassword` / `newApplication` / `setRotateInterval` / `disableApplication` / `clock`
> 放在 `env_test.go` 里。`disableApplication` 目前只能直接改库——第一阶段没有停用应用的
> 管理接口（这是已知交接项，见设计文档附录）。

- [ ] **Step 3: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/grpcapi/
```

期望：`undefined: NewAuthServer`。

- [ ] **Step 4: 实现 RPC**

`internal/grpcapi/auth_service.go`：

```go
package grpcapi

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// AuthServerDeps 是 gRPC 认证服务的依赖。
type AuthServerDeps struct {
	Auth *service.AuthService
}

// NewAuthServer 构造 gRPC 认证服务。
func NewAuthServer(d AuthServerDeps) fpv1.AuthServiceServer {
	return &authServer{auth: d.Auth}
}

type authServer struct {
	fpv1.UnimplementedAuthServiceServer
	auth *service.AuthService
}

// callerAppID 取出拦截器已认证的 appId。
//
// 取不到只可能是拦截器没挂上——那是装配错误，不是调用方的错，
// 所以返回 Internal 而不是 Unauthenticated：后者会让人以为是凭据问题，
// 排障方向直接跑偏。
func callerAppID(ctx context.Context) (string, error) {
	appID, ok := appIDFrom(ctx)
	if !ok || appID == "" {
		return "", status.Error(codes.Internal, "internal error")
	}
	return appID, nil
}

func (s *authServer) SendLoginCode(ctx context.Context, req *fpv1.SendLoginCodeRequest) (*fpv1.SendLoginCodeResponse, error) {
	appID, err := callerAppID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.auth.SendLoginCode(ctx, appID, req.GetPhone()); err != nil {
		return nil, statusFrom(err)
	}
	return &fpv1.SendLoginCodeResponse{}, nil
}

func (s *authServer) Login(ctx context.Context, req *fpv1.LoginRequest) (*fpv1.LoginResponse, error) {
	appID, err := callerAppID(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.auth.Login(ctx, service.LoginInput{
		AppID:         appID,
		ConnectorType: req.GetConnectorType(),
		Credentials:   connector.Credentials(req.GetCredentials()),
		IP:            req.GetIp(),
		UA:            req.GetUserAgent(),
		Mobile:        req.GetMobile(),
	})
	if err != nil {
		return nil, statusFrom(err)
	}
	return &fpv1.LoginResponse{
		Token:     res.Session.Token,
		SessionId: res.Session.ID,
		User:      userInfo(res.User),
	}, nil
}

func (s *authServer) Logout(ctx context.Context, req *fpv1.LogoutRequest) (*fpv1.LogoutResponse, error) {
	if _, err := callerAppID(ctx); err != nil {
		return nil, err
	}
	if err := s.auth.Logout(ctx, req.GetToken()); err != nil {
		return nil, statusFrom(err)
	}
	return &fpv1.LogoutResponse{}, nil
}

func (s *authServer) ValidateToken(ctx context.Context, req *fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
	appID, err := callerAppID(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.auth.ValidateToken(ctx, appID, req.GetToken())
	if err != nil {
		return nil, statusFrom(err)
	}
	return &fpv1.ValidateTokenResponse{
		UserId:    res.Session.UserID.String(),
		SessionId: res.Session.ID,
		// Milliseconds() 对 time.Duration 是整除，天然向下取整。
		// 绝不能改成基于 Seconds() 的四舍五入——那会把 800ms 进位成 1s，
		// 重新引入 min(配置, 剩余) 本来要防的过度缓存。
		CacheTtlMs: res.CacheTTL.Milliseconds(),
		Rotated:    res.Rotated,
		NewToken:   res.NewToken,
	}, nil
}

// userInfo 把领域用户转成传输对象。
func userInfo(u *domain.User) *fpv1.UserInfo {
	if u == nil {
		return nil
	}
	return &fpv1.UserInfo{
		Id:        u.ID.String(),
		Nickname:  u.Nickname,
		AvatarUrl: u.AvatarURL,
		Gender:    u.Gender,
		Status:    u.Status,
	}
}
```

- [ ] **Step 5: 跑测试**

```bash
./scripts/test.sh ./internal/grpcapi/
```

- [ ] **Step 6: 提交**

```bash
git add internal/grpcapi
```

```bash
git commit -m "feat(grpcapi): 发码/登录/登出/校验四个一元 RPC"
```

---

## Task 6: Watch 流——撤销事件中继

一个 fp 进程可能同时挂着几十条 SDK 的 `Watch` 流。**Redis 订阅只开一份**，在进程内扇出给所有流。每条流一份订阅意味着 N 条 Redis 连接、N 份重复的反序列化，且 `RevokePublisher.Subscribe` 的每一次泄漏都是一个 goroutine 加一条连接。

**Files:**
- Create: `internal/grpcapi/watch.go`
- Create: `internal/grpcapi/watch_test.go`
- Modify: `internal/store/revoke.go`（`Subscribe` 改为投递 `RevokeSignal`）
- Modify: `internal/store/revoke_test.go`

**Interfaces:**
- Changes: `store.RevokePublisher.Subscribe(ctx) (<-chan store.RevokeSignal, func(), error)`
- Produces: `store.RevokeSignal{Kind, Event}` 与 `store.RevokeSignalEvent` / `store.RevokeSignalGap`
- Produces: `grpcapi.NewRevokeHub(pub *store.RevokePublisher) *RevokeHub`
- Produces: `(*RevokeHub).Run(ctx) error`——阻塞式，进程生命周期内运行一次
- Produces: `(*RevokeHub).Subscribe(appID uuid.UUID) (<-chan HubEvent, func())`
- Produces: `grpcapi.HubEvent{Purge bool, Reason string, Revoke domain.RevokeEvent}`
- Changes: `AuthServerDeps` 增加 `Hub *RevokeHub`、`Apps AppLookup`

---

- [ ] **Step 1: 明确四条规则**

实现前先记住，它们各自对应一类静默失效：

1. **`AppID == uuid.Nil` 的事件必须推给所有订阅者。** 改密和冻结是跨应用撤销，服务端把 `Nil` 当成"某个应用 ID"去做等值过滤的话，这两类撤销**一条也推不出去**——而按应用过滤的单点踢下线照常工作，所以测试很容易只覆盖后者
2. **订阅者的 channel 必须有缓冲，且满了要丢弃而不是阻塞。** 一条卡住的 gRPC 流会把整个中继堵死，进而拖垮所有其他流的推送。丢弃是安全的：推送只是**加速**，丢了最长 `cache_ttl` 后回源照样会拒
3. **每条流退出时必须注销。** 漏掉的话 hub 的订阅者表只增不减，每条断开的流永久占一个 channel
4. **Redis 订阅重建必须转成一次全量 `Purge`。** 见下一步——这是本任务里唯一一处"修的不是丢事件本身，而是丢事件不可观测"

- [ ] **Step 2: 把订阅重建变成可观测的信号**

### 问题

`RevokePublisher.Subscribe` 目前用 go-redis 的 `Channel()`。连接抖动时 go-redis 会**静默重连并重发 `SUBSCRIBE`**：既不关闭 channel，也不返回错误。那个窗口里发布的撤销事件对本实例**永久丢失**，而 `RevokeHub.Run` 一无所知，SDK 那边看到的流也一直是健康的——**所有人都以为推送在正常工作。**

丢事件本身有兜底（最长 `cache_ttl` 后回源被拒），真正的问题是**它不可观测**。

### 方案

`Channel()` 换成 `ChannelWithSubscriptions()`，它除了 `*Message` 还会投递 `*Subscription`。go-redis 的 `resubscribe()` 在每次重连时重发 `SUBSCRIBE`，Redis 的回包会被解析成一条 `*Subscription{Kind:"subscribe"}` 送上来。

**第一条 `subscribe` 是初次订阅，第二条起就是重连。**

`internal/store/revoke.go` 改动：

```go
// RevokeSignalKind 区分事件流上的两类信号。
type RevokeSignalKind int

const (
	// RevokeSignalEvent 是一条撤销事件。
	RevokeSignalEvent RevokeSignalKind = iota
	// RevokeSignalGap 表示事件流出现了缺口：订阅刚刚重建，
	// 期间发布的事件已永久丢失，且无法知道丢了哪些。
	RevokeSignalGap
)

// RevokeSignal 是订阅流上的一条信号。
//
// 命名取"缺口"而非"重订阅"，是因为消费方关心的是**后果**不是成因。
// 将来若换成 Redis Stream 等别的承载方式，这个信号的含义原样成立。
type RevokeSignal struct {
	Kind RevokeSignalKind
	// Event 仅在 Kind == RevokeSignalEvent 时有效。
	Event domain.RevokeEvent
}

func (p *RevokePublisher) Subscribe(ctx context.Context) (<-chan RevokeSignal, func(), error) {
	sub := p.rdb.Subscribe(ctx, revokeChannel)
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("store: 订阅撤销频道: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	go func() {
		<-ctx.Done()
		_ = sub.Close()
	}()

	out := make(chan RevokeSignal, 64)
	go func() {
		defer close(out)

		// resubscribeCount 数的是本循环观察到的 SUBSCRIBE 确认回包，
		// 也就是 go-redis 重连的次数。
		//
		// **这里能看到的每一条都必然来自重连。** 真正"初次订阅"的那一条确认
		// 已经被上面 sub.Receive(ctx) 那次同步调用读走了——那正是它存在的
		// 意义：阻塞到订阅被确认，Subscribe 才能安全返回。它不会再流到这个
		// channel 里。
		//
		// 所以**不要**在这里写"第一条是初次订阅、跳过"。那样会把第一次真实
		// 重连当成初次订阅放过，一条 Gap 都不发——恰好是本机制要防的那类失效，
		// 而且只在第一次抖动时发作，之后又"恢复正常"，极难归因。
		//
		// 这是察觉订阅重建的唯一途径：没有错误、没有关闭的 channel，只有这个计数。
		resubscribeCount := 0

		for msg := range sub.ChannelWithSubscriptions() {
			var sig RevokeSignal
			switch m := msg.(type) {
			case *redis.Subscription:
				if m.Kind != "subscribe" {
					continue
				}
				resubscribeCount++
				slog.Warn("store: Redis 订阅已重建，期间的撤销事件已丢失",
					"resubscribeCount", resubscribeCount)
				sig = RevokeSignal{Kind: RevokeSignalGap}
			case *redis.Message:
				var ev domain.RevokeEvent
				if err := json.Unmarshal([]byte(m.Payload), &ev); err != nil {
					slog.Error("store: 解析撤销事件失败", "err", err)
					continue
				}
				sig = RevokeSignal{Kind: RevokeSignalEvent, Event: ev}
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

> **`ChannelWithSubscriptions()` 与 `Channel()` 互斥**——go-redis 里先调 `Channel()` 再调它会 panic。确保没有别处还在用 `Channel()`。

对应的 `internal/store/revoke_test.go` 要加一条：

```go
// TestSubscribeSurfacesResubscribeAsGap 守住"丢事件必须可观测"。
//
// go-redis 会在连接抖动时静默重连并重发 SUBSCRIBE，既不报错也不关 channel。
// 没有这个信号的话，丢事件这件事在整个系统里不留任何痕迹——
// 日志里没有、监控里没有、SDK 看到的流状态也一切正常。
//
// 制造重连的方式：用 CLIENT KILL 掐掉订阅连接（拿 CLIENT LIST 找到它），
// 或用一个可控的中间代理断开底层 TCP。断开后必须在合理时间内收到一条
// Kind == RevokeSignalGap。
func TestSubscribeSurfacesResubscribeAsGap(t *testing.T) { … }
```

- [ ] **Step 3: 写 hub 的失败测试**

`internal/grpcapi/watch_test.go`：

```go
package grpcapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

// TestGlobalRevokeReachesEveryApp 守住规则 1。
//
// 改密与冻结产生的事件 AppID 是 uuid.Nil，含义是"跨全部应用"。
// 把它当成一个具体应用 ID 去做等值过滤，这两类撤销一条也推不出去——
// 而按应用过滤的单点踢下线照常工作，所以只测踢下线的话这个 bug 完全隐形。
// 后果：管理员改了密码，被盗用的会话在 SDK 缓存里继续畅通一整个 cache_ttl。
func TestGlobalRevokeReachesEveryApp(t *testing.T) {
	hub, pub := newTestHub(t)
	appA, appB := uuid.New(), uuid.New()

	chA, closeA := hub.Subscribe(appA)
	defer closeA()
	chB, closeB := hub.Subscribe(appB)
	defer closeB()

	pub.Publish(context.Background(), domain.RevokeEvent{
		Tokens: []string{"tok"},
		UserID: uuid.New(),
		AppID:  uuid.Nil, // 跨应用
		Reason: domain.RevokeReasonPasswordChanged,
	})

	for name, ch := range map[string]<-chan HubEvent{"A": chA, "B": chB} {
		select {
		case ev := <-ch:
			if len(ev.Revoke.Tokens) != 1 {
				t.Fatalf("应用 %s 收到的事件 token 数为 %d", name, len(ev.Revoke.Tokens))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("应用 %s 没有收到跨应用撤销事件", name)
		}
	}
}

// TestScopedRevokeDoesNotLeakToOtherApps 是规则 1 的另一半。
//
// 少了它，一个"全部事件推给全部订阅者"的实现也能让上面那条通过——
// 而那意味着 A 应用能实时看到 B 应用的 token 列表。
func TestScopedRevokeDoesNotLeakToOtherApps(t *testing.T) {
	hub, pub := newTestHub(t)
	appA, appB := uuid.New(), uuid.New()

	chA, closeA := hub.Subscribe(appA)
	defer closeA()
	chB, closeB := hub.Subscribe(appB)
	defer closeB()

	pub.Publish(context.Background(), domain.RevokeEvent{
		Tokens: []string{"tok"}, UserID: uuid.New(), AppID: appA,
		Reason: domain.RevokeReasonKick,
	})

	select {
	case <-chA:
	case <-time.After(2 * time.Second):
		t.Fatal("目标应用没收到事件")
	}
	select {
	case ev := <-chB:
		t.Fatalf("其他应用收到了不属于它的撤销事件，泄露了 token: %v", ev.Revoke.Tokens)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestSlowSubscriberDoesNotBlockOthers 守住规则 2。
//
// 一条卡住的 gRPC 流（客户端进程被 SIGSTOP、网络黑洞、对端不读）会让写入
// 阻塞。若中继是同步写，这一条流就会把整个扇出堵死，所有其他应用的推送
// 全部停摆——一个客户端的故障演变成全平台的撤销推送失效。
func TestSlowSubscriberDoesNotBlockOthers(t *testing.T) {
	hub, pub := newTestHub(t)
	slowApp, fastApp := uuid.New(), uuid.New()

	// 订阅但**不读**，把它的缓冲撑满。
	_, closeSlow := hub.Subscribe(slowApp)
	defer closeSlow()
	fast, closeFast := hub.Subscribe(fastApp)
	defer closeFast()

	ctx := context.Background()
	for i := 0; i < revokeBufferSize*3; i++ {
		pub.Publish(ctx, domain.RevokeEvent{
			Tokens: []string{"tok"}, UserID: uuid.New(), AppID: slowApp,
			Reason: domain.RevokeReasonKick,
		})
	}
	// 慢订阅者已经溢出；此时给快订阅者发一条，必须照常送达。
	pub.Publish(ctx, domain.RevokeEvent{
		Tokens: []string{"live"}, UserID: uuid.New(), AppID: fastApp,
		Reason: domain.RevokeReasonKick,
	})

	select {
	case ev := <-fast:
		if ev.Revoke.Tokens[0] != "live" {
			t.Fatalf("快订阅者收到的是 %v", ev.Revoke.Tokens)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("一个卡住的订阅者把整个中继堵死了")
	}
}

// TestUnsubscribeRemovesSubscriber 守住规则 3。
func TestUnsubscribeRemovesSubscriber(t *testing.T) {
	hub, _ := newTestHub(t)
	appID := uuid.New()

	before := hub.subscriberCount()
	_, cancel := hub.Subscribe(appID)
	if hub.subscriberCount() != before+1 {
		t.Fatalf("Subscribe 后订阅者数为 %d，期望 %d", hub.subscriberCount(), before+1)
	}
	cancel()
	if got := hub.subscriberCount(); got != before {
		t.Fatalf("注销后订阅者数为 %d，期望回到 %d——每条断开的流都会永久占一个槽", got, before)
	}
	// 重复注销不应 panic 或把别人的槽删掉。
	cancel()
	if got := hub.subscriberCount(); got != before {
		t.Fatalf("重复注销后订阅者数变成 %d", got)
	}
}

// TestGapBecomesPurgeForEverySubscriber 守住 Step 2 的整条链路。
//
// Redis 订阅重建 → store 发出 RevokeSignalGap → hub 转成 Purge → 推给
// **所有**订阅者。少了最后一环，前面检测到重连也白搭。
//
// 断言"所有订阅者"而不是"某个订阅者"：触发 purge 的前提是不知道丢了
// 哪些事件，因而也不知道涉及哪些应用。按 appID 过滤 purge 是在没有依据的
// 情况下缩小范围——那种实现只测一个订阅者时完全看不出问题。
func TestGapBecomesPurgeForEverySubscriber(t *testing.T) {
	hub, signals := newTestHubWithFakeSignals(t) // 直接投喂信号，不依赖真实 Redis 断连
	appA, appB := uuid.New(), uuid.New()

	chA, closeA := hub.Subscribe(appA)
	defer closeA()
	chB, closeB := hub.Subscribe(appB)
	defer closeB()

	signals <- store.RevokeSignal{Kind: store.RevokeSignalGap}

	for name, ch := range map[string]<-chan HubEvent{"A": chA, "B": chB} {
		select {
		case ev := <-ch:
			if !ev.Purge {
				t.Fatalf("订阅者 %s 收到的不是 purge：%+v", name, ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("订阅者 %s 没有收到 purge——"+
				"订阅重建被检测到了，但没转成对 SDK 的指令", name)
		}
	}
}

// TestPurgeClosesStreamWhenBufferFull 守住"purge 不能被静默丢弃"。
//
// fanout 丢一条撤销是安全的（那个 token 最多多活一个 cache_ttl），
// 但丢一条 purge 会让 SDK 的缓存停在一个**已知不可信**的状态，
// 而且没有任何后续机制会纠正它。所以缓冲满时必须关掉这条流，
// 逼 SDK 重连并自行 purge。
//
// 复用 fanout 的 default 分支（直接丢弃）能让上一条测试通过，
// 却在这里失败——这正是两者代价不对等的地方。
func TestPurgeClosesStreamWhenBufferFull(t *testing.T) {
	hub, signals := newTestHubWithFakeSignals(t)
	appID := uuid.New()

	ch, cancel := hub.Subscribe(appID)
	defer cancel()

	// 不读，把缓冲灌满。
	for i := 0; i < revokeBufferSize+10; i++ {
		signals <- store.RevokeSignal{Kind: store.RevokeSignalEvent, Event: domain.RevokeEvent{
			Tokens: []string{"tok"}, UserID: uuid.New(), AppID: appID,
			Reason: domain.RevokeReasonKick,
		}}
	}
	signals <- store.RevokeSignal{Kind: store.RevokeSignalGap}

	// 排空缓冲，最终必须读到 channel 关闭，而不是一直读到耗尽后阻塞。
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel 已关闭，符合预期
			}
		case <-deadline:
			t.Fatal("缓冲满时 purge 被静默丢弃了——" +
				"SDK 的缓存会停在一个已知不可信的状态，且不会有任何机制纠正")
		}
	}
}

// TestHubUsesExactlyOneRedisSubscription 守住"只开一份订阅"。
//
// 每条流一份 Redis 订阅在功能上完全正确，只是把连接数变成流数的倍数。
// 断言写成"多个订阅者只对应一次 Subscribe 调用"。
func TestHubUsesExactlyOneRedisSubscription(t *testing.T) {
	hub, _ := newTestHub(t)
	for i := 0; i < 5; i++ {
		_, cancel := hub.Subscribe(uuid.New())
		defer cancel()
	}
	if got := hub.redisSubscribeCalls(); got != 1 {
		t.Fatalf("5 个订阅者触发了 %d 次 Redis 订阅，期望 1 次", got)
	}
}
```

> **两个装配辅助，用途不同，都要有：**
>
> - `newTestHub(t)` 返回一个已 `Run` 起来的 hub 与它背后的**真实** `*store.RevokePublisher`。
>   验证"事件真的经过 Redis 走了一遭"的测试用它。
> - `newTestHubWithFakeSignals(t)` 返回 hub 与一个可以直接投喂 `store.RevokeSignal`
>   的 channel，**绕开 Redis**。验证 hub 自身分发逻辑（尤其是 Gap → Purge）的测试用它——
>   靠真实断连来制造 `RevokeSignalGap` 既慢又不稳定，而这里要测的是 hub 收到信号之后
>   做了什么，不是信号怎么产生的。信号怎么产生由 Step 2 的
>   `TestSubscribeSurfacesResubscribeAsGap` 单独验证。
>
> 为此 `RevokeHub` 需要一个包内的构造入口，能接一个现成的 signal channel 而不是
> 自己去 `pub.Subscribe`。把 `Run` 拆成 `Run(ctx)`（真订阅）与
> `run(ctx, <-chan store.RevokeSignal)`（纯分发循环）即可，前者调后者。
>
> `subscriberCount` / `redisSubscribeCalls` 也是包内测试辅助（小写，仅测试用），
> 后者用一个计数装饰器包住 `Subscribe`。
>
> 两个辅助都用 `t.Cleanup` 取消 hub 的 ctx。

- [ ] **Step 4: 跑测试确认失败**

```bash
./scripts/test.sh ./internal/grpcapi/ -run 'TestGlobalRevoke|TestScopedRevoke|TestSlowSubscriber|TestUnsubscribe|TestHubUses'
```

- [ ] **Step 5: 实现 RevokeHub**

`internal/grpcapi/watch.go`：

```go
package grpcapi

import (
	"context"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// revokeBufferSize 是每个订阅者的缓冲深度。
//
// 撤销是低频事件（管理员操作、用户登出），缓冲主要用来吸收 gRPC 写入的
// 瞬时抖动，不需要很深。满了就丢——见 fanout 的说明。
const revokeBufferSize = 64

// RevokeHub 把一份 Redis 撤销订阅扇出给进程内所有 Watch 流。
//
// 只开一份订阅：每条流各订一份的话，连接数与反序列化开销都随流数线性增长，
// 而它们收到的是完全相同的消息。
type RevokeHub struct {
	pub *store.RevokePublisher

	mu   sync.RWMutex
	next uint64
	subs map[uint64]*revokeSub
}

// HubEvent 是推给一条 Watch 流的消息。
type HubEvent struct {
	// Purge 为 true 时要求 SDK 丢弃**全部**缓存，此时 Revoke 字段无意义。
	Purge  bool
	Reason string
	Revoke domain.RevokeEvent
}

type revokeSub struct {
	appID uuid.UUID
	ch    chan HubEvent
}

// NewRevokeHub 构造 RevokeHub。
func NewRevokeHub(pub *store.RevokePublisher) *RevokeHub {
	return &RevokeHub{pub: pub, subs: make(map[uint64]*revokeSub)}
}

// Run 订阅 Redis 并持续扇出，直到 ctx 取消。整个进程只调用一次。
//
// ctx 必须是可取消的：Subscribe 内部虽已派生子 ctx 并由 closeFn 兜底，
// 但这里的 defer 是唯一保证进程退出时那条 Redis 连接被关掉的地方。
func (h *RevokeHub) Run(ctx context.Context) error {
	signals, closeFn, err := h.pub.Subscribe(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	for {
		select {
		case <-ctx.Done():
			return nil
		case sig, ok := <-signals:
			if !ok {
				return nil
			}
			switch sig.Kind {
			case store.RevokeSignalGap:
				// 订阅刚刚重建，期间的事件已永久丢失，且不知道丢了哪些。
				// 唯一安全的动作是让本实例下所有 SDK 清空缓存。
				h.broadcastPurge("redis 订阅重建")
			default:
				h.fanout(sig.Event)
			}
		}
	}
}

// fanout 把一条事件分发给匹配的订阅者。
//
// 写入用非阻塞 select：一条卡住的 gRPC 流（客户端被 SIGSTOP、网络黑洞、
// 对端不读）会让写入永久阻塞，同步写就会把整个中继堵死——一个客户端的
// 故障演变成全平台推送失效。
//
// 丢弃是安全的：推送只是把撤销延迟从 cache_ttl 压到近乎实时的**加速手段**，
// 权威撤销早已通过删除 Redis 会话完成。丢一条的后果是那个 SDK 最长
// cache_ttl 之后回源被拒——正是没有推送时的行为。
func (h *RevokeHub) fanout(ev domain.RevokeEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.subs {
		// AppID 为 Nil 表示跨全部应用（改密、冻结）——必须推给所有人。
		// 把 Nil 当成一个具体应用 ID 去等值比较，这两类撤销一条也推不出去。
		if ev.AppID != uuid.Nil && ev.AppID != sub.appID {
			continue
		}
		select {
		case sub.ch <- HubEvent{Revoke: ev}:
		default:
			slog.Warn("grpcapi: 撤销事件被丢弃，订阅者缓冲已满",
				"appId", sub.appID, "reason", ev.Reason)
		}
	}
}

// broadcastPurge 让本实例下的**全部**订阅者清空缓存。
//
// 刻意不按 appID 过滤：触发它的前提正是"不知道丢了哪些事件"，
// 自然也不知道涉及哪些应用。按应用过滤等于在没有依据的情况下缩小范围。
//
// 范围仅限本实例：其他 fp 实例的订阅没有中断过，它们的 SDK 不需要清缓存。
// 爆炸半径就是真正丢了事件的那一台。
//
// 缓冲满时**不能**像 fanout 那样直接丢弃。两者的代价不对等：丢一条撤销
// 只让一个 token 多活一个 cache_ttl，丢一条 purge 会让整个 SDK 的缓存
// 停在一个**已知不可信**的状态，而且没有任何后续机制会纠正它。
//
// 所以改为关掉那条流。这不激进——撤销频率约每秒 0.1 次，填满 64 格缓冲
// 需要十分钟不读，而 keepalive 十秒就该把这样的连接判死了。关流之后 SDK
// 会重连并 purge（Task 10 已有），复用现成机制，不必新造一套补发逻辑。
func (h *RevokeHub) broadcastPurge(reason string) {
	// 写锁：下面可能要删订阅者。purge 罕见，锁的粒度不重要。
	h.mu.Lock()
	defer h.mu.Unlock()

	slog.Warn("grpcapi: 广播缓存清空指令", "reason", reason, "subscribers", len(h.subs))
	for id, sub := range h.subs {
		select {
		case sub.ch <- HubEvent{Purge: true, Reason: reason}:
		default:
			// Go 允许 range 期间 delete。关掉 channel 会让 Watch 走到
			// !ok 分支正常返回；它 deferred 的注销函数再 delete 一次是空操作。
			// 这里先从 map 摘掉，Close() 就不会二次关闭同一个 channel。
			slog.Warn("grpcapi: 订阅者缓冲已满且需要 purge，关闭该流", "appId", sub.appID)
			close(sub.ch)
			delete(h.subs, id)
		}
	}
}

// Subscribe 登记一个订阅者。返回的函数必须被调用，否则订阅者永久驻留。
func (h *RevokeHub) Subscribe(appID uuid.UUID) (<-chan HubEvent, func()) {
	sub := &revokeSub{appID: appID, ch: make(chan HubEvent, revokeBufferSize)}

	h.mu.Lock()
	h.next++
	id := h.next
	h.subs[id] = sub
	h.mu.Unlock()

	var once sync.Once
	return sub.ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, id)
			h.mu.Unlock()
		})
	}
}
```

**注意 `once`：** 注销函数会被 `defer` 调用，也可能在错误分支里再调一次。没有 `once` 的话，第二次 `delete` 虽然无害，但如果将来改成"关闭 channel"就会 panic。这里花一行把它做成幂等。

- [ ] **Step 6: 实现 Watch RPC**

在 `auth_service.go` 里给 `authServer` 加 `hub` 与 `apps` 字段（`AppLookup` 是取应用内部 UUID 的窄接口），并实现：

```go
// AppLookup 按对外 appId 取应用。用于把 metadata 里的 appId 换成内部 UUID。
type AppLookup interface {
	GetByAppID(ctx context.Context, appID string) (*domain.Application, error)
}

// Watch 把撤销事件推给 SDK。
//
// 流的生命周期就是订阅的生命周期。返回即注销——中继不需要知道流为什么结束。
func (s *authServer) Watch(stream grpc.BidiStreamingServer[fpv1.WatchRequest, fpv1.WatchResponse]) error {
	ctx := stream.Context()
	appIDStr, err := callerAppID(ctx)
	if err != nil {
		return err
	}
	app, err := s.apps.GetByAppID(ctx, appIDStr)
	if err != nil {
		return statusFrom(err)
	}

	// 先订阅再发 ready：反过来的话，客户端收到 ready 就认为推送通道健康、
	// 从而放宽本地缓存窗口，而此刻服务端还没订上，这段时间的撤销全丢。
	events, unsubscribe := s.hub.Subscribe(app.ID)
	defer unsubscribe()

	if err := stream.Send(&fpv1.WatchResponse{
		Event: &fpv1.WatchResponse_Ready{Ready: &fpv1.WatchReady{}},
	}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				// hub 关闭（进程退出），或本订阅者因缓冲满且需要 purge
				// 而被摘掉。两种情况都是正常结束——SDK 会重连，
				// 重连后按 Task 10 的设计自行 purge。
				return nil
			}
			var msg *fpv1.WatchResponse
			if ev.Purge {
				msg = &fpv1.WatchResponse{
					Event: &fpv1.WatchResponse_Purge{
						Purge: &fpv1.WatchPurge{Reason: ev.Reason},
					},
				}
			} else {
				msg = &fpv1.WatchResponse{
					Event: &fpv1.WatchResponse_Revoke{Revoke: revokeEvent(ev.Revoke)},
				}
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// revokeEvent 把领域事件转成传输对象。
func revokeEvent(ev domain.RevokeEvent) *fpv1.RevokeEvent {
	out := &fpv1.RevokeEvent{
		Tokens:  ev.Tokens,
		UserIds: make([]string, 0, len(ev.UserIDs)),
		Reason:  ev.Reason,
		AtMs:    ev.At,
	}
	for _, id := range ev.UserIDs {
		out.UserIds = append(out.UserIds, id.String())
	}
	// uuid.Nil 表示跨全部应用，映射成空串——绝不能写成 "00000000-0000-…"，
	// SDK 那边会把它当成一个真实的应用 ID。
	if ev.AppID != uuid.Nil {
		out.AppId = ev.AppID.String()
	}
	return out
}
```

**上行方向刻意不读。** `WatchRequest` 目前没有任何字段，服务端不需要 `stream.Recv()`。双向流保留上行是为将来订阅更多事件类型预留——现在读它只会多一个 goroutine 和一份退出协调。

- [ ] **Step 7: 补一条流式的端到端测试**

在 `auth_service_test.go` 追加：

```go
// TestWatchDeliversRevokeToClient 走一遍真实的流。
//
// 前面的 hub 测试都在进程内直接读 channel，绕过了 gRPC 编解码与
// oneof 的封装。这条测试确认 ready 与 revoke 两种事件都能正确到达对端。
func TestWatchDeliversRevokeToClient(t *testing.T) {
	env := newGRPCEnv(t)
	ctx, cancel := context.WithTimeout(env.authed(context.Background()), 10*time.Second)
	defer cancel()

	token := env.loginWithPassword(t, ctx)

	stream, err := env.client.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("首条消息: %v", err)
	}
	if first.GetReady() == nil {
		t.Fatalf("首条消息不是 ready: %+v", first)
	}

	// ready 之后再触发撤销，确保不是靠时序侥幸。
	if _, err := env.client.Logout(ctx, &fpv1.LogoutRequest{Token: token}); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("撤销消息: %v", err)
	}
	rev := msg.GetRevoke()
	if rev == nil {
		t.Fatalf("第二条消息不是 revoke: %+v", msg)
	}
	if len(rev.GetTokens()) == 0 || rev.GetTokens()[0] != token {
		t.Fatalf("撤销事件里的 token 是 %v，期望包含 %q", rev.GetTokens(), token)
	}
}
```

- [ ] **Step 8: 跑测试**

```bash
./scripts/test.sh ./internal/grpcapi/
```

- [ ] **Step 9: 提交**

```bash
git add internal/grpcapi internal/store
```

```bash
git commit -m "feat(grpcapi): Watch 双向流、撤销中继与订阅重建检测"
```

---

## Task 7: 接进 main.go——与 HTTP 并存、一起优雅关闭

**Files:**
- Create: `internal/grpcapi/server.go`
- Modify: `cmd/fp/main.go`
- Test: `internal/grpcapi/server_test.go`

**Interfaces:**
- Produces: `grpcapi.New(d Deps) *grpcapi.Server`
- Produces: `(*Server).Serve(lis net.Listener) error` / `(*Server).Shutdown(ctx context.Context)`

---

### 一个必须先知道的陷阱：`GracefulStop` 会永远挂住

`grpc.Server.GracefulStop()` 等待所有进行中的 RPC 结束。**`Watch` 是一条永不主动结束的长流**——它只在 ctx 取消或事件 channel 关闭时返回，而 `GracefulStop` 两者都不触发。直接调用会让进程永远停不下来。

关闭顺序因此是死的：

1. **先关 hub**，关闭全部订阅者 channel → 所有 `Watch` handler 返回
2. **再 `GracefulStop`**，等待进行中的一元 RPC 收尾
3. **超时兜底 `Stop()`**

顺序反了就是挂死。

---

- [ ] **Step 1: 给 RevokeHub 加 Close**

`internal/grpcapi/watch.go` 追加：

```go
// Close 关闭全部订阅者 channel，让所有 Watch handler 返回。
//
// 它存在的唯一理由是让 grpc.Server.GracefulStop 能够返回：Watch 是永不
// 主动结束的长流，不关掉订阅就没有任何机制能让那些 handler 退出，
// 进程会在关闭阶段永远挂住。
//
// 关闭在写锁下进行，与 fanout 的读锁互斥，因此不会出现"向已关闭的
// channel 发送"。之后 Subscribe 返回一个已关闭的 channel，调用方
// （Watch）会立刻走到 !ok 分支正常返回。
func (h *RevokeHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.subs {
		close(sub.ch)
	}
	h.subs = nil
	h.closed = true
}
```

`Subscribe` 开头加：

```go
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		ch := make(chan HubEvent)
		close(ch)
		return ch, func() {}
	}
```

`RevokeHub` 结构体加 `closed bool`。

> 注销函数里的 `delete(h.subs, id)` 在 `h.subs` 已被置 nil 时是空操作——
> Go 允许对 nil map 执行 delete。不需要额外判断。

- [ ] **Step 2: 写服务端生命周期测试**

`internal/grpcapi/server_test.go`：

```go
package grpcapi

import (
	"context"
	"testing"
	"time"
)

// TestShutdownCompletesWithOpenWatchStream 守住关闭顺序。
//
// Watch 是永不主动结束的长流。先 GracefulStop 再关 hub 的话，
// GracefulStop 会等一条永远不返回的 RPC——进程在收到 SIGTERM 后
// 永远停不下来，只能被 SIGKILL。容器环境里表现为每次滚动更新都要
// 等满终止宽限期，且旧实例在此期间仍持有连接。
func TestShutdownCompletesWithOpenWatchStream(t *testing.T) {
	env := newGRPCEnv(t)
	ctx, cancel := context.WithCancel(env.authed(context.Background()))
	defer cancel()

	stream, err := env.client.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if _, err := stream.Recv(); err != nil { // 等 ready，确保流真的建起来了
		t.Fatalf("等待 ready: %v", err)
	}

	done := make(chan struct{})
	go func() {
		// 这里**不能**设一个短于外层断言的超时。设 5 秒的话，顺序反了时
		// Shutdown 自己的超时兜底会调 Stop() 强行推平，done 在 5 秒左右就关闭，
		// 远早于外层 10 秒的上限——顺序错误被掩盖成"慢一点但成功"，测试照常绿。
		// 实测对照：超时设 5 秒时反序变异 5.27s 通过；改成不设超时后 10.22s 失败。
		shutdownCtx, c := context.WithCancel(context.Background())
		defer c()
		env.server.Shutdown(shutdownCtx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("有 Watch 流打开时关闭挂死了")
	}
}
```

> **这条测试守不住关闭顺序，别指望它。** 把 `hub.Close()` 与 `GracefulStop()`
> 调换之后它**不会红**：`Shutdown` 自己的超时兜底会调 `Stop()` 强行推平，
> `done` 仍然在 5 秒左右关闭，远早于这里 10 秒的上限。顺序错误于是被掩盖成
> "慢一点但成功"，而不是"挂死"。
>
> 它仍然有价值——它验证的是"关闭有上界"这个契约。但**顺序必须由下面这条
> 单独的测试来守**，它绕开超时兜底那条路径：

```go
// TestHubCloseUnblocksOpenWatchStream 守住关闭顺序本身。
//
// 上一条测试守不住顺序：Shutdown 的超时兜底会调 Stop() 强行推平，
// 顺序反了也只是慢，不是挂。这条测试直接验证那个因果——
// **单独调用 hub.Close()，不碰 GracefulStop**，断言已打开的 Watch handler
// 会因此返回。这正是"先关 hub，GracefulStop 才可能结束"所依赖的前提。
//
// 少了它，"关闭顺序"这条约束在整个测试套件里没有任何护栏。
func TestHubCloseUnblocksOpenWatchStream(t *testing.T) {
	// …建流并等到 ready；
	// 只调 hub.Close()（不调 Shutdown / GracefulStop）；
	// 断言客户端侧的 Recv 在合理时间内返回（流被服务端正常结束），
	// 即 Watch handler 确实因为订阅者 channel 关闭而退出了。
}
```

> `newGRPCEnv` 需要暴露 `server` 字段（`*grpcapi.Server`），并改用它来启动 bufconn 监听。

- [ ] **Step 3: 实现 Server**

`internal/grpcapi/server.go`：

```go
package grpcapi

import (
	"context"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// Deps 是 gRPC 服务需要的全部依赖。
type Deps struct {
	Auth *service.AuthService
	Apps *service.ApplicationService
	Pub  *store.RevokePublisher

	// AppSecretCacheTTL 是应用凭据验证结果的缓存时长，也是 appSecret
	// 轮换的生效上限。为 0 时取 5 分钟。
	AppSecretCacheTTL time.Duration
}

// Server 是 fp 面向 SDK 的 gRPC 服务。
type Server struct {
	grpc *grpc.Server
	hub  *RevokeHub
}

// New 装配 gRPC 服务。
func New(d Deps) *Server {
	ttl := d.AppSecretCacheTTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	verifier := newAppVerifier(d.Apps, ttl)
	hub := NewRevokeHub(d.Pub)

	srv := grpc.NewServer(
		grpc.UnaryInterceptor(verifier.UnaryInterceptor),
		grpc.StreamInterceptor(verifier.StreamInterceptor),

		// MinTime 必须**小于**客户端的 keepalive Time，否则服务端会认为
		// 客户端 ping 过频，回一个 ENHANCE_YOUR_CALM 的 GOAWAY 把连接掐掉。
		// SDK 侧用 30 秒，这里留 10 秒余量。这是 gRPC 最经典的自伤配置：
		// 双方都"配了 keepalive"，结果连接反而被周期性掐断。
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,

			// MaxConnectionAge 让每条连接活满 30 分钟后被优雅回收（先 GOAWAY，
			// 给 5 分钟宽限期收尾进行中的 RPC），客户端随即重连。
			//
			// 没有它的话，**fp 扩容等于白扩**：gRPC 连接是长连接，L4 LB 按连接
			// 分流，存量 SDK 连接会永远钉在老实例上。新加的实例只能接到新启动的
			// SDK 进程——而 SDK 进程的重启频率是按周算的。老实例继续过载、
			// 新实例长期空转，且没有任何报错。
			//
			// grpc-go 会自动给 MaxConnectionAge 加 ±10% 抖动，避免所有连接
			// 同时到期造成重连风暴。
			MaxConnectionAge:      30 * time.Minute,
			MaxConnectionAgeGrace: 5 * time.Minute,
		}),
	)
	fpv1.RegisterAuthServiceServer(srv, NewAuthServer(AuthServerDeps{
		Auth: d.Auth,
		Apps: d.Apps,
		Hub:  hub,
	}))

	return &Server{grpc: srv, hub: hub}
}

// Run 启动撤销事件中继，阻塞到 ctx 取消。在独立 goroutine 里调用。
func (s *Server) Run(ctx context.Context) error {
	return s.hub.Run(ctx)
}

// Serve 开始接受连接，阻塞到服务停止。
func (s *Server) Serve(lis net.Listener) error {
	return s.grpc.Serve(lis)
}

// Shutdown 优雅关闭。
//
// 顺序不能变：先关 hub 让所有 Watch handler 返回，GracefulStop 才可能结束。
// 反过来的话 GracefulStop 会等一条永不结束的长流，进程永远停不下来。
func (s *Server) Shutdown(ctx context.Context) {
	s.hub.Close()

	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("grpcapi: 优雅关闭超时，强制停止")
		s.grpc.Stop()
	}
}
```

- [ ] **Step 4: 接进 main.go**

`cmd/fp/main.go`，在 `httpSrv` 装配之后加：

```go
	grpcSrv := grpcapi.New(grpcapi.Deps{
		Auth: authSvc,
		Apps: appSvc,
		Pub:  revokePub,
	})

	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("监听 gRPC 地址 %s: %w", cfg.GRPCAddr, err)
	}
	go func() {
		if err := grpcSrv.Run(ctx); err != nil {
			log.Error("撤销事件中继异常退出", "err", err)
			stop()
		}
	}()
	go func() {
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Error("gRPC 服务异常退出", "err", err)
			stop()
		}
	}()
```

> 第一阶段的 `main.go` 里 `service.NewApplicationService(pool)` 是在 `httpapi.Deps`
> 字面量里就地构造的，`AuthService` 也尚未装配（第一阶段没有 HTTP 登录入口）。
> 本步骤需要把它们提成具名变量并补上 `authSvc` 的装配——
> 参照 `internal/service/auth_test.go` 里 `authEnv` 的依赖清单：
> `Apps` / `Users` / `Sessions` / `Logs` / `Registry` / `Notifier` / `Codes`。
> **短信供应商按环境分级，不要一刀切。** 早期版本这里写的是"未配置时启动失败"，
> 没有限定环境，结果是任何人本机跑 `./scripts/run.sh`、任何 CI 跑 `cmd/fp`，
> 都得先备齐哪怕是假的阿里云凭据。那与 `internal/notify/fake.go` 文档注释里
> "用于测试与本地开发"的既有意图冲突。正确的分级是（照抄库里 `SecureCookies: cfg.IsProd()`
> 的现成范式）：
>
> - **生产（`FP_ENV=PROD`）**：四项阿里云配置缺任何一项 → **启动失败**
> - **非生产且四项留空**：注册 `notify.NewFakeProvider`，并打一条**醒目的 WARN**
>   说明"短信走假供应商，验证码不会真的送达"。不能静默——静默降级正是这条约束要防的
> - **非生产但四项齐全**：仍用真实供应商（有人就是想在本机对着真通道调试）
>
> 另外，`Serve` 必须等撤销中继就绪后才开始监听（见下方 `ServeWhenReady`）。
> **通知供应商在生产不能用 `notify.NewFakeProvider`**——它只把短信留在内存里。
> 按 `internal/notify/aliyun.go` 装配真实供应商，未配置时启动失败而不是静默降级。

关闭段落改为：

```go
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	grpcSrv.Shutdown(shutdownCtx)
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("HTTP 优雅关闭超时", "err", err)
	}
```

- [ ] **Step 5: 手工验证端到端启动**

```bash
./scripts/run.sh
```

期望日志里 `http` 与 `grpc` 两个地址都在，`Ctrl-C` 后进程在 10 秒内退出。**特别确认它不是挂住等超时**——挂住说明关闭顺序错了。

- [ ] **Step 6: 跑全量测试并提交**

```bash
./scripts/test.sh
```

```bash
git add -A
```

```bash
git commit -m "feat(cmd): gRPC 服务与 HTTP 并存，按序优雅关闭"
```

---

## Task 8: SDK 连接层

一条 `grpc.ClientConn` 承载全部往来：一元 RPC 走它，`Watch` 流也走它。流的存在让连接永不空闲，回源因此永远是热的。

**Files:**
- Create: `sdk/options.go`, `sdk/client.go`
- Test: `sdk/client_test.go`, `sdk/options_test.go`

**Interfaces:**
- Produces: `fpsdk.Options`、`fpsdk.New(opts Options) (*Client, error)`
- Produces: `(*Client).Close() error`、`(*Client).StreamHealthy() bool`
- Produces: `(*Client).rpc` —— 包内的 `fpv1.AuthServiceClient`，供后续任务使用

> **包名：** 目录是 `sdk/`，包名声明为 `package fpsdk`。设计大纲里的用法是
> `fpsdk.Auth`，import 时业务方写 `import fpsdk "github.com/basicfu/fp/sdk"`
> 或直接依赖包名。目录与包名不同是刻意的：import path 里 `/sdk` 更自然，
> 而代码里 `sdk.New()` 太泛。

---

- [ ] **Step 1: 写 Options 与校验的失败测试**

`sdk/options_test.go`：

```go
package fpsdk

import (
	"strings"
	"testing"
	"time"
)

func TestOptionsRequireEssentials(t *testing.T) {
	cases := map[string]Options{
		"缺地址":      {AppID: "a", AppSecret: "s"},
		"缺 appId":  {Addr: "x:9090", AppSecret: "s"},
		"缺 secret": {Addr: "x:9090", AppID: "a"},
	}
	for name, o := range cases {
		if err := o.validate(); err == nil {
			t.Errorf("%s：validate 没有报错", name)
		}
	}
}

func TestOptionsFillDefaults(t *testing.T) {
	o := Options{Addr: "x:9090", AppID: "a", AppSecret: "s"}
	o.applyDefaults()

	if o.ValidateTimeout <= 0 {
		t.Errorf("ValidateTimeout 默认值为 %v", o.ValidateTimeout)
	}
	if o.Logger == nil {
		t.Error("Logger 默认值为 nil，SDK 内部日志会 panic")
	}
}

// TestInsecureRequiresExplicitOptIn 钉住传输安全的默认值。
//
// appSecret 随每个 RPC 的 metadata 发送。明文传输等于把它印在网线上，
// 任何能抓包的人都能拿到一个可以签发任意用户会话的凭据。
// 所以默认必须要求 TLS，明文只能显式 opt-in。
func TestInsecureRequiresExplicitOptIn(t *testing.T) {
	var o Options
	o.applyDefaults()
	if o.Insecure {
		t.Fatal("默认允许明文——appSecret 会在网络上裸奔")
	}

	creds := newAppCredentials(o)
	if !creds.RequireTransportSecurity() {
		t.Fatal("默认凭据不要求传输层安全")
	}
	insecureCreds := newAppCredentials(Options{Insecure: true})
	if insecureCreds.RequireTransportSecurity() {
		t.Fatal("Insecure=true 时仍要求传输层安全，本地开发无法连接")
	}
}

func TestValidateRejectsNegativeDurations(t *testing.T) {
	o := Options{Addr: "x:9090", AppID: "a", AppSecret: "s", ValidateTimeout: -time.Second}
	err := o.validate()
	if err == nil || !strings.Contains(err.Error(), "ValidateTimeout") {
		t.Fatalf("负的 ValidateTimeout 未被拒绝: %v", err)
	}
}
```

- [ ] **Step 2: 实现 Options**

`sdk/options.go`：

**包注释在 `sdk/doc.go`（Task 1 已建），本文件不要再写一份**——一个包只该有一个包注释。

```go
package fpsdk

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"time"
)

// Options 是 SDK 的全部配置。
//
// 本任务只定义连接层用得到的字段；缓存与降级相关的选项由 Task 9、Task 10
// 各自随其读取方一起加入，避免出现"字段存在但没人读"的悬空配置。
type Options struct {
	// Addr 是 fp 的 gRPC 地址，形如 "fp.internal:9090"。
	Addr string
	// AppID / AppSecret 是应用凭据，来自 fp 控制台。
	AppID     string
	AppSecret string

	// Insecure 允许明文连接。
	//
	// **生产绝不要开。** appSecret 随每个 RPC 的 metadata 发送，明文传输
	// 等于把一个能签发任意用户会话的凭据印在网线上。只在本地开发用。
	Insecure bool
	// TLSConfig 自定义 TLS 配置。为 nil 且 Insecure 为 false 时用系统根证书。
	TLSConfig *tls.Config

	// ValidateTimeout 是单次回源的超时。默认 2 秒。
	ValidateTimeout time.Duration

	// Logger 是 SDK 内部日志。为 nil 时用 slog.Default()。
	Logger *slog.Logger
}
```

> **`sdk/` 包不得 import `internal/` 下的任何包**（Global Constraints）。
> Go 的 internal 规则不会拦住同 module 内的引用，编译能过——所以这条
> 只能靠纪律和评审。SDK 需要的类型（用户信息、撤销原因）一律取自
> `sdk/gen/fp/v1` 的生成类型，或在 `sdk/` 内自定义。

`applyDefaults` / `validate` / `newAppCredentials`：

```go
const defaultValidateTimeout = 2 * time.Second

func (o *Options) applyDefaults() {
	if o.ValidateTimeout <= 0 {
		o.ValidateTimeout = defaultValidateTimeout
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

func (o Options) validate() error {
	switch {
	case o.Addr == "":
		return errors.New("fpsdk: Options.Addr 不能为空")
	case o.AppID == "":
		return errors.New("fpsdk: Options.AppID 不能为空")
	case o.AppSecret == "":
		return errors.New("fpsdk: Options.AppSecret 不能为空")
	case o.ValidateTimeout < 0:
		return errors.New("fpsdk: Options.ValidateTimeout 不能为负")
	}
	return nil
}

// appCredentials 把应用凭据附加到每个 RPC 的 metadata 上。
type appCredentials struct {
	appID, secret string
	insecure      bool
}

func newAppCredentials(o Options) appCredentials {
	return appCredentials{appID: o.AppID, secret: o.AppSecret, insecure: o.Insecure}
}

// GetRequestMetadata 实现 credentials.PerRPCCredentials。
func (c appCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{
		"fp-app-id":     c.appID,
		"fp-app-secret": c.secret,
	}, nil
}

// RequireTransportSecurity 实现 credentials.PerRPCCredentials。
//
// 返回 true 时 grpc-go 会拒绝在明文连接上发送这些凭据——这正是我们要的：
// appSecret 一旦被抓包，攻击者就能签发任意用户的会话。只有显式设了
// Insecure 才放行明文。
func (c appCredentials) RequireTransportSecurity() bool { return !c.insecure }
```

- [ ] **Step 3: 写连接层的失败测试**

`sdk/client_test.go`。测试需要一个真实的 fp gRPC 服务端——**但 `sdk/` 不能 import `internal/`**。
解决办法：在 `sdk/` 的测试里起一个**只实现 `fpv1.AuthServiceServer` 的桩服务端**，
它不碰数据库，只按测试需要返回固定响应。真实链路的验证留给 Task 12 的集成测试
（那个包可以同时 import 两边）。

```go
package fpsdk

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// stubServer 是一个可编排的 AuthService 桩。
type stubServer struct {
	fpv1.UnimplementedAuthServiceServer

	validate   func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error)
	watchReady chan struct{}          // 每次有流建立就发一个信号
	events     chan *fpv1.WatchResponse // 测试往这里塞事件
}

func (s *stubServer) ValidateToken(_ context.Context, req *fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
	return s.validate(req)
}

func (s *stubServer) Watch(stream grpc.BidiStreamingServer[fpv1.WatchRequest, fpv1.WatchResponse]) error {
	if err := stream.Send(&fpv1.WatchResponse{
		Event: &fpv1.WatchResponse_Ready{Ready: &fpv1.WatchReady{}},
	}); err != nil {
		return err
	}
	select {
	case s.watchReady <- struct{}{}:
	default:
	}
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev := <-s.events:
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// stubEnv 是一个连着桩服务端的 Client。Task 9~11 的测试全部复用它。
type stubEnv struct {
	stub   *stubServer
	client *Client
	// addr 是桩服务端的真实监听地址，重连测试要用它在原端口重启。
	addr string
	// stop 停掉桩服务端，模拟 fp 宕机。
	stop func()
}

// newStubEnv 起一个桩服务端并连上去。
//
// 用真实回环监听（127.0.0.1:0）而不是 bufconn：Task 8 的重连测试与 Task 10
// 的降级测试都需要"服务端消失又回来"，bufconn 模拟不了这一点。
//
// opt 用于调整 Options（例如把 DegradedCacheTTL 改短、打开 AllowStaleOnOutage）。
func newStubEnv(t *testing.T, validate func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error), opt ...func(*Options)) *stubEnv {
	t.Helper()
	// …起 grpc.Server、注册 stubServer、net.Listen("tcp", "127.0.0.1:0")、
	// 用 fpsdk.New(Options{Addr: lis.Addr().String(), AppID: "t", AppSecret: "t",
	// Insecure: true, …opt}) 建 client，t.Cleanup 里 Close。
	//
	// 注意：桩服务端**不挂认证拦截器**——本层测的是 SDK 的行为，
	// 服务端的认证已由 Task 4 覆盖。
}

// pushRevoke 让桩服务端往流里推一条撤销事件。
func (e *stubEnv) pushRevoke(t *testing.T, ev *fpv1.RevokeEvent) {
	t.Helper()
	e.stub.events <- &fpv1.WatchResponse{Event: &fpv1.WatchResponse_Revoke{Revoke: ev}}
}

// pushPurge 让桩服务端往流里推一条清空指令。
func (e *stubEnv) pushPurge(t *testing.T, reason string) {
	t.Helper()
	e.stub.events <- &fpv1.WatchResponse{
		Event: &fpv1.WatchResponse_Purge{Purge: &fpv1.WatchPurge{Reason: reason}},
	}
}

// waitUntil 轮询到 cond 为真，超时则以 msg 失败。
//
// 推送是异步的，断言必须轮询而不是 sleep 一个固定时长——固定 sleep 要么
// 太短导致偶发失败，要么太长把测试拖慢，而且两者都会被人用"加长 sleep"糊过去。
func (e *stubEnv) waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// okValidate 返回一个总是成功的 validate 回调。
func okValidate(userID string, cacheTTLMs int64) func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
	return func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return &fpv1.ValidateTokenResponse{UserId: userID, SessionId: "s1", CacheTtlMs: cacheTTLMs}, nil
	}
}

// failValidate 返回一个总是拒绝的 validate 回调。
func failValidate() func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
	return func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return nil, status.Error(codes.Unauthenticated, "token 无效或已过期")
	}
}

// TestStreamHealthyOnlyAfterReady 守住"ready 才算健康"。
//
// 光靠"建流没报错"是不够的：流建立了但服务端还没订上 Redis 的那段时间里
// 撤销事件会丢，而 SDK 却以为推送可用、不收紧缓存窗口。
// ready 是服务端"我已经订上了"的显式承诺。
func TestStreamHealthyOnlyAfterReady(t *testing.T) {
	// …起桩服务端，New 一个 client，
	// 断言：New 返回后的极短时间内 StreamHealthy() 可能是 false，
	// 但在收到 ready 之后必须变成 true。
}

// TestStreamHealthyGoesFalseOnDisconnect 守住断开可感知。
//
// 这是整个降级策略的前提：感知不到断开，就无从收紧缓存窗口，
// "推送不可用时把安全性拉回来"就是一句空话。
func TestStreamHealthyGoesFalseOnDisconnect(t *testing.T) {
	// …建流并等到健康后，Stop() 桩服务端，
	// 断言 StreamHealthy() 在合理时间内变为 false。
}

// TestWatchReconnects 守住重连。
//
// fp 重启是常规操作。SDK 若不重连，推送就此永久失效，
// 而回源仍然正常——所以症状是"踢下线要等一个 cache_ttl 才生效"，
// 隐蔽且只在生产偶发。
func TestWatchReconnects(t *testing.T) {
	// …建流→停服务端→原地址重新起一个→
	// 断言 StreamHealthy() 重新变为 true（给足退避时间）。
}

// TestWatchBackoffResetsAfterReady 守住退避复位。
//
// **上面四条测试都测不到这一条**——把复位逻辑整个删掉，它们依然全绿。
// 因为它们验证的是"能连上""能感知断开""能重连""能退出"，
// 而复位与否只影响**重连要等多久**，不影响最终能否连上。
//
// 不复位的后果：一次长时间的 fp 故障把退避推到上限（30 秒）之后，
// 后续任何一次短暂抖动都要等满 30 秒才重连。表现是"fp 早就恢复了，
// 可 SDK 过了半分钟才接上"，而这半分钟里推送通道是断的、缓存窗口被收紧、
// 回源量陡增——排障时几乎不会有人想到是退避没复位。
//
// 怎么测得到：让桩服务端先连续拒绝若干次把退避推高，再放行，
// 断言"从放行到 StreamHealthy() 变 true"的耗时远小于推高后的退避值。
// 断言必须比较**时长**，不能只断言"最终连上了"——后者不复位也成立。
func TestWatchBackoffResetsAfterReady(t *testing.T) {
	// …见上方说明。注意断言的时间上限要明显小于被推高后的退避值，
	// 否则测试即使在"没复位"的实现下也会通过。
}

// TestCloseStopsWatchLoop 守住不泄漏。
//
// 断言 Close() 之后 goroutine 数回落到基线。业务方可能在测试里
// 反复 New/Close，每次泄漏一个重连循环的话会越积越多。
func TestCloseStopsWatchLoop(t *testing.T) {
	before := runtime.NumGoroutine()
	// …New 若干个 client 再全部 Close，等待收敛，
	// 断言 goroutine 数没有显著增长。
}
```

> 这四个测试的骨架已给出意图与断言点，具体装配（bufconn 或真实回环监听、
> 重连的等待时长）由实现者补齐。**重连测试必须用真实回环监听**（`net.Listen("tcp", "127.0.0.1:0")`
> 然后在同端口重启），bufconn 无法模拟"服务端消失又回来"。

- [ ] **Step 4: 实现 Client**

`sdk/client.go`：

```go
package fpsdk

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// Client 是与 fp 的连接。它是并发安全的，一个进程建一个即可。
type Client struct {
	opts Options
	conn *grpc.ClientConn
	rpc  fpv1.AuthServiceClient

	// streamUp 是推送流的健康状态。它驱动缓存窗口的收紧，
	// 是"流断开时把安全性拉回来"这条策略的唯一输入。
	streamUp atomic.Bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New 建立与 fp 的连接并启动推送流。
func New(opts Options) (*Client, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	opts.applyDefaults()

	transport := credentials.NewTLS(opts.TLSConfig)
	if opts.Insecure {
		opts.Logger.Warn("fpsdk: 使用明文连接，appSecret 将以明文传输——生产环境绝不要这样")
		transport = insecure.NewCredentials()
	}

	conn, err := grpc.NewClient(opts.Addr,
		grpc.WithTransportCredentials(transport),
		grpc.WithPerRPCCredentials(newAppCredentials(opts)),

		// keepalive 是"连接永不空闲"的第二道保险（第一道是 Watch 长流本身）。
		// Time 必须**大于**服务端的 EnforcementPolicy.MinTime（fp 设的是 10 秒），
		// 否则服务端会认为客户端 ping 过频，回 ENHANCE_YOUR_CALM 的 GOAWAY
		// 把连接掐掉——双方都"配了 keepalive"却导致连接被周期性掐断，
		// 是这套机制最经典的自伤方式。
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{opts: opts, conn: conn, rpc: fpv1.NewAuthServiceClient(conn), cancel: cancel}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.runWatch(ctx)
	}()
	return c, nil
}

// StreamHealthy 报告撤销推送流当前是否可用。
func (c *Client) StreamHealthy() bool { return c.streamUp.Load() }

// Close 关闭连接并停止后台循环。
func (c *Client) Close() error {
	c.cancel()
	err := c.conn.Close()
	c.wg.Wait()
	return err
}

// runWatch 维持推送流，断开后按退避重连，直到 ctx 取消。
func (c *Client) runWatch(ctx context.Context) {
	const (
		minBackoff = 200 * time.Millisecond
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff

	for ctx.Err() == nil {
		err := c.watchOnce(ctx)
		c.streamUp.Store(false)

		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.opts.Logger.Warn("fpsdk: 撤销推送流断开，准备重连", "err", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// watchOnce 建一次流并读到断开为止。
func (c *Client) watchOnce(ctx context.Context) error {
	stream, err := c.rpc.Watch(ctx)
	if err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		switch {
		case msg.GetReady() != nil:
			// 只有收到 ready 才置为健康：此前服务端可能还没订上撤销频道，
			// 那段时间的事件会丢，而 SDK 若已认为健康就不会收紧缓存窗口。
			c.streamUp.Store(true)
		case msg.GetRevoke() != nil:
			// 本任务只有连接层，还没有缓存可清。Task 10 会把这里换成
			// 真正的缓存失效，并在 ready 分支补上重连后的整体清空。
			c.opts.Logger.Debug("fpsdk: 收到撤销事件",
				"tokens", len(msg.GetRevoke().GetTokens()),
				"reason", msg.GetRevoke().GetReason())
		case msg.GetPurge() != nil:
			c.opts.Logger.Warn("fpsdk: 收到服务端的缓存清空指令",
				"reason", msg.GetPurge().GetReason())
		default:
			// 未知事件类型（将来的 ConfigChanged / PolicyChanged）。
			// 忽略，不要报错——oneof 的向前兼容就靠这里。
		}
	}
}
```

> **退避重置：** 上面的实现里 `backoff` 只增不减。连接恢复后应重置回 `minBackoff`，
> 否则一次长时间的 fp 故障之后，后续任何一次短暂抖动都要等满 30 秒才重连。
> 实现时在 `watchOnce` 成功收到 ready 后把 backoff 复位——
> 具体做法：让 `watchOnce` 返回一个 `gotReady bool`，`runWatch` 据此复位。
> **这一条必须实现，不是可选优化。**

- [ ] **Step 5: 跑测试并提交**

```bash
./scripts/test.sh ./sdk/...
```

```bash
git add sdk
```

```bash
git commit -m "feat(sdk): 连接层——应用凭据、keepalive、推送流重连与健康状态"
```

---

## Task 9: SDK 缓存层

进程内 LRU + TTL。它是整个方案能成立的原因：没有它，业务方每个请求都要跨服务调一次 fp，fp 变成每条请求路径上的强依赖——**fp 一挂，所有接入方立刻全挂**。

**Files:**
- Create: `sdk/cache.go`
- Test: `sdk/cache_test.go`
- Modify: `sdk/options.go`（加 `CacheSize` 及其默认值——**本任务是它的第一个读者**）
- Modify: `sdk/options_test.go`（补 `CacheSize` 的默认值断言）
- Modify: `go.mod`（引入 `github.com/hashicorp/golang-lru/v2`）

> **选项字段随读取方一起加入。** Task 8 只声明了连接层用到的字段；
> `CacheSize` 在本任务才有读者，所以在本任务加。反过来先把整个 Options
> 声明完整、留一堆没人读的字段，正是第一阶段反复出现的那类问题。
>
> 本任务要加的：
>
> ```go
> // CacheSize 是本地校验结果缓存的容量上限（条）。默认 10000。
> CacheSize int
> ```
>
> `applyDefaults` 补 `if o.CacheSize <= 0 { o.CacheSize = defaultCacheSize }`
> （`defaultCacheSize = 10000`），`validate` 补 `case o.CacheSize < 0:` 分支。

**Interfaces:**
- Produces: `newCache(size int, now func() time.Time) (*cache, error)`
- Produces: `(*cache).put(token string, e entry, ttl time.Duration)`
- Produces: `(*cache).get(token string, maxTTL, maxStale time.Duration) (entry, cacheState)`
- Produces: `(*cache).drop(tokens ...string)`

---

### 一条决定成败的设计：TTL 上限必须在**读取时**施加

推送流断开时 SDK 要把缓存窗口收紧到 `DegradedCacheTTL`。**收紧必须作用于已经在缓存里的条目**，而不只是之后新写入的。

写入时施加上限的写法看起来完全正确，也能让"断开后新写入的条目 TTL 变短"这类测试通过。但它对**存量条目**毫无作用——而流断开那一瞬间，缓存里装的恰恰全是流健康时按完整窗口（比如 30 秒、120 秒）写入的条目。收紧策略于是在最需要它的时刻完全失效。

所以缓存条目存 `cachedAt` 与 `ttl` 两个值，过期判定在 `get` 里现算：

```
有效期 = min(条目自带的 ttl, 调用方本次给的 maxTTL)
```

---

- [ ] **Step 1: 写失败测试**

`sdk/cache_test.go`：

```go
package fpsdk

import (
	"testing"
	"time"
)

// fakeClock 让测试精确控制时间，不用 sleep。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestCache(t *testing.T, size int) (*cache, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	c, err := newCache(size, clk.now)
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}
	return c, clk
}

func TestCacheHitBeforeTTL(t *testing.T) {
	c, clk := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 30*time.Second)

	clk.advance(29 * time.Second)
	got, state := c.get("tok", 0, 0)
	if state != cacheFresh {
		t.Fatalf("TTL 内查询返回 %v，期望 cacheFresh", state)
	}
	if got.userID != "u1" {
		t.Fatalf("取回的 userID 是 %q", got.userID)
	}
}

func TestCacheMissAfterTTL(t *testing.T) {
	c, clk := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 30*time.Second)

	clk.advance(31 * time.Second)
	if _, state := c.get("tok", 0, 0); state == cacheFresh {
		t.Fatal("超过 TTL 后仍判定为新鲜")
	}
}

// TestZeroTTLIsNotCached 钉住交接契约 1。
//
// fp 在会话剩余不足一毫秒时下发 cache_ttl_ms = 0，含义是"不要缓存"。
// 把它当成"没给，用默认值"会缓存一个本该立刻失效的放行判定——
// 一个已经到达 max_lifetime 的会话会被继续放行整整一个缓存窗口。
func TestZeroTTLIsNotCached(t *testing.T) {
	c, _ := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 0)

	if _, state := c.get("tok", 0, 0); state == cacheFresh {
		t.Fatal("ttl=0 的条目被缓存了——0 表示不要缓存，不是未设置")
	}
}

func TestNegativeTTLIsNotCached(t *testing.T) {
	c, _ := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, -time.Second)
	if _, state := c.get("tok", 0, 0); state == cacheFresh {
		t.Fatal("负 TTL 的条目被缓存了")
	}
}

// TestDegradedCapAppliesToExistingEntries 是本任务最重要的一个测试。
//
// 推送流断开时要把缓存窗口收紧到 DegradedCacheTTL。收紧若写在 put 里，
// 这个测试会失败——因为条目是在"流还健康"时以完整窗口写入的，
// 而流断开那一瞬间缓存里装的**全是**这种条目。
// 也就是说：写入时收紧的实现，在最需要收紧的时刻完全不起作用，
// 却能让"断开后新写入的条目 TTL 变短"这类测试顺利通过。
func TestDegradedCapAppliesToExistingEntries(t *testing.T) {
	c, clk := newTestCache(t, 16)

	// 流健康时写入，完整窗口 60 秒。
	c.put("tok", entry{userID: "u1"}, 60*time.Second)

	// 10 秒后流断开，收紧到 5 秒。这条 10 秒前写入的条目必须立即失效。
	clk.advance(10 * time.Second)
	if _, state := c.get("tok", 5*time.Second, 0); state == cacheFresh {
		t.Fatal("流断开后，存量缓存条目仍按完整窗口放行——" +
			"收紧被写在了 put 里，对存量条目无效")
	}

	// 反向确认：3 秒时收紧到 5 秒，应当仍然命中，否则就是把上限当成了"立刻失效"。
	c2, clk2 := newTestCache(t, 16)
	c2.put("tok", entry{userID: "u1"}, 60*time.Second)
	clk2.advance(3 * time.Second)
	if _, state := c2.get("tok", 5*time.Second, 0); state != cacheFresh {
		t.Fatal("收紧到 5 秒后，第 3 秒的条目也失效了——上限被当成了立即过期")
	}
}

// TestMaxTTLNeverExtends 守住上限只能收紧、不能放宽。
//
// min 写反成 max 的话，fp 下发的 cache_ttl 会被本地配置覆盖放大——
// 一个 fp 明确说"只能缓存 2 秒"（会话快到期了）的判定被缓存 30 秒，
// 直接违背 4.5.1「过期判断只由 fp 做」的整个前提。
func TestMaxTTLNeverExtends(t *testing.T) {
	c, clk := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 2*time.Second)

	clk.advance(3 * time.Second)
	if _, state := c.get("tok", 60*time.Second, 0); state == cacheFresh {
		t.Fatal("传入更大的 maxTTL 把条目的有效期放大了——min 写成了 max")
	}
}

// TestStaleWindow 确认陈旧兜底的三档状态。
func TestStaleWindow(t *testing.T) {
	c, clk := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 10*time.Second)

	clk.advance(5 * time.Second)
	if _, s := c.get("tok", 0, time.Minute); s != cacheFresh {
		t.Fatalf("TTL 内应为 cacheFresh，得到 %v", s)
	}
	clk.advance(10 * time.Second) // 已过期 5 秒，仍在 1 分钟的陈旧窗口内
	if _, s := c.get("tok", 0, time.Minute); s != cacheStale {
		t.Fatalf("过期但在陈旧窗口内应为 cacheStale，得到 %v", s)
	}
	clk.advance(2 * time.Minute) // 超出陈旧窗口
	if _, s := c.get("tok", 0, time.Minute); s != cacheMiss {
		t.Fatalf("超出陈旧窗口应为 cacheMiss，得到 %v", s)
	}
}

// TestStaleIsNeverReturnedWhenMaxStaleIsZero 确认默认不吐陈旧数据。
func TestStaleIsNeverReturnedWhenMaxStaleIsZero(t *testing.T) {
	c, clk := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 10*time.Second)
	clk.advance(11 * time.Second)

	if _, s := c.get("tok", 0, 0); s != cacheMiss {
		t.Fatalf("maxStale=0 时过期条目返回 %v，期望 cacheMiss", s)
	}
}

func TestCacheEvictsByCapacity(t *testing.T) {
	c, _ := newTestCache(t, 2)
	c.put("a", entry{userID: "ua"}, time.Minute)
	c.put("b", entry{userID: "ub"}, time.Minute)
	c.put("c", entry{userID: "uc"}, time.Minute)

	if _, s := c.get("a", 0, 0); s == cacheFresh {
		t.Fatal("容量为 2 却装下了 3 条——缓存无界增长会拖垮业务方进程")
	}
	if _, s := c.get("c", 0, 0); s != cacheFresh {
		t.Fatal("最新写入的条目被淘汰了")
	}
}

// TestZeroTTLPutDoesNotEvictOtherEntries 守住 put 的 ttl<=0 提前返回。
//
// **上面的 TestZeroTTLIsNotCached 测不到这一条**——把那个 guard 整个删掉，
// 它依然通过。因为 get 里 `age < effective` 的严格比较在 age==0 时就已经
//把 ttl<=0 的条目读成 miss 了，无论它有没有真的进过 LRU。
//
// guard 真正防的是：一个"不要缓存"的 token 白占一个 LRU 槽位，
// 把一个合法有效的条目挤出去。所以断言要落在**别的条目还在不在**上。
func TestZeroTTLPutDoesNotEvictOtherEntries(t *testing.T) {
	// 容量设 2，先放两条 60 秒的有效条目，再 put 一条 ttl=0 的，
	// 断言那两条都还在。
}

// TestExpiredEntryDoesNotCrowdOutFreshEntry 守住 get 里的 Remove。
//
// 删掉那行 Remove，上面所有给定的测试都还是绿的——因为它们只看
// 某个 token 自己读回来是什么，不看它对**别的条目**的影响。
//
// 而 hashicorp/golang-lru 的 Get 会把命中的键提升为最近使用，
// 哪怕紧接着就被判成过期。少了 Remove，过期条目就成了 LRU 眼里最新鲜的，
// 它留下、新鲜条目被挤走——缓存开始优先淘汰有用的东西。
func TestExpiredEntryDoesNotCrowdOutFreshEntry(t *testing.T) {
	// 容量设 2，放一条短 TTL 的，推进时钟让它过期，
	// get 一次（触发提升 + 应有的 Remove），再放两条新的，
	// 断言两条新的都还在。
}

// TestDropRemovesEntries 确认撤销事件能清掉缓存。
func TestDropRemovesEntries(t *testing.T) {
	c, _ := newTestCache(t, 16)
	c.put("a", entry{userID: "u"}, time.Minute)
	c.put("b", entry{userID: "u"}, time.Minute)

	c.drop("a", "b")
	for _, tok := range []string{"a", "b"} {
		if _, s := c.get(tok, 0, 0); s != cacheMiss {
			t.Fatalf("drop 后 %q 仍在缓存里——撤销推送将不起作用", tok)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./sdk/ -run TestCache
```

- [ ] **Step 3: 引入 LRU 依赖**

```bash
go get github.com/hashicorp/golang-lru/v2@v2.0.7
```

- [ ] **Step 4: 实现缓存**

`sdk/cache.go`：

```go
package fpsdk

import (
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// entry 是一次校验结果的缓存内容。
type entry struct {
	userID    string
	sessionID string
	// rotatedTo 非空表示这个 token 已被 fp 轮换。中间件每次命中都要把它
	// 回传给客户端——fp 在过渡期内会重复告知，SDK 也必须重复转达，
	// 否则缓存命中的那些请求反而成了交接的黑洞。
	rotatedTo string

	// cachedAt 与 ttl 分开存，过期判定在 get 里现算。
	// 合并成一个 expiresAt 就无法在读取时收紧窗口——见下方 get 的说明。
	cachedAt time.Time
	ttl      time.Duration
}

// cacheState 是一次查询的结果状态。
type cacheState int

const (
	// cacheMiss 没有可用条目，必须回源。
	cacheMiss cacheState = iota
	// cacheFresh 条目在有效期内，可以直接放行。
	cacheFresh
	// cacheStale 条目已过期但仍在陈旧窗口内。只有 fp 不可达且调用方
	// 显式允许时才可使用。
	cacheStale
)

// cache 是进程内的校验结果缓存。
//
// hashicorp/golang-lru/v2 自带锁，本类型无需额外同步。
type cache struct {
	lru *lru.Cache[string, entry]
	now func() time.Time
}

func newCache(size int, now func() time.Time) (*cache, error) {
	l, err := lru.New[string, entry](size)
	if err != nil {
		return nil, err
	}
	return &cache{lru: l, now: now}, nil
}

// put 写入一条校验结果。
//
// ttl <= 0 时**什么都不做**。fp 在会话剩余不足一毫秒时就下发 0，
// 语义是"不要缓存"而非"未设置"。把 0 当成缺省值去套本地默认值，
// 会让一个已经到达 max_lifetime 的会话被继续放行整整一个缓存窗口。
func (c *cache) put(token string, e entry, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	e.cachedAt = c.now()
	e.ttl = ttl
	c.lru.Add(token, e)
}

// get 查询一条缓存结果。
//
// maxTTL 是本次查询允许的有效期上限，0 表示不额外收紧。推送流断开时
// 调用方传入 DegradedCacheTTL——收紧因此作用于**已经在缓存里的条目**。
//
// 这一点是本类型全部设计的核心：把收紧写进 put 的话，它只影响之后新写入的
// 条目，而流断开那一瞬间缓存里装的全是流健康时按完整窗口写入的条目，
// 收紧在最需要它的时刻完全失效。
//
// maxStale 是过期之后仍可返回（标记为 cacheStale）的额外时长，0 表示
// 过期即 miss。
func (c *cache) get(token string, maxTTL, maxStale time.Duration) (entry, cacheState) {
	e, ok := c.lru.Get(token)
	if !ok {
		return entry{}, cacheMiss
	}

	effective := e.ttl
	// 只收紧、不放宽：写成 max 会让本地配置覆盖 fp 下发的 cache_ttl，
	// 直接违背「过期判断只由 fp 做」的前提。
	if maxTTL > 0 && maxTTL < effective {
		effective = maxTTL
	}

	age := c.now().Sub(e.cachedAt)
	switch {
	case age < effective:
		return e, cacheFresh
	case maxStale > 0 && age < effective+maxStale:
		return e, cacheStale
	default:
		// 必须显式清掉，而且后果比"占一个槽位"严重得多：
		// hashicorp/golang-lru 的 Get **会把命中的键提升为最近使用**——
		// 哪怕我们下一行就要把它判成过期。不 Remove 的话，过期条目反而
		// 成了 LRU 眼里最新鲜的那个，它活下来，而真正新鲜的条目被挤掉。
		// 也就是说：漏掉这一行不是"少清理一点"，是让缓存优先淘汰有用的东西。
		c.lru.Remove(token)
		return entry{}, cacheMiss
	}
}

// drop 删除若干条目。撤销事件到达时调用。
func (c *cache) drop(tokens ...string) {
	for _, t := range tokens {
		c.lru.Remove(t)
	}
}

// purge 清空缓存。连接长时间断开后重连时调用——断开期间发生的撤销
// 一条都没收到，缓存里的任何条目都不再可信。
func (c *cache) purge() { c.lru.Purge() }
```

- [ ] **Step 5: 跑测试并提交**

```bash
./scripts/test.sh ./sdk/...
```

```bash
git add sdk go.mod go.sum
```

```bash
git commit -m "feat(sdk): LRU+TTL 校验结果缓存，收紧窗口在读取时生效"
```

---

## Task 10: `fpsdk.Auth`——校验、撤销消费、降级、登录登出

把连接层与缓存层接起来，形成业务方真正使用的能力。

**Files:**
- Create: `sdk/auth.go`
- Test: `sdk/auth_test.go`
- Modify: `sdk/client.go`（加 `auth *Auth` 字段与 `Auth()` 访问器；把 `watchOnce` 里 Task 8 留下的日志换成真正的缓存失效）
- Modify: `sdk/options.go`、`sdk/options_test.go`（加降级相关的三个字段——**本任务是它们的第一个读者**）
- Modify: `sdk/client_test.go`（`stubEnv` 加 `auth` 字段 = `client.Auth()`）
- Modify: `go.mod`（引入 `golang.org/x/sync`）

> **本任务要加的选项字段**（连同默认值与校验分支）：
>
> ```go
> // DegradedCacheTTL 是**推送流断开时**的缓存时长上限。默认 5 秒。
> //
> // 推送断开意味着撤销的"加速"能力消失，只剩 TTL 兜底。主动收紧窗口
> // 把安全性拉回来——这是 gRPC 流状态可感知才做得了的事。
> DegradedCacheTTL time.Duration
>
> // AllowStaleOnOutage 决定 fp 不可达时能否继续使用**已过期**的缓存条目。
> //
> // 它不是通常意义上的 "fail-open"。鉴权中间件放行却拿不出用户身份是
> // 讲不通的——那等于接受一个无法验证的 token。真正有意义的降级是
> // "继续用刚才验过的那个身份"：身份已知，只是刷新不了。
> // 完全没有缓存条目时，无论本开关如何都必须拒绝。
> //
> // 零值 false = 不允许。命名为"允许"而非它的反面，是为了让放宽的方向
> // 必须被显式写出来——零值必须落在安全的一侧。
> AllowStaleOnOutage bool
>
> // MaxStaleness 是 AllowStaleOnOutage 生效时，缓存条目最多能被延用多久
> // （从它本该过期的时刻起算）。默认 5 分钟。
> //
> // 必须有上限：没有上限的话，fp 停机一整天，一个早已被踢下线的会话
> // 就能畅通一整天。
> MaxStaleness time.Duration
> ```
>
> `sdk/options_test.go` 补一条：
>
> ```go
> // TestStaleFallbackIsOffByDefault 钉住降级方向的默认值。
> //
> // 字段命名为"允许用陈旧数据"而非它的反面，是为了让放宽的那个方向必须被
> // 显式写出来——没人会主动去关掉一项他不知道存在的开关，零值必须落在
> // 安全的一侧。
> func TestStaleFallbackIsOffByDefault(t *testing.T) {
> 	var o Options
> 	o.applyDefaults()
> 	if o.AllowStaleOnOutage {
> 		t.Fatal("默认允许使用陈旧缓存——fp 一挂，已被撤销的会话会继续通行")
> 	}
> 	if o.MaxStaleness <= 0 {
> 		t.Fatalf("MaxStaleness 默认值为 %v——陈旧兜底必须有上限，"+
> 			"否则 fp 长时间不可用时会无限延用", o.MaxStaleness)
> 	}
> 	if o.DegradedCacheTTL <= 0 {
> 		t.Fatalf("DegradedCacheTTL 默认值为 %v", o.DegradedCacheTTL)
> 	}
> }
> ```

**Interfaces:**
- Produces: `(*Client).Auth() *Auth`
- Produces: `fpsdk.Identity{UserID, SessionID, RotatedTo string}`
- Produces: `(*Auth).Validate(ctx, token string) (*Identity, error)`
- Produces: `(*Auth).Login(ctx, in LoginInput) (*LoginResult, error)`
- Produces: `(*Auth).SendLoginCode(ctx, phone string) error`
- Produces: `(*Auth).Logout(ctx, token string) error`
- Produces: `fpsdk.ErrNoToken`、`fpsdk.ErrUnauthorized`、`fpsdk.ErrUnavailable`

---

- [ ] **Step 1: 写失败测试**

`sdk/auth_test.go`。复用 Task 8 建立的 `stubEnv` / `newStubEnv` / `okValidate` / `failValidate` / `waitUntil`，通过编排 `validate` 回调制造各种情形。

**本任务要给 `stubEnv` 补上 `auth` 字段**（Task 8 里预留了但填不了——那时还没有 `Auth` 类型）：在 `newStubEnv` 建完 client 之后写一行 `env.auth = env.client.Auth()`。

```go
package fpsdk

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// TestValidateCachesAndAvoidsRefetch 确认缓存真的挡住了回源。
//
// 这是整个方案存在的理由：没有本地缓存，业务方每个请求都要跨服务调一次
// fp，fp 于是成为每条请求路径上的强依赖——fp 一挂，全部接入方立刻全挂。
func TestValidateCachesAndAvoidsRefetch(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 30_000}, nil
	})

	for i := 0; i < 10; i++ {
		if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
			t.Fatalf("第 %d 次校验: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("10 次校验回源 %d 次，期望 1 次", got)
	}
}

// TestConcurrentMissesCollapseToOneCall 守住 singleflight。
//
// 缓存过期的瞬间，同一个 token 的并发请求会同时 miss。没有合并的话，
// 一个热门用户的 N 个并发请求会同时打到 fp——正是缓存要防的惊群。
// 流量越大放大越严重，而功能完全正常，只有 fp 的负载图会异常。
func TestConcurrentMissesCollapseToOneCall(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		<-release // 卡住，保证并发请求都落在同一个飞行窗口里
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 30_000}, nil
	})

	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := env.auth.Validate(context.Background(), "tok")
			errs <- err
		}()
	}
	time.Sleep(100 * time.Millisecond) // 让 n 个请求都进入飞行状态
	close(release)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("并发校验失败: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d 个并发 miss 回源 %d 次，期望 1 次（singleflight 未生效）", n, got)
	}
}

// TestZeroCacheTTLForcesRefetch 钉住交接契约 1 的端到端表现。
func TestZeroCacheTTLForcesRefetch(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 0}, nil
	})

	for i := 0; i < 3; i++ {
		if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
			t.Fatalf("第 %d 次: %v", i, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("cache_ttl_ms=0 时 3 次校验只回源 %d 次——0 被当成了默认值", got)
	}
}

// TestRejectedTokenIsNotCached 守住一条内存性质。
//
// 缓存有容量上限。把失败结果也塞进去的话，攻击者用海量随机 token
// 就能把真实条目全部挤出 LRU，逼得每个正常请求都回源——
// 一次廉价的攻击就能让 fp 承受全量鉴权流量。
func TestRejectedTokenIsNotCached(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return nil, status.Error(codes.Unauthenticated, "token 无效或已过期")
	})

	for i := 0; i < 3; i++ {
		if _, err := env.auth.Validate(context.Background(), "bad"); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("第 %d 次返回 %v，期望 ErrUnauthorized", i, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("失败结果被缓存了（只回源 %d 次）——攻击者可用随机 token 挤空缓存", got)
	}
}

// TestRevokeEventDropsCachedToken 是撤销推送的全部意义。
func TestRevokeEventDropsCachedToken(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 300_000}, nil
	})

	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("首次: %v", err)
	}
	env.pushRevoke(t, &fpv1.RevokeEvent{Tokens: []string{"tok"}, UserIds: []string{"u1"}})

	// 推送是异步的，等缓存被清掉。
	env.waitUntil(t, func() bool {
		_, err := env.auth.Validate(context.Background(), "tok")
		return err == nil && calls.Load() == 2
	}, "撤销事件到达后缓存未被清除——被踢下线的用户会继续通行整整一个 cache_ttl")
}

// TestPurgeDropsEverything 守住 SDK 侧对 WatchPurge 的处理。
//
// 服务端在确知漏读了撤销事件、却不知道漏了哪些时发这条指令。SDK 必须清掉
// **全部**条目——只清某一部分（比如按 appID）没有任何依据，因为服务端
// 恰恰是因为"不知道涉及谁"才发的它。
//
// 断言写成"多个不同 token 都要重新回源"：只清一条的实现能让单 token
// 的测试通过，而线上表现是缓存里绝大多数条目仍停在不可信状态。
func TestPurgeDropsEverything(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 300_000}, nil
	})

	tokens := []string{"tok-a", "tok-b", "tok-c"}
	for _, tok := range tokens {
		if _, err := env.auth.Validate(context.Background(), tok); err != nil {
			t.Fatalf("预热 %s: %v", tok, err)
		}
	}
	if got := calls.Load(); got != int32(len(tokens)) {
		t.Fatalf("预热阶段回源 %d 次，期望 %d 次", got, len(tokens))
	}

	env.pushPurge(t, "redis 订阅重建")

	env.waitUntil(t, func() bool {
		for _, tok := range tokens {
			if _, err := env.auth.Validate(context.Background(), tok); err != nil {
				return false
			}
		}
		// 三个 token 全部重新回源过，总次数应为 2 × len(tokens)。
		return calls.Load() == int32(2*len(tokens))
	}, "收到 purge 后缓存没有被全部清空——部分条目仍停在已知不可信的状态")
}

// TestStreamDownTightensCacheWindow 守住降级策略。
//
// 推送断开意味着撤销的"加速"能力消失，只剩 TTL 兜底。SDK 主动把窗口
// 收紧到 DegradedCacheTTL，把安全性拉回来——这正是 gRPC 流状态可感知
// 才做得到、SSE 方案给不了的东西。
func TestStreamDownTightensCacheWindow(t *testing.T) {
	// …建一个 DegradedCacheTTL 很短的 client，先在流健康时缓存一条
	// cache_ttl 很长的结果，然后停掉桩服务端的流，
	// 断言：等 StreamHealthy() 变 false 之后，同一个 token 会重新回源
	// （而不是继续吃那条长窗口的缓存）。
}

// TestUnavailableWithoutStaleFallbackIsRejected 确认默认不放行。
func TestUnavailableWithoutStaleFallbackIsRejected(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return nil, status.Error(codes.Unavailable, "fp 挂了")
	})
	if _, err := env.auth.Validate(context.Background(), "tok"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fp 不可达且无缓存时返回 %v，期望 ErrUnavailable", err)
	}
}

// TestStaleFallbackServesLastKnownIdentity 确认陈旧兜底的语义。
//
// 它给出的是"刚才验过的那个身份"，不是"放行一个未经验证的 token"。
// 完全没有缓存条目时必须照样拒绝——那种情况下根本没有身份可用。
func TestStaleFallbackServesLastKnownIdentity(t *testing.T) {
	// …开 AllowStaleOnOutage 的 client：
	//   1. 先成功校验一次并缓存
	//   2. 让桩服务端此后一律返回 Unavailable
	//   3. 时间推进越过 cache_ttl
	//   4. 断言仍返回同一个 UserID
	//   5. 换一个从未验过的 token，断言返回 ErrUnavailable
}

// TestEmptyTokenIsRejectedWithoutRoundTrip 确认空 token 不打 fp。
func TestEmptyTokenIsRejectedWithoutRoundTrip(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return nil, nil
	})
	if _, err := env.auth.Validate(context.Background(), ""); !errors.Is(err, ErrNoToken) {
		t.Fatalf("空 token 返回 %v，期望 ErrNoToken", err)
	}
	if calls.Load() != 0 {
		t.Fatal("空 token 触发了一次回源——未登录的匿名流量会全部打到 fp")
	}
}

// TestRotationIsSurfacedOnCacheHit 守住轮换交接不被缓存吃掉。
//
// fp 在过渡期内会对每一次带旧 token 的校验重复告知新 token。但 SDK 一旦
// 把首次结果缓存下来，后续请求就不再回源——如果缓存条目丢掉了 rotatedTo，
// 重复告知在 SDK 这一层被截断，客户端仍然只有一次机会。
// fp 侧修好的交接会被 SDK 重新打破。
func TestRotationIsSurfacedOnCacheHit(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return &fpv1.ValidateTokenResponse{
			UserId: "u1", SessionId: "s1", CacheTtlMs: 30_000,
			Rotated: true, NewToken: "new-tok",
		}, nil
	})

	for i := 0; i < 3; i++ {
		id, err := env.auth.Validate(context.Background(), "old-tok")
		if err != nil {
			t.Fatalf("第 %d 次: %v", i, err)
		}
		if id.RotatedTo != "new-tok" {
			t.Fatalf("第 %d 次命中缓存后 RotatedTo 为 %q——"+
				"轮换交接在 SDK 缓存层被截断了", i, id.RotatedTo)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./sdk/ -run 'TestValidate|TestConcurrent|TestZeroCache|TestRejected|TestRevokeEvent|TestStream|TestUnavailable|TestStale|TestEmptyToken|TestRotationIsSurfaced'
```

- [ ] **Step 3: 引入 singleflight**

```bash
go get golang.org/x/sync@v0.22.0
```

- [ ] **Step 4: 实现 Auth**

`sdk/auth.go`：

```go
package fpsdk

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// SDK 对外的哨兵错误。业务方用 errors.Is 判定。
var (
	// ErrNoToken 请求里没有 token。
	ErrNoToken = errors.New("fpsdk: 缺少 token")
	// ErrUnauthorized token 无效、已过期或已被撤销。
	ErrUnauthorized = errors.New("fpsdk: token 无效或已过期")
	// ErrUnavailable fp 不可达，且本地没有可用的缓存结果。
	ErrUnavailable = errors.New("fpsdk: fp 不可达且无可用缓存")
)

// Identity 是一次成功校验得到的身份。
type Identity struct {
	UserID    string
	SessionID string
	// RotatedTo 非空时，**必须**把这个新 token 下发给客户端（换 cookie /
	// 回响应头）。不下发的话，该会话会在 fp 的轮换过渡期结束后被登出。
	RotatedTo string
	// Stale 为 true 表示这是 fp 不可达期间返回的陈旧结果。
	// 业务方可据此拒绝高危操作。
	Stale bool
}

// Auth 是认证能力。用 (*Client).Auth() 取得，并发安全。
type Auth struct {
	c     *Client
	cache *cache
	sf    singleflight.Group
}

// Validate 校验 token，命中本地缓存时不产生任何网络往返。
func (a *Auth) Validate(ctx context.Context, token string) (*Identity, error) {
	if token == "" {
		return nil, ErrNoToken
	}

	// 推送流断开 = 收不到撤销事件 = 撤销只剩 TTL 兜底。
	// 主动收紧窗口把安全性拉回来。上限传给 get，因此对**存量条目**同样生效。
	var maxTTL time.Duration
	if !a.c.StreamHealthy() {
		maxTTL = a.c.opts.DegradedCacheTTL
	}
	var maxStale time.Duration
	if a.c.opts.AllowStaleOnOutage {
		maxStale = a.c.opts.MaxStaleness
	}

	if e, state := a.cache.get(token, maxTTL, maxStale); state == cacheFresh {
		return identityFrom(e, false), nil
	}

	// singleflight：缓存过期的瞬间，同一个 token 的并发请求会同时 miss。
	// 不合并的话，一个热门用户的 N 个并发请求会同时打到 fp——正是缓存
	// 要防的惊群。功能上完全正常，只有 fp 的负载会被放大 N 倍。
	v, err, _ := a.sf.Do(token, func() (any, error) {
		callCtx, cancel := context.WithTimeout(ctx, a.c.opts.ValidateTimeout)
		defer cancel()

		res, err := a.c.rpc.ValidateToken(callCtx, &fpv1.ValidateTokenRequest{Token: token})
		if err != nil {
			return nil, err
		}
		e := entry{
			userID:    res.GetUserId(),
			sessionID: res.GetSessionId(),
			rotatedTo: res.GetNewToken(),
		}
		// cache_ttl_ms == 0 表示"不要缓存"。put 内部会原样忽略，
		// 这里不做任何"没给就用默认值"的兜底——那正是契约禁止的事。
		a.cache.put(token, e, time.Duration(res.GetCacheTtlMs())*time.Millisecond)
		return e, nil
	})
	if err == nil {
		return identityFrom(v.(entry), false), nil
	}

	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied:
		// **不缓存失败结果。** 缓存有容量上限，把失败也塞进去的话，
		// 攻击者用海量随机 token 就能把真实条目全部挤出 LRU，
		// 逼得每个正常请求都回源——一次廉价攻击让 fp 承受全量鉴权流量。
		return nil, ErrUnauthorized
	}

	// 走到这里是 fp 不可达（Unavailable / DeadlineExceeded / 连接错误）。
	// 只有在调用方显式允许时，才用"刚才验过的那个身份"兜底。
	if maxStale > 0 {
		if e, state := a.cache.get(token, maxTTL, maxStale); state == cacheStale {
			a.c.opts.Logger.Warn("fpsdk: fp 不可达，使用陈旧的校验结果",
				"userId", e.userID, "err", err)
			return identityFrom(e, true), nil
		}
	}
	// 没有任何缓存条目时一律拒绝：此刻既验证不了 token，也拿不出身份，
	// "放行"没有任何可以赋予的含义。
	return nil, errors.Join(ErrUnavailable, err)
}

func identityFrom(e entry, stale bool) *Identity {
	return &Identity{
		UserID:    e.userID,
		SessionID: e.sessionID,
		RotatedTo: e.rotatedTo,
		Stale:     stale,
	}
}

// onRevoke 是撤销事件的处理入口，由 Client 的 Watch 循环调用。
func (a *Auth) onRevoke(ev *fpv1.RevokeEvent) {
	a.cache.drop(ev.GetTokens()...)
}

// onPurge 丢弃全部缓存，由 Client 的 Watch 循环在收到 WatchPurge 时调用。
//
// 服务端只在"确知漏读了撤销事件、却不知道漏了哪些"时发这条指令
// （目前唯一触发源是 fp 那侧的 Redis 订阅重建）。代价是一波回源——
// 但只有真正被使用的 token 才会回源，相当于把一个 cache_ttl 周期的
// 回源压缩到更短的窗口里，而不是一次性尖峰。
func (a *Auth) onPurge() {
	a.cache.purge()
}
```

登录相关的三个方法是薄封装：

```go
// LoginInput 是一次登录请求。
type LoginInput struct {
	ConnectorType string
	Credentials   map[string]string
	IP            string
	UserAgent     string
	Mobile        bool
}

// LoginResult 是登录成功的结果。
type LoginResult struct {
	Token     string
	SessionID string
	User      *fpv1.UserInfo
}

// SendLoginCode 给手机号发送登录验证码。
func (a *Auth) SendLoginCode(ctx context.Context, phone string) error {
	_, err := a.c.rpc.SendLoginCode(ctx, &fpv1.SendLoginCodeRequest{Phone: phone})
	return translate(err)
}

// Login 用凭据换取会话 token。
func (a *Auth) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	res, err := a.c.rpc.Login(ctx, &fpv1.LoginRequest{
		ConnectorType: in.ConnectorType,
		Credentials:   in.Credentials,
		Ip:            in.IP,
		UserAgent:     in.UserAgent,
		Mobile:        in.Mobile,
	})
	if err != nil {
		return nil, translate(err)
	}
	return &LoginResult{Token: res.GetToken(), SessionID: res.GetSessionId(), User: res.GetUser()}, nil
}

// Logout 撤销一个 token，并立即清掉本地缓存。
//
// 清缓存不能省：撤销推送会异步到达，但同一进程内紧接着的请求可能在
// 事件到达前就命中了那条缓存——用户点了退出，下一个请求还是登录态。
func (a *Auth) Logout(ctx context.Context, token string) error {
	_, err := a.c.rpc.Logout(ctx, &fpv1.LogoutRequest{Token: token})
	a.cache.drop(token)
	return translate(err)
}

// translate 把 gRPC status 转成 SDK 的哨兵错误，
// 让业务方用 errors.Is 判定而不必 import grpc 的 codes 包。
func translate(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied:
		return errors.Join(ErrUnauthorized, err)
	case codes.Unavailable, codes.DeadlineExceeded:
		return errors.Join(ErrUnavailable, err)
	}
	return err
}
```

`sdk/client.go` 的三处改动：

```go
// Client 结构体加字段：
	// auth 在 New 里构造一次，之后不再替换，因此无需同步保护。
	auth *Auth

// 访问器：
// Auth 返回认证能力。多次调用返回同一个实例。
func (c *Client) Auth() *Auth { return c.auth }
```

`New` 里在**启动 watch goroutine 之前**构造 `auth`（含缓存）——goroutine 一跑起来就可能调 `c.auth`，构造顺序反了就是 nil 解引用，而它只在恰好有事件到达时才崩，本地测试多半复现不出来。

`watchOnce` 把 Task 8 留下的三处日志换成真正的动作：

```go
		case msg.GetReady() != nil:
			c.streamUp.Store(true)
			// 重连后必须清空缓存：断开期间发生的撤销一条都没收到，
			// 缓存里的任何条目都可能是已被撤销的会话。
			// 首次连接也走这条路径，此时缓存本来就是空的，无害。
			c.auth.onPurge()
		case msg.GetRevoke() != nil:
			c.auth.onRevoke(msg.GetRevoke())
		case msg.GetPurge() != nil:
			c.opts.Logger.Warn("fpsdk: 按服务端要求清空校验缓存",
				"reason", msg.GetPurge().GetReason())
			c.auth.onPurge()
```

> **为什么明明有降级收紧、还要在 ready 时 purge：** 流断开期间 `StreamHealthy()` 为 false，
> 校验窗口已被收紧到 `DegradedCacheTTL`，绝大部分暴露已经被这一层挡住了。
> purge 补的是**检测延迟那一段**——从连接实际断掉到 `streamUp` 翻成 false，
> 硬断开是立即的，但静默黑洞要等满 keepalive `Timeout`（10 秒）。
> 那 10 秒里写入的条目带着完整 TTL，只有 purge 能清掉它们。
>
> **代价要知道：** fp 服务端配了 `MaxConnectionAge`（30 分钟，见 Task 7），
> 所以每个 SDK 实例大约每半小时会主动重连一次，每次都 purge。表现是
> 回源量周期性抬头——不是尖峰，因为只有真正被使用的 token 才会回源，
> 相当于把一个 `cache_ttl` 周期的回源压缩到更短的窗口里。
> grpc-go 给 `MaxConnectionAge` 自带 ±10% 抖动，多实例不会同时到期。

- [ ] **Step 5: 跑测试并提交**

```bash
./scripts/test.sh ./sdk/...
```

```bash
git add sdk go.mod go.sum
```

```bash
git commit -m "feat(sdk): Auth 模块——缓存校验、singleflight、撤销消费与降级"
```

---

## Task 11: HTTP 中间件

业务方接入 fp 的实际入口。一行 `client.Auth().Middleware(h)` 就该拿到鉴权、轮换交接与降级。

**Files:**
- Create: `sdk/middleware.go`
- Test: `sdk/middleware_test.go`

**Interfaces:**
- Produces: `(*Auth).Middleware(next http.Handler) http.Handler`
- Produces: `(*Auth).MiddlewareWith(opts MiddlewareOptions) func(http.Handler) http.Handler`
- Produces: `fpsdk.IdentityFrom(ctx context.Context) (*Identity, bool)`
- Produces: `fpsdk.MiddlewareOptions`

---

### 轮换交接在这一层收口

Task 3 让 fp 在过渡期内**重复告知**新 token，Task 10 让缓存条目带上 `rotatedTo`。本任务是最后一棒：**每一次**校验通过（无论命中缓存还是回源）只要 `RotatedTo` 非空，就必须把新 token 交付给客户端。

漏掉这一棒，前两个任务的努力全部作废——而症状要等到 `rotate_interval`（默认 24 小时）之后才在线上显形：一批用户在没有任何操作的情况下集体掉线，日志里看不出任何异常。

---

- [ ] **Step 1: 写失败测试**

`sdk/middleware_test.go`：

```go
package fpsdk

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

func okHandler(seen *Identity) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := IdentityFrom(r.Context()); ok {
			*seen = *id
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddlewareRejectsMissingToken(t *testing.T) {
	env := newStubEnv(t, okValidate("u1", 30_000))
	called := false
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 返回 %d，期望 401", rec.Code)
	}
	if called {
		t.Fatal("无 token 时业务 handler 仍被调用了")
	}
}

func TestMiddlewareAcceptsBearerAndCookie(t *testing.T) {
	env := newStubEnv(t, okValidate("u1", 30_000))
	var seen Identity
	h := env.auth.Middleware(okHandler(&seen))

	t.Run("Bearer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || seen.UserID != "u1" {
			t.Fatalf("code=%d userID=%q", rec.Code, seen.UserID)
		}
	})
	t.Run("Cookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || seen.UserID != "u1" {
			t.Fatalf("code=%d userID=%q", rec.Code, seen.UserID)
		}
	})
}

// TestMiddlewareRelaysRotatedTokenOnEveryRequest 是本任务的核心。
//
// fp 在过渡期内对每一次带旧 token 的校验都重复告知新 token（Task 3），
// SDK 缓存条目也带着 rotatedTo（Task 10）。中间件是最后一棒——
// 只在首次回源时交付、缓存命中时不交付的话，前两个任务全部作废，
// 因为绝大多数请求都是缓存命中。
//
// 症状要等到 rotate_interval（默认 24 小时）之后才显形：
// 一批用户在毫无操作的情况下集体掉线，日志里看不出任何异常。
func TestMiddlewareRelaysRotatedTokenOnEveryRequest(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return &fpv1.ValidateTokenResponse{
			UserId: "u1", SessionId: "s1", CacheTtlMs: 30_000,
			Rotated: true, NewToken: "new-tok",
		}, nil
	})
	var seen Identity
	h := env.auth.Middleware(okHandler(&seen))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer old-tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		var got string
		for _, c := range rec.Result().Cookies() {
			if c.Name == DefaultCookieName {
				got = c.Value
			}
		}
		if got != "new-tok" {
			t.Fatalf("第 %d 次请求没有回写新 token 的 cookie（得到 %q）——"+
				"缓存命中时交接被截断，会话将在过渡期后死亡", i, got)
		}
		if h := rec.Header().Get(RotatedTokenHeader); h != "new-tok" {
			t.Fatalf("第 %d 次请求缺少 %s 响应头（得到 %q）——"+
				"非浏览器客户端无从得知新 token", i, RotatedTokenHeader, h)
		}
	}
}

// TestRotationCookieIsWrittenBeforeHandlerWrites 守住一个 net/http 陷阱。
//
// Set-Cookie 只有在 WriteHeader 之前写进 Header() 才会真的发出去。
// 中间件若在 next.ServeHTTP 之后再回写 cookie，对任何已经写过响应的
// handler 都是静默失效——而"handler 写响应"就是 handler 的全部工作。
func TestRotationCookieIsWrittenBeforeHandlerWrites(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return &fpv1.ValidateTokenResponse{
			UserId: "u1", CacheTtlMs: 30_000, Rotated: true, NewToken: "new-tok",
		}, nil
	})
	// 这个 handler 立刻写响应头并写 body，之后再改 Header() 就没用了。
	h := env.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer old-tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("handler 写过响应后 cookie 就丢了——回写发生在 next 之后")
	}
}

// TestSecureCookieIsOptIn 记录一个刻意的默认值。
//
// Secure 默认 false：SDK 无从知道业务方跑在 HTTP 还是 HTTPS 后面，
// 默认打开会让所有本地开发环境的 cookie 静默失效——而"cookie 没生效"
// 是最难查的一类问题。生产必须显式设 true，demo 与文档都要写明。
func TestSecureCookieIsOptIn(t *testing.T) {
	// …分别用默认 opts 与 CookieSecure: true 跑一次，
	// 断言 cookie 的 Secure 属性符合配置。
}

// TestMiddlewareUnavailableReturns503 守住"认证服务不可用 ≠ 你没通过认证"。
//
// **上面那几条测试都测不到这一条**——把 503 分支删掉（让它落到 default 的
// 401），它们依然全绿。因为它们验证的是"该拒的拒了"，而 401 和 503 都是拒。
//
// 但两者对客户端的含义完全相反：401 的语义是"你的凭据无效"，客户端据此
// 会清掉 cookie 让用户重新登录。fp 抖动一下就把全体用户踢下线——
// 一次几秒的服务端故障被放大成一场登录风暴，而那些 token 其实完全有效。
//
// 断言必须落在**状态码**上（503），不能只断言"没放行"。
func TestMiddlewareUnavailableReturns503(t *testing.T) {
	// …让桩服务端返回 codes.Unavailable，且该 token 没有任何缓存条目
	//（有缓存的话会走陈旧兜底或直接命中，到不了这条分支）。
	// 断言 rec.Code == http.StatusServiceUnavailable。
}

// TestMiddlewareDoesNotEchoToken 守住不把凭据写进响应体。
//
// 把 token 拼进错误信息（"token xxx 无效"）会让它进入前端日志、
// 浏览器控制台、错误上报平台——一个仍然有效的凭据就此四处流传。
func TestMiddlewareDoesNotEchoToken(t *testing.T) {
	env := newStubEnv(t, failValidate())
	h := env.auth.Middleware(okHandler(new(Identity)))

	const token = "非常独特的令牌值-7c1e"
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), token) {
		t.Fatalf("响应体里回显了 token: %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
./scripts/test.sh ./sdk/ -run 'TestMiddleware|TestRotationCookie|TestSecureCookie'
```

- [ ] **Step 3: 实现中间件**

`sdk/middleware.go`：

```go
package fpsdk

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// DefaultCookieName 是默认的会话 cookie 名。
const DefaultCookieName = "fp_token"

// RotatedTokenHeader 是 token 轮换时回传新 token 的响应头。
//
// 除了 cookie 还要发一个响应头：移动端 / 服务间调用这类非浏览器客户端
// 不处理 Set-Cookie，光靠 cookie 交接对它们完全无效。
const RotatedTokenHeader = "X-Fp-New-Token"

type identityCtxKey struct{}

// IdentityFrom 从请求上下文里取出已认证身份。
func IdentityFrom(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(*Identity)
	return id, ok
}

// MiddlewareOptions 定制中间件行为。零值即默认行为。
type MiddlewareOptions struct {
	// TokenFrom 自定义 token 提取。为 nil 时先看 Authorization: Bearer，
	// 再看名为 CookieName 的 cookie。
	TokenFrom func(*http.Request) string
	// CookieName 是会话 cookie 名，为空时用 DefaultCookieName。
	CookieName string
	// CookieSecure 决定回写的 cookie 是否带 Secure。
	//
	// 默认 false：SDK 无从知道业务方跑在 HTTP 还是 HTTPS 后面，默认打开
	// 会让所有本地开发环境的 cookie 静默失效。**生产必须显式设为 true**，
	// 否则会话 cookie 会在任何一段明文 HTTP 上原样出现。
	CookieSecure bool
	// CookiePath / CookieDomain / CookieSameSite 是回写 cookie 的其余属性。
	CookiePath   string
	CookieDomain string
	CookieSameSite http.SameSite

	// OnRotate 覆盖默认的新 token 交付方式。
	OnRotate func(w http.ResponseWriter, r *http.Request, newToken string)
	// OnError 覆盖默认的失败响应（默认写 401，响应体不含 token）。
	OnError func(w http.ResponseWriter, r *http.Request, err error)
}

// Middleware 用默认配置返回鉴权中间件。
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return a.MiddlewareWith(MiddlewareOptions{})(next)
}

// MiddlewareWith 返回一个可定制的鉴权中间件构造器。
func (a *Auth) MiddlewareWith(opts MiddlewareOptions) func(http.Handler) http.Handler {
	if opts.CookieName == "" {
		opts.CookieName = DefaultCookieName
	}
	if opts.CookiePath == "" {
		opts.CookiePath = "/"
	}
	if opts.TokenFrom == nil {
		opts.TokenFrom = defaultTokenFrom(opts.CookieName)
	}
	if opts.OnRotate == nil {
		opts.OnRotate = defaultOnRotate(opts)
	}
	if opts.OnError == nil {
		opts.OnError = defaultOnError
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := a.Validate(r.Context(), opts.TokenFrom(r))
			if err != nil {
				opts.OnError(w, r, err)
				return
			}

			// 交付必须在 next 之前：Set-Cookie 只有写在 WriteHeader 之前
			// 才会真的发出去，而 handler 的第一件事往往就是写响应。
			//
			// 每次都交付，不只在回源时——缓存条目带着 rotatedTo，
			// 而绝大多数请求是缓存命中。只在回源时交付等于把 fp 侧
			// "过渡期内重复告知"的修复在最后一棒重新打破。
			if id.RotatedTo != "" {
				opts.OnRotate(w, r, id.RotatedTo)
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityCtxKey{}, id)))
		})
	}
}

func defaultTokenFrom(cookieName string) func(*http.Request) string {
	return func(r *http.Request) string {
		if h := r.Header.Get("Authorization"); h != "" {
			if v, ok := strings.CutPrefix(h, "Bearer "); ok {
				return strings.TrimSpace(v)
			}
		}
		if c, err := r.Cookie(cookieName); err == nil {
			return c.Value
		}
		return ""
	}
}

func defaultOnRotate(opts MiddlewareOptions) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, _ *http.Request, newToken string) {
		http.SetCookie(w, &http.Cookie{
			Name:     opts.CookieName,
			Value:    newToken,
			Path:     opts.CookiePath,
			Domain:   opts.CookieDomain,
			HttpOnly: true,
			Secure:   opts.CookieSecure,
			SameSite: opts.CookieSameSite,
		})
		// 非浏览器客户端不处理 Set-Cookie，必须另给一条明路。
		w.Header().Set(RotatedTokenHeader, newToken)
	}
}

// defaultOnError 写一个不含任何凭据的 401。
//
// 绝不要把 token 拼进错误信息：它会流进前端日志、浏览器控制台、
// 错误上报平台——一个仍然有效的凭据就此四处流传。
func defaultOnError(w http.ResponseWriter, _ *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNoToken), errors.Is(err, ErrUnauthorized):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case errors.Is(err, ErrUnavailable):
		// 503 而不是 401：认证服务不可用不是"你没通过认证"。
		// 回 401 会让客户端清掉一个其实完全有效的 token，把一次
		// fp 抖动放大成全体用户重新登录。
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}
```

- [ ] **Step 4: 跑测试并提交**

```bash
./scripts/test.sh ./sdk/...
```

```bash
git add sdk
```

```bash
git commit -m "feat(sdk): HTTP 鉴权中间件，含轮换交接与降级响应"
```

---

## Task 12: 端到端集成测试

前面每个任务都在自己的层内验证。本任务把 **SDK → gRPC → service → PG/Redis** 整条链路串起来跑，**第二阶段的验收标准就是它**。

放在 `internal/integration` 包里——只有这个包能同时 import `internal/*` 与 `sdk/`（`sdk/` 自身禁止 import `internal/`）。

**Files:**
- Create: `internal/integration/phase2_env_test.go`, `internal/integration/phase2_test.go`

**Interfaces:**
- Consumes: 全部包的公开 API

**验收对照表**：

| # | 验收项 | 覆盖它的测试 | 第一阶段状态 |
|---|---|---|---|
| 4 | SDK 用 appId/appSecret 初始化 | `TestSDKInitAndReject` | 当时只验了凭据校验 |
| 6 | 带 token 放行、无 token 401 | `TestMiddlewareEndToEnd` | 当时只验了 service 层 |
| 7 | 管理端踢下线后 token 失效 | `TestKickIsPushedToSDKImmediately` | 当时无推送，只验了 service 层 |
| 8 | fp 不可用时缓存期内仍能校验 | `TestSDKSurvivesFpOutage` | 当时只验了 cache_ttl 契约 |
| 新 | 轮换交接不丢 | `TestRotationHandoffSurvivesLostResponse` | — |

---

- [ ] **Step 1: 写集成环境**

`internal/integration/phase2_env_test.go`：起一个监听在 `127.0.0.1:0` 的**真实 gRPC 服务端**（不用 bufconn——本任务要验证"停掉 fp"的行为，必须能真的停掉一个监听），再用 `fpsdk.New` 连上去。

```go
// phase2Env 是一套真实端口上的 fp + 一个连上去的 SDK 客户端。
//
// 用真实回环监听而非 bufconn：验收项 8 要求"fp 不可用时业务不中断"，
// 而 bufconn 无法模拟"服务端消失"。这里必须能 Stop() 掉监听、
// 再在原端口重新起一个。
type phase2Env struct {
	addr   string
	server *grpcapi.Server
	sdk    *fpsdk.Client

	appID, appSecret string
	// …以及 service 层各组件，供测试直接操纵服务端状态（冻结、踢下线）
}

func newPhase2Env(t *testing.T, opts ...func(*fpsdk.Options)) *phase2Env
// stopFp 停掉 gRPC 监听，模拟 fp 宕机。
func (e *phase2Env) stopFp(t *testing.T)
// restartFp 在原地址重新起一个。
func (e *phase2Env) restartFp(t *testing.T)
```

> SDK 连接用 `Insecure: true`——测试环境没有 TLS。**这正是 `Insecure` 存在的理由，
> 也是它必须默认关闭的理由。** 集成测试里出现它是合理的，生产配置里出现就是事故。

- [ ] **Step 2: 写验收测试**

`internal/integration/phase2_test.go`：

```go
// TestSDKInitAndReject 覆盖验收项 4。
//
// 正确的 appId/appSecret 能建立连接并完成调用；错误的必须被拒。
func TestSDKInitAndReject(t *testing.T) {
	// …用正确凭据 New 一个 client，Login 成功；
	// 再用错误 secret New 一个，断言任意 RPC 返回 ErrUnauthorized。
	//
	// 注意：grpc.NewClient 是惰性的，凭据错误不会在 New 时报错，
	// 只在第一次 RPC 时暴露。断言要放在 RPC 上，不能放在 New 上——
	// 放在 New 上的断言会永远通过，看起来在测却什么都没测。
}

// TestMiddlewareEndToEnd 覆盖验收项 6。
func TestMiddlewareEndToEnd(t *testing.T) {
	// …起一个用 SDK 中间件保护的 httptest.Server：
	//   带合法 token → 200，且 handler 拿到正确的 userID
	//   不带 token   → 401
	//   带乱码 token → 401
}

// TestKickIsPushedToSDKImmediately 覆盖验收项 7，是撤销推送的全部价值所在。
//
// 关键在"立即"：没有推送的话，被踢下线的用户会继续通行整整一个
// cache_ttl（默认 30 秒）。本测试把 cache_ttl 配成很长（比如 10 分钟），
// 这样只要断言"踢完之后很快就被拒"，就只可能是推送起了作用——
// TTL 兜底在这个时间尺度上根本来不及。
func TestKickIsPushedToSDKImmediately(t *testing.T) {
	// …cache_ttl 配 600 秒；登录、校验一次（进缓存）、
	// 服务端 accounts.RevokeAllSessions，
	// 然后轮询断言 SDK 侧在数秒内开始拒绝。
}

// TestSDKSurvivesFpOutage 覆盖验收项 8——"fp 一挂，业务不中断"。
//
// 这是整个 opaque token + 本地缓存方案存在的理由。若不成立，
// fp 就成了每个接入方每条请求路径上的强依赖，比各自实现一套用户体系更糟。
func TestSDKSurvivesFpOutage(t *testing.T) {
	// …登录并校验一次（进缓存），stopFp()，
	// 断言 cache_ttl 内的校验仍然成功且不产生任何网络调用。
	//
	// 再断言两件事：
	//   1. 一个**没缓存过**的 token 在 fp 挂掉时返回 ErrUnavailable
	//      （不是 ErrUnauthorized——业务方据此回 503 而不是 401，
	//       否则客户端会清掉一个其实有效的 token）
	//   2. 开了 AllowStaleOnOutage 的 client，在 cache_ttl 过期之后
	//      仍能拿到同一个身份，且 Identity.Stale 为 true
}

// TestRotationHandoffSurvivesLostResponse 是 Task 3 + Task 10 + Task 11 的合验。
//
// 模拟真实的失败模式：轮换发生时，拿到 new_token 的那个响应被丢弃
// （请求取消 / 页面忽略响应体）。客户端仍握着旧 token 继续请求——
// 必须能重新拿到新 token，而不是在过渡期结束时静默登出。
func TestRotationHandoffSurvivesLostResponse(t *testing.T) {
	// …rotate_interval 配 1 秒、cache_ttl 配**允许的最小值**（1 秒）。
	// 注意 cache_ttl **不能配 0**——domain.SessionPolicy.Validate() 要求它为正。
	// 配最小值加上请求之间 >1 秒的间隔即可强制每次回源；
	// GraceDuration = max(15s, cfg) 不受影响，本测试依赖的 15 秒过渡窗口不变。
	// （原意是强制每次回源，
	// 以隔离出 fp 侧的重复告知能力）；
	// 登录、等过 rotate_interval、
	// 第一次校验拿到 newToken 后**丢弃它**，
	// 再用旧 token 校验两次，断言每次都拿到同一个 newToken。
}

// TestStreamOutageTightensCacheWindow 验证降级策略端到端生效。
func TestStreamOutageTightensCacheWindow(t *testing.T) {
	// …cache_ttl 配 600 秒、DegradedCacheTTL 配 1 秒；
	// 登录并缓存，stopFp()，等 StreamHealthy() 变 false，
	// 断言 1 秒后同一个 token 不再命中缓存（转而尝试回源并失败）。
	// 这条测试证明收紧对**存量条目**生效——它是 Task 9 那条设计的端到端体现。
}

// TestRedisSubscriptionBlipDoesNotSilentlyLoseRevocations 是"丢事件必须可观测"
// 这条改动的验收标准。**在改造之前它必然失败。**
//
// 复现的是生产上真实会发生的一幕：Redis 故障转移或网络抖动打断了 fp 的
// 订阅连接。go-redis 会静默重连并重发 SUBSCRIBE——不报错、不关 channel——
// 那个窗口里发布的撤销事件对这台 fp 永久丢失，而 SDK 看到的流一直是健康的。
//
// 改造前：SDK 在整个 cache_ttl 内继续放行一个已被踢下线的用户，
//        且日志、监控、流状态里没有任何异常痕迹。
// 改造后：fp 从"第二次 SUBSCRIBE 确认"察觉到缺口，广播 Purge，SDK 清空缓存，
//        下一次请求回源被拒。
func TestRedisSubscriptionBlipDoesNotSilentlyLoseRevocations(t *testing.T) {
	// 装配要点：
	//
	// 1. cache_ttl 配成很长（比如 600 秒）。这是本测试成立的关键——
	//    只有让 TTL 兜底彻底来不及，才能证明失效是 Purge 带来的。
	//    TTL 配短的话，一个"什么都没做"的实现也会通过。
	//
	// 2. 登录并校验一次，确认条目已进 SDK 本地缓存
	//    （再校验一次，断言没有产生新的回源）。
	//
	// 3. 掐掉 fp 的那条订阅连接，而不是整个 Redis：
	//       CLIENT LIST TYPE pubsub   → 找到订阅连接的 id
	//       CLIENT KILL ID <id>       → 只杀它
	//    用 TYPE pubsub 过滤很重要——杀错连接会把会话存储也一起断掉，
	//    那样测出来的是"Redis 挂了"，不是"订阅抖动了"。
	//
	// 4. 触发撤销（accounts.RevokeAllSessions）。
	//
	// 5. 断言 SDK 在数秒内开始拒绝该 token。
	//    数秒 << 600 秒的 cache_ttl，所以失效只可能来自 Purge。
	//
	// ⚠️ **上面这个写法是错的，它检测不到 Purge 的缺失。** 实测：把
	// broadcastPurge 整个废掉，这条测试仍然 100% 通过。原因是 go-redis 的
	// 自动重订阅往往快到目标 token 的撤销事件仍会经**正常扇出路径**送达——
	// 与 Purge 有没有触发完全无关。也就是说，它验证的是"撤销最终生效了"，
	// 而不是"缺口被检测到并转成了 Purge"。
	//
	// **正确写法：引入一个 canary token。**
	//
	// 除了要被撤销的那个 token，再登录第二个会话拿到 canary token，
	// 同样预热进 SDK 缓存。然后**直接在 store 层删掉 canary 的会话**
	// （SessionStore.Delete，绕开 SessionService.Revoke → announce → Publish
	// 这条链路），使它**根本没有对应的撤销事件存在**。
	//
	// 于是 canary 的缓存条目只有一条失效途径：一次全量 Purge。
	// 它不可能被任何巧合送达的事件命中。
	//
	// 断言写在 canary 上：掐掉订阅连接后，断言**canary token** 在数秒内
	// 开始被拒。这才是"缺口被检测到并转成了 Purge"的唯一证据。
	//
	// 变异验证（必做）：废掉 broadcastPurge，这条测试必须变红。
}

// TestRevokeCrossesFpInstances 是多实例部署的核心验证（设计决策 4.5）。
//
// 生产上 fp 是多实例挂在 LB 后面：SDK 的 Watch 流只落在其中一台，而管理员
// 的踢下线操作会落在另一台，两台之间没有任何直连。它们之间靠 Redis
// pub/sub 桥接——每个实例都 SUBSCRIBE 同一个频道。
//
// 这条链路**没有任何单实例测试能覆盖**：单实例下"发布"和"订阅"发生在
// 同一个进程里，即便有人把广播实现成"只通知本进程的 hub"（完全不碰 Redis），
// 前面所有测试照样全绿——而线上会表现为"踢下线时灵时不灵"，
// 灵不灵取决于 LB 把管理请求分给了哪台机器。这是那种只有到了生产、
// 只有在多实例下、还只是概率性出现的故障。
func TestRevokeCrossesFpInstances(t *testing.T) {
	// 装配：**两个** fp 实例，各自监听不同端口，但共用同一套 PG + Redis。
	//   instanceA := newPhase2Env(t)
	//   instanceB := instanceA.spawnPeer(t)   // 复用同一个 pool/rdb，新起一个 grpcapi.Server
	//
	// 1. SDK 只连 instanceA，登录并校验一次（进本地缓存）
	//    cache_ttl 配成 600 秒，确保后面的失效只可能来自推送而非 TTL 到期
	// 2. 通过 **instanceB** 的 service 层触发撤销
	//    （instanceB.accounts.RevokeAllSessions(...)）
	// 3. 断言 SDK 在数秒内开始拒绝该 token
	//
	// 第 2 步必须走 instanceB。走 instanceA 的话这条测试退化成
	// TestKickIsPushedToSDKImmediately，一个只通知本进程的实现也能通过。
}

// TestSDKWorksAgainstAnyInstance 确认 fp 对会话确实无状态。
//
// 「不需要连接亲和」这条结论的全部依据就是它。一旦有人往 fp 进程里塞了
// 进程本地的会话状态（比如"顺手"加个本地 session 缓存省一次 Redis 读），
// SDK 换一台实例就会拿到不一致的答案，而 LB 什么时候换实例是不可预测的。
func TestSDKWorksAgainstAnyInstance(t *testing.T) {
	// 1. 通过 instanceA 登录，拿到 token
	// 2. 新建一个只连 instanceB 的 SDK client
	// 3. 断言它校验同一个 token 成功，且 userID / sessionID 与 A 给的一致
	// 4. 通过 instanceA 登出
	// 5. 断言连着 instanceB 的 client 也开始拒绝
}
```

- [ ] **Step 3: 跑全量测试**

```bash
./scripts/test.sh
```

期望全绿。集成测试有真实等待，比其余测试慢，这是预期内的。

- [ ] **Step 4: 提交**

```bash
git add internal/integration
```

```bash
git commit -m "test: 第二阶段端到端集成测试，覆盖验收项 4/6/7/8"
```

---

## Task 13: 可运行的示例业务服务

一个能真的跑起来的最小业务服务。它同时是三样东西：接入文档、手工验收工具、以及"接入 fp 到底要写多少代码"的诚实答案——**如果这个文件很长，说明 SDK 的 API 设计有问题。**

**Files:**
- Create: `examples/demo/main.go`
- Create: `examples/demo/README.md`
- Create: `scripts/demo.sh`

---

- [ ] **Step 1: 写 demo**

`examples/demo/main.go`。它应该只有这几件事：读环境变量 → `fpsdk.New` → 三个路由。

```go
// Command demo 是一个接入 fp 的最小业务服务。
//
// 它演示接入 fp 需要写的全部代码。如果这个文件变长了，
// 说明该把重复的部分挪进 SDK。
package main

func main() {
	client, err := fpsdk.New(fpsdk.Options{
		Addr:      os.Getenv("FP_ADDR"),
		AppID:     os.Getenv("FP_APP_ID"),
		AppSecret: os.Getenv("FP_APP_SECRET"),
		// 本地开发没有 TLS。生产环境绝不要开——
		// appSecret 会随每个 RPC 以明文发送。
		Insecure: os.Getenv("FP_INSECURE") == "1",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	auth := client.Auth()
	mux := http.NewServeMux()

	// 公开路由：发验证码、登录。
	mux.HandleFunc("POST /api/login/code", func(w http.ResponseWriter, r *http.Request) { … })
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		// …auth.Login(…) 成功后把 token 写进 cookie
	})

	// 受保护路由：一行中间件。
	mux.Handle("GET /api/me", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := fpsdk.IdentityFrom(r.Context())
		writeJSON(w, map[string]any{"userId": id.UserID, "stale": id.Stale})
	})))

	// 健康检查同时暴露推送流状态，方便手工验收时观察降级。
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"streamHealthy": client.StreamHealthy()})
	})

	log.Fatal(http.ListenAndServe(":8090", mux))
}
```

- [ ] **Step 2: 写 README 与启动脚本**

`examples/demo/README.md` 写清楚手工验收的四步，每一步都要说明**该看到什么**：

1. 起 fp（`./scripts/run.sh`），在管理端建应用、启用 `sms_code`，记下 appId/appSecret
2. 起 demo（`./scripts/demo.sh`），`POST /api/login/code` 再 `POST /api/login`，拿到 token
3. `GET /api/me` 带 token → 200；不带 → 401
4. **杀掉 fp 进程**，立刻再 `GET /api/me` → **仍然 200**（缓存内）。
   `GET /healthz` 的 `streamHealthy` 会变成 `false`。
   等超过 `DegradedCacheTTL` 后再请求 → 503（不是 401）

第 4 步是整个第二阶段的价值所在，README 里要写明它为什么重要。

`scripts/demo.sh` 与 `run.sh` 同构，从 `.env.local` 读 `FP_APP_ID` / `FP_APP_SECRET`。
**这两个值属于凭据，与 PG/Redis 密码一样只能进 git-ignored 的 `.env.local`**，
`.env.example` 里留空字段。

- [ ] **Step 3: 手工走一遍验收**

按 README 的四步实际操作一遍。第 4 步没能返回 200 就是降级路径有问题，回到 Task 10 排查。

- [ ] **Step 4: 提交**

```bash
git add examples scripts .env.example
```

```bash
git commit -m "docs: 可运行的接入示例与手工验收步骤"
```

---

## 六、第二阶段验收对照表

| # | 验收项 | 由谁保证 |
|---|---|---|
| 1 | SDK 一行接入鉴权 | Task 11 `Middleware`，Task 13 demo 实证 |
| 2 | 回源走热连接，不付冷连接代价 | Task 8 `Watch` 长流 + keepalive |
| 3 | 缓存挡住绝大多数回源 | Task 9 + Task 10（`TestValidateCachesAndAvoidsRefetch`） |
| 4 | 并发 miss 不惊群 | Task 10 singleflight |
| 5 | `cache_ttl == 0` 不缓存 | Task 1 wire 语义 + Task 9 `put` + Task 10 端到端 |
| 6 | 踢下线近乎实时生效 | Task 6 中继 + Task 10 消费 + Task 12 端到端 |
| 7 | 推送断开时自动收紧窗口 | Task 9 读取时施加上限 + Task 10 + Task 12 |
| 8 | 轮换交接不丢 | Task 3（fp 重复告知）+ Task 10（缓存带 `rotatedTo`）+ Task 11（每次交付） |
| 9 | fp 挂掉业务不中断 | Task 10 降级 + Task 12 + Task 13 第 4 步手工验收 |
| 10 | 跨应用不串 token | Task 4 拦截器 + Task 5（`TestTokenFromAnotherAppIsRejected`） |
| 11 | 撤销事件不跨应用泄露 | Task 6（`TestScopedRevokeDoesNotLeakToOtherApps`） |
| 12 | 关闭进程不挂死 | Task 7 关闭顺序 + `TestShutdownCompletesWithOpenWatchStream` |
| 13 | 冻结/改密不留竞态窗口 | Task 2 纪元（可选任务；砍掉则本项降级为"窗口很窄"） |
| 14 | **多实例下撤销能跨实例送达** | 设计决策 4.5 + Task 6 每实例一份 Redis 订阅 + Task 12（`TestRevokeCrossesFpInstances`） |
| 15 | **SDK 连任意实例结果一致（无需连接亲和）** | fp 对会话无状态 + Task 12（`TestSDKWorksAgainstAnyInstance`） |
| 16 | **fp 扩容后存量连接会重新分摊** | Task 7 `MaxConnectionAge`（30 分钟 + 5 分钟宽限，自带 ±10% 抖动） |
| 17 | **fp 的 Redis 订阅抖动不会静默丢撤销** | Task 6 重订阅检测 → `WatchPurge` + Task 8 SDK 消费 + Task 12（`TestRedisSubscriptionBlipDoesNotSilentlyLoseRevocations`） |
| 18 | **批量撤销只发一条广播，且单条有体积上限** | Task 2（`TestRevokeUsersEmitsOneEventForManyUsers` / `TestLargeRevocationIsSplitIntoBoundedEvents`） |

---

## 七、给执行者的最后几句

**三条最容易写成"看起来对"的地方**，评审时优先看它们：

1. **Task 9 的 TTL 上限施加时机。** 写进 `put` 里功能完全正确，测试也能绿，但在流断开的那一刻对满缓存的存量条目毫无作用——而那正是唯一需要它的时刻
2. **Task 11 的轮换交付时机。** 只在回源时交付、缓存命中时跳过，99% 的请求都走缓存，等于没做
3. **Task 4 的凭据缓存只缓存成功。** 缓存失败在功能上更"完整"，代价是把 map 的大小交给攻击者

**两条数值约束，错了不报错只出怪事：**

- SDK 的 keepalive `Time`（30 秒）必须**大于** fp 的 `EnforcementPolicy.MinTime`（10 秒）。反了会被服务端周期性掐断连接
- `cache_ttl_ms` 是**毫秒**。写成秒会让回源量放大一千倍，而功能一切正常

### 本计划自身的已知不足

下面这些地方只给了意图、结构与断言点，**没有给完整代码**。它们要么依赖真实监听的起停时序、要么是对已有装配的复用，写死反而会误导：

| 位置 | 内容 |
|---|---|
| Task 5 | `newGRPCEnv` 的 service 层装配（复用 `internal/service/auth_test.go` 的 `authEnv`） |
| Task 8 | `newStubEnv` 的监听装配 |
| Task 8 | `TestStreamHealthyOnlyAfterReady`、`TestStreamHealthyGoesFalseOnDisconnect`、`TestWatchReconnects`、`TestCloseStopsWatchLoop` |
| Task 10 | `TestStreamDownTightensCacheWindow`、`TestStaleFallbackServesLastKnownIdentity` |
| Task 11 | `TestSecureCookieIsOptIn` |
| Task 12 | `phase2Env` 与全部六个验收测试 |
| Task 13 | demo 的登录 handler |

**断言点本身是硬要求。** 装配方式可以自选，等待策略可以自选，但每条测试注释里写明的那个"必须成立的性质"不得省略，也不得弱化成"能跑通就行"——那些注释解释的正是这条测试为什么值得存在。

另外：Task 8 的四条测试里，**重连测试必须用真实回环监听**（`net.Listen("tcp", "127.0.0.1:0")` 后在同端口重启），bufconn 模拟不了"服务端消失又回来"。这一条不是风格选择。

---
