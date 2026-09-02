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
