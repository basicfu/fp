import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { IpTextarea, RoleSelect, parseIps } from '@/components/AccessKeyFields'
import { DeleteConfirm, StatusConfirm } from '@/components/AccessKeyDialogs'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatMinute } from '@/lib/format'
import { accessKeyStateLabels } from '@/lib/labels'
import type { AccessKey, AppPermissions, Role } from '@/lib/types'

export default function AccessKeyDetail() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const key = useResource(() => api.get<AccessKey>(`/access-keys/${id}`), [id])
  const perms = useResource(() => api.get<AppPermissions[]>(`/access-keys/${id}/permissions`), [id])
  const roles = useResource(() => api.get<Role[]>('/roles'), [])
  const [editing, setEditing] = useState(false)
  const [toggling, setToggling] = useState<AccessKey | null>(null)
  const [deleting, setDeleting] = useState<AccessKey | null>(null)

  if (key.loading && !key.data) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (key.error) return <p className="text-sm text-destructive">{key.error}</p>
  const k = key.data
  if (!k) return null
  const roleId = roles.data?.find((r) => r.key === k.roleKey)?.id

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <Link to="/access-keys" className="text-sm text-muted-foreground underline-offset-4 hover:underline">
          ← 访问密钥
        </Link>
        <h1 className="font-mono text-lg font-semibold">{k.accessKeyId}</h1>
        <Badge variant={k.state === 'active' ? 'default' : 'secondary'}>{accessKeyStateLabels[k.state] ?? k.state}</Badge>
        <div className="ml-auto space-x-2">
          <Button variant="outline" onClick={() => setEditing(true)}>
            编辑
          </Button>
          <Button variant="outline" onClick={() => setToggling(k)}>
            {k.status === 'ACTIVE' ? '停用' : '启用'}
          </Button>
          <Button variant="outline" onClick={() => setDeleting(k)}>
            删除
          </Button>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">基本信息</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid grid-cols-[8rem_1fr] gap-y-2 text-sm">
            <dt className="text-muted-foreground">备注</dt>
            <dd>{k.remark}</dd>
            <dt className="text-muted-foreground">角色</dt>
            <dd>
              {!k.roleKey ? (
                '未绑定'
              ) : roleId ? (
                <Link to={`/roles/${roleId}`} className="underline-offset-4 hover:underline">
                  {k.roleKey}
                </Link>
              ) : (
                k.roleKey
              )}
            </dd>
            <dt className="text-muted-foreground">IP 白名单</dt>
            <dd className="font-mono text-xs">{k.allowedIps.length === 0 ? '不限制' : k.allowedIps.join('、')}</dd>
            <dt className="text-muted-foreground">到期时间</dt>
            <dd>{formatMinute(k.expiresAt, '永不过期')}</dd>
            <dt className="text-muted-foreground">最后使用</dt>
            <dd>{formatMinute(k.lastUsedAt, '从未使用')}</dd>
            <dt className="text-muted-foreground">创建时间</dt>
            <dd>{formatMinute(k.createdAt)}</dd>
          </dl>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">可调用的接口</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-xs text-muted-foreground">
            按应用列出这把 key 当前能调用的接口（角色继承已展开）。给绑定的角色在任何应用增加授权，都会出现在这里。
          </p>
          {perms.error && <p className="text-sm text-destructive">{perms.error}</p>}
          {perms.data?.length === 0 && (
            <p className="text-sm text-muted-foreground">没有可调用的接口（未绑定角色，或角色没有任何授权）。</p>
          )}
          {perms.data?.map((g) => (
            <div key={g.appId} className="space-y-1">
              <h3 className="text-sm font-medium">{g.appName}</h3>
              <ul className="space-y-1">
                {g.points.map((p) => (
                  <li key={p.key} className="flex gap-3 text-sm">
                    <span className="font-mono text-xs">{p.key}</span>
                    {p.name && <span className="text-muted-foreground">{p.name}</span>}
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </CardContent>
      </Card>

      {editing && (
        <EditDialog
          k={k}
          roles={roles.data ?? []}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            key.reload()
            perms.reload()
          }}
        />
      )}
      <StatusConfirm target={toggling} onClose={() => setToggling(null)} onDone={key.reload} />
      <DeleteConfirm target={deleting} onClose={() => setDeleting(null)} onDone={() => navigate('/access-keys')} />
    </div>
  )
}

function EditDialog({
  k,
  roles,
  onClose,
  onSaved,
}: {
  k: AccessKey
  roles: Role[]
  onClose: () => void
  onSaved: () => void
}) {
  const [remark, setRemark] = useState(k.remark)
  const [roleKey, setRoleKey] = useState(k.roleKey)
  const [ips, setIps] = useState(k.allowedIps.join('\n'))
  const [validDays, setValidDays] = useState('')
  const [saving, setSaving] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const body: Record<string, unknown> = { remark: remark.trim(), roleKey, allowedIps: parseIps(ips) }
    // 有效期留空表示不改：只改备注时不能把到期时间顺手重算。
    if (validDays.trim() !== '') {
      const days = Number(validDays)
      if (!Number.isInteger(days) || days < 0) {
        toast.error('有效期必须是不小于 0 的整数')
        return
      }
      body.validDays = days
    }
    setSaving(true)
    try {
      await api.patch(`/access-keys/${k.id}`, body)
      toast.success('已生效，正在推送到业务方')
      onSaved()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>编辑访问密钥</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="edit-remark">备注</Label>
            <Input id="edit-remark" value={remark} onChange={(e) => setRemark(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-role">角色</Label>
            <RoleSelect id="edit-role" roles={roles} value={roleKey} onChange={setRoleKey} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-days">重新设置有效期（天）</Label>
            <Input
              id="edit-days"
              type="number"
              min={0}
              value={validDays}
              onChange={(e) => setValidDays(e.target.value)}
              placeholder="留空表示不修改"
            />
            <p className="text-xs text-muted-foreground">
              当前到期时间：{formatMinute(k.expiresAt, '永不过期')}。填 0 改为永不过期，填 N 从现在起算 N 天。
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-ips">IP 白名单</Label>
            <IpTextarea id="edit-ips" value={ips} onChange={setIps} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              取消
            </Button>
            <Button type="submit" disabled={saving}>
              保存
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
