// 这个文件不在 task-8-brief.md 的 Files 列表里，是复审第 1 轮按"Important:
// page 归一化重复且无测试覆盖""搜索框后退/前进不回填"两条意见补的组件级
// 测试——只测 normalizePage 这个纯函数不够，还得证明 Users.tsx 真的用上
// 了它、也真的在查询参数变化时把搜索框状态跟 URL 同步上。
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router'
import Users from './Users'
import type { UserListResponse } from '@/lib/types'

afterEach(() => vi.unstubAllGlobals())

function emptyList(): UserListResponse {
  return { items: [], total: 0 }
}

function stubFetchSequence(...responses: Response[]) {
  const fn = vi.fn()
  for (const r of responses) fn.mockResolvedValueOnce(r)
  vi.stubGlobal('fetch', fn)
  return fn
}

/**
 * 测试专用的"后退"按钮：navigate(-1) 走的是 history.go(-1) 的真实语义。
 *
 * 不能靠重新渲染 <MemoryRouter initialEntries={...}> 来模拟浏览器后退——
 * initialEntries 只在首次挂载时用来初始化历史栈，之后再传新值不会触发
 * 任何导航。真正复现"查询参数变了但 <Route path="/users"> 只重渲染、
 * 不重新挂载 Users 组件"这个场景，必须让同一个 router 实例真的做一次
 * 导航，navigate(-1) 是最接近浏览器后退按钮的方式。
 */
function BackButton() {
  const navigate = useNavigate()
  return <button onClick={() => navigate(-1)}>__back__</button>
}

function renderUsers(initialEntries: string[]) {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <BackButton />
      <Routes>
        <Route path="/users" element={<Users />} />
      </Routes>
    </MemoryRouter>,
  )
}

// 【辨别力】page 参数被手动改成非法值（比如浏览器地址栏手打 ?page=abc）
// 时，如果 Users.tsx 没有把它交给 normalizePage 钳成整数，
// Math.max(1, NaN) 的结果仍是 NaN——分页区域会显示"第 NaN / 1 页"，
// 上一页/下一页两个按钮的禁用判断也会失真。只测 normalizePage 本身
// 不够：这条测试直接渲染整个 Users 页面，证明组件真的调用了归一化后的
// 结果去渲染 <Pagination>，而不是"helper 测试是绿的，组件里其实没用上"。
test('URL 上的 page 是非法值时，分页显示第 1 页而不是第 NaN 页', async () => {
  stubFetchSequence(new Response(JSON.stringify(emptyList()), { status: 200 }))
  renderUsers(['/users?page=abc'])

  await waitFor(() => expect(screen.getByText(/第 1 \/ 1 页/)).toBeTruthy())
})

// 【辨别力】搜索框是受控组件：keywordInput 这个本地 state 才是"真身"，
// 一个 useEffect 在 keyword（来自 URL）变化时把它同步过去。提交搜索、
// 点分页、切状态筛选走的都是 setSp()，这类只改查询参数的导航在同一个
// <Route path="/users"> 上只会重渲染 Users、不会重新挂载它——浏览器
// "后退/前进"到一个不同的 ?keyword= 时同样如此。如果那个 useEffect 被
// 删掉或者依赖数组写错，keywordInput 就不会跟着 keyword 回退：后退到
// "没有关键词"的历史记录后，地址栏和表格数据都变了，输入框里却还留着
// 后退前敲的字，用户会以为筛选没生效。
test('浏览器后退到没有关键词的历史记录时，搜索框回填为空', async () => {
  stubFetchSequence(
    new Response(JSON.stringify(emptyList()), { status: 200 }), // 初次 GET /users
    new Response(JSON.stringify(emptyList()), { status: 200 }), // 搜索后 GET /users?keyword=138
    new Response(JSON.stringify(emptyList()), { status: 200 }), // 后退后重新 GET /users
  )
  renderUsers(['/users'])

  const input = (await waitFor(() =>
    screen.getByPlaceholderText('手机号 / 用户名 / 昵称'),
  )) as HTMLInputElement
  const form = input.closest('form')
  if (!form) throw new Error('未找到搜索表单')

  fireEvent.change(input, { target: { value: '138' } })
  fireEvent.submit(form)

  await waitFor(() => {
    const box = screen.getByPlaceholderText('手机号 / 用户名 / 昵称') as HTMLInputElement
    expect(box.value).toBe('138')
  })

  fireEvent.click(screen.getByRole('button', { name: '__back__' }))

  await waitFor(() => {
    const box = screen.getByPlaceholderText('手机号 / 用户名 / 昵称') as HTMLInputElement
    expect(box.value).toBe('')
  })
})

// 【辨别力】这条测试守住的是"提交搜索不能把输入框卸载重挂"这件事本身。
// 旧实现给输入框加了 key={keyword} 来让后退时的回填生效（见上一条测试），
// 副作用是提交搜索同样会让 keyword 变化——key 一变，React 会把这个
// <Input> 卸载再重新挂载成一个新的 DOM 节点，焦点也随之丢失（浏览器和
// jsdom 都一样：节点被移除后 document.activeElement 掉回 body，不会自动
// 转移到新节点上）。键盘用户按 Enter 提交搜索后，光标会莫名跳出输入框。
// 现在的受控实现不再需要 key：提交时 keywordInput 已经等于新 keyword，
// 那个同步用的 useEffect 是 no-op，节点全程没有被替换过，焦点不会丢。
test('提交搜索后输入框仍然持有焦点，不会因为组件重挂而跳出', async () => {
  stubFetchSequence(
    new Response(JSON.stringify(emptyList()), { status: 200 }), // 初次 GET /users
    new Response(JSON.stringify(emptyList()), { status: 200 }), // 搜索后 GET /users?keyword=138
  )
  renderUsers(['/users'])

  const input = (await waitFor(() =>
    screen.getByPlaceholderText('手机号 / 用户名 / 昵称'),
  )) as HTMLInputElement
  const form = input.closest('form')
  if (!form) throw new Error('未找到搜索表单')

  input.focus()
  expect(document.activeElement).toBe(input)

  fireEvent.change(input, { target: { value: '138' } })
  fireEvent.submit(form)

  await waitFor(() => {
    expect((screen.getByPlaceholderText('手机号 / 用户名 / 昵称') as HTMLInputElement).value).toBe('138')
  })
  // 同一个 DOM 节点应该还在文档里持有焦点——不是"又找到一个值对的输入框"，
  // 而是这个输入框自己没有被换掉。
  expect(document.activeElement).toBe(input)
  expect(document.body.contains(input)).toBe(true)
})
