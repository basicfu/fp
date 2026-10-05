import { test, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import NotifyProviders from './NotifyProviders'
import { apiError, stubApi } from '@/lib/testApi'
import type { NotifyProvider, NotifyProviderType } from '@/lib/types'

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
  { type: 'aliyun', channel: 'sms', fields: [{ key: 'signName', label: '短信签名', type: 'string', required: true }] },
]

const aliyun: NotifyProvider = {
  id: 'p1', type: 'aliyun', channel: 'sms', description: '阿里云-主账号', enabled: true,
  config: {}, createdAt: 1, updatedAt: 1, templateCount: 2,
}

const renderPage = () =>
  render(
    <MemoryRouter>
      <NotifyProviders />
    </MemoryRouter>,
  )

test('列表显示类型、渠道、被引用数，备注是进入详情的链接', async () => {
  stubApi({ 'GET /notify/providers': [aliyun], 'GET /notify/provider-types': types })
  renderPage()
  const link = await screen.findByRole('link', { name: '阿里云-主账号' })
  expect(link.getAttribute('href')).toBe('/notify/providers/p1')
  const row = link.closest('tr')!
  for (const text of ['aliyun', '短信', '2']) expect(row.textContent).toContain(text)
})

test('点启用开关发 PATCH，并刷新列表', async () => {
  const calls = stubApi({
    'GET /notify/providers': [aliyun],
    'GET /notify/provider-types': types,
    'PATCH /notify/providers/p1': { ...aliyun, enabled: false },
  })
  renderPage()
  fireEvent.click(await screen.findByRole('switch', { name: '启用 阿里云-主账号' }))
  await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true))
  expect(calls.find((c) => c.method === 'PATCH')!.body).toEqual({ enabled: false })
  await waitFor(() => expect(calls.filter((c) => c.url === '/notify/providers' && c.method === 'GET').length).toBe(2))
})

test('新建：选类型后按该类型的字段渲染表单，提交带上备注与配置', async () => {
  const calls = stubApi({
    'GET /notify/providers': [],
    'GET /notify/provider-types': types,
    'POST /notify/providers': { ...aliyun, id: 'p2' },
  })
  renderPage()
  await waitFor(() => expect((screen.getByRole('button', { name: '新建供应商' }) as HTMLButtonElement).disabled).toBe(false))
  fireEvent.click(screen.getByRole('button', { name: '新建供应商' }))

  fireEvent.change(await screen.findByLabelText('类型'), { target: { value: 'telegram' } })
  fireEvent.change(screen.getByLabelText('备注'), { target: { value: '告警群' } })
  fireEvent.change(await screen.findByLabelText(/Bot Token/), { target: { value: '123:ABC' } })
  fireEvent.change(screen.getByLabelText(/Chat ID/), { target: { value: '-100' } })
  fireEvent.click(screen.getByRole('button', { name: '创建' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
  expect(calls.find((c) => c.method === 'POST')!.body).toEqual({
    type: 'telegram',
    description: '告警群',
    enabled: true,
    config: { botToken: '123:ABC', chatId: '-100' },
  })
  await waitFor(() => expect(toasts.success).toHaveBeenCalledWith('已创建'))
})

// 供应商仍被模板引用时后端返回 409，页面要把原因告诉人，而不是静默没反应。
test('删除被引用的供应商：显示后端的原因', async () => {
  stubApi({
    'GET /notify/providers': [aliyun],
    'GET /notify/provider-types': types,
    'DELETE /notify/providers/p1': apiError(409, 'NOTIFY_PROVIDER_IN_USE', '供应商仍被模板引用，请先在模板里解除关联'),
  })
  renderPage()
  const row = (await screen.findByRole('link', { name: '阿里云-主账号' })).closest('tr')!
  fireEvent.click(row.querySelector('button')!)
  fireEvent.click(await screen.findByRole('button', { name: '删除', hidden: false }))
  await waitFor(() => expect(toasts.error).toHaveBeenCalledWith('供应商仍被模板引用，请先在模板里解除关联'))
})
