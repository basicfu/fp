import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import NotifyLogs from './NotifyLogs'
import { stubApi } from '@/lib/testApi'
import type { NotifyLog } from '@/lib/types'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const ok: NotifyLog = {
  id: 'l1', channel: 'sms', target: '13800138000', code: 'login_sms', provider: 'aliyun', providerId: 'p1',
  appId: 'app-1', success: true, error: '', createdAt: 1700000000000,
}
const failed: NotifyLog = {
  ...ok, id: 'l2', channel: 'telegram', target: '', code: 'alert', provider: 'telegram', appId: '',
  success: false, error: 'telegram: HTTP 400 chat not found',
}

const renderPage = () =>
  render(
    <MemoryRouter>
      <NotifyLogs />
    </MemoryRouter>,
  )

// code 筛选带 300 ms 防抖。这几个用例用假计时器推进时间：不真等，也不让真实计时器引入抖动。
// 先用真计时器等首屏加载完再切到假的，免得 findBy 在假计时器下空等。
async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

test('显示每次尝试的结果；没有调用方应用的是控制台测试发送', async () => {
  stubApi({ 'GET /notify/logs?limit=20&offset=0': { items: [ok, failed], total: 2 } })
  renderPage()
  const okRow = (await screen.findByText('13800138000')).closest('tr')!
  for (const text of ['login_sms', '短信', 'aliyun', '成功', 'app-1']) expect(okRow.textContent).toContain(text)
  const failRow = screen.getByText('alert').closest('tr')!
  for (const text of ['Telegram', '失败', 'chat not found', '控制台']) expect(failRow.textContent).toContain(text)
})

test('按 code 与结果筛选，条件进查询串', async () => {
  const calls = stubApi({
    'GET /notify/logs?limit=20&offset=0': { items: [ok], total: 1 },
    'GET /notify/logs?limit=20&offset=0&code=login_sms': { items: [ok], total: 1 },
    'GET /notify/logs?limit=20&offset=0&code=login_sms&success=false': { items: [], total: 0 },
  })
  renderPage()
  await screen.findByText('13800138000')
  vi.useFakeTimers()

  fireEvent.change(screen.getByLabelText('按模板 code 筛选'), { target: { value: 'login_sms' } })
  await advance(300)
  expect(calls.at(-1)!.url).toBe('/notify/logs?limit=20&offset=0&code=login_sms')

  fireEvent.change(screen.getByLabelText('按结果筛选'), { target: { value: 'false' } })
  await advance(0)
  expect(screen.getByText('没有记录')).toBeTruthy()
  expect(calls.at(-1)!.url).toBe('/notify/logs?limit=20&offset=0&code=login_sms&success=false')
})

// 后端的 code 是精确匹配：每敲一个字发一次请求，中间每个半截 code 都匹配不到，表格会在"没有记录"与结果之间来回跳。
test('逐键输入 code 只在停顿 300 ms 之后发一次请求', async () => {
  const calls = stubApi({
    'GET /notify/logs?limit=20&offset=0': { items: [ok], total: 1 },
    'GET /notify/logs?limit=20&offset=0&code=abc': { items: [ok], total: 1 },
  })
  renderPage()
  await screen.findByText('13800138000')
  vi.useFakeTimers()

  expect(screen.getByLabelText('按模板 code 筛选').getAttribute('placeholder')).toBe('模板 code（精确匹配）')
  const n = calls.length
  for (const typed of ['a', 'ab', 'abc']) {
    fireEvent.change(screen.getByLabelText('按模板 code 筛选'), { target: { value: typed } })
    await advance(100) // 键间隔小于防抖时间
  }
  expect(calls.length).toBe(n) // 还没到停顿时间，一个请求都没发
  await advance(300)
  expect(calls.slice(n).map((c) => c.url)).toEqual(['/notify/logs?limit=20&offset=0&code=abc'])
})

test('code 两端的空白不进查询串，只敲空白不发请求', async () => {
  const calls = stubApi({
    'GET /notify/logs?limit=20&offset=0': { items: [ok], total: 1 },
    'GET /notify/logs?limit=20&offset=0&code=abc': { items: [ok], total: 1 },
  })
  renderPage()
  await screen.findByText('13800138000')
  vi.useFakeTimers()

  const n = calls.length
  fireEvent.change(screen.getByLabelText('按模板 code 筛选'), { target: { value: '   ' } })
  await advance(300)
  expect(calls.length).toBe(n)
  fireEvent.change(screen.getByLabelText('按模板 code 筛选'), { target: { value: ' abc ' } })
  await advance(300)
  expect(calls.slice(n).map((c) => c.url)).toEqual(['/notify/logs?limit=20&offset=0&code=abc'])
})

test('翻页用 offset，总数来自后端', async () => {
  const calls = stubApi({
    'GET /notify/logs?limit=20&offset=0': { items: [ok], total: 45 },
    'GET /notify/logs?limit=20&offset=20': { items: [failed], total: 45 },
  })
  renderPage()
  expect(await screen.findByText('共 45 条，第 1 / 3 页')).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: '下一页' }))
  expect(await screen.findByText('共 45 条，第 2 / 3 页')).toBeTruthy()
  expect(calls.at(-1)!.url).toBe('/notify/logs?limit=20&offset=20')
})

// 筛选条件一变就要回到第 1 页，否则结果会落在一个不存在的第 N 页。只看最后一次请求会放过
// "先带着旧页码发一次、随后才重置"的延迟重置，所以看的是改动之后的第一个请求。
test('在第 2 页改结果筛选，改动之后的第一个请求就回到第 1 页', async () => {
  const calls = stubApi({
    'GET /notify/logs?limit=20&offset=0': { items: [ok], total: 45 },
    'GET /notify/logs?limit=20&offset=20': { items: [ok], total: 45 },
    'GET /notify/logs?limit=20&offset=0&success=false': { items: [], total: 0 },
    'GET /notify/logs?limit=20&offset=20&success=false': { items: [], total: 0 },
  })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '下一页' }))
  expect(await screen.findByText('共 45 条，第 2 / 3 页')).toBeTruthy()

  const n = calls.length
  fireEvent.change(screen.getByLabelText('按结果筛选'), { target: { value: 'false' } })
  await waitFor(() => expect(calls.length).toBeGreaterThan(n))
  expect(calls[n].url).toBe('/notify/logs?limit=20&offset=0&success=false')
})

test('在第 2 页输入 code，防抖之后的第一个请求就回到第 1 页', async () => {
  const calls = stubApi({
    'GET /notify/logs?limit=20&offset=0': { items: [ok], total: 45 },
    'GET /notify/logs?limit=20&offset=20': { items: [ok], total: 45 },
    'GET /notify/logs?limit=20&offset=0&code=login_sms': { items: [ok], total: 1 },
    'GET /notify/logs?limit=20&offset=20&code=login_sms': { items: [], total: 1 },
  })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '下一页' }))
  expect(await screen.findByText('共 45 条，第 2 / 3 页')).toBeTruthy()
  vi.useFakeTimers()

  const n = calls.length
  fireEvent.change(screen.getByLabelText('按模板 code 筛选'), { target: { value: 'login_sms' } })
  await advance(300)
  expect(calls[n].url).toBe('/notify/logs?limit=20&offset=0&code=login_sms')
})
