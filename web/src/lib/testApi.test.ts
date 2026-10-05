import { test, expect, vi, afterEach } from 'vitest'
import { api } from '@/lib/api'
import { apiError, stubApi, takeUnmatched } from './testApi'

afterEach(() => vi.unstubAllGlobals())

// api.ts 把 fetch 的拒绝包成"无法连接到服务器"，页面照样渲染，所以没登记的请求只能靠这个记录发现。
test('没登记路由的请求被记下来，takeUnmatched 取出后清空', async () => {
  stubApi({ 'GET /known': { ok: true } })
  await api.get('/known')
  await expect(api.get('/typo')).rejects.toMatchObject({ status: 0 })
  expect(takeUnmatched()).toEqual(['GET /typo'])
  expect(takeUnmatched()).toEqual([])
})

// Response 的 body 只能读一次；路由每次命中都要给副本，否则第二次的错误原因会退化成"请求失败（HTTP n）"。
test('Response 路由命中多次，每次都读得到同样的错误原因', async () => {
  stubApi({ 'GET /boom': apiError(500, 'INTERNAL', '服务器内部错误') })
  for (let i = 0; i < 2; i++) {
    await expect(api.get('/boom')).rejects.toMatchObject({ status: 500, code: 'INTERNAL', message: '服务器内部错误' })
  }
})

test('路由函数 throw 用来模拟网络失败，不算没登记', async () => {
  stubApi({
    'GET /down': () => {
      throw new TypeError('Failed to fetch')
    },
  })
  await expect(api.get('/down')).rejects.toMatchObject({ status: 0 })
  expect(takeUnmatched()).toEqual([])
})
