import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LabelHint } from '@/components/ui/label-hint'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { copyToClipboard } from '@/lib/clipboard'
import { errorMessage, useResource } from '@/lib/useResource'
import { toastFormErrors } from '@/lib/formErrors'
import { useCurrentApp } from '@/lib/current-app'
import { formatTime } from '@/lib/format'
import { applicationStatusLabels } from '@/lib/labels'
import { applicationStatusBadgeClassName, GRAY } from '@/lib/status-badge'
import type { Application, CreateApplicationResponse, Role } from '@/lib/types'

/** 「不设默认角色」在 Select 里的占位值。base-ui 的 SelectItem 不接受空串。 */
const NO_DEFAULT_ROLE = '__none__'

const createSchema = z.object({
  name: z.string().min(1, '请输入应用名称'),
  code: z
    .string()
    .min(1, '请输入 code')
    .regex(/^[a-z0-9][a-z0-9-]*$/, 'code 只能用小写字母、数字和连字符，且不能以连字符开头'),
  cookieDomain: z.string(),
})
type CreateValues = z.infer<typeof createSchema>

const editSchema = z.object({
  name: z.string().min(1, '请输入应用名称'),
  cookieDomain: z.string(),
})
type EditValues = z.infer<typeof editSchema>

export default function Applications() {
  const { apps, currentAppId, setCurrentAppId, loading, error, reload } = useCurrentApp()
  const navigate = useNavigate()
  const roles = useResource(() => api.get<Role[]>('/roles'), [])
  const [creating, setCreating] = useState(false)
  const [newSecret, setNewSecret] = useState<CreateApplicationResponse | null>(null)
  const [disabling, setDisabling] = useState<(typeof apps)[number] | null>(null)
  const [editing, setEditing] = useState<(typeof apps)[number] | null>(null)
  const [deleting, setDeleting] = useState<(typeof apps)[number] | null>(null)

  async function toggleStatus(app: (typeof apps)[number]) {
    const next = app.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE'
    try {
      await api.patch(`/applications/${app.id}/status`, { status: next })
      toast.success(next === 'ACTIVE' ? '已启用' : '已停用')
      reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  async function deleteApp(app: (typeof apps)[number]) {
    try {
      await api.del(`/applications/${app.id}`)
      toast.success('已删除')
      reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <Button onClick={() => setCreating(true)}>新建应用</Button>
      </div>

      {/* 只在真正的首次加载（还没有任何数据）时显示这行文字——启用/停用/
          删除之后的 reload() 也会把 loading 短暂置回 true，但 apps 里还留着
          刷新前的数据，这时候再显示"加载中…"只会在表格上方插入/抽走一行，
          造成一次没有必要的跳动，数据到了之后表格自己会原地更新。 */}
      {loading && apps.length === 0 && <p className="text-sm text-muted-foreground">加载中…</p>}
      {error && <p className="text-sm text-destructive">{error}</p>}

      {!loading && apps.length === 0 && (
        <p className="text-center text-sm text-muted-foreground">没有数据</p>
      )}

      {apps.length > 0 && (
        <Table className="table-fixed">
          {/* 操作列要求死宽 180px（3 个 60px 按钮），其余 8 列按百分比分配——
              两者混在一起没法精确算到刚好 100%，靠 colgroup + table-fixed
              固定住每列，实测有出入再调百分比。 */}
          <colgroup>
            <col className="w-[6%]" />
            <col className="w-[13%]" />
            <col className="w-[13%]" />
            <col className="w-[9%]" />
            <col className="w-[9%]" />
            <col className="w-[11%]" />
            <col className="w-[12%]" />
            <col className="w-[6%]" />
            <col className="w-[10%]" />
            {/* 196px = 180px 按钮预留区 + 单元格左右各 8px 的 padding（px-2）——
                列宽包含 padding，不然按钮区域会被 padding 挤得不够 180px。 */}
            <col className="w-[196px]" />
          </colgroup>
          <TableHeader>
            <TableRow>
              <TableHead className="p-0 px-2">序号</TableHead>
              <TableHead className="p-0 px-2">appId</TableHead>
              <TableHead className="p-0 px-2">名称</TableHead>
              <TableHead className="p-0 px-2">code</TableHead>
              <TableHead className="p-0 px-2">默认角色</TableHead>
              <TableHead className="p-0 px-2">Cookie 作用域</TableHead>
              <TableHead className="p-0 px-2">IM</TableHead>
              <TableHead className="p-0 px-2">状态</TableHead>
              <TableHead className="p-0 px-2">创建时间</TableHead>
              <TableHead className="p-0 px-2">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {apps.map((a, i) => {
              const isCurrent = a.id === currentAppId
              return (
                <TableRow
                  key={a.id}
                  // 行高固定，不随内容是 1 行还是 2 行变化——去掉了单元格
                  // padding，靠这个固定高度 + align-middle 把内容上下居中，
                  // 两行内容也不会把行撑高，只有极端超长（3 行起）才会撑开。
                  className="h-12 cursor-pointer"
                  onClick={() => navigate(`/applications/${a.id}`)}
                >
                  <TableCell className="p-0 px-2 text-muted-foreground">{i + 1}</TableCell>
                  <TableCell className="whitespace-normal break-all p-0 px-2 font-mono text-xs text-muted-foreground">
                    {a.appId}
                  </TableCell>
                  <TableCell className="whitespace-normal p-0 px-2">
                    <div className="flex items-center gap-1.5 font-medium">
                      <span className="break-words">{a.name}</span>
                      {isCurrent && <Badge variant="outline">当前</Badge>}
                    </div>
                  </TableCell>
                  <TableCell className="whitespace-normal break-all p-0 px-2 text-muted-foreground">
                    {a.code}
                  </TableCell>
                  <TableCell className="whitespace-normal break-all p-0 px-2 text-muted-foreground">
                    {a.defaultRoleKey || '-'}
                  </TableCell>
                  <TableCell className="whitespace-normal break-all p-0 px-2 text-muted-foreground">
                    {a.cookieDomain || '-'}
                  </TableCell>
                  <TableCell className="whitespace-normal break-all p-0 px-2 text-muted-foreground">
                    {a.im.enabled ? (a.im.bizAuth?.verifyUrl ?? '-') : '停用'}
                  </TableCell>
                  <TableCell className="p-0 px-2">
                    <Badge className={applicationStatusBadgeClassName[a.status] ?? GRAY}>
                      {applicationStatusLabels[a.status] ?? a.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="whitespace-normal p-0 px-2 text-muted-foreground">
                    {formatTime(a.createdAt)}
                  </TableCell>
                  <TableCell className="p-0 px-2">
                    {/* 固定宽度放在这个 flex 容器上，而不是 <td> 自己的 width——
                        table-layout: auto 下 <td> 的 width 只是个建议值，行里
                        实际按钮数量不同时列宽还是会跟着变。给内容套一层固定
                        宽度的容器，才能真正撑住这一列，不管当前行是 2 个还是
                        3 个按钮，列宽都不跳。按钮本身大小、间距都不改，
                        180px（3 个两字按钮的预估上限）只是给这一列预留的
                        空间，靠左对齐。 */}
                    <div className="flex w-[180px] gap-2">
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation()
                          setEditing(a)
                        }}
                      >
                        编辑
                      </Button>
                      <Button
                        variant={a.status === 'ACTIVE' ? 'destructive' : 'default'}
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation()
                          if (a.status === 'ACTIVE') setDisabling(a)
                          else void toggleStatus(a)
                        }}
                      >
                        {a.status === 'ACTIVE' ? '停用' : '启用'}
                      </Button>
                      {a.status === 'DISABLED' && (
                        <Button
                          variant="destructive"
                          size="sm"
                          onClick={(e) => {
                            e.stopPropagation()
                            setDeleting(a)
                          }}
                        >
                          删除
                        </Button>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}

      <CreateDialog
        open={creating}
        onOpenChange={setCreating}
        roles={roles.data ?? []}
        onCreated={(res) => {
          setCreating(false)
          setNewSecret(res)
          reload()
          setCurrentAppId(res.application.id)
        }}
      />

      <SecretDialog value={newSecret} onClose={() => setNewSecret(null)} />

      {editing && (
        <EditDialog
          app={editing}
          roles={roles.data ?? []}
          onOpenChange={(v) => !v && setEditing(null)}
          onSaved={() => {
            setEditing(null)
            reload()
          }}
        />
      )}

      <ConfirmDialog
        open={disabling !== null}
        onOpenChange={(v) => !v && setDisabling(null)}
        title="停用应用"
        description={
          disabling
            ? `停用后，「${disabling.name}」的全部新登录与 SDK 的下一次回源校验都会被拒绝。已签发的 token 最多还能在各接入方本地缓存里存活 ${disabling.session.tokenCacheTtlSeconds} 秒。此操作可以随时再次启用撤销，但停用期间会中断所有依赖该应用登录的下游服务。`
            : ''
        }
        confirmLabel="确认停用"
        onConfirm={() => {
          const a = disabling
          setDisabling(null)
          if (a) void toggleStatus(a)
        }}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="删除应用"
        description={
          deleting
            ? `删除「${deleting.name}」不可撤销：appId/appSecret 永久失效，SDK 无法再回源校验；这个应用的登录方式配置、用户在它下面的默认角色记录、全部权限点（连同角色对这些权限点的授权）、配置中心的版本历史会一并删掉。已签发的 token 不会被单独撤销，但 SDK 的下一次回源校验会直接失败。此操作无法恢复，确定要继续吗？`
            : ''
        }
        confirmLabel="确认删除"
        onConfirm={() => {
          const a = deleting
          setDeleting(null)
          if (a) void deleteApp(a)
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
  onCreated: (res: CreateApplicationResponse) => void
}) {
  const { register, handleSubmit, formState, reset } = useForm<CreateValues>({
    resolver: zodResolver(createSchema),
    defaultValues: { name: '', code: '', cookieDomain: '' },
  })
  // 默认角色不进 react-hook-form 的校验——它是个 Select，没有校验规则，
  // 用本地 state 更直白（跟 RoleDetail 的父角色 Select 是同一个理由）。
  const [defaultRoleKey, setDefaultRoleKey] = useState('')

  async function onSubmit(v: CreateValues) {
    try {
      // 创建接口只接受 name/code——app_id、app_secret 都是服务端生成的，
      // 混进同一个请求体里不合适。cookieDomain/默认角色留空就不需要这次
      // 多余的请求，大多数应用创建时也确实不填这两项。
      const res = await api.post<CreateApplicationResponse>('/applications', { name: v.name, code: v.code })
      if (v.cookieDomain.trim() !== '') {
        res.application = await api.patch<Application>(`/applications/${res.application.id}`, {
          cookieDomain: v.cookieDomain,
        })
      }
      if (defaultRoleKey !== '') {
        res.application = await api.patch<Application>(`/applications/${res.application.id}/default-role`, {
          roleKey: defaultRoleKey,
        })
      }
      reset()
      setDefaultRoleKey('')
      onCreated(res)
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建应用</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit, toastFormErrors)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="name">名称</Label>
            <Input id="name" {...register('name')} />
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="code">code</Label>
              <LabelHint>创建后不可修改。</LabelHint>
            </div>
            <Input id="code" placeholder="my-app" {...register('code')} />
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="defaultRole">默认角色</Label>
              <LabelHint>
                在这个应用里，每个人都自动拥有这个角色，不写任何数据——新用户注册时不需要建一条授权记录。
                显式分配的角色是在它之上叠加，不是替换。
              </LabelHint>
            </div>
            <Select
              value={defaultRoleKey || NO_DEFAULT_ROLE}
              onValueChange={(v) => setDefaultRoleKey(v === NO_DEFAULT_ROLE || v === null ? '' : v)}
              items={[
                { value: NO_DEFAULT_ROLE, label: '不设默认角色' },
                ...roles.map((r) => ({ value: r.code, label: r.code })),
              ]}
            >
              <SelectTrigger id="defaultRole">
                <SelectValue placeholder="不设默认角色" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_DEFAULT_ROLE}>不设默认角色</SelectItem>
                {roles.map((r) => (
                  <SelectItem key={r.id} value={r.code}>
                    {r.code}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="cookieDomain">Cookie 作用域</Label>
            <Input id="cookieDomain" placeholder="example.com" {...register('cookieDomain')} />
          </div>
          <DialogFooter>
            <Button type="submit" disabled={formState.isSubmitting}>创建</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function EditDialog({
  app,
  roles,
  onOpenChange,
  onSaved,
}: {
  app: Application
  roles: Role[]
  onOpenChange: (v: boolean) => void
  onSaved: () => void
}) {
  const { register, handleSubmit, formState } = useForm<EditValues>({
    resolver: zodResolver(editSchema),
    defaultValues: { name: app.name, cookieDomain: app.cookieDomain },
  })
  const [defaultRoleKey, setDefaultRoleKey] = useState(app.defaultRoleKey)

  async function onSubmit(v: EditValues) {
    try {
      await api.patch(`/applications/${app.id}`, v)
      if (defaultRoleKey !== app.defaultRoleKey) {
        await api.patch(`/applications/${app.id}/default-role`, { roleKey: defaultRoleKey })
      }
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
          <DialogTitle>编辑应用</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit, toastFormErrors)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="edit-name">名称</Label>
            <Input id="edit-name" {...register('name')} />
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="edit-defaultRole">默认角色</Label>
              <LabelHint>
                在这个应用里，每个人都自动拥有这个角色，不写任何数据——新用户注册时不需要建一条授权记录。
                显式分配的角色是在它之上叠加，不是替换。
              </LabelHint>
            </div>
            <Select
              value={defaultRoleKey || NO_DEFAULT_ROLE}
              onValueChange={(v) => setDefaultRoleKey(v === NO_DEFAULT_ROLE || v === null ? '' : v)}
              items={[
                { value: NO_DEFAULT_ROLE, label: '不设默认角色' },
                ...roles.map((r) => ({ value: r.code, label: r.code })),
              ]}
            >
              <SelectTrigger id="edit-defaultRole">
                <SelectValue placeholder="不设默认角色" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_DEFAULT_ROLE}>不设默认角色</SelectItem>
                {roles.map((r) => (
                  <SelectItem key={r.id} value={r.code}>
                    {r.code}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-cookieDomain">Cookie 作用域</Label>
            <Input id="edit-cookieDomain" placeholder="example.com" {...register('cookieDomain')} />
          </div>
          <DialogFooter>
            <Button type="submit" disabled={formState.isSubmitting}>保存</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** copyAppSecret 把 appSecret 写入剪贴板，失败时给出提示而不是悄悄没反应。 */
async function copyAppSecret(secret: string) {
  if (await copyToClipboard(secret)) toast.success('已复制')
  else toast.error('复制失败，请手动选中复制')
}

/**
 * SecretDialog 展示刚创建出的明文 appSecret。
 *
 * 后端只在创建响应里返回这一次，之后无法读回（库里只有 bcrypt 哈希）。
 * 所以这个弹窗必须说清楚"关掉就没了"，否则用户会以为随时能回来看。
 */
function SecretDialog({ value, onClose }: { value: CreateApplicationResponse | null; onClose: () => void }) {
  return (
    <Dialog
      open={value !== null}
      // appSecret 只在创建时返回这一次，后端库里只存了 bcrypt 哈希，没有
      // "重新生成"接口——意外关闭这个弹窗（手滑点到遮罩外、习惯性按
      // Escape）会让密钥永久丢失，只能整个应用作废重建，appId 也会跟着变，
      // 下游已配置的 SDK 接入方随之失效。disablePointerDismissal 在源头
      // 拦掉点遮罩（outsidePress）；但它只管 outsidePress，不管 Escape
      // （@base-ui/react 的 DialogInteractions 里 escapeKey 判断只看
      // isTopmost，不看 disablePointerDismissal，两者是分开的开关）。所以
      // onOpenChange 干脆不响应任何 base-ui 内部发起的关闭请求（Escape、
      // 万一 showCloseButton default 被升级改回来时的 X 按钮）——唯一能
      // 关闭它的只有下面"我已保存"按钮直接调用的 onClose()，不经过这里。
      onOpenChange={() => {}}
      disablePointerDismissal
    >
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>应用已创建</DialogTitle>
        </DialogHeader>
        {value && (
          <div className="space-y-3">
            <div className="space-y-1">
              <Label>appId</Label>
              <div className="rounded-md border bg-muted/40 p-2 font-mono text-sm break-all">
                {value.application.appId}
              </div>
            </div>
            <div className="space-y-1">
              <Label>appSecret</Label>
              <div className="rounded-md border bg-muted/40 p-2 font-mono text-sm break-all">{value.appSecret}</div>
            </div>
            <p className="text-sm text-destructive">
              appSecret 只显示这一次，关闭后无法再查看。请立刻复制并妥善保存。
            </p>
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => value && void copyAppSecret(value.appSecret)}>
            复制 appSecret
          </Button>
          <Button onClick={onClose}>我已保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
