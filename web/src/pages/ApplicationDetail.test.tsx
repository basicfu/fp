// 这个文件不在 task-6-brief.md 的 Files 列表里，是实现过程中按任务说明
// 第五节"两处很可能需要实测调整的地方"第 1 条自行补的：brief 用
// z.coerce.number() 把 <input type="number"> 的字符串值转成数字，但
// HTML input 经 react-hook-form 拿到的值本身就是字符串，coerce 是否真的
// 生效必须实测——不然提交上去的是 "3600" 而不是 3600，后端 decodeJSON
// 会因类型不符直接 400，错误消息只说"请求体解析失败"，很难联想到原因。
// 人工浏览器点击（brief Step 10）在本环境做不了，这条测试是它的替代物，
// 而且比人工点一次更可靠：它会一直挡在这里，不会因为下次谁改了实现就失效。
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import ApplicationDetail from './ApplicationDetail'
import type { Application } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

const baseApp: Application = {
  id: 'app-1',
  name: '测试应用',
  slug: 'test-app',
  appId: 'appid-123',
  status: 'ACTIVE',
  cookieDomain: '',
  session: {
    idleTimeoutSeconds: 604800,
    idleTimeoutMobileSeconds: 2592000,
    maxLifetimeSeconds: 7776000,
    rotateIntervalSeconds: 86400,
    extendIntervalSeconds: 600,
    tokenCacheTtlSeconds: 30,
  },
  createdAt: 1700000000000,
  updatedAt: 1700000000000,
}

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={['/applications/app-1']}>
      <Routes>
        <Route path="/applications/:id" element={<ApplicationDetail />} />
      </Routes>
    </MemoryRouter>,
  )
}

function stubFetchSequence(...responses: Response[]) {
  const fn = vi.fn()
  for (const r of responses) fn.mockResolvedValueOnce(r)
  vi.stubGlobal('fetch', fn)
  return fn
}

/** 打开详情页并切到"会话策略"页签，返回"空闲超时（秒）"输入框所在的 form。 */
async function openSessionForm() {
  await waitFor(() => expect(screen.getByText('测试应用')).toBeTruthy())
  fireEvent.click(screen.getByRole('tab', { name: '会话策略' }))
  const idleInput = await waitFor(() => screen.getByLabelText('空闲超时（秒）') as HTMLInputElement)
  const form = idleInput.closest('form')
  if (!form) throw new Error('未找到会话策略表单')
  return { idleInput, form }
}

// 【辨别力】核心问题：<input type="number"> 经 react-hook-form 的 register
// 拿到的 onChange 事件值永远是字符串。如果 sessionSchema 不做数字转换
// （无论是 z.coerce.number() 还是 valueAsNumber），提交上去的 JSON 里
// idleTimeoutSeconds 会是 "7200"（字符串）而不是 7200（数字）——后端
// sessionPolicyDTO 六个字段都是 Go 的 int32，DisallowUnknownFields 解码器
// 遇到字符串填数字字段会直接 400。断言 typeof === 'number' 而不是只比较
// == 值，是因为 JS 的 == 会把 "7200" 和 7200 视为相等，掩盖掉字符串没被
// 转换这件事——这正是这条测试要抓的问题。
test('保存会话策略时，提交给后端的字段是数字而不是字符串', async () => {
  const updated: Application = { ...baseApp, session: { ...baseApp.session, idleTimeoutSeconds: 7200 } }
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify(baseApp), { status: 200 }), // GET 详情
    new Response(JSON.stringify(updated), { status: 200 }), // PUT 会话策略
    new Response(JSON.stringify(updated), { status: 200 }), // 保存后 reload 再次 GET
  )

  renderDetail()
  const { idleInput, form } = await openSessionForm()

  fireEvent.change(idleInput, { target: { value: '7200' } })
  fireEvent.submit(form)

  await waitFor(() => {
    const putCall = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'PUT')
    expect(putCall).toBeDefined()
  })

  const putCall = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'PUT')!
  const [url, init] = putCall as [string, RequestInit]
  expect(url).toBe('/admin/api/applications/app-1/session')
  const body = JSON.parse(init.body as string) as Record<string, unknown>

  // 被编辑的字段。
  expect(body.idleTimeoutSeconds).toBe(7200)
  expect(typeof body.idleTimeoutSeconds).toBe('number')
  // 没被编辑、直接来自 defaultValues 的字段——证明转换对全部六个字段
  // 生效，不是只对"用户碰过"的字段生效。
  expect(body.tokenCacheTtlSeconds).toBe(30)
  expect(typeof body.tokenCacheTtlSeconds).toBe('number')
  expect(typeof body.maxLifetimeSeconds).toBe('number')
})

// Step 10 人工验证清单第 4 条的自动化版本：延期间隔改得比空闲超时还大，
// 必须在前端就被拦下（zod 的 refine），不能把这个请求发给后端。
test('延期间隔大于等于空闲超时时，前端拦截，不发请求且给出中文提示', async () => {
  const fetchMock = stubFetchSequence(new Response(JSON.stringify(baseApp), { status: 200 }))

  renderDetail()
  const { form } = await openSessionForm()
  const extendInput = screen.getByLabelText('延期间隔（秒）') as HTMLInputElement

  // idleTimeoutSeconds 保持默认的 604800，延期间隔改成比它更大的值。
  fireEvent.change(extendInput, { target: { value: '99999999' } })
  fireEvent.submit(form)

  await waitFor(() => expect(screen.getByText(/延期间隔必须小于空闲超时/)).toBeTruthy())
  // 只有初始加载那一次 GET，没有任何 PUT 打到 /session。
  expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PUT')).toBe(false)
})
