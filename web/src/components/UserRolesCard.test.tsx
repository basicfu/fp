import { disabledIMConfig } from '@/lib/testFixtures'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import UserRolesCard from './UserRolesCard'
import type { Application, Role } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const roles: Role[] = [
  { id: 'r1', key: '商城管理员', name: '商城管理员', parentId: '', createdAt: 1 },
  { id: 'r2', key: '客服', name: '客服', parentId: '', createdAt: 1 },
  { id: 'r3', key: '普通用户', name: '普通用户', parentId: '', createdAt: 1 },
]

function appWith(defaultRoleKey: string, over: Partial<Application> = {}): Application {
  return {
    id: 'app-1',
    name: '商城',
    slug: 'mall',
    appId: 'appid-1',
    status: 'ACTIVE',
    cookieDomain: '',
    defaultRoleKey,
    session: {
      idleTimeoutSeconds: 1,
      idleTimeoutMobileSeconds: 0,
      maxLifetimeSeconds: 1,
      rotateIntervalSeconds: 1,
      extendIntervalSeconds: 1,
      tokenCacheTtlSeconds: 1,
    },
    im: disabledIMConfig,
    createdAt: 1,
    updatedAt: 1,
    ...over,
  }
}

function stubFetch(assigned: string[], apps: Application[], onWrite?: (body: unknown) => void) {
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method !== 'GET') {
      onWrite?.(JSON.parse(String(init?.body)))
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    const body: Record<string, unknown> = {
      '/admin/api/users/u1/roles': { roles: assigned },
      '/admin/api/roles': roles,
      '/admin/api/applications': apps,
    }
    if (!(url in body)) return Promise.reject(new Error(`测试没有为 ${method} ${url} 准备响应`))
    return Promise.resolve(new Response(JSON.stringify(body[url]), { status: 200 }))
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function renderCard() {
  return render(
    <MemoryRouter>
      <UserRolesCard userId="u1" />
    </MemoryRouter>,
  )
}

test('显示当前分配的角色', async () => {
  stubFetch(['商城管理员', '客服'], [])
  renderCard()

  await waitFor(() => expect(screen.getByText('商城管理员')).toBeTruthy())
  expect(screen.getByText('客服')).toBeTruthy()
})

// 【辨别力】移除一个角色要发**剩下的全量列表**，不是发被移除的那个。
//
// 后端是 PUT 全量替换。一个把它当增量接口写的实现（发 {roles:['客服']}
// 意为"删掉客服"）在界面上看起来完全正常——移除后本地状态也确实变了——
// 但实际效果是把这个用户的角色**替换成只剩客服**，另外两个角色凭空消失。
// 所以必须断言请求体的内容，不能只断言"发出了请求"。
test('移除角色时发出剩余角色的全量列表', async () => {
  const bodies: unknown[] = []
  stubFetch(['商城管理员', '客服', '普通用户'], [], (b) => bodies.push(b))
  renderCard()

  await waitFor(() => expect(screen.getByText('商城管理员')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '移除 客服' }))

  await waitFor(() => expect(bodies.length).toBe(1))
  expect(bodies[0]).toEqual({ roles: ['商城管理员', '普通用户'] })
})

// 【辨别力】各应用的默认角色必须显示出来，并说明是叠加。
//
// 默认角色不写任何数据，所以它不会出现在"当前分配"里。不提示的话，看到
// 一个"没有显式分配的角色"的用户，管理员会以为他什么都干不了——而他在
// 每个配了默认角色的应用里都有基础能力。
test('列出各应用的默认角色，并说明是叠加', async () => {
  stubFetch([], [appWith('普通用户'), appWith('观众', { id: 'app-2', name: '视频', slug: 'video' })])
  renderCard()

  await waitFor(() => expect(screen.getByText('没有显式分配的角色')).toBeTruthy())
  expect(screen.getByText(/叠加/)).toBeTruthy()
  expect(screen.getByText('普通用户')).toBeTruthy()
  expect(screen.getByText('观众')).toBeTruthy()
  expect(screen.getByText('商城')).toBeTruthy()
  expect(screen.getByText('视频')).toBeTruthy()
})

// 没有任何应用配默认角色时，就别提这一段——凭空多一句会让人去找不存在的设置。
test('没有应用配默认角色时不显示那一段', async () => {
  stubFetch([], [appWith('')])
  renderCard()

  await waitFor(() => expect(screen.getByText('没有显式分配的角色')).toBeTruthy())
  expect(screen.queryByText(/叠加/)).toBeNull()
})

// 改动会推送给在线用户，这一点要写清楚——否则管理员会以为要等对方重新登录。
test('说明改动会立即推送，不需要重新登录', async () => {
  stubFetch(['客服'], [])
  renderCard()

  await waitFor(() => expect(screen.getByText('客服')).toBeTruthy())
  expect(screen.getByText(/不需要重新登录也会生效/)).toBeTruthy()
})
