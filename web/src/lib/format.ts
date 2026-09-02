// 格式化工具。放在 lib 而不是某个页面组件里导出，是因为多个页面都要用——
// 页面之间互相 import 工具函数是坏味道（谁都不该依赖另一个页面的内部实现）。

/** formatTime 把后端的毫秒时间戳转成本地时间字符串。 */
export function formatTime(ms: number): string {
  if (!ms) return '-'
  return new Date(ms).toLocaleString()
}
