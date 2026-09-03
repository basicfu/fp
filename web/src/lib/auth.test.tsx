import { useEffect } from 'react'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
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
