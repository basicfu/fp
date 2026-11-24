import { test, expect, vi, afterEach } from 'vitest'
import { api, ApiError, isNotFound, setUnauthorizedHandler } from './api'

afterEach(() => {
  vi.unstubAllGlobals()
  setUnauthorizedHandler(() => {})
})

function stubFetch(res: Response) {
  const spy = vi.fn().mockResolvedValue(res)
  vi.stubGlobal('fetch', spy)
  return spy
}

test('GET 解析 JSON 响应体', async () => {
  stubFetch(new Response(JSON.stringify({ id: 'x' }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  }))
  await expect(api.get<{ id: string }>('/applications')).resolves.toEqual({ id: 'x' })
})

test('请求路径自动补上 /admin/api 前缀', async () => {
  const spy = stubFetch(new Response('{}', { status: 200 }))
  await api.get('/applications')
  expect(spy.mock.calls[0][0]).toBe('/admin/api/applications')
})

// 【辨别力】后端多个接口返回 204 且**没有响应体**（putConnector、
// setPassword、logout）。直接 await res.json() 会抛 SyntaxError——
// 表现是"保存明明成功了，界面却弹了一个看不懂的错误"。
test('204 无响应体时正常返回而不是抛解析错误', async () => {
  stubFetch(new Response(null, { status: 204 }))
  await expect(api.put('/applications/1/connectors/password', { enabled: true })).resolves.toBeUndefined()
})

test('错误响应取后端的 msg 字段作为消息', async () => {
  stubFetch(new Response(JSON.stringify({ code: 'APP_NOT_FOUND', msg: '应用不存在' }), {
    status: 404,
    headers: { 'Content-Type': 'application/json' },
  }))
  await expect(api.get('/applications/nope')).rejects.toMatchObject({
    status: 404,
    message: '应用不存在',
  })
})

// 【辨别力】反代返回的 502、或路径写错拿到一整页 HTML 时，响应体不是
// JSON。此时必须退回状态码兜底，既不能抛解析错误，也不能把一整页 HTML
// 塞进提示框。
test('响应体不是 JSON 时退回状态码提示，不抛解析错误', async () => {
  stubFetch(new Response('<!doctype html><h1>502 Bad Gateway</h1>', {
    status: 502,
    headers: { 'Content-Type': 'text/html' },
  }))
  // as ApiError：api.get<T> 在这里没有显式类型实参、也没有别的推导来源，
  // T 会推成 unknown，.catch((e) => e) 的返回值因此也是 unknown——加个类型
  //断言只是让 tsc 满意，不影响下面这几个运行时断言在测什么。
  const err = (await api.get('/applications').catch((e) => e)) as ApiError
  expect(err).toBeInstanceOf(ApiError)
  expect(err.status).toBe(502)
  expect(err.message).toContain('502')
  expect(err.message).not.toContain('<')
})

test('401 触发未授权回调并且照样抛错', async () => {
  const onUnauth = vi.fn()
  setUnauthorizedHandler(onUnauth)
  stubFetch(new Response(JSON.stringify({ code: 'ADMIN_SESSION_INVALID', msg: '管理端登录已过期，请重新登录' }), { status: 401 }))

  await expect(api.get('/me')).rejects.toBeInstanceOf(ApiError)
  expect(onUnauth).toHaveBeenCalledOnce()
})

test('POST 带 JSON 请求体与 Content-Type', async () => {
  const spy = stubFetch(new Response('{}', { status: 200 }))
  await api.post('/applications', { name: 'A', code: 'a' })

  const init = spy.mock.calls[0][1] as RequestInit
  expect(init.method).toBe('POST')
  expect(init.body).toBe(JSON.stringify({ name: 'A', code: 'a' }))
  expect(new Headers(init.headers).get('Content-Type')).toContain('application/json')
})

// GET 不该带 Content-Type：带上会让某些反代对无体请求做多余处理。
test('GET 不带请求体也不带 Content-Type', async () => {
  const spy = stubFetch(new Response('{}', { status: 200 }))
  await api.get('/applications')
  const init = spy.mock.calls[0][1] as RequestInit
  expect(init.body).toBeUndefined()
  expect(new Headers(init.headers).get('Content-Type')).toBeNull()
})

// 【辨别力】fetch 在网络层直接失败（断网、连接被拒、fp 服务没启动）时抛出
// 的是浏览器原生 TypeError，消息类似 "Failed to fetch"——不是 ApiError，
// 也不是中文。本项目 UI 文案一律简体中文，这条错误必须在 api.ts 里就地
// 包装掉，不能让英文原文一路冒泡到 Login 页面的错误提示上。
test('网络层失败时包装为 ApiError，状态码 0，消息为中文且不含原始英文', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))

  const err = (await api.get('/applications').catch((e) => e)) as ApiError
  expect(err).toBeInstanceOf(ApiError)
  expect(err.status).toBe(0)
  expect(err.message).toMatch(/[一-龥]/)
  expect(err.message).not.toContain('Failed to fetch')
  expect(err.message).not.toContain('fetch')
})

test('PATCH 请求方法正确', async () => {
  const spy = stubFetch(new Response('{}', { status: 200 }))
  await api.patch('/applications/1', { name: 'B' })
  const init = spy.mock.calls[0][1] as RequestInit
  expect(init.method).toBe('PATCH')
})

test('DELETE 请求方法正确', async () => {
  const spy = stubFetch(new Response(null, { status: 204 }))
  await api.del('/applications/1')
  const init = spy.mock.calls[0][1] as RequestInit
  expect(init.method).toBe('DELETE')
})

// 【辨别力】后端的 code 与 detail 必须原样透到 ApiError 上。
//
// 只断言 message 的话，一个把 code/detail 丢掉的实现照样全绿——而页面
// 想按 code 分支（比如账号被冻结时给一个"联系管理员"的入口）就没了依据。
test('错误响应把 code 与 detail 一并透出来', async () => {
  stubFetch(
    new Response(
      JSON.stringify({
        code: 'ACCOUNT_FROZEN',
        msg: '账号已被冻结',
        detail: '{"desc":"user=01a0 status=FROZEN"}',
      }),
      { status: 403, headers: { 'Content-Type': 'application/json' } },
    ),
  )

  const err = (await api.get('/users/x').catch((e) => e)) as ApiError
  expect(err).toBeInstanceOf(ApiError)
  expect(err.status).toBe(403)
  expect(err.message).toBe('账号已被冻结')
  expect(err.code).toBe('ACCOUNT_FROZEN')
  expect(JSON.parse(err.detail).desc).toBe('user=01a0 status=FROZEN')
})

// 后端没给 code/detail（例如反代返回的非 fp 响应）时退化成空串，
// 不是 undefined——页面里 err.code === 'X' 这种判断不该撞上 undefined。
test('缺少 code 与 detail 时退化成空串', async () => {
  stubFetch(new Response(JSON.stringify({ msg: '出错了' }), { status: 500 }))

  const err = (await api.get('/x').catch((e) => e)) as ApiError
  expect(err.code).toBe('')
  expect(err.detail).toBe('')
})

// ---- 统一响应格式 2：请求带 X-Resp-Format: 2，响应回带同名头才按新格式解析 ----

function v2(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', 'X-Resp-Format': '2' },
  })
}

test('请求带 X-Resp-Format: 2', async () => {
  const spy = stubFetch(new Response('{}', { status: 200 }))
  await api.get('/x')
  const init = spy.mock.calls[0][1] as RequestInit
  expect(new Headers(init.headers).get('X-Resp-Format')).toBe('2')
})

test('新格式成功体只取 data', async () => {
  stubFetch(v2({ data: { items: [1], total: 1 }, rid: 'r1', time: 1 }))
  await expect(api.get('/x')).resolves.toEqual({ items: [1], total: 1 })
})

test('响应没回带格式头时按旧格式原样返回', async () => {
  stubFetch(new Response(JSON.stringify({ data: 'x' }), { status: 200 }))
  await expect(api.get('/x')).resolves.toEqual({ data: 'x' })
})

test('新格式 401 带 relogin：触发未授权回调', async () => {
  const onUnauth = vi.fn()
  setUnauthorizedHandler(onUnauth)
  stubFetch(v2({ code: 'unauthorized', msg: '请先登录', data: { relogin: true }, rid: 'r2', time: 1 }, 401))
  const err = (await api.get('/x').catch((e) => e)) as ApiError
  expect(onUnauth).toHaveBeenCalledTimes(1)
  expect(err.rid).toBe('r2')
})

// 没权限也是 401，但不是登录失效：触发回调会让用户点一个没权限的功能就被踢回登录页。
test('新格式 401 没有 relogin（没权限）：不触发未授权回调', async () => {
  const onUnauth = vi.fn()
  setUnauthorizedHandler(onUnauth)
  stubFetch(v2({ code: 'forbidden', msg: '没有权限执行此操作', rid: 'r3', time: 1 }, 401))
  const err = (await api.get('/x').catch((e) => e)) as ApiError
  expect(onUnauth).not.toHaveBeenCalled()
  expect(err.code).toBe('forbidden')
})

test('新格式 400：data 作为 detail 透出', async () => {
  stubFetch(v2({ code: 'INVALID_ARGUMENT', msg: '参数不对', data: { field: 'name' }, rid: 'r4', time: 1 }, 400))
  const err = (await api.post('/x', {}).catch((e) => e)) as ApiError
  expect(err.status).toBe(400)
  expect(JSON.parse(err.detail)).toEqual({ field: 'name' })
})

test('isNotFound：旧格式 404 与新格式 not_found / *_NOT_FOUND 都认', () => {
  expect(isNotFound(new ApiError(404, 'x'))).toBe(true)
  expect(isNotFound(new ApiError(400, 'x', 'not_found'))).toBe(true)
  expect(isNotFound(new ApiError(400, 'x', 'CONFIG_VERSION_NOT_FOUND'))).toBe(true)
  expect(isNotFound(new ApiError(400, 'x', 'INVALID_ARGUMENT'))).toBe(false)
  expect(isNotFound(new Error('x'))).toBe(false)
})
