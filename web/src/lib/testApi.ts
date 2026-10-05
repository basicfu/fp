import { vi } from 'vitest'

export interface RecordedCall {
  method: string
  /** 已去掉 /admin/api 前缀，含 query。 */
  url: string
  body: unknown
}

type Reply = object | undefined
type Route = Reply | ((call: RecordedCall) => Reply)

// api.ts 会把 fetch 的拒绝统一包成"无法连接到服务器"，页面照样渲染、测试照样绿；
// 所以没登记的请求单独记下来，由 test-setup.ts 在每个测试结束时断言为空。
const unmatched: string[] = []

/** takeUnmatched 取出并清空本测试里没登记路由的请求。 */
export function takeUnmatched(): string[] {
  return unmatched.splice(0)
}

/**
 * stubApi 用一张 "METHOD /path" → 响应 的路由表替换全局 fetch，并记录每次调用。
 *
 * 路由值可以是：普通对象（200 + JSON）、undefined（204）、Response（每次返回一个副本，用来造错误响应），
 * 或者接收调用记录、返回以上三者之一的函数。没登记的路由让请求失败，并在测试结束时报出来。
 *
 * 要故意模拟网络失败，让路由函数 throw：mock 同步抛错会被 api.ts 接住、变成 ApiError(0)，
 * 不会触发上面的未登记守卫。
 */
export function stubApi(routes: Record<string, Route>): RecordedCall[] {
  const calls: RecordedCall[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const call: RecordedCall = {
        method,
        url: url.replace(/^\/admin\/api/, ''),
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      }
      calls.push(call)
      const key = `${method} ${call.url}`
      if (!(key in routes)) {
        unmatched.push(key)
        return Promise.reject(new Error(`没准备 ${key}`))
      }
      const route = routes[key]
      const out = typeof route === 'function' ? route(call) : route
      // body 只能读一次：同一个 Response 第二次被命中时 readError 会退化成"请求失败（HTTP n）"。
      if (out instanceof Response) return Promise.resolve(out.clone())
      if (out === undefined) return Promise.resolve(new Response(null, { status: 204 }))
      return Promise.resolve(new Response(JSON.stringify(out), { status: 200 }))
    }),
  )
  return calls
}

/** apiError 造一个后端错误响应：{code, msg}。 */
export function apiError(status: number, code: string, msg: string): Response {
  return new Response(JSON.stringify({ code, msg }), { status })
}
