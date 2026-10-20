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
  | { type: 'group'; id: string; label: string; depth: number; ids: string[]; count: number; staleCount: number }
  | { type: 'perm'; point: T; depth: number }

export interface PermissionLayout<T> {
  rows: TreeRow<T>[]
  /** 树中所有分组（含折叠起来的嵌套分组）的 id，用于「全部展开」。 */
  groupIds: string[]
  /** 按展示顺序编号，与折叠状态无关。 */
  seq: Map<string, number>
}

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
 * rows 里，但序号和 groupIds 仍按完整树计算。
 */
export function layoutPermissions<T extends TreePoint & { status?: string }>(
  points: T[],
  isOpen: (groupId: string) => boolean,
): PermissionLayout<T> {
  const rows: TreeRow<T>[] = []
  const groupIds: string[] = []
  const seq = new Map<string, number>()

  function walk(n: Node<T>, depth: number, visible: boolean) {
    for (const p of n.own) {
      seq.set(p.id, seq.size + 1)
      if (visible) rows.push({ type: 'perm', point: p, depth })
    }
    for (const k of sortedKids(n)) {
      // 子树里只有一个权限点时不值得单开分组，直接显示成一行。
      if (count(k) === 1) {
        const p = leaves(k)[0]
        seq.set(p.id, seq.size + 1)
        if (visible) rows.push({ type: 'perm', point: p, depth })
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
      walk(end, depth + 1, visible && open)
    }
  }
  walk(build(points), 0, true)
  return { rows, groupIds, seq }
}
