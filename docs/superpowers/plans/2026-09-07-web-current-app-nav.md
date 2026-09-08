# fp web 全局当前应用切换 + 导航重构 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `web/` 后台的应用相关页面从"路由钻取"（`/applications/:id/...`）改成"全局当前应用"模型：左上角下拉切换当前应用（localStorage 记忆），侧边栏拍平成应用列表/用户管理/角色管理/权限管理/配置中心 5 项，权限管理与配置中心变成不带 `:id` 的独立路由，读的是全局当前应用。后端接口一个字节都不改。

**Architecture:** 新增一个 `CurrentAppProvider`（React Context），包在 `Layout` 外面，拉一次 `/applications`、管理 `currentAppId`（localStorage 持久化）。所有原来靠 `useParams().id` 拿应用 id 的页面/组件改成靠 `useCurrentApp().currentApp`。原来挂在 `/applications/:id` 详情页下的"基本信息/会话策略/登录方式"三个 tab 拆成独立组件 `ApplicationSettings`，折进 `/applications` 页面本身；"权限点" tab 拆成独立页面 `/permissions`。

**Tech Stack:** 同上一版（React 19、react-router 8、shadcn `base-nova`/base-ui、Tailwind v4、vitest + @testing-library/react）。不引入任何新依赖。

## Global Constraints

- 后端接口不变：`internal/`、`cmd/`、`proto/` 目录 diff 必须为空。所有 API 调用路径字符串（`/applications/{appId}/...`）不变，只是 `{appId}` 的来源从路由参数换成 `useCurrentApp().currentApp.id`。
- 不引入新的 npm 依赖（不新增 icon 库、不新增状态管理库、不装 `@testing-library/user-event`——本仓库一直没有这个包，用 `fireEvent`）。
- 用户管理（Users/UserDetail）、角色管理（Roles/RoleDetail）页面内部逻辑完全不动。
- `PermissionsPanel.tsx`、`ConnectorsPanel.tsx`、`DynamicForm.tsx`、`ConfirmDialog.tsx` 这几个纯 prop 驱动的组件不改一行——它们本来就不依赖路由参数。
- 现有测试文件改完必须全绿；删除的页面（`ApplicationDetail.tsx`）连带删除它的测试文件，不允许留着一个测不到东西的空文件，也不允许为了让测试通过而删掉本该保留的断言。
- 每个任务完成后跑一次 `npx tsc -b --noEmit` 确认没有类型错误。

---

## 背景排雷（写计划时验证过，执行时不用重新踩一遍）

1. **`CurrentAppContext` 要导出成一个具名的 React Context 对象，不能只导出 `useCurrentApp()` 这个 hook。** 后面好几个页面级测试（`ConfigCenter.test.tsx`、`ConfigVersions.test.tsx`、`Permissions.test.tsx`）都要绕开真实的网络请求，直接拿 `<CurrentAppContext.Provider value={固定的 CurrentAppValue}>` 包一层，不走真实的 `CurrentAppProvider`（那个会真的发 `/applications` 请求）。这比给每个测试文件都多стub一次 `/applications` 的 fetch 更省事、更不脆弱。
2. **"记住的 currentAppId 是否还在最新列表里"这个校验 effect，依赖数组只能放 `[apps.data]`，不能带 `currentAppId`。** 如果带上 `currentAppId`：新建应用后 `onCreated` 里会同时调用 `setCurrentAppId(新id)` 和 `reload()`；`setCurrentAppId` 是同步的，`reload()` 触发的新列表要等一轮网络往返才回来——中间这一帧，`apps.data` 还是**旧**列表（不含新 id），如果校验 effect 因为 `currentAppId` 变化而重新跑一遍，会把刚选中的新应用误判成"不存在"，纠正回旧列表的第一项，用户刚建的应用又被切走了。只在 `apps.data` 变化时校验，就没有这个竞态——`reload()` 的新数据到手后再校验一次，那时新应用已经在列表里，不会误纠正。
3. **`Applications.test.tsx` 现在要用真实的 `CurrentAppProvider` 包一层**（不是像 `ConfigCenter.test.tsx` 那样绕开它）——这个页面测的正是"新建应用后自动成为当前应用""点卡片切换当前应用"这些依赖 provider 真实行为的交互，绕开 provider 就测不到这些。`/applications` 的 GET 请求还是用现成的 `stubFetchSequence` 打桩，只是发请求的组件从 `Applications.tsx` 本身变成了它外面包着的 `CurrentAppProvider`，对测试可见的网络行为没有变化。
4. **routes.tsx 在整个计划期间会被多个任务分别改动一小块**（先包 Provider，再加 `/permissions`，再把 `/config` 系列拍平，最后删掉 `/applications/:id` 系列）。每个任务的 Files 段落都会给出那一刻 routes.tsx 的精确 diff，照着改就行，不需要自己推断"现在应该是什么样"。
5. **`ApplicationDetail.tsx` 到 Task 7 才删。** Task 2 重做 `Layout.tsx` 时，`LayoutOutletContext`/`setCrumbLabel`/`buildBreadcrumb` 的第二个参数这一整套机制**先留着不动**——`ApplicationDetail.tsx` 在 Task 7 之前还活着、还在用它，Task 2 如果提前删掉这套机制，`ApplicationDetail.tsx` 会编译不过，Task 2 自己的 `tsc -b --noEmit` 就会先炸。这套机制的清理和 `buildBreadcrumb` 签名简化，统一放到 Task 7（跟删除 `ApplicationDetail.tsx` 同一个任务），因为那正是它唯一的消费者被移除的时刻。
6. **Task 2 到 Task 6 之间，侧边栏导航的新中文标签（应用列表/用户管理/角色管理）和面包屑的旧标签（应用/用户/角色）会暂时不一致**——`buildBreadcrumb` 的签名/文案要等 Task 7 才统一改。这是刻意的过渡状态，不是遗漏；Task 2 的 `Layout.test.tsx` 断言会刻意只断言导航项的新标签，不去断言面包屑文案，并在测试里写清楚原因。

---

### Task 1: 全局"当前应用"状态（`useCurrentApp` + `CurrentAppProvider`）

**Files:**
- Create: `web/src/lib/current-app.tsx`
- Test: `web/src/lib/current-app.test.tsx`

**Interfaces:**
- Produces:
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
  export const CurrentAppContext: React.Context<CurrentAppValue | null>
  export function useCurrentApp(): CurrentAppValue
  export function CurrentAppProvider({ children }: { children: ReactNode }): JSX.Element
  ```
  后面所有任务都会 `import { useCurrentApp, CurrentAppProvider, CurrentAppContext } from '@/lib/current-app'`（组件用前两个，测试用 `CurrentAppContext` 直接注入固定值）。localStorage key 是字符串常量 `'fp-current-app-id'`，不对外导出（外部不需要知道这个细节）。

- [ ] **Step 1: 写测试**

创建 `web/src/lib/current-app.test.tsx`：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { CurrentAppProvider, useCurrentApp } from './current-app'
import type { Application } from './types'

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
})

const appA: Application = {
  id: 'app-a',
  name: 'A应用',
  slug: 'a',
  appId: 'appid-a',
  status: 'ACTIVE',
  cookieDomain: '',
  defaultRoleKey: '',
  session: {
    idleTimeoutSeconds: 1,
    idleTimeoutMobileSeconds: 0,
    maxLifetimeSeconds: 1,
    rotateIntervalSeconds: 1,
    extendIntervalSeconds: 1,
    tokenCacheTtlSeconds: 1,
  },
  createdAt: 1,
  updatedAt: 1,
}
const appB: Application = { ...appA, id: 'app-b', name: 'B应用', slug: 'b' }

function stubFetchSequence(...responses: Response[]) {
  const fn = vi.fn()
  for (const r of responses) fn.mockResolvedValueOnce(r)
  vi.stubGlobal('fetch', fn)
  return fn
}

function Probe() {
  const { apps, currentAppId, currentApp, setCurrentAppId } = useCurrentApp()
  return (
    <div>
      <div data-testid="count">{apps.length}</div>
      <div data-testid="current-id">{currentAppId}</div>
      <div data-testid="current-name">{currentApp?.name ?? ''}</div>
      {apps.map((a) => (
        <button key={a.id} onClick={() => setCurrentAppId(a.id)}>
          选 {a.name}
        </button>
      ))}
    </div>
  )
}

test('没有记住任何选择时，默认选中列表第一个应用', async () => {
  stubFetchSequence(new Response(JSON.stringify([appA, appB]), { status: 200 }))
  render(
    <CurrentAppProvider>
      <Probe />
    </CurrentAppProvider>,
  )

  await waitFor(() => expect(screen.getByTestId('current-id').textContent).toBe('app-a'))
  expect(screen.getByTestId('current-name').textContent).toBe('A应用')
})

test('localStorage 里记住的 id 还在列表里时，用它而不是第一个', async () => {
  localStorage.setItem('fp-current-app-id', 'app-b')
  stubFetchSequence(new Response(JSON.stringify([appA, appB]), { status: 200 }))
  render(
    <CurrentAppProvider>
      <Probe />
    </CurrentAppProvider>,
  )

  await waitFor(() => expect(screen.getByTestId('current-id').textContent).toBe('app-b'))
})

test('记住的 id 已经不在最新列表里时（应用被删），回退到第一个', async () => {
  localStorage.setItem('fp-current-app-id', 'app-deleted')
  stubFetchSequence(new Response(JSON.stringify([appA, appB]), { status: 200 }))
  render(
    <CurrentAppProvider>
      <Probe />
    </CurrentAppProvider>,
  )

  await waitFor(() => expect(screen.getByTestId('current-id').textContent).toBe('app-a'))
})

test('切换当前应用会写入 localStorage', async () => {
  stubFetchSequence(new Response(JSON.stringify([appA, appB]), { status: 200 }))
  render(
    <CurrentAppProvider>
      <Probe />
    </CurrentAppProvider>,
  )

  await waitFor(() => expect(screen.getByTestId('current-id').textContent).toBe('app-a'))
  fireEvent.click(screen.getByRole('button', { name: '选 B应用' }))

  await waitFor(() => expect(screen.getByTestId('current-id').textContent).toBe('app-b'))
  expect(localStorage.getItem('fp-current-app-id')).toBe('app-b')
})

test('useCurrentApp 在 Provider 之外调用会抛错', () => {
  function Bare() {
    useCurrentApp()
    return null
  }
  expect(() => render(<Bare />)).toThrow('useCurrentApp 必须在 CurrentAppProvider 内使用')
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd web && npx vitest run src/lib/current-app.test.tsx`
Expected: FAIL，找不到 `./current-app` 模块。

- [ ] **Step 3: 实现 current-app.tsx**

创建 `web/src/lib/current-app.tsx`：

```tsx
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { api } from './api'
import { useResource } from './useResource'
import type { Application } from './types'

const STORAGE_KEY = 'fp-current-app-id'

export interface CurrentAppValue {
  apps: Application[]
  currentAppId: string
  currentApp: Application | null
  setCurrentAppId: (id: string) => void
  loading: boolean
  error: string
  reload: () => void
}

export const CurrentAppContext = createContext<CurrentAppValue | null>(null)

/** 拿全局"当前应用"状态。必须在 CurrentAppProvider 内使用，否则直接报错——同 useAuth 的风格，不悄悄退化。 */
export function useCurrentApp(): CurrentAppValue {
  const v = useContext(CurrentAppContext)
  if (!v) throw new Error('useCurrentApp 必须在 CurrentAppProvider 内使用')
  return v
}

function readStoredId(): string {
  try {
    return localStorage.getItem(STORAGE_KEY) ?? ''
  } catch {
    return ''
  }
}

function persistId(id: string) {
  try {
    localStorage.setItem(STORAGE_KEY, id)
  } catch {
    // 隐私模式/配额满：不持久化，本次会话内仍然生效
  }
}

export function CurrentAppProvider({ children }: { children: ReactNode }) {
  const apps = useResource(() => api.get<Application[]>('/applications'), [])
  const [currentAppId, setCurrentAppIdState] = useState<string>(readStoredId)

  // 应用列表变化时（首次拉到、或者后面 reload 拉到新的一份）才重新校验
  // currentAppId 是否还在列表里——不把 currentAppId 放进依赖数组：否则
  // "用户刚选中一个还没进到最新列表里的应用"（比如新建应用后立刻
  // setCurrentAppId 到新 id，此时 reload() 的响应还在路上，apps.data
  // 还是旧列表）会被这个效应误判成"记住的 id 已经不存在了"，把选择
  // 错误地纠正回列表第一项。只在 apps.data 真正变化时才做这个校验，
  // 每次校验用的都是当次渲染里最新的 currentAppId，不是陈旧值。
  useEffect(() => {
    if (!apps.data) return
    const stillExists = apps.data.some((a) => a.id === currentAppId)
    if (stillExists) return
    const fallback = apps.data[0]?.id ?? ''
    setCurrentAppIdState(fallback)
    persistId(fallback)
  }, [apps.data])

  const setCurrentAppId = useCallback((id: string) => {
    setCurrentAppIdState(id)
    persistId(id)
  }, [])

  const currentApp = useMemo(
    () => apps.data?.find((a) => a.id === currentAppId) ?? null,
    [apps.data, currentAppId],
  )

  const value = useMemo<CurrentAppValue>(
    () => ({
      apps: apps.data ?? [],
      currentAppId,
      currentApp,
      setCurrentAppId,
      loading: apps.loading,
      error: apps.error,
      reload: apps.reload,
    }),
    [apps.data, apps.loading, apps.error, apps.reload, currentAppId, currentApp, setCurrentAppId],
  )

  return <CurrentAppContext.Provider value={value}>{children}</CurrentAppContext.Provider>
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd web && npx vitest run src/lib/current-app.test.tsx`
Expected: PASS（5 个用例全绿）。

- [ ] **Step 5: 类型检查**

Run: `cd web && npx tsc -b --noEmit`
Expected: 无错误。

- [ ] **Step 6: Commit**

```bash
git add web/src/lib/current-app.tsx web/src/lib/current-app.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): 新增全局当前应用状态 CurrentAppProvider

拉一次 /applications，currentAppId 存 localStorage；应用列表变化时
才校验记住的 id 是否还有效，不依赖 currentAppId 本身的变化触发校验
——避免"刚选中的新建应用被误纠正回列表第一项"的竞态。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 侧边栏应用切换器 + 5 项导航

**Files:**
- Create: `web/src/components/AppSwitcher.tsx`
- Modify: `web/src/components/Layout.tsx`
- Modify: `web/src/routes.tsx`
- Modify: `web/src/components/Layout.test.tsx`

**Interfaces:**
- Consumes: `useCurrentApp`、`CurrentAppProvider`（Task 1）。
- 不改 `LayoutOutletContext`/`buildBreadcrumb` 的调用方式——这套机制留给 Task 7 清理（见背景排雷第 5 条），本任务只加东西，不删。

- [ ] **Step 1: 创建 AppSwitcher.tsx**

```tsx
import { AppWindow } from 'lucide-react'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useCurrentApp } from '@/lib/current-app'

export default function AppSwitcher() {
  const { apps, currentAppId, setCurrentAppId, loading } = useCurrentApp()

  if (!loading && apps.length === 0) {
    return <p className="px-2 text-xs text-muted-foreground">还没有应用</p>
  }

  return (
    <Select value={currentAppId} onValueChange={(v) => v && setCurrentAppId(v)}>
      <SelectTrigger aria-label="切换当前应用" className="w-full">
        <AppWindow className="size-4 shrink-0 text-muted-foreground" />
        <SelectValue placeholder={loading ? '加载中…' : '选择应用'} />
      </SelectTrigger>
      <SelectContent>
        {apps.map((a) => (
          <SelectItem key={a.id} value={a.id}>
            {a.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
```

保存为 `web/src/components/AppSwitcher.tsx`。

- [ ] **Step 2: 改 Layout.tsx——加图标导入、5 项导航、渲染 AppSwitcher**

把 `web/src/components/Layout.tsx` 顶部的：

```tsx
import type { LucideIcon } from 'lucide-react'
import { AppWindow, LogOut, ShieldCheck, Users as UsersIcon } from 'lucide-react'
```

改成：

```tsx
import type { LucideIcon } from 'lucide-react'
import { AppWindow, KeyRound, LogOut, Settings, ShieldCheck, Users as UsersIcon } from 'lucide-react'
```

再加一行 import（放在其他 `@/components` import 之后）：

```tsx
import AppSwitcher from '@/components/AppSwitcher'
```

把：

```tsx
const nav: { to: string; label: string; icon: LucideIcon }[] = [
  { to: '/applications', label: '应用', icon: AppWindow },
  { to: '/users', label: '用户', icon: UsersIcon },
  { to: '/roles', label: '角色', icon: ShieldCheck },
]
```

改成：

```tsx
const nav: { to: string; label: string; icon: LucideIcon }[] = [
  { to: '/applications', label: '应用列表', icon: AppWindow },
  { to: '/users', label: '用户管理', icon: UsersIcon },
  { to: '/roles', label: '角色管理', icon: ShieldCheck },
  { to: '/permissions', label: '权限管理', icon: KeyRound },
  { to: '/config', label: '配置中心', icon: Settings },
]
```

（`/permissions`、`/config` 这两个路由还没建——Task 4、Task 6 会建。导航项先加上，NavLink 指向一个暂时不存在的路由不会报错，只是暂时点不出内容，这是刻意的过渡状态，见背景排雷第 6 条。）

把：

```tsx
        <SidebarHeader>
          <div className="flex h-8 items-center px-2 text-lg font-semibold">fp</div>
        </SidebarHeader>
```

改成：

```tsx
        <SidebarHeader>
          <div className="flex h-8 items-center px-2 text-lg font-semibold">fp</div>
          <div className="group-data-[collapsible=icon]:hidden">
            <AppSwitcher />
          </div>
        </SidebarHeader>
```

（折叠成图标态时隐藏切换器——`3rem` 宽度放不下一个可用的下拉框，这不是本次要解决的问题，直接隐藏最简单也最不容易出视觉 bug。）

- [ ] **Step 3: routes.tsx 包一层 CurrentAppProvider**

把 `web/src/routes.tsx` 顶部的 import 区块，加一行：

```tsx
import { CurrentAppProvider } from '@/lib/current-app'
```

把：

```tsx
      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
```

改成：

```tsx
      <Route
        element={
          <RequireAuth>
            <CurrentAppProvider>
              <Layout />
            </CurrentAppProvider>
          </RequireAuth>
        }
      >
```

（这一步只是包一层 Provider，下面 9 行 `<Route path=... />` 先原样不动，后面几个任务会逐个改。）

- [ ] **Step 4: 改 Layout.test.tsx**

把 `web/src/components/Layout.test.tsx` 整个替换成：

```tsx
import { useEffect } from 'react'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, within, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useOutletContext } from 'react-router'
import Layout from './Layout'
import { CurrentAppProvider } from '@/lib/current-app'
import type { LayoutOutletContext } from './Layout'

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ username: 'alice', logout: vi.fn() }),
}))

afterEach(() => vi.unstubAllGlobals())

/** 这几条测试关注导航/面包屑本身，不关心应用切换器，固定返回空列表即可。 */
function stubEmptyApplications() {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify([]), { status: 200 })))
}

function renderLayout(initialEntries: string[]) {
  stubEmptyApplications()
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <CurrentAppProvider>
        <Routes>
          <Route element={<Layout />}>
            <Route path="/applications" element={<div>应用页内容</div>} />
            <Route path="/users" element={<div>用户页内容</div>} />
          </Route>
        </Routes>
      </CurrentAppProvider>
    </MemoryRouter>,
  )
}

test('渲染五个导航项，当前用户名和页面内容都显示', async () => {
  renderLayout(['/applications'])
  // shadcn 的 BreadcrumbPage 本身也带 role="link"（aria-disabled，标记当前页）；
  // 把查询范围限定在侧边栏导航列表（data-sidebar="menu"）内，避免和
  // 面包屑里的当前页文案产生歧义匹配。
  const navMenu = document.querySelector('[data-sidebar="menu"]') as HTMLElement
  expect(within(navMenu).getByRole('link', { name: /应用列表/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /用户管理/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /角色管理/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /权限管理/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /配置中心/ })).toBeTruthy()
  expect(screen.getByText('alice')).toBeTruthy()
  expect(screen.getByText('应用页内容')).toBeTruthy()
  // 应用切换器：空列表时显示提示文案，不崩溃。
  await waitFor(() => expect(screen.getByText('还没有应用')).toBeTruthy())
})

test('切到用户列表页，导航项使用新标签"用户管理"', () => {
  renderLayout(['/users'])
  expect(screen.getByText('用户页内容')).toBeTruthy()
  // 面包屑文案这一步还没改（buildBreadcrumb 留给 Task 7 统一处理），
  // 这里只确认导航项本身已经用上新标签，不对面包屑的具体文案做断言。
  const navMenu = document.querySelector('[data-sidebar="menu"]') as HTMLElement
  expect(within(navMenu).getByRole('link', { name: /用户管理/ })).toBeTruthy()
})

test('点击折叠按钮后侧边栏进入 collapsed 状态', () => {
  renderLayout(['/applications'])
  const trigger = screen.getByRole('button', { name: 'Toggle Sidebar' })
  fireEvent.click(trigger)
  expect(document.querySelector('[data-slot="sidebar"][data-state="collapsed"]')).toBeTruthy()
})

function Reporter({ name }: { name: string }) {
  const outlet = useOutletContext<LayoutOutletContext | undefined>()
  useEffect(() => {
    const t = setTimeout(() => outlet?.setCrumbLabel(name))
    return () => clearTimeout(t)
  }, [outlet, name])
  return <div>详情内容</div>
}

test('子页面通过 outlet context 上报的实体名会出现在面包屑里', async () => {
  stubEmptyApplications()
  render(
    <MemoryRouter initialEntries={['/applications/app-1']}>
      <CurrentAppProvider>
        <Routes>
          <Route element={<Layout />}>
            <Route path="/applications/:id" element={<Reporter name="Acme" />} />
          </Route>
        </Routes>
      </CurrentAppProvider>
    </MemoryRouter>,
  )
  expect(await screen.findByText('Acme')).toBeTruthy()
})
```

（最后这条 outlet-context 测试本任务先保留原样——机制本身要等 Task 7 才删，见背景排雷第 5 条。）

- [ ] **Step 5: 运行测试确认通过**

Run: `cd web && npx vitest run src/components/Layout.test.tsx`
Expected: PASS（4 个用例全绿）。

- [ ] **Step 6: 类型检查 + 全量测试**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: 全部 PASS（其余页面测试目前还不需要 `CurrentAppProvider`，因为它们还没被改成用 `useCurrentApp`——那是后面几个任务的事）。

- [ ] **Step 7: Commit**

```bash
git add web/src/components/AppSwitcher.tsx web/src/components/Layout.tsx \
  web/src/components/Layout.test.tsx web/src/routes.tsx
git commit -m "$(cat <<'EOF'
feat(web): 侧边栏加应用切换器，导航拍平成 5 项

左上角新增 AppSwitcher（Select 复用现有组件，折叠态隐藏）；侧边栏
导航从 3 项改成应用列表/用户管理/角色管理/权限管理/配置中心 5 项，
后两项对应的路由还没建，Task 4/6 会补上；routes.tsx 包一层
CurrentAppProvider。面包屑/outlet-context 机制本次不动，留给 Task 7
统一清理。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 应用设置组件 `ApplicationSettings`（从 ApplicationDetail 拆出来）

**Files:**
- Create: `web/src/components/ApplicationSettings.tsx`
- Test: `web/src/components/ApplicationSettings.test.tsx`

**Interfaces:**
- Produces: `export default function ApplicationSettings({ app, onSaved }: { app: Application; onSaved: () => void }): JSX.Element`。Task 6 的 `Applications.tsx` 会渲染它。
- 这一步纯新增，不改、不删 `ApplicationDetail.tsx`（那个文件到 Task 7 才删），两者暂时并存，内容有重复——这是刻意的过渡状态，不是遗漏。

- [ ] **Step 1: 写测试**

创建 `web/src/components/ApplicationSettings.test.tsx`：

```tsx
import { useState } from 'react'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import ApplicationSettings from './ApplicationSettings'
import type { Application } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

const baseApp: Application = {
  id: 'app-1',
  name: '测试应用',
  slug: 'test-app',
  appId: 'appid-123',
  status: 'ACTIVE',
  cookieDomain: '',
  defaultRoleKey: '',
  session: {
    idleTimeoutSeconds: 604800,
    idleTimeoutMobileSeconds: 2592000,
    maxLifetimeSeconds: 7776000,
    rotateIntervalSeconds: 86400,
    extendIntervalSeconds: 600,
    tokenCacheTtlSeconds: 30,
  },
  createdAt: 1700000000000,
  updatedAt: 1700000000000,
}

function stubFetchSequence(...responses: Response[]) {
  const fn = vi.fn()
  for (const r of responses) fn.mockResolvedValueOnce(r)
  vi.stubGlobal('fetch', fn)
  return fn
}

/**
 * Harness 模拟真实用法：ApplicationSettings 不自己拉数据，app 由父组件
 * （Task 6 的 Applications.tsx，读全局当前应用）传入；onSaved 触发时父组件
 * 会重新拉一份新的 app 传回来。这里用本地 state 模拟"重新传入新 app"，
 * nextApp 是测试提前算好的、保存成功后应该变成的样子。
 */
function Harness({ initial, nextApp }: { initial: Application; nextApp?: Application }) {
  const [app, setApp] = useState(initial)
  return <ApplicationSettings app={app} onSaved={() => nextApp && setApp(nextApp)} />
}

/** 切到"会话策略"页签，返回"空闲超时（秒）"输入框所在的 form。 */
async function openSessionForm() {
  fireEvent.click(screen.getByRole('tab', { name: '会话策略' }))
  const idleInput = await waitFor(() => screen.getByLabelText('空闲超时（秒）') as HTMLInputElement)
  const form = idleInput.closest('form')
  if (!form) throw new Error('未找到会话策略表单')
  return { idleInput, form }
}

function findCall(fetchMock: ReturnType<typeof vi.fn>, method: string): [string, RequestInit] {
  const call = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === method)
  if (!call) throw new Error(`没有发出过 ${method} 请求`)
  return call as [string, RequestInit]
}

test('保存会话策略时，提交给后端的字段是数字而不是字符串', async () => {
  const updated: Application = { ...baseApp, session: { ...baseApp.session, idleTimeoutSeconds: 7200 } }
  const fetchMock = stubFetchSequence(new Response(JSON.stringify(updated), { status: 200 }))

  render(<Harness initial={baseApp} nextApp={updated} />)
  const { idleInput, form } = await openSessionForm()

  fireEvent.change(idleInput, { target: { value: '7200' } })
  fireEvent.submit(form)

  await waitFor(() => {
    const putCall = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'PUT')
    expect(putCall).toBeDefined()
  })

  const [url, init] = findCall(fetchMock, 'PUT')
  expect(url).toBe('/admin/api/applications/app-1/session')
  const body = JSON.parse(init.body as string) as Record<string, unknown>

  expect(body.idleTimeoutSeconds).toBe(7200)
  expect(typeof body.idleTimeoutSeconds).toBe('number')
  expect(body.tokenCacheTtlSeconds).toBe(30)
  expect(typeof body.tokenCacheTtlSeconds).toBe('number')
  expect(typeof body.maxLifetimeSeconds).toBe('number')
})

test('延期间隔大于等于空闲超时时，前端拦截，不发请求且给出中文提示', async () => {
  const fetchMock = stubFetchSequence()

  render(<Harness initial={baseApp} />)
  const { form } = await openSessionForm()
  const extendInput = screen.getByLabelText('延期间隔（秒）') as HTMLInputElement

  fireEvent.change(extendInput, { target: { value: '99999999' } })
  fireEvent.submit(form)

  await waitFor(() => expect(screen.getByText(/延期间隔必须小于空闲超时/)).toBeTruthy())
  expect(fetchMock.mock.calls.length).toBe(0)
})

test('停用应用需要二次确认才发 PATCH status=DISABLED；启用应用直接发 PATCH status=ACTIVE', async () => {
  const disabledApp: Application = { ...baseApp, status: 'DISABLED' }
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify(disabledApp), { status: 200 }), // PATCH /status → DISABLED
    new Response(JSON.stringify(baseApp), { status: 200 }), // PATCH /status → ACTIVE
  )

  function Toggle() {
    const [app, setApp] = useState(baseApp)
    return (
      <ApplicationSettings
        app={app}
        onSaved={() => setApp((a) => (a.status === 'ACTIVE' ? disabledApp : baseApp))}
      />
    )
  }
  render(<Toggle />)

  fireEvent.click(screen.getByRole('button', { name: '停用应用' }))
  expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(false)

  const confirmBtn = await waitFor(() => screen.getByRole('button', { name: '确认停用' }))
  fireEvent.click(confirmBtn)

  await waitFor(() => expect(screen.getByRole('button', { name: '启用应用' })).toBeTruthy())

  const [disableUrl, disableInit] = findCall(fetchMock, 'PATCH')
  expect(disableUrl).toBe('/admin/api/applications/app-1/status')
  expect(JSON.parse(disableInit.body as string)).toEqual({ status: 'DISABLED' })

  fireEvent.click(screen.getByRole('button', { name: '启用应用' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '停用应用' })).toBeTruthy())

  const patchCalls = fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')
  expect(patchCalls).toHaveLength(2)
  const [enableUrl, enableInit] = patchCalls[1] as [string, RequestInit]
  expect(enableUrl).toBe('/admin/api/applications/app-1/status')
  expect(JSON.parse(enableInit.body as string)).toEqual({ status: 'ACTIVE' })
})

test('停用应用弹窗点取消，不发请求，应用保持启用', async () => {
  const fetchMock = stubFetchSequence()

  render(<Harness initial={baseApp} />)

  fireEvent.click(screen.getByRole('button', { name: '停用应用' }))
  const cancelBtn = await waitFor(() => screen.getByRole('button', { name: '取消' }))
  fireEvent.click(cancelBtn)

  await new Promise((r) => setTimeout(r, 50))

  expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(false)
  expect(screen.getByRole('button', { name: '停用应用' })).toBeTruthy()
})

test('基本信息表单提交，PATCH 请求体含 name 与 cookieDomain', async () => {
  const updated: Application = { ...baseApp, name: '改名后的应用', cookieDomain: 'example.com' }
  const fetchMock = stubFetchSequence(new Response(JSON.stringify(updated), { status: 200 }))

  render(<Harness initial={baseApp} nextApp={updated} />)

  const nameInput = screen.getByLabelText('名称') as HTMLInputElement
  const cookieInput = screen.getByLabelText('Cookie 作用域') as HTMLInputElement
  fireEvent.change(nameInput, { target: { value: '改名后的应用' } })
  fireEvent.change(cookieInput, { target: { value: 'example.com' } })
  fireEvent.submit(nameInput.closest('form')!)

  await waitFor(() =>
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(
      true,
    ),
  )

  const [url, init] = findCall(fetchMock, 'PATCH')
  expect(url).toBe('/admin/api/applications/app-1')
  const body = JSON.parse(init.body as string) as Record<string, unknown>
  expect(body.name).toBe('改名后的应用')
  expect(body.cookieDomain).toBe('example.com')
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd web && npx vitest run src/components/ApplicationSettings.test.tsx`
Expected: FAIL，找不到 `./ApplicationSettings` 模块。

- [ ] **Step 3: 实现 ApplicationSettings.tsx**

创建 `web/src/components/ApplicationSettings.tsx`（内容取自 `web/src/pages/ApplicationDetail.tsx`，去掉"权限点" tab、去掉面包屑上报的 `useEffect`/`useOutletContext`，`app`/`onSaved` 改成 props）：

```tsx
import { useState } from 'react'
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
import ConnectorsPanel from '@/components/ConnectorsPanel'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/useResource'
import { applicationStatusLabels } from '@/lib/labels'
import { applicationStatusBadgeClassName, GRAY } from '@/lib/status-badge'
import type { Application, SessionPolicy } from '@/lib/types'

export default function ApplicationSettings({ app, onSaved }: { app: Application; onSaved: () => void }) {
  // 只有"停用"这个方向需要二次确认：它会拒绝该应用的全部新登录与 SDK 回源
  // 校验，是四个破坏性操作之一。"启用"是恢复服务，不是破坏性操作，直接执行。
  const [confirmingDisable, setConfirmingDisable] = useState(false)

  async function toggleStatus() {
    const next = app.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE'
    try {
      await api.patch(`/applications/${app.id}/status`, { status: next })
      toast.success(next === 'ACTIVE' ? '已启用' : '已停用')
      onSaved()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  function onToggleStatusClick() {
    if (app.status === 'ACTIVE') {
      setConfirmingDisable(true)
    } else {
      void toggleStatus()
    }
  }

  return (
    <div className="space-y-6 rounded-xl border p-6">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-lg font-semibold">{app.name}</h2>
        <Badge className={applicationStatusBadgeClassName[app.status] ?? GRAY}>
          {applicationStatusLabels[app.status] ?? app.status}
        </Badge>
        <div className="flex-1" />
        <Button variant={app.status === 'ACTIVE' ? 'destructive' : 'default'} onClick={onToggleStatusClick}>
          {app.status === 'ACTIVE' ? '停用应用' : '启用应用'}
        </Button>
      </div>

      <ConfirmDialog
        open={confirmingDisable}
        onOpenChange={setConfirmingDisable}
        title="停用应用"
        description={`停用后，「${app.name}」的全部新登录与 SDK 的下一次回源校验都会被拒绝。已签发的 token 最多还能在各接入方本地缓存里存活 ${app.session.tokenCacheTtlSeconds} 秒。此操作可以随时再次启用撤销，但停用期间会中断所有依赖该应用登录的下游服务。`}
        confirmLabel="确认停用"
        onConfirm={() => {
          setConfirmingDisable(false)
          void toggleStatus()
        }}
      />

      {app.status === 'DISABLED' && (
        <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm">
          该应用已停用：新的登录会被拒绝，SDK 的下一次回源校验也会被拒绝。
          已签发的 token 最多还能在各接入方本地缓存里存活 {app.session.tokenCacheTtlSeconds} 秒。
        </p>
      )}

      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">基本信息</TabsTrigger>
          <TabsTrigger value="session">会话策略</TabsTrigger>
          <TabsTrigger value="connectors">登录方式</TabsTrigger>
        </TabsList>

        <TabsContent value="basic" className="pt-4">
          <BasicForm app={app} onSaved={onSaved} />
        </TabsContent>

        <TabsContent value="session" className="pt-4">
          <SessionForm app={app} onSaved={onSaved} />
        </TabsContent>

        <TabsContent value="connectors" className="pt-4">
          <ConnectorsPanel appId={app.id} />
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
  const { register, handleSubmit, formState } = useForm<SessionPolicy>({
    resolver: zodResolver(sessionSchema),
    defaultValues: app.session,
  })

  async function onSubmit(v: SessionPolicy) {
    try {
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
        <form onSubmit={handleSubmit(onSubmit)} className="max-w-md space-y-4" noValidate>
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

- [ ] **Step 4: 运行测试确认通过**

Run: `cd web && npx vitest run src/components/ApplicationSettings.test.tsx`
Expected: PASS（6 个用例全绿）。

- [ ] **Step 5: 类型检查 + 全量测试**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: 全部 PASS。

- [ ] **Step 6: Commit**

```bash
git add web/src/components/ApplicationSettings.tsx web/src/components/ApplicationSettings.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): 新增 ApplicationSettings 组件

从 ApplicationDetail.tsx 拆出"基本信息/会话策略/登录方式"三个 tab，
app/onSaved 改成 props，不再自己拉数据、不再依赖路由参数。
ApplicationDetail.tsx 暂时并存不动，Task 7 统一删除。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 权限管理独立页

**Files:**
- Create: `web/src/pages/Permissions.tsx`
- Test: `web/src/pages/Permissions.test.tsx`
- Modify: `web/src/routes.tsx`

**Interfaces:**
- Consumes: `useCurrentApp`（Task 1）、`PermissionsPanel`（已有，签名 `{ app: Application; onAppChanged: () => void }` 不变）。

- [ ] **Step 1: 写测试**

创建 `web/src/pages/Permissions.test.tsx`：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import Permissions from './Permissions'
import { CurrentAppContext } from '@/lib/current-app'
import type { CurrentAppValue } from '@/lib/current-app'
import type { Application, PermissionPoint, Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const app: Application = {
  id: 'app-1',
  name: '商城',
  slug: 'mall',
  appId: 'appid-1',
  status: 'ACTIVE',
  cookieDomain: '',
  defaultRoleKey: '',
  session: {
    idleTimeoutSeconds: 1,
    idleTimeoutMobileSeconds: 0,
    maxLifetimeSeconds: 1,
    rotateIntervalSeconds: 1,
    extendIntervalSeconds: 1,
    tokenCacheTtlSeconds: 1,
  },
  createdAt: 1,
  updatedAt: 1,
}

const roles: Role[] = [{ id: 'r1', key: '普通用户', name: '普通用户', parentId: '', createdAt: 1 }]

function point(): PermissionPoint {
  return {
    id: 'p1',
    key: 'GET:/orders/{id}',
    name: '查看订单',
    kind: 'api',
    source: 'app',
    status: 'normal',
    staleForMs: 0,
    lastSeenAt: 1,
    createdAt: 1,
  }
}

function stubFetch() {
  const fn = vi.fn((url: string) => {
    const body: Record<string, unknown> = {
      '/admin/api/applications/app-1/permissions': [point()],
      '/admin/api/roles': roles,
    }
    const key = Object.keys(body).find((k) => url.includes(k))
    return Promise.resolve(new Response(JSON.stringify(key ? body[key] : []), { status: 200 }))
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function currentAppValue(overrides: Partial<CurrentAppValue> = {}): CurrentAppValue {
  return {
    apps: [app],
    currentAppId: app.id,
    currentApp: app,
    setCurrentAppId: () => {},
    loading: false,
    error: '',
    reload: () => {},
    ...overrides,
  }
}

test('渲染当前应用的权限点管理', async () => {
  stubFetch()
  render(
    <MemoryRouter>
      <CurrentAppContext.Provider value={currentAppValue()}>
        <Permissions />
      </CurrentAppContext.Provider>
    </MemoryRouter>,
  )

  expect(screen.getByText(/权限管理/)).toBeTruthy()
  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
})

test('没有任何应用时显示提示，不崩溃', () => {
  render(
    <MemoryRouter>
      <CurrentAppContext.Provider value={currentAppValue({ apps: [], currentAppId: '', currentApp: null })}>
        <Permissions />
      </CurrentAppContext.Provider>
    </MemoryRouter>,
  )
  expect(screen.getByText(/还没有应用/)).toBeTruthy()
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd web && npx vitest run src/pages/Permissions.test.tsx`
Expected: FAIL，找不到 `./Permissions` 模块。

- [ ] **Step 3: 实现 Permissions.tsx**

创建 `web/src/pages/Permissions.tsx`：

```tsx
import { useCurrentApp } from '@/lib/current-app'
import PermissionsPanel from '@/components/PermissionsPanel'

export default function Permissions() {
  const { currentApp, apps, loading, reload } = useCurrentApp()

  if (loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (apps.length === 0) {
    return <p className="text-sm text-muted-foreground">还没有应用，请先在「应用列表」创建一个。</p>
  }
  if (!currentApp) return null

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">权限管理 · {currentApp.name}</h1>
      <PermissionsPanel app={currentApp} onAppChanged={reload} />
    </div>
  )
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd web && npx vitest run src/pages/Permissions.test.tsx`
Expected: PASS。

- [ ] **Step 5: routes.tsx 加路由**

在 `web/src/routes.tsx` 顶部 import 区块加一行：

```tsx
import Permissions from '@/pages/Permissions'
```

把：

```tsx
        <Route path="/roles" element={<Roles />} />
        <Route path="/roles/:id" element={<RoleDetail />} />
```

改成：

```tsx
        <Route path="/roles" element={<Roles />} />
        <Route path="/roles/:id" element={<RoleDetail />} />
        <Route path="/permissions" element={<Permissions />} />
```

- [ ] **Step 6: 类型检查 + 全量测试**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: 全部 PASS。

- [ ] **Step 7: Commit**

```bash
git add web/src/pages/Permissions.tsx web/src/pages/Permissions.test.tsx web/src/routes.tsx
git commit -m "$(cat <<'EOF'
feat(web): 新增权限管理独立页 /permissions

不带 :id，直接读全局当前应用，渲染已有的 PermissionsPanel（组件本身
签名不变）。没有应用时显示提示，不崩溃。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 配置中心 / 版本历史去掉 `:id`

**Files:**
- Modify: `web/src/pages/ConfigCenter.tsx`
- Modify: `web/src/pages/ConfigVersions.tsx`
- Modify: `web/src/pages/ConfigCenter.test.tsx`
- Modify: `web/src/pages/ConfigVersions.test.tsx`
- Modify: `web/src/routes.tsx`

**Interfaces:**
- Consumes: `useCurrentApp`（Task 1）。
- API 调用路径字符串不变，`{appId}` 的来源从 `useParams().id` 换成 `useCurrentApp().currentApp?.id`。

- [ ] **Step 1: 运行现有测试确认基线通过**

Run: `cd web && npx vitest run src/pages/ConfigCenter.test.tsx src/pages/ConfigVersions.test.tsx`
Expected: PASS（改动前的基线）。

- [ ] **Step 2: 改 ConfigCenter.tsx**

把 `web/src/pages/ConfigCenter.tsx` 顶部的：

```tsx
import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
```

改成：

```tsx
import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useCurrentApp } from '@/lib/current-app'
```

把：

```tsx
export default function ConfigCenter() {
  const { id = '' } = useParams()
  const [partition, setPartition] = useState<ConfigPartition>('DEFAULT')
  const snapshot = useResource(
    () => api.get<ConfigSnapshot>(`/applications/${id}/config?type=${partition}`),
    [id, partition],
  )
```

改成：

```tsx
export default function ConfigCenter() {
  const { currentApp } = useCurrentApp()
  const id = currentApp?.id ?? ''
  const [partition, setPartition] = useState<ConfigPartition>('DEFAULT')
  const snapshot = useResource(
    () =>
      id
        ? api.get<ConfigSnapshot>(`/applications/${id}/config?type=${partition}`)
        : Promise.resolve<ConfigSnapshot>({ seq: 0, fields: {} }),
    [id, partition],
  )
```

（没有当前应用时 `id` 是空串，`load` 直接返回一个空快照，不发请求——下面 Step 里会在渲染最前面加一个"还没有应用"的提前 return，正常情况下根本走不到这个分支，这里只是兜底，避免拼出 `/applications//config` 这种畸形 URL。）

在函数体末尾、`return (` 之前（`const hasFieldErrors = ...` 那一行之后），插入：

```tsx
  if (!currentApp) {
    return <p className="text-sm text-muted-foreground">还没有应用，请先在「应用列表」创建一个。</p>
  }
```

把页面头部的：

```tsx
      <div className="flex items-center gap-3">
        <div>
          <Link to={`/applications/${id}`} className="text-sm text-muted-foreground underline-offset-4 hover:underline">
            ← 返回应用详情
          </Link>
          <h1 className="text-xl font-semibold">配置中心</h1>
        </div>
        <div className="flex-1" />
        <Button variant="outline" render={<Link to={`/applications/${id}/config/versions`} />}>
          版本历史
        </Button>
        <Button variant="outline" onClick={() => setAdding(true)}>
          新建配置项
        </Button>
      </div>
```

改成：

```tsx
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">配置中心 · {currentApp.name}</h1>
        <div className="flex-1" />
        <Button variant="outline" render={<Link to="/config/versions" />}>
          版本历史
        </Button>
        <Button variant="outline" onClick={() => setAdding(true)}>
          新建配置项
        </Button>
      </div>
```

（没有"应用详情"页了，"返回"链接删掉；当前应用本身在左上角切换器上已经能看到，标题里加一下名字更直观。）

- [ ] **Step 3: 改 ConfigVersions.tsx**

把 `web/src/pages/ConfigVersions.tsx` 顶部的：

```tsx
import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
```

改成：

```tsx
import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useCurrentApp } from '@/lib/current-app'
```

把：

```tsx
export default function ConfigVersions() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const [partition, setPartition] = useState<ConfigPartition>('DEFAULT')

  const versions = useResource(
    () => api.get<ConfigVersion[]>(`/applications/${id}/config/versions?type=${partition}`),
    [id, partition],
  )
```

改成：

```tsx
export default function ConfigVersions() {
  const { currentApp } = useCurrentApp()
  const id = currentApp?.id ?? ''
  const navigate = useNavigate()
  const [partition, setPartition] = useState<ConfigPartition>('DEFAULT')

  const versions = useResource(
    () =>
      id
        ? api.get<ConfigVersion[]>(`/applications/${id}/config/versions?type=${partition}`)
        : Promise.resolve<ConfigVersion[]>([]),
    [id, partition],
  )
```

把 `confirmRollback` 里的：

```tsx
      navigate(`/applications/${id}/config`)
```

改成：

```tsx
      navigate('/config')
```

在函数体末尾、`return (` 之前（`const unsetResult = ...` 那一行之后），插入：

```tsx
  if (!currentApp) {
    return <p className="text-sm text-muted-foreground">还没有应用，请先在「应用列表」创建一个。</p>
  }
```

把页面头部的：

```tsx
      <div className="flex items-center gap-3">
        <div>
          <Link
            to={`/applications/${id}/config`}
            className="text-sm text-muted-foreground underline-offset-4 hover:underline"
          >
            ← 返回配置中心
          </Link>
          <h1 className="text-xl font-semibold">版本历史</h1>
        </div>
      </div>
```

改成：

```tsx
      <div className="flex items-center gap-3">
        <div>
          <Link to="/config" className="text-sm text-muted-foreground underline-offset-4 hover:underline">
            ← 返回配置中心
          </Link>
          <h1 className="text-xl font-semibold">版本历史 · {currentApp.name}</h1>
        </div>
      </div>
```

- [ ] **Step 4: 改 ConfigCenter.test.tsx**

把 `web/src/pages/ConfigCenter.test.tsx` 顶部的：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import ConfigCenter from './ConfigCenter'
import type { ConfigSnapshot } from '@/lib/types'
```

改成：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import ConfigCenter from './ConfigCenter'
import { CurrentAppContext } from '@/lib/current-app'
import type { CurrentAppValue } from '@/lib/current-app'
import type { Application, ConfigSnapshot } from '@/lib/types'
```

把：

```tsx
function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/applications/app-1/config']}>
      <Routes>
        <Route path="/applications/:id/config" element={<ConfigCenter />} />
      </Routes>
    </MemoryRouter>,
  )
}
```

改成：

```tsx
const fixedApp: Application = {
  id: 'app-1',
  name: '固定应用',
  slug: 'fixed',
  appId: 'appid-1',
  status: 'ACTIVE',
  cookieDomain: '',
  defaultRoleKey: '',
  session: {
    idleTimeoutSeconds: 1,
    idleTimeoutMobileSeconds: 0,
    maxLifetimeSeconds: 1,
    rotateIntervalSeconds: 1,
    extendIntervalSeconds: 1,
    tokenCacheTtlSeconds: 1,
  },
  createdAt: 1,
  updatedAt: 1,
}

/**
 * ConfigCenter 现在从全局"当前应用"读 appId，不再走路由参数。直接塞一个
 * 固定的 context 值，绕开真实的 CurrentAppProvider——不需要真的发一次
 * /applications 请求去凑出一个"当前应用"。id 用 'app-1'，和原来路由参数
 * 的值保持一致，下面每条测试断言的 URL 字符串不用跟着改。
 */
function currentAppValue(): CurrentAppValue {
  return {
    apps: [fixedApp],
    currentAppId: fixedApp.id,
    currentApp: fixedApp,
    setCurrentAppId: () => {},
    loading: false,
    error: '',
    reload: () => {},
  }
}

function renderPage() {
  return render(
    <MemoryRouter>
      <CurrentAppContext.Provider value={currentAppValue()}>
        <ConfigCenter />
      </CurrentAppContext.Provider>
    </MemoryRouter>,
  )
}
```

文件里其余的 `test(...)` 用例本身不用改——它们断言的是页面文案和 fetch 调用记录，不依赖 URL 形状，`id` 还是 `'app-1'`，断言里写的 `/admin/api/applications/app-1/config...` 这类字符串照样成立。

- [ ] **Step 5: 改 ConfigVersions.test.tsx**

把 `web/src/pages/ConfigVersions.test.tsx` 顶部的：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import ConfigVersions from './ConfigVersions'
import type { ConfigSnapshot, ConfigVersion } from '@/lib/types'
```

改成：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import ConfigVersions from './ConfigVersions'
import { CurrentAppContext } from '@/lib/current-app'
import type { CurrentAppValue } from '@/lib/current-app'
import type { Application, ConfigSnapshot, ConfigVersion } from '@/lib/types'
```

把：

```tsx
/** /applications/:id/config 只放一个占位页——验证"回滚成功后跳回配置中心页"
 * 只需要知道路由确实换了，不需要真的渲染 ConfigCenter 那一整套逻辑（那是
 * Task 13 自己的测试范围）。 */
function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/applications/app-1/config/versions']}>
      <Routes>
        <Route path="/applications/:id/config/versions" element={<ConfigVersions />} />
        <Route path="/applications/:id/config" element={<div>CONFIG_CENTER_PLACEHOLDER</div>} />
      </Routes>
    </MemoryRouter>,
  )
}
```

改成：

```tsx
const fixedApp: Application = {
  id: 'app-1',
  name: '固定应用',
  slug: 'fixed',
  appId: 'appid-1',
  status: 'ACTIVE',
  cookieDomain: '',
  defaultRoleKey: '',
  session: {
    idleTimeoutSeconds: 1,
    idleTimeoutMobileSeconds: 0,
    maxLifetimeSeconds: 1,
    rotateIntervalSeconds: 1,
    extendIntervalSeconds: 1,
    tokenCacheTtlSeconds: 1,
  },
  createdAt: 1,
  updatedAt: 1,
}

/** 同 ConfigCenter.test.tsx：绕开真实 CurrentAppProvider，直接塞固定当前应用。 */
function currentAppValue(): CurrentAppValue {
  return {
    apps: [fixedApp],
    currentAppId: fixedApp.id,
    currentApp: fixedApp,
    setCurrentAppId: () => {},
    loading: false,
    error: '',
    reload: () => {},
  }
}

/** /config 只放一个占位页——验证"回滚成功后跳回配置中心页"只需要知道
 * 路由确实换了，不需要真的渲染 ConfigCenter 那一整套逻辑。 */
function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/config/versions']}>
      <CurrentAppContext.Provider value={currentAppValue()}>
        <Routes>
          <Route path="/config/versions" element={<ConfigVersions />} />
          <Route path="/config" element={<div>CONFIG_CENTER_PLACEHOLDER</div>} />
        </Routes>
      </CurrentAppContext.Provider>
    </MemoryRouter>,
  )
}
```

文件里其余的 `test(...)` 用例不用改，理由同 ConfigCenter.test.tsx。

- [ ] **Step 6: routes.tsx 拍平配置中心路由**

把 `web/src/routes.tsx` 顶部的：

```tsx
import ConfigCenter from '@/pages/ConfigCenter'
import ConfigVersions from '@/pages/ConfigVersions'
```

保持不动（import 路径没变，只是 `Route path` 要改）。把：

```tsx
        <Route path="/applications/:id/config" element={<ConfigCenter />} />
        <Route path="/applications/:id/config/versions" element={<ConfigVersions />} />
```

删掉，在 `<Route path="/permissions" element={<Permissions />} />` 那一行之后加：

```tsx
        <Route path="/config" element={<ConfigCenter />} />
        <Route path="/config/versions" element={<ConfigVersions />} />
```

（`/applications/:id` 那一条路由——指向还没删的 `ApplicationDetail.tsx`——本任务不动，留给 Task 7。）

- [ ] **Step 7: 运行测试确认通过**

Run: `cd web && npx vitest run src/pages/ConfigCenter.test.tsx src/pages/ConfigVersions.test.tsx`
Expected: 全部 PASS，与 Step 1 的基线用例数一致。

- [ ] **Step 8: 类型检查 + 全量测试**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: 全部 PASS。

- [ ] **Step 9: Commit**

```bash
git add web/src/pages/ConfigCenter.tsx web/src/pages/ConfigVersions.tsx \
  web/src/pages/ConfigCenter.test.tsx web/src/pages/ConfigVersions.test.tsx web/src/routes.tsx
git commit -m "$(cat <<'EOF'
feat(web): 配置中心/版本历史改用全局当前应用，路由拍平成 /config

appId 来源从 useParams 换成 useCurrentApp；两个页面头部的"返回"链接
改成不带 :id 的目标；没有当前应用时显示提示而不是拼出畸形 URL。
测试绕开真实 CurrentAppProvider，直接注入固定当前应用。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 应用列表页改用全局当前应用（渲染 ApplicationSettings）

**Files:**
- Modify: `web/src/pages/Applications.tsx`
- Modify: `web/src/pages/Applications.test.tsx`

**Interfaces:**
- Consumes: `useCurrentApp`（Task 1）、`ApplicationSettings`（Task 3）。
- `ApplicationDetail.tsx` 和它的路由本任务仍然不动（Task 7 删）——本任务之后，卡片不再链接过去，但文件和路由还留着，属于"已经没有任何 UI 入口，但还没正式删除"的过渡态，不是 bug。

- [ ] **Step 1: 运行现有测试确认基线通过**

Run: `cd web && npx vitest run src/pages/Applications.test.tsx`
Expected: PASS（改动前的基线）。

- [ ] **Step 2: 重写 Applications.tsx**

把 `web/src/pages/Applications.tsx` 整个替换成：

```tsx
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { AppWindow } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import ApplicationSettings from '@/components/ApplicationSettings'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/useResource'
import { useCurrentApp } from '@/lib/current-app'
import { cn } from '@/lib/utils'
import { formatTime } from '@/lib/format'
import { applicationStatusLabels } from '@/lib/labels'
import { applicationStatusBadgeClassName, GRAY } from '@/lib/status-badge'
import type { CreateApplicationResponse } from '@/lib/types'

const createSchema = z.object({
  name: z.string().min(1, '请输入应用名称'),
  slug: z
    .string()
    .min(1, '请输入 slug')
    .regex(/^[a-z0-9][a-z0-9-]*$/, 'slug 只能用小写字母、数字和连字符，且不能以连字符开头'),
})
type CreateValues = z.infer<typeof createSchema>

export default function Applications() {
  const { apps, currentAppId, currentApp, setCurrentAppId, loading, error, reload } = useCurrentApp()
  const [creating, setCreating] = useState(false)
  const [newSecret, setNewSecret] = useState<CreateApplicationResponse | null>(null)

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">应用列表</h1>
        <Button onClick={() => setCreating(true)}>新建应用</Button>
      </div>

      {loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {error && <p className="text-sm text-destructive">{error}</p>}

      {!loading && apps.length === 0 && (
        <p className="text-center text-sm text-muted-foreground">还没有应用</p>
      )}

      {apps.length > 0 && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {apps.map((a) => {
            const isCurrent = a.id === currentAppId
            return (
              <button
                key={a.id}
                type="button"
                onClick={() => setCurrentAppId(a.id)}
                className={cn(
                  'group rounded-xl border bg-card p-4 text-left transition-colors hover:border-primary/50 hover:bg-accent/30',
                  isCurrent && 'border-primary ring-1 ring-primary',
                )}
              >
                <div className="flex items-start justify-between gap-2">
                  <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                    <AppWindow className="size-5" />
                  </div>
                  <div className="flex items-center gap-1.5">
                    {isCurrent && <Badge variant="outline">当前</Badge>}
                    <Badge className={applicationStatusBadgeClassName[a.status] ?? GRAY}>
                      {applicationStatusLabels[a.status] ?? a.status}
                    </Badge>
                  </div>
                </div>
                <div className="mt-3 space-y-1">
                  <div className="font-medium group-hover:underline group-hover:underline-offset-4">{a.name}</div>
                  <div className="text-xs text-muted-foreground">{a.slug}</div>
                </div>
                <div className="mt-4 text-xs text-muted-foreground">创建于 {formatTime(a.createdAt)}</div>
              </button>
            )
          })}
        </div>
      )}

      {currentApp && <ApplicationSettings app={currentApp} onSaved={reload} />}

      <CreateDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(res) => {
          setCreating(false)
          setNewSecret(res)
          reload()
          setCurrentAppId(res.application.id)
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
 * copyAppSecret 把 appSecret 写入剪贴板，失败时给出提示而不是悄悄没反应。
 *
 * 两种失败都要接住：(1) navigator.clipboard 在非安全上下文（http 且非
 * localhost）下整个是 undefined，直接调用会抛错；(2) 即使存在，
 * writeText() 也可能因为权限被拒绝等原因 reject。
 */
async function copyAppSecret(secret: string) {
  try {
    if (!navigator.clipboard) throw new Error('clipboard API 不可用')
    await navigator.clipboard.writeText(secret)
    toast.success('已复制')
  } catch {
    toast.error('复制失败，请手动选中复制')
  }
}

/**
 * SecretDialog 展示刚创建出的明文 appSecret。
 *
 * 后端只在创建响应里返回这一次，之后无法读回（库里只有 bcrypt 哈希）。
 * 所以这个弹窗必须说清楚"关掉就没了"，否则用户会以为随时能回来看。
 */
function SecretDialog({ value, onClose }: { value: CreateApplicationResponse | null; onClose: () => void }) {
  return (
    <Dialog
      open={value !== null}
      // appSecret 只在创建时返回这一次，后端库里只存了 bcrypt 哈希，没有
      // "重新生成"接口——意外关闭这个弹窗（手滑点到遮罩外、习惯性按
      // Escape）会让密钥永久丢失，只能整个应用作废重建，appId 也会跟着变，
      // 下游已配置的 SDK 接入方随之失效。disablePointerDismissal 在源头
      // 拦掉点遮罩（outsidePress）；但它只管 outsidePress，不管 Escape
      // （@base-ui/react 的 DialogInteractions 里 escapeKey 判断只看
      // isTopmost，不看 disablePointerDismissal，两者是分开的开关）。所以
      // onOpenChange 干脆不响应任何 base-ui 内部发起的关闭请求（Escape、
      // 万一 showCloseButton default 被升级改回来时的 X 按钮）——唯一能
      // 关闭它的只有下面"我已保存"按钮直接调用的 onClose()，不经过这里。
      onOpenChange={() => {}}
      disablePointerDismissal
    >
      <DialogContent showCloseButton={false}>
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
          <Button variant="outline" onClick={() => value && void copyAppSecret(value.appSecret)}>
            复制 appSecret
          </Button>
          <Button onClick={onClose}>我已保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
```

（`CreateDialog`/`copyAppSecret`/`SecretDialog` 逐字保留原实现。）

- [ ] **Step 3: 重写 Applications.test.tsx**

把 `web/src/pages/Applications.test.tsx` 整个替换成：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { toast } from 'sonner'
import Applications from './Applications'
import { CurrentAppProvider } from '@/lib/current-app'
import type { Application, CreateApplicationResponse } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  Reflect.deleteProperty(navigator, 'clipboard')
})

const sampleApp: Application = {
  id: 'app-1',
  name: '示例应用',
  slug: 'demo',
  appId: 'appid-xyz',
  status: 'ACTIVE',
  cookieDomain: '',
  defaultRoleKey: '',
  session: {
    idleTimeoutSeconds: 604800,
    idleTimeoutMobileSeconds: 0,
    maxLifetimeSeconds: 7776000,
    rotateIntervalSeconds: 86400,
    extendIntervalSeconds: 600,
    tokenCacheTtlSeconds: 30,
  },
  createdAt: 1700000000000,
  updatedAt: 1700000000000,
}

function stubFetchSequence(...responses: Response[]) {
  const fn = vi.fn()
  for (const r of responses) fn.mockResolvedValueOnce(r)
  vi.stubGlobal('fetch', fn)
  return fn
}

/**
 * 用真实的 CurrentAppProvider（不是像 ConfigCenter.test.tsx 那样绕开它）：
 * 这个页面测的正是"新建应用后自动成为当前应用""点卡片切换当前应用"这些
 * 依赖 provider 真实行为的交互。
 */
function renderApplications() {
  return render(
    <MemoryRouter>
      <CurrentAppProvider>
        <Applications />
      </CurrentAppProvider>
    </MemoryRouter>,
  )
}

test('渲染应用列表', async () => {
  stubFetchSequence(new Response(JSON.stringify([sampleApp]), { status: 200 }))

  renderApplications()

  await waitFor(() => expect(screen.getByText('示例应用')).toBeTruthy())
  expect(screen.getByText('demo')).toBeTruthy()
  // "启用"出现两次：卡片上的状态徽标 + ApplicationSettings 设置区里的状态徽标。
  expect(screen.getAllByText('启用').length).toBeGreaterThanOrEqual(2)
})

test('空列表显示"还没有应用"提示', async () => {
  stubFetchSequence(new Response(JSON.stringify([]), { status: 200 }))

  renderApplications()

  await waitFor(() => expect(screen.getByText('还没有应用')).toBeTruthy())
})

test('默认选中列表第一个应用，点第二张卡片切换当前应用', async () => {
  const appB: Application = { ...sampleApp, id: 'app-2', name: '第二个应用', slug: 'second' }
  stubFetchSequence(new Response(JSON.stringify([sampleApp, appB]), { status: 200 }))

  renderApplications()

  // 默认选中第一个：设置区标题（h2）和卡片标题一起，至少出现两次"示例应用"。
  await waitFor(() => expect(screen.getAllByText('示例应用').length).toBeGreaterThanOrEqual(2))

  fireEvent.click(screen.getByText('第二个应用'))

  await waitFor(() => expect(screen.getAllByText('第二个应用').length).toBeGreaterThanOrEqual(2))
})

const createdSecret: CreateApplicationResponse = {
  application: { ...sampleApp, id: 'app-2', name: '新应用', slug: 'new-app', appId: 'appid-new' },
  appSecret: 'plaintext-secret-abc',
}

/** 走完"新建应用"整个流程，停在 appSecret 弹窗打开的状态，返回 fetch spy。 */
async function openSecretDialog() {
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify([]), { status: 200 }), // CurrentAppProvider 初始列表（空）
    new Response(JSON.stringify(createdSecret), { status: 201 }), // 创建
    new Response(JSON.stringify([createdSecret.application]), { status: 200 }), // 创建成功后 reload()
  )

  renderApplications()
  await waitFor(() => expect(screen.getByText('还没有应用')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '新建应用' }))
  const nameInput = await waitFor(() => screen.getByLabelText('名称') as HTMLInputElement)
  const slugInput = screen.getByLabelText('slug') as HTMLInputElement

  fireEvent.change(nameInput, { target: { value: '新应用' } })
  fireEvent.change(slugInput, { target: { value: 'new-app' } })
  fireEvent.submit(nameInput.closest('form')!)

  await waitFor(() => expect(screen.getByText('plaintext-secret-abc')).toBeTruthy())
  return fetchMock
}

test('新建应用：提交后弹出 appSecret 弹窗且提示只显示一次，POST 请求体正确', async () => {
  const fetchMock = await openSecretDialog()
  expect(screen.getByText(/只显示这一次/)).toBeTruthy()

  const postCall = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'POST')
  expect(postCall).toBeDefined()
  const [url, init] = postCall as [string, RequestInit]
  expect(url).toBe('/admin/api/applications')
  expect(JSON.parse(init.body as string)).toEqual({ name: '新应用', slug: 'new-app' })

  // 关闭弹窗后，新应用出现，且已经自动成为当前应用（设置区标题也是它）。
  fireEvent.click(screen.getByRole('button', { name: '我已保存' }))
  await waitFor(() => expect(screen.getAllByText('新应用').length).toBeGreaterThanOrEqual(2))
})

test('appSecret 弹窗按 Escape 不会关闭', async () => {
  await openSecretDialog()

  fireEvent.keyDown(document, { key: 'Escape', code: 'Escape' })
  await new Promise((r) => setTimeout(r, 50))

  expect(screen.getByText('plaintext-secret-abc')).toBeTruthy()
})

test('appSecret 弹窗不渲染默认的关闭按钮', async () => {
  await openSecretDialog()
  expect(document.querySelector('[data-slot="dialog-close"]')).toBeNull()
})

test('appSecret 弹窗点击遮罩不会关闭', async () => {
  await openSecretDialog()

  const overlay = document.querySelector('[data-slot="dialog-overlay"]')
  if (!overlay) throw new Error('未找到遮罩层')
  fireEvent.pointerDown(overlay)
  fireEvent.click(overlay)
  await new Promise((r) => setTimeout(r, 50))

  expect(screen.getByText('plaintext-secret-abc')).toBeTruthy()
})

test('复制 appSecret 失败（剪贴板 API 不可用）时给出中文提示，而不是悄悄没反应', async () => {
  Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')

  await openSecretDialog()
  fireEvent.click(screen.getByRole('button', { name: '复制 appSecret' }))

  await waitFor(() => expect(errorSpy).toHaveBeenCalledWith('复制失败，请手动选中复制'))
})

test('复制 appSecret 成功时提示已复制', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  const successSpy = vi.spyOn(toast, 'success').mockImplementation(() => 'toast-id')

  await openSecretDialog()
  fireEvent.click(screen.getByRole('button', { name: '复制 appSecret' }))

  await waitFor(() => expect(writeText).toHaveBeenCalledWith('plaintext-secret-abc'))
  expect(successSpy).toHaveBeenCalledWith('已复制')
})
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd web && npx vitest run src/pages/Applications.test.tsx`
Expected: 全部 PASS。

- [ ] **Step 5: 类型检查 + 全量测试**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: 全部 PASS。

- [ ] **Step 6: Commit**

```bash
git add web/src/pages/Applications.tsx web/src/pages/Applications.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): 应用列表页改用全局当前应用，渲染 ApplicationSettings

卡片从 Link 改成 button，点击即设为当前应用（不再导航）；新建应用
成功后自动选中新应用；卡片下方渲染当前应用的 ApplicationSettings。
ApplicationDetail.tsx 从此没有任何 UI 入口，Task 7 正式删除它。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: 删除 ApplicationDetail，清理面包屑/outlet-context 机制

**Files:**
- Delete: `web/src/pages/ApplicationDetail.tsx`
- Delete: `web/src/pages/ApplicationDetail.test.tsx`
- Modify: `web/src/routes.tsx`
- Modify: `web/src/components/Layout.tsx`
- Modify: `web/src/components/Layout.test.tsx`
- Modify: `web/src/lib/breadcrumb.ts`
- Modify: `web/src/lib/breadcrumb.test.ts`

**Interfaces:**
- `buildBreadcrumb` 签名从 `(pathname: string, appName: string | null)` 简化成 `(pathname: string)`——没有任何路由还需要动态实体名了。
- `LayoutOutletContext`/`setCrumbLabel`/`<Outlet context={...}>` 全部移除，`<Outlet />` 不再传 `context`。

- [ ] **Step 1: 删除 ApplicationDetail 及其测试**

```bash
cd web
rm src/pages/ApplicationDetail.tsx src/pages/ApplicationDetail.test.tsx
```

- [ ] **Step 2: routes.tsx 去掉 ApplicationDetail**

把 `web/src/routes.tsx` 顶部的：

```tsx
import Applications from '@/pages/Applications'
import ApplicationDetail from '@/pages/ApplicationDetail'
import Permissions from '@/pages/Permissions'
```

改成：

```tsx
import Applications from '@/pages/Applications'
import Permissions from '@/pages/Permissions'
```

把：

```tsx
        <Route path="/applications" element={<Applications />} />
        <Route path="/applications/:id" element={<ApplicationDetail />} />
        <Route path="/users" element={<Users />} />
```

改成：

```tsx
        <Route path="/applications" element={<Applications />} />
        <Route path="/users" element={<Users />} />
```

- [ ] **Step 3: 重写 buildBreadcrumb**

把 `web/src/lib/breadcrumb.ts` 整个替换成：

```ts
export interface BreadcrumbSegment {
  label: string
  to?: string
}

/**
 * 根据当前路径拼面包屑段。只覆盖 routes.tsx 里实际存在的路由形状；
 * 顶级词条的文案要跟 Layout.tsx 侧边栏的导航标签保持一致。未知路径
 * 回退成一段「fp」，不抛错。
 */
export function buildBreadcrumb(pathname: string): BreadcrumbSegment[] {
  const parts = pathname.split('/').filter(Boolean)

  if (parts[0] === 'applications') return [{ label: '应用列表' }]

  if (parts[0] === 'users') {
    if (parts.length === 1) return [{ label: '用户管理' }]
    return [{ label: '用户管理', to: '/users' }, { label: '用户详情' }]
  }

  if (parts[0] === 'roles') {
    if (parts.length === 1) return [{ label: '角色管理' }]
    return [{ label: '角色管理', to: '/roles' }, { label: '角色详情' }]
  }

  if (parts[0] === 'permissions') return [{ label: '权限管理' }]

  if (parts[0] === 'config') {
    if (parts.length === 1) return [{ label: '配置中心' }]
    if (parts[1] === 'versions') return [{ label: '配置中心', to: '/config' }, { label: '版本历史' }]
  }

  return [{ label: 'fp' }]
}
```

- [ ] **Step 4: 重写 breadcrumb.test.ts**

把 `web/src/lib/breadcrumb.test.ts` 整个替换成：

```ts
import { test, expect } from 'vitest'
import { buildBreadcrumb } from './breadcrumb'

test('应用列表页只有一段面包屑', () => {
  expect(buildBreadcrumb('/applications')).toEqual([{ label: '应用列表' }])
})

test('用户和角色相关路径', () => {
  expect(buildBreadcrumb('/users')).toEqual([{ label: '用户管理' }])
  expect(buildBreadcrumb('/users/u1')).toEqual([
    { label: '用户管理', to: '/users' },
    { label: '用户详情' },
  ])
  expect(buildBreadcrumb('/roles')).toEqual([{ label: '角色管理' }])
  expect(buildBreadcrumb('/roles/r1')).toEqual([
    { label: '角色管理', to: '/roles' },
    { label: '角色详情' },
  ])
})

test('权限管理和配置中心/版本历史', () => {
  expect(buildBreadcrumb('/permissions')).toEqual([{ label: '权限管理' }])
  expect(buildBreadcrumb('/config')).toEqual([{ label: '配置中心' }])
  expect(buildBreadcrumb('/config/versions')).toEqual([
    { label: '配置中心', to: '/config' },
    { label: '版本历史' },
  ])
})

test('未知路径回退成一段「fp」，不抛错', () => {
  expect(buildBreadcrumb('/unknown')).toEqual([{ label: 'fp' }])
  expect(buildBreadcrumb('/')).toEqual([{ label: 'fp' }])
})
```

- [ ] **Step 5: 清理 Layout.tsx 的 outlet-context 机制**

把 `web/src/components/Layout.tsx` 顶部的：

```tsx
import { useEffect, useMemo, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router'
```

改成：

```tsx
import { NavLink, Outlet, useLocation } from 'react-router'
```

把：

```tsx
/** 详情类子页面（目前只有 ApplicationDetail）用它把已加载到的实体名报给面包屑。 */
export interface LayoutOutletContext {
  setCrumbLabel: (label: string | null) => void
}

export default function Layout() {
  const { username, logout } = useAuth()
  const location = useLocation()
  const [crumbLabel, setCrumbLabel] = useState<string | null>(null)

  // 路由一变先清空：不这样做的话，从「应用详情」跳到「用户列表」时，
  // 面包屑会在新页面挂载前的一瞬间还顶着上一个应用的名字。
  useEffect(() => {
    setCrumbLabel(null)
  }, [location.pathname])

  const segments = buildBreadcrumb(location.pathname, crumbLabel)
  // 避免每次 Layout 重渲染都创建新对象：否则依赖它的子页面 effect
  // （比如 ApplicationDetail 那个）会跟着不必要地重新触发 cleanup+执行。
  const outletContext = useMemo<LayoutOutletContext>(() => ({ setCrumbLabel }), [])
```

改成：

```tsx
export default function Layout() {
  const { username, logout } = useAuth()
  const location = useLocation()
  const segments = buildBreadcrumb(location.pathname)
```

把：

```tsx
        <div className="min-w-0 flex-1 p-6">
          <Outlet context={outletContext} />
        </div>
```

改成：

```tsx
        <div className="min-w-0 flex-1 p-6">
          <Outlet />
        </div>
```

- [ ] **Step 6: 重写 Layout.test.tsx**

把 `web/src/components/Layout.test.tsx` 整个替换成：

```tsx
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, within, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import Layout from './Layout'
import { CurrentAppProvider } from '@/lib/current-app'

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ username: 'alice', logout: vi.fn() }),
}))

afterEach(() => vi.unstubAllGlobals())

function stubEmptyApplications() {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify([]), { status: 200 })))
}

function renderLayout(initialEntries: string[]) {
  stubEmptyApplications()
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <CurrentAppProvider>
        <Routes>
          <Route element={<Layout />}>
            <Route path="/applications" element={<div>应用页内容</div>} />
            <Route path="/users" element={<div>用户页内容</div>} />
          </Route>
        </Routes>
      </CurrentAppProvider>
    </MemoryRouter>,
  )
}

test('渲染五个导航项，当前用户名、面包屑和页面内容都显示', async () => {
  renderLayout(['/applications'])
  // shadcn 的 BreadcrumbPage 本身也带 role="link"（aria-disabled，标记当前页），
  // /applications 这种单段路径下面包屑文案和侧边栏导航项现在都是"应用列表"，
  // 会重名，所以把导航项的查询范围限定在侧边栏导航列表（data-sidebar="menu"）内。
  const navMenu = document.querySelector('[data-sidebar="menu"]') as HTMLElement
  expect(within(navMenu).getByRole('link', { name: /应用列表/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /用户管理/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /角色管理/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /权限管理/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /配置中心/ })).toBeTruthy()
  expect(screen.getByText('alice')).toBeTruthy()
  expect(screen.getByText('应用页内容')).toBeTruthy()
  await waitFor(() => expect(screen.getByText('还没有应用')).toBeTruthy())
})

test('面包屑随路由变化，用户列表页展示"用户管理"', () => {
  renderLayout(['/users'])
  expect(screen.getByText('用户页内容')).toBeTruthy()
  // 导航栏里也有一个"用户管理"链接，现在面包屑和导航标签统一了，这里
  // 只确认至少多渲染出一个"用户管理"文案，不精确匹配具体 DOM 节点。
  expect(screen.getAllByText('用户管理').length).toBeGreaterThanOrEqual(2)
})

test('点击折叠按钮后侧边栏进入 collapsed 状态', () => {
  renderLayout(['/applications'])
  const trigger = screen.getByRole('button', { name: 'Toggle Sidebar' })
  fireEvent.click(trigger)
  expect(document.querySelector('[data-slot="sidebar"][data-state="collapsed"]')).toBeTruthy()
})
```

- [ ] **Step 7: 运行测试确认通过**

Run: `cd web && npx vitest run src/components/Layout.test.tsx src/lib/breadcrumb.test.ts`
Expected: 全部 PASS。

- [ ] **Step 8: 类型检查 + 全量测试**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: 全部 PASS（`ApplicationDetail.tsx`/`.test.tsx` 已删除，不会再出现在测试文件列表里）。

- [ ] **Step 9: Commit**

```bash
git add -A web/src/pages/ApplicationDetail.tsx web/src/pages/ApplicationDetail.test.tsx \
  web/src/routes.tsx web/src/components/Layout.tsx web/src/components/Layout.test.tsx \
  web/src/lib/breadcrumb.ts web/src/lib/breadcrumb.test.ts
git commit -m "$(cat <<'EOF'
refactor(web): 删除 ApplicationDetail，清理面包屑/outlet-context 机制

ApplicationDetail.tsx 已经没有任何 UI 入口（Task 6 起卡片点击只做
"设为当前应用"），正式删除；连带删除只为它存在的 LayoutOutletContext/
setCrumbLabel 机制，buildBreadcrumb 简化成单参数、按拍平后的路由给
静态文案，面包屑顶级词条统一成侧边栏导航标签。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: 全量验证

**Files:** 无新增/修改文件（纯验证任务）。

- [ ] **Step 1: 类型检查**

Run: `cd web && npx tsc -b --noEmit`
Expected: 无错误。

- [ ] **Step 2: 跑全量测试**

Run: `cd web && npx vitest run`
Expected: 全部 PASS，包含 Task 1-7 新增/修改的所有测试文件，且 `ApplicationDetail.test.tsx` 已经不在文件列表里。

- [ ] **Step 3: lint**

Run: `cd web && npm run lint`
Expected: 无新增错误（重点检查 `Applications.tsx`/`ConfigCenter.tsx`/`ConfigVersions.tsx`/`Layout.tsx` 删掉的 import 有没有残留未使用的引用）。

- [ ] **Step 4: build**

Run: `cd web && npm run build`
Expected: 构建成功，无 TypeScript 报错。

- [ ] **Step 5: 手动过一遍主流程（若本机有 fp 后端在跑，登录后验证；没有就跳过并在报告里说明跳过原因，不算失败）**

- 新建一个应用 → 自动成为当前应用（左上角下拉和应用列表页设置区都显示它）。
- 左上角下拉切到另一个应用 → 应用列表页的设置区、权限管理页、配置中心页的内容都跟着变成那个应用的数据。
- 刷新页面 → 当前应用记忆还在（localStorage）。
- 侧边栏 5 个导航项图标、文案都正确；折叠/展开正常。
- 一个应用都没有时（如果测试环境方便造这个场景）：应用列表页显示"还没有应用"，权限管理/配置中心页显示"还没有应用，请先在「应用列表」创建一个"，不崩溃。

- [ ] **Step 6: 确认后端目录没有被动到**

Run: `cd /d/fp && git diff --stat 87d95b6..HEAD -- internal cmd proto` （`87d95b6` 换成这个计划分支实际的起点提交）
Expected: 空输出。

- [ ] **Step 7: 确认没有遗留的临时改动**

Run: `cd /d/fp && git status --porcelain`
Expected: 干净。
