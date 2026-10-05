import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import NotifyLogs from './NotifyLogs'
import { stubApi } from '@/lib/testApi'
import type { NotifyLog } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

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

test('显示每次尝试的结果；没有调用方应用的是控制台测试发送', async () => {
  stubApi({ 'GET /notify/logs?limit=20&offset=0': { items: [ok, failed], total: 2 } })
  renderPage()
  const okRow = (await screen.findByText('13800138000')).closest('tr')!
  for (const text of ['login_sms', '短信', 'aliyun', '成功', 'app-1']) expect(okRow.textContent).toContain(text)
  const failRow = screen.getByText('alert').closest('tr')!
  for (const text of ['Telegram', '失败', 'chat not found', '控制台']) expect(failRow.textContent).toContain(text)
})

test('按 code 与结果筛选，条件进查询串，并回到第一页', async () => {
  const calls = stubApi({
    'GET /notify/logs?limit=20&offset=0': { items: [ok], total: 1 },
    'GET /notify/logs?limit=20&offset=0&code=login_sms': { items: [ok], total: 1 },
    'GET /notify/logs?limit=20&offset=0&code=login_sms&success=false': { items: [], total: 0 },
  })
  renderPage()
  await screen.findByText('13800138000')

  fireEvent.change(screen.getByLabelText('按模板 code 筛选'), { target: { value: 'login_sms' } })
  await waitFor(() => expect(calls.some((c) => c.url.includes('code=login_sms') && !c.url.includes('success'))).toBe(true))

  fireEvent.change(screen.getByLabelText('按结果筛选'), { target: { value: 'false' } })
  expect(await screen.findByText('没有记录')).toBeTruthy()
  expect(calls.at(-1)!.url).toBe('/notify/logs?limit=20&offset=0&code=login_sms&success=false')
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

test('在第 2 页改筛选会回到第 1 页', async () => {
  const calls = stubApi({
    'GET /notify/logs?limit=20&offset=0': { items: [ok], total: 45 },
    'GET /notify/logs?limit=20&offset=20': { items: [ok], total: 45 },
    'GET /notify/logs?limit=20&offset=0&success=false': { items: [], total: 0 },
    'GET /notify/logs?limit=20&offset=20&success=false': { items: [], total: 0 },
  })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '下一页' }))
  expect(await screen.findByText('共 45 条，第 2 / 3 页')).toBeTruthy()
  fireEvent.change(screen.getByLabelText('按结果筛选'), { target: { value: 'false' } })
  await waitFor(() => expect(calls.at(-1)!.url).toBe('/notify/logs?limit=20&offset=0&success=false'))
})
