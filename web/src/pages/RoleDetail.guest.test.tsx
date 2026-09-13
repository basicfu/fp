import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import RoleDetail from './RoleDetail'
import { disabledIMConfig } from '@/lib/testFixtures'
import type { AccessKey, Application, PermissionPoint, Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const app: Application = {
  id: 'a1', name: '商城', slug: 'mall', appId: 'app1', status: 'ACTIVE', cookieDomain: '', defaultRoleKey: '',
  session: { idleTimeoutSeconds: 1, idleTimeoutMobileSeconds: 1, maxLifetimeSeconds: 1, rotateIntervalSeconds: 1, extendIntervalSeconds: 1, tokenCacheTtlSeconds: 1 },
  im: disabledIMConfig, createdAt: 1, updatedAt: 1,
}
const perm: PermissionPoint = {
  id: 'p1', key: 'GET:/orders/{id}', name: '查看订单', kind: 'api', source: 'app', status: 'normal',
  staleForMs: 0, lastSeenAt: 1, createdAt: 1,
}
const guest: Role = { id: 'g', key: 'GUEST', name: '访客', parentId: '', createdAt: 1 }
const partner: Role = { id: 'r1', key: '合作方', name: '合作方', parentId: '', createdAt: 1 }

function stub(keys: AccessKey[]) {
  const data: Record<string, unknown> = {
    '/admin/api/roles': [guest, partner],
    '/admin/api/applications': [app],
    '/admin/api/applications/a1/permissions': [perm],
  }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (/^\/admin\/api\/roles\/[^/]+\/permissions$/.test(url)) {
        return Promise.resolve(new Response(JSON.stringify({ grants: [] }), { status: 200 }))
      }
      if (url.startsWith('/admin/api/access-keys?roleKey=')) {
        return Promise.resolve(new Response(JSON.stringify(keys), { status: 200 }))
      }
      if (url in data) return Promise.resolve(new Response(JSON.stringify(data[url]), { status: 200 }))
      return Promise.reject(new Error(`没准备 ${url}`))
    }),
  )
}

const renderAt = (id: string) =>
  render(
    <MemoryRouter initialEntries={[`/roles/${id}?app=a1`]}>
      <Routes>
        <Route path="/roles/:id" element={<RoleDetail />} />
      </Routes>
    </MemoryRouter>,
  )

test('绑定了访问密钥的角色提示数量并链接到筛选后的列表', async () => {
  const k = { id: 'k1' } as AccessKey
  stub([k, { ...k, id: 'k2' }])
  renderAt('r1')
  expect(await screen.findByText(/绑定了 2 把访问密钥/)).toBeTruthy()
  expect(screen.getByRole('link', { name: '查看这些密钥' }).getAttribute('href')).toBe(
    `/access-keys?role=${encodeURIComponent('合作方')}`,
  )
})

test('GUEST 的授权只有「未授权 / 允许」，普通角色有「拒绝」', async () => {
  stub([])
  const { unmount } = renderAt('g')
  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  expect(screen.queryByRole('button', { name: '拒绝' })).toBeNull()
  expect(screen.getByText(/GUEST 是内置角色/)).toBeTruthy()
  unmount()

  renderAt('r1')
  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  expect(screen.getByRole('button', { name: '拒绝' })).toBeTruthy()
})
