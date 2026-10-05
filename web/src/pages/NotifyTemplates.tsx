import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { NativeSelect } from '@/components/NativeSelect'
import NotifyContentFields from '@/components/NotifyContentFields'
import NotifyTabs from '@/components/NotifyTabs'
import { api } from '@/lib/api'
import { formatTime } from '@/lib/format'
import {
  allowedModes,
  emptyContent,
  notifyChannelLabels,
  notifyChannels,
  notifyModeLabels,
} from '@/lib/notify'
import { errorMessage, useResource } from '@/lib/useResource'
import type { NotifyChannel, NotifyContent, NotifyMode, NotifyTemplateRow } from '@/lib/types'

function CreateTemplateDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const navigate = useNavigate()
  const [channel, setChannel] = useState<NotifyChannel>('sms')
  const [mode, setMode] = useState<NotifyMode>('vendor')
  const [code, setCode] = useState('')
  const [description, setDescription] = useState('')
  const [content, setContent] = useState<NotifyContent>(emptyContent())
  const [saving, setSaving] = useState(false)

  function changeChannel(c: NotifyChannel) {
    setChannel(c)
    setMode(allowedModes[c][0])
    setContent(emptyContent())
  }

  function changeMode(m: NotifyMode) {
    setMode(m)
    setContent(emptyContent())
  }

  async function create() {
    setSaving(true)
    try {
      await api.post('/notify/templates', { code, channel, mode, description, enabled: true, content })
      toast.success('已创建，接着给它关联供应商')
      onOpenChange(false)
      navigate(`/notify/templates/${encodeURIComponent(code)}`)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>新建通知模板</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="nt-channel">渠道</Label>
            <NativeSelect id="nt-channel" value={channel} onChange={(e) => changeChannel(e.target.value as NotifyChannel)}>
              {notifyChannels.map((c) => (
                <option key={c} value={c}>
                  {notifyChannelLabels[c]}
                </option>
              ))}
            </NativeSelect>
          </div>
          <div className="space-y-2">
            <Label htmlFor="nt-code">code</Label>
            <Input id="nt-code" value={code} placeholder="业务代码里引用它，创建后不可改" onChange={(e) => setCode(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="nt-mode">模板模式</Label>
            <NativeSelect
              id="nt-mode"
              value={mode}
              disabled={allowedModes[channel].length === 1}
              onChange={(e) => changeMode(e.target.value as NotifyMode)}
            >
              {allowedModes[channel].map((m) => (
                <option key={m} value={m}>
                  {notifyModeLabels[m]}
                </option>
              ))}
            </NativeSelect>
          </div>
          <div className="space-y-2">
            <Label htmlFor="nt-desc">备注</Label>
            <Input id="nt-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
          </div>
          <NotifyContentFields key={`${channel}-${mode}`} channel={channel} mode={mode} value={content} onChange={setContent} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={() => void create()} disabled={saving || !code}>
            创建
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export default function NotifyTemplates() {
  const rows = useResource(() => api.get<NotifyTemplateRow[]>('/notify/templates'), [])
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<NotifyTemplateRow | null>(null)

  async function remove(t: NotifyTemplateRow) {
    try {
      await api.del(`/notify/templates/${encodeURIComponent(t.code)}`)
      toast.success('已删除')
      rows.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-4">
      <NotifyTabs />
      <div className="flex items-center gap-2">
        <Button onClick={() => setCreating(true)}>新建模板</Button>
        <p className="text-sm text-muted-foreground">业务代码只传 code、收件人和变量；渠道、供应商、内容都在这里配置。</p>
      </div>

      {rows.loading && !rows.data && <p className="text-sm text-muted-foreground">加载中…</p>}
      {rows.error && <p className="text-sm text-destructive">{rows.error}</p>}
      {rows.data && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>code</TableHead>
              <TableHead>渠道</TableHead>
              <TableHead>模式</TableHead>
              <TableHead>备注</TableHead>
              <TableHead>供应商</TableHead>
              <TableHead>状态</TableHead>
              <TableHead>更新时间</TableHead>
              <TableHead>操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.data.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} className="text-center text-muted-foreground">
                  还没有模板
                </TableCell>
              </TableRow>
            )}
            {rows.data.map((t) => (
              <TableRow key={t.code}>
                <TableCell className="font-mono text-xs">
                  <Link to={`/notify/templates/${encodeURIComponent(t.code)}`} className="underline-offset-4 hover:underline">
                    {t.code}
                  </Link>
                </TableCell>
                <TableCell>{notifyChannelLabels[t.channel]}</TableCell>
                <TableCell>{notifyModeLabels[t.mode]}</TableCell>
                <TableCell>{t.description}</TableCell>
                <TableCell>{t.providerCount === 0 ? <span className="text-destructive">未关联</span> : t.providerCount}</TableCell>
                <TableCell>
                  <Badge variant={t.enabled ? 'default' : 'secondary'}>{t.enabled ? '启用' : '停用'}</Badge>
                </TableCell>
                <TableCell className="text-muted-foreground">{formatTime(t.updatedAt)}</TableCell>
                <TableCell>
                  <Button variant="outline" size="sm" onClick={() => setDeleting(t)}>
                    删除
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <CreateTemplateDialog open={creating} onOpenChange={setCreating} />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="删除模板"
        description="删除后，业务方再用这个 code 调用会得到「模板不存在」；它的供应商关联一并删除，发送记录保留。"
        confirmLabel="删除"
        onConfirm={() => {
          if (deleting) void remove(deleting)
          setDeleting(null)
        }}
      />
    </div>
  )
}
