/** 用户列表每页条数。 */
export const PAGE_SIZE = 20

export interface UserQuery {
  /** 从 1 开始的页码。 */
  page: number
  keyword?: string
  status?: string
}

/**
 * normalizePage 把任意来源的页码值钳成一个 >= 1 的整数。
 *
 * 调用方不止一处：buildUserQuery 算 offset 要用，Users.tsx 从 URL 读出
 * page 参数、传给 Pagination 组件显示用的也是同一个值。两处各自实现一遍
 * 钳制逻辑的问题在于——以后有人改了其中一处，"URL 里的页码"和"发给
 * 后端的 offset"会悄悄对不上，不会有任何测试或类型错误提醒。所以抽成
 * 这一个函数，两处都调它。
 *
 * 入参故意收 unknown：调用方的原始值可能是 URLSearchParams#get() 返回的
 * `string | null`（缺参数、或被手动改成 "abc"/"-3"/"2.7" 这类非法值），
 * 也可能是内部已经算好的 number。`Number(x)` 对这两类输入都能给出一个
 * 数字（非法输入得到 NaN），下面统一钳到 >= 1 的整数。
 */
export function normalizePage(raw: unknown): number {
  const n = Math.floor(Number(raw)) || 1
  return Math.max(1, n)
}

/**
 * buildUserQuery 拼出 /users 的查询串。
 *
 * 后端收的是 limit/offset，不是页码——两者只在第 2 页之后才看得出差别，
 * 所以这里单独抽成函数并配了测试，而不是在组件里顺手算。
 */
export function buildUserQuery({ page, keyword, status }: UserQuery): string {
  const p = normalizePage(page)
  const q = new URLSearchParams()
  q.set('limit', String(PAGE_SIZE))
  q.set('offset', String((p - 1) * PAGE_SIZE))
  if (keyword) q.set('keyword', keyword)
  if (status) q.set('status', status)
  return q.toString()
}
