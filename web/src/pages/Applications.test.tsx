import { disabledIMConfig } from '@/lib/testFixtures'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useParams } from 'react-router'
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
  code: 'demo',
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
  im: disabledIMConfig,
  createdAt: 1700000000000,
  updatedAt: 1700000000000,
}

/**
 * 按 URL/方法分派、带最小状态的假后端——不排队，这个页面同时会拉
 * /applications 和 /roles（新建/编辑弹窗里的默认角色下拉要用），两个
 * 请求谁先谁后不由测试决定。写操作（POST/PATCH/DELETE）会更新内存里的
 * `apps`，让写完之后的 reload() 能看到变化，比排队返回固定响应更接近
 * 真实后端，也不会因为多了一个并发请求就打乱顺序假设。
 */
function stubFetch(initialApps: Application[]) {
  let apps = initialApps
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'

    if (url === '/admin/api/roles' && method === 'GET') {
      return Promise.resolve(new Response(JSON.stringify([]), { status: 200 }))
    }
    if (url === '/admin/api/applications' && method === 'GET') {
      return Promise.resolve(new Response(JSON.stringify(apps), { status: 200 }))
    }
    if (url === '/admin/api/applications' && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as { name: string; code: string }
      const created: Application = { ...sampleApp, id: 'app-2', appId: 'appid-new', ...body }
      apps = [...apps, created]
      const secret: CreateApplicationResponse = { application: created, appSecret: 'plaintext-secret-abc' }
      return Promise.resolve(new Response(JSON.stringify(secret), { status: 201 }))
    }
    const statusMatch = /^\/admin\/api\/applications\/([^/]+)\/status$/.exec(url)
    if (statusMatch && method === 'PATCH') {
      const body = JSON.parse(String(init?.body)) as { status: Application['status'] }
      apps = apps.map((a) => (a.id === statusMatch[1] ? { ...a, status: body.status } : a))
      return Promise.resolve(new Response(JSON.stringify(apps.find((a) => a.id === statusMatch[1])), { status: 200 }))
    }
    const roleMatch = /^\/admin\/api\/applications\/([^/]+)\/default-role$/.exec(url)
    if (roleMatch && method === 'PATCH') {
      const body = JSON.parse(String(init?.body)) as { roleKey: string }
      apps = apps.map((a) => (a.id === roleMatch[1] ? { ...a, defaultRoleKey: body.roleKey } : a))
      return Promise.resolve(new Response(JSON.stringify(apps.find((a) => a.id === roleMatch[1])), { status: 200 }))
    }
    const patchMatch = /^\/admin\/api\/applications\/([^/]+)$/.exec(url)
    if (patchMatch && method === 'PATCH') {
      const body = JSON.parse(String(init?.body)) as Partial<Application>
      apps = apps.map((a) => (a.id === patchMatch[1] ? { ...a, ...body } : a))
      return Promise.resolve(new Response(JSON.stringify(apps.find((a) => a.id === patchMatch[1])), { status: 200 }))
    }
    const deleteMatch = /^\/admin\/api\/applications\/([^/]+)$/.exec(url)
    if (deleteMatch && method === 'DELETE') {
      apps = apps.filter((a) => a.id !== deleteMatch[1])
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    return Promise.reject(new Error(`测试没有为 ${method} ${url} 准备响应`))
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

/** 从 fetch spy 的调用记录里找某个 HTTP 方法的最后一次调用。 */
function lastCallWithMethod(fetchMock: ReturnType<typeof vi.fn>, method: string) {
  const calls = fetchMock.mock.calls.filter(([, init]) => ((init as RequestInit | undefined)?.method ?? 'GET') === method)
  const call = calls.at(-1)
  if (!call) throw new Error(`没有发出过 ${method} 请求`)
  return call as [string, RequestInit]
}

/** 编辑页是独立路由（见 Applications.tsx 的注释），这里用一个占位组件顶替真实的
 *  ApplicationDetail，只用来确认"编辑"/双击确实把地址栏带到了对的 id 上。 */
function EditRouteStub() {
  const { id } = useParams()
  return <div>编辑页：{id}</div>
}

/**
 * 用真实的 CurrentAppProvider（不是像 ConfigCenter.test.tsx 那样绕开它）：
 * 这个页面测的正是"新建应用后自动成为当前应用""点表格行切换当前应用"这些
 * 依赖 provider 真实行为的交互。
 */
function renderApplications() {
  return render(
    <MemoryRouter initialEntries={['/applications']}>
      <CurrentAppProvider>
        <Routes>
          <Route path="/applications" element={<Applications />} />
          <Route path="/applications/:id" element={<EditRouteStub />} />
        </Routes>
      </CurrentAppProvider>
    </MemoryRouter>,
  )
}

test('渲染应用列表', async () => {
  stubFetch([sampleApp])

  renderApplications()

  await waitFor(() => expect(screen.getByText('示例应用')).toBeTruthy())
  expect(screen.getByText('demo')).toBeTruthy()
  expect(screen.getByText('启用')).toBeTruthy()
  // ACTIVE 状态的徽标要带绿色语义色，不能只是文案对了但样式跟 DISABLED 撞色。
  expect(screen.getByText('启用').className).toContain('bg-green-100')
  // sampleApp 的 IM 是关着的（disabledIMConfig），IM 列该显示「停用」而不是网址。
  // 用 selector 限定在 <td> 上：同一行里「停用」应用的按钮文案也是"停用"，
  // 不加限定会因为匹配到两处而报错。
  expect(screen.getByText('停用', { selector: 'td' })).toBeTruthy()
})

// 【辨别力】默认角色要在列表里展示，且要跟着应用数据一起刷新，不能
// 只在新建/编辑弹窗里能看到——不然想确认"这个应用现在的默认角色是
// 什么"得挨个点开编辑弹窗才知道。
test('默认角色列显示 code 后面，未设置时显示占位符', async () => {
  const withDefault: Application = { ...sampleApp, defaultRoleKey: '普通用户' }
  const withoutDefault: Application = { ...sampleApp, id: 'app-2', code: 'second', defaultRoleKey: '' }
  stubFetch([withDefault, withoutDefault])

  renderApplications()

  await waitFor(() => expect(screen.getByText('普通用户')).toBeTruthy())
  const rowWithout = screen.getByText('second').closest('tr') as HTMLElement
  expect(rowWithout.textContent).toContain('-')
})

test('IM 接入启用且配了业务方回调时，IM 列显示回调地址', async () => {
  const appWithIM: Application = {
    ...sampleApp,
    im: {
      enabled: true,
      connPolicy: 'replace',
      connLimit: 5,
      allowGuest: false,
      guestIpRate: 20,
      bizAuth: { verifyUrl: 'http://biz.example.com/verify', timeoutMs: 2000, cacheSize: 10000 },
    },
  }
  stubFetch([appWithIM])

  renderApplications()

  await waitFor(() => expect(screen.getByText('http://biz.example.com/verify')).toBeTruthy())
})

// 「基本信息」页签从详情页挪掉了，名称/Cookie 作用域改在列表页的编辑弹窗里
// 改——点「编辑」不再导航到 /applications/:id（那个页面现在只剩会话策略/
// 登录方式/IM 接入）。
test('点"编辑"打开编辑弹窗而不是导航，提交后 PATCH 请求体含 name 与 cookieDomain', async () => {
  const fetchMock = stubFetch([sampleApp])

  renderApplications()
  await waitFor(() => expect(screen.getByText('示例应用')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '编辑' }))
  expect(screen.queryByText('编辑页：app-1')).toBeNull()

  const nameInput = await waitFor(() => screen.getByLabelText('名称') as HTMLInputElement)
  const cookieInput = screen.getByLabelText('Cookie 作用域') as HTMLInputElement
  fireEvent.change(nameInput, { target: { value: '改名后的应用' } })
  fireEvent.change(cookieInput, { target: { value: 'example.com' } })
  fireEvent.submit(nameInput.closest('form')!)

  await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(true))
  const [url, init] = lastCallWithMethod(fetchMock, 'PATCH')
  expect(url).toBe('/admin/api/applications/app-1')
  expect(JSON.parse(init.body as string)).toEqual({ name: '改名后的应用', cookieDomain: 'example.com' })
})

test('单击一行同样进入独立的编辑页', async () => {
  stubFetch([sampleApp])

  renderApplications()
  const row = await waitFor(() => screen.getByText('示例应用').closest('tr') as HTMLElement)

  fireEvent.click(row)

  await waitFor(() => expect(screen.getByText('编辑页：app-1')).toBeTruthy())
})

// 行内的「编辑」「启用/停用」按钮必须挡住冒泡，否则点它们会先触发外层
// <tr> 的 onClick，把人带去编辑页——跟按钮本身要做的事完全不一样。
test('点行内的"停用"按钮不会顺带触发整行的点击导航', async () => {
  stubFetch([sampleApp])

  renderApplications()
  await waitFor(() => expect(screen.getByText('示例应用')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '停用' }))

  // 停用要走二次确认弹窗，而不是直接跳到编辑页。
  await waitFor(() => expect(screen.getByRole('button', { name: '确认停用' })).toBeTruthy())
  expect(screen.queryByText('编辑页：app-1')).toBeNull()
})

test('启用中的应用点"停用"需要二次确认；已停用的应用点"启用"直接生效', async () => {
  const fetchMock = stubFetch([sampleApp])

  renderApplications()
  await waitFor(() => expect(screen.getByText('示例应用')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '停用' }))
  fireEvent.click(await screen.findByRole('button', { name: '确认停用' }))

  await waitFor(() => expect(screen.getByRole('button', { name: '启用' })).toBeTruthy())
  const disableCall = lastCallWithMethod(fetchMock, 'PATCH')
  expect(disableCall[0]).toBe('/admin/api/applications/app-1/status')
  expect(JSON.parse(disableCall[1].body as string)).toEqual({ status: 'DISABLED' })

  fireEvent.click(screen.getByRole('button', { name: '启用' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '停用' })).toBeTruthy())
  const enableCall = lastCallWithMethod(fetchMock, 'PATCH')
  expect(enableCall[0]).toBe('/admin/api/applications/app-1/status')
  expect(JSON.parse(enableCall[1].body as string)).toEqual({ status: 'ACTIVE' })
})

// 「删除」只在应用已停用时才出现——后端也会拒绝对启用中的应用删除，
// 但前端先把这条路挡掉，不让人点了个必然报错的按钮。
test('启用中的应用不显示"删除"按钮，停用后才出现', async () => {
  stubFetch([sampleApp])

  renderApplications()
  await waitFor(() => expect(screen.getByText('示例应用')).toBeTruthy())
  expect(screen.queryByRole('button', { name: '删除' })).toBeNull()
})

test('点"删除"需要二次确认，确认后发 DELETE 请求并刷新列表', async () => {
  const disabledApp: Application = { ...sampleApp, status: 'DISABLED' }
  const fetchMock = stubFetch([disabledApp])

  renderApplications()
  await waitFor(() => expect(screen.getByRole('button', { name: '删除' })).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '删除' }))
  expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false)

  fireEvent.click(await screen.findByRole('button', { name: '确认删除' }))

  await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(true))
  const [url] = lastCallWithMethod(fetchMock, 'DELETE')
  expect(url).toBe('/admin/api/applications/app-1')

  await waitFor(() => expect(screen.getByText('没有数据')).toBeTruthy())
})

test('空列表显示"没有数据"提示', async () => {
  stubFetch([])

  renderApplications()

  await waitFor(() => expect(screen.getByText('没有数据')).toBeTruthy())
})

test('默认选中列表第一个应用，那一行带「当前」徽标', async () => {
  const appB: Application = { ...sampleApp, id: 'app-2', name: '第二个应用', code: 'second' }
  stubFetch([sampleApp, appB])

  renderApplications()

  await waitFor(() => expect(screen.getByText('示例应用').closest('tr')?.textContent).toContain('当前'))
  const secondRow = screen.getByText('第二个应用').closest('tr') as HTMLElement
  expect(secondRow.textContent).not.toContain('当前')
})

/** 走完"新建应用"整个流程，停在 appSecret 弹窗打开的状态，返回 fetch spy。 */
async function openSecretDialog() {
  const fetchMock = stubFetch([])

  renderApplications()
  await waitFor(() => expect(screen.getByText('没有数据')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '新建应用' }))
  const nameInput = await waitFor(() => screen.getByLabelText('名称') as HTMLInputElement)
  const codeInput = screen.getByLabelText('code') as HTMLInputElement

  fireEvent.change(nameInput, { target: { value: '新应用' } })
  fireEvent.change(codeInput, { target: { value: 'new-app' } })
  fireEvent.submit(nameInput.closest('form')!)

  await waitFor(() => expect(screen.getByText('plaintext-secret-abc')).toBeTruthy())
  return fetchMock
}

test('新建应用：提交后弹出 appSecret 弹窗且提示只显示一次，POST 请求体正确', async () => {
  const fetchMock = await openSecretDialog()
  expect(screen.getByText(/只显示这一次/)).toBeTruthy()

  const [url, init] = lastCallWithMethod(fetchMock, 'POST')
  expect(url).toBe('/admin/api/applications')
  expect(JSON.parse(init.body as string)).toEqual({ name: '新应用', code: 'new-app' })

  // 关闭弹窗后，新应用出现在列表里，且已经自动成为当前应用（那一行带「当前」徽标）。
  fireEvent.click(screen.getByRole('button', { name: '我已保存' }))
  await waitFor(() => expect(screen.getByText('新应用').closest('tr')?.textContent).toContain('当前'))
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
