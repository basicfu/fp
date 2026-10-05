import { test, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { setUnauthorizedHandler } from '@/lib/api'
import { ChangeAccountDialog } from './ChangeAccountDialog'

const mocks = vi.hoisted(() => ({
  setAccountDialogOpen: vi.fn(),
  renameUser: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))
vi.mock('@/lib/auth', () => ({
  useAuth: () => ({
    username: 'admin',
    accountDialogOpen: true,
    setAccountDialogOpen: mocks.setAccountDialogOpen,
    renameUser: mocks.renameUser,
  }),
}))
vi.mock('sonner', () => ({ toast: { success: mocks.success, error: mocks.error } }))

beforeEach(() => vi.clearAllMocks())
afterEach(() => {
  vi.unstubAllGlobals()
  // 还原 api.ts 的默认回调（空函数），免得用例里换上的 spy 漏给后面的用例。
  setUnauthorizedHandler(() => {})
})

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

test('登录名预填当前值', () => {
  render(<ChangeAccountDialog />)
  expect((screen.getByLabelText('登录名') as HTMLInputElement).value).toBe('admin')
})

test('两次新密码不一致时不发请求并提示', async () => {
  const fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
  render(<ChangeAccountDialog />)
  fill('旧密码', 'admin')
  fill('新密码', 'aaa')
  fill('确认新密码', 'bbb')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.error).toHaveBeenCalled())
  expect(String(mocks.error.mock.calls[0][0])).toContain('不一致')
  expect(fetchMock).not.toHaveBeenCalled()
})

test('提交后同步登录名并关闭对话框', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ token: 't', username: 'boss' }), { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  render(<ChangeAccountDialog />)
  fill('登录名', 'boss')
  fill('旧密码', 'admin')
  fill('新密码', 'n3w-pass')
  fill('确认新密码', 'n3w-pass')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.renameUser).toHaveBeenCalledWith('boss'))
  const [url, init] = fetchMock.mock.calls[0]
  expect(url).toBe('/admin/api/me')
  expect(init.method).toBe('PUT')
  expect(JSON.parse(String(init.body))).toEqual({ username: 'boss', oldPassword: 'admin', newPassword: 'n3w-pass' })
  expect(mocks.setAccountDialogOpen).toHaveBeenCalledWith(false)
  expect(mocks.success).toHaveBeenCalled()
})

test('新密码留空时只改登录名', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ token: 't', username: 'root' }), { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  render(<ChangeAccountDialog />)
  fill('登录名', 'root')
  fill('旧密码', 'admin')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(fetchMock).toHaveBeenCalled())
  expect(JSON.parse(String(fetchMock.mock.calls[0][1].body))).toEqual({ username: 'root', oldPassword: 'admin', newPassword: '' })
})

// 旧密码错是 400，对话框必须留着让人重填。useAuth 在本文件里是 mock，没有真 Provider，
// 所以靠 spy 顶住 api.ts 的全局 401 回调（真实应用里它会把用户踢回登录页）：
// 400 不该触发它，响应一旦变成 401 这条就会红。
test('旧密码错误时提示原因并保持对话框打开，且不触发全局 401 回调', async () => {
  const onUnauthorized = vi.fn()
  setUnauthorizedHandler(onUnauthorized)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ code: 'ADMIN_OLD_PASSWORD_WRONG', msg: '旧密码不正确' }), { status: 400 }),
  ))
  render(<ChangeAccountDialog />)
  fill('旧密码', 'nope')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith('旧密码不正确'))
  expect(onUnauthorized).not.toHaveBeenCalled()
  expect(mocks.renameUser).not.toHaveBeenCalled()
  expect(mocks.setAccountDialogOpen).not.toHaveBeenCalledWith(false)
})
