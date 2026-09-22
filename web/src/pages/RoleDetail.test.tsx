import { disabledIMConfig } from '@/lib/testFixtures'
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
  { id: 'r1', code: '商城管理员', name: '商城管理员', parentId: '', createdAt: 1700000000000 },
  { id: 'r2', code: '客服', name: '客服', parentId: 'r1', createdAt: 1700000000000 },
  { id: 'r3', code: 'GUEST', name: '访客', parentId: '', createdAt: 1700000000000 },
]

function makeApp(overrides: Partial<Application>): Application {
  return {
    id: 'app-1',
    name: '商城',
    code: 'mall',
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
    im: disabledIMConfig,
    createdAt: 1700000000000,
    updatedAt: 1700000000000,
    ...overrides,
  }
}

// 两个应用，用来验证"一个应用一个下拉"——只用一个应用测不出"下拉是不是
// 按应用分开的"这件事，一个实现把所有应用的权限点糊成一个下拉也能通过。
const apps: Application[] = [makeApp({ id: 'app-1', name: '商城' }), makeApp({ id: 'app-2', name: '视频' })]

const mallPerms: PermissionPoint[] = [
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

const videoPerms: PermissionPoint[] = [
  {
    id: 'p3',
    key: 'GET:/videos/{id}',
    name: '查看视频',
    kind: 'api',
    source: 'app',
    status: 'normal',
    staleForMs: 0,
    lastSeenAt: 1700000000000,
    createdAt: 1700000000000,
  },
]

/** 按 URL 应答，不排队——这个页面好几个请求的完成顺序不由测试决定。 */
function stubFetch(grantsByRole: Record<string, RoleGrant[]>, onWrite?: (url: string, init: RequestInit) => void) {
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method !== 'GET') {
      onWrite?.(url, init as RequestInit)
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    const body: Record<string, unknown> = {
      '/admin/api/roles': roles,
      '/admin/api/applications': apps,
      '/admin/api/applications/app-1/permissions': mallPerms,
      '/admin/api/applications/app-2/permissions': videoPerms,
      '/admin/api/roles/r1/permissions': { grants: grantsByRole.r1 ?? [] },
      '/admin/api/roles/r2/permissions': { grants: grantsByRole.r2 ?? [] },
      '/admin/api/roles/r3/permissions': { grants: grantsByRole.r3 ?? [] },
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

/**
 * 展开某个应用名下面那个下拉。
 *
 * 应用名和它下面的权限点列表是两个各自独立的异步请求——应用名一到就能
 * 找到标题，但下拉本身要等这个应用自己的 permissions 请求回来才会渲染
 * （见 AppPermissionSelect 里 `points.length > 0` 那个门槛），所以这里要
 * 等按钮出现，不能找到标题就立刻点。
 */
async function openDropdown(appName: string) {
  const heading = screen.getByText(appName)
  const trigger = await waitFor(() => {
    const btn = heading.parentElement!.querySelector('button[role="combobox"]')
    if (!btn) throw new Error('下拉按钮还没渲染出来')
    return btn as HTMLButtonElement
  })
  fireEvent.click(trigger)
  return trigger
}

/** 下拉展开后，选项列表里那一行（跟收起状态下已选标签的同名文字区分开）。 */
function optionRow(label: string) {
  const el = [...document.querySelectorAll('[cmdk-item]')].find((n) => n.textContent?.includes(label))
  if (!el) throw new Error(`选项列表里没找到「${label}」`)
  return el as HTMLElement
}

// 【辨别力】一个应用必须对应一个独立的下拉。
//
// 角色是全局的，权限点是按应用存的——把所有应用的权限点糊成一个下拉，
// 管理员没法区分自己在给哪个应用授权，也容易跨应用误勾。
test('按应用渲染独立的权限下拉，每个应用一个', async () => {
  stubFetch({})
  renderAt('/roles/r1')

  await waitFor(() => expect(screen.getByText('商城')).toBeTruthy())
  expect(screen.getByText('视频')).toBeTruthy()
  // 应用名和它自己的权限点列表是两个独立的异步请求，要等两个下拉都真正
  // 渲染出来（不是还停在"加载中…"）才能数数量。
  await waitFor(() => expect(document.querySelectorAll('button[role="combobox"]').length).toBe(2))
})

// 【辨别力】当前授权必须回显到下拉的选中态上，deny 不能当 allow 用。
//
// 这条是最容易假绿的地方：一个根本不读 grants 的实现，界面照样能渲染、
// 点击照样能提交，只是所有权限点都显示成"未选中"。
test('已授权（allow）的权限点回显为选中，deny 和未授权的不选中', async () => {
  stubFetch({
    r1: [
      { permissionId: 'p1', effect: 'allow' },
      { permissionId: 'p2', effect: 'deny' },
    ],
  })
  renderAt('/roles/r1')

  // p1 是 allow：作为已选标签出现在收起的下拉里。
  expect(await screen.findByText('GET:/orders/{id}（查看订单）')).toBeTruthy()
  // p2 是 deny，这个下拉只表达二态，deny 不当选中——不该出现在已选标签里。
  expect(screen.queryByText('DELETE:/orders/{id}（删除订单）')).toBeNull()
})

test('勾选一个权限点，发出 PUT 且 effect 为 allow', async () => {
  const writes: { url: string; body: unknown }[] = []
  stubFetch({}, (url, init) => writes.push({ url, body: JSON.parse(String(init.body)) }))
  renderAt('/roles/r1')

  await waitFor(() => expect(screen.getByText('商城')).toBeTruthy())
  await openDropdown('商城')
  fireEvent.click(await screen.findByText('GET:/orders/{id}（查看订单）'))

  await waitFor(() => expect(writes.length).toBe(1))
  expect(writes[0].url).toBe('/admin/api/roles/r1/permissions/p1')
  expect(writes[0].body).toEqual({ effect: 'allow' })
})

// 【辨别力】收回走的是同一个接口、effect 传空串，不是 DELETE。
//
// 一个把"收回"实现成只改本地状态、不发请求的版本，界面上看起来完全
// 正常（标签确实消失了），但刷新之后权限还在，接入方那边并没有变化。
test('取消勾选已授权的权限点，发出 PUT 且 effect 为空串（收回）', async () => {
  const writes: { url: string; body: unknown }[] = []
  stubFetch({ r1: [{ permissionId: 'p1', effect: 'allow' }] }, (url, init) =>
    writes.push({ url, body: JSON.parse(String(init.body)) }),
  )
  renderAt('/roles/r1')

  await waitFor(() => expect(screen.getByText('商城')).toBeTruthy())
  await openDropdown('商城')
  fireEvent.click(optionRow('GET:/orders/{id}（查看订单）'))

  await waitFor(() => expect(writes.length).toBe(1))
  expect(writes[0].url).toBe('/admin/api/roles/r1/permissions/p1')
  expect(writes[0].body).toEqual({ effect: '' })
})

// 有父角色时必须说清楚这个下拉只管本角色自己的授权。
//
// 不说的话，管理员会在这里找从父角色继承来的权限，找不到就以为丢了，
// 或者以为在这里能取消它——而实际上取消不了。
test('有父角色时提示继承来的权限不在下拉里', async () => {
  stubFetch({})
  renderAt('/roles/r2')

  await waitFor(() => expect(screen.getByText('客服')).toBeTruthy())
  expect(screen.getByText(/继承自 商城管理员/)).toBeTruthy()
  expect(screen.getByText(/不能在这里取消/)).toBeTruthy()
})

// 授权下拉现在只表达"未授权 / 允许"二态，不再单独暴露"拒绝"——GUEST
// 原本"只能配置允许"的限制因此对所有角色自然成立，这里只需确认
// GUEST 的说明文字还在。
test('GUEST 角色显示内置角色说明', async () => {
  stubFetch({})
  renderAt('/roles/r3')

  await waitFor(() => expect(screen.getByText('商城')).toBeTruthy())
  expect(screen.getByText(/GUEST 是内置角色/)).toBeTruthy()
})
