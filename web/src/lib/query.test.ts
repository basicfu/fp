import { test, expect } from 'vitest'
import { buildUserQuery, PAGE_SIZE } from './query'

// 【辨别力】后端要的是 offset，不是页码。
//
// 第 1 页时 offset 和 page-1 都等于 0，两种实现都对；只有翻到第 2 页
// 之后才分得出来。一个把页码当 offset 发出去的实现，表现是"翻到第 2 页
// 只往后挪了一条记录"——看起来像后端分页坏了，实际错在这里。
// 所以这条测试**必须**断言第 3 页，不能只测第 1 页。
test('分页参数发的是 offset 而不是页码', () => {
  expect(new URLSearchParams(buildUserQuery({ page: 1 })).get('offset')).toBe('0')
  expect(new URLSearchParams(buildUserQuery({ page: 2 })).get('offset')).toBe(String(PAGE_SIZE))
  expect(new URLSearchParams(buildUserQuery({ page: 3 })).get('offset')).toBe(String(PAGE_SIZE * 2))
})

test('每页条数随请求发出', () => {
  expect(new URLSearchParams(buildUserQuery({ page: 1 })).get('limit')).toBe(String(PAGE_SIZE))
})

test('关键词与状态为空时不发这两个参数', () => {
  const q = new URLSearchParams(buildUserQuery({ page: 1 }))
  expect(q.has('keyword')).toBe(false)
  expect(q.has('status')).toBe(false)
})

test('关键词与状态非空时原样带上', () => {
  const q = new URLSearchParams(buildUserQuery({ page: 1, keyword: '138', status: 'FROZEN' }))
  expect(q.get('keyword')).toBe('138')
  expect(q.get('status')).toBe('FROZEN')
})

// 关键词里可能有 & = # 之类，必须转义，否则会拼出一个畸形查询串。
test('关键词做 URL 转义', () => {
  const raw = buildUserQuery({ page: 1, keyword: 'a&b=c' })
  expect(raw).not.toContain('a&b=c')
  expect(new URLSearchParams(raw).get('keyword')).toBe('a&b=c')
})

// 页码越界时兜底到第 1 页，不要发出负的 offset。
test('页码小于 1 时按第 1 页处理', () => {
  expect(new URLSearchParams(buildUserQuery({ page: 0 })).get('offset')).toBe('0')
  expect(new URLSearchParams(buildUserQuery({ page: -3 })).get('offset')).toBe('0')
})
