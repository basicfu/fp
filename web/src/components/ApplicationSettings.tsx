import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import ConnectorsPanel from '@/components/ConnectorsPanel'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/useResource'
import { applicationStatusLabels } from '@/lib/labels'
import { applicationStatusBadgeClassName, GRAY } from '@/lib/status-badge'
import type { Application, SessionPolicy } from '@/lib/types'

export default function ApplicationSettings({ app, onSaved }: { app: Application; onSaved: () => void }) {
  // 只有"停用"这个方向需要二次确认：它会拒绝该应用的全部新登录与 SDK 回源
  // 校验，是四个破坏性操作之一。"启用"是恢复服务，不是破坏性操作，直接执行。
  const [confirmingDisable, setConfirmingDisable] = useState(false)

  async function toggleStatus() {
    const next = app.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE'
    try {
      await api.patch(`/applications/${app.id}/status`, { status: next })
      toast.success(next === 'ACTIVE' ? '已启用' : '已停用')
      onSaved()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  function onToggleStatusClick() {
    if (app.status === 'ACTIVE') {
      setConfirmingDisable(true)
    } else {
      void toggleStatus()
    }
  }

  return (
    <div className="space-y-6 rounded-xl border p-6">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-lg font-semibold">{app.name}</h2>
        <Badge className={applicationStatusBadgeClassName[app.status] ?? GRAY}>
          {applicationStatusLabels[app.status] ?? app.status}
        </Badge>
        <div className="flex-1" />
        <Button variant={app.status === 'ACTIVE' ? 'destructive' : 'default'} onClick={onToggleStatusClick}>
          {app.status === 'ACTIVE' ? '停用应用' : '启用应用'}
        </Button>
      </div>

      <ConfirmDialog
        open={confirmingDisable}
        onOpenChange={setConfirmingDisable}
        title="停用应用"
        description={`停用后，「${app.name}」的全部新登录与 SDK 的下一次回源校验都会被拒绝。已签发的 token 最多还能在各接入方本地缓存里存活 ${app.session.tokenCacheTtlSeconds} 秒。此操作可以随时再次启用撤销，但停用期间会中断所有依赖该应用登录的下游服务。`}
        confirmLabel="确认停用"
        onConfirm={() => {
          setConfirmingDisable(false)
          void toggleStatus()
        }}
      />

      {app.status === 'DISABLED' && (
        <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm">
          该应用已停用：新的登录会被拒绝，SDK 的下一次回源校验也会被拒绝。
          已签发的 token 最多还能在各接入方本地缓存里存活 {app.session.tokenCacheTtlSeconds} 秒。
        </p>
      )}

      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">基本信息</TabsTrigger>
          <TabsTrigger value="session">会话策略</TabsTrigger>
          <TabsTrigger value="connectors">登录方式</TabsTrigger>
        </TabsList>

        <TabsContent value="basic" className="pt-4">
          <BasicForm app={app} onSaved={onSaved} />
        </TabsContent>

        <TabsContent value="session" className="pt-4">
          <SessionForm app={app} onSaved={onSaved} />
        </TabsContent>

        <TabsContent value="connectors" className="pt-4">
          <ConnectorsPanel appId={app.id} />
        </TabsContent>
      </Tabs>
    </div>
  )
}

const basicSchema = z.object({
  name: z.string().min(1, '请输入应用名称'),
  cookieDomain: z.string(),
})
type BasicValues = z.infer<typeof basicSchema>

function BasicForm({ app, onSaved }: { app: Application; onSaved: () => void }) {
  const { register, handleSubmit, formState } = useForm<BasicValues>({
    resolver: zodResolver(basicSchema),
    defaultValues: { name: app.name, cookieDomain: app.cookieDomain },
  })

  async function onSubmit(v: BasicValues) {
    try {
      await api.patch(`/applications/${app.id}`, v)
      toast.success('已保存')
      onSaved()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Card>
      <CardContent className="pt-6">
        <form onSubmit={handleSubmit(onSubmit)} className="max-w-md space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="name">名称</Label>
            <Input id="name" {...register('name')} />
            {formState.errors.name && <p className="text-sm text-destructive">{formState.errors.name.message}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="cookieDomain">Cookie 作用域</Label>
            <Input id="cookieDomain" placeholder="example.com" {...register('cookieDomain')} />
            <p className="text-xs text-muted-foreground">留空表示不限定。</p>
          </div>
          <div className="space-y-1 text-sm text-muted-foreground">
            <div>slug：<span className="font-mono">{app.slug}</span>（创建后不可修改）</div>
            <div>appId：<span className="font-mono">{app.appId}</span></div>
          </div>
          <Button type="submit" disabled={formState.isSubmitting}>保存</Button>
        </form>
      </CardContent>
    </Card>
  )
}

/**
 * 会话策略的校验规则镜像自后端 domain.SessionPolicy.Validate。
 *
 * 这里做校验只为让用户当场看到问题，**后端始终是权威**：两边一旦不一致，
 * 以后端返回的错误为准（onSubmit 的 catch 会把它显示出来）。绝不能因为
 * 前端拦住了就认为后端可以不校验。
 */
const sessionSchema = z
  .object({
    idleTimeoutSeconds: z.coerce.number().int().positive('必须大于 0'),
    idleTimeoutMobileSeconds: z.coerce.number().int().min(0, '不能为负'),
    maxLifetimeSeconds: z.coerce.number().int().positive('必须大于 0'),
    rotateIntervalSeconds: z.coerce.number().int().positive('必须大于 0'),
    extendIntervalSeconds: z.coerce.number().int().positive('必须大于 0'),
    tokenCacheTtlSeconds: z.coerce.number().int().positive('必须大于 0'),
  })
  .refine((p) => p.rotateIntervalSeconds <= p.maxLifetimeSeconds, {
    path: ['rotateIntervalSeconds'],
    message: '轮换间隔不能大于绝对上限',
  })
  .refine((p) => p.extendIntervalSeconds < p.idleTimeoutSeconds, {
    path: ['extendIntervalSeconds'],
    message: '延期间隔必须小于空闲超时，否则用户会因为"少延"而意外掉线',
  })
  .refine((p) => p.tokenCacheTtlSeconds <= p.idleTimeoutSeconds, {
    path: ['tokenCacheTtlSeconds'],
    message: '缓存窗口不能大于空闲超时，否则 token 过期后仍可能被 SDK 放行',
  })

const sessionFields: { key: keyof SessionPolicy; label: string; help?: string }[] = [
  { key: 'idleTimeoutSeconds', label: '空闲超时（秒）', help: '桌面端多久不活动就掉线' },
  { key: 'idleTimeoutMobileSeconds', label: '移动端空闲超时（秒）', help: '填 0 表示与桌面端相同' },
  { key: 'maxLifetimeSeconds', label: '绝对上限（秒）', help: '不论是否活跃，超过就必须重新登录' },
  { key: 'rotateIntervalSeconds', label: '轮换间隔（秒）' },
  { key: 'extendIntervalSeconds', label: '延期间隔（秒）', help: '降频用，避免每次请求都写 Redis' },
  { key: 'tokenCacheTtlSeconds', label: 'SDK 缓存窗口（秒）', help: '接入方本地缓存 token 校验结果的时长' },
]

function SessionForm({ app, onSaved }: { app: Application; onSaved: () => void }) {
  const { register, handleSubmit, formState } = useForm<SessionPolicy>({
    resolver: zodResolver(sessionSchema),
    defaultValues: app.session,
  })

  async function onSubmit(v: SessionPolicy) {
    try {
      await api.put(`/applications/${app.id}/session`, v)
      toast.success('已保存')
      onSaved()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">会话策略</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit(onSubmit)} className="max-w-md space-y-4" noValidate>
          {sessionFields.map((f) => (
            <div key={f.key} className="space-y-2">
              <Label htmlFor={f.key}>{f.label}</Label>
              <Input id={f.key} type="number" {...register(f.key)} />
              {f.help && <p className="text-xs text-muted-foreground">{f.help}</p>}
              {formState.errors[f.key] && (
                <p className="text-sm text-destructive">{String(formState.errors[f.key]?.message)}</p>
              )}
            </div>
          ))}
          <Button type="submit" disabled={formState.isSubmitting}>保存</Button>
        </form>
      </CardContent>
    </Card>
  )
}
