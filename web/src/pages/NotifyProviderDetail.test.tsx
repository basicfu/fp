import { test, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import NotifyProviderDetail from './NotifyProviderDetail'
import { apiError, stubApi } from '@/lib/testApi'
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

// 模板停用后业务方调用会被拒绝：只看"关联：启用"会以为这条还在生效。
test('被引用的模板已停用时，在模板 code 后标明', async () => {
  const off: NotifyProviderUsage = { code: 'old_alert', channel: 'telegram', templateEnabled: false, providerTemplateId: '', enabled: true, priority: 0 }
  stubApi({ ...routes, 'GET /notify/providers/p1/templates': [usages[0], off] })
  renderPage()
  const offRow = (await screen.findByRole('link', { name: 'old_alert' })).closest('tr')!
  expect(offRow.textContent).toContain('（模板已停用）')
  expect(screen.getByRole('link', { name: 'order_alert' }).closest('tr')!.textContent).not.toContain('模板已停用')
})

// 类型被注册表移除后表单渲染不出来；备注若还能编辑却没有保存按钮，改了也存不了。
test('类型已不受支持：备注不能编辑，并说明为什么、能做什么', async () => {
  stubApi({ ...routes, 'GET /notify/provider-types': [] })
  renderPage()
  const desc = (await screen.findByLabelText('备注')) as HTMLInputElement
  expect(desc.disabled).toBe(true)
  expect(screen.getByText(/已不受支持.*供应商列表.*删除/)).toBeTruthy()
  expect(screen.queryByRole('button', { name: '保存' })).toBeNull()
})

test('引用列表加载失败：这一块显示原因，不是一片空白', async () => {
  stubApi({ ...routes, 'GET /notify/providers/p1/templates': apiError(500, 'INTERNAL', '服务器内部错误') })
  renderPage()
  expect(await screen.findByText('服务器内部错误')).toBeTruthy()
  expect(screen.getByLabelText('备注')).toBeTruthy() // 页面其余部分照常
})

test('类型列表加载失败：整页显示原因，而不是一直"加载中…"', async () => {
  stubApi({ ...routes, 'GET /notify/provider-types': apiError(500, 'INTERNAL', '服务器内部错误') })
  renderPage()
  expect(await screen.findByText('服务器内部错误')).toBeTruthy()
  expect(screen.queryByText('加载中…')).toBeNull()
})

test('供应商加载失败（还没有数据）：整页显示原因', async () => {
  stubApi({ ...routes, 'GET /notify/providers/p1': apiError(404, 'NOTIFY_PROVIDER_NOT_FOUND', '供应商不存在') })
  renderPage()
  expect(await screen.findByText('供应商不存在')).toBeTruthy()
  expect(screen.queryByLabelText('备注')).toBeNull()
})

// 整页只剩一行红字只该发生在"还没有数据"的时候；保存后重新拉取失败，错误显示在页内，页面照常能用。
test('保存后重新拉取失败：错误显示在页内，表单还在', async () => {
  let gets = 0
  stubApi({
    ...routes,
    'GET /notify/providers/p1': () => (++gets === 1 ? provider : apiError(500, 'INTERNAL', '服务器内部错误')),
    'PATCH /notify/providers/p1': provider,
  })
  renderPage()
  fireEvent.change(await screen.findByLabelText('备注'), { target: { value: '新备注' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  expect(await screen.findByText('服务器内部错误')).toBeTruthy()
  expect((screen.getByLabelText('备注') as HTMLInputElement).value).toBe('新备注')
})
