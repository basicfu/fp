import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import Permissions from './Permissions'
import { CurrentAppContext } from '@/lib/current-app'
import type { CurrentAppValue } from '@/lib/current-app'
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
  createdAt: 1,
  updatedAt: 1,
}

const roles: Role[] = [{ id: 'r1', key: '普通用户', name: '普通用户', parentId: '', createdAt: 1 }]

function point(): PermissionPoint {
  return {
    id: 'p1',
    key: 'GET:/orders/{id}',
    name: '查看订单',
    kind: 'api',
    source: 'app',
    status: 'normal',
    staleForMs: 0,
    lastSeenAt: 1,
    createdAt: 1,
  }
}

function stubFetch() {
  const fn = vi.fn((url: string) => {
    const body: Record<string, unknown> = {
      '/admin/api/applications/app-1/permissions': [point()],
      '/admin/api/roles': roles,
    }
    const key = Object.keys(body).find((k) => url.includes(k))
    return Promise.resolve(new Response(JSON.stringify(key ? body[key] : []), { status: 200 }))
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function currentAppValue(overrides: Partial<CurrentAppValue> = {}): CurrentAppValue {
  return {
    apps: [app],
    currentAppId: app.id,
    currentApp: app,
    setCurrentAppId: () => {},
    loading: false,
    error: '',
    reload: () => {},
    ...overrides,
  }
}

test('渲染当前应用的权限点管理', async () => {
  stubFetch()
  render(
    <MemoryRouter>
      <CurrentAppContext.Provider value={currentAppValue()}>
        <Permissions />
      </CurrentAppContext.Provider>
    </MemoryRouter>,
  )

  // 页面级"权限管理 · 应用名"标题已经去掉（应用名已经在侧边栏的应用切换
  // 器里显示，这里再重复一遍是冗余）——用 PermissionsPanel 一定会渲染的
  // "默认角色"确认这是当前应用的权限点管理，而不是断言一个已经不存在的标题。
  expect(screen.getByText('默认角色')).toBeTruthy()
  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
})

test('没有任何应用时显示提示，不崩溃', () => {
  render(
    <MemoryRouter>
      <CurrentAppContext.Provider value={currentAppValue({ apps: [], currentAppId: '', currentApp: null })}>
        <Permissions />
      </CurrentAppContext.Provider>
    </MemoryRouter>,
  )
  expect(screen.getByText(/还没有应用/)).toBeTruthy()
})
