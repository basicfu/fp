import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import AccessKeys from './AccessKeys'
import { selectableRoles } from '@/components/AccessKeyFields'
import type { AccessKey, Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const key: AccessKey = {
  id: 'k1', accessKeyId: 'FPAK7Q2M9X4K1D8R3T6W0000', remark: '顺丰', roleKey: '合作方', allowedIps: [],
  status: 'ACTIVE', state: 'active', expiresAt: 0, lastUsedAt: 0, createdAt: 1700000000000, updatedAt: 1700000000000,
}
const roles: Role[] = [
  { id: 'r1', key: '合作方', name: '合作方', parentId: '', createdAt: 1 },
  { id: 'r2', key: 'GUEST', name: '访客', parentId: '', createdAt: 1 },
]

function stubFetch(onWrite?: (url: string, method: string, body: unknown) => unknown) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      if (method !== 'GET') {
        const out = onWrite?.(url, method, init?.body ? JSON.parse(String(init.body)) : undefined)
        return Promise.resolve(
          out === undefined ? new Response(null, { status: 204 }) : new Response(JSON.stringify(out), { status: 201 }),
        )
      }
      if (url === '/admin/api/access-keys') return Promise.resolve(new Response(JSON.stringify([key]), { status: 200 }))
      if (url === '/admin/api/roles') return Promise.resolve(new Response(JSON.stringify(roles), { status: 200 }))
      return Promise.reject(new Error(`没准备 ${method} ${url}`))
    }),
  )
}

const renderPage = () =>
  render(
    <MemoryRouter>
      <AccessKeys />
    </MemoryRouter>,
  )

async function openCreateAndSubmit(remark: string) {
  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '新建访问密钥' }))
  fireEvent.change(await waitFor(() => screen.getByLabelText('备注')), { target: { value: remark } })
}

test('列表显示状态、未使用、永不过期与不限制 IP', async () => {
  stubFetch()
  renderPage()
  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())
  const row = screen.getByText('顺丰').closest('tr')!
  for (const text of ['正常', '从未使用', '永不过期', '不限制', '合作方']) {
    expect(row.textContent).toContain(text)
  }
})

test('新建提交备注、有效期与按行拆开的 IP，并展示一次性密钥', async () => {
  const bodies: unknown[] = []
  stubFetch((_u, _m, body) => {
    bodies.push(body)
    return { accessKey: { ...key, id: 'k2' }, secret: 'SECRET-ONCE' }
  })
  renderPage()
  await openCreateAndSubmit('圆通')
  fireEvent.change(screen.getByLabelText('有效期（天）'), { target: { value: '30' } })
  fireEvent.change(screen.getByLabelText('IP 白名单'), { target: { value: '1.2.3.4\n\n10.0.0.0/8\n' } })
  fireEvent.click(screen.getByRole('button', { name: '创建' }))

  await waitFor(() => expect(bodies.length).toBe(1))
  expect(bodies[0]).toEqual({ remark: '圆通', roleKey: '', validDays: 30, allowedIps: ['1.2.3.4', '10.0.0.0/8'] })
  await waitFor(() => expect(screen.getByText('SECRET-ONCE')).toBeTruthy())
})

// 【辨别力】一次性密钥弹窗按 Esc 关不掉，只有「我已保存」能关——关掉 SK 就再也找不回来了。
test('一次性密钥弹窗只能点「我已保存」关闭', async () => {
  stubFetch(() => ({ accessKey: { ...key, id: 'k2' }, secret: 'SECRET-ONCE' }))
  renderPage()
  await openCreateAndSubmit('圆通')
  fireEvent.click(screen.getByRole('button', { name: '创建' }))
  await waitFor(() => expect(screen.getByText('SECRET-ONCE')).toBeTruthy())

  fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
  await new Promise((r) => setTimeout(r, 50))
  expect(screen.queryByText('SECRET-ONCE')).toBeTruthy()

  fireEvent.click(screen.getByRole('button', { name: '我已保存' }))
  await waitFor(() => expect(screen.queryByText('SECRET-ONCE')).toBeNull())
})

test('角色下拉不提供 GUEST', () => {
  expect(selectableRoles(roles).map((r) => r.key)).toEqual(['合作方'])
})
