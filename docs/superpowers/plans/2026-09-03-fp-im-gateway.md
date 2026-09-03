# fp-im 实施计划：无队列的连接网关

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按 `docs/superpowers/specs/2026-09-03-fp-im-design.md` 交付 fp-im 服务、`fpim` Go SDK（server 端 + client 端）以及 `fpsdk` 的两处配套改动，能在多节点、单 Redis 或 Cluster 下跑通 client↔im↔server 的双向转发。

**Architecture:** fp-im 是独立二进制 `cmd/fp-im`，内部按职责分成 `internal/im/{model,rendezvous,redisx,registry,bus,hub,auth,appcfg,fpauth,wsapi,imgrpc,config}` 十二个小包。Redis 只存三张控制面表和一个节点频道，im 不存消息。业务 server 通过 `sdk/im`（包名 `fpim`）的 gRPC 双向流接入；client 走 ws。

**Tech Stack:** Go 1.25（工具链 1.26）、`google.golang.org/grpc` 1.83、`github.com/redis/go-redis/v9` 9.22（HExpire / SSubscribe / SPublish）、`github.com/coder/websocket`（新增）、`github.com/google/uuid`、buf v1.72 生成 proto、标准库 `testing`。

---

## 一、范围

**本计划做的**

| 项 | 落点 |
|---|---|
| proto 契约 `fp.im.v1.ImService` | `proto/fp/im/v1/im.proto` → `sdk/gen/fp/im/v1` |
| fp-im 服务全部逻辑 | `internal/im/**`、`cmd/fp-im` |
| server 端 SDK（Push / PushMany / Sessions / Kick / OnMessage / OnEvent） | `sdk/im` |
| client 端 Go SDK（Dial / Send / OnMessage / OnClose） | `sdk/im` |
| `fpsdk` 配套：`Options.OnRevoke` 回调、中间件 `AllowGuest` | `sdk/client.go`、`sdk/options.go`、`sdk/middleware.go` |
| 多节点端到端集成测试 | `internal/integration/im_e2e_test.go` |
| 可运行示例与交接文档 | `examples/im-demo`、`docs/im.md` |

**本计划不做的**（spec 第十四节）：PushChannel / Broadcast / 业务频道、`Push(subject, connId)` 定向终端、fp 签发访客 token、JS 客户端、二进制帧、节点 gRPC 直连、fp 配置中心。

## 二、对 spec 的三处修正（实现前必读）

写计划时对照代码库发现三处 spec 与现实不符，按下面执行，不要按 spec 原文：

1. **握手帧必须始终带 `app`。** spec 3.5 节说"app 从 token 的 claim 里解出来"，但 fp 签发的是不透明 token（`fp:sess:{token}` 存 Redis），没有 claim。fp-im 要用某个 app 的 `app_id/app_secret` 去调 fp 的 `ValidateToken`，所以必须先知道 app。握手帧统一为 `{"t":"auth","app":"a1","token":"…"}` 或 `{"t":"auth","app":"a1","guest":"<uuid>"}`。token 与 app 不匹配时 fp 会拒绝，等价于认证失败。
2. **`AppConfigSource` 的第一版实现是 JSON 文件，不是 fp 配置中心。** fp 目前没有配置中心（平台设计第六节是规划，第二阶段计划明确列为不做）。fp-im 从 `FP_IM_APPS_FILE` 读一个 JSON 文件，每 10 秒检查 mtime 变化即重载。文件里每个 app 同时带 `app_id/app_secret`，这一对既用于 fp-im 调 fp 验 token，也用于校验业务 server 连 fp-im 时的凭据。配置中心落地后加一个实现替换即可。
3. **Cluster 不做真机集成测试。** 开发/测试环境只有一台 LAN 上的单机 Redis 8.2.1（`FP_TEST_REDIS_URL`），没有 docker。Cluster 相关的正确性靠三条保证：所有多 key 操作要么单 key 要么同 hash tag（架构断言检查脚本只有一个 KEYS）；`redisx.Open` 的探测逻辑有单元测试；统一使用 sharded pub/sub，单机与 Cluster 走同一条代码路径。

另外两处小偏离：spec 5.4 说被顶替的旧节点"不再 HDEL"，实现上旧节点关闭连接时照常 `HDEL`（connId 全局唯一，删不存在的 field 是空操作，省掉一个分支）；发送队列满关闭连接用标准关闭码 `1013`（try again later），reason `backpressure`，spec 只定义了 4001–4004 四个自定义码。

## 三、Global Constraints

继承自前三阶段，未变：

- `sdk/` 与 `examples/` 不得 import `github.com/basicfu/fp/internal`；`sdk/` 不得出现 `panic`（`sdk/arch_test.go` 断言）。
- 新增直接依赖必须登记在 `internal/integration/dependency_whitelist_test.go` 的 `allowedDirectDependencies`，带一行理由注释。
- 生成代码只进 `sdk/gen/**` 并提交；`buf.gen.yaml` 的 `clean: true` 会清空 `sdk/gen`，那里不得放手写文件。`./scripts/gen.sh` 生成，`buf lint STANDARD` 必须通过：service 名以 `Service` 结尾、RPC 消息必须叫 `<Rpc>Request`/`<Rpc>Response`、enum 零值以 `_UNSPECIFIED` 结尾且值带枚举名前缀。
- 测试只用标准库 `testing`，失败信息用中文说明被违反的性质。需要 Redis 的测试用 `testsupport.NewTestRedis(t)`，`FP_TEST_REDIS_URL` 未设置时直接 `t.Fatal` 打印指引，**不得静默跳过**。全仓 `-p 1`，不得调用 `t.Parallel()`。
- 断言不用 `time.Sleep`，用轮询 `waitUntil`。真实回环监听要重试 `net.Listen`（Windows 释放端口慢）。
- 时间戳一律毫秒 `int64`；注释与错误信息用中文，解释"为什么不是另一种做法"。
- 每个 Task 结束必须 `git commit`，Conventional Commits，中文主题。
- 文件 LF 换行（`.gitattributes` 已强制）。

本计划新增：

- Redis 最低 8.0：依赖 `HEXPIRE`、`HTTL`、`SSUBSCRIBE`/`SPUBLISH`、脚本里的 `cjson`。
- key 与频道名精确为：`fp:im:node`、`fp:im:srv:{app}`、`fp:im:{app:subject}:conn`、`fp:im:node:{nodeId}`。花括号只出现在 conn key 的 hash tag 里。
- 所有 Lua 脚本只允许一个 KEYS（Cluster 合法性），`internal/im/registry/arch_test.go` 断言。
- subject 字符串格式 `u:{uid}` / `g:{uuid}`，`internal/im/model` 与 `sdk/im` 各有一份实现，`internal/integration/im_parity_test.go` 断言两份对同一输入输出一致。
- ws 关闭码：4001 认证失败、4002 策略拒绝、4003 被踢（含顶替）、4004 服务不可用、1013 背压。
- 断开原因字符串：`client`、`timeout`、`kicked`、`replaced`、`revoked`、`backpressure`。
- 配置默认值（spec 第十二节）：`conn_policy=replace`、`conn_limit=5`、`allow_guest=false`、`guest_ip_rate=20`、`node.heartbeat=3s`、`node.dead_after=10s`、`conn.field_ttl=30m`、`conn.field_renew=10m`、`conn.idle_timeout=60s`、`conn.auth_timeout=5s`、`conn.send_queue=256`、`pipeline.flush_interval=0`、`pipeline.flush_size=1`。`flush_size>1` 时必须设 `flush_interval>0`，否则单条命令会永远等不到刷新，配置加载时报错。
- `internal/im/**` 不得 import `github.com/basicfu/fp/internal/{store,service,domain,httpapi,grpcapi,connector,notify}`；只有 `internal/im/fpauth` 可以 import `github.com/basicfu/fp/sdk`（`sdk/gen` 除外）。`internal/im/arch_test.go` 断言。
- ws 帧的 `p` 字段是 JSON 原样值（`json.RawMessage`），im 不解析。Go SDK 的 `Send(payload []byte)` 要求 payload 是合法 JSON，否则返回错误。

## 四、六个实现决策

**4.1 写管道 `redisx.Runner` 是唯一的 Redis 写入口。** 默认立即执行（`Pipelined` 一次），配置攒批后按"时长或条数先到"刷新。registry、bus 都通过它发命令，所以攒批开关不需要改任何业务代码。`PushMany` 不依赖攒批，自己把多条 `HGETALL` 放进一次 `Run`。

**4.2 hub 只做内存路由，不碰 Redis 写。** 握手脚本、`HDEL`、续期由 `wsapi` 和 `cmd` 的循环调 registry 完成。hub 通过三个小接口（`Registry` 读 + 踢、`Liveness`、`Publisher`）依赖外界，单元测试全用假实现，不需要 Redis。

**4.3 节点频道的信封是自定义二进制编码，不是 JSON。** payload 本身是 JSON，再包一层 JSON 要 base64，体积多三分之一。信封只有六个字段，长度前缀编码 40 行代码。

**4.4 fp-im 校验业务 server 凭据的方式与 fp 相同。** gRPC metadata `fp-app-id` / `fp-app-secret`，用 `crypto/subtle` 与 apps 文件里的明文 secret 比较。fp-im 没有数据库，也不做 bcrypt。

**4.5 撤销事件关连接需要 `fpsdk` 开一个回调。** 今天 `Auth.onRevoke` 是未导出方法。新增 `Options.OnRevoke func(RevokeEvent)`，在 watch 循环里于 `cache.drop` 之后调用。fp-im 用它按 token 关闭 ws。

**4.6 fpim server SDK 的 handler 同步调用。** `OnMessage` 在接收协程里同步执行，保证同一 subject 的顺序与 im 投递顺序一致；慢操作由业务方自己开协程。这与 spec 第六节"拓扑不变时有序"的承诺一致。

## 五、文件结构

**新建**

| 文件 | 职责 |
|---|---|
| `proto/fp/im/v1/im.proto` | `ImService.Connect` 双向流与全部消息 |
| `sdk/gen/fp/im/v1/*.pb.go` | 生成产物 |
| `sdk/contract_im_test.go` | im proto 的表面契约断言 |
| `internal/im/model/subject.go` `meta.go` `keys.go` `ua.go` `nodeid.go` `appconfig.go` `frame.go` | 纯类型与纯函数：subject、连接元数据、key 命名、UA 解析、nodeId、app 配置、帧与关闭码 |
| `internal/im/rendezvous/rendezvous.go` | rendezvous hash |
| `internal/im/redisx/open.go` `runner.go` | Cluster 探测的打开函数、写管道 |
| `internal/im/registry/liveness.go` `conns.go` `scripts.go` `arch_test.go` | 三张表的读写、Lua 脚本、单 KEYS 断言 |
| `internal/im/bus/envelope.go` `bus.go` | 信封编解码、节点频道发布订阅 |
| `internal/im/hub/hub.go` `deliver.go` `push.go` `events.go` | 连接表、server 流表、deliver、Push/Kick/Sessions、事件 |
| `internal/im/hub/hubtest/fakes.go` | hub 依赖接口的假实现，hub / wsapi / imgrpc 测试共用 |
| `internal/im/auth/auth.go` | `Authenticator`、`AppConfigSource` 接口与哨兵错误 |
| `internal/im/appcfg/file.go` | JSON 文件实现的 `AppConfigSource` |
| `internal/im/fpauth/fpauth.go` | 基于 `fpsdk` 的 `Authenticator`，每 app 一个 `fpsdk.Client` |
| `internal/im/wsapi/handler.go` `conn.go` `guestlimit.go` | ws 升级、握手、读写循环、访客限流 |
| `internal/im/imgrpc/server.go` `stream.go` `appauth.go` | gRPC 服务、流注册、凭据拦截器 |
| `internal/im/config/config.go` | `FP_IM_*` 环境变量 |
| `internal/im/arch_test.go` | import 规则断言 |
| `cmd/fp-im/main.go` | 装配与生命周期 |
| `sdk/im/subject.go` `server.go` `client.go` `doc.go` | `fpim` 包 |
| `internal/integration/im_e2e_test.go` `im_parity_test.go` | 多节点端到端、常量与格式配对 |
| `examples/im-demo/main.go` `README.md` | 回显示例与手工验收 |
| `docs/im.md` | 交接文档 |
| `scripts/run-im.sh` | 启动脚本 |

**修改**

| 文件 | 改动 |
|---|---|
| `sdk/options.go` | 新增 `OnRevoke func(RevokeEvent)` |
| `sdk/client.go` | watch 循环里调用 `OnRevoke` |
| `sdk/auth.go` | 导出 `RevokeEvent` 类型 |
| `sdk/middleware.go` | `MiddlewareOptions.AllowGuest`、`Identity.GuestID`、`GuestIDHeader` |
| `internal/integration/dependency_whitelist_test.go` | 登记 `github.com/coder/websocket` |
| `.env.example`、`scripts/env.sh` | `FP_IM_*` 变量 |
| `go.mod` / `go.sum` | 新增 websocket 依赖 |

## 六、跨任务接口总表

后面每个 Task 的 **Interfaces** 只写本任务相关的部分，这里是全貌，名字以此为准：

```go
// internal/im/model
type SubjectKind string            // "u" | "g"
type Subject struct{ Kind SubjectKind; ID string }
func User(id string) Subject; func Guest(id string) Subject
func ParseSubject(s string) (Subject, error); func (s Subject) String() string
func IsUUIDv4(s string) bool
type ConnMeta struct{ Node string `json:"n"`; OS string `json:"os"`; Mobile bool `json:"m"`; At int64 `json:"ts"` }
func (m ConnMeta) Encode() string; func DecodeMeta(s string) (ConnMeta, error)
func ParseUA(ua string) (os string, mobile bool)
func NewNodeID(host string, now time.Time) string
func ConnKey(app, subject string) string; func SrvKey(app string) string; func NodeChannel(nodeID string) string
const KeyNodes = "fp:im:node"
type Policy string; const PolicyReplace, PolicyReject, PolicyLimit, PolicyNone Policy = "replace","reject","limit","none"
type AppConfig struct{ AppID, AppSecret string; ConnPolicy Policy; ConnLimit int; AllowGuest bool; GuestIPRate int }
func (c AppConfig) Validate() error
const CloseAuthFailed, ClosePolicyRejected, CloseKicked, CloseUnavailable, CloseBackpressure = 4001,4002,4003,4004,1013
const ReasonClient, ReasonTimeout, ReasonKicked, ReasonReplaced, ReasonRevoked, ReasonBackpressure = "client","timeout","kicked","replaced","revoked","backpressure"
type EventBody struct{ Kind string `json:"k"`; OS string `json:"os"`; Mobile bool `json:"m"`; UA string `json:"ua"`; Reason string `json:"r"`; At int64 `json:"ts"` }

// internal/im/rendezvous
func Pick(nodes []string, key string) (string, bool)

// internal/im/redisx
type Mode string; const ModeStandalone, ModeCluster Mode = "standalone","cluster"
func Open(ctx context.Context, url string) (redis.UniversalClient, Mode, error)
type Runner struct{…}; func NewRunner(c redis.UniversalClient, flushInterval time.Duration, flushSize int) (*Runner, error)
func (r *Runner) Run(ctx context.Context, fn func(redis.Pipeliner)) error; func (r *Runner) Close()

// internal/im/registry
type Liveness struct{…}; func NewLiveness(c redis.UniversalClient, nodeID string, heartbeat, deadAfter time.Duration) *Liveness
func (l *Liveness) NodeID() string; Beat(ctx) error; Refresh(ctx) error; Run(ctx); TrackApp(app string)
func (l *Liveness) SetServing(ctx, app string, on bool) error; LiveNodes() []string; IsLive(node string) bool; ServerNodes(app string) []string
type ConnRef struct{ ConnID, Node string }
type HandshakeResult struct{ Rejected bool; Kicked []ConnRef }
type Conns struct{…}; func NewConns(run *Runner, fieldTTL time.Duration) *Conns
func (c *Conns) Handshake(ctx, app, subject, connID string, meta ConnMeta, policy Policy, limit int, live []string) (HandshakeResult, error)
func (c *Conns) Remove(ctx, app, subject, connID string) error
func (c *Conns) Lookup(ctx, app, subject string, live []string) (map[string]ConnMeta, error)
func (c *Conns) LookupMany(ctx, app string, subjects []string, live []string) ([]map[string]ConnMeta, error)
func (c *Conns) KickAll(ctx, app, subject string) ([]ConnRef, error)
func (c *Conns) KickOne(ctx, app, subject, connID string) (*ConnRef, error)
func (c *Conns) Renew(ctx, app, subject string, connIDs []string) error

// internal/im/bus
const TypeMsg, TypeUp, TypeEvt, TypeKick byte = 1,2,3,4
type Envelope struct{ Type byte; Hops uint8; App, Subject, ConnID, Extra string; Payload []byte }
func (e Envelope) Encode() []byte; func Decode(b []byte) (Envelope, error)
type SignalKind int; const SignalEnvelope, SignalGap SignalKind = iota, …
type Signal struct{ Kind SignalKind; Env Envelope }
type Bus struct{…}; func New(c redis.UniversalClient, run *Runner) *Bus
func (b *Bus) Publish(ctx, nodeID string, env Envelope) error
func (b *Bus) Subscribe(ctx, nodeID string) (<-chan Signal, func(), error)

// internal/im/hub
type Conn interface{ ID() string; Token() string; Send(payload []byte) bool; Close(code int, reason string) }
type Stream interface{ Send(*fpimv1.ConnectResponse) error }
type Registry interface{ Lookup…; LookupMany…; KickAll…; KickOne… }          // registry.Conns 满足
type Liveness interface{ LiveNodes; ServerNodes; TrackApp; SetServing }         // registry.Liveness 满足
type Publisher interface{ Publish(ctx, nodeID string, env bus.Envelope) error } // bus.Bus 满足
type Hub struct{…}; func New(nodeID string, reg Registry, live Liveness, pub Publisher, apps auth.AppConfigSource) *Hub
func (h *Hub) AddConn(ctx, app string, sub Subject, c Conn, meta ConnMeta, ua string)
func (h *Hub) RemoveConn(ctx, app string, sub Subject, connID, reason string)
func (h *Hub) AddStream(ctx, app string, s Stream) (remove func())
func (h *Hub) Deliver(ctx, env bus.Envelope)
func (h *Hub) HandleEnvelope(ctx, env bus.Envelope)
type PushStatus int; const PushSent, PushNotOnline, PushUnavailable PushStatus = iota+1, …
type PushResult struct{ Subject string; Status PushStatus; Nodes int }
func (h *Hub) Push(ctx, app string, sub Subject, payload []byte) PushResult
func (h *Hub) PushMany(ctx, app string, subs []Subject, payload []byte) []PushResult
type Session struct{ ConnID, Node, OS string; Mobile bool; ConnectedAt int64 }
func (h *Hub) Sessions(ctx, app string, sub Subject) ([]Session, error)
func (h *Hub) Kick(ctx, app string, sub Subject, connIDs ...string) error
func (h *Hub) Evict(ctx, app string, sub Subject, refs []ConnRef, reason string)
func (h *Hub) OnRevoked(ctx, app string, tokens []string)
func (h *Hub) ForEachConn(fn func(app string, sub Subject, connID string, meta ConnMeta))

// internal/im/auth
var ErrUnauthorized, ErrUnavailable error
type Authenticator interface{ Verify(ctx, app, token string) (Subject, error) }
type AppConfigSource interface{ Get(app string) (AppConfig, bool); Apps() []string }

// sdk/im (package fpim)
type Subject struct{ Kind SubjectKind; ID string }; func User(id) Subject; func Guest(id) Subject; func Parse(s) (Subject, error); String()
type ServerConfig struct{ Addr, AppID, AppSecret string; Insecure bool; TLSConfig *tls.Config; RequestTimeout time.Duration; Logger *slog.Logger }
func NewServer(cfg ServerConfig) (*Server, error)
func (s *Server) OnMessage(func(ctx, Inbound) error); OnEvent(func(ctx, Event)); StreamHealthy() bool; Close() error
func (s *Server) Push(ctx, Subject, []byte) (PushResult, error); PushMany(ctx, []Subject, []byte) ([]PushResult, error)
func (s *Server) Sessions(ctx, Subject) ([]Session, error); Kick(ctx, Subject, connIDs ...string) error
type ClientConfig struct{ URL, App, Token, Guest, UA, OS string; Mobile bool; Logger *slog.Logger }
func Dial(ctx, ClientConfig) (*Client, error)
func (c *Client) OnMessage(func([]byte)); OnClose(func(code int)); Send(ctx, []byte) error; Close() error
```

---

## Task 1: proto 契约与代码生成

`ImService` 只有一条双向流。命名受 `buf lint STANDARD` 约束：RPC 叫 `Connect`，消息必须叫 `ConnectRequest`（server→im）和 `ConnectResponse`（im→server）。

**Files:**
- Create: `proto/fp/im/v1/im.proto`
- Generate: `sdk/gen/fp/im/v1/im.pb.go`、`sdk/gen/fp/im/v1/im_grpc.pb.go`
- Test: `sdk/contract_im_test.go`

**Interfaces:**
- Produces: Go 包 `fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"`，类型 `ConnectRequest{ReqId; Body oneof Push/PushMany/Kick/Sessions}`、`ConnectResponse{Body oneof Ready/Result/Inbound/Event}`、枚举 `PushStatus`、`EventKind`。

- [ ] **Step 1: 写 proto**

```protobuf
syntax = "proto3";

package fp.im.v1;

option go_package = "github.com/basicfu/fp/sdk/gen/fp/im/v1;fpimv1";

// ImService 是业务 server 接入 fp-im 的唯一入口。
//
// 只有一条双向流：server→im 发请求（Push / PushMany / Kick / Sessions），
// im→server 发结果、client 上来的消息和连接事件。
// 不拆成一元 RPC 的原因与 fp.v1.AuthService.Watch 相同：一条永不空闲的流
// 让所有调用都走热连接，而且 Inbound/Event 本来就只能推。
service ImService {
  rpc Connect(stream ConnectRequest) returns (stream ConnectResponse);
}

// ConnectRequest 是 server 发给 im 的一帧。req_id 由 SDK 生成，
// im 在 Result 里原样带回，SDK 据此配对；Result 的顺序不保证。
message ConnectRequest {
  string req_id = 1;
  oneof body {
    PushRequest     push      = 2;
    PushManyRequest push_many = 3;
    KickRequest     kick      = 4;
    SessionsRequest sessions  = 5;
  }
}

// ConnectResponse 是 im 发给 server 的一帧。
// 用 oneof 是为了将来加新事件时旧 SDK 落到 default 忽略即可。
message ConnectResponse {
  oneof body {
    Ready   ready   = 1;
    Result  result  = 2;
    Inbound inbound = 3;
    Event   event   = 4;
  }
}

// Ready 是流建立后 im 发的第一帧，此后 Push 才会被处理。
message Ready {
  string node_id = 1;
}

message PushRequest {
  string subject = 1;
  bytes  payload = 2;
}

message PushManyRequest {
  repeated string subjects = 1;
  bytes           payload  = 2;
}

// KickRequest 的 conn_ids 为空表示踢掉该 subject 的全部连接。
message KickRequest {
  string          subject  = 1;
  repeated string conn_ids = 2;
}

message SessionsRequest {
  string subject = 1;
}

enum PushStatus {
  PUSH_STATUS_UNSPECIFIED = 0;
  // 此刻有连接，已递给持有它的节点。不代表到达 client 进程。
  PUSH_STATUS_SENT = 1;
  PUSH_STATUS_NOT_ONLINE = 2;
  // Redis 不可用等 im 自身故障。
  PUSH_STATUS_UNAVAILABLE = 3;
}

message PushResult {
  string     subject = 1;
  PushStatus status  = 2;
  // 收到这条消息的节点数，Sent 时 >= 1。
  int32 nodes = 3;
}

message Session {
  string conn_id         = 1;
  string node_id         = 2;
  string os              = 3;
  bool   mobile          = 4;
  int64  connected_at_ms = 5;
}

// Result 是对某个 ConnectRequest 的应答。error 非空表示整个请求失败，
// 文案只用于日志，SDK 不据此分支。
message Result {
  string              req_id   = 1;
  string              error    = 2;
  repeated PushResult pushes   = 3;
  repeated Session    sessions = 4;
}

// Inbound 是 client 发上来的一条消息，payload 原样透传。
message Inbound {
  string subject = 1;
  string conn_id = 2;
  bytes  payload = 3;
}

enum EventKind {
  EVENT_KIND_UNSPECIFIED  = 0;
  EVENT_KIND_CONNECTED    = 1;
  EVENT_KIND_DISCONNECTED = 2;
}

// Event 是连接生命周期事件。ua 只在 Connected 里有值，Redis 不存它。
// reason 只在 Disconnected 里有值：client / timeout / kicked / replaced / revoked / backpressure。
message Event {
  EventKind kind    = 1;
  string    subject = 2;
  string    conn_id = 3;
  string    os      = 4;
  bool      mobile  = 5;
  string    ua      = 6;
  string    reason  = 7;
  int64     at_ms   = 8;
}
```

- [ ] **Step 2: 生成并确认 lint 通过**

Run: `./scripts/gen.sh`
Expected: 无 lint 报错，`sdk/gen/fp/im/v1/im.pb.go` 与 `im_grpc.pb.go` 出现，`go build ./...` 通过。

- [ ] **Step 3: 写契约测试**

`sdk/contract_im_test.go`（`package fpsdk`）：

```go
package fpsdk

import (
	"testing"

	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestImServiceSurface 钉住 ImService 的形状：只有一条双向流。
// 有人加一元 RPC 时这里会红，逼他先想清楚为什么不走这条流。
func TestImServiceSurface(t *testing.T) {
	svc := fpimv1.File_fp_im_v1_im_proto.Services().ByName("ImService")
	if svc == nil {
		t.Fatal("proto 里找不到 ImService")
	}
	if got := svc.Methods().Len(); got != 1 {
		t.Fatalf("ImService 应只有 1 个方法，实际 %d", got)
	}
	m := svc.Methods().Get(0)
	if m.Name() != protoreflect.Name("Connect") || !m.IsStreamingClient() || !m.IsStreamingServer() {
		t.Fatalf("唯一方法应是双向流 Connect，实际 %s client=%v server=%v", m.Name(), m.IsStreamingClient(), m.IsStreamingServer())
	}
}

// TestImEnumZeroValues 钉住枚举零值语义：UNSPECIFIED 永远不能被当成有效状态。
func TestImEnumZeroValues(t *testing.T) {
	if fpimv1.PushStatus_PUSH_STATUS_UNSPECIFIED != 0 || fpimv1.EventKind_EVENT_KIND_UNSPECIFIED != 0 {
		t.Fatal("枚举零值必须是 UNSPECIFIED")
	}
	if fpimv1.PushStatus_PUSH_STATUS_SENT != 1 || fpimv1.PushStatus_PUSH_STATUS_NOT_ONLINE != 2 || fpimv1.PushStatus_PUSH_STATUS_UNAVAILABLE != 3 {
		t.Fatal("PushStatus 的线上数值不能改，SDK 与 im 可能不同版本")
	}
}
```

- [ ] **Step 4: 跑测试**

Run: `./scripts/test.sh ./sdk -run 'TestIm' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add proto/fp/im/v1/im.proto sdk/gen/fp/im/v1 sdk/contract_im_test.go
git commit -m "feat(proto): ImService 双向流契约与代码生成"
```

---

## Task 2: `internal/im/model` 纯类型与纯函数

没有 I/O 的东西全放这里，后面所有包都依赖它，它不依赖任何包。

**Files:**
- Create: `internal/im/model/subject.go`、`meta.go`、`keys.go`、`ua.go`、`nodeid.go`、`appconfig.go`、`frame.go`
- Test: `internal/im/model/subject_test.go`、`meta_test.go`、`keys_test.go`、`ua_test.go`、`appconfig_test.go`

**Interfaces:**
- Produces: 见第六节 `internal/im/model` 段，全部在本任务定义。

- [ ] **Step 1: subject 测试**

```go
package model

import "testing"

func TestParseSubject(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Subject
		ok   bool
	}{
		{"u:1001", Subject{KindUser, "1001"}, true},
		{"g:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f", Subject{KindGuest, "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"}, true},
		{"g:not-a-uuid", Subject{}, false},
		{"x:1", Subject{}, false},
		{"u:", Subject{}, false},
		{"", Subject{}, false},
	} {
		got, err := ParseSubject(tc.in)
		if (err == nil) != tc.ok {
			t.Fatalf("ParseSubject(%q) err=%v，期望 ok=%v", tc.in, err, tc.ok)
		}
		if tc.ok && got != tc.want {
			t.Fatalf("ParseSubject(%q)=%+v，期望 %+v", tc.in, got, tc.want)
		}
		if tc.ok && got.String() != tc.in {
			t.Fatalf("String() 必须还原输入：%q → %q", tc.in, got.String())
		}
	}
}

func TestIsUUIDv4(t *testing.T) {
	if !IsUUIDv4("6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f") {
		t.Fatal("合法 v4 被拒")
	}
	// 版本位是 1，不是 4：访客 id 必须是随机 uuid，时间型 uuid 可被推测
	if IsUUIDv4("6f1c3c2e-4b1a-1d2e-9f0e-7a8b9c0d1e2f") {
		t.Fatal("v1 uuid 不能当访客 id")
	}
	if IsUUIDv4("6F1C3C2E4B1A4D2E9F0E7A8B9C0D1E2F") {
		t.Fatal("只接受带连字符的小写标准写法，避免同一访客有两种 key")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `./scripts/test.sh ./internal/im/model -run 'TestParseSubject|TestIsUUIDv4' -v`
Expected: FAIL，包不存在。

- [ ] **Step 3: 实现 subject.go**

```go
// Package model 是 fp-im 的纯类型层：没有 I/O，不依赖仓库内任何其他包。
package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// SubjectKind 是连接主体的类型前缀。
// 登录用户和访客用不同前缀，是为了两个命名空间在 Redis key 上永远不可能相撞。
type SubjectKind string

const (
	KindUser  SubjectKind = "u"
	KindGuest SubjectKind = "g"
)

// Subject 是一条连接归属的主体。同一 Subject 可以有多条连接（多终端）。
type Subject struct {
	Kind SubjectKind
	ID   string
}

func User(id string) Subject  { return Subject{Kind: KindUser, ID: id} }
func Guest(id string) Subject { return Subject{Kind: KindGuest, ID: id} }

func (s Subject) String() string { return string(s.Kind) + ":" + s.ID }

var ErrBadSubject = errors.New("model: subject 格式非法")

// ParseSubject 解析 "u:{uid}" / "g:{uuid}"。访客 id 必须是标准写法的 uuid v4，
// 否则前端拼一个可预测的字符串就能冒充别人。
func ParseSubject(s string) (Subject, error) {
	kind, id, ok := strings.Cut(s, ":")
	if !ok || id == "" {
		return Subject{}, fmt.Errorf("%w: %q", ErrBadSubject, s)
	}
	switch SubjectKind(kind) {
	case KindUser:
		return User(id), nil
	case KindGuest:
		if !IsUUIDv4(id) {
			return Subject{}, fmt.Errorf("%w: 访客 id 不是 uuid v4: %q", ErrBadSubject, id)
		}
		return Guest(id), nil
	}
	return Subject{}, fmt.Errorf("%w: 未知前缀 %q", ErrBadSubject, kind)
}

// IsUUIDv4 只接受 8-4-4-4-12 小写带连字符的 v4。
// 大写或无连字符的写法虽然是同一个 uuid，但会生成不同的 Redis key，
// 同一访客就成了两个人，所以直接拒绝而不是归一化。
func IsUUIDv4(s string) bool {
	if len(s) != 36 || strings.ToLower(s) != s {
		return false
	}
	u, err := uuid.Parse(s)
	return err == nil && u.Version() == 4
}
```

- [ ] **Step 4: meta / keys / frame / appconfig 测试**

```go
package model

import "testing"

func TestConnMetaRoundTrip(t *testing.T) {
	m := ConnMeta{Node: "im-a", OS: "android", Mobile: true, At: 1756800000000}
	got, err := DecodeMeta(m.Encode())
	if err != nil || got != m {
		t.Fatalf("往返不一致：%+v → %q → %+v (%v)", m, m.Encode(), got, err)
	}
	// 字段名必须是短名，这是 Redis 里每条连接都要存的东西
	if s := m.Encode(); s != `{"n":"im-a","os":"android","m":true,"ts":1756800000000}` {
		t.Fatalf("编码格式变了：%s", s)
	}
}

func TestKeys(t *testing.T) {
	if got := ConnKey("a1", "u:1001"); got != "fp:im:{a1:u:1001}:conn" {
		t.Fatalf("ConnKey=%q", got)
	}
	if got := SrvKey("a1"); got != "fp:im:srv:a1" {
		t.Fatalf("SrvKey=%q", got)
	}
	if got := NodeChannel("im-a"); got != "fp:im:node:im-a" {
		t.Fatalf("NodeChannel=%q", got)
	}
	if KeyNodes != "fp:im:node" {
		t.Fatalf("KeyNodes=%q", KeyNodes)
	}
}

func TestAppConfigValidate(t *testing.T) {
	base := AppConfig{AppID: "a1", AppSecret: "s", ConnPolicy: PolicyReplace, ConnLimit: 5, GuestIPRate: 20}
	if err := base.Validate(); err != nil {
		t.Fatalf("合法配置被拒：%v", err)
	}
	bad := base
	bad.ConnPolicy = "whatever"
	if bad.Validate() == nil {
		t.Fatal("未知策略必须报错")
	}
	bad = base
	bad.ConnPolicy = PolicyLimit
	bad.ConnLimit = 0
	if bad.Validate() == nil {
		t.Fatal("limit 策略下 ConnLimit 必须 >= 1")
	}
	bad = base
	bad.ConnPolicy = PolicyNone
	if bad.Validate() == nil {
		t.Fatal("none 只是脚本内部的重登记模式，不能出现在 app 配置里")
	}
}
```

- [ ] **Step 5: 实现 meta.go、keys.go、appconfig.go、frame.go、nodeid.go**

`meta.go`：

```go
package model

import "encoding/json"

// ConnMeta 是 conn hash 里每个 field 的 value。不存 UA 原文：
// 十万条连接每条几百字节的 UA 会把 Redis 内存花在没人读的东西上。
type ConnMeta struct {
	Node   string `json:"n"`
	OS     string `json:"os"`
	Mobile bool   `json:"m"`
	At     int64  `json:"ts"`
}

func (m ConnMeta) Encode() string {
	b, _ := json.Marshal(m) // 全是标量字段，Marshal 不会失败
	return string(b)
}

func DecodeMeta(s string) (ConnMeta, error) {
	var m ConnMeta
	err := json.Unmarshal([]byte(s), &m)
	return m, err
}

// EventBody 是 Connected/Disconnected 事件经节点频道转发时的 payload。
type EventBody struct {
	Kind   string `json:"k"`
	OS     string `json:"os"`
	Mobile bool   `json:"m"`
	UA     string `json:"ua,omitempty"`
	Reason string `json:"r,omitempty"`
	At     int64  `json:"ts"`
}

const (
	EventConnected    = "connected"
	EventDisconnected = "disconnected"
)
```

`keys.go`：

```go
package model

// KeyNodes 是节点存活表：hash nodeId → 心跳毫秒时间戳。
const KeyNodes = "fp:im:node"

// SrvKey 是"哪些节点持有该 app 的 server 流"表：hash nodeId → 心跳毫秒时间戳。
func SrvKey(app string) string { return "fp:im:srv:" + app }

// ConnKey 是某 subject 的连接表。花括号是 Cluster 的 hash tag：
// 同一 subject 的所有操作落在同一槽，脚本才能合法地只碰这一个 key。
func ConnKey(app, subject string) string { return "fp:im:{" + app + ":" + subject + "}:conn" }

// NodeChannel 是节点私有的 sharded pub/sub 频道，节点启动时订阅一次。
func NodeChannel(nodeID string) string { return "fp:im:node:" + nodeID }
```

`appconfig.go`：

```go
package model

import "fmt"

// Policy 是 app 级的连接策略。
type Policy string

const (
	PolicyReplace Policy = "replace" // 新连接顶掉同 subject 的旧连接
	PolicyReject  Policy = "reject"  // 已有连接时拒绝新连接
	PolicyLimit   Policy = "limit"   // 最多 ConnLimit 条，超出拒新
	// PolicyNone 只在 Redis 重连后重登记时传给脚本：清残留、HSET、HEXPIRE，不判定。
	// 它不是 app 可配置的值。
	PolicyNone Policy = "none"
)

// AppConfig 是一个接入应用在 fp-im 里的全部配置。
// AppID/AppSecret 同时用于 fp-im 调 fp 验 token，和校验业务 server 连 fp-im 的凭据。
type AppConfig struct {
	AppID       string `json:"app_id"`
	AppSecret   string `json:"app_secret"`
	ConnPolicy  Policy `json:"conn_policy"`
	ConnLimit   int    `json:"conn_limit"`
	AllowGuest  bool   `json:"allow_guest"`
	GuestIPRate int    `json:"guest_ip_rate"`
}

func (c AppConfig) Validate() error {
	if c.AppID == "" || c.AppSecret == "" {
		return fmt.Errorf("model: app %q 缺少 app_id 或 app_secret", c.AppID)
	}
	switch c.ConnPolicy {
	case PolicyReplace, PolicyReject:
	case PolicyLimit:
		if c.ConnLimit < 1 {
			return fmt.Errorf("model: app %q 策略为 limit 时 conn_limit 必须 >= 1", c.AppID)
		}
	default:
		return fmt.Errorf("model: app %q 未知 conn_policy %q", c.AppID, c.ConnPolicy)
	}
	if c.AllowGuest && c.GuestIPRate < 1 {
		return fmt.Errorf("model: app %q 允许访客时 guest_ip_rate 必须 >= 1", c.AppID)
	}
	return nil
}
```

`frame.go`：

```go
package model

import "encoding/json"

// ws 关闭码。4001–4004 是 spec 定义的自定义码，1013 是标准 "try again later"。
const (
	CloseAuthFailed     = 4001
	ClosePolicyRejected = 4002
	CloseKicked         = 4003
	CloseUnavailable    = 4004
	CloseBackpressure   = 1013
)

// 断开原因，进 Disconnected 事件的 reason 字段。
const (
	ReasonClient       = "client"
	ReasonTimeout      = "timeout"
	ReasonKicked       = "kicked"
	ReasonReplaced     = "replaced"
	ReasonRevoked      = "revoked"
	ReasonBackpressure = "backpressure"
)

// 帧类型。
const (
	FrameAuth  = "auth"
	FrameHello = "hello"
	FrameMsg   = "msg"
	FramePing  = "ping"
	FramePong  = "pong"
)

// AuthFrame 是 client 连接后的第一帧。App 必须给：fp 的 token 是不透明的，
// im 要先知道用哪个 app 的凭据去 fp 验它。
type AuthFrame struct {
	T      string `json:"t"`
	App    string `json:"app"`
	Token  string `json:"token,omitempty"`
	Guest  string `json:"guest,omitempty"`
	UA     string `json:"ua,omitempty"`
	OS     string `json:"os,omitempty"`
	Mobile *bool  `json:"mobile,omitempty"`
}

// Frame 是握手之后的通用帧。P 对 im 是不透明的 JSON 原样值。
type Frame struct {
	T    string          `json:"t"`
	Conn string          `json:"conn,omitempty"`
	P    json.RawMessage `json:"p,omitempty"`
}
```

`nodeid.go`：

```go
package model

import (
	"fmt"
	"time"
)

// NewNodeID 生成 "主机标识-启动毫秒时间戳"。每次启动都是新节点，
// 这样重启后旧条目会被当成死节点过滤，而不是被误认为仍然有效。
func NewNodeID(host string, now time.Time) string {
	return fmt.Sprintf("%s-%d", host, now.UnixMilli())
}
```

- [ ] **Step 6: UA 解析测试与实现**

`ua_test.go`：

```go
package model

import "testing"

func TestParseUA(t *testing.T) {
	for _, tc := range []struct {
		ua     string
		os     string
		mobile bool
	}{
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Mobile Safari/537.36", "android", true},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15", "ios", true},
		{"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X)", "ios", true},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120", "windows", false},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Safari/605.1.15", "mac", false},
		{"Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/120", "linux", false},
		{"curl/8.4.0", "other", false},
		{"", "other", false},
	} {
		os, mobile := ParseUA(tc.ua)
		if os != tc.os || mobile != tc.mobile {
			t.Errorf("ParseUA(%q)=(%s,%v)，期望 (%s,%v)", tc.ua, os, mobile, tc.os, tc.mobile)
		}
	}
}
```

`ua.go`：

```go
package model

import "strings"

// ParseUA 只提取两个值：操作系统族与是否移动端。
// 不引入 UA 解析库：我们只需要六个取值，子串匹配足够，而且库的更新节奏不由我们控制。
// 顺序有讲究：iPhone 的 UA 里含 "like Mac OS X"，Android 的 UA 里含 "Linux"，
// 所以先判移动端再判桌面端。
func ParseUA(ua string) (os string, mobile bool) {
	switch {
	case strings.Contains(ua, "Android"):
		return "android", true
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"), strings.Contains(ua, "iPod"):
		return "ios", true
	case strings.Contains(ua, "Windows"):
		return "windows", strings.Contains(ua, "Mobile")
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		return "mac", false
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		return "linux", strings.Contains(ua, "Mobile")
	}
	return "other", strings.Contains(ua, "Mobile")
}
```

- [ ] **Step 7: 跑全部 model 测试**

Run: `./scripts/test.sh ./internal/im/model -v`
Expected: 全部 PASS

- [ ] **Step 8: 提交**

```bash
git add internal/im/model
git commit -m "feat(im): model 纯类型层：subject、连接元数据、key 命名、UA 解析"
```

---

## Task 3: rendezvous hash

client→im→server 转发时按 subject 选目标节点。rendezvous 而不是取模，是为了节点增减时只有该节点份额的 subject 换目标。

**Files:**
- Create: `internal/im/rendezvous/rendezvous.go`
- Test: `internal/im/rendezvous/rendezvous_test.go`

**Interfaces:**
- Produces: `func Pick(nodes []string, key string) (string, bool)`，nodes 为空返回 `("", false)`。

- [ ] **Step 1: 测试**

```go
package rendezvous

import "testing"

func TestPickDeterministic(t *testing.T) {
	nodes := []string{"im-a", "im-b", "im-c"}
	a, ok := Pick(nodes, "u:1001")
	if !ok {
		t.Fatal("非空列表必须选出节点")
	}
	for i := 0; i < 100; i++ {
		if b, _ := Pick([]string{"im-c", "im-a", "im-b"}, "u:1001"); b != a {
			t.Fatalf("同一 key 在同一节点集合上必须恒选同节点，且与输入顺序无关：%s vs %s", a, b)
		}
	}
	if _, ok := Pick(nil, "u:1001"); ok {
		t.Fatal("空列表必须返回 false")
	}
}

// 删掉一个节点后，原本不在该节点上的 key 一个都不许挪。这就是用 rendezvous 而不是取模的理由。
func TestPickMinimalDisruption(t *testing.T) {
	nodes := []string{"im-a", "im-b", "im-c", "im-d"}
	before := map[string]string{}
	for i := 0; i < 2000; i++ {
		k := "u:" + string(rune('a'+i%26)) + string(rune('0'+i/26%10)) + string(rune('0'+i/260))
		before[k], _ = Pick(nodes, k)
	}
	after := []string{"im-a", "im-b", "im-d"}
	moved := 0
	for k, was := range before {
		now, _ := Pick(after, k)
		if was != "im-c" && now != was {
			t.Fatalf("key %s 原在 %s，删除 im-c 后不应移动，却到了 %s", k, was, now)
		}
		if was == "im-c" {
			moved++
		}
	}
	if moved == 0 {
		t.Fatal("测试数据没有覆盖到被删节点，样本太小")
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `./scripts/test.sh ./internal/im/rendezvous -v`
Expected: FAIL

- [ ] **Step 3: 实现**

```go
// Package rendezvous 实现最高随机权重哈希（HRW）。
package rendezvous

import "hash/fnv"

// Pick 在 nodes 中为 key 选出权重最高的节点。
// 用 fnv-1a 而不是 xxhash：这里每秒几万次 64 字节输入，fnv 的差距在纳秒级，
// 而 xxhash 是间接依赖，直接 import 要过依赖白名单。
func Pick(nodes []string, key string) (string, bool) {
	var (
		best   string
		bestW  uint64
		picked bool
	)
	for _, n := range nodes {
		h := fnv.New64a()
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write([]byte(key))
		w := h.Sum64()
		// 权重相等时按节点名字典序打破，保证不同调用方算出同一个结果
		if !picked || w > bestW || (w == bestW && n < best) {
			best, bestW, picked = n, w, true
		}
	}
	return best, picked
}
```

- [ ] **Step 4: 跑测试**

Run: `./scripts/test.sh ./internal/im/rendezvous -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/im/rendezvous
git commit -m "feat(im): rendezvous hash 选转发目标节点"
```

---

## Task 4: `internal/im/redisx`：Cluster 探测的打开函数与写管道

**Files:**
- Create: `internal/im/redisx/open.go`、`internal/im/redisx/runner.go`
- Test: `internal/im/redisx/open_test.go`、`internal/im/redisx/runner_test.go`

**Interfaces:**
- Consumes: `testsupport.NewTestRedis(t) *redis.Client`（只在测试里，取 `FP_TEST_REDIS_URL`）。
- Produces: `Open(ctx, url) (redis.UniversalClient, Mode, error)`；`NewRunner(c, flushInterval, flushSize) (*Runner, error)`；`(*Runner).Run(ctx, func(redis.Pipeliner)) error`；`(*Runner).Close()`。

- [ ] **Step 1: Open 的测试**

```go
package redisx

import (
	"context"
	"os"
	"testing"

	"github.com/basicfu/fp/internal/testsupport"
)

func TestOpenDetectsStandalone(t *testing.T) {
	testsupport.NewTestRedis(t) // 只为了触发"未设置 FP_TEST_REDIS_URL 就 Fatal"的统一指引
	c, mode, err := Open(context.Background(), os.Getenv("FP_TEST_REDIS_URL"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()
	if mode != ModeStandalone {
		t.Fatalf("测试环境是单机 Redis，探测结果却是 %s", mode)
	}
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("返回的客户端不可用：%v", err)
	}
}

func TestParseClusterEnabled(t *testing.T) {
	if !clusterEnabled("# Cluster\r\ncluster_enabled:1\r\n") {
		t.Fatal("cluster_enabled:1 应判为 Cluster")
	}
	if clusterEnabled("# Cluster\r\ncluster_enabled:0\r\n") || clusterEnabled("") {
		t.Fatal("cluster_enabled:0 或空串应判为单机")
	}
}
```

- [ ] **Step 2: 实现 open.go**

```go
// Package redisx 是 fp-im 对 go-redis 的两处薄封装：按 INFO 探测模式的打开函数，
// 以及所有写命令共用的管道。
package redisx

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

type Mode string

const (
	ModeStandalone Mode = "standalone"
	ModeCluster    Mode = "cluster"
)

// Open 先按单机连上去，用 INFO cluster 判断是不是 Cluster，是就换成 ClusterClient。
// 不做配置项：同一个地址不可能既是单机又是 Cluster，让人填只会填错。
// 无论哪种模式，上层都只用 SSUBSCRIBE/SPUBLISH，单机 Redis 7 起同样支持。
func Open(ctx context.Context, url string) (redis.UniversalClient, Mode, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, "", fmt.Errorf("redisx: 解析 redis url: %w", err)
	}
	single := redis.NewClient(opt)
	info, err := single.Info(ctx, "cluster").Result()
	if err != nil {
		_ = single.Close()
		return nil, "", fmt.Errorf("redisx: INFO cluster: %w", err)
	}
	if !clusterEnabled(info) {
		return single, ModeStandalone, nil
	}
	_ = single.Close()
	cc := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:     []string{opt.Addr},
		Username:  opt.Username,
		Password:  opt.Password,
		TLSConfig: opt.TLSConfig,
	})
	if err := cc.Ping(ctx).Err(); err != nil {
		_ = cc.Close()
		return nil, "", fmt.Errorf("redisx: ping cluster: %w", err)
	}
	return cc, ModeCluster, nil
}

func clusterEnabled(info string) bool {
	for _, line := range strings.Split(info, "\n") {
		if strings.TrimSpace(line) == "cluster_enabled:1" {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Runner 的测试**

用 go-redis 的 Hook 数管道执行次数，验证"默认每条立即发"和"攒批先到为准"。

```go
package redisx

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/testsupport"
	"github.com/redis/go-redis/v9"
)

type countHook struct{ pipelines atomic.Int64 }

func (h *countHook) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (h *countHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (h *countHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.pipelines.Add(1)
		return next(ctx, cmds)
	}
}

func TestRunnerImmediateByDefault(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	h := &countHook{}
	rdb.AddHook(h)
	r, _ := NewRunner(rdb, 0, 1)
	defer r.Close()
	for i := 0; i < 3; i++ {
		var cmd *redis.IntCmd
		if err := r.Run(context.Background(), func(p redis.Pipeliner) { cmd = p.Incr(context.Background(), "fp:im:test:ctr") }); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if cmd.Val() != int64(i+1) {
			t.Fatalf("命令结果应在 Run 返回后可读：第 %d 次 Incr 得 %d", i+1, cmd.Val())
		}
	}
	if got := h.pipelines.Load(); got != 3 {
		t.Fatalf("默认配置每条命令一次往返，3 条应是 3 次管道执行，实际 %d", got)
	}
}

func TestRunnerBatchesBySizeOrInterval(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	h := &countHook{}
	rdb.AddHook(h)
	r, _ := NewRunner(rdb, 50*time.Millisecond, 3)
	defer r.Close()

	// 三条并发进来，凑满 size=3 立刻刷，只有 1 次管道
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.Run(context.Background(), func(p redis.Pipeliner) { p.Incr(context.Background(), "fp:im:test:ctr") })
		}()
	}
	wg.Wait()
	if got := h.pipelines.Load(); got != 1 {
		t.Fatalf("凑满 flush_size 应合成 1 次管道，实际 %d", got)
	}

	// 单独一条，凑不满 size，靠 interval 刷出去；Run 要在 interval 附近返回而不是永远等
	start := time.Now()
	if err := r.Run(context.Background(), func(p redis.Pipeliner) { p.Incr(context.Background(), "fp:im:test:ctr") }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if el := time.Since(start); el < 30*time.Millisecond || el > 2*time.Second {
		t.Fatalf("单条命令应由 interval 刷出，耗时 %v 不合理", el)
	}
	if got := h.pipelines.Load(); got != 2 {
		t.Fatalf("interval 刷新后应是 2 次管道，实际 %d", got)
	}
}

func TestRunnerRejectsSizeWithoutInterval(t *testing.T) {
	if _, err := NewRunner(nil, 0, 2); err == nil {
		t.Fatal("flush_size>1 且 flush_interval=0 会让单条命令永远等不到刷新，NewRunner 必须拒绝")
	}
}
```

- [ ] **Step 4: 实现 runner.go**

```go
package redisx

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Runner 是所有 Redis 写命令的入口。默认每次 Run 立即执行一次管道；
// 配置了攒批后，多个 Run 合并成一次往返，按"时长或条数先到"刷新。
// 把开关放在这里而不是各调用方，是为了业务代码永远只写 Run(fn)，不感知攒批。
type Runner struct {
	c        redis.UniversalClient
	interval time.Duration
	size     int

	mu      sync.Mutex
	pending []job
	timer   *time.Timer
	closed  bool
}

type job struct {
	fn   func(redis.Pipeliner)
	done chan error
}

var ErrClosed = errors.New("redisx: runner 已关闭")

// NewRunner 的参数对应配置 pipeline.flush_interval / pipeline.flush_size。
// size>1 而 interval=0 是配置错误：单条命令会永远凑不满，这里拒绝而不是运行期挂死。
func NewRunner(c redis.UniversalClient, flushInterval time.Duration, flushSize int) (*Runner, error) {
	if flushSize > 1 && flushInterval <= 0 {
		return nil, errors.New("redisx: pipeline.flush_size > 1 时必须设置 pipeline.flush_interval")
	}
	return &Runner{c: c, interval: flushInterval, size: flushSize}, nil
}

func (r *Runner) batching() bool { return r.interval > 0 }

// Run 把 fn 里排进管道的命令发出去，返回后 fn 捕获的 Cmd 都已填好结果。
// 返回值是管道级错误；单条命令的错误（如 redis.Nil）由调用方看各自的 Cmd.Err()。
func (r *Runner) Run(ctx context.Context, fn func(redis.Pipeliner)) error {
	if !r.batching() {
		_, err := r.c.Pipelined(ctx, func(p redis.Pipeliner) error { fn(p); return nil })
		return ignoreNil(err)
	}
	j := job{fn: fn, done: make(chan error, 1)}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	r.pending = append(r.pending, j)
	if r.size > 1 && len(r.pending) >= r.size {
		batch := r.take()
		r.mu.Unlock()
		r.flush(batch)
	} else {
		if len(r.pending) == 1 {
			r.timer = time.AfterFunc(r.interval, r.flushByTimer)
		}
		r.mu.Unlock()
	}
	select {
	case err := <-j.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) take() []job {
	batch := r.pending
	r.pending = nil
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	return batch
}

func (r *Runner) flushByTimer() {
	r.mu.Lock()
	batch := r.take()
	r.mu.Unlock()
	r.flush(batch)
}

// flush 用独立的 5 秒超时而不是某个调用方的 ctx：一批里的命令来自不同调用方，
// 谁的 ctx 都不该决定别人的命运。
func (r *Runner) flush(batch []job) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		for _, j := range batch {
			j.fn(p)
		}
		return nil
	})
	err = ignoreNil(err)
	for _, j := range batch {
		j.done <- err
	}
}

// Close 把还没刷的批刷出去。之后的 Run 返回 ErrClosed。
func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	batch := r.take()
	r.mu.Unlock()
	r.flush(batch)
}

// Pipelined 会把第一条返回 redis.Nil 的命令当成整批的错误返回，
// 但 Nil 只是"key 不存在"，不是故障，调用方要看各自的 Cmd。
func ignoreNil(err error) error {
	if errors.Is(err, redis.Nil) {
		return nil
	}
	return err
}
```

- [ ] **Step 5: 跑测试**

Run: `./scripts/test.sh ./internal/im/redisx -v`
Expected: 全部 PASS

- [ ] **Step 6: 提交**

```bash
git add internal/im/redisx
git commit -m "feat(im): redisx 打开函数按 INFO 探测 Cluster，写管道可配置攒批"
```

---

## Task 5: `internal/im/registry`：三张表与 Lua 脚本

这是 fp-im 唯一直接写 Redis 的包。所有脚本单 KEYS，本包的 `arch_test.go` 断言。

**Files:**
- Create: `internal/im/registry/liveness.go`、`conns.go`、`scripts.go`、`arch_test.go`
- Test: `internal/im/registry/liveness_test.go`、`conns_test.go`

**Interfaces:**
- Consumes: `model.*`、`redisx.NewRunner(c, interval, size) (*Runner, error)`、`(*Runner).Run`。
- Produces: `Liveness`、`Conns`、`ConnRef`、`HandshakeResult`（签名见第六节）。

- [ ] **Step 1: 脚本文件 scripts.go**

```go
package registry

import "github.com/redis/go-redis/v9"

// 源码单独存一份给 arch_test 用；redis.Script 不暴露源码。
var scriptSources = map[*redis.Script]string{}

func newScript(src string) *redis.Script {
	s := redis.NewScript(src)
	scriptSources[s] = src
	return s
}

func scriptSource(s *redis.Script) string { return scriptSources[s] }

// handshakeScript 在一个 key 上完成：清死节点残留 → 按策略判定 → 登记自己 → 给自己的 field 设 TTL。
// 存活节点列表作为参数传入而不是在脚本里读 fp:im:node：那是另一个 key、另一个槽，Cluster 不允许。
//
// KEYS[1] conn hash
// ARGV[1] connId  ARGV[2] 元数据 JSON  ARGV[3] policy  ARGV[4] limit  ARGV[5] fieldTTL 秒  ARGV[6..] 存活节点
// 返回 {"OK", {connId, nodeId, ...}}（被顶替的连接，扁平列表）或 {"REJECT"}
var handshakeScript = newScript(`
local live = {}
for i = 6, #ARGV do live[ARGV[i]] = true end
local all = redis.call('HGETALL', KEYS[1])
local existing = {}
for i = 1, #all, 2 do
  local cid, meta = all[i], all[i + 1]
  local node = cjson.decode(meta)['n']
  if not live[node] then
    redis.call('HDEL', KEYS[1], cid)
  elseif cid ~= ARGV[1] then
    existing[#existing + 1] = cid
    existing[#existing + 1] = node
  end
end
local count = #existing / 2
local policy = ARGV[3]
if policy == 'reject' and count >= 1 then return {'REJECT'} end
if policy == 'limit' and count >= tonumber(ARGV[4]) then return {'REJECT'} end
local kicked = {}
if policy == 'replace' then
  for i = 1, #existing, 2 do
    redis.call('HDEL', KEYS[1], existing[i])
    kicked[#kicked + 1] = existing[i]
    kicked[#kicked + 1] = existing[i + 1]
  end
end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('HEXPIRE', KEYS[1], tonumber(ARGV[5]), 'FIELDS', 1, ARGV[1])
return {'OK', kicked}
`)

// kickAllScript 原子地取走并删除整个 hash。返回扁平的 {connId, meta, ...}。
var kickAllScript = newScript(`
local all = redis.call('HGETALL', KEYS[1])
if #all > 0 then redis.call('DEL', KEYS[1]) end
return all
`)

// kickOneScript 原子地取走并删除一个 field。返回 meta 或 false。
var kickOneScript = newScript(`
local meta = redis.call('HGET', KEYS[1], ARGV[1])
if meta then redis.call('HDEL', KEYS[1], ARGV[1]) end
return meta
`)
```

- [ ] **Step 2: 单 KEYS 架构断言**

`internal/im/registry/arch_test.go`（`package registry`）：

```go
package registry

import (
	"regexp"
	"testing"

	"github.com/redis/go-redis/v9"
)

// 每个脚本只能引用 KEYS[1]。引用 KEYS[2] 意味着跨槽访问，单机能跑、Cluster 上会 CROSSSLOT。
func TestScriptsTouchSingleKey(t *testing.T) {
	re := regexp.MustCompile(`KEYS\[(\d+)\]`)
	for name, s := range map[string]*redis.Script{
		"handshake": handshakeScript,
		"kickAll":   kickAllScript,
		"kickOne":   kickOneScript,
	} {
		src := scriptSource(s)
		if src == "" {
			t.Fatalf("脚本 %s 没有经 newScript 登记源码", name)
		}
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			if m[1] != "1" {
				t.Errorf("脚本 %s 引用了 KEYS[%s]：脚本只能碰一个 key，否则 Cluster 下非法", name, m[1])
			}
		}
	}
}
```

- [ ] **Step 3: Conns 测试（真实 Redis）**

```go
package registry

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/basicfu/fp/internal/testsupport"
	"github.com/redis/go-redis/v9"
)

func newConns(t *testing.T, fieldTTL time.Duration) (*Conns, *redis.Client) {
	rdb := testsupport.NewTestRedis(t)
	run, err := redisx.NewRunner(rdb, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(run.Close)
	return NewConns(rdb, run, fieldTTL), rdb
}

func meta(node string) model.ConnMeta {
	return model.ConnMeta{Node: node, OS: "linux", At: 1}
}

func TestHandshakePolicyMatrix(t *testing.T) {
	ctx := context.Background()
	live := []string{"im-a", "im-b"}
	for _, tc := range []struct {
		name     string
		policy   model.Policy
		limit    int
		existing int // 先登记多少条活连接（都在 im-b）
		wantRej  bool
		wantKick int
	}{
		{"replace 无旧连接", model.PolicyReplace, 0, 0, false, 0},
		{"replace 顶掉 2 条", model.PolicyReplace, 0, 2, false, 2},
		{"reject 无旧连接", model.PolicyReject, 0, 0, false, 0},
		{"reject 有旧连接", model.PolicyReject, 0, 1, true, 0},
		{"limit 2 未满", model.PolicyLimit, 2, 1, false, 0},
		{"limit 2 已满", model.PolicyLimit, 2, 2, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newConns(t, 30*time.Minute)
			for i := 0; i < tc.existing; i++ {
				if _, err := c.Handshake(ctx, "a1", "u:1", fmt.Sprintf("old%d", i), meta("im-b"), model.PolicyNone, 0, live); err != nil {
					t.Fatalf("预置连接：%v", err)
				}
			}
			res, err := c.Handshake(ctx, "a1", "u:1", "new", meta("im-a"), tc.policy, tc.limit, live)
			if err != nil {
				t.Fatalf("Handshake: %v", err)
			}
			if res.Rejected != tc.wantRej {
				t.Fatalf("Rejected=%v，期望 %v", res.Rejected, tc.wantRej)
			}
			if len(res.Kicked) != tc.wantKick {
				t.Fatalf("Kicked=%d 条，期望 %d", len(res.Kicked), tc.wantKick)
			}
			for _, k := range res.Kicked {
				if k.Node != "im-b" {
					t.Fatalf("被顶替的连接应报告所在节点 im-b，实际 %q", k.Node)
				}
			}
			got, _ := c.Lookup(ctx, "a1", "u:1", live)
			if tc.wantRej {
				if _, ok := got["new"]; ok {
					t.Fatal("被拒绝的连接不能被登记")
				}
			} else if got["new"].Node != "im-a" {
				t.Fatalf("登记后应能查到自己，实际 %+v", got)
			}
		})
	}
}

func TestHandshakeCleansDeadNodeEntriesAndIgnoresThemForLimit(t *testing.T) {
	ctx := context.Background()
	c, _ := newConns(t, 30*time.Minute)
	// im-dead 的两条残留 + im-b 的一条活连接
	for i, n := range []string{"im-dead", "im-dead", "im-b"} {
		_, _ = c.Handshake(ctx, "a1", "u:1", fmt.Sprintf("%s-%d", n, i), meta(n), model.PolicyNone, 0, []string{"im-dead", "im-b"})
	}
	res, err := c.Handshake(ctx, "a1", "u:1", "new", meta("im-a"), model.PolicyLimit, 2, []string{"im-a", "im-b"})
	if err != nil || res.Rejected {
		t.Fatalf("残留不该算进 limit：err=%v rejected=%v", err, res.Rejected)
	}
	got, _ := c.Lookup(ctx, "a1", "u:1", []string{"im-a", "im-b", "im-dead"})
	if len(got) != 2 {
		t.Fatalf("脚本应已 HDEL 死节点残留，剩 im-b 与 new 两条，实际 %d 条：%+v", len(got), got)
	}
}

func TestLookupFiltersDeadNodes(t *testing.T) {
	ctx := context.Background()
	c, _ := newConns(t, 30*time.Minute)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c1", meta("im-a"), model.PolicyNone, 0, nil)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c2", meta("im-dead"), model.PolicyNone, 0, nil)
	got, err := c.Lookup(ctx, "a1", "u:1", []string{"im-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["c1"].Node != "im-a" {
		t.Fatalf("读路径必须过滤死节点条目，实际 %+v", got)
	}
}

func TestFieldTTLAndRenew(t *testing.T) {
	ctx := context.Background()
	c, rdb := newConns(t, 2*time.Second)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c1", meta("im-a"), model.PolicyReplace, 0, []string{"im-a"})
	ttl, err := rdb.HTTL(ctx, model.ConnKey("a1", "u:1"), "c1").Result()
	if err != nil || len(ttl) != 1 || ttl[0] < 1 || ttl[0] > 2 {
		t.Fatalf("握手后 field 应带 2 秒 TTL，实际 %v (%v)", ttl, err)
	}
	run, _ := redisx.NewRunner(rdb, 0, 1)
	defer run.Close()
	c2 := NewConns(rdb, run, 60*time.Second)
	if err := c2.Renew(ctx, "a1", "u:1", []string{"c1"}); err != nil {
		t.Fatal(err)
	}
	ttl, _ = rdb.HTTL(ctx, model.ConnKey("a1", "u:1"), "c1").Result()
	if ttl[0] < 55 {
		t.Fatalf("Renew 后 TTL 应接近 60 秒，实际 %d", ttl[0])
	}
}

func TestKickAllAndKickOne(t *testing.T) {
	ctx := context.Background()
	c, _ := newConns(t, 30*time.Minute)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c1", meta("im-a"), model.PolicyNone, 0, nil)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c2", meta("im-b"), model.PolicyNone, 0, nil)
	one, err := c.KickOne(ctx, "a1", "u:1", "c2")
	if err != nil || one == nil || one.Node != "im-b" {
		t.Fatalf("KickOne 应返回 c2 所在节点：%+v %v", one, err)
	}
	if again, _ := c.KickOne(ctx, "a1", "u:1", "c2"); again != nil {
		t.Fatal("已删除的 field 再踢应返回 nil")
	}
	all, err := c.KickAll(ctx, "a1", "u:1")
	if err != nil || len(all) != 1 || all[0].ConnID != "c1" {
		t.Fatalf("KickAll 应返回剩下的 c1：%+v %v", all, err)
	}
	if got, _ := c.Lookup(ctx, "a1", "u:1", []string{"im-a", "im-b"}); len(got) != 0 {
		t.Fatal("KickAll 后 hash 应为空")
	}
}

func TestRemoveDeletesKeyWhenEmpty(t *testing.T) {
	ctx := context.Background()
	c, rdb := newConns(t, time.Minute)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c1", meta("im-a"), model.PolicyNone, 0, nil)
	if err := c.Remove(ctx, "a1", "u:1", "c1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.Exists(ctx, model.ConnKey("a1", "u:1")).Result(); n != 0 {
		t.Fatal("最后一个 field 删掉后 Redis 应自动删 key，在线 subject 数才等于 key 数")
	}
}
```

- [ ] **Step 4: 实现 conns.go**

```go
package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/redis/go-redis/v9"
)

// ConnRef 指向某条连接及其所在节点。
type ConnRef struct {
	ConnID string
	Node   string
}

type HandshakeResult struct {
	Rejected bool
	Kicked   []ConnRef
}

// Conns 读写每个 subject 的连接表 fp:im:{app:subject}:conn。
type Conns struct {
	c        redis.UniversalClient
	run      *redisx.Runner
	fieldTTL time.Duration
}

func NewConns(c redis.UniversalClient, run *redisx.Runner, fieldTTL time.Duration) *Conns {
	return &Conns{c: c, run: run, fieldTTL: fieldTTL}
}

// Handshake 跑握手脚本。脚本直接执行，不经写管道：它的返回值决定要不要接受连接，
// 攒批只会给握手加延迟而省不了什么。
func (c *Conns) Handshake(ctx context.Context, app, subject, connID string, meta model.ConnMeta, policy model.Policy, limit int, live []string) (HandshakeResult, error) {
	args := []interface{}{connID, meta.Encode(), string(policy), limit, int64(c.fieldTTL.Seconds())}
	for _, n := range live {
		args = append(args, n)
	}
	// 自己所在的节点必须在存活列表里，否则脚本会把刚登记的自己当残留删掉
	if !contains(live, meta.Node) {
		args = append(args, meta.Node)
	}
	raw, err := handshakeScript.Run(ctx, c.c, []string{model.ConnKey(app, subject)}, args...).Slice()
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("registry: 握手脚本: %w", err)
	}
	if len(raw) == 0 {
		return HandshakeResult{}, errors.New("registry: 握手脚本返回空")
	}
	if raw[0] == "REJECT" {
		return HandshakeResult{Rejected: true}, nil
	}
	var res HandshakeResult
	if len(raw) > 1 {
		flat, _ := raw[1].([]interface{})
		for i := 0; i+1 < len(flat); i += 2 {
			res.Kicked = append(res.Kicked, ConnRef{ConnID: str(flat[i]), Node: str(flat[i+1])})
		}
	}
	return res, nil
}

// Remove 在断开时删自己的 field。走写管道：结果没人等。
func (c *Conns) Remove(ctx context.Context, app, subject, connID string) error {
	return c.run.Run(ctx, func(p redis.Pipeliner) { p.HDel(ctx, model.ConnKey(app, subject), connID) })
}

// Lookup 返回 subject 的连接，已过滤掉不在 live 里的节点。
func (c *Conns) Lookup(ctx context.Context, app, subject string, live []string) (map[string]model.ConnMeta, error) {
	many, err := c.LookupMany(ctx, app, []string{subject}, live)
	if err != nil {
		return nil, err
	}
	return many[0], nil
}

// LookupMany 把多个 HGETALL 放进一次 Run，这是 PushMany 只花一次往返的来源。
func (c *Conns) LookupMany(ctx context.Context, app string, subjects []string, live []string) ([]map[string]model.ConnMeta, error) {
	cmds := make([]*redis.MapStringStringCmd, len(subjects))
	err := c.run.Run(ctx, func(p redis.Pipeliner) {
		for i, s := range subjects {
			cmds[i] = p.HGetAll(ctx, model.ConnKey(app, s))
		}
	})
	if err != nil {
		return nil, fmt.Errorf("registry: HGETALL: %w", err)
	}
	liveSet := map[string]bool{}
	for _, n := range live {
		liveSet[n] = true
	}
	out := make([]map[string]model.ConnMeta, len(subjects))
	for i, cmd := range cmds {
		m := map[string]model.ConnMeta{}
		for cid, raw := range cmd.Val() {
			meta, err := model.DecodeMeta(raw)
			if err != nil || !liveSet[meta.Node] {
				continue
			}
			m[cid] = meta
		}
		out[i] = m
	}
	return out, nil
}

func (c *Conns) KickAll(ctx context.Context, app, subject string) ([]ConnRef, error) {
	raw, err := kickAllScript.Run(ctx, c.c, []string{model.ConnKey(app, subject)}).Slice()
	if err != nil {
		return nil, fmt.Errorf("registry: kickAll: %w", err)
	}
	var refs []ConnRef
	for i := 0; i+1 < len(raw); i += 2 {
		meta, err := model.DecodeMeta(str(raw[i+1]))
		if err != nil {
			continue
		}
		refs = append(refs, ConnRef{ConnID: str(raw[i]), Node: meta.Node})
	}
	return refs, nil
}

func (c *Conns) KickOne(ctx context.Context, app, subject, connID string) (*ConnRef, error) {
	raw, err := kickOneScript.Run(ctx, c.c, []string{model.ConnKey(app, subject)}, connID).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("registry: kickOne: %w", err)
	}
	meta, err := model.DecodeMeta(str(raw))
	if err != nil {
		return nil, nil
	}
	return &ConnRef{ConnID: connID, Node: meta.Node}, nil
}

// Renew 给本节点持有的 connIDs 续 field TTL。一个 subject 一条命令，走写管道。
func (c *Conns) Renew(ctx context.Context, app, subject string, connIDs []string) error {
	if len(connIDs) == 0 {
		return nil
	}
	return c.run.Run(ctx, func(p redis.Pipeliner) {
		p.HExpire(ctx, model.ConnKey(app, subject), c.fieldTTL, connIDs...)
	})
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Liveness 测试**

```go
package registry

import (
	"context"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/testsupport"
)

func TestLivenessSeesPeersAndDropsDead(t *testing.T) {
	ctx := context.Background()
	rdb := testsupport.NewTestRedis(t)
	now := time.UnixMilli(1_000_000)
	clock := func() time.Time { return now }
	a := NewLiveness(rdb, "im-a", 3*time.Second, 10*time.Second)
	b := NewLiveness(rdb, "im-b", 3*time.Second, 10*time.Second)
	a.now, b.now = clock, clock

	if err := a.Beat(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !b.IsLive("im-a") || !b.IsLive("im-b") {
		t.Fatalf("b 应看到 a 与自己：%v", b.LiveNodes())
	}
	now = now.Add(11 * time.Second) // a 不再心跳
	_ = b.Refresh(ctx)
	if b.IsLive("im-a") {
		t.Fatal("超过 dead_after 未心跳的节点必须被判死")
	}
	if !b.IsLive("im-b") {
		t.Fatal("自己永远算活的")
	}
}

func TestLivenessServerNodes(t *testing.T) {
	ctx := context.Background()
	rdb := testsupport.NewTestRedis(t)
	a := NewLiveness(rdb, "im-a", 3*time.Second, 10*time.Second)
	b := NewLiveness(rdb, "im-b", 3*time.Second, 10*time.Second)
	if err := a.SetServing(ctx, "a1", true); err != nil {
		t.Fatal(err)
	}
	_ = a.Beat(ctx)
	b.TrackApp("a1")
	_ = b.Refresh(ctx)
	if got := b.ServerNodes("a1"); len(got) != 1 || got[0] != "im-a" {
		t.Fatalf("b 应看到 im-a 持有 a1 的 server 流，实际 %v", got)
	}
	_ = a.SetServing(ctx, "a1", false)
	_ = b.Refresh(ctx)
	if got := b.ServerNodes("a1"); len(got) != 0 {
		t.Fatalf("SetServing(false) 应立即 HDEL，实际 %v", got)
	}
}
```

- [ ] **Step 6: 实现 liveness.go**

```go
package registry

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/redis/go-redis/v9"
)

// Liveness 维护两张心跳表的本地快照：fp:im:node 与每个 app 的 fp:im:srv:{app}。
// 热路径只读快照，不碰 Redis；快照每 heartbeat 刷一次，最多滞后一个周期。
type Liveness struct {
	c         redis.UniversalClient
	nodeID    string
	heartbeat time.Duration
	deadAfter time.Duration
	now       func() time.Time

	mu      sync.RWMutex
	live    map[string]int64            // nodeId → 心跳毫秒
	srv     map[string]map[string]int64 // app → nodeId → 心跳毫秒
	serving map[string]bool             // 本节点正在为哪些 app 持有 server 流
	tracked map[string]bool             // 需要刷新 srv 表的 app
}

func NewLiveness(c redis.UniversalClient, nodeID string, heartbeat, deadAfter time.Duration) *Liveness {
	return &Liveness{
		c: c, nodeID: nodeID, heartbeat: heartbeat, deadAfter: deadAfter, now: time.Now,
		live: map[string]int64{}, srv: map[string]map[string]int64{}, serving: map[string]bool{}, tracked: map[string]bool{},
	}
}

func (l *Liveness) NodeID() string { return l.nodeID }

// Beat 写自己的心跳：节点表一条，每个正在服务的 app 各一条。
func (l *Liveness) Beat(ctx context.Context) error {
	ts := l.now().UnixMilli()
	l.mu.RLock()
	apps := make([]string, 0, len(l.serving))
	for a := range l.serving {
		apps = append(apps, a)
	}
	l.mu.RUnlock()
	_, err := l.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, model.KeyNodes, l.nodeID, ts)
		for _, a := range apps {
			p.HSet(ctx, model.SrvKey(a), l.nodeID, ts)
		}
		return nil
	})
	return err
}

// Refresh 重读节点表与所有被追踪 app 的 srv 表，过滤掉过期条目。
func (l *Liveness) Refresh(ctx context.Context) error {
	l.mu.RLock()
	apps := make([]string, 0, len(l.tracked))
	for a := range l.tracked {
		apps = append(apps, a)
	}
	l.mu.RUnlock()

	var nodeCmd *redis.MapStringStringCmd
	srvCmds := make([]*redis.MapStringStringCmd, len(apps))
	_, err := l.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		nodeCmd = p.HGetAll(ctx, model.KeyNodes)
		for i, a := range apps {
			srvCmds[i] = p.HGetAll(ctx, model.SrvKey(a))
		}
		return nil
	})
	if err != nil {
		return err
	}
	cutoff := l.now().UnixMilli() - l.deadAfter.Milliseconds()
	live := fresh(nodeCmd.Val(), cutoff)
	live[l.nodeID] = l.now().UnixMilli() // 自己永远算活的，哪怕 Beat 还没跑
	srv := map[string]map[string]int64{}
	for i, a := range apps {
		m := fresh(srvCmds[i].Val(), cutoff)
		for n := range m {
			if _, ok := live[n]; !ok {
				delete(m, n) // srv 表里的节点还得在节点表里活着
			}
		}
		srv[a] = m
	}
	l.mu.Lock()
	l.live, l.srv = live, srv
	l.mu.Unlock()
	return nil
}

func fresh(raw map[string]string, cutoff int64) map[string]int64 {
	out := map[string]int64{}
	for n, v := range raw {
		ts, err := strconv.ParseInt(v, 10, 64)
		if err == nil && ts >= cutoff {
			out[n] = ts
		}
	}
	return out
}

// Run 每 heartbeat 做一次 Beat + Refresh，直到 ctx 结束。
func (l *Liveness) Run(ctx context.Context) {
	t := time.NewTicker(l.heartbeat)
	defer t.Stop()
	for {
		_ = l.Beat(ctx)
		_ = l.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// TrackApp 让 Refresh 开始拉该 app 的 srv 表。hub 第一次需要某 app 的 ServerNodes 时调用。
func (l *Liveness) TrackApp(app string) {
	l.mu.Lock()
	l.tracked[app] = true
	l.mu.Unlock()
}

// SetServing 立即写或删 srv 表里自己的条目。删要立即：其他节点的缓存滞后 3 秒，
// 越早删越少消息被误发到这里。
func (l *Liveness) SetServing(ctx context.Context, app string, on bool) error {
	l.mu.Lock()
	if on {
		l.serving[app] = true
		l.tracked[app] = true
	} else {
		delete(l.serving, app)
	}
	l.mu.Unlock()
	if on {
		return l.c.HSet(ctx, model.SrvKey(app), l.nodeID, l.now().UnixMilli()).Err()
	}
	return l.c.HDel(ctx, model.SrvKey(app), l.nodeID).Err()
}

func (l *Liveness) LiveNodes() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0, len(l.live)+1)
	for n := range l.live {
		out = append(out, n)
	}
	if _, ok := l.live[l.nodeID]; !ok {
		out = append(out, l.nodeID)
	}
	sort.Strings(out)
	return out
}

func (l *Liveness) IsLive(node string) bool {
	if node == l.nodeID {
		return true
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.live[node]
	return ok
}

// ServerNodes 返回持有该 app server 流且活着的节点，含自己（若自己在服务）。
func (l *Liveness) ServerNodes(app string) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0, len(l.srv[app])+1)
	for n := range l.srv[app] {
		out = append(out, n)
	}
	if l.serving[app] && !contains(out, l.nodeID) {
		out = append(out, l.nodeID)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 7: 跑测试**

Run: `./scripts/test.sh ./internal/im/registry -v`
Expected: 全部 PASS。若 `HEXPIRE` 报 unknown command，说明 `FP_TEST_REDIS_URL` 指向的不是 Redis 8，停下来换环境，不要改测试。

- [ ] **Step 8: 提交**

```bash
git add internal/im/registry
git commit -m "feat(im): registry 三张表与单 key 握手脚本，field 级 HEXPIRE"
```

---

## Task 6: `internal/im/bus`：信封编码与节点频道

**Files:**
- Create: `internal/im/bus/envelope.go`、`internal/im/bus/bus.go`
- Test: `internal/im/bus/envelope_test.go`、`internal/im/bus/bus_test.go`

**Interfaces:**
- Consumes: `model.NodeChannel`、`redisx.Runner`。
- Produces: `Envelope`、`Encode/Decode`、`Bus.Publish`、`Bus.Subscribe`、`Signal`（见第六节）。

- [ ] **Step 1: 信封测试**

```go
package bus

import (
	"bytes"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	in := Envelope{Type: TypeUp, Hops: 1, App: "a1", Subject: "u:1001", ConnID: "c-1", Extra: "replaced", Payload: []byte(`{"x":1}`)}
	out, err := Decode(in.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != in.Type || out.Hops != in.Hops || out.App != in.App || out.Subject != in.Subject ||
		out.ConnID != in.ConnID || out.Extra != in.Extra || !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("往返不一致：%+v → %+v", in, out)
	}
}

func TestEnvelopeNoBase64Overhead(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1000)
	e := Envelope{Type: TypeMsg, App: "a1", Subject: "u:1", Payload: payload}
	if n := len(e.Encode()); n > 1000+64 {
		t.Fatalf("信封开销应是常数级，1000 字节 payload 编码后 %d 字节", n)
	}
}

func TestDecodeRejectsTruncated(t *testing.T) {
	e := Envelope{Type: TypeMsg, App: "a1", Subject: "u:1", Payload: []byte("hello")}
	b := e.Encode()
	for cut := 0; cut < len(b); cut++ {
		if _, err := Decode(b[:cut]); err == nil {
			t.Fatalf("截断到 %d 字节仍能解码，会把损坏的消息投给 client", cut)
		}
	}
}
```

- [ ] **Step 2: 实现 envelope.go**

```go
// Package bus 是 im 节点之间唯一的通信通道：Redis sharded pub/sub 上的节点私有频道。
package bus

import (
	"encoding/binary"
	"errors"
)

// 信封类型。
const (
	TypeMsg  byte = 1 // server→client 的推送，投给本地 Subject 的所有连接
	TypeUp   byte = 2 // client→server 的消息，交给本地 server 流
	TypeEvt  byte = 3 // 连接事件，同 TypeUp 路由，Payload 是 model.EventBody 的 JSON
	TypeKick byte = 4 // 踢掉本地某条连接，Extra 是 reason
)

// Envelope 是节点频道上的一条消息。payload 本身是 JSON，所以信封不再用 JSON：
// 再包一层就得 base64，体积多三分之一。
type Envelope struct {
	Type    byte
	Hops    uint8
	App     string
	Subject string
	ConnID  string
	Extra   string
	Payload []byte
}

// Encode 布局：type(1) hops(1) 然后五个长度前缀（uvarint）的字节串。
func (e Envelope) Encode() []byte {
	b := make([]byte, 0, 16+len(e.App)+len(e.Subject)+len(e.ConnID)+len(e.Extra)+len(e.Payload))
	b = append(b, e.Type, e.Hops)
	for _, s := range [][]byte{[]byte(e.App), []byte(e.Subject), []byte(e.ConnID), []byte(e.Extra), e.Payload} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	return b
}

var ErrBadEnvelope = errors.New("bus: 信封损坏")

func Decode(b []byte) (Envelope, error) {
	if len(b) < 2 {
		return Envelope{}, ErrBadEnvelope
	}
	e := Envelope{Type: b[0], Hops: b[1]}
	rest := b[2:]
	fields := make([][]byte, 5)
	for i := range fields {
		n, k := binary.Uvarint(rest)
		if k <= 0 || uint64(len(rest)-k) < n {
			return Envelope{}, ErrBadEnvelope
		}
		fields[i] = rest[k : k+int(n)]
		rest = rest[k+int(n):]
	}
	if len(rest) != 0 {
		return Envelope{}, ErrBadEnvelope
	}
	e.App, e.Subject, e.ConnID, e.Extra = string(fields[0]), string(fields[1]), string(fields[2]), string(fields[3])
	e.Payload = append([]byte(nil), fields[4]...)
	return e, nil
}
```

- [ ] **Step 3: Bus 测试（真实 Redis）**

```go
package bus

import (
	"context"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestPublishReachesOnlyTargetNode(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	run, _ := redisx.NewRunner(rdb, 0, 1)
	defer run.Close()
	b := New(rdb, run)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chA, closeA, err := b.Subscribe(ctx, "im-a")
	if err != nil {
		t.Fatal(err)
	}
	defer closeA()
	chB, closeB, err := b.Subscribe(ctx, "im-b")
	if err != nil {
		t.Fatal(err)
	}
	defer closeB()

	env := Envelope{Type: TypeMsg, App: "a1", Subject: "u:1", Payload: []byte(`1`)}
	if err := b.Publish(ctx, "im-a", env); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-chA:
		if sig.Kind != SignalEnvelope || sig.Env.Subject != "u:1" {
			t.Fatalf("im-a 收到的不是发出的信封：%+v", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("im-a 5 秒内没收到")
	}
	select {
	case sig := <-chB:
		t.Fatalf("im-b 不该收到发给 im-a 的消息：%+v", sig)
	case <-time.After(200 * time.Millisecond):
	}
}
```

- [ ] **Step 4: 实现 bus.go**

模式照抄 `internal/store/revoke.go`：`Receive` 等订阅确认、派生子 ctx、用 `ChannelWithSubscriptions` 把 go-redis 的静默重订阅变成可观察的 `SignalGap`。

```go
package bus

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/redis/go-redis/v9"
)

type SignalKind int

const (
	SignalEnvelope SignalKind = iota
	// SignalGap 表示 go-redis 断线后自动重订阅了一次：中间的消息已经丢了，
	// 上层要把本地连接重新登记一遍（spec 第七节"Redis 断线重连"）。
	SignalGap
)

type Signal struct {
	Kind SignalKind
	Env  Envelope
}

type Bus struct {
	c   redis.UniversalClient
	run *redisx.Runner
}

func New(c redis.UniversalClient, run *redisx.Runner) *Bus { return &Bus{c: c, run: run} }

// Publish 走写管道，SPUBLISH 的返回值（订阅者数）没人关心。
func (b *Bus) Publish(ctx context.Context, nodeID string, env Envelope) error {
	return b.run.Run(ctx, func(p redis.Pipeliner) {
		p.SPublish(ctx, model.NodeChannel(nodeID), env.Encode())
	})
}

// Subscribe 订阅本节点频道。返回的 closeFn 幂等。
// 出通道缓冲 1024：节点频道是全节点所有跨节点消息的总入口，缓冲小了会在突发时阻塞 go-redis 的读协程。
func (b *Bus) Subscribe(ctx context.Context, nodeID string) (<-chan Signal, func(), error) {
	sub := b.c.SSubscribe(ctx, model.NodeChannel(nodeID))
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("bus: 订阅节点频道: %w", err)
	}
	out := make(chan Signal, 1024)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(out)
		defer sub.Close()
		first := true
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-sub.ChannelWithSubscriptions():
				if !ok {
					return
				}
				switch v := m.(type) {
				case *redis.Subscription:
					// 第一条是我们自己的订阅确认；之后再出现就是 go-redis 重连后的重订阅
					if first {
						first = false
						continue
					}
					out <- Signal{Kind: SignalGap}
				case *redis.Message:
					env, err := Decode([]byte(v.Payload))
					if err != nil {
						slog.Warn("bus: 丢弃损坏的信封", "err", err, "len", len(v.Payload))
						continue
					}
					select {
					case out <- Signal{Kind: SignalEnvelope, Env: env}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return out, cancel, nil
}
```

- [ ] **Step 5: 跑测试**

Run: `./scripts/test.sh ./internal/im/bus -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/im/bus
git commit -m "feat(im): bus 节点频道 sharded pub/sub 与二进制信封"
```

---

## Task 7: `internal/im/hub` 核心：连接表、server 流表、deliver、事件

hub 是纯内存的路由器。三个依赖接口在本包定义，测试全用假实现。

**Files:**
- Create: `internal/im/hub/hub.go`、`internal/im/hub/deliver.go`、`internal/im/hub/events.go`
- Test: `internal/im/hub/hub_test.go`（含假实现）、`internal/im/hub/deliver_test.go`

**Interfaces:**
- Consumes: `bus.Envelope`、`registry.ConnRef`、`model.*`、`rendezvous.Pick`、`auth.AppConfigSource`（Task 10 定义，这里先在本包内声明同形接口 `appSource`，Task 10 落地后替换为 `auth.AppConfigSource`；为避免两次改动，**本任务直接创建 `internal/im/auth/auth.go`** 的接口部分，见 Step 1）。
- Produces: `Conn`、`Stream`、`Registry`、`Liveness`、`Publisher` 接口；`Hub` 与 `New`、`AddConn`、`RemoveConn`、`AddStream`、`Deliver`、`HandleEnvelope`、`ForEachConn`。

- [ ] **Step 1: 先创建 `internal/im/auth/auth.go`（只有接口与哨兵错误）**

```go
// Package auth 定义 fp-im 对外部身份与配置的两个依赖接口。
// im 的其他包只认这两个接口；fp 的具体实现在 fpauth / appcfg。
package auth

import (
	"context"
	"errors"

	"github.com/basicfu/fp/internal/im/model"
)

var (
	ErrUnauthorized = errors.New("auth: 凭证无效")
	ErrUnavailable  = errors.New("auth: 身份服务不可用")
)

// Authenticator 用 app 的凭据去验 client 的 token。
type Authenticator interface {
	Verify(ctx context.Context, app, token string) (model.Subject, error)
}

// AppConfigSource 提供 app 级配置。Get 必须是纯内存读：握手与 Push 的热路径都会调它。
type AppConfigSource interface {
	Get(app string) (model.AppConfig, bool)
	Apps() []string
}
```

- [ ] **Step 2: hub 测试的假实现与核心测试**

`internal/im/hub/hub_test.go`：

```go
package hub

import (
	"context"
	"sync"
	"testing"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
)

type fakeConn struct {
	id, token string
	mu        sync.Mutex
	sent      [][]byte
	closed    *struct{ code int; reason string }
	full      bool
}

func (c *fakeConn) ID() string    { return c.id }
func (c *fakeConn) Token() string { return c.token }
func (c *fakeConn) Send(p []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.full {
		return false
	}
	c.sent = append(c.sent, p)
	return true
}
func (c *fakeConn) Close(code int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = &struct{ code int; reason string }{code, reason}
}

type fakeStream struct {
	mu   sync.Mutex
	got  []*fpimv1.ConnectResponse
	fail bool
}

func (s *fakeStream) Send(r *fpimv1.ConnectResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return context.Canceled
	}
	s.got = append(s.got, r)
	return nil
}

type fakeReg struct {
	lookup map[string]map[string]model.ConnMeta // subject → connId → meta
	kicked []registry.ConnRef
}

func (r *fakeReg) Lookup(_ context.Context, _, subject string, _ []string) (map[string]model.ConnMeta, error) {
	return r.lookup[subject], nil
}
func (r *fakeReg) LookupMany(ctx context.Context, app string, subjects []string, live []string) ([]map[string]model.ConnMeta, error) {
	out := make([]map[string]model.ConnMeta, len(subjects))
	for i, s := range subjects {
		out[i], _ = r.Lookup(ctx, app, s, live)
	}
	return out, nil
}
func (r *fakeReg) KickAll(_ context.Context, _, subject string) ([]registry.ConnRef, error) {
	var refs []registry.ConnRef
	for cid, m := range r.lookup[subject] {
		refs = append(refs, registry.ConnRef{ConnID: cid, Node: m.Node})
	}
	delete(r.lookup, subject)
	return refs, nil
}
func (r *fakeReg) KickOne(_ context.Context, _, subject, connID string) (*registry.ConnRef, error) {
	m, ok := r.lookup[subject][connID]
	if !ok {
		return nil, nil
	}
	delete(r.lookup[subject], connID)
	return &registry.ConnRef{ConnID: connID, Node: m.Node}, nil
}

type fakeLive struct {
	nodes   []string
	servers map[string][]string
	serving map[string]bool
}

func (l *fakeLive) LiveNodes() []string            { return l.nodes }
func (l *fakeLive) ServerNodes(app string) []string { return l.servers[app] }
func (l *fakeLive) TrackApp(string)                {}
func (l *fakeLive) SetServing(_ context.Context, app string, on bool) error {
	if l.serving == nil {
		l.serving = map[string]bool{}
	}
	l.serving[app] = on
	return nil
}

type fakePub struct {
	mu   sync.Mutex
	sent []struct {
		node string
		env  bus.Envelope
	}
}

func (p *fakePub) Publish(_ context.Context, node string, env bus.Envelope) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, struct {
		node string
		env  bus.Envelope
	}{node, env})
	return nil
}

type fakeApps map[string]model.AppConfig

func (a fakeApps) Get(app string) (model.AppConfig, bool) { c, ok := a[app]; return c, ok }
func (a fakeApps) Apps() []string {
	var out []string
	for k := range a {
		out = append(out, k)
	}
	return out
}

func newHub(t *testing.T) (*Hub, *fakeReg, *fakeLive, *fakePub) {
	t.Helper()
	reg := &fakeReg{lookup: map[string]map[string]model.ConnMeta{}}
	live := &fakeLive{nodes: []string{"im-a", "im-b", "im-c"}, servers: map[string][]string{}}
	pub := &fakePub{}
	apps := fakeApps{"a1": {AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace}}
	return New("im-a", reg, live, pub, apps), reg, live, pub
}

func TestAddConnEmitsConnectedEventToLocalStream(t *testing.T) {
	h, _, live, _ := newHub(t)
	s := &fakeStream{}
	h.AddStream(context.Background(), "a1", s)
	if !live.serving["a1"] {
		t.Fatal("第一条 server 流出现时必须 SetServing(true)")
	}
	c := &fakeConn{id: "c1", token: "tok"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a", OS: "ios", Mobile: true, At: 5}, "UA/1")
	if len(s.got) != 1 || s.got[0].GetEvent() == nil {
		t.Fatalf("应收到 1 条 Connected 事件，实际 %+v", s.got)
	}
	ev := s.got[0].GetEvent()
	if ev.Kind != fpimv1.EventKind_EVENT_KIND_CONNECTED || ev.Subject != "u:1" || ev.ConnId != "c1" || ev.Ua != "UA/1" || ev.Os != "ios" || !ev.Mobile {
		t.Fatalf("Connected 事件字段不对：%+v", ev)
	}
	h.RemoveConn(context.Background(), "a1", model.User("1"), "c1", model.ReasonClient)
	if len(s.got) != 2 || s.got[1].GetEvent().Kind != fpimv1.EventKind_EVENT_KIND_DISCONNECTED || s.got[1].GetEvent().Reason != "client" {
		t.Fatalf("应收到 Disconnected(client)，实际 %+v", s.got)
	}
}

func TestRemoveLastStreamClearsServing(t *testing.T) {
	h, _, live, _ := newHub(t)
	rm1 := h.AddStream(context.Background(), "a1", &fakeStream{})
	rm2 := h.AddStream(context.Background(), "a1", &fakeStream{})
	rm1()
	if !live.serving["a1"] {
		t.Fatal("还有一条流时不能 SetServing(false)")
	}
	rm2()
	if live.serving["a1"] {
		t.Fatal("最后一条流断开必须立即 SetServing(false)")
	}
}

func TestHandleEnvelopeMsgFansOutToLocalConns(t *testing.T) {
	h, _, _, _ := newHub(t)
	c1 := &fakeConn{id: "c1"}
	c2 := &fakeConn{id: "c2"}
	h.AddConn(context.Background(), "a1", model.User("1"), c1, model.ConnMeta{Node: "im-a"}, "")
	h.AddConn(context.Background(), "a1", model.User("1"), c2, model.ConnMeta{Node: "im-a"}, "")
	h.HandleEnvelope(context.Background(), bus.Envelope{Type: bus.TypeMsg, App: "a1", Subject: "u:1", Payload: []byte(`{"a":1}`)})
	if len(c1.sent) != 1 || len(c2.sent) != 1 {
		t.Fatalf("同 subject 的每条本地连接都应收到：c1=%d c2=%d", len(c1.sent), len(c2.sent))
	}
	// 本地没有的 subject：静默丢弃，不 panic
	h.HandleEnvelope(context.Background(), bus.Envelope{Type: bus.TypeMsg, App: "a1", Subject: "u:404", Payload: []byte(`1`)})
}

func TestHandleEnvelopeKickClosesLocalConn(t *testing.T) {
	h, _, _, _ := newHub(t)
	c := &fakeConn{id: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	h.HandleEnvelope(context.Background(), bus.Envelope{Type: bus.TypeKick, App: "a1", Subject: "u:1", ConnID: "c1", Extra: model.ReasonReplaced})
	if c.closed == nil || c.closed.code != model.CloseKicked || c.closed.reason != model.ReasonReplaced {
		t.Fatalf("KICK 应以 4003/replaced 关闭本地连接，实际 %+v", c.closed)
	}
}

func TestSendQueueFullClosesWithBackpressure(t *testing.T) {
	h, _, _, _ := newHub(t)
	c := &fakeConn{id: "c1", full: true}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	h.HandleEnvelope(context.Background(), bus.Envelope{Type: bus.TypeMsg, App: "a1", Subject: "u:1", Payload: []byte(`1`)})
	if c.closed == nil || c.closed.code != model.CloseBackpressure || c.closed.reason != model.ReasonBackpressure {
		t.Fatalf("发送队列满应以 1013/backpressure 关闭，实际 %+v", c.closed)
	}
}
```

`internal/im/hub/deliver_test.go`：

```go
package hub

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
)

func up(subject string) bus.Envelope {
	return bus.Envelope{Type: bus.TypeUp, App: "a1", Subject: subject, ConnID: "c1", Payload: []byte(`{"m":1}`)}
}

func TestDeliverLocalStreamWins(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-a", "im-b"}
	s := &fakeStream{}
	h.AddStream(context.Background(), "a1", s)
	h.Deliver(context.Background(), up("u:1"))
	if len(s.got) != 1 || s.got[0].GetInbound() == nil || s.got[0].GetInbound().Subject != "u:1" {
		t.Fatalf("本地有流必须本地直投，实际 %+v", s.got)
	}
	if len(pub.sent) != 0 {
		t.Fatal("本地直投不该发 Redis")
	}
}

func TestDeliverForwardsOneHopByRendezvous(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-b", "im-c"}
	h.Deliver(context.Background(), up("u:1"))
	if len(pub.sent) != 1 || pub.sent[0].env.Hops != 1 || pub.sent[0].env.Type != bus.TypeUp {
		t.Fatalf("本地无流应转发一跳且 hops=1，实际 %+v", pub.sent)
	}
	first := pub.sent[0].node
	for i := 0; i < 20; i++ {
		h.Deliver(context.Background(), up("u:1"))
		if pub.sent[len(pub.sent)-1].node != first {
			t.Fatal("同 subject 必须恒选同一目标节点，否则顺序会乱")
		}
	}
}

func TestDeliverDropsAfterOneHopOrWhenNoServer(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-b"}
	e := up("u:1")
	e.Hops = 1
	h.Deliver(context.Background(), e)
	if len(pub.sent) != 0 {
		t.Fatal("hops>=1 且本地无流必须静默丢弃，不能再转")
	}
	live.servers["a1"] = nil
	h.Deliver(context.Background(), up("u:2"))
	if len(pub.sent) != 0 {
		t.Fatal("全网无 server 必须静默丢弃")
	}
}

func TestDeliverSameSubjectSameLocalStream(t *testing.T) {
	h, _, _, _ := newHub(t)
	s1, s2 := &fakeStream{}, &fakeStream{}
	h.AddStream(context.Background(), "a1", s1)
	h.AddStream(context.Background(), "a1", s2)
	for i := 0; i < 10; i++ {
		h.Deliver(context.Background(), up("u:1"))
	}
	if !(len(s1.got) == 10 && len(s2.got) == 0) && !(len(s1.got) == 0 && len(s2.got) == 10) {
		t.Fatalf("同 subject 的消息必须落在同一条本地流：s1=%d s2=%d", len(s1.got), len(s2.got))
	}
}

func TestDeliverExcludesSelfWhenForwarding(t *testing.T) {
	// 自己在 srv 表里（缓存里还没来得及删）但本地已无流：不能发给自己
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-a", "im-b"}
	h.Deliver(context.Background(), up("u:1"))
	if len(pub.sent) != 1 || pub.sent[0].node != "im-b" {
		t.Fatalf("应排除自己转发给 im-b，实际 %+v", pub.sent)
	}
}
```

- [ ] **Step 3: 确认失败**

Run: `./scripts/test.sh ./internal/im/hub -v`
Expected: FAIL，包不存在。

- [ ] **Step 4: 实现 hub.go**

```go
// Package hub 是 im 节点的内存路由器：本地连接表、本地 server 流表、跨节点转发判定。
// 它不直接碰 Redis：写由 wsapi 与 cmd 的循环通过 registry 完成，hub 只读快照与发布信封。
package hub

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
)

// Conn 是一条 ws 连接在 hub 眼里的样子。Send 必须非阻塞：返回 false 表示发送队列满。
type Conn interface {
	ID() string
	Token() string
	Send(payload []byte) bool
	Close(code int, reason string)
}

// Stream 是一条 server 流。实现要自己保证 Send 并发安全（gRPC 流不允许并发 Send）。
type Stream interface {
	Send(*fpimv1.ConnectResponse) error
}

type Registry interface {
	Lookup(ctx context.Context, app, subject string, live []string) (map[string]model.ConnMeta, error)
	LookupMany(ctx context.Context, app string, subjects []string, live []string) ([]map[string]model.ConnMeta, error)
	KickAll(ctx context.Context, app, subject string) ([]registry.ConnRef, error)
	KickOne(ctx context.Context, app, subject, connID string) (*registry.ConnRef, error)
}

type Liveness interface {
	LiveNodes() []string
	ServerNodes(app string) []string
	TrackApp(app string)
	SetServing(ctx context.Context, app string, on bool) error
}

type Publisher interface {
	Publish(ctx context.Context, nodeID string, env bus.Envelope) error
}

type connEntry struct {
	conn Conn
	meta model.ConnMeta
}

type Hub struct {
	nodeID string
	reg    Registry
	live   Liveness
	pub    Publisher
	apps   auth.AppConfigSource

	mu      sync.RWMutex
	conns   map[string]map[string]*connEntry // app+"\x00"+subject → connId → entry
	byToken map[string][]Conn                // app+"\x00"+token → conns（撤销时用）
	streams map[string][]Stream              // app → streams
}

func New(nodeID string, reg Registry, live Liveness, pub Publisher, apps auth.AppConfigSource) *Hub {
	return &Hub{
		nodeID: nodeID, reg: reg, live: live, pub: pub, apps: apps,
		conns: map[string]map[string]*connEntry{}, byToken: map[string][]Conn{}, streams: map[string][]Stream{},
	}
}

func (h *Hub) NodeID() string { return h.nodeID }

func key(app, subject string) string { return app + "\x00" + subject }

// AddConn 登记连接并向 server 发 Connected 事件。调用前 registry 的握手脚本必须已成功。
func (h *Hub) AddConn(ctx context.Context, app string, sub model.Subject, c Conn, meta model.ConnMeta, ua string) {
	k := key(app, sub.String())
	h.mu.Lock()
	if h.conns[k] == nil {
		h.conns[k] = map[string]*connEntry{}
	}
	h.conns[k][c.ID()] = &connEntry{conn: c, meta: meta}
	if tok := c.Token(); tok != "" {
		h.byToken[key(app, tok)] = append(h.byToken[key(app, tok)], c)
	}
	h.mu.Unlock()
	h.emit(ctx, app, sub, c.ID(), model.EventBody{Kind: model.EventConnected, OS: meta.OS, Mobile: meta.Mobile, UA: ua, At: meta.At})
}

// RemoveConn 从表里删除并发 Disconnected 事件。幂等：重复删除不发第二次事件。
func (h *Hub) RemoveConn(ctx context.Context, app string, sub model.Subject, connID, reason string) {
	k := key(app, sub.String())
	h.mu.Lock()
	entry, ok := h.conns[k][connID]
	if ok {
		delete(h.conns[k], connID)
		if len(h.conns[k]) == 0 {
			delete(h.conns, k)
		}
		if tok := entry.conn.Token(); tok != "" {
			tk := key(app, tok)
			list := h.byToken[tk][:0]
			for _, c := range h.byToken[tk] {
				if c.ID() != connID {
					list = append(list, c)
				}
			}
			if len(list) == 0 {
				delete(h.byToken, tk)
			} else {
				h.byToken[tk] = list
			}
		}
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	h.emit(ctx, app, sub, connID, model.EventBody{Kind: model.EventDisconnected, OS: entry.meta.OS, Mobile: entry.meta.Mobile, Reason: reason, At: nowMs()})
}

// AddStream 登记一条 server 流；第一条时向 Redis 声明本节点在服务该 app。
// 返回的 remove 在流断开时调用；最后一条流移除时立即撤销声明。
func (h *Hub) AddStream(ctx context.Context, app string, s Stream) (remove func()) {
	h.mu.Lock()
	h.streams[app] = append(h.streams[app], s)
	first := len(h.streams[app]) == 1
	h.mu.Unlock()
	if first {
		if err := h.live.SetServing(ctx, app, true); err != nil {
			slog.Error("hub: 声明 server 流失败", "app", app, "err", err)
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			list := h.streams[app][:0]
			for _, x := range h.streams[app] {
				if x != s {
					list = append(list, x)
				}
			}
			last := len(list) == 0
			if last {
				delete(h.streams, app)
			} else {
				h.streams[app] = list
			}
			h.mu.Unlock()
			if last {
				if err := h.live.SetServing(context.Background(), app, false); err != nil {
					slog.Error("hub: 撤销 server 流声明失败", "app", app, "err", err)
				}
			}
		})
	}
}

// localStream 按 subject 哈希在本地流里选一条，同 subject 恒选同一条。
func (h *Hub) localStream(app, subject string) (Stream, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	list := h.streams[app]
	if len(list) == 0 {
		return nil, false
	}
	return list[hash32(subject)%uint32(len(list))], true
}

// sendLocal 把 payload 投给本地该 subject 的全部连接，返回投出的条数。
// 发送队列满的连接以 1013/backpressure 关闭：让它重连并向业务方拉历史，比无限堆积拖垮节点好。
func (h *Hub) sendLocal(app, subject string, payload []byte) int {
	h.mu.RLock()
	entries := make([]*connEntry, 0, len(h.conns[key(app, subject)]))
	for _, e := range h.conns[key(app, subject)] {
		entries = append(entries, e)
	}
	h.mu.RUnlock()
	n := 0
	for _, e := range entries {
		if e.conn.Send(payload) {
			n++
		} else {
			e.conn.Close(model.CloseBackpressure, model.ReasonBackpressure)
		}
	}
	return n
}

func (h *Hub) closeLocal(app, subject, connID string, code int, reason string) bool {
	h.mu.RLock()
	e, ok := h.conns[key(app, subject)][connID]
	h.mu.RUnlock()
	if ok {
		e.conn.Close(code, reason)
	}
	return ok
}

// HandleEnvelope 处理从节点频道收到的一条信封。
func (h *Hub) HandleEnvelope(ctx context.Context, env bus.Envelope) {
	switch env.Type {
	case bus.TypeMsg:
		h.sendLocal(env.App, env.Subject, env.Payload)
	case bus.TypeUp, bus.TypeEvt:
		h.Deliver(ctx, env)
	case bus.TypeKick:
		h.closeLocal(env.App, env.Subject, env.ConnID, model.CloseKicked, env.Extra)
	default:
		slog.Warn("hub: 未知信封类型", "type", env.Type)
	}
}

// ForEachConn 遍历本地连接，给续期与重登记用。回调里不得再调 hub 的方法。
func (h *Hub) ForEachConn(fn func(app string, sub model.Subject, connID string, meta model.ConnMeta)) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for k, m := range h.conns {
		app, subject := splitKey(k)
		sub, err := model.ParseSubject(subject)
		if err != nil {
			continue
		}
		for cid, e := range m {
			fn(app, sub, cid, e.meta)
		}
	}
}

func splitKey(k string) (app, subject string) {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

func hash32(s string) uint32 {
	f := fnv.New32a()
	f.Write([]byte(s))
	return f.Sum32()
}
```

- [ ] **Step 5: 实现 deliver.go**

```go
package hub

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/rendezvous"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
)

// Deliver 把 client 的消息或连接事件送到 server。
// 本地有流 → 本地直投；否则按 subject 做 rendezvous 转一跳；已转过一跳还没有流 → 静默丢弃。
// 静默是有意的：server 的故障对 client 不可见，client 靠自己的超时重发。
func (h *Hub) Deliver(ctx context.Context, env bus.Envelope) {
	if s, ok := h.localStream(env.App, env.Subject); ok {
		if err := s.Send(toResponse(env)); err != nil {
			slog.Warn("hub: 写 server 流失败，消息丢弃", "app", env.App, "subject", env.Subject, "err", err)
		}
		return
	}
	if env.Hops >= 1 {
		return
	}
	h.live.TrackApp(env.App)
	nodes := h.live.ServerNodes(env.App)
	candidates := nodes[:0:0]
	for _, n := range nodes {
		if n != h.nodeID {
			candidates = append(candidates, n)
		}
	}
	target, ok := rendezvous.Pick(candidates, env.Subject)
	if !ok {
		return
	}
	env.Hops++
	if err := h.pub.Publish(ctx, target, env); err != nil {
		slog.Warn("hub: 转发到节点失败，消息丢弃", "target", target, "err", err)
	}
}

func toResponse(env bus.Envelope) *fpimv1.ConnectResponse {
	if env.Type == bus.TypeEvt {
		var body model.EventBody
		_ = json.Unmarshal(env.Payload, &body)
		kind := fpimv1.EventKind_EVENT_KIND_DISCONNECTED
		if body.Kind == model.EventConnected {
			kind = fpimv1.EventKind_EVENT_KIND_CONNECTED
		}
		return &fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Event{Event: &fpimv1.Event{
			Kind: kind, Subject: env.Subject, ConnId: env.ConnID,
			Os: body.OS, Mobile: body.Mobile, Ua: body.UA, Reason: body.Reason, AtMs: body.At,
		}}}
	}
	return &fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Inbound{Inbound: &fpimv1.Inbound{
		Subject: env.Subject, ConnId: env.ConnID, Payload: env.Payload,
	}}}
}
```

- [ ] **Step 6: 实现 events.go**

```go
package hub

import (
	"context"
	"encoding/json"
	"time"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
)

func nowMs() int64 { return time.Now().UnixMilli() }

// emit 把连接事件当成一条 Evt 信封走 Deliver，路由规则与 client 消息完全一样。
func (h *Hub) emit(ctx context.Context, app string, sub model.Subject, connID string, body model.EventBody) {
	if body.At == 0 {
		body.At = nowMs()
	}
	payload, _ := json.Marshal(body)
	h.Deliver(ctx, bus.Envelope{Type: bus.TypeEvt, App: app, Subject: sub.String(), ConnID: connID, Payload: payload})
}
```

- [ ] **Step 7: 跑测试**

Run: `./scripts/test.sh ./internal/im/hub -v`
Expected: 全部 PASS

- [ ] **Step 8: 提交**

```bash
git add internal/im/auth internal/im/hub
git commit -m "feat(im): hub 内存路由：连接表、server 流表、deliver 转一跳、连接事件"
```

---

## Task 8: hub 的 server 侧操作：Push / PushMany / Sessions / Kick / Evict / OnRevoked

**Files:**
- Create: `internal/im/hub/push.go`
- Test: `internal/im/hub/push_test.go`

**Interfaces:**
- Consumes: Task 7 的 `Hub` 与假实现。
- Produces: `PushStatus`、`PushResult`、`Session`、`Push`、`PushMany`、`Sessions`、`Kick`、`Evict`、`OnRevoked`。

- [ ] **Step 1: 测试**

```go
package hub

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
)

func TestPushLocalShortCircuitUnderSingleConnPolicy(t *testing.T) {
	h, reg, _, pub := newHub(t) // a1 策略 replace
	c := &fakeConn{id: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	// 注册表故意放一条别的节点的条目：单连接策略下本地命中就不该再查表
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c9": {Node: "im-b"}}
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != PushSent || res.Nodes != 1 {
		t.Fatalf("本地命中应 Sent{1}，实际 %+v", res)
	}
	if len(c.sent) != 1 || len(pub.sent) != 0 {
		t.Fatalf("本地直投且不发 Redis：sent=%d pub=%d", len(c.sent), len(pub.sent))
	}
}

func TestPushUnderLimitPolicyStillFansOutToOtherNodes(t *testing.T) {
	h, reg, _, pub := newHub(t)
	h.apps = fakeApps{"a1": {AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyLimit, ConnLimit: 3}}
	c := &fakeConn{id: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-a"}, "c2": {Node: "im-b"}, "c3": {Node: "im-b"}}
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != PushSent || res.Nodes != 2 {
		t.Fatalf("本地 1 + im-b 1 = Sent{2}，实际 %+v", res)
	}
	if len(pub.sent) != 1 || pub.sent[0].node != "im-b" || pub.sent[0].env.Type != bus.TypeMsg {
		t.Fatalf("应只喊 im-b 一次且排除自己，实际 %+v", pub.sent)
	}
}

func TestPushNotOnline(t *testing.T) {
	h, _, _, pub := newHub(t)
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != PushNotOnline || len(pub.sent) != 0 {
		t.Fatalf("无连接应 NotOnline 且不喊，实际 %+v %+v", res, pub.sent)
	}
}

func TestPushManyOneLookup(t *testing.T) {
	h, reg, _, pub := newHub(t)
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b"}}
	reg.lookup["u:2"] = map[string]model.ConnMeta{"c2": {Node: "im-c"}, "c3": {Node: "im-b"}}
	res := h.PushMany(context.Background(), "a1", []model.Subject{model.User("1"), model.User("2"), model.User("3")}, []byte(`1`))
	if len(res) != 3 || res[0].Status != PushSent || res[1].Status != PushSent || res[1].Nodes != 2 || res[2].Status != PushNotOnline {
		t.Fatalf("PushMany 结果不对：%+v", res)
	}
	if len(pub.sent) != 3 {
		t.Fatalf("u:1→im-b、u:2→im-b、u:2→im-c 共 3 次喊，实际 %d", len(pub.sent))
	}
}

func TestSessionsFromRegistry(t *testing.T) {
	h, reg, _, _ := newHub(t)
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b", OS: "ios", Mobile: true, At: 7}}
	got, err := h.Sessions(context.Background(), "a1", model.User("1"))
	if err != nil || len(got) != 1 || got[0].ConnID != "c1" || got[0].Node != "im-b" || got[0].OS != "ios" || !got[0].Mobile || got[0].ConnectedAt != 7 {
		t.Fatalf("Sessions=%+v err=%v", got, err)
	}
}

func TestKickAllLocalAndRemote(t *testing.T) {
	h, reg, _, pub := newHub(t)
	local := &fakeConn{id: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), local, model.ConnMeta{Node: "im-a"}, "")
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-a"}, "c2": {Node: "im-b"}}
	if err := h.Kick(context.Background(), "a1", model.User("1")); err != nil {
		t.Fatal(err)
	}
	if local.closed == nil || local.closed.code != model.CloseKicked || local.closed.reason != model.ReasonKicked {
		t.Fatalf("本地连接应直接关闭 4003/kicked，实际 %+v", local.closed)
	}
	if len(pub.sent) != 1 || pub.sent[0].node != "im-b" || pub.sent[0].env.Type != bus.TypeKick || pub.sent[0].env.ConnID != "c2" || pub.sent[0].env.Extra != model.ReasonKicked {
		t.Fatalf("远端连接应发 KICK 信封，实际 %+v", pub.sent)
	}
}

func TestKickOneUnknownIsNoop(t *testing.T) {
	h, _, _, pub := newHub(t)
	if err := h.Kick(context.Background(), "a1", model.User("1"), "nope"); err != nil {
		t.Fatal(err)
	}
	if len(pub.sent) != 0 {
		t.Fatal("不存在的 connId 不该发任何信封")
	}
}

func TestEvictReplacedRefs(t *testing.T) {
	h, _, _, pub := newHub(t)
	local := &fakeConn{id: "old-local"}
	h.AddConn(context.Background(), "a1", model.User("1"), local, model.ConnMeta{Node: "im-a"}, "")
	h.Evict(context.Background(), "a1", model.User("1"), []registry.ConnRef{{ConnID: "old-local", Node: "im-a"}, {ConnID: "old-remote", Node: "im-c"}}, model.ReasonReplaced)
	if local.closed == nil || local.closed.reason != model.ReasonReplaced {
		t.Fatalf("本地被顶替的连接应以 replaced 关闭，实际 %+v", local.closed)
	}
	if len(pub.sent) != 1 || pub.sent[0].node != "im-c" || pub.sent[0].env.Extra != model.ReasonReplaced {
		t.Fatalf("远端被顶替的连接应发 KICK(replaced)，实际 %+v", pub.sent)
	}
}

func TestOnRevokedClosesConnsByToken(t *testing.T) {
	h, _, _, _ := newHub(t)
	c1 := &fakeConn{id: "c1", token: "t1"}
	c2 := &fakeConn{id: "c2", token: "t2"}
	h.AddConn(context.Background(), "a1", model.User("1"), c1, model.ConnMeta{Node: "im-a"}, "")
	h.AddConn(context.Background(), "a1", model.User("1"), c2, model.ConnMeta{Node: "im-a"}, "")
	h.OnRevoked(context.Background(), "a1", []string{"t1", "t-unknown"})
	if c1.closed == nil || c1.closed.code != model.CloseAuthFailed || c1.closed.reason != model.ReasonRevoked {
		t.Fatalf("持有被撤销 token 的连接应以 4001/revoked 关闭，实际 %+v", c1.closed)
	}
	if c2.closed != nil {
		t.Fatal("其他 token 的连接不能被误关")
	}
}
```

- [ ] **Step 2: 实现 push.go**

```go
package hub

import (
	"context"
	"log/slog"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
)

type PushStatus int

const (
	PushSent PushStatus = iota + 1
	PushNotOnline
	PushUnavailable
)

type PushResult struct {
	Subject string
	Status  PushStatus
	Nodes   int
}

type Session struct {
	ConnID      string
	Node        string
	OS          string
	Mobile      bool
	ConnectedAt int64
}

// singleConn 判断该 app 是否全网最多一条连接：是则本地命中就不必再查注册表。
func (h *Hub) singleConn(app string) bool {
	cfg, ok := h.apps.Get(app)
	return ok && (cfg.ConnPolicy == model.PolicyReplace || cfg.ConnPolicy == model.PolicyReject)
}

func (h *Hub) Push(ctx context.Context, app string, sub model.Subject, payload []byte) PushResult {
	return h.PushMany(ctx, app, []model.Subject{sub}, payload)[0]
}

// PushMany 对每个 subject：先投本地，单连接策略下本地命中即返回；否则一次 LookupMany 找其他节点。
func (h *Hub) PushMany(ctx context.Context, app string, subs []model.Subject, payload []byte) []PushResult {
	results := make([]PushResult, len(subs))
	localHits := make([]int, len(subs))
	var pending []int // 还需要查注册表的下标
	for i, s := range subs {
		results[i].Subject = s.String()
		localHits[i] = h.sendLocal(app, s.String(), payload)
		if localHits[i] > 0 && h.singleConn(app) {
			results[i].Status, results[i].Nodes = PushSent, 1
			continue
		}
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		return results
	}
	subjects := make([]string, len(pending))
	for j, i := range pending {
		subjects[j] = subs[i].String()
	}
	lookups, err := h.reg.LookupMany(ctx, app, subjects, h.live.LiveNodes())
	if err != nil {
		slog.Warn("hub: 查注册表失败", "app", app, "err", err)
		for _, i := range pending {
			results[i].Status = PushUnavailable
		}
		return results
	}
	for j, i := range pending {
		nodes := map[string]bool{}
		for _, meta := range lookups[j] {
			if meta.Node != h.nodeID {
				nodes[meta.Node] = true
			}
		}
		for n := range nodes {
			env := bus.Envelope{Type: bus.TypeMsg, App: app, Subject: subs[i].String(), Payload: payload}
			if err := h.pub.Publish(ctx, n, env); err != nil {
				slog.Warn("hub: 喊节点失败", "node", n, "err", err)
				delete(nodes, n)
			}
		}
		total := len(nodes)
		if localHits[i] > 0 {
			total++
		}
		if total == 0 {
			results[i].Status = PushNotOnline
		} else {
			results[i].Status, results[i].Nodes = PushSent, total
		}
	}
	return results
}

func (h *Hub) Sessions(ctx context.Context, app string, sub model.Subject) ([]Session, error) {
	conns, err := h.reg.Lookup(ctx, app, sub.String(), h.live.LiveNodes())
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(conns))
	for cid, m := range conns {
		out = append(out, Session{ConnID: cid, Node: m.Node, OS: m.OS, Mobile: m.Mobile, ConnectedAt: m.At})
	}
	return out, nil
}

// Kick 不传 connIDs 踢全部。本地连接直接关，远端连接发 KICK 信封。
func (h *Hub) Kick(ctx context.Context, app string, sub model.Subject, connIDs ...string) error {
	var refs []registry.ConnRef
	if len(connIDs) == 0 {
		all, err := h.reg.KickAll(ctx, app, sub.String())
		if err != nil {
			return err
		}
		refs = all
	} else {
		for _, cid := range connIDs {
			ref, err := h.reg.KickOne(ctx, app, sub.String(), cid)
			if err != nil {
				return err
			}
			if ref != nil {
				refs = append(refs, *ref)
			}
		}
	}
	h.Evict(ctx, app, sub, refs, model.ReasonKicked)
	return nil
}

// Evict 关闭一组已从注册表删除的连接：本地的直接关，远端的发 KICK 信封。
// 握手脚本返回的被顶替列表和 Kick 都走这里。
func (h *Hub) Evict(ctx context.Context, app string, sub model.Subject, refs []registry.ConnRef, reason string) {
	for _, r := range refs {
		if r.Node == h.nodeID {
			h.closeLocal(app, sub.String(), r.ConnID, model.CloseKicked, reason)
			continue
		}
		env := bus.Envelope{Type: bus.TypeKick, App: app, Subject: sub.String(), ConnID: r.ConnID, Extra: reason}
		if err := h.pub.Publish(ctx, r.Node, env); err != nil {
			slog.Warn("hub: 发 KICK 失败", "node", r.Node, "err", err)
		}
	}
}

// OnRevoked 在 fp 撤销 token 时关闭持有这些 token 的本地连接。
// 用 4001 而不是 4003：对 client 来说这和 token 过期是同一件事，应去重新登录而不是重连。
func (h *Hub) OnRevoked(ctx context.Context, app string, tokens []string) {
	var victims []Conn
	h.mu.RLock()
	for _, t := range tokens {
		victims = append(victims, h.byToken[key(app, t)]...)
	}
	h.mu.RUnlock()
	for _, c := range victims {
		c.Close(model.CloseAuthFailed, model.ReasonRevoked)
	}
}
```

- [ ] **Step 3: 跑测试**

Run: `./scripts/test.sh ./internal/im/hub -v`
Expected: 全部 PASS

- [ ] **Step 4: 提交**

```bash
git add internal/im/hub
git commit -m "feat(im): hub 的 Push/PushMany/Sessions/Kick 与撤销关连接"
```

---

## Task 9: `fpsdk` 开放撤销回调 `Options.OnRevoke`

fp-im 要在 fp 撤销 token 时关掉对应 ws。今天 `Auth.onRevoke` 是未导出方法，只会清缓存。

**Files:**
- Modify: `sdk/options.go`（`Options` 加字段）、`sdk/auth.go`（导出 `RevokeEvent`，`onRevoke` 里调回调）
- Test: `sdk/client_test.go`（复用现有 `newStubEnv` 桩服务）

**Interfaces:**
- Produces: `type RevokeEvent struct{ Tokens, UserIDs []string; AppID, Reason string; AtMs int64 }`；`Options.OnRevoke func(RevokeEvent)`。

- [ ] **Step 1: 测试**

在 `sdk/client_test.go` 里加。先读该文件顶部的 `newStubEnv` / `startStub` 用法，桩服务的 Watch 侧有一个可以往流里推 `RevokeEvent` 的方法（若没有，仿照现有 revoke 测试给桩加一个 `pushRevoke(ev *fpv1.RevokeEvent)`）。

```go
func TestOnRevokeCallbackReceivesEvent(t *testing.T) {
	env := newStubEnv(t)
	got := make(chan RevokeEvent, 1)
	opts := env.options()
	opts.OnRevoke = func(ev RevokeEvent) { got <- ev }
	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitUntil(t, c.StreamHealthy, "流应就绪")
	env.pushRevoke(&fpv1.RevokeEvent{Tokens: []string{"tok-1"}, AppId: "app-1", Reason: "logout", AtMs: 123})
	select {
	case ev := <-got:
		if len(ev.Tokens) != 1 || ev.Tokens[0] != "tok-1" || ev.AppID != "app-1" || ev.Reason != "logout" || ev.AtMs != 123 {
			t.Fatalf("回调收到的事件字段不对：%+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("5 秒内回调没被调用")
	}
}
```

`env.options()` 若不存在，就按该文件里其他测试构造 `Options` 的方式内联写。

- [ ] **Step 2: 确认失败**

Run: `./scripts/test.sh ./sdk -run TestOnRevokeCallbackReceivesEvent -v`
Expected: FAIL，`RevokeEvent`/`OnRevoke` 未定义。

- [ ] **Step 3: 实现**

`sdk/options.go` 的 `Options` 加：

```go
	// OnRevoke 在收到 fp 的撤销事件后被调用（缓存已先清掉）。
	// 给需要在撤销时做额外动作的进程用，比如 fp-im 关闭持有该 token 的 ws。
	// 回调在 watch 协程里同步执行，必须快；nil 表示不关心。
	OnRevoke func(RevokeEvent)
```

`sdk/auth.go`：

```go
// RevokeEvent 是暴露给 Options.OnRevoke 的撤销事件，字段与 fp.v1.RevokeEvent 一一对应。
// 不直接暴露 proto 类型：SDK 的公开面不该绑在生成代码上。
type RevokeEvent struct {
	Tokens  []string
	UserIDs []string
	AppID   string // 空表示所有 app
	Reason  string
	AtMs    int64
}

func (a *Auth) onRevoke(ev *fpv1.RevokeEvent) {
	a.cache.drop(ev.GetTokens()...)
	if a.c.opts.OnRevoke != nil {
		a.c.opts.OnRevoke(RevokeEvent{
			Tokens: ev.GetTokens(), UserIDs: ev.GetUserIds(), AppID: ev.GetAppId(), Reason: ev.GetReason(), AtMs: ev.GetAtMs(),
		})
	}
}
```

- [ ] **Step 4: 跑 sdk 全部测试**

Run: `./scripts/test.sh ./sdk -v`
Expected: 全部 PASS（含 `TestSDKHasNoPanic`）。

- [ ] **Step 5: 提交**

```bash
git add sdk/options.go sdk/auth.go sdk/client_test.go
git commit -m "feat(sdk): Options.OnRevoke 把撤销事件暴露给宿主进程"
```

---

## Task 10: `appcfg` 文件配置源、`fpauth` 适配器、`internal/im` 的 import 断言

**Files:**
- Create: `internal/im/appcfg/file.go`、`internal/im/fpauth/fpauth.go`、`internal/im/arch_test.go`
- Test: `internal/im/appcfg/file_test.go`、`internal/im/fpauth/fpauth_test.go`

**Interfaces:**
- Consumes: `auth.Authenticator`、`auth.AppConfigSource`、`model.AppConfig`、`fpsdk.New` / `Options` / `Auth.Validate` / `Options.OnRevoke`。
- Produces: `appcfg.LoadFile(path) (*appcfg.Source, error)`、`(*Source).Get/Apps/Reload() error/Watch(ctx, interval)`；`fpauth.New(cfg fpauth.Config) (*fpauth.Authenticator, error)`、`Verify`、`Close()`。

- [ ] **Step 1: appcfg 测试**

```go
package appcfg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/model"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFileDefaultsAndValidation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "apps.json")
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1"},{"app_id":"a2","app_secret":"s2","conn_policy":"limit","conn_limit":3,"allow_guest":true}]}`)
	src, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	a1, ok := src.Get("a1")
	if !ok || a1.ConnPolicy != model.PolicyReplace || a1.ConnLimit != 5 || a1.GuestIPRate != 20 || a1.AllowGuest {
		t.Fatalf("未写的字段应取 spec 默认值，实际 %+v", a1)
	}
	if a2, _ := src.Get("a2"); a2.ConnPolicy != model.PolicyLimit || a2.ConnLimit != 3 || !a2.AllowGuest {
		t.Fatalf("显式字段应生效，实际 %+v", a2)
	}
	if _, ok := src.Get("nope"); ok {
		t.Fatal("未配置的 app 必须返回 false")
	}
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1","conn_policy":"bogus"}]}`)
	if _, err := LoadFile(p); err == nil {
		t.Fatal("非法策略必须在加载时报错，而不是握手时才发现")
	}
}

func TestWatchReloadsOnChangeAndKeepsOldOnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "apps.json")
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1"}]}`)
	src, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Watch(ctx, 20*time.Millisecond)

	time.Sleep(30 * time.Millisecond) // 让 mtime 变化可被观察到（文件系统时间精度）
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1"},{"app_id":"a2","app_secret":"s2"}]}`)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := src.Get("a2"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("文件变化 3 秒内没有被重载")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	write(t, p, `not json`)
	time.Sleep(200 * time.Millisecond)
	if _, ok := src.Get("a2"); !ok {
		t.Fatal("坏文件不能把已加载的配置清空，要保留上一份")
	}
}
```

这里的 `time.Sleep` 只用于制造 mtime 差异，不用于断言，断言仍是轮询。

- [ ] **Step 2: 实现 file.go**

```go
// Package appcfg 是 AppConfigSource 的 JSON 文件实现。
// fp 还没有配置中心，先从文件读；文件变化后重载，坏文件保留上一份。
package appcfg

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/basicfu/fp/internal/im/model"
)

type file struct {
	Apps []model.AppConfig `json:"apps"`
}

type Source struct {
	path  string
	mu    sync.RWMutex
	apps  map[string]model.AppConfig
	mtime time.Time
}

func LoadFile(path string) (*Source, error) {
	s := &Source{path: path}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload 读文件、填默认值、逐个校验；任何一个 app 非法整份拒绝。
func (s *Source) Reload() error {
	st, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("appcfg: %w", err)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("appcfg: %w", err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("appcfg: 解析 %s: %w", s.path, err)
	}
	apps := make(map[string]model.AppConfig, len(f.Apps))
	for _, a := range f.Apps {
		if a.ConnPolicy == "" {
			a.ConnPolicy = model.PolicyReplace
		}
		if a.ConnLimit == 0 {
			a.ConnLimit = 5
		}
		if a.GuestIPRate == 0 {
			a.GuestIPRate = 20
		}
		if err := a.Validate(); err != nil {
			return err
		}
		if _, dup := apps[a.AppID]; dup {
			return fmt.Errorf("appcfg: app_id %q 重复", a.AppID)
		}
		apps[a.AppID] = a
	}
	s.mu.Lock()
	s.apps, s.mtime = apps, st.ModTime()
	s.mu.Unlock()
	return nil
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

// Watch 每 interval 看一次 mtime，变了就 Reload。不用 fsnotify：多一个依赖换来的只是几秒的及时性。
func (s *Source) Watch(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st, err := os.Stat(s.path)
		if err != nil {
			continue
		}
		s.mu.RLock()
		changed := !st.ModTime().Equal(s.mtime)
		s.mu.RUnlock()
		if !changed {
			continue
		}
		if err := s.Reload(); err != nil {
			slog.Error("appcfg: 重载失败，保留上一份配置", "path", s.path, "err", err)
			s.mu.Lock()
			s.mtime = st.ModTime() // 记住这个坏版本的 mtime，避免每个周期重复报错
			s.mu.Unlock()
			continue
		}
		slog.Info("appcfg: 已重载", "path", s.path, "apps", len(s.Apps()))
	}
}
```

- [ ] **Step 3: fpauth 实现与测试**

`fpauth` 每个 app 一个 `fpsdk.Client`（fp 的 SDK 是单 app 的）。测试用 `sdk/client_test.go` 里同款的桩 gRPC 服务太重，这里只测两件不依赖 fp 的性质：未知 app 返回 `ErrUnauthorized`、`Verify` 把 `fpsdk.ErrUnauthorized`/`ErrUnavailable` 正确翻译。翻译函数拆出来单测。

`internal/im/fpauth/fpauth.go`：

```go
// Package fpauth 用 fpsdk 实现 auth.Authenticator。每个 app 一个 fpsdk.Client：
// fp 的 SDK 按 app 凭据建连，fp-im 服务多个 app 就得有多个。
// 这是 internal/im 里唯一允许 import github.com/basicfu/fp/sdk 的包（arch_test 断言）。
package fpauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
	fpsdk "github.com/basicfu/fp/sdk"
)

type Config struct {
	FPAddr   string
	Insecure bool
	Apps     auth.AppConfigSource
	// OnRevoke 在某 app 的 token 被撤销时调用，fp-im 用它关 ws。
	OnRevoke func(app string, tokens []string)
	Logger   *slog.Logger
}

type Authenticator struct {
	cfg     Config
	mu      sync.Mutex
	clients map[string]*fpsdk.Client
}

func New(cfg Config) (*Authenticator, error) {
	if cfg.FPAddr == "" || cfg.Apps == nil {
		return nil, errors.New("fpauth: FPAddr 与 Apps 必填")
	}
	return &Authenticator{cfg: cfg, clients: map[string]*fpsdk.Client{}}, nil
}

// client 按需为 app 建 fpsdk.Client。懒建而不是启动时全建：apps 文件会热重载。
func (a *Authenticator) client(app string) (*fpsdk.Client, error) {
	cfg, ok := a.cfg.Apps.Get(app)
	if !ok {
		return nil, auth.ErrUnauthorized
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.clients[app]; ok {
		return c, nil
	}
	c, err := fpsdk.New(fpsdk.Options{
		Addr: a.cfg.FPAddr, AppID: cfg.AppID, AppSecret: cfg.AppSecret, Insecure: a.cfg.Insecure, Logger: a.cfg.Logger,
		OnRevoke: func(ev fpsdk.RevokeEvent) {
			if a.cfg.OnRevoke != nil && len(ev.Tokens) > 0 {
				a.cfg.OnRevoke(app, ev.Tokens)
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("fpauth: 为 app %s 建 fp 客户端: %w", app, err)
	}
	a.clients[app] = c
	return c, nil
}

func (a *Authenticator) Verify(ctx context.Context, app, token string) (model.Subject, error) {
	c, err := a.client(app)
	if err != nil {
		return model.Subject{}, err
	}
	id, err := c.Auth().Validate(ctx, token)
	if err != nil {
		return model.Subject{}, translate(err)
	}
	return model.User(id.UserID), nil
}

// translate 把 fpsdk 的哨兵错误映射到 auth 的两个：握手代码只需要分"拒绝"和"稍后再试"。
func translate(err error) error {
	switch {
	case errors.Is(err, fpsdk.ErrNoToken), errors.Is(err, fpsdk.ErrUnauthorized):
		return auth.ErrUnauthorized
	case errors.Is(err, fpsdk.ErrUnavailable):
		return auth.ErrUnavailable
	}
	return fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
}

func (a *Authenticator) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var errs []error
	for app, c := range a.clients {
		if err := c.Close(); err != nil {
			errs = append(errs, fmt.Errorf("app %s: %w", app, err))
		}
	}
	a.clients = map[string]*fpsdk.Client{}
	return errors.Join(errs...)
}
```

`internal/im/fpauth/fpauth_test.go`：

```go
package fpauth

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
	fpsdk "github.com/basicfu/fp/sdk"
)

type apps map[string]model.AppConfig

func (a apps) Get(app string) (model.AppConfig, bool) { c, ok := a[app]; return c, ok }
func (a apps) Apps() []string                          { return nil }

func TestUnknownAppIsUnauthorized(t *testing.T) {
	a, err := New(Config{FPAddr: "127.0.0.1:1", Insecure: true, Apps: apps{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Verify(context.Background(), "nope", "tok"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("未配置的 app 应 ErrUnauthorized，实际 %v", err)
	}
}

func TestTranslate(t *testing.T) {
	for _, tc := range []struct{ in, want error }{
		{fpsdk.ErrUnauthorized, auth.ErrUnauthorized},
		{fpsdk.ErrNoToken, auth.ErrUnauthorized},
		{fpsdk.ErrUnavailable, auth.ErrUnavailable},
		{errors.New("something else"), auth.ErrUnavailable},
	} {
		if got := translate(tc.in); !errors.Is(got, tc.want) {
			t.Errorf("translate(%v)=%v，期望 %v", tc.in, got, tc.want)
		}
	}
}
```

- [ ] **Step 4: `internal/im/arch_test.go`**

复制 `internal/service/arch_test.go` 的 `archTestDir` 与 `walkNonTestGoFiles`（这两个 helper 在每个包里各存一份是有意的，仓库没有共享测试包），然后：

```go
package im_test

// archTestDir / walkNonTestGoFiles 与 internal/service/arch_test.go 相同，此处省略，实现时整段复制。

func TestImImportRules(t *testing.T) {
	root := archTestDir(t)
	forbidden := []struct{ prefix, reason string }{
		{"github.com/basicfu/fp/internal/store", "fp-im 没有数据库，不能借用 fp 的存储层"},
		{"github.com/basicfu/fp/internal/service", "fp-im 不理解 fp 的业务"},
		{"github.com/basicfu/fp/internal/domain", "fp-im 有自己的 model，不共享 fp 的领域类型"},
		{"github.com/basicfu/fp/internal/httpapi", "两个服务的传输层互不可见"},
		{"github.com/basicfu/fp/internal/grpcapi", "两个服务的传输层互不可见"},
		{"github.com/basicfu/fp/internal/connector", "登录方式是 fp 的事"},
		{"github.com/basicfu/fp/internal/notify", "fp-im 不发通知"},
	}
	walkNonTestGoFiles(t, root, func(path string, file *ast.File, _ *token.FileSet) {
		inFpauth := strings.Contains(filepath.ToSlash(path), "/internal/im/fpauth/")
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, f := range forbidden {
				if p == f.prefix || strings.HasPrefix(p, f.prefix+"/") {
					t.Errorf("%s imports %q：%s", path, p, f.reason)
				}
			}
			// 只有 fpauth 可以碰 fpsdk 的手写部分；生成代码 sdk/gen 谁都可以用
			if (p == "github.com/basicfu/fp/sdk" || strings.HasPrefix(p, "github.com/basicfu/fp/sdk/") && !strings.HasPrefix(p, "github.com/basicfu/fp/sdk/gen")) && !inFpauth {
				t.Errorf("%s imports %q：fp-im 只能通过 auth.Authenticator / auth.AppConfigSource 接口触达 fpsdk，实现只允许在 internal/im/fpauth", path, p)
			}
		}
	})
}
```

- [ ] **Step 5: 跑测试**

Run: `./scripts/test.sh ./internal/im/... -v`
Expected: 全部 PASS

- [ ] **Step 6: 提交**

```bash
git add internal/im/appcfg internal/im/fpauth internal/im/arch_test.go
git commit -m "feat(im): 文件版 AppConfigSource、fpsdk 适配的 Authenticator、import 规则断言"
```

---

## Task 11: `internal/im/wsapi`：ws 握手、读写循环、访客限流

先把 Task 7 的假实现搬进可复用的 `hubtest` 包（wsapi、imgrpc、integration 三处测试都要用），再写 wsapi。本任务引入 `github.com/coder/websocket`。

**Files:**
- Create: `internal/im/hub/hubtest/fakes.go`（从 `hub_test.go` 搬出并导出）
- Modify: `internal/im/hub/hub_test.go`、`push_test.go`、`deliver_test.go`（改用 hubtest）
- Create: `internal/im/wsapi/handler.go`、`conn.go`、`guestlimit.go`
- Test: `internal/im/wsapi/handler_test.go`、`guestlimit_test.go`
- Modify: `internal/integration/dependency_whitelist_test.go`（登记 websocket）、`go.mod`

**Interfaces:**
- Consumes: `hub.Hub`（`AddConn/RemoveConn/Evict/Deliver`）、`auth.Authenticator`、`auth.AppConfigSource`、`registry.HandshakeResult/ConnRef`、`model.*`。
- Produces: `wsapi.New(Deps) http.Handler`；`Deps{Hub *hub.Hub; Conns Handshaker; Live Liveness; Auth auth.Authenticator; Apps auth.AppConfigSource; Guests *GuestLimiter; Cfg Config}`；接口 `Handshaker{Handshake(...); Remove(...)}`（`registry.Conns` 满足）、`Liveness{NodeID() string; LiveNodes() []string}`（`registry.Liveness` 满足）；`Config{AuthTimeout, IdleTimeout time.Duration; SendQueue int; MaxFrame int64; TrustProxy bool}`；`NewGuestLimiter()`、`(*GuestLimiter).Allow(app, ip, guestID string, rate int, now time.Time) bool`、`Sweep(now)`。

- [ ] **Step 1: 建 hubtest 包**

`internal/im/hub/hubtest/fakes.go`：把 Task 7 `hub_test.go` 里的 `fakeConn/fakeStream/fakeReg/fakeLive/fakePub/fakeApps` 原样搬来，改名导出为 `Conn/Stream/Reg/Live/Pub/Apps`，字段导出（`Sent`、`Closed`、`Full`、`Got`、`Fail`、`Lookup`、`Nodes`、`Servers`, `Serving`、`Published`），并加：

```go
// Package hubtest 提供 hub 三个依赖接口与 Conn/Stream 的假实现，给 hub、wsapi、imgrpc、integration 的测试共用。
package hubtest

// NewHub 用假实现装一个 nodeID 为 im-a、a1 策略为 replace 的 Hub。
func NewHub() (*hub.Hub, *Reg, *Live, *Pub, Apps) {
	reg := &Reg{Lookup: map[string]map[string]model.ConnMeta{}}
	live := &Live{Nodes: []string{"im-a", "im-b", "im-c"}, Servers: map[string][]string{}}
	pub := &Pub{}
	apps := Apps{"a1": {AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace}}
	return hub.New("im-a", reg, live, pub, apps), reg, live, pub, apps
}
```

`Closed` 的类型改为导出结构 `type Closure struct{ Code int; Reason string }`，字段 `Closed *Closure`；`Pub.Published []Published`，`type Published struct{ Node string; Env bus.Envelope }`。hub 的三个测试文件把 `newHub(t)` 换成 `hubtest.NewHub()`，字段名对应改为导出名。跑 `./scripts/test.sh ./internal/im/hub -v` 全绿后再往下。

- [ ] **Step 2: 加依赖并登记白名单**

```bash
go get github.com/coder/websocket@v1.8.13
```

`internal/integration/dependency_whitelist_test.go` 的 `allowedDirectDependencies` 加一行：

```go
	"github.com/coder/websocket", // fp-im 的 ws 传输；纯 Go、无间接依赖、ctx 驱动的 API
```

Run: `./scripts/test.sh ./internal/integration -run TestGoModDirectDependenciesAreWhitelisted -v` → PASS。

- [ ] **Step 3: 访客限流测试与实现**

`guestlimit_test.go`：

```go
package wsapi

import (
	"testing"
	"time"
)

func TestGuestLimiterCountsDistinctIDsPerIPPerMinute(t *testing.T) {
	g := NewGuestLimiter()
	now := time.UnixMilli(0)
	for i := 0; i < 3; i++ {
		if !g.Allow("a1", "1.2.3.4", "g"+string(rune('a'+i)), 3, now) {
			t.Fatalf("前 3 个新访客应放行，第 %d 个被拒", i+1)
		}
	}
	if g.Allow("a1", "1.2.3.4", "gz", 3, now) {
		t.Fatal("第 4 个新访客应被拒")
	}
	if !g.Allow("a1", "1.2.3.4", "ga", 3, now) {
		t.Fatal("已见过的访客 id 重连不占额度")
	}
	if !g.Allow("a1", "5.6.7.8", "gz", 3, now) {
		t.Fatal("额度按 IP 分，另一个 IP 不受影响")
	}
	if !g.Allow("a1", "1.2.3.4", "gz", 3, now.Add(61*time.Second)) {
		t.Fatal("一分钟窗口过后额度应重置")
	}
}
```

`guestlimit.go`：

```go
package wsapi

import (
	"sync"
	"time"
)

// GuestLimiter 限制每个 (app, IP) 每分钟能带来多少个"新"访客 id。
// 这是 im 对自生成访客 id 的唯一防线：id 免费无限造，只能限制造它的人。
type GuestLimiter struct {
	mu      sync.Mutex
	buckets map[string]*guestBucket
}

type guestBucket struct {
	start time.Time
	ids   map[string]struct{}
}

func NewGuestLimiter() *GuestLimiter { return &GuestLimiter{buckets: map[string]*guestBucket{}} }

func (g *GuestLimiter) Allow(app, ip, guestID string, rate int, now time.Time) bool {
	k := app + "\x00" + ip
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.buckets[k]
	if b == nil || now.Sub(b.start) >= time.Minute {
		b = &guestBucket{start: now, ids: map[string]struct{}{}}
		g.buckets[k] = b
	}
	if _, seen := b.ids[guestID]; seen {
		return true
	}
	if len(b.ids) >= rate {
		return false
	}
	b.ids[guestID] = struct{}{}
	return true
}

// Sweep 清掉过期桶，cmd 每分钟调一次，否则 IP 数量无上限。
func (g *GuestLimiter) Sweep(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, b := range g.buckets {
		if now.Sub(b.start) >= time.Minute {
			delete(g.buckets, k)
		}
	}
}
```

- [ ] **Step 4: conn.go**

```go
package wsapi

import (
	"context"
	"sync"

	"github.com/coder/websocket"
)

// wsConn 实现 hub.Conn。Send 非阻塞地进发送队列；队列满返回 false，由 hub 决定关闭。
// Close 只记录意图并唤醒写协程，真正的 ws.Close 由写协程做，避免两个协程同时写同一条 ws。
type wsConn struct {
	id, token string
	ws        *websocket.Conn
	sendq     chan []byte

	once   sync.Once
	closed chan struct{}
	code   int
	reason string
}

func newWsConn(id, token string, ws *websocket.Conn, queue int) *wsConn {
	return &wsConn{id: id, token: token, ws: ws, sendq: make(chan []byte, queue), closed: make(chan struct{})}
}

func (c *wsConn) ID() string    { return c.id }
func (c *wsConn) Token() string { return c.token }

func (c *wsConn) Send(p []byte) bool {
	select {
	case c.sendq <- p:
		return true
	default:
		return false
	}
}

func (c *wsConn) Close(code int, reason string) {
	c.once.Do(func() {
		c.code, c.reason = code, reason
		close(c.closed)
	})
}

// closure 返回 Close 记录的关闭码与原因；未被主动关闭时 ok=false。
func (c *wsConn) closure() (code int, reason string, ok bool) {
	select {
	case <-c.closed:
		return c.code, c.reason, true
	default:
		return 0, "", false
	}
}

// writeLoop 把队列里的 payload 包成 msg 帧写出去，直到被关闭或写失败。
func (c *wsConn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case p := <-c.sendq:
			if err := c.ws.Write(ctx, websocket.MessageText, p); err != nil {
				return
			}
		}
	}
}

// msgFrame 手拼 {"t":"msg","p":<payload>}：payload 已是 JSON，走 json.Marshal 会多一次拷贝与转义。
func msgFrame(payload []byte) []byte {
	b := make([]byte, 0, len(payload)+16)
	b = append(b, `{"t":"msg","p":`...)
	b = append(b, payload...)
	return append(b, '}')
}

var pongFrame = []byte(`{"t":"pong"}`)
```

- [ ] **Step 5: handler.go**

```go
// Package wsapi 是 client 一侧的传输层：ws 升级、握手、读写循环、连接生命周期。
package wsapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type Handshaker interface {
	Handshake(ctx context.Context, app, subject, connID string, meta model.ConnMeta, policy model.Policy, limit int, live []string) (registry.HandshakeResult, error)
	Remove(ctx context.Context, app, subject, connID string) error
}

type Liveness interface {
	NodeID() string
	LiveNodes() []string
}

type Config struct {
	AuthTimeout time.Duration // 连接后等第一帧的时间
	IdleTimeout time.Duration // 无任何帧则关闭
	SendQueue   int
	MaxFrame    int64
	TrustProxy  bool // 是否信任 X-Forwarded-For 的第一跳
}

type Deps struct {
	Hub    *hub.Hub
	Conns  Handshaker
	Live   Liveness
	Auth   auth.Authenticator
	Apps   auth.AppConfigSource
	Guests *GuestLimiter
	Cfg    Config
}

type server struct{ Deps }

func New(d Deps) http.Handler {
	if d.Cfg.MaxFrame == 0 {
		d.Cfg.MaxFrame = 1 << 20
	}
	return &server{Deps: d}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 不校验 Origin：身份靠第一帧里的 token，不靠 cookie，跨站 ws 拿不到 token 也就没有 CSRF 面。
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(s.Cfg.MaxFrame)
	ctx := r.Context()

	af, ok := s.readAuthFrame(ctx, ws)
	if !ok {
		_ = ws.Close(model.CloseAuthFailed, "auth frame")
		return
	}
	appCfg, ok := s.Apps.Get(af.App)
	if !ok {
		_ = ws.Close(model.CloseAuthFailed, "unknown app")
		return
	}
	sub, code := s.resolveSubject(ctx, af, appCfg, clientIP(r, s.Cfg.TrustProxy))
	if code != 0 {
		_ = ws.Close(websocket.StatusCode(code), "auth")
		return
	}
	osName, mobile := af.OS, af.Mobile != nil && *af.Mobile
	if osName == "" {
		osName, mobile = model.ParseUA(af.UA)
	}
	connID, err := uuid.NewV7()
	if err != nil {
		_ = ws.Close(model.CloseUnavailable, "id")
		return
	}
	meta := model.ConnMeta{Node: s.Live.NodeID(), OS: osName, Mobile: mobile, At: time.Now().UnixMilli()}
	res, err := s.Conns.Handshake(ctx, af.App, sub.String(), connID.String(), meta, appCfg.ConnPolicy, appCfg.ConnLimit, s.Live.LiveNodes())
	if err != nil {
		slog.Warn("wsapi: 握手脚本失败", "app", af.App, "err", err)
		_ = ws.Close(model.CloseUnavailable, "registry")
		return
	}
	if res.Rejected {
		_ = ws.Close(model.ClosePolicyRejected, "policy")
		return
	}
	s.Hub.Evict(ctx, af.App, sub, res.Kicked, model.ReasonReplaced)

	c := newWsConn(connID.String(), af.Token, ws, s.Cfg.SendQueue)
	s.Hub.AddConn(ctx, af.App, sub, c, meta, af.UA)
	hello, _ := json.Marshal(model.Frame{T: model.FrameHello, Conn: c.id})
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		s.teardown(af.App, sub, c, model.ReasonClient)
		return
	}
	wctx, cancelWrite := context.WithCancel(ctx)
	go c.writeLoop(wctx)

	reason := s.readLoop(ctx, af.App, sub, c)
	cancelWrite()
	s.teardown(af.App, sub, c, reason)
}

func (s *server) readAuthFrame(ctx context.Context, ws *websocket.Conn) (model.AuthFrame, bool) {
	actx, cancel := context.WithTimeout(ctx, s.Cfg.AuthTimeout)
	defer cancel()
	_, data, err := ws.Read(actx)
	if err != nil {
		return model.AuthFrame{}, false
	}
	var af model.AuthFrame
	if json.Unmarshal(data, &af) != nil || af.T != model.FrameAuth || af.App == "" {
		return model.AuthFrame{}, false
	}
	return af, true
}

// resolveSubject 返回 subject，或非零的关闭码。
func (s *server) resolveSubject(ctx context.Context, af model.AuthFrame, cfg model.AppConfig, ip string) (model.Subject, int) {
	switch {
	case af.Token != "":
		sub, err := s.Auth.Verify(ctx, af.App, af.Token)
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

// readLoop 读到连接结束，返回断开原因。
func (s *server) readLoop(ctx context.Context, app string, sub model.Subject, c *wsConn) string {
	for {
		rctx, cancel := context.WithTimeout(ctx, s.Cfg.IdleTimeout)
		_, data, err := c.ws.Read(rctx)
		cancel()
		if err != nil {
			if _, reason, ok := c.closure(); ok {
				return reason // 被 hub 主动关闭（踢、撤销、背压）
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return model.ReasonTimeout
			}
			return model.ReasonClient
		}
		var f model.Frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch f.T {
		case model.FrameMsg:
			if len(f.P) == 0 {
				continue
			}
			s.Hub.Deliver(ctx, bus.Envelope{Type: bus.TypeUp, App: app, Subject: sub.String(), ConnID: c.id, Payload: []byte(f.P)})
		case model.FramePing:
			c.Send(pongFrame)
		}
	}
}

// teardown 的顺序：先从注册表删（别的节点尽早停止喊我），再从 hub 删并发事件，最后关 ws。
func (s *server) teardown(app string, sub model.Subject, c *wsConn, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Conns.Remove(ctx, app, sub.String(), c.id); err != nil {
		slog.Warn("wsapi: 注册表删除失败", "app", app, "conn", c.id, "err", err)
	}
	s.Hub.RemoveConn(ctx, app, sub, c.id, reason)
	code, _, ok := c.closure()
	if !ok {
		code = websocket.StatusNormalClosure.Int()
	}
	_ = c.ws.Close(websocket.StatusCode(code), reason)
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
```

`websocket.StatusCode` 是 `int` 的命名类型，`StatusNormalClosure.Int()` 若该版本没有 `Int()` 方法，改为 `int(websocket.StatusNormalClosure)`。

- [ ] **Step 6: handler 测试**

```go
package wsapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"github.com/coder/websocket"
)

type fakeAuth map[string]model.Subject

func (a fakeAuth) Verify(_ context.Context, _, token string) (model.Subject, error) {
	if s, ok := a[token]; ok {
		return s, nil
	}
	return model.Subject{}, auth.ErrUnauthorized
}

type fakeHandshaker struct {
	result  registry.HandshakeResult
	removed []string
}

func (f *fakeHandshaker) Handshake(context.Context, string, string, string, model.ConnMeta, model.Policy, int, []string) (registry.HandshakeResult, error) {
	return f.result, nil
}
func (f *fakeHandshaker) Remove(_ context.Context, _, _, connID string) error {
	f.removed = append(f.removed, connID)
	return nil
}

type fakeLiveness struct{}

func (fakeLiveness) NodeID() string       { return "im-a" }
func (fakeLiveness) LiveNodes() []string  { return []string{"im-a"} }

type env struct {
	srv    *httptest.Server
	h      *hub.Hub
	stream *hubtest.Stream
	hs     *fakeHandshaker
	apps   hubtest.Apps
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	h, _, _, _, apps := hubtest.NewHub()
	stream := &hubtest.Stream{}
	h.AddStream(context.Background(), "a1", stream)
	hs := &fakeHandshaker{}
	if cfg.AuthTimeout == 0 {
		cfg.AuthTimeout = 2 * time.Second
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 10 * time.Second
	}
	if cfg.SendQueue == 0 {
		cfg.SendQueue = 8
	}
	handler := New(Deps{Hub: h, Conns: hs, Live: fakeLiveness{}, Auth: fakeAuth{"tok-1": model.User("1")}, Apps: apps, Guests: NewGuestLimiter(), Cfg: cfg})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &env{srv: srv, h: h, stream: stream, hs: hs, apps: apps}
}

func (e *env) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, c *websocket.Conn) (model.Frame, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		return model.Frame{}, err
	}
	var f model.Frame
	_ = json.Unmarshal(b, &f)
	return f, nil
}

func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHandshakeWithTokenThenMessageAndPing(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "tok-1", "ua": "Mozilla/5.0 (iPhone)"})
	hello, err := read(t, c)
	if err != nil || hello.T != "hello" || hello.Conn == "" {
		t.Fatalf("应收到 hello 帧，实际 %+v %v", hello, err)
	}
	waitUntil(t, func() bool { return len(e.stream.Got) >= 1 }, "server 流应收到 Connected")
	ev := e.stream.Got[0].GetEvent()
	if ev == nil || ev.Kind != fpimv1.EventKind_EVENT_KIND_CONNECTED || ev.Subject != "u:1" || ev.Os != "ios" || !ev.Mobile || ev.Ua == "" {
		t.Fatalf("Connected 事件不对：%+v", ev)
	}

	send(t, c, map[string]any{"t": "msg", "p": map[string]any{"x": 1}})
	waitUntil(t, func() bool { return len(e.stream.Got) >= 2 }, "server 流应收到 Inbound")
	in := e.stream.Got[1].GetInbound()
	if in == nil || in.Subject != "u:1" || in.ConnId != hello.Conn || string(in.Payload) != `{"x":1}` {
		t.Fatalf("Inbound 不对：%+v", in)
	}

	send(t, c, map[string]any{"t": "ping"})
	if f, _ := read(t, c); f.T != "pong" {
		t.Fatalf("ping 应回 pong，实际 %+v", f)
	}
}

func TestNoAuthFrameCloses4001(t *testing.T) {
	e := newEnv(t, Config{AuthTimeout: 200 * time.Millisecond})
	c := e.dial(t)
	defer c.CloseNow()
	_, err := read(t, c)
	if websocket.CloseStatus(err) != model.CloseAuthFailed {
		t.Fatalf("超时不发握手帧应 4001，实际 %v", err)
	}
}

func TestBadTokenCloses4001AndGuestRules(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "bad"})
	if _, err := read(t, c); websocket.CloseStatus(err) != model.CloseAuthFailed {
		t.Fatalf("坏 token 应 4001，实际 %v", err)
	}
	c.CloseNow()

	// a1 不允许访客
	c = e.dial(t)
	send(t, c, map[string]any{"t": "auth", "app": "a1", "guest": "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"})
	if _, err := read(t, c); websocket.CloseStatus(err) != model.CloseAuthFailed {
		t.Fatalf("app 未开访客应 4001，实际 %v", err)
	}
	c.CloseNow()

	e.apps["a1"] = model.AppConfig{AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace, AllowGuest: true, GuestIPRate: 20}
	c = e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": "auth", "app": "a1", "guest": "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"})
	if f, err := read(t, c); err != nil || f.T != "hello" {
		t.Fatalf("开了访客应 hello，实际 %+v %v", f, err)
	}
	waitUntil(t, func() bool { return len(e.stream.Got) >= 1 && e.stream.Got[0].GetEvent().Subject == "g:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f" }, "访客的 Connected 应带 g: 前缀")
}

func TestPolicyRejectCloses4002(t *testing.T) {
	e := newEnv(t, Config{})
	e.hs.result = registry.HandshakeResult{Rejected: true}
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "tok-1"})
	if _, err := read(t, c); websocket.CloseStatus(err) != model.ClosePolicyRejected {
		t.Fatalf("策略拒绝应 4002，实际 %v", err)
	}
}

func TestKickCloses4003AndEmitsDisconnected(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "tok-1"})
	hello, _ := read(t, c)
	e.h.Evict(context.Background(), "a1", model.User("1"), []registry.ConnRef{{ConnID: hello.Conn, Node: "im-a"}}, model.ReasonReplaced)
	if _, err := read(t, c); websocket.CloseStatus(err) != model.CloseKicked {
		t.Fatalf("被顶替应 4003，实际 %v", err)
	}
	waitUntil(t, func() bool {
		for _, g := range e.stream.Got {
			if ev := g.GetEvent(); ev != nil && ev.Kind == fpimv1.EventKind_EVENT_KIND_DISCONNECTED {
				return ev.Reason == model.ReasonReplaced
			}
		}
		return false
	}, "应发 Disconnected(replaced)")
	if len(e.hs.removed) != 1 || e.hs.removed[0] != hello.Conn {
		t.Fatalf("断开时必须从注册表删自己，实际 %v", e.hs.removed)
	}
}

func TestIdleTimeoutDisconnects(t *testing.T) {
	e := newEnv(t, Config{IdleTimeout: 300 * time.Millisecond})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "tok-1"})
	_, _ = read(t, c)
	if _, err := read(t, c); err == nil {
		t.Fatal("空闲超时应关闭连接")
	}
	waitUntil(t, func() bool {
		for _, g := range e.stream.Got {
			if ev := g.GetEvent(); ev != nil && ev.Kind == fpimv1.EventKind_EVENT_KIND_DISCONNECTED {
				return ev.Reason == model.ReasonTimeout
			}
		}
		return false
	}, "应发 Disconnected(timeout)")
}
```

- [ ] **Step 7: 跑测试**

Run: `./scripts/test.sh ./internal/im/... -v`
Expected: 全部 PASS

- [ ] **Step 8: 提交**

```bash
git add go.mod go.sum internal/im/hub internal/im/wsapi internal/integration/dependency_whitelist_test.go
git commit -m "feat(im): wsapi 握手、读写循环与访客限流；hubtest 共享假实现"
```

---

## Task 12: `internal/im/imgrpc`：ImService 服务端

**Files:**
- Create: `internal/im/imgrpc/server.go`、`stream.go`、`appauth.go`
- Test: `internal/im/imgrpc/server_test.go`

**Interfaces:**
- Consumes: `hub.Hub`（`AddStream/Push/PushMany/Sessions/Kick/NodeID`）、`auth.AppConfigSource`、`fpimv1`。
- Produces: `imgrpc.New(Deps{Hub *hub.Hub; Apps auth.AppConfigSource; Workers int}) *Server`；`(*Server).Serve(lis net.Listener) error`；`(*Server).Stop(ctx)`；常量 `KeepaliveMinTime = 10 * time.Second`、`MDAppID = "fp-app-id"`、`MDAppSecret = "fp-app-secret"`。

- [ ] **Step 1: appauth.go**

```go
package imgrpc

import (
	"context"
	"crypto/subtle"

	"github.com/basicfu/fp/internal/im/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// 与 fp 的 grpcapi 使用相同的 metadata 键，业务方的 SDK 配置同一对 app_id/app_secret 就能同时连 fp 和 fp-im。
const (
	MDAppID     = "fp-app-id"
	MDAppSecret = "fp-app-secret"
)

type appCtxKey struct{}

func appFrom(ctx context.Context) string {
	v, _ := ctx.Value(appCtxKey{}).(string)
	return v
}

// streamAuth 用常量时间比较校验凭据。fp-im 没有数据库也不做 bcrypt：apps 文件里就是明文 secret。
func streamAuth(apps auth.AppConfigSource) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		md, _ := metadata.FromIncomingContext(ss.Context())
		id, secret := first(md, MDAppID), first(md, MDAppSecret)
		cfg, ok := apps.Get(id)
		if !ok || subtle.ConstantTimeCompare([]byte(cfg.AppSecret), []byte(secret)) != 1 {
			return status.Error(codes.Unauthenticated, "应用凭据无效")
		}
		return handler(srv, &wrapped{ServerStream: ss, ctx: context.WithValue(ss.Context(), appCtxKey{}, id)})
	}
}

func first(md metadata.MD, k string) string {
	if v := md.Get(k); len(v) > 0 {
		return v[0]
	}
	return ""
}

type wrapped struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrapped) Context() context.Context { return w.ctx }
```

- [ ] **Step 2: stream.go**

```go
package imgrpc

import (
	"context"
	"sync"

	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/model"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
)

// sender 给 gRPC 流加锁：gRPC 不允许并发 Send，而 hub 会从多个协程往同一条流写。
type sender struct {
	mu sync.Mutex
	s  fpimv1.ImService_ConnectServer
}

func (s *sender) Send(r *fpimv1.ConnectResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s.Send(r)
}

// handle 执行一条请求并生成 Result。subject 非法或 body 未知都变成 Result.Error，不断流。
func handle(ctx context.Context, h *hub.Hub, app string, req *fpimv1.ConnectRequest) *fpimv1.ConnectResponse {
	res := &fpimv1.Result{ReqId: req.GetReqId()}
	fail := func(msg string) *fpimv1.ConnectResponse {
		res.Error = msg
		return &fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Result{Result: res}}
	}
	switch b := req.GetBody().(type) {
	case *fpimv1.ConnectRequest_Push:
		sub, err := model.ParseSubject(b.Push.GetSubject())
		if err != nil {
			return fail(err.Error())
		}
		res.Pushes = []*fpimv1.PushResult{toProto(h.Push(ctx, app, sub, b.Push.GetPayload()))}
	case *fpimv1.ConnectRequest_PushMany:
		subs := make([]model.Subject, 0, len(b.PushMany.GetSubjects()))
		for _, s := range b.PushMany.GetSubjects() {
			sub, err := model.ParseSubject(s)
			if err != nil {
				return fail(err.Error())
			}
			subs = append(subs, sub)
		}
		for _, r := range h.PushMany(ctx, app, subs, b.PushMany.GetPayload()) {
			res.Pushes = append(res.Pushes, toProto(r))
		}
	case *fpimv1.ConnectRequest_Kick:
		sub, err := model.ParseSubject(b.Kick.GetSubject())
		if err != nil {
			return fail(err.Error())
		}
		if err := h.Kick(ctx, app, sub, b.Kick.GetConnIds()...); err != nil {
			return fail(err.Error())
		}
	case *fpimv1.ConnectRequest_Sessions:
		sub, err := model.ParseSubject(b.Sessions.GetSubject())
		if err != nil {
			return fail(err.Error())
		}
		list, err := h.Sessions(ctx, app, sub)
		if err != nil {
			return fail(err.Error())
		}
		for _, s := range list {
			res.Sessions = append(res.Sessions, &fpimv1.Session{ConnId: s.ConnID, NodeId: s.Node, Os: s.OS, Mobile: s.Mobile, ConnectedAtMs: s.ConnectedAt})
		}
	default:
		return fail("未知请求类型")
	}
	return &fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Result{Result: res}}
}

func toProto(r hub.PushResult) *fpimv1.PushResult {
	st := fpimv1.PushStatus_PUSH_STATUS_UNAVAILABLE
	switch r.Status {
	case hub.PushSent:
		st = fpimv1.PushStatus_PUSH_STATUS_SENT
	case hub.PushNotOnline:
		st = fpimv1.PushStatus_PUSH_STATUS_NOT_ONLINE
	}
	return &fpimv1.PushResult{Subject: r.Subject, Status: st, Nodes: int32(r.Nodes)}
}
```

- [ ] **Step 3: server.go**

```go
// Package imgrpc 是业务 server 一侧的传输层：ImService.Connect 双向流。
package imgrpc

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/hub"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// KeepaliveMinTime 与 fpim.KeepaliveTime(30s) 配对：客户端 ping 间隔必须大于这里，否则被 GOAWAY。
// 配对关系由 internal/integration/im_parity_test.go 守着。
const KeepaliveMinTime = 10 * time.Second

type Deps struct {
	Hub  *hub.Hub
	Apps auth.AppConfigSource
	// Workers 是每条流并发处理请求的上限。Push 会等 Redis，串行会让一条慢请求拖住整条流。
	Workers int
}

type Server struct {
	fpimv1.UnimplementedImServiceServer
	deps Deps
	grpc *grpc.Server
}

func New(d Deps) *Server {
	if d.Workers <= 0 {
		d.Workers = 64
	}
	s := &Server{deps: d}
	s.grpc = grpc.NewServer(
		grpc.StreamInterceptor(streamAuth(d.Apps)),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: KeepaliveMinTime, PermitWithoutStream: true}),
	)
	fpimv1.RegisterImServiceServer(s.grpc, s)
	return s
}

func (s *Server) Serve(lis net.Listener) error { return s.grpc.Serve(lis) }

// Stop 先优雅停，超时就硬停。
func (s *Server) Stop(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.grpc.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		s.grpc.Stop()
	}
}

func (s *Server) Connect(stream fpimv1.ImService_ConnectServer) error {
	ctx := stream.Context()
	app := appFrom(ctx)
	snd := &sender{s: stream}
	remove := s.deps.Hub.AddStream(ctx, app, snd)
	defer remove()
	if err := snd.Send(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Ready{Ready: &fpimv1.Ready{NodeId: s.deps.Hub.NodeID()}}}); err != nil {
		return err
	}
	sem := make(chan struct{}, s.deps.Workers)
	for {
		req, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		sem <- struct{}{}
		go func(req *fpimv1.ConnectRequest) {
			defer func() { <-sem }()
			_ = snd.Send(handle(ctx, s.deps.Hub, app, req))
		}(req)
	}
}
```

- [ ] **Step 4: 测试（bufconn）**

```go
package imgrpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func newClient(t *testing.T) (fpimv1.ImServiceClient, *hubtest.Reg) {
	t.Helper()
	h, reg, _, _, apps := hubtest.NewHub()
	srv := New(Deps{Hub: h, Apps: apps})
	lis := bufconn.Listen(1 << 20)
	go srv.Serve(lis)
	t.Cleanup(func() { srv.Stop(context.Background()) })
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return fpimv1.NewImServiceClient(cc), reg
}

func authed(ctx context.Context, id, secret string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, MDAppID, id, MDAppSecret, secret)
}

func TestConnectRejectsBadCredentials(t *testing.T) {
	c, _ := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := c.Connect(authed(ctx, "a1", "wrong"))
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("错误凭据应 Unauthenticated，实际 %v", err)
	}
}

func TestConnectReadyThenPushAndSessions(t *testing.T) {
	c, reg := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := c.Connect(authed(ctx, "a1", "s"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetReady() == nil || first.GetReady().NodeId != "im-a" {
		t.Fatalf("第一帧应是 Ready{im-a}，实际 %+v %v", first, err)
	}
	reg.Lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b", OS: "mac"}}
	_ = stream.Send(&fpimv1.ConnectRequest{ReqId: "r1", Body: &fpimv1.ConnectRequest_Push{Push: &fpimv1.PushRequest{Subject: "u:1", Payload: []byte(`1`)}}})
	_ = stream.Send(&fpimv1.ConnectRequest{ReqId: "r2", Body: &fpimv1.ConnectRequest_Sessions{Sessions: &fpimv1.SessionsRequest{Subject: "u:1"}}})
	_ = stream.Send(&fpimv1.ConnectRequest{ReqId: "r3", Body: &fpimv1.ConnectRequest_Push{Push: &fpimv1.PushRequest{Subject: "bogus", Payload: []byte(`1`)}}})
	got := map[string]*fpimv1.Result{}
	for len(got) < 3 {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.GetResult(); r != nil {
			got[r.ReqId] = r
		}
	}
	if got["r1"].Pushes[0].Status != fpimv1.PushStatus_PUSH_STATUS_SENT || got["r1"].Pushes[0].Nodes != 1 {
		t.Fatalf("r1 应 Sent{1}，实际 %+v", got["r1"])
	}
	if len(got["r2"].Sessions) != 1 || got["r2"].Sessions[0].NodeId != "im-b" || got["r2"].Sessions[0].Os != "mac" {
		t.Fatalf("r2 Sessions 不对：%+v", got["r2"])
	}
	if got["r3"].Error == "" {
		t.Fatal("非法 subject 应以 Result.Error 返回而不是断流")
	}
}
```

- [ ] **Step 5: 跑测试**

Run: `./scripts/test.sh ./internal/im/imgrpc -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/im/imgrpc
git commit -m "feat(im): imgrpc ImService 双向流、凭据拦截器与请求分发"
```

---

## Task 13: `internal/im/config` 与 `cmd/fp-im` 装配

**Files:**
- Create: `internal/im/config/config.go`、`cmd/fp-im/main.go`、`scripts/run-im.sh`
- Modify: `.env.example`、`scripts/env.sh`
- Test: `internal/im/config/config_test.go`

**Interfaces:**
- Consumes: 前面所有包。
- Produces: `config.Config` 与 `config.Load() (*Config, error)`；可运行的 `fp-im` 二进制。

- [ ] **Step 1: config 测试**

```go
package config

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndRequired(t *testing.T) {
	t.Setenv("FP_IM_REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("FP_IM_FP_ADDR", "localhost:9090")
	t.Setenv("FP_IM_APPS_FILE", "apps.json")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8081" || cfg.GRPCAddr != ":9091" || cfg.Node.Heartbeat != 3*time.Second || cfg.Node.DeadAfter != 10*time.Second ||
		cfg.Conn.FieldTTL != 30*time.Minute || cfg.Conn.FieldRenew != 10*time.Minute || cfg.Conn.IdleTimeout != 60*time.Second ||
		cfg.Conn.AuthTimeout != 5*time.Second || cfg.Conn.SendQueue != 256 || cfg.Pipeline.FlushInterval != 0 || cfg.Pipeline.FlushSize != 1 {
		t.Fatalf("默认值与 spec 第十二节不符：%+v", cfg)
	}
	t.Setenv("FP_IM_REDIS_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("缺少必填变量必须报错，不能静默用默认值")
	}
}

func TestLoadRejectsBadPipelineAndRenew(t *testing.T) {
	t.Setenv("FP_IM_REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("FP_IM_FP_ADDR", "localhost:9090")
	t.Setenv("FP_IM_APPS_FILE", "apps.json")
	t.Setenv("FP_IM_PIPELINE_FLUSH_SIZE", "64")
	if _, err := Load(); err == nil {
		t.Fatal("flush_size>1 且无 flush_interval 必须报错")
	}
	t.Setenv("FP_IM_PIPELINE_FLUSH_SIZE", "1")
	t.Setenv("FP_IM_CONN_FIELD_RENEW", "20m")
	if _, err := Load(); err == nil {
		t.Fatal("field_renew 必须小于 field_ttl 的一半，否则续期赶不上过期")
	}
}
```

- [ ] **Step 2: 实现 config.go**

```go
// Package config 读 fp-im 的环境变量。风格与 internal/config 一致：FP_IM_ 前缀，必填项缺失直接报错。
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env        string
	HTTPAddr   string
	GRPCAddr   string
	RedisURL   string
	FPAddr     string
	FPInsecure bool
	AppsFile   string
	LogLevel   string
	TrustProxy bool
	Node       struct{ Heartbeat, DeadAfter time.Duration }
	Conn       struct {
		FieldTTL, FieldRenew, IdleTimeout, AuthTimeout time.Duration
		SendQueue                                       int
	}
	Pipeline struct {
		FlushInterval time.Duration
		FlushSize     int
	}
}

func Load() (*Config, error) {
	c := &Config{
		Env:        envOr("FP_IM_ENV", "DEV"),
		HTTPAddr:   envOr("FP_IM_HTTP_ADDR", ":8081"),
		GRPCAddr:   envOr("FP_IM_GRPC_ADDR", ":9091"),
		RedisURL:   os.Getenv("FP_IM_REDIS_URL"),
		FPAddr:     os.Getenv("FP_IM_FP_ADDR"),
		FPInsecure: envOr("FP_IM_FP_INSECURE", "true") == "true",
		AppsFile:   os.Getenv("FP_IM_APPS_FILE"),
		LogLevel:   envOr("FP_IM_LOG_LEVEL", "info"),
		TrustProxy: envOr("FP_IM_TRUST_PROXY", "false") == "true",
	}
	var missing []string
	for _, kv := range []struct{ k, v string }{{"FP_IM_REDIS_URL", c.RedisURL}, {"FP_IM_FP_ADDR", c.FPAddr}, {"FP_IM_APPS_FILE", c.AppsFile}} {
		if kv.v == "" {
			missing = append(missing, kv.k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: 缺少必填环境变量 %s", strings.Join(missing, ", "))
	}
	var err error
	if c.Node.Heartbeat, err = dur("FP_IM_NODE_HEARTBEAT", 3*time.Second); err != nil {
		return nil, err
	}
	if c.Node.DeadAfter, err = dur("FP_IM_NODE_DEAD_AFTER", 10*time.Second); err != nil {
		return nil, err
	}
	if c.Conn.FieldTTL, err = dur("FP_IM_CONN_FIELD_TTL", 30*time.Minute); err != nil {
		return nil, err
	}
	if c.Conn.FieldRenew, err = dur("FP_IM_CONN_FIELD_RENEW", 10*time.Minute); err != nil {
		return nil, err
	}
	if c.Conn.IdleTimeout, err = dur("FP_IM_CONN_IDLE_TIMEOUT", 60*time.Second); err != nil {
		return nil, err
	}
	if c.Conn.AuthTimeout, err = dur("FP_IM_CONN_AUTH_TIMEOUT", 5*time.Second); err != nil {
		return nil, err
	}
	if c.Conn.SendQueue, err = num("FP_IM_CONN_SEND_QUEUE", 256); err != nil {
		return nil, err
	}
	if c.Pipeline.FlushInterval, err = dur("FP_IM_PIPELINE_FLUSH_INTERVAL", 0); err != nil {
		return nil, err
	}
	if c.Pipeline.FlushSize, err = num("FP_IM_PIPELINE_FLUSH_SIZE", 1); err != nil {
		return nil, err
	}
	if c.Pipeline.FlushSize > 1 && c.Pipeline.FlushInterval <= 0 {
		return nil, errors.New("config: FP_IM_PIPELINE_FLUSH_SIZE > 1 时必须设置 FP_IM_PIPELINE_FLUSH_INTERVAL")
	}
	if c.Node.DeadAfter <= c.Node.Heartbeat {
		return nil, errors.New("config: FP_IM_NODE_DEAD_AFTER 必须大于 FP_IM_NODE_HEARTBEAT")
	}
	if c.Conn.FieldRenew*2 >= c.Conn.FieldTTL {
		return nil, errors.New("config: FP_IM_CONN_FIELD_RENEW 必须小于 FP_IM_CONN_FIELD_TTL 的一半")
	}
	return c, nil
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func dur(k string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(k)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s 不是合法时长: %w", k, err)
	}
	return d, nil
}

func num(k string, fallback int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("config: %s 必须是正整数", k)
	}
	return n, nil
}
```

- [ ] **Step 3: cmd/fp-im/main.go**

```go
// fp-im 是独立于 fp 的连接网关二进制。装配顺序与 cmd/fp 一致：配置 → 日志 → 存储 → 服务 → 监听 → 等信号。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/basicfu/fp/internal/im/appcfg"
	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/config"
	"github.com/basicfu/fp/internal/im/fpauth"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/imgrpc"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/basicfu/fp/internal/im/registry"
	"github.com/basicfu/fp/internal/im/wsapi"
	"github.com/basicfu/fp/internal/logging"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fp-im 启动失败", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	host, _ := os.Hostname()
	nodeID := model.NewNodeID(host, time.Now())

	rdb, mode, err := redisx.Open(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()
	runner, err := redisx.NewRunner(rdb, cfg.Pipeline.FlushInterval, cfg.Pipeline.FlushSize)
	if err != nil {
		return err
	}
	defer runner.Close()

	apps, err := appcfg.LoadFile(cfg.AppsFile)
	if err != nil {
		return err
	}
	go apps.Watch(ctx, 10*time.Second)

	live := registry.NewLiveness(rdb, nodeID, cfg.Node.Heartbeat, cfg.Node.DeadAfter)
	conns := registry.NewConns(rdb, runner, cfg.Conn.FieldTTL)
	b := bus.New(rdb, runner)

	var h *hub.Hub
	authn, err := fpauth.New(fpauth.Config{
		FPAddr: cfg.FPAddr, Insecure: cfg.FPInsecure, Apps: apps, Logger: log,
		OnRevoke: func(app string, tokens []string) { h.OnRevoked(context.Background(), app, tokens) },
	})
	if err != nil {
		return err
	}
	defer authn.Close()
	h = hub.New(nodeID, conns, live, b, apps)

	// 节点频道：先订阅、再登记心跳，保证别的节点看到我时我已经在听
	if err := runBusLoop(ctx, b, nodeID, h, conns, live, log); err != nil {
		return err
	}
	if err := live.Beat(ctx); err != nil {
		return fmt.Errorf("首个心跳失败: %w", err)
	}
	go live.Run(ctx)
	go renewLoop(ctx, h, conns, cfg.Conn.FieldRenew)
	guests := wsapi.NewGuestLimiter()
	go sweepLoop(ctx, guests)

	mux := http.NewServeMux()
	mux.Handle("/ws", wsapi.New(wsapi.Deps{
		Hub: h, Conns: conns, Live: live, Auth: authn, Apps: apps, Guests: guests,
		Cfg: wsapi.Config{AuthTimeout: cfg.Conn.AuthTimeout, IdleTimeout: cfg.Conn.IdleTimeout, SendQueue: cfg.Conn.SendQueue, TrustProxy: cfg.TrustProxy},
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	httpSrv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux}
	grpcSrv := imgrpc.New(imgrpc.Deps{Hub: h, Apps: apps})
	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

	fatal := make(chan error, 2)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal <- fmt.Errorf("http: %w", err)
		}
	}()
	go func() {
		if err := grpcSrv.Serve(grpcLis); err != nil {
			fatal <- fmt.Errorf("grpc: %w", err)
		}
	}()
	log.Info("fp-im 启动", "node", nodeID, "redis", mode, "http", cfg.HTTPAddr, "grpc", cfg.GRPCAddr)

	var errs []error
	select {
	case <-ctx.Done():
	case err := <-fatal:
		errs = append(errs, err)
		stop()
	}
	// 与 cmd/fp 相同：两个独立的 10 秒预算。ws 连接在 http.Server.Shutdown 里不会被等，
	// 所以先停 gRPC 让 server 不再 Push，再关 HTTP。
	gctx, gcancel := context.WithTimeout(context.Background(), 10*time.Second)
	grpcSrv.Stop(gctx)
	gcancel()
	hctx, hcancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = httpSrv.Shutdown(hctx)
	hcancel()
	for {
		select {
		case err := <-fatal:
			errs = append(errs, err)
			continue
		default:
		}
		break
	}
	return errors.Join(errs...)
}

// runBusLoop 订阅节点频道并起协程分发。收到 Gap（Redis 重连过）时把本地连接重新登记一遍。
func runBusLoop(ctx context.Context, b *bus.Bus, nodeID string, h *hub.Hub, conns *registry.Conns, live *registry.Liveness, log *slog.Logger) error {
	ch, closeFn, err := b.Subscribe(ctx, nodeID)
	if err != nil {
		return err
	}
	go func() {
		defer closeFn()
		for sig := range ch {
			switch sig.Kind {
			case bus.SignalEnvelope:
				h.HandleEnvelope(ctx, sig.Env)
			case bus.SignalGap:
				log.Warn("节点频道重订阅，重新登记本地连接")
				_ = live.Beat(ctx)
				reregister(ctx, h, conns, live)
			}
		}
	}()
	return nil
}

// reregister 用 policy=none 跑握手脚本：只清残留、HSET、HEXPIRE。限速每秒 2000 条，避免重连风暴打爆 Redis。
func reregister(ctx context.Context, h *hub.Hub, conns *registry.Conns, live *registry.Liveness) {
	type item struct {
		app, connID string
		sub         model.Subject
		meta        model.ConnMeta
	}
	var items []item
	h.ForEachConn(func(app string, sub model.Subject, connID string, meta model.ConnMeta) {
		items = append(items, item{app, connID, sub, meta})
	})
	tick := time.NewTicker(time.Second / 2000)
	defer tick.Stop()
	for _, it := range items {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		_, _ = conns.Handshake(ctx, it.app, it.sub.String(), it.connID, it.meta, model.PolicyNone, 0, live.LiveNodes())
	}
}

// renewLoop 每 renew 周期按 subject 分组给本地连接续 field TTL。
func renewLoop(ctx context.Context, h *hub.Hub, conns *registry.Conns, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		type k struct{ app, sub string }
		groups := map[k][]string{}
		h.ForEachConn(func(app string, sub model.Subject, connID string, _ model.ConnMeta) {
			groups[k{app, sub.String()}] = append(groups[k{app, sub.String()}], connID)
		})
		for g, ids := range groups {
			_ = conns.Renew(ctx, g.app, g.sub, ids)
		}
	}
}

func sweepLoop(ctx context.Context, g *wsapi.GuestLimiter) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Sweep(time.Now())
		}
	}
}
```

- [ ] **Step 4: 脚本与环境变量**

`scripts/env.sh` 在导出 `FP_TEST_REDIS_URL` 之后加：

```sh
export FP_IM_REDIS_URL="${FP_IM_REDIS_URL:-$FP_REDIS_URL}"
export FP_IM_FP_ADDR="${FP_IM_FP_ADDR:-localhost:9090}"
export FP_IM_APPS_FILE="${FP_IM_APPS_FILE:-./tmp/im-apps.json}"
```

`.env.example` 追加：

```
# fp-im（可选服务）。未设置 FP_IM_REDIS_URL 时复用 FP_REDIS_URL。
# FP_IM_HTTP_ADDR=:8081
# FP_IM_GRPC_ADDR=:9091
# FP_IM_FP_ADDR=localhost:9090
# FP_IM_APPS_FILE=./tmp/im-apps.json   # {"apps":[{"app_id":"…","app_secret":"…","allow_guest":true}]}
# FP_IM_TRUST_PROXY=false
```

`scripts/run-im.sh`（照 `scripts/run.sh` 的写法）：

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source ./scripts/env.sh
exec go run ./cmd/fp-im
```

- [ ] **Step 5: 编译与冒烟**

Run: `go build ./... && ./scripts/test.sh ./internal/im/config -v`
Expected: 编译通过，config 测试 PASS。用一个只含 `{"apps":[]}` 的 apps 文件启动 `./scripts/run-im.sh`，日志出现 `fp-im 启动`，`curl -i localhost:8081/healthz` 返回 204，Ctrl-C 后进程 10 秒内退出。

- [ ] **Step 6: 提交**

```bash
git add internal/im/config cmd/fp-im scripts/run-im.sh scripts/env.sh .env.example
git commit -m "feat(cmd): fp-im 二进制装配、心跳/续期/重登记循环、启动脚本"
```

---

## Task 14: `sdk/im` server 端 SDK

包名 `fpim`。连接管理照 `sdk/client.go`：一条流、keepalive、指数退避、健康持续时长门槛。请求靠 `reqId` 配对；流断开时所有在途请求返回 `ErrUnavailable`。

**Files:**
- Create: `sdk/im/doc.go`、`sdk/im/subject.go`、`sdk/im/server.go`
- Test: `sdk/im/subject_test.go`、`sdk/im/server_test.go`

**Interfaces:**
- Consumes: `fpimv1`。
- Produces: 第六节 `sdk/im` 段的 `Subject`、`ServerConfig`、`NewServer`、`Server` 方法；类型 `Inbound{Subject Subject; ConnID string; Payload []byte}`、`Event{Kind EventKind; Subject Subject; ConnID, OS string; Mobile bool; UA, Reason string; At int64}`、`EventKind`（`EventConnected`/`EventDisconnected`）、`PushResult{Subject Subject; Status PushStatus; Nodes int}`、`PushStatus`（`Sent`/`NotOnline`/`Unavailable`）、`Session`；错误 `ErrUnavailable`、`ErrBadPayload`；常量 `KeepaliveTime = 30 * time.Second`。

- [ ] **Step 1: subject.go 与测试**

`subject.go` 是 `internal/im/model/subject.go` 的复制品（sdk 不能 import internal），函数名改为 `Parse`；同时提供 `IsUUIDv4`。测试与 model 的相同，只改函数名。这份重复由 `internal/integration/im_parity_test.go` 守着。

- [ ] **Step 2: server 测试（桩 ImService）**

```go
package fpim

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// stubIm 是最小的 ImService：校验 metadata、发 Ready、把 Push 原样回 Sent、可主动推 Inbound/Event。
type stubIm struct {
	fpimv1.UnimplementedImServiceServer
	mu      sync.Mutex
	streams []fpimv1.ImService_ConnectServer
	seenMD  metadata.MD
}

func (s *stubIm) Connect(st fpimv1.ImService_ConnectServer) error {
	md, _ := metadata.FromIncomingContext(st.Context())
	s.mu.Lock()
	s.seenMD = md
	s.streams = append(s.streams, st)
	s.mu.Unlock()
	if err := st.Send(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Ready{Ready: &fpimv1.Ready{NodeId: "stub"}}}); err != nil {
		return err
	}
	for {
		req, err := st.Recv()
		if err != nil {
			return nil
		}
		res := &fpimv1.Result{ReqId: req.ReqId}
		switch b := req.Body.(type) {
		case *fpimv1.ConnectRequest_Push:
			res.Pushes = []*fpimv1.PushResult{{Subject: b.Push.Subject, Status: fpimv1.PushStatus_PUSH_STATUS_SENT, Nodes: 1}}
		case *fpimv1.ConnectRequest_PushMany:
			for _, sub := range b.PushMany.Subjects {
				res.Pushes = append(res.Pushes, &fpimv1.PushResult{Subject: sub, Status: fpimv1.PushStatus_PUSH_STATUS_SENT, Nodes: 1})
			}
		case *fpimv1.ConnectRequest_Sessions:
			res.Sessions = []*fpimv1.Session{{ConnId: "c1", NodeId: "stub", Os: "ios", Mobile: true, ConnectedAtMs: 9}}
		}
		_ = st.Send(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Result{Result: res}})
	}
}

func (s *stubIm) push(resp *fpimv1.ConnectResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.streams {
		_ = st.Send(resp)
	}
}

func startStub(t *testing.T) (*stubIm, string, func()) {
	t.Helper()
	stub := &stubIm{}
	var lis net.Listener
	var err error
	for i := 0; i < 20; i++ { // Windows 释放端口慢
		lis, err = net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	fpimv1.RegisterImServiceServer(srv, stub)
	go srv.Serve(lis)
	return stub, lis.Addr().String(), srv.Stop
}

func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServerPushSessionsAndCredentials(t *testing.T) {
	stub, addr, stop := startStub(t)
	defer stop()
	s, err := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitUntil(t, s.StreamHealthy, "流应就绪")
	if got := stub.seenMD.Get("fp-app-id"); len(got) != 1 || got[0] != "a1" {
		t.Fatalf("metadata 应带 fp-app-id，实际 %v", stub.seenMD)
	}
	res, err := s.Push(context.Background(), User("1"), []byte(`{"a":1}`))
	if err != nil || res.Status != Sent || res.Nodes != 1 {
		t.Fatalf("Push 结果不对：%+v %v", res, err)
	}
	if _, err := s.Push(context.Background(), User("1"), []byte(`not json`)); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("非 JSON payload 应 ErrBadPayload，实际 %v", err)
	}
	sess, err := s.Sessions(context.Background(), User("1"))
	if err != nil || len(sess) != 1 || sess[0].ConnID != "c1" || sess[0].OS != "ios" || !sess[0].Mobile || sess[0].ConnectedAt != 9 {
		t.Fatalf("Sessions 不对：%+v %v", sess, err)
	}
}

func TestServerHandlersReceiveInboundAndEvents(t *testing.T) {
	stub, addr, stop := startStub(t)
	defer stop()
	s, _ := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true})
	defer s.Close()
	var mu sync.Mutex
	var inbound []Inbound
	var events []Event
	s.OnMessage(func(_ context.Context, in Inbound) error { mu.Lock(); inbound = append(inbound, in); mu.Unlock(); return nil })
	s.OnEvent(func(_ context.Context, ev Event) { mu.Lock(); events = append(events, ev); mu.Unlock() })
	waitUntil(t, s.StreamHealthy, "流应就绪")
	stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Inbound{Inbound: &fpimv1.Inbound{Subject: "u:7", ConnId: "c", Payload: []byte(`1`)}}})
	stub.push(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Event{Event: &fpimv1.Event{Kind: fpimv1.EventKind_EVENT_KIND_CONNECTED, Subject: "g:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f", ConnId: "c", Os: "mac", Ua: "x", AtMs: 3}}})
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(inbound) == 1 && len(events) == 1 }, "应收到 1 条 Inbound 与 1 个事件")
	if inbound[0].Subject != User("7") || string(inbound[0].Payload) != "1" {
		t.Fatalf("Inbound 不对：%+v", inbound[0])
	}
	if events[0].Kind != EventConnected || events[0].Subject.Kind != KindGuest || events[0].OS != "mac" || events[0].At != 3 {
		t.Fatalf("Event 不对：%+v", events[0])
	}
}

func TestServerPushFailsFastWhenStreamDown(t *testing.T) {
	_, addr, stop := startStub(t)
	s, _ := NewServer(ServerConfig{Addr: addr, AppID: "a1", AppSecret: "sec", Insecure: true})
	defer s.Close()
	waitUntil(t, s.StreamHealthy, "流应就绪")
	stop()
	waitUntil(t, func() bool { return !s.StreamHealthy() }, "桩停掉后流应变为不健康")
	if _, err := s.Push(context.Background(), User("1"), []byte(`1`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("流断开时 Push 应立即 ErrUnavailable，实际 %v", err)
	}
}
```

- [ ] **Step 3: 实现 server.go**

```go
package fpim

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// KeepaliveTime 必须大于 fp-im 侧的 KeepaliveMinTime(10s)。
const KeepaliveTime = 30 * time.Second

var (
	ErrUnavailable = errors.New("fpim: 与 fp-im 的流不可用")
	ErrBadPayload  = errors.New("fpim: payload 必须是合法 JSON")
)

type ServerConfig struct {
	Addr           string
	AppID          string
	AppSecret      string
	Insecure       bool
	TLSConfig      *tls.Config
	RequestTimeout time.Duration // 默认 5s
	Logger         *slog.Logger
}

type PushStatus int

const (
	Sent PushStatus = iota + 1
	NotOnline
	Unavailable
)

type PushResult struct {
	Subject Subject
	Status  PushStatus
	Nodes   int
}

type Session struct {
	ConnID      string
	Node        string
	OS          string
	Mobile      bool
	ConnectedAt int64
}

type Inbound struct {
	Subject Subject
	ConnID  string
	Payload []byte
}

type EventKind int

const (
	EventConnected EventKind = iota + 1
	EventDisconnected
)

type Event struct {
	Kind    EventKind
	Subject Subject
	ConnID  string
	OS      string
	Mobile  bool
	UA      string
	Reason  string
	At      int64
}

type Server struct {
	cfg  ServerConfig
	conn *grpc.ClientConn
	rpc  fpimv1.ImServiceClient
	log  *slog.Logger

	onMessage atomic.Pointer[func(context.Context, Inbound) error]
	onEvent   atomic.Pointer[func(context.Context, Event)]

	mu      sync.Mutex
	stream  fpimv1.ImService_ConnectClient // 当前流，nil 表示未就绪
	pending map[string]chan *fpimv1.Result
	up      atomic.Bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type appCreds struct{ id, secret string; insecure bool }

func (c appCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"fp-app-id": c.id, "fp-app-secret": c.secret}, nil
}
func (c appCreds) RequireTransportSecurity() bool { return !c.insecure }

func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Addr == "" || cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, errors.New("fpim: Addr、AppID、AppSecret 必填")
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	creds := credentials.NewTLS(cfg.TLSConfig)
	if cfg.Insecure {
		creds = insecure.NewCredentials()
		cfg.Logger.Warn("fpim: 以明文连接 fp-im，只应在开发环境使用")
	}
	conn, err := grpc.NewClient(cfg.Addr,
		grpc.WithTransportCredentials(creds),
		grpc.WithPerRPCCredentials(appCreds{cfg.AppID, cfg.AppSecret, cfg.Insecure}),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: KeepaliveTime, Timeout: 10 * time.Second, PermitWithoutStream: true}),
	)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, conn: conn, rpc: fpimv1.NewImServiceClient(conn), log: cfg.Logger, pending: map[string]chan *fpimv1.Result{}}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go s.runLoop(ctx)
	return s, nil
}

func (s *Server) OnMessage(fn func(context.Context, Inbound) error) { s.onMessage.Store(&fn) }
func (s *Server) OnEvent(fn func(context.Context, Event))           { s.onEvent.Store(&fn) }
func (s *Server) StreamHealthy() bool                              { return s.up.Load() }

func (s *Server) Close() error {
	s.cancel()
	err := s.conn.Close()
	s.wg.Wait()
	return err
}

// runLoop 与 fpsdk.Client.runWatch 同构：退避 200ms→30s，健康持续 5s 才复位。
func (s *Server) runLoop(ctx context.Context) {
	defer s.wg.Done()
	const minBackoff, maxBackoff, healthyFor = 200 * time.Millisecond, 30 * time.Second, 5 * time.Second
	backoff := minBackoff
	for ctx.Err() == nil {
		start := time.Now()
		gotReady := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if gotReady && time.Since(start) >= healthyFor {
			backoff = minBackoff
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

func (s *Server) runOnce(ctx context.Context) (gotReady bool) {
	stream, err := s.rpc.Connect(ctx)
	if err != nil {
		s.log.Warn("fpim: 建流失败", "err", err)
		return false
	}
	defer s.dropStream()
	for {
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("fpim: 流断开", "err", err)
			}
			return gotReady
		}
		switch b := resp.GetBody().(type) {
		case *fpimv1.ConnectResponse_Ready:
			s.mu.Lock()
			s.stream = stream
			s.mu.Unlock()
			s.up.Store(true)
			gotReady = true
			s.log.Info("fpim: 流就绪", "node", b.Ready.GetNodeId())
		case *fpimv1.ConnectResponse_Result:
			s.mu.Lock()
			ch := s.pending[b.Result.GetReqId()]
			delete(s.pending, b.Result.GetReqId())
			s.mu.Unlock()
			if ch != nil {
				ch <- b.Result
			}
		case *fpimv1.ConnectResponse_Inbound:
			if fn := s.onMessage.Load(); fn != nil {
				sub, _ := Parse(b.Inbound.GetSubject())
				if err := (*fn)(ctx, Inbound{Subject: sub, ConnID: b.Inbound.GetConnId(), Payload: b.Inbound.GetPayload()}); err != nil {
					s.log.Warn("fpim: OnMessage 返回错误", "subject", sub, "err", err)
				}
			}
		case *fpimv1.ConnectResponse_Event:
			if fn := s.onEvent.Load(); fn != nil {
				sub, _ := Parse(b.Event.GetSubject())
				kind := EventDisconnected
				if b.Event.GetKind() == fpimv1.EventKind_EVENT_KIND_CONNECTED {
					kind = EventConnected
				}
				(*fn)(ctx, Event{Kind: kind, Subject: sub, ConnID: b.Event.GetConnId(), OS: b.Event.GetOs(), Mobile: b.Event.GetMobile(), UA: b.Event.GetUa(), Reason: b.Event.GetReason(), At: b.Event.GetAtMs()})
			}
		}
	}
}

// dropStream 把流标为不可用并让所有在途请求立刻失败，而不是等到超时。
func (s *Server) dropStream() {
	s.up.Store(false)
	s.mu.Lock()
	s.stream = nil
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	s.mu.Unlock()
}

func (s *Server) call(ctx context.Context, body func(reqID string) *fpimv1.ConnectRequest) (*fpimv1.Result, error) {
	id := uuid.NewString()
	ch := make(chan *fpimv1.Result, 1)
	s.mu.Lock()
	stream := s.stream
	if stream == nil {
		s.mu.Unlock()
		return nil, ErrUnavailable
	}
	s.pending[id] = ch
	s.mu.Unlock()
	if err := stream.Send(body(id)); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, errors.Join(ErrUnavailable, err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()
	select {
	case res, ok := <-ch:
		if !ok {
			return nil, ErrUnavailable
		}
		if res.GetError() != "" {
			return nil, errors.New("fpim: " + res.GetError())
		}
		return res, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, errors.Join(ErrUnavailable, ctx.Err())
	}
}

func (s *Server) Push(ctx context.Context, sub Subject, payload []byte) (PushResult, error) {
	rs, err := s.PushMany(ctx, []Subject{sub}, payload)
	if err != nil {
		return PushResult{}, err
	}
	return rs[0], nil
}

func (s *Server) PushMany(ctx context.Context, subs []Subject, payload []byte) ([]PushResult, error) {
	if !json.Valid(payload) {
		return nil, ErrBadPayload
	}
	names := make([]string, len(subs))
	for i, x := range subs {
		names[i] = x.String()
	}
	res, err := s.call(ctx, func(id string) *fpimv1.ConnectRequest {
		return &fpimv1.ConnectRequest{ReqId: id, Body: &fpimv1.ConnectRequest_PushMany{PushMany: &fpimv1.PushManyRequest{Subjects: names, Payload: payload}}}
	})
	if err != nil {
		return nil, err
	}
	out := make([]PushResult, len(res.GetPushes()))
	for i, p := range res.GetPushes() {
		sub, _ := Parse(p.GetSubject())
		st := Unavailable
		switch p.GetStatus() {
		case fpimv1.PushStatus_PUSH_STATUS_SENT:
			st = Sent
		case fpimv1.PushStatus_PUSH_STATUS_NOT_ONLINE:
			st = NotOnline
		}
		out[i] = PushResult{Subject: sub, Status: st, Nodes: int(p.GetNodes())}
	}
	return out, nil
}

func (s *Server) Sessions(ctx context.Context, sub Subject) ([]Session, error) {
	res, err := s.call(ctx, func(id string) *fpimv1.ConnectRequest {
		return &fpimv1.ConnectRequest{ReqId: id, Body: &fpimv1.ConnectRequest_Sessions{Sessions: &fpimv1.SessionsRequest{Subject: sub.String()}}}
	})
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(res.GetSessions()))
	for _, x := range res.GetSessions() {
		out = append(out, Session{ConnID: x.GetConnId(), Node: x.GetNodeId(), OS: x.GetOs(), Mobile: x.GetMobile(), ConnectedAt: x.GetConnectedAtMs()})
	}
	return out, nil
}

func (s *Server) Kick(ctx context.Context, sub Subject, connIDs ...string) error {
	_, err := s.call(ctx, func(id string) *fpimv1.ConnectRequest {
		return &fpimv1.ConnectRequest{ReqId: id, Body: &fpimv1.ConnectRequest_Kick{Kick: &fpimv1.KickRequest{Subject: sub.String(), ConnIds: connIDs}}}
	})
	return err
}
```

- [ ] **Step 4: doc.go 与 sdk 架构断言**

`sdk/im/doc.go`：

```go
// Package fpim 是 fp-im 的 Go SDK：Server 给业务 server 用，Client 给 Go 端的设备或程序用。
// 与 fpsdk 同样的两条硬规则：不 import internal/，不 panic（sdk/arch_test.go 会遍历 sdk/ 下所有子目录）。
package fpim
```

确认 `sdk/arch_test.go` 的 `walkNonTestGoFiles` 会递归进 `sdk/im`（它从 `sdk/` 根开始走，只跳过 `gen`），无需改动。

- [ ] **Step 5: 跑测试**

Run: `./scripts/test.sh ./sdk/... -v`
Expected: 全部 PASS，含 `TestSDKHasNoPanic`、`TestSDKDoesNotImportInternal`。

- [ ] **Step 6: 提交**

```bash
git add sdk/im
git commit -m "feat(sdk): fpim server 端 SDK：一条双向流、reqId 配对、退避重连"
```

---

## Task 15: `sdk/im` client 端 Go SDK

**Files:**
- Create: `sdk/im/client.go`
- Test: `sdk/im/client_test.go`

**Interfaces:**
- Consumes: `github.com/coder/websocket`（已在白名单）。
- Produces: `ClientConfig`、`Dial(ctx, ClientConfig) (*Client, error)`、`(*Client).OnMessage(func([]byte))`、`OnClose(func(code int))`、`Send(ctx, []byte) error`、`Close() error`；关闭码常量 `CloseAuthFailed=4001`、`ClosePolicyRejected=4002`、`CloseKicked=4003`、`CloseUnavailable=4004`。

- [ ] **Step 1: 测试（httptest 假 im）**

```go
package fpim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeIm 是最小的 ws 端点：读 auth 帧、回 hello、把 msg 原样回推、可按指令关闭。
type fakeIm struct {
	closeWith atomic.Int32 // 非零：握手后立刻以该码关闭
	mu        sync.Mutex
	auths     []map[string]any
	conns     atomic.Int32
}

func (f *fakeIm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	f.conns.Add(1)
	ctx := r.Context()
	_, b, err := c.Read(ctx)
	if err != nil {
		return
	}
	var af map[string]any
	_ = json.Unmarshal(b, &af)
	f.mu.Lock()
	f.auths = append(f.auths, af)
	f.mu.Unlock()
	if code := f.closeWith.Load(); code != 0 {
		_ = c.Close(websocket.StatusCode(code), "test")
		return
	}
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"t":"hello","conn":"c1"}`))
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		var fr struct {
			T string          `json:"t"`
			P json.RawMessage `json:"p"`
		}
		_ = json.Unmarshal(b, &fr)
		if fr.T == "msg" {
			_ = c.Write(ctx, websocket.MessageText, []byte(`{"t":"msg","p":`+string(fr.P)+`}`))
		}
		if fr.T == "ping" {
			_ = c.Write(ctx, websocket.MessageText, []byte(`{"t":"pong"}`))
		}
	}
}

func newFake(t *testing.T) (*fakeIm, string) {
	f := &fakeIm{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestDialSendsAuthFrameAndEchoes(t *testing.T) {
	f, url := newFake(t)
	got := make(chan []byte, 1)
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "tok", OS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.OnMessage(func(p []byte) { got <- p })
	if len(f.auths) != 1 || f.auths[0]["t"] != "auth" || f.auths[0]["app"] != "a1" || f.auths[0]["token"] != "tok" || f.auths[0]["os"] != "linux" {
		t.Fatalf("握手帧不对：%v", f.auths)
	}
	if err := c.Send(context.Background(), []byte(`{"hi":1}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		if string(p) != `{"hi":1}` {
			t.Fatalf("回显不对：%s", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("5 秒内没收到回显")
	}
	if err := c.Send(context.Background(), []byte(`nope`)); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("非 JSON 应 ErrBadPayload，实际 %v", err)
	}
}

func TestDialFailsOn4001AndDoesNotReconnect(t *testing.T) {
	f, url := newFake(t)
	f.closeWith.Store(CloseAuthFailed)
	_, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Token: "bad"})
	if err == nil {
		t.Fatal("4001 应让 Dial 失败")
	}
	time.Sleep(300 * time.Millisecond)
	if f.conns.Load() != 1 {
		t.Fatalf("认证失败不能自动重连，实际连接了 %d 次", f.conns.Load())
	}
}

func TestClientReconnectsAfterServerDropAndReportsClose(t *testing.T) {
	f := &fakeIm{}
	srv := httptest.NewServer(f)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	c, err := Dial(context.Background(), ClientConfig{URL: url, App: "a1", Guest: "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	codes := make(chan int, 4)
	c.OnClose(func(code int) { codes <- code })
	srv.CloseClientConnections()
	select {
	case <-codes:
	case <-time.After(5 * time.Second):
		t.Fatal("连接被切断后 OnClose 应被调用")
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.conns.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("切断后应自动重连")
		}
		time.Sleep(20 * time.Millisecond)
	}
	srv.Close()
}
```

- [ ] **Step 2: 实现 client.go**

```go
package fpim

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// 与 fp-im 一致的关闭码。4001/4003 不重连：client 应重新登录或接受被踢，而不是反复撞门。
const (
	CloseAuthFailed     = 4001
	ClosePolicyRejected = 4002
	CloseKicked         = 4003
	CloseUnavailable    = 4004
)

type ClientConfig struct {
	URL    string // wss://host/ws
	App    string
	Token  string // 与 Guest 二选一
	Guest  string // 访客 uuid v4，调用方自己持久化
	UA     string
	OS     string
	Mobile bool
	Logger *slog.Logger
}

type Client struct {
	cfg ClientConfig
	log *slog.Logger

	onMessage atomic.Pointer[func([]byte)]
	onClose   atomic.Pointer[func(int)]

	mu     sync.Mutex
	ws     *websocket.Conn
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed atomic.Bool
}

type frame struct {
	T    string          `json:"t"`
	Conn string          `json:"conn,omitempty"`
	P    json.RawMessage `json:"p,omitempty"`
}

// Dial 建立连接并完成握手；握手失败直接返回错误，之后的断线由内部退避重连。
func Dial(ctx context.Context, cfg ClientConfig) (*Client, error) {
	if cfg.URL == "" || cfg.App == "" || (cfg.Token == "" && cfg.Guest == "") {
		return nil, errors.New("fpim: URL、App 与 Token/Guest 必填")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	c := &Client{cfg: cfg, log: cfg.Logger}
	ws, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.setWS(ws)
	c.wg.Add(1)
	go c.runLoop(rctx, ws)
	return c, nil
}

func (c *Client) OnMessage(fn func([]byte)) { c.onMessage.Store(&fn) }
func (c *Client) OnClose(fn func(code int)) { c.onClose.Store(&fn) }

func (c *Client) setWS(ws *websocket.Conn) {
	c.mu.Lock()
	c.ws = ws
	c.mu.Unlock()
}

// connect 拨号、发握手帧、等 hello。任何一步失败都返回错误并关掉半成品连接。
func (c *Client) connect(ctx context.Context) (*websocket.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, c.cfg.URL, nil)
	if err != nil {
		return nil, err
	}
	auth := map[string]any{"t": "auth", "app": c.cfg.App}
	if c.cfg.Token != "" {
		auth["token"] = c.cfg.Token
	} else {
		auth["guest"] = c.cfg.Guest
	}
	if c.cfg.UA != "" {
		auth["ua"] = c.cfg.UA
	}
	if c.cfg.OS != "" {
		auth["os"], auth["mobile"] = c.cfg.OS, c.cfg.Mobile
	}
	b, _ := json.Marshal(auth)
	if err := ws.Write(dctx, websocket.MessageText, b); err != nil {
		ws.CloseNow()
		return nil, err
	}
	_, data, err := ws.Read(dctx)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	var f frame
	if json.Unmarshal(data, &f) != nil || f.T != "hello" {
		ws.CloseNow()
		return nil, errors.New("fpim: 握手未收到 hello")
	}
	return ws, nil
}

// runLoop 读到断开，通知 OnClose，然后除非是 4001/4003 或已 Close，否则退避重连。
func (c *Client) runLoop(ctx context.Context, ws *websocket.Conn) {
	defer c.wg.Done()
	backoff := 200 * time.Millisecond
	for {
		code := c.readUntilClosed(ctx, ws)
		if fn := c.onClose.Load(); fn != nil && !c.closed.Load() {
			(*fn)(code)
		}
		if c.closed.Load() || ctx.Err() != nil || code == CloseAuthFailed || code == CloseKicked {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			next, err := c.connect(ctx)
			if err == nil {
				ws = next
				c.setWS(ws)
				backoff = 200 * time.Millisecond
				break
			}
			if st := websocket.CloseStatus(err); st == CloseAuthFailed || st == CloseKicked {
				return
			}
			c.log.Warn("fpim: 重连失败", "err", err)
			if backoff *= 2; backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

// readUntilClosed 读消息、回 ping，直到连接结束，返回关闭码（未知则 -1）。
func (c *Client) readUntilClosed(ctx context.Context, ws *websocket.Conn) int {
	pingCtx, stopPing := context.WithCancel(ctx)
	defer stopPing()
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
				c.mu.Lock()
				_ = ws.Write(pingCtx, websocket.MessageText, []byte(`{"t":"ping"}`))
				c.mu.Unlock()
			}
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return int(websocket.CloseStatus(err))
		}
		var f frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.T == "msg" {
			if fn := c.onMessage.Load(); fn != nil {
				(*fn)([]byte(f.P))
			}
		}
	}
}

// Send 只表示已写入 ws；对端是否收到由业务层按配方（id 去重 + 超时重发）保证。
func (c *Client) Send(ctx context.Context, payload []byte) error {
	if !json.Valid(payload) {
		return ErrBadPayload
	}
	b := make([]byte, 0, len(payload)+16)
	b = append(b, `{"t":"msg","p":`...)
	b = append(b, payload...)
	b = append(b, '}')
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws == nil {
		return ErrUnavailable
	}
	return c.ws.Write(ctx, websocket.MessageText, b)
}

func (c *Client) Close() error {
	c.closed.Store(true)
	c.cancel()
	c.mu.Lock()
	ws := c.ws
	c.mu.Unlock()
	var err error
	if ws != nil {
		err = ws.Close(websocket.StatusNormalClosure, "bye")
	}
	c.wg.Wait()
	return err
}
```

- [ ] **Step 3: 跑测试**

Run: `./scripts/test.sh ./sdk/... -v`
Expected: 全部 PASS

- [ ] **Step 4: 提交**

```bash
git add sdk/im/client.go sdk/im/client_test.go
git commit -m "feat(sdk): fpim client 端 Go SDK：握手、心跳、退避重连"
```

---

## Task 16: `fpsdk` 中间件 `AllowGuest`

**Files:**
- Modify: `sdk/middleware.go`、`sdk/auth.go`（`Identity` 加字段）
- Test: `sdk/middleware_test.go`

**Interfaces:**
- Produces: `MiddlewareOptions.AllowGuest bool`；`Identity.GuestID string`；`const GuestIDHeader = "X-Guest-Id"`；`func (id *Identity) IsGuest() bool`。

- [ ] **Step 1: 测试**

在 `sdk/middleware_test.go` 里加（沿用该文件已有的构造 `Auth` 的方式）：

```go
func TestMiddlewareAllowGuest(t *testing.T) {
	a := newTestAuth(t) // 该文件已有的 helper；若名字不同用现有的
	var seen *Identity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen, _ = IdentityFrom(r.Context()) })
	h := a.MiddlewareWith(MiddlewareOptions{AllowGuest: true})(next)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(GuestIDHeader, "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || seen == nil || seen.GuestID != "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f" || !seen.IsGuest() {
		t.Fatalf("合法访客头应放行并注入 GuestID，实际 code=%d id=%+v", rec.Code, seen)
	}

	seen = nil
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set(GuestIDHeader, "not-a-uuid")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("非 uuid v4 的访客头应 401，实际 %d", rec.Code)
	}

	off := a.MiddlewareWith(MiddlewareOptions{})(next)
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set(GuestIDHeader, "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f")
	rec = httptest.NewRecorder()
	off.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("未开 AllowGuest 时访客头必须无效，实际 %d", rec.Code)
	}
}
```

- [ ] **Step 2: 实现**

`sdk/auth.go` 的 `Identity` 加：

```go
	// GuestID 非空表示这是一个访客请求：没有 token，身份来自 X-Guest-Id 头（仅 AllowGuest 开启时）。
	GuestID string
```

```go
func (id *Identity) IsGuest() bool { return id != nil && id.GuestID != "" }
```

`sdk/middleware.go`：

```go
// GuestIDHeader 与 fp-im 握手帧里的 guest 字段承载同一个值：前端生成并持久化的 uuid v4。
const GuestIDHeader = "X-Guest-Id"
```

`MiddlewareOptions` 加：

```go
	// AllowGuest 开启后，无 token 但带合法 X-Guest-Id 的请求以访客身份放行，权限按 fp 配置的 guest 角色判。
	// 访客 id 不经任何签发方，所以只做格式校验；滥造 id 的防线在 fp-im 的按 IP 限流。
	AllowGuest bool
```

在 `MiddlewareWith` 返回的 handler 里，取到 token 为空之后、返回 `ErrNoToken` 之前插入：

```go
			if token == "" && opts.AllowGuest {
				if gid := r.Header.Get(GuestIDHeader); gid != "" {
					if !isUUIDv4(gid) {
						opts.OnError(w, r, ErrUnauthorized)
						return
					}
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityCtxKey{}, &Identity{GuestID: gid})))
					return
				}
			}
```

`isUUIDv4` 与 `sdk/im/subject.go` 里的实现相同（`uuid.Parse` + `Version()==4` + 小写带连字符），复制到 `sdk/middleware.go`；`fpsdk` 不能 import `fpim`（反向依赖会让所有 fpsdk 用户拉进 websocket）。

- [ ] **Step 3: 跑测试并提交**

Run: `./scripts/test.sh ./sdk -v` → 全部 PASS。

```bash
git add sdk/middleware.go sdk/auth.go sdk/middleware_test.go
git commit -m "feat(sdk): Auth 中间件 AllowGuest，X-Guest-Id 以访客身份放行"
```

---

## Task 17: 多节点端到端集成测试与配对断言

两个 im 节点在同一进程内起（不同 nodeId、共用真实 Redis、各自的回环端口），一个 `fpim.Server` 连节点 B，`fpim.Client` 连节点 A。`Authenticator` 用假的（不起 fp）。

**Files:**
- Create: `internal/integration/im_e2e_test.go`、`internal/integration/im_parity_test.go`
- Test: 同上

**Interfaces:**
- Consumes: 全部。

- [ ] **Step 1: 配对断言 im_parity_test.go**

```go
package integration_test

import (
	"testing"

	"github.com/basicfu/fp/internal/im/imgrpc"
	"github.com/basicfu/fp/internal/im/model"
	fpim "github.com/basicfu/fp/sdk/im"
)

// SDK 的 keepalive 间隔必须大于 im 的最小允许值，否则连接会被 GOAWAY。两个常量在两个包里，只有这里能同时看见。
func TestImKeepalivePairing(t *testing.T) {
	if fpim.KeepaliveTime <= imgrpc.KeepaliveMinTime {
		t.Fatalf("fpim.KeepaliveTime(%v) 必须大于 imgrpc.KeepaliveMinTime(%v)", fpim.KeepaliveTime, imgrpc.KeepaliveMinTime)
	}
}

// subject 的字符串格式在 internal/im/model 与 sdk/im 各实现一份，必须逐字一致。
func TestSubjectFormatParity(t *testing.T) {
	for _, s := range []string{"u:1001", "g:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f", "g:bad", "x:1", ""} {
		a, errA := model.ParseSubject(s)
		b, errB := fpim.Parse(s)
		if (errA == nil) != (errB == nil) {
			t.Fatalf("%q：model err=%v，fpim err=%v", s, errA, errB)
		}
		if errA == nil && a.String() != b.String() {
			t.Fatalf("%q：model=%s fpim=%s", s, a, b)
		}
	}
	if model.CloseAuthFailed != fpim.CloseAuthFailed || model.CloseKicked != fpim.CloseKicked || model.ClosePolicyRejected != fpim.ClosePolicyRejected || model.CloseUnavailable != fpim.CloseUnavailable {
		t.Fatal("关闭码两边不一致")
	}
	if imgrpc.MDAppID != "fp-app-id" || imgrpc.MDAppSecret != "fp-app-secret" {
		t.Fatal("metadata 键必须与 fp 的 grpcapi 相同，业务方才能用同一对凭据")
	}
}
```

- [ ] **Step 2: 端到端测试 im_e2e_test.go**

```go
package integration_test

import (
	"context"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/appcfg"
	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/imgrpc"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/basicfu/fp/internal/im/registry"
	"github.com/basicfu/fp/internal/im/wsapi"
	"github.com/basicfu/fp/internal/testsupport"
	fpim "github.com/basicfu/fp/sdk/im"
	"github.com/redis/go-redis/v9"
)

type staticAuth map[string]model.Subject

func (a staticAuth) Verify(_ context.Context, _, token string) (model.Subject, error) {
	if s, ok := a[token]; ok {
		return s, nil
	}
	return model.Subject{}, auth.ErrUnauthorized
}

type imNode struct {
	id      string
	hub     *hub.Hub
	live    *registry.Liveness
	wsURL   string
	grpcAdd string
}

// startNode 起一个完整的 im 节点：registry、bus、hub、wsapi、imgrpc，都用真实 Redis。
func startNode(t *testing.T, rdb *redis.Client, id string, apps auth.AppConfigSource, authn auth.Authenticator) *imNode {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	run, _ := redisx.NewRunner(rdb, 0, 1)
	t.Cleanup(run.Close)
	live := registry.NewLiveness(rdb, id, 200*time.Millisecond, time.Second)
	conns := registry.NewConns(rdb, run, 30*time.Minute)
	b := bus.New(rdb, run)
	h := hub.New(id, conns, live, b, apps)
	ch, closeFn, err := b.Subscribe(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	go func() {
		for sig := range ch {
			if sig.Kind == bus.SignalEnvelope {
				h.HandleEnvelope(ctx, sig.Env)
			}
		}
	}()
	live.TrackApp("a1")
	_ = live.Beat(ctx)
	go live.Run(ctx)

	wsLis := listenAddr(t, "127.0.0.1:0")
	httpSrv := &http.Server{Handler: wsapi.New(wsapi.Deps{
		Hub: h, Conns: conns, Live: live, Auth: authn, Apps: apps, Guests: wsapi.NewGuestLimiter(),
		Cfg: wsapi.Config{AuthTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second, SendQueue: 64},
	})}
	go httpSrv.Serve(wsLis)
	t.Cleanup(func() { httpSrv.Close() })

	grpcLis := listenAddr(t, "127.0.0.1:0")
	g := imgrpc.New(imgrpc.Deps{Hub: h, Apps: apps})
	go g.Serve(grpcLis)
	t.Cleanup(func() { g.Stop(context.Background()) })

	return &imNode{id: id, hub: h, live: live, wsURL: "ws://" + wsLis.Addr().String() + "/", grpcAdd: grpcLis.Addr().String()}
}

func writeApps(t *testing.T, policy string) *appcfg.Source {
	t.Helper()
	p := t.TempDir() + "/apps.json"
	if err := os.WriteFile(p, []byte(`{"apps":[{"app_id":"a1","app_secret":"s1","conn_policy":"`+policy+`","conn_limit":3,"allow_guest":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := appcfg.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestImTwoNodesEndToEnd(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	apps := writeApps(t, "replace")
	authn := staticAuth{"tok-1": model.User("1"), "tok-2": model.User("2")}
	a := startNode(t, rdb, "im-a-"+time.Now().Format("150405.000"), apps, authn)
	b := startNode(t, rdb, "im-b-"+time.Now().Format("150405.000"), apps, authn)

	// server 连 B
	srv, err := fpim.NewServer(fpim.ServerConfig{Addr: b.grpcAdd, AppID: "a1", AppSecret: "s1", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	var mu sync.Mutex
	var inbound []fpim.Inbound
	var events []fpim.Event
	srv.OnMessage(func(_ context.Context, in fpim.Inbound) error { mu.Lock(); inbound = append(inbound, in); mu.Unlock(); return nil })
	srv.OnEvent(func(_ context.Context, ev fpim.Event) { mu.Lock(); events = append(events, ev); mu.Unlock() })
	waitUntil(t, srv.StreamHealthy, "server 流应就绪")
	// 等 A 的 srv 缓存看到 B（心跳 200ms）
	waitUntil(t, func() bool { return len(a.live.ServerNodes("a1")) > 0 }, "A 应看到 B 持有 a1 的 server 流")

	// client 连 A
	cli, err := fpim.Dial(context.Background(), fpim.ClientConfig{URL: a.wsURL, App: "a1", Token: "tok-1", OS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	got := make(chan []byte, 8)
	cli.OnMessage(func(p []byte) { got <- p })

	// ① Connected 事件经 A→Redis→B 到 server
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(events) == 1 && events[0].Kind == fpim.EventConnected }, "应收到 Connected")
	connID := events[0].ConnID

	// ② client→im→server：A 无流，转一跳到 B
	if err := cli.Send(context.Background(), []byte(`{"q":1}`)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(inbound) == 1 }, "server 应收到 client 的消息")
	if inbound[0].Subject != fpim.User("1") || inbound[0].ConnID != connID || string(inbound[0].Payload) != `{"q":1}` {
		t.Fatalf("Inbound 不对：%+v", inbound[0])
	}

	// ③ server→im→client：Push 从 B 发出，client 在 A
	res, err := srv.Push(context.Background(), fpim.User("1"), []byte(`{"r":2}`))
	if err != nil || res.Status != fpim.Sent || res.Nodes != 1 {
		t.Fatalf("Push 应 Sent{1}，实际 %+v %v", res, err)
	}
	select {
	case p := <-got:
		if string(p) != `{"r":2}` {
			t.Fatalf("client 收到 %s", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client 5 秒内没收到推送")
	}

	// ④ NotOnline
	if res, _ := srv.Push(context.Background(), fpim.User("2"), []byte(`1`)); res.Status != fpim.NotOnline {
		t.Fatalf("离线 subject 应 NotOnline，实际 %+v", res)
	}

	// ⑤ Sessions
	sess, err := srv.Sessions(context.Background(), fpim.User("1"))
	if err != nil || len(sess) != 1 || sess[0].ConnID != connID || sess[0].Node != a.id || sess[0].OS != "linux" {
		t.Fatalf("Sessions 不对：%+v %v", sess, err)
	}

	// ⑥ replace：同 subject 从 B 再连，A 上的旧连接被顶掉（4003），server 收到 Disconnected(replaced)
	closed := make(chan int, 1)
	cli.OnClose(func(code int) { closed <- code })
	cli2, err := fpim.Dial(context.Background(), fpim.ClientConfig{URL: b.wsURL, App: "a1", Token: "tok-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer cli2.Close()
	select {
	case code := <-closed:
		if code != fpim.CloseKicked {
			t.Fatalf("旧连接应 4003，实际 %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("旧连接 5 秒内没被顶掉")
	}
	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range events {
			if ev.Kind == fpim.EventDisconnected && ev.ConnID == connID {
				return ev.Reason == "replaced"
			}
		}
		return false
	}, "应收到 Disconnected(replaced)")

	// ⑦ Kick 全部：cli2 被踢
	closed2 := make(chan int, 1)
	cli2.OnClose(func(code int) { closed2 <- code })
	if err := srv.Kick(context.Background(), fpim.User("1")); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-closed2:
		if code != fpim.CloseKicked {
			t.Fatalf("Kick 应 4003，实际 %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kick 5 秒内没生效")
	}
}
```

`listenAddr` 与 `waitUntil` 复用 `phase2_env_test.go` 里已有的（同一个 `integration_test` 包）。`redis` 只用于 `startNode` 的参数类型。

- [ ] **Step 3: 跑测试**

Run: `./scripts/test.sh ./internal/integration -run 'TestIm' -v`
Expected: 全部 PASS。这条测试覆盖 spec 第二、五、六节的主路径与顶替、踢人、事件。

- [ ] **Step 4: 提交**

```bash
git add internal/integration/im_e2e_test.go internal/integration/im_parity_test.go
git commit -m "test(integration): fp-im 双节点端到端与常量配对断言"
```

---

## Task 18: 示例、交接文档

**Files:**
- Create: `examples/im-demo/main.go`、`examples/im-demo/README.md`、`docs/im.md`

- [ ] **Step 1: examples/im-demo/main.go**

回显 server：收到什么推回什么，并把连接事件打日志。

```go
// im-demo 是最小的 fp-im 接入示例：把 client 发来的消息原样推回去。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	fpim "github.com/basicfu/fp/sdk/im"
)

func main() {
	srv, err := fpim.NewServer(fpim.ServerConfig{
		Addr: envOr("FP_IM_ADDR", "localhost:9091"), AppID: os.Getenv("FP_APP_ID"), AppSecret: os.Getenv("FP_APP_SECRET"), Insecure: true,
	})
	if err != nil {
		slog.Error("连接 fp-im 失败", "err", err)
		os.Exit(1)
	}
	defer srv.Close()
	srv.OnEvent(func(_ context.Context, ev fpim.Event) {
		slog.Info("连接事件", "kind", ev.Kind, "subject", ev.Subject, "conn", ev.ConnID, "os", ev.OS, "reason", ev.Reason)
	})
	srv.OnMessage(func(ctx context.Context, in fpim.Inbound) error {
		res, err := srv.Push(ctx, in.Subject, in.Payload)
		slog.Info("回显", "subject", in.Subject, "status", res.Status, "err", err)
		return err
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
}

func envOr(k, v string) string {
	if x := os.Getenv(k); x != "" {
		return x
	}
	return v
}
```

- [ ] **Step 2: examples/im-demo/README.md 手工验收步骤**

```markdown
# im-demo 手工验收

1. `./scripts/run.sh` 起 fp；在控制台建一个应用，记下 app_id / app_secret；用它登录拿到一个 token。
2. 写 `tmp/im-apps.json`：`{"apps":[{"app_id":"<app_id>","app_secret":"<secret>","allow_guest":true}]}`。
3. 开两个终端各起一个 im 节点：`FP_IM_HTTP_ADDR=:8081 FP_IM_GRPC_ADDR=:9091 ./scripts/run-im.sh` 与 `FP_IM_HTTP_ADDR=:8082 FP_IM_GRPC_ADDR=:9092 ./scripts/run-im.sh`。
4. `FP_APP_ID=… FP_APP_SECRET=… FP_IM_ADDR=localhost:9092 go run ./examples/im-demo`（server 连第二个节点）。
5. 用任意 ws 工具连 `ws://localhost:8081/ws`（client 连第一个节点），发 `{"t":"auth","app":"<app_id>","token":"<token>"}`，应收到 `{"t":"hello","conn":"…"}`，demo 日志出现 Connected。
6. 发 `{"t":"msg","p":{"hi":1}}`，应收到 `{"t":"msg","p":{"hi":1}}`。
7. 再开一个 ws 连 `ws://localhost:8082/ws` 用同一个 token 握手：第一个连接应以 4003 被关闭，demo 日志出现 Disconnected(replaced)。
8. 访客：发 `{"t":"auth","app":"<app_id>","guest":"<crypto.randomUUID()>"}`，同样能收发；demo 日志里 subject 是 `g:` 前缀。
9. Ctrl-C 一个节点，另一节点 10 秒后日志不再把它算作存活；重连的 client 一切正常。
```

- [ ] **Step 3: docs/im.md 交接文档**

内容：一页纸的架构图（spec 第三节的拓扑）、启动方式、apps 文件格式、ws 协议（帧、关闭码）、`fpim` 两个 API 的最小用法、运维要点（Redis 8、心跳与判死、field TTL、写管道开关、`INFO cluster` 探测）、已知不足（下面第八节的表）、指向 spec 的链接。用 `docs/console.md` 的口吻写，600 字以内，不复制 spec。

- [ ] **Step 4: 编译、全量测试、提交**

Run: `go build ./... && ./scripts/test.sh`
Expected: 全绿（含 `TestExamplesDoNotImportInternal`）。

```bash
git add examples/im-demo docs/im.md
git commit -m "docs: fp-im 接入示例、手工验收步骤与交接文档"
```

---

## 七、验收对照表

| # | 验收项（spec 章节） | 由谁保证 |
|---|---|---|
| 1 | 三张表 key 命名、hash tag、field 级 TTL（3.2） | `model/keys_test`、`registry/conns_test` |
| 2 | 只订阅节点私有频道，sharded pub/sub（3.3） | `bus/bus_test`、`redisx/open_test` |
| 3 | 写管道默认立即发、可配攒批（3.4、十二） | `redisx/runner_test`、`config_test` |
| 4 | 握手：token / 访客 / 策略矩阵 / 残留清理 / 顶替（四） | `registry/conns_test`、`wsapi/handler_test`、e2e ⑥ |
| 5 | 访客按 IP 限流（4.1） | `wsapi/guestlimit_test` |
| 6 | Push 本地短路、NotOnline、PushMany 一次查表（5.1–5.2） | `hub/push_test`、e2e ③④ |
| 7 | Sessions / Kick 全部与单条（5.3–5.4） | `hub/push_test`、`imgrpc/server_test`、e2e ⑤⑦ |
| 8 | deliver：本地直投、转一跳、hops 上限、静默丢弃（六） | `hub/deliver_test`、e2e ② |
| 9 | Connected / Disconnected 事件与 reason（六） | `hub/hub_test`、`wsapi/handler_test`、e2e ①⑥ |
| 10 | 撤销 token 关连接（十一） | `hub/push_test` `TestOnRevokedClosesConnsByToken`、`sdk` `TestOnRevokeCallbackReceivesEvent` |
| 11 | 断开 HDEL、Redis 重连重登记、心跳判死（七） | `wsapi/handler_test`、`registry/liveness_test`、`cmd` 的 `reregister`（无自动测试，见已知不足） |
| 12 | SDK 承诺：Sent/NotOnline、handler 同步、流断开快速失败（十） | `sdk/im/server_test` |
| 13 | client SDK：握手、4001/4003 不重连、断线重连（10.2） | `sdk/im/client_test` |
| 14 | `AllowGuest` 中间件（4.1） | `sdk/middleware_test` |
| 15 | 架构规则：单 KEYS、import 边界、依赖白名单、无 panic | `registry/arch_test`、`internal/im/arch_test`、`integration/dependency_whitelist_test`、`sdk/arch_test` |
| 16 | 常量配对：keepalive、subject 格式、关闭码、metadata 键 | `integration/im_parity_test` |

## 八、给执行者的最后几句

**最容易写成"看起来对"的地方**

- 握手脚本的存活列表参数里必须包含自己的 nodeId，否则脚本第一步会把刚 HSET 的自己删掉。`Conns.Handshake` 已经补，但别在别处绕过它直接跑脚本。
- `Hub.AddConn` 之前必须先跑握手脚本，`teardown` 里必须先 `Remove` 再 `RemoveConn`。顺序反了会出现"注册表里有、hub 里没有"的窗口，Push 会返回 Sent 却没人收。
- `sender.Send` 加锁不是可选项：gRPC 流并发 Send 会直接 panic。
- `Runner.Run` 在攒批模式下用调用方的 ctx 只决定"等不等"，不决定"发不发"：命令已进批就一定会发。
- fpim client 的 `Send` 持锁写 ws，ping 协程也持同一把锁；不要在持锁时调 handler。

**数值约束**：心跳 3s / 判死 10s、field TTL 30m / 续期 10m、握手 5s、空闲 60s、发送队列 256、重登记限速 2000/s、bus 出通道缓冲 1024、imgrpc 每流 64 并发。改任何一个都要同时改 spec 第十二节。

### 本计划自身的已知不足

| 处 | 情况 | 要求 |
|---|---|---|
| `cmd/fp-im` 的 `reregister` / `renewLoop` | 没有自动测试，逻辑只有十几行 | 手工验收第 9 步覆盖判死；重登记靠断 Redis 再恢复后看日志 `节点频道重订阅` |
| Cluster | 无真机测试 | 单 KEYS 断言 + `INFO cluster` 单测；上线前在真实 Cluster 跑一遍 e2e（把 `FP_TEST_REDIS_URL` 指过去即可） |
| `docs/im.md` | 只给了提纲 | 写完对照 spec 第九节的三句承诺，一句都不能多 |
| Task 9 的桩 `pushRevoke` | 依赖 `sdk/client_test.go` 现有桩的形状 | 若桩没有该能力，按现有 revoke 测试的写法补，不要另起一套桩 |

**断言点本身是硬要求。**
