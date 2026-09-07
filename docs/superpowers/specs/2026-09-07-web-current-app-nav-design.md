# fp web 后台：全局"当前应用"切换 + 导航重构

日期：2026-09-07

## 背景

`web/` 后台目前的"应用"相关页面是路由驱动的：`/applications` 列表 → 点进 `/applications/:id` 看基本信息/会话策略/登录方式/权限点四个 tab → 再点进 `/applications/:id/config`、`/applications/:id/config/versions`。用户/角色是全局实体，各自有自己的列表+详情路由，与应用无关。

用户要求把交互模型换成"全局当前应用"：左上角一个下拉选应用（localStorage 记住选择），侧边栏拍平成 5 个一级菜单（应用列表/用户管理/角色管理/权限管理/配置中心，各配图标），其中权限管理和配置中心不再挂在某个应用的详情路由下，而是独立页面，读的是"当前选中的那个应用"。

## 目标 / 非目标

**目标**
1. 新增全局"当前应用"状态：应用列表（一次性拉取）+ 当前选中的 appId（localStorage 持久化，默认第一个）。
2. 左上角（侧边栏顶部）放一个应用切换下拉。
3. 侧边栏改成 5 项：应用列表 / 用户管理 / 角色管理 / 权限管理 / 配置中心，各配 lucide 图标。
4. 权限管理、配置中心（含版本历史）拍平成独立路由（`/permissions`、`/config`、`/config/versions`），不再带 `:id`，内部读全局当前应用。
5. 应用的"基本信息/会话策略/登录方式"三个设置 tab 也改成读全局当前应用，折进 `/applications` 页面本身（不再有单独的应用详情路由），点应用卡片＝把它设为当前应用（与左上角下拉是同一份状态）。
6. 后端接口**完全不变**——`/applications/{appId}/...` 系列接口签名不动，前端只是把 `{appId}` 的来源从路由参数换成全局状态。

**非目标**
- 不改用户管理、角色管理的数据模型或页面（它们本来就是全局的，与"当前应用"无关，样式不动）。
- 不改任何后端代码（`internal/`、`cmd/`、`proto/`）。
- 不做"多应用同屏对比"之类的新功能，切换应用是覆盖式的（切走了就看不到另一个的数据），符合当前后端接口本来就是单应用视角的现实。

## 一、全局"当前应用"状态

新增 `src/lib/current-app.tsx`：

```ts
export interface CurrentAppValue {
  apps: Application[]
  currentAppId: string
  currentApp: Application | null
  setCurrentAppId: (id: string) => void
  loading: boolean
  error: string
  reload: () => void
}
export const CurrentAppContext = React.createContext<CurrentAppValue | null>(null)
export function useCurrentApp(): CurrentAppValue // 抛错版本，没有 Provider 时直接报错，同 useAuth 的风格
export function CurrentAppProvider({ children }: { children: ReactNode }): JSX.Element
```

`CurrentAppProvider` 用 `useResource` 拉一次 `/applications`；`currentAppId` 用 `useState` 的惰性初始值从 `localStorage['fp-current-app-id']` 读，写入时 `try/catch` 包一层（同 Task 2 那次教训一致，隐私模式/配额满不阻塞交互）。应用列表到手后，如果记住的 id 已经不在列表里了（应用被删，或第一次打开、localStorage 里本来没有），落到列表第一项；仍然存在就不动它。

**挂载位置**：包在 `RequireAuth` 里、`Layout` 外面——`Layout`（左上角下拉）和它下面所有路由页面都要用：

```tsx
<Route element={<RequireAuth><CurrentAppProvider><Layout /></CurrentAppProvider></RequireAuth>}>
```

## 二、左上角应用切换器

新增 `src/components/AppSwitcher.tsx`，复用现有 `Select`（不新增依赖），渲染在 `Layout.tsx` 的 `SidebarHeader` 里、"fp" 字样下方。选项就是 `apps`，`value`/`onValueChange` 接到 `currentAppId`/`setCurrentAppId`。侧边栏折叠成图标态时隐藏这个下拉（用 `group-data-[collapsible=icon]:hidden`，与折叠态下其余文字元素的处理方式一致）——折叠宽度（`3rem`）放不下一个可用的下拉框，这不是本次要解决的问题。

应用列表为空（新装环境，还没建过应用）时，下拉区域显示"还没有应用"提示文案，不渲染 `Select`。

## 三、侧边栏与路由

`Layout.tsx` 的 `nav` 数组：

| label | to | icon |
|---|---|---|
| 应用列表 | `/applications` | `AppWindow` |
| 用户管理 | `/users` | `Users` |
| 角色管理 | `/roles` | `ShieldCheck` |
| 权限管理 | `/permissions` | `KeyRound` |
| 配置中心 | `/config` | `Settings` |

`routes.tsx` 拍平：

```
/applications           Applications（列表 + 当前应用的设置区）
/users, /users/:id      不变
/roles, /roles/:id      不变
/permissions            新页面 Permissions
/config                 原 ConfigCenter，去掉 :id
/config/versions        原 ConfigVersions，去掉 :id
```

删除：`ApplicationDetail.tsx`（页面整体消失，内容拆到下面第四节）、路由 `/applications/:id`、`/applications/:id/config`、`/applications/:id/config/versions`。

`buildBreadcrumb` 不再需要 `appName` 参数（没有任何页面还需要把动态加载到的实体名报给面包屑——这正是本次改动顺带清理的机会）：

```ts
export function buildBreadcrumb(pathname: string): BreadcrumbSegment[]
```
按拍平后的路由给静态文案，顶级词条直接用上表的中文标签（应用列表/用户管理/角色管理/权限管理/配置中心），`/users/:id`、`/roles/:id` 仍然是"用户详情"/"角色详情"，`/config/versions` 是"配置中心 › 版本历史"。

`Layout.tsx` 里 `LayoutOutletContext`/`setCrumbLabel`/`useMemo(outletContext)`/路由变化清空面包屑的那个 `useEffect`——这一整套是上一版为了给 `ApplicationDetail.tsx` 上报应用名而加的，`ApplicationDetail.tsx` 一删，这套机制没有任何消费者了，一并删除，`<Outlet />` 不再传 `context`。

## 四、应用列表页 = 列表 + 当前应用设置区

`Applications.tsx` 改用 `useCurrentApp()`（不再自己 `useResource('/applications')`）。卡片网格里的每张卡片从 `<Link>` 改成 `<button onClick={() => setCurrentAppId(a.id)}>`——点击不再导航，而是把它设为当前应用；当前应用的卡片加一个视觉标记（边框高亮 + "当前"徽标）。新建应用成功后自动把新应用设为当前应用（`setCurrentAppId(res.application.id)`），体验上"建完就在编辑它"。

卡片网格下方新增 `<ApplicationSettings app={currentApp} onSaved={reload} />`（`currentApp` 非空时才渲染；应用列表为空时不渲染，只显示"还没有应用"）。

新增 `src/components/ApplicationSettings.tsx`，内容是原 `ApplicationDetail.tsx` 去掉"权限点" tab 和面包屑上报 `useEffect` 之后的东西：应用名+状态徽标+停用二次确认+「基本信息／会话策略／登录方式」三个 tab（`BasicForm`/`SessionForm`/`ConnectorsPanel`，逐字保留原实现），标题行右侧的"配置中心"链接和新增的"权限管理"链接都改成不带 id 的 `/config`、`/permissions`（因为现在是全局当前应用驱动，不用再拼 id）。

## 五、权限管理独立页

新增 `src/pages/Permissions.tsx`：从 `useCurrentApp()` 取 `currentApp`，直接渲染已有的 `PermissionsPanel`（组件本身签名 `{ app, onAppChanged }` 完全不变，`onAppChanged` 接 `useCurrentApp().reload`）。没有应用时显示"还没有应用，请先在应用列表创建一个"，不崩溃。

## 六、配置中心 / 版本历史去掉 `:id`

`ConfigCenter.tsx`、`ConfigVersions.tsx`：`useParams().id` 换成 `useCurrentApp().currentApp?.id`（没有当前应用时提前 return 一个提示文案，不发请求）。所有内部 API 调用路径字符串不变（`/applications/{appId}/config...`，`{appId}` 换个来源而已）。页面内的导航链接同步去掉 `:id`：
- `ConfigCenter.tsx` 头部"← 返回应用详情"链接删掉（没有"应用详情"路由了，当前应用已经在左上角看得到），"版本历史"按钮的 `Link` 目标从 `/applications/${id}/config/versions` 改成 `/config/versions`。
- `ConfigVersions.tsx` 头部"← 返回配置中心"目标从 `/applications/${id}/config` 改成 `/config`；回滚成功后 `navigate` 的目标同样改成 `/config`。

## 七、受影响的测试

- `ApplicationDetail.test.tsx` 整个删除（页面本身删除）。
- `Applications.test.tsx` 重写：渲染时用真实的 `CurrentAppProvider` 包一层（而不是只套 `MemoryRouter`），因为这个页面测的正是"创建应用后自动成为当前应用""点卡片切换当前应用"这些依赖 provider 真实行为的交互；`/applications` 的 GET 仍然用现成的 `stubFetchSequence` 模式打桩，只是发起请求的组件从 `Applications` 变成了它外面的 `CurrentAppProvider`，对测试可见的网络行为没有变化。
- 新增 `ApplicationSettings.test.tsx`：从 `ApplicationDetail.test.tsx` 里迁移"基本信息/会话策略/登录方式"相关的用例（渲染、保存、停用二次确认），去掉"权限点" tab 和面包屑相关的用例；直接给 `ApplicationSettings` 传 `app` prop（不需要 `CurrentAppProvider`，也不需要路由）。
- `ConfigCenter.test.tsx`、`ConfigVersions.test.tsx`：只改 `renderPage()` 这一个渲染入口——从"`MemoryRouter` + `Route path="/applications/:id/config"`"改成"`CurrentAppContext.Provider`（塞一个固定的 `{ id: 'app-1', ... }` 当前应用，`setCurrentAppId`/`reload` 用 `vi.fn()` 占位）+ 不带 `:id` 的路由/或者直接不用路由，因为组件内部不再读路由参数"。已有的业务逻辑用例（保存、类型变更确认、回滚等）断言的是 UI 文案和 fetch 调用记录，不依赖 URL 形状，预期不用改动断言本身。
- `PermissionsPanel.test.tsx`：组件签名没变，不需要改。
- 新增 `Permissions.test.tsx`：渲染 `<Permissions />`，用同样的 `CurrentAppContext.Provider` 直接注入固定当前应用，验证渲染出 `PermissionsPanel` 且能看到权限点数据（复用 `PermissionsPanel.test.tsx` 里已经验证过的接口约定，这里只需一两条烟雾测试，不重复造 `PermissionsPanel` 自己的完整用例）。
- `Layout.test.tsx`：nav 断言从 3 项改成 5 项（含新图标）；删除"子页面通过 outlet context 上报的实体名会出现在面包屑里"这条用例（机制已删除）；渲染时需要包一层 `CurrentAppContext.Provider`（`AppSwitcher` 依赖它），固定一个空的或单个应用的当前应用状态，避免真的发网络请求。
- `breadcrumb.test.ts`：按新的扁平路由和新签名（去掉 `appName` 参数）重写用例。

## 验证方式

- `npx tsc -b --noEmit`、`npx vitest run`、`npm run lint`、`npm run build` 全绿。
- 手动过一遍：新建应用→自动成为当前应用→左上角下拉能看到并切换→权限管理/配置中心页面跟着当前应用切换而变化→刷新页面后当前应用记忆还在（localStorage）。
- 不涉及任何后端接口改动，`internal/`、`cmd/`、`proto/` 目录 diff 应为空。
