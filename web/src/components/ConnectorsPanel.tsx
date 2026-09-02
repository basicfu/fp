import { toast } from 'sonner'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { DynamicForm } from './DynamicForm'
import type { ConnectorConfig, ConnectorSchema } from '@/lib/types'

/** 登录方式类型的中文名。认不出来的类型直接显示原始类型名，不阻塞。 */
const typeLabels: Record<string, string> = {
  password: '密码登录',
  sms_code: '短信验证码登录',
}

export default function ConnectorsPanel({ appId }: { appId: string }) {
  // 两份数据：全部已注册的登录方式（含字段元数据），以及本应用已保存的配置。
  // 注意 /applications/{id}/connectors 只返回**配置过的**，所以要以
  // /connectors 的清单为准做外连接——否则从没配过的登录方式压根不会显示。
  const schemas = useResource(() => api.get<ConnectorSchema[]>('/connectors'), [])
  const configs = useResource(() => api.get<ConnectorConfig[]>(`/applications/${appId}/connectors`), [appId])

  if (schemas.loading || configs.loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  const err = schemas.error || configs.error
  if (err) return <p className="text-sm text-destructive">{err}</p>
  if (!schemas.data || !configs.data) return null

  const byType = new Map(configs.data.map((c) => [c.type, c]))

  async function save(type: string, enabled: boolean, config: Record<string, unknown>) {
    try {
      await api.put(`/applications/${appId}/connectors/${type}`, { enabled, config })
      toast.success('已保存')
      configs.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-4">
      {schemas.data.map((s) => {
        const current = byType.get(s.type)
        const enabled = current?.enabled ?? false
        return (
          <Card key={s.type}>
            <CardHeader className="flex flex-row items-center gap-3 space-y-0">
              <CardTitle className="text-base">{typeLabels[s.type] ?? s.type}</CardTitle>
              <Badge variant="outline" className="font-mono text-xs">{s.type}</Badge>
              <div className="flex-1" />
              <div className="flex items-center gap-2">
                <Label htmlFor={`enabled-${s.type}`} className="text-sm font-normal">
                  {enabled ? '已启用' : '已关闭'}
                </Label>
                <Switch
                  id={`enabled-${s.type}`}
                  checked={enabled}
                  // 开关只改 enabled，配置原样带回去——SetConnector 是全量覆盖，
                  // 不带的话会把已保存的配置清空。
                  onCheckedChange={(v) => void save(s.type, v, current?.config ?? {})}
                />
              </div>
            </CardHeader>
            <CardContent>
              {s.fields.length === 0 ? (
                <p className="text-sm text-muted-foreground">这种登录方式没有可配置项。</p>
              ) : (
                <DynamicForm
                  // key 里带上配置的引用，保存后重新拉到的数据能重置表单默认值
                  key={JSON.stringify(current?.config ?? {})}
                  fields={s.fields}
                  values={current?.config ?? {}}
                  onSubmit={(config) => save(s.type, enabled, config)}
                />
              )}
            </CardContent>
          </Card>
        )
      })}
    </div>
  )
}
