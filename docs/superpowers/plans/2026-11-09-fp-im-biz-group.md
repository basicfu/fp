# fp-im 业务方主体分组 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 业务方主体可带分组(`b.<group>:<uid>`)和分组级在线策略,业务 server 能一次列出某分组全部在线主体。

**Architecture:** 分组与策略都由业务方鉴权回调返回,写进 subject;每个 fp-im 节点用"记脏 + 写当前状态"的单写协程维护自己那份 Redis set 索引;ListGroup 合并存活节点的索引,再用连接表核实。

**Tech Stack:** Go 1.26、Redis 8(HEXPIRE)、gRPC 双向流、protobuf(`scripts/gen.sh`)。

**Spec:** `docs/superpowers/specs/2026-11-09-fp-im-biz-group-design.md`(以下 §x 都指它)

## Global Constraints

- 分组名正则 `^[a-z][a-z0-9_]{0,15}$`;subject 写法 `b.<group>:<uid>`;默认分组仍 `b:<uid>`。
- 分组策略取值 `replace|reject|limit`;`limit` 时 `conn_limit` ∈ [1, 100];不返回策略时用应用级 `ConnPolicy/ConnLimit`;默认分组不认回调策略。
- 每应用每节点同时存在的分组数 ≤ 32;ListGroup 结果 ≤ 10000 个主体;LookupMany 按 1000 一批。
- 索引 key `fp:im:{<app>:grp:<group>:<node>}:idx`,成员是裸 uid;每次写入都 `EXPIRE` field TTL(`cfg.Conn.FieldTTL`,默认 30 分钟)。
- 写失败退避:1s 起翻倍,封顶 30s。
- **本计划是 §9 的"第一步"**:回调**不**带 `X-Fp-Im-Features` 头。头的常量与发送代码写好,但由 `bizauth.Config.SendFeatures bool`(默认 false)控制,第二步只改默认值。
- 测试:`scripts/test.sh ./internal/im/<pkg>/...`(会读 `FP_REDIS_URL`/`FP_POSTGRES_URL`,已在环境里);不跑全量 `./...`,除非是最后一步。
- 注释少而精、写 Why;中文。**不要 git commit。**
- 不改 fp 本体(`internal/domain`、`internal/service`、`internal/httpapi`、`web/`、`proto/fp/`)。

## Review Focus

1. `Hub.ForEachConn` Parse 分组主体失败会静默跳过 → 续期/重登记漏掉分组连接 —— Task 1 测试守住。
2. 分组数上限必须在 `Conns.Handshake` 之前判,否则 replace 已顶掉旧连接 —— Task 3 测试守住。
3. 索引写入在 add/remove 快速交替、写失败重试、整份重写与增量并发时最终与本地计数一致 —— Task 4 测试守住。
4. 第一次 SADD 新建的 key 必须有 TTL;快照为空时只 DEL —— Task 4 测试守住。
5. `reregistrar.run` 在"存活条目仍在"时提前 return,索引重写要在它之前 —— Task 5 测试守住。

---

### Task 1: 分组主体(model + SDK)

**Files:** `internal/im/model/subject.go`、`internal/im/model/keys.go`、`sdk/im/subject.go`、对应 `_test.go`、`internal/integration/im_parity_test.go`

**Interfaces — Produces:**
- `model.Subject{Kind, Group, ID}`;`model.BizGroup(group, id string) Subject`;`(Subject).IsDefaultBiz() bool`;`model.ValidateGroup(g string) error`(包 `ErrBadGroup`);`String()` → `b.<g>:<id>` 或旧写法。
- `model.GroupIdxKey(app, group, node string) string`。
- `fpim.Subject` 同构:`Group` 字段、`BizGroup`、`IsDefaultBiz`、`Parse`。

- [x] 测试:往返 `BizGroup("r","a:b").String()=="b.r:a:b"` 且 Parse 回来 Group=r、ID=a:b;`b:1001` 解析 Group=="";拒绝 `u.x:1`、`g.x:<uuid>`、`b.:x`、`b.R:x`、`b.<17 个字母>:x`、`b.1a:x`;`IsDefaultBiz` 四种组合;`GroupIdxKey("a1","r","n1")=="fp:im:{a1:grp:r:n1}:idx"`。parity 测试输入集补这些写法。
- [x] 实现;`ParseSubject` 在 kind 段带 `.` 时只接受 `b.`。
- [x] `hub.ForEachConn` 能遍历到分组主体的测试(建一条 `b.r:x` 连接,ForEachConn 回调收到它)。
- [x] `scripts/test.sh ./internal/im/model/... ./sdk/im/... ./internal/im/hub/... ./internal/integration/... -run Parity` 通过。

### Task 2: 回调返回分组与策略

**Files:** `internal/im/auth/auth.go`、`internal/im/bizauth/{bizauth,cache}.go`、`internal/im/fpauth/fpauth.go`、`internal/im/multiauth/multiauth.go`、`internal/im/wsapi/handler.go`、测试

**Interfaces — Produces:**
- `auth.Identity{Subject model.Subject; Policy model.Policy; Limit int}`;`Policy==""` 表示用应用级。
- `auth.Authenticator.Verify(ctx, VerifyRequest) (Identity, error)`(fpauth/访客返回 `Identity{Subject: …}`)。
- `bizauth.HeaderFeatures = "X-Fp-Im-Features"`,值 `"group"`;`bizauth.Config.SendFeatures bool`。

- [x] bizauth 测试:响应 `{uid, group:"r"}` → `b.r:<uid>`;不带 group → `b:<uid>`;group 非法 → `ErrUnavailable`;`conn_policy:"limit",conn_limit:5` 带出;`limit` 缺上限/101/未知策略 → `ErrUnavailable`;默认分组带策略 → 忽略策略(用应用级);缓存命中后分组与策略不丢;`SendFeatures=true` 时请求带头,默认不带。
- [x] 实现(`verifyResponse` 加 `Group`、`ConnPolicy`、`ConnLimit`;缓存存 `Identity`)。
- [x] handler 测试(沿用 `handler_test.go` 的台架):同 uid `web` 组 limit 2 → 第 3 条 4002;`app` 组 replace → 第二条顶掉第一条(4003);两组互不影响;无分组策略时按应用级。
- [x] 实现:`Conns.Handshake` 的 policy/limit 取 `id.Policy`/`id.Limit`(非空时)。
- [x] `scripts/test.sh ./internal/im/bizauth/... ./internal/im/fpauth/... ./internal/im/multiauth/... ./internal/im/wsapi/...` 通过。

### Task 3: hub 分组计数与上限

**Files:** `internal/im/hub/hub.go`、`internal/im/wsapi/handler.go`、测试

**Interfaces — Produces:**
- `hub.GroupIndex` 接口(由 Task 4 的 registry 实现):`MarkDirty(app, group, uid string)`。`hub.New` 增加可选注入(`SetGroupIndex(GroupIndex)`,nil 时不记)。
- `(*Hub).GroupCount(app, uid…)` 不需要;提供:
  - `(*Hub).GroupAdmit(app, group string) bool`:本节点该应用已有分组数 < 32 或 group 已存在;
  - `(*Hub).GroupSnapshot(app, group string) []string`:本节点该分组有连接的 uid;
  - `(*Hub).GroupsOf() [][2]string`:本节点所有 (app, group);
  - `(*Hub).GroupLocalCount(app, group, uid string) int`(写协程读当前状态用)。
- 计数在 `AddConn`/`RemoveConn` 里随 `h.conns` 同锁维护;0↔1 时 `MarkDirty`(锁外调用也可,但必须在计数更新之后)。

- [x] 测试:同 subject 两条连接,断一条不标脏、断两条才标脏;32 个分组后第 33 个 `GroupAdmit` false,已有分组仍 true;handler 在 `Handshake` 前判上限,被拒(`auth_unavailable`/4004)时旧连接仍在。
- [x] 实现。
- [x] `scripts/test.sh ./internal/im/hub/... ./internal/im/wsapi/...` 通过。

### Task 4: 索引写协程与读取

**Files:** 新建 `internal/im/registry/groupidx.go`、`groupidx_test.go`

**Interfaces — Produces:**
- `registry.NewGroupIndex(c redis.UniversalClient, nodeID string, ttl time.Duration, state GroupState) *GroupIndex`;`GroupState` = hub 提供的 `GroupLocalCount`/`GroupSnapshot`/`GroupsOf`。
- `(*GroupIndex).MarkDirty(app, group, uid)`(合并进 map,非阻塞唤醒);`Run(ctx)`;`RewriteAll(ctx)`(入同一协程执行,返回前完成);`Drain(ctx)`(写完剩余脏集合)。
- `registry.ListGroup(ctx, c, conns *Conns, app, group string, live []string) ([]SubjectConns, error)`,`SubjectConns{UID string; Conns map[string]model.ConnMeta}`;超过 10000 返回 `ErrGroupTooLarge`。

- [x] 测试(真 Redis):add→SADD 且 PTTL>0;add/remove/add 快速交替后成员与本地计数一致;用会失败的客户端(或关掉的 client)写失败后恢复能补上;RewriteAll 与并发 MarkDirty 交错后最终一致;快照为空时 key 被 DEL;ListGroup 合并两个节点、跳过不在 live 的节点、去掉只在索引里没连接的成员、超 10000 报错。
- [x] 实现(重写用 `MULTI{DEL; SADD…; EXPIRE}`,成员多时 SADD 分批但同一个 MULTI)。
- [x] `scripts/test.sh ./internal/im/registry/...` 通过;`registry/arch_test.go`(单 key 约束)仍过。

### Task 5: ListGroup 接口与装配

**Files:** `proto/fpim/v1/im.proto`(+ `scripts/gen.sh` 生成 `sdk/gen`)、`internal/im/imgrpc/stream.go`、`sdk/im/server.go`、`cmd/fpim/main.go`、`internal/integration/im_e2e_test.go`、`docs/im.md`

**Interfaces — Produces:**
- proto:`ListGroupRequest{group=1}` 作为 `ConnectRequest.list_group = 8`;`Result.group_members = 6`(`repeated SubjectSessions`)。
- SDK:`(*Server).ListGroup(ctx, group string) ([]SubjectSessions, error)`;对端回"未知请求类型"时返回 `fpim.ErrUnsupported`。

- [x] imgrpc 测试:空 group 报错;返回 subject 写成 `b.<g>:<uid>`。SDK 测试:旧版回"未知请求类型" → `ErrUnsupported`。
- [x] main 装配:创建 GroupIndex 注入 hub 并 `go Run`;`renewLoop` 每周期 `RewriteAll`;`reregistrar.run` 在 `SelfPresent` 判断之前(或至少在 `present` 提前 return 之前)调 `RewriteAll`;`shutdown` 拆完 ws 后 `Drain`(10s 预算)。补 `cmd/fpim/main_test.go` 一条:Gap 且存活条目仍在时也触发了重写。
- [x] e2e:两节点分组主体分连两个节点 → ListGroup 拿全;杀一个节点后立即从结果消失;Push/Kick 分组主体可用;`b:r-x` 与 `b.r:x` 互不顶号。
- [x] `docs/im.md`:主体前缀表加 `b.<group>:`、回调响应加 `group`/`conn_policy`/`conn_limit`、ListGroup、升级注意(两步)。
- [x] `scripts/test.sh ./internal/im/... ./sdk/... ./cmd/... ./internal/integration/...` 通过;`go vet ./...` 干净。
