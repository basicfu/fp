import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { toast } from 'sonner'
import Applications from './Applications'
import { CurrentAppProvider } from '@/lib/current-app'
import type { Application, CreateApplicationResponse } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  Reflect.deleteProperty(navigator, 'clipboard')
})

const sampleApp: Application = {
  id: 'app-1',
  name: '示例应用',
  slug: 'demo',
  appId: 'appid-xyz',
  status: 'ACTIVE',
  cookieDomain: '',
  defaultRoleKey: '',
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

/**
 * 用真实的 CurrentAppProvider（不是像 ConfigCenter.test.tsx 那样绕开它）：
 * 这个页面测的正是"新建应用后自动成为当前应用""点卡片切换当前应用"这些
 * 依赖 provider 真实行为的交互。
 */
function renderApplications() {
  return render(
    <MemoryRouter>
      <CurrentAppProvider>
        <Applications />
      </CurrentAppProvider>
    </MemoryRouter>,
  )
}

test('渲染应用列表', async () => {
  stubFetchSequence(new Response(JSON.stringify([sampleApp]), { status: 200 }))

  renderApplications()

  // 等到设置区也挂载（不能只等卡片）：CurrentAppProvider 首次拉到列表后
  // 还要再走一轮 effect 才会把 currentAppId 设成第一个应用，卡片本身在
  // 那之前就已经渲染出来了，只等"示例应用"出现一次会在这个 effect 落地
  // 之前就先满足，导致后面断言的 ApplicationSettings 还没挂载。
  await waitFor(() => expect(screen.getAllByText('示例应用').length).toBeGreaterThanOrEqual(2))
  // "demo"（slug）和"启用"都出现两次：卡片上的一份 + ApplicationSettings
  // 设置区里的一份（设置区也展示 slug 和状态徽标）。
  expect(screen.getAllByText('demo').length).toBeGreaterThanOrEqual(2)
  expect(screen.getAllByText('启用').length).toBeGreaterThanOrEqual(2)
})

test('空列表显示"还没有应用"提示', async () => {
  stubFetchSequence(new Response(JSON.stringify([]), { status: 200 }))

  renderApplications()

  await waitFor(() => expect(screen.getByText('还没有应用')).toBeTruthy())
})

test('默认选中列表第一个应用，点第二张卡片切换当前应用', async () => {
  const appB: Application = { ...sampleApp, id: 'app-2', name: '第二个应用', slug: 'second' }
  stubFetchSequence(new Response(JSON.stringify([sampleApp, appB]), { status: 200 }))

  renderApplications()

  // 默认选中第一个：设置区标题（h2）和卡片标题一起，至少出现两次"示例应用"。
  await waitFor(() => expect(screen.getAllByText('示例应用').length).toBeGreaterThanOrEqual(2))

  fireEvent.click(screen.getByText('第二个应用'))

  await waitFor(() => expect(screen.getAllByText('第二个应用').length).toBeGreaterThanOrEqual(2))
})

const createdSecret: CreateApplicationResponse = {
  application: { ...sampleApp, id: 'app-2', name: '新应用', slug: 'new-app', appId: 'appid-new' },
  appSecret: 'plaintext-secret-abc',
}

/** 走完"新建应用"整个流程，停在 appSecret 弹窗打开的状态，返回 fetch spy。 */
async function openSecretDialog() {
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify([]), { status: 200 }), // CurrentAppProvider 初始列表（空）
    new Response(JSON.stringify(createdSecret), { status: 201 }), // 创建
    new Response(JSON.stringify([createdSecret.application]), { status: 200 }), // 创建成功后 reload()
  )

  renderApplications()
  await waitFor(() => expect(screen.getByText('还没有应用')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '新建应用' }))
  const nameInput = await waitFor(() => screen.getByLabelText('名称') as HTMLInputElement)
  const slugInput = screen.getByLabelText('slug') as HTMLInputElement

  fireEvent.change(nameInput, { target: { value: '新应用' } })
  fireEvent.change(slugInput, { target: { value: 'new-app' } })
  fireEvent.submit(nameInput.closest('form')!)

  await waitFor(() => expect(screen.getByText('plaintext-secret-abc')).toBeTruthy())
  return fetchMock
}

test('新建应用：提交后弹出 appSecret 弹窗且提示只显示一次，POST 请求体正确', async () => {
  const fetchMock = await openSecretDialog()
  expect(screen.getByText(/只显示这一次/)).toBeTruthy()

  const postCall = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'POST')
  expect(postCall).toBeDefined()
  const [url, init] = postCall as [string, RequestInit]
  expect(url).toBe('/admin/api/applications')
  expect(JSON.parse(init.body as string)).toEqual({ name: '新应用', slug: 'new-app' })

  // 关闭弹窗后，新应用出现，且已经自动成为当前应用（设置区标题也是它）。
  fireEvent.click(screen.getByRole('button', { name: '我已保存' }))
  await waitFor(() => expect(screen.getAllByText('新应用').length).toBeGreaterThanOrEqual(2))
})

test('appSecret 弹窗按 Escape 不会关闭', async () => {
  await openSecretDialog()

  fireEvent.keyDown(document, { key: 'Escape', code: 'Escape' })
  await new Promise((r) => setTimeout(r, 50))

  expect(screen.getByText('plaintext-secret-abc')).toBeTruthy()
})

test('appSecret 弹窗不渲染默认的关闭按钮', async () => {
  await openSecretDialog()
  expect(document.querySelector('[data-slot="dialog-close"]')).toBeNull()
})

test('appSecret 弹窗点击遮罩不会关闭', async () => {
  await openSecretDialog()

  const overlay = document.querySelector('[data-slot="dialog-overlay"]')
  if (!overlay) throw new Error('未找到遮罩层')
  fireEvent.pointerDown(overlay)
  fireEvent.click(overlay)
  await new Promise((r) => setTimeout(r, 50))

  expect(screen.getByText('plaintext-secret-abc')).toBeTruthy()
})

test('复制 appSecret 失败（剪贴板 API 不可用）时给出中文提示，而不是悄悄没反应', async () => {
  Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')

  await openSecretDialog()
  fireEvent.click(screen.getByRole('button', { name: '复制 appSecret' }))

  await waitFor(() => expect(errorSpy).toHaveBeenCalledWith('复制失败，请手动选中复制'))
})

test('复制 appSecret 成功时提示已复制', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  const successSpy = vi.spyOn(toast, 'success').mockImplementation(() => 'toast-id')

  await openSecretDialog()
  fireEvent.click(screen.getByRole('button', { name: '复制 appSecret' }))

  await waitFor(() => expect(writeText).toHaveBeenCalledWith('plaintext-secret-abc'))
  expect(successSpy).toHaveBeenCalledWith('已复制')
})
