# 第三阶段实施计划：管理控制台

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让平台管理员在浏览器里完成应用管理、登录方式配置、用户管理三件事，把第二阶段验收文档里那套 curl 流程整体替换掉。

**Architecture:** 后端补三处缺口（应用改名/停用、connector 配置校验、静态资源托管），前端是一个 React SPA，构建产物由 `go:embed` 打进 `fp` 二进制，部署仍然是单文件。登录方式配置页不写死表单——它读 `GET /admin/api/connectors` 返回的 `Field[]` 元数据动态渲染，所以以后新增登录方式（微信、扫码、OIDC）前端零改动。这条是整个控制台唯一有真实逻辑的部分，也是测试的重点。

**Tech Stack:** Go 1.26 + chi v5（沿用）；React 19.2 + TypeScript 6.0 + Vite 8.2 + Tailwind CSS 4.3 + shadcn/ui + react-hook-form 7 + zod 3 + react-router 8；测试用 Vitest 4 + React Testing Library 16。

---

## Global Constraints

下面每一条都是**在本机实测验证过的**，不是从文档抄的。多数与官方文档或常见教程冲突——按文档写会直接失败。违反任意一条都会导致构建红或静默出错。

### 工具链版本（实测）

- Node v24.19.0 / npm 12.0.2。**本机没有 pnpm / yarn / bun**，一律用 npm。
- `npm create vite@latest web -- --template react-ts` 装出：React 19.2.8、Vite 8.2.2、TypeScript 6.0.2。模板自带的 lint 是 **oxlint**，不是 ESLint——不要去找 `.eslintrc`。
- 前端全部代码位于仓库根下的 `web/`。

### TypeScript 6 的坑

- **tsconfig 里不许出现 `baseUrl`。** TS 6.0 已废弃它，写了直接报 `TS5101` 构建失败。shadcn 官方文档现在仍然教你加 `baseUrl` —— 别照做。只在 `tsconfig.app.json` 和 `tsconfig.node.json` 里写 `"paths": {"@/*": ["./src/*"]}`，`@/` 别名即可正常解析。

### Vitest 的两个坑

- `vite.config.ts` 里要放 `test:` 配置块，`defineConfig` **必须从 `vitest/config` 导入**，不能从 `vite` 导入——否则 `tsc -b` 报 `TS2769: No overload matches this call`。
- **不要用 `globals: true`。** 它只影响运行时，TypeScript 依然不认识 `test` / `expect`，`tsc -b` 会报 `TS2593` / `TS2304`。每个测试文件顶部显式写 `import { test, expect } from 'vitest'`。
- 已实测：jsdom 环境下 `Response` / `Headers` / `fetch` 三个全局都可用，`new Response(null, { status: 204 })` 也正常。API 客户端的测试桩直接用它们即可，不需要 polyfill 或额外的 mock 库。

### shadcn/ui 的三个坑

- CLI 是 `shadcn@4.19.1`。**添加组件必须用命名空间形式**：`npx shadcn@latest add @shadcn/button`。写成裸名字 `npx shadcn@latest add button` 会**静默成功**——退出码 0、无任何报错、也不生成任何文件。这是本阶段最容易浪费时间的一个坑。
- **shadcn v4 的 registry 里没有 `form` 组件了。** 网上所有教 `<Form>` / `<FormField>` / `<FormMessage>` 的文章都已过期。动态表单引擎直接建在 react-hook-form 之上，不要去找 shadcn 的 Form 封装。
- shadcn v4 底层已从 Radix 换成 `@base-ui/react`。看到 `@base-ui` 相关依赖是正常的。

### 不引入 TanStack Table

当前 npm 上的 `@tanstack/react-table` 是 v9.2.4，是一次彻底重写：`useReactTable` 和 `getCoreRowModel` 都不存在了（改叫 `ReactTable` / `createCoreRowModel`），`ColumnDef` 需要 2–3 个类型参数，网上示例全部不适用。而本阶段两张表格（应用列表、用户列表）都是**服务端分页**、无客户端排序筛选分组需求——TanStack 的价值点一个都用不上。**用 shadcn 的 `Table` 组件 + 手写分页即可。** 等真的需要列排序/列显隐时再引，那时它的文档也跟上了。

### go:embed 的坑

- **必须写 `//go:embed all:dist`，不能写 `//go:embed dist`。** 仓库里 `web/dist/` 只提交一个 `.gitkeep`（构建产物不入库），而不带 `all:` 前缀的 embed 会跳过 `.` 开头的文件，于是目录里"没有可嵌入的文件"，编译直接失败：`pattern web/dist: cannot embed directory web/dist: contains no embeddable files`。带 `all:` 前缀就能编译通过。
- 这条的意义是：**任何人 clone 仓库后不跑 npm 也必须能 `go build ./...`**。

### 前端产物预算

实测完整栈（React + Tailwind + shadcn + RHF + zod + 字体）构建产物约 470 KB，JS 约 325 KB（gzip 99 KB）。`go:embed` 完全可接受。如果某次构建后产物超过 1.5 MB，说明误引了大依赖，要查。

### 沿用第一、二阶段的既有约束

- 所有测试通过 `./scripts/test.sh` 运行（内部串行 `-p 1`，因为共用同一套局域网 Postgres/Redis）。当前基线 **368 个测试函数全绿**，本阶段结束时不允许有任何既有测试变红而未被解释。
- Go 侧注释、错误信息、日志一律简体中文。前端 UI 文案同样简体中文。
- 安全耦合（"改了 A 就必须连带做 B"）一律放在 `internal/service` 层，不放在 `internal/httpapi`。理由见 `internal/httpapi/user.go` 中 `revokeAllSessions` 上方的注释：放在传输层的话，以后新增的 gRPC 管理入口会漏掉，而且不会有任何测试变红。
- 领域错误用 `domain.Errorf(domain.ErrXxx, ...)`，由 `httpapi.writeError` 统一映射状态码。不要在 handler 里手写状态码。

### 本阶段的测试标准

第二阶段的教训写在设计文档附录里，这里重申并加一条：

> 写计划时"强调了"不等于"守住了"。每写一条"这是必须的"，都要同时问：**哪一条测试会因为违反它而变红？**
>
> 补充：还要再问一句——**这条测试的前置数据里，有没有某种巧合，使得正确实现和错误实现产出同样的结果？** 第二阶段有四条测试就是栽在这里：它们确实在跑，也确实绿，但换成错误实现照样绿。

本计划中凡是标注「**辨别力**」的测试，都是专门为破除某个巧合而设计的，不许简化。

---

## 文件结构

### 后端（改动）

| 文件 | 职责 |
|---|---|
| `internal/service/application.go` | 新增 `Update` / `SetStatus`；`SetConnector` 增加配置校验；构造函数增加 connector 元数据依赖 |
| `internal/httpapi/application.go` | 新增 `update` / `setStatus` handler |
| `internal/httpapi/static.go` | **新建**。SPA 静态资源托管：路径回退、缓存头、未构建时的友好降级 |
| `internal/httpapi/router.go` | 挂新路由与静态资源 |
| `cmd/fp/main.go` | 把 registry 注入 ApplicationService，把 embed FS 注入 router |
| `web/embed.go` | **新建**。`package web`，`//go:embed all:dist` |
| `web/dist/.gitkeep` | **新建**。占位，保证未构建前端时 `go build` 也能过 |
| `.gitignore` | 忽略 `web/node_modules`、`web/dist/*`（但保留 `.gitkeep`） |
| `scripts/build-web.sh` | **新建**。构建前端产物 |

### 前端（全新）

| 文件 | 职责 |
|---|---|
| `web/package.json` / `vite.config.ts` / `tsconfig*.json` / `index.html` | 工程配置 |
| `web/src/main.tsx` / `web/src/index.css` | 入口 |
| `web/src/lib/api.ts` | fetch 封装：错误形状解析、204 空响应、401 跳登录 |
| `web/src/lib/api.test.ts` | 上面这些行为的测试 |
| `web/src/lib/utils.ts` | shadcn 生成的 `cn()` |
| `web/src/components/ui/*` | shadcn 生成的组件 |
| `web/src/components/DynamicForm.tsx` | **动态表单引擎**。`Field[]` → zod schema → RHF → 提交载荷 |
| `web/src/components/DynamicForm.test.tsx` | 引擎的测试。本阶段测试重点 |
| `web/src/components/Layout.tsx` | 侧边栏 + 顶栏 + 登出 |
| `web/src/components/Pagination.tsx` | 服务端分页控件 |
| `web/src/pages/Login.tsx` | 登录页 |
| `web/src/pages/Applications.tsx` | 应用列表 + 创建 |
| `web/src/pages/ApplicationDetail.tsx` | 应用详情：会话策略、启停、登录方式配置 |
| `web/src/pages/Users.tsx` | 用户列表：分页 + 搜索 + 状态筛选 |
| `web/src/pages/UserDetail.tsx` | 用户详情：身份、会话、登录日志、冻结、重置密码、踢设备 |
| `web/src/routes.tsx` | 路由表 + 登录守卫 |

### 明确不做（写清理由，免得以后被当成"忘了"）

- **secret 字段的脱敏与落库加密。** `domain.Field` 的注释写着 secret "UI 上脱敏显示、落库加密"，但两样都没实现。本阶段不补，因为：当前两个 connector（password、sms_code）的 ConfigSchema 里**一个 secret 字段都没有**，唯一用到 secret 的是 `notify/aliyun.go` 的 Provider，而通知供应商配置页不在本阶段五个页面里。等真的要做通知配置页时，连同"`SetConnector` 全量覆盖 config 会导致留空即删除"这个问题一起解决（正确解法是服务端保留缺省的 secret 键，而不是让前端把明文读回来再回填）。
- **全局登录日志页。** 登录日志只在用户详情页里按用户查看，`GET /admin/api/users/{id}/login-logs` 已经够用。不为一个不存在的页面造接口。
- **appSecret 轮换。** 创建时返回一次即可，轮换等有人真的需要时再说。
- **管理员账号管理。** 平台管理员靠 `EnsureBootstrap` 从配置引导，本阶段不做增删改查界面。

---

## Task 1: 应用的改名与停用（后端）

**为什么需要：** `domain.ApplicationStatusDisabled` 这个常量目前只有读的地方（`GetActiveByAppID` 里判断），**没有任何代码写它**——也就是说"停用应用"这个能力在数据库里有字段、在代码里有常量、在 DTO 里有输出，但根本无法触发。同理 `cookie_domain` 字段 `Create` 不设置、也没有任何更新入口，永远是空串。本任务把这两条打通。

**停用的传播时机（重要，不要额外加撤销逻辑）：** gRPC 拦截器的 `appVerifier` 刻意只缓存"这对 appId/appSecret 有效"这个事实，**不缓存 `*domain.Application`**（见 `internal/grpcapi/auth_interceptor.go` 里 appVerifier 上方的注释）。应用是否启用由 `AuthService.activeApp` → `GetActiveByAppID` 每次重新判定。所以停用应用后：新登录立刻被拒、SDK 的下一次回源校验立刻被拒，唯一的滞后是 SDK 本地缓存的 `cache_ttl`（上限就是该应用自己配的 `TokenCacheTTLSeconds`）。**不需要**在停用时去撤销会话——那会是重复的执行点。

**Files:**
- Modify: `internal/service/application.go`（在 `UpdateSessionPolicy` 后面加两个方法）
- Modify: `internal/httpapi/application.go`
- Modify: `internal/httpapi/router.go`
- Test: `internal/service/application_test.go`、`internal/httpapi/application_test.go`

**Interfaces:**
- Produces:
  - `func (s *ApplicationService) Update(ctx context.Context, id uuid.UUID, name, cookieDomain string) (*domain.Application, error)`
  - `func (s *ApplicationService) SetStatus(ctx context.Context, id uuid.UUID, status string) (*domain.Application, error)`
  - HTTP `PATCH /admin/api/applications/{id}` 体 `{"name":"...","cookieDomain":"..."}`，返回 `applicationDTO`
  - HTTP `PATCH /admin/api/applications/{id}/status` 体 `{"status":"ACTIVE"|"DISABLED"}`，返回 `applicationDTO`
- Consumes: 既有的 `applicationColumns`、`scanApplication`、`toApplicationDTO`、`pathUUID`

---

- [ ] **Step 1: 写失败的测试——停用后应用取不到**

追加到 `internal/service/application_test.go`：

```go
func TestSetStatusDisabledBlocksGetActive(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 停用前拿得到
	if _, err := svc.GetActiveByAppID(ctx, app.AppID); err != nil {
		t.Fatalf("停用前 GetActiveByAppID: %v", err)
	}

	got, err := svc.SetStatus(ctx, app.ID, domain.ApplicationStatusDisabled)
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got.Status != domain.ApplicationStatusDisabled {
		t.Fatalf("Status = %q, want DISABLED", got.Status)
	}

	// 停用后 GetActiveByAppID 必须拒绝，但 GetByAppID 仍然找得到——
	// 这两者的区别正是"停用"而非"删除"的含义。
	if _, err := svc.GetActiveByAppID(ctx, app.AppID); err == nil {
		t.Fatal("停用后 GetActiveByAppID 仍然成功，停用形同虚设")
	}
	if _, err := svc.GetByAppID(ctx, app.AppID); err != nil {
		t.Fatalf("停用后 GetByAppID 应仍可查到: %v", err)
	}

	// 能重新启用
	if _, err := svc.SetStatus(ctx, app.ID, domain.ApplicationStatusActive); err != nil {
		t.Fatalf("重新启用: %v", err)
	}
	if _, err := svc.GetActiveByAppID(ctx, app.AppID); err != nil {
		t.Fatalf("重新启用后 GetActiveByAppID: %v", err)
	}
}

func TestSetStatusRejectsUnknownStatus(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, bad := range []string{"", "disabled", "ENABLED", "DELETED"} {
		if _, err := svc.SetStatus(ctx, app.ID, bad); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("status=%q err = %v, want ErrInvalidArgument", bad, err)
		}
	}
}

func TestSetStatusOnMissingApplication(t *testing.T) {
	svc := newAppService(t)
	if _, err := svc.SetStatus(context.Background(), uuid.New(), domain.ApplicationStatusDisabled); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
```

`uuid` 若尚未在该测试文件导入，加 `"github.com/google/uuid"`。

- [ ] **Step 2: 写失败的测试——改名不许误伤别的字段**

这条是「**辨别力**」测试。没有它的话，一个把 `Update` 写成 `UPDATE application SET name=$2, cookie_domain=$3, idle_timeout_seconds=$4, ...` 并传零值策略的实现会通过前面所有测试——改名成功、返回值里名字对了，但这个应用的会话策略被悄悄清零，所有 token 立刻失效。

```go
func TestUpdateOnlyTouchesNameAndCookieDomain(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, secret, err := svc.Create(ctx, "旧名", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 先把会话策略改成一组与默认值明显不同的值。必须先改：沿用默认值的话，
	// "策略被清零"与"策略没动"在断言上有可能因为默认值本身含零值而无法区分。
	want := domain.SessionPolicy{
		IdleTimeoutSeconds:       3601,
		IdleTimeoutMobileSeconds: 7202,
		MaxLifetimeSeconds:       86403,
		RotateIntervalSeconds:    604,
		ExtendIntervalSeconds:    305,
		TokenCacheTTLSeconds:     56,
	}
	if _, err := svc.UpdateSessionPolicy(ctx, app.ID, want); err != nil {
		t.Fatalf("UpdateSessionPolicy: %v", err)
	}

	got, err := svc.Update(ctx, app.ID, "新名", "example.com")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != "新名" {
		t.Fatalf("Name = %q, want 新名", got.Name)
	}
	if got.CookieDomain != "example.com" {
		t.Fatalf("CookieDomain = %q, want example.com", got.CookieDomain)
	}
	// slug 与 appId 是身份，改名不许动
	if got.Slug != app.Slug || got.AppID != app.AppID {
		t.Fatalf("slug/appId 被改动: %+v", got)
	}
	// 会话策略必须原封不动
	if got.Session != want {
		t.Fatalf("会话策略被改名连带改动了: got %+v, want %+v", got.Session, want)
	}
	// appSecret 必须仍然有效
	if _, err := svc.VerifySecret(ctx, app.AppID, secret); err != nil {
		t.Fatalf("改名后 appSecret 失效: %v", err)
	}
	// 状态必须仍是启用
	if got.Status != domain.ApplicationStatusActive {
		t.Fatalf("Status = %q, want ACTIVE", got.Status)
	}
}

func TestUpdateRejectsEmptyName(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(ctx, app.ID, "", "example.com"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}
```

- [ ] **Step 3: 运行，确认失败**

```bash
./scripts/test.sh ./internal/service -run 'TestSetStatus|TestUpdateOnly|TestUpdateRejects'
```

预期：编译失败，`svc.SetStatus` / `svc.Update` 未定义。

- [ ] **Step 4: 实现两个方法**

加在 `internal/service/application.go` 的 `UpdateSessionPolicy` 之后：

```go
// Update 修改应用的展示名与 cookie 作用域。
//
// 只碰这两列。slug 与 app_id 是应用的身份，已经被 SDK 配置、被其他系统
// 引用，改掉等于换了一个应用；status 走 SetStatus；会话策略走
// UpdateSessionPolicy。每样东西一个入口，避免一次"改名"顺手把别的字段
// 覆盖成零值。
func (s *ApplicationService) Update(ctx context.Context, id uuid.UUID, name, cookieDomain string) (*domain.Application, error) {
	if name == "" {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "name 不能为空")
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE application SET
			name          = $2,
			cookie_domain = $3,
			updated_at    = now()
		WHERE id = $1
		RETURNING `+applicationColumns, id, name, cookieDomain)

	app, err := scanApplication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "应用不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 更新应用: %w", err)
	}
	return app, nil
}

// SetStatus 启用或停用应用。
//
// 停用不撤销任何已签发的 token，也不需要：应用是否启用由
// GetActiveByAppID 在每次登录和每次 SDK 回源校验时重新判定，gRPC 拦截器
// 的凭据缓存刻意不缓存应用状态（见 grpcapi.appVerifier 的注释）。所以
// 停用的实际生效延迟上限就是该应用自己配置的 TokenCacheTTLSeconds
// （SDK 本地缓存），在这里再撤销一遍只会制造第二个执行点。
func (s *ApplicationService) SetStatus(ctx context.Context, id uuid.UUID, status string) (*domain.Application, error) {
	switch status {
	case domain.ApplicationStatusActive, domain.ApplicationStatusDisabled:
	default:
		return nil, domain.Errorf(domain.ErrInvalidArgument,
			"未知的应用状态 %q，只接受 %s 或 %s",
			status, domain.ApplicationStatusActive, domain.ApplicationStatusDisabled)
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE application SET status = $2, updated_at = now()
		WHERE id = $1
		RETURNING `+applicationColumns, id, status)

	app, err := scanApplication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.Errorf(domain.ErrNotFound, "应用不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("service: 更新应用状态: %w", err)
	}
	return app, nil
}
```

- [ ] **Step 5: 运行，确认通过**

```bash
./scripts/test.sh ./internal/service -run 'TestSetStatus|TestUpdateOnly|TestUpdateRejects'
```

- [ ] **Step 6: 写 HTTP 层的失败测试**

> **先读再写：** 下面四个测试用的 `newHTTPEnv` / `createApp` / `doJSON` 等是**示意名**。打开 `internal/httpapi/application_test.go` 与 `internal/httpapi/env_test.go`，用那里真实存在的辅助函数名与签名重写，**不要新造一套并行脚手架**。"未登录返回 401"那条照 `admin_test.go` 里既有的写法来。

```go
func TestPatchApplicationUpdatesName(t *testing.T) {
	e := newHTTPEnv(t)
	app := e.createApp(t, "旧名", "a")

	var got applicationDTO
	e.doJSON(t, http.MethodPatch, "/admin/api/applications/"+app.ID, map[string]any{
		"name": "新名", "cookieDomain": "example.com",
	}, http.StatusOK, &got)

	if got.Name != "新名" || got.CookieDomain != "example.com" {
		t.Fatalf("got = %+v", got)
	}
}

func TestPatchApplicationStatusDisables(t *testing.T) {
	e := newHTTPEnv(t)
	app := e.createApp(t, "A", "a")

	var got applicationDTO
	e.doJSON(t, http.MethodPatch, "/admin/api/applications/"+app.ID+"/status", map[string]any{
		"status": "DISABLED",
	}, http.StatusOK, &got)
	if got.Status != "DISABLED" {
		t.Fatalf("Status = %q", got.Status)
	}
}

func TestPatchApplicationStatusRejectsUnknown(t *testing.T) {
	e := newHTTPEnv(t)
	app := e.createApp(t, "A", "a")

	e.doJSON(t, http.MethodPatch, "/admin/api/applications/"+app.ID+"/status", map[string]any{
		"status": "ENABLED",
	}, http.StatusBadRequest, nil)
}

// 未登录必须 401——这两个新接口和其他管理接口一样受 requireAdmin 保护。
func TestPatchApplicationRequiresAdmin(t *testing.T) {
	e := newHTTPEnv(t)
	app := e.createApp(t, "A", "a")

	e.doAnonymousJSON(t, http.MethodPatch, "/admin/api/applications/"+app.ID, map[string]any{
		"name": "x",
	}, http.StatusUnauthorized)
}
```

- [ ] **Step 7: 实现 HTTP handler 与路由**

在 `internal/httpapi/application.go` 里加：

```go
type updateApplicationRequest struct {
	Name         string `json:"name"`
	CookieDomain string `json:"cookieDomain"`
}

// update 修改应用展示名与 cookie 作用域。
// 会话策略与启停各有自己的接口，这里不受理——理由见 service.Update 的注释。
func (h *applicationHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req updateApplicationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	app, err := h.svc.Update(r.Context(), id, req.Name, req.CookieDomain)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toApplicationDTO(*app))
}

type setApplicationStatusRequest struct {
	Status string `json:"status"`
}

func (h *applicationHandler) setStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req setApplicationStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	app, err := h.svc.SetStatus(r.Context(), id, req.Status)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toApplicationDTO(*app))
}
```

在 `internal/httpapi/router.go` 的 `r.Get("/applications/{id}", appH.get)` 之后加：

```go
			// PATCH 而不是 PUT：这两个接口都是**局部更新**，只碰自己那几列。
			// 与下面 /session 的全量替换语义刻意不同。
			r.Patch("/applications/{id}", appH.update)
			r.Patch("/applications/{id}/status", appH.setStatus)
```

- [ ] **Step 8: 运行全套测试**

```bash
./scripts/test.sh
```

预期：全绿。基线是 368 个测试函数，本任务新增 7 个。

- [ ] **Step 9: 提交**

```bash
git add internal/service/application.go internal/service/application_test.go internal/httpapi/application.go internal/httpapi/application_test.go internal/httpapi/router.go
git commit -m "feat(app): 补上应用改名与启停，让 DISABLED 状态真正可写"
```

---

## Task 2: 登录方式配置的服务端校验（后端）

**为什么需要：** `SetConnector` 目前只检查类型非空，之后就把任意字符串当 connector 类型、把任意 JSON 当配置写进库。也就是说 `PUT /admin/api/applications/{id}/connectors/wechat` 会成功——为一个根本不存在的登录方式写一行配置，返回 204，没有任何人会发现。控制台马上就要成为这个接口的第一个真实调用方，配置写错时管理员必须当场看到错误，而不是保存成功、然后登录一直失败。

**校验四条：**
1. 类型必须是已注册的 connector（`password`、`sms_code`）
2. 配置里不许出现该 connector 的 `ConfigSchema()` 之外的键——这条能挡住前后端字段名漂移
3. `Required: true` 的字段必须有值
4. 值的类型必须与 `Field.Type` 相符（bool 字段不接受字符串 `"true"`）

**为什么放 service 层不放 handler：** 与 `internal/httpapi/user.go` 里 `revokeAllSessions` 上方注释同一条理由——放传输层的话，以后新增的管理入口会漏掉，而且不会有任何测试变红。

**会打破一个既有测试（这是好事）：** `internal/service/application_test.go` 里 `TestConnectorCRUD`（约 168 行）用的配置是 `{"minLength": 8}`，而 password 的 `ConfigSchema()` 只有 `allowPhone` / `allowUsername` / `allowEmail`——加上第 2 条校验后这个测试必然变红。**这正是校验真的在起作用的证据。** 把它改成用 `allowPhone`（bool）而不是绕过校验。

**Files:**
- Modify: `internal/service/application.go`
- Modify: `internal/service/application_test.go`（改既有的 `TestConnectorCRUD` + 新增校验测试）
- Modify: `cmd/fp/main.go`（构造顺序调整）
- Modify: 全部 `NewApplicationService(pool)` 调用点（共 12 处，见下）
- Test: `internal/service/application_test.go`

**Interfaces:**
- Produces:
  - `type ConnectorSchemas interface { Schemas() map[string][]domain.Field }`（`*connector.Registry` 已满足）
  - `func NewApplicationService(pool *pgxpool.Pool, schemas ConnectorSchemas) *ApplicationService`（**签名变更**）
- Consumes: `domain.Field`、`domain.FieldType*`

---

- [ ] **Step 1: 写失败的测试**

在 `internal/service/application_test.go` 顶部加一个测试用的 schema 提供者，然后加校验测试：

```go
// stubSchemas 让 SetConnector 的校验测试不依赖真实 registry，
// 从而能覆盖到"必填字段"这条——线上两个 connector 恰好都没有必填字段。
type stubSchemas map[string][]domain.Field

func (s stubSchemas) Schemas() map[string][]domain.Field { return s }

func TestSetConnectorRejectsUnregisteredType(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// wechat 没有注册。写成功的话，库里会多出一行永远不会被使用的配置，
	// 而管理员以为自己开通了微信登录。
	if err := svc.SetConnector(ctx, app.ID, "wechat", true, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	list, err := svc.ListConnectors(ctx, app.ID)
	if err != nil {
		t.Fatalf("ListConnectors: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("被拒绝的配置仍然落库了: %+v", list)
	}
}

func TestSetConnectorRejectsUnknownConfigKey(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// minLength 不在 password 的 ConfigSchema 里。前后端字段名漂移时，
	// 这条校验是唯一会喊出声的地方。
	err = svc.SetConnector(ctx, app.ID, "password", true, map[string]any{"minLength": float64(8)})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestSetConnectorRejectsWrongValueType(t *testing.T) {
	svc := newAppService(t)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// allowPhone 是 bool。前端如果把开关序列化成字符串 "true"，
	// connector.ConfigBool 读到的会是 false —— 开关看起来开着，实际关着。
	err = svc.SetConnector(ctx, app.ID, "password", true, map[string]any{"allowPhone": "true"})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestSetConnectorRejectsMissingRequiredField(t *testing.T) {
	// 线上两个 connector 都没有必填字段，只能用 stub 覆盖这条分支。
	svc := NewApplicationService(testsupport.NewTestDB(t), stubSchemas{
		"demo": {
			{Key: "apiKey", Label: "API Key", Type: domain.FieldTypeString, Required: true},
			{Key: "note", Label: "备注", Type: domain.FieldTypeString},
		},
	})
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 缺 apiKey
	if err := svc.SetConnector(ctx, app.ID, "demo", true, map[string]any{"note": "x"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("缺必填 err = %v, want ErrInvalidArgument", err)
	}
	// 必填给了空串同样不算数
	if err := svc.SetConnector(ctx, app.ID, "demo", true, map[string]any{"apiKey": ""}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("必填为空串 err = %v, want ErrInvalidArgument", err)
	}
	// 给全了就该成功；可选字段不给也没问题
	if err := svc.SetConnector(ctx, app.ID, "demo", true, map[string]any{"apiKey": "k"}); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
}

// 未注入元数据时必须**失败关闭**，不能退化成"跳过校验"。
// 如果 nil 意味着放行，那么任何一个忘了传 registry 的调用点都会静默地
// 失去全部校验，而所有测试照绿——这正是第二阶段反复栽跟头的那类缺陷。
func TestSetConnectorFailsClosedWithoutSchemas(t *testing.T) {
	svc := NewApplicationService(testsupport.NewTestDB(t), nil)
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.SetConnector(ctx, app.ID, "password", true, nil); err == nil {
		t.Fatal("未注入 connector 元数据时 SetConnector 竟然成功了")
	}
}

func TestSetConnectorAcceptsIntFromJSONFloat(t *testing.T) {
	// JSONB 反序列化出来的数字一律是 float64（见 connector.ConfigInt 的注释）。
	// int 字段的校验必须接受整数值的 float64，否则任何走过一次 JSON 的
	// 配置都会被自己的校验拒掉。
	svc := NewApplicationService(testsupport.NewTestDB(t), stubSchemas{
		"demo": {{Key: "ttl", Label: "TTL", Type: domain.FieldTypeInt}},
	})
	ctx := context.Background()

	app, _, err := svc.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.SetConnector(ctx, app.ID, "demo", true, map[string]any{"ttl": float64(30)}); err != nil {
		t.Fatalf("整数值的 float64 被拒: %v", err)
	}
	// 但小数不是整数
	if err := svc.SetConnector(ctx, app.ID, "demo", true, map[string]any{"ttl": float64(1.5)}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("小数 err = %v, want ErrInvalidArgument", err)
	}
}
```

- [ ] **Step 2: 改既有的 `TestConnectorCRUD`**

把该测试里的 `cfg := map[string]any{"minLength": float64(8)}` 一段整体换成合法配置：

```go
	cfg := map[string]any{"allowPhone": true}
	if err := svc.SetConnector(ctx, app.ID, "password", true, cfg); err != nil {
		t.Fatalf("SetConnector: %v", err)
	}
	// 重复设置应为 upsert 而非报错
	cfg["allowPhone"] = false
	if err := svc.SetConnector(ctx, app.ID, "password", true, cfg); err != nil {
		t.Fatalf("SetConnector upsert: %v", err)
	}

	got, err := svc.GetConnector(ctx, app.ID, "password")
	if err != nil {
		t.Fatalf("GetConnector: %v", err)
	}
	if !got.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if got.Config["allowPhone"] != false {
		t.Fatalf("allowPhone = %v, want false", got.Config["allowPhone"])
	}
```

- [ ] **Step 3: 改 `newAppService` 辅助函数**

`internal/service/application_test.go` 里的 `newAppService` 目前是 `NewApplicationService(testsupport.NewTestDB(t))`。改成注入真实 registry，这样上面用真实类型名（`password`）的测试才有意义：

```go
func newAppService(t *testing.T) *ApplicationService {
	t.Helper()
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(nil)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	if err := reg.Register(connector.NewSMSCode(nil)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}
	return NewApplicationService(testsupport.NewTestDB(t), reg)
}
```

> **注意：** `connector.NewPassword(nil)` / `NewSMSCode(nil)` 传 nil 依赖是刻意的——这里只用它们的 `Type()` 与 `ConfigSchema()`，不会调 `Authenticate`。如果这两个构造函数对 nil 参数有校验而报错，改成传一个空的桩实现。

- [ ] **Step 4: 运行，确认失败**

```bash
./scripts/test.sh ./internal/service -run 'TestSetConnector|TestConnectorCRUD'
```

预期：编译失败（`NewApplicationService` 参数个数不对）。

- [ ] **Step 5: 实现**

改 `internal/service/application.go`：

```go
// ConnectorSchemas 是 SetConnector 校验配置所需的最小能力。
// *connector.Registry 满足它。用窄接口而不是直接依赖 *connector.Registry，
// 是为了让测试能注入自定义元数据——线上两个 connector 恰好都没有必填字段，
// 直接依赖真实 registry 的话"必填校验"这条分支永远测不到。
type ConnectorSchemas interface {
	Schemas() map[string][]domain.Field
}

// ApplicationService 管理接入端应用及其登录方式配置。
type ApplicationService struct {
	pool    *pgxpool.Pool
	schemas ConnectorSchemas
}

// NewApplicationService 构造 ApplicationService。
//
// schemas 为 nil 时 SetConnector 会直接报错而不是跳过校验——失败关闭。
// 让 nil 等于"不校验"的话，任何一个忘了注入的调用点都会静默地失去全部
// 配置校验，而没有任何测试会变红。
func NewApplicationService(pool *pgxpool.Pool, schemas ConnectorSchemas) *ApplicationService {
	return &ApplicationService{pool: pool, schemas: schemas}
}
```

把 `SetConnector` 开头的校验换成：

```go
func (s *ApplicationService) SetConnector(ctx context.Context, appID uuid.UUID, connectorType string, enabled bool, config map[string]any) error {
	if connectorType == "" {
		return domain.Errorf(domain.ErrInvalidArgument, "connector 类型不能为空")
	}
	if config == nil {
		config = map[string]any{}
	}
	if err := s.validateConnectorConfig(connectorType, config); err != nil {
		return err
	}
	// ……以下 json.Marshal 与 INSERT ... ON CONFLICT 保持原样
```

在 `SetConnector` 后面加校验函数：

```go
// validateConnectorConfig 按 connector 自己声明的 ConfigSchema 校验一份配置。
//
// 四条：类型已注册、无多余键、必填有值、值类型相符。多余键这条是给
// 前后端字段名漂移准备的——少了它，前端把 allowPhone 写成 allow_phone
// 会一路静默写库，开关看起来是开的，实际读到的永远是默认值。
func (s *ApplicationService) validateConnectorConfig(connectorType string, config map[string]any) error {
	if s.schemas == nil {
		return fmt.Errorf("service: ApplicationService 未注入 connector 元数据，无法校验 %q 的配置", connectorType)
	}
	all := s.schemas.Schemas()
	fields, ok := all[connectorType]
	if !ok {
		return domain.Errorf(domain.ErrNotFound, "未知的登录方式 %q", connectorType)
	}

	byKey := make(map[string]domain.Field, len(fields))
	for _, f := range fields {
		byKey[f.Key] = f
	}
	for key := range config {
		if _, ok := byKey[key]; !ok {
			return domain.Errorf(domain.ErrInvalidArgument, "%s 不支持配置项 %q", connectorType, key)
		}
	}
	for _, f := range fields {
		v, present := config[f.Key]
		if !present {
			if f.Required {
				return domain.Errorf(domain.ErrInvalidArgument, "%s 缺少必填配置项 %q", connectorType, f.Key)
			}
			continue
		}
		if err := checkFieldValue(f, v); err != nil {
			return err
		}
	}
	return nil
}

// checkFieldValue 校验单个配置值的类型。
//
// int 分支接受整数值的 float64：配置在库里是 JSONB，反序列化出来的数字
// 一律是 float64（同 connector.ConfigInt 的注释）。不接受的话，任何一份
// 存过又读回来的配置都会被自己的校验拒掉。
func checkFieldValue(f domain.Field, v any) error {
	switch f.Type {
	case domain.FieldTypeBool:
		if _, ok := v.(bool); !ok {
			return domain.Errorf(domain.ErrInvalidArgument, "配置项 %q 需要布尔值，收到 %T", f.Key, v)
		}
	case domain.FieldTypeInt:
		switch n := v.(type) {
		case float64:
			if n != math.Trunc(n) {
				return domain.Errorf(domain.ErrInvalidArgument, "配置项 %q 需要整数，收到 %v", f.Key, n)
			}
		case int, int32, int64:
		default:
			return domain.Errorf(domain.ErrInvalidArgument, "配置项 %q 需要整数，收到 %T", f.Key, v)
		}
	case domain.FieldTypeString, domain.FieldTypeSecret:
		str, ok := v.(string)
		if !ok {
			return domain.Errorf(domain.ErrInvalidArgument, "配置项 %q 需要字符串，收到 %T", f.Key, v)
		}
		if f.Required && str == "" {
			return domain.Errorf(domain.ErrInvalidArgument, "配置项 %q 是必填项，不能为空", f.Key)
		}
	default:
		// 未知的 FieldType 说明有人加了新类型却没同步这里。放行会让新类型
		// 完全失去校验，所以宁可报错——这是内部一致性问题，不是调用方的错。
		return fmt.Errorf("service: 未知的配置项类型 %q（字段 %q）", f.Type, f.Key)
	}
	return nil
}
```

导入里加 `"math"`。

- [ ] **Step 6: 更新全部 12 处构造调用**

`NewApplicationService` 的调用点（`grep -rn "NewApplicationService(" --include='*.go' .`）：

| 文件 | 传什么 |
|---|---|
| `cmd/fp/main.go` | `registry`——但**必须把 `registry` 的构造移到 `appSvc` 之前**，见下一步 |
| `internal/service/application_test.go` | 已在 Step 3 处理 |
| `internal/service/account_test.go` | 真实 registry（照 Step 3 的 `newAppService` 写法） |
| `internal/service/auth_test.go`（3 处） | 真实 registry |
| `internal/service/user_test.go` | 真实 registry |
| `internal/grpcapi/env_test.go` | 真实 registry（该文件已经在构造 registry，复用即可） |
| `internal/httpapi/env_test.go` | 真实 registry |
| `internal/httpapi/user_test.go` | 真实 registry |
| `internal/integration/env_test.go` | 真实 registry |
| `internal/integration/phase2_env_test.go` | 真实 registry |

多数测试文件本来就在构造 registry（要传给 `AuthDeps`），直接把同一个实例传进来即可；顺序上要先建 registry 再建 appSvc。

- [ ] **Step 7: 调整 `cmd/fp/main.go` 的构造顺序**

当前顺序是 `appSvc := service.NewApplicationService(pool)` 在前、`registry := connector.NewRegistry()` 在后。把 registry 那一整段（`NewRegistry` + 两次 `Register`）**上移到 `appSvc` 之前**，然后：

```go
	appSvc := service.NewApplicationService(pool, registry)
```

注意 `connector.NewPassword(userSvc)` 依赖 `userSvc`，所以最终顺序是：`userSvc` → `codeSvc` → `registry` → `appSvc`。

- [ ] **Step 8: 运行全套测试**

```bash
./scripts/test.sh
```

- [ ] **Step 9: 确认构建与静态检查干净**

```bash
go build ./... && go vet ./... && gofmt -l .
```

- [ ] **Step 10: 提交**

```bash
git add -A
git commit -m "feat(app): SetConnector 按 ConfigSchema 校验配置，未知类型与未知键不再静默落库"
```

---

## Task 3: 静态资源托管与单二进制打包（后端）

**目标：** `fp` 二进制自带控制台前端，`./fp` 起来后浏览器打开 `http://localhost:8080/` 就是控制台。同时保证**没构建过前端的人照样能 `go build ./...`**，且打开首页时看到的是一句人话而不是空白页。

**三条必须守住的行为，每条都有对应测试：**
1. `/admin/api/**` 下不存在的路径返回 **JSON 404**，绝不返回 `index.html`。SPA 回退写错的话，前端所有 API 调用在拼错路径时会拿到一段 HTML，然后 `res.json()` 抛一个和真实原因毫无关系的解析错误——这类问题极难排查。
2. 前端未构建时（`dist` 里只有 `.gitkeep`）返回 **503 + 明确提示**，不是空白页也不是 panic。
3. `/assets/**` 是 Vite 产出的带内容哈希的文件，可以 `immutable` 长缓存；`index.html` 必须 `no-cache`，否则发布新版本后用户拿着旧 HTML 去加载已经不存在的旧 JS。

**Files:**
- Create: `web/embed.go`
- Create: `web/dist/.gitkeep`（空文件）
- Create: `internal/httpapi/static.go`
- Create: `internal/httpapi/static_test.go`
- Create: `scripts/build-web.sh`
- Modify: `internal/httpapi/router.go`
- Modify: `cmd/fp/main.go`
- Modify: `.gitignore`

**Interfaces:**
- Produces:
  - `web.Dist() fs.FS`——返回以 `dist/` 为根的只读文件系统
  - `httpapi.Deps.Console fs.FS`——为 nil 时等同于"未构建"
- Consumes: 既有的 `writeJSON`、`errorBody`

---

- [ ] **Step 1: 建 embed 包与占位文件**

```bash
mkdir -p web/dist
touch web/dist/.gitkeep
```

`web/embed.go`：

```go
// Package web 把管理控制台的前端构建产物嵌进二进制。
package web

import (
	"embed"
	"io/fs"
)

// 必须写 all: 前缀。
//
// 不带 all: 的话 embed 会跳过以 . 开头的文件，而仓库里 web/dist/ 只提交了
// 一个 .gitkeep（构建产物不入库）——于是"目录里没有可嵌入的文件"，编译
// 直接失败：cannot embed directory web/dist: contains no embeddable files。
// 带上 all: 之后，没跑过 npm 的人也能 go build ./...，只是打开控制台会看到
// 一句"前端尚未构建"的提示（见 httpapi/static.go）。
//
//go:embed all:dist
var assets embed.FS

// Dist 返回以 dist/ 为根的前端产物文件系统。
func Dist() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		// embed 的目录结构在编译期就固定了，这里失败意味着上面的 embed
		// 指令被改坏了，属于编译期就该发现的错误。
		panic("web: 无法定位 dist 目录: " + err.Error())
	}
	return sub
}
```

- [ ] **Step 2: 确认 `go build` 在前端未构建时也能过**

```bash
go build ./...
```

预期：通过。如果报 `contains no embeddable files`，说明 `all:` 前缀漏了。

- [ ] **Step 3: 写失败的测试**

`internal/httpapi/static_test.go`：

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// builtConsole 模拟一份构建好的前端产物。
func builtConsole() fstest.MapFS {
	return fstest.MapFS{
		"index.html":               {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/index-abc123.js":   {Data: []byte("console.log(1)")},
		"assets/index-def456.css":  {Data: []byte("body{}")},
		"favicon.svg":              {Data: []byte("<svg/>")},
	}
}

// 未构建：只有 .gitkeep，没有 index.html。
func unbuiltConsole() fstest.MapFS {
	return fstest.MapFS{".gitkeep": {Data: []byte{}}}
}

func TestStaticServesIndexAtRoot(t *testing.T) {
	h := newStaticHandler(builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "id=root") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Fatalf("index.html 的 Cache-Control = %q，必须含 no-cache", cc)
	}
}

// SPA 回退：前端路由（/users/xxx）在服务端不存在对应文件，必须回 index.html，
// 否则刷新页面就 404。
func TestStaticFallsBackToIndexForClientRoutes(t *testing.T) {
	h := newStaticHandler(builtConsole())
	for _, p := range []string{"/users", "/users/123", "/applications/abc/connectors"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "id=root") {
			t.Fatalf("%s: code = %d body = %q", p, rec.Code, rec.Body.String())
		}
	}
}

// 带内容哈希的资源可以长缓存。
func TestStaticSetsImmutableOnHashedAssets(t *testing.T) {
	h := newStaticHandler(builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") {
		t.Fatalf("Cache-Control = %q，带哈希的资源应当 immutable", cc)
	}
}

// 【辨别力】assets 下**不存在**的文件必须 404，不能回退到 index.html。
//
// 没有这条的话，一个"任何找不到的路径都回 index.html"的实现会通过上面
// 所有测试，而线上表现是：某个 JS 文件名写错时，浏览器拿到一份 HTML 并
// 以 text/html 执行它，报出一个和真实原因毫无关系的语法错误。
func TestStaticDoesNotFallBackForMissingAssets(t *testing.T) {
	h := newStaticHandler(builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/does-not-exist.js", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404; body = %q", rec.Code, rec.Body.String())
	}
}

// 未构建时给一句人话，而不是空白页或 panic。
func TestStaticReportsNotBuilt(t *testing.T) {
	h := newStaticHandler(unbuiltConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "build-web.sh") {
		t.Fatalf("提示里应当告诉人怎么构建，实际 body = %q", rec.Body.String())
	}
}

func TestStaticNilFSReportsNotBuilt(t *testing.T) {
	h := newStaticHandler(nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

// 【辨别力】这条是整个任务里最要紧的一条：API 的 404 必须是 JSON，
// 绝不能被 SPA 回退吃掉变成 index.html。
func TestRouterAPINotFoundStaysJSON(t *testing.T) {
	// 用本包既有的路由脚手架装配一个带 Console 的 router。
	// 具体辅助函数名以 env_test.go 里实际存在的为准。
	h := newTestRouterWithConsole(t, builtConsole())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/api/no-such-endpoint", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON——API 的 404 被 SPA 回退吃掉了", ct)
	}
	var body errorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if body.Error == "" {
		t.Fatal("error 字段为空")
	}
}

// /healthz 是显式注册的路由，不能被 /* 通配吃掉。
func TestRouterHealthzNotShadowedByStatic(t *testing.T) {
	h := newTestRouterWithConsole(t, builtConsole())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "id=root") {
		t.Fatal("/healthz 返回了 index.html")
	}
}
```

> **实现者注意：** `newTestRouterWithConsole` 需要你在 `internal/httpapi/env_test.go` 里新增——照该文件里既有的构造 router 的写法，多传一个 `Console: fsys` 即可。不要为它新造一套依赖装配。

- [ ] **Step 4: 运行，确认失败**

```bash
./scripts/test.sh ./internal/httpapi -run 'TestStatic|TestRouterAPINotFound|TestRouterHealthz'
```

预期：编译失败，`newStaticHandler` 未定义。

- [ ] **Step 5: 实现 `internal/httpapi/static.go`**

```go
package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// notBuiltMessage 在前端尚未构建时返回。
//
// 给一句能照着做的话，而不是空白页：仓库里 web/dist/ 只有一个 .gitkeep，
// 任何 clone 下来直接 go run 的人都会撞上这个页面。
const notBuiltMessage = "管理控制台前端尚未构建。请先运行 ./scripts/build-web.sh 再启动 fp。"

// newStaticHandler 返回托管管理控制台前端的 handler。
//
// 路径解析规则，按顺序：
//  1. dist 里存在同名文件      → 直接返回该文件
//  2. 路径以 /assets/ 开头     → 404。assets 下的文件名都带内容哈希，
//     找不到就是真的没有，回退成 index.html 只会让浏览器把一份 HTML
//     当 JS 执行，报出与真实原因无关的语法错误
//  3. 其余路径                 → 回 index.html，交给前端路由
//
// 注意这个 handler 不认识 /admin/api——那些路由在 chi 里注册得更具体，
// 根本走不到这里；API 的 404 由 /admin/api 子路由自己的 NotFound 处理。
func newStaticHandler(dist fs.FS) http.Handler {
	if dist == nil {
		return notBuiltHandler()
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return notBuiltHandler()
	}

	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(upath, "/")

		if name != "" {
			if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
				// Vite 给 assets/ 下的文件名都加了内容哈希，内容一变文件名
				// 就变，可以放心长缓存。其余文件（favicon 之类）不加。
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
		}

		// SPA 回退：index.html 必须每次revalidate，否则发布新版本后用户
		// 拿着缓存里的旧 HTML 去请求已经不存在的旧 JS 文件名。
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	})
}

// notBuiltHandler 是前端未构建时的降级 handler。
func notBuiltHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(notBuiltMessage))
	})
}
```

- [ ] **Step 6: 挂到路由上**

`internal/httpapi/router.go`：`Deps` 里加字段——

```go
	// Console 是管理控制台的前端构建产物。为 nil（或其中没有 index.html）
	// 时，非 API 路径统一返回 503 加一句"请先构建前端"，而不是空白页。
	Console fs.FS
```

在 `r.Route("/admin/api", ...)` 的 `func(r chi.Router)` **最开头**加一行，让 API 的 404 保持 JSON：

```go
		// API 的 404 必须是 JSON。少了这行，chi 会用它默认的纯文本 404，
		// 前端的 res.json() 会抛一个与真实原因无关的解析错误。
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotFound, errorBody{Error: "接口不存在"})
		})
```

在 `return r` 之前加静态资源兜底：

```go
	// 放在最后：chi 的路由匹配偏好更具体的模式，/healthz 与 /admin/api/*
	// 都比 /* 具体，不会被这条吃掉。
	r.Handle("/*", newStaticHandler(d.Console))
```

- [ ] **Step 7: 运行测试确认通过**

```bash
./scripts/test.sh ./internal/httpapi
```

若 `TestRouterAPINotFoundStaysJSON` 仍红，说明 chi 的匹配优先级与预期不符——**不要**改成在静态 handler 里判断 `/admin/api` 前缀绕过去（那会把同一条规则写在两个地方）。正确做法是确认 `r.NotFound` 注册在了子路由内部而不是根路由上。

- [ ] **Step 8: 接进 `cmd/fp/main.go`**

导入 `"github.com/basicfu/fp/web"`，在 `httpapi.Deps{...}` 里加：

```go
			Console:  web.Dist(),
```

- [ ] **Step 9: 写构建脚本**

`scripts/build-web.sh`：

```bash
#!/usr/bin/env bash
# 构建管理控制台前端。产物落在 web/dist/，由 web/embed.go 嵌进二进制。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/web"

if [ ! -d node_modules ]; then
  echo "==> 安装前端依赖"
  npm ci
fi

echo "==> 构建前端"
npm run build

echo "==> 产物："
du -sh "$ROOT/web/dist"
```

```bash
chmod +x scripts/build-web.sh
```

- [ ] **Step 10: 更新 .gitignore**

追加：

```
web/node_modules/
web/dist/*
!web/dist/.gitkeep
```

- [ ] **Step 11: 全量验证**

```bash
go build ./... && go vet ./... && gofmt -l . && ./scripts/test.sh
```

- [ ] **Step 12: 提交**

```bash
git add -A
git commit -m "feat(httpapi): go:embed 托管控制台静态资源，API 404 保持 JSON"
```

---

## Task 4: 前端工程骨架与 API 客户端

**这一任务只做地基**：能构建、能跑测试、能跟后端通话。任何页面都不在本任务里。

**Files:**
- Create: `web/`（Vite 脚手架产出的全部文件）
- Create: `web/src/lib/api.ts`
- Create: `web/src/lib/api.test.ts`
- Modify: `web/vite.config.ts`、`web/tsconfig*.json`、`web/package.json`

**Interfaces:**
- Produces:
  - `class ApiError extends Error { status: number }`
  - `api.get<T>(path)` / `api.post<T>(path, body?)` / `api.put<T>` / `api.patch<T>` / `api.del<T>`
  - `setUnauthorizedHandler(fn: () => void)`
  - 路径一律**不带** `/admin/api` 前缀（客户端内部补），即 `api.get('/applications')`

---

- [ ] **Step 1: 生成脚手架**

在仓库根执行：

```bash
npm create vite@latest web -- --template react-ts
cd web && npm i
```

- [ ] **Step 2: 装依赖**

```bash
cd web
npm i react-router react-hook-form zod @hookform/resolvers
npm i -D vitest @testing-library/react @testing-library/jest-dom jsdom @types/node
npm i tailwindcss @tailwindcss/vite
```

- [ ] **Step 3: 接 Tailwind 与 shadcn**

把 `web/src/index.css` 的**全部内容**替换成：

```css
@import "tailwindcss";
```

然后：

```bash
npx shadcn@latest init -d -y
```

再装组件——**必须带 `@shadcn/` 命名空间**：

```bash
npx shadcn@latest add @shadcn/button @shadcn/input @shadcn/label @shadcn/table @shadcn/card @shadcn/badge @shadcn/select @shadcn/switch @shadcn/dialog @shadcn/tabs @shadcn/separator @shadcn/sonner
```

> 写成裸名字（`npx shadcn@latest add button`）会**静默成功**：退出码 0、无报错、不生成任何文件。执行后务必 `ls src/components/ui/` 确认这 12 个文件都在。
>
> 另外：**shadcn v4 的 registry 里没有 `form`**，别去装它，也别去找 `<Form>` / `<FormField>`。Task 7 的动态表单直接建在 react-hook-form 上。

- [ ] **Step 4: 删掉全部 `baseUrl`**

`shadcn init` 会往 tsconfig 里写 `"baseUrl": "."`。**TypeScript 6.0 已废弃 `baseUrl`，留着直接构建失败**（`TS5101`）。

打开 `web/tsconfig.json`、`web/tsconfig.app.json`、`web/tsconfig.node.json`，把每一处 `"baseUrl"` 整行删掉，**保留 `paths`**：

```json
    "paths": {
      "@/*": ["./src/*"]
    }
```

`paths` 至少要出现在 `tsconfig.app.json` 里；`tsconfig.json` 顶层也留一份，shadcn CLI 靠它认别名。

- [ ] **Step 5: 写 `web/vite.config.ts`**

整体替换成：

```ts
// defineConfig 必须从 vitest/config 导入，不能从 vite 导入。
// 从 vite 导入时，下面的 test 配置块会让 tsc -b 报 TS2769。
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  server: {
    // 开发时把 API 转发给本机的 fp，浏览器看到的仍是同源，
    // 管理端会话 cookie 因此能正常带上。
    proxy: { '/admin/api': 'http://localhost:8080' },
  },
  test: {
    environment: 'jsdom',
    // 刻意不开 globals：它只影响运行时，TypeScript 依然不认识 test/expect，
    // tsc -b 会报 TS2593/TS2304。每个测试文件顶部显式 import 即可。
    globals: false,
  },
})
```

- [ ] **Step 6: 加测试脚本**

`web/package.json` 的 `scripts` 里加：

```json
    "test": "vitest run",
    "test:watch": "vitest"
```

- [ ] **Step 7: 写失败的测试 `web/src/lib/api.test.ts`**

```ts
import { test, expect, vi, afterEach } from 'vitest'
import { api, ApiError, setUnauthorizedHandler } from './api'

afterEach(() => {
  vi.unstubAllGlobals()
  setUnauthorizedHandler(() => {})
})

function stubFetch(res: Response) {
  const spy = vi.fn().mockResolvedValue(res)
  vi.stubGlobal('fetch', spy)
  return spy
}

test('GET 解析 JSON 响应体', async () => {
  stubFetch(new Response(JSON.stringify({ id: 'x' }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  }))
  await expect(api.get<{ id: string }>('/applications')).resolves.toEqual({ id: 'x' })
})

test('请求路径自动补上 /admin/api 前缀', async () => {
  const spy = stubFetch(new Response('{}', { status: 200 }))
  await api.get('/applications')
  expect(spy.mock.calls[0][0]).toBe('/admin/api/applications')
})

// 【辨别力】后端多个接口返回 204 且**没有响应体**（putConnector、
// setPassword、logout）。直接 await res.json() 会抛 SyntaxError——
// 表现是"保存明明成功了，界面却弹了一个看不懂的错误"。
test('204 无响应体时正常返回而不是抛解析错误', async () => {
  stubFetch(new Response(null, { status: 204 }))
  await expect(api.put('/applications/1/connectors/password', { enabled: true })).resolves.toBeUndefined()
})

test('错误响应取后端的 error 字段作为消息', async () => {
  stubFetch(new Response(JSON.stringify({ error: '应用不存在' }), {
    status: 404,
    headers: { 'Content-Type': 'application/json' },
  }))
  await expect(api.get('/applications/nope')).rejects.toMatchObject({
    status: 404,
    message: '应用不存在',
  })
})

// 【辨别力】反代返回的 502、或路径写错拿到一整页 HTML 时，响应体不是
// JSON。此时必须退回状态码兜底，既不能抛解析错误，也不能把一整页 HTML
// 塞进提示框。
test('响应体不是 JSON 时退回状态码提示，不抛解析错误', async () => {
  stubFetch(new Response('<!doctype html><h1>502 Bad Gateway</h1>', {
    status: 502,
    headers: { 'Content-Type': 'text/html' },
  }))
  const err = await api.get('/applications').catch((e) => e)
  expect(err).toBeInstanceOf(ApiError)
  expect(err.status).toBe(502)
  expect(err.message).toContain('502')
  expect(err.message).not.toContain('<')
})

test('401 触发未授权回调并且照样抛错', async () => {
  const onUnauth = vi.fn()
  setUnauthorizedHandler(onUnauth)
  stubFetch(new Response(JSON.stringify({ error: '未登录' }), { status: 401 }))

  await expect(api.get('/me')).rejects.toBeInstanceOf(ApiError)
  expect(onUnauth).toHaveBeenCalledOnce()
})

test('POST 带 JSON 请求体与 Content-Type', async () => {
  const spy = stubFetch(new Response('{}', { status: 200 }))
  await api.post('/applications', { name: 'A', slug: 'a' })

  const init = spy.mock.calls[0][1] as RequestInit
  expect(init.method).toBe('POST')
  expect(init.body).toBe(JSON.stringify({ name: 'A', slug: 'a' }))
  expect(new Headers(init.headers).get('Content-Type')).toContain('application/json')
})

// GET 不该带 Content-Type：带上会让某些反代对无体请求做多余处理。
test('GET 不带请求体也不带 Content-Type', async () => {
  const spy = stubFetch(new Response('{}', { status: 200 }))
  await api.get('/applications')
  const init = spy.mock.calls[0][1] as RequestInit
  expect(init.body).toBeUndefined()
  expect(new Headers(init.headers).get('Content-Type')).toBeNull()
})
```

- [ ] **Step 8: 运行，确认失败**

```bash
cd web && npm test
```

预期：`Cannot find module './api'`。

- [ ] **Step 9: 实现 `web/src/lib/api.ts`**

```ts
// 管理控制台的 HTTP 客户端。
//
// 认证走的是后端在登录时下发的 HttpOnly cookie，所以这里没有任何
// token 存取逻辑——浏览器自动带。开发模式下 Vite 把 /admin/api 代理到
// 本机 fp，浏览器看到的仍是同源，cookie 照常生效。
const BASE = '/admin/api'

/** ApiError 携带 HTTP 状态码，页面据此区分"没权限"和"输入不合法"。 */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

type UnauthorizedHandler = () => void

let onUnauthorized: UnauthorizedHandler = () => {}

/**
 * 注册 401 回调。由路由层调用，用来把用户送回登录页。
 *
 * 放在这里而不是让每个调用方自己判断 401：漏判一处的后果是用户看到
 * 一个"未登录"的错误提示却停在原地，不知道该做什么。
 */
export function setUnauthorizedHandler(fn: UnauthorizedHandler) {
  onUnauthorized = fn
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin' }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }

  const res = await fetch(BASE + path, init)

  if (res.status === 401) {
    onUnauthorized()
  }
  if (!res.ok) {
    throw new ApiError(res.status, await readErrorMessage(res))
  }
  // 后端多个接口返回 204 且不带响应体（putConnector、setPassword、
  // logout）。对这些响应调 res.json() 会抛 SyntaxError。
  if (res.status === 204) {
    return undefined as T
  }
  return (await res.json()) as T
}

/**
 * 从错误响应里取可读的消息。
 *
 * 后端的错误一律是 {"error": "..."}（见 httpapi/respond.go）。但反代插进来
 * 的 502、或者请求路径写错时，响应体可能是一整页 HTML——那时用状态码兜底，
 * 既不抛解析错误，也不把 HTML 塞进提示框。
 */
async function readErrorMessage(res: Response): Promise<string> {
  try {
    const data = (await res.json()) as { error?: unknown }
    if (typeof data?.error === 'string' && data.error !== '') {
      return data.error
    }
  } catch {
    // 不是 JSON，落到下面的兜底
  }
  return `请求失败（HTTP ${res.status}）`
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}
```

- [ ] **Step 10: 运行测试与构建**

```bash
cd web && npm test && npm run build
```

两者都必须通过。`npm run build` 会跑 `tsc -b`——如果报 `TS5101`，说明 Step 4 的 `baseUrl` 没删干净；报 `TS2769` 说明 Step 5 的 `defineConfig` 还是从 `vite` 导的；报 `TS2593`/`TS2304` 说明测试文件里漏了显式 import。

- [ ] **Step 11: 确认产物体积在预算内**

```bash
du -sh web/dist
```

预期 500 KB 上下。超过 1.5 MB 说明误引了大依赖，要查 `web/package.json`。

- [ ] **Step 12: 确认 Go 侧没被前端目录带坏**

```bash
go build ./... && go vet ./...
```

`web/node_modules` 里没有 `.go` 文件，Go 工具链会直接跳过；这一步只是确认它确实没有变慢或报错。

- [ ] **Step 13: 提交**

```bash
git add -A
git commit -m "feat(web): 前端工程骨架与 API 客户端"
```

---

## Task 5: 登录页、认证状态与布局

**Files:**
- Create: `web/src/lib/auth.tsx`
- Create: `web/src/lib/auth.test.tsx`
- Create: `web/src/components/Layout.tsx`
- Create: `web/src/pages/Login.tsx`
- Create: `web/src/routes.tsx`
- Modify: `web/src/main.tsx`
- Delete: `web/src/App.tsx`、`web/src/App.css`、`web/src/assets/react.svg`（脚手架示例，用不上）

**Interfaces:**
- Produces:
  - `<AuthProvider>` / `useAuth(): { status, username, login, logout }`
  - `status: 'loading' | 'authed' | 'anon'`
  - `<AppRoutes />`——挂在 `<BrowserRouter>` 内
- Consumes: `api`、`ApiError`、`setUnauthorizedHandler`（Task 4）

**已验证：** `react-router@8.3.1` 导出 `BrowserRouter` / `Routes` / `Route` / `Navigate` / `NavLink` / `Outlet` / `useNavigate` / `useParams` / `useSearchParams` / `Link`，与 v6 同名，照常用即可。

---

- [ ] **Step 1: 写失败的测试 `web/src/lib/auth.test.tsx`**

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { AuthProvider, useAuth } from './auth'

afterEach(() => vi.unstubAllGlobals())

function Probe() {
  const { status, username } = useAuth()
  return <div data-testid="probe">{status}:{username ?? '-'}</div>
}

test('挂载时探测 /me，已登录则进入 authed', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ id: '1', username: 'admin' }), { status: 200 }),
  ))

  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('authed:admin'))
})

// 【辨别力】未登录时 /me 返回 401。若实现把 401 当成"加载中"或直接抛到
// 顶层，界面会永远停在骨架屏上——用户看到的是一个转不完的圈，而不是登录页。
test('未登录时 /me 返回 401，落到 anon 而不是卡在 loading', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ error: '未登录' }), { status: 401 }),
  ))

  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('anon:-'))
})

// 网络断了、后端 500——同样必须落到 anon，让用户看到登录页并能重试，
// 而不是白屏。
test('探测失败时同样落到 anon', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network down')))

  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('anon:-'))
})
```

- [ ] **Step 2: 运行，确认失败**

```bash
cd web && npm test -- auth
```

- [ ] **Step 3: 实现 `web/src/lib/auth.tsx`**

```tsx
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { api, setUnauthorizedHandler } from './api'

type Status = 'loading' | 'authed' | 'anon'

interface MeResponse {
  id: string
  username: string
}

interface AuthValue {
  status: Status
  username: string | null
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthValue | null>(null)

export function useAuth(): AuthValue {
  const v = useContext(AuthContext)
  if (!v) throw new Error('useAuth 必须在 AuthProvider 内使用')
  return v
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status>('loading')
  const [username, setUsername] = useState<string | null>(null)

  // 任何一次请求拿到 401 都直接把状态打回未登录：管理端会话有效期两小时，
  // 用户很可能在某个页面上停留到过期，这时不该等他点到下一个按钮才发现。
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setStatus('anon')
      setUsername(null)
    })
    return () => setUnauthorizedHandler(() => {})
  }, [])

  // 首次挂载探测当前会话。
  //
  // 失败（401、网络断、后端 500）一律落到 anon，绝不留在 loading：
  // 留在 loading 的界面是一个永远转不完的圈，用户既看不到登录页也无从重试。
  useEffect(() => {
    let alive = true
    api
      .get<MeResponse>('/me')
      .then((me) => {
        if (!alive) return
        setUsername(me.username)
        setStatus('authed')
      })
      .catch(() => {
        if (!alive) return
        setUsername(null)
        setStatus('anon')
      })
    return () => {
      alive = false
    }
  }, [])

  const login = useCallback(async (u: string, p: string) => {
    const res = await api.post<{ username: string }>('/login', { username: u, password: p })
    setUsername(res.username)
    setStatus('authed')
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.post('/logout')
    } finally {
      // 后端登出失败也要把前端状态清掉：cookie 可能已经过期，
      // 留在"已登录"状态只会让用户在每个页面上撞 401。
      setUsername(null)
      setStatus('anon')
    }
  }, [])

  const value = useMemo<AuthValue>(
    () => ({ status, username, login, logout }),
    [status, username, login, logout],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd web && npm test -- auth
```

- [ ] **Step 5: 写登录页 `web/src/pages/Login.tsx`**

```tsx
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { useAuth } from '@/lib/auth'

const schema = z.object({
  username: z.string().min(1, '请输入用户名'),
  password: z.string().min(1, '请输入密码'),
})
type Values = z.infer<typeof schema>

export default function Login() {
  const { login } = useAuth()
  const [serverError, setServerError] = useState('')
  const { register, handleSubmit, formState } = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { username: '', password: '' },
  })

  async function onSubmit(v: Values) {
    setServerError('')
    try {
      await login(v.username, v.password)
    } catch (e) {
      setServerError(e instanceof Error ? e.message : '登录失败')
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-muted/30 p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>fp 管理控制台</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit(onSubmit)} className="space-y-4" noValidate>
            <div className="space-y-2">
              <Label htmlFor="username">用户名</Label>
              <Input id="username" autoComplete="username" {...register('username')} />
              {formState.errors.username && (
                <p className="text-sm text-destructive">{formState.errors.username.message}</p>
              )}
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">密码</Label>
              <Input id="password" type="password" autoComplete="current-password" {...register('password')} />
              {formState.errors.password && (
                <p className="text-sm text-destructive">{formState.errors.password.message}</p>
              )}
            </div>
            {serverError && <p className="text-sm text-destructive">{serverError}</p>}
            <Button type="submit" className="w-full" disabled={formState.isSubmitting}>
              {formState.isSubmitting ? '登录中…' : '登录'}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
```

- [ ] **Step 6: 写布局 `web/src/components/Layout.tsx`**

```tsx
import { NavLink, Outlet } from 'react-router'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/lib/auth'

const nav = [
  { to: '/applications', label: '应用' },
  { to: '/users', label: '用户' },
]

export default function Layout() {
  const { username, logout } = useAuth()

  return (
    <div className="flex min-h-screen">
      <aside className="w-52 shrink-0 border-r bg-muted/20 p-4">
        <div className="mb-6 px-2 text-lg font-semibold">fp</div>
        <nav className="space-y-1">
          {nav.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              className={({ isActive }) =>
                `block rounded-md px-3 py-2 text-sm ${
                  isActive ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground hover:bg-accent/50'
                }`
              }
            >
              {n.label}
            </NavLink>
          ))}
        </nav>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center justify-end gap-3 border-b px-6">
          <span className="text-sm text-muted-foreground">{username}</span>
          <Button variant="outline" size="sm" onClick={() => void logout()}>
            退出
          </Button>
        </header>
        <main className="min-w-0 flex-1 p-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
```

- [ ] **Step 7: 写路由 `web/src/routes.tsx`**

先只挂登录页和一个占位首页，其余页面在后续任务里逐个填进来。

```tsx
import { Navigate, Route, Routes } from 'react-router'
import Layout from '@/components/Layout'
import Login from '@/pages/Login'
import { useAuth } from '@/lib/auth'

/**
 * RequireAuth 把未登录的访问送回登录页。
 *
 * loading 期间渲染一个空白占位而不是直接跳转：首次进入时 /me 还没回来，
 * 直接跳转会让已登录用户先看到一次登录页闪烁。
 */
function RequireAuth({ children }: { children: React.ReactNode }) {
  const { status } = useAuth()
  if (status === 'loading') return <div className="p-8 text-sm text-muted-foreground">加载中…</div>
  if (status === 'anon') return <Navigate to="/login" replace />
  return <>{children}</>
}

export default function AppRoutes() {
  const { status } = useAuth()

  return (
    <Routes>
      <Route
        path="/login"
        element={status === 'authed' ? <Navigate to="/applications" replace /> : <Login />}
      />
      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route path="/" element={<Navigate to="/applications" replace />} />
        {/* 后续任务把应用与用户页面挂在这里 */}
      </Route>
      <Route path="*" element={<Navigate to="/applications" replace />} />
    </Routes>
  )
}
```

- [ ] **Step 8: 改入口 `web/src/main.tsx`**

```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { Toaster } from '@/components/ui/sonner'
import { AuthProvider } from '@/lib/auth'
import AppRoutes from '@/routes'
import './index.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <AuthProvider>
        <AppRoutes />
        <Toaster richColors position="top-right" />
      </AuthProvider>
    </BrowserRouter>
  </StrictMode>,
)
```

删掉脚手架示例文件：

```bash
cd web && rm -f src/App.tsx src/App.css src/assets/react.svg
```

- [ ] **Step 9: 构建与测试**

```bash
cd web && npm test && npm run build
```

- [ ] **Step 10: 人工验证登录闭环**

一个终端跑后端：

```bash
go run ./cmd/fp
```

另一个终端跑前端：

```bash
cd web && npm run dev
```

浏览器打开 Vite 打印的地址（默认 `http://localhost:5173`）：
1. 应当自动落到 `/login`
2. 用 `.env.local` 里配置的 `BOOTSTRAP_ADMIN_USER` / `BOOTSTRAP_ADMIN_PASSWORD` 登录
3. 登录后跳到 `/applications`（此时是空白主区，正常——页面还没做）
4. 刷新页面，应当**保持登录**（cookie 生效、`/me` 探测成功）
5. 点"退出"，应当回到登录页
6. 故意输错密码，应当看到后端返回的中文错误消息，而不是 `[object Object]` 或英文原文

- [ ] **Step 11: 提交**

```bash
git add -A
git commit -m "feat(web): 登录页、认证状态与主布局"
```

---

## Task 6: 应用列表、创建与详情

**Files:**
- Create: `web/src/lib/types.ts`
- Create: `web/src/lib/useResource.ts`
- Create: `web/src/lib/useResource.test.tsx`
- Create: `web/src/pages/Applications.tsx`
- Create: `web/src/pages/ApplicationDetail.tsx`
- Modify: `web/src/routes.tsx`

**Interfaces:**
- Produces:
  - `web/src/lib/types.ts` 里的全部 DTO 类型，后续任务直接引用
  - `useResource<T>(load, deps) => { data, loading, error, reload }`
- Consumes: `api`（Task 4）、`Layout`（Task 5）

---

- [ ] **Step 1: 写 `web/src/lib/types.ts`**

字段名与后端 DTO 的 JSON tag 一一对应，**不要改名**——改了之后哪里对不上，TypeScript 不会告诉你，因为 `api.get<T>` 只是断言。

```ts
// 与后端 internal/httpapi 里的 DTO 一一对应。
// 字段名必须与 Go 结构体的 json tag 完全一致。

export interface SessionPolicy {
  idleTimeoutSeconds: number
  idleTimeoutMobileSeconds: number
  maxLifetimeSeconds: number
  rotateIntervalSeconds: number
  extendIntervalSeconds: number
  tokenCacheTtlSeconds: number
}

export type ApplicationStatus = 'ACTIVE' | 'DISABLED'

export interface Application {
  id: string
  name: string
  slug: string
  appId: string
  status: ApplicationStatus
  cookieDomain: string
  session: SessionPolicy
  createdAt: number
  updatedAt: number
}

export interface CreateApplicationResponse {
  application: Application
  /** 明文密钥，只在创建时返回这一次，之后无法读回。 */
  appSecret: string
}

export type FieldType = 'string' | 'int' | 'bool' | 'secret'

/** 与 internal/domain/field.go 的 Field 对应。管理 UI 靠它自动生成表单。 */
export interface Field {
  key: string
  label: string
  type: FieldType
  required?: boolean
  default?: unknown
  help?: string
}

export interface ConnectorSchema {
  type: string
  fields: Field[]
}

export interface ConnectorConfig {
  type: string
  enabled: boolean
  config: Record<string, unknown>
}

export type UserStatus = 'ACTIVE' | 'FROZEN' | 'PENDING_DELETE' | 'DELETED'

export interface Identity {
  type: string
  subject: string
  lastLoginAt: number
}

export interface User {
  id: string
  nickname: string
  avatarUrl: string
  status: UserStatus
  hasPassword: boolean
  identities: Identity[]
  createdAt: number
}

export interface UserListResponse {
  items: User[]
  total: number
}

export interface UserSession {
  id: string
  appId: string
  ip: string
  ua: string
  mobile: boolean
  firstAuthAt: number
  idleExpiresAt: number
}

export interface LoginLog {
  id: string
  identityType: string
  subject: string
  event: string
  success: boolean
  reason: string
  ip: string
  ua: string
  createdAt: number
}
```

- [ ] **Step 2: 写 `useResource` 的失败测试**

```tsx
// web/src/lib/useResource.test.tsx
import { test, expect } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { useResource } from './useResource'

function Probe({ dep, load }: { dep: number; load: (d: number) => Promise<string> }) {
  const { data, loading, error } = useResource(() => load(dep), [dep])
  return <div data-testid="out">{loading ? 'loading' : error ? `err:${error}` : `data:${data}`}</div>
}

test('加载成功后给出数据', async () => {
  render(<Probe dep={1} load={async (d) => `v${d}`} />)
  await waitFor(() => expect(screen.getByTestId('out').textContent).toBe('data:v1'))
})

test('加载失败给出错误消息', async () => {
  render(<Probe dep={1} load={async () => { throw new Error('炸了') }} />)
  await waitFor(() => expect(screen.getByTestId('out').textContent).toBe('err:炸了'))
})

// 【辨别力】依赖变化引发的乱序返回。
//
// 用户在搜索框里连打两个字，第一次请求慢、第二次快，第二次先回来。
// 没有 alive 守卫的实现会在第一次请求最终返回时把旧结果盖上去——
// 界面显示的是上一个关键词的结果，而搜索框里是新关键词。
// 这个 bug 在真机上偶发、极难复现，只能靠测试挡住。
test('依赖变化后，先发出的慢请求不许覆盖后发出的结果', async () => {
  const resolvers: Record<number, (v: string) => void> = {}
  const load = (d: number) =>
    new Promise<string>((resolve) => {
      resolvers[d] = resolve
    })

  const { rerender } = render(<Probe dep={1} load={load} />)
  rerender(<Probe dep={2} load={load} />)

  // 第二次（dep=2）先返回
  await waitFor(() => expect(resolvers[2]).toBeDefined())
  resolvers[2]('v2')
  await waitFor(() => expect(screen.getByTestId('out').textContent).toBe('data:v2'))

  // 第一次（dep=1）后返回，必须被丢弃
  resolvers[1]('v1')
  await new Promise((r) => setTimeout(r, 20))
  expect(screen.getByTestId('out').textContent).toBe('data:v2')
})
```

- [ ] **Step 3: 运行，确认失败**

```bash
cd web && npm test -- useResource
```

- [ ] **Step 4: 实现 `web/src/lib/useResource.ts`**

```ts
import { useCallback, useEffect, useState } from 'react'

/** errorMessage 把任意抛出物转成能显示给人看的字符串。 */
export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : '请求失败'
}

interface Resource<T> {
  data: T | null
  loading: boolean
  error: string
  /** reload 重新拉取，用于写操作之后刷新列表。 */
  reload: () => void
}

/**
 * useResource 是五个页面共用的"拉数据"钩子。
 *
 * alive 守卫不是可有可无的卫生措施：依赖变化（翻页、改搜索词）会连续发出
 * 多个请求，网络乱序时先发的可能后到。没有守卫的话，界面会显示上一个
 * 关键词的结果，而输入框里是新关键词——真机上偶发且难复现。
 */
export function useResource<T>(load: () => Promise<T>, deps: unknown[]): Resource<T> {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [nonce, setNonce] = useState(0)

  useEffect(() => {
    let alive = true
    setLoading(true)
    setError('')
    load()
      .then((d) => {
        if (!alive) return
        setData(d)
        setLoading(false)
      })
      .catch((e) => {
        if (!alive) return
        setError(errorMessage(e))
        setLoading(false)
      })
    return () => {
      alive = false
    }
    // load 每次渲染都是新函数，不能进依赖数组；由调用方通过 deps 声明。
  }, [...deps, nonce]) // eslint-disable-line react-hooks/exhaustive-deps

  const reload = useCallback(() => setNonce((n) => n + 1), [])
  return { data, loading, error, reload }
}
```

- [ ] **Step 5: 运行测试确认通过**

```bash
cd web && npm test -- useResource
```

- [ ] **Step 6: 写应用列表页 `web/src/pages/Applications.tsx`**

```tsx
import { useState } from 'react'
import { Link } from 'react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import type { Application, CreateApplicationResponse } from '@/lib/types'

const createSchema = z.object({
  name: z.string().min(1, '请输入应用名称'),
  slug: z
    .string()
    .min(1, '请输入 slug')
    .regex(/^[a-z0-9][a-z0-9-]*$/, 'slug 只能用小写字母、数字和连字符，且不能以连字符开头'),
})
type CreateValues = z.infer<typeof createSchema>

export default function Applications() {
  const apps = useResource(() => api.get<Application[]>('/applications'), [])
  const [creating, setCreating] = useState(false)
  const [newSecret, setNewSecret] = useState<CreateApplicationResponse | null>(null)

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">应用</h1>
        <Button onClick={() => setCreating(true)}>新建应用</Button>
      </div>

      {apps.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {apps.error && <p className="text-sm text-destructive">{apps.error}</p>}

      {apps.data && (
        <div className="overflow-x-auto rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>slug</TableHead>
                <TableHead>appId</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>创建时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {apps.data.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-muted-foreground">
                    还没有应用
                  </TableCell>
                </TableRow>
              )}
              {apps.data.map((a) => (
                <TableRow key={a.id}>
                  <TableCell>
                    <Link to={`/applications/${a.id}`} className="font-medium underline-offset-4 hover:underline">
                      {a.name}
                    </Link>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{a.slug}</TableCell>
                  <TableCell className="font-mono text-xs">{a.appId}</TableCell>
                  <TableCell>
                    <Badge variant={a.status === 'ACTIVE' ? 'default' : 'secondary'}>
                      {a.status === 'ACTIVE' ? '启用' : '停用'}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{formatTime(a.createdAt)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <CreateDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(res) => {
          setCreating(false)
          setNewSecret(res)
          apps.reload()
        }}
      />

      <SecretDialog value={newSecret} onClose={() => setNewSecret(null)} />
    </div>
  )
}

function CreateDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (res: CreateApplicationResponse) => void
}) {
  const { register, handleSubmit, formState, reset } = useForm<CreateValues>({
    resolver: zodResolver(createSchema),
    defaultValues: { name: '', slug: '' },
  })

  async function onSubmit(v: CreateValues) {
    try {
      const res = await api.post<CreateApplicationResponse>('/applications', v)
      reset()
      onCreated(res)
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建应用</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="name">名称</Label>
            <Input id="name" {...register('name')} />
            {formState.errors.name && <p className="text-sm text-destructive">{formState.errors.name.message}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="slug">slug</Label>
            <Input id="slug" placeholder="my-app" {...register('slug')} />
            <p className="text-xs text-muted-foreground">创建后不可修改。</p>
            {formState.errors.slug && <p className="text-sm text-destructive">{formState.errors.slug.message}</p>}
          </div>
          <DialogFooter>
            <Button type="submit" disabled={formState.isSubmitting}>创建</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * SecretDialog 展示刚创建出的明文 appSecret。
 *
 * 后端只在创建响应里返回这一次，之后无法读回（库里只有 bcrypt 哈希）。
 * 所以这个弹窗必须说清楚"关掉就没了"，否则用户会以为随时能回来看。
 */
function SecretDialog({ value, onClose }: { value: CreateApplicationResponse | null; onClose: () => void }) {
  return (
    <Dialog open={value !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>应用已创建</DialogTitle>
        </DialogHeader>
        {value && (
          <div className="space-y-3">
            <div className="space-y-1">
              <Label>appId</Label>
              <div className="rounded-md border bg-muted/40 p-2 font-mono text-sm break-all">
                {value.application.appId}
              </div>
            </div>
            <div className="space-y-1">
              <Label>appSecret</Label>
              <div className="rounded-md border bg-muted/40 p-2 font-mono text-sm break-all">{value.appSecret}</div>
            </div>
            <p className="text-sm text-destructive">
              appSecret 只显示这一次，关闭后无法再查看。请立刻复制并妥善保存。
            </p>
          </div>
        )}
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              if (value) void navigator.clipboard?.writeText(value.appSecret).then(() => toast.success('已复制'))
            }}
          >
            复制 appSecret
          </Button>
          <Button onClick={onClose}>我已保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** formatTime 把后端的毫秒时间戳转成本地时间字符串。 */
export function formatTime(ms: number): string {
  if (!ms) return '-'
  return new Date(ms).toLocaleString()
}
```

- [ ] **Step 7: 写应用详情页 `web/src/pages/ApplicationDetail.tsx`**

本任务只做「基本信息」与「会话策略」两个页签，「登录方式」页签在 Task 7 填。

```tsx
import { useParams } from 'react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import type { Application, SessionPolicy } from '@/lib/types'

export default function ApplicationDetail() {
  const { id = '' } = useParams()
  const app = useResource(() => api.get<Application>(`/applications/${id}`), [id])

  if (app.loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (app.error) return <p className="text-sm text-destructive">{app.error}</p>
  if (!app.data) return null

  const a = app.data

  async function toggleStatus() {
    const next = a.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE'
    try {
      await api.patch(`/applications/${id}/status`, { status: next })
      toast.success(next === 'ACTIVE' ? '已启用' : '已停用')
      app.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">{a.name}</h1>
        <Badge variant={a.status === 'ACTIVE' ? 'default' : 'secondary'}>
          {a.status === 'ACTIVE' ? '启用' : '停用'}
        </Badge>
        <div className="flex-1" />
        <Button variant={a.status === 'ACTIVE' ? 'destructive' : 'default'} onClick={() => void toggleStatus()}>
          {a.status === 'ACTIVE' ? '停用应用' : '启用应用'}
        </Button>
      </div>

      {a.status === 'DISABLED' && (
        <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm">
          该应用已停用：新的登录会被拒绝，SDK 的下一次回源校验也会被拒绝。
          已签发的 token 最多还能在各接入方本地缓存里存活 {a.session.tokenCacheTtlSeconds} 秒。
        </p>
      )}

      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">基本信息</TabsTrigger>
          <TabsTrigger value="session">会话策略</TabsTrigger>
          <TabsTrigger value="connectors">登录方式</TabsTrigger>
        </TabsList>

        <TabsContent value="basic" className="pt-4">
          <BasicForm app={a} onSaved={app.reload} />
        </TabsContent>

        <TabsContent value="session" className="pt-4">
          <SessionForm app={a} onSaved={app.reload} />
        </TabsContent>

        <TabsContent value="connectors" className="pt-4">
          {/* Task 7 在这里挂 <ConnectorsPanel appId={id} /> */}
          <p className="text-sm text-muted-foreground">登录方式配置</p>
        </TabsContent>
      </Tabs>
    </div>
  )
}

const basicSchema = z.object({
  name: z.string().min(1, '请输入应用名称'),
  cookieDomain: z.string(),
})
type BasicValues = z.infer<typeof basicSchema>

function BasicForm({ app, onSaved }: { app: Application; onSaved: () => void }) {
  const { register, handleSubmit, formState } = useForm<BasicValues>({
    resolver: zodResolver(basicSchema),
    defaultValues: { name: app.name, cookieDomain: app.cookieDomain },
  })

  async function onSubmit(v: BasicValues) {
    try {
      await api.patch(`/applications/${app.id}`, v)
      toast.success('已保存')
      onSaved()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Card>
      <CardContent className="pt-6">
        <form onSubmit={handleSubmit(onSubmit)} className="max-w-md space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="name">名称</Label>
            <Input id="name" {...register('name')} />
            {formState.errors.name && <p className="text-sm text-destructive">{formState.errors.name.message}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="cookieDomain">Cookie 作用域</Label>
            <Input id="cookieDomain" placeholder="example.com" {...register('cookieDomain')} />
            <p className="text-xs text-muted-foreground">留空表示不限定。</p>
          </div>
          <div className="space-y-1 text-sm text-muted-foreground">
            <div>slug：<span className="font-mono">{app.slug}</span>（创建后不可修改）</div>
            <div>appId：<span className="font-mono">{app.appId}</span></div>
          </div>
          <Button type="submit" disabled={formState.isSubmitting}>保存</Button>
        </form>
      </CardContent>
    </Card>
  )
}

/**
 * 会话策略的校验规则镜像自后端 domain.SessionPolicy.Validate。
 *
 * 这里做校验只为让用户当场看到问题，**后端始终是权威**：两边一旦不一致，
 * 以后端返回的错误为准（onSubmit 的 catch 会把它显示出来）。绝不能因为
 * 前端拦住了就认为后端可以不校验。
 */
const sessionSchema = z
  .object({
    idleTimeoutSeconds: z.coerce.number().int().positive('必须大于 0'),
    idleTimeoutMobileSeconds: z.coerce.number().int().min(0, '不能为负'),
    maxLifetimeSeconds: z.coerce.number().int().positive('必须大于 0'),
    rotateIntervalSeconds: z.coerce.number().int().positive('必须大于 0'),
    extendIntervalSeconds: z.coerce.number().int().positive('必须大于 0'),
    tokenCacheTtlSeconds: z.coerce.number().int().positive('必须大于 0'),
  })
  .refine((p) => p.rotateIntervalSeconds <= p.maxLifetimeSeconds, {
    path: ['rotateIntervalSeconds'],
    message: '轮换间隔不能大于绝对上限',
  })
  .refine((p) => p.extendIntervalSeconds < p.idleTimeoutSeconds, {
    path: ['extendIntervalSeconds'],
    message: '延期间隔必须小于空闲超时，否则用户会因为"少延"而意外掉线',
  })
  .refine((p) => p.tokenCacheTtlSeconds <= p.idleTimeoutSeconds, {
    path: ['tokenCacheTtlSeconds'],
    message: '缓存窗口不能大于空闲超时，否则 token 过期后仍可能被 SDK 放行',
  })

const sessionFields: { key: keyof SessionPolicy; label: string; help?: string }[] = [
  { key: 'idleTimeoutSeconds', label: '空闲超时（秒）', help: '桌面端多久不活动就掉线' },
  { key: 'idleTimeoutMobileSeconds', label: '移动端空闲超时（秒）', help: '填 0 表示与桌面端相同' },
  { key: 'maxLifetimeSeconds', label: '绝对上限（秒）', help: '不论是否活跃，超过就必须重新登录' },
  { key: 'rotateIntervalSeconds', label: '轮换间隔（秒）' },
  { key: 'extendIntervalSeconds', label: '延期间隔（秒）', help: '降频用，避免每次请求都写 Redis' },
  { key: 'tokenCacheTtlSeconds', label: 'SDK 缓存窗口（秒）', help: '接入方本地缓存 token 校验结果的时长' },
]

function SessionForm({ app, onSaved }: { app: Application; onSaved: () => void }) {
  const { register, handleSubmit, formState } = useForm({
    resolver: zodResolver(sessionSchema),
    defaultValues: app.session,
  })

  async function onSubmit(v: SessionPolicy) {
    try {
      // 全量替换：后端 decodeJSON 开了 DisallowUnknownFields 且六项都必填，
      // 所以这里必须把六个字段一次性全发出去（路由用的是 PUT 不是 PATCH）。
      await api.put(`/applications/${app.id}/session`, v)
      toast.success('已保存')
      onSaved()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">会话策略</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit(onSubmit as never)} className="max-w-md space-y-4" noValidate>
          {sessionFields.map((f) => (
            <div key={f.key} className="space-y-2">
              <Label htmlFor={f.key}>{f.label}</Label>
              <Input id={f.key} type="number" {...register(f.key)} />
              {f.help && <p className="text-xs text-muted-foreground">{f.help}</p>}
              {formState.errors[f.key] && (
                <p className="text-sm text-destructive">{String(formState.errors[f.key]?.message)}</p>
              )}
            </div>
          ))}
          <Button type="submit" disabled={formState.isSubmitting}>保存</Button>
        </form>
      </CardContent>
    </Card>
  )
}
```

> **实现者注意：** `z.coerce.number()` 是这里的关键——HTML `<input type="number">` 通过 react-hook-form 拿到的仍然是**字符串**。不 coerce 的话，提交上去的是 `"3600"` 而不是 `3600`，后端 `decodeJSON` 会因为类型不符直接 400，而错误信息只会说"请求体解析失败"，很难联想到是这个原因。
>
> 如果安装到的 zod 版本里 `z.coerce` 的行为有变，改用 `z.number()` 配 `register(key, { valueAsNumber: true })` 达到同样效果，但**不要**两个都不做。

- [ ] **Step 8: 挂路由**

`web/src/routes.tsx` 的受保护区块里加：

```tsx
        <Route path="/applications" element={<Applications />} />
        <Route path="/applications/:id" element={<ApplicationDetail />} />
```

并在顶部 import 这两个页面。

- [ ] **Step 9: 构建与测试**

```bash
cd web && npm test && npm run build
```

- [ ] **Step 10: 人工验证**

后端 `go run ./cmd/fp`，前端 `npm run dev`，登录后：
1. 新建一个应用，确认弹窗展示 appId 与 appSecret，且提示"只显示这一次"
2. 关闭弹窗后列表里出现新应用
3. 点进详情，改名保存，返回列表确认名字变了
4. 回到详情的「会话策略」页签，确认六个字段填的是当前值；把「延期间隔」改成比「空闲超时」还大，点保存，应当**在前端就被拦下**并给出中文提示
5. 改一组合法值保存成功，刷新页面确认值被持久化
6. 点「停用应用」，确认徽标变成"停用"且出现那段说明文字；再点「启用应用」恢复

- [ ] **Step 11: 提交**

```bash
git add -A
git commit -m "feat(web): 应用列表、创建与详情（基本信息 + 会话策略）"
```

---

## Task 7: 动态表单引擎与登录方式配置页

**这是整个控制台唯一有真实逻辑的部分，也是本阶段的测试重点。**

后端每个 connector 用 `ConfigSchema() []domain.Field` 声明自己的可配置项，`GET /admin/api/connectors` 把它们一次性吐出来。控制台据此**动态渲染**表单——以后新增微信登录、扫码登录、OIDC，只要后端把 `ConfigSchema()` 写对，前端一行不用改。这条是设计文档里写死的目标，本任务是它的兑现点。

**载荷规则（一句话，测试按它写）：** 空字符串且非必填的字段从提交载荷里省略，让 connector 用自己代码里的默认值；其余字段一律带上。布尔字段永远带上——开关没有"空"状态，界面显示什么就必须提交什么，否则界面在撒谎。

**Files:**
- Create: `web/src/components/DynamicForm.tsx`
- Create: `web/src/components/DynamicForm.test.tsx`
- Create: `web/src/components/ConnectorsPanel.tsx`
- Modify: `web/src/pages/ApplicationDetail.tsx`（把 `connectors` 页签接上）

**Interfaces:**
- Produces: `<DynamicForm fields values onSubmit submitLabel? />`
- Consumes: `Field` / `ConnectorSchema` / `ConnectorConfig`（Task 6 的 types.ts）、`api`

---

- [ ] **Step 1: 写失败的测试 `web/src/components/DynamicForm.test.tsx`**

```tsx
import { test, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { DynamicForm } from './DynamicForm'
import type { Field } from '@/lib/types'

function submitForm() {
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
}

test('按 schema 渲染出每个字段', () => {
  const fields: Field[] = [
    { key: 'apiKey', label: 'API 密钥', type: 'string' },
    { key: 'enabled', label: '开启', type: 'bool' },
  ]
  render(<DynamicForm fields={fields} values={{}} onSubmit={vi.fn()} />)

  expect(screen.getByLabelText('API 密钥')).toBeDefined()
  expect(screen.getByLabelText('开启')).toBeDefined()
})

test('展示字段的 help 文案', () => {
  const fields: Field[] = [
    { key: 'autoRegister', label: '自动注册', type: 'bool', help: '关闭后未注册手机号无法登录' },
  ]
  render(<DynamicForm fields={fields} values={{}} onSubmit={vi.fn()} />)
  expect(screen.getByText('关闭后未注册手机号无法登录')).toBeDefined()
})

test('必填字段为空时不提交并给出提示', async () => {
  const onSubmit = vi.fn()
  const fields: Field[] = [{ key: 'apiKey', label: 'API 密钥', type: 'string', required: true }]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  submitForm()
  await waitFor(() => expect(screen.getByText(/必填|不能为空|请填写/)).toBeDefined())
  expect(onSubmit).not.toHaveBeenCalled()
})

// 【辨别力】int 字段必须提交 number 而不是 string。
//
// <input type="number"> 经 react-hook-form 拿到的是字符串 "30"。直接提交的话
// 后端 decodeJSON 会因为类型不符返回 400，而错误消息只说"请求体解析失败"，
// 排查时几乎不可能联想到是这里。断言必须同时检查值和类型——只写
// toEqual({ttl: 30}) 的话，"30" 和 30 在某些断言下会被判等而放过去。
test('int 字段提交的是数字而不是字符串', async () => {
  const onSubmit = vi.fn()
  const fields: Field[] = [{ key: 'ttl', label: '有效期', type: 'int' }]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  fireEvent.change(screen.getByLabelText('有效期'), { target: { value: '30' } })
  submitForm()

  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  const payload = onSubmit.mock.calls[0][0]
  expect(payload.ttl).toBe(30)
  expect(typeof payload.ttl).toBe('number')
})

// 【辨别力】schema 里的 default 必须体现在界面初始状态上。
//
// password 与 sms_code 的开关默认值都是 true。若实现用 defaultValues: {}
// 起手，开关会显示成"关"，而实际生效的是 connector 代码里的 true——
// 界面在撒谎，管理员据此做的判断全是错的。
test('未配置过时采用 schema 声明的默认值', async () => {
  const onSubmit = vi.fn()
  const fields: Field[] = [
    { key: 'allowPhone', label: '允许手机号', type: 'bool', default: true },
    { key: 'allowEmail', label: '允许邮箱', type: 'bool', default: false },
  ]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  expect(screen.getByLabelText('允许手机号').getAttribute('aria-checked')).toBe('true')
  expect(screen.getByLabelText('允许邮箱').getAttribute('aria-checked')).toBe('false')

  submitForm()
  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  expect(onSubmit.mock.calls[0][0]).toEqual({ allowPhone: true, allowEmail: false })
})

// 【辨别力】已有配置必须覆盖 schema 的 default。
//
// 顺序写反的话，任何被管理员显式关掉的开关，每次打开页面都会显示成"开"。
test('已有配置优先于 schema 默认值', () => {
  const fields: Field[] = [{ key: 'allowPhone', label: '允许手机号', type: 'bool', default: true }]
  render(<DynamicForm fields={fields} values={{ allowPhone: false }} onSubmit={vi.fn()} />)

  expect(screen.getByLabelText('允许手机号').getAttribute('aria-checked')).toBe('false')
})

test('secret 字段渲染成密码输入框', () => {
  const fields: Field[] = [{ key: 'sk', label: '密钥', type: 'secret' }]
  render(<DynamicForm fields={fields} values={{}} onSubmit={vi.fn()} />)
  expect(screen.getByLabelText('密钥').getAttribute('type')).toBe('password')
})

// 载荷规则：非必填的空字符串省略，让 connector 用自己代码里的默认值。
test('非必填的空字符串不进入提交载荷', async () => {
  const onSubmit = vi.fn()
  const fields: Field[] = [
    { key: 'note', label: '备注', type: 'string' },
    { key: 'name', label: '名称', type: 'string' },
  ]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'x' } })
  submitForm()

  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  expect(onSubmit.mock.calls[0][0]).toEqual({ name: 'x' })
})

// 【辨别力】未来后端加了新的 FieldType，前端不许白屏。
//
// 这个控制台的整个卖点就是"后端加登录方式，前端零改动"。如果一个不认识的
// 类型会让页面崩掉，这个卖点就是假的——而且崩的是**已有的**配置页面，
// 连带把能正常配置的登录方式一起带走。
test('遇到不认识的字段类型时退化成文本框而不是崩溃', async () => {
  const onSubmit = vi.fn()
  const fields = [{ key: 'weird', label: '未来字段', type: 'duration' }] as unknown as Field[]
  render(<DynamicForm fields={fields} values={{}} onSubmit={onSubmit} />)

  const input = screen.getByLabelText('未来字段')
  expect(input.getAttribute('type')).toBe('text')
  fireEvent.change(input, { target: { value: '5m' } })
  submitForm()

  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  expect(onSubmit.mock.calls[0][0]).toEqual({ weird: '5m' })
})

test('没有任何字段时也能渲染并提交空配置', async () => {
  const onSubmit = vi.fn()
  render(<DynamicForm fields={[]} values={{}} onSubmit={onSubmit} />)

  submitForm()
  await waitFor(() => expect(onSubmit).toHaveBeenCalled())
  expect(onSubmit.mock.calls[0][0]).toEqual({})
})
```

- [ ] **Step 2: 运行，确认失败**

```bash
cd web && npm test -- DynamicForm
```

- [ ] **Step 3: 实现 `web/src/components/DynamicForm.tsx`**

```tsx
import { Controller, useForm } from 'react-hook-form'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import type { Field } from '@/lib/types'

type Values = Record<string, unknown>

interface Props {
  /** 后端 ConfigSchema() 声明的字段。 */
  fields: Field[]
  /** 该应用当前已保存的配置；未配置过时传 {}。 */
  values: Values
  onSubmit: (config: Values) => void | Promise<void>
  submitLabel?: string
}

/**
 * initialValue 决定一个字段的初始状态。
 *
 * 优先级：已保存的配置 > schema 声明的 default > 按类型兜底。
 * 顺序不能反——反了的话，任何被管理员显式关掉的开关，每次打开页面
 * 都会重新显示成"开"。
 */
function initialValue(f: Field, saved: Values): unknown {
  if (Object.prototype.hasOwnProperty.call(saved, f.key)) return saved[f.key]
  if (f.default !== undefined && f.default !== null) return f.default
  return f.type === 'bool' ? false : ''
}

/**
 * DynamicForm 按后端声明的字段元数据渲染配置表单。
 *
 * 存在的理由：新增一种登录方式时，后端写好 ConfigSchema() 就够了，
 * 前端不需要任何改动。所以这里**不许**出现任何针对具体 connector 的
 * 特判——一旦出现，这个组件就退化成了几个写死表单的集合。
 *
 * 约束：Field.Key 必须是简单标识符，不能含 `.` 或 `[`——react-hook-form
 * 会把它们解释成嵌套路径。后端所有 ConfigSchema 目前都满足。
 */
export function DynamicForm({ fields, values, onSubmit, submitLabel = '保存' }: Props) {
  const defaults: Values = {}
  for (const f of fields) defaults[f.key] = initialValue(f, values)

  const { register, control, handleSubmit, formState } = useForm<Values>({ defaultValues: defaults })

  function buildPayload(raw: Values): Values {
    const out: Values = {}
    for (const f of fields) {
      const v = raw[f.key]
      if (f.type === 'bool') {
        // 开关没有"空"状态：界面显示什么就提交什么，否则界面在撒谎。
        out[f.key] = Boolean(v)
        continue
      }
      if (f.type === 'int') {
        // <input type="number"> 给出来的是字符串。不转成 number 的话，
        // 后端 decodeJSON 会因类型不符返回一个看不出原因的 400。
        if (v === '' || v === undefined || v === null) continue
        out[f.key] = Number(v)
        continue
      }
      // string / secret / 未知类型
      if (v === '' || v === undefined || v === null) {
        // 非必填留空 → 省略该键，让 connector 用自己代码里的默认值。
        // 写成空串的话，会把 connector 的默认值覆盖成""。
        if (!f.required) continue
      }
      out[f.key] = v
    }
    return out
  }

  return (
    <form onSubmit={handleSubmit((raw) => onSubmit(buildPayload(raw)))} className="space-y-4" noValidate>
      {fields.map((f) => (
        <div key={f.key} className="space-y-2">
          {f.type === 'bool' ? (
            <div className="flex items-center gap-3">
              <Controller
                control={control}
                name={f.key}
                render={({ field }) => (
                  <Switch
                    id={f.key}
                    checked={Boolean(field.value)}
                    onCheckedChange={field.onChange}
                  />
                )}
              />
              <Label htmlFor={f.key}>{f.label}</Label>
            </div>
          ) : (
            <>
              <Label htmlFor={f.key}>
                {f.label}
                {f.required && <span className="ml-1 text-destructive">*</span>}
              </Label>
              <Input
                id={f.key}
                type={inputType(f.type)}
                {...register(f.key, {
                  required: f.required ? `${f.label}不能为空` : false,
                })}
              />
            </>
          )}
          {f.help && <p className="text-xs text-muted-foreground">{f.help}</p>}
          {formState.errors[f.key] && (
            <p className="text-sm text-destructive">{String(formState.errors[f.key]?.message)}</p>
          )}
        </div>
      ))}
      <Button type="submit" disabled={formState.isSubmitting}>
        {submitLabel}
      </Button>
    </form>
  )
}

/**
 * inputType 把后端的字段类型映射成 input 的 type。
 *
 * default 分支刻意退化成 text 而不是抛错：这个控制台的卖点就是"后端加
 * 登录方式、前端零改动"。将来后端加一种新 FieldType 时，旧版前端应当
 * 还能把它当字符串编辑，而不是整页崩掉——崩掉会连带把同一页上本来能
 * 正常配置的其他登录方式一起带走。
 */
function inputType(t: string): string {
  switch (t) {
    case 'int':
      return 'number'
    case 'secret':
      return 'password'
    default:
      return 'text'
  }
}
```

> **实现者注意（很可能需要微调）：** 上面用 `aria-checked` 断言开关状态，前提是 shadcn 的 `Switch` 渲染成带 `role="switch"` 的元素并同步 `aria-checked`。运行测试后如果断言对不上，**先去看 `src/components/ui/switch.tsx` 实际渲染出什么**，据此调整**测试的断言方式**（例如改用 `toHaveAttribute('data-state', 'checked')`），但**不要**为了让测试好写而把 Switch 换成原生 checkbox——那会让控制台的开关和其他地方长得不一样。同理，若 `<Label htmlFor>` 无法与 Switch 关联出可访问名，给 Switch 加 `aria-label={f.label}`。

- [ ] **Step 4: 运行测试确认通过**

```bash
cd web && npm test -- DynamicForm
```

十条测试必须全绿。任何一条为了让实现好写而被删改的，都要在提交信息里说明原因。

- [ ] **Step 5: 写 `web/src/components/ConnectorsPanel.tsx`**

```tsx
import { toast } from 'sonner'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { DynamicForm } from './DynamicForm'
import type { ConnectorConfig, ConnectorSchema } from '@/lib/types'

/** 登录方式类型的中文名。认不出来的类型直接显示原始类型名，不阻塞。 */
const typeLabels: Record<string, string> = {
  password: '密码登录',
  sms_code: '短信验证码登录',
}

export default function ConnectorsPanel({ appId }: { appId: string }) {
  // 两份数据：全部已注册的登录方式（含字段元数据），以及本应用已保存的配置。
  // 注意 /applications/{id}/connectors 只返回**配置过的**，所以要以
  // /connectors 的清单为准做外连接——否则从没配过的登录方式压根不会显示。
  const schemas = useResource(() => api.get<ConnectorSchema[]>('/connectors'), [])
  const configs = useResource(() => api.get<ConnectorConfig[]>(`/applications/${appId}/connectors`), [appId])

  if (schemas.loading || configs.loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  const err = schemas.error || configs.error
  if (err) return <p className="text-sm text-destructive">{err}</p>
  if (!schemas.data || !configs.data) return null

  const byType = new Map(configs.data.map((c) => [c.type, c]))

  async function save(type: string, enabled: boolean, config: Record<string, unknown>) {
    try {
      await api.put(`/applications/${appId}/connectors/${type}`, { enabled, config })
      toast.success('已保存')
      configs.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-4">
      {schemas.data.map((s) => {
        const current = byType.get(s.type)
        const enabled = current?.enabled ?? false
        return (
          <Card key={s.type}>
            <CardHeader className="flex-row items-center gap-3 space-y-0">
              <CardTitle className="text-base">{typeLabels[s.type] ?? s.type}</CardTitle>
              <Badge variant="outline" className="font-mono text-xs">{s.type}</Badge>
              <div className="flex-1" />
              <div className="flex items-center gap-2">
                <Label htmlFor={`enabled-${s.type}`} className="text-sm font-normal">
                  {enabled ? '已启用' : '已关闭'}
                </Label>
                <Switch
                  id={`enabled-${s.type}`}
                  checked={enabled}
                  // 开关只改 enabled，配置原样带回去——SetConnector 是全量覆盖，
                  // 不带的话会把已保存的配置清空。
                  onCheckedChange={(v) => void save(s.type, v, current?.config ?? {})}
                />
              </div>
            </CardHeader>
            <CardContent>
              {s.fields.length === 0 ? (
                <p className="text-sm text-muted-foreground">这种登录方式没有可配置项。</p>
              ) : (
                <DynamicForm
                  // key 里带上配置的引用，保存后重新拉到的数据能重置表单默认值
                  key={JSON.stringify(current?.config ?? {})}
                  fields={s.fields}
                  values={current?.config ?? {}}
                  onSubmit={(config) => save(s.type, enabled, config)}
                />
              )}
            </CardContent>
          </Card>
        )
      })}
    </div>
  )
}
```

> **注意那个 `key={JSON.stringify(...)}`：** react-hook-form 的 `defaultValues` 只在首次挂载时生效。保存成功后 `configs.reload()` 拿到新数据，但表单不会自动重置——换 key 强制重挂是最省事的正确做法。少了它，保存后界面看着没问题，但如果后端对配置做了规范化（比如省略的键被填上默认值），界面显示的仍是提交前的旧状态。

- [ ] **Step 6: 接到应用详情页**

`web/src/pages/ApplicationDetail.tsx` 里，把 connectors 页签的占位换掉：

```tsx
        <TabsContent value="connectors" className="pt-4">
          <ConnectorsPanel appId={id} />
        </TabsContent>
```

并在顶部加 `import ConnectorsPanel from '@/components/ConnectorsPanel'`。

- [ ] **Step 7: 构建与测试**

```bash
cd web && npm test && npm run build
```

- [ ] **Step 8: 人工验证——包括"新增登录方式前端零改动"这条**

后端 `go run ./cmd/fp`，前端 `npm run dev`，进入某个应用的「登录方式」页签：

1. 应当看到两张卡片：密码登录（三个开关：允许手机号/用户名/邮箱）、短信验证码登录（一个开关：首次登录自动注册）
2. 三个开关的初始状态必须与 `internal/connector/password.go` 里 `ConfigSchema()` 声明的 default 一致：允许手机号=开、允许用户名=开、允许邮箱=关
3. 把「允许邮箱」打开并保存，刷新页面确认状态被持久化
4. 打开右上角的启用开关，刷新确认持久化
5. **零改动验证：** 临时在 `internal/connector/smscode.go` 的 `ConfigSchema()` 里加一个字段：

```go
		{Key: "codeLength", Label: "验证码位数", Type: domain.FieldTypeInt, Default: 6, Help: "临时验证用"},
```

   重启后端、**不动前端任何代码**、刷新页面。短信卡片上应当自动多出一个数字输入框，初始值 6。填 8 保存，应当成功。改回 `"codeLength": "abc"` 之类是做不到的（输入框是 number），但可以验证 Task 2 的校验：用 curl 发一个未知键，应当返回 400。验证完把这个字段**删掉**，不要提交进仓库。

- [ ] **Step 9: 提交**

```bash
git add -A
git commit -m "feat(web): 动态表单引擎与登录方式配置页"
```

---

## Task 8: 用户列表（分页、搜索、状态筛选）

**Files:**
- Create: `web/src/lib/query.ts`
- Create: `web/src/lib/query.test.ts`
- Create: `web/src/components/Pagination.tsx`
- Create: `web/src/pages/Users.tsx`
- Modify: `web/src/routes.tsx`

**Interfaces:**
- Produces: `buildUserQuery(p): string`、`<Pagination page total pageSize onPage />`
- Consumes: `api`、`useResource`、`UserListResponse`

**后端接口形状：** `GET /admin/api/users?keyword=&status=&limit=&offset=` → `{ items: User[], total: number }`。注意是 **offset 不是 page**，这是下面那条辨别力测试要挡的东西。

---

- [ ] **Step 1: 写失败的测试 `web/src/lib/query.test.ts`**

```ts
import { test, expect } from 'vitest'
import { buildUserQuery, PAGE_SIZE } from './query'

// 【辨别力】后端要的是 offset，不是页码。
//
// 第 1 页时 offset 和 page-1 都等于 0，两种实现都对；只有翻到第 2 页
// 之后才分得出来。一个把页码当 offset 发出去的实现，表现是"翻到第 2 页
// 只往后挪了一条记录"——看起来像后端分页坏了，实际错在这里。
// 所以这条测试**必须**断言第 3 页，不能只测第 1 页。
test('分页参数发的是 offset 而不是页码', () => {
  expect(new URLSearchParams(buildUserQuery({ page: 1 })).get('offset')).toBe('0')
  expect(new URLSearchParams(buildUserQuery({ page: 2 })).get('offset')).toBe(String(PAGE_SIZE))
  expect(new URLSearchParams(buildUserQuery({ page: 3 })).get('offset')).toBe(String(PAGE_SIZE * 2))
})

test('每页条数随请求发出', () => {
  expect(new URLSearchParams(buildUserQuery({ page: 1 })).get('limit')).toBe(String(PAGE_SIZE))
})

test('关键词与状态为空时不发这两个参数', () => {
  const q = new URLSearchParams(buildUserQuery({ page: 1 }))
  expect(q.has('keyword')).toBe(false)
  expect(q.has('status')).toBe(false)
})

test('关键词与状态非空时原样带上', () => {
  const q = new URLSearchParams(buildUserQuery({ page: 1, keyword: '138', status: 'FROZEN' }))
  expect(q.get('keyword')).toBe('138')
  expect(q.get('status')).toBe('FROZEN')
})

// 关键词里可能有 & = # 之类，必须转义，否则会拼出一个畸形查询串。
test('关键词做 URL 转义', () => {
  const raw = buildUserQuery({ page: 1, keyword: 'a&b=c' })
  expect(raw).not.toContain('a&b=c')
  expect(new URLSearchParams(raw).get('keyword')).toBe('a&b=c')
})

// 页码越界时兜底到第 1 页，不要发出负的 offset。
test('页码小于 1 时按第 1 页处理', () => {
  expect(new URLSearchParams(buildUserQuery({ page: 0 })).get('offset')).toBe('0')
  expect(new URLSearchParams(buildUserQuery({ page: -3 })).get('offset')).toBe('0')
})
```

- [ ] **Step 2: 运行，确认失败**

```bash
cd web && npm test -- query
```

- [ ] **Step 3: 实现 `web/src/lib/query.ts`**

```ts
/** 用户列表每页条数。 */
export const PAGE_SIZE = 20

export interface UserQuery {
  /** 从 1 开始的页码。 */
  page: number
  keyword?: string
  status?: string
}

/**
 * buildUserQuery 拼出 /users 的查询串。
 *
 * 后端收的是 limit/offset，不是页码——两者只在第 2 页之后才看得出差别，
 * 所以这里单独抽成函数并配了测试，而不是在组件里顺手算。
 */
export function buildUserQuery({ page, keyword, status }: UserQuery): string {
  const p = Math.max(1, Math.floor(page) || 1)
  const q = new URLSearchParams()
  q.set('limit', String(PAGE_SIZE))
  q.set('offset', String((p - 1) * PAGE_SIZE))
  if (keyword) q.set('keyword', keyword)
  if (status) q.set('status', status)
  return q.toString()
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd web && npm test -- query
```

- [ ] **Step 5: 写分页组件 `web/src/components/Pagination.tsx`**

```tsx
import { Button } from '@/components/ui/button'

interface Props {
  page: number
  total: number
  pageSize: number
  onPage: (p: number) => void
}

/**
 * 极简分页：上一页 / 下一页 / 第 x 页共 y 页。
 *
 * 没做页码跳转按钮——用户列表主要靠搜索定位，翻很多页的场景不存在。
 * 真需要时再加。
 */
export default function Pagination({ page, total, pageSize, onPage }: Props) {
  const pages = Math.max(1, Math.ceil(total / pageSize))
  return (
    <div className="flex items-center justify-end gap-3 text-sm">
      <span className="text-muted-foreground">
        共 {total} 条，第 {page} / {pages} 页
      </span>
      <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onPage(page - 1)}>
        上一页
      </Button>
      <Button variant="outline" size="sm" disabled={page >= pages} onClick={() => onPage(page + 1)}>
        下一页
      </Button>
    </div>
  )
}
```

- [ ] **Step 6: 写用户列表页 `web/src/pages/Users.tsx`**

```tsx
import { Link, useSearchParams } from 'react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import Pagination from '@/components/Pagination'
import { api } from '@/lib/api'
import { useResource } from '@/lib/useResource'
import { buildUserQuery, PAGE_SIZE } from '@/lib/query'
import { formatTime } from '@/pages/Applications'
import type { UserListResponse, UserStatus } from '@/lib/types'

export const statusLabels: Record<UserStatus, string> = {
  ACTIVE: '正常',
  FROZEN: '已冻结',
  PENDING_DELETE: '注销保护期',
  DELETED: '已注销',
}

const ALL = '__all__'

export default function Users() {
  // 查询条件放进 URL：刷新、后退、把链接发给同事，都能回到同一个视图。
  const [sp, setSp] = useSearchParams()
  const page = Number(sp.get('page') ?? '1')
  const keyword = sp.get('keyword') ?? ''
  const status = sp.get('status') ?? ''

  const list = useResource(
    () => api.get<UserListResponse>(`/users?${buildUserQuery({ page, keyword, status })}`),
    [page, keyword, status],
  )

  function update(next: Record<string, string>) {
    const q = new URLSearchParams(sp)
    for (const [k, v] of Object.entries(next)) {
      if (v) q.set(k, v)
      else q.delete(k)
    }
    setSp(q)
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">用户</h1>

      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          const v = new FormData(e.currentTarget).get('keyword')
          // 改搜索条件必须回到第 1 页：停在第 5 页搜一个只有 3 条结果的
          // 关键词，会得到一张空表，看起来像"搜不到"。
          update({ keyword: String(v ?? ''), page: '' })
        }}
      >
        <Input name="keyword" defaultValue={keyword} placeholder="手机号 / 用户名 / 昵称" className="w-64" />
        <Button type="submit" variant="secondary">搜索</Button>

        <Select
          value={status || ALL}
          onValueChange={(v) => update({ status: v === ALL ? '' : v, page: '' })}
        >
          <SelectTrigger className="w-40"><SelectValue placeholder="全部状态" /></SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>全部状态</SelectItem>
            {(Object.keys(statusLabels) as UserStatus[]).map((s) => (
              <SelectItem key={s} value={s}>{statusLabels[s]}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </form>

      {list.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {list.error && <p className="text-sm text-destructive">{list.error}</p>}

      {list.data && (
        <>
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>昵称</TableHead>
                  <TableHead>登录标识</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>注册时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.data.items.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={4} className="text-center text-muted-foreground">没有匹配的用户</TableCell>
                  </TableRow>
                )}
                {list.data.items.map((u) => (
                  <TableRow key={u.id}>
                    <TableCell>
                      <Link to={`/users/${u.id}`} className="font-medium underline-offset-4 hover:underline">
                        {u.nickname || '（未设置）'}
                      </Link>
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {u.identities.map((i) => `${i.type}:${i.subject}`).join('  ') || '-'}
                    </TableCell>
                    <TableCell>
                      <Badge variant={u.status === 'ACTIVE' ? 'default' : 'secondary'}>
                        {statusLabels[u.status] ?? u.status}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground">{formatTime(u.createdAt)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          <Pagination
            page={Math.max(1, page)}
            total={list.data.total}
            pageSize={PAGE_SIZE}
            onPage={(p) => update({ page: String(p) })}
          />
        </>
      )}
    </div>
  )
}
```

> `<SelectItem value="">` 在 shadcn/base-ui 的 Select 里是非法的（空串被当成"未选择"），所以用 `__all__` 这个哨兵值代表"全部状态"，发请求前再转回空串。这不是洁癖——直接用空串会让下拉框在运行时报错。

- [ ] **Step 7: 挂路由**

```tsx
        <Route path="/users" element={<Users />} />
```

- [ ] **Step 8: 构建与测试**

```bash
cd web && npm test && npm run build
```

- [ ] **Step 9: 人工验证**

需要库里有至少 21 个用户才能验证翻页。用 demo 服务或直接连库造数据都行；实在没有就先跳过第 3 条，在 Task 10 的端到端验收里补。

1. 打开 `/users`，看到用户列表
2. 搜一个手机号片段，结果收窄；**地址栏出现 `?keyword=`**，刷新页面结果保持
3. 翻到第 2 页，确认看到的是第 21–40 条而不是第 2–21 条
4. 在第 2 页上改搜索词，确认自动回到第 1 页
5. 状态筛选选「已冻结」，确认只剩冻结用户

- [ ] **Step 10: 提交**

```bash
git add -A
git commit -m "feat(web): 用户列表，含分页、搜索与状态筛选"
```

---

## Task 9: 用户详情

**Files:**
- Create: `web/src/pages/UserDetail.tsx`
- Modify: `web/src/routes.tsx`

**用到的后端接口：**

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/users/{id}` | 基本信息与身份列表 |
| PATCH | `/users/{id}/status` | 冻结 / 解冻 |
| PUT | `/users/{id}/password` | 管理员重置密码（204） |
| GET | `/users/{id}/sessions` | 在线设备 |
| DELETE | `/users/{id}/sessions/{sid}` | 踢单个设备 |
| DELETE | `/users/{id}/sessions` | 踢全部设备 |
| GET | `/users/{id}/login-logs?limit=` | 登录日志 |

**状态机（来自 `domain.CanTransitionUserStatus`）：** ACTIVE ↔ FROZEN，ACTIVE → PENDING_DELETE。界面只提供 ACTIVE ↔ FROZEN 的切换——注销流程属于终端用户自助，不是管理员该在这里点的。

---

- [ ] **Step 1: 写页面 `web/src/pages/UserDetail.tsx`**

```tsx
import { useState } from 'react'
import { useParams } from 'react-router'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatTime } from '@/pages/Applications'
import { statusLabels } from '@/pages/Users'
import type { LoginLog, User, UserSession } from '@/lib/types'

export default function UserDetail() {
  const { id = '' } = useParams()
  const user = useResource(() => api.get<User>(`/users/${id}`), [id])
  const sessions = useResource(() => api.get<UserSession[]>(`/users/${id}/sessions`), [id])
  const logs = useResource(() => api.get<LoginLog[]>(`/users/${id}/login-logs?limit=50`), [id])
  const [resetting, setResetting] = useState(false)

  if (user.loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (user.error) return <p className="text-sm text-destructive">{user.error}</p>
  if (!user.data) return null
  const u = user.data

  async function toggleFrozen() {
    const next = u.status === 'ACTIVE' ? 'FROZEN' : 'ACTIVE'
    try {
      await api.patch(`/users/${id}/status`, { status: next })
      toast.success(next === 'FROZEN' ? '已冻结' : '已解冻')
      user.reload()
      // 冻结会连带撤销全部会话（后端 AccountService 里的安全耦合），
      // 所以在线设备列表也要刷新，否则界面上还挂着已经失效的设备。
      sessions.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  async function revokeOne(sid: string) {
    try {
      await api.del(`/users/${id}/sessions/${sid}`)
      toast.success('已下线该设备')
      sessions.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  async function revokeAll() {
    try {
      const res = await api.del<{ revoked: number }>(`/users/${id}/sessions`)
      toast.success(`已下线 ${res.revoked} 个设备`)
      sessions.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">{u.nickname || '（未设置昵称）'}</h1>
        <Badge variant={u.status === 'ACTIVE' ? 'default' : 'secondary'}>
          {statusLabels[u.status] ?? u.status}
        </Badge>
        <div className="flex-1" />
        <Button variant="outline" onClick={() => setResetting(true)}>重置密码</Button>
        {(u.status === 'ACTIVE' || u.status === 'FROZEN') && (
          <Button variant={u.status === 'ACTIVE' ? 'destructive' : 'default'} onClick={() => void toggleFrozen()}>
            {u.status === 'ACTIVE' ? '冻结账号' : '解除冻结'}
          </Button>
        )}
      </div>

      <Card>
        <CardHeader><CardTitle className="text-base">身份</CardTitle></CardHeader>
        <CardContent className="space-y-2 text-sm">
          <div className="text-muted-foreground">用户 ID：<span className="font-mono">{u.id}</span></div>
          <div className="text-muted-foreground">注册时间：{formatTime(u.createdAt)}</div>
          <div className="text-muted-foreground">是否设过密码：{u.hasPassword ? '是' : '否'}</div>
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow><TableHead>类型</TableHead><TableHead>标识</TableHead><TableHead>最近登录</TableHead></TableRow>
              </TableHeader>
              <TableBody>
                {u.identities.length === 0 && (
                  <TableRow><TableCell colSpan={3} className="text-center text-muted-foreground">没有已绑定的身份</TableCell></TableRow>
                )}
                {u.identities.map((i) => (
                  <TableRow key={`${i.type}:${i.subject}`}>
                    <TableCell className="font-mono text-xs">{i.type}</TableCell>
                    <TableCell className="font-mono text-xs">{i.subject}</TableCell>
                    <TableCell className="text-muted-foreground">{formatTime(i.lastLoginAt)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center space-y-0">
          <CardTitle className="text-base">在线设备</CardTitle>
          <div className="flex-1" />
          <Button
            variant="outline"
            size="sm"
            disabled={!sessions.data || sessions.data.length === 0}
            onClick={() => void revokeAll()}
          >
            全部下线
          </Button>
        </CardHeader>
        <CardContent>
          {sessions.error && <p className="text-sm text-destructive">{sessions.error}</p>}
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>端</TableHead><TableHead>IP</TableHead><TableHead>User-Agent</TableHead>
                  <TableHead>首次认证</TableHead><TableHead>空闲到期</TableHead><TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {sessions.data?.length === 0 && (
                  <TableRow><TableCell colSpan={6} className="text-center text-muted-foreground">没有在线设备</TableCell></TableRow>
                )}
                {sessions.data?.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell>{s.mobile ? '移动端' : '桌面端'}</TableCell>
                    <TableCell className="font-mono text-xs">{s.ip || '-'}</TableCell>
                    <TableCell className="max-w-xs truncate text-xs text-muted-foreground" title={s.ua}>{s.ua || '-'}</TableCell>
                    <TableCell className="text-muted-foreground">{formatTime(s.firstAuthAt)}</TableCell>
                    <TableCell className="text-muted-foreground">{formatTime(s.idleExpiresAt)}</TableCell>
                    <TableCell>
                      <Button variant="ghost" size="sm" onClick={() => void revokeOne(s.id)}>下线</Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="text-base">登录日志（最近 50 条）</CardTitle></CardHeader>
        <CardContent>
          {logs.error && <p className="text-sm text-destructive">{logs.error}</p>}
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>时间</TableHead><TableHead>事件</TableHead><TableHead>标识</TableHead>
                  <TableHead>结果</TableHead><TableHead>IP</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {logs.data?.length === 0 && (
                  <TableRow><TableCell colSpan={5} className="text-center text-muted-foreground">没有登录记录</TableCell></TableRow>
                )}
                {logs.data?.map((l) => (
                  <TableRow key={l.id}>
                    <TableCell className="text-muted-foreground">{formatTime(l.createdAt)}</TableCell>
                    <TableCell className="font-mono text-xs">{l.event}</TableCell>
                    {/* subject 是后端脱敏过的（service.MaskSubject），这里原样显示 */}
                    <TableCell className="font-mono text-xs">{l.identityType}:{l.subject}</TableCell>
                    <TableCell>
                      {l.success ? (
                        <Badge variant="default">成功</Badge>
                      ) : (
                        <Badge variant="destructive" title={l.reason}>失败</Badge>
                      )}
                    </TableCell>
                    <TableCell className="font-mono text-xs">{l.ip || '-'}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <ResetPasswordDialog
        userId={id}
        open={resetting}
        onOpenChange={setResetting}
        onDone={() => {
          setResetting(false)
          user.reload()
          // 重置密码会连带撤销全部会话（后端的安全耦合），设备列表必须刷新。
          sessions.reload()
        }}
      />
    </div>
  )
}

function ResetPasswordDialog({
  userId, open, onOpenChange, onDone,
}: {
  userId: string
  open: boolean
  onOpenChange: (v: boolean) => void
  onDone: () => void
}) {
  const [pwd, setPwd] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit() {
    setBusy(true)
    try {
      // 返回 204 无响应体——api.put 已经处理，不要在这里 .json()
      await api.put(`/users/${userId}/password`, { password: pwd })
      toast.success('密码已重置，该用户全部设备已下线')
      setPwd('')
      onDone()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>重置密码</DialogTitle></DialogHeader>
        <div className="space-y-2">
          <Label htmlFor="newpwd">新密码</Label>
          <Input id="newpwd" type="password" value={pwd} onChange={(e) => setPwd(e.target.value)} />
          <p className="text-sm text-muted-foreground">
            重置后该用户的全部设备会立即下线，需要用新密码重新登录。
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>取消</Button>
          <Button disabled={busy || pwd === ''} onClick={() => void submit()}>确认重置</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
```

- [ ] **Step 2: 挂路由**

```tsx
        <Route path="/users/:id" element={<UserDetail />} />
```

- [ ] **Step 3: 构建与测试**

```bash
cd web && npm test && npm run build
```

- [ ] **Step 4: 人工验证**

需要一个登录过的真实用户。用 `examples/demo` 跑一遍登录即可造出来。

1. 从用户列表点进详情，确认身份、在线设备、登录日志三块都有数据
2. 点某个设备的「下线」，确认该行消失；用 demo 的受保护接口验证该 token 确实失效了
3. 点「冻结账号」，确认状态变成"已冻结"，**且在线设备列表被清空**（这是后端的安全耦合，界面必须如实反映）
4. 点「解除冻结」恢复
5. 点「重置密码」设一个新密码，确认提示成功、设备列表清空；用新密码在 demo 里能登录，旧密码不能

- [ ] **Step 5: 提交**

```bash
git add -A
git commit -m "feat(web): 用户详情，含身份、在线设备、登录日志与管理操作"
```

---

## Task 10: 构建集成、文档与端到端验收

**目标：** 把前端产物真正装进 `fp` 二进制，并用控制台**完整走一遍第二阶段的验收流程**——那份流程原本全是 curl，本阶段的价值就在于让它不再需要 curl。

**Files:**
- Create: `scripts/test-all.sh`
- Create: `docs/console.md`
- Modify: `examples/demo/README.md`
- Modify: `internal/integration/`（新增一个确认二进制真的带着前端的测试）

---

- [ ] **Step 1: 写"二进制确实带着前端"的测试**

前面 Task 3 的测试用的是 `fstest.MapFS`，证明的是 handler 的逻辑对。**没有任何测试证明 `web.Dist()` 里真的有东西**——一个 `go:embed` 指令写错路径、或者构建脚本没跑，表现是线上打开控制台看到 503，而全部测试照绿。

新建 `internal/integration/console_test.go`：

```go
package integration_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/basicfu/fp/web"
)

// TestEmbeddedConsoleIsPresentOrClearlyAbsent 确认嵌进二进制的前端产物
// 要么是完整的，要么是明确的"没构建"，不存在第三种半吊子状态。
//
// 为什么不直接断言"必须有 index.html"：仓库里 web/dist/ 只提交了
// .gitkeep，任何没跑过 ./scripts/build-web.sh 的开发环境和 CI 都会是空的，
// 硬性要求会让全量测试在干净 clone 上必红。所以这里断言的是**一致性**：
// 有 index.html 就必须同时有 assets/ 下的产物；两者都没有则跳过。
// 缺一半的状态（比如构建到一半被打断）会被这条抓住。
func TestEmbeddedConsoleIsPresentOrClearlyAbsent(t *testing.T) {
	dist := web.Dist()

	if _, err := fs.Stat(dist, "index.html"); err != nil {
		t.Skip("前端未构建（web/dist 为空）——跑 ./scripts/build-web.sh 后本测试才有意义")
	}

	entries, err := fs.ReadDir(dist, "assets")
	if err != nil {
		t.Fatalf("有 index.html 却没有 assets 目录，前端产物不完整: %v", err)
	}
	var hasJS, hasCSS bool
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".js"):
			hasJS = true
		case strings.HasSuffix(e.Name(), ".css"):
			hasCSS = true
		}
	}
	if !hasJS || !hasCSS {
		t.Fatalf("assets 里缺 js 或 css：hasJS=%v hasCSS=%v", hasJS, hasCSS)
	}
}
```

- [ ] **Step 2: 写 `scripts/test-all.sh`**

```bash
#!/usr/bin/env bash
# 跑全部测试：Go 侧 + 前端。
#
# scripts/test.sh 只跑 Go，且要接受 -run 之类的参数转发，所以不在那里
# 混入前端。但只有一条命令能一次跑完全部，前端测试才不会被忘掉。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "==> Go 测试"
"$ROOT/scripts/test.sh"

echo
echo "==> 前端测试"
cd "$ROOT/web"
if [ ! -d node_modules ]; then
  npm ci
fi
npm test
```

```bash
chmod +x scripts/test-all.sh
```

- [ ] **Step 3: 完整构建一次并确认单二进制可用**

```bash
./scripts/build-web.sh
go build -o fp ./cmd/fp
```

启动（用 `.env.local` 里的配置）：

```bash
./fp
```

浏览器打开 `http://localhost:8080/`——应当直接是控制台登录页，**不需要 Vite dev server**。

再确认三条关键行为：

```bash
curl -i http://localhost:8080/admin/api/no-such-endpoint
```

必须是 `404` + `Content-Type: application/json` + `{"error":"接口不存在"}`。**如果返回的是一段 HTML，说明 SPA 回退把 API 吃掉了，回 Task 3 修。**

```bash
curl -sI http://localhost:8080/ | grep -i cache-control
```

必须含 `no-cache`。

```bash
curl -i http://localhost:8080/users/some-user-id
```

必须返回 200 + index.html（前端路由回退），而不是 404。

- [ ] **Step 4: 确认全量测试通过**

```bash
./scripts/test-all.sh
go build ./... && go vet ./... && gofmt -l .
```

`TestEmbeddedConsoleIsPresentOrClearlyAbsent` 此时应当**真的跑**（不是 skip），因为 Step 3 已经构建过前端了。

- [ ] **Step 5: 写 `docs/console.md`**

```markdown
# 管理控制台

控制台是一个 React SPA，构建产物由 `go:embed` 打进 `fp` 二进制，
部署时不需要额外的静态服务器。

## 构建与运行

    ./scripts/build-web.sh      # 构建前端，产物落在 web/dist/
    go build -o fp ./cmd/fp
    ./fp                        # 浏览器打开 http://localhost:8080/

**没跑过 `build-web.sh` 也能 `go build`**（`web/dist/` 里提交了一个
`.gitkeep`，embed 指令用的是 `all:` 前缀），只是打开控制台会看到一句
"管理控制台前端尚未构建"。

## 开发

两个终端：

    go run ./cmd/fp                    # 后端，监听 8080
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
| `/applications/:id` | 基本信息、会话策略、登录方式配置 |
| `/users` | 用户列表，分页 / 关键词 / 状态筛选 |
| `/users/:id` | 身份、在线设备、登录日志、冻结、重置密码、踢设备 |

登录方式配置页是**动态渲染**的：表单字段来自后端每个 connector 的
`ConfigSchema()`（`GET /admin/api/connectors`）。新增一种登录方式时，
后端写好 `ConfigSchema()` 即可，前端不需要任何改动。
```

- [ ] **Step 6: 在 `examples/demo/README.md` 里指向控制台**

在该文件开头的准备工作部分，把"用 curl 创建应用"那几步旁边补一句：

```markdown
> 从第三阶段起，下面这些准备步骤都可以在管理控制台里点完，不必用 curl：
> 先 `./scripts/build-web.sh && go build -o fp ./cmd/fp && ./fp`，
> 浏览器打开 http://localhost:8080/ 。详见 [docs/console.md](../../docs/console.md)。
> curl 的写法保留在这里，供脚本化和排障使用。
```

- [ ] **Step 7: 端到端验收——用控制台走完第二阶段那套流程**

**这是本阶段的验收标准。全程不使用 curl，只用浏览器和 demo 服务。**

准备：

```bash
./scripts/build-web.sh
go build -o fp ./cmd/fp
./fp
```

1. **登录控制台** — 打开 `http://localhost:8080/`，用 `.env.local` 里的
   `BOOTSTRAP_ADMIN_USER` / `BOOTSTRAP_ADMIN_PASSWORD` 登录。

2. **建应用** — 新建一个应用，把弹窗里的 appId 与 appSecret 记下来，
   填进 demo 的配置。

3. **配登录方式** — 进入该应用的「登录方式」页签，把「短信验证码登录」
   打开并保存。确认刷新后仍是打开状态。

4. **调会话策略** — 「会话策略」页签，把 SDK 缓存窗口改成一个便于观察的
   小值（例如 10 秒），保存。

5. **跑 demo 登录** — 按 `examples/demo/README.md` 启动 demo，用短信验证码
   登录一个手机号（假供应商的验证码会以 WARN 打在 fp 的日志里）。

6. **看到这个用户** — 回控制台的「用户」页，搜刚才那个手机号，点进详情。
   确认：身份列表里有这条手机号、在线设备里有一台、登录日志里有一条成功记录。

7. **踢设备** — 点「下线」。回到 demo，用刚才的 token 调受保护接口，
   应当被拒。**注意**：如果 SDK 本地缓存还没过期，会有最多第 4 步设定的
   那么多秒的延迟——这正是 `cache_ttl` 的语义，不是 bug。

8. **冻结账号** — 点「冻结账号」，确认在线设备列表被清空、状态变成"已冻结"。
   用同一个手机号在 demo 里重新登录，应当被拒。再「解除冻结」，登录恢复正常。

9. **重置密码** — 点「重置密码」设一个新密码，确认设备列表被清空。

10. **停用应用** — 回到该应用详情，点「停用应用」。demo 里再登录，应当被拒
    （错误来自 `GetActiveByAppID`）。重新启用后恢复。

每一步都要**实际看到**预期结果。任何一步对不上都属于验收不通过，
不要在没有解释的情况下继续。

- [ ] **Step 8: 提交**

```bash
git add -A
git commit -m "feat: 控制台构建集成、文档与端到端验收"
```

---

## 计划自审

按 writing-plans 的要求，写完后对着范围重新核一遍。

**范围覆盖**

| 约定的范围 | 落在哪个任务 |
|---|---|
| 后端：应用停用接口 | Task 1 |
| 后端：ApplicationService 更新方法 | Task 1 |
| 后端：SetConnector 校验 connector 类型 | Task 2 |
| 后端：全局登录日志接口 | **刻意不做**，理由见「明确不做」——五个页面里没有全局日志页，登录日志只在用户详情里按用户查看 |
| 前端骨架 + go:embed | Task 3（后端侧）、Task 4（前端侧） |
| 动态表单引擎（由 ConfigSchema 驱动） | Task 7 |
| 页面：登录 | Task 5 |
| 页面：应用列表 + 详情 | Task 6 |
| 页面：登录方式配置 | Task 7 |
| 页面：用户列表 | Task 8 |
| 页面：用户详情 | Task 9 |
| 验收：不用 curl 走完第二阶段流程 | Task 10 Step 7 |

**类型一致性**：`Field` / `ConnectorSchema` / `ConnectorConfig` / `Application` /
`SessionPolicy` / `User` / `UserSession` / `LoginLog` 全部在 Task 6 Step 1 的
`types.ts` 里定义一次，Task 7/8/9 只引用不重复定义。后端新增的两个方法
（`Update`、`SetStatus`）与两条路由在 Task 1 定义，Task 6 的页面消费。
`useResource` / `errorMessage` 在 Task 6 定义，Task 7/8/9 消费。
`formatTime` 在 Task 6 的 `Applications.tsx` 里导出，Task 8/9 从那里 import。
`statusLabels` 在 Task 8 的 `Users.tsx` 里导出，Task 9 从那里 import。

**每条"必须"都有测试守着吗？** 逐条对：

| 要求 | 哪条测试会因为违反它而变红 |
|---|---|
| DISABLED 状态必须真的能写、且真的挡住登录 | `TestSetStatusDisabledBlocksGetActive` |
| 改名不许连带清空会话策略 | `TestUpdateOnlyTouchesNameAndCookieDomain`（辨别力） |
| 未注册的 connector 类型不许落库 | `TestSetConnectorRejectsUnregisteredType` |
| 配置里的未知键不许落库 | `TestSetConnectorRejectsUnknownConfigKey` |
| bool 字段不接受字符串 | `TestSetConnectorRejectsWrongValueType` |
| 未注入元数据时失败关闭而不是跳过校验 | `TestSetConnectorFailsClosedWithoutSchemas` |
| int 字段要接受 JSON 来的 float64 | `TestSetConnectorAcceptsIntFromJSONFloat` |
| API 的 404 必须是 JSON，不许被 SPA 回退吃掉 | `TestRouterAPINotFoundStaysJSON`（辨别力） |
| assets 下不存在的文件必须 404 | `TestStaticDoesNotFallBackForMissingAssets`（辨别力） |
| 前端没构建时给人话而不是白屏 | `TestStaticReportsNotBuilt` |
| `go build` 在前端未构建时也要过 | Task 3 Step 2 的显式验证 + `all:` 前缀的注释 |
| 204 响应不许把前端搞崩 | `api.test.ts` 的「204 无响应体」（辨别力） |
| 非 JSON 错误体不许抛解析错误 | `api.test.ts` 的「响应体不是 JSON」（辨别力） |
| /me 返回 401 时不许卡在 loading | `auth.test.tsx` 的「落到 anon 而不是卡在 loading」（辨别力） |
| 乱序返回不许覆盖新结果 | `useResource.test.tsx` 的「慢请求不许覆盖」（辨别力） |
| int 字段提交 number 而不是 string | `DynamicForm.test.tsx`（辨别力，同时断言值和类型） |
| schema 的 default 必须体现在界面上 | `DynamicForm.test.tsx`（辨别力） |
| 已有配置优先于 default | `DynamicForm.test.tsx`（辨别力） |
| 未知字段类型不许白屏 | `DynamicForm.test.tsx`（辨别力） |
| 分页发的是 offset 不是页码 | `query.test.ts`（辨别力，断言到第 3 页） |
| 二进制里的前端产物不许半吊子 | `TestEmbeddedConsoleIsPresentOrClearlyAbsent` |

**没有测试、只能靠人工验收的部分**，如实列在这里，不假装它们被守住了：

- 页面布局与视觉。没有截图对比测试，靠 Task 5/6/7/8/9 各自的人工验证步骤。
- 「后端加登录方式、前端零改动」这条设计目标的端到端验证。`DynamicForm`
  的单元测试证明了引擎按元数据渲染，但"后端真加一个字段、前端真的自动
  多出一个控件"只有 Task 7 Step 8 的人工步骤在验。要把它变成自动化测试，
  需要一套端到端框架（Playwright），本阶段不引入。
- 停用应用后 SDK 在 `cache_ttl` 内仍可能放行——这条行为在第二阶段有测试，
  本阶段只在界面上把它说清楚（应用详情页那段提示文字），没有新测试。

**已知会变红的既有测试**：`internal/service/application_test.go` 的
`TestConnectorCRUD`，因为它用的配置键 `minLength` 不在 password 的
ConfigSchema 里。Task 2 Step 2 指定了改法。**这是校验生效的证据，不是回归。**

---

## 执行方式

计划已保存到 `docs/superpowers/plans/2026-09-02-fp-phase3-admin-console.md`。两种执行方式：

**1. Subagent-Driven（推荐）** — 每个任务派一个全新的 subagent，任务之间做代码审查，迭代快。

**2. Inline Execution** — 在当前会话里按 executing-plans 批量执行，带检查点。

前两个阶段都用的第一种，且正是它在计划里挑出了十几处真实缺陷。建议照旧。
