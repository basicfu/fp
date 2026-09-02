/** 用户列表每页条数。 */
export const PAGE_SIZE = 20

export interface UserQuery {
  /** 从 1 开始的页码。 */
  page: number
  keyword?: string
  status?: string
}

/**
 * buildUserQuery 拼出 /users 的查询串。
 *
 * 后端收的是 limit/offset，不是页码——两者只在第 2 页之后才看得出差别，
 * 所以这里单独抽成函数并配了测试，而不是在组件里顺手算。
 */
export function buildUserQuery({ page, keyword, status }: UserQuery): string {
  const p = Math.max(1, Math.floor(page) || 1)
  const q = new URLSearchParams()
  q.set('limit', String(PAGE_SIZE))
  q.set('offset', String((p - 1) * PAGE_SIZE))
  if (keyword) q.set('keyword', keyword)
  if (status) q.set('status', status)
  return q.toString()
}
