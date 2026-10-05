import { useEffect } from 'react'
import { test, expect, vi, afterEach } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { api } from './api'
import { AuthProvider, useAuth } from './auth'

afterEach(() => vi.unstubAllGlobals())

function Probe() {
  const { status, username } = useAuth()
  return <div data-testid="probe">{status}:{username ?? '-'}</div>
}

test('挂载时探测 /me，已登录则进入 authed', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ id: '1', username: 'admin' }), { status: 200 }),
  ))

  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('authed:admin'))
})

// 【辨别力】未登录时 /me 返回 401。若实现把 401 当成"加载中"或直接抛到
// 顶层，界面会永远停在骨架屏上——用户看到的是一个转不完的圈，而不是登录页。
test('未登录时 /me 返回 401，落到 anon 而不是卡在 loading', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ error: '未登录' }), { status: 401 }),
  ))

  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('anon:-'))
})

// 网络断了、后端 500——同样必须落到 anon，让用户看到登录页并能重试，
// 而不是白屏。
test('探测失败时同样落到 anon', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network down')))

  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('anon:-'))
})

// LogoutProbe 把 logout 函数本身暴露给测试（挂在 holder 上），这样测试才能
// 直接 await 它返回的 Promise，而不只是观察 status 状态。
//
// 这一点很关键：/logout 返回 401 时，api.ts 的全局 401 回调
//（setUnauthorizedHandler 注册的那个）会独立地把 status 打成 anon——与
// logout() 内部是否 catch 了自己的异常无关。只断言 status 最终变成 anon，
// 在 logout() 缺 catch 的 bug 存在时也会通过，起不到辨别作用。真正暴露这个
// bug 的是 logout() 返回的 Promise 本身：没有 catch 时它会 reject（并在
// Layout.tsx 的 void logout() 调用点变成控制台里一条未处理的 rejection）。
function LogoutProbe({ holder }: { holder: { logout?: () => Promise<void> } }) {
  const { status, username, logout } = useAuth()
  // 在 effect 里赋值而不是渲染期间直接赋值：渲染函数应该是纯的，
  // "把值暴露给测试"这个副作用放到 commit 之后做更符合 React 的语义。
  useEffect(() => {
    holder.logout = logout
  }, [holder, logout])
  return <div data-testid="probe">{status}:{username ?? '-'}</div>
}

test('调用 logout 后状态回到 anon', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: '1', username: 'admin' }), { status: 200 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 })),
  )

  const holder: { logout?: () => Promise<void> } = {}
  render(<AuthProvider><LogoutProbe holder={holder} /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('authed:admin'))

  await expect(holder.logout!()).resolves.toBeUndefined()
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('anon:-'))
})

// 【辨别力】后端 /logout 挂在 requireAdmin 中间件之后：管理端会话 cookie
// 一旦过期，这个请求本身就会先被中间件拦成 401（这正是 logout() 原来那句
// "cookie 可能已经过期"注释预设的主场景）。此时 api.post('/logout') 会
// throw ApiError——如果 logout() 只用 try/finally 不用 catch，这个异常会
// 从 logout() 里重新抛出，调用方（Layout.tsx 的 void logout()）没有接住，
// 变成控制台里一条 Uncaught (in promise) ApiError。
//
// 断言 resolves 而不是 rejects：这才是这条测试的辨别力所在——只看 status
// 是否变成 anon 测不出这个 bug（见上面 LogoutProbe 的注释）。
test('后端 logout 返回 401 时，状态仍回到 anon，且 logout() 本身不 reject', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: '1', username: 'admin' }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: '管理端凭据无效或已过期' }), { status: 401 })),
  )

  const holder: { logout?: () => Promise<void> } = {}
  render(<AuthProvider><LogoutProbe holder={holder} /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('authed:admin'))

  await expect(holder.logout!()).resolves.toBeUndefined()
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('anon:-'))
})

function LoginProbe({ holder }: { holder: { login?: (u: string, p: string) => Promise<{ defaultPassword: boolean }> } }) {
  const { status, username, login } = useAuth()
  useEffect(() => {
    holder.login = login
  }, [holder, login])
  return <div data-testid="probe">{status}:{username ?? '-'}</div>
}

test('login 返回后端给的 defaultPassword 并进入 authed', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ msg: '未登录' }), { status: 401 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ token: 't', username: 'admin', defaultPassword: true }), { status: 200 })),
  )
  const holder: { login?: (u: string, p: string) => Promise<{ defaultPassword: boolean }> } = {}
  render(<AuthProvider><LoginProbe holder={holder} /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('anon:-'))

  await expect(holder.login!('admin', 'admin')).resolves.toEqual({ defaultPassword: true })
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('authed:admin'))
})

// 下面几条用真实的 AuthProvider 钉 Provider 自己的状态转换（组件测试里的 useAuth 都是
// mock，覆盖不到这里）。探针不往外塞 holder：往 props 里的对象赋值会被 oxlint 的
// react(immutability) 报警，所以值靠渲染读、动作靠点按钮触发。
function StateProbe() {
  const { status, username, accountDialogOpen, setAccountDialogOpen, renameUser, login, logout } = useAuth()
  return (
    <div>
      <div data-testid="probe">{status}:{username ?? '-'}</div>
      <div data-testid="dialog">{accountDialogOpen ? 'open' : 'closed'}</div>
      <button onClick={() => setAccountDialogOpen(true)}>open-dialog</button>
      <button onClick={() => renameUser('boss')}>rename</button>
      <button onClick={() => void login('admin', 'pw')}>login</button>
      <button onClick={() => void logout()}>logout</button>
    </div>
  )
}

// 【辨别力】会话过期时开关必须随状态一起复位：否则下次登录一成功，Layout 里的对话框
// 就会自己弹出来。先断言开关确实是开着的，免得"本来就是关的"让末尾的断言空过。
test('任意请求返回 401：状态回到 anon，且「修改密码」对话框开关被复位', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: '1', username: 'admin' }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ msg: '管理端登录已过期' }), { status: 401 })),
  )
  render(<AuthProvider><StateProbe /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('authed:admin'))

  fireEvent.click(screen.getByRole('button', { name: 'open-dialog' }))
  expect(screen.getByTestId('dialog').textContent).toBe('open')

  // 任何一个经由 api 发出的请求答 401，走的都是 Provider 注册的那个全局回调。
  await act(async () => {
    await expect(api.get('/anything')).rejects.toMatchObject({ status: 401 })
  })
  expect(screen.getByTestId('probe').textContent).toBe('anon:-')
  expect(screen.getByTestId('dialog').textContent).toBe('closed')
})

// 【辨别力】/logout 这里回 204，碰不到全局 401 回调——能把开关复位的只有 logout()
// 自己的 finally，所以这条钉的就是那一行。
test('logout 之后「修改密码」对话框开关被复位', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: '1', username: 'admin' }), { status: 200 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 })),
  )
  render(<AuthProvider><StateProbe /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('authed:admin'))

  fireEvent.click(screen.getByRole('button', { name: 'open-dialog' }))
  expect(screen.getByTestId('dialog').textContent).toBe('open')

  fireEvent.click(screen.getByRole('button', { name: 'logout' }))
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('anon:-'))
  expect(screen.getByTestId('dialog').textContent).toBe('closed')
})

test('renameUser 只改显示的登录名，会话仍是 authed', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ id: '1', username: 'admin' }), { status: 200 })))
  render(<AuthProvider><StateProbe /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('authed:admin'))

  fireEvent.click(screen.getByRole('button', { name: 'rename' }))
  expect(screen.getByTestId('probe').textContent).toBe('authed:boss')
})

// 【辨别力】未登录时登录页上遗留的"去修改"提示还能把开关置 true；这次登录成功后它若
// 还开着，Layout 一挂载对话框就会自己弹出来。
test('login 会复位遗留的「修改密码」对话框开关', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ msg: '未登录' }), { status: 401 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ token: 't', username: 'admin', defaultPassword: false }), { status: 200 })),
  )
  render(<AuthProvider><StateProbe /></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('anon:-'))

  fireEvent.click(screen.getByRole('button', { name: 'open-dialog' }))
  expect(screen.getByTestId('dialog').textContent).toBe('open')

  fireEvent.click(screen.getByRole('button', { name: 'login' }))
  await waitFor(() => expect(screen.getByTestId('probe').textContent).toBe('authed:admin'))
  expect(screen.getByTestId('dialog').textContent).toBe('closed')
})
