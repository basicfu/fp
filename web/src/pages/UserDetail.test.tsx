// 不在 task-9-brief.md 的 Files 列表里（brief 本身没给这个任务列测试代码），
// 但按任务说明第六节要求补上：brief Step 4 的人工浏览器验证（登录造一个真实
// 用户、点下线/冻结/重置密码分别观察设备列表变化）在本环境做不了，这些测试
// 是它的自动化替代——尤其是两条"安全耦合"（冻结/重置密码都会连带撤销全部
// 会话）必须在界面上如实反映，界面测试比一次性的人工点击更可靠，以后谁改坏
// 了 sessions.reload() 这条线，这里会立刻变红。
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { toast } from 'sonner'
import UserDetail from './UserDetail'
import type { User, UserSession } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const sampleUser: User = {
  id: 'user-1',
  nickname: '张三',
  avatarUrl: '',
  status: 'ACTIVE',
  hasPassword: true,
  identities: [{ type: 'phone', subject: '138****0000', lastLoginAt: 1700000000000 }],
  createdAt: 1690000000000,
}

const sampleSession: UserSession = {
  id: 'sess-1',
  appId: 'app-1',
  ip: '1.2.3.4',
  ua: 'Mozilla/5.0',
  mobile: false,
  firstAuthAt: 1700000000000,
  idleExpiresAt: 1700100000000,
}

function stubFetchSequence(...responses: Response[]) {
  const fn = vi.fn()
  for (const r of responses) fn.mockResolvedValueOnce(r)
  vi.stubGlobal('fetch', fn)
  return fn
}

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={['/users/user-1']}>
      <Routes>
        <Route path="/users/:id" element={<UserDetail />} />
      </Routes>
    </MemoryRouter>,
  )
}

/**
 * 数一下 GET /users/{id}/sessions（拉取在线设备列表）发了几次。
 *
 * 【复审第 1 轮修复】必须同时按 URL 和方法过滤，不能只看 URL：
 * "全部下线"走的是 DELETE /users/{id}/sessions——和这个 GET 是完全相同
 * 的 URL，只有方法不同。只按 URL 过滤的话，revokeAll 自己那次 DELETE
 * 请求会被误计成一次"列表被拉取"，删掉 sessions.reload() 之后这里的
 * 计数依然是 2（1 次初始 GET + 1 次自己的 DELETE），测试不会变红——
 * 这个坑是靠变异测试才现出原形的：第一次删掉 revokeAll 里的
 * sessions.reload() 后重跑测试，8/8 全绿，说明这条断言当时是摆设。
 */
function sessionsListCallCount(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls.filter(([url, init]) => {
    if (url !== '/admin/api/users/user-1/sessions') return false
    return (init as RequestInit | undefined)?.method === 'GET'
  }).length
}

/** 点开"重置密码"弹窗，填入新密码并提交。调用方负责准备好 fetch 序列。 */
async function openResetDialogAndSubmit(pwd = 'new-secret-pwd') {
  fireEvent.click(screen.getByRole('button', { name: '重置密码' }))
  const pwdInput = await waitFor(() => screen.getByLabelText('新密码') as HTMLInputElement)
  fireEvent.change(pwdInput, { target: { value: pwd } })
  fireEvent.click(screen.getByRole('button', { name: '确认重置' }))
}

// Step 4 人工验证第 1 条的自动化替代：身份、在线设备、登录日志三块
// 确实都渲染出了各自接口返回的数据。
test('初次加载渲染身份、在线设备、登录日志三块数据', async () => {
  stubFetchSequence(
    new Response(JSON.stringify(sampleUser), { status: 200 }),
    new Response(JSON.stringify([sampleSession]), { status: 200 }),
    new Response(
      JSON.stringify([
        { id: 'log-1', identityType: 'phone', subject: '138****0000', event: 'login', success: true, reason: '', ip: '9.9.9.9', ua: '', createdAt: 1700000000000 },
      ]),
      { status: 200 },
    ),
  )

  renderDetail()

  await waitFor(() => expect(screen.getByText('张三')).toBeTruthy())
  expect(screen.getByText('是否设过密码：是')).toBeTruthy()
  expect(screen.getByText('138****0000')).toBeTruthy() // 身份表的标识列
  expect(screen.getByText('1.2.3.4')).toBeTruthy() // 在线设备表的 IP 列
  expect(screen.getByText('成功')).toBeTruthy() // 登录日志表的结果列
})

// 【辨别力】后端 AccountService.SetStatus 迁移到不可登录状态时连带撤销
// 全部会话（这条安全耦合的真实位置在 internal/service/account.go，不在
// 这个页面里）。界面必须如实反映：冻结成功后不能只刷新用户状态，还得把
// 在线设备列表也重新拉一遍，否则管理员看到的是一份已经全部失效、却还
// 显示在表格里的"僵尸设备"列表。断言方式刻意数请求次数而不是只看
// UI 有没有报错——只看报错的话，删掉 sessions.reload() 这一行代码之后
// 页面照样能正常渲染、不抛任何异常，测试却应该为此变红。
// 【终审必须修】冻结账号现在要经过二次确认弹窗（ConfirmDialog）——弹窗
// 内的确认按钮是"确认冻结"，与触发按钮"冻结账号"文案不同（照抄
// ResetPasswordDialog 的先例：触发是"重置密码"、确认是"确认重置"），
// 避免两个按钮同名导致 getByRole 命中多个元素。
test('冻结账号成功后，在线设备列表被重新拉取', async () => {
  const frozenUser: User = { ...sampleUser, status: 'FROZEN' }
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify(sampleUser), { status: 200 }), // GET 用户详情
    new Response(JSON.stringify([sampleSession]), { status: 200 }), // GET 在线设备
    new Response(JSON.stringify([]), { status: 200 }), // GET 登录日志
    new Response(JSON.stringify(frozenUser), { status: 200 }), // PATCH 状态
    new Response(JSON.stringify(frozenUser), { status: 200 }), // reload GET 用户详情
    new Response(JSON.stringify([]), { status: 200 }), // reload GET 在线设备——已被撤销，变空
  )

  renderDetail()
  await waitFor(() => expect(screen.getByText('张三')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '冻结账号' }))
  const confirmBtn = await waitFor(() => screen.getByRole('button', { name: '确认冻结' }))
  fireEvent.click(confirmBtn)

  // 等按钮文案翻成"解除冻结"，说明 user.reload() 那次 GET 已经跑完并
  // 重新渲染过——这一步只是确保下面数请求次数时冻结流程已经走完，
  // 不是在测按钮文案本身。
  await waitFor(() => expect(screen.getByRole('button', { name: '解除冻结' })).toBeTruthy())

  expect(sessionsListCallCount(fetchMock)).toBe(2)
})

// 【终审 3(A) 的核心行为】二次确认弹窗必须真的能拦下操作，不能只是摆设。
// 点"冻结账号"弹出弹窗后点"取消"，不能发出任何 PATCH /status 请求——
// 用户的状态必须原封不动地停在 ACTIVE。
test('冻结账号弹窗点取消，不会真的冻结账号', async () => {
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify(sampleUser), { status: 200 }),
    new Response(JSON.stringify([sampleSession]), { status: 200 }),
    new Response(JSON.stringify([]), { status: 200 }),
  )

  renderDetail()
  await waitFor(() => expect(screen.getByText('张三')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '冻结账号' }))
  const cancelBtn = await waitFor(() => screen.getByRole('button', { name: '取消' }))
  fireEvent.click(cancelBtn)

  // 给一点时间：如果实现有问题（比如取消按钮误接到了 onConfirm），
  // 请求会在这段等待内被发出。
  await new Promise((r) => setTimeout(r, 50))

  expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(false)
  expect(screen.getByRole('button', { name: '冻结账号' })).toBeTruthy()
})

// 与上一条同理：管理员重置密码在 AccountService.ResetPassword 里同样会
// 撤销该用户全部会话，设备列表必须跟着刷新。
test('重置密码成功后，在线设备列表被重新拉取', async () => {
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify(sampleUser), { status: 200 }), // GET 用户详情
    new Response(JSON.stringify([sampleSession]), { status: 200 }), // GET 在线设备
    new Response(JSON.stringify([]), { status: 200 }), // GET 登录日志
    new Response(null, { status: 204 }), // PUT 重置密码，无响应体
    new Response(JSON.stringify(sampleUser), { status: 200 }), // reload GET 用户详情
    new Response(JSON.stringify([]), { status: 200 }), // reload GET 在线设备——已被撤销，变空
  )

  renderDetail()
  await waitFor(() => expect(screen.getByText('张三')).toBeTruthy())

  await openResetDialogAndSubmit()

  await waitFor(() => expect(sessionsListCallCount(fetchMock)).toBe(2))
})

// 【辨别力】PUT /users/{id}/password 后端返回 204 且没有响应体
// （internal/httpapi/user.go 的 setPassword）。api.ts 已经对 204 特殊处理、
// 不会去调 res.json()，但这条测试守住的是"这个页面的重置密码流程"整体
// 没有在别处引入解析——万一有人在 ResetPasswordDialog.submit 里改成
// `const res = await api.put(...); res.xxx`，204 响应体是 undefined，
// 取属性会抛 TypeError，走进 catch 分支变成 toast.error，而不是这里
// 断言的 toast.success。
test('重置密码接口返回 204 无响应体时，正常提示成功且不因解析响应体报错', async () => {
  stubFetchSequence(
    new Response(JSON.stringify(sampleUser), { status: 200 }),
    new Response(JSON.stringify([sampleSession]), { status: 200 }),
    new Response(JSON.stringify([]), { status: 200 }),
    new Response(null, { status: 204 }),
    new Response(JSON.stringify(sampleUser), { status: 200 }),
    new Response(JSON.stringify([]), { status: 200 }),
  )
  const successSpy = vi.spyOn(toast, 'success').mockImplementation(() => 'toast-id')
  const errorSpy = vi.spyOn(toast, 'error').mockImplementation(() => 'toast-id')

  renderDetail()
  await waitFor(() => expect(screen.getByText('张三')).toBeTruthy())

  await openResetDialogAndSubmit()

  await waitFor(() => expect(successSpy).toHaveBeenCalledWith('密码已重置，该用户全部设备已下线'))
  expect(errorSpy).not.toHaveBeenCalled()
})

// 状态机（domain.CanTransitionUserStatus）只允许 ACTIVE ↔ FROZEN 由管理员
// 在这个页面操作；PENDING_DELETE/DELETED 是终端用户自助注销流程走出来的
// 状态，管理员不该在这里把它们当成"能一键切回去"的开关，所以这两种状态下
// 冻结/解冻按钮都不应该渲染。
test.each(['PENDING_DELETE', 'DELETED'] as const)(
  '状态为 %s 时不渲染冻结/解冻按钮',
  async (status) => {
    const u: User = { ...sampleUser, status }
    stubFetchSequence(
      new Response(JSON.stringify(u), { status: 200 }),
      new Response(JSON.stringify([]), { status: 200 }),
      new Response(JSON.stringify([]), { status: 200 }),
    )

    renderDetail()
    await waitFor(() => expect(screen.getByText('张三')).toBeTruthy())

    expect(screen.queryByRole('button', { name: '冻结账号' })).toBeNull()
    expect(screen.queryByRole('button', { name: '解除冻结' })).toBeNull()
    // 重置密码不受这条状态机限制，任何状态下都应该照常可用。
    expect(screen.getByRole('button', { name: '重置密码' })).toBeTruthy()
  },
)

// 踢单个设备走的是 DELETE /users/{id}/sessions/{sid}，不经过
// AccountService 里"迁移状态"那条耦合，但界面同样必须刷新设备列表——
// 不然管理员点了"下线"，那一行却还留在表格里，会以为操作没生效。
test('踢单个设备成功后，设备列表被重新拉取', async () => {
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify(sampleUser), { status: 200 }), // GET 用户详情
    new Response(JSON.stringify([sampleSession]), { status: 200 }), // GET 在线设备
    new Response(JSON.stringify([]), { status: 200 }), // GET 登录日志
    new Response(JSON.stringify({ revoked: 1 }), { status: 200 }), // DELETE 单个设备
    new Response(JSON.stringify([]), { status: 200 }), // reload GET 在线设备——已清空
  )

  renderDetail()
  await waitFor(() => expect(screen.getByText('张三')).toBeTruthy())
  await waitFor(() => expect(screen.getByText('1.2.3.4')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '下线' }))

  await waitFor(() => expect(screen.getByText('没有在线设备')).toBeTruthy())
  expect(sessionsListCallCount(fetchMock)).toBe(2)
})

// 复审第 1 轮 Important：revokeAll 是四个会触发 reload 的操作
// （冻结/重置密码/踢单个设备/全部下线）里唯一会去读自己响应体字段的——
// UserDetail.tsx 里 `已下线 ${res.revoked} 个设备` 直接把 DELETE
// /users/{id}/sessions 的响应体字段拼进提示文案。revokeOne 那条测试
// 完全不碰响应体（它拿到 { revoked: 1 } 但从来不读），所以那条测试对
// "字段名/形状漂移"这类回归的覆盖是零；revokeAll 也走一条不同的路径
// （DELETE .../sessions 而不是 .../sessions/{sid}）。这条测试特意让
// mock 返回 { revoked: 3 }（不是 1，避免"提示文案里凑巧出现别的数字"
// 这种假阳性），断言里同时守两件独立的事：
//   1. sessionsListCallCount === 2——全部下线后设备列表被重新拉取；
//   2. toast.success 收到的文案精确是"已下线 3 个设备"——res.revoked
//      这个字段真的被正确读出来拼进了提示，不是读到 undefined 或者
//      读错了字段名。
// 【终审必须修】全部下线现在也要经过二次确认弹窗，确认按钮文案是
// "确认全部下线"，与触发按钮"全部下线"不同名，理由同上一条冻结账号。
test('全部下线成功后，在线设备列表被重新拉取，且提示文案带上后端返回的下线数量', async () => {
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify(sampleUser), { status: 200 }), // GET 用户详情
    new Response(JSON.stringify([sampleSession]), { status: 200 }), // GET 在线设备
    new Response(JSON.stringify([]), { status: 200 }), // GET 登录日志
    new Response(JSON.stringify({ revoked: 3 }), { status: 200 }), // DELETE 全部设备
    new Response(JSON.stringify([]), { status: 200 }), // reload GET 在线设备——已清空
  )
  const successSpy = vi.spyOn(toast, 'success').mockImplementation(() => 'toast-id')

  renderDetail()
  await waitFor(() => expect(screen.getByText('张三')).toBeTruthy())
  await waitFor(() => expect(screen.getByText('1.2.3.4')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '全部下线' }))
  const confirmBtn = await waitFor(() => screen.getByRole('button', { name: '确认全部下线' }))
  fireEvent.click(confirmBtn)

  await waitFor(() => expect(sessionsListCallCount(fetchMock)).toBe(2))
  expect(successSpy).toHaveBeenCalledWith('已下线 3 个设备')
})

// 二次确认弹窗必须真的能拦下操作：点"取消"不能发出 DELETE /sessions。
test('全部下线弹窗点取消，不会真的下线任何设备', async () => {
  const fetchMock = stubFetchSequence(
    new Response(JSON.stringify(sampleUser), { status: 200 }),
    new Response(JSON.stringify([sampleSession]), { status: 200 }),
    new Response(JSON.stringify([]), { status: 200 }),
  )

  renderDetail()
  await waitFor(() => expect(screen.getByText('张三')).toBeTruthy())
  await waitFor(() => expect(screen.getByText('1.2.3.4')).toBeTruthy())

  fireEvent.click(screen.getByRole('button', { name: '全部下线' }))
  const cancelBtn = await waitFor(() => screen.getByRole('button', { name: '取消' }))
  fireEvent.click(cancelBtn)

  await new Promise((r) => setTimeout(r, 50))

  expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false)
  // 设备行还在——没有被误删。
  expect(screen.getByText('1.2.3.4')).toBeTruthy()
})
