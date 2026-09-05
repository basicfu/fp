import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import Roles from './Roles'
import type { Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const roles: Role[] = [
  { id: 'r1', key: '商城管理员', name: '商城管理员', parentId: '', createdAt: 1700000000000 },
  { id: 'r2', key: '客服', name: '客服中心', parentId: 'r1', createdAt: 1700000000000 },
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

function renderRoles() {
  return render(
    <MemoryRouter>
      <Roles />
    </MemoryRouter>,
  )
}

// 【辨别力】父角色列要显示父角色的 key，不是它的 UUID。
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

  await waitFor(() => expect(screen.getByRole('link', { name: '商城管理员' })).toBeTruthy())
  const row = screen.getByRole('link', { name: '商城管理员' }).closest('tr')
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

  await waitFor(() => expect(screen.getByRole('link', { name: '商城管理员' })).toBeTruthy())
  const row = screen.getByRole('link', { name: '商城管理员' }).closest('tr')
  fireEvent.click(Array.from(row!.querySelectorAll('button')).find((b) => b.textContent === '删除')!)
  fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '确认删除' })))

  await waitFor(() => expect(writes).toContain('DELETE /admin/api/roles/r1'))
})

// 建角色时 key 不可改这件事必须在建之前就说，建完再说就晚了。
test('新建对话框提示标识不可修改', async () => {
  stubFetch(roles)
  renderRoles()

  await waitFor(() => expect(screen.getByRole('link', { name: '商城管理员' })).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '新建角色' }))

  await waitFor(() => expect(screen.getByLabelText('标识')).toBeTruthy())
  expect(screen.getByText(/不可修改/)).toBeTruthy()
})

test('新建时提交 key、name 与 parentId', async () => {
  const bodies: unknown[] = []
  stubFetch(roles, (_u, _m, b) => bodies.push(b))
  renderRoles()

  await waitFor(() => expect(screen.getByRole('link', { name: '商城管理员' })).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '新建角色' }))

  const keyInput = await waitFor(() => screen.getByLabelText('标识'))
  fireEvent.change(keyInput, { target: { value: '视频管理员' } })
  fireEvent.change(screen.getByLabelText('显示名'), { target: { value: '视频管理员' } })
  fireEvent.click(screen.getByRole('button', { name: '创建' }))

  await waitFor(() => expect(bodies.length).toBe(1))
  expect(bodies[0]).toEqual({ key: '视频管理员', name: '视频管理员', parentId: '' })
})

// 编辑对话框里不能出现"标识"输入框——key 改不了，给个能填的框是骗人。
test('编辑对话框不提供修改标识的输入框', async () => {
  stubFetch(roles)
  renderRoles()

  await waitFor(() => expect(screen.getByRole('link', { name: '商城管理员' })).toBeTruthy())
  const row = screen.getByRole('link', { name: '商城管理员' }).closest('tr')
  fireEvent.click(Array.from(row!.querySelectorAll('button')).find((b) => b.textContent === '编辑')!)

  await waitFor(() => expect(screen.getByLabelText('显示名')).toBeTruthy())
  expect(screen.queryByLabelText('标识')).toBeNull()
})
