import { expect, test } from 'vitest'
import { layoutPermissions } from './permissionTree'

const pts = (...keys: string[]) => keys.map((key, i) => ({ id: `p${i}`, key, status: 'normal' }))
const open = () => true
const closed = () => false

function shape(keys: string[], isOpen = open) {
  return layoutPermissions(pts(...keys), isOpen).rows.map((r) =>
    r.type === 'group' ? `G:${r.label}@${r.depth}(${r.count})` : `P:${r.point.key}@${r.depth}`,
  )
}

test('同一路径的权限点挂在同一分组下，自有权限点在子分组之前', () => {
  expect(shape(['GET:/orders/{id}/x', 'POST:/orders/{id}/x', 'GET:/orders', 'POST:/orders'])).toEqual([
    'G:/orders@0(4)',
    'P:GET:/orders@1',
    'P:POST:/orders@1',
    'G:/orders/{id}/x@1(2)',
    'P:GET:/orders/{id}/x@2',
    'P:POST:/orders/{id}/x@2',
  ])
})

test('无自有权限点且只有一个子分组的链压成一个标题', () => {
  expect(shape(['GET:/api/v1/a', 'POST:/api/v1/a', 'GET:/api/v1/b', 'GET:/api/v1/c'])).toEqual([
    'G:/api/v1@0(4)',
    'G:/api/v1/a@1(2)',
    'P:GET:/api/v1/a@2',
    'P:POST:/api/v1/a@2',
    'P:GET:/api/v1/b@1',
    'P:GET:/api/v1/c@1',
  ])
})

test('子树只有一个权限点时直接显示成一行', () => {
  expect(shape(['GET:/ping', 'GET:/a/b/c'])).toEqual(['P:GET:/a/b/c@0', 'P:GET:/ping@0'])
})

test('分组按段名排序', () => {
  const r = shape(['GET:/z/1', 'GET:/z/2', 'GET:/a/1', 'GET:/a/2'])
  expect(r.filter((s) => s.startsWith('G:'))).toEqual(['G:/a@0(2)', 'G:/z@0(2)'])
})

test('折叠的分组不输出后代行，序号与折叠无关', () => {
  const keys = ['GET:/a/1', 'GET:/a/2', 'GET:/b/1', 'GET:/b/2']
  const closedL = layoutPermissions(pts(...keys), closed)
  const openL = layoutPermissions(pts(...keys), open)
  expect(closedL.rows).toHaveLength(2)
  expect(openL.rows).toHaveLength(6)
  expect([...closedL.seq.entries()]).toEqual([...openL.seq.entries()])
  expect(openL.seq.get('p3')).toBe(4)
})

test('groupIds 含折叠起来的嵌套分组；分组统计 stale 数和后代 id', () => {
  const points = [
    { id: '1', key: 'GET:/a', status: 'normal' },
    { id: '2', key: 'GET:/a/x/1', status: 'stale' },
    { id: '3', key: 'GET:/a/x/2', status: 'normal' },
  ]
  const l = layoutPermissions(points, closed)
  expect(l.groupIds).toEqual(['/a', '/a/x'])
  const g = l.rows[0]
  expect(g.type === 'group' && g.ids).toEqual(['1', '2', '3'])
  expect(g.type === 'group' && g.staleCount).toBe(1)
})

test('空列表没有行', () => {
  expect(layoutPermissions([], open).rows).toEqual([])
})
