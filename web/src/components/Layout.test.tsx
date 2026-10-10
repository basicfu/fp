import { disabledIMConfig } from '@/lib/testFixtures'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, within, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import Layout from './Layout'
import { CurrentAppProvider, useCurrentApp } from '@/lib/current-app'
import { stubApi } from '@/lib/testApi'
import type { Application } from '@/lib/types'

const authMock = vi.hoisted(() => ({
  username: 'alice',
  logout: vi.fn(),
  accountDialogOpen: false,
  setAccountDialogOpen: vi.fn(),
  endSession: vi.fn(),
}))
vi.mock('@/lib/auth', () => ({ useAuth: () => authMock }))

afterEach(() => vi.unstubAllGlobals())

function stubEmptyApplications() {
  stubApi({ 'GET /applications': [] })
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
            <Route path="/notify/*" element={<div>通知页内容</div>} />
          </Route>
        </Routes>
      </CurrentAppProvider>
    </MemoryRouter>,
  )
}

test('渲染全部导航项，当前用户名、面包屑和页面内容都显示', async () => {
  renderLayout(['/applications'])
  // shadcn 的 BreadcrumbPage 本身也带 role="link"（aria-disabled，标记当前页），
  // /applications 这种单段路径下面包屑文案和侧边栏导航项现在都是"应用列表"，
  // 会重名，所以把导航项的查询范围限定在侧边栏导航列表（data-sidebar="menu"）内。
  // 表头的应用切换器现在也是一个 data-sidebar="menu"（团队切换器视觉一致），
  // 所以还要再限定在 data-sidebar="content" 里，只留导航区那一个。
  const navMenu = document.querySelector('[data-sidebar="content"] [data-sidebar="menu"]') as HTMLElement
  expect(within(navMenu).getByRole('link', { name: /应用列表/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /用户管理/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /访问密钥/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /角色管理/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /权限管理/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /配置中心/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /系统配置/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /通知中心/ })).toBeTruthy()
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

const appA: Application = {
  id: 'app-a',
  name: 'A应用',
  code: 'a',
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
  im: disabledIMConfig,
  createdAt: 1,
  updatedAt: 1,
}
const appB: Application = { ...appA, id: 'app-b', name: 'B应用', code: 'b' }

test('在切换器里选另一个应用后，路由子页面跟着显示新的当前应用', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify([appA, appB]), { status: 200 })))

  function Probe() {
    const { currentApp } = useCurrentApp()
    return <div>当前：{currentApp?.name ?? '无'}</div>
  }

  render(
    <MemoryRouter initialEntries={['/applications']}>
      <CurrentAppProvider>
        <Routes>
          <Route element={<Layout />}>
            <Route path="/applications" element={<Probe />} />
          </Route>
        </Routes>
      </CurrentAppProvider>
    </MemoryRouter>,
  )

  await waitFor(() => expect(screen.getByText('当前：A应用')).toBeTruthy())

  const trigger = screen.getByRole('combobox', { name: '切换当前应用' })
  fireEvent.pointerDown(trigger)
  fireEvent.click(trigger)
  const option = await screen.findByRole('option', { name: 'B应用' })
  fireEvent.pointerDown(option)
  fireEvent.click(option)

  await waitFor(() => expect(screen.getByText('当前：B应用')).toBeTruthy())
})

test('用户菜单里有「修改密码」，点它会打开改密对话框', async () => {
  authMock.setAccountDialogOpen.mockClear()
  renderLayout(['/applications'])
  const trigger = screen.getByText('alice').closest('button')!
  fireEvent.pointerDown(trigger)
  fireEvent.click(trigger)
  fireEvent.click(await screen.findByRole('menuitem', { name: /修改密码/ }))
  expect(authMock.setAccountDialogOpen).toHaveBeenCalledWith(true)
})

test('accountDialogOpen 为 true 时渲染改密对话框', async () => {
  authMock.accountDialogOpen = true
  try {
    renderLayout(['/applications'])
    expect(await screen.findByRole('dialog', { name: '修改账号' })).toBeTruthy()
  } finally {
    authMock.accountDialogOpen = false
  }
})

// 「通知中心」的子菜单。查询一律限定在侧栏导航列表里：面包屑里也有同名的「模板」等文案，
// 列表为什么要这样限定见最上面"渲染全部导航项"那条用例的注释。
const sidebarNav = () => document.querySelector('[data-sidebar="content"] [data-sidebar="menu"]') as HTMLElement
const notifyParent = () => within(sidebarNav()).getByRole('link', { name: '通知中心' })
// 父行里依次是图标和展开提示，展开提示（ChevronRight）是最后一个 svg。
const chevronOf = (row: HTMLElement) => Array.from(row.querySelectorAll('svg')).at(-1)!

function subMenuLinks() {
  const sub = sidebarNav().querySelector<HTMLElement>('[data-sidebar="menu-sub"]')
  if (!sub) throw new Error('侧栏里没有渲染子菜单')
  return within(sub)
    .getAllByRole('link')
    .map((a) => ({ name: a.textContent, href: a.getAttribute('href'), active: a.hasAttribute('data-active') }))
}

test('不在通知中心时：只有指向 /notify 的「通知中心」一行，不渲染子菜单，展开提示不旋转', () => {
  renderLayout(['/applications'])
  const nav = sidebarNav()

  expect(notifyParent().getAttribute('href')).toBe('/notify')
  expect(chevronOf(notifyParent()).classList.contains('rotate-90')).toBe(false)
  expect(nav.querySelector('[data-sidebar="menu-sub"]')).toBeNull()
  for (const name of ['模板', '供应商', '发送记录']) {
    expect(within(nav).queryByRole('link', { name })).toBeNull()
  }
  // 没有子项的条目保持原样：只有图标，没有展开提示。
  expect(within(nav).getByRole('link', { name: '应用列表' }).querySelectorAll('svg')).toHaveLength(1)
})

test('在 /notify/templates：子菜单依次是模板 / 供应商 / 发送记录，只有「模板」高亮，父行不抢高亮', () => {
  renderLayout(['/notify/templates'])

  expect(subMenuLinks()).toEqual([
    { name: '模板', href: '/notify/templates', active: true },
    { name: '供应商', href: '/notify/providers', active: false },
    { name: '发送记录', href: '/notify/logs', active: false },
  ])
  expect(notifyParent().getAttribute('href')).toBe('/notify')
  expect(notifyParent().hasAttribute('data-active')).toBe(false)
  expect(chevronOf(notifyParent()).classList.contains('rotate-90')).toBe(true)
})

test('在 /notify/providers：高亮落在「供应商」', () => {
  renderLayout(['/notify/providers'])
  expect(subMenuLinks().map((l) => [l.name, l.active])).toEqual([
    ['模板', false],
    ['供应商', true],
    ['发送记录', false],
  ])
})

test('在模板详情页 /notify/templates/login_sms：「模板」仍保持高亮', () => {
  renderLayout(['/notify/templates/login_sms'])
  expect(subMenuLinks().map((l) => [l.name, l.active])).toEqual([
    ['模板', true],
    ['供应商', false],
    ['发送记录', false],
  ])
})

// 真实路由里 /notify 会立刻重定向到 /notify/templates；这里的占位路由不重定向，
// 钉的是"在分组内但没有子项命中"时由父行兜底高亮，免得整个侧栏没有任何高亮。
test('在 /notify 本身：子菜单照常展开，没有子项命中，父行自己高亮', () => {
  renderLayout(['/notify'])
  expect(notifyParent().hasAttribute('data-active')).toBe(true)
  expect(subMenuLinks().map((l) => l.active)).toEqual([false, false, false])
})
