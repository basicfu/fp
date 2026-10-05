import type { NotifyChannel, NotifyContent, NotifyMode } from './types'

export const notifyChannelLabels: Record<NotifyChannel, string> = {
  sms: '短信',
  email: '邮件',
  telegram: 'Telegram',
  wecom_bot: '企业微信机器人',
  dingtalk_bot: '钉钉机器人',
  webhook: '自定义 Webhook',
}

export const notifyModeLabels: Record<NotifyMode, string> = {
  vendor: '供应商模板',
  custom: '自定义内容',
}

export const notifyChannels = Object.keys(notifyChannelLabels) as NotifyChannel[]

/** 各渠道允许的模板模式，与后端 domain.NotifyChannel.AllowsMode 一致。 */
export const allowedModes: Record<NotifyChannel, NotifyMode[]> = {
  sms: ['vendor'],
  email: ['vendor', 'custom'],
  telegram: ['custom'],
  wecom_bot: ['custom'],
  dingtalk_bot: ['custom'],
  webhook: ['custom'],
}

/** sms / email 发给某个具体的人；IM 与 webhook 发给配置好的固定目标。 */
export function needsRecipient(channel: NotifyChannel): boolean {
  return channel === 'sms' || channel === 'email'
}

/**
 * extractVariables 取出文本里的 {name} 占位符（去重、保持首次出现的顺序）。
 *
 * 与后端 notify.ExtractPlaceholders 同一个形状：name 是标识符。`${code}`、`{{code}}`、
 * `{code}` 三种写法里的 code 都能取到，所以供应商模板原文也能用它预填变量。
 */
export function extractVariables(...texts: string[]): string[] {
  const out: string[] = []
  for (const m of texts.join('\n').matchAll(/\{([A-Za-z_][A-Za-z0-9_]*)\}/g)) {
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
