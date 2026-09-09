import { disabledIMConfig } from '@/lib/testFixtures'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { CurrentAppProvider, useCurrentApp } from './current-app'
import type { Application } from './types'

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
})

const appA: Application = {
  id: 'app-a',
  name: 'A应用',
  slug: 'a',
  appId: 'appid-a',
  status: 'ACTIVE',
  cookieDomain: '',
  defaultRoleKey: '',
  session: {
    idleTimeoutSeconds: 1,
    idleTimeoutMobileSeconds: 0,
    maxLifetimeSeconds: 1,
    rotateIntervalSeconds: 1,
    extendIntervalSeconds: 1,
    tokenCacheTtlSeconds: 1,
  },
  im: disabledIMConfig,
  createdAt: 1,
  updatedAt: 1,
}
const appB: Application = { ...appA, id: 'app-b', name: 'B应用', slug: 'b' }

function stubFetchSequence(...responses: Response[]) {
  const fn = vi.fn()
  for (const r of responses) fn.mockResolvedValueOnce(r)
  vi.stubGlobal('fetch', fn)
  return fn
}

function Probe() {
  const { apps, currentAppId, currentApp, setCurrentAppId } = useCurrentApp()
  return (
    <div>
      <div data-testid="count">{apps.length}</div>
      <div data-testid="current-id">{currentAppId}</div>
      <div data-testid="current-name">{currentApp?.name ?? ''}</div>
      {apps.map((a) => (
        <button key={a.id} onClick={() => setCurrentAppId(a.id)}>
          选 {a.name}
        </button>
      ))}
    </div>
  )
}

test('没有记住任何选择时，默认选中列表第一个应用', async () => {
  stubFetchSequence(new Response(JSON.stringify([appA, appB]), { status: 200 }))
  render(
    <CurrentAppProvider>
      <Probe />
    </CurrentAppProvider>,
  )

  await waitFor(() => expect(screen.getByTestId('current-id').textContent).toBe('app-a'))
  expect(screen.getByTestId('current-name').textContent).toBe('A应用')
})

test('localStorage 里记住的 id 还在列表里时，用它而不是第一个', async () => {
  localStorage.setItem('fp-current-app-id', 'app-b')
  stubFetchSequence(new Response(JSON.stringify([appA, appB]), { status: 200 }))
  render(
    <CurrentAppProvider>
      <Probe />
    </CurrentAppProvider>,
  )

  await waitFor(() => expect(screen.getByTestId('current-id').textContent).toBe('app-b'))
})

test('记住的 id 已经不在最新列表里时（应用被删），回退到第一个', async () => {
  localStorage.setItem('fp-current-app-id', 'app-deleted')
  stubFetchSequence(new Response(JSON.stringify([appA, appB]), { status: 200 }))
  render(
    <CurrentAppProvider>
      <Probe />
    </CurrentAppProvider>,
  )

  await waitFor(() => expect(screen.getByTestId('current-id').textContent).toBe('app-a'))
})

test('切换当前应用会写入 localStorage', async () => {
  stubFetchSequence(new Response(JSON.stringify([appA, appB]), { status: 200 }))
  render(
    <CurrentAppProvider>
      <Probe />
    </CurrentAppProvider>,
  )

  await waitFor(() => expect(screen.getByTestId('current-id').textContent).toBe('app-a'))
  fireEvent.click(screen.getByRole('button', { name: '选 B应用' }))

  await waitFor(() => expect(screen.getByTestId('current-id').textContent).toBe('app-b'))
  expect(localStorage.getItem('fp-current-app-id')).toBe('app-b')
})

test('useCurrentApp 在 Provider 之外调用会抛错', () => {
  function Bare() {
    useCurrentApp()
    return null
  }
  expect(() => render(<Bare />)).toThrow('useCurrentApp 必须在 CurrentAppProvider 内使用')
})
