// 状态文案。放在 lib 而不是某个页面组件里导出，是因为不止一个页面要用——
// 页面之间互相 import 工具函数是坏味道（谁都不该依赖另一个页面的内部实现）。
import type { ApplicationStatus, PermissionStatus, UserStatus } from './types'

/** 用户状态的中文文案。 */
export const statusLabels: Record<UserStatus, string> = {
  ACTIVE: '正常',
  FROZEN: '已冻结',
  PENDING_DELETE: '注销保护期',
  DELETED: '已注销',
}

/**
 * 应用状态的中文文案。
 *
 * 【终审】之前 Applications.tsx 与 ApplicationSettings.tsx 各自内联写着
 * `status === 'ACTIVE' ? '启用' : '停用'`，跟用户状态走的
 * `statusLabels[u.status] ?? u.status`（有兜底）是两种不同做法。内联三元
 * 的问题不是重复本身，是它没有兜底：后端将来加第三种应用状态（比如
 * ARCHIVED）时，三元表达式没有第三个分支，会把它错误显示成"停用"，而
 * 用 Record + `?? a.status` 的话，认不出的状态会显示原始值——不好看，
 * 但至少不是一个错误的答案。
 */
export const applicationStatusLabels: Record<ApplicationStatus, string> = {
  ACTIVE: '启用',
  DISABLED: '停用',
}

/**
 * 权限点状态的中文文案。
 *
 * 「过渡中」这个词是刻意的：它不是"已删除"。SDK 上报的是全量快照，但
 * 快照里没有某一条**不等于**它消失了——业务服务多半多实例，滚动发布时
 * 新旧版本同时在跑、交替上报。所以只标"多久没被报过"，删不删由人决定。
 */
export const permissionStatusLabels: Record<PermissionStatus, string> = {
  normal: '正常',
  stale: '过渡中',
  manual: '手动',
}

/** 授权效果的中文文案。空串（没授权）由界面单独处理，不在这里。 */
export const effectLabels: Record<'allow' | 'deny', string> = {
  allow: '允许',
  deny: '拒绝',
}
