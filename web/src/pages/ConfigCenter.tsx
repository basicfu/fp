import { useEffect, useState, type KeyboardEvent } from 'react'
import { Link } from 'react-router'
import { Plus } from 'lucide-react'
import { load as loadYAML } from 'js-yaml'
import { useCurrentApp } from '@/lib/current-app'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { useConfigTypes } from '@/lib/useConfigTypes'
import { cn } from '@/lib/utils'
import { DEFAULT_PARTITION, type ConfigPartition, type ConfigSnapshot, type SaveConfigResponse } from '@/lib/types'

/** 分区名的字符集：字母开头，字母/数字/下划线，不超过 64 个字符——与后端 domain.IsConfigType 逐字一致。 */
const partitionNamePattern = /^[A-Za-z][A-Za-z0-9_]{0,63}$/

/**
 * normalizeYAML 给"key:value"这种冒号后漏了空格的行补上那个空格（列表
 * 项前缀、缩进都保留，注释行不动）——跟后端 domain.NormalizeConfigYAML
 * 是同一条启发式规则的前端版本，只用来在打字时就让校验通过，真正落库的
 * 补全动作由后端做一遍权威的。
 */
function normalizeYAML(text: string): string {
  return text
    .split('\n')
    .map((line) => {
      const trimmed = line.trimStart()
      if (trimmed === '' || trimmed.startsWith('#')) return line
      return line.replace(/^(\s*(?:-\s+)?[^:\s][^:]*):(\S)/, '$1: $2')
    })
    .join('\n')
}

/**
 * validateYAML 只做一件事：这段文本（补完冒号空格之后）能不能被解析、且
 * 顶层是不是一个映射。不做任何值级别的校验——YAML 自己的字面量语法就是
 * 类型信息。跟后端 domain.ParseConfigYAML 是同一条校验规则的前端版本，
 * 只是提前到打字时就告诉人，不用等点保存才知道。
 */
function validateYAML(text: string): string {
  const normalized = normalizeYAML(text)
  if (normalized.trim() === '') return ''
  let parsed: unknown
  try {
    parsed = loadYAML(normalized)
  } catch (e) {
    return e instanceof Error ? e.message : '不是合法的 YAML'
  }
  if (parsed === null || parsed === undefined) return ''
  if (typeof parsed !== 'object' || Array.isArray(parsed)) {
    return '顶层必须是一个映射（key: value 的形式），不能是列表或裸标量'
  }
  return ''
}

export default function ConfigCenter() {
  const { currentApp, apps, loading, error } = useCurrentApp()
  const id = currentApp?.id ?? ''
  const [partition, setPartition] = useState<ConfigPartition>(DEFAULT_PARTITION)
  const types = useConfigTypes(id)
  const snapshot = useResource(
    () =>
      id
        ? api.get<ConfigSnapshot>(`/applications/${id}/config?type=${partition}`)
        : Promise.resolve<ConfigSnapshot>({ seq: 0, value: '' }),
    [id, partition],
  )

  // draft 是编辑框里的原文；服务端数据一到（首次加载、切分区、保存成功后
  // 的 reload）就用它整体覆盖——不做任何"合并本地改动"的尝试，YAML
  // 编辑框本来就是"整份替换"的心智模型，不是逐字段增量编辑。
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const [addingType, setAddingType] = useState(false)
  const [newType, setNewType] = useState('')
  const [creatingType, setCreatingType] = useState(false)

  useEffect(() => {
    if (!snapshot.data) return
    setDraft(snapshot.data.value)
  }, [snapshot.data])

  // 边框颜色的实时反馈不占布局（只是描边变色，不产生/挪走任何一段
  // 文本），所以保留；但校验失败的具体原因不再常驻显示成一段 <p>——
  // 那段文本会随着每次按键增删，把下面的按钮一跳一跳地顶上顶下。原因
  // 改成点保存时才用 toast 报，是"操作触发的错误用 toast"这条既有原则
  // 的自然延伸，而不是新开一条例外。
  const validationError = validateYAML(draft)
  const dirty = snapshot.data !== null && draft !== snapshot.data.value

  async function handleSave(push: boolean) {
    const err = validateYAML(draft)
    if (err) {
      toast.error(err)
      return
    }
    setSaving(true)
    try {
      const res = await api.put<SaveConfigResponse>(`/applications/${id}/config`, {
        type: partition,
        push,
        value: draft,
      })
      toast.success(push ? `已推送，当前版本 seq=${res.seq}` : `已落库，seq=${res.seq}，实例重启后生效`)
      snapshot.reload()
      types.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  async function handleDeleteType() {
    setDeleting(true)
    try {
      await api.del(`/applications/${id}/config?type=${partition}`)
      toast.success(`已删除「${partition}」分区的全部历史版本`)
      setConfirmDelete(false)
      if (partition !== DEFAULT_PARTITION) setPartition(DEFAULT_PARTITION)
      else snapshot.reload()
      types.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setDeleting(false)
    }
  }

  function openAddType() {
    setNewType('')
    setAddingType(true)
  }

  async function confirmAddType() {
    const t = newType.trim()
    if (!t) {
      toast.error('请输入分区名')
      return
    }
    if (!partitionNamePattern.test(t)) {
      toast.error('分区名必须以字母开头，只能包含字母、数字、下划线，且不超过 64 个字符')
      return
    }
    if (types.types.includes(t)) {
      toast.error('这个分区已经存在')
      return
    }
    setCreatingType(true)
    try {
      // 新分区落一个空值的版本——这样它立刻在数据库里真实存在（能被
      // /config/types 列出来），不是只活在前端这一次会话里的临时状态。
      await api.put<SaveConfigResponse>(`/applications/${id}/config`, { type: t, push: false, value: '' })
      toast.success(`已创建分区「${t}」`)
      setAddingType(false)
      setPartition(t)
      types.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setCreatingType(false)
    }
  }

  /** Tab 在这个编辑框里是缩进，不是切到下一个控件——YAML 靠缩进表达层级，浏览器默认的"移走焦点"在这没用。 */
  function handleTextareaKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key !== 'Tab') return
    e.preventDefault()
    const el = e.currentTarget
    const start = el.selectionStart
    const end = el.selectionEnd
    setDraft(draft.slice(0, start) + '  ' + draft.slice(end))
    requestAnimationFrame(() => {
      el.selectionStart = el.selectionEnd = start + 2
    })
  }

  if (error) return <p className="text-sm text-destructive">{error}</p>
  if (loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (apps.length === 0) {
    return <p className="text-sm text-muted-foreground">还没有应用，请先在「应用列表」创建一个。</p>
  }
  if (!currentApp) return null

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-2">
        <Tabs value={partition} onValueChange={(v) => setPartition(v as ConfigPartition)}>
          <TabsList>
            {types.types.map((p) => (
              <TabsTrigger key={p} value={p}>
                {p}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
        {/* "+"建一个新分区——名字随便起，不是只能叫 WEB。确认后立刻落一个
            空值版本，分区从这一刻起就是数据库里真实存在的东西。 */}
        <Button variant="outline" size="icon-sm" aria-label="新建分区" onClick={openAddType}>
          <Plus />
        </Button>
        <div className="flex-1" />
        <Button variant="outline" render={<Link to="/config/versions" />}>
          版本历史
        </Button>
      </div>

      {/* 只在**真的还没拿到过数据**时显示"加载中…"占位——useResource 的
          reload（每次保存/删除/切分区之后都会触发）会把 loading 重新置
          true，但不会把已经拿到的 data 清空。如果这里改成"只要 loading
          就隐藏编辑框"，编辑框会在每次保存后瞬间卸载又重新挂载一次：
          光标位置、正在输入的内容全部丢一遍，用户体验很差，测试里也会
          撞上"元素被换了一个新的 DOM 节点"的时序坑。 */}
      {snapshot.data === null && snapshot.loading && (
        <p className="text-sm text-muted-foreground">加载中…</p>
      )}
      {snapshot.error && <p className="text-sm text-destructive">{snapshot.error}</p>}

      {snapshot.data !== null && !snapshot.error && (
        <>
          {/* 一整个 YAML 编辑框：一个字段就是一行 key: value，注释
              （# ...）就是备注，不用再为每个字段单独维护类型/备注/值三样
              东西。冒号后漏个空格（比如 port:4379）不会挡保存——打字校验
              和真正落库都会自动补上那个空格。 */}
          <Label htmlFor={`cfg-${partition}-yaml`} className="sr-only">
            {partition} 分区的配置（YAML）
          </Label>
          <textarea
            id={`cfg-${partition}-yaml`}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={handleTextareaKeyDown}
            spellCheck={false}
            placeholder={'upstream:\n  timeout: 3000 # 超时时间（毫秒）\n  retries: 3\nfee_rate: 0.02 # 手续费率\n'}
            className={cn(
              // 最高不超过默认高度（60vh）：resize-y 允许用户往小了拖，但不能
              // 拖得比这更高——拖高了会把下面"仅保存/保存并推送/删除"这一排
              // 按钮顶到视口外面去，够不着。
              'h-[60vh] max-h-[60vh] w-full resize-y overflow-y-auto rounded-lg border bg-transparent p-3 font-mono text-sm leading-relaxed outline-none',
              validationError ? 'border-destructive' : 'border-input',
            )}
          />

          <div className="flex items-center gap-2">
            <Button variant="outline" onClick={() => void handleSave(false)} disabled={saving || !dirty}>
              仅保存
            </Button>
            <Button onClick={() => void handleSave(true)} disabled={saving || !dirty}>
              保存并推送
            </Button>
            <div className="flex-1" />
            <Button variant="destructive" onClick={() => setConfirmDelete(true)} disabled={deleting}>
              删除
            </Button>
          </div>
        </>
      )}

      <Dialog open={addingType} onOpenChange={setAddingType}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>新建分区</DialogTitle>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="cfg-new-type">分区名</Label>
            <Input
              id="cfg-new-type"
              value={newType}
              onChange={(e) => setNewType(e.target.value)}
              placeholder="WEB / MOBILE / ADMIN_PANEL…"
              className="font-mono"
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setAddingType(false)}>
              取消
            </Button>
            <Button type="button" onClick={() => void confirmAddType()} disabled={creatingType}>
              确认
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`删除「${partition}」分区`}
        description={`删除「${partition}」分区的全部内容，包括所有历史版本，不可撤销——删完之后这个分区一个版本都不剩，没有任何东西可以回滚回去。运行中的实例会保留最后一次成功加载的值，直到重启或下次重新拉取才会感知到这个分区已经没了。`}
        confirmLabel="确认删除"
        onConfirm={() => void handleDeleteType()}
      />
    </div>
  )
}
