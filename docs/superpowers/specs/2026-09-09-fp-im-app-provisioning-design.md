# fp-im 的 app 配置由 fp 下发设计

**日期**：2026-09-09
**状态**：设计已定，待细化为实施计划
**上游**：`2026-09-03-fp-im-design.md`、`2026-09-05-fp-im-biz-auth-design.md`、`2026-09-08-config-file-yaml-design.md` 第十节

---

## 一、要做什么

fp-im 现在从一个本地 JSON 文件（`apps_file` → `internal/im/appcfg`）读接入应用的全部信息，
其中包括**每个 app 的 `app_secret` 明文**。这份文件要人手维护，与 fp 里那份应用清单
天然会漂移；而 fp 才是应用的唯一真相。

> **fp-im 不再持有任何业务应用的 secret，app 的准入与策略全部由 fp 下发。**

一条决定性的约束先摆在这里：**fp 只存 `app_secret_hash`（bcrypt），明文只在创建应用时
返回一次。** 所以"把 app 列表连同 secret 一起下发给 fp-im"物理上不可能，方案只能是
**fp-im 以自己的身份代各 app 向 fp 提问**。

## 二、明确不做

| 项 | 一句话理由 |
|---|---|
| `ListApps` 之类的全量清单 API | fp-im 只在 client 握手带上 app 时才需要那个 app 的信息，逐个懒查即可。有了全量 API 就要回答"谁能拿全量"，而这个问题不该存在 |
| 给 fp-im 建一条 `application` 记录 | 那是"不是应用的应用"：会话策略、登录方式、配置分区对它全是死字段。IM 凭据单独一张表 |
| 复用 fp 的授权模块（角色/权限点） | `user_role.user_id` 外键钉死主体是**用户**，`permission.application_id` 说权限点归属某个业务应用。"应用作为主体"与"对 fp 自身控制面的权限"在里面都没有表示 |
| 改 `ValidateTokenRequest/Response` 与 `RevokeEvent` | 一个字段都不用改，理由见第五、第八节。这是本设计最重要的性质——协议改动只有第七节那一个 oneof 分支 |
| 跨应用单点登录（拆掉 `sess.AppID != app.ID`） | 将来要做，但那是独立的一件事。本期在"token 与应用必须匹配"这条前提下设计 |
| 给 fp 的 gRPC 加 TLS | 沿用上一份 spec 的结论：fp 的 `grpc.NewServer` 没传 `grpc.Creds`，生产靠反代终结 |
| 按 `im_enabled` 过滤下发给 fp-im 的撤销事件 | 要为每条事件多查一次应用。多收几条无关撤销的代价只是 `byToken` 查不到、什么都不做 |

## 三、fp-im 的身份

fp-im 到 fp **只有一条 gRPC 连接**，连接级凭据里**没有 app_id**：

```
fp-app-secret:  <IM secret>
fp-caller-type: im
```

### 3.1 IM secret 单独存

```sql
CREATE TABLE im_credential (
    id          smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),  -- 单行表
    secret_hash text        NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
```

单行表（`CHECK (id = 1)`）而不是"随便一张表存一行"：约束写在 schema 里，不靠代码自觉。
控制台可以在线重新生成，**fp 不用重启**——它每次校验都读库。fp-im 需要带新凭据重启。

**轮换有一个 5 分钟窗口必须知道**：`appVerifier` 的成功缓存 TTL 默认 5 分钟
（`internal/grpcapi/server.go` 的 `AppSecretCacheTTL`），且只缓存成功。所以改完 secret：

- 新 secret **立刻**生效（没缓存过，直接走 bcrypt）
- 旧 secret **还能再用最多 5 分钟**

日常轮换无所谓；因泄露而轮换时要么等这 5 分钟，要么在改写时主动清掉该条缓存。
**实现时必须清**——因泄露而轮换却还留 5 分钟窗口是讲不通的。

### 3.2 `fp-caller-type` 是权威声明，不是提示

拦截器按它决定**只**比对哪一份凭据：

| `fp-caller-type` | 比对对象 | bcrypt 次数 |
|---|---|---|
| 空 / 不带（默认） | 该 app 的 `app_secret_hash` | 1 |
| `im` | `im_credential.secret_hash` | 1 |

**声明本身不授予任何东西**：声明 `im` 却没有 IM secret → 比对失败；持有 IM secret 却
声明为空 → 拿去跟 app 的 secret 比 → 失败。它只决定敲哪扇门，判定权全在 bcrypt。
所以让它权威（而不是"提示 + 两边都试"）在安全上零损失，却把最坏情况从两次 bcrypt
（各 50–100ms）砍到一次。

默认空值就是今天的行为，**所有已接入的 SDK 一个字节都不用改**。

它必须走 metadata 而不是请求体：拦截器要同时管一元与流式，而 `StreamServerInterceptor`
在拦截时还没有任何消息可读。

### 3.3 双向隔离，不是提权

| 凭据类型 | `GetAppIMConfig` / `VerifyAppCredential` | `ValidateToken` / `Watch` | `Login` / `SendLoginCode` / `Logout` | `ReportPermissions` / `GetPolicy` / `GetConfig` |
|---|---|---|---|---|
| `im` | ✅ | ✅ | ❌ | ❌ |
| 空（普通 app） | ❌ | ✅ | ✅ | ✅ |

**`type=im` 不是普通 app 的超集。** 关键是那条 `Login` 的 ❌：如果 im 能签发会话，
一份泄露的 IM secret 就等于能对**任意** app 冒充**任意**用户——那比"读到 app 配置"
严重一个量级。隔离之后 IM secret 的能力被压到"只能核实，不能签发"。

## 四、application 上的 IM 配置

```sql
ALTER TABLE application
  ADD COLUMN im_enabled       boolean NOT NULL DEFAULT false,
  ADD COLUMN im_conn_policy   text    NOT NULL DEFAULT 'replace'
      CHECK (im_conn_policy IN ('replace','reject','limit')),
  ADD COLUMN im_conn_limit    int     NOT NULL DEFAULT 5,
  ADD COLUMN im_allow_guest   boolean NOT NULL DEFAULT false,
  ADD COLUMN im_guest_ip_rate int     NOT NULL DEFAULT 20,
  ADD COLUMN im_biz_auth      jsonb;   -- NULL = 不支持业务方令牌
```

**`im_enabled` 默认关。** 一个应用在有人明确打开之前，连不上 fp-im——业务 server 接不进来，
client 握手也拒。

**`im_biz_auth` 用可空 jsonb 而不是铺平成三列**：`biz_auth` 是"整组有或整组没有"的东西，
铺平之后就得靠"`verify_url` 是不是空串"这种间接判断——`internal/im/model` 的 `BizAuth`
当初选嵌套正是为了避开这一点，SQL 的 NULL 恰好是同一个语义。内部形状与今天的 JSON 一致：

```json
{"verify_url": "https://…", "timeout": "2s", "cache_size": 10000}
```

控制台在应用管理页加一个 IM 面板，`im_enabled` 关着时其余项折叠。**保存时就校验**
（`verify_url` 必须 https、`conn_policy=limit` 时 `conn_limit >= 1`、`allow_guest` 时
`guest_ip_rate >= 1`），不要留到 fp-im 解析时才报——那时报错的是错的人。

### 4.1 关掉 `im_enabled` 不断开在线连接

| | 在线连接 | 新握手 |
|---|---|---|
| `im_enabled` 关 / `status=disabled` | **保持** | 拒（4002） |
| 重新打开 | —— | 恢复 |

这与今天 `status=disabled` 的行为**逐字相同**：`ApplicationService.SetStatus` 只改一列、
不发任何撤销，而 fp-im 对已建立的连接从不重新验证（"验证只发生在握手那一刻"）。
所以不需要"踢光这个 app"的事件，也不用给 hub 加任何新能力。

## 五、为什么 `ValidateToken` 一个字段都不用改

这是本设计的核心，值得单独一节。

`ValidateToken` 需要 app —— `sess.AppID != app.ID` 是安全边界，`app.Session` 还要用来算
过期、轮换与 `cache_ttl_ms`。而 app 今天来自 metadata 的 `fp-app-id`。

**metadata 里的 app_id 之所以看起来"钉死在连接上"，只是因为 `PerRPCCredentials` 把它塞在
那儿，不代表不能逐调用带。** fp-im 的做法是：

- 连接级凭据**不输出** `fp-app-id`（只有 secret 与 caller-type）
- 每次调用从 **client 的 ws 握手帧**里取 `app`，用 `metadata.AppendToOutgoingContext` 附上

于是服务端拿到的东西与普通 app 调用时**完全一样**，`AuthService.ValidateToken` 一行不改，
`sess.AppID != app.ID` 那条检查照旧在 fp 侧执行，安全边界一寸没挪。

`Watch` 同理：流建立时把 app_id 附在该流的 metadata 上。

## 六、两个新 RPC

都只对 `type=im` 开放，**作用域一律来自 metadata**，请求体里不再重复 app_id——
两处都有就要回答"不一致算谁的"，而这个问题不该存在。

```protobuf
service IMGatewayService {
  // GetAppIMConfig 返回 metadata 中那个应用的 IM 配置。
  rpc GetAppIMConfig(GetAppIMConfigRequest) returns (GetAppIMConfigResponse);
  // VerifyAppCredential 核实 secret 是否为 metadata 中那个应用的 appSecret。
  rpc VerifyAppCredential(VerifyAppCredentialRequest) returns (VerifyAppCredentialResponse);
}

message GetAppIMConfigRequest {}
message GetAppIMConfigResponse {
  bool   allow_guest    = 1;
  string conn_policy    = 2;   // replace / reject / limit
  int32  conn_limit     = 3;
  int32  guest_ip_rate  = 4;
  BizAuth biz_auth      = 5;   // 不设表示不支持业务方令牌
}
message BizAuth {
  string verify_url  = 1;
  int64  timeout_ms  = 2;
  int32  cache_size  = 3;
}

message VerifyAppCredentialRequest { string secret = 1; }
message VerifyAppCredentialResponse {}
```

`GetAppIMConfig` 在应用不存在 / `status=disabled` / `im_enabled=false` 时返回
`FailedPrecondition`，fp-im 映射成关闭码 4002。

### 6.1 `VerifyAppCredential` 的检查顺序

**先 bcrypt 验凭据，再看 `im_enabled`。** 顺序反了的话，一个手里没有 `app_secret` 的人
也能探出某个 app 有没有开 IM。而凭据验过之后再告诉它"IM 没开"是安全的——能走到这一步
的人本来就持有那份 secret，而且运维**需要**这个区分才知道去翻开关：

| 情况 | 返回 |
|---|---|
| app 不存在 / secret 错 / `status=disabled` | `Unauthenticated`（不区分，防 appId 枚举，沿用 `VerifySecret` 现有纪律） |
| 凭据有效但 `im_enabled=false` | `FailedPrecondition`（明确指向那个开关） |

## 七、`WatchResponse` 的唯一新增

IM 配置变更要能推给 fp-im，替掉今天 `appcfg` 那套 mtime 轮询。`WatchResponse` 的 oneof
加一个分支：

```protobuf
message AppIMConfigChanged {
  string app_id = 1;
}
```

向后兼容：proto 注释里本来就写着"旧 SDK 遇到不认识的分支会落到 default，忽略即可"。

**这是本设计对协议的全部改动。** `ValidateToken`、`RevokeEvent` 都不动。

## 八、撤销的扇出：一处会静默失效的地方

`RevokeEvent` 已经带 `app_id`，且**空串表示跨全部应用的撤销**（改密、冻结）——proto 与
SDK 的 `RevokeEvent.AppID` 都已经有这个字段与语义，不用改。

fp 侧只改一处：`RevokeHub` 对 `type=im` 订阅者注册成通配，`fanout` 不按 app 过滤。

**fp-im 侧有一个必须显式补的洞。** 今天 fp-im 每个 app 一个 client，一条"空 app_id"的
跨应用撤销会被 **N 个客户端各收一份**，每个用自己闭包里的 app 调
`OnRevoked(app, tokens)`——扇出是靠连接数量天然做到的。收敛成一条流之后只收到**一份**：

```
ev.AppID != ""  → OnRevoked(ev.AppID, tokens)
ev.AppID == ""  → 对本节点持有连接的每个 app 各调一次
```

照抄今天的写法（`OnRevoked(ev.AppID, tokens)`）会拿空串去查 `byToken`
（键是 `app + "\x00" + token`），一条也查不到——**改密码、冻结用户之后 ws 全都不会被关，
而且零报错、零日志**。这属于"删掉也不会让任何现有测试变红"的那类，必须配一条集成测试
钉死。

## 九、调用流程

### 9.1 业务 server 接入 fp-im

```
业务 server ──gRPC Connect──▶ fp-im                    fp
              md: app-id=X, app-secret=secretX
                              │
                              │ streamAuth 拦截器：本地已无任何 secret
                              │ VerifyAppCredential(secretX)
                              │   md: app-id=X（逐调用附上）
                              │       app-secret=IM secret
                              │       caller-type=im
                              ├──────────────────────────▶
                              │                          bcrypt(secretX vs X.hash)
                              │                          再查 status / im_enabled
                              ◀──────────────────────────┤
                              │ AddStream(X) → 首条流 SetServing(X,true)
```

每条业务 server 流一次（`StreamServerInterceptor`），不是每条消息一次。

**这里新增了一个依赖**：fp 不可达时业务 server 接不进 fp-im，而今天它只比对本地文件。
缓解：fp-im 对"secretX 验过了"这个事实加一层**只缓存成功**的缓存（键
`app_id + ":" + sha256(secret)`，与 fp 的 `appVerifier` 同一套推理——缓存失败等于把 map
大小交给调用方）。TTL 取 5 分钟，与 fp 侧一致。

### 9.2 client 用 fp token 握手

```
client ──ws──▶ fp-im                                    fp
   {"t":"auth","app":"X","token":"…"}
               │
               │ ① 取 X 的 IM 配置（本地缓存未命中才回源）
               │    GetAppIMConfig()  md: app-id=X, caller-type=im
               ├────────────────────────────────────────▶
               │   不存在/disabled/未启用 → 4002；fp 不可达 → 4004
               │ ② Auth().Validate(ctx, token)  md: app-id=X, caller-type=im
               ├────────────────────────────────────────▶
               │   fp: activeApp(X) → Sessions.Validate(token, X)
               │       sess.AppID != app.ID → 拒（这条检查仍在 fp 侧）
               ◀────────────────────────────────────────┤
               │ ③ subject = u:{user_id}，按 ① 的 conn_policy 登记
```

**① 必须在 ② 之前**：`im_enabled` 关着时该给 **4002**（策略拒绝，别重连），不是 **4001**
（认证失败，去重新登录）。token 可能完全有效，把 client 指去重新登录是错的方向——它
登录完还是连不上，变成死循环。

稳态下回源 **0 次**：`GetAppIMConfig` 的结果缓存在 fp-im 进程内，`ValidateToken` 的结果由
`fpsdk.Client` 按 fp 下发的 `cache_ttl_ms` 缓存。

### 9.3 访客握手

`{"t":"auth","app":"X","guest":"<uuid v4>"}` —— **全程不碰 `ValidateToken`**，但仍然需要
① 那次 `GetAppIMConfig` 才知道 `allow_guest` 与 `guest_ip_rate`。这就是那个 RPC 必须存在
的原因：这条路上没有任何 token 可以顺带把配置带回来。

### 9.4 为什么 9.1 与 9.2 各自都要判 `im_enabled`

**它们根本不在同一台机器上。**

```
          ┌─── 节点 A ───┐   业务 server(X) 连在这，9.1 跑过
          └──────────────┘
          ┌─── 节点 B ───┐   client(X) 被 LB 分到这，9.1 从未发生
          └──────────────┘
```

`fp:im:srv:{X}` 这张表与整套跨节点转发之所以存在，就是因为业务 server 只连在**部分**
节点上，而 client 的 ws 落在**所有**节点上。

而 9.2 内部那两次（fp-im 的缓存 + fp 的 `activeApp`）也不是重复：**前者读自己的缓存，
后者读库**。只留前者的话，"关掉后不再允许新连接"就依赖 fp-im 缓存的新鲜度——而 fp 自己
的设计里就承认推送会漏（`WatchPurge` 存在的全部理由就是"Redis 订阅重建时会漏读事件且
不知道漏了哪些"）。漏一次推送这个开关就悄悄失效。fp 侧那次的代价是零：`activeApp` 本来
就要读那一行查 `status`，`im_enabled` 在同一行上。

## 九点五、SDK 侧的改动

`fpsdk.Options` 今天硬性要求 `AppID != ""`，而 fp-im 的连接**没有**固定 app。所以：

| 改动 | 说明 |
|---|---|
| `Options.CallerType`（新增，默认空） | 设成 `"im"` 时：`validate()` 放行空 `AppID`；`appCredentials.GetRequestMetadata` 不再输出 `fp-app-id`，改为输出 `fp-caller-type: im` |
| 逐调用带 app 的入口 | `Auth().Validate(ctx, token)` 的 `ctx` 本来就透传给 RPC，所以调用方用 `metadata.AppendToOutgoingContext` 即可，SDK 不必新增方法 |
| `Client` 暴露 `GetAppIMConfig` / `VerifyAppCredential` | 挂在同一条 `ClientConn` 上，与 `cfgRPC` 同一写法 |

`Auth.Validate` 那层 LRU 缓存**天然安全**：键是 token，token 全局唯一，一个 client 服务
所有 app 不会串。

**`CallerType` 为空时行为与今天逐字相同**，已接入的业务方 SDK 无需任何改动。

## 十、fp-im 侧的改动

| 动作 | 对象 |
|---|---|
| 删除 | `internal/im/appcfg` 整个包、`config-im.yaml` 的 `apps_file`、`model.AppConfig.AppSecret` |
| 新增 | 一个从 fp 拉配置的 `auth.AppConfigSource` 实现（含本地缓存 + Watch 刷新） |
| 改 | `fpauth`：`clients map[string]*fpsdk.Client` → **一个** client，启动时建 |
| 改 | `fpauth` 的 `OnRevoke`：app 从 `ev.AppID` 取，空串时扇给所有 app（第八节） |
| 改 | `imgrpc.streamAuth`：本地明文比对 → `VerifyAppCredential` + 成功缓存 |
| 改 | `config-im.yaml`：`fpsdk` 段去掉 `app_id`，加 `secret` |

`config-im.yaml` 的最终形态：

```yaml
fpsdk:
  addr: localhost:9090
  secret: <IM secret>     # 控制台生成
# apps_file 删除
```

**一个顺带的好处**：收敛成一条连接之后，"未认证的 ws 握手能按任意 app 名触发拨号"这个
放大攻击面直接消失——fp-im 启动时建好唯一那条连接，任何 app 字符串都不会产生新连接。

## 十一、测试要点

1. **`type=im` 不能 `Login`** —— 这条是 IM secret 泄露时的爆炸半径边界
2. **`type=im` 不能读别的分区**：`GetConfig` / `GetPolicy` / `ReportPermissions` 一律拒
3. **普通 app 不能调两个新 RPC**
4. **`fp-caller-type` 权威**：持 IM secret 但不声明 → 失败；声明 im 但拿 app secret → 失败
5. **`VerifyAppCredential` 的顺序**：secret 错时不泄露 `im_enabled`（两种情况同一个错误）
6. **`im_enabled=false` 时 `ValidateToken` 拒**（即使 fp-im 的缓存是脏的）
7. **关掉 `im_enabled` 不断开在线连接、只拒新握手**
8. **跨应用撤销（`ev.AppID == ""`）确实关掉了多个 app 的 ws** —— 第八节那个洞，必须是
   集成测试（同一个测试里起两个 app 的连接，发一条空 app_id 的撤销，断言两边都断）
9. **逐调用 app_id**：同一条连接上连着为两个不同 app 验 token，各自拿到正确结果，
   且 A 的 token 拿到 B 的 app 上验必须失败
10. **IM secret 轮换后旧 secret 立即失效**（缓存被主动清掉，不是等 5 分钟）

## 十二、迁移与上线顺序

1. 迁移编号 `00010`（`00009_config_yaml.sql` 必须先落地）
2. 先发 fp：新列默认 `im_enabled=false`，对所有现存应用都是关的；新 RPC 无人调用；
   拦截器对不带 `fp-caller-type` 的调用行为不变 —— **对现网零影响**
3. 控制台建 IM 凭据，给需要 IM 的应用逐个打开 `im_enabled` 并配好参数（值照抄现在的
   `im-apps.json`）
4. 再发 fp-im：带 IM secret 启动，删掉 `apps_file`

第 2 步与第 4 步之间可以停留任意久：这期间 fp 是新的、fp-im 是旧的，旧 fp-im 仍然读它的
本地文件，两边互不干扰。**没有必须同时发布的窗口。**
