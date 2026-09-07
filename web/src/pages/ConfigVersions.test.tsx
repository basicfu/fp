// 【与 brief 的出入】brief 给的 Step 1 示例代码块本身编译不过（结尾多了一个
// 悬空的 `})`），而且假定了一个"点开就能看到完整回滚提示"的时序：
// "将变成未配置"这条提示依赖当前版本与目标版本的两份完整快照，这两份是
// 组件挂载后才异步拉回来的，不能假设 [回滚到 vN] 按钮一出现数据就已经
// 齐了。真这么假设，测试会在本机偶发通过、在 CI 上偶发失败（点太快，
// 拉到的是不完整数据）。这里在按钮变为可点（disabled=false）之后再点，
// 复现的是"数据没齐之前挡住用户手误"这个真实场景，而不是掩盖竞态。
//
// 另外，brief 例子里 `screen.getByText(/\bb\b/)` 直接搜整个文档：背景的
// 版本列表里 v2 那一行的"改动清单"本来就含有同一个 key "b"（这正是这条
// 用例要制造的场景——b 是 v2 才加的，回滚到 v1 会丢），弹窗打开后背景并不
// 会被卸载（Dialog 只是浮层），两处"b"同时在场，不加范围限定会命中两个
// 元素、报"找到多个元素"而不是验证弹窗内容。这里改成先拿到
// role="dialog" 的容器，再用 @testing-library/react 自带的 within 把查询
// 限定在弹窗里。
//
// 仓库**没有**装 @testing-library/user-event（不在 package.json 里），
// 用既有的 fireEvent，写法照 ApplicationSettings.test.tsx。
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import ConfigVersions from './ConfigVersions'
import { CurrentAppContext } from '@/lib/current-app'
import type { CurrentAppValue } from '@/lib/current-app'
import type { Application, ConfigSnapshot, ConfigVersion } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/**
 * stubVersions 支持 Task 14 用到的三个接口：
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

test('列出版本并标出每版改了哪些 key，最老一版没有更老版本可比时标成"初始版本"', async () => {
  stubVersions(
    [
      { seq: 2, createdAt: 1757000000 },
      { seq: 1, createdAt: 1756000000 },
    ],
    {
      1: { seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } },
      2: {
        seq: 2,
        fields: {
          a: { type: 'int', desc: '', value: 1 },
          b: { type: 'int', desc: '', value: 2 },
        },
      },
    },
  )
  renderPage()

  const row1 = await screen.findByTestId('version-1')
  expect(screen.getByTestId('version-2')).toBeTruthy()

  // "改了哪些"是相邻两版 diff 出来的，后端不存这个字段。
  // 【辨别力】v2 里没变的 a 不能出现在改动清单里，否则一个"把整份 fields
  // 都列成改动"的实现同样会绿。
  //
  // 断言落在**改动清单这个容器**上，不是整行的 textContent——整行还带着
  // 版本号、时间戳、按钮文案，用 not.toContain('a') 去查一个单字母会被
  // 那些文字里任意一个 a 误伤，测试会因为无关的文案改动而假红。
  const changed = await screen.findByTestId('changed-2')
  const keys = Array.from(changed.querySelectorAll('li')).map((li) => li.textContent)
  expect(keys).toEqual(['b'])

  // v1 是这个分区最老的一版（seq-1=0 从未存在过），没有更老的版本可比，
  // 标成"初始版本"，不是渲染一份对不出前一版的空 diff。
  expect(within(row1).getByText('初始版本')).toBeTruthy()
})

test('最老一行的前一版已被修剪查不到时，同样标成"初始版本"（不同于 seq=1 的平凡情形）', async () => {
  // 只提供 seq=6 的快照：seq=5 对服务端来说"查不到"，模拟
  // ConfigService.Save 里超过 ConfigMaxVersions 后从最老开始删的情形——
  // 这里要验证的是"请求发出去了、服务端 404"这条路径，不是 seq<1 那种
  // 本地就能判断、不必发请求的平凡情形。
  stubVersions([{ seq: 6, createdAt: 6 }], { 6: { seq: 6, fields: { x: { type: 'int', desc: '', value: 1 } } } })
  renderPage()

  const row = await screen.findByTestId('version-6')
  expect(await within(row).findByText('初始版本')).toBeTruthy()
})

test('回滚前提示哪些项将变成未配置', async () => {
  stubVersions(
    [
      { seq: 2, createdAt: 2 },
      { seq: 1, createdAt: 1 },
    ],
    {
      1: { seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } },
      2: {
        seq: 2,
        fields: {
          a: { type: 'int', desc: '', value: 1 },
          b: { type: 'int', desc: '', value: 2 },
        },
      },
    },
  )
  renderPage()

  fireEvent.click(await findEnabledButton('回滚到 v1'))

  // v2 才新增的 b 在 v1 里没有——回滚后它会变成未配置，运行中的实例
  // 保持旧值并报错，新起的实例会缺值起不来。这条提示必须出现。
  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).getByText(/回滚后以下配置项将变成未配置/)).toBeTruthy()
  expect(within(dialog).getByText(/\bb\b/)).toBeTruthy()
})

// 【终审必须修】上面那条测试只造了"key 在目标版本里根本不存在"这一种
// 会变成未配置的情形。真实还可达的另一种是：key 在目标版本里存在，但
// value 是 JSON null（这一项当时"已创建但没填值"）——旧实现只判
// `!(k in target.fields)`，会把这种情形错判成"没变化"，回滚提示对着一个
// 会导致新实例起不来的操作完全沉默。ConfigCenter.tsx 的 commitRawText
// 会在 array/object 的文本框清空时把 value 写成 null 并允许保存，所以
// v6 填了值、v5 是未配置、从 v6 回滚到 v5 这个场景是真实可达的。
test('回滚前提示——目标版本里 key 存在但 value 是 null，同样算变成未配置', async () => {
  stubVersions(
    [
      { seq: 2, createdAt: 2 },
      { seq: 1, createdAt: 1 },
    ],
    {
      // v1（回滚目标）：endpoints 这一项存在于 fields 里，但没填值。
      1: { seq: 1, fields: { endpoints: { type: 'array', desc: '', value: null } } },
      // v2（当前版本）：填成了一个非空数组。
      2: { seq: 2, fields: { endpoints: { type: 'array', desc: '', value: ['a'] } } },
    },
  )
  renderPage()

  fireEvent.click(await findEnabledButton('回滚到 v1'))

  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).getByText(/回滚后以下配置项将变成未配置/)).toBeTruthy()
  expect(within(dialog).getByText(/endpoints/)).toBeTruthy()
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
test('背景快照批量还没拉回来之前，[回滚到 vN] 必须保持 disabled；拉回来后显示具体的未配置清单', async () => {
  const gate = deferred()
  stubVersionsWithGatedSnapshot(
    [
      { seq: 2, createdAt: 2 },
      { seq: 1, createdAt: 1 },
    ],
    {
      1: { seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } },
      2: {
        seq: 2,
        fields: {
          a: { type: 'int', desc: '', value: 1 },
          b: { type: 'int', desc: '', value: 2 },
        },
      },
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
  expect(within(dialog).getByText(/回滚后以下配置项将变成未配置/)).toBeTruthy()
  expect(within(dialog).getByText(/\bb\b/)).toBeTruthy()
  expect(within(dialog).queryByText(/无法确认/)).toBeNull()
})

test('回滚同样要选生效方式', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubVersions([{ seq: 1, createdAt: 1 }], { 1: { seq: 1, fields: {} } }, calls)
  renderPage()

  fireEvent.click(await findEnabledButton('回滚到 v1'))
  fireEvent.click(screen.getByLabelText(/仅落库/))
  fireEvent.click(screen.getByRole('button', { name: '确认回滚' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
  const body = calls.find((c) => c.method === 'POST')!.body as { type: string; seq: number; push: boolean }
  expect(body).toEqual({ type: 'DEFAULT', seq: 1, push: false })
})

// 【审查追加】上面那条测试断言的是 push:false 这一侧（点了"仅落库"才
// 确认），从未验证过保持默认"立即推送"不动时，POST body 里的 push 是
// 不是真的是 true——把实现里的 push: rollbackPush 硬编码成 push: false
// 之后，上面那条测试依然全绿（它本来就期望 false）。这条测试补 push:true
// 这一侧：不碰生效方式，直接确认。
test('回滚保持默认的立即推送不动时，POST body 的 push 是 true', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubVersions([{ seq: 1, createdAt: 1 }], { 1: { seq: 1, fields: {} } }, calls)
  renderPage()

  fireEvent.click(await findEnabledButton('回滚到 v1'))
  // 不碰"生效方式"单选，保持默认的"立即推送"。
  fireEvent.click(screen.getByRole('button', { name: '确认回滚' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
  const body = calls.find((c) => c.method === 'POST')!.body as { type: string; seq: number; push: boolean }
  expect(body).toEqual({ type: 'DEFAULT', seq: 1, push: true })
})

test('回滚成功后跳回配置中心页', async () => {
  stubVersions([{ seq: 1, createdAt: 1 }], { 1: { seq: 1, fields: {} } })
  renderPage()

  fireEvent.click(await findEnabledButton('回滚到 v1'))
  fireEvent.click(await screen.findByRole('button', { name: '确认回滚' }))

  expect(await screen.findByText('CONFIG_CENTER_PLACEHOLDER')).toBeTruthy()
})
