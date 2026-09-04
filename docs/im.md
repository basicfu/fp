# fp-im 交接文档

fp-im 是无队列的 WebSocket 连接网关：只管"谁在线、递一条消息给谁"，返回
`Sent`/`NotOnline`，并告知上下线。**不是**消息队列或聊天记录存储，不存消息、不保证
送达/去重——业务方自己按"id 去重 + 落库 + 超时重发 + 上线同步"实现可靠投递（设计
第九节）。

## 启动

```bash
FP_IM_HTTP_ADDR=:8081 FP_IM_GRPC_ADDR=:9091 ./scripts/run-im.sh
```

依赖 `FP_IM_REDIS_URL`（默认复用 `FP_REDIS_URL`）、`FP_IM_FP_ADDR`、`FP_IM_APPS_FILE`：

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
| 4004 | 后端不可用 | 退避后重连 |
| 4005 | 空闲超时 | 立即重连，不退避 |
| 1013 | 背压 | 退避重连，且要拉一次历史补漏 |

## 两个 SDK

server：`srv.OnMessage`/`OnEvent` 在同一条 gRPC 读循环里**同步**执行，回调里若要
再调 `Push`/`Sessions`/`Kick`（都要等 Result），必须另开 goroutine，否则会把读循环
自己死锁到 `RequestTimeout`（`examples/im-demo` 真实踩过，见其 README 第 6 步）。
client：`fpim.Dial` 后 `OnMessage`/`OnClose`/`Send`，握手、心跳、按上表退避重连全部
内部处理。

## 运维要点

Redis 最低 8.0（`HEXPIRE`/`HTTL`/分片订阅）；心跳 3s、判死 10s；conn 元数据 field
TTL 默认 30 分钟、每 10 分钟续期；写管道默认立即发，可配攒批；单机/Cluster 由
`INFO cluster` 自动探测。

## 升级注意

**消息信封线路格式变过两次**（插入转发候选列表、游标 1→2 字节），新旧节点互相
解不出对方的信封。**升级必须整批替换，不能滚动。**

## 已知不足

- `reregister`/`renewLoop` 无自动测试，Redis 抖动后重登记靠看日志确认。
- `Liveness.Run` 丢弃心跳/刷新错误且不打日志，Redis 故障时存活视图会静默冻结。
- `FP_IM_TRUST_PROXY` 取 XFF 最左跳，client 可伪造，仅在网关前有可信代理时可开。
- Cluster 无真机集成测试，上线前建议对真实 Cluster 跑一遍 `im_e2e_test.go`。

设计：`docs/superpowers/specs/2026-09-03-fp-im-design.md`；实施：
`docs/superpowers/plans/2026-09-03-fp-im-gateway.md`。
