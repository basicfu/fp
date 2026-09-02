import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

interface Props {
  open: boolean
  onOpenChange: (v: boolean) => void
  title: string
  /** 说清楚这个操作的后果，不要只写"确定要 XX 吗？"。 */
  description: string
  confirmLabel: string
  cancelLabel?: string
  onConfirm: () => void
}

/**
 * ConfirmDialog 是破坏性操作（停用应用、冻结账号、批量下线设备……）的
 * 二次确认弹窗。
 *
 * 与 Applications.tsx 的 SecretDialog 刻意相反：那个弹窗里"关闭"本身就是
 * 事故（appSecret 只显示一次，关掉就永久丢了），所以要在源头拦掉
 * Escape/点遮罩/默认关闭按钮。这里恰恰相反——"关闭"（不管是点取消、按
 * Escape 还是点遮罩）什么都不会发生，是最安全的默认动作；真正有风险的
 * 只有点确认按钮那一下。所以这里完全不拦截 onOpenChange，交给 base-ui
 * 的默认行为，任何方式都能让管理员安全地反悔。
 *
 * 确认按钮固定用 destructive 变体：调用方（四个破坏性操作）没有一个是
 * "安全"的，不需要再加一个 variant prop 让调用方选错。
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  cancelLabel = '取消',
  onConfirm,
}: Props) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {cancelLabel}
          </Button>
          <Button variant="destructive" onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
