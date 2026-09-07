# fp web 后台 UI 主题与布局重做 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `web/`（React 19 + Tailwind v4 + shadcn `base-nova` 风格 + base-ui + next-themes）后台从纯灰度、无图标的简陋布局，改成四套可切换 CSS 变量配色 + 标准图标侧边栏/顶栏骨架，Applications 页改卡片网格，不改任何后端接口或业务逻辑。

**Architecture:** 主题色只动 CSS 变量（`[data-theme="x"]` 属性选择器 + 已有的 `.dark` 类叠加），不碰组件代码；侧边栏/顶栏用 shadcn 官方 `sidebar` 区块（base-ui 版）重写 `Layout.tsx`；面包屑走一个纯函数 `buildBreadcrumb(pathname, extraLabel)`，应用名通过 `Outlet` context 由 `ApplicationDetail.tsx` 单向上报，不新增网络请求。

**Tech Stack:** React 19、react-router 8、shadcn（`base-nova` 风格，组件基于 `@base-ui/react` 而非 Radix，用 `render` prop 而不是 `asChild`）、Tailwind v4、`next-themes`、`lucide-react`、vitest + @testing-library/react。

## Global Constraints

- 不修改 `internal/`、`cmd/`、`proto/` 下的任何后端代码，不新增/修改后端接口。
- 不引入 `lucide-react`（已装）之外的新图标库，不引入状态管理库。
- 新装 shadcn 组件统一从 `@/lib/utils` 导入 `cn`（项目现有约定），**不use** shadcn CLI 这次生成时默认引入的独立 `cn` npm 包（详见 Task 4）。
- `ApplicationDetail` / `RoleDetail` / `UserDetail` / `ConfigCenter` / `ConfigVersions` / `Login` 内部表单和业务逻辑不动；`ApplicationDetail.tsx` 只新增一处上报面包屑文案的 `useEffect`（Task 5）。
- 现有测试文件（`Users.test.tsx`、`Applications.test.tsx`、`Roles.test.tsx` 等）改完必须全绿，不允许为了让测试通过而删测试。
- 每个任务完成后跑一次 `npx tsc -b --noEmit` 确认没有类型错误（比等到最后一个任务再统一检查更快定位问题）。

---

## 背景排雷（写计划前实测确认，执行时不用重新踩一遍）

1. **`next-themes` 目前没有真正接上。** 依赖已装，`src/components/ui/sonner.tsx` 已经在用 `useTheme()`，但 `src/main.tsx` 从没渲染过 `<ThemeProvider>`——`useTheme()` 落到默认 context，`resolvedTheme` 恒为 `undefined`。Task 1 会把这个 provider 接上，顺带修好这个哑掉的行为。
2. **jsdom 没有 `window.matchMedia`。** shadcn 的 `useIsMobile`（`sidebar` 区块依赖它）和 `next-themes` 的 `enableSystem` 都会调用它，不 mock 的话任何渲染 `<Sidebar>` 的测试直接抛 `TypeError: window.matchMedia is not a function`。Task 4 会在 `src/test-setup.ts` 里补一个最小 mock。
3. **`npx shadcn@latest add` 在这个仓库会触发"文件已存在，是否覆盖"的交互式提示**（`separator.tsx`/`button.tsx`/`input.tsx` 之前已经装过），非交互环境下会卡住。用 `yes n | npx --yes shadcn@latest add ...` 让它对所有"是否覆盖"一律回答"否"，已经实测跑通。
4. **这个仓库这一版的 shadcn CLI 会额外装一个叫 `cn` 的 npm 包**（`cn@^0.2.6`，`clsx`+`tailwind-merge` 的等价替代），新生成的组件文件里 `import { cn } from "cn"`，而不是项目一直在用的 `@/lib/utils`。两者功能等价（实测读过 `cn` 包源码：就是 `wrapClsx(twMerge, clsx)`），但同一个仓库里两套 `cn` 是没有必要的不一致，Task 4 会把新文件的 import 改回 `@/lib/utils` 并卸载这个多余依赖。
5. **`base-nova` 风格的组件用 base-ui 的 `render` prop组合，不是 Radix 的 `asChild`。** 比如 `<DialogPrimitive.Close render={<Button variant="ghost" />}><XIcon /></DialogPrimitive.Close>`（见现有 `dialog.tsx`）。新写的 `SidebarMenuButton`/`BreadcrumbLink`/`DropdownMenuTrigger` 用法要照这个模式，不是 `asChild`。
6. **base-ui 的 `Select` 选项只在先收到 `pointerdown` 再收到 `click` 时才会真正选中**（`allowMouseSelectionRef` 只由 `pointerdown` 置位，纯 `fireEvent.click` 会被当成非真实鼠标点击忽略）——`ConfigCenter.test.tsx` 里已经踩过并注释过这个坑，Task 2 的测试要照抄同样的 `fireEvent.pointerDown` + `fireEvent.click` 顺序。
7. **`TableRow` 已经自带 `hover:bg-muted/50`。** 设计文档里提到的"表格行 hover 态"其实已经实现，不需要额外改动。

---

### Task 1: 接入 next-themes ThemeProvider + 明暗模式切换按钮

**Files:**
- Modify: `web/src/main.tsx`
- Create: `web/src/components/ThemeModeToggle.tsx`
- Test: `web/src/components/ThemeModeToggle.test.tsx`

**Interfaces:**
- Produces: `export default function ThemeModeToggle(): JSX.Element`，无 props，供 Task 4 的 `Layout.tsx` 直接渲染。

- [ ] **Step 1: 写 ThemeModeToggle 的测试（先写会失败的测试）**

创建 `web/src/components/ThemeModeToggle.test.tsx`：

```tsx
import { test, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ThemeModeToggle from './ThemeModeToggle'

test('渲染明暗切换按钮，点击不抛错', () => {
  render(<ThemeModeToggle />)
  const button = screen.getByRole('button', { name: '切换明暗模式' })
  fireEvent.click(button)
  expect(button).toBeTruthy()
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd web && npx vitest run src/components/ThemeModeToggle.test.tsx`
Expected: FAIL，报 `Failed to resolve import "./ThemeModeToggle"` 或找不到模块。

- [ ] **Step 3: 创建 ThemeModeToggle.tsx**

```tsx
import { Moon, Sun } from 'lucide-react'
import { useTheme } from 'next-themes'
import { Button } from '@/components/ui/button'

export default function ThemeModeToggle() {
  const { resolvedTheme, setTheme } = useTheme()

  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label="切换明暗模式"
      className="relative"
      onClick={() => setTheme(resolvedTheme === 'dark' ? 'light' : 'dark')}
    >
      <Sun className="h-[1.2rem] w-[1.2rem] scale-100 rotate-0 transition-all dark:scale-0 dark:-rotate-90" />
      <Moon className="absolute h-[1.2rem] w-[1.2rem] scale-0 rotate-90 transition-all dark:scale-100 dark:rotate-0" />
    </Button>
  )
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd web && npx vitest run src/components/ThemeModeToggle.test.tsx`
Expected: PASS

- [ ] **Step 5: 接入 main.tsx，让 next-themes 真正生效**

把 `web/src/main.tsx` 改成：

```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { ThemeProvider } from 'next-themes'
import { Toaster } from '@/components/ui/sonner'
import { AuthProvider } from '@/lib/auth'
import AppRoutes from '@/routes'
import './index.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
      <BrowserRouter>
        <AuthProvider>
          <AppRoutes />
          <Toaster richColors position="top-right" />
        </AuthProvider>
      </BrowserRouter>
    </ThemeProvider>
  </StrictMode>,
)
```

- [ ] **Step 6: 类型检查 + 跑全量测试确认没有回归**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: 全部 PASS（`main.tsx` 不在任何测试的 import 链上，这一步主要是确认 `ThemeModeToggle` 没有引入类型错误）。

- [ ] **Step 7: Commit**

```bash
git add web/src/main.tsx web/src/components/ThemeModeToggle.tsx web/src/components/ThemeModeToggle.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): 接入 next-themes 并新增明暗模式切换按钮

main.tsx 之前从未渲染 ThemeProvider，sonner.tsx 里的 useTheme() 一直
拿到的是空 context——现在真正接上，顺带加一个可点击的明暗切换按钮。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 四套配色预设（CSS 变量）+ 主题色切换器

**Files:**
- Modify: `web/index.html`
- Modify: `web/src/index.css`
- Create: `web/src/components/ThemeColorSwitcher.tsx`
- Test: `web/src/components/ThemeColorSwitcher.test.tsx`

**Interfaces:**
- Produces: `export type ThemeColor = 'blue' | 'violet' | 'emerald' | 'mono'`；`export default function ThemeColorSwitcher(): JSX.Element`（Task 4 的 `Layout.tsx` 会渲染它）；localStorage key `fp-ui-theme`；`<html data-theme="...">` 属性，四个取值同 `ThemeColor`。
- Consumes: 已有的 `Select`/`SelectTrigger`/`SelectContent`/`SelectItem`/`SelectValue`（`@/components/ui/select`，已装，不需要新装依赖）。

- [ ] **Step 1: 改 `index.html`，加防闪烁的内联脚本**

把 `web/index.html` 改成：

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <link rel="icon" type="image/svg+xml" href="/favicon.svg" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>fp 管理控制台</title>
    <script>
      // 在任何 CSS/JS bundle 加载前，把持久化过的配色预设写回 <html
      // data-theme>——同步执行，避免出现"先看到默认配色、bundle 加载完
      // 才跳到用户上次选的配色"这一下闪烁。next-themes 自己的 .dark 类
      // 切换不需要这段脚本处理，它在 ThemeProvider 挂载时用 useLayoutEffect
      // 同步写 class，在 CSR 应用里本来就没有 SSR 场景下那种水合闪烁问题。
      ;(function () {
        var KEY = 'fp-ui-theme'
        var THEMES = ['blue', 'violet', 'emerald', 'mono']
        var saved = null
        try {
          saved = localStorage.getItem(KEY)
        } catch (e) {}
        var theme = THEMES.indexOf(saved) !== -1 ? saved : 'blue'
        document.documentElement.setAttribute('data-theme', theme)
      })()
    </script>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

- [ ] **Step 2: 把 `index.css` 里跟主色相关的变量拆成四套 `[data-theme]` 预设**

当前 `web/src/index.css` 的 `:root` 块里有 `--primary`/`--primary-foreground`/`--ring`/`--sidebar-primary`/`--sidebar-primary-foreground`/`--sidebar-ring`/`--chart-1` 这 7 个变量，`.dark` 块里也有对应的深色版本。把这 7 个变量**从 `:root` 和 `.dark` 里删掉**，改成下面四套 `[data-theme="..."]` 块（浅色）和 `[data-theme="..."].dark` 块（深色），插在原来 `.dark { ... }` 块结束的 `}` 之后、`@layer base` 之前：

```css
[data-theme="blue"] {
  --primary: oklch(0.546 0.215 262.9);
  --primary-foreground: oklch(0.98 0.01 259);
  --ring: oklch(0.546 0.215 262.9);
  --sidebar-primary: oklch(0.546 0.215 262.9);
  --sidebar-primary-foreground: oklch(0.98 0.01 259);
  --sidebar-ring: oklch(0.546 0.215 262.9);
  --chart-1: oklch(0.546 0.215 262.9);
}
[data-theme="blue"].dark {
  --primary: oklch(0.72 0.16 259);
  --primary-foreground: oklch(0.18 0.04 259);
  --ring: oklch(0.72 0.16 259);
  --sidebar-primary: oklch(0.72 0.16 259);
  --sidebar-primary-foreground: oklch(0.18 0.04 259);
  --sidebar-ring: oklch(0.72 0.16 259);
  --chart-1: oklch(0.72 0.16 259);
}

[data-theme="violet"] {
  --primary: oklch(0.556 0.233 293.5);
  --primary-foreground: oklch(0.98 0.01 293);
  --ring: oklch(0.556 0.233 293.5);
  --sidebar-primary: oklch(0.556 0.233 293.5);
  --sidebar-primary-foreground: oklch(0.98 0.01 293);
  --sidebar-ring: oklch(0.556 0.233 293.5);
  --chart-1: oklch(0.556 0.233 293.5);
}
[data-theme="violet"].dark {
  --primary: oklch(0.75 0.16 293);
  --primary-foreground: oklch(0.18 0.05 293);
  --ring: oklch(0.75 0.16 293);
  --sidebar-primary: oklch(0.75 0.16 293);
  --sidebar-primary-foreground: oklch(0.18 0.05 293);
  --sidebar-ring: oklch(0.75 0.16 293);
  --chart-1: oklch(0.75 0.16 293);
}

[data-theme="emerald"] {
  --primary: oklch(0.6 0.14 162.5);
  --primary-foreground: oklch(0.98 0.01 162);
  --ring: oklch(0.6 0.14 162.5);
  --sidebar-primary: oklch(0.6 0.14 162.5);
  --sidebar-primary-foreground: oklch(0.98 0.01 162);
  --sidebar-ring: oklch(0.6 0.14 162.5);
  --chart-1: oklch(0.6 0.14 162.5);
}
[data-theme="emerald"].dark {
  --primary: oklch(0.75 0.14 162);
  --primary-foreground: oklch(0.17 0.04 162);
  --ring: oklch(0.75 0.14 162);
  --sidebar-primary: oklch(0.75 0.14 162);
  --sidebar-primary-foreground: oklch(0.17 0.04 162);
  --sidebar-ring: oklch(0.75 0.14 162);
  --chart-1: oklch(0.75 0.14 162);
}

/* 现有的纯灰度配色，原样保留成一个可选预设，不再是唯一的默认值。 */
[data-theme="mono"] {
  --primary: oklch(0.205 0 0);
  --primary-foreground: oklch(0.985 0 0);
  --ring: oklch(0.708 0 0);
  --sidebar-primary: oklch(0.205 0 0);
  --sidebar-primary-foreground: oklch(0.985 0 0);
  --sidebar-ring: oklch(0.708 0 0);
  --chart-1: oklch(0.87 0 0);
}
[data-theme="mono"].dark {
  --primary: oklch(0.922 0 0);
  --primary-foreground: oklch(0.205 0 0);
  --ring: oklch(0.556 0 0);
  /* 原来这里的深色 --sidebar-primary 一直是 oklch(0.488 0.243 264.376)
     ——一个和其余纯灰度变量对不上的蓝色残留值（大概率是脚手架默认主题
     没清干净）。mono 预设要求深浅色都是纯灰度，这里一并修正。 */
  --sidebar-primary: oklch(0.922 0 0);
  --sidebar-primary-foreground: oklch(0.205 0 0);
  --sidebar-ring: oklch(0.556 0 0);
  --chart-1: oklch(0.87 0 0);
}
```

- [ ] **Step 3: 写 ThemeColorSwitcher 的测试**

创建 `web/src/components/ThemeColorSwitcher.test.tsx`：

```tsx
import { test, expect, afterEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ThemeColorSwitcher from './ThemeColorSwitcher'

afterEach(() => {
  document.documentElement.removeAttribute('data-theme')
  localStorage.clear()
})

test('选择配色后写入 data-theme 属性和 localStorage', async () => {
  render(<ThemeColorSwitcher />)

  const trigger = screen.getByRole('combobox', { name: '切换主题色' })
  // base-ui 的 Select 选项只在先收到 pointerdown 再收到 click 时才会选中
  // （纯 click 会被内部 allowMouseSelectionRef 判定成非真实鼠标点击而忽略），
  // 同 ConfigCenter.test.tsx 的先例手动补上同样的顺序。
  fireEvent.pointerDown(trigger)
  fireEvent.click(trigger)
  const option = await screen.findByRole('option', { name: '紫罗兰' })
  fireEvent.pointerDown(option)
  fireEvent.click(option)

  expect(document.documentElement.getAttribute('data-theme')).toBe('violet')
  expect(localStorage.getItem('fp-ui-theme')).toBe('violet')
})

test('挂载时读取 <html data-theme> 上已有的预设作为初始选中值', () => {
  document.documentElement.setAttribute('data-theme', 'emerald')
  render(<ThemeColorSwitcher />)
  expect(screen.getByText('翡翠绿')).toBeTruthy()
})
```

- [ ] **Step 4: 运行测试确认失败**

Run: `cd web && npx vitest run src/components/ThemeColorSwitcher.test.tsx`
Expected: FAIL，找不到 `./ThemeColorSwitcher` 模块。

- [ ] **Step 5: 创建 ThemeColorSwitcher.tsx**

```tsx
import { useEffect, useState } from 'react'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

export const THEME_COLORS = [
  { value: 'blue', label: '科技蓝' },
  { value: 'violet', label: '紫罗兰' },
  { value: 'emerald', label: '翡翠绿' },
  { value: 'mono', label: '极简黑白' },
] as const

export type ThemeColor = (typeof THEME_COLORS)[number]['value']

const STORAGE_KEY = 'fp-ui-theme'
const DEFAULT_THEME: ThemeColor = 'blue'

function isThemeColor(v: string | null): v is ThemeColor {
  return THEME_COLORS.some((t) => t.value === v)
}

/** 和 index.html 里防闪烁脚本读取的是同一个来源，保证初始值一致。 */
function currentTheme(): ThemeColor {
  const attr = document.documentElement.getAttribute('data-theme')
  return isThemeColor(attr) ? attr : DEFAULT_THEME
}

export default function ThemeColorSwitcher() {
  const [theme, setThemeState] = useState<ThemeColor>(DEFAULT_THEME)

  useEffect(() => {
    setThemeState(currentTheme())
  }, [])

  return (
    <Select
      value={theme}
      onValueChange={(v) => {
        if (!isThemeColor(v)) return
        document.documentElement.setAttribute('data-theme', v)
        localStorage.setItem(STORAGE_KEY, v)
        setThemeState(v)
      }}
    >
      <SelectTrigger aria-label="切换主题色" className="w-28">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {THEME_COLORS.map((t) => (
          <SelectItem key={t.value} value={t.value}>
            {t.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
```

- [ ] **Step 6: 运行测试确认通过**

Run: `cd web && npx vitest run src/components/ThemeColorSwitcher.test.tsx`
Expected: PASS

- [ ] **Step 7: 类型检查 + 全量测试**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: 全部 PASS。

- [ ] **Step 8: Commit**

```bash
git add web/index.html web/src/index.css web/src/components/ThemeColorSwitcher.tsx web/src/components/ThemeColorSwitcher.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): 四套可切换配色预设 + 主题色切换器

primary/ring/sidebar-primary/chart-1 这几个跟主色相关的变量拆成
[data-theme="blue|violet|emerald|mono"] 四套预设，深浅色都配齐；
index.html 加一段同步内联脚本避免刷新闪烁；顺带修正 mono 预设深色下
一直残留的蓝色 sidebar-primary。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 面包屑纯函数 `buildBreadcrumb`

**Files:**
- Create: `web/src/lib/breadcrumb.ts`
- Test: `web/src/lib/breadcrumb.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export interface BreadcrumbSegment {
    label: string
    to?: string
  }
  export function buildBreadcrumb(pathname: string, appName: string | null): BreadcrumbSegment[]
  ```
  Task 4 的 `Layout.tsx` 会调用它渲染面包屑。

- [ ] **Step 1: 写测试**

创建 `web/src/lib/breadcrumb.test.ts`：

```ts
import { test, expect } from 'vitest'
import { buildBreadcrumb } from './breadcrumb'

test('应用列表页只有一段面包屑', () => {
  expect(buildBreadcrumb('/applications', null)).toEqual([{ label: '应用' }])
})

test('应用详情页展示应用名，取不到名字时退化成"应用详情"', () => {
  expect(buildBreadcrumb('/applications/app-1', 'Acme')).toEqual([
    { label: '应用', to: '/applications' },
    { label: 'Acme' },
  ])
  expect(buildBreadcrumb('/applications/app-1', null)).toEqual([
    { label: '应用', to: '/applications' },
    { label: '应用详情' },
  ])
})

test('配置中心和版本历史页拼出完整层级', () => {
  expect(buildBreadcrumb('/applications/app-1/config', 'Acme')).toEqual([
    { label: '应用', to: '/applications' },
    { label: 'Acme', to: '/applications/app-1' },
    { label: '配置中心' },
  ])
  expect(buildBreadcrumb('/applications/app-1/config/versions', 'Acme')).toEqual([
    { label: '应用', to: '/applications' },
    { label: 'Acme', to: '/applications/app-1' },
    { label: '配置中心', to: '/applications/app-1/config' },
    { label: '版本历史' },
  ])
})

test('用户和角色相关路径', () => {
  expect(buildBreadcrumb('/users', null)).toEqual([{ label: '用户' }])
  expect(buildBreadcrumb('/users/u1', null)).toEqual([
    { label: '用户', to: '/users' },
    { label: '用户详情' },
  ])
  expect(buildBreadcrumb('/roles', null)).toEqual([{ label: '角色' }])
  expect(buildBreadcrumb('/roles/r1', null)).toEqual([
    { label: '角色', to: '/roles' },
    { label: '角色详情' },
  ])
})

test('未知路径回退成一段「fp」，不抛错', () => {
  expect(buildBreadcrumb('/unknown', null)).toEqual([{ label: 'fp' }])
  expect(buildBreadcrumb('/', null)).toEqual([{ label: 'fp' }])
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd web && npx vitest run src/lib/breadcrumb.test.ts`
Expected: FAIL，找不到 `./breadcrumb` 模块。

- [ ] **Step 3: 实现 buildBreadcrumb**

创建 `web/src/lib/breadcrumb.ts`：

```ts
export interface BreadcrumbSegment {
  label: string
  to?: string
}

/**
 * 根据当前路径拼面包屑段。只覆盖 routes.tsx 里实际存在的路由形状；
 * appName 是 ApplicationDetail.tsx 已经加载到的应用名（见 Layout.tsx 的
 * outlet context），拿不到时退化成「应用详情」这个占位文案，不在这里
 * 发任何请求。未知路径回退成一段「fp」，不抛错。
 */
export function buildBreadcrumb(pathname: string, appName: string | null): BreadcrumbSegment[] {
  const parts = pathname.split('/').filter(Boolean)

  if (parts[0] === 'applications') {
    if (parts.length === 1) return [{ label: '应用' }]
    const id = parts[1]
    const appLabel = appName ?? '应用详情'
    if (parts.length === 2) {
      return [{ label: '应用', to: '/applications' }, { label: appLabel }]
    }
    if (parts[2] === 'config') {
      const base: BreadcrumbSegment[] = [
        { label: '应用', to: '/applications' },
        { label: appLabel, to: `/applications/${id}` },
      ]
      if (parts.length === 3) return [...base, { label: '配置中心' }]
      if (parts[3] === 'versions') {
        return [...base, { label: '配置中心', to: `/applications/${id}/config` }, { label: '版本历史' }]
      }
    }
  }

  if (parts[0] === 'users') {
    if (parts.length === 1) return [{ label: '用户' }]
    return [{ label: '用户', to: '/users' }, { label: '用户详情' }]
  }

  if (parts[0] === 'roles') {
    if (parts.length === 1) return [{ label: '角色' }]
    return [{ label: '角色', to: '/roles' }, { label: '角色详情' }]
  }

  return [{ label: 'fp' }]
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd web && npx vitest run src/lib/breadcrumb.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/breadcrumb.ts web/src/lib/breadcrumb.test.ts
git commit -m "$(cat <<'EOF'
feat(web): 新增 buildBreadcrumb 纯函数

按当前路径拼面包屑段，应用详情/配置中心/版本历史三级路由需要的
应用名从外部传入（不在这里发请求），未知路径回退成「fp」不抛错。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 安装 shadcn sidebar/dropdown-menu/avatar/breadcrumb，重写 Layout.tsx

**Files:**
- Create（由 shadcn CLI 生成，随后手动修正 import）: `web/src/components/ui/sidebar.tsx`、`web/src/components/ui/dropdown-menu.tsx`、`web/src/components/ui/avatar.tsx`、`web/src/components/ui/breadcrumb.tsx`、`web/src/components/ui/tooltip.tsx`、`web/src/components/ui/sheet.tsx`、`web/src/components/ui/skeleton.tsx`、`web/src/hooks/use-mobile.ts`
- Modify: `web/src/components/Layout.tsx`（全量重写）
- Modify: `web/src/main.tsx`（加 `TooltipProvider`）
- Modify: `web/src/test-setup.ts`（加 `matchMedia` mock）
- Modify: `web/package.json` / `web/package-lock.json`（移除多余的 `cn` 依赖）
- Test: `web/src/components/Layout.test.tsx`

**Interfaces:**
- Consumes: `ThemeColorSwitcher`（Task 2）、`ThemeModeToggle`（Task 1）、`buildBreadcrumb`（Task 3）、`useAuth()`（已有，来自 `@/lib/auth`，返回 `{ username, logout }`）。
- Produces: `export interface LayoutOutletContext { setCrumbLabel: (label: string | null) => void }`，Task 5 的 `ApplicationDetail.tsx` 会通过 `useOutletContext<LayoutOutletContext | undefined>()` 消费它。

- [ ] **Step 1: 用 shadcn CLI 装组件**

Run:
```bash
cd web
yes n | npx --yes shadcn@latest add sidebar dropdown-menu avatar breadcrumb --yes
```
这条命令会连带装上 `sidebar` 依赖的 `sheet`/`skeleton`/`tooltip`（`separator` 已经装过，CLI 会问是否覆盖，`yes n |` 会自动回答"否"，保留仓库里已有的 `separator.tsx` 不被覆盖）。

Expected：命令结束后 `web/src/components/ui/` 下新增 `sidebar.tsx`、`dropdown-menu.tsx`、`avatar.tsx`、`breadcrumb.tsx`、`sheet.tsx`、`skeleton.tsx`、`tooltip.tsx`，`web/src/hooks/use-mobile.ts` 也会被创建。用 `git status --porcelain -- web` 确认这些文件是新增（`??`），`separator.tsx`/`button.tsx`/`input.tsx` 没有被覆盖（不在改动列表里）。

- [ ] **Step 2: 把新文件里的 `cn` 导入改回项目约定，卸载多余依赖**

这批 CLI 生成的文件会 `import { cn } from "cn"`（一个新装的独立 npm 包），项目里其余组件全部是 `import { cn } from "@/lib/utils"`。把新文件统一改过来：

Run（Windows Git Bash）：
```bash
cd web
grep -rl 'from "cn"' src/components/ui src/hooks | xargs sed -i 's/from "cn"/from "@\/lib\/utils"/'
npm uninstall cn
```

Expected：`grep -rl 'from "cn"' src/components/ui src/hooks` 之后不再有任何匹配；`web/package.json` 的 `dependencies` 里没有 `"cn"` 这一行。

- [ ] **Step 3: 在 test-setup.ts 里补 `window.matchMedia` mock**

把 `web/src/test-setup.ts` 改成：

```ts
// vitest 全局测试初始化。
//
// 项目刻意关闭了 vitest 的 globals（见 vite.config.ts 里的注释），每个测试
// 文件顶部都要显式 import { test, expect, ... } from 'vitest'。但这也意味着
// @testing-library/react 的自动清理失效了：它的 dist/index.js 在模块顶层用
// `typeof afterEach === 'function'` 探测全局作用域来决定要不要注册
// afterEach(cleanup)——globals 关闭后全局作用域里没有 afterEach，这个探测
// 恒为 false，组件测试之间不会自动 unmount。
//
// 后果很隐蔽：同一个测试文件里，前一个 test 渲染出的 DOM 会残留到下一个
// test，screen.getByTestId 之类的查询会因为"匹配到多个元素"而失败，
// 报错信息和真正的业务逻辑毫无关系，容易把人带偏去怀疑组件本身有 bug。
//
// 用这个 setup 文件手动补上同等效果：每个 test 结束后调用 cleanup()。
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

afterEach(() => {
  cleanup()
})

// jsdom 没有实现 window.matchMedia：shadcn 的 useIsMobile（sidebar 区块
// 内部用它判断是否切到移动端 Sheet 布局）和 next-themes 在 enableSystem
// 时都会调用它。不 mock 的话，任何渲染到 <Sidebar> 或被 <ThemeProvider>
// 包裹的组件一测试就抛 "window.matchMedia is not a function"，报错和真正
// 的业务逻辑毫无关系。固定返回 matches: false，等价于"不是移动端、不是
// 深色系统偏好"，测试环境统一按桌面浅色场景走。
if (!window.matchMedia) {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
}
```

- [ ] **Step 4: 写 Layout 的测试（先写会失败的测试）**

创建 `web/src/components/Layout.test.tsx`：

```tsx
import { test, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import Layout from './Layout'

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ username: 'alice', logout: vi.fn() }),
}))

function renderLayout(initialEntries: string[]) {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <Routes>
        <Route element={<Layout />}>
          <Route path="/applications" element={<div>应用页内容</div>} />
          <Route path="/users" element={<div>用户页内容</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  )
}

test('渲染三个导航项，当前用户名和页面内容都显示', () => {
  renderLayout(['/applications'])
  expect(screen.getByRole('link', { name: /应用/ })).toBeTruthy()
  expect(screen.getByRole('link', { name: /用户/ })).toBeTruthy()
  expect(screen.getByRole('link', { name: /角色/ })).toBeTruthy()
  expect(screen.getByText('alice')).toBeTruthy()
  expect(screen.getByText('应用页内容')).toBeTruthy()
})

test('面包屑随路由变化，用户列表页展示"用户"', () => {
  renderLayout(['/users'])
  expect(screen.getByText('用户页内容')).toBeTruthy()
  // 导航栏里也有一个"用户"链接，这里只确认面包屑区域至少多渲染出一个
  // "用户"文案，不去精确匹配具体 DOM 节点。
  expect(screen.getAllByText('用户').length).toBeGreaterThanOrEqual(2)
})

test('点击折叠按钮后侧边栏进入 collapsed 状态', () => {
  renderLayout(['/applications'])
  const trigger = screen.getByRole('button', { name: 'Toggle Sidebar' })
  fireEvent.click(trigger)
  expect(document.querySelector('[data-slot="sidebar"][data-state="collapsed"]')).toBeTruthy()
})
```

- [ ] **Step 5: 运行测试确认失败**

Run: `cd web && npx vitest run src/components/Layout.test.tsx`
Expected: FAIL（当前 `Layout.tsx` 还是旧版本，没有 `role="button" name="Toggle Sidebar"`，也不会渲染出符合新断言的结构）。

- [ ] **Step 6: 重写 Layout.tsx**

把 `web/src/components/Layout.tsx` 整个替换成：

```tsx
import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router'
import type { LucideIcon } from 'lucide-react'
import { AppWindow, LogOut, ShieldCheck, Users as UsersIcon } from 'lucide-react'
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
} from '@/components/ui/sidebar'
import ThemeColorSwitcher from '@/components/ThemeColorSwitcher'
import ThemeModeToggle from '@/components/ThemeModeToggle'
import { useAuth } from '@/lib/auth'
import { buildBreadcrumb } from '@/lib/breadcrumb'

const nav: { to: string; label: string; icon: LucideIcon }[] = [
  { to: '/applications', label: '应用', icon: AppWindow },
  { to: '/users', label: '用户', icon: UsersIcon },
  { to: '/roles', label: '角色', icon: ShieldCheck },
]

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

  return (
    <SidebarProvider>
      <Sidebar collapsible="icon">
        <SidebarHeader>
          <div className="flex h-8 items-center px-2 text-lg font-semibold">fp</div>
        </SidebarHeader>
        <SidebarContent>
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                {nav.map((n) => (
                  <SidebarMenuItem key={n.to}>
                    <SidebarMenuButton
                      render={<NavLink to={n.to} />}
                      isActive={location.pathname.startsWith(n.to)}
                      tooltip={n.label}
                    >
                      <n.icon />
                      <span>{n.label}</span>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>
      </Sidebar>
      <SidebarInset>
        <header className="sticky top-0 z-20 flex h-14 shrink-0 items-center justify-between gap-3 border-b bg-background/95 px-4 backdrop-blur supports-[backdrop-filter]:bg-background/60">
          <div className="flex items-center gap-2">
            <SidebarTrigger />
            <Breadcrumb>
              <BreadcrumbList>
                {segments.flatMap((s, i) => {
                  const item = (
                    <BreadcrumbItem key={`item-${i}`}>
                      {s.to ? (
                        <BreadcrumbLink render={<NavLink to={s.to} />}>{s.label}</BreadcrumbLink>
                      ) : (
                        <BreadcrumbPage>{s.label}</BreadcrumbPage>
                      )}
                    </BreadcrumbItem>
                  )
                  return i === 0 ? [item] : [<BreadcrumbSeparator key={`sep-${i}`} />, item]
                })}
              </BreadcrumbList>
            </Breadcrumb>
          </div>
          <div className="flex items-center gap-1">
            <ThemeColorSwitcher />
            <ThemeModeToggle />
            <DropdownMenu>
              <DropdownMenuTrigger render={<Button variant="ghost" className="gap-2 px-2" />}>
                <Avatar size="sm">
                  <AvatarFallback>{(username ?? '?').slice(0, 1).toUpperCase()}</AvatarFallback>
                </Avatar>
                <span className="text-sm">{username}</span>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuLabel>{username}</DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onClick={() => void logout()}>
                  <LogOut />
                  退出
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>
        <main className="min-w-0 flex-1 p-6">
          <Outlet context={{ setCrumbLabel } satisfies LayoutOutletContext} />
        </main>
      </SidebarInset>
    </SidebarProvider>
  )
}
```

- [ ] **Step 7: 把 TooltipProvider 接到 main.tsx**

`sidebar` 区块的折叠态图标提示依赖 `Tooltip`，CLI 装完会提示"记得用 `TooltipProvider` 包裹应用根节点"。把 `web/src/main.tsx` 改成：

```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { ThemeProvider } from 'next-themes'
import { TooltipProvider } from '@/components/ui/tooltip'
import { Toaster } from '@/components/ui/sonner'
import { AuthProvider } from '@/lib/auth'
import AppRoutes from '@/routes'
import './index.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
      <TooltipProvider>
        <BrowserRouter>
          <AuthProvider>
            <AppRoutes />
            <Toaster richColors position="top-right" />
          </AuthProvider>
        </BrowserRouter>
      </TooltipProvider>
    </ThemeProvider>
  </StrictMode>,
)
```

- [ ] **Step 8: 运行 Layout 测试确认通过**

Run: `cd web && npx vitest run src/components/Layout.test.tsx`
Expected: PASS

- [ ] **Step 9: 类型检查 + 全量测试**

Run: `cd web && npx tsc -b --noEmit && npx vitest run`
Expected: 全部 PASS。如果有其它页面测试因为找不到 `useAuth` mock 之外的原因失败，先看是不是 Step 2 的 `sed` 漏改了某个文件里的 `from "cn"`（`npx tsc -b --noEmit` 会先报出来）。

- [ ] **Step 10: Commit**

```bash
git add web/src/components/ui/sidebar.tsx web/src/components/ui/dropdown-menu.tsx \
  web/src/components/ui/avatar.tsx web/src/components/ui/breadcrumb.tsx \
  web/src/components/ui/tooltip.tsx web/src/components/ui/sheet.tsx \
  web/src/components/ui/skeleton.tsx web/src/hooks/use-mobile.ts \
  web/src/components/Layout.tsx web/src/components/Layout.test.tsx \
  web/src/main.tsx web/src/test-setup.ts web/package.json web/package-lock.json
git commit -m "$(cat <<'EOF'
feat(web): 用 shadcn sidebar 区块重写 Layout

图标+分组侧边栏（可折叠，Cmd/Ctrl+B）、粘性顶栏（面包屑+主题切换+
头像下拉菜单）替换掉原来的纯文字侧边栏。新装组件的 cn 导入统一改回
项目现有的 @/lib/utils；test-setup.ts 补 matchMedia mock（sidebar 的
useIsMobile 和 next-themes 都会调用，jsdom 没有原生实现）。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: ApplicationDetail 上报面包屑应用名

**Files:**
- Modify: `web/src/pages/ApplicationDetail.tsx`

**Interfaces:**
- Consumes: `LayoutOutletContext`（Task 4，从 `@/components/Layout` 导出）。

- [ ] **Step 1: 确认现状：`ApplicationDetail.test.tsx` 不经过 `Layout`，不受影响**

Run: `cd web && npx vitest run src/pages/ApplicationDetail.test.tsx`
Expected: PASS（改动前先跑一遍作为基线；这个测试文件直接渲染 `<Route path="/applications/:id" element={<ApplicationDetail />} />`，不经过 `Layout` 的 `<Outlet>`，所以 `useOutletContext()` 会拿到 `undefined`——下面的实现必须对此保持安全）。

- [ ] **Step 2: 在 ApplicationDetail.tsx 里接入面包屑上报**

在 `web/src/pages/ApplicationDetail.tsx` 顶部的 import 区块，把：

```tsx
import { Link, useParams } from 'react-router'
```

改成：

```tsx
import { useEffect } from 'react'
import { Link, useOutletContext, useParams } from 'react-router'
```

再加一行 import：

```tsx
import type { LayoutOutletContext } from '@/components/Layout'
```

然后把组件函数体里的：

```tsx
export default function ApplicationDetail() {
  const { id = '' } = useParams()
  const app = useResource(() => api.get<Application>(`/applications/${id}`), [id])
```

改成：

```tsx
export default function ApplicationDetail() {
  const { id = '' } = useParams()
  const app = useResource(() => api.get<Application>(`/applications/${id}`), [id])
  // 不是所有渲染场景都套着 Layout 的 <Outlet>（比如这个文件自己的单元
  // 测试就没有），拿不到 context 时 setCrumbLabel 是 undefined，下面全部
  // 用 ?. 兜底，不能假设它一定存在。
  const outlet = useOutletContext<LayoutOutletContext | undefined>()
  useEffect(() => {
    outlet?.setCrumbLabel(app.data?.name ?? null)
    return () => outlet?.setCrumbLabel(null)
  }, [outlet, app.data?.name])
```

（`useState` 那一行 `const [confirmingDisable, setConfirmingDisable] = useState(false)` 保持在原位置不动，只是现在它前面多了这一段 `useEffect`。）

- [ ] **Step 3: 运行测试确认没有回归**

Run: `cd web && npx vitest run src/pages/ApplicationDetail.test.tsx`
Expected: PASS（`useOutletContext()` 在没有父级 `Outlet` 提供 context 时返回 `undefined`，`outlet?.setCrumbLabel(...)` 是安全的 no-op，不会抛错）。

- [ ] **Step 4: 手动验证面包屑在真实路由下工作**

Run: `cd web && npm run dev`，登录后打开一个应用详情页，确认顶栏面包屑第二段显示的是应用的真实名字（不是"应用详情"占位文案）；再点进"配置中心"，确认面包屑第二段仍然是应用名（配置中心页面本身不加载应用实体，这一段会在离开应用详情页时被清空——这是已知的、设计文档里写明的降级行为，不是 bug）。

- [ ] **Step 5: 类型检查**

Run: `cd web && npx tsc -b --noEmit`
Expected: 无错误。

- [ ] **Step 6: Commit**

```bash
git add web/src/pages/ApplicationDetail.tsx
git commit -m "$(cat <<'EOF'
feat(web): ApplicationDetail 把应用名上报给 Layout 面包屑

通过 Outlet context 单向上报，不新增网络请求；context 拿不到时
（比如这个文件自己的单测）全部用 ?. 兜底，不会抛错。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Applications 列表页改卡片网格 + 状态语义色

**Files:**
- Create: `web/src/lib/status-badge.ts`
- Modify: `web/src/pages/Applications.tsx`
- Modify: `web/src/pages/Applications.test.tsx`（只新增一条测试，不改动已有测试）

**Interfaces:**
- Produces: `export const applicationStatusBadgeClassName: Record<ApplicationStatus, string>`（Task 7 会在同一个文件里追加 `userStatusBadgeClassName`）。

- [ ] **Step 1: 运行现有测试确认基线通过**

Run: `cd web && npx vitest run src/pages/Applications.test.tsx`
Expected: PASS（改动前的基线）。

- [ ] **Step 2: 创建 status-badge.ts**

```ts
import type { ApplicationStatus } from './types'

// 用 Tailwind 内置色板而不是主题变量：这是和主色调无关的"正常/异常"
// 语义状态色，四套主题预设切换时不应该跟着变。
const GREEN = 'border-transparent bg-green-100 text-green-800 dark:bg-green-500/15 dark:text-green-400'
const GRAY = 'border-transparent bg-muted text-muted-foreground'

export const applicationStatusBadgeClassName: Record<ApplicationStatus, string> = {
  ACTIVE: GREEN,
  DISABLED: GRAY,
}
```

保存为 `web/src/lib/status-badge.ts`。

- [ ] **Step 3: 把 Applications.tsx 的表格改成卡片网格**

在 `web/src/pages/Applications.tsx` 顶部，把：

```tsx
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatTime } from '@/lib/format'
import { applicationStatusLabels } from '@/lib/labels'
```

改成：

```tsx
import { AppWindow } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatTime } from '@/lib/format'
import { applicationStatusLabels } from '@/lib/labels'
import { applicationStatusBadgeClassName } from '@/lib/status-badge'
```

（`Table`/`TableBody`/`TableCell`/`TableHead`/`TableHeader`/`TableRow` 不再用到，整行删掉，避免 oxlint 报未使用的 import。）

把渲染部分：

```tsx
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
                      {applicationStatusLabels[a.status] ?? a.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{formatTime(a.createdAt)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
```

改成：

```tsx
      {apps.data && apps.data.length === 0 && (
        <p className="text-center text-sm text-muted-foreground">还没有应用</p>
      )}

      {apps.data && apps.data.length > 0 && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {apps.data.map((a) => (
            <Link
              key={a.id}
              to={`/applications/${a.id}`}
              className="group rounded-xl border bg-card p-4 transition-colors hover:border-primary/50 hover:bg-accent/30"
            >
              <div className="flex items-start justify-between gap-2">
                <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                  <AppWindow className="size-5" />
                </div>
                <Badge className={applicationStatusBadgeClassName[a.status]}>
                  {applicationStatusLabels[a.status] ?? a.status}
                </Badge>
              </div>
              <div className="mt-3 space-y-1">
                <div className="font-medium group-hover:underline group-hover:underline-offset-4">{a.name}</div>
                <div className="text-xs text-muted-foreground">{a.slug}</div>
              </div>
              <div className="mt-4 text-xs text-muted-foreground">创建于 {formatTime(a.createdAt)}</div>
            </Link>
          ))}
        </div>
      )}
```

（appId 那一列原来是纯文本展示，卡片网格里不再单独显示——它已经能在应用详情页看到；这属于视觉重排，appId 本身的数据和用法完全没变。）

- [ ] **Step 4: 运行已有测试确认没有回归**

Run: `cd web && npx vitest run src/pages/Applications.test.tsx`
Expected: 全部 PASS（这些测试都是按文本内容断言的，`screen.getByText('示例应用')`/`'demo'`/`'启用'`/`'还没有应用'`，跟卡片还是表格无关）。

- [ ] **Step 5: 追加一条新测试，验证状态徽标用上了语义色**

在 `web/src/pages/Applications.test.tsx` 末尾追加：

```tsx
test('启用状态的徽标使用绿色语义样式', async () => {
  stubFetchSequence(new Response(JSON.stringify([sampleApp]), { status: 200 }))

  render(
    <MemoryRouter>
      <Applications />
    </MemoryRouter>,
  )

  const badge = await waitFor(() => screen.getByText('启用'))
  expect(badge.className).toContain('bg-green-100')
})
```

- [ ] **Step 6: 运行测试确认通过**

Run: `cd web && npx vitest run src/pages/Applications.test.tsx`
Expected: 全部 PASS。

- [ ] **Step 7: 类型检查**

Run: `cd web && npx tsc -b --noEmit`
Expected: 无错误（确认删掉 Table 系列 import 之后没有遗漏的引用）。

- [ ] **Step 8: Commit**

```bash
git add web/src/lib/status-badge.ts web/src/pages/Applications.tsx web/src/pages/Applications.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): Applications 列表改卡片网格，状态徽标用语义色

对齐 shadcn-admin 参考站的 Apps 集成页风格；新建 status-badge.ts
存放和主题预设无关的语义状态色（绿=启用，灰=停用），Users 页后续
复用同一个文件。

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Users 列表页视觉打磨（搜索图标 + 状态语义色）

**Files:**
- Modify: `web/src/lib/status-badge.ts`
- Modify: `web/src/pages/Users.tsx`

**Interfaces:**
- Consumes: `applicationStatusBadgeClassName`（Task 6，同文件追加 `userStatusBadgeClassName`，两个 map 都保留导出）。

说明：`Roles.tsx` 没有筛选框，`Role` 类型也没有状态字段（只有 `key`/`name`/`parentId`/`createdAt`），设计文档里"筛选栏+状态徽标"这两条打磨项对它都不适用；`TableRow` 也已经自带 `hover:bg-muted/50`（见计划开头的排雷记录）。所以 `Roles.tsx` 在这一版**不需要改动**——它已经通过 Task 2 的主题变量和 Task 4 的 `Layout.tsx` 自动获得新配色和新顶栏，不用重复劳动。

- [ ] **Step 1: 运行现有测试确认基线通过**

Run: `cd web && npx vitest run src/pages/Users.test.tsx`
Expected: PASS。

- [ ] **Step 2: 在 status-badge.ts 里追加用户状态的语义色**

把 `web/src/lib/status-badge.ts` 改成：

```ts
import type { ApplicationStatus, UserStatus } from './types'

// 用 Tailwind 内置色板而不是主题变量：这是和主色调无关的"正常/异常"
// 语义状态色，四套主题预设切换时不应该跟着变。
const GREEN = 'border-transparent bg-green-100 text-green-800 dark:bg-green-500/15 dark:text-green-400'
const RED = 'border-transparent bg-red-100 text-red-800 dark:bg-red-500/15 dark:text-red-400'
const AMBER = 'border-transparent bg-amber-100 text-amber-800 dark:bg-amber-500/15 dark:text-amber-400'
const GRAY = 'border-transparent bg-muted text-muted-foreground'

export const applicationStatusBadgeClassName: Record<ApplicationStatus, string> = {
  ACTIVE: GREEN,
  DISABLED: GRAY,
}

export const userStatusBadgeClassName: Record<UserStatus, string> = {
  ACTIVE: GREEN,
  FROZEN: RED,
  PENDING_DELETE: AMBER,
  DELETED: GRAY,
}
```

- [ ] **Step 3: Users.tsx 加搜索图标、换状态徽标语义色**

在 `web/src/pages/Users.tsx` 顶部的 import 区块，把：

```tsx
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
```

改成：

```tsx
import { Search } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { userStatusBadgeClassName } from '@/lib/status-badge'
```

把搜索输入框：

```tsx
        <Input
          name="keyword"
          value={keywordInput}
          onChange={(e) => setKeywordInput(e.target.value)}
          placeholder="手机号 / 用户名 / 昵称"
          className="w-64"
        />
```

改成（外面套一层相对定位容器放图标，`Input` 本身的 `name`/`value`/`onChange`/`placeholder` 一个都不动，只加了 `pl-8` 给图标让位置，测试用的 `getByPlaceholderText` 照样能找到它）：

```tsx
        <div className="relative w-64">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            name="keyword"
            value={keywordInput}
            onChange={(e) => setKeywordInput(e.target.value)}
            placeholder="手机号 / 用户名 / 昵称"
            className="w-64 pl-8"
          />
        </div>
```

把状态徽标：

```tsx
                    <TableCell>
                      <Badge variant={u.status === 'ACTIVE' ? 'default' : 'secondary'}>
                        {statusLabels[u.status] ?? u.status}
                      </Badge>
                    </TableCell>
```

改成：

```tsx
                    <TableCell>
                      <Badge className={userStatusBadgeClassName[u.status]}>
                        {statusLabels[u.status] ?? u.status}
                      </Badge>
                    </TableCell>
```

- [ ] **Step 4: 运行测试确认没有回归**

Run: `cd web && npx vitest run src/pages/Users.test.tsx`
Expected: 全部 PASS（三条已有测试都是靠 `getByPlaceholderText('手机号 / 用户名 / 昵称')` 找输入框、靠 `.value`/`document.activeElement` 断言，跟外面多了一层 `<div>` 和图标无关）。

- [ ] **Step 5: 类型检查**

Run: `cd web && npx tsc -b --noEmit`
Expected: 无错误。

- [ ] **Step 6: Commit**

```bash
git add web/src/lib/status-badge.ts web/src/pages/Users.tsx
git commit -m "$(cat <<'EOF'
feat(web): Users 列表页搜索框加图标，状态徽标改语义色

Roles.tsx 没有筛选框也没有状态字段，这一版不需要改动。

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
Expected: 全部 PASS，包括 Task 1-7 新增的测试文件和所有原有页面测试（`ApplicationDetail.test.tsx`、`Applications.test.tsx`、`Users.test.tsx`、`Roles.test.tsx`、`RoleDetail.test.tsx`、`UserDetail.test.tsx`、`ConfigCenter.test.tsx`、`ConfigVersions.test.tsx`）。

- [ ] **Step 3: lint**

Run: `cd web && npm run lint`
Expected: 无错误（重点检查 Task 6 删掉 Table 系列 import 后有没有残留未使用的引用，以及 Task 4 新装的 shadcn 组件文件是否触发既有的 oxlint 规则）。

- [ ] **Step 4: build 产物检查**

Run: `cd web && npm run build`
Expected: 构建成功，无 TypeScript 报错（`build` 脚本里含 `tsc -b`，这一步等于把 Step 1 再确认一遍并顺带验证 Vite 打包本身没问题）。

- [ ] **Step 5: 起本地开发服务器，浏览器里过一遍四套配色 × 明暗两态**

Run: `cd web && npm run dev`（需要本机 `fp` 服务已经在 `:8080` 跑着，登录后才能看到后台页面；vite.config.ts 已经把 `/admin/api` 代理过去了）。

依次确认（8 种组合：4 套配色 × 2 种明暗）：
- 顶栏「切换主题色」下拉里选一遍 科技蓝 / 紫罗兰 / 翡翠绿 / 极简黑白，每选一个都确认侧边栏 active 状态、按钮、徽标的强调色跟着变，正文的黑白灰骨架不变。
- 顶栏「切换明暗模式」按钮点一下，确认整站切到深色，八种组合下文字和背景对比度都清晰可读（对齐已装的 `ui-ux-pro-max` skill §6 `color-accessible-pairs` 规则：正文文字对比度 ≥ 4.5:1）。
- 侧边栏折叠按钮点一下，确认收起后只剩图标，鼠标悬停在图标上有文字提示；再点一下展开。
- Applications 页确认卡片网格排版正常，点卡片能进详情页，「新建应用」流程（含 appSecret 弹窗）还能正常走完。
- Users 页确认搜索框图标显示正常、状态徽标带语义色（绿/红/黄/灰），筛选和分页照常工作。
- 应用详情页确认顶栏面包屑第二段显示真实应用名；点进「配置中心」确认面包屑第三段是"配置中心"（不受 Task 5 已知降级行为影响，这一段本来就没有实体名要显示）。
- 刷新页面确认没有明显的配色闪烁（Task 2 的防闪烁脚本生效）。

- [ ] **Step 6: 记录已知的、有意偏离设计文档的地方**

设计文档 [2026-09-07-web-ui-theme-design.md](../specs/2026-09-07-web-ui-theme-design.md) 第三节写了"表头 sticky"，实施时发现表格本身分页在 20 条/页、篇幅不长，给 `<thead>` 加 `sticky` 在没有独立滚动容器的情况下容易和顶栏的 `sticky` 打架（两者都相对 viewport 定位，需要精确计算顶栏高度做偏移，收益和复杂度不成正比）。改为只让顶栏（面包屑/主题切换/头像所在的 `<header>`）保持 `sticky`，表格本身跟随整页滚动——不再单独处理。这一步没有代码改动，只是在这里写清楚，供以后翻看这份计划的人知道为什么代码里没有表头 sticky。

- [ ] **Step 7: 最终确认没有遗留的临时改动**

Run: `cd /d/fp && git status --porcelain`
Expected: 干净或只剩预期之外的、需要单独处理的文件（比如别的任务遗留的 `.claude/` 目录——那个和这份计划无关，不用处理）。
