# 管理控制台

控制台是一个 React SPA，构建产物由 `go:embed` 打进 `fp` 二进制，
部署时不需要额外的静态服务器。

## 构建与运行

    ./scripts/build-web.sh                # 构建前端，产物落在 web/dist/
    export FP_POSTGRES_URL=postgres://postgres:password@127.0.0.1:5432/fp?sslmode=disable
    export FP_REDIS_URL=redis://:password@127.0.0.1:6379/0
    ./scripts/run.sh                      # 起 fp，浏览器打开 http://localhost:8080/

`fp` 只要求这两个环境变量，缺一不启动：

    ERROR fp 启动失败 err="config: 环境变量 FP_POSTGRES_URL 未设置"

`FP_ENV` 是第三个、可选的环境变量，缺省 `DEV`（大小写不敏感）。它决定两件
事：一是生产专属校验（目前只有管理端 cookie 带不带 `Secure` 这一项），
二是日志要不要同时落盘——`FP_ENV` 不是 `dev` 时，日志除了打 stdout，还会
**同时**写一份到 `/logs` 目录，按天滚动成 `2026-06-11.log` 这种文件名，
默认保留 30 天，超期自动删除；`dev`（缺省值）下只打 stdout，不碰
`/logs`。

其余启动配置（监听地址、首次管理员、阿里云短信）不在环境变量或文件里，
存在数据库的「系统配置」表中，通过控制台「系统配置」页面维护——改完需要
重启 fp 才会生效。阿里云短信四项凭据任何环境都不强制必填，缺了退化成内存
假供应商（验证码只进日志，不会真的发送），后续会挪进统一的通知中心配置。
系统配置表还没有任何版本时（全新库）全部用零值默认
启动，监听地址默认 `:8080`/`:9090`。

`scripts/run.sh` 会顺手 source 一下仓库根目录的 `.env.local`（如果存在），
本机开发可以把这两条连接串写在那里，不用每次手动 `export`。

    go build -o fp ./cmd/fp && FP_POSTGRES_URL=... FP_REDIS_URL=... ./fp

**控制台的登录账号**：系统配置里的 `bootstrap_admin.user` /
`bootstrap_admin.password`，两项都为空时（包括全新库、从未配置过）落到
内置默认值 `admin` / `admin`。注意 `EnsureBootstrap` 是
`ON CONFLICT DO NOTHING`——账号一旦建过，改系统配置里的这两项不会改
密码，得直接改库或在控制台里改密码。

**没跑过 `build-web.sh` 也能 `go build`**（`web/dist/` 里提交了一个
`.gitkeep`，embed 指令用的是 `all:` 前缀），只是打开控制台会看到一句
"管理控制台前端尚未构建"。

**静态资源挂在 `/static/` 前缀下，方便接 CDN。** `index.html` 本身（以及
它自己的 `/admin/api/*`）直接连源站，不建议上 CDN——内容要么每次都会变
（HTML 靠 ETag 做条件请求，见下面），要么是带会话的动态数据，缓存了要么
没意义要么有风险。真正适合上 CDN 的是 JS/CSS/字体这些静态产物，它们默认
就在 `/static/` 这个前缀下（`internal/httpapi/static.go` 认这个前缀，
`web/vite.config.ts` 的 `base` 生产构建时默认也是 `/static/`），CDN 只要
把自己的某个路径映射回源到这里就行，不用碰 `index.html`。

要让构建产物直接引用 CDN 域名（而不是先落到源站的 `/static/` 再指望 CDN
按路径映射），构建前设一下环境变量：

    VITE_ASSET_BASE=https://static.example.com/fp/ ./scripts/build-web.sh

`index.html` 里的 `<script>`/`<link>` 地址就会直接指向这个 CDN 域名。
`assets/` 下带内容哈希的文件（JS/CSS/字体）会被打上
`Cache-Control: public, max-age=31536000, immutable`，可以放心长缓存；
`public/` 目录直接拷过来、文件名不带哈希的文件（比如 `favicon.svg`）不会，
避免图标换了线上还是老的。

**`index.html` 本身用 ETag 做条件请求**，不是简单的"不缓存"：
`Cache-Control: no-cache` 只是告诉客户端每次都要来验证一下，服务端会比对
请求带的 `If-None-Match`，内容没变时回 `304 Not Modified`（不带 body），
省下整份 HTML 的传输——这块逻辑在 fp 自己的 Go 代码里，不依赖 CDN。

## 开发

两个终端：

    ./scripts/run.sh                   # 后端，监听 8080
    cd web && npm run dev              # 前端，监听 5173

Vite 把 `/admin/api` 代理到 `localhost:8080`，浏览器看到的仍是同源，
管理端会话 cookie 正常生效。改前端代码有热更新，改后端要重启。

## 测试

    ./scripts/test.sh          # 只跑 Go
    cd web && npm test         # 只跑前端
    ./scripts/test-all.sh      # 两个都跑

## 工具链上的几个坑

这些都是实测踩出来的，与官方文档冲突，改动前端工程配置前先读：

- **tsconfig 里不能有 `baseUrl`**——TypeScript 6 已废弃，写了报 `TS5101`。
  只留 `paths`。shadcn 官方文档目前仍然教你加，别照做。
- **shadcn 加组件必须带命名空间**：`npx shadcn@latest add @shadcn/button`。
  裸名字会静默成功但不生成任何文件。
- **shadcn v4 没有 `form` 组件**，动态表单直接建在 react-hook-form 上。
- `vite.config.ts` 的 `defineConfig` 从 `vitest/config` 导入，不是 `vite`，
  否则 `tsc -b` 报 `TS2769`。
- 测试文件里显式 `import { test, expect } from 'vitest'`，不要依赖
  `globals: true`——它只影响运行时，TypeScript 仍然不认识。

## 页面

| 路径 | 内容 |
|---|---|
| `/login` | 平台管理员登录 |
| `/applications` | 应用列表，新建应用（appSecret 只显示一次）；选中的应用的基本信息、会话策略、登录方式配置就内联在同一页里，不再是单独的详情路由 |
| `/users` | 用户列表，分页 / 关键词 / 状态筛选 |
| `/users/:id` | 身份、角色、在线设备、登录日志、冻结、重置密码、踢设备 |
| `/roles` | 角色列表，新建 / 改显示名与继承 / 删除 |
| `/roles/:id` | 授权编辑器：给这个角色勾选某个应用的权限点 |
| `/permissions` | 当前应用（顶部切换器选中的那个）的权限点管理与默认角色 |
| `/access-keys` | 访问密钥列表：新建（Secret 只显示一次）、停用、删除，可按角色筛选 |
| `/access-keys/:id` | 访问密钥详情：基本信息、按应用分组的可调用接口、编辑 |
| `/config` | 当前应用的配置中心：按 DEFAULT / WEB 分区查看与编辑配置项 |
| `/config/versions` | 当前应用的配置版本历史：逐版 diff、回滚到某一版 |
| `/system-config` | fp 自身的系统配置：env、监听地址、首次管理员、阿里云短信，保存后重启生效 |
| `/system-config/versions` | 系统配置版本历史：查看某一版内容、回滚到某一版 |

授权相关的三处刻意分开放，因为它们的归属不同：

- **角色是全局的**，所以单独一个顶级菜单。「普通用户」这类角色天然跨应用，
  应用专属的角色靠命名区分（商城管理员 / 视频管理员）。
- **权限点属于应用**，所以在应用详情下，和该应用的**默认角色**同一页——
  默认角色决定"新用户零配置能干什么"，与权限点是一件事的两面。
- **给用户分配角色**在用户详情里，因为那是这个用户的属性。

GUEST 是内置角色——未登录请求和所有登录用户都拥有它，访问密钥不拥有；不能删除、不能设父角色、只能配「允许」。

**升级前检查**：迁移 `00011_access_key.sql` 用 `INSERT ... ON CONFLICT (key) DO NOTHING` 建这个角色——如果目标 fp 实例的数据库里已经存在一个 key 为 `GUEST` 的角色（不管什么原因建的），迁移不会覆盖它，它现有的全部授权会在迁移跑完的瞬间立刻对所有匿名请求（allow）和所有登录用户（deny）生效，没有任何提示。升级前建议先查一遍：

```sql
SELECT r.key, rp.effect, count(*) FROM role r
LEFT JOIN role_permission rp ON rp.role_id = r.id
WHERE r.key = 'GUEST' GROUP BY 1, 2;
```

有结果就人工核对这些授权是否符合预期，再执行迁移。

授权编辑器（`/roles/:id`）要先选一个应用：角色是全局的、权限点是按应用存的，
把几个应用几百个权限点混在一张表里既没法看也容易误授。选中的应用记在
URL 的 `?app=` 上，刷新和分享链接都能回到同一个视图。

它只列**本角色自己**的授权，不含从父角色继承来的——继承来的在这里取消不了
（那条授权属于父角色），列出来只会让人以为能改。

登录方式配置页是**动态渲染**的：表单字段来自后端每个 connector 的
`ConfigSchema()`（`GET /admin/api/connectors`）。新增一种登录方式时，
后端写好 `ConfigSchema()` 即可，前端不需要任何改动。

## IM 接入

fp-im 是配套的 WebSocket 连接网关。**一个应用在有人明确打开之前连不上它**
——`im_enabled` 默认关，新版 fp 发布后所有现存应用都是关的，对现网零影响。

两件事要在控制台做，顺序不能反：

**1. 生成 IM 网关凭据**（应用管理页 →「IM 接入」页签 → IM 网关凭据卡片）

全库只有一份，所有 fp-im 实例共用——fp-im 是一个服务，不是一群应用。明文
**只在生成后的那个对话框里显示一次**，库里只存 bcrypt 哈希，关掉就再也读不
回来。把它填进每个 fp-im 实例的环境变量：

```bash
export FP_IM_FPSDK_ADDR=fp.internal:9090
export FP_IM_FPSDK_SECRET=<这里>
```

**轮换有一个 10 秒窗口。** 旧凭据在库里立刻失效，但 fp 的凭据缓存会让它最多
再活 10 秒（`grpcapi.DefaultIMSecretCacheTTL`）。日常轮换无所谓；因泄露而轮换
时要知道有这个窗口。

为什么不是"轮换时主动清缓存"：fp 可以多实例部署，主动清只清得掉本实例的，
别的实例照样留满 TTL——要做对就得再走一遍 Redis 广播，为一条凭据引入一整条
中继不划算。短 TTL 零新增管道、跨实例天然一致。

**2. 给需要接入的应用打开 IM**（同一页签的上半部分）

| 参数 | 说明 |
|---|---|
| `连接策略` | `replace` 新连顶旧连 / `reject` 已有连接就拒新 / `limit` 限并发条数 |
| `并发上限` | 只在 `limit` 策略下有意义 |
| `允许访客` | 访客握手不带 token，只按 IP 限流 |
| `访客限流` | 每个 IP 的速率上限 |
| `业务方令牌` | 开启后 client 用 `kind:biz` 握手时，网关回调你的接口验证 |

`业务方令牌` 的回调地址由业务方自己的部署决定（http/https 均可）。回调超时
必须明显小于握手的 5 秒上限：配大了 client 会先被握手超时踢掉，拿到的关闭码
从 4004（该退避重连）变成 4001（该重新登录），方向完全反了。

**关掉 `im_enabled` 不会断开已在线的连接**，只挡新握手。这与「停用应用」
（`status=disabled`）的行为逐字相同——两者都只影响新的认证，不主动撤销
已签发的东西。要立刻踢人用 server SDK 的 `Kick`。
