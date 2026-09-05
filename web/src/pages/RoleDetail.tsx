import { useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { permissionStatusLabels } from '@/lib/labels'
import type { Application, Effect, PermissionPoint, Role, RoleGrant } from '@/lib/types'

/**
 * RoleDetail 是授权编辑器：给一个角色勾选它能用哪些权限点。
 *
 * 为什么要先选应用：角色是全局的，权限点是按应用存的。一个「商城管理员」
 * 只会挂商城的权限点，把三个应用几百个权限点混在一张表里既没法看、也容易
 * 误授。选中的应用记在 URL 上，刷新和分享链接都能回到同一个视图。
 */
export default function RoleDetail() {
  const { id = '' } = useParams()
  const [sp, setSp] = useSearchParams()
  const appId = sp.get('app') ?? ''

  const role = useResource(() => api.get<Role[]>('/roles'), [])
  const apps = useResource(() => api.get<Application[]>('/applications'), [])
  const perms = useResource(
    () => (appId ? api.get<PermissionPoint[]>(`/applications/${appId}/permissions`) : Promise.resolve([])),
    [appId],
  )
  const grants = useResource(() => api.get<{ grants: RoleGrant[] }>(`/roles/${id}/permissions`), [id])

  const appName = apps.data?.find((a) => a.id === appId)?.name
  const r = role.data?.find((x) => x.id === id)
  const parent = r?.parentId ? role.data?.find((x) => x.id === r.parentId) : undefined

  if (role.loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (role.error) return <p className="text-sm text-destructive">{role.error}</p>
  if (!r) return <p className="text-sm text-destructive">角色不存在</p>

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <Link to="/roles" className="text-sm text-muted-foreground underline-offset-4 hover:underline">
          ← 角色
        </Link>
        <h1 className="text-xl font-semibold">{r.key}</h1>
        {r.name !== r.key && <span className="text-sm text-muted-foreground">{r.name}</span>}
        {parent && <Badge variant="secondary">继承自 {parent.key}</Badge>}
      </div>

      {parent && (
        <p className="rounded-md border bg-muted/30 p-3 text-sm text-muted-foreground">
          这里只列出<strong>本角色自己</strong>的授权。从「{parent.key}」继承来的权限不在表里，
          也不能在这里取消——要改得去改「{parent.key}」。本角色自己的设置优先于继承来的。
        </p>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">授权</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-muted-foreground">应用</span>
            <Select
              value={appId || undefined}
              onValueChange={(v) => {
                const q = new URLSearchParams(sp)
                if (v) q.set('app', v)
                else q.delete('app')
                setSp(q)
              }}
            >
              <SelectTrigger className="w-56">
                {/*
                  显式给出要显示的文字。base-ui 的 Select.Value 在拿不到
                  对应 item 的标签时会直接把 value 渲染出来——这里的 value
                  是应用的 UUID，界面上就成了一串没人认得的十六进制。
                  从 URL 带着 ?app= 进来时必然如此（选项还没加载）。
                */}
                <SelectValue placeholder="选择一个应用">{appName}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                {(apps.data ?? []).map((a) => (
                  <SelectItem key={a.id} value={a.id}>
                    {a.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {!appId && <p className="text-sm text-muted-foreground">先选一个应用，再给这个角色分配它的权限点。</p>}

          {appId && (
            <GrantTable
              roleId={id}
              perms={perms}
              grants={grants.data?.grants ?? []}
              loading={perms.loading || grants.loading}
              error={perms.error || grants.error}
              onChanged={grants.reload}
            />
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function GrantTable({
  roleId,
  perms,
  grants,
  loading,
  error,
  onChanged,
}: {
  roleId: string
  perms: { data: PermissionPoint[] | null }
  grants: RoleGrant[]
  loading: boolean
  error: string
  onChanged: () => void
}) {
  const [keyword, setKeyword] = useState('')
  // saving 记的是正在提交的那个权限点 id：只禁用那一行的按钮，不冻结整张表。
  const [saving, setSaving] = useState('')

  const effectOf = new Map<string, Effect>(grants.map((g) => [g.permissionId, g.effect]))

  const all = perms.data ?? []
  const kw = keyword.trim().toLowerCase()
  const shown = kw ? all.filter((p) => p.key.toLowerCase().includes(kw) || p.name.toLowerCase().includes(kw)) : all

  async function set(p: PermissionPoint, next: Effect) {
    setSaving(p.id)
    try {
      await api.put(`/roles/${roleId}/permissions/${p.id}`, { effect: next })
      // 后端在这里推送 PolicyChanged，SDK 会重新拉策略——所以文案说"已生效"
      // 而不是"已保存"：这个改动是立即到达业务服务的，不需要谁去重启。
      toast.success(next === '' ? '已收回，正在推送到接入方' : '已生效，正在推送到接入方')
      onChanged()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving('')
    }
  }

  if (loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (error) return <p className="text-sm text-destructive">{error}</p>

  return (
    <div className="space-y-3">
      <Input
        value={keyword}
        onChange={(e) => setKeyword(e.target.value)}
        placeholder="按路径或名称筛选，例如 orders"
        className="w-72"
      />

      <div className="overflow-x-auto rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>权限点</TableHead>
              <TableHead>名称</TableHead>
              <TableHead>状态</TableHead>
              <TableHead className="text-right">授权</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {all.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  这个应用还没有权限点。接入方启动时 SDK 会自动上报，也可以在应用详情里手动添加。
                </TableCell>
              </TableRow>
            )}
            {all.length > 0 && shown.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  没有匹配的权限点
                </TableCell>
              </TableRow>
            )}
            {shown.map((p) => {
              const cur = effectOf.get(p.id) ?? ''
              return (
                <TableRow key={p.id}>
                  <TableCell className="font-mono text-xs">{p.key}</TableCell>
                  <TableCell className="text-muted-foreground">{p.name || '-'}</TableCell>
                  <TableCell>
                    <Badge variant={p.status === 'normal' ? 'default' : 'secondary'}>
                      {permissionStatusLabels[p.status] ?? p.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right">
                    <EffectPicker
                      value={cur}
                      disabled={saving === p.id}
                      onChange={(next) => void set(p, next)}
                    />
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </div>

      <p className="text-xs text-muted-foreground">
        默认<strong>拒绝</strong>：没有勾「允许」的权限点，这个角色就用不了。
        「拒绝」是显式否决，会盖过同一个人从其他角色拿到的「允许」——只在要挖洞时用。
      </p>
    </div>
  )
}

const options: { value: Effect; label: string }[] = [
  { value: '', label: '未授权' },
  { value: 'allow', label: '允许' },
  { value: 'deny', label: '拒绝' },
]

/**
 * EffectPicker 用三个分段按钮而不是复选框。
 *
 * 效果有三态（未授权 / 允许 / 拒绝），复选框只有两态，勉强表达三态要么加
 * 第二个控件、要么让"不确定"态承担语义——两种都会让人猜。三个按钮把三种
 * 结果和当前状态一次性摊开，不需要猜。
 */
function EffectPicker({
  value,
  disabled,
  onChange,
}: {
  value: Effect
  disabled: boolean
  onChange: (v: Effect) => void
}) {
  return (
    <div className="inline-flex gap-1">
      {options.map((o) => (
        <Button
          key={o.value || 'none'}
          size="sm"
          variant={value === o.value ? (o.value === 'deny' ? 'destructive' : 'default') : 'outline'}
          disabled={disabled || value === o.value}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </Button>
      ))}
    </div>
  )
}
