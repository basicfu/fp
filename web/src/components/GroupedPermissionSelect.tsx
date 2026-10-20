import { useMemo, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { layoutPermissions, methodColumnWidths } from '@/lib/permissionTree'
import { cn } from '@/lib/utils'

export interface SelectablePoint {
  id: string
  key: string
  name: string
}

const INDENT_PX = 12
const MAX_TAGS = 3

const labelOf = (p: SelectablePoint) => (p.name ? `${p.key}（${p.name}）` : p.key)

/**
 * 角色授权用的权限点下拉：列表按路径前缀分组折叠，勾选即时回调。
 *
 * 每次变更把完整的新选中集合交给 onChange，由调用方自己算 diff 提交。组件内部
 * 先乐观更新，等调用方重新拉取后的 selected 回来再对齐——否则提交到刷新之间
 * 勾选框会弹回旧状态。
 */
export function GroupedPermissionSelect({
  points,
  selected,
  onChange,
  disabled,
}: {
  points: SelectablePoint[]
  selected: string[]
  onChange: (next: string[]) => void
  disabled?: boolean
}) {
  const [local, setLocal] = useState(selected)
  const selectedKey = selected.join(',')
  const [syncedKey, setSyncedKey] = useState(selectedKey)
  // 渲染期同步而不是 effect：外部选中集合变了就以它为准，省一次多余渲染。
  if (syncedKey !== selectedKey) {
    setSyncedKey(selectedKey)
    setLocal(selected)
  }

  const [open, setOpen] = useState(false)
  const [keyword, setKeyword] = useState('')
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set())

  const kw = keyword.trim().toLowerCase()
  const chosen = useMemo(() => new Set(local), [local])
  const shown = useMemo(
    () => (kw ? points.filter((p) => p.key.toLowerCase().includes(kw) || p.name.toLowerCase().includes(kw)) : points),
    [points, kw],
  )
  // 默认全部展开，只记用户折叠了哪些；搜索时一律展开，清空关键字后恢复。
  const layout = useMemo(
    () => layoutPermissions(shown, (id) => kw !== '' || !collapsedGroups.has(id)),
    [shown, kw, collapsedGroups],
  )

  const methodWidths = useMemo(() => methodColumnWidths(layout.rows), [layout.rows])

  function commit(ids: string[], on: boolean) {
    if (disabled) return
    const next = new Set(chosen)
    ids.forEach((id) => (on ? next.add(id) : next.delete(id)))
    const arr = points.filter((p) => next.has(p.id)).map((p) => p.id)
    setLocal(arr)
    onChange(arr)
  }

  function toggleGroupOpen(id: string) {
    setCollapsedGroups((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const shownIds = shown.map((p) => p.id)
  const shownCount = shownIds.filter((id) => chosen.has(id)).length
  const allShown = shownIds.length > 0 && shownCount === shownIds.length
  const tags = points.filter((p) => chosen.has(p.id))

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          role="combobox"
          aria-expanded={open}
          disabled={disabled}
          className="flex min-h-9 w-full flex-wrap items-center gap-1.5 rounded-md border bg-secondary/40 px-2 py-1 text-left text-sm hover:bg-secondary/60 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {tags.length === 0 && <span className="text-muted-foreground">搜索</span>}
          {tags.slice(0, MAX_TAGS).map((p) => (
            <Badge key={p.id} variant="secondary" className="max-w-full font-mono text-xs font-normal">
              <span className="truncate">{labelOf(p)}</span>
            </Badge>
          ))}
          {tags.length > MAX_TAGS && <Badge variant="outline">+{tags.length - MAX_TAGS} 项</Badge>}
          <span className="ml-auto text-muted-foreground">▾</span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] min-w-[360px] p-0">
        <Input
          autoFocus
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          placeholder="搜索"
          className="rounded-b-none border-0 border-b shadow-none focus-visible:ring-0"
        />
        <div className="max-h-[40vh] overflow-y-auto py-1">
          {shown.length === 0 ? (
            <p className="p-4 text-center text-sm text-muted-foreground">没有匹配的权限点</p>
          ) : (
            <>
              <Row onClick={() => commit(shownIds, !allShown)}>
                <Checkbox
                  checked={allShown}
                  indeterminate={!allShown && shownCount > 0}
                  onClick={(e) => e.stopPropagation()}
                  onCheckedChange={(v) => commit(shownIds, v === true)}
                  aria-label="选中全部"
                />
                <span>选中全部</span>
                <span className="ml-auto text-xs text-muted-foreground">
                  {shownCount} / {shownIds.length}
                </span>
              </Row>
              {layout.rows.map((row) => {
                const pad = { paddingLeft: 12 + row.depth * INDENT_PX }
                if (row.type === 'group') {
                  const n = row.ids.filter((id) => chosen.has(id)).length
                  const full = n === row.ids.length
                  const isOpen = kw !== '' || !collapsedGroups.has(row.id)
                  return (
                    <Row key={`g:${row.id}`} style={pad} className="font-semibold" onClick={() => toggleGroupOpen(row.id)}>
                      <span className="w-4 shrink-0 text-xs text-muted-foreground">{isOpen ? '▼' : '▶'}</span>
                      <Checkbox
                        checked={full}
                        indeterminate={!full && n > 0}
                        onClick={(e) => e.stopPropagation()}
                        onCheckedChange={(v) => commit(row.ids, v === true)}
                        aria-label={`选中分组 ${row.label}`}
                      />
                      <span className="font-mono text-xs">{row.label}</span>
                      <span className="ml-auto text-xs font-normal text-muted-foreground">
                        {n} / {row.ids.length}
                      </span>
                    </Row>
                  )
                }
                const p = row.point
                const on = chosen.has(p.id)
                return (
                  <Row key={p.id} style={pad} data-option={labelOf(p)} onClick={() => commit([p.id], !on)}>
                    <span className="w-4 shrink-0" />
                    <Checkbox
                      checked={on}
                      onClick={(e) => e.stopPropagation()}
                      onCheckedChange={(v) => commit([p.id], v === true)}
                      aria-label={`选中 ${p.key}`}
                    />
                    {/* 方法名列宽按同组最长的实时算（最长 + 1 个空格），同组各行的路径从同一列开始 */}
                    <span className="font-mono text-xs" data-key={p.key}>
                      <span className="inline-block" style={{ minWidth: `${methodWidths.get(row.groupId ?? '') ?? 0}ch` }}>
                        {p.key.slice(0, p.key.indexOf(':') + 1)}
                      </span>
                      {p.key.slice(p.key.indexOf(':') + 1)}
                    </span>
                    {p.name && <span className="text-xs text-muted-foreground">{p.name}</span>}
                  </Row>
                )
              })}
            </>
          )}
        </div>
        <div className="flex items-center justify-between border-t px-3 py-1.5">
          <Button variant="ghost" size="sm" disabled={local.length === 0} onClick={() => commit(points.map((p) => p.id), false)}>
            清空
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>
            关闭
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  )
}

function Row({ className, children, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      role="option"
      className={cn('flex h-8 cursor-pointer select-none items-center gap-2 pl-3 pr-3 text-sm hover:bg-muted', className)}
      {...props}
    >
      {children}
    </div>
  )
}
