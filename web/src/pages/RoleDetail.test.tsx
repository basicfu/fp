import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import RoleDetail from './RoleDetail'
import type { Application, PermissionPoint, Role, RoleGrant } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const roles: Role[] = [
  { id: 'r1', key: '商城管理员', name: '商城管理员', parentId: '', createdAt: 1700000000000 },
  { id: 'r2', key: '客服', name: '客服', parentId: 'r1', createdAt: 1700000000000 },
]

const apps: Application[] = [
  {
    id: 'app-1',
    name: '商城',
    slug: 'mall',
    appId: 'appid-1',
    status: 'ACTIVE',
    cookieDomain: '',
    defaultRoleKey: '',
    session: {
      idleTimeoutSeconds: 1,
      idleTimeoutMobileSeconds: 0,
      maxLifetimeSeconds: 1,
      rotateIntervalSeconds: 1,
      extendIntervalSeconds: 1,
      tokenCacheTtlSeconds: 1,
    },
    createdAt: 1700000000000,
    updatedAt: 1700000000000,
  },
]

const perms: PermissionPoint[] = [
  {
    id: 'p1',
    key: 'GET:/orders/{id}',
    name: '查看订单',
    kind: 'api',
    source: 'app',
    status: 'normal',
    staleForMs: 0,
    lastSeenAt: 1700000000000,
    createdAt: 1700000000000,
  },
  {
    id: 'p2',
    key: 'DELETE:/orders/{id}',
    name: '删除订单',
    kind: 'api',
    source: 'app',
    status: 'normal',
    staleForMs: 0,
    lastSeenAt: 1700000000000,
    createdAt: 1700000000000,
  },
]

/** 按 URL 应答，不排队——这个页面四个请求的完成顺序不由测试决定。 */
function stubFetch(grants: RoleGrant[], onWrite?: (url: string, init: RequestInit) => void) {
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method !== 'GET') {
      onWrite?.(url, init as RequestInit)
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    const body: Record<string, unknown> = {
      '/admin/api/roles': roles,
      '/admin/api/applications': apps,
      '/admin/api/applications/app-1/permissions': perms,
      '/admin/api/roles/r1/permissions': { grants },
      '/admin/api/roles/r2/permissions': { grants: [] },
    }
    if (!(url in body)) return Promise.reject(new Error(`测试没有为 ${method} ${url} 准备响应`))
    return Promise.resolve(new Response(JSON.stringify(body[url]), { status: 200 }))
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/roles/:id" element={<RoleDetail />} />
      </Routes>
    </MemoryRouter>,
  )
}

/** 找到某个权限点那一行里的三个授权按钮。 */
function rowButtons(permKey: string) {
  const cell = screen.getByText(permKey)
  const row = cell.closest('tr')
  if (!row) throw new Error(`没找到 ${permKey} 所在的行`)
  return {
    none: within(row, '未授权'),
    allow: within(row, '允许'),
    deny: within(row, '拒绝'),
  }
}

function within(row: HTMLElement, label: string): HTMLButtonElement {
  const btn = Array.from(row.querySelectorAll('button')).find((b) => b.textContent === label)
  if (!btn) throw new Error(`行内没找到「${label}」按钮`)
  return btn as HTMLButtonElement
}

// 【辨别力】当前授权必须回显到按钮的选中态上。
//
// 这条是整个授权编辑器最容易假绿的地方：一个根本不读 grants 的实现，
// 界面照样能渲染、点击照样能提交，只是每一行都显示成"未授权"。管理员
// 看到的是一张"什么都没授过"的表，会重新授一遍已经授过的，或者以为
// 收回成功了其实没有。所以必须断言**已授的那行**和**没授的那行**呈现
// 不同——只断言其中一行是不够的。
test('已授权的行回显为选中，未授权的行不选中', async () => {
  stubFetch([{ permissionId: 'p1', effect: 'allow' }])
  renderAt('/roles/r1?app=app-1')

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())

  // p1 已授 allow：「允许」是当前值，被禁用（点它没有意义）
  const granted = rowButtons('GET:/orders/{id}')
  expect(granted.allow.disabled).toBe(true)
  expect(granted.none.disabled).toBe(false)

  // p2 没授：「未授权」才是当前值
  const ungranted = rowButtons('DELETE:/orders/{id}')
  expect(ungranted.none.disabled).toBe(true)
  expect(ungranted.allow.disabled).toBe(false)
})

test('deny 也要回显，不能和 allow 混为一谈', async () => {
  stubFetch([{ permissionId: 'p2', effect: 'deny' }])
  renderAt('/roles/r1?app=app-1')

  await waitFor(() => expect(screen.getByText('DELETE:/orders/{id}')).toBeTruthy())
  const row = rowButtons('DELETE:/orders/{id}')
  expect(row.deny.disabled).toBe(true)
  expect(row.allow.disabled).toBe(false)
})

test('点「允许」发出 PUT，effect 为 allow', async () => {
  const writes: { url: string; body: unknown }[] = []
  stubFetch([], (url, init) => writes.push({ url, body: JSON.parse(String(init.body)) }))
  renderAt('/roles/r1?app=app-1')

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(rowButtons('GET:/orders/{id}').allow)

  await waitFor(() => expect(writes.length).toBe(1))
  expect(writes[0].url).toBe('/admin/api/roles/r1/permissions/p1')
  expect(writes[0].body).toEqual({ effect: 'allow' })
})

// 【辨别力】收回走的是同一个接口、effect 传空串。
//
// 一个把"收回"实现成 DELETE、或者干脆不发请求只改本地状态的版本，在
// 界面上看起来完全正常（按钮确实变了），但刷新之后权限还在。
test('点「未授权」发出 PUT 且 effect 为空串（收回）', async () => {
  const writes: { url: string; body: unknown }[] = []
  stubFetch([{ permissionId: 'p1', effect: 'allow' }], (url, init) =>
    writes.push({ url, body: JSON.parse(String(init.body)) }),
  )
  renderAt('/roles/r1?app=app-1')

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(rowButtons('GET:/orders/{id}').none)

  await waitFor(() => expect(writes.length).toBe(1))
  expect(writes[0].url).toBe('/admin/api/roles/r1/permissions/p1')
  expect(writes[0].body).toEqual({ effect: '' })
})

// 有父角色时必须说清楚这张表只管本角色自己的授权。
//
// 不说的话，管理员会在这里找从父角色继承来的权限，找不到就以为丢了，
// 或者以为在这里能取消它——而实际上取消不了。
test('有父角色时提示继承来的权限不在表里', async () => {
  stubFetch([])
  renderAt('/roles/r2?app=app-1')

  await waitFor(() => expect(screen.getByText('客服')).toBeTruthy())
  expect(screen.getByText(/继承自 商城管理员/)).toBeTruthy()
  expect(screen.getByText(/不能在这里取消/)).toBeTruthy()
})

test('没选应用时不请求权限点，并提示先选应用', async () => {
  const fetchMock = stubFetch([])
  renderAt('/roles/r1')

  await waitFor(() => expect(screen.getByText('商城管理员')).toBeTruthy())
  expect(screen.getByText(/先选一个应用/)).toBeTruthy()
  const permCalls = fetchMock.mock.calls.filter(([url]) => String(url).includes('/permissions'))
  // 只应该有 /roles/r1/permissions（回显用），不该有 /applications/*/permissions
  expect(permCalls.every(([url]) => String(url) === '/admin/api/roles/r1/permissions')).toBe(true)
})

// 【辨别力】应用选择器要显示应用名，不是 UUID。
//
// base-ui 的 Select.Value 在拿不到对应 item 的标签时，会**直接把 value
// 渲染出来**——这里的 value 是应用的 UUID。从 URL 带着 ?app= 进来时必然
// 触发（这也是分享链接、刷新页面的正常路径），界面上就成了一串没人认得
// 的十六进制，管理员根本不知道自己在给哪个应用配权限。
//
// 这条是实机跑出来的：单元测试当时全绿，浏览器里显示的是
// 01a0715a-888a-7137-a08a-f8543be6538e。
test('应用选择器显示应用名而不是 UUID', async () => {
  stubFetch([])
  renderAt('/roles/r1?app=app-1')

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  const trigger = document.querySelector('[data-slot="select-trigger"]')
  expect(trigger?.textContent).toContain('商城')
  expect(trigger?.textContent).not.toContain('app-1')
})
