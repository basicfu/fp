import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { Toaster } from 'sonner'
import Roles from './Roles'
import { CurrentAppContext } from '@/lib/current-app'
import type { CurrentAppValue } from '@/lib/current-app'
import type { Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const roles: Role[] = [
  { id: 'r1', code: '商城管理员', name: '商城管理员', parentId: '', createdAt: 1700000000000 },
  { id: 'r2', code: '客服', name: '客服中心', parentId: 'r1', createdAt: 1700000000000 },
]

function stubFetch(list: Role[], onWrite?: (url: string, method: string, body: unknown) => void) {
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method !== 'GET') {
      onWrite?.(url, method, init?.body ? JSON.parse(String(init.body)) : undefined)
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    if (url !== '/admin/api/roles') return Promise.reject(new Error(`没准备 ${method} ${url}`))
    return Promise.resolve(new Response(JSON.stringify(list), { status: 200 }))
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function currentAppValue(overrides: Partial<CurrentAppValue> = {}): CurrentAppValue {
  return {
    apps: [],
    currentAppId: '',
    currentApp: null,
    setCurrentAppId: () => {},
    loading: false,
    error: '',
    reload: () => {},
    ...overrides,
  }
}

function renderRoles(appsValue: CurrentAppValue = currentAppValue()) {
  return render(
    <MemoryRouter>
      <CurrentAppContext.Provider value={appsValue}>
        <Roles />
      </CurrentAppContext.Provider>
    </MemoryRouter>,
  )
}

// 【辨别力】父角色列要显示父角色的 code，不是它的 UUID。
//
// 后端返回的 parentId 是 UUID。直接渲染它在界面上是一串没人认得的十六
// 进制，管理员没法确认继承关系配对没配对——而配错继承等于把权限给错人。
test('继承关系显示父角色的名字而不是 UUID', async () => {
  stubFetch(roles)
  renderRoles()

  await waitFor(() => expect(screen.getByText('客服')).toBeTruthy())
  const row = screen.getByText('客服').closest('tr')
  expect(row?.textContent).toContain('商城管理员')
  expect(row?.textContent).not.toContain('r1')
})

// 【辨别力】删除角色的确认框要写清三处连带后果。
//
// 删角色不只是删一行：它会从所有持有它的用户身上摘掉（user_role.roles[]
// 里的 array_remove），解除它的全部授权，还会清空把它设成默认角色的应用
// 的那个设置。一个只写"确定删除吗"的确认框，管理员是在不知道会影响谁的
// 情况下按下确认的。
test('删除确认框写明会从用户身上摘掉、解除授权、清空默认角色设置', async () => {
  stubFetch(roles)
  renderRoles()

  await waitFor(() => expect(screen.getByText('商城管理员', { selector: 'span.font-medium' })).toBeTruthy())
  const row = screen.getByText('商城管理员', { selector: 'span.font-medium' }).closest('tr')
  const delBtn = Array.from(row!.querySelectorAll('button')).find((b) => b.textContent === '删除')
  fireEvent.click(delBtn!)

  const dialog = await waitFor(() => screen.getByText(/从所有持有它的用户身上摘掉/))
  expect(dialog.textContent).toContain('解除它的全部授权')
  expect(dialog.textContent).toContain('默认角色')
  expect(dialog.textContent).toContain('不可撤销')
})

test('确认后发出 DELETE', async () => {
  const writes: string[] = []
  stubFetch(roles, (url, method) => writes.push(`${method} ${url}`))
  renderRoles()

  await waitFor(() => expect(screen.getByText('商城管理员', { selector: 'span.font-medium' })).toBeTruthy())
  const row = screen.getByText('商城管理员', { selector: 'span.font-medium' }).closest('tr')
  fireEvent.click(Array.from(row!.querySelectorAll('button')).find((b) => b.textContent === '删除')!)
  fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '确认删除' })))

  await waitFor(() => expect(writes).toContain('DELETE /admin/api/roles/r1'))
})

// 【辨别力】删角色后要顺带刷新应用列表的缓存，不只是刷新角色列表。
//
// 应用列表（默认角色显示在其中）用的是 CurrentAppProvider 里全局缓存的
// 一份数据，跟这个页面自己的 roles 资源是两份独立状态。后端删角色时会
// 顺带清空把它设成默认角色的应用，但如果这里不主动 reload 应用列表的
// 缓存，用户切到应用列表页看到的还是删除前的默认角色，得手动刷新整个
// 页面才会更新——这个测试专门盯住"删除角色"这一步有没有触发那次 reload。
test('删除角色后刷新应用列表缓存，避免继续显示已删除的默认角色', async () => {
  stubFetch(roles)
  const reloadApps = vi.fn()
  renderRoles(currentAppValue({ reload: reloadApps }))

  await waitFor(() => expect(screen.getByText('商城管理员', { selector: 'span.font-medium' })).toBeTruthy())
  const row = screen.getByText('商城管理员', { selector: 'span.font-medium' }).closest('tr')
  fireEvent.click(Array.from(row!.querySelectorAll('button')).find((b) => b.textContent === '删除')!)
  fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '确认删除' })))

  await waitFor(() => expect(reloadApps).toHaveBeenCalled())
})

// 建角色时 code 不可改这件事必须在建之前就说，建完再说就晚了。
test('新建对话框提示 code 不可修改', async () => {
  stubFetch(roles)
  renderRoles()

  await waitFor(() => expect(screen.getByText('商城管理员', { selector: 'span.font-medium' })).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '新建角色' }))

  await waitFor(() => expect(screen.getByLabelText('code')).toBeTruthy())

  // 说明文字挂在 Label 旁的提示图标上（悬浮/聚焦展示），图标本身不进 Tab 顺序，
  // 所以要显式聚焦它才会出现，不是对话框一打开就常驻显示。对话框走 Portal
  // 渲染在 container 外面，要在整个 document 里找。
  const hint = document.querySelector('[data-slot="tooltip-trigger"]')
  if (!hint) throw new Error('未找到提示图标')
  fireEvent.focus(hint)

  expect(await screen.findByText(/不可修改/)).toBeTruthy()
})

test('新建时提交 code、name 与 parentId', async () => {
  const bodies: unknown[] = []
  stubFetch(roles, (_u, _m, b) => bodies.push(b))
  renderRoles()

  await waitFor(() => expect(screen.getByText('商城管理员', { selector: 'span.font-medium' })).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '新建角色' }))

  const codeInput = await waitFor(() => screen.getByLabelText('code'))
  fireEvent.change(codeInput, { target: { value: '视频管理员' } })
  fireEvent.change(screen.getByLabelText('角色名'), { target: { value: '视频管理员' } })
  fireEvent.click(screen.getByRole('button', { name: '创建' }))

  await waitFor(() => expect(bodies.length).toBe(1))
  expect(bodies[0]).toEqual({ code: '视频管理员', name: '视频管理员', parentId: '' })
})

// 编辑对话框里不能出现 code 输入框——code 改不了，给个能填的框是骗人。
test('编辑对话框不提供修改 code 的输入框', async () => {
  stubFetch(roles)
  renderRoles()

  await waitFor(() => expect(screen.getByText('商城管理员', { selector: 'span.font-medium' })).toBeTruthy())
  const row = screen.getByText('商城管理员', { selector: 'span.font-medium' }).closest('tr')
  fireEvent.click(Array.from(row!.querySelectorAll('button')).find((b) => b.textContent === '编辑')!)

  await waitFor(() => expect(screen.getByLabelText('角色名')).toBeTruthy())
  expect(screen.queryByLabelText('code')).toBeNull()
})

test('GUEST 标「内置」且删除按钮不可用', async () => {
  stubFetch([...roles, { id: 'g', code: 'GUEST', name: '访客', parentId: '', createdAt: 1700000000000 }])
  renderRoles()
  const row = (await waitFor(() => screen.getByText('GUEST', { selector: 'span.font-medium' }))).closest('tr')!
  expect(row.textContent).toContain('内置')
  const del = Array.from(row.querySelectorAll('button')).find((b) => b.textContent === '删除') as HTMLButtonElement
  expect(del.disabled).toBe(true)
})

test('删除被访问密钥绑定的角色时提示绑定数量', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn((_url: string, init?: RequestInit) =>
      Promise.resolve(
        (init?.method ?? 'GET') === 'DELETE'
          ? new Response(JSON.stringify({ code: 'ROLE_IN_USE', msg: '有 2 把访问密钥绑定了该角色，请先改绑或删除这些密钥' }), { status: 409 })
          : new Response(JSON.stringify(roles), { status: 200 }),
      ),
    ),
  )
  render(
    <MemoryRouter>
      <CurrentAppContext.Provider value={currentAppValue()}>
        <Roles />
        <Toaster />
      </CurrentAppContext.Provider>
    </MemoryRouter>,
  )
  const row = (await waitFor(() => screen.getByText('商城管理员', { selector: 'span.font-medium' }))).closest('tr')!
  fireEvent.click(Array.from(row.querySelectorAll('button')).find((b) => b.textContent === '删除')!)
  fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '确认删除' })))
  expect(await screen.findByText(/2 把访问密钥/)).toBeTruthy()
})
