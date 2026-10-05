import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { toast } from 'sonner'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { DynamicForm } from '@/components/DynamicForm'
import { api } from '@/lib/api'
import { notifyChannelLabels } from '@/lib/notify'
import { errorMessage, useResource } from '@/lib/useResource'
import type { NotifyProvider, NotifyProviderType, NotifyProviderUsage } from '@/lib/types'

/**
 * 供应商详情：改备注与配置，并反向列出被哪些模板引用——停用或删除一个供应商之前，
 * 先看看影响范围。secret 字段回显的是掩码，原样提交表示保持原值。
 */
export default function NotifyProviderDetail() {
  const { id = '' } = useParams()
  const provider = useResource(() => api.get<NotifyProvider>(`/notify/providers/${id}`), [id])
  const types = useResource(() => api.get<NotifyProviderType[]>('/notify/provider-types'), [])
  const usages = useResource(() => api.get<NotifyProviderUsage[]>(`/notify/providers/${id}/templates`), [id])
  const [description, setDescription] = useState<string | null>(null)

  if (provider.error) return <p className="text-sm text-destructive">{provider.error}</p>
  if (!provider.data || !types.data) return <p className="text-sm text-muted-foreground">加载中…</p>

  const p = provider.data
  const spec = types.data.find((t) => t.type === p.type)

  async function save(config: Record<string, unknown>) {
    try {
      await api.patch(`/notify/providers/${id}`, { description: description ?? p.description, config })
      toast.success('已保存')
      provider.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="max-w-2xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold">{p.description || '（未命名）'}</h1>
        <p className="text-sm text-muted-foreground">
          类型 {p.type}
          {p.channel ? `，渠道 ${notifyChannelLabels[p.channel]}` : ''}
        </p>
      </div>

      <div className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="npd-desc">备注</Label>
          <Input id="npd-desc" value={description ?? p.description} onChange={(e) => setDescription(e.target.value)} />
        </div>
        {spec ? (
          <DynamicForm key={p.updatedAt} fields={spec.fields} values={p.config} onSubmit={save} submitLabel="保存" />
        ) : (
          <p className="text-sm text-destructive">这个供应商的类型 {p.type} 已不受支持，只能删除。</p>
        )}
      </div>

      <div className="space-y-2">
        <h2 className="text-base font-medium">被这些模板引用</h2>
        {usages.data && usages.data.length === 0 && <p className="text-sm text-muted-foreground">还没有模板引用它。</p>}
        {usages.data && usages.data.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>模板</TableHead>
                <TableHead>供应商侧模板 ID</TableHead>
                <TableHead>优先级</TableHead>
                <TableHead>关联</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {usages.data.map((u) => (
                <TableRow key={u.code}>
                  <TableCell>
                    <Link to={`/notify/templates/${encodeURIComponent(u.code)}`} className="underline-offset-4 hover:underline">
                      {u.code}
                    </Link>
                  </TableCell>
                  <TableCell className="font-mono text-xs">{u.providerTemplateId || '-'}</TableCell>
                  <TableCell>{u.priority}</TableCell>
                  <TableCell>{u.enabled ? '启用' : '已禁用'}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  )
}
