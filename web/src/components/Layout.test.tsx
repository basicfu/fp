import { useEffect } from 'react'
import { test, expect, vi } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useOutletContext } from 'react-router'
import Layout from './Layout'
import type { LayoutOutletContext } from './Layout'

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
  // shadcn 的 BreadcrumbPage 本身也带 role="link"（aria-disabled，标记当前页），
  // 在 /applications 这种单段路径下文案会和侧边栏导航项重名（都是"应用"），
  // 所以这里把查询范围限定在侧边栏导航列表（data-sidebar="menu"）内，
  // 避免和面包屑里的"应用"当前页文案产生歧义匹配。
  const navMenu = document.querySelector('[data-sidebar="menu"]') as HTMLElement
  expect(within(navMenu).getByRole('link', { name: /应用/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /用户/ })).toBeTruthy()
  expect(within(navMenu).getByRole('link', { name: /角色/ })).toBeTruthy()
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

function Reporter({ name }: { name: string }) {
  const outlet = useOutletContext<LayoutOutletContext | undefined>()
  useEffect(() => {
    // 用 setTimeout 错开一个 commit：真实的 ApplicationDetail 也是等异步请求
    // 回来后才在单独一次 re-render 里上报名字，不会和 Layout 那个「路由变化
    // 时重置 crumbLabel」的 effect 挤在同一次 commit 里（子先父后的 effect
    // 顺序下，如果两者同一个 commit 触发，Layout 的重置会在 Reporter 上报
    // 之后执行，把值又清成 null）。这里同步 setCrumbLabel 会复现那种竞态，
    // 所以延后一拍，贴近真实场景。
    const t = setTimeout(() => outlet?.setCrumbLabel(name))
    return () => clearTimeout(t)
  }, [outlet, name])
  return <div>详情内容</div>
}

test('子页面通过 outlet context 上报的实体名会出现在面包屑里', async () => {
  render(
    <MemoryRouter initialEntries={['/applications/app-1']}>
      <Routes>
        <Route element={<Layout />}>
          <Route path="/applications/:id" element={<Reporter name="Acme" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  )
  expect(await screen.findByText('Acme')).toBeTruthy()
})
