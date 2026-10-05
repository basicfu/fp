import { test, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { setUnauthorizedHandler } from '@/lib/api'
import { apiError, stubApi, type RecordedCall } from '@/lib/testApi'
import { ChangeAccountDialog } from './ChangeAccountDialog'

const mocks = vi.hoisted(() => ({
  open: true,
  setAccountDialogOpen: vi.fn(),
  renameUser: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))
vi.mock('@/lib/auth', async () => {
  const { useCallback, useState } = await import('react')
  return {
    // 登录名放进真 state：renameUser 之后组件才拿得到新名并回填，同步登录名的用例才测得到这一步。
    useAuth: () => {
      const [username, setUsername] = useState('admin')
      const renameUser = useCallback((name: string) => {
        mocks.renameUser(name)
        setUsername(name)
      }, [])
      return { username, accountDialogOpen: mocks.open, setAccountDialogOpen: mocks.setAccountDialogOpen, renameUser }
    },
  }
})
vi.mock('sonner', () => ({ toast: { success: mocks.success, error: mocks.error } }))

beforeEach(() => {
  vi.clearAllMocks()
  mocks.open = true
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

const putCalls = (calls: RecordedCall[]) => calls.filter((c) => c.method === 'PUT')

// 让悬着的 promise 链（fetch → res.json() → then）跑完，再断言"没发生"。
const flush = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })

// 提交类用例不关心打开时的 /me 同步：让它失败（会被静默忽略），免得多出的 renameUser 调用混进断言；
// 同步本身由文末几条专门的用例覆盖。每次新建，不跨用例共用 Response。
const meDown = () => ({ 'GET /me': apiError(500, 'INTERNAL', '服务器内部错误') })

test('登录名预填当前值', () => {
  stubApi(meDown())
  render(<ChangeAccountDialog />)
  expect(valueOf('登录名')).toBe('admin')
})

test('两次新密码不一致时不发请求并提示', async () => {
  const calls = stubApi(meDown())
  render(<ChangeAccountDialog />)
  fill('旧密码', 'admin')
  fill('新密码', 'aaa')
  fill('确认新密码', 'bbb')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.error).toHaveBeenCalled())
  expect(String(mocks.error.mock.calls[0][0])).toContain('不一致')
  expect(putCalls(calls)).toEqual([])
})

test('提交后同步登录名并关闭对话框', async () => {
  const calls = stubApi({ ...meDown(), 'PUT /me': { token: 't', username: 'boss' } })
  render(<ChangeAccountDialog />)
  fill('登录名', 'boss')
  fill('旧密码', 'admin')
  fill('新密码', 'n3w-pass')
  fill('确认新密码', 'n3w-pass')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.renameUser).toHaveBeenCalledWith('boss'))
  expect(putCalls(calls)).toEqual([
    { method: 'PUT', url: '/me', body: { username: 'boss', oldPassword: 'admin', newPassword: 'n3w-pass' } },
  ])
  expect(mocks.setAccountDialogOpen).toHaveBeenCalledWith(false)
  expect(mocks.success).toHaveBeenCalled()
})

test('新密码留空时只改登录名', async () => {
  const calls = stubApi({ ...meDown(), 'PUT /me': { token: 't', username: 'root' } })
  render(<ChangeAccountDialog />)
  fill('登录名', 'root')
  fill('旧密码', 'admin')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(putCalls(calls)).toHaveLength(1))
  expect(putCalls(calls)[0].body).toEqual({ username: 'root', oldPassword: 'admin', newPassword: '' })
})

// 旧密码错是 400，对话框必须留着让人重填。useAuth 在本文件里是 mock，没有真 Provider，
// 所以靠 spy 顶住 api.ts 的全局 401 回调（真实应用里它会把用户踢回登录页）：
// 400 不该触发它，响应一旦变成 401 这条就会红。
test('旧密码错误时提示原因并保持对话框打开，且不触发全局 401 回调', async () => {
  const onUnauthorized = vi.fn()
  setUnauthorizedHandler(onUnauthorized)
  stubApi({ ...meDown(), 'PUT /me': apiError(400, 'ADMIN_OLD_PASSWORD_WRONG', '旧密码不正确') })
  render(<ChangeAccountDialog />)
  fill('旧密码', 'nope')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith('旧密码不正确'))
  expect(onUnauthorized).not.toHaveBeenCalled()
  expect(mocks.renameUser).not.toHaveBeenCalled()
  expect(mocks.setAccountDialogOpen).not.toHaveBeenCalledWith(false)
})

// 以下几条钉「打开时先向后端取当前登录名」。场景：同一浏览器的另一个标签页已经把名字改成 boss，
// cookie 共用、本页不掉线，但上下文（上面的 mock，初始 admin）里还是旧名。

// 这是原始缺陷本身：不同步的话，这里只改密码提交上去的 username 是 admin，后端会把名字悄悄改回去。
test('打开时先取当前登录名：回填新名，只改密码也不会把名字改回去', async () => {
  const calls = stubApi({
    'GET /me': { id: '1', username: 'boss' },
    'PUT /me': { token: 't', username: 'boss' },
  })
  render(<ChangeAccountDialog />)

  await waitFor(() => expect(valueOf('登录名')).toBe('boss'))
  expect(mocks.renameUser).toHaveBeenCalledWith('boss')

  fill('旧密码', 'admin')
  fill('新密码', 'n3w-pass')
  fill('确认新密码', 'n3w-pass')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(mocks.success).toHaveBeenCalled())
  expect(putCalls(calls)).toEqual([
    { method: 'PUT', url: '/me', body: { username: 'boss', oldPassword: 'admin', newPassword: 'n3w-pass' } },
  ])

  // 同步到新名字之后不能再多取一次 /me：登录名若进了同步 effect 的依赖，名字每变一次就会
  // 再请求一次，effect 就成了"取名字 → 改名字 → 再取名字"的环，只是靠名字相同才收住。
  await flush()
  expect(calls.filter((c) => c.method === 'GET')).toHaveLength(1)
})

test('打开时取登录名失败（500）：静默忽略，不改登录名、不弹错误，表单照常可提交', async () => {
  const calls = stubApi({ ...meDown(), 'PUT /me': { token: 't', username: 'admin' } })
  render(<ChangeAccountDialog />)
  await flush()

  expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual(['GET /me'])
  expect(mocks.renameUser).not.toHaveBeenCalled()
  expect(mocks.error).not.toHaveBeenCalled()
  expect(valueOf('登录名')).toBe('admin')

  fill('旧密码', 'admin')
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(mocks.success).toHaveBeenCalled())
  expect(putCalls(calls)[0].body).toEqual({ username: 'admin', oldPassword: 'admin', newPassword: '' })
})

test('对话框在 /me 返回前就被关掉：迟到的响应不再改登录名', async () => {
  const calls = stubApi({ 'GET /me': { id: '1', username: 'boss' } })
  const { rerender } = render(<ChangeAccountDialog />)
  // render 与 rerender 在同一个同步段里：此刻 /me 的响应还没来得及处理，对话框就已经关了。
  mocks.open = false
  rerender(<ChangeAccountDialog />)
  await flush()

  expect(calls).toHaveLength(1)
  expect(mocks.renameUser).not.toHaveBeenCalled()
})

test('/me 返回的名字与上下文一致：不清掉已经开始填写的内容', async () => {
  const calls = stubApi({ 'GET /me': { id: '1', username: 'admin' } })
  render(<ChangeAccountDialog />)
  // 同一个同步段里填好：此刻 /me 的响应还没处理。
  fill('旧密码', 'typed-early')
  await flush()

  expect(calls).toHaveLength(1)
  expect(valueOf('旧密码')).toBe('typed-early')
})

test('对话框关着时不请求（Layout 里它常驻挂载），打开的那一刻才取', async () => {
  mocks.open = false
  const calls = stubApi({ 'GET /me': { id: '1', username: 'boss' } })
  const { rerender } = render(<ChangeAccountDialog />)
  await flush()
  expect(calls).toEqual([])

  mocks.open = true
  rerender(<ChangeAccountDialog />)
  await waitFor(() => expect(mocks.renameUser).toHaveBeenCalledWith('boss'))
  expect(calls).toHaveLength(1)
})
