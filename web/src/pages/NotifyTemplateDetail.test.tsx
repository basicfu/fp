import { test, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import NotifyTemplateDetail from './NotifyTemplateDetail'
import { apiError, stubApi, type RecordedCall } from '@/lib/testApi'
import type { NotifyContent, NotifyProvider, NotifyTemplateDetail as Detail } from '@/lib/types'

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('sonner', () => ({ toast: toasts }))

beforeEach(() => vi.clearAllMocks())
afterEach(() => vi.unstubAllGlobals())

const smsDetail: Detail = {
  code: 'login_sms', channel: 'sms', mode: 'vendor', description: '登录短信', enabled: true,
  content: { content: '验证码 ${code}', variables: ['code'] },
  createdAt: 1, updatedAt: 1,
  providers: [
    { providerId: 'p1', providerType: 'aliyun', providerDescription: '阿里云-主账号', providerEnabled: true, providerTemplateId: 'SMS_1', enabled: true, priority: 5 },
    { providerId: 'p2', providerType: 'aliyun', providerDescription: '阿里云-备用', providerEnabled: false, providerTemplateId: 'SMS_2', enabled: false, priority: 0 },
  ],
}

const providers: NotifyProvider[] = [
  { id: 'p1', type: 'aliyun', channel: 'sms', description: '阿里云-主账号', enabled: true, config: {}, createdAt: 1, updatedAt: 1 },
  { id: 'p2', type: 'aliyun', channel: 'sms', description: '阿里云-备用', enabled: false, config: {}, createdAt: 1, updatedAt: 1 },
  { id: 'p3', type: 'aliyun', channel: 'sms', description: '腾讯云', enabled: true, config: {}, createdAt: 1, updatedAt: 1 },
  { id: 't1', type: 'telegram', channel: 'telegram', description: '告警 bot', enabled: true, config: {}, createdAt: 1, updatedAt: 1 },
]

const renderPage = () =>
  render(
    <MemoryRouter initialEntries={['/notify/templates/login_sms']}>
      <Routes>
        <Route path="/notify/templates/:code" element={<NotifyTemplateDetail />} />
      </Routes>
    </MemoryRouter>,
  )

const routes = (detail: Detail = smsDetail) => ({
  'GET /notify/templates/login_sms': detail,
  'GET /notify/providers': providers,
})

test('显示关联的供应商：各自的供应商侧模板 ID、优先级与启停', async () => {
  stubApi(routes())
  renderPage()
  const main = await screen.findByLabelText('阿里云-主账号 的供应商侧模板 ID')
  expect((main as HTMLInputElement).value).toBe('SMS_1')
  expect((screen.getByLabelText('阿里云-主账号 的优先级') as HTMLInputElement).value).toBe('5')
  expect(screen.getByRole('switch', { name: '启用 阿里云-主账号' }).getAttribute('aria-checked')).toBe('true')
  expect(screen.getByRole('switch', { name: '启用 阿里云-备用' }).getAttribute('aria-checked')).toBe('false')
  // 供应商本身被停用了，要让人看得出来——这条关联开着也不会被选中。
  expect(screen.getByText('供应商已停用')).toBeTruthy()
})

// 这是需求里"某家运营商短期不用了，临时禁用"的落点：开关一拨就保存，只影响这个模板。
test('拨启用开关立即保存这一条关联', async () => {
  const calls = stubApi({ ...routes(), 'PUT /notify/templates/login_sms/providers/p1': undefined })
  renderPage()
  fireEvent.click(await screen.findByRole('switch', { name: '启用 阿里云-主账号' }))
  await waitFor(() => expect(toasts.success).toHaveBeenCalledWith('已保存'))
  // "只影响这一个模板"靠的是恰好一次写请求、打在这个模板的这条关联上；只看第一个 PUT 的请求体发现不了写错地方或多写。
  const writes = calls.filter((c) => c.method !== 'GET')
  expect(writes.map((c) => `${c.method} ${c.url}`)).toEqual(['PUT /notify/templates/login_sms/providers/p1'])
  expect(writes[0].body).toEqual({ providerTemplateId: 'SMS_1', enabled: false, priority: 5 })
})

// 开关一拨就保存，保存失败时服务端仍是旧值：开关必须退回去，否则界面写着"已停用"而发送仍在用这家，
// 只剩一条几秒就消失的提示。
test('拨启用开关保存失败：开关退回服务端的旧值', async () => {
  stubApi({
    ...routes(),
    'PUT /notify/templates/login_sms/providers/p1': apiError(400, 'NOTIFY_LINK_INVALID', '供应商类型 "log" 已不受支持'),
  })
  renderPage()
  const sw = await screen.findByRole('switch', { name: '启用 阿里云-主账号' })
  fireEvent.click(sw)
  expect(sw.getAttribute('aria-checked')).toBe('false') // 点下去先翻过去，回滚才有意义
  await waitFor(() => expect(toasts.error).toHaveBeenCalledWith('供应商类型 "log" 已不受支持'))
  await waitFor(() => expect(sw.getAttribute('aria-checked')).toBe('true'))
  expect(toasts.success).not.toHaveBeenCalled()
})

test('改优先级后点保存；移除关联', async () => {
  const calls = stubApi({
    ...routes(),
    'PUT /notify/templates/login_sms/providers/p1': undefined,
    'DELETE /notify/templates/login_sms/providers/p2': undefined,
  })
  renderPage()
  fireEvent.change(await screen.findByLabelText('阿里云-主账号 的优先级'), { target: { value: '9' } })
  const row = screen.getByLabelText('阿里云-主账号 的优先级').closest('tr')!
  fireEvent.click(Array.from(row.querySelectorAll('button')).find((b) => b.textContent === '保存')!)
  await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
  expect(calls.find((c) => c.method === 'PUT')!.body).toEqual({ providerTemplateId: 'SMS_1', enabled: true, priority: 9 })

  const row2 = screen.getByLabelText('阿里云-备用 的优先级').closest('tr')!
  fireEvent.click(Array.from(row2.querySelectorAll('button')).find((b) => b.textContent === '移除')!)
  await waitFor(() => expect(calls.some((c) => c.method === 'DELETE')).toBe(true))
})

test('添加供应商：只列同渠道且未关联的，vendor 模式必须填供应商侧模板 ID', async () => {
  const calls = stubApi({ ...routes(), 'PUT /notify/templates/login_sms/providers/p3': undefined })
  renderPage()
  const select = (await screen.findByLabelText('添加供应商')) as HTMLSelectElement
  await waitFor(() => expect(select.options.length).toBeGreaterThan(1))
  // p1、p2 已关联，telegram 的 t1 渠道不符，只剩 p3。
  expect(Array.from(select.options).map((o) => o.value)).toEqual(['', 'p3'])

  fireEvent.change(select, { target: { value: 'p3' } })
  const add = screen.getByRole('button', { name: '关联' }) as HTMLButtonElement
  expect(add.disabled).toBe(true)
  fireEvent.change(screen.getByLabelText('供应商侧模板 ID'), { target: { value: 'SMS_9' } })
  expect(add.disabled).toBe(false)
  fireEvent.click(add)
  await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
  expect(calls.find((c) => c.method === 'PUT')!.body).toEqual({ providerTemplateId: 'SMS_9', enabled: true, priority: 0 })
})

test('编辑内容后保存：PATCH 带内容、备注与启停', async () => {
  const calls = stubApi({ ...routes(), 'PATCH /notify/templates/login_sms': smsDetail })
  renderPage()
  const save = (await screen.findByRole('button', { name: '保存模板' })) as HTMLButtonElement
  expect(save.disabled).toBe(true) // 没改动不能保存

  fireEvent.change(screen.getByLabelText(/供应商模板原文/), { target: { value: '验证码 ${code}，5 分钟内有效' } })
  fireEvent.click(screen.getByRole('switch', { name: /启用（停用后/ }))
  expect(save.disabled).toBe(false)
  fireEvent.click(save)
  await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true))
  expect(calls.find((c) => c.method === 'PATCH')!.body).toEqual({
    content: { content: '验证码 ${code}，5 分钟内有效', variables: ['code'] },
    description: '登录短信',
    enabled: false,
  })
})

test('测试发送：按渠道要收件人，按变量逐个填，提交到 test 接口', async () => {
  const calls = stubApi({ ...routes(), 'POST /notify/templates/login_sms/test': undefined })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '测试发送' }))
  fireEvent.change(await screen.findByLabelText('手机号'), { target: { value: '13800138000' } })
  fireEvent.change(screen.getByLabelText('code'), { target: { value: '123456' } })
  fireEvent.click(screen.getByRole('button', { name: '发送' }))
  await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
  expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ to: '13800138000', params: { code: '123456' } })
  await waitFor(() => expect(toasts.success).toHaveBeenCalled())
})

// 后端按键集合校验 params：必须恰好等于模板当前的变量，多一个（改名后残留的旧键）少一个（没填的）都整条拒绝，
// 而且不告诉你是哪个键。所以请求体里的 params 只能由当前变量表生成，不能是"用户敲过的键"。
test('测试发送：变量改名后，旧变量名不会再随请求发出', async () => {
  let cur: Detail = smsDetail
  const calls = stubApi({
    'GET /notify/providers': providers,
    'GET /notify/templates/login_sms': () => cur,
    'PATCH /notify/templates/login_sms': (c: RecordedCall) => {
      cur = { ...cur, content: (c.body as { content: NotifyContent }).content, updatedAt: cur.updatedAt + 1 }
      return cur
    },
    'POST /notify/templates/login_sms/test': undefined,
  })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '测试发送' }))
  fireEvent.change(await screen.findByLabelText('code'), { target: { value: '123456' } })
  fireEvent.click(screen.getByRole('button', { name: '取消' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

  // 变量 code 改名为 otp 并保存，再重新打开弹窗（重新拉取返回前弹窗里还是旧变量，findBy 会等到 otp 出现）。
  fireEvent.change(screen.getByLabelText('变量'), { target: { value: 'otp' } })
  fireEvent.click(screen.getByRole('button', { name: '保存模板' }))
  await waitFor(() => expect(toasts.success).toHaveBeenCalledWith('已保存'))
  fireEvent.click(screen.getByRole('button', { name: '测试发送' }))
  fireEvent.change(await screen.findByLabelText('otp'), { target: { value: '654321' } })
  fireEvent.change(screen.getByLabelText('手机号'), { target: { value: '13800138000' } })
  fireEvent.click(screen.getByRole('button', { name: '发送' }))
  await waitFor(() => expect(toasts.success).toHaveBeenCalledWith(expect.stringContaining('已发送')))
  expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ to: '13800138000', params: { otp: '654321' } })
})

// 空串值后端认（只校验键集合），整个键缺席不认。
test('测试发送：没填的变量以空串发出，键集合始终与变量一致', async () => {
  const calls = stubApi({ ...routes(), 'POST /notify/templates/login_sms/test': undefined })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '测试发送' }))
  fireEvent.change(await screen.findByLabelText('手机号'), { target: { value: '13800138000' } })
  fireEvent.click(screen.getByRole('button', { name: '发送' }))
  await waitFor(() => expect(toasts.success).toHaveBeenCalled())
  expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ to: '13800138000', params: { code: '' } })
})

// 变量可以叫 to：它的输入框 id 不能与收件人输入框重名，否则点标签聚焦到错的框、两个框的值串在一起。
test('测试发送：变量名叫 to 时，收件人与变量各填各的', async () => {
  const toVar: Detail = { ...smsDetail, content: { content: '验证码 ${to}', variables: ['to'] } }
  const calls = stubApi({ ...routes(toVar), 'POST /notify/templates/login_sms/test': undefined })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '测试发送' }))
  fireEvent.change(await screen.findByLabelText('手机号'), { target: { value: '13800138000' } })
  fireEvent.change(screen.getByLabelText('to'), { target: { value: '654321' } })
  fireEvent.click(screen.getByRole('button', { name: '发送' }))
  await waitFor(() => expect(toasts.success).toHaveBeenCalled())
  expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ to: '13800138000', params: { to: '654321' } })
})

// 一次真发要等各家供应商依次尝试，可能长达几十秒；请求在途时再点"发送"不能再发一条。
test('测试发送：请求在途时按钮禁用，再点一次不会重复发送', async () => {
  const calls = stubApi({ ...routes(), 'POST /notify/templates/login_sms/test': undefined })
  // stubApi 的路由只能立即应答；在它外面再包一层，让 test 请求的响应挂着，直到测试放行。
  let release!: () => void
  const gate = new Promise<void>((resolve) => (release = resolve))
  const respond = globalThis.fetch
  vi.stubGlobal('fetch', (url: string, init?: RequestInit) => {
    const res = respond(url, init)
    return init?.method === 'POST' ? gate.then(() => res) : res
  })
  const posts = () => calls.filter((c) => c.method === 'POST')

  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '测试发送' }))
  fireEvent.change(await screen.findByLabelText('手机号'), { target: { value: '13800138000' } })
  fireEvent.change(screen.getByLabelText('code'), { target: { value: '123456' } })
  const send = screen.getByRole('button', { name: '发送' }) as HTMLButtonElement
  fireEvent.click(send)
  await waitFor(() => expect(posts()).toHaveLength(1)) // 第一条已发出，响应还挂着
  expect(send.disabled).toBe(true)
  fireEvent.click(send)

  release()
  await waitFor(() => expect(toasts.success).toHaveBeenCalled())
  expect(posts()).toHaveLength(1)
})

// 在途保护不能把失败锁死：失败后按钮要恢复，否则一次失败就再也发不了（弹窗重开也不行）。
test('测试发送失败后按钮恢复，可以再发一次', async () => {
  let attempt = 0
  const calls = stubApi({
    ...routes(),
    'POST /notify/templates/login_sms/test': () =>
      ++attempt === 1 ? apiError(500, 'NOTIFY_SEND_FAILED', '通知发送失败，请稍后重试') : undefined,
  })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '测试发送' }))
  fireEvent.change(await screen.findByLabelText('手机号'), { target: { value: '13800138000' } })
  fireEvent.change(screen.getByLabelText('code'), { target: { value: '123456' } })
  const send = screen.getByRole('button', { name: '发送' }) as HTMLButtonElement
  fireEvent.click(send)
  await waitFor(() => expect(toasts.error).toHaveBeenCalled())
  await waitFor(() => expect(send.disabled).toBe(false))

  fireEvent.click(send)
  await waitFor(() => expect(toasts.success).toHaveBeenCalled())
  expect(calls.filter((c) => c.method === 'POST')).toHaveLength(2)
})

// 全部供应商失败时后端只回通用错误，具体原因在发送记录里——提示里要把人指过去。
test('测试发送失败：提示里指向发送记录', async () => {
  stubApi({ ...routes(), 'POST /notify/templates/login_sms/test': apiError(500, 'NOTIFY_SEND_FAILED', '通知发送失败，请稍后重试') })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '测试发送' }))
  fireEvent.change(await screen.findByLabelText('手机号'), { target: { value: '13800138000' } })
  fireEvent.change(screen.getByLabelText('code'), { target: { value: '1' } })
  fireEvent.click(screen.getByRole('button', { name: '发送' }))
  await waitFor(() => expect(toasts.error).toHaveBeenCalled())
  expect(String(toasts.error.mock.calls[0][0])).toContain('发送记录')
})

// 没有可用供应商时根本没有尝试，发送记录里什么也没有——提示里不能把人指去看记录，原因就在提示本身。
test('测试发送：没有可用供应商时只提示原因，不指向发送记录', async () => {
  stubApi({ ...routes(), 'POST /notify/templates/login_sms/test': apiError(404, 'NOTIFY_PROVIDER_MISSING', '通知模板 "login_sms" 没有可用的供应商') })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '测试发送' }))
  fireEvent.change(await screen.findByLabelText('手机号'), { target: { value: '13800138000' } })
  fireEvent.change(screen.getByLabelText('code'), { target: { value: '1' } })
  fireEvent.click(screen.getByRole('button', { name: '发送' }))
  await waitFor(() => expect(toasts.error).toHaveBeenCalled())
  expect(toasts.error.mock.calls[0][0]).toBe('通知模板 "login_sms" 没有可用的供应商')
})

// IM 渠道一个模板只挂一个实例：已经有一个之后就不再给"添加供应商"入口；也没有收件人输入。
test('telegram 模板：已关联一个供应商后隐藏添加入口，测试发送没有收件人', async () => {
  const tg: Detail = {
    code: 'login_sms', channel: 'telegram', mode: 'custom', description: '', enabled: true,
    content: { content: '订单 {id}', variables: ['id'] }, createdAt: 1, updatedAt: 1,
    providers: [{ providerId: 't1', providerType: 'telegram', providerDescription: '告警 bot', providerEnabled: true, providerTemplateId: '', enabled: true, priority: 0 }],
  }
  stubApi(routes(tg))
  renderPage()
  await screen.findByLabelText('告警 bot 的优先级')
  expect(screen.queryByLabelText('添加供应商')).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: '测试发送' }))
  await screen.findByLabelText('id')
  expect(screen.queryByLabelText('手机号')).toBeNull()
  expect(screen.queryByLabelText('邮箱')).toBeNull()
})
