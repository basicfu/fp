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
  { id: 'r1', code: '合作方', name: '合作方', parentId: '', createdAt: 1 },
  { id: 'r2', code: 'GUEST', name: '访客', parentId: '', createdAt: 1 },
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
  expect(selectableRoles(roles).map((r) => r.code)).toEqual(['合作方'])
})

// 【辨别力】编辑不再跳到单独的详情页，点"编辑"应该直接在列表页弹窗，
// 且弹窗里备注/角色/IP 都能改——不是退化成只读展示或者只能改一两个字段。
test('点"编辑"直接弹窗而不是跳转，能改备注、角色与 IP 白名单', async () => {
  const bodies: Record<string, unknown>[] = []
  stubFetch((_u, method, body) => {
    if (method === 'PATCH') bodies.push(body as Record<string, unknown>)
  })
  renderPage()

  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '编辑' }))

  // 还在同一个页面上——列表本身没被替换掉。
  const remarkInput = await waitFor(() => screen.getByLabelText('备注') as HTMLInputElement)
  expect(screen.getByText('顺丰')).toBeTruthy()
  expect(remarkInput.value).toBe('顺丰')

  fireEvent.change(remarkInput, { target: { value: '顺丰速运' } })
  fireEvent.change(screen.getByLabelText('IP 白名单'), { target: { value: '9.9.9.9' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(bodies.length).toBe(1))
  expect(bodies[0].remark).toBe('顺丰速运')
  expect(bodies[0].allowedIps).toEqual(['9.9.9.9'])
})

// 有效期留空表示不改——只想改备注/角色/IP 时，不该被逼着重新算一次到期时间。
test('编辑时有效期留空不发送 validDays，填了才带上', async () => {
  const bodies: Record<string, unknown>[] = []
  stubFetch((_u, method, body) => {
    if (method === 'PATCH') bodies.push(body as Record<string, unknown>)
  })
  renderPage()

  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '编辑' }))
  fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '保存' })))
  await waitFor(() => expect(bodies.length).toBe(1))
  expect(bodies[0]).not.toHaveProperty('validDays')

  fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '编辑' })))
  fireEvent.change(await waitFor(() => screen.getByLabelText('重新设置有效期（天）')), { target: { value: '30' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(bodies.length).toBe(2))
  expect(bodies[1].validDays).toBe(30)
})

// 删除确认框要点出"最后使用时间"，方便判断这把 key 是不是还有人在用——
// 这条断言原来挂在已经删掉的详情页测试里，详情页没了之后这条覆盖不能丢。
test('删除确认框显示最后使用时间', async () => {
  stubFetch()
  renderPage()

  await waitFor(() => expect(screen.getByText('顺丰')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '删除' }))
  const desc = await waitFor(() => screen.getByText(/最后使用：/))
  expect(desc.textContent).toContain('从未使用')
})

// 【辨别力】列表里 IP 白名单只显示"N 条"，具体是哪些 IP 只有悬浮才看得到——
// 悬浮出来的内容必须是真的能看全、能复制的，不能只是摆设。
test('IP 白名单悬浮显示完整列表，且能复制', async () => {
  const withIps: AccessKey = { ...key, allowedIps: ['1.2.3.4', '10.0.0.0/8'] }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url === '/admin/api/access-keys') return Promise.resolve(new Response(JSON.stringify([withIps]), { status: 200 }))
      if (url === '/admin/api/roles') return Promise.resolve(new Response(JSON.stringify(roles), { status: 200 }))
      return Promise.reject(new Error(`没准备 GET ${url}`))
    }),
  )
  renderPage()

  await waitFor(() => expect(screen.getByText('2 条')).toBeTruthy())
  fireEvent.focus(screen.getByText('2 条'))

  expect(await screen.findByText(/1\.2\.3\.4/)).toBeTruthy()
  expect(screen.getByText(/10\.0\.0\.0\/8/)).toBeTruthy()

  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  fireEvent.click(screen.getByRole('button', { name: '复制' }))
  await waitFor(() => expect(writeText).toHaveBeenCalledWith('1.2.3.4\n10.0.0.0/8'))
})
