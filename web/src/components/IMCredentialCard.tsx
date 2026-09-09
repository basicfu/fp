import { useState } from 'react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { errorMessage, useResource } from '@/lib/useResource'
import type { IMCredentialStatus } from '@/lib/types'

/**
 * IMCredentialCard 管理 fp-im 网关连 fp 用的那一份凭据。
 *
 * 全库只有一份——fp-im 是一个服务而不是一群应用，多实例 fp-im 共用同一份。
 * 所以这张卡片不挂在某个应用下面。
 */
export default function IMCredentialCard() {
  const status = useResource(() => api.get<IMCredentialStatus>('/im-credential'), [])
  const [confirming, setConfirming] = useState(false)
  const [secret, setSecret] = useState<string | null>(null)

  async function rotate() {
    try {
      const res = await api.post<{ secret: string }>('/im-credential/rotate')
      setSecret(res.secret)
      status.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>IM 网关凭据</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          fp-im 用这份凭据连 fp，填在它的 <code>config-im.yaml</code> 的{' '}
          <code>fpsdk.secret</code>。全部 fp-im 实例共用同一份。
        </p>
        <p className="text-sm">
          当前状态：
          {status.loading ? '加载中…' : status.data?.exists ? ' 已生成' : ' 尚未生成'}
        </p>
        <Button variant={status.data?.exists ? 'destructive' : 'default'} onClick={() => setConfirming(true)}>
          {status.data?.exists ? '重新生成' : '生成'}
        </Button>

        <ConfirmDialog
          open={confirming}
          onOpenChange={setConfirming}
          title="重新生成 IM 网关凭据？"
          description={
            '旧凭据会立刻在库里失效，但 fp 的凭据缓存还会让它最多再活 10 秒。' +
            '生成后必须把新值填进所有 fp-im 实例的 config-im.yaml 并重启，' +
            '否则它们会全部连不上 fp。'
          }
          confirmLabel="重新生成"
          onConfirm={() => {
            setConfirming(false)
            void rotate()
          }}
        />

        {/* 明文只在这一次可见：库里只存 bcrypt 哈希，关掉这个对话框就再也读不回来。 */}
        <Dialog open={secret !== null} onOpenChange={(o) => !o && setSecret(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>新的 IM 网关凭据</DialogTitle>
              <DialogDescription>
                <strong>只显示这一次。</strong>关掉之后无法再读回——库里只存哈希。
                现在就把它填进所有 fp-im 实例的 config-im.yaml。
              </DialogDescription>
            </DialogHeader>
            <code className="block break-all rounded-md border bg-muted p-3 text-sm">{secret}</code>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  )
}
