import { test, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import NotifyProviderDetail from './NotifyProviderDetail'
import { stubApi } from '@/lib/testApi'
import type { NotifyProvider, NotifyProviderType, NotifyProviderUsage } from '@/lib/types'

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('sonner', () => ({ toast: toasts }))

beforeEach(() => vi.clearAllMocks())
afterEach(() => vi.unstubAllGlobals())

const types: NotifyProviderType[] = [
  {
    type: 'telegram',
    channel: 'telegram',
    fields: [
      { key: 'botToken', label: 'Bot Token', type: 'secret', required: true },
      { key: 'chatId', label: 'Chat ID', type: 'string', required: true },
    ],
  },
]

// 后端读取时已把 secret 脱敏成掩码。
const provider: NotifyProvider = {
  id: 'p1', type: 'telegram', channel: 'telegram', description: '告警 bot', enabled: true,
  config: { botToken: '********', chatId: '-100' }, createdAt: 1, updatedAt: 1,
}
const usages: NotifyProviderUsage[] = [
  { code: 'order_alert', channel: 'telegram', templateEnabled: true, providerTemplateId: '', enabled: true, priority: 0 },
]

const renderPage = () =>
  render(
    <MemoryRouter initialEntries={['/notify/providers/p1']}>
      <Routes>
        <Route path="/notify/providers/:id" element={<NotifyProviderDetail />} />
      </Routes>
    </MemoryRouter>,
  )

const routes = {
  'GET /notify/providers/p1': provider,
  'GET /notify/provider-types': types,
  'GET /notify/providers/p1/templates': usages,
}

test('回显配置（secret 是掩码），并反向列出引用它的模板', async () => {
  stubApi(routes)
  renderPage()
  expect(((await screen.findByLabelText(/Bot Token/)) as HTMLInputElement).value).toBe('********')
  expect((screen.getByLabelText(/Chat ID/) as HTMLInputElement).value).toBe('-100')
  const link = screen.getByRole('link', { name: 'order_alert' })
  expect(link.getAttribute('href')).toBe('/notify/templates/order_alert')
})

// 【辨别力】没碰 secret 字段就保存：提交的必须是掩码本身，后端据此保持原值；
// 如果前端把它当空值丢掉或清空，一次改备注就会把凭据冲掉。
test('只改备注保存：secret 原样回传掩码', async () => {
  const calls = stubApi({ ...routes, 'PATCH /notify/providers/p1': provider })
  renderPage()
  fireEvent.change(await screen.findByLabelText('备注'), { target: { value: '告警 bot（主）' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true))
  expect(calls.find((c) => c.method === 'PATCH')!.body).toEqual({
    description: '告警 bot（主）',
    config: { botToken: '********', chatId: '-100' },
  })
  await waitFor(() => expect(toasts.success).toHaveBeenCalledWith('已保存'))
})

test('没有模板引用时给出提示', async () => {
  stubApi({ ...routes, 'GET /notify/providers/p1/templates': [] })
  renderPage()
  expect(await screen.findByText('还没有模板引用它。')).toBeTruthy()
})
