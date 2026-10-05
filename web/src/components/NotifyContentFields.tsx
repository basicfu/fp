import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { NativeSelect } from '@/components/NativeSelect'
import { extractVariables, parseLines } from '@/lib/notify'
import type { NotifyChannel, NotifyContent, NotifyMode } from '@/lib/types'

interface Props {
  channel: NotifyChannel
  mode: NotifyMode
  value: NotifyContent
  onChange: (v: NotifyContent) => void
}

/**
 * LinesField 是"每行一项"的多行输入。文本放在本地 state 里：如果每次都拿解析后的数组
 * 回写 value，用户刚敲下的换行（解析时会被当成空行丢掉）就消失了，根本没法输入第二行。
 */
function LinesField({
  id,
  label,
  value,
  onChange,
}: {
  id: string
  label: string
  value: string[]
  onChange: (v: string[]) => void
}) {
  const [text, setText] = useState(value.join('\n'))
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      <textarea
        id={id}
        rows={3}
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          onChange(parseLines(e.target.value))
        }}
        className="w-full rounded-md border bg-transparent px-3 py-2 font-mono text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
      />
    </div>
  )
}

function contentLabel(channel: NotifyChannel, mode: NotifyMode, method?: string): string {
  if (mode === 'vendor') return '供应商模板原文（仅供核对，不参与渲染）'
  if (channel === 'webhook') return method === 'GET' ? 'Query 模板（如 title={title}&body={body}）' : 'Body 模板'
  if (channel === 'email') return '正文模板'
  return '消息模板'
}

/**
 * NotifyContentFields 按渠道与模式渲染模板内容的编辑表单，各渠道用到的字段与后端
 * notify.ValidateContent 一一对应。
 *
 * custom 模式的变量由正文（与邮件主题）里的 {name} 自动推出，不让人手填——后端要求两者
 * 恰好一致，手填只会制造不一致；vendor 模式的正文是供应商那边的原文，写法由供应商决定，
 * 变量要人按供应商模板填，这里只提供"从原文提取"来预填。
 */
export default function NotifyContentFields({ channel, mode, value, onChange }: Props) {
  const custom = mode === 'custom'
  const isEmailCustom = channel === 'email' && custom
  const isWebhook = channel === 'webhook'

  function patch(p: Partial<NotifyContent>) {
    const next = { ...value, ...p }
    if (custom && ('content' in p || 'subject' in p)) {
      next.variables = extractVariables(next.subject ?? '', next.content)
    }
    onChange(next)
  }

  return (
    <div className="space-y-4">
      {isEmailCustom && (
        <>
          <div className="space-y-2">
            <Label htmlFor="nc-subject">邮件主题</Label>
            <Input id="nc-subject" value={value.subject ?? ''} onChange={(e) => patch({ subject: e.target.value })} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="nc-ctype">正文类型</Label>
            <NativeSelect
              id="nc-ctype"
              value={value.contentType || 'text/plain'}
              onChange={(e) => patch({ contentType: e.target.value })}
            >
              <option value="text/plain">纯文本</option>
              <option value="text/html">HTML</option>
            </NativeSelect>
          </div>
        </>
      )}

      {isWebhook && (
        <>
          <div className="space-y-2">
            <Label htmlFor="nc-method">请求方式</Label>
            <NativeSelect
              id="nc-method"
              value={value.method || 'POST'}
              onChange={(e) => {
                const method = e.target.value
                patch({ method, contentType: method === 'GET' ? undefined : value.contentType })
              }}
            >
              <option value="POST">POST</option>
              <option value="GET">GET</option>
            </NativeSelect>
          </div>
          {(value.method || 'POST') === 'POST' && (
            <div className="space-y-2">
              <Label htmlFor="nc-wctype">Content-Type</Label>
              <NativeSelect
                id="nc-wctype"
                value={value.contentType || 'application/json'}
                onChange={(e) => patch({ contentType: e.target.value })}
              >
                <option value="application/json">application/json</option>
                <option value="text/plain">text/plain</option>
              </NativeSelect>
            </div>
          )}
        </>
      )}

      <div className="space-y-2">
        <Label htmlFor="nc-content">{contentLabel(channel, mode, value.method)}</Label>
        <textarea
          id="nc-content"
          rows={6}
          value={value.content}
          onChange={(e) => patch({ content: e.target.value })}
          className="w-full rounded-md border bg-transparent px-3 py-2 font-mono text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
        />
        {custom && <p className="text-xs text-muted-foreground">变量写成 {'{name}'}，发送时按调用方传的 params 替换。</p>}
      </div>

      <div className="space-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor="nc-vars">变量</Label>
          {!custom && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => onChange({ ...value, variables: extractVariables(value.content) })}
            >
              从原文提取
            </Button>
          )}
        </div>
        <Input
          id="nc-vars"
          value={value.variables.join(', ')}
          readOnly={custom}
          placeholder={custom ? '由内容自动推出' : '逗号分隔，如 code, minutes'}
          onChange={(e) =>
            onChange({
              ...value,
              variables: e.target.value
                .split(',')
                .map((s) => s.trim())
                .filter(Boolean),
            })
          }
        />
        <p className="text-xs text-muted-foreground">调用方传的 params 必须与这里的变量完全一致，多了少了都会被拒绝。</p>
      </div>

      {channel === 'wecom_bot' && (
        <>
          <LinesField
            id="nc-mlist"
            label="@ 成员（userid，每行一个，@all 表示所有人）"
            value={value.mentionedList ?? []}
            onChange={(v) => patch({ mentionedList: v })}
          />
          <LinesField
            id="nc-mmobile"
            label="@ 手机号（每行一个）"
            value={value.mentionedMobileList ?? []}
            onChange={(v) => patch({ mentionedMobileList: v })}
          />
        </>
      )}

      {channel === 'dingtalk_bot' && (
        <>
          <LinesField
            id="nc-atm"
            label="@ 手机号（每行一个）"
            value={value.atMobiles ?? []}
            onChange={(v) => patch({ atMobiles: v })}
          />
          <div className="flex items-center gap-3">
            <Switch id="nc-atall" checked={Boolean(value.isAtAll)} onCheckedChange={(v) => patch({ isAtAll: v })} />
            <Label htmlFor="nc-atall">@ 所有人</Label>
          </div>
        </>
      )}
    </div>
  )
}
