import { useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { NativeSelect } from '@/components/NativeSelect'
import NotifyContentFields from '@/components/NotifyContentFields'
import { ApiError, api } from '@/lib/api'
import { needsRecipient, notifyChannelLabels, notifyModeLabels } from '@/lib/notify'
import { errorMessage, useResource } from '@/lib/useResource'
import type { NotifyContent, NotifyLink, NotifyProvider, NotifyTemplateDetail as Detail } from '@/lib/types'

/** 一条关联：供应商实例 + 它在这个模板下的供应商侧模板 ID、优先级、启停。 */
function LinkRow({ code, vendor, link, onChanged }: { code: string; vendor: boolean; link: NotifyLink; onChanged: () => void }) {
  const [providerTemplateId, setProviderTemplateId] = useState(link.providerTemplateId)
  const [priority, setPriority] = useState(String(link.priority))
  const [enabled, setEnabled] = useState(link.enabled)
  // 请求在途时整行不能再操作：双击「移除」的第二个 DELETE 会 404，用户先看到成功又看到失败。
  const [busy, setBusy] = useState(false)
  // 服务端已确认的启用状态。link 是重新拉取之后才更新的 prop，一次保存成功到重新拉取返回之间它还是旧值，
  // 回滚用它的话会把开关退到一个服务端已经不是的状态。
  const confirmed = useRef(link.enabled)
  useEffect(() => {
    confirmed.current = link.enabled
  }, [link.enabled])
  const path = `/notify/templates/${encodeURIComponent(code)}/providers/${link.providerId}`
  const name = link.providerDescription || link.providerType

  async function save(nextEnabled = enabled) {
    setBusy(true)
    try {
      await api.put(path, { providerTemplateId, enabled: nextEnabled, priority: Number(priority) || 0 })
      confirmed.current = nextEnabled
      toast.success('已保存')
      onChanged()
    } catch (e) {
      // 开关是先翻后存的；失败时服务端仍是上一次确认的值，不退回去界面就和实际生效的状态对不上。
      setEnabled(confirmed.current)
      toast.error(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    setBusy(true)
    try {
      await api.del(path)
      toast.success('已解除关联')
      onChanged()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <TableRow>
      <TableCell>
        {name}
        {!link.providerEnabled && <Badge variant="secondary" className="ml-2">供应商已停用</Badge>}
      </TableCell>
      <TableCell>
        {vendor ? (
          <Input
            aria-label={`${name} 的供应商侧模板 ID`}
            className="font-mono text-xs"
            value={providerTemplateId}
            onChange={(e) => setProviderTemplateId(e.target.value)}
          />
        ) : (
          '-'
        )}
      </TableCell>
      <TableCell>
        <Input
          aria-label={`${name} 的优先级`}
          title="数字越大越先试，默认 0"
          type="number"
          className="w-24"
          value={priority}
          onChange={(e) => setPriority(e.target.value)}
        />
      </TableCell>
      <TableCell>
        <Switch
          aria-label={`启用 ${name}`}
          checked={enabled}
          disabled={busy}
          onCheckedChange={(v) => {
            setEnabled(v)
            void save(v)
          }}
        />
      </TableCell>
      <TableCell className="space-x-2">
        <Button variant="outline" size="sm" disabled={busy} onClick={() => void save()}>
          保存
        </Button>
        <Button variant="outline" size="sm" disabled={busy} onClick={() => void remove()}>
          移除
        </Button>
      </TableCell>
    </TableRow>
  )
}

function AddLink({ code, detail, onAdded }: { code: string; detail: Detail; onAdded: () => void }) {
  const providers = useResource(() => api.get<NotifyProvider[]>('/notify/providers'), [])
  const [providerId, setProviderId] = useState('')
  const [providerTemplateId, setProviderTemplateId] = useState('')
  const vendor = detail.mode === 'vendor'
  const linked = new Set(detail.providers.map((l) => l.providerId))
  const candidates = (providers.data ?? []).filter((p) => p.channel === detail.channel && !linked.has(p.id))
  // 加载中、加载失败、确实没有可选的是三件事：失败时说"没有可关联"会误导人去重复新建。
  const emptyLabel = providers.error
    ? `加载供应商失败：${providers.error}`
    : !providers.data
      ? '加载中…'
      : candidates.length === 0
        ? '没有可关联的同渠道供应商'
        : '请选择'

  async function add() {
    try {
      await api.put(`/notify/templates/${encodeURIComponent(code)}/providers/${providerId}`, {
        providerTemplateId,
        enabled: true,
        priority: 0,
      })
      toast.success('已关联')
      setProviderId('')
      setProviderTemplateId('')
      onAdded()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="flex flex-wrap items-end gap-3">
      <div className="space-y-2">
        <Label htmlFor="add-provider">添加供应商</Label>
        <NativeSelect id="add-provider" className="w-64" value={providerId} onChange={(e) => setProviderId(e.target.value)}>
          <option value="">{emptyLabel}</option>
          {candidates.map((p) => (
            <option key={p.id} value={p.id}>
              {p.description || p.type}（{p.type}）
            </option>
          ))}
        </NativeSelect>
      </div>
      {vendor && (
        <div className="space-y-2">
          <Label htmlFor="add-ptid">供应商侧模板 ID</Label>
          <Input id="add-ptid" className="w-56 font-mono text-xs" value={providerTemplateId} onChange={(e) => setProviderTemplateId(e.target.value)} />
        </div>
      )}
      <Button onClick={() => void add()} disabled={!providerId || (vendor && !providerTemplateId.trim())}>
        关联
      </Button>
    </div>
  )
}

function TestSendDialog({ detail, open, onOpenChange }: { detail: Detail; open: boolean; onOpenChange: (v: boolean) => void }) {
  const [to, setTo] = useState('')
  const [params, setParams] = useState<Record<string, string>>({})
  const [sending, setSending] = useState(false)
  const recipient = needsRecipient(detail.channel)

  async function send() {
    setSending(true)
    try {
      // 后端要求 params 的键集合与模板当前的变量完全一致（值可以是空串）。params 里是敲过的键：
      // 变量改名后会残留旧键，没填的变量又缺席，两种都会被整条拒绝，所以按当前变量表生成。
      const body = Object.fromEntries(detail.content.variables.map((v) => [v, params[v] ?? '']))
      await api.post(`/notify/templates/${encodeURIComponent(detail.code)}/test`, { to, params: body })
      toast.success('已发送，结果见「发送记录」')
      onOpenChange(false)
    } catch (e) {
      // 只有"全部供应商都失败"才有记录可看；其余错误（没有可用供应商、变量不对……）原因就在提示本身。
      const hint = e instanceof ApiError && e.code === 'NOTIFY_SEND_FAILED' ? '（具体原因见「发送记录」）' : ''
      toast.error(errorMessage(e) + hint)
    } finally {
      setSending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>测试发送</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground">走与业务方调用完全相同的发送路径，会真的发出去。</p>
          {recipient && (
            <div className="space-y-2">
              <Label htmlFor="ts-to">{detail.channel === 'sms' ? '手机号' : '邮箱'}</Label>
              <Input id="ts-to" value={to} onChange={(e) => setTo(e.target.value)} />
            </div>
          )}
          {detail.content.variables.map((v) => (
            <div key={v} className="space-y-2">
              <Label htmlFor={`ts-var-${v}`}>{v}</Label>
              <Input id={`ts-var-${v}`} value={params[v] ?? ''} onChange={(e) => setParams({ ...params, [v]: e.target.value })} />
            </div>
          ))}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={() => void send()} disabled={sending}>
            发送
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

interface Form {
  content: NotifyContent
  description: string
  enabled: boolean
}

export default function NotifyTemplateDetail() {
  const { code = '' } = useParams()
  const detail = useResource(() => api.get<Detail>(`/notify/templates/${encodeURIComponent(code)}`), [code])
  // draft 是还没保存的编辑，只在保存成功时清掉。saved 是刚保存成功的内容：服务端数据的 updatedAt
  // 还没追上 upTo（重新拉取没返回）时，由它顶替服务端值显示，否则表单会先闪回保存前的旧内容。
  // upTo 取保存响应里的 updatedAt，不能取点击保存时渲染出的那份：连点两次保存时，第一次的重新拉取可能在第二次
  // 保存在途时才返回，点击时的 updatedAt 就比落地后的旧，第二次保存成功的那一刻会闪回服务端的上一版。
  // 草稿不能绑在 updatedAt 上：重新拉取会带来新的 updatedAt，保存成功后的新编辑会因此被当成过期而丢掉。
  const [draft, setDraft] = useState<Form | null>(null)
  const [saved, setSaved] = useState<{ upTo: number; v: Form } | null>(null)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)

  // 整页错误只在还没有数据时出现；有数据时重新拉取失败，错误显示在页内，页面照常可用。
  if (!detail.data) {
    return detail.error ? (
      <p className="text-sm text-destructive">{detail.error}</p>
    ) : (
      <p className="text-sm text-muted-foreground">加载中…</p>
    )
  }

  const d = detail.data
  const cur =
    draft ?? (saved && d.updatedAt < saved.upTo ? saved.v : { content: d.content, description: d.description, enabled: d.enabled })
  const vendor = d.mode === 'vendor'
  const singleProvider = !needsRecipient(d.channel)

  async function save() {
    if (!draft) return
    const sent = draft
    setSaving(true)
    try {
      const resp = await api.patch<Pick<Detail, 'updatedAt'>>(`/notify/templates/${encodeURIComponent(code)}`, {
        content: sent.content,
        description: sent.description,
        enabled: sent.enabled,
      })
      toast.success('已保存')
      setSaved({ upTo: resp.updatedAt, v: sent })
      // 请求在途时又改过的话，那次新编辑还没存，要留着。
      setDraft((x) => (x === sent ? null : x))
      detail.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="max-w-3xl space-y-8">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-mono text-xl font-semibold break-all">{d.code}</h1>
        <Badge variant="secondary">{notifyChannelLabels[d.channel]}</Badge>
        <Badge variant="secondary">{notifyModeLabels[d.mode]}</Badge>
        <Button className="ml-auto" variant="outline" onClick={() => setTesting(true)}>
          测试发送
        </Button>
      </div>
      {detail.error && <p className="text-sm text-destructive">{detail.error}</p>}

      <section className="space-y-4">
        <h2 className="text-base font-medium">模板内容</h2>
        <div className="space-y-2">
          <Label htmlFor="ntd-desc">备注</Label>
          <Input id="ntd-desc" value={cur.description} onChange={(e) => setDraft({ ...cur, description: e.target.value })} />
        </div>
        <div className="flex items-center gap-3">
          <Switch id="ntd-enabled" checked={cur.enabled} onCheckedChange={(v) => setDraft({ ...cur, enabled: v })} />
          <Label htmlFor="ntd-enabled">启用（停用后业务方调用会被拒绝）</Label>
        </div>
        <NotifyContentFields
          key={d.updatedAt}
          channel={d.channel}
          mode={d.mode}
          value={cur.content}
          onChange={(content) => setDraft({ ...cur, content })}
        />
        <Button onClick={() => void save()} disabled={draft === null || saving}>
          保存模板
        </Button>
      </section>

      <section className="space-y-4">
        <h2 className="text-base font-medium">关联的供应商</h2>
        <p className="text-sm text-muted-foreground">
          {singleProvider
            ? '这个渠道的模板只能关联一个供应商实例，不做降级。'
            : '发送时按优先级从高到低尝试（数字越大越先试，默认 0），同优先级的随机排序；失败自动降级到下一个。禁用的供应商不参与。'}
        </p>
        {d.providers.length === 0 ? (
          <p className="text-sm text-destructive">还没有关联供应商，业务方调用会失败。</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>供应商</TableHead>
                <TableHead>{vendor ? '供应商侧模板 ID' : ''}</TableHead>
                <TableHead>优先级</TableHead>
                <TableHead>启用</TableHead>
                <TableHead>操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {d.providers.map((l) => (
                <LinkRow key={l.providerId} code={d.code} vendor={vendor} link={l} onChanged={detail.reload} />
              ))}
            </TableBody>
          </Table>
        )}
        {!(singleProvider && d.providers.length > 0) && <AddLink code={d.code} detail={d} onAdded={detail.reload} />}
      </section>

      <TestSendDialog detail={d} open={testing} onOpenChange={setTesting} />
    </div>
  )
}
