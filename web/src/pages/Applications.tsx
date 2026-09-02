import { useState } from 'react'
import { Link } from 'react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatTime } from '@/lib/format'
import type { Application, CreateApplicationResponse } from '@/lib/types'

const createSchema = z.object({
  name: z.string().min(1, '请输入应用名称'),
  slug: z
    .string()
    .min(1, '请输入 slug')
    .regex(/^[a-z0-9][a-z0-9-]*$/, 'slug 只能用小写字母、数字和连字符，且不能以连字符开头'),
})
type CreateValues = z.infer<typeof createSchema>

export default function Applications() {
  const apps = useResource(() => api.get<Application[]>('/applications'), [])
  const [creating, setCreating] = useState(false)
  const [newSecret, setNewSecret] = useState<CreateApplicationResponse | null>(null)

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">应用</h1>
        <Button onClick={() => setCreating(true)}>新建应用</Button>
      </div>

      {apps.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {apps.error && <p className="text-sm text-destructive">{apps.error}</p>}

      {apps.data && (
        <div className="overflow-x-auto rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>slug</TableHead>
                <TableHead>appId</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>创建时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {apps.data.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-muted-foreground">
                    还没有应用
                  </TableCell>
                </TableRow>
              )}
              {apps.data.map((a) => (
                <TableRow key={a.id}>
                  <TableCell>
                    <Link to={`/applications/${a.id}`} className="font-medium underline-offset-4 hover:underline">
                      {a.name}
                    </Link>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{a.slug}</TableCell>
                  <TableCell className="font-mono text-xs">{a.appId}</TableCell>
                  <TableCell>
                    <Badge variant={a.status === 'ACTIVE' ? 'default' : 'secondary'}>
                      {a.status === 'ACTIVE' ? '启用' : '停用'}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{formatTime(a.createdAt)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <CreateDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(res) => {
          setCreating(false)
          setNewSecret(res)
          apps.reload()
        }}
      />

      <SecretDialog value={newSecret} onClose={() => setNewSecret(null)} />
    </div>
  )
}

function CreateDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (res: CreateApplicationResponse) => void
}) {
  const { register, handleSubmit, formState, reset } = useForm<CreateValues>({
    resolver: zodResolver(createSchema),
    defaultValues: { name: '', slug: '' },
  })

  async function onSubmit(v: CreateValues) {
    try {
      const res = await api.post<CreateApplicationResponse>('/applications', v)
      reset()
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
        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="name">名称</Label>
            <Input id="name" {...register('name')} />
            {formState.errors.name && <p className="text-sm text-destructive">{formState.errors.name.message}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="slug">slug</Label>
            <Input id="slug" placeholder="my-app" {...register('slug')} />
            <p className="text-xs text-muted-foreground">创建后不可修改。</p>
            {formState.errors.slug && <p className="text-sm text-destructive">{formState.errors.slug.message}</p>}
          </div>
          <DialogFooter>
            <Button type="submit" disabled={formState.isSubmitting}>创建</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * copyAppSecret 把 appSecret 写入剪贴板，失败时给出提示而不是悄悄没反应。
 *
 * 两种失败都要接住：(1) navigator.clipboard 在非安全上下文（http 且非
 * localhost）下整个是 undefined，直接调用会抛错；(2) 即使存在，
 * writeText() 也可能因为权限被拒绝等原因 reject。之前的写法只有
 * `.then(() => toast.success(...))`，没有 `.catch`——用户点了没反应，
 * 控制台还会留一条未处理的 rejection（与 auth.tsx 的 logout() 是同一类
 * 问题）。
 */
async function copyAppSecret(secret: string) {
  try {
    if (!navigator.clipboard) throw new Error('clipboard API 不可用')
    await navigator.clipboard.writeText(secret)
    toast.success('已复制')
  } catch {
    toast.error('复制失败，请手动选中复制')
  }
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
