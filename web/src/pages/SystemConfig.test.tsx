import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { toast } from 'sonner'
import SystemConfig from './SystemConfig'
import type { ConfigSnapshot } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/** stubSystemConfig 是个有状态的假后端，支持这个页面用到的两个接口。 */
function stubSystemConfig(initial: ConfigSnapshot, calls: Array<{ method: string; body: unknown }> = []) {
  let state: ConfigSnapshot = { ...initial }
  const fn = vi.fn(async (_url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const body = init?.body ? (JSON.parse(init.body as string) as unknown) : undefined
    calls.push({ method, body })
    if (method === 'PUT') {
      const put = body as { value: string }
      state = { seq: state.seq + 1, value: put.value }
      return jsonResponse({ seq: state.seq })
    }
    return jsonResponse(state)
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function yamlBox(): HTMLTextAreaElement {
  return screen.getByRole('textbox') as HTMLTextAreaElement
}

function renderPage() {
  return render(
    <MemoryRouter>
      <SystemConfig />
    </MemoryRouter>,
  )
}

test('加载出来的 YAML 原文原样显示在编辑框里', async () => {
  stubSystemConfig({ seq: 1, value: 'env: dev\n' })
  renderPage()
  await waitFor(() => expect(yamlBox().value).toBe('env: dev\n'))
})

test('没有改动时保存按钮禁用', async () => {
  stubSystemConfig({ seq: 1, value: 'env: dev\n' })
  renderPage()
  await waitFor(() => expect(yamlBox().value).toBe('env: dev\n'))
  expect((screen.getByRole('button', { name: '保存' }) as HTMLButtonElement).disabled).toBe(true)
})

test('点保存提交 PUT，body 只有 value 字段', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubSystemConfig({ seq: 1, value: 'env: dev\n' }, calls)
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('env: dev\n'))
  fireEvent.change(yamlBox(), { target: { value: 'env: prod\n' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
  const put = calls.find((c) => c.method === 'PUT')!.body
  expect(put).toEqual({ value: 'env: prod\n' })
})

test('YAML 不合法时点保存被 toast 挡下，不提交', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubSystemConfig({ seq: 1, value: 'env: dev\n' }, calls)
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('env: dev\n'))
  fireEvent.change(yamlBox(), { target: { value: '- a\n- b\n' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(errorSpy).toHaveBeenCalled())
  expect(calls.some((c) => c.method === 'PUT')).toBe(false)
})

test('提供入口跳到版本历史页', async () => {
  stubSystemConfig({ seq: 1, value: 'env: dev\n' })
  renderPage()
  await waitFor(() => expect(yamlBox().value).toBe('env: dev\n'))
  const link = screen.getByRole('link', { name: '版本历史' })
  expect(link.getAttribute('href')).toBe('/system-config/versions')
})
