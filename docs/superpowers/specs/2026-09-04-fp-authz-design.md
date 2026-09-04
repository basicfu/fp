# fp 授权模块设计

**日期：** 2026-09-04
**状态：** 已确认，待实现

## 一、目标

fp 现在能回答"你是谁"，回答不了"你能做什么"。每个接入方仍然要自己造一套权限系统——这正是 fp 要消灭的复制粘贴。设计文档第五节把授权列为 IAM 的另一半，本设计是它的落地。

核心诉求（来自设计文档 1.1）：**权限路径从代码变成注册数据**。3s 那 200 行硬编码的接口路径数组，加一个接口就要改数组再发版，是这个模块要解药的东西。

## 二、范围

**本期做**

- 功能权限：角色 → 权限点 → 能不能调这个接口
- 权限点由 SDK 启动时**自动上报**（业务方零标注）
- 角色继承、allow/deny、应用级默认角色
- 策略编译与推送，SDK 侧本地判定不走网络
- 控制台：角色管理、权限点管理、给用户分配角色

**本期不做（各自附理由，不是遗漏）**

- **数据范围与组织树**（设计文档 5.3 原本就留给实现阶段判断）。数据范围必然侵入业务方的查询语句——SDK 要给出可拼接的过滤条件——接入成本高很多。casbin 的 model.conf 一开始就把维度留好，以后加不用重构策略结构。
- **菜单与按钮权限**（5.4）。`permission` 表的 `kind` 与 `parent_id` 两列现在就建好，SDK 上报协议里也留了对应字段：以后加菜单只是多一种 `kind` 加一个枚举接口，不需要改表、更不需要让所有已接入方重新上报一遍。**这两列现在留着不启用，是为了避免那次迁移，不是投机。**
- **权限点分组**（"订单相关权限"这类）。路径自动收集会产出几百个权限点，角色编辑页需要分组才可用。但结构上分组就是树里的非叶子节点，可以复用已留的 `parent_id`，不需要新表。本期不做，需要时再说。
- **全局用户扩展字段**。用户级（非 per-app）的自定义字段，需要界面能动态增删字段、以及字段级的可见/可改规则（参考 Casdoor 的 Public / Self / Admin 三档）。独立一块，后期做。

## 三、与 Casdoor 的对照

设计文档把 Casdoor 列为业界参考（它同样基于 casbin）。实际查证后，有三处刻意分道，记录理由以免以后被当成疏漏。

**Casdoor 的模型**：容器是 Organization。用户属于恰好一个组织，标识 `组织名/用户名`，**可以登录该组织下的任意应用**——没有 user↔application 关系表。Role 的字段是 `Owner`(=组织)、`Name`、`DisplayName`、`IsEnabled`、`Users[]`、子角色。Permission 的字段是 `Organization`、`Model`、`Adapter`、`SubUsers[]`、`SubRoles[]`、`SubGroups[]`、`SubDomains[]`、`ResourceType`、`Resources[]`、`Actions[]`、`Effect`。文档明确说 Permission **不直接指向应用**，应用隔离靠 `SubDomains`（casbin 的 domain 维度）。用户的自定义字段是 User 上的 `Properties map[string]string`，组织级共享。

| | Casdoor | fp | 为什么分道 |
|---|---|---|---|
| 角色归属 | 组织级（全局） | **同样全局** | 早先曾设计成应用级，后改回与 Casdoor 一致。关键理由是「普通用户」这类角色天然跨应用——在商城能下单、在视频能观看，是同一个身份的两面；应用级会强迫每个应用各建一个同名角色，纯属重复。应用专属的角色靠命名约定区分（商城管理员 / 视频管理员）。角色的应用归属由它挂了哪些应用的权限点决定，不由角色自己声明 |
| 角色与用户的关系存在哪 | `role.Users[]`（角色持有用户列表） | **`user_role.roles[]`（用户持有角色列表）** | fp 的热路径是**每次登录取某人在该应用的角色**，Casdoor 的形状要扫全部角色才能回答；而"谁是管理员"在 fp 只是控制台的偶发查询。另外 Casdoor 给某人分配角色要重写一整行可能装着上千用户的记录，fp 只改一个用户的行 |
| 用户扩展字段 | User 上的 `Properties`，组织级 | 本期不做，后期做**全局**（与 Casdoor 一致） | 曾考虑做成 per-(用户, 应用)，理由是同一个人在 A 是"商户"、在 B 是"学员"。最终按全局做，与 Casdoor 一致，避免同一份资料散落在多行 |

**该抄而本期没抄的**：Casdoor 的字段级可见性规则（Public / Self / Admin，决定某个扩展字段谁能看谁能改）。做全局扩展字段时一并抄。

## 四、数据模型

```sql
-- ① 用户在某个应用下的注册事实。首次登录写入一次，之后不再改。
CREATE TABLE user_extra (
    user_id        uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    application_id uuid NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    first_login_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, application_id)
);
CREATE INDEX user_extra_app_idx ON user_extra (application_id, first_login_at DESC);

-- ② 授权关系。**每人一行**，不按应用拆——角色是全局的。
-- 稀疏：没有行表示"只有各应用的默认角色"。
CREATE TABLE user_role (
    user_id    uuid PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    roles      text[] NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_role_roles_idx ON user_role USING gin (roles);

-- ③ 角色。**全局**，不带 application_id。
--
-- 角色的应用归属由它挂了哪些应用的权限点决定（见 role_permission），
-- 不由角色本身声明。这让「普通用户」这类天然跨应用的角色只需建一个、
-- 同时挂上商城与视频的权限点，而不必在每个应用里各建一个同名角色。
--
-- 应用专属的角色靠**命名约定**区分：商城管理员 / 视频管理员 / 商城客服。
-- 这是约定不是约束——没有机制阻止有人建一个不带前缀的「管理员」并挂上
-- 两个应用的权限。当前只有一个平台管理员，这不构成问题；将来若要做
-- 应用级的角色隔离，给本表加一个**可空**的 application_id
-- （NULL = 全局角色，有值 = 只属于该应用）即可，已有数据与判定逻辑都不用改。
--
-- key 是身份，**不可修改**；要改显示文字改 name。理由见本节末尾的说明。
CREATE TABLE role (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    key        text NOT NULL UNIQUE,           -- "商城管理员"
    name       text NOT NULL,
    parent_id  uuid REFERENCES role(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- ④ 权限点。
--
-- 命名说明：Casdoor 里 "Permission" 指的是授权本身（主体+资源+动作+效果），
-- 而这里 permission 指的是**被授权的那个东西**，role_permission 才是授权。
-- 对着 Casdoor 文档看本表时注意这个差异。
--
-- key 可以修改（一条 UPDATE）：role_permission 按 permission_id 引用，
-- 授权关系自动跟随，只需推一次 PolicyChanged 让 SDK 重拉扁平表。
-- 改完之后下次上报会如实反映代码——代码里的路由确实是新值就匹配上并
-- 刷新 last_seen_at；代码里还是旧值就把旧的重新建出来（那个路由真的存在，
-- 在控制台改成代码不提供的路径本来就是撒谎）。
CREATE TABLE permission (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    application_id uuid NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    key            text NOT NULL,                  -- "GET:/orders/{id}"
    name           text NOT NULL DEFAULT '',       -- 显示名，人维护，上报不覆盖
    kind           text NOT NULL DEFAULT 'api',    -- menu/button 本期不启用
    parent_id      uuid REFERENCES permission(id) ON DELETE SET NULL,  -- 菜单树/分组用，本期为空
    source         text NOT NULL,                  -- app | manual
    last_seen_at   timestamptz,                    -- manual 的为 NULL
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (application_id, key)
);

-- ⑤ 角色 → 权限点。两个外键，不再抄一份 application_id——
-- 权限点属于哪个应用由 permission 自己说。
CREATE TABLE role_permission (
    role_id       uuid NOT NULL REFERENCES role(id) ON DELETE CASCADE,
    permission_id uuid NOT NULL REFERENCES permission(id) ON DELETE CASCADE,
    effect        text NOT NULL DEFAULT 'allow',   -- allow | deny
    PRIMARY KEY (role_id, permission_id)
);

-- ⑥ 应用的默认角色。roles 为空的用户按它判定。
ALTER TABLE application ADD COLUMN default_role_key text NOT NULL DEFAULT '';
```

⑤ 上那两条外键就是"删权限点连带删授权关系"的执行点——由数据库保证，不靠代码记得清理。

**`user_role.roles` 是 `text[]` 而不是关系表**，因此它没有外键。这是刻意的取舍：数组人眼可读、登录时零 join（会话与 SDK 策略表里用的都是 key 字符串，关系表要多一次 join 才能拿到）。代价是删角色时必须连带清理：

```sql
UPDATE user_role SET roles = array_remove(roles, $1) WHERE roles @> ARRAY[$1];
```

GIN 索引直接命中，一条 UPDATE。这条耦合放在 service 层的删除方法里，**并配一条会因为漏做而变红的测试**——与仓库既有的"冻结账号必须连带撤销会话"是同一类处理。

**`role.key` 不可修改，`permission.key` 可以。** 这个不对称有具体理由，不是任意规定：

| | key 可改 | 为什么 |
|---|---|---|
| `role.key` | 否 | `user_role.roles[]` 按**字符串**引用它（没有外键），且**已签发会话里刻着它**——改了之后那批用户会在会话刷新前丢掉这个角色 |
| `permission.key` | 是 | `role_permission` 按 **id** 引用，授权关系自动跟随；会话里不含权限点 key。改完推一次 `PolicyChanged` 让 SDK 重拉即可 |

也就是说：`role.key` 不可改是选择 `user_role.roles text[]` 的直接后果。哪天它改成按 `role_id` 的关系表，`role.key` 也就能改了。

角色因此只剩"删除"一种需要连带清理的操作。

**`user_application` 改名为 `user_extra` 并瘦身**：原表的 `nickname` / `status` / `extra` 三列自建表起从未被任何代码读写，删除。它们对应的 per-app 用户资料与状态两个功能都还没做，且 per-app `status` 真要启用需要改登录流程同时判全局与应用内状态，不是加列就完事。留着的唯一后果是让下一个人以为有代码在用——这个仓库刚被 `ApplicationStatusDisabled` 坑过一次（有字段、有常量、有 DTO 输出，就是没人写它，直到第三阶段才发现"停用应用"根本触发不了）。

**原表上"严禁添加 role 列（设计文档 5.5）"那条注释**防的是授权模块**交付之前**的简化字段，现在正是它说的交付时刻。角色落在 `user_role` 而不是这张表上，5.5 的意图（不留过渡态、直接接 casbin）得到满足。

## 五、权限点的身份：路径

权限点的 key 是 `方法:路由模式`，例如 `GET:/orders/{id}`。

**为什么用路径而不是显式权限码**：显式码（`fpsdk.Require("order:create")`）能让权限身份与 URL 解耦，URL 重构不影响授权。但一个项目几百个接口都要逐个标注，现实中没人会坚持下去——而没被标注的接口就是没有保护的接口。路径方案零标注，代价是**改 URL 等于换了一个权限点**：老的进"过渡中"，新的以"正常"出现，授权关系不会自动跟过去。这个代价是可见的（控制台上看得到），不是静默丢权限。

**必须用匹配到的路由模式，不能用原始 URL**，否则 `/orders/123` 与 `/orders/456` 会变成两个权限点。已实测 chi 的可行性：

- 顶层中间件里 `chi.RouteContext(ctx).RoutePattern()` 是**空串**（路由尚未匹配）
- 子路由中间件里只到 `"/orders/*"`
- handler 里才是完整的 `"/orders/{id}"`——但鉴权必须在 handler 之前拦下

解法是在顶层中间件里主动做一次试匹配，已验证可行：

```go
rctx := chi.RouteContext(req.Context())
probe := chi.NewRouteContext()
if rctx.Routes.Match(probe, req.Method, req.URL.Path) {
    pattern := probe.RoutePattern()   // "/orders/{id}"
}
```

嵌套路由与多路径参数都正确：`DELETE /orders/9/items/7` → `/orders/{id}/items/{itemId}`。

## 六、权限点的生命周期

上报是**全量快照**，但"这次没上报"**不等于**"消失了"——业务服务多半多实例，滚动发布时新旧版本同时在跑，交替上报会让权限点在两个状态间反复横跳。所以判据是"**多久没有任何实例报过它**"。

**状态不存库，算出来：**

| 界面状态 | 判据 |
|---|---|
| 手动 | `source = 'manual'` |
| 过渡中 | `source = 'app'` 且 `now - last_seen_at > 阈值` |
| 正常 | 其余 |

阈值默认 30 分钟，按应用可配。滚动发布通常几分钟内完成，窗口内两个版本都在报，不会误判。

**没有"已停用"状态。** 人看到"过渡中已经好几个小时"就自己删，**删就是真删**（连带删授权关系，见第四节的外键）。删除前控制台必须提示"当前有 N 个角色持有它"——这是唯一的安全网，因为没有可逆的中间态。

**过渡是自动且可逆的**：误删接口、下个版本又加回来，下次上报 `last_seen_at` 一刷新就自动回到"正常"，不需要人操作。回滚同理。

**人工改过的不被上报覆盖**：`name` 只在权限点**首次创建时**写入；`parent_id` 人工改过之后上报不再动它。否则每次重启，运营填的中文名和整理好的归类就没了。

## 七、角色的解析与缓存

**角色在签发会话时解析好，刻进会话。**

fp 用的是不透明 token（随机串 + Redis 会话），不是 JWT——所以不受"签发后改不了"的限制，会话里的角色可以就地更新。

```
在应用 X 里的有效角色 = 用户的全局角色 ∪ { X 的默认角色 }

登录 → 查 user_role（每人一行，全局）
     → 并上 application.default_role_key
     → 连同 Epoch 一起刻进 domain.Session
```

新注册用户 `user_role` **一行都不写**，天然只有该应用的默认角色。

**判定路径完全不碰数据库**：SDK 每次 `Allow` 用的是会话里带回来的角色 + 本地策略表。读 `default_role_key` 只在登录那一刻发生一次。

**为什么是并集，而不是"没有显式角色时才用默认"**：角色是全局的，如果改成"有显式角色就不用默认"，那么——

```
张三被设为「商城管理员」→ user_role 有行了
→ 他登录视频时全局角色是 ['商城管理员']
→ 商城管理员在视频没有任何权限
→ 张三连视频都看不了了
```

给他加一个商城的管理权限，顺手把他看视频的能力弄没了。所以必须并入默认角色。

并集的两个常见顾虑在全局角色下都不成立：

- **"界面看不全"**——控制台显示 `商城管理员 + 普通用户（默认）`，并集是显式可见的
- **"想让某人低于基线做不到"**——用 deny：给他一个 `黑名单` 角色带 `deny: 视频观看`，deny-override 直接盖掉

**角色不需要按应用过滤。** 会话里刻的是用户的全局角色全集；某个应用的 SDK 本地策略表里只有**在该应用有权限点的角色**，其余角色查不到条目、自然不贡献任何权限。所以张三带着「商城管理员」去视频，那个角色在视频的策略表里根本不存在，等同于没有。

**控制台要区分"默认"与"显式"**：

```
张三    商城管理员 + 普通用户（默认）
李四    普通用户（显式）+ 普通用户（默认）→ 显示为 普通用户
```

把应用默认角色从「普通用户」改成「访客」时，跟随默认的那批人会跟着变。界面上要能看出某个角色是显式分配的还是跟随默认，否则管理员改一次默认角色会莫名其妙影响一批人而看不出原因。

## 八、策略编译与推送

**服务端用 casbin，SDK 不用。**

服务端把 `role` + `role_permission` 编译成 casbin policy，用 `GetImplicitPermissionsForUser` **展开角色继承**，推给 SDK 的是每个角色的**隐式权限全集**——一张扁平表：

```
推给「商城」这个应用的 SDK：
  商城管理员  → allow: [GET:/orders/{id}, POST:/orders, DELETE:/orders/{id}]  deny: []
  普通用户    → allow: [GET:/orders/{id}, POST:/orders]                        deny: []
  黑名单      → allow: []                                                      deny: [POST:/orders]

推给「视频」这个应用的 SDK：
  普通用户    → allow: [GET:/videos/{id}]                                      deny: []
  视频管理员  → allow: [GET:/videos/{id}, DELETE:/videos/{id}]                 deny: []
```

注意「商城管理员」不出现在视频那份表里——它在视频没有任何 `role_permission` 行。所以张三带着这个角色去视频，查不到条目、不贡献任何权限，等同于没有。**角色是全局的，但每份策略表只装该应用用得上的那些。**

SDK 侧的判定退化成：取用户角色 → 查表 → **有 deny 即拒，有 allow 即过，都没有则拒**（默认拒绝，deny-override）。

**为什么 SDK 不带 casbin**：

1. SDK 是业务方要 import 的库，不给它塞 casbin 及其传递依赖是实打实的好处
2. 判定变成纯 map 查找，可以穷举测试
3. 服务端仍然用 casbin，`model.conf` 的表达力留着——以后加数据范围时改的是服务端，**不动已发布的 SDK**

代价是服务端多一步展开继承，`GetImplicitPermissionsForUser` 直接给这个，不用自己写图遍历。

**推送复用已有的 Watch 长流**（第二阶段建的撤销推送通道），新增两类事件：

| 事件 | 触发 | SDK 的动作 |
|---|---|---|
| `PolicyChanged{appID}` | 改角色、改角色权限、删权限点 | 拉一次全量策略（角色数 × 权限点数，很小） |
| `UserRoleChanged{userID}` | 改某人的角色、改应用默认角色 | 丢掉该用户的缓存，下次校验时 fp 返回新角色 |

角色变更**立即生效**，且**不踢人**——`UserRoleChanged` 只让 SDK 丢缓存重新校验，不影响登录态。这与 `Epoch` 的语义相反：`Epoch` 不一致是**拒绝**（撤销），角色变更是**刷新**。

新 SDK 实例启动时拉一次全量策略；`WatchPurge`（第二阶段已有的、订阅重连后的兜底）到达时同样重拉。

## 九、SDK 形态

**核心是框架无关的纯函数**，不碰 `http.Request`，只吃字符串：

```go
func (a *Authz) Allow(ctx context.Context, userID, method, pattern string) (bool, error)
```

**为什么返回 `(bool, error)` 而不是单个 `error`**：调用方必须分得清"**没权限**"和"**没能判定**"，这两件事的处理完全相反——前者是 403、业务照常运行；后者是可用性事件（fp 不可达、本地策略还没拉到），业务方可能要降级、告警、或按自己的策略放行内部接口。`false, nil` 是明确拒绝，`_, err` 是"我不知道"。这与 SDK 里 token 校验那套 `MaxStaleness` 降级思路一致。

**为什么叫 `Allow` 不叫 `Enforce`**：`Enforce` 是 casbin 的术语，而我们刻意不让 SDK 依赖 casbin，API 上也不该泄露它。

**框架适配器是可选的薄壳**，各二三十行，只干一件事：从该框架取出匹配到的路由模式，然后调核心。

| 框架 | 取路由模式 | 上报枚举 |
|---|---|---|
| chi | 顶层中间件里试匹配（第五节已验证） | `chi.Walk` |
| gin | `c.FullPath()` | `engine.Routes()` |
| echo | `c.Path()` | `e.Routes()` |
| 标准库 `ServeMux` | **没有**路由模式的概念 | 没有枚举接口 |

标准库那一行是实话：**它给不了自动化**。用 `ServeMux` 的人直接调 `Allow` 并传自己的资源字符串，权限点用 `Declare` 手动声明。这不是缺陷，是那个框架本身没有路由模式这个东西——文档要写明，不假装能自动搞定。

```go
// 上报
fpsdk.CollectChi(router, fpsdk.StripPrefix("/api/v1"))
fpsdk.CollectGin(engine)
fpsdk.Declare([]Point{...})   // 手动；以后的菜单按钮也走这个

// 鉴权
fpsdk.ChiAuthz(client)   // 中间件
ok, err := fp.Authz().Allow(ctx, userID, "GET", "/orders/{id}")  // 直接调
```

`StripPrefix` 的作用是让分组推导有意义（`/api/v1/orders/{id}` 不该被归到 `api` 组）。本期不做分组，但前缀配置现在就留，避免以后要求所有接入方改初始化代码。

**"哪些路由归 fp 管"就是"你在哪里调了 `Allow`"**——不需要额外开关。没被问到的路由 fp 一概不管（公开接口、健康检查、回调本来就该这样）；问到的默认拒绝：你既然显式问了，那就该管住。

## 十、上报协议

```protobuf
rpc ReportPermissions(ReportPermissionsRequest) returns (ReportPermissionsResponse);

message ReportPermissionsRequest {
  repeated PermissionPoint points = 1;   // 本次的全量快照
}

message PermissionPoint {
  string key    = 1;   // "GET:/orders/{id}"
  string kind   = 2;   // 本期恒为 "api"
  string parent = 3;   // 父权限点的 key（不是 uuid，SDK 不知道 id）；本期恒为空
  string name   = 4;   // 可选；SDK 给不出时留空，由人在控制台填
}
```

服务端处理：

1. 快照里的每一条 → 存在则刷新 `last_seen_at`；不存在则新建（`source='app'`，`name` 按上报值写入，`parent` 按 key 解析成 `parent_id`——解析不到就留空，不因为一个父节点没上报而整批失败）
2. **快照里没有的一律不动**——状态是算出来的，不需要写库
3. `source='manual'` 的**永远不被上报触碰**（业务方的路由清单里当然没有人手动加的东西）
4. `name` / `parent_id` **只在首次创建时**由上报写入，之后不覆盖

`kind` 与 `parent` 现在恒为 `api` / 空，但协议里留着——以后 SDK 上报菜单时不用改协议版本，老服务端也能忽略它们。

## 十一、测试策略

第三阶段的教训是"服务端有信息、传输层丢了，而所有测试照绿"——因为没有测试跨越边界。本模块的测试重点同理。

1. **判定逻辑穷举。** SDK 侧的判定是纯函数（角色集 × 权限点 → 允许/拒绝），把 allow/deny/都没有/多角色叠加/deny-override 全部列成表驱动测试。这是本模块唯一有真实逻辑的地方，必须测死。
2. **默认角色的两条分支。** `user_role` 有行 vs 无行，分别断言解析出的角色。**辨别力要求**：无行那条的前置必须让应用配了一个与显式角色不同的默认角色，否则"用了默认"和"用了显式"产出同样结果，测不出顺序写反。
3. **角色继承展开。** `order-admin.parent = normal`，断言推给 SDK 的 `order-admin` 条目里**包含 normal 的权限点**。只测服务端 casbin 的返回值不够——要断言推出去的那份扁平表。
4. **路由模式提取。** chi 适配器对嵌套路由、多路径参数、以及未匹配路径的行为。第五节的实测要固化成测试。
5. **上报的三条规则。** 快照缺失不删；`manual` 不被触碰；`name`/`parent` 人工改过后不被覆盖。第三条的**辨别力要求**：必须先人工改过 `name` 再上报一次，断言改动还在——直接测"上报写入 name"是测不到覆盖问题的。
6. **过渡中的判据。** 用可控时钟，断言"阈值内不算消失"与"超过阈值算消失"，以及"重新上报后回到正常"。
7. **删权限点连带删授权。** 断言外键真的级联了，而不是只在应用层删了一次。
8. **端到端穿透。** 改一个人的角色 → 断言 SDK 侧下一次 `Allow` 的结果变了。这条对应第三阶段发现的那类缺陷：只测服务端返回值不够，要穿到 SDK 出口。
9. **变异验证。** 上述每条标注「辨别力」的测试，实现时都要把实现改坏跑一次确认真的变红。第三阶段有一条测试正是靠这个步骤才被发现是假绿的。

## 十二、新增依赖

**实施结果：零新增依赖。** 设计时预留了 `github.com/casbin/casbin/v2`（仅服务端，用于展开角色继承），实施时发现不需要——展开继承就是沿 `parent_id` 往上走一遍并让子角色的直接授权优先，二十行的事（见 `internal/service/policy.go` 的 `effectiveFor`）。为此引入 casbin 及其传递依赖，换来的只是同一个循环。

真正需要 casbin 的是**数据范围**（本期非目标）——那时它的 `model.conf` 表达力才值这个依赖。届时再引入，且只在服务端：SDK 侧的判定是一张扁平表上的 map 查找，永远不需要 casbin。

判定逻辑落在 `sdk/authzcore`，服务端与 SDK 共用同一份实现。选这个位置是因为：`sdk/` 不得 import `internal/`（`sdk/arch_test.go`），`internal/` 不宜 import sdk 的业务包，而 `sdk/gen` 是 buf 的 clean 目标（手写文件会被下次 `./scripts/gen.sh` 删掉——实施时真踩到了这一脚）。

## 十三、实施顺序建议

1. 数据模型与迁移（含 `user_application` → `user_extra` 的改名与瘦身）
2. 角色与权限点的 service 层 + 控制台接口
3. 上报接口与生命周期规则（第六、十节）
4. 策略编译（服务端 casbin）与扁平表的推送格式
5. SDK：核心 `Allow` + 策略缓存 + Watch 事件处理
6. SDK：chi/gin 适配器与 `Collect*`
7. 会话里刻角色 + `UserRoleChanged` 推送
8. 控制台页面：角色管理、权限点管理、给用户分配角色
9. 端到端验收：建角色 → 勾权限 → 分配给人 → demo 里验证 `Allow` 的结果随之改变
