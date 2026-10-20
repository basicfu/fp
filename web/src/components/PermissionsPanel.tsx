import { useEffect, useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LabelHint } from '@/components/ui/label-hint'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { toastFormErrors } from '@/lib/formErrors'
import { formatDuration, formatTime } from '@/lib/format'
import { permissionStatusLabels } from '@/lib/labels'
import { colorPrefix, layoutPermissions, methodColumnWidths, prefixColorIndex, type TreeRow } from '@/lib/permissionTree'
import type { Application, PermissionPoint } from '@/lib/types'

/**
 * PermissionsPanel 是应用详情下的「权限点」页。
 *
 * 权限点属于应用（角色是全局的），所以它挂在应用详情而不是单独一个顶级
 * 菜单。默认角色的设置挪到了应用列表的新建/编辑弹窗里，这里不再管。
 */
export default function PermissionsPanel({ app }: { app: Application }) {
  const perms = useResource(() => api.get<PermissionPoint[]>(`/applications/${app.id}/permissions`), [app.id])
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<PermissionPoint | null>(null)
  const [deleting, setDeleting] = useState<{ p: PermissionPoint; holders: string[] } | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [batchDeleting, setBatchDeleting] = useState<{ ids: string[]; keys: string[]; holders: string[] } | null>(
    null,
  )
  const [keyword, setKeyword] = useState('')
  // 默认全部展开，所以只记「用户折叠了哪些」：搜索时的自动展开不写进来，清空
  // 关键字后才能恢复用户原来的样子。
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set())

  // 草稿用字符串存：输入框清空重填的中途不能把数字强行改回去。
  const [depthInput, setDepthInput] = useState(() => loadSetting(MAX_DEPTH_KEY, DEFAULT_MAX_DEPTH))
  const maxDepth = parseCount(depthInput) ?? DEFAULT_MAX_DEPTH
  const [toneInput, setToneInput] = useState(() => loadSetting(TONE_COUNT_KEY, DEFAULT_TONE_COUNT))
  const toneDepth = parseCount(toneInput) ?? DEFAULT_TONE_COUNT

  const all = useMemo(() => perms.data ?? [], [perms.data])
  const kw = keyword.trim().toLowerCase()
  const shown = useMemo(
    () => (kw ? all.filter((p) => p.key.toLowerCase().includes(kw) || p.name.toLowerCase().includes(kw)) : all),
    [all, kw],
  )
  const shownIds = shown.map((p) => p.id)
  const shownSelectedCount = shownIds.filter((id) => selected.has(id)).length
  const allShownSelected = shownIds.length > 0 && shownSelectedCount === shownIds.length

  const layout = useMemo(
    () => layoutPermissions(shown, (id) => kw !== '' || !collapsedGroups.has(id), maxDepth),
    [shown, kw, collapsedGroups, maxDepth],
  )
  // 分色不分组：取路径前 n 段当前缀，前缀相同的行同色、不同前缀按序轮换颜色。
  // 编号基于全量权限点算，搜索过滤时颜色不跟着跳。
  const prefixIndex = useMemo(() => prefixColorIndex(all.map((p) => p.key), toneDepth), [all, toneDepth])
  const toneOf = (key: string) => {
    if (toneDepth === 0) return undefined
    const p = colorPrefix(key, toneDepth)
    return p === null ? undefined : prefixIndex.get(p)
  }
  const methodWidths = useMemo(() => methodColumnWidths(layout.rows), [layout.rows])
  const allExpanded =
    layout.groupIds.length > 0 && layout.groupIds.every((id) => kw !== '' || !collapsedGroups.has(id))

  // 权限点被删掉（不管是这里批量删的，还是单条删的）之后，选中集合里
  // 残留的那个 id 不会自己消失——下次重新拉到的列表里已经没有这一项，
  // 但 Set 是独立状态，不会跟着同步。不清理的话"已选 N 项"会一直算上
  // 这个不存在的 id，多选几次、删几次，这个数字就跟界面上实际打钩的
  // 数量对不上。
  useEffect(() => {
    if (!perms.data) return
    const validIds = new Set(perms.data.map((p) => p.id))
    setSelected((prev) => {
      const next = new Set([...prev].filter((id) => validIds.has(id)))
      return next.size === prev.size ? prev : next
    })
  }, [perms.data])

  function toggleAll(checked: boolean) {
    setSelected((prev) => {
      const next = new Set(prev)
      shownIds.forEach((id) => (checked ? next.add(id) : next.delete(id)))
      return next
    })
  }

  function toggleGroup(ids: string[], checked: boolean) {
    setSelected((prev) => {
      const next = new Set(prev)
      ids.forEach((id) => (checked ? next.add(id) : next.delete(id)))
      return next
    })
  }

  function toggleCollapse(id: string) {
    setCollapsedGroups((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function toggleExpandAll() {
    setCollapsedGroups(allExpanded ? new Set(layout.groupIds) : new Set())
  }

  function toggleOne(id: string, checked: boolean) {
    setSelected((prev) => {
      const next = new Set(prev)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  /**
   * 删除前先问后端"谁在用它"。
   *
   * 这是唯一的安全网：设计上没有可逆的"停用"中间态，删除就是真删，并且
   * 会连带删掉所有角色对它的授权（数据库外键级联）。所以确认框里必须写
   * 清楚有几个角色会因此掉权限，而不是笼统地问"确定删除吗"。
   */
  async function askDelete(p: PermissionPoint) {
    try {
      const res = await api.get<{ roles: string[] }>(`/permissions/${p.id}/holders`)
      setDeleting({ p, holders: res.roles })
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  async function remove(p: PermissionPoint) {
    try {
      await api.del(`/permissions/${p.id}`)
      toast.success('已删除')
      perms.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  /** 批量删除同样先查一遍持有者——只是要把选中的每一项都问一遍，再把角色名去重合并。 */
  async function askBatchDelete() {
    const ids = [...selected]
    try {
      const results = await Promise.all(ids.map((id) => api.get<{ roles: string[] }>(`/permissions/${id}/holders`)))
      const holders = [...new Set(results.flatMap((r) => r.roles))]
      const keys = all.filter((p) => selected.has(p.id)).map((p) => p.key)
      setBatchDeleting({ ids, keys, holders })
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  /**
   * 逐条发 DELETE 而不是并发一把梭：批量里某一条失败（比如网络抖了一下）
   * 不该连累其它条的判断——已经删成功的要从选中里摘掉，不用再选一遍；
   * 失败的留着选中状态，方便直接再点一次重试。
   */
  async function removeBatch(ids: string[]) {
    const succeededIds: string[] = []
    const failures: string[] = []
    for (const id of ids) {
      try {
        await api.del(`/permissions/${id}`)
        succeededIds.push(id)
      } catch (e) {
        failures.push(errorMessage(e))
      }
    }
    if (succeededIds.length > 0) {
      toast.success(`已删除 ${succeededIds.length} 个权限点`)
      perms.reload()
    }
    if (failures.length > 0) {
      toast.error(`有 ${failures.length} 个删除失败：${failures.join('；')}`)
    }
    setSelected((prev) => {
      const next = new Set(prev)
      succeededIds.forEach((id) => next.delete(id))
      return next
    })
  }

  return (
    <div className="space-y-6">
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" onClick={() => setAdding(true)}>
            添加权限
          </Button>
          <Input
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            placeholder="按路径或名称筛选"
            className="w-72"
          />
          <Button variant="outline" onClick={toggleExpandAll}>
            {allExpanded ? '全部折叠' : '全部展开'}
          </Button>
          <label className="flex items-center gap-2 text-sm text-muted-foreground">
            最大层级
            <Input
              type="number"
              min={0}
              value={depthInput}
              onChange={(e) => {
                setDepthInput(e.target.value)
                if (parseCount(e.target.value) !== null) saveSetting(MAX_DEPTH_KEY, e.target.value)
              }}
              className="w-16"
              aria-label="最大层级"
            />
          </label>
          <label className="flex items-center gap-2 text-sm text-muted-foreground">
            分组颜色
            <Input
              type="number"
              min={0}
              value={toneInput}
              onChange={(e) => {
                setToneInput(e.target.value)
                if (parseCount(e.target.value) !== null) saveSetting(TONE_COUNT_KEY, e.target.value)
              }}
              className="w-16"
              aria-label="分组颜色"
            />
          </label>
        </div>

        {/* 只在真正首次加载（还没有任何数据）时显示这行文字——增删改之后的
            reload() 也会把 loading 短暂置回 true，这时候表格已经有上一次的
            数据在显示，再插一行"加载中…"只会造成一次没必要的跳动。 */}
        {perms.loading && !perms.data && <p className="text-sm text-muted-foreground">加载中…</p>}
        {perms.error && <p className="text-sm text-destructive">{perms.error}</p>}

        {perms.data && (
          <>
            {/* 列线：权限点多了以后靠横线很难把同一行的各列对上眼。 */}
            <Table className="table-fixed border [&_td]:border-r [&_th]:border-r [&_td:last-child]:border-r-0 [&_th:last-child]:border-r-0">
              {/* 操作列固定 136px（120px 按钮预留区 + 单元格左右各 8px padding），
                  勾选列 32px、序号列 44px，其余列按百分比分配——跟 Applications 表格
                  是同一套做法。 */}
              <colgroup>
                <col className="w-[32px]" />
                <col className="w-[44px]" />
                <col className="w-[24%]" />
                <col className="w-[16%]" />
                <col className="w-[10%]" />
                <col className="w-[12%]" />
                <col className="w-[14%]" />
                <col className="w-[136px]" />
              </colgroup>
              <TableHeader>
                <TableRow>
                  <TableHead className="p-0 px-2 text-center">
                    <Checkbox
                      checked={allShownSelected}
                      indeterminate={!allShownSelected && shownSelectedCount > 0}
                      onCheckedChange={(v) => toggleAll(v === true)}
                      aria-label="全选"
                    />
                  </TableHead>
                  <TableHead className="p-0 px-2 text-center">序号</TableHead>
                  <TableHead className="p-0 px-2">权限点</TableHead>
                  <TableHead className="p-0 px-2">名称</TableHead>
                  <TableHead className="p-0 px-2">状态</TableHead>
                  <TableHead className="p-0 px-2">已过渡</TableHead>
                  <TableHead className="p-0 px-2">最近上报</TableHead>
                  <TableHead className="p-0 px-2">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {all.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={8} className="text-center text-muted-foreground">
                      没有数据
                    </TableCell>
                  </TableRow>
                )}
                {all.length > 0 && shown.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={8} className="text-center text-muted-foreground">
                      没有匹配的权限点
                    </TableCell>
                  </TableRow>
                )}
                {layout.rows.map((row) =>
                  row.type === 'group' ? (
                    <GroupRow
                      key={`g:${row.id}`}
                      row={row}
                      tone={toneOf(row.label)}
                      open={kw !== '' || !collapsedGroups.has(row.id)}
                      selected={selected}
                      onToggle={() => toggleCollapse(row.id)}
                      onCheck={(checked) => toggleGroup(row.ids, checked)}
                    />
                  ) : (
                    <PermRow
                      key={row.point.id}
                      p={row.point}
                      depth={row.depth}
                      methodCh={methodWidths.get(row.groupId ?? '') ?? 0}
                      tone={toneOf(row.point.key)}
                      seq={layout.seq.get(row.point.id) ?? 0}
                      checked={selected.has(row.point.id)}
                      onToggle={(checked) => toggleOne(row.point.id, checked)}
                      onEdit={() => setEditing(row.point)}
                      onDelete={() => void askDelete(row.point)}
                    />
                  ),
                )}
              </TableBody>
            </Table>

            {selected.size > 0 && (
              <div className="flex items-center gap-2">
                <Button variant="destructive" onClick={() => void askBatchDelete()}>
                  批量删除
                </Button>
                <span className="text-sm text-muted-foreground">已选 {selected.size} 项</span>
              </div>
            )}
          </>
        )}
      </div>

      {/* AddDialog 自己决定什么时候关（全部成功才关，部分失败要留着让人改），
          这里的 onAdded 只管重新拉取列表，不管开关。 */}
      <AddDialog open={adding} onOpenChange={setAdding} appId={app.id} onAdded={() => perms.reload()} />

      {editing && (
        <EditDialog
          key={editing.id}
          point={editing}
          onOpenChange={(v) => !v && setEditing(null)}
          onSaved={() => {
            setEditing(null)
            perms.reload()
          }}
        />
      )}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="删除权限点"
        description={
          deleting
            ? deleting.holders.length === 0
              ? `删除「${deleting.p.key}」。当前没有角色持有它，删除不会让任何人掉权限。` +
                `如果接入方的代码里还有这个接口，下次上报会把它重新建出来（那时是一个没有任何授权的新条目）。`
              : `删除「${deleting.p.key}」会同时解除 ${deleting.holders.length} 个角色对它的授权：` +
                `${deleting.holders.join('、')}。这些角色下的用户会立刻失去调用这个接口的能力。` +
                `没有可撤销的"停用"中间态，删掉就是删掉；重新建出来的是一个没有任何授权的新条目。`
            : ''
        }
        confirmLabel="确认删除"
        onConfirm={() => {
          const d = deleting
          setDeleting(null)
          if (d) void remove(d.p)
        }}
      />

      <ConfirmDialog
        open={batchDeleting !== null}
        onOpenChange={(v) => !v && setBatchDeleting(null)}
        title="批量删除权限点"
        description={
          batchDeleting
            ? batchDeleting.holders.length === 0
              ? `删除这 ${batchDeleting.ids.length} 个权限点：${batchDeleting.keys.join('、')}。` +
                `当前没有角色持有它们，删除不会让任何人掉权限。如果接入方的代码里还有这些接口，` +
                `下次上报会把它们重新建出来（那时是没有任何授权的新条目）。`
              : `删除这 ${batchDeleting.ids.length} 个权限点：${batchDeleting.keys.join('、')}。` +
                `会同时解除 ${batchDeleting.holders.length} 个角色对它们的授权：${batchDeleting.holders.join('、')}。` +
                `这些角色下的用户会立刻失去调用这些接口的能力。` +
                `没有可撤销的"停用"中间态，删掉就是删掉；重新建出来的是没有任何授权的新条目。`
            : ''
        }
        confirmLabel="确认删除"
        onConfirm={() => {
          const d = batchDeleting
          setBatchDeleting(null)
          if (d) void removeBatch(d.ids)
        }}
      />
    </div>
  )
}

const addSchema = z.object({
  key: z.string().min(1, '请输入权限点标识'),
  name: z.string(),
})
type AddValues = z.infer<typeof addSchema>

/** 把批量文本框的一行拆成 key/name——以第一个空格分割，名称可选。 */
function parseBulkLine(line: string): { key: string; name: string } {
  const idx = line.indexOf(' ')
  if (idx === -1) return { key: line, name: '' }
  return { key: line.slice(0, idx), name: line.slice(idx + 1).trim() }
}

function splitBulkLines(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
}

function AddDialog({
  open,
  onOpenChange,
  appId,
  onAdded,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  appId: string
  onAdded: () => void
}) {
  const [text, setText] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function onSubmit() {
    const lines = splitBulkLines(text)
    if (lines.length === 0) {
      toast.error('请至少输入一行')
      return
    }
    setSubmitting(true)
    const failedLines: string[] = []
    let succeeded = 0
    // 逐条提交而不是 Promise.all：批量里某一行失败（比如标识重复）不该
    // 影响其它行的结果判断——并发提交的话没法把失败原因跟具体哪一行对上号。
    for (const line of lines) {
      const { key, name } = parseBulkLine(line)
      try {
        await api.post(`/applications/${appId}/permissions`, { key, name, kind: 'api' })
        succeeded++
      } catch (e) {
        failedLines.push(line)
        toast.error(`${key}：${errorMessage(e)}`)
      }
    }
    setSubmitting(false)
    if (succeeded > 0) onAdded()
    if (failedLines.length === 0) {
      toast.success(`已添加 ${succeeded} 个权限点`)
      setText('')
      onOpenChange(false)
    } else {
      // 只把失败的行留在框里，成功的不用再填一遍。
      setText(failedLines.join('\n'))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>添加权限</DialogTitle>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void onSubmit()
          }}
          className="space-y-4"
          noValidate
        >
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="perm-bulk">权限点</Label>
              <LabelHint>
                每行一个：方法:路由模式，后面可选跟一个名称，用空格分隔，例如
                GET:/orders/{'{id}'} 查看订单详情。路由要用路由模式，不是具体 URL——
                写 /orders/{'{id}'} 而不是 /orders/123，否则每个 id 都会变成一个独立的权限点。
              </LabelHint>
            </div>
            <textarea
              id="perm-bulk"
              rows={8}
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder={'GET:/user/login 登录\nGET:/user/register'}
              className="w-full rounded-md border bg-transparent px-3 py-2 font-mono text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={submitting}>
              添加
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function EditDialog({
  point,
  onOpenChange,
  onSaved,
}: {
  point: PermissionPoint
  onOpenChange: (v: boolean) => void
  onSaved: () => void
}) {
  const { register, handleSubmit, formState } = useForm<AddValues>({
    resolver: zodResolver(addSchema),
    defaultValues: { key: point.key, name: point.name },
  })

  async function onSubmit(v: AddValues) {
    try {
      await api.patch(`/permissions/${point.id}`, v)
      toast.success('已保存')
      onSaved()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>编辑权限点</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit, toastFormErrors)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="edit-perm-key">标识</Label>
              <LabelHint>
                标识可以改（比如打错了一个字母）：已有的授权按内部 id 关联，会自动跟过来，
                改完立刻推送给接入方。但如果接入方代码里的路由还是旧值，下次上报会把旧的重新建出来。
              </LabelHint>
            </div>
            <Input id="edit-perm-key" className="font-mono" {...register('key')} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-perm-name">名称</Label>
            <Input id="edit-perm-name" {...register('name')} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={formState.isSubmitting}>
              保存
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

const MAX_DEPTH_KEY = 'fp.permissions.maxGroupDepth'
const DEFAULT_MAX_DEPTH = 2
const TONE_COUNT_KEY = 'fp.permissions.groupColorCount'
const DEFAULT_TONE_COUNT = 2

/** 0 是有效值：最大层级 0 = 不分组，分组颜色 0 = 不分色。 */
function parseCount(v: string): number | null {
  const n = Number(v)
  return Number.isInteger(n) && n >= 0 ? n : null
}

// 隐私模式等场景下 localStorage 会抛异常，读写都兜住，退回默认值即可。
function loadSetting(key: string, fallback: number): string {
  try {
    const v = localStorage.getItem(key)
    if (v !== null && parseCount(v) !== null) return v
  } catch {
    // ignore
  }
  return String(fallback)
}

function saveSetting(key: string, v: string) {
  try {
    localStorage.setItem(key, v)
  } catch {
    // ignore
  }
}

/** 每层缩进固定一小段，不画网格线。 */
const INDENT_PX = 12

/**
 * 按路径前缀分色：前缀相同的行同色，不同前缀轮流取色，颜色用完循环。只给文字
 * 上色、不铺底色，亮色/暗色主题各用一套明度合适的色。
 */
const TONE_CLASSES = [
  'text-blue-600 dark:text-blue-400',
  'text-emerald-600 dark:text-emerald-400',
  'text-amber-600 dark:text-amber-400',
  'text-fuchsia-600 dark:text-fuchsia-400',
]
const toneClass = (tone?: number) => (tone === undefined ? '' : TONE_CLASSES[tone % TONE_CLASSES.length])

function GroupRow({
  row,
  tone,
  open,
  selected,
  onToggle,
  onCheck,
}: {
  row: Extract<TreeRow<PermissionPoint>, { type: 'group' }>
  tone?: number
  open: boolean
  selected: Set<string>
  onToggle: () => void
  onCheck: (checked: boolean) => void
}) {
  const n = row.ids.filter((id) => selected.has(id)).length
  const full = n === row.ids.length
  return (
    <TableRow className="h-12 cursor-pointer select-none font-semibold" onClick={onToggle}>
      <TableCell className="p-0 px-2 text-center" onClick={(e) => e.stopPropagation()}>
        <Checkbox
          checked={full}
          indeterminate={!full && n > 0}
          onCheckedChange={(v) => onCheck(v === true)}
          aria-label={`选中分组 ${row.label}`}
        />
      </TableCell>
      <TableCell className="p-0 px-2 text-center" />
      <TableCell colSpan={6} className="p-0 px-2">
        <div className="flex items-center" style={{ paddingLeft: row.depth * INDENT_PX }}>
          <span className="w-4 shrink-0 text-xs text-muted-foreground">{open ? '▼' : '▶'}</span>
          <span className={`font-mono text-xs ${toneClass(tone)}`} data-group={row.label}>
            {row.label}
          </span>
          <span className="ml-2 text-xs font-normal text-muted-foreground">{row.count} 个</span>
          {row.staleCount > 0 && (
            <Badge variant="destructive" className="ml-2">
              {row.staleCount} 过渡中
            </Badge>
          )}
        </div>
      </TableCell>
    </TableRow>
  )
}

function PermRow({
  p,
  depth,
  methodCh,
  tone,
  seq,
  checked,
  onToggle,
  onEdit,
  onDelete,
}: {
  p: PermissionPoint
  depth: number
  methodCh: number
  tone?: number
  seq: number
  checked: boolean
  onToggle: (checked: boolean) => void
  onEdit: () => void
  onDelete: () => void
}) {
  return (
    <TableRow className="h-12 cursor-pointer" onClick={() => onToggle(!checked)}>
      <TableCell className="p-0 px-2 text-center" onClick={(e) => e.stopPropagation()}>
        <Checkbox checked={checked} onCheckedChange={(v) => onToggle(v === true)} aria-label={`选中 ${p.key}`} />
      </TableCell>
      <TableCell className="p-0 px-2 text-center text-muted-foreground">{seq}</TableCell>
      <TableCell className="whitespace-normal break-all p-0 px-2 font-mono text-xs">
        {/* 留出与分组 ▶ 同宽的空位，让权限点与同级分组名左对齐 */}
        <div className="flex" style={{ paddingLeft: depth * INDENT_PX }}>
          <span className="w-4 shrink-0" />
          {/* 方法名列宽按同组最长的实时算（最长 + 1 个空格），同组各行的路径从同一列开始 */}
          <span data-key={p.key} className={toneClass(tone)}>
            <span className="inline-block" style={{ minWidth: `${methodCh}ch` }}>
              {p.key.slice(0, p.key.indexOf(':') + 1)}
            </span>
            {p.key.slice(p.key.indexOf(':') + 1)}
          </span>
        </div>
      </TableCell>
      <TableCell className="whitespace-normal break-words p-0 px-2 text-muted-foreground">{p.name || '-'}</TableCell>
      <TableCell className="p-0 px-2">
        <Badge variant={p.status === 'normal' ? 'default' : p.status === 'stale' ? 'destructive' : 'secondary'}>
          {permissionStatusLabels[p.status] ?? p.status}
        </Badge>
      </TableCell>
      <TableCell className="whitespace-normal p-0 px-2 text-muted-foreground">
        {p.status === 'stale' ? formatDuration(p.staleForMs) : '-'}
      </TableCell>
      <TableCell className="whitespace-normal p-0 px-2 text-muted-foreground">
        {p.source === 'manual' ? '（手动添加）' : formatTime(p.lastSeenAt)}
      </TableCell>
      <TableCell className="p-0 px-2">
        <div className="flex w-[120px] gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={(e) => {
              e.stopPropagation()
              onEdit()
            }}
          >
            编辑
          </Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={(e) => {
              e.stopPropagation()
              onDelete()
            }}
          >
            删除
          </Button>
        </div>
      </TableCell>
    </TableRow>
  )
}
