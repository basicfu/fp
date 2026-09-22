import { disabledIMConfig } from '@/lib/testFixtures'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import ApplicationDetail from './ApplicationDetail'
import type { Application } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const app: Application = {
  id: 'app-1',
  name: '示例应用',
  code: 'demo',
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
  im: disabledIMConfig,
  createdAt: 1700000000000,
  updatedAt: 1700000000000,
}

function stubFetch(status = 200, body: unknown = app) {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status })))
}

const renderDetail = (path = '/applications/app-1') =>
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/applications/:id" element={<ApplicationDetail />} />
      </Routes>
    </MemoryRouter>,
  )

/**
 * 独立路由是这个页面存在的理由：直接用 id 拉这一个应用，不依赖
 * Applications.tsx 那份全局列表——地址栏里的 URL 就是完整状态，
 * 刷新页面也能凭 id 重新拉到同一个应用，不会被弹窗那样的页面内状态丢掉。
 */
test('按 id 拉取应用并展示设置区', async () => {
  stubFetch()
  renderDetail()

  // 应用名以标题形式展示——回到列表的导航交给全局导航条里的面包屑，
  // 这个页面自己不再重复一份，名称/code/appId 的展示与编辑都挪到了
  // Applications.tsx 的列表/编辑弹窗里，这个页面只剩会话策略等 tab。
  await waitFor(() => expect(screen.getByRole('heading', { name: '示例应用' })).toBeTruthy())

  // 完整设置区都在：会话策略页签，不再是弹窗里的一部分；「基本信息」页签已经
  // 移除，「停用应用」按钮也挪到了列表页的操作列。
  expect(screen.getByRole('tab', { name: '会话策略' })).toBeTruthy()
  expect(screen.queryByRole('tab', { name: '基本信息' })).toBeNull()
  expect(screen.queryByRole('button', { name: '停用应用' })).toBeNull()
})

test('应用不存在时显示错误而不是崩溃', async () => {
  stubFetch(404, { code: 'NOT_FOUND', msg: '应用不存在' })
  renderDetail()

  await waitFor(() => expect(screen.getByText('应用不存在')).toBeTruthy())
})
