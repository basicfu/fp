// 格式化工具。放在 lib 而不是某个页面组件里导出，是因为多个页面都要用——
// 页面之间互相 import 工具函数是坏味道（谁都不该依赖另一个页面的内部实现）。

/** formatTime 把后端的毫秒时间戳转成本地时间字符串。 */
export function formatTime(ms: number): string {
  if (!ms) return '-'
  return new Date(ms).toLocaleString()
}

/**
 * formatDuration 把毫秒转成"3 天 4 小时"这样的粗粒度中文时长。
 *
 * 权限点列表用它显示"已过渡多久"——这是人决定要不要手动删掉那条的唯一
 * 依据（设计上没有可逆的"停用"中间态，删除就是真删）。所以宁可粗，不能
 * 让人误判：只显示最大的两级单位，不显示秒。
 */
export function formatDuration(ms: number): string {
  if (ms <= 0) return '-'
  const min = Math.floor(ms / 60000)
  if (min < 1) return '不到 1 分钟'
  const units: [number, string][] = [
    [60 * 24, '天'],
    [60, '小时'],
    [1, '分钟'],
  ]
  const parts: string[] = []
  let rest = min
  for (const [size, label] of units) {
    const n = Math.floor(rest / size)
    if (n > 0) {
      parts.push(`${n} ${label}`)
      rest -= n * size
    }
    if (parts.length === 2) break
  }
  return parts.join(' ')
}
