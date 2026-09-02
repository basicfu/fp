// 状态文案。放在 lib 而不是某个页面组件里导出，是因为不止一个页面要用——
// 页面之间互相 import 工具函数是坏味道（谁都不该依赖另一个页面的内部实现）。
import type { UserStatus } from './types'

/** 用户状态的中文文案。 */
export const statusLabels: Record<UserStatus, string> = {
  ACTIVE: '正常',
  FROZEN: '已冻结',
  PENDING_DELETE: '注销保护期',
  DELETED: '已注销',
}
