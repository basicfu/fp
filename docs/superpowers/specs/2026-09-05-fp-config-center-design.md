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

`config_item`（有哪些配置项）的真相在代码里，由 SDK 上报；`config_version`（值是多少）的真相在控制台。两边各管各的，不会漂移——这正是 3s / xxzj 拷贝后必然漂移的那类问题的解法，与授权模块"权限点由 SDK 上报"是同一个范式。

## 二、范围

**做：**

- SDK 侧的 struct 绑定、启动拉取、热更新、变更回调
- 配置项 schema 的上报与生命周期管理
- 控制台的配置表单、版本历史、回滚
- 前端配置（浏览器 JS 读取）的下发路径

**不做（本期明确排除，各有理由，见对应章节）：**

| 项 | 理由 |
|---|---|
| 默认值 | 见 4.2 |
| 环境维度（dev/test/prod） | 见 4.5 |
| 本地快照文件 | 见 4.6 |
| secret 类型与落库加密 | 见 4.7 |
| 校验规则（min/max/oneof） | 见 6.3 |
| 灰度 / 按实例标签定向下发 | 见 10.3 |

后四条推翻了主设计文档第六节与 10.3 的原始设想，反转记录集中在第十四节。

## 三、与 Nacos / Apollo 的对照

| | Nacos / Apollo | fp 配置中心 |
|---|---|---|
| 配置的单位 | 一整个文件（properties / yaml / json） | 单个配置项 |
| schema 从哪来 | 没有 schema，配置就是文本 | **SDK 反射 struct 上报** |
| 控制台形态 | 文本编辑器 | 按类型渲染的表单 |
| 类型安全 | 无，靠客户端解析时才发现 | 保存时就按 `value_type` 转换 |
| 灰度发布 | 有（按 IP / label） | 无，用"仅落库、重启生效"替代 |

差别的根源是**配置项带 schema**。Nacos 面对的是任意语言、任意格式的配置文件，只能当文本处理；fp 的接入方是自家 Go 服务，SDK 能反射出结构，因此可以把"裸 JSON 编辑器"换成带类型的表单。代价是不支持"把一整个 yaml 丢进去"，这是刻意的取舍。

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

### 4.2 没有默认值

**`Bind` 时任意字段在 fp 上没有值 → 返回 error，进程起不来。** 不提供默认值机制。

理由不是"少写点 tag"：

1. **默认值省不下那次人工动作。** 上游密钥这类东西根本不可能写默认值，必然要人去控制台配。既然启动本来就卡在"人去控制台配"这一步，给另外几个非敏感项写默认值并不能让项目提前启动。
2. **取消之后，配置只剩一个真相源。** 有默认值就永远要回答"这个值到底来自代码还是 fp"。
3. **取消之后，字段改名不再是静默事故。** 有默认值时，改字段名 = 新 key 在 fp 上不存在 = 静默回落到默认值，生产上那个被调成 0.02 的费率会悄悄变回 0.006 跑上三天。没有默认值，同一个改名直接让 `Bind` 报错、发布失败——**没有可以静默回退的东西**。

`Bind` 返回的 error 是结构化的，一次列全部缺失的 key，不是报第一个就返回：

```
fpsdk: 3 个配置项尚未在 fp 上配置，已上报，请到控制台【商城 / 配置中心】填写：
  upstream.api_key   (string)
  upstream.timeout   (int)
  fee_rate           (float)
```

缺失的字段留 Go 零值，`cfg` 照样返回——**SDK 不替业务方决定能不能带伤启动**，愿意继续跑的人自己判断。

### 4.3 上报是控制台表单的唯一来源

取消默认值会撞出一个死锁：代码启动要 fp 上有值 → fp 上有值要控制台上有这个 key → 控制台上有 key 要有人知道该建哪些 key。

破解方式是**把上报和取值拆成两步**：`Bind` 先上报 schema（这步总是成功），再取值（缺值才失败）。于是首次部署有两条路，都通：

- **起一次 → 失败 → 控制台上这些项已经自动列出来了、标着「未配置」→ 人填 → 活**
- 或者人**提前建好**配置项并填值（见 7.1 的「待接入」态），代码一上线直接就能起

这也是"上报到底还有没有必要"的答案：**有，而且它现在是控制台表单的唯一来源。** 不上报的话，人在控制台上凭什么知道要建 `upstream.api_key` 这个 key、它该渲染成什么控件？只能靠人读代码手抄一遍——两份定义必然漂移，就是这个项目要治的病。

上报还顺带提供第二个能力：**对账**——哪些配置项还在被代码引用、哪些已经没人用了可以删（见第七节）。

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

两条实现约束：

- **快照没变就不换指针、不触发 `OnChange`。** 别人给自己的新 key 配值也会生成新版本、推给你，你拉到的全量里你关心的字段一个都没变。不比较就回调的话，别人配置一次你的连接池重建一次。
- **热更新时某个 key 消失（回滚导致）→ 保持旧值 + WARN，绝不清成零值。** 清零值是危险的（`fee_rate=0` 就是免手续费）。

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

**代价与配套纪律：密钥类配置项必须建成 `type=DEFAULT`，绝不能建成 `WEB`**——否则它会随 `/api/config` 一路吐到浏览器里。secret 标记去掉之后，`type` 分类是唯一的闸。好在 `type` 是建配置项时定死的分类，不是一个随手能勾出泄露的开关（这正是它比 `public` 布尔字段好的地方）。

**本模块不解决第三阶段交接事项第 3 条**（connector 的 secret 字段脱敏与落库加密）。那条债务仍然挂着，而且现在多了一个立场冲突：配置中心决定密钥明文存库明文显示，connector 那边原计划是加密+脱敏。两者要么统一到明文、要么统一到加密，**需要单独裁决，不在本模块范围内**。真到加微信 AppSecret 那天必须先解决。

## 五、数据模型

```sql
-- ① 配置项 = schema。不含值。
CREATE TABLE config_item (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    application_id uuid NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    key            text NOT NULL,                    -- "upstream.timeout"
    type           text NOT NULL DEFAULT 'DEFAULT',  -- DEFAULT | WEB
    value_type     text NOT NULL,                    -- bool|int|float|string|array|object
    description    text NOT NULL DEFAULT '',         -- 备注，人维护，上报永不覆盖
    last_seen_at   timestamptz,                      -- NULL = 从未被上报过
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (application_id, key)
);

-- ② 版本。一次保存一行，装该应用**全部**键值的整快照。
--    当前值 = seq 最大那行的 values。不另设 config_value 表。
CREATE TABLE config_version (
    application_id uuid NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    seq            bigint NOT NULL,                  -- 应用内自增：v1 v2 v3
    values         jsonb NOT NULL,                   -- {"upstream.timeout": 3000, "fee_rate": 0.02}
    actor_id       uuid NOT NULL REFERENCES admin(id),
    comment        text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, seq)
);
```

**只有两张表。** 没有 `config_value`（当前值 = 最新版本的 `values`），没有 `changed_keys`（是派生数据，取 seq 与 seq-1 两行 diff 即可，存了就多一处会对不上的地方），没有 `pushed`（生效方式是一次性的动作参数，执行完就完了；而且它长得像"生效了没有"，实际记的是"保存那一刻推没推"，容易被误读）。

**存整快照而不是变更点。** 50 个配置项 × 500 次变更 ≈ 750KB，存储代价可以忽略，换来的是四种操作全都是一行 jsonb 的事：

```sql
-- 当前值
SELECT values FROM config_version WHERE application_id = $1 ORDER BY seq DESC LIMIT 1;
-- v6 那一刻的完整状态
SELECT values FROM config_version WHERE application_id = $1 AND seq = 6;
-- 回滚到 v6（v7 原样保留，不删）
INSERT INTO config_version (application_id, seq, values, actor_id, comment)
SELECT application_id, $2, values, $3, $4 FROM config_version WHERE application_id = $1 AND seq = 6;
-- 某个 key 的历史
SELECT seq, values -> $2 FROM config_version WHERE application_id = $1 ORDER BY seq DESC;
```

对比"只存变更点"的时态表设计：那个方案存储更省（750KB → 30KB，省的量毫无意义），但每次读当前值都要 `DISTINCT ON` 加一层 NULL 过滤，还要第三张表和一个复合外键。**用可忽略的存储换掉一整层查询复杂度是划算的。**

`seq` 在事务里取 `MAX(seq)+1`，靠主键约束兜并发；管理操作低频，冲突了重试即可。

**删除配置项**是一次版本变更：删 `config_item` 那行 + 新版本的 `values` 里去掉该 key。历史版本的 `values` 里它还在，所以回滚能把它恢复。删的若是一个从未配过值的项（「待接入」态），`values` 本来就没有它——只删 `config_item` 行，不生成空版本。

**未配置**的判据：`config_item` 有行，但最新 `values` 里没有该 key。SDK 的 `missing` 就是这些。

## 六、类型系统

### 6.1 类型表

fp 的类型系统**不带任何一门语言的特性**——将来接入其他语言时，控制台上看到的必须还是它认得的东西。

| fp `value_type` | Go 侧映射 | 控制台控件 |
|---|---|---|
| `bool` | `bool` | 开关 |
| `int` | 各整数类型；`time.Duration` **按毫秒**当整数处理 | 数字输入 |
| `float` | `float32` / `float64` | 数字输入 |
| `string` | `string` | 文本框 |
| `array` | 切片、数组 | JSON 数组 |
| `object` | `map`、标了 `fp:"json"` 的 struct | JSON 对象 |

**没有 `duration` 类型。** 其他语言没有这个概念。Go 侧 `time.Duration` 仍然能写，但那是 **Go SDK 的私事**：它按毫秒上报成 `int`，解析时乘 `time.Millisecond`，fp 完全不知道有这回事。单位约定写死在文档里，控制台上它就是个整数。

反射时必须**先判具体类型再判 Kind**——`time.Duration` 底层是 `int64`，顺序反了时长会被当成普通整数、丢掉毫秒换算。

不支持的类型（指针、interface、chan、func）在 `Bind` 时直接报错，不静默跳过。

### 6.2 值的存储与传输

`config_version.values` 里存 **JSON 原生类型**，不是一律字符串：

```json
{"upstream.timeout": 3000, "fee_rate": 0.02, "feature.new": true, "limits": [1, 2, 3]}
```

保存时按 `value_type` 转一次，转不过去才报错。于是：

- 控制台读回来直接是正确类型
- SDK 直接 `json.Unmarshal` 到 struct 字段的类型，不需要自己实现类型转换
- `value_type` **根本不用下发给 SDK**（协议因此少一个字段）

key 是**扁平的点分路径**（`upstream.timeout`）而不是嵌套 JSON。理由是它要和 `config_item.key` 对得上，控制台也按 key 逐项编辑、逐项做 diff。SDK 侧把扁平路径填进嵌套 struct 的那段逻辑本来就要写（要算 `missing`、要处理 `time.Duration`），多一层点路径不增加多少事。

### 6.3 弱约束

**类型是弱约束，不做 min / max / oneof 校验。** 保存时尝试按 `value_type` 转换，`"3"` 填进 `int` 字段照样过，**只有转不过去才提示**。

范围和枚举的校验交给业务方在 `Bind` 之后自己写一行 if。代价是错值已经落库并推给全线实例了才被发现——这条取舍认了。

连带一个简化：**上报时的类型冲突不阻断**。灰度期间 v1 报 `int`、v2 报 `string` 是可能的，但值统一按 JSON 存、各 SDK 按自己的 struct 解析、互不影响，所以 `value_type` 只是控制台的渲染提示，**以最后一次上报为准**即可，不需要冲突检测机制。真到 v1 是 `int` 而 v2 是 `object` 那种解不动的地步，v1 的 `Bind` 会因为转换失败而报错——那正是本节开头说的"强转换失败才提示"。

## 七、配置项的生命周期

### 7.1 四态，都是算出来的

上报是**全量快照**，但"这次没上报"**不等于**"消失了"——业务服务多半多实例，滚动发布时新旧版本同时在跑，交替上报会让配置项在两个状态间反复横跳。所以判据是"**多久没有任何实例报过它**"，与权限点同理。

**状态不存库：**

| type | last_seen_at | 状态 | 能删吗 |
|---|---|---|---|
| `WEB` | 恒 NULL | 前端配置 | 能 |
| `DEFAULT` | NULL | **待接入**（人已建好，代码还没上报） | 能 |
| `DEFAULT` | 阈值内 | 正常 | **不能** |
| `DEFAULT` | 超阈值 | 代码里已无人引用 | 能 |

阈值默认 30 分钟，按应用可配（沿用权限点的那一个）。滚动发布通常几分钟内完成，窗口内两个版本都在报，不会误判。

**「正常」态删不掉**这条同时挡住了一类误操作：v2 还没全量上线就把 v1 还在读的 key 删了。

**「待接入」态**顺带兜住另一个陷阱：手建配置项时 key 抄错了（比如把 `upstream.timeout` 写成 `upstream.time_out`），代码上线后它会一直挂在「待接入」——错误自己会暴露出来，不需要额外的校对机制。

### 7.2 上报的处理规则

1. 快照里有、库里也有 → 刷 `last_seen_at`，`value_type` 以本次上报为准
2. 快照里有、库里没有 → 新建，`type` 默认 `DEFAULT`，`description` 留空由人填
3. 快照里没有的**一律不动**——状态是算出来的，不需要写库
4. `description` 与 `type` **只由人维护，上报永不覆盖**（同 `permission.name`）

规则 4 的含义包括：人手建了一个 `type=WEB` 的项，后端某天也在 struct 里声明了同名 key，上报撞上时**合并**——刷 `last_seen_at`、保留人填的 `description` 和 `type=WEB`。key 撞上就是同一个配置项，它从此进入正常的生命周期管理。

**多次 `Bind` 必须累积上报并集。** 一个进程里绑两个 struct 时，第二次上报若只带自己那份快照，会让第一份的 key 全部停止刷新 `last_seen_at`、30 分钟后被判成「代码里已无人引用」。SDK 内部维护一张累积的注册表，每次 `Bind` 后重新上报并集。

### 7.3 `type` 的语义

`type` 表达的是**"这一项会不会被下发到前端"**，不是"归谁用"：

- `DEFAULT` —— 不下发。密钥类必须是这个（4.7）
- `WEB` —— 会出现在 `BindType(client, "WEB")` 拉到的那份里

`WEB` 项后端只要在 struct 里声明了照样能读。所以 `site.title` 这种前后端都要用的东西不必建两个 key、不必同步两份值。

`type` 是可扩展的枚举（将来可能有 `MOBILE`、`MINIPROGRAM`），SDK 的 API 收 string 而不是 enum。

## 八、SDK 形态

### 8.1 两个绑定入口

```go
// ① 后端自己用的：绑 struct，参与上报，缺值会报错
cfg, err := fpsdk.Bind[ShopConfig](client)
cfg.Load()                                        // *ShopConfig
cfg.OnChange(func(old, new *ShopConfig) { ... })

// ② 转发给前端的：按 type 拉全量，不绑 struct、不参与上报、没有缺值概念
web, err := fpsdk.BindType(client, "WEB")
web.Load()                                        // map[string]any
web.OnChange(func(old, new map[string]any) { ... })
```

`BindType(client)` 不传类型就是全部。

两者是同一套形态（`Load` + `OnChange`），共用同一条 Watch 流、同一次 `GetConfig`、同样遵守「快照没变就不触发」。

**`BindType` 返回 `map[string]any` 而不是 `map[string]string`**：SDK 按 JSON 解析一遍再交出去，业务方 `json.Encode` 出来直接是 `{"feature.new": true, "limits": [1,2,3]}`，而不是一堆 `"true"` / `"[1,2,3]"` 要前端再转一次。

### 8.2 前端读配置

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

### 8.3 实现约束清单

1. 快照没变就不换指针、不触发 `OnChange`（4.4）
2. 热更新时某个 key 消失 → 保持旧值 + WARN，绝不清成零值（4.4）
3. 多次 `Bind` 累积上报并集（7.2）
4. `time.Duration` 反射时先判具体类型再判 Kind（6.1）
5. `MissingConfigError` 一次列全，不是报第一个就返回（4.2）

## 九、协议

新建 `ConfigService`，**推送仍走已有的 Watch 长流**（主设计 3.3 定的"一条流统一回源与推送"，`WatchResponse` 的 oneof 早就为此预留了扩展位）。

```protobuf
service ConfigService {
  // ReportSchema 上报本进程绑定的全部配置项。全量快照，与 ReportPermissions 同构。
  rpc ReportSchema(ReportSchemaRequest) returns (ReportSchemaResponse);

  // GetConfig 拉取该应用当前的全部配置值。
  rpc GetConfig(GetConfigRequest) returns (GetConfigResponse);
}

message ReportSchemaRequest {
  repeated SchemaItem items = 1;   // 全量快照，缺失的一律不删（见 7.2）
}
message SchemaItem {
  string key        = 1;           // "upstream.timeout"
  string value_type = 2;           // bool|int|float|string|array|object
}
message ReportSchemaResponse {}

message GetConfigRequest {}
message GetConfigResponse {
  // version 是 config_version.seq。
  int64 version = 1;
  // values 是整个 JSON 对象，原样来自 config_version.values。
  // 刻意不拆成 map<string,string>：值是 JSON 原生类型，拆开会退化成
  // 一堆待解析的字符串，SDK 就得自己实现一遍类型转换。
  string values = 2;
  // web_keys 是 values 里 type=WEB 的那些 key，供 BindType 过滤。
  repeated string web_keys = 3;
}

// WatchResponse 的 oneof 新增一个分支。
message ConfigChanged {
  int64 version = 1;
}
```

**`GetConfig` 返回该应用全部的值，由 SDK 自己按 struct 挑并算出 `missing`。** 因此服务端不必记住"哪个实例上报了哪些 key"——无状态，多实例、灰度期新旧版本并存都不用特殊处理。

`value_type` 不下发（6.2）。

## 十、生效方式与发布协同

### 10.1 一次保存 = 一个版本

表单上改多少项都行，点一次保存 → 生成一个版本 → 推一次。**SDK 看不到中间态**（改了 `fee_rate` 还没改 `fee_min` 的那一瞬间）。没有草稿态、没有"编辑中"，不引入 Apollo 那套两阶段发布。

### 10.2 保存时选生效方式

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

回滚同样要选生效方式——回滚就是生成一个新版本，没有例外。

### 10.3 不做灰度下发

按实例标签定向下发（Apollo 的灰度发布）是这个问题唯一严谨的解——新旧实例同时在跑也各拿各的值，没有 10.2 那个漏洞。**本期不做**：它要给表、UI、推送各加一层 label 维度，而"仅落库、重启生效"已经覆盖了滚动发布这个主场景。

将来要加是纯增量（`config_version` 加一个可空的 label 维度），已有数据和判定逻辑都不用改。

### 10.4 改名与不兼容变更

**改 key（字段改名）是安全的**，因为 fp 上的配置项是"所有在跑版本的并集"，不是某一版代码的镜像：

```
线上 v1：struct 有 A、B、xxx        fp 上：A、B、xxx
① 提前建好 bbb 并填值 → fp 上：A、B、xxx、bbb → 新版本 → 推送
② v1 拉全量 → 按自己的 struct 取 A、B、xxx；bbb 不在 struct 里，忽略 → v1 无感
③ v2 上线 → 读 bbb ✓，xxx 从此没人上报
④ 30 分钟后 xxx 转为「代码里已无人引用」→ 人工确认后删除
```

①那次推送会让 v1 拉一次全量，但解出来的 struct 一个字段都没变——这正是 4.4 那条"快照没变就不触发 `OnChange`"要挡的情况。

**改语义（key 和类型都没变，意思变了）fp 检测不到**：单位从秒改成毫秒、枚举值换了一批，类型一样、值也合法，只是意思变了。这是**纪律不是机制**：

> **不兼容的语义变更必须换 key。** 与数据库改列语义要新开一列是同一条规矩。

## 十一、控制台

**配置中心页**（选应用）：

- 配置项列表，按 key 的点前缀折叠分组，每行按 `value_type` 渲染控件
- 顶部两条醒目提示：**N 项未配置**（红）、**N 项代码里已无人引用**（黄）
- 每行可编辑：值、`description`、`type`（改 `type` 要二次确认——`DEFAULT` 改成 `WEB` 就是把它下发到浏览器）
- `value_type` 人工也能改，但只对 `WEB` 项有意义：`DEFAULT` 项下次上报会按 7.2 规则 1 以代码为准覆盖回去。UI 上要说明这一点，不要让人以为改了就定了
- `[新建配置项]`：key + type + value_type + 值 + 备注。**建的时候必须同时填值**（没有代码强制它，空着没意义）
- `[保存]`：选生效方式 + 备注

**版本历史 tab**：版本列表（seq / 时间 / 操作人 / 备注 / 本次改了哪些 key）+ 两版 diff + 回滚。"改了哪些"取 seq 与 seq-1 两行 `values` 在应用层 diff，不存字段。

**回滚前必须提示"回滚后这些项将变成未配置"**——回滚到 v6 时，v7 才新增的配置项在 v6 的 `values` 里没有，运行中的实例会走 4.4 那条"保持旧值 + WARN"，而新启动的实例会缺值起不来。

**不复用 `DynamicForm`。** 结构差异太大（那是 connector 的单个动态表单，这是带分组、状态徽标、批量保存的列表），而且第三阶段终审查出它的 DOM id 没加命名空间会让同名字段串台——配置项的 key 带点、撞得更狠。新组件从第一行起就带命名空间。

前端工程配置改动前先读 `docs/console.md` 里那六条与官方文档冲突的坑（第三阶段交接事项第五节）。

## 十二、与现有代码的接触面

| 位置 | 改动 |
|---|---|
| `proto/fp/v1/` | 新增 `config.proto`；`WatchResponse` 的 oneof 加 `ConfigChanged` 分支 |
| `internal/domain/` | 新增 `ConfigItem`、`ConfigVersion` 及状态判定 |
| `internal/store/migrations/` | 新增迁移（两张表） |
| `internal/service/` | 新增 `ConfigService`：上报合并、取值、保存、回滚、删除保护 |
| `internal/grpcapi/` | 新增 `ConfigService` 的 gRPC 实现；推送接入既有 Watch 流 |
| `internal/httpapi/` | 新增控制台的配置中心路由 |
| `sdk/` | 新增 `config.go`（反射、绑定、快照、回调）、`bindtype.go` |
| `web/src/pages/` | 新增配置中心页与版本历史 |

**不引入新依赖。** 反射、`encoding/json` 都在标准库里，`sdk/arch_test.go` 的分层约束不受影响。

## 十三、测试策略

第二、三阶段的教训是"服务端有信息、传输层丢了，而所有测试照绿"，以及"写计划时强调了不等于守住了"。所以下面每一条都要能回答：**哪一条测试会因为违反它而变红？** 标注「辨别力」的，实现时必须做一次**变异验证**——把实现改坏跑一次确认真的变红，再改回来。

1. **反射生成 schema**（表驱动）：各 Go 类型 → `value_type`；`time.Duration` 走 int 毫秒；嵌套 struct 展开成点分组；`fp:"json"` 整体成 object；不支持的类型报错。
   **辨别力**：`time.Duration` 那条必须断言毫秒换算，只断言"类型是 int"的话，先判 Kind 后判具体类型的错误实现照样绿。

2. **`missing` 列全。**
   **辨别力**：至少缺 2 项，断言两项都在——只缺 1 项的话，"报第一个就 return"的实现也会绿。

3. **快照相同不触发 `OnChange`。** 推一次内容完全相同的配置，断言回调 0 次、指针未变。这条对应 10.4 那个"别人配自己的新 key"场景。

4. **「仅落库、重启生效」。**
   **辨别力**：两条腿都要断言——没有 `ConfigChanged` 发出，**并且**新起一次 `Bind` 能拿到新值。只断言前者的话，"根本没存"的实现也会绿。

5. **上报合并保留人填的 `description` 与 `type`。**
   **辨别力**：必须先人工改过 `description`、把 `type` 设成 `WEB`，再上报一次，断言两者都还在。直接测"上报能新建"是测不到覆盖问题的。

6. **多次 `Bind` 累积上报并集。**
   **辨别力**：绑两个 struct，断言第一个 struct 的 key 在第二次上报后 `last_seen_at` 仍被刷新。只断言"两个 struct 都能取到值"是测不出这个的——取值走的是全量，不受上报影响。

7. **四态判定**用可控时钟逐态断言，含「阈值内不算消失」与「超过阈值算消失」两条边界；以及**「正常」态删不掉**的保护。

8. **回滚生成新版本而非删除历史**：回滚到 v6 得到 v8，断言 v7 仍在且内容未变。

9. **回滚导致某 key 消失时 SDK 保持旧值**，并断言打了 WARN。

10. **`BindType` 的过滤与解析**：建 `DEFAULT` 和 `WEB` 两类项，断言 `BindType(client, "WEB")` 只拿到 WEB 那些，且 bool / array 解析成了真正的 JSON 类型而不是字符串。
    **辨别力**：两类项都必须有值，否则"过滤对了"和"压根没过滤"产出同样结果。

11. **弱类型转换**：`"3"` 存进 `int` 项成功、`"abc"` 失败并给出可读错误。

12. **端到端穿透**：控制台改值 → 断言 SDK 侧 `cfg.Load()` 真的变了、`web.Load()` 真的变了。对应第三阶段那条教训——只测服务端返回值不够，要穿到 SDK 出口。

## 十四、对主设计文档的反转记录

| 主设计文档原文 | 本模块的决定 | 理由 |
|---|---|---|
| §6「层级：应用 × 环境（dev/test/prod）」 | **删掉环境维度** | 与"一个 fp 部署 = 一套用户体系"矛盾，环境必须靠部署隔离（4.5） |
| §6「本地快照文件兜底（fp 挂了业务不挂）」、§10.3「配置：使用本地快照文件」 | **不做快照** | 代价是 fp 不可达期间业务方进程无法重启（4.6） |
| §6「加密配置项（密钥类，落库加密，UI 上脱敏显示）」 | **不做 secret 类型** | 配套纪律：密钥类必须建成 `DEFAULT`（4.7） |
| §6「配置项带 schema：类型 / **默认值** / **校验规则** / 说明 / 是否加密」 | **无默认值、无校验规则** | 默认值省不下那次人工动作且制造歧义（4.2）；校验规则本期从简（6.3） |

前两条是主设计文档里"fp 挂了业务不挂"这条承诺的实质削弱，第三条是安全面的实质放宽。**都是有意识的取舍，不是遗漏**——但主设计文档第六节应当同步更新，否则下一个人读到的是两份互相矛盾的设计。

## 十五、实施顺序建议

1. 数据模型与迁移（两张表）
2. 服务端：上报合并、四态判定、取值
3. proto + gRPC：`ConfigService`、`WatchResponse` 加分支
4. SDK：反射生成 schema、`Bind`、`MissingConfigError`
5. SDK：热更新、`OnChange`、快照比较
6. SDK：`BindType`
7. 服务端：保存 / 版本 / 回滚 / 删除保护 / 生效方式
8. 控制台：配置项列表与表单
9. 控制台：版本历史与回滚
10. 端到端穿透测试（第十三节第 12 条）

1–6 完成时就能验证"改配置不用发版"这个核心命题（用 SQL 直接改 `config_version` 即可），不必等控制台。
