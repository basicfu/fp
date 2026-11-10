# fp-im 业务方主体分组设计

- 日期:2026-11-09
- 状态:已实现第一步(2026-11-09,未发布);第二步是把 bizauth 的 SendFeatures 默认改为 true 的新版本
- 相关:[fp-im 设计](2026-09-03-fp-im-design.md)、[业务方认证](2026-09-05-fp-im-biz-auth-design.md)
- 起因:kkdesk 的中继节点复用 kkdesk 应用接入 fp-im,但 fp-im 没有"分组"概念

## 1. 背景

业务方令牌(`kind: biz`)握手成功后,主体是 `b:{uid}`。一个应用下所有业务方主体共用一个命名空间,fp-im 不区分它们是哪一类东西。

kkdesk 现在同时有两类连接:设备(`b:<9 位数字>`)和中继节点(`b:r-n-<hex>`)。区分它们全靠 kkdesk 自己约定 uid 前缀,问题有三个:

1. **列不出"某一类全部在线的"**。fp-im 的连接表是每个 subject 一个 key(`fp:im:{app:subject}:conn`),只能按已知 subject 去查。kkdesk 想知道"现在哪些中继连着",只能拿库里全部节点 id 去 `SessionsMany` 核对。
2. **前缀是业务方私下的约定**。fp-im、SDK、日志都不知道 `r-` 是什么,同一个 uid 字符串要在两个仓库各解析一遍。业务方自己判断"这是设备还是中继"时,也只能靠字符串前缀。
3. 同一个应用再接入第三类连接(比如网页控制台、Agent),又要再造一个前缀。

## 2. 目标与非目标

目标:

- 业务方主体可以带一个**分组**,由业务方的鉴权回调决定。不带分组就是现在的默认分组,行为逐字不变。
- 业务 server 能**一次列出某个分组此刻全部在线的主体**,结果与 `SessionsMany` 同样精确(以连接表为准)。
- 分组写进 subject,Push / Kick / Sessions / 事件原样可用,不新增一套寻址方式。
- **每个分组可以有自己的在线策略**(replace / reject / limit),也由回调决定。例如同一个用户:`web` 组允许多处同时在线,`app` 组和 `relay` 组只允许一处,新连上的顶掉旧的。
- 不碰 fp 本体(数据库、控制台、`GetAppIMConfig`):改动只在 fp-im 和 SDK。

非目标(都是"有需求再加",不是不能做):

- 分页列出。v1 一次返回整组,上限 10000 个主体。kkdesk 中继是几十到几百台;真遇到几十万量级的分组再加游标。
- 默认分组的在线列表。默认分组通常就是一个应用的全部终端用户,规模大,而且业务方本来就按 id 查;真要列,就给它显式起一个分组。
- fp 用户(`u:`)和访客(`g:`)的分组。它们的身份不由业务方决定。
- 按分组广播。业务方可以先 ListGroup 再 PushMany。

## 3. 别人怎么做

| 项目 | 分组 / 命名空间 | 在线列表怎么存 |
|---|---|---|
| Centrifugo | channel 带 namespace 前缀(`ns:chan`),每个 namespace 单独配 presence 等选项 | Redis:每个 channel 一个 hash 存成员,加一个 zset 存过期时间,读时顺手清过期成员 |
| Socket.IO(redis-adapter) | room | **每个节点只在内存里记自己的 room 成员**;`fetchSockets()` 向所有节点广播查询后汇总 |
| Phoenix Presence | topic | 每个节点维护自己那份,节点间用 CRDT 合并;节点挂了,它那份整块作废 |

fp-im 已经有节点存活表(`fp:im:node`),连接表的读法也是"过滤掉不在存活列表里的节点"(`LookupMany`)。所以本设计取第二种做法:每个节点只负责自己那份,存活表直接当判死依据,与连接表口径一致,不另造一套过期判定。

## 4. 分组与 subject 写法

- **分组名**:`^[a-z][a-z0-9_]{0,15}$`。小写且短,因为它会成为 Redis key 和每条事件 subject 的一部分。
- **subject 写法**:`b.<group>:<uid>`,例如 `b.r:n-9f3a…`。默认分组仍是 `b:<uid>`。
- **为什么把 `.` 放在冒号前面**:业务方 uid 不约束字符集(可以含 `:`、`/`),写成 `b:r/<uid>` 或 `b:r:<uid>` 会与现有 uid 撞车。现有前缀 `u` / `g` / `b` 都不含 `.`,所以 `Cut(s, ":")` 切出来的 kind 段带 `.`,就一定是分组主体。uid 里再有冒号也没关系:`b.r:a:b` 解析为分组 `r`、uid `a:b`。
- **Parse 必须拒绝的写法**:`u.x:…` / `g.x:…`(只有 b 能带分组)、`b.:x`(空分组)、`b.R:x`(大写)、分组名超长。
- **连接表 key** 自然变成 `fp:im:{app:b.r:<uid>}:conn`,与默认分组的同名 uid 天然隔离:`b:1001` 和 `b.r:1001` 是两个主体,互不顶号。
- **类型**:`model.Subject` / `fpim.Subject` 新增导出字段 `Group`,与 `Kind`、`ID` 并列,构造函数 `BizGroup(group, id)`。`Kind` 仍是 `KindBiz`,`Biz(id)` 等于 `BizGroup("", id)`。
  - **默认分组 = `Kind == KindBiz && Group == ""`**。只判 `Kind == KindBiz` 的现有代码(比如 kkdesk 的 `idFromSubject`)必须补上 `Group == ""`,否则 uid 是纯数字的分组主体会被当成默认分组。SDK 提供 `IsDefaultBiz()`,免得各家自己写。
- **不受影响的部分**:hub 路由内存键(`app\x00subject`)、信封编码(带长度前缀)、rendezvous 哈希、握手 Lua 脚本都只把 subject 当字符串。
- **必须跟着改的部分**:凡是 Parse subject 的地方,都要认新写法。特别是 `Hub.ForEachConn`:它 Parse 失败会静默跳过,续期和重登记都会漏掉分组连接,30 分钟后这些连接会从全网"消失"。这一处要有测试守住。

## 5. 谁决定分组

**由业务方的鉴权回调决定,fp-im 只校验格式。** 这与 uid 由回调决定是同一个信任模型:fp-im 本来就完全信任回调给的 uid,再信任它给的分组,不增加新的信任面。

- 回调成功响应新增可选字段 `group`:

      {"code":"success","msg":"","time":1761000000000,"uid":"n-9f3a…","group":"r","cache_seconds":0}

- 没有或为空,就是默认分组。格式不对时与"uid 非法"同样处理:回 `auth_unavailable`,关闭码 4004。这是业务方的配置错误,不是 client 的错,所以让它退避重连。
- 回调结果缓存(`cache_seconds`)连同分组一起缓存,缓存键仍是 (app, token, verify_url)。**所以分组只能由令牌本身决定**;同一令牌想按别的条件给出不同分组,就得设 `cache_seconds=0`。
- 新 fp-im(第二步,见 §9)回调时会带请求头 `X-Fp-Im-Features: group`。业务方看到它才返回 `group`。

### 5.1 分组的在线策略

回调在返回 `group` 的同时,可以返回这个分组的在线策略:

    {"code":"success","uid":"1001","group":"web","conn_policy":"limit","conn_limit":5,…}
    {"code":"success","uid":"1001","group":"app","conn_policy":"replace",…}

- 取值与应用级策略相同:`replace`(新连接顶掉旧的)、`reject`(已有连接就拒绝新的)、`limit`(最多 `conn_limit` 条,满了拒新)。`limit` 时 `conn_limit` 取 1~100。
- 不返回时,沿用应用级策略(fp 控制台里配的 `im_conn_policy`)。
- 只对分组主体生效。默认分组仍只用应用级策略,现有行为不变。
- 格式不对(未知策略、`limit` 缺上限或越界)时,与分组名非法同样处理:`auth_unavailable` / 4004。
- 策略与分组一起进回调结果缓存。
- **策略按"这一次握手"执行**。握手脚本本来就是每次握手现传策略和上限(`registry.Conns.Handshake` 的 policy / limit 参数),所以换成按分组取值只是换一个参数来源,脚本不用改。由此带来一条约定:同一分组每次都应返回同一个策略。如果某次返回 replace,就会把这个 subject 已有的连接全部顶掉,不管之前的连接是按什么策略连上的。
- **不同分组是不同的 subject,互相不影响**。同一个用户 `b.web:1001` 开 5 个标签页,与 `b.app:1001` 的手机连接互不顶号。

为什么由回调返回,而不是在 fp 控制台按分组配置:分组本来就是回调决定的,策略跟着它走,业务方一处就能改全;放到 fp 控制台,就要给 fp 加表字段、页面和下发协议,而且业务方新加一个分组,得先去控制台登记才能用。

**client 不需要另带"我是哪一组"的字段。** 想以哪种身份连,本来就写在业务方令牌里:kkdesk 中继令牌的 `kind:"relay"` 就是这个标识,而且有签名,不能伪造。另加一个未签名的提示字段对业务方没有增量;同一令牌换个提示还会命中缓存里的旧分组,反而产生歧义。业务方要拒绝"没资格进这个分组"的连接,就在回调里按令牌类型判断,回拒绝码。

## 6. 在线索引

### 6.1 存储

每个(应用, 分组, fp-im 节点)对应一个 Redis set:

    fp:im:{<app>:grp:<group>:<node>}:idx      成员 = uid(不含 "b.<group>:")

- 只给非默认分组建索引。
- 一个节点只写自己的 key,写入不需要跨节点协调。每个 key 用自己的 hash tag,在 Cluster 下各落一个槽,每条命令只碰一个 key。
- **每次写入都同时 `EXPIRE`**,取连接表 field TTL(默认 30 分钟)。节点 id 每次启动都会换新,崩溃的节点没人续期,它那份最多 30 分钟后自己消失;在此之前读的一方按存活表跳过它(§6.4)。

### 6.2 写入:记脏 + 写当前状态

照搬 `Liveness` 写服务表的做法(锁里只记"变了",写协程写"现在是什么"):

1. `Hub` 新增按 (app, group) 的本地计数:`map[app\x00group]map[uid]int`,值是本节点上这个 subject 的连接数。由 `AddConn` / `RemoveConn` 在改连接表的同一把锁 `h.mu` 里维护。
2. 计数从 0 变成 1,或从 1 变成 0 时,把 (app, group, uid) 放进一个**脏集合**(map,重复标脏自动合并),然后非阻塞地唤醒写协程。
3. 每个节点有**一个**索引写协程。它在锁里取走整个脏集合,并读出这些 uid **此刻**的计数,出锁后逐个写:
   - 计数大于 0:`SADD idx uid` + `EXPIRE idx ttl`;
   - 计数为 0:`SREM idx uid`。
   - 同一 key 的几条命令放进一个管道。
4. 写失败时,把这批 uid 重新标脏,按退避(1s 起,翻倍,封顶 30s)重试。

为什么这样是对的:写到 Redis 的永远是写协程读到的那一刻的本地真实状态。写完之后再变,一定会重新标脏、再写一次。所以不管命令之间谁先谁后,最终都收敛到本地状态,不存在"旧操作覆盖新状态"。也不需要操作队列,不会溢出。

被 replace 顶掉的旧连接,由持有它的节点在自己的 `RemoveConn` 里减计数。同一个 subject 在两个节点上各有连接时(limit 策略,或 replace 顶号的瞬间),两个节点的集合里都有它,由读的一方去重。

### 6.3 整份重写

单条写失败有重试,但 Redis 重启、Cluster 主从切换都可能丢掉已经写成功的数据,而且不一定伴随节点频道的 Gap 信号。所以再加一次周期性的整份重写兜底:

- **时机**:
  - `renewLoop` 每个续期周期(默认 10 分钟)执行一次;
  - `reregistrar` 收到 Gap 时也执行。注意 `reregistrar.run` 在"存活条目仍在"时会提前 return,索引重写必须放在那次 return 之前,否则多数 Gap 都触发不了重写。
- **做法**:由同一个写协程执行。它在锁里取这个分组的 uid 快照,出锁后发 `MULTI { DEL idx; SADD idx …; EXPIRE idx ttl }`;快照为空时只发 `DEL`。
- **为什么不会覆盖更新的状态**:重写和 §6.2 在同一个协程里串行执行。快照之后发生的变化都已经标脏,会在这次重写之后再写一遍。
- 成本与本节点在这个分组的连接数成正比,和 `Renew` 一个量级。
- **优雅关闭**:`shutdown` 拆完全部 ws 之后,写协程把剩下的脏集合写完再退出,每步预算 10 秒,与现有关闭步骤一致。

### 6.4 读:ListGroup

1. 取存活节点列表(`Liveness.LiveNodes()`)。
2. 用一个管道对每个存活节点的索引 key 做 `SMEMBERS`,把结果合并去重。
3. 按 1000 个一批调 `LookupMany` 核实,同时滤掉死节点上的连接;**没有活连接的 uid 丢掉**。所以结果和 `SessionsMany` 一样精确,索引只是"去哪儿找"的提示。
4. 返回 `[]SubjectSessions`;同一 subject 在多个节点上的连接合并成一条。
5. 索引合并后超过 10000 个 uid 时报错"分组过大",不返回部分结果。到这个量级就该加分页了(§2)。

一致性边界(写进 SDK 注释):

- 读的是某一刻的快照,刚连上、刚断开的主体可能在、也可能不在结果里。
- 连接刚建立后,写协程写入之前有一个很短的窗口(正常是毫秒级);Redis 写失败时窗口是退避重试的时间。这期间这个主体不在结果里。要实时判断某一个主体在不在线,用 `Sessions`。
- 本节点存活视图把某个健康节点暂时剔除时(`Liveness.DropLocal`,最长约 3 秒),那个节点上的主体也不在结果里,与 `SessionsMany` 同一口径。

## 7. 接口

### 7.1 proto(`fpim.v1`)

```proto
message ConnectRequest {
  ...
  ListGroupRequest list_group = 8;
}

// ListGroupRequest 列出某分组此刻全部在线的主体。group 不能为空:默认分组不建索引。
message ListGroupRequest {
  string group = 1;
}

message Result {
  ...
  // ListGroup 的结果。与 sessions_many 分开放,调用方不用按请求类型猜字段含义。
  repeated SubjectSessions group_members = 6;
}
```

`Event.subject`、`PushRequest.subject` 等字段类型不变,只是多了 `b.<group>:<uid>` 这种写法。

### 7.2 server SDK

```go
func BizGroup(group, id string) Subject
func (s Subject) IsDefaultBiz() bool
func (s *Server) ListGroup(ctx context.Context, group string) ([]SubjectSessions, error)
var ErrUnsupported = errors.New("fpim: 对端 fp-im 不支持该请求")
```

- `Parse` 认 `b.<group>:<uid>`,并拒绝 §4 列出的写法。
- 旧版 fp-im 对 ListGroup 回"未知请求类型",SDK 把它映射成 `ErrUnsupported`,调用方据此回退到 `SessionsMany`。

### 7.3 client SDK

不变(§5)。

## 8. 校验与上限

| 项 | 规则 | 违反时 |
|---|---|---|
| 分组名 | `^[a-z][a-z0-9_]{0,15}$` | 握手:`auth_unavailable` / 4004;gRPC 请求:`error` 非空 |
| uid | 与现在相同(非空、≤128 字节、无空字节) | 同上 |
| 每个应用在单个节点上同时存在的分组数 | ≤ 32 | 新握手 `auth_unavailable` / 4004,并打警告日志 |
| 分组的 `conn_policy` / `conn_limit` | 见 §5.1 | 握手:`auth_unavailable` / 4004 |
| ListGroup 结果 | ≤ 10000 个主体 | `error` 非空 |

**分组数上限的作用和检查位置**:这个上限是为了防止业务方回调有 bug、每个连接回一个不同的分组,造出无数个索引 key。检查必须放在拿到分组之后、跑握手脚本之前(`wsapi/handler.go` 里 `resolveSubject` 与 `Conns.Handshake` 之间),读 hub 的 (app, group) 计数。放到 `AddConn` 里就晚了:握手脚本已经按 replace 把旧连接顶掉,这时再拒新连接,两条都没了。

## 9. 兼容与升级顺序

**默认分组零变化**:回调不返回 `group` 时,subject、key、事件、SDK 行为逐字不变。

**fp-im 分两个版本上线**:

| 步骤 | 内容 | 为什么 |
|---|---|---|
| 第一步 | 能解析、处理分组主体,能接 ListGroup;回调**不带** `X-Fp-Im-Features` | 业务 server 的 gRPC 流可能连在任意节点上。如果一上来就有分组主体,而流还落在旧节点,旧节点 `imgrpc` 的 `ParseSubject` 会拒掉对它的 Push / Kick / Sessions |
| 第二步 | 回调带上 `X-Fp-Im-Features: group` | 等全部节点都升到第一步之后再发。第二步滚动期间,有的节点发这个头、有的不发,同一个中继可能以两种写法出现(下面过渡期那一段),但每个节点都认得两种写法,不会出错 |

只靠请求头做不到一个版本内滚动升级:每个节点只知道自己支持分组,不知道全网是否都支持。

**业务方的顺序**:

- 旧 SDK 收到分组主体会解析失败,事件和上行被丢弃,所以必须先升级 SDK,再让回调返回分组。
- 业务方如果是多实例滚动发布,即使回调和 SDK 在同一个进程里,也不能一次发布同时改两样:新实例已经返回分组,事件却可能投到还没升级的旧实例上被丢掉。
- 所以业务方要加一个配置开关:全部实例升级完,再打开"返回 group"。返回 group 需要同时满足两个条件:这个开关已打开,请求里带了 `X-Fp-Im-Features: group`。

**过渡期一个主体有两种写法**(以 kkdesk 中继为例:`b:r-<id>` 和 `b.r:<id>`):

- 它们是两个 subject,replace 顶号不跨这两种写法。
- 业务方:
  - 推送先发新写法,返回 NotOnline 再发旧写法;
  - Kick、查连接两种写法都要做;
  - 判在线以 ListGroup 加旧写法的 `SessionsMany` 合起来为准。

**ListGroup 打到旧 fp-im 节点**:SDK 返回 `ErrUnsupported`,业务方回退到原来的做法。

## 10. 改动清单

**fp-im**

| 文件 | 改动 |
|---|---|
| `internal/im/model/subject.go` | `Subject.Group`、`BizGroup`、`IsDefaultBiz`、`String`、`ParseSubject`、新增 `ValidateGroup` |
| `internal/im/model/keys.go` | 索引 key 构造函数 |
| `internal/im/bizauth/bizauth.go`、`internal/im/auth/auth.go` | `verifyResponse` 加 `Group`、`ConnPolicy`、`ConnLimit`,验证结果把策略带出来;第二步时设请求头;校验分组名和策略;用 `BizGroup` 构造主体 |
| `internal/im/hub/hub.go` | (app, group) 计数与脏集合;`AddConn` / `RemoveConn` 维护它们;取快照的方法;`ForEachConn` 认分组主体。hub 不直接碰 Redis,写入只能通过注入的接口 |
| `internal/im/wsapi/handler.go` | 分组数上限检查(§8);调 `Conns.Handshake` 时,有分组策略就用它,没有才用 `appCfg.ConnPolicy` / `ConnLimit` |
| `internal/im/registry/`(新文件) | 索引写协程:SADD+EXPIRE、SREM、MULTI 重写、退避;ListGroup 的读取 |
| `internal/im/imgrpc/stream.go` | ListGroup 分支 |
| `proto/fpim/v1/im.proto`、`sdk/gen` | 新消息,重新生成代码 |
| `cmd/fpim/main.go` | 装配写协程;`renewLoop` 接上重写;`reregistrar.run` 在提前 return 之前重写;`shutdown` 里排空 |
| `sdk/im/subject.go`、`sdk/im/server.go` | `Parse`、`BizGroup`、`IsDefaultBiz`、`ListGroup`、`ErrUnsupported` |
| `docs/im.md` | 主体前缀表、回调响应约定、Kick 的主体写法、升级注意 |

**kkdesk**(在 kkdesk 仓库做)

| 文件 | 改动 |
|---|---|
| `services/server/internal/api/imverify.go` | `imAuthOK` 加 `Group`、`ConnPolicy`;中继分支按"配置开关打开且请求带 `X-Fp-Im-Features`"回 `{uid: node_id, group: "r", conn_policy: "replace"}`,否则照旧回 `r-<node_id>`。用 replace 而不是 reject:中继进程重启后要马上连回来,不能被还没超时的旧连接挡住 |
| `services/server/internal/relayfleet/auth.go` | `Authenticate` 返回裸 id,前缀和分组由调用方决定 |
| `services/server/internal/gateway/fpgateway.go` | `imServer` 接口加 `ListGroup`(连同编译期检查和测试替身);`relaySubject` / `relayFromSubject` 认两种写法;`idFromSubject` 补 `Group == ""`;按 §9 的过渡期规则推送和踢人 |
| 中继控制面 | 启动时和之后每 60 秒调一次 `ListGroup("r")`:校正本实例记的中继当前连接(后台的在线展示仍取节点每 10 秒的上报汇总:它本身就证明控制通道活着,比 60 秒一次的快照及时)。遇到 `ErrUnsupported` 只看旧写法 |

全部 fp-im 和 kkdesk 实例都切到新写法之后,删掉 `r-` 前缀的兼容代码。

## 11. 测试

- **`model` / `sdk/im`**:
  - `Parse` 与 `String` 往返一致;
  - `b.r:a:b` 解析为分组 `r`、uid `a:b`;
  - §4 列出的非法写法都被拒绝;
  - 默认分组写法不变;
  - `IsDefaultBiz`;
  - `internal/integration/im_parity_test.go` 的网关与 SDK 配对测试扩充输入集。
- **`bizauth`**:
  - 响应带 `group`、不带、格式非法三种情况;
  - 分组策略:不带时沿用应用级策略;`limit` 缺上限或越界、未知策略都拒绝;策略随缓存一起命中;
  - 缓存命中时分组不丢;
  - 第二步时请求带上请求头。
- **`hub`**:
  - 同一 subject 在本节点有两条连接,断一条不标脏,两条都断才标脏;
  - `ForEachConn` 能遍历到分组主体;
  - 分组数上限在握手脚本之前生效,被拒时旧连接没被顶掉。
- **索引写协程**:
  - 先 add 后 remove、remove 后紧跟 add、写失败后重试,最终 Redis 都与本地计数一致;
  - 重写之后的变化不会被重写覆盖;
  - 每次写入都带过期时间;
  - 快照为空时只 DEL。
- **`imgrpc`**:
  - ListGroup 合并去重;
  - 跳过死节点;
  - 用连接表核实,只在索引里、没有活连接的成员被去掉;
  - 超过 10000 个时报错。
- **`wsapi`**:同一个 uid,`web` 组按 limit 5 连 5 条都在、第 6 条被拒(4002);`app` 组按 replace 第二条把第一条顶掉(4003);两组的连接互不影响;没有分组策略时按应用级策略。
- **`internal/integration/im_e2e_test.go`**:
  - 两个节点,分组主体分别连在两个节点上,`ListGroup` 能拿全;
  - 杀掉一个节点后,它上面的主体从结果里消失,不用等 30 分钟 TTL;
  - Push、Kick 分组主体可用;
  - 旧写法与新写法的主体互不顶号。
- **Cluster**:索引 key 与连接表 key 各自单槽,沿用现有的"脚本只碰一个 key"架构测试(`registry/arch_test.go`)。

## 12. 取舍(评审时请核对)

1. **分组由业务方回调决定**,fp-im 不维护允许名单,也不需要在 fp 控制台配置;client 不带分组提示,身份就写在令牌里。代价是回调写错时,fp-im 只能靠格式校验和"每节点 32 个分组"兜底。
2. **只有业务方主体能分组**,默认分组不建索引。
3. **分组的在线策略由回调返回**,不在 fp 控制台配置;不返回时沿用应用级策略。
4. **每个节点一份索引**,以存活表判死,读时用连接表核实。没选 Centrifugo 那种全局 zset:那要另造一套过期判定,与连接表的死节点过滤是两套口径。
5. **索引写入用"记脏 + 写当前状态"**,失败退避重试,另外每 10 分钟整份重写兜底。不做逐条操作队列。
6. **ListGroup v1 一次返回整组**,上限 10000,不分页。
7. **fp-im 分两个版本上线**,业务方加配置开关;过渡期同一主体有两种写法,由业务方兼容。
