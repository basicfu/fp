import { test, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import Login from './Login'

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
  setAccountDialogOpen: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
}))
vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ login: mocks.login, setAccountDialogOpen: mocks.setAccountDialogOpen }),
}))
vi.mock('sonner', () => ({ toast: { warning: mocks.warning, error: mocks.error } }))

beforeEach(() => vi.clearAllMocks())

function submit() {
  render(<Login />)
  fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'admin' } })
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'admin' } })
  fireEvent.click(screen.getByRole('button', { name: '登录' }))
}

test('仍是默认密码时弹出可忽略的提示，"去修改"打开改密对话框', async () => {
  mocks.login.mockResolvedValue({ defaultPassword: true })
  submit()
  await waitFor(() => expect(mocks.warning).toHaveBeenCalled())
  const [message, options] = mocks.warning.mock.calls[0]
  expect(String(message)).toContain('默认密码')
  expect(options.action.label).toBe('去修改')
  options.action.onClick()
  expect(mocks.setAccountDialogOpen).toHaveBeenCalledWith(true)
})

// Sonner 只在 toast.closeButton（或 Toaster 的 closeButton）为真时才画 ×，而全局
// Toaster 没开，所以得逐条声明；不然"可关闭"只剩过一阵自己消失。时长要明显长于
// Toaster 的 2 秒（main.tsx），回退成默认值用户还没看清、来不及点"去修改"就没了。
test('默认密码提示带关闭按钮，且停留时间长于 Toaster 默认的 2 秒', async () => {
  mocks.login.mockResolvedValue({ defaultPassword: true })
  submit()
  await waitFor(() => expect(mocks.warning).toHaveBeenCalled())
  const [, options] = mocks.warning.mock.calls[0]
  expect(options.closeButton).toBe(true)
  expect(options.duration).toBeGreaterThan(2000)
})

test('不是默认密码时不弹提示', async () => {
  mocks.login.mockResolvedValue({ defaultPassword: false })
  submit()
  await waitFor(() => expect(mocks.login).toHaveBeenCalled())
  expect(mocks.warning).not.toHaveBeenCalled()
})
