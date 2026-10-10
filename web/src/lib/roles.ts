/** 内置访客角色的 key：匿名请求只有它，登录用户判定时并入它，访问密钥不拥有它。 */
export const GUEST_ROLE_KEY = 'GUEST'

/** 内置超级管理员角色的 key：持有它就跳过权限检查，任何权限点都放行。 */
export const ADMIN_ROLE_KEY = 'ADMIN'

/** 内置角色不能删除、不能设父角色。 */
export function isBuiltinRole(code: string): boolean {
  return code === GUEST_ROLE_KEY || code === ADMIN_ROLE_KEY
}
