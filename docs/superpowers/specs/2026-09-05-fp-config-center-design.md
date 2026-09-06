# fp 配置中心模块设计

**日期**：2026-09-05
**状态**：设计已定，待细化为实施计划
**上游**：`2026-08-24-fp-foundation-platform-design.md` 第六节

---

## 一、要做什么

对症 `3s/core/config/config.go` 那 335 行编译期 Go 变量——改一个费率要重新编译发版。

> **代码声明结构，控制台填值，fp 推变更。**

代码里的 struct 说"我需要哪些配置项、什么类型"，控制台说"值是多少"。

## 二、明确不做

| 项 | 一句话理由 |
|---|---|
| SDK 上报 schema | 配置项一律人建；`Bind` 的错误清单代替它告知"该建哪些 key"。后加是纯增量 |
| 默认值 | 密钥类必然要人配，启动本来就卡在这一步；且有默认值时改字段名会静默回落，事故无声 |
| 环境维度（dev/test/prod） | 与"一个 fp 部署 = 一套用户体系"矛盾，环境必须靠部署隔离 |
| 本地快照文件 | **代价：fp 不可达期间业务方进程无法重启** |
| secret 类型与落库加密 | **配套纪律：密钥类必须建在 `DEFAULT` 分区**，否则会随前端配置吐到浏览器 |
| min / max / oneof 校验 | 只校验类型转换。范围与枚举由业务方在 `Bind` 之后自己判 |
| 灰度 / 按标签定向下发 | 用「仅落库、重启生效」覆盖滚动发布这个主场景 |
| `actor_id` | 今天只有一个管理员（`AdminService` 没有创建第二个的路径），这一列恒为同值 |

后五条推翻了主设计文档 §6 与 §10.3。前两条（快照、secret）是"fp 挂了业务不挂"这条承诺与安全面的实质削弱——**主设计 §6 应当同步更新**，否则下一个人读到两份互相矛盾的设计。

另记一条与授权模块的不对称：**权限点由 SDK 上报，配置项由人手建**。不是原则分歧，是本期取舍。

## 三、数据模型

```sql
CREATE TABLE config (
    application_id uuid        NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    type           text        NOT NULL,   -- DEFAULT | WEB，分区
    seq            bigint      NOT NULL,   -- 分区内自增：v1 v2 v3
    fields         jsonb       NOT NULL,   -- {key: {type, desc, value}}
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, type, seq)
);
```

```json
{
  "upstream.timeout": {"type": "int",    "desc": "上游超时（毫秒）", "value": 3000},
  "fee_rate":         {"type": "float",  "desc": "手续费率",        "value": 0.02},
  "risk.mode":        {"type": "string", "desc": "",                "value": null}
}
```

- **未配置** = `value` 是 JSON `null`；**已删除** = key 不在 map 里。两个状态靠 map 本身表达，不要额外的列
- 一次保存 = 一行 = 一个完整版本。**回滚 = 复制一行**，`type` / `desc` / `value` 一起回去
- 只保留最近 100 版
- `seq` 在事务里取该分区的 `MAX(seq)+1`，靠主键约束兜并发

**`type` 是分区，不是标记。** 主键含它，所以同名 key 在 `DEFAULT` 和 `WEB` 下是两个独立的配置项，各有各的值与版本序列。`site.title` 前后端都要用就配两遍。`WEB` 分区会被下发到浏览器，`DEFAULT` 不会。分区可扩展（将来 `MOBILE` 等），SDK 的 API 收 string 不收 enum。

```sql
-- 当前配置
SELECT seq, fields FROM config WHERE application_id=$1 AND type=$2 ORDER BY seq DESC LIMIT 1;
-- 回滚到 v6
INSERT INTO config (application_id, type, seq, fields)
SELECT application_id, type, $3, fields FROM config WHERE application_id=$1 AND type=$2 AND seq=6;
-- 某个 key 的历史
SELECT seq, fields->$3, created_at FROM config
WHERE application_id=$1 AND type=$2 AND fields ? $3 ORDER BY seq DESC;
-- 修剪
DELETE FROM config WHERE application_id=$1 AND type=$2 AND seq <= $3 - 100;
```

## 四、类型系统

| fp 类型 | Go 侧映射 | 控制台控件 |
|---|---|---|
| `bool` | `bool` | 开关 |
| `int` | 各整数类型；`time.Duration` **按毫秒**当整数 | 数字输入 |
| `float` | `float32` / `float64` | 数字输入 |
| `string` | `string` | 文本框 |
| `array` | 切片、数组 | JSON 数组 |
| `object` | `map`、标了 `fp:"json"` 的 struct | JSON 对象 |

- **没有 `duration` 类型**——其他语言没这概念。`time.Duration` 是 Go SDK 的私事：按毫秒当 `int` 处理，解析时乘 `time.Millisecond`
- 反射时**先判具体类型再判 Kind**：`time.Duration` 底层是 `int64`，顺序反了会丢掉毫秒换算
- 不支持的类型（指针、interface、chan、func）在 `Bind` 时直接报错，不静默跳过
- `value` 存 JSON 原生类型。保存时按该项的 `type` 转一次，**转不过去才报错**（`"3"` 填进 `int` 照样过）
- 类型不下发给 SDK——它直接 `json.Unmarshal` 到 struct 字段的类型
- **类型随时可改**，走「仅落库、重启生效」；误选「立即推送」时旧实例解析失败，走下面第五节第 3 条兜底

## 五、SDK

```go
// ① 后端自己用的：绑 struct，读 DEFAULT 分区
cfg, err := fpsdk.Bind[ShopConfig](client)
var miss *fpsdk.MissingConfigError
if errors.As(err, &miss) { log.Fatalf("配置未配齐：%v", miss.Keys) }

c := cfg.Load()                                   // *ShopConfig，原子快照
cfg.OnChange(func(old, new *ShopConfig) { ... })
cfg.OnError(func(err error) { ... })

// ② 转发给前端的：拉某个分区的全量，不绑 struct，没有缺值概念
web, err := fpsdk.BindType(client, "WEB")
web.Load()                                        // map[string]any
```

```go
type ShopConfig struct {
    FeeRate  float64                    // fee_rate
    Upstream UpstreamConfig             // 嵌套 struct = 分组，展开成 upstream.*
}
type UpstreamConfig struct {
    Timeout time.Duration               // upstream.timeout
    APIKey  string                      // upstream.api_key
}
```

key 从字段名推导为 snake_case，嵌套用点连接。**唯一的 tag 是 `fp:"json"`**：标了它的 struct 整体当一个 `object`，填一段 JSON。

`Load()` 返回**跨字段一致的快照**——一次请求内所有字段来自同一个版本。缺失的字段留 Go 零值，`cfg` 照样返回，带不带伤启动由业务方决定。

`BindType` **必须指定分区**，没有"不传就是全部"的重载（分区间同名 key 是不同配置项，合并就要回答"撞了算谁的"）。返回 `map[string]any` 而非 `map[string]string`，业务方 `json.Encode` 出去直接是正确的 JSON 类型。前端怎么拿是业务方的事——挂个 handler 吐 `web.Load()`，或挂 `OnChange` 用 WS 推。**SDK 不提供 HTTP handler**，也不让前端直连 fp（fp 是全局单点，前端流量量级完全不同）。

### 实现约束

1. **快照没变就不换指针、不触发 `OnChange`** —— 同分区里别人改了你不关心的 key 也会推给你，不比较的话别人配一次你的连接池重建一次
2. **某个 key 消失（被删或回滚）→ 保持旧值 + `OnError`**，绝不清成零值（`fee_rate=0` 就是免手续费）
3. **解析失败 → 保持旧快照 + `OnError`**，绝不崩、绝不半解析
4. 没挂 `OnError` 时打 ERROR 日志，不静默
5. **收到 Watch 流的 `ready` 就重拉一次配置**——断线期间发布的 `ConfigChanged` 一条都收不到，光靠下一次变更来补会让配置无限期停在旧值。这是配置侧对应 `WatchPurge` 的那条兜底，只是配置不需要"丢弃全部"，重拉即可
6. `MissingConfigError` **一次列全并带类型**——它是人在控制台建配置项的唯一依据，漏一项就要多跑一轮"起→失败"

```
fpsdk: 3 个配置项尚未在 fp 上配置，请到控制台【商城 / 配置中心 / DEFAULT】创建并填值：
  upstream.api_key   (string)
  upstream.timeout   (int)
  fee_rate           (float)
```

首次部署：**起一次 → 失败 → 照着清单建好填值 → 再起 → 活**。

## 六、协议

推送走已有的 Watch 长流（`WatchResponse` 的 oneof 早就预留了扩展位）。

```protobuf
service ConfigService {
  rpc GetConfig(GetConfigRequest) returns (GetConfigResponse);
}

message GetConfigRequest  { string type = 1; }        // DEFAULT | WEB
message GetConfigResponse {
  int64  version = 1;                                  // config.seq
  string values  = 2;                                  // JSON 对象 {key: value}，只含已配置的项
}

// WatchResponse 的 oneof 新增分支。一次保存只动一个分区，所以 type 是单值。
message ConfigChanged { string type = 1; int64 version = 2; }
```

`values` 刻意不拆成 `map<string,string>`——值是 JSON 原生类型，拆开会退化成待解析的字符串，SDK 就得自己实现一遍类型转换。`missing` 由 SDK 自己算（struct 里有、`values` 里没有的），服务端不知道调用方的 struct 长什么样。

## 七、生效方式

一次保存 = 一个版本 = 推一次，SDK 看不到中间态。没有草稿态。

```
[保存]  生效方式：  ◉ 立即推送（默认）
                   ○ 仅落库，实例重启后生效
```

后者就是**不发 `ConfigChanged`**。它解决的是：同一个 key，线上 v1 要 `xxx`、即将发布的 v2 要 `bbb`，而发布前就得改值。

```
选「仅落库」→ v1 pod（运行中）内存里仍是 xxx，不受影响
             v2 pod（新启动）Bind 拉到 bbb  ✓
```

**已知漏洞**：v1 pod 若在此期间因节点故障重启，会读到 `bbb`。窗口短，认了。

回滚同样要选生效方式。改类型（第四节）走的也是这条路。

**改 key** 是安全的：新 key 不存在 → `Bind` 报错 → 发布失败，不会静默跑错值。做法是发版前先建好新 key 填值，旧 key 全量上线后手动删（没有对账会提醒你）。

**改语义**（key 和类型都没变，单位从秒改毫秒之类）fp 检测不到，是纪律不是机制：**不兼容的语义变更必须换 key**，与数据库改列语义要新开一列同理。

## 八、控制台

- 配置中心页，先切 `DEFAULT` / `WEB` 分区 tab（版本序列、回滚、推送都是分区内的事）
- 配置项列表：按 key 点前缀折叠分组，按类型渲染控件，顶部「N 项未配置」红色提示
- 每行可编辑：值、`desc`、`type`（改类型二次确认，提示"旧实例若收到推送会解析失败"）
- `[新建配置项]`：key + 类型 + 值 + 备注（分区由当前 tab 决定）。**必须同时填值**
- `[删除]`：二次确认，提示"没有机制能确认它是否还被代码读取"
- 版本历史 tab：列表 + 两版 diff + 回滚。"改了哪些"取相邻两版 `fields` 在应用层 diff，不存字段
- **回滚前提示"回滚后这些项将变成未配置"**——v7 才新增的项在 v6 里没有

**不复用 `DynamicForm`**：结构差异太大，且第三阶段终审查出它的 DOM id 没加命名空间会让同名字段串台——配置项的 key 带点，撞得更狠。新组件从第一行起就带命名空间。改前端工程配置前先读 `docs/console.md` 那六条坑。

## 九、接触面

| 位置 | 改动 |
|---|---|
| `proto/fp/v1/` | 新增 `config.proto`；`WatchResponse` 的 oneof 加 `ConfigChanged` |
| `internal/domain/` | `Config`、`ConfigField`（`fields` 里那个对象的 Go 结构） |
| `internal/store/migrations/` | 新增迁移（一张表） |
| `internal/service/` | `ConfigService`：取值、保存、回滚、删除、修剪、推送 |
| `internal/grpcapi/` | `ConfigService` 的 gRPC 实现；推送接入既有 Watch 流 |
| `internal/httpapi/` | 控制台的配置中心路由 |
| `sdk/` | `config.go`（反射、绑定、快照、回调）、`bindtype.go` |
| `web/src/pages/` | 配置中心页与版本历史 |

**不引入新依赖**（反射与 `encoding/json` 都在标准库），`sdk/arch_test.go` 的分层约束不受影响。

## 十、测试策略

标「辨别力」的实现时必须做**变异验证**——把实现改坏跑一次确认真的变红，再改回来。

1. **反射生成 key 与类型**（表驱动）：各 Go 类型 → fp 类型；嵌套 struct 展开成点分组；`fp:"json"` 整体成 object；不支持的类型报错。
   **辨别力**：`time.Duration` 必须断言毫秒换算，只断言"类型是 int"的话，先判 Kind 的错误实现照样绿。

2. **`MissingConfigError` 列全且带类型。**
   **辨别力**：至少缺 2 项，断言两项都在**且类型正确**——只缺 1 项的话"报第一个就 return"会绿；不断言类型的话人拿着清单会建错类型。

3. **快照相同不触发 `OnChange`**：推一次内容完全相同的配置，断言回调 0 次、指针未变。

4. **「仅落库、重启生效」。**
   **辨别力**：两条腿都断言——没有 `ConfigChanged` 发出，**且**新起一次 `Bind` 能拿到新值。只断言前者的话"根本没存"会绿。

5. **改类型的两条路**：某项从 `int` 改成 `object` 并填 JSON——
   - 选「仅落库」→ 旧 `Bind` 的 `Load()` 仍是原 int，`OnChange` / `OnError` 都没触发
   - 选「立即推送」→ 旧 `Bind` 的 `Load()` **仍是原 int**、`OnError` 触发一次、**进程没崩**

   **辨别力**：第二条必须断言"值还是旧的"而不只是"报了错"——半解析后换掉其余字段的实现照样会报错，却已破坏快照一致性。

6. **分区隔离**：两个分区建同名 key、配不同的值，断言各拿各的；改一个分区不影响另一个的 `seq`；`ConfigChanged{type:"WEB"}` 不会让 `Bind` 那份重新解析。
   **辨别力**：两个分区的值必须**不同**，否则"分区对了"和"压根没分区"同结果。

7. **`BindType` 解析**：断言 bool / array / object 解析成真正的 JSON 类型而非字符串。

8. **未配置 vs 已删除**：`value` 为 JSON `null` 的项进 `missing` 且仍在列表中；key 移出 `fields` 的两处都不出现。
   **辨别力**：两种情形必须同时造出来，只造一种的话把两者混为一谈的实现会绿。

9. **版本可精确还原**：造 v1..v5（含一次删除、一次重新添加、一次改类型），逐版本断言 `fields` 与写入时一致；回滚 v3 生成 v6，断言 v6 与 v3 逐字节相同且 v4/v5 仍在。

10. **修剪**：造 105 个版本，断言 v1..v5 被删、v6..v105 还在、当前配置不受影响。

11. **弱类型转换**：`"3"` 存进 `int` 成功、`"abc"` 失败并给出可读错误。

12. **收到 `ready` 重拉配置**：断开重连后，断言 `Load()` 拿到了**断线期间**发生的那次变更，即使重连后没有任何新的 `ConfigChanged`。
    **辨别力**：变更必须发生在断线**期间**——断线前改（重连前就拉到了）或重连后改（有 `ConfigChanged` 推送）都测不到这个缺口。

13. **端到端穿透**：控制台改值 → 断言 SDK 侧 `cfg.Load()` 与 `web.Load()` 真的变了。对应第三阶段的教训——只测服务端返回值不够，要穿到 SDK 出口。

## 十一、实施顺序

1. 建表与迁移
2. 服务端：取值、保存、版本、回滚、删除、修剪
3. proto + gRPC：`GetConfig`、`WatchResponse` 加 `ConfigChanged`
4. SDK：反射生成 key 与类型、`Bind`、`MissingConfigError`
5. SDK：热更新、`OnChange`、`OnError`、快照比较
6. SDK：`BindType`
7. 服务端：推送与生效方式
8. 控制台：分区 tab、列表与表单、新建与删除
9. 控制台：版本历史与回滚
10. 端到端穿透测试

1–7 完成时就能验证"改配置不用发版"（用 SQL 直接改 `config` 表即可），不必等控制台。

---

## 附：实施后的交接事项

配置中心实施完毕：**82 个 commit**，手写约 13,256 行 / 91 个文件（不含 `sdk/gen/` 的 protobuf 生成产物与两份设计文档）。15 个任务各自通过独立审查，触发 15 轮任务级修复；全分支终审判定「可以合并，需先修 1 处」，修复波次 4 个 commit 后复审全部核销。

交付时状态：`./scripts/test.sh` 退出码 0（26 个包），前端 15 files / 112 tests 全绿，`tsc -b` 干净，`gofmt -l` 无输出，`./scripts/gen.sh` 后 `git status` 无变化。

### 一、合并前必须知道的三件事

**1. 主设计文档 §6 / §10.3 / §11 已被本模块改写。** §10.3 原本承诺"fp 不可用时配置使用本地快照文件，业务不中断"——**这条承诺已被撤销**。现在的准确表述是：fp 不可达期间**无法启动新进程**，已在跑的进程不受影响（值在内存里）。别的模块若曾依赖那条降级契约来论证自己的可用性，需要重新评估。

**2. 迁移编号被改过。** 本分支的迁移是 `00008_config.sql`，不是最初的 `00007`——`main` 的 authz 已经占了 00007。goose 按版本号记账，同号会让其中一条被**永久静默跳过**。这个坑在测试库里真实发生过：`config` 表一度只因为某次实现的 workaround 手工建出来而存在，迁移本身从未执行。**将来任何并行分支加迁移前，先看一眼 main 上的最大编号。**

**3. `WatchResponse` 的 oneof 字段号已占到 6。** `revoke=1` / `ready=2` / `purge=3` / `policy_changed=4`（authz） / `user_role_changed=5`（authz） / `config_changed=6`。proto 字段号一经发布不可改动、不可重用。

### 二、留给下一阶段的待办（按优先级）

1. **控制台只能看到最近 20 版，服务端保留 100 版。** `internal/httpapi/config.go` 把 `limit` 硬编码成 20，导致 `ConfigService.ListVersions` 的 `limit` 参数成了死参数，且它的钳制语义反直觉（`limit<=0 || limit>100` 一律改成 20，传 500 被砍到 20 而不是 `min(limit,100)`；同一文件的 `checkConfigType` 对非法输入是显式报错，这里却静默改写）。v21..v100 保留着但控制台够不到、回滚不了。两头一起改。

2. **`specsOf` 不检测 snake_case key 碰撞。** `type C struct { APIKey string; ApiKey string }` 会推导出两个 `api_key`，缺失清单里把它列两遍，人在控制台建一个、两个字段一起被填上，全程无任何迹象。`specsOf` 已经按 key 升序返回，排序后扫一遍相邻项即可，约五行。

3. **`newConfigServer` 对 nil `Configs` 无守护。** `internal/grpcapi/config_service.go` 直接 `s.cfgs.Current(...)`，`Deps.Configs` 为 nil 时一次 `GetConfig` 会 nil 解引用**崩掉整个 gRPC 进程**。同包的 `authServer.requireAuthz()` 为完全同类的可选依赖建了 `Unimplemented` 守护，理由写得很清楚："一个可选功能没配，不该把认证也一起带走"。生产与三处测试 env 都装配到位，所以是埋雷不是活 bug——但两种依赖两种标准。

4. **`MissingConfigError` 的文案没带分区。** 设计文档的样例是「请到控制台【商城 / 配置中心 / **DEFAULT**】创建并填值」，实现是「请到控制台创建并填值」。应用名 SDK 确实不知道，但分区（`b.typ`）知道。这份清单是人建配置项的唯一依据，少一个分区名就多一次猜。

5. **`fetchConfig` 用 `context.Background()`**，既无超时也不受 Client 生命周期 ctx 约束。同一个 SDK 里 `refreshPolicy(ctx)` 用生命周期 ctx、`Options.ValidateTimeout` 给校验留了旋钮——配置这条什么都没有。grpc 层的 `minConnectTimeout` 兜住了最坏情况（数十秒而非无限），`Close()` 也能靠 `conn.Close()` 中断在途 RPC，所以不是挂死，但它是第三种超时策略。

6. **`Binding[T]` 与 `TypeBinding` 有约 40 行近乎逐字重复**（`raise` / `reloadFromPush` / "DeepEqual 比较→换指针→取回调→调用"那段骨架）。回调签名不同让泛型抽不干净，但把 `mu`/`onError`/`raise` 抽成一个内嵌小 struct 是干净的。
   **反例**：`ConfigHub` 与 `RevokeHub` 骨架也高度重复，但两者溢出策略真的不同（摘订阅者 vs 丢事件+purge 才摘），**不要**强行抽取。

7. **`docs/console.md` 的路由表漏了配置中心的两条路由**（`/applications/:id/config`、`/applications/:id/config/versions`）。同一次合并里给 authz 补了 `/roles`、`/roles/:id`，只补了一半。

8. **`BindType` 与 `Bind` 有一处行为不一致**：首次加载解析失败时前者返回 `(b, nil)`（表面成功、`Load()` 空、只有一条日志），后者返回 `(nil, err)`。**当前生产路径不可达**——`fetchConfig` 解出的 `json.RawMessage` 必然是合法 JSON，`Unmarshal` 进 `any` 不会失败。但哪天 `fetchConfig` 的解码方式变了（改成流式、或允许部分失败），这条就会从"不可达"变成真实差异。

9. **`AddFieldDialog` 建不出空字符串值**：`if (!textValue.trim())` 拦下，但 `""` 在后端是**合法的已配置值**（`IsSet` 对 `""` 返回 true）。另外那里 `trim` 只用于判空、存的是未 trim 的原串，判据与存的东西不是一回事。

### 三、这份计划自身被实现者挑出的缺陷（方法论记录）

**二十处，全部源自计划本身，且都是被"真的把代码跑起来"的审查抓到的，不是读出来的。** 分四类：

**A. 编造仓库里不存在的东西（六次，全部集中在测试脚手架）**

| 计划里写的 | 仓库里实际是 |
|---|---|
| `newConfigFixture` 用两返回值的 `apps.Create` | 返回三个值 `(*Application, secret, error)` |
| `newTestEnv` / `authedCtx` / `assertStatusCode`（grpcapi） | `newGRPCEnv` / `(e *grpcEnv).authed`；`assertStatusCode` 根本不存在 |
| `env.do` / `env.getJSON` / `env.createApp`（httpapi） | 三个**包级函数** `newAdminEnv` / `do` / `decode` |
| "每个测试文件必须自己写 `afterEach(cleanup)`" | `web/src/test-setup.ts` 已全局注册 |
| `import userEvent from '@testing-library/user-event'` | 该包不在 `package.json` 里 |
| `newFullEnv` / `stopGRPC` / `startGRPC`（integration） | `newPhase2Env` / `stopFp` / `restartFp` |

这是最系统性的一类，**六次全部出现在同一个位置**。教训很具体：**凡是引用仓库既有辅助函数的地方，落笔前 grep 一次确认名字存在**——比事后让每个实现者各自绕一圈便宜得多。

**B. 计划给的代码有真 bug（四处）**

- `CoerceConfigValue` 对 array/object 走 `[]any`/`map[string]any` 往返，**超过 2^53 的整数被静默舍入**（`{"id":9007199254740993}` → `...992`，`err` 为 nil），且 object 的 key 被重排。讽刺的是同一份计划的 Task 8 早就点破了这个坑（"转成 any 再转回来会把整数变成 float64、把字段顺序打乱，原样透传"），Task 1 却没应用。
- `applyValues` 的 `*out = *base` 浅拷贝让 slice/map 字段与旧快照共享底层存储，**`encoding/json` 解码 slice 时容量够就原地复用旧数组**——会连带改写那份"被约定为不可变、此刻可能正被别的 goroutine 通过 `Load()` 持有"的旧快照。修复还漏了 `[N][]T` 这个形状（数组套引用类型），复审又补了一次。
- `getVersion` 用 `fmt.Sscanf(seq, "%d", &seq)` 解析路径参数：`%d` **不检查尾部残余字符**，`Sscanf("12abc", "%d", &seq)` 返回 `seq=12, err=nil`，`/versions/12abc` 会错误地返回 200。
- `CreatedAt` 三处 SQL 缺 `* 1000`，产出的是秒而非毫秒，与仓库既有约定（`application.go` 注释明写"时间统一转成毫秒"）相反。

**C. 计划给的验证步骤本身是假的（一处，但最隐蔽）**

计划让每个 SDK 任务跑 `./scripts/test.sh ./sdk -run TestArch` 来"确认分层约束"。仓库里**没有任何以 `TestArch` 开头的测试**，`go test` 输出 `ok ... [no tests to run]` 并以 0 退出——**这条验证永远是绿的**。它比 A 类更危险：A 类会编译不过、立刻暴露；这一条会安静地绿。真实名字是 `TestSDKDoesNotImportInternal` / `TestSDKHasNoPanic` / `TestExamplesDoNotImportInternal` / `TestSDKDoesNotImportWebFrameworks`。

**D. 计划给的测试没有辨别力（多处）**

三种不同的失效形态，值得分开记：

1. **因巧合而通过**：`configspec.go` 那行 `append(append([]int{}, index...), i)` 的双重防御拷贝，简化成朴素的 `append(index, i)` 后**全部测试照样绿**——因为 fixture 只嵌套 1 层、2 个字段，这个规模下 `cap` 恒等于 `len`，每次 append 都新分配，天然不共享底层数组。要 **4 层**嵌套才撞得上（3 层仍不够，实现者重放 `len`/`cap` 增长过程定位到的）。
2. **被另一条路径顺带救活**：`TestConfigChangedPushTriggersReload` 只删掉 `GetConfigChanged` 分支的重载调用仍稳定通过 8/8，两处都删才失败——它的绿一直来自"首次建流 ready 触发的那次重载"，从未真正测过它名字所指的路径。
3. **只断言通过的那一侧**：`disabled={!diffsReady}` 整段删掉、5 条测试全绿（helper 只"等到 disabled===false"，从不断言它**曾经**是 disabled）；`push: rollbackPush` 硬编码成 `false`、5 条全绿（唯一读 body 的测试在断言前先点了"仅落库"，`push:true` 从未被断言过）。

还有一处更细的：`TestTypeChangeBothPaths` 原本用**单字段** struct 测"解析失败保持整份旧快照"——而 `encoding/json` 对标量类型不匹配是全有全无，单字段时"保持旧快照"与"半解析"产出完全相同的结果，抓获率 **0%**（同一变异跑 8/8 全 PASS）。加第二个字段后仍只有约 40%，因为 `raise(err)` 唤醒测试 goroutine 与 `snap.Store(next)` 之间没有同步点——补了沉降等待才到 10/10。

### 四、执行过程中确立、后续应当延续的实践

- **变异验证必须是"改坏跑一次确认真的变红"，而不是"跑一次绿了就算"。** 本轮 19 处标「辨别力」的测试全部做过，直接产出了上面 D 类的全部四个发现。更进一步的做法值得推广：**外科手术式变异**——比如让实现"照常换指针但抑制回调"，用来证明"指针恒等"那条断言不是与"回调次数"冗余；只有这样才能判断一条断言是否**独立承重**。
- **审查要跑代码，不要读代码。** 本轮全部二十个发现无一例外都来自实跑：伪造一条 Redis 消息证明 `Gap` 可被伪造、8 个 goroutine 并发证明冲突错误没被分类、对着**数据库自己的时钟**比对证明时间戳单位错了、注入 5 种断连时长测出真实退避时刻表。
- **共享测试基础设施要按 db 过滤。** `killPubSubConnection` 原本数的是整个 Redis 服务器的 pubsub 连接，既被别的 worktree 干扰，又在本模块引入第二条常驻订阅后**结构性地永远不可能成立**。改成按 `rdb.Options().DB` 过滤 + 杀本库全部，将来加第三条订阅也不用再改。
- **`testsupport.NewTestDB` 每次调用都 TRUNCATE。** 拿 pool 和建数据的顺序反了，报错会是一句与真实原因毫无关系的外键失败。
- **实现者应当把计划当作可证伪的。** 本轮有多个任务因为实现者拒绝照抄 brief 而避免了返工——它们核实了仓库现状、发现 brief 引用的东西不存在或本身有 bug，并在报告里写明依据。这比"忠实执行"更有价值。
