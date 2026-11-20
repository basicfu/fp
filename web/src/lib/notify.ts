import type { NotifyChannel, NotifyContent, NotifyMode } from './types'

export const notifyChannelLabels: Record<NotifyChannel, string> = {
  sms: '短信',
  email: '邮件',
  telegram: 'Telegram',
  wecom_bot: '企业微信机器人',
  dingtalk_bot: '钉钉机器人',
  webhook: '自定义 Webhook',
  wechat_mp: '微信服务号',
}

export const notifyModeLabels: Record<NotifyMode, string> = {
  vendor: '供应商模板',
  custom: '自定义内容',
}

export const notifyChannels = Object.keys(notifyChannelLabels) as NotifyChannel[]

/**
 * 各渠道允许的模板模式，与后端 domain.NotifyChannel.AllowsMode 的集合一致。
 * 新建模板默认取第一个：邮件放 custom 在前，因为内置的邮件类型只有 smtp，它只能发自定义模板。
 */
export const allowedModes: Record<NotifyChannel, NotifyMode[]> = {
  sms: ['vendor'],
  email: ['custom', 'vendor'],
  telegram: ['custom'],
  wecom_bot: ['custom'],
  dingtalk_bot: ['custom'],
  webhook: ['custom'],
  wechat_mp: ['vendor'],
}

/** sms / email / 服务号发给某个具体的人；IM 与 webhook 发给配置好的固定目标。 */
export function needsRecipient(channel: NotifyChannel): boolean {
  return channel === 'sms' || channel === 'email' || channel === 'wechat_mp'
}

/** recipientLabel 是测试发送时收件人输入框的标签。服务号的收件人是 fp 用户 ID，fp 换成他绑定的 openid。 */
export function recipientLabel(channel: NotifyChannel): string {
  if (channel === 'sms') return '手机号'
  if (channel === 'wechat_mp') return '用户 ID（该用户要已开启微信通知）'
  return '邮箱'
}

/**
 * extractVariables 取出文本里的 {name} 占位符（去重、保持首次出现的顺序）。
 *
 * 与后端 notify.ExtractPlaceholders 同一个形状：name 是标识符。`${code}`、`{{code}}`、
 * `{code}` 三种写法里的 code 都能取到，所以供应商模板原文也能用它预填变量。
 * 服务号模板原文写成 `{{thing1.DATA}}`，取出来的是 thing1。
 */
export function extractVariables(...texts: string[]): string[] {
  const out: string[] = []
  for (const m of texts.join('\n').matchAll(/\{([A-Za-z_][A-Za-z0-9_]*)(?:\.DATA)?\}/g)) {
    if (!out.includes(m[1])) out.push(m[1])
  }
  return out
}

export function emptyContent(): NotifyContent {
  return { content: '', variables: [] }
}

/** parseLines 把多行文本拆成列表，空行与首尾空白忽略。 */
export function parseLines(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
}
