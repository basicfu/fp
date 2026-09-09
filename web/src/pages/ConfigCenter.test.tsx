// 【与 brief 的出入】brief 给的示例代码 import 了
// `@testing-library/user-event`，但这个包**不在** package.json 的
// devDependencies 里，加进去会违反"不引入新依赖"的硬约束。改用仓库里
// 已经在用的 `fireEvent`（同样来自 @testing-library/react）。
import { disabledIMConfig } from '@/lib/testFixtures'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { toast } from 'sonner'
import ConfigCenter from './ConfigCenter'
import { CurrentAppContext } from '@/lib/current-app'
import type { CurrentAppValue } from '@/lib/current-app'
import type { Application, ConfigSnapshot } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/**
 * stubConfig 是个有状态的假后端，按分区名分别维护一份快照，支持这个页面
 * 用到的四个接口：
 * - GET    .../config?type=X       → state[X] ?? { seq: 0, value: '' }
 * - GET    .../config/types        → 已经存过版本的分区名列表（可能包含
 *   或不包含 DEFAULT——不影响断言，useConfigTypes 自己会去重、把 DEFAULT
 *   排到最前面）
 * - PUT    .../config              → 按 body.type 各自的 seq 自增
 * - DELETE .../config?type=X       → 从 state 里彻底删掉这个分区
 *
 * calls 记下每次请求的 method 与解析后的 body，供断言用。
 */
function stubConfig(initial: Record<string, ConfigSnapshot>, calls: Array<{ method: string; body: unknown }> = []) {
  const state: Record<string, ConfigSnapshot> = { ...initial }
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const body = init?.body ? (JSON.parse(init.body as string) as unknown) : undefined
    calls.push({ method, body })

    const type = new URL(url, 'http://x').searchParams.get('type') ?? ''

    if (method === 'PUT') {
      const put = body as { type: string; value: string }
      const nextSeq = (state[put.type]?.seq ?? 0) + 1
      state[put.type] = { seq: nextSeq, value: put.value }
      return jsonResponse({ seq: nextSeq })
    }
    if (method === 'DELETE') {
      delete state[type]
      return new Response(null, { status: 204 })
    }
    if (url.includes('/config/types')) {
      return jsonResponse(Object.keys(state).sort())
    }
    return jsonResponse(state[type] ?? { seq: 0, value: '' })
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

const fixedApp: Application = {
  id: 'app-1',
  name: '固定应用',
  slug: 'fixed',
  appId: 'appid-1',
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

function currentAppValue(): CurrentAppValue {
  return {
    apps: [fixedApp],
    currentAppId: fixedApp.id,
    currentApp: fixedApp,
    setCurrentAppId: () => {},
    loading: false,
    error: '',
    reload: () => {},
  }
}

function renderPage() {
  return render(
    <MemoryRouter>
      <CurrentAppContext.Provider value={currentAppValue()}>
        <ConfigCenter />
      </CurrentAppContext.Provider>
    </MemoryRouter>,
  )
}

function yamlBox(): HTMLTextAreaElement {
  return screen.getByRole('textbox') as HTMLTextAreaElement
}

test('提供入口跳到版本历史页', async () => {
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } })
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  const link = screen.getByRole('link', { name: '版本历史' })
  expect(link.getAttribute('href')).toBe('/config/versions')
})

test('加载出来的 YAML 原文（含注释）原样显示在编辑框里', async () => {
  stubConfig({ DEFAULT: { seq: 1, value: 'fee_rate: 0.02 # 手续费率\napi_key: # 上游密钥，先占个位\n' } })
  renderPage()

  await waitFor(() =>
    expect(yamlBox().value).toBe('fee_rate: 0.02 # 手续费率\napi_key: # 上游密钥，先占个位\n'),
  )
})

test('点"仅保存"提交 push=false，点"保存并推送"提交 push=true', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } }, calls)
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  fireEvent.change(yamlBox(), { target: { value: 'a: 9\n' } })
  fireEvent.click(screen.getByRole('button', { name: '仅保存' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
  const first = calls.find((c) => c.method === 'PUT')!.body as { type: string; push: boolean; value: string }
  expect(first.type).toBe('DEFAULT')
  expect(first.push).toBe(false)
  expect(first.value).toBe('a: 9\n')

  // 【辨别力】不能只等编辑框显示 'a: 9\n' 就继续——那正好是这次编辑框里
  // 打的值，reload 触发的 effect 还没跑完时编辑框看起来也是这个值，两者
  // 用同一个断言分不出来。等"仅保存"按钮重新变回禁用（dirty 归零）才是
  // 真正等到了 reload+effect 都落地：这里踩过一次真实的坑——用编辑框
  // 的值当完成信号，第二次编辑会在 reload 的 effect 事后才触发、把刚打
  // 的新值又冲掉。
  await waitFor(() => expect((screen.getByRole('button', { name: '仅保存' }) as HTMLButtonElement).disabled).toBe(true))
  fireEvent.change(yamlBox(), { target: { value: 'a: 10\n' } })
  fireEvent.click(screen.getByRole('button', { name: '保存并推送' }))

  await waitFor(() => expect(calls.filter((c) => c.method === 'PUT').length).toBe(2))
  const second = calls.filter((c) => c.method === 'PUT')[1]!.body as { push: boolean }
  expect(second.push).toBe(true)
})

test('没有改动时两个保存按钮都禁用', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } }, calls)
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  expect((screen.getByRole('button', { name: '仅保存' }) as HTMLButtonElement).disabled).toBe(true)
  expect((screen.getByRole('button', { name: '保存并推送' }) as HTMLButtonElement).disabled).toBe(true)
})

// 校验失败不再常驻显示成一段 <p>（那会让下面的按钮跟着内容一跳一跳），
// 边框变红是唯一的实时反馈；具体原因等点了保存才用 toast 报——按钮本身
// 不因为"当前不合法"就被禁用（不合法但内容是脏的，允许点，点了才挡）。
test('YAML 语法不合法时，边框变红但按钮仍可点；点保存会被 toast 挡下，不会真的提交', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } }, calls)
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  fireEvent.change(yamlBox(), { target: { value: 'a:\n  b: 1\n c: 2\n' } })

  expect(document.querySelector('p.text-destructive')).toBeNull()
  expect(yamlBox().className).toContain('border-destructive')
  const saveBtn = screen.getByRole('button', { name: '仅保存' }) as HTMLButtonElement
  expect(saveBtn.disabled).toBe(false)

  fireEvent.click(saveBtn)
  expect(errorSpy).toHaveBeenCalled()
  expect(calls.some((c) => c.method === 'PUT')).toBe(false)
})

test('顶层不是映射时点保存同样会被 toast 挡下', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } }, calls)
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  fireEvent.change(yamlBox(), { target: { value: '- a\n- b\n' } })
  fireEvent.click(screen.getByRole('button', { name: '仅保存' }))

  await waitFor(() => expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('顶层必须是一个映射')))
  expect(calls.some((c) => c.method === 'PUT')).toBe(false)
})

// Tab 在这个编辑框里是缩进两个空格，不是把焦点移到下一个控件——不然
// 敲代码时按一下 Tab，光标直接飞出编辑框，完全没法用。
test('编辑框里按 Tab 插入两个空格，不移动焦点', async () => {
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } })
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  const box = yamlBox()
  box.focus()
  box.setSelectionRange(box.value.length, box.value.length)
  fireEvent.keyDown(box, { key: 'Tab' })

  await waitFor(() => expect(box.value).toBe('a: 1\n  '))
  expect(document.activeElement).toBe(box)
})

// 编辑框曾经只有 min-h（可以无限拖高），拖过视口高度会把下面"仅保存/
// 保存并推送/删除"这一排按钮顶没了。max-h 把它钉死在默认高度。
test('编辑框有最高高度限制，不会无限拖高', async () => {
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } })
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  expect(yamlBox().className).toContain('max-h-[60vh]')
})

// port:4379 这种冒号后漏空格的写法，本来会被判成"顶层不是映射"（整行被
// 当成一个裸标量），现在打字校验和真正提交都会自动补上那个空格，不该
// 挡住保存。
test('冒号后没有空格（如 port:4379）也允许保存，不报错', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } }, calls)
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  fireEvent.change(yamlBox(), { target: { value: 'port:4379\n' } })

  expect(document.querySelector('p.text-destructive')).toBeNull()
  const saveBtn = screen.getByRole('button', { name: '仅保存' }) as HTMLButtonElement
  expect(saveBtn.disabled).toBe(false)
  fireEvent.click(saveBtn)

  await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
})

test('切换分区会重新拉取', async () => {
  const urls: string[] = []
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    if (!init?.method || init.method === 'GET') urls.push(url)
    if (url.includes('/config/types')) return jsonResponse(['DEFAULT', 'WEB'])
    return jsonResponse({ seq: 1, value: 'a: 1\n' })
  })
  vi.stubGlobal('fetch', fn)
  renderPage()

  const tab = await screen.findByRole('tab', { name: 'WEB' })
  fireEvent.click(tab)

  await waitFor(() => expect(urls.some((u) => u.includes('/config?type=WEB'))).toBe(true))
  // 【辨别力】要断言 DEFAULT 也被拉过——只断言 WEB 的话，
  // 一个把 type 写死成 WEB 的实现同样会绿。
  expect(urls.some((u) => u.includes('/config?type=DEFAULT'))).toBe(true)
})

test('保存失败时 toast 提示，编辑框内容不变', async () => {
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } })
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    if ((init?.method ?? 'GET') === 'PUT') {
      return new Response(JSON.stringify({ code: 'INTERNAL', msg: '存储暂时不可用' }), { status: 500 })
    }
    if (url.includes('/config/types')) return jsonResponse(['DEFAULT'])
    return jsonResponse({ seq: 1, value: 'a: 1\n' })
  })
  vi.stubGlobal('fetch', fn)
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  fireEvent.change(yamlBox(), { target: { value: 'a: 9\n' } })
  fireEvent.click(screen.getByRole('button', { name: '仅保存' }))

  await waitFor(() => expect(errorSpy).toHaveBeenCalled())
  expect(yamlBox().value).toBe('a: 9\n')
})

// 点"+"建一个新分区：名字随便起，确认后立刻落一个空值版本（真正存进
// 数据库），标签页变多、自动切到新分区。
test('点加号新建分区，确认后标签页变多并切到新分区', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } }, calls)
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  fireEvent.click(screen.getByRole('button', { name: '新建分区' }))
  fireEvent.change(await screen.findByLabelText('分区名'), { target: { value: 'MOBILE' } })
  fireEvent.click(screen.getByRole('button', { name: '确认' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
  const put = calls.find((c) => c.method === 'PUT')!.body as { type: string; value: string; push: boolean }
  expect(put).toEqual({ type: 'MOBILE', value: '', push: false })

  await waitFor(() => expect(screen.getByRole('tab', { name: 'MOBILE' }).getAttribute('aria-selected')).toBe('true'))
})

test('新建分区名字不合法时用 toast 提示，不创建', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } }, calls)
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  fireEvent.click(screen.getByRole('button', { name: '新建分区' }))
  fireEvent.change(await screen.findByLabelText('分区名'), { target: { value: '1mobile' } })
  fireEvent.click(screen.getByRole('button', { name: '确认' }))

  expect(errorSpy).toHaveBeenCalled()
  expect(calls.some((c) => c.method === 'PUT')).toBe(false)
})

test('新建分区名字已存在时用 toast 提示，不创建', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } }, calls)
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  fireEvent.click(screen.getByRole('button', { name: '新建分区' }))
  fireEvent.change(await screen.findByLabelText('分区名'), { target: { value: 'DEFAULT' } })
  fireEvent.click(screen.getByRole('button', { name: '确认' }))

  await waitFor(() => expect(errorSpy).toHaveBeenCalledWith('这个分区已经存在'))
  expect(calls.some((c) => c.method === 'PUT')).toBe(false)
})

// 删除非 DEFAULT 分区：二次确认，确认后发 DELETE、切回 DEFAULT 标签页。
test('删除非 DEFAULT 分区需要二次确认，确认后发 DELETE 并切回 DEFAULT', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' }, WEB: { seq: 1, value: 'b: 1\n' } }, calls)
  renderPage()

  fireEvent.click(await screen.findByRole('tab', { name: 'WEB' }))
  await waitFor(() => expect(yamlBox().value).toBe('b: 1\n'))

  fireEvent.click(screen.getByRole('button', { name: '删除' }))
  const dialog = await screen.findByRole('dialog')
  expect(dialog.textContent).toContain('不可撤销')
  fireEvent.click(screen.getByRole('button', { name: '确认删除' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'DELETE')).toBe(true))
  await waitFor(() => expect(screen.getByRole('tab', { name: 'DEFAULT' }).getAttribute('aria-selected')).toBe('true'))
})

// 删掉 DEFAULT 分区的全部历史之后，DEFAULT 标签页依然要展示（它是应用
// 天然就有的分区），只是内容变回空——不会因为删完了就从标签页里消失。
test('删除 DEFAULT 分区后，DEFAULT 标签页仍然展示，内容变空', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ DEFAULT: { seq: 1, value: 'a: 1\n' } }, calls)
  renderPage()

  await waitFor(() => expect(yamlBox().value).toBe('a: 1\n'))
  fireEvent.click(screen.getByRole('button', { name: '删除' }))
  fireEvent.click(await screen.findByRole('button', { name: '确认删除' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'DELETE')).toBe(true))
  expect(await screen.findByRole('tab', { name: 'DEFAULT' })).toBeTruthy()
  await waitFor(() => expect(yamlBox().value).toBe(''))
})
