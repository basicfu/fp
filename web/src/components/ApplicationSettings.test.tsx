import { disabledIMConfig } from '@/lib/testFixtures'
import { useState } from 'react'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { toast } from 'sonner'
import ApplicationSettings from './ApplicationSettings'
import type { Application } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

const baseApp: Application = {
  id: 'app-1',
  name: '测试应用',
  slug: 'test-app',
  appId: 'appid-123',
  status: 'ACTIVE',
  cookieDomain: '',
  defaultRoleKey: '',
  session: {
    idleTimeoutSeconds: 604800,
    idleTimeoutMobileSeconds: 2592000,
    maxLifetimeSeconds: 7776000,
    rotateIntervalSeconds: 86400,
    extendIntervalSeconds: 600,
    tokenCacheTtlSeconds: 30,
  },
  im: disabledIMConfig,
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
 * Harness 模拟真实用法：ApplicationSettings 不自己拉数据，app 由父组件
 * （Task 6 的 Applications.tsx，读全局当前应用）传入；onSaved 触发时父组件
 * 会重新拉一份新的 app 传回来。这里用本地 state 模拟"重新传入新 app"，
 * nextApp 是测试提前算好的、保存成功后应该变成的样子。
 */
function Harness({ initial, nextApp }: { initial: Application; nextApp?: Application }) {
  const [app, setApp] = useState(initial)
  return <ApplicationSettings app={app} onSaved={() => nextApp && setApp(nextApp)} />
}

/** 切到"会话策略"页签，返回"空闲超时（秒）"输入框所在的 form。 */
async function openSessionForm() {
  fireEvent.click(screen.getByRole('tab', { name: '会话策略' }))
  const idleInput = await waitFor(() => screen.getByLabelText('空闲超时（秒）') as HTMLInputElement)
  const form = idleInput.closest('form')
  if (!form) throw new Error('未找到会话策略表单')
  return { idleInput, form }
}

function findCall(fetchMock: ReturnType<typeof vi.fn>, method: string): [string, RequestInit] {
  const call = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === method)
  if (!call) throw new Error(`没有发出过 ${method} 请求`)
  return call as [string, RequestInit]
}

test('保存会话策略时，提交给后端的字段是数字而不是字符串', async () => {
  const updated: Application = { ...baseApp, session: { ...baseApp.session, idleTimeoutSeconds: 7200 } }
  const fetchMock = stubFetchSequence(new Response(JSON.stringify(updated), { status: 200 }))

  render(<Harness initial={baseApp} nextApp={updated} />)
  const { idleInput, form } = await openSessionForm()

  fireEvent.change(idleInput, { target: { value: '7200' } })
  fireEvent.submit(form)

  await waitFor(() => {
    const putCall = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'PUT')
    expect(putCall).toBeDefined()
  })

  const [url, init] = findCall(fetchMock, 'PUT')
  expect(url).toBe('/admin/api/applications/app-1/session')
  const body = JSON.parse(init.body as string) as Record<string, unknown>

  expect(body.idleTimeoutSeconds).toBe(7200)
  expect(typeof body.idleTimeoutSeconds).toBe('number')
  expect(body.tokenCacheTtlSeconds).toBe(30)
  expect(typeof body.tokenCacheTtlSeconds).toBe('number')
  expect(typeof body.maxLifetimeSeconds).toBe('number')
})

// 校验失败不再常驻显示成一段 <p>（会跟着字段改来改去顶动布局），改成用
// toast 报——与平台上其它表单同一条规则。
test('延期间隔大于等于空闲超时时，前端拦截，不发请求且用 toast 给出中文提示', async () => {
  const fetchMock = stubFetchSequence()
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')

  render(<Harness initial={baseApp} />)
  const { form } = await openSessionForm()
  const extendInput = screen.getByLabelText('延期间隔（秒）') as HTMLInputElement

  fireEvent.change(extendInput, { target: { value: '99999999' } })
  fireEvent.submit(form)

  await waitFor(() => expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('延期间隔必须小于空闲超时')))
  expect(fetchMock.mock.calls.length).toBe(0)
})

test('停用应用需要二次确认才发 PATCH status=DISABLED；启用应用直接发 PATCH status=ACTIVE', async () => {
  const disabledApp: Application = { ...baseApp, status: 'DISABLED' }
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify(disabledApp), { status: 200 }), // PATCH /status → DISABLED
    new Response(JSON.stringify(baseApp), { status: 200 }), // PATCH /status → ACTIVE
  )

  function Toggle() {
    const [app, setApp] = useState(baseApp)
    return (
      <ApplicationSettings
        app={app}
        onSaved={() => setApp((a) => (a.status === 'ACTIVE' ? disabledApp : baseApp))}
      />
    )
  }
  render(<Toggle />)

  fireEvent.click(screen.getByRole('button', { name: '停用应用' }))
  expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(false)

  const confirmBtn = await waitFor(() => screen.getByRole('button', { name: '确认停用' }))
  fireEvent.click(confirmBtn)

  await waitFor(() => expect(screen.getByRole('button', { name: '启用应用' })).toBeTruthy())

  const [disableUrl, disableInit] = findCall(fetchMock, 'PATCH')
  expect(disableUrl).toBe('/admin/api/applications/app-1/status')
  expect(JSON.parse(disableInit.body as string)).toEqual({ status: 'DISABLED' })

  fireEvent.click(screen.getByRole('button', { name: '启用应用' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '停用应用' })).toBeTruthy())

  const patchCalls = fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')
  expect(patchCalls).toHaveLength(2)
  const [enableUrl, enableInit] = patchCalls[1] as [string, RequestInit]
  expect(enableUrl).toBe('/admin/api/applications/app-1/status')
  expect(JSON.parse(enableInit.body as string)).toEqual({ status: 'ACTIVE' })
})

test('停用应用弹窗点取消，不发请求，应用保持启用', async () => {
  const fetchMock = stubFetchSequence()

  render(<Harness initial={baseApp} />)

  fireEvent.click(screen.getByRole('button', { name: '停用应用' }))
  const cancelBtn = await waitFor(() => screen.getByRole('button', { name: '取消' }))
  fireEvent.click(cancelBtn)

  await new Promise((r) => setTimeout(r, 50))

  expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(false)
  expect(screen.getByRole('button', { name: '停用应用' })).toBeTruthy()
})

test('基本信息表单提交，PATCH 请求体含 name 与 cookieDomain', async () => {
  const updated: Application = { ...baseApp, name: '改名后的应用', cookieDomain: 'example.com' }
  const fetchMock = stubFetchSequence(new Response(JSON.stringify(updated), { status: 200 }))

  render(<Harness initial={baseApp} nextApp={updated} />)

  const nameInput = screen.getByLabelText('名称') as HTMLInputElement
  const cookieInput = screen.getByLabelText('Cookie 作用域') as HTMLInputElement
  fireEvent.change(nameInput, { target: { value: '改名后的应用' } })
  fireEvent.change(cookieInput, { target: { value: 'example.com' } })
  fireEvent.submit(nameInput.closest('form')!)

  await waitFor(() =>
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(
      true,
    ),
  )

  const [url, init] = findCall(fetchMock, 'PATCH')
  expect(url).toBe('/admin/api/applications/app-1')
  const body = JSON.parse(init.body as string) as Record<string, unknown>
  expect(body.name).toBe('改名后的应用')
  expect(body.cookieDomain).toBe('example.com')
})
