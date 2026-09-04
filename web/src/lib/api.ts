// 管理控制台的 HTTP 客户端。
//
// 认证走的是后端在登录时下发的 HttpOnly cookie，所以这里没有任何
// token 存取逻辑——浏览器自动带。开发模式下 Vite 把 /admin/api 代理到
// 本机 fp，浏览器看到的仍是同源，cookie 照常生效。
const BASE = '/admin/api'

/** ApiError 携带 HTTP 状态码，页面据此区分"没权限"和"输入不合法"。 */
export class ApiError extends Error {
  // 不用构造函数参数属性（constructor(readonly status: number)）：
  // 这是非 erasable 语法（会生成运行时赋值代码），当前 tsconfig 开了
  // erasableSyntaxOnly，写成参数属性会让 tsc -b 报 TS1294。
  readonly status: number
  /** 后端的机器可读错误码，例如 ACCOUNT_FROZEN。响应里没有时是空串。 */
  readonly code: string
  /** 后端的诊断详情，JSON 字符串，可能为空。原样打进日志即可。 */
  readonly detail: string

  constructor(status: number, message: string, code = '', detail = '') {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.detail = detail
  }
}

type UnauthorizedHandler = () => void

let onUnauthorized: UnauthorizedHandler = () => {}

/**
 * 注册 401 回调。由路由层调用，用来把用户送回登录页。
 *
 * 放在这里而不是让每个调用方自己判断 401：漏判一处的后果是用户看到
 * 一个"未登录"的错误提示却停在原地，不知道该做什么。
 */
export function setUnauthorizedHandler(fn: UnauthorizedHandler) {
  onUnauthorized = fn
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin' }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }

  let res: Response
  try {
    res = await fetch(BASE + path, init)
  } catch {
    // fetch 在网络层直接失败（断网、连接被拒、fp 服务没启动）时抛出的是
    // 浏览器原生 TypeError（消息类似 "Failed to fetch"），既不是 ApiError，
    // 也不是中文——原样冒泡到页面会让用户在 Login.tsx 的 catch 里看到一句
    // 看不懂的英文。统一包装成 ApiError，状态码用 0 表示"请求没能到达
    // 服务器"（与后端可能返回的任何真实 HTTP 状态码都不冲突）。
    throw new ApiError(0, '无法连接到服务器，请检查网络或确认 fp 服务已启动')
  }

  if (res.status === 401) {
    onUnauthorized()
  }
  if (!res.ok) {
    const e = await readError(res)
    throw new ApiError(res.status, e.msg, e.code, e.detail)
  }
  // 后端多个接口返回 204 且不带响应体（putConnector、setPassword、
  // logout）。对这些响应调 res.json() 会抛 SyntaxError。
  if (res.status === 204) {
    return undefined as T
  }
  return (await res.json()) as T
}

/**
 * 从错误响应里取出 code / msg / detail。
 *
 * 后端的错误一律是 {"code","msg","detail"}（见 httpapi/respond.go）：
 * msg 面向终端用户可直接展示，code 机器可读，detail 是排查用的 JSON 串
 * （没有细节时该字段整个不出现）。
 *
 * 但反代插进来的 502、或者请求路径写错时，响应体可能是一整页 HTML——
 * 那时用状态码兜底，既不抛解析错误，也不把 HTML 塞进提示框。
 */
async function readError(res: Response): Promise<{ msg: string; code: string; detail: string }> {
  try {
    const data = (await res.json()) as { code?: unknown; msg?: unknown; detail?: unknown }
    if (typeof data?.msg === 'string' && data.msg !== '') {
      return {
        msg: data.msg,
        code: typeof data.code === 'string' ? data.code : '',
        detail: typeof data.detail === 'string' ? data.detail : '',
      }
    }
  } catch {
    // 不是 JSON，落到下面的兜底
  }
  return { msg: `请求失败（HTTP ${res.status}）`, code: '', detail: '' }
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}
