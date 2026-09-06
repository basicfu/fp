import { useState } from 'react'
import { Link, useParams } from 'react-router'
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
import PermissionsPanel from '@/components/PermissionsPanel'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { applicationStatusLabels } from '@/lib/labels'
import type { Application, SessionPolicy } from '@/lib/types'

export default function ApplicationDetail() {
  const { id = '' } = useParams()
  const app = useResource(() => api.get<Application>(`/applications/${id}`), [id])
  // 只有"停用"这个方向需要二次确认：它会拒绝该应用的全部新登录与 SDK 回源
  // 校验，是四个破坏性操作之一。"启用"是恢复服务，不是破坏性操作，直接执行。
  const [confirmingDisable, setConfirmingDisable] = useState(false)

  if (app.loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (app.error) return <p className="text-sm text-destructive">{app.error}</p>
  if (!app.data) return null

  const a = app.data

  async function toggleStatus() {
    const next = a.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE'
    try {
      await api.patch(`/applications/${id}/status`, { status: next })
      toast.success(next === 'ACTIVE' ? '已启用' : '已停用')
      app.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  function onToggleStatusClick() {
    if (a.status === 'ACTIVE') {
      setConfirmingDisable(true)
    } else {
      void toggleStatus()
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">{a.name}</h1>
        <Badge variant={a.status === 'ACTIVE' ? 'default' : 'secondary'}>
          {applicationStatusLabels[a.status] ?? a.status}
        </Badge>
        <div className="flex-1" />
        <Link
          to={`/applications/${id}/config`}
          className="text-sm text-muted-foreground underline-offset-4 hover:underline"
        >
          配置中心
        </Link>
        <Button variant={a.status === 'ACTIVE' ? 'destructive' : 'default'} onClick={onToggleStatusClick}>
          {a.status === 'ACTIVE' ? '停用应用' : '启用应用'}
        </Button>
      </div>

      <ConfirmDialog
        open={confirmingDisable}
        onOpenChange={setConfirmingDisable}
        title="停用应用"
        description={`停用后，「${a.name}」的全部新登录与 SDK 的下一次回源校验都会被拒绝。已签发的 token 最多还能在各接入方本地缓存里存活 ${a.session.tokenCacheTtlSeconds} 秒。此操作可以随时再次启用撤销，但停用期间会中断所有依赖该应用登录的下游服务。`}
        confirmLabel="确认停用"
        onConfirm={() => {
          setConfirmingDisable(false)
          void toggleStatus()
        }}
      />

      {a.status === 'DISABLED' && (
        <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm">
          该应用已停用：新的登录会被拒绝，SDK 的下一次回源校验也会被拒绝。
          已签发的 token 最多还能在各接入方本地缓存里存活 {a.session.tokenCacheTtlSeconds} 秒。
        </p>
      )}

      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">基本信息</TabsTrigger>
          <TabsTrigger value="session">会话策略</TabsTrigger>
          <TabsTrigger value="connectors">登录方式</TabsTrigger>
          <TabsTrigger value="permissions">权限点</TabsTrigger>
        </TabsList>

        <TabsContent value="basic" className="pt-4">
          <BasicForm app={a} onSaved={app.reload} />
        </TabsContent>

        <TabsContent value="session" className="pt-4">
          <SessionForm app={a} onSaved={app.reload} />
        </TabsContent>

        <TabsContent value="connectors" className="pt-4">
          <ConnectorsPanel appId={id} />
        </TabsContent>

        <TabsContent value="permissions" className="pt-4">
          <PermissionsPanel app={a} onAppChanged={app.reload} />
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
  // 显式给 useForm 一个类型实参：sessionSchema 六个字段全用 z.coerce.number()，
  // 但 zod 3 对 coerce 的类型声明和不 coerce 完全一样（input=output=number），
  // 不反映"运行时其实接受字符串再转"这件事。实测发现光这一条本身不会让
  // handleSubmit(onSubmit) 报类型错——之所以显式标注，是为了不依赖"resolver
  // 与 defaultValues 两处推断刚好碰巧一致"这种脆弱的隐式行为：defaultValues
  // 来自 app.session（SessionPolicy），resolver 来自 zodResolver(sessionSchema)，
  // 两者都应该、也确实推出同一个数字形状，但把它写成显式类型实参能让这件事
  // 不必依赖推断，未来升级 zod/@hookform/resolvers 时更不容易因为推断结果
  // 变化而在这里悄悄崩掉。
  const { register, handleSubmit, formState } = useForm<SessionPolicy>({
    resolver: zodResolver(sessionSchema),
    defaultValues: app.session,
  })

  async function onSubmit(v: SessionPolicy) {
    try {
      // 全量替换：后端 decodeJSON 开了 DisallowUnknownFields 且六项都必填，
      // 所以这里必须把六个字段一次性全发出去（路由用的是 PUT 不是 PATCH）。
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
