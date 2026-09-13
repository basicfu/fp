import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import AccessKeyDetail from './AccessKeyDetail'
import type { AccessKey, AppPermissions, Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const key: AccessKey = {
  id: 'k1', accessKeyId: 'FPAK7Q2M9X4K1D8R3T6W0000', remark: '顺丰', roleKey: '合作方', allowedIps: ['1.2.3.4/32'],
  status: 'ACTIVE', state: 'active', expiresAt: 1800000000000, lastUsedAt: 1790000000000,
  createdAt: 1700000000000, updatedAt: 1700000000000,
}
const perms: AppPermissions[] = [{ appId: 'a1', appName: '商城', points: [{ key: 'GET:/orders/{id}', name: '查看订单' }] }]
const roles: Role[] = [{ id: 'r1', key: '合作方', name: '合作方', parentId: '', createdAt: 1 }]

function stubFetch(onWrite?: (url: string, method: string, body: Record<string, unknown>) => void) {
  const data: Record<string, unknown> = {
    '/admin/api/access-keys/k1': key,
    '/admin/api/access-keys/k1/permissions': perms,
    '/admin/api/roles': roles,
  }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      if (method !== 'GET') {
        onWrite?.(url, method, init?.body ? JSON.parse(String(init.body)) : {})
        return Promise.resolve(new Response(null, { status: 204 }))
      }
      if (url in data) return Promise.resolve(new Response(JSON.stringify(data[url]), { status: 200 }))
      return Promise.reject(new Error(`没准备 ${method} ${url}`))
    }),
  )
}

const renderDetail = () =>
  render(
    <MemoryRouter initialEntries={['/access-keys/k1']}>
      <Routes>
        <Route path="/access-keys/:id" element={<AccessKeyDetail />} />
      </Routes>
    </MemoryRouter>,
  )

test('按应用分组展示可调用的接口', async () => {
  stubFetch()
  renderDetail()
  await waitFor(() => expect(screen.getByText('商城')).toBeTruthy())
  expect(screen.getByText('GET:/orders/{id}')).toBeTruthy()
  expect(screen.getByText('查看订单')).toBeTruthy()
})

// 【辨别力】有效期留空时 PATCH 请求体里没有 validDays；填了才带上。
test('编辑时有效期留空不发送 validDays', async () => {
  const bodies: Record<string, unknown>[] = []
  stubFetch((_u, method, body) => {
    if (method === 'PATCH') bodies.push(body)
  })
  renderDetail()
  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '编辑' }))
  fireEvent.change(await waitFor(() => screen.getByLabelText('备注')), { target: { value: '顺丰速运' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(bodies.length).toBe(1))
  expect(bodies[0]).not.toHaveProperty('validDays')
  expect(bodies[0].remark).toBe('顺丰速运')

  fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '编辑' })))
  fireEvent.change(await waitFor(() => screen.getByLabelText('重新设置有效期（天）')), { target: { value: '30' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(bodies.length).toBe(2))
  expect(bodies[1].validDays).toBe(30)
})

test('删除确认框显示最后使用时间', async () => {
  stubFetch()
  renderDetail()
  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '删除' }))
  const desc = await waitFor(() => screen.getByText(/最后使用：/))
  expect(desc.textContent).not.toContain('从未使用')
})
