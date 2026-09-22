import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LabelHint } from '@/components/ui/label-hint'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { useCurrentApp } from '@/lib/current-app'
import { formatTime } from '@/lib/format'
import { toastFormErrors } from '@/lib/formErrors'
import { GUEST_ROLE_KEY } from '@/lib/roles'
import type { Role } from '@/lib/types'

/** 「无父角色」在 Select 里的占位值。base-ui 的 SelectItem 不接受空串。 */
const NO_PARENT = '__none__'

// parentId 不进表单校验：它是个 Select，没有任何校验规则。为了读回一个
// 受控值而走 react-hook-form 的 watch()，除了多一层间接，还会让 React
// Compiler 判定整个组件不可 memo（oxlint 的 react(incompatible-library)）。
// 用本地 state 更直白。
const createSchema = z.object({
  code: z.string().min(1, '请输入角色 code'),
  name: z.string().min(1, '请输入角色名'),
})
type CreateValues = z.infer<typeof createSchema>

export default function Roles() {
  const navigate = useNavigate()
  const roles = useResource(() => api.get<Role[]>('/roles'), [])
  const { reload: reloadApps } = useCurrentApp()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Role | null>(null)
  const [deleting, setDeleting] = useState<Role | null>(null)

  const list = roles.data ?? []
  // 角色数量是几十级，客户端建映射够用，不值得为它加一个后端接口。
  const nameOf = new Map(list.map((r) => [r.id, r.code]))

  async function remove(role: Role) {
    try {
      await api.del(`/roles/${role.id}`)
      toast.success(`已删除「${role.code}」`)
      roles.reload()
      // 删角色时后端会顺带清空把它设成默认角色的应用（见删除确认文案）。
      // 应用列表用的是 CurrentAppProvider 里全局缓存的一份数据，不会因为
      // 这里的 roles.reload() 自动更新，不单独 reload 的话，应用列表页会
      // 继续显示已经被删掉的角色，直到用户刷新整个页面。
      reloadApps()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-4">
      <div>
        <Button onClick={() => setCreating(true)}>新建角色</Button>
      </div>

      {/* 只在真正首次加载（还没有任何数据）时显示这行文字——增删改之后的
          reload() 也会把 loading 短暂置回 true，这时候表格已经有上一次的
          数据在显示，再插一行"加载中…"只会造成一次没必要的跳动。 */}
      {roles.loading && !roles.data && <p className="text-sm text-muted-foreground">加载中…</p>}
      {roles.error && <p className="text-sm text-destructive">{roles.error}</p>}

      {roles.data && (
        <Table className="table-fixed">
          {/* 操作列固定 136px（120px 按钮预留区 + 单元格左右各 8px padding），
              其余 5 列按百分比分配——跟 Applications 表格是同一套做法。 */}
          <colgroup>
            <col className="w-[6%]" />
            <col className="w-[24%]" />
            <col className="w-[20%]" />
            <col className="w-[16%]" />
            <col className="w-[16%]" />
            <col className="w-[136px]" />
          </colgroup>
          <TableHeader>
            <TableRow>
              <TableHead className="p-0 px-2">序号</TableHead>
              <TableHead className="p-0 px-2">code</TableHead>
              <TableHead className="p-0 px-2">角色名</TableHead>
              <TableHead className="p-0 px-2">继承自</TableHead>
              <TableHead className="p-0 px-2">创建时间</TableHead>
              <TableHead className="p-0 px-2">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-center text-muted-foreground">
                  没有数据
                </TableCell>
              </TableRow>
            )}
            {list.map((r, i) => (
              <TableRow
                key={r.id}
                className="h-12 cursor-pointer"
                onClick={() => navigate(`/roles/${r.id}`)}
              >
                <TableCell className="p-0 px-2 text-muted-foreground">{i + 1}</TableCell>
                <TableCell className="whitespace-normal break-all p-0 px-2">
                  <span className="font-medium">{r.code}</span>
                  {r.code === GUEST_ROLE_KEY && (
                    <Badge variant="secondary" className="ml-2">
                      内置
                    </Badge>
                  )}
                </TableCell>
                <TableCell className="whitespace-normal break-words p-0 px-2 text-muted-foreground">
                  {r.name}
                </TableCell>
                <TableCell className="whitespace-normal break-all p-0 px-2 text-muted-foreground">
                  {r.parentId ? (nameOf.get(r.parentId) ?? r.parentId) : '-'}
                </TableCell>
                <TableCell className="whitespace-normal p-0 px-2 text-muted-foreground">
                  {formatTime(r.createdAt)}
                </TableCell>
                <TableCell className="p-0 px-2">
                  <div className="flex w-[120px] gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={(e) => {
                        e.stopPropagation()
                        setEditing(r)
                      }}
                    >
                      编辑
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      disabled={r.code === GUEST_ROLE_KEY}
                      onClick={(e) => {
                        e.stopPropagation()
                        setDeleting(r)
                      }}
                    >
                      删除
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
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
            ? `删除「${deleting.code}」会同时把它从所有持有它的用户身上摘掉，并解除它的全部授权；` +
              `如果有应用把它设成了默认角色，那个设置也会被清空。已登录的用户要等会话刷新后才会失去这个角色。此操作不可撤销。` +
              `有访问密钥绑定时无法删除，需要先改绑或删除这些密钥。`
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
    defaultValues: { code: '', name: '' },
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
        <form onSubmit={handleSubmit(onSubmit, toastFormErrors)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="code">code</Label>
              <LabelHint>创建后不可修改：用户身上和已签发的会话里都按这个字符串引用它。</LabelHint>
            </div>
            <Input id="code" {...register('code')} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="name">角色名</Label>
            <Input id="name" {...register('name')} />
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="parent">继承自</Label>
              <LabelHint>子角色自动拥有父角色的全部权限；子角色自己的授权优先于继承来的。</LabelHint>
            </div>
            <Select
              value={parentId || NO_PARENT}
              onValueChange={(v) => setParentId(v === NO_PARENT || v === null ? '' : v)}
            >
              <SelectTrigger id="parent">
                {/* 同 RoleDetail：value 是 UUID，拿不到标签时会把它直接显示出来。 */}
                <SelectValue placeholder="不继承">{roles.find((x) => x.id === parentId)?.code}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_PARENT}>不继承</SelectItem>
                {roles.map((r) => (
                  <SelectItem key={r.id} value={r.id}>
                    {r.code}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
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
          <DialogTitle>编辑「{role.code}」</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit, toastFormErrors)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="edit-name">角色名</Label>
            <Input id="edit-name" {...register('name')} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-parent">继承自</Label>
            <Select
              value={parentId || NO_PARENT}
              disabled={role.code === GUEST_ROLE_KEY}
              onValueChange={(v) => setParentId(v === NO_PARENT || v === null ? '' : v)}
            >
              <SelectTrigger id="edit-parent">
                <SelectValue placeholder="不继承">{roles.find((x) => x.id === parentId)?.code}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_PARENT}>不继承</SelectItem>
                {/* 自己不能当自己的父角色。更长的环（A→B→A）由后端在写入时拒绝。 */}
                {roles
                  .filter((r) => r.id !== role.id)
                  .map((r) => (
                    <SelectItem key={r.id} value={r.id}>
                      {r.code}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
            {role.code === GUEST_ROLE_KEY && (
              <p className="text-xs text-muted-foreground">内置角色 GUEST 不能设置父角色。</p>
            )}
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
