import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import SystemConfigVersions from './SystemConfigVersions'
import type { ConfigVersion, ConfigSnapshot } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

const versions: ConfigVersion[] = [
  { seq: 2, createdAt: 2000 },
  { seq: 1, createdAt: 1000 },
]

const snapshots: Record<number, ConfigSnapshot> = {
  1: { seq: 1, value: 'env: dev\n' },
  2: { seq: 2, value: 'env: prod\n' },
}

function stub(calls: Array<{ method: string; url: string; body: unknown }> = []) {
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const body = init?.body ? (JSON.parse(init.body as string) as unknown) : undefined
    calls.push({ method, url, body })

    if (url.endsWith('/system-config/versions')) return jsonResponse(versions)
    if (method === 'POST' && url.endsWith('/rollback')) {
      const seq = (body as { seq: number }).seq
      return jsonResponse({ seq: seq === 2 ? 2 : 3 })
    }
    const m = /\/system-config\/versions\/(\d+)$/.exec(url)
    if (m) return jsonResponse(snapshots[Number(m[1])])
    return jsonResponse({})
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function renderPage() {
  return render(
    <MemoryRouter>
      <SystemConfigVersions />
    </MemoryRouter>,
  )
}

test('列出版本，最新一条标为当前版本', async () => {
  stub()
  renderPage()
  await waitFor(() => expect(screen.getByTestId('version-2')).toBeTruthy())
  expect(screen.getByTestId('version-2').textContent).toContain('当前版本')
  expect(screen.getByTestId('version-1').textContent).not.toContain('当前版本')
})

test('点查看弹出该版本的原文', async () => {
  stub()
  renderPage()
  await waitFor(() => expect(screen.getByTestId('version-1')).toBeTruthy())

  const row = screen.getByTestId('version-1')
  fireEvent.click(within(row).getByRole('button', { name: '查看' }))

  await waitFor(() => expect(screen.getByText('env: dev')).toBeTruthy())
})

test('当前版本没有回滚按钮，其余版本有', async () => {
  stub()
  renderPage()
  await waitFor(() => expect(screen.getByTestId('version-2')).toBeTruthy())
  expect(within(screen.getByTestId('version-2')).queryByRole('button', { name: /回滚到/ })).toBeNull()
  expect(within(screen.getByTestId('version-1')).getByRole('button', { name: '回滚到 v1' })).toBeTruthy()
})

test('点回滚后二次确认，确认后提交 POST /rollback', async () => {
  const calls: Array<{ method: string; url: string; body: unknown }> = []
  stub(calls)
  renderPage()
  await waitFor(() => expect(screen.getByTestId('version-1')).toBeTruthy())

  fireEvent.click(within(screen.getByTestId('version-1')).getByRole('button', { name: '回滚到 v1' }))
  const dialog = await screen.findByRole('dialog')
  fireEvent.click(within(dialog).getByRole('button', { name: '确认回滚' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/rollback'))).toBe(true))
  const rollback = calls.find((c) => c.method === 'POST')!.body
  expect(rollback).toEqual({ seq: 1 })
})
