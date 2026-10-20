import { expect, test } from 'vitest'
import { colorPrefix, layoutPermissions, methodColumnWidths, prefixColorIndex } from './permissionTree'

const pts = (...keys: string[]) => keys.map((key, i) => ({ id: `p${i}`, key, status: 'normal' }))
const open = () => true
const closed = () => false

function shape(keys: string[], isOpen = open) {
  return layoutPermissions(pts(...keys), isOpen).rows.map((r) =>
    r.type === 'group' ? `G:${r.label}@${r.depth}(${r.count})` : `P:${r.point.key}@${r.depth}`,
  )
}

test('同一路径的权限点挂在同一分组下，自有权限点在子分组之前', () => {
  const x = ['GET', 'POST', 'PUT', 'DELETE'].map((m) => `${m}:/orders/{id}/x`)
  expect(shape([...x, 'GET:/orders', 'POST:/orders'])).toEqual([
    'G:/orders@0(6)',
    'P:GET:/orders@1',
    'P:POST:/orders@1',
    'G:/orders/{id}/x@1(4)',
    ...x.map((k) => `P:${k}@2`),
  ])
})

test('无自有权限点且只有一个子分组的链压成一个标题', () => {
  const a = ['GET', 'POST', 'PUT', 'DELETE'].map((m) => `${m}:/api/v1/a`)
  expect(shape([...a, 'GET:/api/v1/b', 'GET:/api/v1/c'])).toEqual([
    'G:/api/v1@0(6)',
    'G:/api/v1/a@1(4)',
    ...a.map((k) => `P:${k}@2`),
    'P:GET:/api/v1/b@1',
    'P:GET:/api/v1/c@1',
  ])
})

test('少于 4 个权限点的子树不成组，平铺在当前层', () => {
  expect(shape(['GET:/ping', 'GET:/a/b/c'])).toEqual(['P:GET:/a/b/c@0', 'P:GET:/ping@0'])
  expect(shape(['GET:/a/1', 'GET:/a/2', 'GET:/a/3'])).toEqual(['P:GET:/a/1@0', 'P:GET:/a/2@0', 'P:GET:/a/3@0'])
})

test('分组按段名排序', () => {
  const r = shape(['GET:/z/1', 'GET:/z/2', 'GET:/z/3', 'GET:/z/4', 'GET:/a/1', 'GET:/a/2', 'GET:/a/3', 'GET:/a/4'])
  expect(r.filter((s) => s.startsWith('G:'))).toEqual(['G:/a@0(4)', 'G:/z@0(4)'])
})

test('折叠的分组不输出后代行，序号与折叠无关', () => {
  const keys = [1, 2, 3, 4].flatMap((n) => [`GET:/a/${n}`, `GET:/b/${n}`]).sort()
  const closedL = layoutPermissions(pts(...keys), closed)
  const openL = layoutPermissions(pts(...keys), open)
  expect(closedL.rows).toHaveLength(2)
  expect(openL.rows).toHaveLength(10)
  expect([...closedL.seq.entries()]).toEqual([...openL.seq.entries()])
  expect(openL.seq.get('p7')).toBe(8)
})

test('groupIds 含折叠起来的嵌套分组；分组统计 stale 数和后代 id', () => {
  const points = [
    { id: '1', key: 'GET:/a', status: 'normal' },
    { id: '2', key: 'GET:/a/x/1', status: 'stale' },
    { id: '3', key: 'GET:/a/x/2', status: 'normal' },
    { id: '4', key: 'GET:/a/x/3', status: 'normal' },
    { id: '5', key: 'GET:/a/x/4', status: 'normal' },
  ]
  const l = layoutPermissions(points, closed)
  expect(l.groupIds).toEqual(['/a', '/a/x'])
  const g = l.rows[0]
  expect(g.type === 'group' && g.ids).toEqual(['1', '2', '3', '4', '5'])
  expect(g.type === 'group' && g.staleCount).toBe(1)
})

test('空列表没有行', () => {
  expect(layoutPermissions([], open).rows).toEqual([])
})

test('maxDepth 限制分组层数，更深的权限点平铺在最深一层分组里', () => {
  const keys = [1, 2, 3, 4].map((n) => `GET:/a/${n}`).concat([1, 2, 3, 4].map((n) => `GET:/a/x/${n}`))
  const rows = layoutPermissions(pts(...keys), open, 1).rows
  expect(rows.filter((r) => r.type === 'group')).toHaveLength(1)
  expect(rows).toHaveLength(9)
  expect(layoutPermissions(pts(...keys), open, 2).rows.filter((r) => r.type === 'group')).toHaveLength(2)
})

test('分色前缀取前 n 段，不足 n 段的不参与；编号按字典序、不随过滤变化', () => {
  expect(colorPrefix('GET:/console/api/devices/{id}', 3)).toBe('/console/api/devices')
  expect(colorPrefix('GET:/ping', 3)).toBeNull()
  const keys = ['GET:/console/api/stats', 'GET:/console/api/calls', 'POST:/console/api/calls/x', 'GET:/ping']
  expect([...prefixColorIndex(keys, 3).entries()]).toEqual([
    ['/console/api/calls', 0],
    ['/console/api/stats', 1],
  ])
})

test('方法名列宽按直接所在分组统计：最长方法 + 冒号 + 1 个空格', () => {
  const keys = ['GET:/a/1', 'POST:/a/2', 'DELETE:/a/3', 'GET:/a/4', 'GET:/ping']
  const w = methodColumnWidths(layoutPermissions(pts(...keys), open).rows)
  expect(w.get('/a')).toBe(8) // DELETE(6) + ':' + ' '
  expect(w.get('')).toBe(5) // 顶层平铺只有 GET
})
