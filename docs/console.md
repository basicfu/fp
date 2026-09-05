# 管理控制台

控制台是一个 React SPA，构建产物由 `go:embed` 打进 `fp` 二进制，
部署时不需要额外的静态服务器。

## 构建与运行

    ./scripts/build-web.sh      # 构建前端，产物落在 web/dist/
    ./scripts/run.sh            # 起 fp，浏览器打开 http://localhost:8080/

**必须走 `scripts/run.sh`，不能直接跑 `./fp` 或 `go run ./cmd/fp`。**
`fp` 二进制只读环境变量、不加载任何 dotenv 文件，而 `.env.local` 里存的是
零件（`FP_PG_HOST`、`FP_PG_PORT`、`FP_PG_PASSWORD`…），`config.Load()` 要的
却是拼好的 `FP_POSTGRES_URL` / `FP_REDIS_URL`。拼装这一步在 `scripts/env.sh`
里，`run.sh` 会 source 它。直接跑二进制会得到：

    ERROR fp 启动失败 err="config: 缺少必填环境变量 FP_POSTGRES_URL, FP_REDIS_URL"

确实想跑构建出来的二进制（例如验证 `go:embed` 的产物），先把连接串导进当前
shell 再跑：

    set -a; . ./scripts/env.sh; set +a
    go build -o fp ./cmd/fp && ./fp

**控制台的登录账号**：`run.sh` 在 `FP_BOOTSTRAP_ADMIN_USER` /
`FP_BOOTSTRAP_ADMIN_PASSWORD` 未设置时默认用 `admin` / `admin123456`。
想换成别的，在 `.env.local` 里设这两项。注意 `EnsureBootstrap` 是
`ON CONFLICT DO NOTHING`——账号一旦建过，改这两个环境变量不会改密码。

**没跑过 `build-web.sh` 也能 `go build`**（`web/dist/` 里提交了一个
`.gitkeep`，embed 指令用的是 `all:` 前缀），只是打开控制台会看到一句
"管理控制台前端尚未构建"。

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
| `/applications` | 应用列表，新建应用（appSecret 只显示一次） |
| `/applications/:id` | 基本信息、会话策略、登录方式配置、权限点与默认角色 |
| `/users` | 用户列表，分页 / 关键词 / 状态筛选 |
| `/users/:id` | 身份、角色、在线设备、登录日志、冻结、重置密码、踢设备 |
| `/roles` | 角色列表，新建 / 改显示名与继承 / 删除 |
| `/roles/:id` | 授权编辑器：给这个角色勾选某个应用的权限点 |

授权相关的三处刻意分开放，因为它们的归属不同：

- **角色是全局的**，所以单独一个顶级菜单。「普通用户」这类角色天然跨应用，
  应用专属的角色靠命名区分（商城管理员 / 视频管理员）。
- **权限点属于应用**，所以在应用详情下，和该应用的**默认角色**同一页——
  默认角色决定"新用户零配置能干什么"，与权限点是一件事的两面。
- **给用户分配角色**在用户详情里，因为那是这个用户的属性。

授权编辑器（`/roles/:id`）要先选一个应用：角色是全局的、权限点是按应用存的，
把几个应用几百个权限点混在一张表里既没法看也容易误授。选中的应用记在
URL 的 `?app=` 上，刷新和分享链接都能回到同一个视图。

它只列**本角色自己**的授权，不含从父角色继承来的——继承来的在这里取消不了
（那条授权属于父角色），列出来只会让人以为能改。

登录方式配置页是**动态渲染**的：表单字段来自后端每个 connector 的
`ConfigSchema()`（`GET /admin/api/connectors`）。新增一种登录方式时，
后端写好 `ConfigSchema()` 即可，前端不需要任何改动。
