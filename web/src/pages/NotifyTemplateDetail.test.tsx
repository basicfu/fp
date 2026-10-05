import { test, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
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

const saveButton = () => screen.getByRole('button', { name: '保存模板' }) as HTMLButtonElement
const isDetailGet = (method: string, url: string) => method === 'GET' && url.endsWith('/notify/templates/login_sms')
const detailGets = (calls: RecordedCall[]) => calls.filter((c) => isDetailGet(c.method, c.url))
const patches = (calls: RecordedCall[]) => calls.filter((c) => c.method === 'PATCH')

/**
 * holdResponses 在已经 stubApi 的 fetch 外面再包一层：hold() 之后发出的、满足 match 的请求，
 * 响应一直挂着，直到 release()。stubApi 的路由只能立即应答，而"请求在途时界面是什么样"正是要钉住的行为。
 */
function holdResponses(match: (method: string, url: string) => boolean) {
  let release!: () => void
  const gate = new Promise<void>((resolve) => (release = resolve))
  let holding = false
  const respond = globalThis.fetch
  vi.stubGlobal('fetch', (url: string, init?: RequestInit) => {
    const res = respond(url, init)
    return holding && match(init?.method ?? 'GET', url) ? gate.then(() => res) : res
  })
  return {
    hold: () => {
      holding = true
    },
    release,
  }
}

/**
 * statefulServer 是会记住 PATCH 的假服务端：保存之后再 GET，拿到的是保存后的内容，updatedAt 递增。
 * 它还模拟了真实服务端会做的两件事：规整备注（去首尾空白）；别处的变化随重新拉取一起到来（第一个供应商被改了名）——
 * 后者让测试在表单显示的是用户自己编辑的时候，也能确认"重新拉取已经落地"。
 */
function statefulServer() {
  let cur: Detail = smsDetail
  return stubApi({
    'GET /notify/providers': providers,
    'GET /notify/templates/login_sms': () => cur,
    'PATCH /notify/templates/login_sms': (c: RecordedCall) => {
      const b = c.body as { content: NotifyContent; description: string; enabled: boolean }
      cur = {
        ...cur,
        ...b,
        description: b.description.trim(),
        updatedAt: cur.updatedAt + 1,
        providers: [{ ...cur.providers[0], providerDescription: '阿里云-主账号（已改名）' }, cur.providers[1]],
      }
      return cur
    },
  })
}

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

// 代码里是数字越大越先试、默认 0；按"1 = 首选"的直觉去配，主备会颠倒。
test('说明优先级的方向：数字越大越先试，默认 0', async () => {
  stubApi(routes())
  renderPage()
  expect(await screen.findByText(/数字越大越先试，默认 0/)).toBeTruthy()
  expect(screen.getByLabelText('阿里云-主账号 的优先级').getAttribute('title')).toBe('数字越大越先试，默认 0')
})

// 供应商列表有三种状态，选项文案要分得清：失败时说"没有可关联"是误导，人会去建一个已经存在的供应商。
test('添加供应商：供应商列表加载失败时，选项里写明原因，不说"没有可关联"', async () => {
  stubApi({ 'GET /notify/templates/login_sms': smsDetail, 'GET /notify/providers': apiError(500, 'INTERNAL', '服务器内部错误') })
  renderPage()
  const select = (await screen.findByLabelText('添加供应商')) as HTMLSelectElement
  await waitFor(() => expect(select.options[0].text).toContain('服务器内部错误'))
  expect(screen.queryByText('没有可关联的同渠道供应商')).toBeNull()
})

test('添加供应商：列表加载中显示"加载中…"，加载完才轮到"请选择"', async () => {
  stubApi(routes())
  const gate = holdResponses((method, url) => method === 'GET' && url.endsWith('/notify/providers'))
  gate.hold()
  renderPage()
  const select = (await screen.findByLabelText('添加供应商')) as HTMLSelectElement
  expect(select.options[0].text).toBe('加载中…')
  gate.release()
  await waitFor(() => expect(select.options[0].text).toBe('请选择'))
})

test('添加供应商：同渠道的供应商都已关联时，才说"没有可关联的同渠道供应商"', async () => {
  stubApi({ ...routes(), 'GET /notify/providers': providers.filter((p) => p.id !== 'p3') })
  renderPage()
  const select = (await screen.findByLabelText('添加供应商')) as HTMLSelectElement
  await waitFor(() => expect(select.options[0].text).toBe('没有可关联的同渠道供应商'))
})

// 保存成功后，重新拉取的请求发出时那次渲染已经提交；它还挂着，表单只能显示刚保存的内容，而不是闪回保存前的旧内容。
test('保存成功后、重新拉取返回之前，表单显示刚保存的内容，不闪回旧内容', async () => {
  const calls = statefulServer()
  const gate = holdResponses(isDetailGet)
  renderPage()
  fireEvent.change(await screen.findByLabelText(/供应商模板原文/), { target: { value: '验证码 ${code}，5 分钟内有效' } })
  fireEvent.change(screen.getByLabelText('备注'), { target: { value: '新备注  ' } })
  gate.hold()
  fireEvent.click(saveButton())
  await waitFor(() => expect(detailGets(calls)).toHaveLength(2))
  expect((screen.getByLabelText(/供应商模板原文/) as HTMLTextAreaElement).value).toBe('验证码 ${code}，5 分钟内有效')
  expect((screen.getByLabelText('备注') as HTMLInputElement).value).toBe('新备注  ')
  expect(saveButton().disabled).toBe(true)

  gate.release()
  // 返回之后以服务端为准（它去掉了备注的首尾空白）。
  await waitFor(() => expect((screen.getByLabelText('备注') as HTMLInputElement).value).toBe('新备注'))
  expect((screen.getByLabelText(/供应商模板原文/) as HTMLTextAreaElement).value).toBe('验证码 ${code}，5 分钟内有效')
})

// 保存成功之后的任何新编辑都不能被丢：重新拉取返回时 updatedAt 变了，不能因此把草稿当成过期的。
test('保存成功后、重新拉取返回之前的新编辑不会被丢掉', async () => {
  const calls = statefulServer()
  const gate = holdResponses(isDetailGet)
  renderPage()
  fireEvent.change(await screen.findByLabelText('备注'), { target: { value: 'A' } })
  gate.hold()
  fireEvent.click(saveButton())
  await waitFor(() => expect(detailGets(calls)).toHaveLength(2))
  fireEvent.change(screen.getByLabelText('备注'), { target: { value: 'B' } })
  expect(saveButton().disabled).toBe(false)

  gate.release()
  await screen.findByText('阿里云-主账号（已改名）') // 重新拉取已经落地
  // 再让 React 把落地之后的副作用跑完：重置草稿的 bug 往往晚一拍才生效，落地那一刻看一眼发现不了。
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  expect((screen.getByLabelText('备注') as HTMLInputElement).value).toBe('B')
  expect(saveButton().disabled).toBe(false)

  fireEvent.click(saveButton())
  await waitFor(() => expect(toasts.success).toHaveBeenCalledTimes(2))
  expect(patches(calls).map((c) => (c.body as { description: string }).description)).toEqual(['A', 'B'])
})

test('保存请求在途时又改了内容：成功后这次新编辑还在，按钮仍可点', async () => {
  const calls = statefulServer()
  const gate = holdResponses((method) => method === 'PATCH')
  renderPage()
  fireEvent.change(await screen.findByLabelText('备注'), { target: { value: 'A' } })
  gate.hold()
  fireEvent.click(saveButton())
  await waitFor(() => expect(patches(calls)).toHaveLength(1)) // 请求在途
  fireEvent.change(screen.getByLabelText('备注'), { target: { value: 'B' } })

  gate.release()
  await screen.findByText('阿里云-主账号（已改名）') // 保存成功，重新拉取已经落地
  expect((screen.getByLabelText('备注') as HTMLInputElement).value).toBe('B')
  expect(saveButton().disabled).toBe(false)
})

test('保存模板：请求在途时按钮禁用、再点不会重复提交；失败后按钮恢复，编辑还在', async () => {
  let attempt = 0
  const calls = stubApi({
    ...routes(),
    'PATCH /notify/templates/login_sms': () =>
      ++attempt === 1 ? apiError(400, 'NOTIFY_TEMPLATE_INVALID', '模板内容不合法') : smsDetail,
  })
  const gate = holdResponses((method) => method === 'PATCH')
  renderPage()
  fireEvent.change(await screen.findByLabelText('备注'), { target: { value: '新备注' } })
  gate.hold()
  fireEvent.click(saveButton())
  await waitFor(() => expect(patches(calls)).toHaveLength(1))
  expect(saveButton().disabled).toBe(true)
  fireEvent.click(saveButton())

  gate.release()
  await waitFor(() => expect(toasts.error).toHaveBeenCalledWith('模板内容不合法'))
  expect(patches(calls)).toHaveLength(1)
  await waitFor(() => expect(saveButton().disabled).toBe(false))
  expect((screen.getByLabelText('备注') as HTMLInputElement).value).toBe('新备注')
})

// 整页只剩一行红字只该发生在"还没有数据"的时候；有数据时重新拉取失败，页面照常能用。
test('详情加载失败（还没有数据）：整页只显示错误原因', async () => {
  stubApi({ 'GET /notify/templates/login_sms': apiError(404, 'NOTIFY_TEMPLATE_NOT_FOUND', '通知模板 "login_sms" 不存在') })
  renderPage()
  expect(await screen.findByText('通知模板 "login_sms" 不存在')).toBeTruthy()
  expect(screen.queryByRole('button', { name: '保存模板' })).toBeNull()
})

test('已经有数据时重新拉取失败：错误显示在页内，页面照常可用', async () => {
  let gets = 0
  stubApi({
    'GET /notify/providers': providers,
    'GET /notify/templates/login_sms': () => (++gets === 1 ? smsDetail : apiError(500, 'INTERNAL', '服务器内部错误')),
    'PUT /notify/templates/login_sms/providers/p1': undefined,
  })
  renderPage()
  fireEvent.click(await screen.findByRole('switch', { name: '启用 阿里云-主账号' }))
  expect(await screen.findByText('服务器内部错误')).toBeTruthy()
  // 页面没有被错误顶替：表单还在，能继续编辑。
  fireEvent.change(screen.getByLabelText('备注'), { target: { value: '新备注' } })
  expect(saveButton().disabled).toBe(false)
})

// 失败要退回服务端已确认的值。第一次保存成功后、重新拉取返回前，link 这个 prop 还是旧值：
// 此时第二次保存失败，若退回 link.enabled，开关会显示"启用"，而服务端已经是"停用"。
test('拨启用开关：第二次保存失败时退回第一次保存成功的值，而不是过期的 prop', async () => {
  let puts = 0
  stubApi({
    ...routes(),
    'PUT /notify/templates/login_sms/providers/p1': () =>
      ++puts === 1 ? undefined : apiError(500, 'INTERNAL', '服务器内部错误'),
  })
  const gate = holdResponses(isDetailGet)
  renderPage()
  const sw = await screen.findByRole('switch', { name: '启用 阿里云-主账号' })
  gate.hold() // 第一次保存之后的重新拉取挂起：link 保持旧值
  fireEvent.click(sw) // 启用 → 停用，保存成功
  await waitFor(() => expect(toasts.success).toHaveBeenCalledWith('已保存'))
  await waitFor(() => expect(sw.getAttribute('aria-disabled')).not.toBe('true')) // 这一行不再忙
  expect(sw.getAttribute('aria-checked')).toBe('false')

  fireEvent.click(sw) // 停用 → 启用，保存失败
  await waitFor(() => expect(toasts.error).toHaveBeenCalledWith('服务器内部错误'))
  await waitFor(() => expect(sw.getAttribute('aria-checked')).toBe('false'))
  gate.release()
})

// 请求在途时这一行不能再操作：双击"移除"第二个 DELETE 会 404，用户先看到成功、又看到失败。
test('移除关联：请求在途时这一行的开关、保存、移除都禁用，双击只发一个 DELETE', async () => {
  const calls = stubApi({ ...routes(), 'DELETE /notify/templates/login_sms/providers/p2': undefined })
  const gate = holdResponses((method) => method === 'DELETE')
  renderPage()
  const row = (await screen.findByLabelText('阿里云-备用 的优先级')).closest('tr')!
  const other = screen.getByLabelText('阿里云-主账号 的优先级').closest('tr')!
  const button = (r: HTMLElement, name: string) => within(r).getByRole('button', { name }) as HTMLButtonElement
  const deletes = () => calls.filter((c) => c.method === 'DELETE')

  gate.hold()
  fireEvent.click(button(row, '移除'))
  await waitFor(() => expect(deletes()).toHaveLength(1)) // 请求在途
  expect(button(row, '移除').disabled).toBe(true)
  expect(button(row, '保存').disabled).toBe(true)
  expect(within(row).getByRole('switch').getAttribute('aria-disabled')).toBe('true')
  fireEvent.click(button(row, '移除')) // 双击的第二下
  // 别的行不受影响。
  expect(button(other, '移除').disabled).toBe(false)

  gate.release()
  await waitFor(() => expect(toasts.success).toHaveBeenCalledWith('已解除关联'))
  expect(deletes()).toHaveLength(1)
  expect(toasts.success).toHaveBeenCalledTimes(1)
})

// 回滚目标还要跟着 link.enabled 的变化走：别处（另一位管理员）停用了这一行，随一次重新拉取到来之后，
// 本行的保存失败时要退回服务端当前的值，而不是页面最初加载时的值。
test('拨启用开关保存失败：退回重新拉取带来的最新值', async () => {
  let gets = 0
  stubApi({
    'GET /notify/providers': providers,
    // 第二次起主账号这一行已被别处停用，并改了名（让测试能确认重新拉取已经落地）。
    'GET /notify/templates/login_sms': () =>
      ++gets === 1
        ? smsDetail
        : {
            ...smsDetail,
            providers: [{ ...smsDetail.providers[0], providerDescription: '阿里云-主账号（已改名）', enabled: false }, smsDetail.providers[1]],
          },
    'PUT /notify/templates/login_sms/providers/p2': undefined,
    'PUT /notify/templates/login_sms/providers/p1': apiError(500, 'INTERNAL', '服务器内部错误'),
  })
  renderPage()
  const backup = (await screen.findByLabelText('阿里云-备用 的优先级')).closest('tr')!
  fireEvent.click(within(backup).getByRole('button', { name: '保存' })) // 触发一次重新拉取
  await screen.findByText('阿里云-主账号（已改名）')

  const sw = screen.getByRole('switch', { name: '启用 阿里云-主账号（已改名）' })
  fireEvent.click(sw)
  await waitFor(() => expect(toasts.error).toHaveBeenCalledWith('服务器内部错误'))
  await waitFor(() => expect(sw.getAttribute('aria-disabled')).not.toBe('true')) // 这一行不再忙，回滚已经落地
  expect(sw.getAttribute('aria-checked')).toBe('false')
})
