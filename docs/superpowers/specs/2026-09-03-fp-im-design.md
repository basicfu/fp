# fp-im 设计：无队列的连接网关

**日期**：2026-09-03
**状态**：设计已定，待写实施计划
**取代**：`2026-09-02-fp-im-message-flow.md`（已删除，那一版把 im 做成了有状态的收件箱，本文推翻它）

---

## 一、定位与约束

fp-im 是一个通用的连接网关：管 ws 连接、管路由、把字节从一端搬到另一端。它**不存消息、不解析 payload、不理解业务**。可靠性由业务方端到端闭环，fp-im 只提供闭环所需的三个信号（见第九节）。

三条部署约束，全文设计都建立在它们之上：

1. 业务 server 通过 SDK 用 gRPC 连 fp-im 的 LB，落到**随机一个**节点，维持一条双向流。
2. client 通过 ws 连 fp-im 的 LB，落到**随机一个**节点。
3. im 节点之间**只通过 Redis 互通**，不做 gRPC 互联。Redis 为 8.0 以上，可能是 Cluster。

目标量级：全网每秒数万条消息，单节点十万级连接。热路径每条消息最多两条 Redis 命令；需要时可开启写管道攒批，让往返次数与消息量脱钩。

fp-im 当前依赖 `fpsdk`（验 token、拉 app 配置）。依赖通过两个接口隔离（第十一节），将来拆开成本很低。

---

## 二、术语

| 术语 | 含义 |
|---|---|
| **app** | 接入应用，来自 token 的 claim 或访客握手帧。所有 key、路由都按 app 隔离 |
| **subject** | 连接的主体，带类型前缀。登录用户 `u:{uid}`，访客 `g:{uuid}`。SDK 暴露为 `Subject{Kind, ID}` |
| **connId** | 一条 ws 连接的唯一 id，im 生成，uuidv7。一个 subject 可有多条连接（多终端） |
| **nodeId** | im 节点 id = 主机标识 + 启动时间戳。每次启动都是新节点，避免重启后旧条目被当成活的 |
| **server 流** | 业务 server 的 SDK 与某个 im 节点之间的一条 gRPC 双向流。一个节点可能持有同一 app 的多条流，也可能一条都没有 |
| **session** | 在 SDK API 语境里指 subject 名下的一条连接，等同 connId |

---

## 三、总览

### 3.1 拓扑

```
 client ──ws──► LB ──► im-A ┐
 client ──ws──► LB ──► im-B ├──── 只通过 Redis 互通（sharded pub/sub + 三张表）
 server ─SDK gRPC─► LB ──► im-C ┘
```

节点完全对称：任何节点都可能同时持有任意 subject 的 ws 连接和任意 app 的 server 流。

### 3.2 Redis 里的三张表

| key | 类型 | 内容 | 维护 |
|---|---|---|---|
| `fp:im:node` | hash | nodeId → 心跳时间戳 | 每节点每 3 秒 `HSET`。超过 10 秒未刷新视为死亡（两个值都是配置） |
| `fp:im:srv:{app}` | hash | nodeId → 心跳时间戳 | 持有该 app 至少一条 server 流的节点每 3 秒 `HSET`；最后一条流断开时立即 `HDEL` |
| `fp:im:{app:subject}:conn` | hash | connId → 连接元数据 JSON | 握手时写，断开时删。field 级 `HEXPIRE`，持有连接的节点周期续期 |

`{app:subject}` 是 Cluster 的 hash tag，例如 `fp:im:{a1:u:1001}:conn`。每个 subject 一个 key，理由：Cluster 下按 subject 均匀分片，不会出现一个 app 一个大 hash 的热点；hash 删空最后一个 field 时 Redis 自动删 key，所以 key 数等于此刻在线的 subject 数；握手脚本只需锁一个 key。

连接元数据 JSON：

```json
{"n":"im-B","os":"android","m":true,"ts":1756800000}
```

`n` 所在节点，`os` 取值 `android | ios | windows | mac | linux | other`，`m` 是否移动端，`ts` 连接时间。不存原始 UA。

**条目有效性的唯一判据：`n` 在 `fp:im:node` 存活列表里。** 活着的节点一定会在关闭连接时 `HDEL` 自己的条目，所以"节点活着但条目是假的"不存在。死节点的残留条目在所有读路径上被过滤（Push、Sessions、握手脚本），并在该 subject 下次握手时被脚本顺手删掉，最晚在 field TTL（默认 30 分钟）到期时自动消失。残留期间只占内存，不影响任何判定。

field TTL 由持有连接的节点续期：每个节点每 `conn.field_renew`（默认 10 分钟）对本地全部连接按 subject key 分组，每组发一条 `HEXPIRE key ttl FIELDS n connId...`，进写管道。10 万连接的节点是每秒约 170 条命令。所有 field 过期后 hash 自动删除。

### 3.3 频道

统一用 sharded pub/sub（`SSUBSCRIBE` / `SPUBLISH`）。单机 Redis 7 以上同样支持这两条命令，所以不区分模式。普通 `PUBLISH` 在 Cluster 里是全集群广播，每秒数万条扛不住。启动时执行一次 `INFO cluster`，`cluster_enabled:1` 则用 Cluster 客户端，否则用单机客户端，不需要配置项。

| 频道 | 谁订阅 | 承载 |
|---|---|---|
| `fp:im:node:{nodeId}` | 该节点自己，启动时订阅一次 | 所有定向消息：`MSG`（给某 subject 的推送）、`UP`（转给 server 的消息和事件）、`KICK` |

第一版只有这一类频道。每个节点在 Redis 上只有一个订阅，订阅数与连接数无关。

### 3.4 节点本地状态

| 内存结构 | 内容 |
|---|---|
| 连接表 | `app:subject → []conn`，每条 conn 带 connId、ws、发送队列、os/mobile |
| server 流表 | `app → []stream` |
| 存活节点缓存 | `fp:im:node` 的快照，每 3 秒 `HGETALL` 刷新 |
| server 节点缓存 | 每个已知 app 的 `fp:im:srv:{app}` 快照，每 3 秒 `HGETALL` 刷新 |
| 写管道 | 一条，热路径命令全部经它发出。默认每条命令立即发送；配置 `pipeline.flush_interval` / `pipeline.flush_size` 后攒批，两者先到为准。Cluster 客户端按槽并行发往各分片 |

### 3.5 帧格式

ws 帧用 JSON（第一版；后续可加二进制协商）：

| 方向 | 帧 | 说明 |
|---|---|---|
| client→im | `{"t":"auth","token":"…"}` 或 `{"t":"auth","guest":"<uuid>","app":"a1"}`，可选 `ua`、`os`、`mobile` | 握手，连接建立后第一帧，5 秒内不发则关闭 |
| im→client | `{"t":"hello","conn":"<connId>"}` | 握手成功 |
| client→im | `{"t":"msg","p":<payload>}` | 发给 server。payload 对 im 是不透明字节，client 若需要 clientMsgId 放在 payload 里 |
| im→client | `{"t":"msg","p":<payload>}` | 来自 server 的推送 |
| 双向 | `{"t":"ping"}` / `{"t":"pong"}` | 应用层心跳，60 秒无任何帧关闭连接 |

im→client **没有错误帧**。server 侧的任何故障对 client 不可见，client 靠自己的超时重发。

gRPC 流上的帧（proto 放 `proto/fp/im/v1`，这里只列语义）：

| 方向 | 帧 |
|---|---|
| server→im | `Push{subject, payload}`、`PushMany{subjects[], payload}`、`Kick{subject, connIds[]}`、`Sessions{subject}`，每帧带 `reqId` |
| im→server | `Result{reqId, status, nodes, sessions[]}`、`Inbound{subject, connId, payload}`、`Event{kind, subject, connId, os, mobile, ua, ts}` |

---

## 四、握手与连接策略

```
client ──ws {t:"auth", token | guest+app, ua?, os?, mobile?}──► im-B
                    │
                    ▼
        ┌───────────────────────────────────────────────┐
        │ 解析主体                                       │
        │   带 token → Authenticator 验证 → app, u:{uid} │
        │   带 guest → app 配置允许访客                   │
        │             && guest 是合法 uuid v4            │
        │             && 本 IP 新访客数未超每分钟上限     │
        │             → g:{guest}                        │
        │   两者都没有 / 验证失败 → 关闭 4001            │
        └───────────────────┬───────────────────────────┘
                            ▼
        os / mobile：握手帧显式给了就用，否则解析 ua 得到
                            ▼
   ┌────────────────────────────────────────────────────────┐
   │ EVALSHA 握手脚本   KEYS[1] = fp:im:{a1:u:1001}:conn      │ ← Redis 1 次
   │   ARGV: connId, 元数据 JSON, policy, N, fieldTTL, 存活节点列表│
   │                                                        │
   │   ① 遍历 hash，n 不在存活列表的 field 全部 HDEL          │
   │   ② 按 policy 判定（数的是①之后的数量）：               │
   │      replace  → 记下现存条目，全部 HDEL，HSET 自己       │
   │      reject   → 现存 ≥1 返回 REJECT，不写               │
   │      limit N  → 现存 ≥N 返回 REJECT，否则 HSET 自己      │
   │   ③ HEXPIRE key fieldTTL FIELDS 1 connId               │
   │   返回 {OK | REJECT, 被顶替的 [connId, nodeId]...}       │
   └───────────────┬────────────────────────────────────────┘
                   │
      REJECT ──────┼──► 关闭 4002
                   │
                   ▼
   被顶替的每个 (connId, nodeId)：
     SPUBLISH fp:im:node:{nodeId}  t=KICK app cid=<subject> conn=<connId> reason=replaced
     （旧节点收到后关闭那条 ws，close code 4003；条目脚本已删，旧节点不再 HDEL）
                   │
                   ▼
   本地连接表登记
                   │
                   ▼
   deliver(Event{Connected, subject, connId, os, mobile, ua, ts}, hops=0)   ← 第六节
                   │
                   ▼
   回 {t:"hello", conn:<connId>}
```

要点：

- 脚本只碰一个 key，Cluster 合法。同 subject 的两条连接并发握手时脚本在 Redis 上串行，后到的一定看得见先到的，策略不会被绕过。
- 存活节点列表作为参数传入，脚本不访问第二个 key。列表最多滞后 3 秒。
- 唯一的不精确窗口：节点崩溃到被判死之间（默认 10 秒），该节点的条目仍算活的。N 个终端全在死节点上的用户立刻重连会被拒一次。要缩窗口就调心跳和判死阈值。这次被拒的后果按策略分两种，**不能笼统说成"客户端的重连退避会自然跨过这个窗口"**：
  - `replace`（默认）：重连本身就会顶掉死节点留下的残留条目，握手直接成功，窗口对它不存在。
  - `reject` / `limit`：这次重连拿到的是 4002（策略拒绝），而 4002 的契约是"不自动重连"——client SDK 会就此永久停止重连，退避跨不过这个窗口。SDK 会把 4002 通过 `OnClose` 报给调用方、并把 `GaveUp()` 置真（`sdk/im/client.go`），何时重试由调用方决定。用这两种策略的应用必须自己实现这次重试，否则崩溃窗口里重连的终端会一直连不回来。
- 连接策略是 app 级配置：`replace`（新顶旧，默认）、`reject`（拒新）、`limit`（最多 N 条，超出拒新）。
- 访客与登录用户在策略、路由、事件上一视同仁，区别只在握手的主体解析。

### 4.1 访客

访客 id 由前端用 `crypto.randomUUID()` 生成并持久化，不签名、不过期。122 位随机熵让它无法被猜到，冒用难度与偷 token 相同。fp 不签发访客 token：访客没有需要 fp 管理的生命周期，签发只会引入会话存储和续期问题。

im 对访客的唯一防线是握手处的**按 IP 限流新访客**（每 IP 每分钟首次出现的 guest id 数量上限，配置）。

访客调用业务 server 的 HTTP 接口时，`fpsdk.Auth` 的中间件需要新增 `AllowGuest` 选项：开启后请求头 `X-Guest-Id` 是合法 uuid v4 时 subject 为 `g:{id}`，权限按 fp 里配置的 guest 角色判定。这是 `fpsdk` 侧的配套改动，写进实施计划的前置任务。

---

## 五、server→im→client

### 5.1 Push(subject)

```
server.Push(u:1001, payload)
        │ gRPC 流 → im-A
        ▼
   本地连接表有 a1:u:1001 ？
        │
   有 ──┼──► 逐条写 ws 帧 {t:"msg", p}
        │    策略为 replace / reject（全网最多一条）→ 返回 Sent{nodes=1}，结束   ← Redis 0 次
        │    策略为 limit N → 继续往下找其他节点，SPUBLISH 时排除自己
        │
        ▼
   HGETALL fp:im:{a1:u:1001}:conn                          ← Redis 1 次（进管道）
        │
        ▼
   用存活节点缓存过滤掉死节点的条目，按 nodeId 去重，排除自己
        │
   ┌────┴──────────────┐
   空                  {im-B, im-C}
   │                    │
   ▼                    ▼
 返回 NotOnline    对每个节点  SPUBLISH fp:im:node:{node}     ← Redis 每节点 1 次（进管道）
 （本地也没投过）      t=MSG app=a1 sub=u:1001 p=<payload>
                        │
                   返回 Sent{nodes}
                        │
   ═════════════════════╪═══════════════════════════════════
                        ▼
   im-B 节点频道读协程收到 MSG：
     本地连接表查 a1:u:1001 的所有连接 → 每条写 ws 帧 {t:"msg", p}
     本地没有（刚断线）→ 丢弃
   无 Redis 命令
```

`Sent` 的含义：此刻有连接，已经递给持有它的节点。不代表到达 client 进程，更不代表被处理。

`replace` 策略下的本地短路有一个毫秒级窗口：新连接在别的节点刚跑完握手脚本、`KICK` 尚未到达时，本节点会把消息投给即将被踢的旧连接，server 拿到 `Sent` 但新连接没收到。这不破坏端到端语义，新连接的 `Connected` 事件会让 server 重推未得到业务回执的消息。`reject` 没有这个窗口。

### 5.2 PushMany(subjects[])

一帧带多个 subject，im 对每个 subject 执行 5.1 的逻辑，所有 `HGETALL` 和 `SPUBLISH` 放进同一批管道。返回每个 subject 各自的状态。一个 3 人会话 7 个终端的推送 = 1 次 gRPC + 1 次 Redis 往返。

### 5.3 Sessions(subject)

`HGETALL` 后按存活节点缓存过滤，返回 `[{connId, nodeId, os, mobile, connectedAt}]`。

### 5.4 Kick(subject, connIds...)

| 调用 | 行为 |
|---|---|
| `Kick(subject)` | 单 key 脚本：`HGETALL` 后 `DEL`，返回全部条目；对每个存活节点 `SPUBLISH KICK` |
| `Kick(subject, connId...)` | `HDEL` 指定 field，对各自所在节点 `SPUBLISH KICK` |

收到 `KICK` 的节点关闭对应 ws（close code 4003），不再 `HDEL`，也不再发 `Disconnected` 事件之外的任何东西。`Disconnected` 事件照常发，`reason=kicked`。

---

## 六、client→im→server

client 发来的消息和连接生命周期事件走同一个投递函数。这个方向**没有任何持久化**，im 不回 ack，server 的业务应答（一条普通的 Push）就是 client 的 ack。

```
deliver(msg, hops)
        │
        ▼
   本地有该 app 的 server 流？
        │
   有 ──┼──► 按 subject hash 选一条流，写 gRPC 帧 Inbound / Event        ← 结束，Redis 0 次
        │
   没有 ▼
   hops ≥ 1 ？ ──是──► 静默丢弃                                  ← 已经转过一次，不再转
        │否
        ▼
   server 节点缓存 fp:im:srv:{app} 的存活列表，排除自己
        │
   空 ──┼──► 静默丢弃                                            ← 此刻全网没有 server
        │
   非空 ▼
   按 subject 做 rendezvous hash 选 im-C
   SPUBLISH fp:im:node:im-C  t=UP app sub conn hops=hops+1 p=<payload>
        │                                                       ← Redis 1 次（进管道）
   ═════╪═════════════════════════════════════════════════
        ▼
   im-C 节点频道读协程收到 UP → deliver(msg, hops)               ← 同一个函数
```

| 情况 | 走向 |
|---|---|
| 本节点有流 | 本地直投，最常见 |
| 本节点没流，别处有 | 转一跳到 hash 选定的节点，那边直投 |
| 转到的节点刚好也断了流 | 它按自己的缓存再转一跳（hops=1），到了就投，还没有就丢 |
| server 整体不可用 | 缓存为空，丢弃，client 超时重发 |

节点丢掉最后一条 server 流时立即 `HDEL` 自己，其他节点的缓存最多滞后 3 秒，这 3 秒内发来的 `UP` 靠接收方的那一跳兜住。

**顺序**：同一 subject 的消息在本地有流时永远走同一条本地流，本地没流时永远 hash 到同一节点的同一条流，拓扑不变时有序。server 流增减导致路径切换的瞬间可能乱序，需要严格顺序的业务在 payload 里带序号。

**事件**：`Connected` 在握手成功后发，`Disconnected` 在连接关闭后发（reason：`client` 正常关闭 / `timeout` 心跳超时 / `kicked` 被踢 / `replaced` 被顶替 / `revoked` token 被撤销 / `backpressure` 发送队列满）。两者都带 connId，server 收到旧连接的 `Disconnected` 晚于新连接的 `Connected` 时可据 connId 忽略。节点崩溃时该节点上连接的 `Disconnected` 不会发出，server 只会收到它们重连后的 `Connected`。

---

## 七、断开与故障

**client 正常断开 / im 主动关闭**

```
本地连接表删除
HDEL fp:im:{app:subject}:conn <connId>                    ← Redis 1 次（进管道）
deliver(Event{Disconnected, reason}, 0)
```

**im 节点优雅关闭**（发版、缩容：收到 SIGTERM/SIGINT）

```
① 关 HTTP 监听，不再接受新的 ws 握手
   （http.Server.Shutdown 按 Go 的契约不等待也不关闭被劫持的连接，ws 走的正是劫持，所以它对存量 ws 无效）
② 主动关闭本地全部 ws：close code 4004（服务不可用 → client 退避后重连到别的节点），
   reason = "shutdown"；走的是与踢人/背压完全相同的拆连接路径，所以
   HDEL fp:im:{app:subject}:conn 与 Disconnected 事件都照常发生
③ 停 gRPC（必须排在②之后：②的断开事件要经业务 server 的 Connect 长流才发得出去）
④ HDEL fp:im:node 与各 fp:im:srv:{app} 里自己的条目
```

每一步各有独立的时间预算（默认 10 秒）。业务方会收到 reason 为 `shutdown` 的断开事件，
它与 `client`/`timeout` 的区别是"与用户行为和这条连接本身都无关，client 马上会重连回来"。

**im 节点崩溃**

```
它的 ws 全断，client 经 LB 重连到其他节点，走第四节
它在 fp:im:node 的心跳 10 秒后过期，其他节点的缓存把它剔除
它留在各 conn hash 里的条目：所有读路径过滤；下次该 subject 握手时脚本 HDEL；无人续期，最晚 field TTL 到期自动消失
它的 pub/sub 订阅随 Redis 连接一起消失
它在 fp:im:srv:{app} 里的条目过期后，rendezvous hash 自动把它的份额分给别人
它持有的 server 流断开，SDK 经 LB 重连到任一节点
```

**server 流断开**

```
本地流表删除
本节点该 app 最后一条流 → HDEL fp:im:srv:{app} <nodeId>       ← Redis 1 次
正在写入途中的 Inbound 丢失，client 重发覆盖
```

**Redis 断线重连**

```
SSUBSCRIBE 本节点频道
HSET fp:im:node 心跳，HSET 各 fp:im:srv:{app}
若发现自己在 fp:im:node 里的条目消失（Redis 重启过、数据没了）
   → 把本地所有连接重新用握手脚本登记（policy 传 `none`：只清残留、HSET、HEXPIRE，不判定），限速
断线期间：Push 返回 Unavailable；client→server 消息静默丢弃；握手拒绝（关闭 4004）
```

**fp 不可用**（fp 模式）

```
已建立的连接不受影响
新握手：命中 fpsdk.Auth 缓存的照常通过；未命中的按 SDK 的 fail-open / fail-closed 配置
访客握手不经过 fp，不受影响
```

---

## 八、Redis 代价汇总

| 路径 | 命令 | 备注 |
|---|---|---|
| 握手 | `EVALSHA` 1 | 单 key 脚本，含 HEXPIRE |
| Push，单连接策略且本地命中 | 0 | |
| Push，其他 | `HGETALL` 1 + 每目标节点 `SPUBLISH` 1 | 通常共 2 |
| PushMany | 每 subject 同上，合一批 | |
| Sessions | `HGETALL` 1 | |
| Kick | `EVALSHA` 或 `HDEL` 1 + 每节点 `SPUBLISH` 1 | |
| client→server，本地有流 | 0 | 最常见 |
| client→server，本地无流 | `SPUBLISH` 1，最多 2 | |
| 断开 | `HDEL` 1 | |
| 后台 | 每节点每 3 秒 `HSET` 1 + 每 app `HSET` 1 + `HGETALL` 若干 | 与消息量无关 |
| field 续期 | 每节点每 `conn.field_renew` 对本地连接按 subject 分组各 `HEXPIRE` 1 | 10 万连接约每秒 170 条 |

每秒 3 万条消息、最坏情况全是跨节点 Push，是每秒 6 万条命令，按 hash tag 散在各分片上。默认每条命令一次往返；开启写管道攒批（比如 5 毫秒或 256 条）后每节点每秒往返约 200 次，代价是每条消息平均多 2 到 3 毫秒延迟。

---

## 九、fp-im 的承诺与业务方的配方

fp-im 对业务方只承诺三句：

1. 对方此刻在线，我立刻递过去一次，并告诉你 `Sent` 还是 `NotOnline`。
2. 对方上线、下线，我告诉你。
3. 同一个 subject 的**上行**（client→server）消息，在拓扑不变时是有序的。

第三句只对上行成立，**下行（server→client 的 Push）对同一个 subject 不保证顺序**：网关侧
`imgrpc.Server.Connect` 对流上收到的每条请求各起一个协程并发处理（见那里的注释与
`Deps.Workers`），先发出的 Push 完全可能后落地。业务方要保证下行顺序，只能自己在 payload 里
带序号（配方里的 seq），不能依赖网关。上行之所以有序，是因为一条 ws 连接的帧由单个读循环
串行处理，且 server SDK 侧也是单消费者按入队顺序回调。

不承诺送达、不承诺不重复、不存消息。业务方的闭环配方固定四步：**id 去重 + 落库 + 超时重发 + 上线同步**。

两个场景的落法：

| | 聊天 | 设备控制 |
|---|---|---|
| client→server | client 带 clientMsgId 发送，没等到 server 应答就重发；server 以 `(subject, clientMsgId)` 唯一键落库，幂等 | 设备带 cmdId 回报结果，没等到应答就重发；server 按 cmdId 更新状态 |
| server→client | server 落库并分配会话内 seq，`Push`；`NotOnline` 什么都不做；client 按 seq 去重、发现 seq 断档就拉；收到 `Connected` 或 client 主动同步时推增量 | server 先落库指令 `cmdId` 状态"待执行"再 `Push`；`NotOnline` 或超时未回报就在 `Connected` 时重推；设备按 cmdId 幂等执行 |
| 多终端 | 每个终端各自的 seq 游标 | 设备通常单连接 |

这套配方后续可以做成 `fpsdk` 的可选辅助包，im 服务本身不碰。

---

## 十、SDK API

### 10.1 server 侧（Go）

```go
im := fpsdk.IM.Server(cfg)          // 经 LB 连一个 im 节点，一条双向流，SDK 管重连

im.OnMessage(func(ctx context.Context, in fpim.Inbound) error {
    // in.Subject / in.ConnID / in.Payload
    return nil                        // 返回值不影响 im，im 不等 ack
})
im.OnEvent(func(ctx context.Context, ev fpim.Event) {
    // ev.Kind: Connected | Disconnected
    // ev.Subject / ev.ConnID / ev.OS / ev.Mobile / ev.UA / ev.Reason / ev.At
})

res, err := im.Push(ctx, subject, payload)           // res.Status: Sent | NotOnline | Unavailable
ress, err := im.PushMany(ctx, subjects, payload)     // 每个 subject 一个 res
sess, err := im.Sessions(ctx, subject)               // []Session{ConnID, NodeID, OS, Mobile, ConnectedAt}
err = im.Kick(ctx, subject, connIDs...)              // 不传 connID 踢全部
```

`Subject` 类型：

```go
type Subject struct{ Kind SubjectKind; ID string }   // Kind: User | Guest
func User(uid string) Subject
func Guest(id string) Subject
func (s Subject) String() string                    // "u:1001" / "g:8f3a…"
```

### 10.2 client 侧

提供 Go 客户端（设备、原生程序、测试用），不提供 JS 客户端。对浏览器和小程序来说 fp-im 就是一个 ws 端点，按 3.5 节的帧格式和下面的关闭码自己接：

| close code | 含义 | 客户端动作 |
|---|---|---|
| 4001 | 认证失败（token 无效或已被撤销） | 不要自动重连，去重新登录换一个新 token |
| 4002 | 被连接策略拒绝 | 不要重试，策略状态在服务端，立即重连大概率还是被拒 |
| 4003 | 被踢（含被顶替） | 由业务决定是否重连，这条连接本身没有错 |
| 4004 | 服务不可用（身份服务或 Redis 注册表断线期间） | 退避后重连，和 client 自身无关 |
| 4005 | 网关因连接空闲太久主动关闭 | 立即重连，不要退避——服务端一切正常，只是常规清理 |
| 1013 | 背压（发送队列跟不上，网关主动断开避免无限堆积） | 退避后重连，并向业务方拉一次历史补上可能错过的消息 |

客户端负责：连接后 5 秒内发握手帧、应用层 ping、指数退避重连、访客 id 的生成（`crypto.randomUUID()`）与持久化。`send` 不返回结果，可靠性由业务层按第九节的配方实现。

Go 客户端把上面这些封装掉：

```go
c, err := fpsdk.IM.Client(ctx, fpim.ClientConfig{
    URL:   "wss://im.example.com/ws",
    Token: token,                       // 或 Guest: id, App: "a1"
    OS:    "linux", Mobile: false,      // 可选，不传则 im 按 UA 解析
})
c.OnMessage(func(p []byte) { … })
c.OnClose(func(code int) { … })         // 4001 / 4002 / 4003 / 4004 / 4005 / 1013
err = c.Send(ctx, payload)              // 只表示已写入 ws，不代表 server 收到
c.Close()
```

握手、ping 由客户端内部处理；4004（服务不可用）和 1013（背压，重连后还要拉一次历史）自动退避重连；4005（空闲超时）立即重连、不退避；被踢（4003）、策略拒绝（4002）、认证失败（4001）不自动重连，交给调用方（4001 还需要调用方去重新登录换 token）。

---

## 十一、与 fp 的关系

im 用到 fp 的只有两件事，各抽一个接口：

| 接口 | fp 实现（默认，第一版唯一实现） | 预留 |
|---|---|---|
| `Authenticator.Verify(token) → (app, subject, err)` | `fpsdk.Auth`：本地缓存 + 回源 + 撤销订阅。收到撤销事件时关闭对应 ws（reason `revoked`） | JWT：公钥或 HMAC 密钥来自配置，`sub` claim 即 subject |
| `AppConfigSource.Get(app) → AppConfig` | fp 配置中心，热更新 | 本地配置文件 |

访客解析、注册表、路由、事件与实现无关。

依赖方向：fp-im → fp，单向。fp 不依赖 fp-im（平台设计已定）。fp-im 不再需要 fp 时，换掉两个实现即可。

`fpsdk` 侧的配套改动：`Auth` 中间件新增 `AllowGuest` 选项（4.1 节）。

---

## 十二、配置项

app 级（来自 `AppConfigSource`，热更新）：

| 项 | 默认 | 说明 |
|---|---|---|
| `conn_policy` | `replace` | `replace` / `reject` / `limit` |
| `conn_limit` | 5 | 仅 `limit` 时生效 |
| `allow_guest` | false | 是否允许访客握手 |
| `guest_ip_rate` | 20/分钟 | 每 IP 每分钟新访客数上限 |

节点级（启动配置）：

| 项 | 默认 | 说明 |
|---|---|---|
| `node.heartbeat` | 3s | 心跳与缓存刷新间隔 |
| `node.dead_after` | 10s | 判死阈值 |
| `conn.field_ttl` | 30m | conn hash 里每个 field 的 TTL |
| `conn.field_renew` | 10m | 节点对本地连接续期的间隔，必须小于 `conn.field_ttl` 的一半 |
| `conn.idle_timeout` | 60s | 无帧关闭。**下界 50s**：client SDK 每 25 秒发一次心跳（`fpim.PingInterval`），空闲超时低于它的两倍会让全网 client 被周期性以 4005 踢下线，而 4005 的契约是"立即重连不退避"，形成稳定的重连风暴。低于下界启动直接失败 |
| `conn.auth_timeout` | 5s | 握手帧等待 |
| `conn.send_queue` | 256 | 每连接发送队列长度，满则关闭连接（reason `backpressure`） |
| `pipeline.flush_interval` | 0 | 写管道攒批时长，0 为不按时长攒 |
| `pipeline.flush_size` | 1 | 写管道攒批条数，1 为每条立即发。两项都设时先到为准 |

Redis 是单机还是 Cluster 由启动时 `INFO cluster` 探测，不配置。

---

## 十三、测试要点

单元层（不依赖 Redis）：

- 握手脚本的策略矩阵：`replace` / `reject` / `limit` × 现存 0 / 1 / N 条 × 含死节点残留，验证返回值与被顶替列表。脚本用嵌入式 Lua 解释器跑，或用 miniredis 跑真实脚本。
- `deliver` 的四种走向（本地有流 / 转一跳 / 转到后再转一跳 / 全网无 server），以及 hops 上限。
- Push 的本地短路：`replace` 命中本地不发 Redis；`limit` 命中本地仍发但排除自己。
- 存活过滤：`HGETALL` 结果里含死节点条目时 `NotOnline` 判定与 `Sessions` 输出正确。
- 访客解析：uuid 格式校验、IP 限流、`allow_guest` 关闭时拒绝。
- rendezvous hash 的稳定性：节点集合不变时同 subject 恒选同节点；增删一个节点时只有该节点份额的 subject 换目标。
- field 续期：续期循环按 subject 分组发 `HEXPIRE`，只覆盖本节点的 connId；写管道在默认配置下每条立即发，配置攒批后按时长或条数先到者刷新。

集成层（真实 Redis 8，Cluster 用 docker 起三主）：

- 三节点场景：client 在 A、server 流在 B、Push 从 C 发出，验证送达；B 的流断开后消息经 A 或 C 的缓存转一跳到位。
- 节点崩溃：kill 一个节点，10 秒后其条目被过滤，subject 重连后握手脚本清理残留。
- Redis 重连：断 Redis 再恢复，订阅恢复、心跳恢复、连接重新登记。
- 压测：单节点 10 万连接、每秒 1 万条 Push，观察管道往返次数和 Redis 命令数与第八节的估算一致。

架构断言（沿用 `internal/service/arch_test.go` 的做法）：im 包只能通过 `Authenticator` / `AppConfigSource` 接口触达 fpsdk。

---

## 十四、后续迭代（本版不做）

| 项 | 触发条件 | 方向 |
|---|---|---|
| PushChannel / Broadcast / 业务频道订阅 | 一对多推送量大到 server 查成员再 `PushMany` 成为瓶颈 | 节点按本地订阅者 `SSUBSCRIBE fp:im:ch:{app}:{channel}`，发送方一条 `SPUBLISH`，Redis 负责扇出。订阅凭证由业务方签发。当前用 `PushMany` 代替 |
| Push 定向到某个终端 | 业务需要"只推手机" | `Push(subject, connId)`，注册表已有 connId，只是 API 没开 |
| fp 签发访客 token | 访客需要被 fp 管理（合并、封禁、跨设备） | 加 `GuestLogin` RPC，im 握手逻辑不变 |
| JS 客户端 | 多个前端重复写握手、心跳、重连 | 协议已定，封装即可 |
| JWT Authenticator / 文件 AppConfigSource | fp-im 要脱离 fp 部署 | 接口已留 |
| 二进制帧 | JSON 编解码成为瓶颈 | 握手时协商 |
| 节点直连 | Redis pub/sub 成为瓶颈 | 平台设计已预留，`fp:im:node` 表里加地址 |

---

## 十五、与 8 月平台设计的差异

| 8 月设计 | 本文 | 原因 |
|---|---|---|
| 核心抽象是 Channel | 第一版只有 subject 点对点，Channel 延后 | 当前没有一对多需求；`PushMany` 已覆盖会话推送 |
| 可选的有界离线队列 | 彻底不做，im 无队列 | 场景分析表明业务方本来就要落库和上线同步，im 再存一份是重复，且引入一致性负担 |
| `conn:{clientId}` 单值 | `fp:im:{app:subject}:conn` hash，多连接为多 field | 多终端与连接策略需要 |
| clientId | subject（`u:` / `g:` 前缀） | 访客不是用户，命名空间要分开 |
| 背压：队列满断连 | 保留 | |
| 顺序：跨节点加序号 | 靠稳定路由保证拓扑不变时有序，不加序号 | 加序号需要 im 侧状态 |
