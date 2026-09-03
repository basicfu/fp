import { test, expect } from 'vitest'
import { buildUserQuery, normalizePage, PAGE_SIZE } from './query'

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

// normalizePage 是 buildUserQuery 与 Users.tsx 共用的页码钳制逻辑。
// 【辨别力】此前 Users.tsx 曾经自己重复实现过一遍同样的算法（未受测试
// 覆盖，也不保证与这里同步）——这里直接针对 normalizePage 本身补单元
// 测试，以后两处调用点才不会因为其中一处被单独改动而悄悄分叉。
test('normalizePage 把非法输入（非数字、负数、小数）钳到 >= 1 的整数', () => {
  expect(normalizePage('abc')).toBe(1) // 非数字字符串（比如手改 ?page=abc）→ NaN → 兜底
  expect(normalizePage(-3)).toBe(1) // 负数
  expect(normalizePage('-3')).toBe(1) // 负数字符串
  expect(normalizePage(0)).toBe(1) // 0
  expect(normalizePage(2.7)).toBe(2) // 小数向下取整，不能原样透传给 Pagination 显示成"第 2.7 页"

  for (const v of ['abc', -3, '-3', 0, 2.7]) {
    expect(Number.isInteger(normalizePage(v))).toBe(true)
    expect(normalizePage(v)).toBeGreaterThanOrEqual(1)
  }
})

test('normalizePage 缺省（null/undefined）按第 1 页处理，合法页码原样返回', () => {
  // URLSearchParams#get() 在参数不存在时返回 null，不是 undefined——
  // 两种"缺省"都要处理到，否则其中一种会漏测。
  expect(normalizePage(null)).toBe(1)
  expect(normalizePage(undefined)).toBe(1)
  expect(normalizePage(3)).toBe(3)
  expect(normalizePage('5')).toBe(5)
})
