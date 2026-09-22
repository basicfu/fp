import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { LabelHint } from '@/components/ui/label-hint'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import ConnectorsPanel from '@/components/ConnectorsPanel'
import ApplicationIMSettings from '@/components/ApplicationIMSettings'
import IMCredentialCard from '@/components/IMCredentialCard'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/useResource'
import { toastFormErrors } from '@/lib/formErrors'
import type { Application, SessionPolicy } from '@/lib/types'

export default function ApplicationSettings({ app, onSaved }: { app: Application; onSaved: () => void }) {
  return (
    <div className="space-y-6">
      {app.status === 'DISABLED' && (
        <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm">
          该应用已停用：新的登录会被拒绝，SDK 的下一次回源校验也会被拒绝。
          已签发的 token 最多还能在各接入方本地缓存里存活 {app.session.tokenCacheTtlSeconds} 秒。
        </p>
      )}

      <Tabs defaultValue="session">
        <TabsList>
          <TabsTrigger value="session">会话策略</TabsTrigger>
          <TabsTrigger value="connectors">登录方式</TabsTrigger>
          <TabsTrigger value="im">IM 接入</TabsTrigger>
        </TabsList>

        <TabsContent value="session" className="pt-4">
          <SessionForm app={app} onSaved={onSaved} />
        </TabsContent>

        <TabsContent value="connectors" className="pt-4">
          <ConnectorsPanel appId={app.id} />
        </TabsContent>

        <TabsContent value="im" className="space-y-4 pt-4">
          <ApplicationIMSettings app={app} onSaved={onSaved} />
          <IMCredentialCard />
        </TabsContent>
      </Tabs>
    </div>
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

const sessionFields: { key: keyof SessionPolicy; label: string; hint?: string }[] = [
  { key: 'idleTimeoutSeconds', label: '空闲超时（秒）' },
  { key: 'idleTimeoutMobileSeconds', label: '移动端空闲超时（秒）', hint: '填 0 表示与桌面端相同' },
  { key: 'maxLifetimeSeconds', label: '绝对上限（秒）', hint: '不论是否活跃，超过就必须重新登录，与空闲超时是两个独立的上限' },
  { key: 'rotateIntervalSeconds', label: '轮换间隔（秒）' },
  { key: 'extendIntervalSeconds', label: '延期间隔（秒）' },
  { key: 'tokenCacheTtlSeconds', label: 'SDK 缓存窗口（秒）', hint: '接入方本地缓存 token 校验结果的时长' },
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
        <form onSubmit={handleSubmit(onSubmit, toastFormErrors)} className="max-w-md space-y-4" noValidate>
          {sessionFields.map((f) => (
            <div key={f.key} className="space-y-2">
              <div className="flex items-center gap-1.5">
                <Label htmlFor={f.key}>{f.label}</Label>
                {f.hint && <LabelHint>{f.hint}</LabelHint>}
              </div>
              <Input id={f.key} type="number" {...register(f.key)} />
            </div>
          ))}
          <Button type="submit" disabled={formState.isSubmitting}>保存</Button>
        </form>
      </CardContent>
    </Card>
  )
}
