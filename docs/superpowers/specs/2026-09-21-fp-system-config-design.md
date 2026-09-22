# fp 系统配置迁入配置中心设计

**日期**：2026-09-21
**状态**：设计已定，待细化为实施计划
**上游**：`2026-09-08-config-file-yaml-design.md`、`2026-09-05-fp-config-center-design.md`

---

## 一、要做什么

`config.yaml` 现在装两类完全不同的东西：`postgres.url`/`redis.url` 这两项没有
它们就连数据库都连不上，是真正的启动前置条件；`http.addr`、`bootstrap_admin`、
`sms.aliyun.*` 这些不是——它们只是"进程起来之后要用到的值"，本可以像业务方的
配置中心一样存进数据库、在控制台里改。继续把两类东西混在同一份要手工分发到
每台机器的 YAML 文件里，图的只是"看着是一份文件"，代价是每次改一个监听地址
都要登机器改文件、重启。

改成两条路径：

> **必须让进程连上数据库/Redis 的两项，走环境变量：`POSTGRES_URL`、
> `REDIS_URL`，缺一不启动。其余全部挪进数据库里的一张"系统配置"表，
> 通过控制台新增的「系统配置」页面维护，改完重启生效。**

`config.yaml`、`config.example.yaml`、`-c` 参数随之整个删除。

## 二、明确不做

| 项 | 一句话理由 |
|---|---|
| 系统配置改完实时生效（热重载） | `http.addr`、`bootstrap_admin` 这类本来就不支持不重启生效；用户已经明确说了"仅保存，重启后读取最新即可"，为它接一条 Redis 广播是白做的功 |
| 系统配置支持多个"分区"（像 DEFAULT/WEB 那样） | fp 自己只有一个消费者（`cmd/fp` 进程本身），没有"转发给浏览器 / 转发给别的端"这种需要区分受众的场景 |
| `POSTGRES_URL`/`REDIS_URL` 也挪进数据库 | 先有鸡还是先有蛋——读数据库本身就要先连上数据库，这两项天然只能来自进程启动时能拿到的介质（环境变量） |
| 系统配置表挂在 `application` 表下（借用现有 `config` 表 + 一个"系统应用"哨兵行） | 会让「应用列表」页面里凭空多出一个不是真实应用的行，还要在每处列表查询里过滤掉它；专门开一张不挂 `application_id` 的表，语义更直接 |
| `bootstrap_admin` 留空时完全不创建管理员（沿用今天的语义） | 系统配置表首次启动必然是空的，不给默认值会导致谁都登不进控制台去配置——见第五节 |

## 三、环境变量与启动流程

`cmd/fp/main.go` 开头新增两个大写环境变量的强制读取，缺一直接返回错误、
不启动：

```go
pgURL := os.Getenv("POSTGRES_URL")
redisURL := os.Getenv("REDIS_URL")
if pgURL == "" || redisURL == "" {
    // 报错列出缺的是哪个，不是笼统的"环境变量缺失"
}
```

启动顺序：

```
读 POSTGRES_URL / REDIS_URL（缺失即退出）
→ 连 Postgres → Migrate
→ 连 Redis
→ SystemConfigService.Current()：没有任何版本时 Value=""
→ config.Parse(value)：空文本 = 全部用零值默认（现有 Load 对空文件已是这个行为，直接复用）
→ 用解析出的 cfg 继续走原来的 bootstrap_admin / sms / 监听地址装配逻辑
```

`internal/config.Config` 去掉 `Postgres`、`Redis` 两个字段，其余字段（`Env`、
`Log`、`HTTP`、`GRPC`、`BootstrapAdmin`、`SMS`）原样保留。`Load(path string)`
改名为 `Parse(yamlText string)`：不再 `os.Open` 文件，直接吃系统配置表里的
YAML 原文；`KnownFields(true)`、默认值预填、必填校验（`env: prod` 时阿里云
四项）这套逻辑不变，只是错误信息不再提"配置文件路径"。

## 四、数据模型

```sql
-- 系统配置的版本快照：一次保存一行，Value 是 YAML 原文，原样存储——
-- 与 config 表同一条"整版快照"纪律，理由见 2026-09-05 设计文档第三节。
-- 没有 application_id / type：fp 自己只有一份系统配置，不需要这两个维度。
CREATE TABLE system_config (
    seq        bigint      NOT NULL PRIMARY KEY,
    value      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
```

回滚 = 复制一行；保留最近 100 版，修剪规则与 `config` 表一致。

`internal/domain` 新增：

```go
type SystemConfig struct {
    Seq       int64
    Value     string
    CreatedAt int64
}
```

YAML 校验直接复用 `domain.ParseConfigYAML` / `domain.NormalizeConfigYAML`——
这两个函数本来就不关心分区，只关心"是不是一份顶层为映射的 YAML"。

## 五、Service 层：`SystemConfigService`

新文件 `internal/service/system_config.go`，形状是 `ConfigService` 去掉
`applicationID`/`type`/`push` 三个维度后的样子：

```go
type SystemConfigService struct{ pool *pgxpool.Pool }

func (s *SystemConfigService) Current(ctx context.Context) (domain.SystemConfig, error)
func (s *SystemConfigService) Version(ctx context.Context, seq int64) (domain.SystemConfig, error)
func (s *SystemConfigService) ListVersions(ctx context.Context, limit int) ([]domain.SystemConfig, error)
func (s *SystemConfigService) Save(ctx context.Context, value string) (int64, error)
func (s *SystemConfigService) Rollback(ctx context.Context, seq int64) (int64, error)
```

不是把 `ConfigService` 改造成兼容两张表——那会让一个类型的方法签名为了
一个只有一处会用到的维度而变形，新开一个小文件更直接。`ConfigMaxVersions`
（100）常量与修剪 SQL 抄一份，不共用：两张表的主键结构不同（`(application_id,
type, seq)` vs `(seq)`），SQL 本来就不是一份。

`Current()` 一个版本都没有时返回 `Seq=0, Value=""`，不是错误——与
`ConfigService.Current` 同一语义，"还没配过"是正常状态。

**没有 `push` 参数、没有 `Publisher` 依赖**——这张表只在进程启动时读一次，
不广播。

## 六、bootstrap_admin 的默认值语义变化

现状：`config.yaml` 里 `bootstrap_admin.user`/`password` 两项都填才创建管理员，
都留空就跳过（`AdminService.EnsureBootstrap` 的既有行为，不动）。

变化点在 `cmd/fp/main.go` 调用 `EnsureBootstrap` 之前：**系统配置解析出的
`BootstrapAdmin` 两项都为空时，落到内置默认值 `admin`/`admin`**（不再是跳过）。
原因是系统配置表首次启动必为空，不给默认值会导致数据库里一个管理员都没有、
谁都登不进控制台去创建这张表的第一条记录——这是本次改动引入的"先有鸡还是
先有蛋"，处理方式与用户在第一轮讨论里确认的一致。

`EnsureBootstrap` 本身是 `ON CONFLICT DO NOTHING`，所以这个默认值只在
"库里还没有任何管理员"时才真正生效；用户在系统配置里显式填了 `bootstrap_admin`
（或者已经用默认账号登录改了密码）之后，这条内置默认值不会覆盖已有账号。

## 七、HTTP API

参照 `/applications/{id}/config` 那一组，去掉 `{id}` 与 `push`：

```
GET    /admin/api/system-config
PUT    /admin/api/system-config              body: {value}
GET    /admin/api/system-config/versions
GET    /admin/api/system-config/versions/{seq}
POST   /admin/api/system-config/rollback     body: {seq}
```

新文件 `internal/httpapi/system_config.go`（`systemConfigHandler`），路由挂在
`router.go` 现有的 `requireAdmin` 分组里，与 `/im-credential`、`/access-keys`
同一权限层级（平台管理员）。`Deps` 新增 `SystemConfigs *service.SystemConfigService`
字段，装配方式与 `Configs` 字段一致（非 nil 才挂载这组路由，保持与
`IMCreds`/`AccessKeys` 相同的可选装配约定，方便单元测试不必每次都传全）。

## 八、前端

新增导航项「系统配置」（`web/src/pages/SystemConfig.tsx`），路由 `/system-config`。
页面是 `ConfigCenter.tsx` 的简化版：一个 YAML 文本框 + 「保存」+「版本历史」
链接，没有分区 tab、没有「保存并推送」按钮、不依赖 `useCurrentApp`。

`normalizeYAML`/`validateYAML`/Tab 键缩进这几段逻辑两个页面完全一样，抽成
共享 hook `web/src/lib/useYamlEditor.ts`，`ConfigCenter.tsx` 与 `SystemConfig.tsx`
都改用它——顺手清理，不是新范围。

占位符提示当前系统配置支持哪些字段（对齐 `config.example.yaml` 曾经的内容）：

```
env: dev                # dev / prod
log:
  level: info
http:
  addr: ":8080"
grpc:
  addr: ":9090"
bootstrap_admin:
  user: admin
  password: admin
sms:
  aliyun:
    access_key_id: ""
    ...
```

版本历史页面 `web/src/pages/SystemConfigVersions.tsx` 是 `ConfigVersions.tsx`
的简化版（同样去掉应用/分区两个维度），支持回滚。

## 九、连带改动

**新建：**

| 文件 | 职责 |
|---|---|
| `internal/store/migrations/000XX_system_config.sql` | 建 `system_config` 表 |
| `internal/service/system_config.go` | `SystemConfigService` |
| `internal/service/system_config_test.go` | 对应单测 |
| `internal/httpapi/system_config.go` | `systemConfigHandler` |
| `internal/httpapi/system_config_test.go` | 对应单测 |
| `web/src/pages/SystemConfig.tsx` | 系统配置编辑页 |
| `web/src/pages/SystemConfigVersions.tsx` | 系统配置版本历史页 |
| `web/src/lib/useYamlEditor.ts` | 从 `ConfigCenter.tsx` 抽出的共享 YAML 编辑逻辑 |

**改：**

| 文件 | 改什么 |
|---|---|
| `internal/config/config.go` | 删 `Postgres`/`Redis` 字段；`Load(path)` → `Parse(yamlText)` |
| `internal/config/config_test.go` | 用例改成传 YAML 字符串而不是写临时文件；删掉 postgres/redis 必填校验的用例 |
| `internal/domain/config.go` | 新增 `SystemConfig` 结构体 |
| `internal/httpapi/router.go` | 新增 `SystemConfigs` 字段与路由分组 |
| `cmd/fp/main.go` | 环境变量读取；启动顺序改成第三节描述的样子；`bootstrap_admin` 默认值兜底 |
| `web/src/components/Layout.tsx` | 导航加「系统配置」 |
| `web/src/routes.tsx` | 加 `/system-config`、`/system-config/versions` 两条路由 |
| `web/src/lib/api.ts` / `types.ts` | 新增系统配置相关的类型与调用封装 |
| `web/src/pages/ConfigCenter.tsx` | 改用 `useYamlEditor` |
| `docs/console.md` | 补系统配置页面的说明 |

**删：**

| 文件 | 理由 |
|---|---|
| `config.yaml`（本机文件，未进 git） | 被环境变量 + 系统配置表取代 |
| `config.example.yaml` | 同上 |
| `.gitignore` 里 `/config.yaml` 那一行 | 文件不再存在，忽略规则也不需要 |

**不动：** `internal/store/config.go`（`ConfigPublisher`，业务方配置中心的广播
机制，与系统配置无关）、`internal/service/config.go`（`ConfigService`，业务方
配置中心）、`config-im.yaml` / `internal/im/config`（fp-im 的配置文件是独立的
一件事，不在本次范围内）。

## 十、测试要点

1. `internal/config`：`Parse("")` 返回全默认值；`Parse` 未知键报错；`env: prod`
   缺阿里云四项报错；正常文本每个字段读到预期值
2. `SystemConfigService`：`Current` 空表返回 `Seq=0`；`Save` 递增 `seq`；超过
   100 版触发修剪；`Rollback` 到内容相同的版本不产生新版本号（与
   `ConfigService.Rollback` 同一断言)
3. `internal/httpapi/system_config_test.go`：覆盖 get/save/versions/rollback
   四个接口的鉴权（未登录 401、非管理员 403）与基本读写
4. `cmd/fp/main.go` 的启动流程：`POSTGRES_URL`/`REDIS_URL` 任一缺失时进程
   不起来且报错信息点出缺的是哪个（可以是一个不依赖真实数据库的单元测试，
   只测环境变量读取那一小段逻辑）
5. `bootstrap_admin` 默认值：系统配置为空时最终传给 `EnsureBootstrap` 的是
   `admin`/`admin`；系统配置显式给了非空值时用给定值
