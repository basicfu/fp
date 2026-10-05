import { vi } from 'vitest'

export interface RecordedCall {
  method: string
  /** 已去掉 /admin/api 前缀，含 query。 */
  url: string
  body: unknown
}

type Route = unknown | ((call: RecordedCall) => unknown)

/**
 * stubApi 用一张 "METHOD /path" → 响应 的路由表替换全局 fetch，并记录每次调用。
 *
 * 路由值可以是：普通对象（200 + JSON）、undefined（204）、Response（原样返回，用来造错误响应），
 * 或者接收调用记录、返回以上三者之一的函数。没登记的路由直接让请求失败，测试里漏准备一条
 * 请求就会立刻暴露，不会悄悄走到别的分支。
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
      if (!(key in routes)) return Promise.reject(new Error(`没准备 ${key}`))
      const route = routes[key]
      const out = typeof route === 'function' ? (route as (c: RecordedCall) => unknown)(call) : route
      if (out instanceof Response) return Promise.resolve(out)
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
