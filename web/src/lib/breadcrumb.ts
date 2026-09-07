export interface BreadcrumbSegment {
  label: string
  to?: string
}

/**
 * 根据当前路径拼面包屑段。只覆盖 routes.tsx 里实际存在的路由形状；
 * appName 是 ApplicationDetail.tsx 已经加载到的应用名（见 Layout.tsx 的
 * outlet context），拿不到时退化成「应用详情」这个占位文案，不在这里
 * 发任何请求。未知路径回退成一段「fp」，不抛错。
 */
export function buildBreadcrumb(pathname: string, appName: string | null): BreadcrumbSegment[] {
  const parts = pathname.split('/').filter(Boolean)

  if (parts[0] === 'applications') {
    if (parts.length === 1) return [{ label: '应用' }]
    const id = parts[1]
    const appLabel = appName ?? '应用详情'
    if (parts.length === 2) {
      return [{ label: '应用', to: '/applications' }, { label: appLabel }]
    }
    if (parts[2] === 'config') {
      const base: BreadcrumbSegment[] = [
        { label: '应用', to: '/applications' },
        { label: appLabel, to: `/applications/${id}` },
      ]
      if (parts.length === 3) return [...base, { label: '配置中心' }]
      if (parts[3] === 'versions') {
        return [...base, { label: '配置中心', to: `/applications/${id}/config` }, { label: '版本历史' }]
      }
    }
  }

  if (parts[0] === 'users') {
    if (parts.length === 1) return [{ label: '用户' }]
    return [{ label: '用户', to: '/users' }, { label: '用户详情' }]
  }

  if (parts[0] === 'roles') {
    if (parts.length === 1) return [{ label: '角色' }]
    return [{ label: '角色', to: '/roles' }, { label: '角色详情' }]
  }

  return [{ label: 'fp' }]
}
