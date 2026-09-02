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
 * SecretDialog 展示刚创建出的明文 appSecret。
 *
 * 后端只在创建响应里返回这一次，之后无法读回（库里只有 bcrypt 哈希）。
 * 所以这个弹窗必须说清楚"关掉就没了"，否则用户会以为随时能回来看。
 */
function SecretDialog({ value, onClose }: { value: CreateApplicationResponse | null; onClose: () => void }) {
  return (
    <Dialog open={value !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
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
          <Button
            variant="outline"
            onClick={() => {
              if (value) void navigator.clipboard?.writeText(value.appSecret).then(() => toast.success('已复制'))
            }}
          >
            复制 appSecret
          </Button>
          <Button onClick={onClose}>我已保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
