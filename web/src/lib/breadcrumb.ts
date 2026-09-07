export interface BreadcrumbSegment {
  label: string
  to?: string
}

/**
 * 根据当前路径拼面包屑段。只覆盖 routes.tsx 里实际存在的路由形状；
 * 顶级词条的文案要跟 Layout.tsx 侧边栏的导航标签保持一致。未知路径
 * 回退成一段「fp」，不抛错。
 */
export function buildBreadcrumb(pathname: string): BreadcrumbSegment[] {
  const parts = pathname.split('/').filter(Boolean)

  if (parts[0] === 'applications') return [{ label: '应用列表' }]

  if (parts[0] === 'users') {
    if (parts.length === 1) return [{ label: '用户管理' }]
    return [{ label: '用户管理', to: '/users' }, { label: '用户详情' }]
  }

  if (parts[0] === 'roles') {
    if (parts.length === 1) return [{ label: '角色管理' }]
    return [{ label: '角色管理', to: '/roles' }, { label: '角色详情' }]
  }

  if (parts[0] === 'permissions') return [{ label: '权限管理' }]

  if (parts[0] === 'config') {
    if (parts.length === 1) return [{ label: '配置中心' }]
    if (parts[1] === 'versions') return [{ label: '配置中心', to: '/config' }, { label: '版本历史' }]
  }

  return [{ label: 'fp' }]
}
