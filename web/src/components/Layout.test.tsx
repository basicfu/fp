import { test, expect, vi } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
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
