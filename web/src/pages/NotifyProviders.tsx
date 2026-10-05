import { useState } from 'react'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { DynamicForm } from '@/components/DynamicForm'
import { NativeSelect } from '@/components/NativeSelect'
import NotifyTabs from '@/components/NotifyTabs'
import { api } from '@/lib/api'
import { notifyChannelLabels } from '@/lib/notify'
import { errorMessage, useResource } from '@/lib/useResource'
import type { NotifyProvider, NotifyProviderType } from '@/lib/types'

function CreateProviderDialog({
  open,
  onOpenChange,
  types,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  types: NotifyProviderType[]
  onCreated: () => void
}) {
  const [typ, setTyp] = useState('')
  const [description, setDescription] = useState('')
  const spec = types.find((t) => t.type === typ)

  async function create(config: Record<string, unknown>) {
    try {
      await api.post('/notify/providers', { type: typ, description, enabled: true, config })
      toast.success('已创建')
      setTyp('')
      setDescription('')
      onOpenChange(false)
      onCreated()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建供应商</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="np-type">类型</Label>
            <NativeSelect id="np-type" value={typ} onChange={(e) => setTyp(e.target.value)}>
              <option value="">请选择</option>
              {types.map((t) => (
                <option key={t.type} value={t.type}>
                  {t.type}
                  {t.channel ? `（${notifyChannelLabels[t.channel]}）` : ''}
                </option>
              ))}
            </NativeSelect>
          </div>
          <div className="space-y-2">
            <Label htmlFor="np-desc">备注</Label>
            <Input
              id="np-desc"
              value={description}
              placeholder="例如：阿里云-主账号"
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          {spec && <DynamicForm key={spec.type} fields={spec.fields} values={{}} onSubmit={create} submitLabel="创建" />}
        </div>
      </DialogContent>
    </Dialog>
  )
}

export default function NotifyProviders() {
  const providers = useResource(() => api.get<NotifyProvider[]>('/notify/providers'), [])
  const types = useResource(() => api.get<NotifyProviderType[]>('/notify/provider-types'), [])
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<NotifyProvider | null>(null)

  async function toggle(p: NotifyProvider, enabled: boolean) {
    try {
      await api.patch(`/notify/providers/${p.id}`, { enabled })
      providers.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  async function remove(p: NotifyProvider) {
    try {
      await api.del(`/notify/providers/${p.id}`)
      toast.success('已删除')
      providers.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-4">
      <NotifyTabs />
      <div className="flex items-center gap-2">
        <Button onClick={() => setCreating(true)} disabled={!types.data}>
          新建供应商
        </Button>
        <p className="text-sm text-muted-foreground">一份凭据就是一个供应商实例；同一类型可以建多个（比如两个阿里云账号）。</p>
      </div>

      {providers.loading && !providers.data && <p className="text-sm text-muted-foreground">加载中…</p>}
      {providers.error && <p className="text-sm text-destructive">{providers.error}</p>}
      {providers.data && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>备注</TableHead>
              <TableHead>类型</TableHead>
              <TableHead>渠道</TableHead>
              <TableHead>被模板引用</TableHead>
              <TableHead>启用</TableHead>
              <TableHead>操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {providers.data.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-center text-muted-foreground">
                  还没有供应商
                </TableCell>
              </TableRow>
            )}
            {providers.data.map((p) => (
              <TableRow key={p.id}>
                <TableCell>
                  <Link to={`/notify/providers/${p.id}`} className="underline-offset-4 hover:underline">
                    {p.description || '（未命名）'}
                  </Link>
                </TableCell>
                <TableCell className="font-mono text-xs">{p.type}</TableCell>
                <TableCell>{p.channel ? notifyChannelLabels[p.channel] : '-'}</TableCell>
                <TableCell>{p.templateCount ?? 0}</TableCell>
                <TableCell>
                  <Switch
                    aria-label={`启用 ${p.description || p.type}`}
                    checked={p.enabled}
                    onCheckedChange={(v) => void toggle(p, v)}
                  />
                </TableCell>
                <TableCell>
                  <Button variant="outline" size="sm" onClick={() => setDeleting(p)}>
                    删除
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <CreateProviderDialog
        open={creating}
        onOpenChange={setCreating}
        types={types.data ?? []}
        onCreated={providers.reload}
      />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="删除供应商"
        description="仍被模板引用的供应商无法删除，需要先在模板里解除关联。"
        confirmLabel="删除"
        onConfirm={() => {
          if (deleting) void remove(deleting)
          setDeleting(null)
        }}
      />
    </div>
  )
}
