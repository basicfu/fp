import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { cn } from '@/lib/utils'
import type { ConfigField, ConfigPartition, ConfigSnapshot, ConfigValueType, SaveConfigResponse } from '@/lib/types'

const partitions: ConfigPartition[] = ['DEFAULT', 'WEB']

const valueTypeOptions: ConfigValueType[] = ['bool', 'int', 'float', 'string', 'array', 'object']

/** 值类型的中文名，仅用于展示；提交给后端的仍然是英文 token（与 domain.ConfigValue* 逐字一致）。 */
const configValueTypeLabels: Record<ConfigValueType, string> = {
  bool: '布尔',
  int: '整数',
  float: '浮点数',
  string: '字符串',
  array: '数组（JSON）',
  object: '对象（JSON）',
}

/** 改类型或新建配置项时，给新类型一个合理的初始值。 */
function defaultValueForType(t: ConfigValueType): unknown {
  switch (t) {
    case 'bool':
      return false
    case 'int':
    case 'float':
      return 0
    case 'string':
      return ''
    case 'array':
      return []
    case 'object':
      return {}
  }
}

/** array/object 的文本框内容：未配置显示空串，否则是格式化过的 JSON。 */
function rawTextForValue(value: unknown): string {
  if (value === null || value === undefined) return ''
  return JSON.stringify(value, null, 2)
}

/**
 * 校验 int/float 字段编辑中的原始输入串，返回错误文案（''表示合法）。
 *
 * 【终审必须修】空字符串专门拦下来报错，不能当成"未配置"放行：后端
 * domain.CoerceConfigValue 判"未配置"靠 `len(s)==0 || s=="null"`，这个
 * 判断发生在去引号之前——JS 空字符串序列化上 wire 是带引号的 `""`（长度
 * 2），落不进那个分支，最终会被当成非法数字解析失败。而 Save 是"一项转
 * 不过去就整批拒绝"，所以清空一个数字字段会连累同一次保存里的其余字段
 * 全部不生效，报错还是后端的通用文案，看不出是哪个字段的问题。
 *
 * 这里的"清空"在这个 UI 里没有对应到"未配置"的语义：未配置（value 为
 * null）是这一项从服务端读回来时就是这个状态（新建时没有代码强制填值），
 * 不是通过清空输入框产生的。要让一个数字字段变回未配置，应该走删除配置
 * 项这条路——错误文案里直接写清楚，不指望管理员自己想到。
 */
function numericFieldError(type: 'int' | 'float', raw: string): string {
  if (raw === '') {
    return '数字字段不能为空；要让它变回未配置状态，请删除这个配置项。'
  }
  if (type === 'int') {
    // 含小数点的也要拦：后端用 int64 解析，3.7 这类值会在那边报错，
    // 不能等后端 400 才发现。
    if (!/^-?\d+$/.test(raw)) return '不是合法的整数。'
  } else if (Number.isNaN(Number(raw))) {
    return '不是合法的数字。'
  }
  return ''
}

/**
 * 按 key 的第一段点前缀分组，组内再按 key 排序。
 *
 * 用 draft 当前的 key 集合而不是服务端原始快照——新建/删除都要立刻反映到
 * 分组里，不能等保存之后才更新。
 */
function groupFields(keys: string[]): [string, string[]][] {
  const groups = new Map<string, string[]>()
  for (const key of keys) {
    const dot = key.indexOf('.')
    // dot <= 0 而不是 === -1：以点开头的 key（比如 ".foo"）切出来的前缀是
    // 空字符串，会渲染成一个看不见文字的分组标题，比"未分组"更让人费解。
    const group = dot <= 0 ? '未分组' : key.slice(0, dot)
    const arr = groups.get(group)
    if (arr) arr.push(key)
    else groups.set(group, [key])
  }
  const entries = [...groups.entries()]
  for (const [, arr] of entries) arr.sort()
  return entries.sort(([a], [b]) => {
    if (a === b) return 0
    // "未分组"固定排最后：其余组名是管理员自己起的、有意义的前缀，
    // 未分组只是"没有前缀"，不该抢在有意义的分组前面。
    if (a === '未分组') return 1
    if (b === '未分组') return -1
    return a.localeCompare(b)
  })
}

export default function ConfigCenter() {
  const { id = '' } = useParams()
  const [partition, setPartition] = useState<ConfigPartition>('DEFAULT')
  const snapshot = useResource(
    () => api.get<ConfigSnapshot>(`/applications/${id}/config?type=${partition}`),
    [id, partition],
  )

  // draft 是本地编辑中的 fields，保存时永远整份提交——接口是全量替换，
  // 只提交被改过的字段会让其余字段在新版本里凭空消失。
  const [draft, setDraft] = useState<Record<string, ConfigField>>({})
  // array/object 字段编辑中的原始 JSON 文本，与 draft 分开存：不合法的
  // 半成品 JSON（比如刚打完一个 "{"）不该直接进 draft，那会让"全量提交"
  // 把一个解析不出来的东西发给后端。失焦时才校验、才真正写回 draft。
  const [rawText, setRawText] = useState<Record<string, string>>({})
  // fieldErrors 是字段级校验错误：key -> 错误文案，没有错误的字段不在
  // 这个对象里。【终审必须修】原来只有 array/object 的 JSON 校验会写进
  // 这里（叫 jsonErrors），但数字字段清空、或填非法值同样会导致后端整批
  // 拒绝保存——两类错误本质上是同一件事（"draft 里这一项转不成声明的类型，
  // 保存前必须挡住"），合并成一个通用的字段级错误表，避免以后再加一种
  // 类型又要建第三套校验状态。
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [push, setPush] = useState(true)
  const [saving, setSaving] = useState(false)
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [adding, setAdding] = useState(false)
  const [deleteKey, setDeleteKey] = useState<string | null>(null)
  const [typeChange, setTypeChange] = useState<{ key: string; next: ConfigValueType } | null>(null)

  // 分区切换、或保存成功后的 reload，都要用服务端的权威数据重置本地草稿：
  // 不重置的话，切分区会把上一个分区的编辑内容错误地留在界面上；保存成功
  // 后不重置的话，界面会一直显示提交前的草稿，看不出这次保存真正生效的
  // 是什么（尤其是 push=false 时后端可能做过强类型转换）。
  useEffect(() => {
    if (!snapshot.data) return
    setDraft(snapshot.data.fields)
    const rt: Record<string, string> = {}
    for (const [k, f] of Object.entries(snapshot.data.fields)) {
      if (f.type === 'array' || f.type === 'object') rt[k] = rawTextForValue(f.value)
    }
    setRawText(rt)
    setFieldErrors({})
    setPush(true)
  }, [snapshot.data])

  /** 设置或清空某个字段的错误文案；message 为空串表示清掉这一项的错误。 */
  function setFieldError(key: string, message: string) {
    setFieldErrors((e) => {
      if (message === '') {
        if (!(key in e)) return e
        const next = { ...e }
        delete next[key]
        return next
      }
      if (e[key] === message) return e
      return { ...e, [key]: message }
    })
  }

  function updateValue(key: string, value: unknown) {
    setDraft((d) => ({ ...d, [key]: { ...d[key], value } }))
    // int/float 走 <input type="number">，值在编辑中始终是原始字符串
    // （见 ValueControl）——每次变化都校验一遍，不等失焦，因为空字符串
    // 这种"看起来什么都没做错"的中间态本身就是需要立刻拦住的那个问题。
    const type = draft[key]?.type
    if (type === 'int' || type === 'float') {
      setFieldError(key, numericFieldError(type, String(value)))
    }
  }

  function updateRawText(key: string, text: string) {
    setRawText((r) => ({ ...r, [key]: text }))
  }

  /** 失焦时校验 array/object 的 JSON 文本，合法才写回 draft.value。 */
  function commitRawText(key: string) {
    const field = draft[key]
    if (!field) return
    const text = (rawText[key] ?? '').trim()
    if (text === '') {
      // 清空文本框视为把这一项重新变回"未配置"，而不是一个错误——这一点
      // array/object 与 int/float 不同：array/object 的"空"在 JSON 里没有
      // 歧义（就是没填），数字的"空"在 wire 上是带引号的空字符串，两者不
      // 能同一套处理，这也是 numericFieldError 单独存在的原因。
      updateValue(key, null)
      setFieldError(key, '')
      return
    }
    let parsed: unknown
    try {
      parsed = JSON.parse(text)
    } catch {
      setFieldError(key, '不是合法的 JSON，请修正后再保存。')
      return
    }
    // 形状必须与声明的类型匹配：array 不能是 object，反之亦然，
    // 与后端 domain.CoerceConfigValue 的校验对齐。
    if (field.type === 'array' && !Array.isArray(parsed)) {
      setFieldError(key, '内容必须是 JSON 数组。')
      return
    }
    if (field.type === 'object' && (Array.isArray(parsed) || typeof parsed !== 'object' || parsed === null)) {
      setFieldError(key, '内容必须是 JSON 对象。')
      return
    }
    updateValue(key, parsed)
    setFieldError(key, '')
  }

  function requestTypeChange(key: string, next: ConfigValueType) {
    if (draft[key]?.type === next) return
    setTypeChange({ key, next })
  }

  function confirmTypeChange() {
    if (!typeChange) return
    const { key, next } = typeChange
    setDraft((d) => ({ ...d, [key]: { ...d[key], type: next, value: defaultValueForType(next) } }))
    if (next === 'array' || next === 'object') {
      setRawText((r) => ({ ...r, [key]: next === 'array' ? '[]' : '{}' }))
    }
    // defaultValueForType 给的初始值对新类型总是合法的（0/false/''/[]/{}），
    // 旧类型可能留下的错误必须清掉，否则改完类型保存按钮还会莫名其妙地
    // 保持禁用。
    setFieldError(key, '')
    setTypeChange(null)
  }

  function confirmDelete() {
    const key = deleteKey
    if (!key) return
    setDraft((d) => {
      const next = { ...d }
      delete next[key]
      return next
    })
    setRawText((r) => {
      const next = { ...r }
      delete next[key]
      return next
    })
    setFieldError(key, '')
    setDeleteKey(null)
  }

  function addField(key: string, field: ConfigField) {
    setDraft((d) => ({ ...d, [key]: field }))
    if (field.type === 'array' || field.type === 'object') {
      setRawText((r) => ({ ...r, [key]: rawTextForValue(field.value) }))
    }
    setAdding(false)
  }

  function toggleGroup(name: string) {
    setCollapsed((s) => {
      const next = new Set(s)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  async function handleSave() {
    if (Object.keys(fieldErrors).length > 0) return
    setSaving(true)
    try {
      const res = await api.put<SaveConfigResponse>(`/applications/${id}/config`, {
        type: partition,
        push,
        // 永远提交完整的 draft——接口是全量替换，只提交被改过的字段的话，
        // 其余字段会在新版本里凭空消失（等于被删）。
        fields: draft,
      })
      toast.success(push ? `已推送，当前版本 seq=${res.seq}` : `已落库，seq=${res.seq}，实例重启后生效`)
      snapshot.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  const unsetCount = Object.values(draft).filter((f) => f.value === null).length
  const groups = groupFields(Object.keys(draft))
  const hasFieldErrors = Object.keys(fieldErrors).length > 0

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <div>
          <Link to={`/applications/${id}`} className="text-sm text-muted-foreground underline-offset-4 hover:underline">
            ← 返回应用详情
          </Link>
          <h1 className="text-xl font-semibold">配置中心</h1>
        </div>
        <div className="flex-1" />
        <Button variant="outline" onClick={() => setAdding(true)}>
          新建配置项
        </Button>
      </div>

      <Tabs value={partition} onValueChange={(v) => setPartition(v as ConfigPartition)}>
        <TabsList>
          {partitions.map((p) => (
            <TabsTrigger key={p} value={p}>
              {p}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      {snapshot.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {snapshot.error && <p className="text-sm text-destructive">{snapshot.error}</p>}

      {!snapshot.loading && !snapshot.error && (
        <>
          {unsetCount > 0 && (
            <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
              {unsetCount} 项未配置：代码里已经在读这些 key，但还没有人填过值。
            </p>
          )}

          <div className="flex flex-wrap items-center gap-4 rounded-md border p-3">
            <span className="text-sm font-medium">生效方式</span>
            <div className="flex items-center gap-2">
              <input
                type="radio"
                id={`cfg-${partition}-push-immediate`}
                name={`cfg-${partition}-push-mode`}
                className="size-4"
                checked={push}
                onChange={() => setPush(true)}
              />
              <Label htmlFor={`cfg-${partition}-push-immediate`} className="font-normal">
                立即推送（默认）
              </Label>
            </div>
            <div className="flex items-center gap-2">
              <input
                type="radio"
                id={`cfg-${partition}-push-lazy`}
                name={`cfg-${partition}-push-mode`}
                className="size-4"
                checked={!push}
                onChange={() => setPush(false)}
              />
              <Label htmlFor={`cfg-${partition}-push-lazy`} className="font-normal">
                仅落库，实例重启后生效
              </Label>
            </div>
            <div className="flex-1" />
            <Button onClick={() => void handleSave()} disabled={saving || hasFieldErrors}>
              保存
            </Button>
          </div>

          <div className="space-y-3">
            {groups.length === 0 && <p className="text-sm text-muted-foreground">这个分区还没有任何配置项。</p>}
            {groups.map(([name, keys]) => (
              <div key={name} className="rounded-md border">
                <button
                  type="button"
                  onClick={() => toggleGroup(name)}
                  aria-expanded={!collapsed.has(name)}
                  className="flex w-full items-center gap-2 rounded-t-md p-2 text-left text-sm font-medium hover:bg-muted/50"
                >
                  <span aria-hidden>{collapsed.has(name) ? '▸' : '▾'}</span>
                  {name}
                  <span className="text-xs font-normal text-muted-foreground">({keys.length})</span>
                </button>
                {!collapsed.has(name) && (
                  <div className="divide-y border-t">
                    {keys.map((key) => (
                      <FieldRow
                        key={key}
                        partition={partition}
                        fieldKey={key}
                        field={draft[key]}
                        rawText={rawText[key] ?? ''}
                        error={fieldErrors[key] ?? ''}
                        onValueChange={(v) => updateValue(key, v)}
                        onRawTextChange={(t) => updateRawText(key, t)}
                        onRawTextBlur={() => commitRawText(key)}
                        onRequestTypeChange={(next) => requestTypeChange(key, next)}
                        onRequestDelete={() => setDeleteKey(key)}
                      />
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        </>
      )}

      <AddFieldDialog open={adding} onOpenChange={setAdding} existingKeys={draft} onAdd={addField} />

      <ConfirmDialog
        open={deleteKey !== null}
        onOpenChange={(v) => !v && setDeleteKey(null)}
        title="删除配置项"
        description={
          deleteKey
            ? `删除「${deleteKey}」。没有机制能确认它是否还被代码读取；删错了，运行中的实例会保持旧值并报错，新起的实例会起不来。这里的删除要点了下面的"保存"才真正生效。`
            : ''
        }
        confirmLabel="确认删除"
        onConfirm={confirmDelete}
      />

      <ConfirmDialog
        open={typeChange !== null}
        onOpenChange={(v) => !v && setTypeChange(null)}
        title="修改配置项类型"
        description={
          typeChange
            ? `把「${typeChange.key}」的类型从「${configValueTypeLabels[draft[typeChange.key]?.type ?? 'string']}」改成` +
              `「${configValueTypeLabels[typeChange.next]}」。旧实例若收到推送会解析失败：它们的代码是按旧类型读这个值的，` +
              `类型一变，反序列化会直接报错。请确认所有还在跑的实例都已经能处理新类型，或者先选"仅落库"，等实例逐个重启完再推送。`
            : ''
        }
        confirmLabel="确认修改"
        onConfirm={confirmTypeChange}
      />
    </div>
  )
}

/** ValueControl 按类型渲染值控件，FieldRow 与 AddFieldDialog 共用。 */
function ValueControl({
  id,
  ariaLabel,
  type,
  value,
  rawText,
  invalid,
  onChange,
  onRawTextChange,
  onRawTextBlur,
}: {
  id: string
  ariaLabel: string
  type: ConfigValueType
  /** bool/int/float/string 的当前值；array/object 时不读这个，读 rawText。 */
  value: unknown
  rawText: string
  invalid: boolean
  onChange: (v: unknown) => void
  onRawTextChange: (text: string) => void
  onRawTextBlur: () => void
}) {
  if (type === 'bool') {
    return (
      <div className="flex items-center gap-2">
        <Switch id={id} aria-label={ariaLabel} checked={Boolean(value)} onCheckedChange={onChange} />
        <span className="text-sm text-muted-foreground">{value ? '开' : '关'}</span>
      </div>
    )
  }

  if (type === 'array' || type === 'object') {
    return (
      <textarea
        id={id}
        aria-label={ariaLabel}
        rows={4}
        className={cn(
          'w-full rounded-lg border bg-transparent px-2.5 py-1.5 font-mono text-sm outline-none focus-visible:ring-3 focus-visible:ring-ring/50',
          invalid ? 'border-destructive' : 'border-input',
        )}
        value={rawText}
        onChange={(e) => onRawTextChange(e.target.value)}
        onBlur={onRawTextBlur}
      />
    )
  }

  return (
    <Input
      id={id}
      aria-label={ariaLabel}
      type={type === 'int' || type === 'float' ? 'number' : 'text'}
      step={type === 'float' ? 'any' : undefined}
      // Input 组件自带 aria-invalid:border-destructive 之类的样式（见
      // ui/input.tsx），数字字段校验失败时靠这个属性变红，和 array/object
      // 的 textarea 视觉上保持一致，不用再手写一套 className 判断。
      aria-invalid={invalid || undefined}
      value={value === null || value === undefined ? '' : String(value)}
      onChange={(e) => onChange(e.target.value)}
    />
  )
}

/** TypeSelect 是类型下拉，FieldRow 与 AddFieldDialog 共用。 */
function TypeSelect({
  id,
  ariaLabel,
  value,
  onChange,
}: {
  id: string
  ariaLabel: string
  value: ConfigValueType
  onChange: (v: ConfigValueType) => void
}) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as ConfigValueType)}>
      <SelectTrigger id={id} aria-label={ariaLabel} className="w-32">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {valueTypeOptions.map((t) => (
          <SelectItem key={t} value={t}>
            {configValueTypeLabels[t]}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function FieldRow({
  partition,
  fieldKey,
  field,
  rawText,
  error,
  onValueChange,
  onRawTextChange,
  onRawTextBlur,
  onRequestTypeChange,
  onRequestDelete,
}: {
  partition: ConfigPartition
  fieldKey: string
  field: ConfigField
  rawText: string
  /** 这一项当前的校验错误文案，''表示没有错误。 */
  error: string
  onValueChange: (value: unknown) => void
  onRawTextChange: (text: string) => void
  onRawTextBlur: () => void
  onRequestTypeChange: (next: ConfigValueType) => void
  onRequestDelete: () => void
}) {
  // 命名空间化：key 本身带点、比 connector 的字段名更容易撞——两个分区
  // 各自的字段列表若不加区分会共享同一个 DOM id。
  const domId = `cfg-${partition}-${fieldKey}`
  const unset = field.value === null
  const invalid = error !== ''

  return (
    <div className="space-y-2 p-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0 space-y-0.5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="break-all font-mono text-sm">{fieldKey}</span>
            {unset && <Badge variant="destructive">未配置</Badge>}
          </div>
          {field.desc && <p className="text-xs text-muted-foreground">{field.desc}</p>}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <TypeSelect
            id={`${domId}-type`}
            ariaLabel={`${fieldKey} 的类型`}
            value={field.type}
            onChange={onRequestTypeChange}
          />
          <Button variant="outline" size="sm" onClick={onRequestDelete}>
            删除 {fieldKey}
          </Button>
        </div>
      </div>

      <ValueControl
        id={domId}
        ariaLabel={fieldKey}
        type={field.type}
        value={field.value}
        rawText={rawText}
        invalid={invalid}
        onChange={onValueChange}
        onRawTextChange={onRawTextChange}
        onRawTextBlur={onRawTextBlur}
      />
      {invalid && <p className="text-sm text-destructive">{error}</p>}
    </div>
  )
}

function AddFieldDialog({
  open,
  onOpenChange,
  existingKeys,
  onAdd,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  existingKeys: Record<string, ConfigField>
  onAdd: (key: string, field: ConfigField) => void
}) {
  const [key, setKey] = useState('')
  const [type, setType] = useState<ConfigValueType>('string')
  const [desc, setDesc] = useState('')
  const [boolValue, setBoolValue] = useState(false)
  const [textValue, setTextValue] = useState('')
  const [error, setError] = useState('')

  // 每次打开都清空，不残留上一次没提交完的输入。
  useEffect(() => {
    if (!open) return
    setKey('')
    setType('string')
    setDesc('')
    setBoolValue(false)
    setTextValue('')
    setError('')
  }, [open])

  /** 切换类型时把值编辑器一并清空，避免上一个类型残留的文本串到新控件里。 */
  function handleTypeChange(next: ConfigValueType) {
    setType(next)
    setBoolValue(false)
    setTextValue('')
    setError('')
  }

  function submit() {
    const k = key.trim()
    if (!k) {
      setError('请输入 key')
      return
    }
    if (Object.prototype.hasOwnProperty.call(existingKeys, k)) {
      setError('这个 key 在当前分区已经存在')
      return
    }

    let value: unknown
    if (type === 'bool') {
      // 开关没有"空"状态，天然满足"值必填"。
      value = boolValue
    } else if (type === 'array' || type === 'object') {
      const t = textValue.trim()
      // 值必填：这是凭空新建的条目，不像自动发现出来的未配置项那样
      // 是"代码已声明、等人填"——没有任何代码强制它，留空没有意义。
      if (!t) {
        setError('值必填')
        return
      }
      try {
        const parsed = JSON.parse(t) as unknown
        if (type === 'array' && !Array.isArray(parsed)) throw new Error()
        if (type === 'object' && (Array.isArray(parsed) || typeof parsed !== 'object' || parsed === null)) {
          throw new Error()
        }
        value = parsed
      } catch {
        setError(`值不是合法的 JSON${type === 'array' ? '数组' : '对象'}`)
        return
      }
    } else if (type === 'int' || type === 'float') {
      const t = textValue.trim()
      if (!t) {
        setError('值必填')
        return
      }
      const n = Number(t)
      if (!Number.isFinite(n) || (type === 'int' && !Number.isInteger(n))) {
        setError(type === 'int' ? '不是合法的整数' : '不是合法的数字')
        return
      }
      value = n
    } else {
      if (!textValue.trim()) {
        setError('值必填')
        return
      }
      value = textValue
    }

    onAdd(k, { type, desc, value })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建配置项</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="cfg-new-key">key</Label>
            <Input
              id="cfg-new-key"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              placeholder="upstream.timeout"
              className="font-mono"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="cfg-new-type">类型</Label>
            <TypeSelect id="cfg-new-type" ariaLabel="类型" value={type} onChange={handleTypeChange} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="cfg-new-value">值</Label>
            <ValueControl
              id="cfg-new-value"
              ariaLabel="值"
              type={type}
              value={type === 'bool' ? boolValue : textValue}
              rawText={textValue}
              invalid={false}
              onChange={(v) => (type === 'bool' ? setBoolValue(Boolean(v)) : setTextValue(String(v)))}
              onRawTextChange={setTextValue}
              onRawTextBlur={() => {}}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="cfg-new-desc">备注</Label>
            <Input id="cfg-new-desc" value={desc} onChange={(e) => setDesc(e.target.value)} />
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button type="button" onClick={submit}>
            创建
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
