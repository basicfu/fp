import { test, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { setUnauthorizedHandler } from '@/lib/api'
import { apiError, stubApi } from '@/lib/testApi'
import { ChangeAccountDialog } from './ChangeAccountDialog'

const mocks = vi.hoisted(() => ({
  setAccountDialogOpen: vi.fn(),
  endSession: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))
vi.mock('@/lib/auth', () => ({
  useAuth: () => ({
    username: 'admin',
    accountDialogOpen: true,
    setAccountDialogOpen: mocks.setAccountDialogOpen,
    endSession: mocks.endSession,
  }),
}))
vi.mock('sonner', () => ({ toast: { success: mocks.success, error: mocks.error } }))

beforeEach(() => {
  vi.clearAllMocks()
})
afterEach(() => {
  vi.unstubAllGlobals()
  // 还原 api.ts 的默认回调（空函数），免得用例里换上的 spy 漏给后面的用例。
  setUnauthorizedHandler(() => {})
})

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

function valueOf(label: string) {
  return (screen.getByLabelText(label) as HTMLInputElement).value
}

// 让悬着的 promise 链（fetch → res.json() → then）跑完，再断言"没发生"。
const flush = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })

// 路由表留空：打开对话框不该向后端取任何东西，任何请求都会被 stubApi 记下并在用例结束时判失败。
test('登录名预填当前值，打开时不发请求', async () => {
  const calls = stubApi({})
  render(<ChangeAccountDialog />)
  await flush()

  expect(valueOf('登录名')).toBe('admin')
  expect(calls).toEqual([])
})

test('两次新密码不一致时不发请求并提示', async () => {
  const calls = stubApi({})
  render(<ChangeAccountDialog />)
  fill('旧密码', 'admin')
  fill('新密码', 'aaa')
  fill('确认新密码', 'bbb')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.error).toHaveBeenCalled())
  expect(String(mocks.error.mock.calls[0][0])).toContain('不一致')
  expect(calls).toEqual([])
})

// 后端改成功后会作废全部会话，包括当前这个：本页不能再用旧会话，必须结束本地会话回登录页。
test('提交成功：请求体不变，提示重新登录，并结束本地会话', async () => {
  const calls = stubApi({ 'PUT /me': { username: 'boss' } })
  render(<ChangeAccountDialog />)
  fill('登录名', 'boss')
  fill('旧密码', 'admin')
  fill('新密码', 'n3w-pass')
  fill('确认新密码', 'n3w-pass')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.endSession).toHaveBeenCalledTimes(1))
  expect(calls).toEqual([
    { method: 'PUT', url: '/me', body: { username: 'boss', oldPassword: 'admin', newPassword: 'n3w-pass' } },
  ])
  expect(mocks.success).toHaveBeenCalledWith('账号信息已修改，请用新的登录名和密码重新登录')
})

test('新密码留空时只改登录名', async () => {
  const calls = stubApi({ 'PUT /me': { username: 'root' } })
  render(<ChangeAccountDialog />)
  fill('登录名', 'root')
  fill('旧密码', 'admin')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(calls).toHaveLength(1))
  expect(calls[0].body).toEqual({ username: 'root', oldPassword: 'admin', newPassword: '' })
})

// 旧密码错是 400，对话框必须留着让人重填。useAuth 在本文件里是 mock，没有真 Provider，
// 所以靠 spy 顶住 api.ts 的全局 401 回调（真实应用里它会把用户踢回登录页）：
// 400 不该触发它，响应一旦变成 401 这条就会红。
test('旧密码错误时提示原因并保持对话框打开，不结束会话，也不触发全局 401 回调', async () => {
  const onUnauthorized = vi.fn()
  setUnauthorizedHandler(onUnauthorized)
  stubApi({ 'PUT /me': apiError(400, 'ADMIN_OLD_PASSWORD_WRONG', '旧密码不正确') })
  render(<ChangeAccountDialog />)
  fill('旧密码', 'nope')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith('旧密码不正确'))
  expect(onUnauthorized).not.toHaveBeenCalled()
  expect(mocks.endSession).not.toHaveBeenCalled()
  expect(mocks.setAccountDialogOpen).not.toHaveBeenCalledWith(false)
})
