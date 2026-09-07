import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, within, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import Layout from './Layout'
import { CurrentAppProvider, useCurrentApp } from '@/lib/current-app'
import type { Application } from '@/lib/types'

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
