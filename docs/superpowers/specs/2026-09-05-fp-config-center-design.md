# fp 配置中心模块设计

**日期**：2026-09-05
**状态**：设计已定，待细化为实施计划
**上游**：`2026-08-24-fp-foundation-platform-design.md` 第六节

---

## 一、目标

对症 `3s/core/config/config.go` 那 335 行 Go 变量：费率、Redis key 前缀、业务常量、base64 logo 全混在一起，**改一个费率要重新编译发版**。

要治的不是"配置写在代码里"这件事本身——把配置项声明在代码里是好事：类型安全、IDE 能跳转、改名有编译错误。要治的是**值被焊死在编译期**。

所以本模块的核心命题只有一句：

> **代码声明结构，控制台填值，fp 推变更。**

代码里的 struct 说"我需要哪些配置项、什么类型"，控制台说"值是多少"。改值不重新编译、不重新发版。

## 二、范围

**做：**

- SDK 侧的 struct 绑定、启动拉取、热更新、变更回调
- 控制台的配置表单、版本历史、回滚
- 前端配置（浏览器 JS 读取）的下发路径

**不做（本期明确排除，各有理由，见对应章节）：**

| 项 | 理由 |
|---|---|
| SDK 上报 schema | 见 4.3 |
| 默认值 | 见 4.2 |
| 环境维度（dev/test/prod） | 见 4.5 |
| 本地快照文件 | 见 4.6 |
| secret 类型与落库加密 | 见 4.7 |
| 校验规则（min/max/oneof） | 见 6.3 |
| 灰度 / 按实例标签定向下发 | 见 9.3 |
| 变更操作人（`actor_id`） | 见 5.3 |

其中环境、快照、secret、默认值与校验规则五条推翻了主设计文档第六节与 10.3 的原始设想，反转记录集中在第十三节。

## 三、与 Nacos / Apollo 的对照

| | Nacos / Apollo | fp 配置中心 |
|---|---|---|
| 配置的单位 | 一整个文件（properties / yaml / json） | 单个配置项 |
| 有没有 schema | 没有，配置就是文本 | **有**：类型 + 说明，人在控制台维护 |
| 控制台形态 | 文本编辑器 | 按类型渲染的表单 |
| 类型安全 | 无，客户端解析时才发现 | 保存时就按配置项的类型转换 |
| 灰度发布 | 有（按 IP / label） | 无，用"仅落库、重启生效"替代 |

差别的根源是**配置项带类型**。Nacos 面对的是任意语言、任意格式的配置文件，只能当文本处理；fp 的接入方是自家 Go 服务，配置项是可枚举的，因此可以把"裸 JSON 编辑器"换成带类型的表单。代价是不支持"把一整个 yaml 丢进去"，这是刻意的取舍。

## 四、核心机制

### 4.1 SDK 侧的形态：struct 绑定

```go
type ShopConfig struct {
    FeeRate  float64
    Sms      SmsConfig            // 嵌套 struct = 分组，展开成 sms.*
    Upstream UpstreamConfig
}
type UpstreamConfig struct {
    Timeout time.Duration         // upstream.timeout
    APIKey  string                // upstream.api_key
}

cfg, err := fpsdk.Bind[ShopConfig](client)
var miss *fpsdk.MissingConfigError
if errors.As(err, &miss) {
    log.Fatalf("配置未配齐：%v", miss.Keys)
}

c := cfg.Load()                   // *ShopConfig，原子快照
fee := amount * c.FeeRate
```

`Load()` 返回的是一个**跨字段一致的快照**：一次请求内 `c.FeeRate` 和 `c.Sms.DailyLimit` 一定来自同一个版本，不会出现 A 字段是新值、B 字段是旧值。这是选 struct 绑定而不是 `Get("fee_rate")` 式取值的主要理由，其次才是类型安全。

**key 从字段名推导**为 snake_case，嵌套 struct 用点连接：`FeeRate` → `fee_rate`，`Upstream.Timeout` → `upstream.timeout`。不写 tag 是常态。

**唯一的 tag 是 `fp:"json"`**：标了它的 struct 字段不展开成分组，而是整体作为一个 `object` 类型的配置项，控制台上填一段 JSON。字段固定的结构拆成分组更好编辑，供应商列表那种拆不动的走 JSON。

**为什么不用裸的包级变量**（`var FeeRate float64`）：两道坎都过不去。第一，Go 运行时无法枚举一个包里的变量——`reflect` 只能操作你手上已有的值，没有 `reflect.Package`，runtime 那份符号表也没有公开 API 能列出包级变量并取到地址；能做到的只有编译期代码生成（`stringer` / `wire` 那个路子）。第二，就算生成了，`var FeeRate float64` 被后台 goroutine 写、被请求 goroutine 读就是 data race——Go 内存模型不保证非原子读写安全，编译器可以把它缓存进寄存器，race detector 会直接报。要安全就得包成 atomic，那读法就退回 `FeeRate.Load()`，比 struct 更啰嗦且 key 要手写字符串。

### 4.2 没有默认值

**`Bind` 时任意字段在 fp 上没有值 → 返回 error，进程起不来。** 不提供默认值机制。

理由不是"少写点 tag"：

1. **默认值省不下那次人工动作。** 上游密钥这类东西根本不可能写默认值，必然要人去控制台配。既然启动本来就卡在"人去控制台配"这一步，给另外几个非敏感项写默认值并不能让项目提前启动。
2. **取消之后，配置只剩一个真相源。** 有默认值就永远要回答"这个值到底来自代码还是 fp"。
3. **取消之后，字段改名不再是静默事故。** 有默认值时，改字段名 = 新 key 在 fp 上不存在 = 静默回落到默认值，生产上那个被调成 0.02 的费率会悄悄变回 0.006 跑上三天。没有默认值，同一个改名直接让 `Bind` 报错、发布失败——**没有可以静默回退的东西**。

缺失的字段留 Go 零值，`cfg` 照样返回——**SDK 不替业务方决定能不能带伤启动**，愿意继续跑的人自己判断。

### 4.3 配置项从哪来：人建，`Bind` 的报错就是那份清单

**没有 SDK 上报。** 配置项一律由人在控制台创建。

那"该建哪些 key"从哪知道？——`Bind` 的错误信息就是清单。它必须**一次列全**，并带上每一项该建成什么类型：

```
fpsdk: 3 个配置项尚未在 fp 上配置，请到控制台【商城 / 配置中心 / DEFAULT】创建并填值：
  upstream.api_key   (string)
  upstream.timeout   (int)
  fee_rate           (float)
```

类型是 SDK 反射 struct 算出来的（6.1 的映射表）。所以**反射那部分逻辑一条都不能省**——它要干三件事：推导 key、推导类型（只为了生成这份清单）、把拉到的值填进 struct。

于是首次部署的流程是：**起一次 → 失败 → 照着错误清单在控制台建好并填值 → 再起 → 活**。

**这条省掉的东西要认：**

- **手抄有成本。** 50 个配置项要照着错误信息建 50 遍，会抄错。抄错的表现是 `Bind` 一直报缺同一个 key、而控制台上看着"有"（实际是 `upstream.time_out` 这种），只能靠人眼对。
- **没有对账。** 代码里删掉一个配置项之后，库里那条永远留着，没有任何机制能告诉你"它已经没人读了、可以删"。库会慢慢攒垃圾。
- **删除没有保护。** 人删掉一个代码还在读的配置项，运行中的实例走 4.4 第二条（保持旧值 + `OnError`），新起的实例直接缺值起不来。兜底还在，但它是事后的。

**上报将来要加是纯增量**：加一个 `ReportSchema` RPC + 一张记录 `last_seen_at` 的心跳表，`config` 表一列都不用动——因为"`type` / `desc` 归人维护"这条规则在有没有上报时都成立。所以现在砍掉它不堵路。真要加的时候记住一件事：**上报必须是周期性心跳**（比如每 10 分钟一次、阈值 30 分钟），只在 `Bind` 时发一次的话，一批 pod 连跑三天不重启，它自己的配置项就会被判成"无人引用"。

### 4.4 热更新

控制台改值 → fp 发 `ConfigChanged` → SDK 拉一次全量 → **原子替换整个快照**。

**推送只推信号，不推变更内容**，与授权模块的 `PolicyChanged` 一致。配置项就几十条，拉全量比处理增量的乱序、丢失、部分应用简单得多。

除了原子替换，额外提供一个回调，给需要重建资源的人用：

```go
cfg.OnChange(func(old, new *ShopConfig) {
    if old.DB.MaxConns != new.DB.MaxConns {
        pool.Resize(new.DB.MaxConns)
    }
})
```

**不做 `fp:"noreload"`（变了就退出进程）那种方案**：一次误改会把全线实例同时重启。

三条实现约束，共同的骨架是——**新快照只要不能完整、正确地解析出来，就一律保持旧快照**：

- **快照没变就不换指针、不触发 `OnChange`。** 同一个分区里别人改了你不关心的 key，也会生成新版本、推给你，而你拉到的全量里你关心的字段一个都没变。不比较就回调的话，别人配置一次你的连接池重建一次。
- **某个 key 消失（被删或回滚导致）→ 保持旧值 + 报错，绝不清成零值。** 清零值是危险的（`fee_rate=0` 就是免手续费）。
- **解析失败（有人把 `type` 从 `int` 改成了 `object`）→ 保持旧快照 + 报错。** 旧服务继续用旧值跑下去，不因为配置被改成了新版本代码要的形状而崩掉。这是 6.4「类型可改」那条规则的兜底腿。

报错走一个独立的回调：

```go
cfg.OnError(func(err error) { alert(err) })
```

**为什么不把 error 塞进 `OnChange` 的参数里**：上面第一条已经定死"快照没变就不触发 `OnChange`"，而这两种出错情形下快照恰恰**没变**——从 `OnChange` 里发出一次"什么都没变"的回调会直接和那条规则打架，还逼着每个业务方在回调开头写一行判空。业务方没挂 `OnError` 时 SDK 打 ERROR 日志，不静默。

### 4.5 没有环境维度

主设计文档写的是"应用 × 环境（dev/test/prod）"。删掉环境这一层：**一个 fp 部署 = 一个环境**。

理由是它与"一个 fp 部署 = 一套用户体系"（主设计 1.3）本来就矛盾——一个 fp 同时管 dev 和 prod 的话，dev 的服务拿着同一套 app_secret 连上来，能读到生产的用户数据，而认证 / 用户 / 权限这几个模块并没有环境隔离的概念。既然环境必须靠部署隔离，配置中心再单独做一层环境就是多余的。

省掉的是：表少一列、UI 少一个切换器、SDK 少一个启动参数、以及"这个值属于哪个环境"的每一处判断。

真要在一个 fp 里隔开两套配置，建两个 application 即可，零机制。

### 4.6 没有本地快照文件

主设计文档第六节写的是"本地快照文件兜底（fp 挂了业务不挂）"，10.3 写的是"配置：使用本地快照文件"。**这两条都推翻。**

**代价必须写明白：fp 不可达期间，业务方进程无法重启。** 已经在跑的进程不受影响（值在内存里），但 fp 挂着的时候如果碰上节点故障重新调度 pod，那个 pod 起不来，直到 fp 恢复。

这条与主设计原则 4（fp 是全局单点，不能放大故障域）有张力。做了决定就要认这个代价，而不是假装它不存在——**fp 的可用性从此是业务方重启路径上的硬依赖**。

### 4.7 没有 secret 类型

主设计文档第六节写的是"加密配置项（密钥类，落库加密，UI 上脱敏显示）"。**不做**：不加密落库，控制台明文显示，也不保留"这一项是密钥"的类型标记。

**代价与配套纪律：密钥类配置项必须建在 `DEFAULT` 分区，绝不能建在 `WEB`**——否则它会随前端配置一路吐到浏览器里。secret 标记去掉之后，分区是唯一的闸。好在分区是建配置项时定死的，不是一个随手能勾出泄露的开关。

**本模块不解决第三阶段交接事项第 3 条**（connector 的 secret 字段脱敏与落库加密）。那条债务仍然挂着，而且现在多了一个立场冲突：配置中心决定密钥明文存库明文显示，connector 那边原计划是加密 + 脱敏。两者要么统一到明文、要么统一到加密，**需要单独裁决，不在本模块范围内**。真到加微信 AppSecret 那天必须先解决。

## 五、数据模型

### 5.1 一张表

```sql
-- 配置的版本快照。一次保存一行，自包含。
CREATE TABLE config (
    application_id uuid        NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    type           text        NOT NULL,   -- DEFAULT | WEB，分区
    seq            bigint      NOT NULL,   -- 分区内自增：v1 v2 v3
    fields         jsonb       NOT NULL,   -- {key: {type, desc, value}}
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, type, seq)
);
```

`fields` 长这样：

```json
{
  "upstream.timeout": {"type": "int",    "desc": "上游超时（毫秒）", "value": 3000},
  "fee_rate":         {"type": "float",  "desc": "手续费率",        "value": 0.02},
  "risk.mode":        {"type": "string", "desc": "",                "value": null}
}
```

用 map 而不是数组：key 本来就唯一，做 JSON 对象的键最自然，`fields->'upstream.timeout'` 是对象查找而不是遍历，还天然防重复 key。

**两个状态靠 map 本身表达，不需要额外的列：**

- **未配置**：`value` 是 JSON `null`。`Bind` 的 `missing` 就是这些
- **不存在 / 已删除**：key 根本不在 map 里

**没有单独的 schema 表。** `type` 和 `desc` 是配置项的属性，但它们和 `value` 一样都由人在控制台维护、都该随版本走——回滚就该回到"人当时写下的那一份"，包括类型和说明。把它们拆出去只会制造一个"schema 不回滚、值回滚"的半吊子语义。

### 5.2 为什么是整版快照而不是变更点

另一个候选是时态行：一行一个字段的变更，只写本次真的变了的那几项，查询时 `DISTINCT ON (key)` 取每个 key 的最大 `seq`。它更省存储，但两条决定性的劣势：

**回滚会变成一个能写错的东西。** 时态行的回滚要靠 `seq <= 6` + `DISTINCT ON (key)` + 一层 `deleted` 过滤全都写对才对，写错了产出的是"看起来合理但其实不是 v6"的状态，很难发现。整版快照的回滚是**复制一行**，没有可以写错的地方。

**只有整版快照能安全修剪。** 时态行按 `seq` 删老行会把"某个字段最后一次修改恰好落在老版本里"的那一行删掉，那个字段就凭空消失了；躲开这个（只删后面还有更新行的历史行）又会让老版本的恢复结果变错——**删了什么、坏了什么都不可预期**。整版快照每行自包含，删掉 100 版之前的，后果就一句话：那些版本回滚不了，其余一切照常。

存储代价被上限兜住：50 个配置项一版约 7KB，100 版 700KB，一个应用两个分区 1.4MB。

### 5.3 没有 `actor_id`

不记录"谁改的"。今天 fp 只有一个管理员——`AdminService` 只有 `EnsureBootstrap` / `Login` / `Authenticate` / `Logout`，`internal/httpapi/admin.go` 只有 login / logout / me，**没有任何创建第二个管理员的路径**，主设计里那个"平台管理员"页也还没实现。这一列今天永远是同一个值，零信息量。

将来多管理员落地时补这一列是**无损的**：老数据只有一个可能的取值，加列之后一条 `UPDATE` 回填即可。

版本历史因此记录的是"什么时候、变成了什么"，不记录"谁干的"。

### 5.4 查询

```sql
-- 当前配置
SELECT seq, fields FROM config
WHERE application_id=$1 AND type=$2 ORDER BY seq DESC LIMIT 1;

-- v6 的完整状态
SELECT fields FROM config WHERE application_id=$1 AND type=$2 AND seq=6;

-- 回滚到 v6：复制一行，type / desc / value 一起回去
INSERT INTO config (application_id, type, seq, fields)
SELECT application_id, type, $3, fields FROM config
WHERE application_id=$1 AND type=$2 AND seq=6;

-- 某个 key 的历史（对象查找，不用展开数组）
SELECT seq, fields->$3 AS entry, created_at FROM config
WHERE application_id=$1 AND type=$2 AND fields ? $3 ORDER BY seq DESC;

-- 版本列表；"改了哪些"取相邻两版的 fields 在应用层 diff
SELECT seq, created_at FROM config
WHERE application_id=$1 AND type=$2 ORDER BY seq DESC LIMIT 20;

-- 修剪：只保留最近 100 版
DELETE FROM config WHERE application_id=$1 AND type=$2 AND seq <= $3 - 100;
```

`seq` 在事务里取该分区的 `MAX(seq)+1`，靠主键约束兜并发；管理操作低频，冲突了重试即可。

### 5.5 `type` 是分区

主键含 `type`，所以它划出的是**互不相干的分区**：

| | `DEFAULT` | `WEB` |
|---|---|---|
| 谁读 | `Bind[T]` 绑的 struct | `BindType(client, "WEB")` |
| 会不会到浏览器 | 不会 | 会 |
| 版本序列 | 自己的 v1 v2 v3 | 自己的 v1 v2 v3 |

**同名 key 在两个分区下是两个独立的配置项**，各有各的值、各有各的版本历史。`site.title` 这种前后端都要用的东西**要配两遍**——这个代价换来的是零歧义：不必回答"这个值到底属于谁、改了会影响谁、算谁的版本"。

密钥类必须落在 `DEFAULT`（4.7）。分区是可扩展的枚举（将来可能有 `MOBILE`、`MINIPROGRAM`），SDK 的 API 收 string 而不是 enum，加一个分区不需要改协议。

## 六、类型系统

### 6.1 类型表

fp 的类型系统**不带任何一门语言的特性**——将来接入其他语言时，控制台上看到的必须还是它认得的东西。

| fp 类型 | Go 侧映射 | 控制台控件 |
|---|---|---|
| `bool` | `bool` | 开关 |
| `int` | 各整数类型；`time.Duration` **按毫秒**当整数处理 | 数字输入 |
| `float` | `float32` / `float64` | 数字输入 |
| `string` | `string` | 文本框 |
| `array` | 切片、数组 | JSON 数组 |
| `object` | `map`、标了 `fp:"json"` 的 struct | JSON 对象 |

**没有 `duration` 类型。** 其他语言没有这个概念。Go 侧 `time.Duration` 仍然能写，但那是 **Go SDK 的私事**：它按毫秒当 `int` 处理，解析时乘 `time.Millisecond`，fp 完全不知道有这回事。单位约定写死在文档里，控制台上它就是个整数。

反射时必须**先判具体类型再判 Kind**——`time.Duration` 底层是 `int64`，顺序反了时长会被当成普通整数、丢掉毫秒换算。

不支持的类型（指针、interface、chan、func）在 `Bind` 时直接报错，不静默跳过。

### 6.2 值的存储与传输

`fields` 里的 `value` 存 **JSON 原生类型**，不是一律字符串。保存时按该项的 `type` 转一次，转不过去才报错。于是：

- 控制台读回来直接是正确类型
- SDK 直接 `json.Unmarshal` 到 struct 字段的类型，不需要自己实现类型转换
- 类型**根本不用下发给 SDK**（协议因此少一个字段）

key 是**扁平的点分路径**（`upstream.timeout`）而不是嵌套 JSON。理由是控制台按 key 逐项编辑、逐项做 diff。SDK 侧把扁平路径填进嵌套 struct 的那段逻辑本来就要写（要算 `missing`、要处理 `time.Duration`），多一层点路径不增加多少事。

### 6.3 弱约束

**类型是弱约束，不做 min / max / oneof 校验。** 保存时尝试按类型转换，`"3"` 填进 `int` 字段照样过，**只有转不过去才提示**。

范围和枚举的校验交给业务方在 `Bind` 之后自己写一行 if。代价是错值已经落库并推给全线实例了才被发现——这条取舍认了。

### 6.4 类型可改，改法有规矩

配置项的 `type` 随时可改（没有上报来跟人抢这个字段，规则很简单）。要改类型的场景长这样：v2 代码把 `upstream.timeout` 从 `int` 改成了一个 `object`，发版前得先把控制台上的值改成 JSON。而这时 v1 还在跑，它要的还是那个 `int`。

改法和 9.2 的发布协同是同一套：

```
① 控制台把 upstream.timeout 的类型从 int 改成 object，填上 JSON 值
② 保存时选「仅落库，实例重启后生效」→ 不发 ConfigChanged
   → v1 内存里仍是那个 int，照常跑，完全不受影响  ✓
③ v2 上线，Bind 拉到 JSON，解析成 object  ✓
```

**若②误选了「立即推送」**：v1 拉到全量、解析失败 → 走 4.4 第三条，**保持旧快照 + `OnError` 报错**。旧服务继续用旧值跑下去，不崩——它只是收到一个"配置被改成了我认不得的形状"的信号。这是这条设计的兜底腿，也是为什么 4.4 那条约束不能省。

改类型是一次普通的版本变更，因此**可以被回滚撤销**（`type` 就在 `fields` 里，跟着版本走）。

## 七、SDK 形态

### 7.1 两个绑定入口

```go
// ① 后端自己用的：绑 struct，读 DEFAULT 分区，缺值会报错
cfg, err := fpsdk.Bind[ShopConfig](client)
cfg.Load()                                        // *ShopConfig
cfg.OnChange(func(old, new *ShopConfig) { ... })
cfg.OnError(func(err error) { ... })              // 解析失败 / key 消失，见 4.4

// ② 转发给前端的：拉某个分区的全量，不绑 struct，没有缺值概念
web, err := fpsdk.BindType(client, "WEB")
web.Load()                                        // map[string]any
web.OnChange(func(old, new map[string]any) { ... })
```

**`BindType` 必须指定分区**，没有"不传就是全部"的重载——分区之间同名 key 是不同的配置项（5.5），合并成一个 map 就得回答"撞了算谁的"，而这个问题不该存在。

两者是同一套形态（`Load` + `OnChange` + `OnError`），共用同一条 Watch 流、同样遵守「快照没变就不触发」；各自拉自己分区的 `GetConfig`，也各自只响应带着自己分区的 `ConfigChanged`。

**`BindType` 返回 `map[string]any` 而不是 `map[string]string`**：SDK 按 JSON 解析一遍再交出去，业务方 `json.Encode` 出来直接是 `{"feature.new": true, "limits": [1,2,3]}`，而不是一堆 `"true"` / `"[1,2,3]"` 要前端再转一次。

### 7.2 前端读配置

**SDK 不提供 HTTP handler**，只交出 `map`——传输方式是业务方的事：

```go
// 要 HTTP 拉就三行
mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
    json.NewEncoder(w).Encode(web.Load())
})

// 要 WS 实时推就挂回调
web.OnChange(func(old, new map[string]any) {
    hub.Broadcast(new)
})
```

**为什么不让前端直连 fp**：fp 是全局单点（主设计原则 4），而前端流量的量级是"每个终端用户每次打开页面一次请求"，与 SDK 的量级完全不同；直连还要额外处理 CORS、限流、缓存。走业务后端则 fp 侧零新增接口、零新增流量。这也正是 3s 当年的做法。

### 7.3 实现约束清单

1. 快照没变就不换指针、不触发 `OnChange`（4.4）
2. 某个 key 消失 → 保持旧值 + `OnError`，绝不清成零值（4.4）
3. 解析失败 → 保持旧快照 + `OnError`，绝不崩、也绝不半解析（4.4 / 6.4）
4. 没挂 `OnError` 时打 ERROR 日志，不静默（4.4）
5. `time.Duration` 反射时先判具体类型再判 Kind（6.1）
6. `MissingConfigError` 一次列全，并带上每项该建成的类型（4.3）——它是人在控制台建配置项的唯一依据，漏一项就要多跑一轮"起→失败"

## 八、协议

新建 `ConfigService`，**推送仍走已有的 Watch 长流**（主设计 3.3 定的"一条流统一回源与推送"，`WatchResponse` 的 oneof 早就为此预留了扩展位）。

```protobuf
service ConfigService {
  // GetConfig 拉取该应用某个分区当前的全部配置值。
  //
  // 只有这一个 RPC：配置项由人在控制台创建，SDK 不上报 schema（设计文档 4.3）。
  rpc GetConfig(GetConfigRequest) returns (GetConfigResponse);
}

message GetConfigRequest {
  string type = 1;                   // DEFAULT | WEB，分区
}
message GetConfigResponse {
  // version 是该分区的 config.seq。
  int64 version = 1;
  // values 是一个 JSON 对象：{key: value}，只含已配置的项
  // （fields 里 value 为 null 的不出现）。
  //
  // 刻意不拆成 map<string,string>：值是 JSON 原生类型，拆开会退化成
  // 一堆待解析的字符串，SDK 就得自己实现一遍类型转换。
  string values = 2;
}

// WatchResponse 的 oneof 新增一个分支。
message ConfigChanged {
  // type 指出哪个分区变了。一次保存只动一个分区（版本序列是分区内自增的），
  // 所以这里一定是单值。SDK 据此只刷新对应的那份绑定。
  string type    = 1;
  int64  version = 2;
}
```

**`missing` 由 SDK 自己算**（struct 里有、`values` 里没有的），服务端不返回——它不知道调用方的 struct 长什么样，也不需要知道。

## 九、生效方式与发布协同

### 9.1 一次保存 = 一个版本

表单上改多少项都行，点一次保存 → 生成一个版本 → 推一次。**SDK 看不到中间态**（改了 `fee_rate` 还没改 `fee_min` 的那一瞬间）。没有草稿态、没有"编辑中"，不引入 Apollo 那套两阶段发布。

### 9.2 保存时选生效方式

```
[保存]  生效方式：  ◉ 立即推送（默认）
                   ○ 仅落库，实例重启后生效
```

选后者就是**不发 `ConfigChanged`**，实现成本接近零。

它解决的是这个场景：同一个 key，线上 v1 代码要 `xxx`、即将发布的 v2 要 `bbb`，而发布前就得把值改成 `bbb`：

```
选「仅落库」→ v1 pod（运行中）内存里仍是 xxx，不受影响
             v2 pod（新启动）Bind 拉到 bbb  ✓
             滚动发布天然把新旧分开
```

**已知漏洞**：v1 pod 如果在这期间因节点故障意外重启，会读到 `bbb`。窗口短、概率低，认了。

改类型（6.4）走的也是这条路。回滚同样要选生效方式——回滚就是生成一个新版本，没有例外。

### 9.3 不做灰度下发

按实例标签定向下发（Apollo 的灰度发布）是这个问题唯一严谨的解——新旧实例同时在跑也各拿各的值，没有 9.2 那个漏洞。**本期不做**：它要给表、UI、推送各加一层 label 维度，而"仅落库、重启生效"已经覆盖了滚动发布这个主场景。

将来要加是纯增量（`config` 加一个可空的 label 维度），已有数据和判定逻辑都不用改。

### 9.4 改名与不兼容变更

**改 key（字段改名）**：新 key 在 fp 上不存在 → `Bind` 直接报错、发布失败，不会静默跑错值（4.2 第 3 条）。正确做法是发版前先在控制台建好新 key 并填值；旧 key 在全量上线后手动删掉——没有对账机制会提醒你（4.3），得自己记着。

**改语义（key 和类型都没变，意思变了）fp 检测不到**：单位从秒改成毫秒、枚举值换了一批，类型一样、值也合法，只是意思变了。这是**纪律不是机制**：

> **不兼容的语义变更必须换 key。** 与数据库改列语义要新开一列是同一条规矩。

## 十、控制台

**配置中心页**（选应用）：

- **先切分区**（`DEFAULT` / `WEB` 两个 tab）——版本序列、回滚、推送都是分区内的事，两个分区不该混在一个列表里
- 配置项列表，按 key 的点前缀折叠分组，每行按类型渲染控件
- 顶部一条醒目提示：**N 项未配置**（红，即 `value` 为 JSON `null` 的那些）
- 每行可编辑：值、`desc`、`type`（改类型要二次确认，并提示"旧实例若收到推送会解析失败"，见 6.4）
- `[新建配置项]`：key + 类型 + 值 + 备注（分区由当前所在的 tab 决定）。**建的时候必须同时填值**——没有代码强制它，空着没意义
- `[删除]`：二次确认，并提示"没有机制能确认它是否还被代码读取；删错了运行中的实例会保持旧值并报错，新起的实例会起不来"
- `[保存]`：选生效方式

**版本历史 tab**：版本列表（seq / 时间 / 本次改了哪些 key）+ 两版 diff + 回滚。"改了哪些"取相邻两版的 `fields` 在应用层 diff，不存字段。

**回滚前必须提示"回滚后这些项将变成未配置"**——回滚到 v6 时，v7 才新增的配置项在 v6 的 `fields` 里没有，运行中的实例会走 4.4 第二条"保持旧值 + `OnError`"，而新启动的实例会缺值起不来。

**不复用 `DynamicForm`。** 结构差异太大（那是 connector 的单个动态表单，这是带分组、批量保存的列表），而且第三阶段终审查出它的 DOM id 没加命名空间会让同名字段串台——配置项的 key 带点、撞得更狠。新组件从第一行起就带命名空间。

前端工程配置改动前先读 `docs/console.md` 里那六条与官方文档冲突的坑（第三阶段交接事项第五节）。

## 十一、与现有代码的接触面

| 位置 | 改动 |
|---|---|
| `proto/fp/v1/` | 新增 `config.proto`；`WatchResponse` 的 oneof 加 `ConfigChanged` 分支 |
| `internal/domain/` | 新增 `Config`、`ConfigField`（`fields` 里那个对象的 Go 结构） |
| `internal/store/migrations/` | 新增迁移（一张表） |
| `internal/service/` | 新增 `ConfigService`：取值、保存、回滚、删除、修剪、推送 |
| `internal/grpcapi/` | 新增 `ConfigService` 的 gRPC 实现；推送接入既有 Watch 流 |
| `internal/httpapi/` | 新增控制台的配置中心路由 |
| `sdk/` | 新增 `config.go`（反射、绑定、快照、回调）、`bindtype.go` |
| `web/src/pages/` | 新增配置中心页与版本历史 |

**不引入新依赖。** 反射、`encoding/json` 都在标准库里，`sdk/arch_test.go` 的分层约束不受影响。

## 十二、测试策略

第二、三阶段的教训是"服务端有信息、传输层丢了，而所有测试照绿"，以及"写计划时强调了不等于守住了"。所以下面每一条都要能回答：**哪一条测试会因为违反它而变红？** 标注「辨别力」的，实现时必须做一次**变异验证**——把实现改坏跑一次确认真的变红，再改回来。

1. **反射生成 key 与类型**（表驱动）：各 Go 类型 → fp 类型；嵌套 struct 展开成点分组；`fp:"json"` 整体成 object；不支持的类型报错。
   **辨别力**：`time.Duration` 那条必须断言毫秒换算，只断言"类型是 int"的话，先判 Kind 后判具体类型的错误实现照样绿。

2. **`MissingConfigError` 列全、且带类型。**
   **辨别力**：至少缺 2 项，断言两项都在**且类型正确**——只缺 1 项的话，"报第一个就 return"的实现也会绿；不断言类型的话，人拿着这份清单去控制台会建错类型（4.3 说明了这份清单是唯一依据）。

3. **快照相同不触发 `OnChange`。** 推一次内容完全相同的配置，断言回调 0 次、指针未变。

4. **「仅落库、重启生效」。**
   **辨别力**：两条腿都要断言——没有 `ConfigChanged` 发出，**并且**新起一次 `Bind` 能拿到新值。只断言前者的话，"根本没存"的实现也会绿。

5. **改类型的两条路（6.4）。** 把某项的类型从 `int` 改成 `object` 并填 JSON 值：
   - 选「仅落库」→ 断言旧 `Bind` 的 `Load()` 仍是原来那个 int，`OnChange` 与 `OnError` 都没被调用
   - 选「立即推送」→ 断言旧 `Bind` 的 `Load()` **仍是原来那个 int**（保持旧快照）、`OnError` 被调用了一次、**进程没崩**

   **辨别力**：第二条必须断言"值还是旧的"而不只是"报了错"——半解析后把其余字段换掉的实现照样会报错，却已经破坏了快照的一致性。

6. **分区隔离。** 在 `DEFAULT` 和 `WEB` 下建同名 key、配不同的值，断言：`Bind` 拿到 DEFAULT 那个、`BindType(client, "WEB")` 拿到 WEB 那个；改其中一个分区不影响另一个的 `seq`；`ConfigChanged{type:"WEB"}` 不会让 `Bind` 那份重新解析。
   **辨别力**：两个分区的值必须**不同**，否则"分区对了"和"压根没分区"产出同样结果。

7. **`BindType` 的解析**：断言 bool / array / object 解析成了真正的 JSON 类型而不是字符串。

8. **未配置 vs 已删除。** `value` 为 JSON `null` 的项出现在 `missing` 里且仍在控制台列表中；key 从 `fields` 里移除的项两处都不出现。
   **辨别力**：两种情形必须同时造出来，只造一种的话，把两者混为一谈的实现（比如都当成删除）会绿。

9. **版本快照可精确还原。** 造 v1..v5 五次变更（含一次删除、一次重新添加、一次改类型），逐版本断言 `fields` 与写入时一致；回滚到 v3 生成 v6，断言 v6 的 `fields` 与 v3 逐字节相同、且 v4 / v5 仍在。

10. **修剪。** 造 105 个版本，断言 v1..v5 被删、v6..v105 还在，且当前配置不受影响。

11. **弱类型转换**：`"3"` 存进 `int` 项成功、`"abc"` 失败并给出可读错误。

12. **端到端穿透**：控制台改值 → 断言 SDK 侧 `cfg.Load()` 真的变了、`web.Load()` 真的变了。对应第三阶段那条教训——只测服务端返回值不够，要穿到 SDK 出口。

## 十三、对主设计文档的反转记录

| 主设计文档原文 | 本模块的决定 | 理由 |
|---|---|---|
| §6「层级：应用 × 环境（dev/test/prod）」 | **删掉环境维度** | 与"一个 fp 部署 = 一套用户体系"矛盾，环境必须靠部署隔离（4.5） |
| §6「本地快照文件兜底（fp 挂了业务不挂）」、§10.3「配置：使用本地快照文件」 | **不做快照** | 代价是 fp 不可达期间业务方进程无法重启（4.6） |
| §6「加密配置项（密钥类，落库加密，UI 上脱敏显示）」 | **不做 secret 类型** | 配套纪律：密钥类必须建在 `DEFAULT` 分区（4.7） |
| §6「配置项带 schema：类型 / **默认值** / **校验规则** / 说明 / 是否加密」 | **无默认值、无校验规则** | 默认值省不下那次人工动作且制造歧义（4.2）；校验规则本期从简（6.3） |

前两条是主设计文档里"fp 挂了业务不挂"这条承诺的实质削弱，第三条是安全面的实质放宽。**都是有意识的取舍，不是遗漏**——但主设计文档第六节应当同步更新，否则下一个人读到的是两份互相矛盾的设计。

另外记一笔与授权模块的不对称：**授权模块的权限点由 SDK 上报，配置中心的配置项由人手建**（4.3）。这不是原则上的分歧，只是本期的取舍——上报后加是纯增量。但在两个模块之间来回看的人会注意到这个不一致，值得在实施计划里写明。

## 十四、实施顺序建议

1. 数据模型与迁移（一张表）
2. 服务端：取值、保存、版本、回滚、删除、修剪
3. proto + gRPC：`ConfigService.GetConfig`、`WatchResponse` 加 `ConfigChanged` 分支
4. SDK：反射生成 key 与类型、`Bind`、`MissingConfigError`
5. SDK：热更新、`OnChange`、`OnError`、快照比较
6. SDK：`BindType`
7. 服务端：推送与生效方式
8. 控制台：分区 tab、配置项列表与表单、新建与删除
9. 控制台：版本历史与回滚
10. 端到端穿透测试（第十二节第 12 条）

1–7 完成时就能验证"改配置不用发版"这个核心命题（用 SQL 直接改 `config` 表即可），不必等控制台。
