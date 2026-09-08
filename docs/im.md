# fp-im 交接文档

fp-im 是无队列的 WebSocket 连接网关：只管"谁在线、递一条消息给谁"，返回
`Sent`/`NotOnline`，并告知上下线。**不是**消息队列或聊天记录存储，不存消息、不保证
送达/去重——业务方自己按"id 去重 + 落库 + 超时重发 + 上线同步"实现可靠投递（设计
第九节）。

## 启动

```bash
cp config-im.example.yaml config-im.yaml   # 首次：填入 Redis 连接串与 fp 地址
./scripts/run-im.sh
```

`fp-im` 读 `./config-im.yaml`（`-c` 可指定别的路径），与 fp 的 `config.yaml`
**完全独立**：不引用、不继承它的任何默认值。想跟 fp 共用一个 Redis，就把同一条
URL 抄进 `config-im.yaml` 的 `redis.url`——旧版那条"`FP_IM_REDIS_URL` 未设时复用
`FP_REDIS_URL`"的 shell 回退没有了，隐式继承比多抄一行难懂得多。

`redis.url` 与 `apps_file` 是必填项，缺一个直接启动失败。`fpsdk.addr`（fp 的
**gRPC** 地址，不是 HTTP）不由 `internal/im/config` 校验——"谁用谁校验"，它由
`fpauth.New` 在装配阶段挡住，报错时机一样是启动时。

**传输安全没有开关，由 `env` 推导**：`env: dev` 明文，`env: prod` 走 TLS。一个
默认为 true 的 `insecure` 旋钮最可能的失效方式就是被连同整份 dev 配置抄到生产上，
而 `appSecret` 是随每个 RPC 的 metadata 明文发的。

> **【部署硬要求】** fp 的 gRPC 服务端目前没有传 `grpc.Creds`，只服务明文。所以
> `env: prod` 下 fp-im 连 fp 的那条 TLS **必须**由 fp 前面的反代 / 网关终结。
> 在给 fp 的 gRPC 补上 TLS 之前，这不是可选项。

```json
{"apps":[{"app_id":"…","app_secret":"…","allow_guest":true,
  "biz_auth":{"verify_url":"https://biz.example.com/verify","timeout":"2s","cache_size":10000}}]}
```

除 `app_id`/`app_secret` 外都有默认值，10 秒检测 mtime 热更新，坏文件保留旧配置。

`biz_auth` 整组为空表示这个应用不支持业务方令牌。`verify_url` 必填且必须是
https（令牌明文走在请求体里）；`timeout` 缺省 2 秒，是整个请求的超时；
`cache_size` 缺省 10000。

运维要留意三点：

- **`timeout` 必须明显小于握手的 5 秒上限。** 配大了会让 client 先被握手
  超时踢掉，拿到的关闭码从 4004（该退避重连）变成 4001（该重新登录），
  方向完全反了——client 会以为是自己的令牌坏了，其实只是业务方接口慢。
- **改 `verify_url` 会立刻让旧缓存失效。** 缓存键包含 `verify_url` 本身，
  切到新后端（灰度、迁移、修配错的地址）之后，旧后端对同一个令牌给出的
  结论不会继续生效，下一次握手一定会回调新地址。
- **`cache_size` 在首次为某个 app 建缓存时定容，之后改配置不缩容，重启
  才生效。** 缓存容量不值得为热更新引入重建逻辑，热改小这个值不会立刻
  生效。

## ws 协议

握手帧 `{"t":"auth","app":"a1","token":"…"}`（或 `guest`：uuid v4），5 秒不发即关闭；
成功回 `{"t":"hello","conn":"…"}`；之后 `{"t":"msg","p":<payload>}` 双向透传；
`ping`/`pong` 心跳，60 秒无帧关闭。六个关闭码及 client 应对：

| 码 | 含义 | client 该做什么 |
|---|---|---|
| 4001 | 认证失败 | 不重连，去重新登录换 token |
| 4002 | 策略拒绝 | 不重连，状态在服务端不会变 |
| 4003 | 被踢/顶替 | 不自动重连，由业务决定 |
| 4004 | 后端不可用，或本节点正在优雅关闭 | 退避后重连（会落到别的节点） |
| 4005 | 空闲超时 | 立即重连，不退避 |
| 1013 | 背压 | 退避重连，且要拉一次历史补漏 |

握手帧的 `kind` 字段决定这个令牌送去哪里验证：

| kind | 验证方 | 主体前缀 |
|---|---|---|
| 不写或 `fp` | fp，走 SDK | `u:` |
| `biz` | 回调该应用配置里的 `verify_url` | `b:` |
| 其它值 | 拒绝，关闭码 4001（不会兜底当成 fp） | — |

应用没配 `biz_auth` 时，`kind: "biz"` 同样是 4001（与令牌本身无效同一个
关闭码，不区分是为了不泄露"哪些 app 开了什么"）。

不写 `kind` 的老客户端行为不变。带 `guest` 的访客路径也不变。

业务方的用户 1001 是 `b:1001`，与 fp 的 `u:1001` 在 Redis 键上天然隔离，
两者不会互相顶号、不会收到对方的消息。

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

**验证只发生在握手那一刻，已建立的连接不会被重新验证。** `cache_seconds`
只影响"下一次握手要不要重新回调你的接口"，不影响已经连上的连接——一条
连接一旦握手成功，会一直活到空闲超时或 client 自己断开为止，就算你立刻
在自己的系统里吊销了这个令牌，这条连接也不会因此断开。这与 fp 令牌的路径
不同：fp 撤销一个令牌会通过 `fpauth.OnRevoke` → `hub.OnRevoked` 当场关掉
持有它的本地连接，业务方令牌没有对应的撤销通路。**要立刻踢掉某个用户，
用 server SDK 的 `Kick`（主体写 `b:{user_id}`），不能指望令牌撤销生效。**

**这个接口对公网是裸的**，网关不带任何凭据，没有东西证明请求来自网关。
实际风险有限（攻击者要先有令牌才能拿去验，而有了有效令牌本来就能直接
连网关），但建议把它放在内网或做网络层隔离。

## 两个 SDK

server：`srv.OnMessage`/`OnEvent` 由 `Server` 内部一个独立的消费协程按入队顺序
调用，不是在读 gRPC 流的那条循环里同步执行——回调里可以直接同步调用
`Push`/`PushMany`/`Sessions`/`Kick` 等它们的 Result 返回，不会自死锁。Inbound 与
Event 共用同一条容量 256 的队列（单消费者保证同一 subject 的处理顺序与投递顺序
一致，且事件与消息的相对顺序也不变），队列满了会丢弃并计数（`srv.DroppedFrames()`
查询）、同时打一条警告日志，不会阻塞读循环。`Close()` 会等这个消费协程真正退出
再返回，之后不会再有回调被调用。最小接入示例、可以直接照抄的同步写法见
`examples/im-demo/main.go`。

client：`fpim.Dial` 后 `OnMessage`/`OnClose`/`Send`，握手、心跳（每 25 秒，
`fpim.PingInterval`）、按上表退避重连全部内部处理。收到 4001/4002/4003 时 SDK 按契约永久停止
重连——**包括在重连尝试中撞上这三个码**（比如 `reject`/`limit` 策略的应用在节点崩溃窗口里重连
被拒）：这时 `OnClose` 会带着那个码再回调一次，`GaveUp()` 也会变成 true，调用方靠任一条都能发现
"它不会自己回来了"，该重新登录还是提示用户由调用方决定。一个可以直接照抄的最小用法（握手、发消息、收回显）见
`examples/im-demo/README.md` 第 5 步的 `wsclient` 小工具，用的就是这个 client SDK。

## 顺序保证只有一个方向

同一 subject 的**上行**（client→server）在拓扑不变时有序：一条 ws 的帧由单个读循环串行
处理，server SDK 侧也是单消费者按入队顺序回调。

**下行（`Push`）对同一 subject 不保证顺序**：网关的 `imgrpc.Server.Connect` 对流上每条请求
各起一个协程并发处理（`Deps.Workers`，默认 64），先发出的 Push 完全可能后落地。要顺序就自己
在 payload 里带 seq，这是设计第九节配方里 client 去重那一步本来就要做的事。

## 运维要点

Redis 最低 8.0（`HEXPIRE`/`HTTL`/分片订阅）；心跳 3s、判死 10s；两张心跳表（`fp:im:node`、
`fp:im:srv:{app}`）的字段带 100 秒过期（判死阈值的 10 倍，每次心跳刷满），优雅关闭时还会主动
删掉自己的条目；conn 元数据 field TTL 默认 30 分钟、每 10 分钟续期；写管道默认立即发，可配
攒批；单机/Cluster 由 `INFO cluster` 自动探测。

`conn.idle_timeout` 有 50 秒下界（client SDK 每 25 秒发一次心跳，`fpim.PingInterval`
的两倍），低于它启动直接失败：配成 20s 会让全网 client 每 20 秒被以 4005 踢一次并立即重连
（4005 的契约就是不退避），形成稳定的重连风暴。

**优雅关闭（发版、缩容）**：收到 SIGTERM/SIGINT 后依次做四件事——关 HTTP 监听（不再接受新
握手）→ 以 **4004**、reason `shutdown` 主动关掉本地全部 ws 并等拆连接走完 → 停 gRPC → 删自己
的心跳条目；每步各有 10 秒预算。client 看到 4004 会退避后重连到别的节点；业务方会收到 reason
为 `shutdown` 的断开事件（与 `client`/`timeout` 的区别：与用户行为无关，这条连接马上会回来）。
关 ws 必须排在停 gRPC 之前，否则断开事件没有出口——改动这段顺序前先看
`cmd/fp-im/main.go` 里 `shutdown` 的注释和 `TestServeGracefulShutdownClosesWebSockets`。

**`http.trust_proxy`**：开启后访客限流的 IP 取 `X-Forwarded-For` 的**最右一跳**，也就是直连
本网关的那个代理亲手追加的值（client 改不了）。因此**只在网关前确实有代理时开**：直接对公网
暴露时开启它，等于把限流键交给所有人伪造。多层代理下最右跳是内层代理的地址，那一层后面的所有
client 会共用一个限流桶（偏严、可能误伤，但不会被绕过）。nginx 用默认写法即可：

```nginx
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;  # 追加，最右一跳就是它看到的来源
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
proxy_read_timeout 3600s;   # 大于 conn.idle_timeout，别让代理抢在网关前面断 ws
```

`biz_auth` 的验证接口是该应用业务方令牌的强依赖，它不可用时对应的新连接会全部被拒
（fp 令牌与访客不受影响）。

## 升级注意

**消息信封线路格式变过两次**（插入转发候选列表、游标 1→2 字节），新旧节点互相
解不出对方的信封。**升级必须整批替换，不能滚动。**

**`conn.idle_timeout` 现在有 50 秒下界，低于它进程直接启动失败**（不是警告、不是取
默认值）。这是破坏性变更：升级前先检查现网这一项的取值，配得比 50s 小的部署会起不来。原因
见运维要点那一节。

**断开事件多了一个 reason 取值 `shutdown`**（本节点优雅关闭时发出）。业务方若对 reason 做
了穷举匹配（switch 没有 default），升级前要先补上这一支。

## 已知不足

- `renewLoop` 无自动测试（重登记 `reregistrar` 已有测试，见 `cmd/fp-im/main_test.go`）。
- 业务 server 一直挂着 `Connect` 长流时，关闭第三步的 `GracefulStop` 会用满 10 秒预算才硬停
  ——ws 与注册表在第二步就已经处理干净，只是进程多活 10 秒。
- `cmd/fp-im` 只有第一批测试（甲一的启动事件、乙一的优雅关闭、Gap 重登记四条），装配里其它
  顺序（先订阅后心跳、同步 Refresh 在监听之前）仍然只有注释守着。热重载补追踪那一半也是：
  回调机制本身有 `appcfg` 的单测守着，但 `serve()` 里
  `apps.OnReload(func() { trackApps(live, apps) })` 这一行删掉不会让任何测试变红——要覆盖它
  得让 `apps.Watch` 的 10 秒周期在测试里可配，成本大于收益。
- 优雅关闭有一个残留窗口：收到终止信号到某条连接真正被拆掉之间，本节点的节点频道订阅可能
  已经断了（`serve` 的 ctx 一取消，`bus.Subscribe` 那条订阅就开始收摊），而它的注册表条目
  还在。这段时间里别的节点给这个 subject 发推送，会算出目标是本节点、`SPUBLISH` 返回 0 个
  订阅者、把本节点剔出本地视图后去试下一个候选——而这条连接就在本节点上，没有别的候选能送
  到，这些推送静默丢失。窗口本批已经从"直到进程退出"（连接根本不拆）压到"关 ws 到拆完"的
  几十毫秒量级，属于改善但没有消除。要消除得在拆连接全部走完之后才停订阅，或者关闭第一步
  就先把自己从注册表/存活表里摘掉再拆连接——都涉及重排关闭流程，本批没做。
- Cluster 无真机集成测试，上线前建议对真实 Cluster 跑一遍 `im_e2e_test.go`。
- `internal/integration/im_e2e_test.go` 的 `startNode` 仍然是**手抄**的装配顺序，与
  `cmd/fp-im` 的 `serve()` 各写一份，只能靠人工保持同步。

设计：`docs/superpowers/specs/2026-09-03-fp-im-design.md`；实施：
`docs/superpowers/plans/2026-09-03-fp-im-gateway.md`。
