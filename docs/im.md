# fp-im 交接文档

fp-im 是无队列的 WebSocket 连接网关：只管"谁在线、递一条消息给谁"，返回
`Sent`/`NotOnline`，并告知上下线。**不是**消息队列或聊天记录存储，不存消息、不保证
送达/去重——业务方自己按"id 去重 + 落库 + 超时重发 + 上线同步"实现可靠投递（设计
第九节）。

## 启动

```bash
FP_IM_HTTP_ADDR=:8081 FP_IM_GRPC_ADDR=:9091 ./scripts/run-im.sh
```

依赖 `FP_IM_REDIS_URL`、`FP_IM_FP_ADDR`、`FP_IM_APPS_FILE`：三者对二进制本身都是
必填项，缺一个直接启动失败（`internal/im/config.Load`）。"`FP_IM_REDIS_URL` 默认
复用 `FP_REDIS_URL`"这条回退**只在 `scripts/run-im.sh`（经 `scripts/env.sh`）里
成立**，是本机联调的便利写法；直接跑 `./fp-im` 二进制的人没有这层回退，必须自己
显式传全这三个变量。

```json
{"apps":[{"app_id":"…","app_secret":"…","allow_guest":true}]}
```

除 `app_id`/`app_secret` 外都有默认值，10 秒检测 mtime 热更新，坏文件保留旧配置。

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

`FP_IM_CONN_IDLE_TIMEOUT` 有 50 秒下界（client SDK 每 25 秒发一次心跳，`fpim.PingInterval`
的两倍），低于它启动直接失败：配成 20s 会让全网 client 每 20 秒被以 4005 踢一次并立即重连
（4005 的契约就是不退避），形成稳定的重连风暴。

**优雅关闭（发版、缩容）**：收到 SIGTERM/SIGINT 后依次做四件事——关 HTTP 监听（不再接受新
握手）→ 以 **4004**、reason `shutdown` 主动关掉本地全部 ws 并等拆连接走完 → 停 gRPC → 删自己
的心跳条目；每步各有 10 秒预算。client 看到 4004 会退避后重连到别的节点；业务方会收到 reason
为 `shutdown` 的断开事件（与 `client`/`timeout` 的区别：与用户行为无关，这条连接马上会回来）。
关 ws 必须排在停 gRPC 之前，否则断开事件没有出口——改动这段顺序前先看
`cmd/fp-im/main.go` 里 `shutdown` 的注释和 `TestServeGracefulShutdownClosesWebSockets`。

**`FP_IM_TRUST_PROXY`**：开启后访客限流的 IP 取 `X-Forwarded-For` 的**最右一跳**，也就是直连
本网关的那个代理亲手追加的值（client 改不了）。因此**只在网关前确实有代理时开**：直接对公网
暴露时开启它，等于把限流键交给所有人伪造。多层代理下最右跳是内层代理的地址，那一层后面的所有
client 会共用一个限流桶（偏严、可能误伤，但不会被绕过）。nginx 用默认写法即可：

```nginx
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;  # 追加，最右一跳就是它看到的来源
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
proxy_read_timeout 3600s;   # 大于 conn.idle_timeout，别让代理抢在网关前面断 ws
```

## 升级注意

**消息信封线路格式变过两次**（插入转发候选列表、游标 1→2 字节），新旧节点互相
解不出对方的信封。**升级必须整批替换，不能滚动。**

## 已知不足

- `renewLoop` 无自动测试（重登记 `reregistrar` 已有测试，见 `cmd/fp-im/main_test.go`）。
- 业务 server 一直挂着 `Connect` 长流时，关闭第三步的 `GracefulStop` 会用满 10 秒预算才硬停
  ——ws 与注册表在第二步就已经处理干净，只是进程多活 10 秒。
- `cmd/fp-im` 只有第一批测试（甲一的启动事件、乙一的优雅关闭、Gap 重登记三条），装配里其它
  顺序（先订阅后心跳、同步 Refresh 在监听之前）仍然只有注释守着。
- Cluster 无真机集成测试，上线前建议对真实 Cluster 跑一遍 `im_e2e_test.go`。
- `internal/integration/im_e2e_test.go` 的 `startNode` 仍然是**手抄**的装配顺序，与
  `cmd/fp-im` 的 `serve()` 各写一份，只能靠人工保持同步。

设计：`docs/superpowers/specs/2026-09-03-fp-im-design.md`；实施：
`docs/superpowers/plans/2026-09-03-fp-im-gateway.md`。
