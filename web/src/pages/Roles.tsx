import { useState } from 'react'
import { Link } from 'react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatTime } from '@/lib/format'
import type { Role } from '@/lib/types'

/** 「无父角色」在 Select 里的占位值。base-ui 的 SelectItem 不接受空串。 */
const NO_PARENT = '__none__'

// parentId 不进表单校验：它是个 Select，没有任何校验规则。为了读回一个
// 受控值而走 react-hook-form 的 watch()，除了多一层间接，还会让 React
// Compiler 判定整个组件不可 memo（oxlint 的 react(incompatible-library)）。
// 用本地 state 更直白。
const createSchema = z.object({
  key: z.string().min(1, '请输入角色标识'),
  name: z.string().min(1, '请输入显示名'),
})
type CreateValues = z.infer<typeof createSchema>

export default function Roles() {
  const roles = useResource(() => api.get<Role[]>('/roles'), [])
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Role | null>(null)
  const [deleting, setDeleting] = useState<Role | null>(null)

  const list = roles.data ?? []
  // 角色数量是几十级，客户端建映射够用，不值得为它加一个后端接口。
  const nameOf = new Map(list.map((r) => [r.id, r.key]))

  async function remove(role: Role) {
    try {
      await api.del(`/roles/${role.id}`)
      toast.success(`已删除「${role.key}」`)
      roles.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">角色</h1>
        <Button onClick={() => setCreating(true)}>新建角色</Button>
      </div>

      <p className="text-sm text-muted-foreground">
        角色是<strong>全局</strong>的，不属于某个应用——「普通用户」这类角色天然跨应用，
        在商城能下单、在视频能观看，是同一个身份的两面。应用专属的角色靠命名区分
        （商城管理员 / 视频管理员）。一个角色属于哪个应用，由它挂了哪些应用的权限点决定。
      </p>

      {roles.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {roles.error && <p className="text-sm text-destructive">{roles.error}</p>}

      {roles.data && (
        <div className="overflow-x-auto rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>标识</TableHead>
                <TableHead>显示名</TableHead>
                <TableHead>继承自</TableHead>
                <TableHead>创建时间</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-muted-foreground">
                    还没有角色
                  </TableCell>
                </TableRow>
              )}
              {list.map((r) => (
                <TableRow key={r.id}>
                  <TableCell>
                    <Link to={`/roles/${r.id}`} className="font-medium underline-offset-4 hover:underline">
                      {r.key}
                    </Link>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{r.name}</TableCell>
                  <TableCell className="text-muted-foreground">
                    {r.parentId ? (nameOf.get(r.parentId) ?? r.parentId) : '-'}
                  </TableCell>
                  <TableCell className="text-muted-foreground">{formatTime(r.createdAt)}</TableCell>
                  <TableCell className="space-x-2 text-right">
                    <Button variant="outline" size="sm" onClick={() => setEditing(r)}>
                      编辑
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => setDeleting(r)}>
                      删除
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <CreateDialog
        open={creating}
        onOpenChange={setCreating}
        roles={list}
        onCreated={() => {
          setCreating(false)
          roles.reload()
        }}
      />

      {editing && (
        <EditDialog
          key={editing.id}
          role={editing}
          roles={list}
          onOpenChange={(v) => !v && setEditing(null)}
          onSaved={() => {
            setEditing(null)
            roles.reload()
          }}
        />
      )}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="删除角色"
        description={
          deleting
            ? `删除「${deleting.key}」会同时把它从所有持有它的用户身上摘掉，并解除它的全部授权；` +
              `如果有应用把它设成了默认角色，那个设置也会被清空。已登录的用户要等会话刷新后才会失去这个角色。此操作不可撤销。`
            : ''
        }
        confirmLabel="确认删除"
        onConfirm={() => {
          const r = deleting
          setDeleting(null)
          if (r) void remove(r)
        }}
      />
    </div>
  )
}

function CreateDialog({
  open,
  onOpenChange,
  roles,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  roles: Role[]
  onCreated: () => void
}) {
  const { register, handleSubmit, formState, reset } = useForm<CreateValues>({
    resolver: zodResolver(createSchema),
    defaultValues: { key: '', name: '' },
  })
  const [parentId, setParentId] = useState('')

  async function onSubmit(v: CreateValues) {
    try {
      await api.post('/roles', { ...v, parentId })
      toast.success('已创建')
      reset()
      setParentId('')
      onCreated()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建角色</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="key">标识</Label>
            <Input id="key" placeholder="商城管理员" {...register('key')} />
            <p className="text-xs text-muted-foreground">
              创建后<strong>不可修改</strong>：用户身上和已签发的会话里都按这个字符串引用它。
            </p>
            {formState.errors.key && <p className="text-sm text-destructive">{formState.errors.key.message}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="name">显示名</Label>
            <Input id="name" placeholder="商城管理员" {...register('name')} />
            {formState.errors.name && <p className="text-sm text-destructive">{formState.errors.name.message}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="parent">继承自</Label>
            <Select
              value={parentId || NO_PARENT}
              onValueChange={(v) => setParentId(v === NO_PARENT || v === null ? '' : v)}
            >
              <SelectTrigger id="parent">
                <SelectValue placeholder="不继承" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_PARENT}>不继承</SelectItem>
                {roles.map((r) => (
                  <SelectItem key={r.id} value={r.id}>
                    {r.key}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              子角色自动拥有父角色的全部权限；子角色自己的授权优先于继承来的。
            </p>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={formState.isSubmitting}>
              创建
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

const editSchema = z.object({
  name: z.string().min(1, '请输入显示名'),
})
type EditValues = z.infer<typeof editSchema>

function EditDialog({
  role,
  roles,
  onOpenChange,
  onSaved,
}: {
  role: Role
  roles: Role[]
  onOpenChange: (v: boolean) => void
  onSaved: () => void
}) {
  const { register, handleSubmit, formState } = useForm<EditValues>({
    resolver: zodResolver(editSchema),
    defaultValues: { name: role.name },
  })
  const [parentId, setParentId] = useState(role.parentId)

  async function onSubmit(v: EditValues) {
    try {
      await api.patch(`/roles/${role.id}`, { ...v, parentId })
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
          <DialogTitle>编辑「{role.key}」</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="edit-name">显示名</Label>
            <Input id="edit-name" {...register('name')} />
            {formState.errors.name && <p className="text-sm text-destructive">{formState.errors.name.message}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-parent">继承自</Label>
            <Select
              value={parentId || NO_PARENT}
              onValueChange={(v) => setParentId(v === NO_PARENT || v === null ? '' : v)}
            >
              <SelectTrigger id="edit-parent">
                <SelectValue placeholder="不继承" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_PARENT}>不继承</SelectItem>
                {/* 自己不能当自己的父角色。更长的环（A→B→A）由后端在写入时拒绝。 */}
                {roles
                  .filter((r) => r.id !== role.id)
                  .map((r) => (
                    <SelectItem key={r.id} value={r.id}>
                      {r.key}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
          </div>
          <p className="text-xs text-muted-foreground">
            标识 <span className="font-mono">{role.key}</span> 不可修改。
          </p>
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
