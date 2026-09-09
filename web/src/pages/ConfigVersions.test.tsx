// 仓库**没有**装 @testing-library/user-event（不在 package.json 里），
// 用既有的 fireEvent，写法照 ApplicationSettings.test.tsx。
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { toast } from 'sonner'
import ConfigVersions from './ConfigVersions'
import { CurrentAppContext } from '@/lib/current-app'
import type { CurrentAppValue } from '@/lib/current-app'
import type { Application, ConfigSnapshot, ConfigVersion } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/**
 * stubVersions 支持版本历史页用到的三个接口：
 * - GET  .../config/versions        → list（不管 query 里的 type 是什么，
 *   固定返回同一份列表——分区只影响 URL，不影响这里的假数据）
 * - GET  .../config/versions/{seq}  → snapshots[seq]；snapshots 里没有的
 *   seq 一律 404，对应后端 domain.CodeConfigVersionNotFound（版本号从未
 *   用过，或者已经被 ConfigService 的修剪逻辑删掉），前端拿它判定
 *   "初始版本"。
 * - POST .../config/rollback        → { seq: 下一个版本号 }，具体值测试
 *   不关心，只关心请求发没发、body 是什么。
 *
 * calls 记下每次请求的 method 与解析后的 body（GET 没有 body，记
 * undefined），供断言用。
 */
function stubVersions(
  list: ConfigVersion[],
  snapshots: Record<number, ConfigSnapshot>,
  calls: Array<{ method: string; body: unknown }> = [],
) {
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const body = init?.body ? (JSON.parse(init.body as string) as unknown) : undefined
    calls.push({ method, body })

    if (method === 'POST' && url.includes('/config/rollback')) {
      const maxSeq = Math.max(0, ...list.map((v) => v.seq))
      return jsonResponse({ seq: maxSeq + 1 })
    }
    if (url.includes('/config/types')) {
      return jsonResponse(['DEFAULT'])
    }
    const versionMatch = /\/config\/versions\/(\d+)/.exec(url)
    if (versionMatch) {
      const seq = Number(versionMatch[1])
      const snap = snapshots[seq]
      if (!snap) return jsonResponse({ code: 'CONFIG_VERSION_NOT_FOUND', msg: '配置版本不存在' }, 404)
      return jsonResponse(snap)
    }
    if (url.includes('/config/versions')) {
      return jsonResponse(list)
    }
    throw new Error(`stubVersions: 没料到的请求 ${method} ${url}`)
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

/**
 * deferred 造一个可以手动放行的 promise：resolve() 被调用之前，await 它
 * 的代码会一直挂着。竞态测试用它精确控制"背景快照请求还没回来"这个
 * 窗口期，而不是靠猜时序（比如插一个 setTimeout）。
 */
function deferred(): { promise: Promise<void>; resolve: () => void } {
  let resolve!: () => void
  const promise = new Promise<void>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

/**
 * stubVersionsWithGatedSnapshot 和 stubVersions 一样，但版本详情接口
 * （GET .../config/versions/{seq}）在 gate resolve 之前一直挂起——列表
 * 接口（GET .../config/versions）不受影响、立刻返回。这就是竞态本身的
 * 成因：列表请求天然比详情请求的批量快一整轮异步，用这个 stub 把这个
 * 窗口期在测试里拉长到可控制的程度，而不是依赖真实网络延迟的偶然性。
 */
function stubVersionsWithGatedSnapshot(
  list: ConfigVersion[],
  snapshots: Record<number, ConfigSnapshot>,
  gate: Promise<void>,
) {
  const fn = vi.fn(async (url: string) => {
    if (url.includes('/config/types')) {
      return jsonResponse(['DEFAULT'])
    }
    const versionMatch = /\/config\/versions\/(\d+)/.exec(url)
    if (versionMatch) {
      await gate
      const seq = Number(versionMatch[1])
      const snap = snapshots[seq]
      if (!snap) return jsonResponse({ code: 'CONFIG_VERSION_NOT_FOUND', msg: '配置版本不存在' }, 404)
      return jsonResponse(snap)
    }
    if (url.includes('/config/versions')) {
      return jsonResponse(list)
    }
    throw new Error(`stubVersionsWithGatedSnapshot: 没料到的请求 ${url}`)
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
  createdAt: 1,
  updatedAt: 1,
}

/** 同 ConfigCenter.test.tsx：绕开真实 CurrentAppProvider，直接塞固定当前应用。 */
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

/** /config 只放一个占位页——验证"回滚成功后跳回配置中心页"只需要知道
 * 路由确实换了，不需要真的渲染 ConfigCenter 那一整套逻辑。 */
function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/config/versions']}>
      <CurrentAppContext.Provider value={currentAppValue()}>
        <Routes>
          <Route path="/config/versions" element={<ConfigVersions />} />
          <Route path="/config" element={<div>CONFIG_CENTER_PLACEHOLDER</div>} />
        </Routes>
      </CurrentAppContext.Provider>
    </MemoryRouter>,
  )
}

/** 等 [回滚到 vN] 按钮从 disabled 变成可点——见文件头注释。 */
async function findEnabledButton(name: string): Promise<HTMLButtonElement> {
  const btn = await screen.findByRole('button', { name })
  await waitFor(() => expect((btn as HTMLButtonElement).disabled).toBe(false))
  return btn as HTMLButtonElement
}

test('列出版本并标出每版改了哪些 key（+/- 形式），最老一版没有更老版本可比时标成"初始版本"', async () => {
  stubVersions(
    [
      { seq: 2, createdAt: 1757000000 },
      { seq: 1, createdAt: 1756000000 },
    ],
    {
      1: { seq: 1, value: 'a: 1\n' },
      2: { seq: 2, value: 'a: 1\nb: 2\n' },
    },
  )
  renderPage()

  const row1 = await screen.findByTestId('version-1')
  expect(screen.getByTestId('version-2')).toBeTruthy()

  // "改了哪些"是相邻两版 diff 出来的，后端不存这个字段。
  // 【辨别力】v2 里没变的 a 不能出现在改动清单里，否则一个"把整份内容
  // 都列成改动"的实现同样会绿。
  const changed = await screen.findByTestId('changed-2')
  expect(changed.textContent).toContain('+ b: 2')
  expect(changed.textContent).not.toContain('a: 1')

  // v1 是这个分区最老的一版（seq-1=0 从未存在过），没有更老的版本可比，
  // 标成"初始版本"，不是渲染一份对不出前一版的空 diff。
  expect(within(row1).getByText('初始版本')).toBeTruthy()
})

test('最老一行的前一版已被修剪查不到时，同样标成"初始版本"（不同于 seq=1 的平凡情形）', async () => {
  // 只提供 seq=6 的快照：seq=5 对服务端来说"查不到"，模拟
  // ConfigService.Save 里超过 ConfigMaxVersions 后从最老开始删的情形——
  // 这里要验证的是"请求发出去了、服务端 404"这条路径，不是 seq<1 那种
  // 本地就能判断、不必发请求的平凡情形。
  stubVersions([{ seq: 6, createdAt: 6 }], { 6: { seq: 6, value: 'x: 1\n' } })
  renderPage()

  const row = await screen.findByTestId('version-6')
  expect(await within(row).findByText('初始版本')).toBeTruthy()
})

// 改类型（int → object）在新模型下就是把同一个 key 的值从标量改写成
// 映射——diff 应该把它当成"这个 path 的值变了"，而不是拆成两条无关的
// 增/删（如果内部按嵌套结构拍平，object 变量下的子 key 会被拆成新的
// path，这里用一个标量→标量的改动更贴近"改了一个值"的常见场景）。
test('值变了（不是新增/删除）会同时出现一条 - 旧值和一条 + 新值', async () => {
  stubVersions(
    [
      { seq: 2, createdAt: 2 },
      { seq: 1, createdAt: 1 },
    ],
    {
      1: { seq: 1, value: 'timeout: 3000\n' },
      2: { seq: 2, value: 'timeout: 5000\n' },
    },
  )
  renderPage()

  const changed = await screen.findByTestId('changed-2')
  expect(changed.textContent).toContain('- timeout: 3000')
  expect(changed.textContent).toContain('+ timeout: 5000')
})

test('回滚前提示哪些配置项将被删除', async () => {
  stubVersions(
    [
      { seq: 2, createdAt: 2 },
      { seq: 1, createdAt: 1 },
    ],
    {
      1: { seq: 1, value: 'a: 1\n' },
      2: { seq: 2, value: 'a: 1\nb: 2\n' },
    },
  )
  renderPage()

  fireEvent.click(await findEnabledButton('回滚到 v1'))

  // v2 才新增的 b 在 v1 里没有——回滚后它会消失，运行中的实例保持旧值
  // 并报错，新起的实例会缺值起不来。这条提示必须出现。
  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).getByText(/回滚后以下配置项将被删除/)).toBeTruthy()
  expect(within(dialog).getByText(/\bb\b/)).toBeTruthy()
})

// 嵌套 key 同样要能被识别成"会被删除"：YAML 支持真正的嵌套结构（不再
// 只是靠 key 里带点模拟的扁平命名），拍平成 upstream.timeout 这样的
// 路径来比较。
test('回滚前提示——嵌套 key 在目标版本里不存在，同样算会被删除', async () => {
  stubVersions(
    [
      { seq: 2, createdAt: 2 },
      { seq: 1, createdAt: 1 },
    ],
    {
      1: { seq: 1, value: 'upstream:\n  timeout: 3000\n' },
      2: { seq: 2, value: 'upstream:\n  timeout: 3000\n  retries: 3\n' },
    },
  )
  renderPage()

  fireEvent.click(await findEnabledButton('回滚到 v1'))

  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).getByText(/回滚后以下配置项将被删除/)).toBeTruthy()
  expect(within(dialog).getByText(/upstream\.retries/)).toBeTruthy()
})

// 【审查追加】上面这条测试点按钮前先等它变成 enabled（findEnabledButton），
// 只验证了"数据就绪之后提示是对的"这一侧，从未验证过"数据没就绪之前按钮
// 确实是 disabled"——把 ConfigVersions.tsx 里的 disabled={!diffsReady}
// 整段删掉，上面那条测试原封不动地全绿：因为它只关心按钮*最终*变
// enabled、弹窗*最终*显示对的内容，一个从未被禁用过的按钮同样满足这两条。
// 这条测试专门补上被删掉也会变红的那一半：用可控延迟的 stub 把"列表已经
// 渲染、背景快照批量还没回来"这个窗口期在测试里拉长，在窗口期内断言按钮
// 确实是 disabled，再放行、确认它变 enabled 且弹窗显示的是具体 key 列表
// 而不是"数据不全"的兜底文案。
test('背景快照批量还没拉回来之前，[回滚到 vN] 必须保持 disabled；拉回来后显示具体的删除清单', async () => {
  const gate = deferred()
  stubVersionsWithGatedSnapshot(
    [
      { seq: 2, createdAt: 2 },
      { seq: 1, createdAt: 1 },
    ],
    {
      1: { seq: 1, value: 'a: 1\n' },
      2: { seq: 2, value: 'a: 1\nb: 2\n' },
    },
    gate.promise,
  )
  renderPage()

  // 列表接口不受 gate 影响，行和按钮会先渲染出来——但此刻背景的两份
  // 快照请求（v2 自己、以及它的前一版 v1）全部还挂在 gate 上没回来。
  const btn = (await screen.findByRole('button', { name: '回滚到 v1' })) as HTMLButtonElement

  // 【辨别力】这一步是本测试真正守住的东西：删掉 disabled={!diffsReady}
  // 之后，btn.disabled 在这一刻会是原生默认值 false，这条断言会立刻失败。
  expect(btn.disabled).toBe(true)

  gate.resolve()
  await waitFor(() => expect(btn.disabled).toBe(false))

  fireEvent.click(btn)
  const dialog = await screen.findByRole('dialog')
  // 数据已经就绪：弹窗必须显示具体的 key 列表，不能是"数据不全，无法
  // 确认"的兜底文案——那条兜底是留给数据真的取不到的时候用的安全网，
  // 此刻数据齐了，不该触发。
  expect(within(dialog).getByText(/回滚后以下配置项将被删除/)).toBeTruthy()
  expect(within(dialog).getByText(/\bb\b/)).toBeTruthy()
  expect(within(dialog).queryByText(/无法确认/)).toBeNull()
})

// "生效方式"单选（立即推送 / 仅落库）已经去掉了——回滚不再有选择，
// 一律立即推送，这条测试钉住 POST body 里的 push 恒为 true。
test('确认回滚时 POST body 的 push 恒为 true', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubVersions(
    [
      { seq: 2, createdAt: 2 },
      { seq: 1, createdAt: 1 },
    ],
    { 1: { seq: 1, value: 'a: 1\n' }, 2: { seq: 2, value: 'a: 2\n' } },
    calls,
  )
  renderPage()

  fireEvent.click(await findEnabledButton('回滚到 v1'))
  fireEvent.click(screen.getByRole('button', { name: '确认回滚' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
  const body = calls.find((c) => c.method === 'POST')!.body as { type: string; seq: number; push: boolean }
  expect(body).toEqual({ type: 'DEFAULT', seq: 1, push: true })
})

// 当前（最新）版本本身没有"回滚到 vN"按钮——回滚到自己没有意义。
test('最新版本那一行不出现"回滚到 vN"按钮，更早的版本仍然有', async () => {
  stubVersions(
    [
      { seq: 2, createdAt: 2 },
      { seq: 1, createdAt: 1 },
    ],
    { 1: { seq: 1, value: 'a: 1\n' }, 2: { seq: 2, value: 'a: 2\n' } },
  )
  renderPage()

  const row2 = await screen.findByTestId('version-2')
  expect(within(row2).queryByRole('button', { name: '回滚到 v2' })).toBeNull()
  const row1 = await screen.findByTestId('version-1')
  expect(within(row1).getByRole('button', { name: '回滚到 v1' })).toBeTruthy()
})

// 目标版本的内容和当前版本完全一样时，后端不会为"什么都没变"这件事
// 凭空生出一个新版本——POST /config/rollback 原样返回当前 seq。前端要
// 据此换一句不误导人的提示，而不是照常说"已回滚并推送，seq=X"。
test('目标版本内容与当前版本完全一致时，不产生新版本，toast 提示内容未变化', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const body = init?.body ? (JSON.parse(init.body as string) as unknown) : undefined
    calls.push({ method, body })
    if (method === 'POST' && url.includes('/config/rollback')) {
      return jsonResponse({ seq: 2 })
    }
    if (url.includes('/config/types')) return jsonResponse(['DEFAULT'])
    const versionMatch = /\/config\/versions\/(\d+)/.exec(url)
    if (versionMatch) {
      const snapshots: Record<number, ConfigSnapshot> = { 1: { seq: 1, value: 'a: 1\n' }, 2: { seq: 2, value: 'a: 1\n' } }
      const snap = snapshots[Number(versionMatch[1])]
      if (!snap) return jsonResponse({ code: 'CONFIG_VERSION_NOT_FOUND', msg: '配置版本不存在' }, 404)
      return jsonResponse(snap)
    }
    if (url.includes('/config/versions')) {
      return jsonResponse([
        { seq: 2, createdAt: 2 },
        { seq: 1, createdAt: 1 },
      ])
    }
    throw new Error(`没料到的请求 ${method} ${url}`)
  })
  vi.stubGlobal('fetch', fn)
  const successSpy = vi.spyOn(toast, 'success').mockImplementation(() => 'toast-id')

  renderPage()
  fireEvent.click(await findEnabledButton('回滚到 v1'))
  fireEvent.click(await screen.findByRole('button', { name: '确认回滚' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
  await waitFor(() =>
    expect(successSpy).toHaveBeenCalledWith('这一版与当前内容完全一致，没有产生新版本'),
  )
})

// 标签页不再固定是 DEFAULT/WEB 两个——按 /config/types 实际返回的分区名
// 动态生成，DEFAULT 永远在场（即使这个应用还没对它存过版本）。
test('标签页按实际存在的分区动态生成，DEFAULT 永远在场', async () => {
  const fn = vi.fn(async (url: string) => {
    if (url.includes('/config/types')) return jsonResponse(['MOBILE', 'WEB'])
    if (url.includes('/config/versions')) return jsonResponse([])
    throw new Error(`没料到的请求 ${url}`)
  })
  vi.stubGlobal('fetch', fn)
  renderPage()

  expect(await screen.findByRole('tab', { name: 'DEFAULT' })).toBeTruthy()
  expect(screen.getByRole('tab', { name: 'MOBILE' })).toBeTruthy()
  expect(screen.getByRole('tab', { name: 'WEB' })).toBeTruthy()
})

test('回滚成功后跳回配置中心页', async () => {
  stubVersions(
    [
      { seq: 2, createdAt: 2 },
      { seq: 1, createdAt: 1 },
    ],
    { 1: { seq: 1, value: 'a: 1\n' }, 2: { seq: 2, value: 'a: 2\n' } },
  )
  renderPage()

  fireEvent.click(await findEnabledButton('回滚到 v1'))
  fireEvent.click(await screen.findByRole('button', { name: '确认回滚' }))

  expect(await screen.findByText('CONFIG_CENTER_PLACEHOLDER')).toBeTruthy()
})
