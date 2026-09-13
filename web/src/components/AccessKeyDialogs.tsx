import { toast } from 'sonner'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/useResource'
import { formatMinute } from '@/lib/format'
import type { AccessKey } from '@/lib/types'

interface Props {
  target: AccessKey | null
  onClose: () => void
  onDone: () => void
}

/** StatusConfirm 停用或启用前的确认。显示最后使用时间，方便判断还有没有人在用。 */
export function StatusConfirm({ target, onClose, onDone }: Props) {
  const disabling = target?.status === 'ACTIVE'
  async function run(k: AccessKey) {
    try {
      await api.patch(`/access-keys/${k.id}/status`, { status: k.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE' })
      toast.success('已生效，正在推送到业务方')
      onDone()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }
  return (
    <ConfirmDialog
      open={target !== null}
      onOpenChange={(v) => !v && onClose()}
      title={disabling ? '停用访问密钥' : '启用访问密钥'}
      description={
        target
          ? `「${target.remark}」最后使用：${formatMinute(target.lastUsedAt, '从未使用')}。` +
            (disabling ? '停用后，使用这把 key 的请求会立即被拒绝。' : '启用后立即可以调用。')
          : ''
      }
      confirmLabel={disabling ? '确认停用' : '确认启用'}
      onConfirm={() => {
        const k = target
        onClose()
        if (k) void run(k)
      }}
    />
  )
}

/** DeleteConfirm 删除前的确认。删除是真删，不可恢复。 */
export function DeleteConfirm({ target, onClose, onDone }: Props) {
  async function run(k: AccessKey) {
    try {
      await api.del(`/access-keys/${k.id}`)
      toast.success('已删除')
      onDone()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }
  return (
    <ConfirmDialog
      open={target !== null}
      onOpenChange={(v) => !v && onClose()}
      title="删除访问密钥"
      description={
        target
          ? `「${target.remark}」最后使用：${formatMinute(target.lastUsedAt, '从未使用')}。` +
            '删除后，使用这把 key 的请求立即失败，且不可恢复。'
          : ''
      }
      confirmLabel="确认删除"
      onConfirm={() => {
        const k = target
        onClose()
        if (k) void run(k)
      }}
    />
  )
}
