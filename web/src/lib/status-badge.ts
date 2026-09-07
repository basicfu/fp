import type { ApplicationStatus, UserStatus } from './types'

// 用 Tailwind 内置色板而不是主题变量：这是和主色调无关的"正常/异常"
// 语义状态色，四套主题预设切换时不应该跟着变。
const GREEN = 'border-transparent bg-green-100 text-green-800 dark:bg-green-500/15 dark:text-green-400'
const RED = 'border-transparent bg-red-100 text-red-800 dark:bg-red-500/15 dark:text-red-400'
const AMBER = 'border-transparent bg-amber-100 text-amber-800 dark:bg-amber-500/15 dark:text-amber-400'
export const GRAY = 'border-transparent bg-muted text-muted-foreground'

export const applicationStatusBadgeClassName: Record<ApplicationStatus, string> = {
  ACTIVE: GREEN,
  DISABLED: GRAY,
}

export const userStatusBadgeClassName: Record<UserStatus, string> = {
  ACTIVE: GREEN,
  FROZEN: RED,
  PENDING_DELETE: AMBER,
  DELETED: GRAY,
}
