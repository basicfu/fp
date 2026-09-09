import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { toastFormErrors } from '@/lib/formErrors'
import { formatDuration, formatTime } from '@/lib/format'
import { permissionStatusLabels } from '@/lib/labels'
import type { Application, PermissionPoint, Role } from '@/lib/types'

const NO_DEFAULT = '__none__'

/**
 * PermissionsPanel 是应用详情下的「权限点」页。
 *
 * 权限点属于应用（角色是全局的），所以它挂在应用详情而不是单独一个顶级
 * 菜单。同一页还管应用的默认角色——那也是 per-app 的设置，且与权限点是
 * 同一件事的两面：默认角色决定"新用户零配置能干什么"。
 */
export default function PermissionsPanel({ app, onAppChanged }: { app: Application; onAppChanged: () => void }) {
  const perms = useResource(() => api.get<PermissionPoint[]>(`/applications/${app.id}/permissions`), [app.id])
  const roles = useResource(() => api.get<Role[]>('/roles'), [])
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<PermissionPoint | null>(null)
  const [deleting, setDeleting] = useState<{ p: PermissionPoint; holders: string[] } | null>(null)
  const [keyword, setKeyword] = useState('')

  const all = perms.data ?? []
  const kw = keyword.trim().toLowerCase()
  const shown = kw ? all.filter((p) => p.key.toLowerCase().includes(kw) || p.name.toLowerCase().includes(kw)) : all
  const staleCount = all.filter((p) => p.status === 'stale').length

  /**
   * 删除前先问后端"谁在用它"。
   *
   * 这是唯一的安全网：设计上没有可逆的"停用"中间态，删除就是真删，并且
   * 会连带删掉所有角色对它的授权（数据库外键级联）。所以确认框里必须写
   * 清楚有几个角色会因此掉权限，而不是笼统地问"确定删除吗"。
   */
  async function askDelete(p: PermissionPoint) {
    try {
      const res = await api.get<{ roles: string[] }>(`/permissions/${p.id}/holders`)
      setDeleting({ p, holders: res.roles })
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  async function remove(p: PermissionPoint) {
    try {
      await api.del(`/permissions/${p.id}`)
      toast.success('已删除')
      perms.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  async function setDefaultRole(key: string) {
    try {
      await api.patch(`/applications/${app.id}/default-role`, { roleKey: key })
      toast.success(key ? `新用户默认拥有「${key}」` : '已取消默认角色')
      onAppChanged()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-6">
      <div className="space-y-2 rounded-md border p-4">
        <Label htmlFor="default-role">默认角色</Label>
        <Select
          value={app.defaultRoleKey || NO_DEFAULT}
          onValueChange={(v) => void setDefaultRole(v === NO_DEFAULT || v === null ? '' : v)}
          // items 让 Select.Value 能把受控 value 映射回标签；缺了它，收起
          // 状态在 items 未就绪前会直接显示 value 本身。
          items={[{ value: NO_DEFAULT, label: '不设默认角色' }, ...(roles.data ?? []).map((r) => ({ value: r.key, label: r.key }))]}
        >
          <SelectTrigger id="default-role" className="w-56">
            <SelectValue placeholder="不设默认角色" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NO_DEFAULT}>不设默认角色</SelectItem>
            {(roles.data ?? []).map((r) => (
              <SelectItem key={r.id} value={r.key}>
                {r.key}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-xs text-muted-foreground">
          在这个应用里，每个人都自动拥有这个角色，<strong>不写任何数据</strong>——新用户注册时不需要建一条授权记录。
          显式分配的角色是在它之上<strong>叠加</strong>，不是替换：给某人加了「商城管理员」，他仍然保有默认角色的能力。
        </p>
      </div>

      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            placeholder="按路径或名称筛选"
            className="w-72"
          />
          <div className="flex-1" />
          <Button variant="outline" onClick={() => setAdding(true)}>
            手动添加
          </Button>
        </div>

        {staleCount > 0 && (
          <p className="rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-sm">
            有 {staleCount} 个权限点处于「过渡中」：接入方最近的上报里没有它们。
            这<strong>不代表它们已经没了</strong>——业务服务多半多实例，滚动发布时新旧版本同时在跑、交替上报，
            所以 fp 不会自动删。看「已过渡」那一列，如果已经过了很久还在，多半是真的下线了，可以手动删掉。
          </p>
        )}

        {perms.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
        {perms.error && <p className="text-sm text-destructive">{perms.error}</p>}

        {perms.data && (
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>权限点</TableHead>
                  <TableHead>名称</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>已过渡</TableHead>
                  <TableHead>最近上报</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {all.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={6} className="text-center text-muted-foreground">
                      还没有权限点。接入方用 fpsdk 启动时会自动上报全部路由，不需要写任何注解。
                    </TableCell>
                  </TableRow>
                )}
                {all.length > 0 && shown.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={6} className="text-center text-muted-foreground">
                      没有匹配的权限点
                    </TableCell>
                  </TableRow>
                )}
                {shown.map((p) => (
                  <TableRow key={p.id}>
                    <TableCell className="font-mono text-xs">{p.key}</TableCell>
                    <TableCell className="text-muted-foreground">{p.name || '-'}</TableCell>
                    <TableCell>
                      <Badge
                        variant={
                          p.status === 'normal' ? 'default' : p.status === 'stale' ? 'destructive' : 'secondary'
                        }
                      >
                        {permissionStatusLabels[p.status] ?? p.status}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {p.status === 'stale' ? formatDuration(p.staleForMs) : '-'}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {p.source === 'manual' ? '（手动添加）' : formatTime(p.lastSeenAt)}
                    </TableCell>
                    <TableCell className="space-x-2 text-right">
                      <Button variant="outline" size="sm" onClick={() => setEditing(p)}>
                        编辑
                      </Button>
                      <Button variant="outline" size="sm" onClick={() => void askDelete(p)}>
                        删除
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>

      <AddDialog
        open={adding}
        onOpenChange={setAdding}
        appId={app.id}
        onAdded={() => {
          setAdding(false)
          perms.reload()
        }}
      />

      {editing && (
        <EditDialog
          key={editing.id}
          point={editing}
          onOpenChange={(v) => !v && setEditing(null)}
          onSaved={() => {
            setEditing(null)
            perms.reload()
          }}
        />
      )}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="删除权限点"
        description={
          deleting
            ? deleting.holders.length === 0
              ? `删除「${deleting.p.key}」。当前没有角色持有它，删除不会让任何人掉权限。` +
                `如果接入方的代码里还有这个接口，下次上报会把它重新建出来（那时是一个没有任何授权的新条目）。`
              : `删除「${deleting.p.key}」会同时解除 ${deleting.holders.length} 个角色对它的授权：` +
                `${deleting.holders.join('、')}。这些角色下的用户会立刻失去调用这个接口的能力。` +
                `没有可撤销的"停用"中间态，删掉就是删掉；重新建出来的是一个没有任何授权的新条目。`
            : ''
        }
        confirmLabel="确认删除"
        onConfirm={() => {
          const d = deleting
          setDeleting(null)
          if (d) void remove(d.p)
        }}
      />
    </div>
  )
}

const addSchema = z.object({
  key: z.string().min(1, '请输入权限点标识'),
  name: z.string(),
})
type AddValues = z.infer<typeof addSchema>

function AddDialog({
  open,
  onOpenChange,
  appId,
  onAdded,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  appId: string
  onAdded: () => void
}) {
  const { register, handleSubmit, formState, reset } = useForm<AddValues>({
    resolver: zodResolver(addSchema),
    defaultValues: { key: '', name: '' },
  })

  async function onSubmit(v: AddValues) {
    try {
      await api.post(`/applications/${appId}/permissions`, { ...v, kind: 'api' })
      toast.success('已添加')
      reset()
      onAdded()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>手动添加权限点</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit, toastFormErrors)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="perm-key">标识</Label>
            <Input id="perm-key" placeholder="GET:/orders/{id}" className="font-mono" {...register('key')} />
            <p className="text-xs text-muted-foreground">
              格式是 <span className="font-mono">方法:路由模式</span>，用<strong>路由模式</strong>不是具体 URL——
              写 <span className="font-mono">/orders/{'{id}'}</span> 而不是 <span className="font-mono">/orders/123</span>，
              否则每个 id 都会变成一个独立的权限点。
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="perm-name">名称</Label>
            <Input id="perm-name" placeholder="查看订单详情" {...register('name')} />
            <p className="text-xs text-muted-foreground">可留空。填了之后接入方的上报不会覆盖它。</p>
          </div>
          <p className="text-xs text-muted-foreground">
            手动添加的权限点标为「手动」，<strong>不参与</strong>「过渡中」的判定——接入方没上报它是正常的。
          </p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={formState.isSubmitting}>
              添加
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function EditDialog({
  point,
  onOpenChange,
  onSaved,
}: {
  point: PermissionPoint
  onOpenChange: (v: boolean) => void
  onSaved: () => void
}) {
  const { register, handleSubmit, formState } = useForm<AddValues>({
    resolver: zodResolver(addSchema),
    defaultValues: { key: point.key, name: point.name },
  })

  async function onSubmit(v: AddValues) {
    try {
      await api.patch(`/permissions/${point.id}`, v)
      toast.success('已保存')
      onSaved()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>编辑权限点</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit, toastFormErrors)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="edit-perm-key">标识</Label>
            <Input id="edit-perm-key" className="font-mono" {...register('key')} />
            <p className="text-xs text-muted-foreground">
              标识<strong>可以改</strong>（比如打错了一个字母）：已有的授权按内部 id 关联，会自动跟过来，
              改完立刻推送给接入方。但如果接入方代码里的路由还是旧值，下次上报会把旧的重新建出来。
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-perm-name">名称</Label>
            <Input id="edit-perm-name" {...register('name')} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={formState.isSubmitting}>
              保存
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
