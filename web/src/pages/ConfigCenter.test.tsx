// 【与 brief 的出入】brief 给的 Step 2 示例代码 import 了
// `@testing-library/user-event`，但这个包**不在** package.json 的
// devDependencies 里（`grep -rn user-event package.json package-lock.json`
// 零匹配），加进去会违反任务里"不引入新依赖"的硬约束。改用仓库里
// ApplicationDetail.test.tsx 已经在用的 `fireEvent`（同样来自
// @testing-library/react，本来就是依赖）。userEvent.clear+type 在这里
// 等价于对着同一个受控 input 触发一次 fireEvent.change 把值整个换掉。
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import ConfigCenter from './ConfigCenter'
import { CurrentAppContext } from '@/lib/current-app'
import type { CurrentAppValue } from '@/lib/current-app'
import type { Application, ConfigSnapshot } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

/**
 * stubConfig 让配置接口不管 type 是什么都返回同一份快照，PUT 固定返回
 * { seq: snapshot.seq + 1 }。传入的 calls 数组会记下每次请求的 method 与
 * 解析后的 body，供断言用（GET 没有 body，记 undefined）。
 */
function stubConfig(snapshot: ConfigSnapshot, calls: Array<{ method: string; body: unknown }> = []) {
  const fn = vi.fn(async (_url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const body = init?.body ? (JSON.parse(init.body as string) as unknown) : undefined
    calls.push({ method, body })
    if (method === 'PUT') return jsonResponse({ seq: snapshot.seq + 1 })
    return jsonResponse(snapshot)
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

/** stubConfigCapturingUrls 只关心 GET 打过哪些 URL，固定返回一份最小快照。 */
function stubConfigCapturingUrls(urls: string[]) {
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    if (!init?.method || init.method === 'GET') urls.push(url)
    return jsonResponse({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } })
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

/**
 * ConfigCenter 现在从全局"当前应用"读 appId，不再走路由参数。直接塞一个
 * 固定的 context 值，绕开真实的 CurrentAppProvider——不需要真的发一次
 * /applications 请求去凑出一个"当前应用"。id 用 'app-1'，和原来路由参数
 * 的值保持一致，下面每条测试断言的 URL 字符串不用跟着改。
 */
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

// 【与 brief 的出入】这条测试、以及它验证的这个链接，都不在 task-13-brief
// 的范围里——是给 Task 14（版本历史与回滚页）补的入口。Task 14 的 brief
// 只要求新建 ConfigVersions.tsx 和加路由，没有要求任何地方链接过去；但
// 路由能访问不等于功能可用，控制台里如果没有入口，管理员根本不知道这个
// 页面存在，等于白做。这里补一个最小的跳转链接，并用这条测试钉住它的
// 目标路径，防止以后重构 ConfigCenter 时被无意删掉。
// 【与 brief 的出入】brief 说这条用例"不用改"，但它断言的 href 字符串
// 本身就是路由拍平前的形状（带 :id）——Step 2 把 Link 目标换成不带 appId
// 的 `/config/versions` 之后，这里如果不跟着改，就会跟其余用例一样"看起来
// 不依赖 URL 形状"的说法自相矛盾。改成断言拍平后的目标路径。
test('提供入口跳到版本历史页', async () => {
  stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } })
  renderPage()

  await screen.findByLabelText('a')
  const link = screen.getByRole('link', { name: '版本历史' })
  expect(link.getAttribute('href')).toBe('/config/versions')
})

test('未配置的项标出来并计数', async () => {
  stubConfig({
    seq: 1,
    fields: {
      fee_rate: { type: 'float', desc: '手续费率', value: 0.02 },
      api_key: { type: 'string', desc: '上游密钥', value: null },
    },
  })
  renderPage()

  // 顶部提示必须给出数量——人是照着它决定还要不要继续配的。
  expect(await screen.findByText(/1 项未配置/)).toBeTruthy()
  // 未配置的项本身仍然在列表上（它就是待填的那一行）。
  expect(screen.getByLabelText('api_key')).toBeTruthy()
})

test('保存时把完整的 fields 全量提交', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig(
    {
      seq: 1,
      fields: {
        a: { type: 'int', desc: '', value: 1 },
        b: { type: 'int', desc: '', value: 2 },
      },
    },
    calls,
  )
  renderPage()

  const input = await screen.findByLabelText('a')
  fireEvent.change(input, { target: { value: '9' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
  const put = calls.find((c) => c.method === 'PUT')!
  const body = put.body as { type: string; push: boolean; fields: Record<string, unknown> }
  expect(body.type).toBe('DEFAULT')
  // 【辨别力】没被改的 b 也必须在提交里。接口是全量替换——只提交被改过的
  // 字段的话，b 会在新版本里凭空消失（等于被删了）。
  expect(Object.keys(body.fields).sort()).toEqual(['a', 'b'])
})

test('生效方式默认是立即推送，可切成仅落库', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } }, calls)
  renderPage()

  await screen.findByLabelText('a')
  fireEvent.click(screen.getByLabelText(/仅落库/))
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
  expect((calls.find((c) => c.method === 'PUT')!.body as { push: boolean }).push).toBe(false)
})

test('切换分区会重新拉取', async () => {
  const urls: string[] = []
  stubConfigCapturingUrls(urls)
  renderPage()

  const tab = await screen.findByRole('tab', { name: 'WEB' })
  fireEvent.click(tab)

  await waitFor(() => expect(urls.some((u) => u.includes('type=WEB'))).toBe(true))
  // 【辨别力】要断言 DEFAULT 也被拉过——只断言 WEB 的话，
  // 一个把 type 写死成 WEB 的实现同样会绿。
  expect(urls.some((u) => u.includes('type=DEFAULT'))).toBe(true)
})

test('删除配置项要二次确认，并说明没有机制能确认它是否还被读取', async () => {
  stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } })
  renderPage()

  fireEvent.click(await screen.findByRole('button', { name: '删除 a' }))
  expect(screen.getByText(/没有机制能确认它是否还被代码读取/)).toBeTruthy()
})

// 下面两条不在 brief 的 Step 2 示例代码块里，是为主任务说明里明确写出的
// 两条硬约束单独补的：这两条约束和"删除要二次确认"同等级别地写在同一份
// 硬约束清单里，但清单给的 5 条测试骨架唯独没有覆盖它们。没有测试的话，
// "改类型二次确认"这条约束只存在于代码审查里，下一个人重构时改掉了也
// 不会有任何红灯。

test('修改配置项类型需要二次确认，并提示旧实例可能解析失败', async () => {
  stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } })
  renderPage()

  await screen.findByLabelText('a')
  // base-ui 的 Select 选项只在 onClick 里判断 allowMouseSelectionRef——
  // 这个 ref 只由 onPointerDown 置 true，纯 fireEvent.click（没有先经过
  // pointerdown）会被它的"疑似非真实鼠标点击"分支挡掉、什么都不选中。
  // 实测确认过：只 fireEvent.click 时 trigger 文案不变、也不会弹出确认框。
  // userEvent 会自动补全 pointerdown→click 序列，但这个包不在依赖里
  // （见文件头注释），这里手动补上同样的顺序。
  const trigger = screen.getByRole('combobox', { name: 'a 的类型' })
  fireEvent.pointerDown(trigger)
  fireEvent.click(trigger)
  const option = await screen.findByRole('option', { name: '字符串' })
  fireEvent.pointerDown(option)
  fireEvent.click(option)

  expect(await screen.findByText(/旧实例若收到推送会解析失败/)).toBeTruthy()
  // 确认前不能已经生效：数字输入框应该还在，说明类型还没真的改成 string。
  // 仓库没有接 @testing-library/jest-dom（package.json 里虽有这个包，
  // 但没有任何 setup 文件 expect.extend 过它），toHaveAttribute 这类
  // matcher 用不了，改用原生 DOM 属性断言。
  expect((screen.getByLabelText('a') as HTMLInputElement).type).toBe('number')

  fireEvent.click(screen.getByRole('button', { name: '确认修改' }))
  // 确认之后才真的切换成字符串输入框。
  await waitFor(() => expect((screen.getByLabelText('a') as HTMLInputElement).type).toBe('text'))
})

test('新建配置项时值必填，留空不能创建', async () => {
  stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } })
  renderPage()

  await screen.findByLabelText('a')
  fireEvent.click(screen.getByRole('button', { name: '新建配置项' }))
  fireEvent.change(await screen.findByLabelText('key'), { target: { value: 'b' } })
  // 值留空直接点创建。
  fireEvent.click(screen.getByRole('button', { name: '创建' }))

  expect(await screen.findByText(/值必填/)).toBeTruthy()
  // 没有新字段被加进列表——对话框还开着、b 没有出现在页面上的其他地方。
  expect(screen.queryByLabelText('b')).toBeNull()
})

// 【终审 Important】数字字段清空后，提交上去的是 JS 空字符串 ""，不是
// JSON null——后端 CoerceConfigValue 判"未配置"发生在去引号之前，"" 带
// 引号长度是 2，落不进那个分支，最终解析 int64 失败。Save 是"一项转不过去
// 就整批拒绝"，清空一个数字字段会连累同一次保存里其余字段全部不生效。
// 这两条测试锁住前端必须先一步拦住这种情况，不能指望后端 400 才发现。

test('数字字段清空后保存按钮被禁用，并显示错误提示', async () => {
  stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } })
  renderPage()

  const input = await screen.findByLabelText('a')
  fireEvent.change(input, { target: { value: '' } })

  // 提示要指出"数字不能为空"，并给出正确的路径（删除配置项，不是清空）——
  // 清空在这个 UI 里没有对应到"未配置"的语义，那个状态只来自服务端。
  expect(await screen.findByText(/数字字段不能为空/)).toBeTruthy()
  expect((screen.getByRole('button', { name: '保存' }) as HTMLButtonElement).disabled).toBe(true)
})

test('数字字段填非法值（int 填 3.7）同样被拦，保存按钮禁用', async () => {
  stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } })
  renderPage()

  const input = await screen.findByLabelText('a')
  fireEvent.change(input, { target: { value: '3.7' } })

  expect(await screen.findByText(/不是合法的整数/)).toBeTruthy()
  expect((screen.getByRole('button', { name: '保存' }) as HTMLButtonElement).disabled).toBe(true)
})

// 【终审必须修】设计文档 §8 明写"每行可编辑：值、desc、type"，但 desc 一度
// 只有只读展示，输入框只存在于新建对话框——只在新建时能填一次。改错别字
// 唯一的路是删除+重建，而删除正是这个页面自己要二次确认、且明说"没有
// 机制确认它是否还被代码读取"的高风险操作。
test('可以编辑 desc，保存后提交新值', async () => {
  const calls: Array<{ method: string; body: unknown }> = []
  stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '旧备注', value: 1 } } }, calls)
  renderPage()

  const descInput = await screen.findByLabelText('a 的备注')
  expect((descInput as HTMLInputElement).value).toBe('旧备注')
  fireEvent.change(descInput, { target: { value: '新备注' } })
  fireEvent.click(screen.getByRole('button', { name: '保存' }))

  await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
  const put = calls.find((c) => c.method === 'PUT')!
  const body = put.body as { fields: Record<string, { desc: string }> }
  expect(body.fields.a.desc).toBe('新备注')
})

// 【终审必须修】设计文档 §2：没有 secret 类型之后，分区是阻止密钥被下发到
// 浏览器的唯一的闸——但控制台上 DEFAULT 与 WEB 两个 tab 长得完全一样，
// 切过去没有任何提示。这条测试钉住切到 WEB 时必须出现警示、切回 DEFAULT
// 后必须消失，防止以后重构把这条提示误删还全绿。
test('切到 WEB 分区会提示内容会下发到浏览器，切回 DEFAULT 后提示消失', async () => {
  stubConfig({ seq: 1, fields: { a: { type: 'int', desc: '', value: 1 } } })
  renderPage()

  await screen.findByLabelText('a')
  expect(screen.queryByText(/会被下发到浏览器/)).toBeNull()

  fireEvent.click(await screen.findByRole('tab', { name: 'WEB' }))
  expect(await screen.findByText(/会被下发到浏览器/)).toBeTruthy()
  // 顺带钉住第二个关键信息点：密钥类必须建在 DEFAULT。
  expect(screen.getByText(/密钥类配置项必须建在 DEFAULT 分区/)).toBeTruthy()

  fireEvent.click(screen.getByRole('tab', { name: 'DEFAULT' }))
  await waitFor(() => expect(screen.queryByText(/会被下发到浏览器/)).toBeNull())
})
