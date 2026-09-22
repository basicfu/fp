import { useState } from 'react'
import { useParams } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { MultiSelect } from '@/components/ui/multi-select'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { GUEST_ROLE_KEY } from '@/lib/roles'
import type { Application, PermissionPoint, Role, RoleGrant } from '@/lib/types'

/**
 * RoleDetail 是授权编辑器：给一个角色勾选它能用哪些权限点。
 *
 * 每个应用一个下拉：角色是全局的，权限点是按应用存的，把所有应用的权限点
 * 混在一个下拉里没法看、也容易跨应用误授。下拉里勾选/取消勾选即时生效
 * （PUT 一次就推送给接入方），不需要额外点保存；勾选"选中全部"/"清空"
 * 会对该应用下所有权限点批量生效。
 *
 * 这里只表达"允许/未授权"二态——"拒绝"（显式否决继承来的允许）这个更
 * 少用的三态语义不再通过这个下拉暴露，GUEST 角色原本"只能配置允许"的
 * 限制也就自然满足了，不需要再单独判断。
 */
export default function RoleDetail() {
  const { id = '' } = useParams()

  const role = useResource(() => api.get<Role[]>('/roles'), [])
  const apps = useResource(() => api.get<Application[]>('/applications'), [])
  const grants = useResource(() => api.get<{ grants: RoleGrant[] }>(`/roles/${id}/permissions`), [id])

  const r = role.data?.find((x) => x.id === id)
  const parent = r?.parentId ? role.data?.find((x) => x.id === r.parentId) : undefined

  if (role.loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (role.error) return <p className="text-sm text-destructive">{role.error}</p>
  if (!r) return <p className="text-sm text-destructive">角色不存在</p>

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">{r.code}</h1>
        {r.name !== r.code && <span className="text-sm text-muted-foreground">{r.name}</span>}
        {parent && <Badge variant="secondary">继承自 {parent.code}</Badge>}
      </div>

      {r.code === GUEST_ROLE_KEY && (
        <p className="rounded-md border bg-muted/30 p-3 text-sm text-muted-foreground">
          GUEST 是内置角色：未登录的请求和所有登录用户都拥有它，访问密钥不拥有它。
        </p>
      )}

      {parent && (
        <p className="rounded-md border bg-muted/30 p-3 text-sm text-muted-foreground">
          这里只列出<strong>本角色自己</strong>的授权。从「{parent.code}」继承来的权限不在下面的下拉里，
          也不能在这里取消——要改得去改「{parent.code}」。本角色自己的设置优先于继承来的。
        </p>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">授权</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {apps.error && <p className="text-sm text-destructive">{apps.error}</p>}
          {grants.error && <p className="text-sm text-destructive">{grants.error}</p>}
          {apps.loading && !apps.data && <p className="text-sm text-muted-foreground">加载中…</p>}
          {apps.data?.length === 0 && (
            <p className="text-sm text-muted-foreground">还没有应用，请先在「应用列表」创建一个。</p>
          )}
          {apps.data?.map((app) => (
            <AppPermissionSelect
              key={app.id}
              app={app}
              roleId={id}
              grants={grants.data?.grants ?? []}
              onChanged={grants.reload}
            />
          ))}
        </CardContent>
      </Card>
    </div>
  )
}

function AppPermissionSelect({
  app,
  roleId,
  grants,
  onChanged,
}: {
  app: Application
  roleId: string
  grants: RoleGrant[]
  onChanged: () => void
}) {
  const perms = useResource(() => api.get<PermissionPoint[]>(`/applications/${app.id}/permissions`), [app.id])
  const [saving, setSaving] = useState(false)

  const points = perms.data ?? []
  const options = points.map((p) => ({ value: p.id, label: p.name ? `${p.key}（${p.name}）` : p.key }))
  const allowed = new Set(grants.filter((g) => g.effect === 'allow').map((g) => g.permissionId))
  const selected = points.filter((p) => allowed.has(p.id)).map((p) => p.id)

  // 批量算出这次和上次比，哪些权限点新勾了、哪些新取消了——不管是点了单
  // 一行、还是点了"选中全部"/"清空"，都走同一套 diff，一次性并发提交。
  async function handleChange(next: string[]) {
    const nextSet = new Set(next)
    const prevSet = new Set(selected)
    const toGrant = next.filter((v) => !prevSet.has(v))
    const toRevoke = selected.filter((v) => !nextSet.has(v))
    if (toGrant.length === 0 && toRevoke.length === 0) return
    setSaving(true)
    try {
      await Promise.all([
        ...toGrant.map((pid) => api.put(`/roles/${roleId}/permissions/${pid}`, { effect: 'allow' })),
        ...toRevoke.map((pid) => api.put(`/roles/${roleId}/permissions/${pid}`, { effect: '' })),
      ])
      // 后端在这里推送 PolicyChanged，SDK 会重新拉策略——所以文案说"已生效"
      // 而不是"已保存"：这个改动是立即到达业务服务的，不需要谁去重启。
      toast.success('已生效，正在推送到接入方')
      onChanged()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-2">
      <div className="text-sm font-medium">{app.name}</div>
      {perms.error && <p className="text-sm text-destructive">{perms.error}</p>}
      {perms.loading && !perms.data && <p className="text-sm text-muted-foreground">加载中…</p>}
      {perms.data && points.length === 0 && (
        <p className="text-sm text-muted-foreground">这个应用还没有权限点。</p>
      )}
      {points.length > 0 && (
        <MultiSelect
          options={options}
          defaultValue={selected}
          onValueChange={(next) => void handleChange(next)}
          placeholder="搜索"
          variant="secondary"
          maxCount={options.length}
          disabled={saving}
        />
      )}
    </div>
  )
}
