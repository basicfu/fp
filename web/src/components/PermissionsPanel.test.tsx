import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import PermissionsPanel from './PermissionsPanel'
import type { Application, PermissionPoint, Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const app: Application = {
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
}

const roles: Role[] = [{ id: 'r1', key: '普通用户', name: '普通用户', parentId: '', createdAt: 1 }]

function point(over: Partial<PermissionPoint>): PermissionPoint {
  return {
    id: 'p1',
    key: 'GET:/orders/{id}',
    name: '查看订单',
    kind: 'api',
    source: 'app',
    status: 'normal',
    staleForMs: 0,
    lastSeenAt: 1700000000000,
    createdAt: 1700000000000,
    ...over,
  }
}

function stubFetch(perms: PermissionPoint[], holders: string[] = [], onWrite?: (url: string, m: string) => void) {
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method !== 'GET') {
      onWrite?.(url, method)
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    const body: Record<string, unknown> = {
      '/admin/api/applications/app-1/permissions': perms,
      '/admin/api/roles': roles,
      '/admin/api/permissions/p1/holders': { roles: holders },
    }
    if (!(url in body)) return Promise.reject(new Error(`测试没有为 ${method} ${url} 准备响应`))
    return Promise.resolve(new Response(JSON.stringify(body[url]), { status: 200 }))
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function renderPanel(a: Application = app) {
  return render(
    <MemoryRouter>
      <PermissionsPanel app={a} onAppChanged={() => {}} />
    </MemoryRouter>,
  )
}

// 【辨别力】删除前必须先问"谁在用它"，并把角色名写进确认框。
//
// 这是唯一的安全网：删除是真删，并且会连带删掉所有角色对它的授权
// （数据库外键级联），没有可逆的"停用"中间态。一个直接弹"确定删除吗"
// 的实现在界面上毫无异样，但管理员是在完全不知道会影响谁的情况下按下
// 确认的。所以断言两件事：确实发了 holders 请求，且角色名出现在文案里。
test('删除前先查持有者，并在确认框里点名', async () => {
  const fetchMock = stubFetch([point({})], ['商城管理员', '客服'])
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '删除' }))

  await waitFor(() => expect(screen.getByText(/商城管理员、客服/)).toBeTruthy())
  expect(screen.getByText(/2 个角色/)).toBeTruthy()
  expect(fetchMock.mock.calls.some(([u]) => String(u) === '/admin/api/permissions/p1/holders')).toBe(true)
})

// 没人持有时文案要说"不会让任何人掉权限"，而不是照样吓唬人。
test('没有角色持有时，确认框说明不会影响任何人', async () => {
  stubFetch([point({})], [])
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '删除' }))

  await waitFor(() => expect(screen.getByText(/没有角色持有它/)).toBeTruthy())
})

// 确认之后才真的发 DELETE。
test('确认后发出 DELETE', async () => {
  const writes: string[] = []
  stubFetch([point({})], [], (url, m) => writes.push(`${m} ${url}`))
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '删除' }))
  const confirm = await waitFor(() => screen.getByRole('button', { name: '确认删除' }))
  fireEvent.click(confirm)

  await waitFor(() => expect(writes).toContain('DELETE /admin/api/permissions/p1'))
})

// 【辨别力】「过渡中」必须说清楚"不代表已经没了"。
//
// SDK 上报的是全量快照，但快照里缺一条不等于它消失了——滚动发布时新旧
// 版本同时在跑、交替上报。一个把这个状态显示成"已删除"或不加解释的界面
// 会诱导管理员去删掉一个其实还在服务的接口，那会让线上掉权限。
test('有过渡中的权限点时给出解释，而不是只标一个状态', async () => {
  stubFetch([point({ status: 'stale', staleForMs: 3 * 3600 * 1000 })])
  renderPanel()

  await waitFor(() => expect(screen.getByText('过渡中')).toBeTruthy())
  expect(screen.getByText(/这不代表它们已经没了|不代表它们已经没了/)).toBeTruthy()
  // 已过渡时长要显示出来——它是人判断"该不该删"的唯一依据
  expect(screen.getByText('3 小时')).toBeTruthy()
})

test('没有过渡中的权限点时不显示那条提示', async () => {
  stubFetch([point({ status: 'normal' })])
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  expect(screen.queryByText(/不代表它们已经没了/)).toBeNull()
})

// 手动添加的权限点没有"最近上报"可言，不能显示成 1970 年。
test('手动添加的权限点不显示上报时间', async () => {
  stubFetch([point({ source: 'manual', status: 'manual', lastSeenAt: 0 })])
  renderPanel()

  await waitFor(() => expect(screen.getByText('手动')).toBeTruthy())
  expect(screen.getByText('（手动添加）')).toBeTruthy()
})

// 默认角色是叠加不是替换——这句必须写在界面上。
//
// 理解成"替换"的人会不敢给用户加角色，怕把默认能力弄没；或者反过来，
// 以为清空显式角色就能收回权限，而默认角色其实还在。
test('默认角色一栏说明它是叠加而非替换', async () => {
  stubFetch([])
  renderPanel({ ...app, defaultRoleKey: '普通用户' })

  await waitFor(() => expect(screen.getByText('默认角色')).toBeTruthy())
  expect(screen.getByText(/叠加/)).toBeTruthy()
  expect(screen.getByText(/不写任何数据/)).toBeTruthy()
})
