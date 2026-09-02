// 同样不在 brief 的 Files 列表里：brief Step 10 的人工验证第 1/2 两项
// （新建应用→弹窗展示 appId/appSecret 且只显示一次→关闭后列表出现新应用）
// 在本环境做不了真实鼠标点击，这里用等价的自动化交互测试补上，覆盖面比
// 一次性的人工点击更持久。
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { toast } from 'sonner'
import Applications from './Applications'
import type { Application, CreateApplicationResponse } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  // navigator.clipboard 被下面几条测试用 Object.defineProperty 改写过，
  // 不清掉的话会漏到后面的测试文件里（jsdom 的 window/navigator 在同一个
  // 测试文件的多个 test 之间是共享的）。
  Reflect.deleteProperty(navigator, 'clipboard')
})

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

const createdSecret: CreateApplicationResponse = {
  application: { ...sampleApp, id: 'app-2', name: '新应用', slug: 'new-app', appId: 'appid-new' },
  appSecret: 'plaintext-secret-abc',
}

/** 走完"新建应用"整个流程，停在 appSecret 弹窗打开的状态，返回 fetch spy。 */
async function openSecretDialog() {
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify([]), { status: 200 }), // 初始列表（空）
    new Response(JSON.stringify(createdSecret), { status: 201 }), // 创建
    new Response(JSON.stringify([createdSecret.application]), { status: 200 }), // 创建成功后 reload
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

  // appSecret 弹窗：展示明文密钥。
  await waitFor(() => expect(screen.getByText('plaintext-secret-abc')).toBeTruthy())
  return fetchMock
}

// Step 10 人工验证第 1/2 条的自动化版本。
test('新建应用：提交后弹出 appSecret 弹窗且提示只显示一次，POST 请求体正确', async () => {
  const fetchMock = await openSecretDialog()
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

// 【辨别力】appSecret 只在创建时返回这一次，后端库里只存了 bcrypt 哈希，
// 没有"重新生成"接口。这个弹窗一旦能被 Escape 意外关闭，用户手滑按一下
// 就永久丢失密钥，只能整个应用作废重建（appId 也会跟着变，下游已配置的
// SDK 接入方随之失效）。复审第 1 轮 Important。
test('appSecret 弹窗按 Escape 不会关闭', async () => {
  await openSecretDialog()

  fireEvent.keyDown(document, { key: 'Escape', code: 'Escape' })
  // Escape 触发的关闭走的是 base-ui 内部的异步分发路径，给一点时间让它
  // 有机会生效——如果没被拦住，这段等待之后弹窗就会消失。
  await new Promise((r) => setTimeout(r, 50))

  expect(screen.getByText('plaintext-secret-abc')).toBeTruthy()
})

// 只留"复制 appSecret"和"我已保存"两个显式按钮——默认的右上角关闭按钮
// （X）必须不渲染，而不是渲染出来但点了没反应（那样用户会以为弹窗卡死）。
test('appSecret 弹窗不渲染默认的关闭按钮', async () => {
  await openSecretDialog()
  expect(document.querySelector('[data-slot="dialog-close"]')).toBeNull()
})

test('appSecret 弹窗点击遮罩不会关闭', async () => {
  await openSecretDialog()

  const overlay = document.querySelector('[data-slot="dialog-overlay"]')
  if (!overlay) throw new Error('未找到遮罩层')
  // outsidePress 检测同时看 pointerdown 与 click，两个都发一遍。
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
