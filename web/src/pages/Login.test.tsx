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

test('不是默认密码时不弹提示', async () => {
  mocks.login.mockResolvedValue({ defaultPassword: false })
  submit()
  await waitFor(() => expect(mocks.login).toHaveBeenCalled())
  expect(mocks.warning).not.toHaveBeenCalled()
})
