/**
 * 权限点按路径分组的纯函数。key 形如 `GET:/api/v1/products/{id}`，冒号后的
 * 路径逐段建前缀树；方法:路径始终作为一个整串展示，不参与分组。
 */

export interface TreePoint {
  id: string
  key: string
}

interface Node<T> {
  seg: string
  path: string
  kids: Map<string, Node<T>>
  own: T[]
}

export type TreeRow<T> =
  | {
      type: 'group'
      id: string
      label: string
      depth: number
      ids: string[]
      count: number
      staleCount: number
    }
  // groupId：直接所在分组的 id，顶层平铺的没有；用来按分组统计方法名列宽。
  | { type: 'perm'; point: T; depth: number; groupId?: string }

export interface PermissionLayout<T> {
  rows: TreeRow<T>[]
  /** 树中所有分组（含折叠起来的嵌套分组）的 id，用于「全部展开」。 */
  groupIds: string[]
  /** 按展示顺序编号，与折叠状态无关。 */
  seq: Map<string, number>
}

/** 权限点少于这个数的子树不成组。 */
const MIN_GROUP_SIZE = 4

function build<T extends TreePoint>(points: T[]): Node<T> {
  const root: Node<T> = { seg: '', path: '', kids: new Map(), own: [] }
  for (const p of points) {
    let n = root
    for (const s of p.key.slice(p.key.indexOf(':') + 1).split('/').filter(Boolean)) {
      let kid = n.kids.get(s)
      if (!kid) {
        kid = { seg: s, path: `${n.path}/${s}`, kids: new Map(), own: [] }
        n.kids.set(s, kid)
      }
      n = kid
    }
    n.own.push(p)
  }
  return root
}

function leaves<T>(n: Node<T>): T[] {
  return [...n.own, ...sortedKids(n).flatMap(leaves)]
}

function sortedKids<T>(n: Node<T>): Node<T>[] {
  return [...n.kids.values()].sort((a, b) => (a.seg < b.seg ? -1 : a.seg > b.seg ? 1 : 0))
}

function count<T>(n: Node<T>): number {
  return n.own.length + [...n.kids.values()].reduce((a, k) => a + count(k), 0)
}

/** 没有自有权限点且只有一个子分组的链压成一个标题（/api/v1），避免一层层点开。分组名一律用完整路径，嵌套时也不省略前缀。 */
function squash<T>(n: Node<T>): Node<T> {
  while (n.own.length === 0 && n.kids.size === 1) n = [...n.kids.values()][0]
  return n
}

/**
 * 把权限点铺成表格行。isOpen 决定每个分组是否展开；折叠的分组其后代不出现在
 * rows 里，但序号和 groupIds 仍按完整树计算。maxDepth 限制分组嵌套的层数。
 */
export function layoutPermissions<T extends TreePoint & { status?: string }>(
  points: T[],
  isOpen: (groupId: string) => boolean,
  maxDepth = Infinity,
): PermissionLayout<T> {
  const rows: TreeRow<T>[] = []
  const groupIds: string[] = []
  const seq = new Map<string, number>()

  function walk(n: Node<T>, depth: number, visible: boolean, groupId?: string) {
    for (const p of n.own) {
      seq.set(p.id, seq.size + 1)
      if (visible) rows.push({ type: 'perm', point: p, depth, groupId })
    }
    for (const k of sortedKids(n)) {
      // 太小的子树单开一个分组反而多一次点击；超过最大层级的更深路径也不再分组。
      // 这两种都直接把权限点平铺在当前层。
      if (count(k) < MIN_GROUP_SIZE || depth >= maxDepth) {
        for (const p of leaves(k)) {
          seq.set(p.id, seq.size + 1)
          if (visible) rows.push({ type: 'perm', point: p, depth, groupId })
        }
        continue
      }
      const end = squash(k)
      const id = end.path
      groupIds.push(id)
      const ls = leaves(end)
      const open = isOpen(id)
      if (visible) {
        rows.push({
          type: 'group',
          id,
          label: id,
          depth,
          ids: ls.map((p) => p.id),
          count: ls.length,
          staleCount: ls.filter((p) => p.status === 'stale').length,
        })
      }
      walk(end, depth + 1, visible && open, id)
    }
  }
  walk(build(points), 0, true)
  return { rows, groupIds, seq }
}

/** 取路径前 depth 段作为分色用的前缀；不足 depth 段的返回 null（不参与分色）。 */
export function colorPrefix(key: string, depth: number): string | null {
  const path = key.includes(':') ? key.slice(key.indexOf(':') + 1) : key
  const segs = path.split('/').filter(Boolean)
  return segs.length < depth ? null : '/' + segs.slice(0, depth).join('/')
}

/**
 * 给前缀依次编号（按字典序），界面拿编号去循环取色：前缀相同的权限点同色，
 * 相邻前缀换色。基于全量权限点计算，搜索过滤时颜色不会跟着跳。
 */
export function prefixColorIndex(keys: string[], depth: number): Map<string, number> {
  const prefixes = new Set<string>()
  for (const k of keys) {
    const p = colorPrefix(k, depth)
    if (p !== null) prefixes.add(p)
  }
  return new Map([...prefixes].sort().map((p, i) => [p, i]))
}

/**
 * 每个分组（顶层平铺的归在 ''）里「方法:」这一列的宽度，单位 ch：该组最长的方法名
 * + 冒号 + 1 个空格，让同组各行的路径从同一列开始，又不浪费空间。
 */
export function methodColumnWidths(rows: TreeRow<{ key: string }>[]): Map<string, number> {
  const widths = new Map<string, number>()
  for (const r of rows) {
    if (r.type !== 'perm') continue
    const colon = r.point.key.indexOf(':')
    const w = (colon === -1 ? 0 : colon) + 2
    const id = r.groupId ?? ''
    if (w > (widths.get(id) ?? 0)) widths.set(id, w)
  }
  return widths
}
