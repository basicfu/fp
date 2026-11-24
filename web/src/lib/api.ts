// 管理控制台的 HTTP 客户端。
//
// 认证走的是后端在登录时下发的 HttpOnly cookie，所以这里没有任何
// token 存取逻辑——浏览器自动带。开发模式下 Vite 把 /admin/api 代理到
// 本机 fp，浏览器看到的仍是同源，cookie 照常生效。
const BASE = '/admin/api'

// 请求带这个头，服务端按统一响应格式 2 回包（成功 {data,rid,time}，失败 {code,msg,data?,rid,time}）。
// 只认响应里同名的回带头来决定怎么解析：没回带（老版本 fp、网关自己的错误页）就按旧格式处理，
// 不会因为发版顺序不同而解析错。本规范与 kkdesk 统一，改动需两边同步（见 sdk/resp）。
const FORMAT_HEADER = 'X-Resp-Format'

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
  /** 请求编号，用户报"出错了"时给它即可在服务端日志里定位；旧格式响应没有，为空串。 */
  readonly rid: string

  constructor(status: number, message: string, code = '', detail = '', rid = '') {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.detail = detail
    this.rid = rid
  }
}

/**
 * 判断"记录不存在"。新格式下 404 并进了 400，只能认错误码；旧格式仍是 404 + 各资源自己的 *_NOT_FOUND。
 * 页面不要再直接比 status === 404。
 */
export function isNotFound(e: unknown): boolean {
  return e instanceof ApiError && (e.status === 404 || e.code === 'not_found' || e.code.endsWith('_NOT_FOUND'))
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
  const headers: Record<string, string> = { [FORMAT_HEADER]: '2' }
  const init: RequestInit = { method, credentials: 'same-origin', headers }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
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

  const v2 = res.headers.get(FORMAT_HEADER) === '2'
  // 旧格式：任何 401 都是登录失效。新格式：401 还包含"没有权限"，只有 data.relogin 为真才是登录失效，
  // 否则点一个没权限的功能就会被踢回登录页。
  if (res.status === 401 && !v2) {
    onUnauthorized()
  }
  if (!res.ok) {
    const e = await readError(res, v2)
    if (v2 && e.relogin) onUnauthorized()
    throw new ApiError(res.status, e.msg, e.code, e.detail, e.rid)
  }
  // 后端多个接口返回 204 且不带响应体（putConnector、setPassword、
  // logout）。对这些响应调 res.json() 会抛 SyntaxError。
  if (res.status === 204) {
    return undefined as T
  }
  const json = await res.json()
  // 新格式成功体统一是 {data,rid,time}：页面只关心 data，保持各接口原有的返回形状。
  return (v2 ? json?.data : json) as T
}

/**
 * 从错误响应里取出 code / msg 等。
 *
 * 新格式（v2）：{"code","msg","data"?,"rid","time"}，data 是业务详情（没有就不出现），
 * 401 且 data.relogin 为真表示登录态失效。旧格式：{"code","msg","detail"}（见 httpapi/respond.go），
 * detail 是排查用的 JSON 串。msg 面向终端用户可直接展示，code 机器可读。
 *
 * 但反代插进来的 502、或者请求路径写错时，响应体可能是一整页 HTML——
 * 那时用状态码兜底，既不抛解析错误，也不把 HTML 塞进提示框。
 */
async function readError(
  res: Response,
  v2: boolean,
): Promise<{ msg: string; code: string; detail: string; rid: string; relogin: boolean }> {
  try {
    const data = (await res.json()) as { code?: unknown; msg?: unknown; detail?: unknown; data?: unknown; rid?: unknown }
    if (typeof data?.msg === 'string' && data.msg !== '') {
      const extra = v2 && data.data && typeof data.data === 'object' ? (data.data as Record<string, unknown>) : null
      return {
        msg: data.msg,
        code: typeof data.code === 'string' ? data.code : '',
        detail: typeof data.detail === 'string' ? data.detail : extra ? JSON.stringify(extra) : '',
        rid: typeof data.rid === 'string' ? data.rid : '',
        relogin: res.status === 401 && extra?.relogin === true,
      }
    }
  } catch {
    // 不是 JSON，落到下面的兜底
  }
  return { msg: `请求失败（HTTP ${res.status}）`, code: '', detail: '', rid: '', relogin: false }
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}
