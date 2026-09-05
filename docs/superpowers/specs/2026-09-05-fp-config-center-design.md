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
