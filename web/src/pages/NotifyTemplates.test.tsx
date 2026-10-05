import { test, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import NotifyTemplates from './NotifyTemplates'
import { stubApi } from '@/lib/testApi'
import type { NotifyTemplateRow } from '@/lib/types'

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('sonner', () => ({ toast: toasts }))

beforeEach(() => vi.clearAllMocks())
afterEach(() => vi.unstubAllGlobals())

const rows: NotifyTemplateRow[] = [
  { code: 'login_sms', channel: 'sms', mode: 'vendor', description: '登录短信', enabled: true, providerCount: 2, updatedAt: 1700000000000 },
  { code: 'order_alert', channel: 'wecom_bot', mode: 'custom', description: '', enabled: false, providerCount: 0, updatedAt: 1700000000000 },
]

const renderPage = () =>
  render(
    <MemoryRouter initialEntries={['/notify/templates']}>
      <Routes>
        <Route path="/notify/templates" element={<NotifyTemplates />} />
        <Route path="/notify/templates/:code" element={<div>详情页</div>} />
      </Routes>
    </MemoryRouter>,
  )

test('列表显示渠道、模式、供应商数；没关联供应商的要醒目提示', async () => {
  stubApi({ 'GET /notify/templates': rows })
  renderPage()
  const sms = (await screen.findByRole('link', { name: 'login_sms' })).closest('tr')!
  for (const text of ['短信', '供应商模板', '登录短信', '2', '启用']) expect(sms.textContent).toContain(text)
  const im = screen.getByRole('link', { name: 'order_alert' }).closest('tr')!
  for (const text of ['企业微信机器人', '自定义内容', '未关联', '停用']) expect(im.textContent).toContain(text)
})

test('新建短信模板：从供应商原文提取变量，提交后跳到详情页去关联供应商', async () => {
  const calls = stubApi({ 'GET /notify/templates': [], 'POST /notify/templates': { code: 'login_sms' } })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '新建模板' }))

  fireEvent.change(await screen.findByLabelText('code'), { target: { value: 'login_sms' } })
  fireEvent.change(screen.getByLabelText('备注'), { target: { value: '登录短信' } })
  fireEvent.change(screen.getByLabelText(/供应商模板原文/), { target: { value: '验证码 ${code}' } })
  fireEvent.click(screen.getByRole('button', { name: '从原文提取' }))
  fireEvent.click(screen.getByRole('button', { name: '创建' }))

  await waitFor(() => expect(screen.getByText('详情页')).toBeTruthy())
  expect(calls.find((c) => c.method === 'POST')!.body).toEqual({
    code: 'login_sms',
    channel: 'sms',
    mode: 'vendor',
    description: '登录短信',
    enabled: true,
    content: { content: '验证码 ${code}', variables: ['code'] },
  })
})

// 渠道决定允许的模式：短信只有供应商模板（模式框锁定），换成 webhook 后只剩自定义，并出现请求方式。
test('切换渠道会重置模式与内容表单', async () => {
  stubApi({ 'GET /notify/templates': [] })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '新建模板' }))

  const mode = await screen.findByLabelText('模板模式')
  expect((mode as HTMLSelectElement).value).toBe('vendor')
  expect((mode as HTMLSelectElement).disabled).toBe(true)

  fireEvent.change(screen.getByLabelText('渠道'), { target: { value: 'webhook' } })
  expect((screen.getByLabelText('模板模式') as HTMLSelectElement).value).toBe('custom')
  expect(screen.getByLabelText('请求方式')).toBeTruthy()

  fireEvent.change(screen.getByLabelText('渠道'), { target: { value: 'email' } })
  const emailMode = screen.getByLabelText('模板模式') as HTMLSelectElement
  expect(emailMode.disabled).toBe(false)
  expect(Array.from(emailMode.options).map((o) => o.value)).toEqual(['vendor', 'custom'])
})

// code 创建后不可改，写错了只能删掉重建；确认弹窗里要说清后果。
test('删除模板：确认后调用 DELETE 并刷新列表', async () => {
  let deleted = false
  const calls = stubApi({
    'GET /notify/templates': () => (deleted ? [rows[1]] : rows),
    'DELETE /notify/templates/login_sms': () => {
      deleted = true
      return undefined
    },
  })
  renderPage()
  const row = (await screen.findByRole('link', { name: 'login_sms' })).closest('tr')!
  fireEvent.click(Array.from(row.querySelectorAll('button')).find((b) => b.textContent === '删除')!)
  expect(await screen.findByText(/模板不存在/)).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: '删除' }))

  await waitFor(() => expect(screen.queryByRole('link', { name: 'login_sms' })).toBeNull())
  expect(calls.some((c) => c.method === 'DELETE' && c.url === '/notify/templates/login_sms')).toBe(true)
  expect(toasts.success).toHaveBeenCalledWith('已删除')
})

test('创建失败时提示后端原因，不跳转', async () => {
  stubApi({
    'GET /notify/templates': [],
    'POST /notify/templates': new Response(JSON.stringify({ code: 'NOTIFY_TEMPLATE_CODE_TAKEN', msg: 'code "x" 已被占用' }), { status: 409 }),
  })
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: '新建模板' }))
  fireEvent.change(await screen.findByLabelText('code'), { target: { value: 'x' } })
  fireEvent.click(screen.getByRole('button', { name: '创建' }))
  await waitFor(() => expect(toasts.error).toHaveBeenCalledWith('code "x" 已被占用'))
  expect(screen.queryByText('详情页')).toBeNull()
})
