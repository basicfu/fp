import type { FieldErrors, FieldValues } from 'react-hook-form'
import { toast } from 'sonner'

/**
 * toastFormErrors 把 react-hook-form 的字段级校验错误合并成一条 toast，
 * 传给 handleSubmit 的第二个参数（onInvalid）。取代原来每个字段各自的
 * 内联错误文案——内联文案在字段合法/不合法之间来回增删一段 <p>，会把
 * 下面的控件顶得一跳一跳的；toast 悬浮展示，不占布局、不改内容高度。
 */
export function toastFormErrors<T extends FieldValues>(errors: FieldErrors<T>): void {
  const messages = Object.values(errors)
    .map((e) => (e && typeof e === 'object' && 'message' in e ? String(e.message ?? '') : ''))
    .filter(Boolean)
  if (messages.length > 0) toast.error(messages.join('；'))
}
