import { useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { formatTime } from '@/lib/format'
import { useResource, errorMessage } from '@/lib/useResource'
import type { ConfigSnapshot, ConfigVersion, SaveConfigResponse } from '@/lib/types'

export default function SystemConfigVersions() {
  const navigate = useNavigate()
  const versions = useResource(() => api.get<ConfigVersion[]>('/system-config/versions'), [])

  const [viewing, setViewing] = useState<number | null>(null)
  const [viewingValue, setViewingValue] = useState('')
  const [viewingLoading, setViewingLoading] = useState(false)

  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)

  async function openView(seq: number) {
    setViewing(seq)
    setViewingLoading(true)
    try {
      const snap = await api.get<ConfigSnapshot>(`/system-config/versions/${seq}`)
      setViewingValue(snap.value)
    } catch (e) {
      toast.error(errorMessage(e))
      setViewing(null)
    } finally {
      setViewingLoading(false)
    }
  }

  async function confirmRollback() {
    if (rollbackTarget === null) return
    const latestSeqBefore = versions.data?.[0]?.seq
    try {
      const res = await api.post<SaveConfigResponse>('/system-config/rollback', { seq: rollbackTarget })
      toast.success(
        res.seq === latestSeqBefore
          ? '这一版与当前内容完全一致，没有产生新版本'
          : `已回滚，当前版本 seq=${res.seq}，重启 fp 后生效`,
      )
      navigate('/system-config')
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  if (versions.error) return <p className="text-sm text-destructive">{versions.error}</p>
  if (versions.loading && !versions.data) return <p className="text-sm text-muted-foreground">加载中…</p>

  return (
    <div className="space-y-6">
      <h1 className="text-lg font-medium">系统配置版本历史</h1>

      <div className="space-y-3">
        {versions.data?.length === 0 && <p className="text-sm text-muted-foreground">还没有任何版本。</p>}
        {versions.data?.map((v, idx) => {
          const isCurrent = idx === 0
          return (
            <div
              key={v.seq}
              data-testid={`version-${v.seq}`}
              className="flex flex-wrap items-center justify-between gap-3 rounded-md border p-3"
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-sm font-medium">v{v.seq}</span>
                <span className="text-xs text-muted-foreground">{formatTime(v.createdAt)}</span>
                {isCurrent && <Badge variant="secondary">当前版本</Badge>}
              </div>
              <div className="flex items-center gap-2">
                <Button variant="outline" size="sm" onClick={() => void openView(v.seq)}>
                  查看
                </Button>
                {!isCurrent && (
                  <Button variant="outline" size="sm" onClick={() => setRollbackTarget(v.seq)}>
                    回滚到 v{v.seq}
                  </Button>
                )}
              </div>
            </div>
          )
        })}
      </div>

      <Dialog open={viewing !== null} onOpenChange={(v) => !v && setViewing(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>v{viewing} 的内容</DialogTitle>
          </DialogHeader>
          {viewingLoading ? (
            <p className="text-sm text-muted-foreground">加载中…</p>
          ) : (
            <pre className="max-h-[50vh] overflow-auto rounded-md bg-muted/40 p-3 font-mono text-xs leading-relaxed">
              {viewingValue}
            </pre>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={rollbackTarget !== null}
        onOpenChange={(v) => !v && setRollbackTarget(null)}
        title={`回滚到 v${rollbackTarget}`}
        description="回滚只落库，需要重启 fp 才会生效。"
        confirmLabel="确认回滚"
        onConfirm={() => void confirmRollback()}
      />
    </div>
  )
}
