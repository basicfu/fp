import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ApiError, api } from '@/lib/api'
import { formatTime } from '@/lib/format'
import { useResource, errorMessage } from '@/lib/useResource'
import type { ConfigField, ConfigPartition, ConfigSnapshot, ConfigVersion, SaveConfigResponse } from '@/lib/types'

const partitions: ConfigPartition[] = ['DEFAULT', 'WEB']

/**
 * valuesEqual 判断两个配置值是否"相同"，array/object 要按结构比较：两次
 * 从 JSON 解出来的对象即使内容一样也是不同的引用，`===` 恒为 false；纯
 * JSON.stringify 比较又会被 key 顺序碰巧不同坑到——虽然后端
 * CoerceConfigValue 特意保留原始字节顺序不重新序列化，就是为了让这层
 * "字节级 diff"稳定（见 internal/domain/config.go 的注释），但前端这层
 * 不应该依赖那份后端承诺，自己按结构递归比较更稳妥。
 */
function valuesEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (typeof a !== 'object' || typeof b !== 'object' || a === null || b === null) return false
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false
    return a.every((v, i) => valuesEqual(v, b[i]))
  }
  const ao = a as Record<string, unknown>
  const bo = b as Record<string, unknown>
  const keysA = Object.keys(ao)
  if (keysA.length !== Object.keys(bo).length) return false
  return keysA.every((k) => Object.prototype.hasOwnProperty.call(bo, k) && valuesEqual(ao[k], bo[k]))
}

/**
 * diffFieldKeys 比较相邻两版的 fields，返回发生变化的 key（按字母序）。
 * 后端不存"这一版改了什么"，只能靠前端把两份完整快照拉全了自己比。
 *
 * "变化"三选一即算：key 只在一边出现（新增/删除）；或者两边都有但
 * type/desc/value 任意一项不同。没变的 key 一律不出现在结果里——这是
 * 辨别力所在：一个偷懒把整份 fields 都当"改动"的实现在这里就会露馅。
 */
function diffFieldKeys(curr: Record<string, ConfigField>, prev: Record<string, ConfigField>): string[] {
  const keys = new Set([...Object.keys(curr), ...Object.keys(prev)])
  const changed: string[] = []
  for (const key of keys) {
    const c = curr[key]
    const p = prev[key]
    if (!c || !p || c.type !== p.type || c.desc !== p.desc || !valuesEqual(c.value, p.value)) {
      changed.push(key)
    }
  }
  return changed.sort()
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
  | { kind: 'diff'; changed: string[] }

/** 回滚会让哪些 key 变成未配置。known=false 表示数据不全，答不出来。 */
type UnsetKeysResult = { known: true; keys: string[] } | { known: false }

export default function ConfigVersions() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const [partition, setPartition] = useState<ConfigPartition>('DEFAULT')

  const versions = useResource(
    () => api.get<ConfigVersion[]>(`/applications/${id}/config/versions?type=${partition}`),
    [id, partition],
  )

  // snapshots 缓存每一版、以及每一版"前一版"的完整快照（seq -> 状态），
  // 供 diff 与回滚提示共用——两者都只是这份数据的不同读法，不必分别拉取。
  const [snapshots, setSnapshots] = useState<Record<number, SnapshotState>>({})
  // 整批快照是不是都已经有结果（不管 ok/missing/error）。[回滚到 vN]
  // 按钮在这变成 true 之前必须保持 disabled——见下面 effect 里的注释。
  const [diffsReady, setDiffsReady] = useState(false)

  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)
  const [rollbackPush, setRollbackPush] = useState(true)
  const [rollbackSaving, setRollbackSaving] = useState(false)

  // 版本列表到手后，为每一版及其"前一版"分别拉整份快照。
  //
  // alive 守卫：分区切换会连续触发这个 effect，防止旧分区的响应在切区
  // 之后才回来，把 snapshots 弄成两个分区的数据混在一起——与 useResource
  // 里的 alive 守卫同一个理由。
  useEffect(() => {
    setSnapshots({})
    setDiffsReady(false)
    const list = versions.data
    if (!list || list.length === 0) {
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
    return { kind: 'diff', changed: diffFieldKeys(curr.snapshot.fields, prev.snapshot.fields) }
  }

  /** 当前（最新）版本的快照——versions 降序返回，第一项就是当前版本。 */
  function currentSnapshot(): ConfigSnapshot | null {
    const latestSeq = versions.data?.[0]?.seq
    if (latestSeq === undefined) return null
    const s = snapshots[latestSeq]
    return s?.status === 'ok' ? s.snapshot : null
  }

  /**
   * 回滚到 targetSeq 后，"当前有、目标版本没有"的 key——它们会变成未配置。
   * 判据是 key 存不存在，不是值是否为 null：回滚是整版替换，当前版本
   * 独有的 key 在目标版本里根本不存在这一条目，效果与被删除等价（运行中
   * 的实例保持旧值并报错，新起的实例缺值起不来），必须在回滚前说清楚。
   */
  function willUnsetKeys(targetSeq: number): UnsetKeysResult {
    const curr = currentSnapshot()
    const target = snapshots[targetSeq]
    if (!curr || target?.status !== 'ok') return { known: false }
    const keys = Object.keys(curr.fields)
      .filter((k) => !(k in target.snapshot.fields))
      .sort()
    return { known: true, keys }
  }

  function openRollback(seq: number) {
    setRollbackTarget(seq)
    setRollbackPush(true)
  }

  async function confirmRollback() {
    if (rollbackTarget === null) return
    setRollbackSaving(true)
    try {
      const res = await api.post<SaveConfigResponse>(`/applications/${id}/config/rollback`, {
        type: partition,
        seq: rollbackTarget,
        push: rollbackPush,
      })
      toast.success(
        rollbackPush ? `已回滚并推送，当前版本 seq=${res.seq}` : `已回滚，seq=${res.seq}，实例重启后生效`,
      )
      navigate(`/applications/${id}/config`)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setRollbackSaving(false)
    }
  }

  const unsetResult: UnsetKeysResult = rollbackTarget !== null ? willUnsetKeys(rollbackTarget) : { known: true, keys: [] }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <div>
          <Link
            to={`/applications/${id}/config`}
            className="text-sm text-muted-foreground underline-offset-4 hover:underline"
          >
            ← 返回配置中心
          </Link>
          <h1 className="text-xl font-semibold">版本历史</h1>
        </div>
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

      {versions.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {versions.error && <p className="text-sm text-destructive">{versions.error}</p>}

      {!versions.loading && !versions.error && (
        <div className="space-y-3">
          {versions.data?.length === 0 && <p className="text-sm text-muted-foreground">这个分区还没有任何版本。</p>}
          {versions.data?.map((v) => {
            const diff = rowDiff(v.seq)
            return (
              <div key={v.seq} data-testid={`version-${v.seq}`} className="space-y-2 rounded-md border p-3">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-sm font-medium">v{v.seq}</span>
                    <span className="text-xs text-muted-foreground">{formatTime(v.createdAt)}</span>
                    {diff.kind === 'initial' && <Badge variant="secondary">初始版本</Badge>}
                  </div>
                  <Button variant="outline" size="sm" onClick={() => openRollback(v.seq)} disabled={!diffsReady}>
                    回滚到 v{v.seq}
                  </Button>
                </div>

                {diff.kind === 'loading' && <p className="text-sm text-muted-foreground">正在比对上一版本…</p>}
                {diff.kind === 'error' && <p className="text-sm text-destructive">{diff.message}</p>}
                {diff.kind === 'diff' && (
                  <>
                    <ul data-testid={`changed-${v.seq}`} className="list-disc space-y-0.5 pl-5 text-sm">
                      {diff.changed.map((k) => (
                        <li key={k} className="font-mono">
                          {k}
                        </li>
                      ))}
                    </ul>
                    {diff.changed.length === 0 && (
                      <p className="text-sm text-muted-foreground">与上一版相比没有变化。</p>
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
            {!unsetResult.known && (
              <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
                无法确认回滚后是否有配置项会变成未配置（快照没能全部取到），请谨慎操作。
              </p>
            )}
            {unsetResult.known && unsetResult.keys.length > 0 && (
              <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
                回滚后以下配置项将变成未配置：{unsetResult.keys.join('、')}
              </p>
            )}

            <div className="flex flex-wrap items-center gap-4">
              <span className="text-sm font-medium">生效方式</span>
              <div className="flex items-center gap-2">
                <input
                  type="radio"
                  id="cfg-rollback-push-immediate"
                  name="cfg-rollback-push-mode"
                  className="size-4"
                  checked={rollbackPush}
                  onChange={() => setRollbackPush(true)}
                />
                <Label htmlFor="cfg-rollback-push-immediate" className="font-normal">
                  立即推送（默认）
                </Label>
              </div>
              <div className="flex items-center gap-2">
                <input
                  type="radio"
                  id="cfg-rollback-push-lazy"
                  name="cfg-rollback-push-mode"
                  className="size-4"
                  checked={!rollbackPush}
                  onChange={() => setRollbackPush(false)}
                />
                <Label htmlFor="cfg-rollback-push-lazy" className="font-normal">
                  仅落库，实例重启后生效
                </Label>
              </div>
            </div>
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
