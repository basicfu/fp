import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { AppWindow } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import ApplicationSettings from '@/components/ApplicationSettings'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/useResource'
import { useCurrentApp } from '@/lib/current-app'
import { cn } from '@/lib/utils'
import { formatTime } from '@/lib/format'
import { applicationStatusLabels } from '@/lib/labels'
import { applicationStatusBadgeClassName, GRAY } from '@/lib/status-badge'
import type { CreateApplicationResponse } from '@/lib/types'

const createSchema = z.object({
  name: z.string().min(1, '请输入应用名称'),
  slug: z
    .string()
    .min(1, '请输入 slug')
    .regex(/^[a-z0-9][a-z0-9-]*$/, 'slug 只能用小写字母、数字和连字符，且不能以连字符开头'),
})
type CreateValues = z.infer<typeof createSchema>

export default function Applications() {
  const { apps, currentAppId, currentApp, setCurrentAppId, loading, error, reload } = useCurrentApp()
  const [creating, setCreating] = useState(false)
  const [newSecret, setNewSecret] = useState<CreateApplicationResponse | null>(null)

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">应用列表</h1>
        <Button onClick={() => setCreating(true)}>新建应用</Button>
      </div>

      {loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {error && <p className="text-sm text-destructive">{error}</p>}

      {!loading && apps.length === 0 && (
        <p className="text-center text-sm text-muted-foreground">还没有应用</p>
      )}

      {apps.length > 0 && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {apps.map((a) => {
            const isCurrent = a.id === currentAppId
            return (
              <button
                key={a.id}
                type="button"
                onClick={() => setCurrentAppId(a.id)}
                className={cn(
                  'group rounded-xl border bg-card p-4 text-left transition-colors hover:border-primary/50 hover:bg-accent/30',
                  isCurrent && 'border-primary ring-1 ring-primary',
                )}
              >
                <div className="flex items-start justify-between gap-2">
                  <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                    <AppWindow className="size-5" />
                  </div>
                  <div className="flex items-center gap-1.5">
                    {isCurrent && <Badge variant="outline">当前</Badge>}
                    <Badge className={applicationStatusBadgeClassName[a.status] ?? GRAY}>
                      {applicationStatusLabels[a.status] ?? a.status}
                    </Badge>
                  </div>
                </div>
                <div className="mt-3 space-y-1">
                  <div className="font-medium group-hover:underline group-hover:underline-offset-4">{a.name}</div>
                  <div className="text-xs text-muted-foreground">{a.slug}</div>
                </div>
                <div className="mt-4 text-xs text-muted-foreground">创建于 {formatTime(a.createdAt)}</div>
              </button>
            )
          })}
        </div>
      )}

      {currentApp && <ApplicationSettings app={currentApp} onSaved={reload} />}

      <CreateDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(res) => {
          setCreating(false)
          setNewSecret(res)
          reload()
          setCurrentAppId(res.application.id)
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
 * writeText() 也可能因为权限被拒绝等原因 reject。
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
