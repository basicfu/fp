import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useCurrentApp } from '@/lib/current-app'
import { toast } from 'sonner'
import { load as loadYAML } from 'js-yaml'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ApiError, api } from '@/lib/api'
import { formatTime } from '@/lib/format'
import { useResource, errorMessage } from '@/lib/useResource'
import { useConfigTypes } from '@/lib/useConfigTypes'
import { cn } from '@/lib/utils'
import { DEFAULT_PARTITION, type ConfigPartition, type ConfigSnapshot, type ConfigVersion, type SaveConfigResponse } from '@/lib/types'

/**
 * parseYAMLObject 把版本快照的 YAML 原文解析成一个扁平化前的对象，解析
 * 失败或顶层不是映射时按空对象处理——展示层的兜底：保存时后端早就拒绝过
 * 不合法的 YAML，历史记录里理论上不会出现解析不出来的版本，这里只是
 * 防御，不该让 diff 页面因为一份意外数据而崩溃。
 */
function parseYAMLObject(text: string): Record<string, unknown> {
  if (text.trim() === '') return {}
  try {
    const v: unknown = loadYAML(text)
    if (v && typeof v === 'object' && !Array.isArray(v)) return v as Record<string, unknown>
  } catch {
    // 忽略，走下面的空对象兜底。
  }
  return {}
}

/**
 * flatten 把嵌套对象拍平成 "a.b.c" -> 叶子值 的映射，数组与标量都当叶子
 * （不逐元素比较数组内部——那是"整个数组变了"还是"数组里改了一项"这种
 * 更细的语义，对配置项这种量级没必要，改动清单反而会因为数组下标错位
 * 显得琐碎）。
 */
function flatten(obj: unknown, prefix = ''): Map<string, unknown> {
  const out = new Map<string, unknown>()
  const isPlainObject = obj !== null && typeof obj === 'object' && !Array.isArray(obj)
  if (!isPlainObject) {
    if (prefix) out.set(prefix, obj)
    return out
  }
  const entries = Object.entries(obj as Record<string, unknown>)
  if (entries.length === 0) {
    if (prefix) out.set(prefix, obj)
    return out
  }
  for (const [k, v] of entries) {
    const path = prefix ? `${prefix}.${k}` : k
    for (const [p, val] of flatten(v, path)) out.set(p, val)
  }
  return out
}

function valuesEqual(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b)
}

/** formatDiffValue 是改动清单里一行的值展示：字符串不加引号（更好读），其余按 JSON 字面量。 */
function formatDiffValue(v: unknown): string {
  if (v === undefined) return ''
  if (typeof v === 'string') return v
  return JSON.stringify(v)
}

interface DiffLine {
  kind: 'added' | 'removed'
  path: string
  value: unknown
}

/** diffValues 比较两份解析后的配置对象，返回 +/- 形式的改动清单，按 path 排序、删除排在同一 key 的新增前面。 */
function diffValues(curr: Record<string, unknown>, prev: Record<string, unknown>): DiffLine[] {
  const currFlat = flatten(curr)
  const prevFlat = flatten(prev)
  const keys = [...new Set([...currFlat.keys(), ...prevFlat.keys()])].sort()
  const lines: DiffLine[] = []
  for (const k of keys) {
    const hasCurr = currFlat.has(k)
    const hasPrev = prevFlat.has(k)
    const same = hasCurr && hasPrev && valuesEqual(currFlat.get(k), prevFlat.get(k))
    if (same) continue
    if (hasPrev) lines.push({ kind: 'removed', path: k, value: prevFlat.get(k) })
    if (hasCurr) lines.push({ kind: 'added', path: k, value: currFlat.get(k) })
  }
  return lines
}

/**
 * 单个版本号的快照拉取状态。
 *
 * missing 专指"服务端确认这个版本号不存在"（404/CodeConfigVersionNotFound）
 * ——版本号从未用过（seq=1 的前一版 seq=0），或者是 ConfigService.Save 里
 * 超过 ConfigMaxVersions 之后从最老开始修剪掉的，两种情况前端都无法也
 * 不需要区分，一律视为"没有更老的版本可比"。error 是其余的失败（网络、
 * 5xx……），必须和 missing 分开：拿它当 missing 处理会把"不知道"悄悄
 * 说成"确定没有"，那正是回滚提示这条安全网要防的事。
 */
type SnapshotState =
  | { status: 'loading' }
  | { status: 'missing' }
  | { status: 'error'; message: string }
  | { status: 'ok'; snapshot: ConfigSnapshot }

type RowDiff =
  | { kind: 'loading' }
  | { kind: 'initial' }
  | { kind: 'error'; message: string }
  | { kind: 'diff'; lines: DiffLine[] }

/** 回滚会让哪些 key 消失（当前有、目标版本没有）。known=false 表示数据不全，答不出来。 */
type RemovedKeysResult = { known: true; keys: string[] } | { known: false }

export default function ConfigVersions() {
  const { currentApp, apps, loading, error } = useCurrentApp()
  const id = currentApp?.id ?? ''
  const navigate = useNavigate()
  const [partition, setPartition] = useState<ConfigPartition>(DEFAULT_PARTITION)
  const types = useConfigTypes(id)

  const versions = useResource(
    () =>
      id
        ? api.get<ConfigVersion[]>(`/applications/${id}/config/versions?type=${partition}`)
        : Promise.resolve<ConfigVersion[]>([]),
    [id, partition],
  )

  // snapshots 缓存每一版、以及每一版"前一版"的完整快照（seq -> 状态），
  // 供 diff 与回滚提示共用——两者都只是这份数据的不同读法，不必分别拉取。
  const [snapshots, setSnapshots] = useState<Record<number, SnapshotState>>({})
  // 整批快照是不是都已经有结果（不管 ok/missing/error）。[回滚到 vN]
  // 按钮在这变成 true 之前必须保持 disabled——见下面 effect 里的注释。
  const [diffsReady, setDiffsReady] = useState(false)

  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)
  const [rollbackSaving, setRollbackSaving] = useState(false)

  // 版本列表到手后，为每一版及其"前一版"分别拉整份快照。
  //
  // alive 守卫：分区切换会连续触发这个 effect，防止旧分区的响应在切区
  // 之后才回来，把 snapshots 弄成两个分区的数据混在一起——与 useResource
  // 里的 alive 守卫同一个理由。
  useEffect(() => {
    const list = versions.data
    if (!list) return
    setSnapshots({})
    setDiffsReady(false)
    if (list.length === 0) {
      setDiffsReady(true)
      return
    }
    let alive = true

    async function load(seq: number): Promise<SnapshotState> {
      // seq 从 1 开始（ConfigService.Save 用 COALESCE(MAX(seq),0)+1），
      // <1 注定查不到，不必真的发一次请求。
      if (seq < 1) return { status: 'missing' }
      try {
        const snapshot = await api.get<ConfigSnapshot>(
          `/applications/${id}/config/versions/${seq}?type=${partition}`,
        )
        return { status: 'ok', snapshot }
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return { status: 'missing' }
        return { status: 'error', message: errorMessage(e) }
      }
    }

    void (async () => {
      const need = new Set<number>()
      for (const v of list) {
        need.add(v.seq)
        need.add(v.seq - 1)
      }
      const pairs = await Promise.all(Array.from(need).map(async (seq) => [seq, await load(seq)] as const))
      if (!alive) return
      const next: Record<number, SnapshotState> = {}
      for (const [seq, state] of pairs) next[seq] = state
      setSnapshots(next)
      setDiffsReady(true)
    })()

    return () => {
      alive = false
    }
  }, [id, partition, versions.data])

  // 切分区时如果正开着回滚弹窗，里面引用的 seq 是上一个分区的，必须关掉。
  useEffect(() => {
    setRollbackTarget(null)
  }, [partition])

  function rowDiff(seq: number): RowDiff {
    const curr: SnapshotState = snapshots[seq] ?? { status: 'loading' }
    if (curr.status === 'loading') return { kind: 'loading' }
    if (curr.status === 'error') return { kind: 'error', message: curr.message }
    // curr.status === 'missing' 理论上不会发生在"自己"这一版身上——它来自
    // versions 列表，服务端既然报了这个 seq，查它自己就不应该 404。稳妥
    // 起见按初始版本处理，不让一个理论上不该出现的状态直接崩渲染。
    if (curr.status === 'missing') return { kind: 'initial' }

    const prev: SnapshotState = snapshots[seq - 1] ?? { status: 'loading' }
    if (prev.status === 'loading') return { kind: 'loading' }
    if (prev.status === 'missing') return { kind: 'initial' }
    if (prev.status === 'error') return { kind: 'error', message: prev.message }
    return {
      kind: 'diff',
      lines: diffValues(parseYAMLObject(curr.snapshot.value), parseYAMLObject(prev.snapshot.value)),
    }
  }

  /** 当前（最新）版本的快照——versions 降序返回，第一项就是当前版本。 */
  function currentSnapshot(): ConfigSnapshot | null {
    const latestSeq = versions.data?.[0]?.seq
    if (latestSeq === undefined) return null
    const s = snapshots[latestSeq]
    return s?.status === 'ok' ? s.snapshot : null
  }

  /**
   * 回滚到 targetSeq 后，"当前存在、目标版本里不存在"的 key——它们会
   * 消失。YAML 自由编辑模型下没有"已建未填"这个中间态了，判据只有一条：
   * 这个 key（拍平后的路径）当前有、目标版本没有。
   *
   * 运行中的实例保持旧值并报错、新起的实例缺值起不来，必须在回滚前说
   * 清楚。
   */
  function willRemoveKeys(targetSeq: number): RemovedKeysResult {
    const curr = currentSnapshot()
    const target = snapshots[targetSeq]
    if (!curr || target?.status !== 'ok') return { known: false }
    const currFlat = flatten(parseYAMLObject(curr.value))
    const targetFlat = flatten(parseYAMLObject(target.snapshot.value))
    const keys = [...currFlat.keys()].filter((k) => !targetFlat.has(k)).sort()
    return { known: true, keys }
  }

  function openRollback(seq: number) {
    setRollbackTarget(seq)
  }

  async function confirmRollback() {
    if (rollbackTarget === null) return
    // 目标版本与当前值完全一致时，后端不会为"什么都没变"这件事凭空生出
    // 一个新版本号——用回滚前记下的最新 seq 和响应里的 seq 一比，就知道
    // 后端是不是真的落了新版本，据此换一句不误导人的提示。
    const latestSeqBefore = versions.data?.[0]?.seq
    setRollbackSaving(true)
    try {
      const res = await api.post<SaveConfigResponse>(`/applications/${id}/config/rollback`, {
        type: partition,
        seq: rollbackTarget,
        push: true,
      })
      toast.success(
        res.seq === latestSeqBefore ? '这一版与当前内容完全一致，没有产生新版本' : `已回滚并推送，当前版本 seq=${res.seq}`,
      )
      navigate('/config')
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setRollbackSaving(false)
    }
  }

  const removedResult: RemovedKeysResult =
    rollbackTarget !== null ? willRemoveKeys(rollbackTarget) : { known: true, keys: [] }

  if (error) return <p className="text-sm text-destructive">{error}</p>
  if (loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (apps.length === 0) {
    return <p className="text-sm text-muted-foreground">还没有应用，请先在「应用列表」创建一个。</p>
  }
  if (!currentApp) return null

  return (
    <div className="space-y-6">
      <Link to="/config" className="text-sm text-muted-foreground underline-offset-4 hover:underline">
        ← 返回配置中心
      </Link>

      <Tabs value={partition} onValueChange={(v) => setPartition(v as ConfigPartition)}>
        <TabsList>
          {types.types.map((p) => (
            <TabsTrigger key={p} value={p}>
              {p}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      {versions.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {versions.error && <p className="text-sm text-destructive">{versions.error}</p>}

      {!versions.loading && !versions.error && (
        <div className="space-y-3">
          {versions.data?.length === 0 && <p className="text-sm text-muted-foreground">这个分区还没有任何版本。</p>}
          {versions.data?.map((v, idx) => {
            const diff = rowDiff(v.seq)
            // 第一项（idx===0）是当前版本——回滚到自己没有意义，这一行不
            // 给"回滚到 vN"按钮。
            const isCurrent = idx === 0
            return (
              <div key={v.seq} data-testid={`version-${v.seq}`} className="space-y-2 rounded-md border p-3">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-sm font-medium">v{v.seq}</span>
                    <span className="text-xs text-muted-foreground">{formatTime(v.createdAt)}</span>
                    {diff.kind === 'initial' && <Badge variant="secondary">初始版本</Badge>}
                    {isCurrent && <Badge variant="secondary">当前版本</Badge>}
                  </div>
                  {!isCurrent && (
                    <Button variant="outline" size="sm" onClick={() => openRollback(v.seq)} disabled={!diffsReady}>
                      回滚到 v{v.seq}
                    </Button>
                  )}
                </div>

                {diff.kind === 'loading' && <p className="text-sm text-muted-foreground">正在比对上一版本…</p>}
                {diff.kind === 'error' && <p className="text-sm text-destructive">{diff.message}</p>}
                {diff.kind === 'diff' && (
                  <>
                    {diff.lines.length === 0 && (
                      <p className="text-sm text-muted-foreground">与上一版相比没有变化。</p>
                    )}
                    {diff.lines.length > 0 && (
                      <pre
                        data-testid={`changed-${v.seq}`}
                        className="overflow-x-auto rounded-md bg-muted/40 p-2 font-mono text-xs leading-relaxed"
                      >
                        {diff.lines.map((l, i) => (
                          <div
                            key={i}
                            className={cn(l.kind === 'added' ? 'text-emerald-600 dark:text-emerald-400' : 'text-destructive')}
                          >
                            {l.kind === 'added' ? '+ ' : '- '}
                            {l.path}: {formatDiffValue(l.value)}
                          </div>
                        ))}
                      </pre>
                    )}
                  </>
                )}
              </div>
            )
          })}
        </div>
      )}

      <Dialog open={rollbackTarget !== null} onOpenChange={(v) => !v && setRollbackTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>回滚到 v{rollbackTarget}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            {!removedResult.known && (
              <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
                无法确认回滚后是否有配置项会被删除（快照没能全部取到），请谨慎操作。
              </p>
            )}
            {removedResult.known && removedResult.keys.length > 0 && (
              <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
                回滚后以下配置项将被删除：{removedResult.keys.join('、')}
              </p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRollbackTarget(null)}>
              取消
            </Button>
            <Button variant="destructive" onClick={() => void confirmRollback()} disabled={rollbackSaving}>
              确认回滚
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
