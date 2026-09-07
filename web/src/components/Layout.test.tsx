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
