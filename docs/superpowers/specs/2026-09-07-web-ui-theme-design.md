# fp web 后台 UI 主题与布局重做

日期：2026-09-07

## 背景

`web/`（应用/用户/角色管理后台，React 19 + Tailwind v4 + shadcn `base-nova` 风格 + base-ui + next-themes）目前视觉上非常朴素：`--primary` 等色板是纯灰度（oklch 0 chroma），[Layout.tsx](../../../web/src/components/Layout.tsx) 侧边栏只有文字链接、无分组/图标/折叠，顶栏只有一个退出按钮；[Applications.tsx](../../../web/src/pages/Applications.tsx)/[Users.tsx](../../../web/src/pages/Users.tsx) 是纯表格。目标是在不改动任何后端接口和业务逻辑的前提下，把整体视觉和后台骨架对齐主流 shadcn 后台产品的水准。

参考对象：
- `ui.shadcn.com` 官方站的主题切换下拉（Blue/Green/Neutral/…），证明配色就是纯 CSS 变量 + 一个 attribute 切换，不需要为选色纠结。
- `shadcn-admin.netlify.app`（开源 shadcn 后台模板）：图标+分组侧边栏（可折叠）、顶栏面包屑+搜索+主题切换+头像菜单、数据表格页（筛选+状态徽标+行内操作菜单）、卡片网格页（App 集成列表）。

## 目标 / 非目标

**目标**
1. 配色从纯灰度换成可切换的多套 CSS 变量预设，而不是"选一个然后写死"。
2. 侧边栏 + 顶栏重做成标准后台骨架（图标导航、可折叠、面包屑、头像菜单）。
3. 列表页视觉对齐参考站：Users/Roles 表格页做筛选栏和状态徽标的样式打磨；Applications 页改成卡片网格。
4. 遵循已装的 `ui-styling` / `ui-ux-pro-max` skill 里的可访问性与间距规则（对比度、触控区域、语义化 token）。

**非目标**
- 不新增页面、不改后端接口、不新增业务功能（比如顶栏搜索框暂时只是 UI 占位，除非确认有可用的全局搜索接口）。
- 不引入除 lucide-react（已装）之外的新图标库；不引入状态管理库。
- ApplicationDetail / RoleDetail / UserDetail / ConfigCenter / ConfigVersions / Login 页面内部表单和业务逻辑不动，只跟随全局主题变量和外层骨架自动变化；如果这些页面本身有表格/卡片，顺带按同一套视觉规则微调，但不改交互逻辑。

## 一、主题变量与切换器

### 颜色预设

在 [index.css](../../../web/src/index.css) 里，把现有 `:root` / `.dark` 的灰度色值保留作为 `mono`（极简黑白）预设，再新增三套：`blue`（科技蓝，默认）、`violet`（紫罗兰）、`emerald`（翡翠绿）。每套预设都要同时给出浅色和深色两组值（用户已确认深浅色同等重要，都要打磨），写法沿用 shadcn 官方"Method 3"模式，用 `[data-theme="xxx"]` 包一层，和 `next-themes` 现有的 `.dark` 类叠加：

```css
[data-theme="blue"] {
  --primary: oklch(0.55 0.22 259);
  --primary-foreground: oklch(0.98 0 0);
  --ring: oklch(0.55 0.22 259);
  --sidebar-primary: oklch(0.55 0.22 259);
  --chart-1: oklch(0.55 0.22 259);
  /* ... */
}
[data-theme="blue"].dark {
  --primary: oklch(0.65 0.19 259);
  /* ... 深色下更亮更低饱和，保证对比度 */
}
```

四套预设分别对齐之前浏览器里给用户看过的方向：
- `blue`：默认，深色侧边栏 + 蓝色强调（对齐 GitHub/Vercel 类企业后台观感）
- `violet`：靛紫强调（Linear 风格）
- `emerald`：绿色强调（配置发布/运行状态语义）
- `mono`：现有纯灰度精修版，色彩只用在状态徽标等极少数地方

`--radius` 等非色彩变量不随预设变化，四套只切主色相关变量（primary/ring/sidebar-primary/chart-1，以及从 primary 派生的少量强调色），border/background/muted 等中性色统一维持现有灰度骨架，保证阅读区域不因为换主题而跳动。

### 切换器

新增 `src/components/ThemeColorSwitcher.tsx`：一个下拉（复用已有 shadcn `select` 或新装 `dropdown-menu`），选项为四套预设的中文名。选中后：
- 把 `data-theme` 属性写到 `<html>` 根节点
- 存入 `localStorage`（key `fp-ui-theme`），首屏用一段内联脚本或 `useEffect` 前置读取，避免刷新闪烁（做法与 `next-themes` 的 `attribute="class"` 思路一致，只是这里是自定义的第二个 attribute，不需要引入新依赖）
- 默认值 `blue`

顶栏里这个下拉和已有的明暗模式切换（当前项目已装 `next-themes` 但还没有可见的切换按钮，UI 上要补一个）并排放置。

## 二、应用外壳重做（Layout.tsx）

对照 `shadcn-admin` 的骨架，重写 [Layout.tsx](../../../web/src/components/Layout.tsx)：

**侧边栏**

用 shadcn 官方 `sidebar` 区块（`npx shadcn add sidebar`，内含 `SidebarProvider`/`Sidebar`/`SidebarHeader`/`SidebarContent`/`SidebarGroup`/`SidebarMenu`/`SidebarTrigger` 等一整套原语），不再手写 div。理由：虽然当前只有 3 个一级导航项，用户明确后续会继续加导航项，官方区块自带折叠（icon-collapsed 模式）、快捷键（Cmd/Ctrl+B）、状态持久化（cookie）、移动端 Sheet 化、`SidebarGroup` 分组能力，现在切进来能省掉后续再迁移一次的成本。

- `SidebarHeader`：产品标记（"fp"）
- `SidebarContent` 里一个 `SidebarGroup`，`SidebarMenu` 三项：应用 / 用户 / 角色，各配一个 lucide 图标（`AppWindow` / `Users` / `ShieldCheck`），当前路由高亮（`SidebarMenuButton` 的 `isActive`）；后续新增导航项直接往这个 `SidebarMenu`（或新开一个 `SidebarGroup` 分组）里加
- 折叠由区块自带的 `collapsible="icon"` 模式处理，折叠态图标 hover 自动显示 tooltip（区块内置，不必再单独装 `tooltip` 组件）；折叠状态区块自己持久化，不用再手写 `localStorage` 逻辑
- 顶栏或侧边栏头部放 `SidebarTrigger` 作为展开/收起按钮

**顶栏**
- 左侧：面包屑（`breadcrumb` 新组件），根据当前路由拼出层级，比如 应用 / Acme / 配置中心 / 版本历史（应用名需要从已加载的详情数据里取，取不到时退化成纯路径文案，不额外发请求）
- 右侧：主题色切换下拉、明暗模式切换按钮、头像下拉菜单（`dropdown-menu` + `avatar` 新组件，菜单项：当前用户名（只读）、退出）

**响应式**：后台以桌面使用为主，窄屏下的抽屉式侧边栏（Sheet）是 `sidebar` 区块自带行为，直接用默认表现即可，不需要额外开发。

## 三、列表页视觉

- **Users.tsx / Roles.tsx**：保留现有筛选表单 + `useSearchParams` 逻辑不动，只调整视觉——筛选输入框加搜索图标、状态用带语义色的徽标（active=绿、suspended/disabled=红调，而不是当前 default/secondary 两个中性变体）、表格行 hover 态、表头 sticky。
- **Applications.tsx**：列表区从 `Table` 改成卡片网格（`grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4`），每张卡片：应用名 + slug + 状态徽标 + 创建时间 + 点击进详情，视觉上对齐参考站的 `Apps` 集成网格。新建/详情跳转逻辑不变，`CreateDialog` / `SecretDialog` 不动。
- 其余页面内表格（ConfigVersions 等）只做色板跟随，不改结构。

## 四、需要新增的 shadcn 组件

用现有 `shadcn` CLI（已是依赖）添加，风格保持 `base-nova` / neutral 一致：`sidebar`（含其依赖的 `separator`——已装、`tooltip`、`sheet`，CLI 会自动带出）、`dropdown-menu`、`avatar`、`breadcrumb`。

## 五、验证方式

- `npm run dev` 起本地服务，在浏览器里过一遍：四套配色 × 明暗两态 = 8 种组合下，Layout/Applications/Users/Roles 页面的可读性和对比度（对齐已装 `ui-ux-pro-max` skill 里 §1/§6 的对比度规则）。
- `npm run test`（vitest）：现有测试文件（`Applications.test.tsx` 目前没有，但 `Users.test.tsx` 等按 DOM 结构/文案查询）要跟着结构调整同步更新，改完必须全绿。
- `npm run lint`（oxlint）。
- 不新增/修改任何 `internal/`、`cmd/`、`proto/` 下的后端代码。
