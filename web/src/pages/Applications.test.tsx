// 同样不在 brief 的 Files 列表里：brief Step 10 的人工验证第 1/2 两项
// （新建应用→弹窗展示 appId/appSecret 且只显示一次→关闭后列表出现新应用）
// 在本环境做不了真实鼠标点击，这里用等价的自动化交互测试补上，覆盖面比
// 一次性的人工点击更持久。
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import Applications from './Applications'
import type { Application, CreateApplicationResponse } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

const sampleApp: Application = {
  id: 'app-1',
  name: '示例应用',
  slug: 'demo',
  appId: 'appid-xyz',
  status: 'ACTIVE',
  cookieDomain: '',
  session: {
    idleTimeoutSeconds: 604800,
    idleTimeoutMobileSeconds: 0,
    maxLifetimeSeconds: 7776000,
    rotateIntervalSeconds: 86400,
    extendIntervalSeconds: 600,
    tokenCacheTtlSeconds: 30,
  },
  createdAt: 1700000000000,
  updatedAt: 1700000000000,
}

function stubFetchSequence(...responses: Response[]) {
  const fn = vi.fn()
  for (const r of responses) fn.mockResolvedValueOnce(r)
  vi.stubGlobal('fetch', fn)
  return fn
}

test('渲染应用列表', async () => {
  stubFetchSequence(new Response(JSON.stringify([sampleApp]), { status: 200 }))

  render(
    <MemoryRouter>
      <Applications />
    </MemoryRouter>,
  )

  await waitFor(() => expect(screen.getByText('示例应用')).toBeTruthy())
  expect(screen.getByText('demo')).toBeTruthy()
  expect(screen.getByText('启用')).toBeTruthy()
})

test('空列表显示"还没有应用"提示', async () => {
  stubFetchSequence(new Response(JSON.stringify([]), { status: 200 }))

  render(
    <MemoryRouter>
      <Applications />
    </MemoryRouter>,
  )

  await waitFor(() => expect(screen.getByText('还没有应用')).toBeTruthy())
})

// Step 10 人工验证第 1/2 条的自动化版本。
test('新建应用：提交后弹出 appSecret 弹窗且提示只显示一次，POST 请求体正确', async () => {
  const created: CreateApplicationResponse = {
    application: { ...sampleApp, id: 'app-2', name: '新应用', slug: 'new-app', appId: 'appid-new' },
    appSecret: 'plaintext-secret-abc',
  }
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify([]), { status: 200 }), // 初始列表（空）
    new Response(JSON.stringify(created), { status: 201 }), // 创建
    new Response(JSON.stringify([created.application]), { status: 200 }), // 创建成功后 reload
  )

  render(
    <MemoryRouter>
      <Applications />
    </MemoryRouter>,
  )
  await waitFor(() => expect(screen.getByText('还没有应用')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '新建应用' }))
  const nameInput = await waitFor(() => screen.getByLabelText('名称') as HTMLInputElement)
  const slugInput = screen.getByLabelText('slug') as HTMLInputElement

  fireEvent.change(nameInput, { target: { value: '新应用' } })
  fireEvent.change(slugInput, { target: { value: 'new-app' } })
  fireEvent.submit(nameInput.closest('form')!)

  // appSecret 弹窗：展示明文密钥，并明确提示"只显示这一次"。
  await waitFor(() => expect(screen.getByText('plaintext-secret-abc')).toBeTruthy())
  expect(screen.getByText(/只显示这一次/)).toBeTruthy()

  const postCall = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'POST')
  expect(postCall).toBeDefined()
  const [url, init] = postCall as [string, RequestInit]
  expect(url).toBe('/admin/api/applications')
  expect(JSON.parse(init.body as string)).toEqual({ name: '新应用', slug: 'new-app' })

  // 关闭弹窗后列表刷新，新应用出现。
  fireEvent.click(screen.getByRole('button', { name: '我已保存' }))
  await waitFor(() => expect(screen.getByText('新应用')).toBeTruthy())
})
