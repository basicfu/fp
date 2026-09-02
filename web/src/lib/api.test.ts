import { test, expect, vi, afterEach } from 'vitest'
import { api, ApiError, setUnauthorizedHandler } from './api'

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

test('错误响应取后端的 error 字段作为消息', async () => {
  stubFetch(new Response(JSON.stringify({ error: '应用不存在' }), {
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
  stubFetch(new Response(JSON.stringify({ error: '未登录' }), { status: 401 }))

  await expect(api.get('/me')).rejects.toBeInstanceOf(ApiError)
  expect(onUnauth).toHaveBeenCalledOnce()
})

test('POST 带 JSON 请求体与 Content-Type', async () => {
  const spy = stubFetch(new Response('{}', { status: 200 }))
  await api.post('/applications', { name: 'A', slug: 'a' })

  const init = spy.mock.calls[0][1] as RequestInit
  expect(init.method).toBe('POST')
  expect(init.body).toBe(JSON.stringify({ name: 'A', slug: 'a' }))
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
