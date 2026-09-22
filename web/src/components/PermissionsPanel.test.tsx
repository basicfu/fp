import { disabledIMConfig } from '@/lib/testFixtures'
import { test, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import PermissionsPanel from './PermissionsPanel'
import type { Application, PermissionPoint } from '@/lib/types'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const app: Application = {
  id: 'app-1',
  name: '商城',
  code: 'mall',
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
  createdAt: 1700000000000,
  updatedAt: 1700000000000,
}

function point(over: Partial<PermissionPoint>): PermissionPoint {
  return {
    id: 'p1',
    key: 'GET:/orders/{id}',
    name: '查看订单',
    kind: 'api',
    source: 'app',
    status: 'normal',
    staleForMs: 0,
    lastSeenAt: 1700000000000,
    createdAt: 1700000000000,
    ...over,
  }
}

function stubFetch(perms: PermissionPoint[], holders: string[] = [], onWrite?: (url: string, m: string) => void) {
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method !== 'GET') {
      onWrite?.(url, method)
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    const body: Record<string, unknown> = {
      '/admin/api/applications/app-1/permissions': perms,
      '/admin/api/permissions/p1/holders': { roles: holders },
    }
    if (!(url in body)) return Promise.reject(new Error(`测试没有为 ${method} ${url} 准备响应`))
    return Promise.resolve(new Response(JSON.stringify(body[url]), { status: 200 }))
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

function renderPanel(a: Application = app) {
  return render(
    <MemoryRouter>
      <PermissionsPanel app={a} />
    </MemoryRouter>,
  )
}

/**
 * 专给"添加权限"批量提交用的假后端：POST 按 key 各自判断成功/失败，
 * 不像上面 stubFetch 那样一律返回 204——批量提交要能表达"这一行失败、
 * 别的照样成功"，不能所有写请求共用同一个响应。
 */
function stubFetchCreate(perms: PermissionPoint[], failKeys: string[] = []) {
  const writes: { key: string; name: string; kind: string }[] = []
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method === 'GET' && url === '/admin/api/applications/app-1/permissions') {
      return Promise.resolve(new Response(JSON.stringify(perms), { status: 200 }))
    }
    if (method === 'POST' && url === '/admin/api/applications/app-1/permissions') {
      const body = JSON.parse(String(init?.body)) as { key: string; name: string; kind: string }
      writes.push(body)
      if (failKeys.includes(body.key)) {
        return Promise.resolve(
          new Response(JSON.stringify({ code: 'PERMISSION_EXISTS', msg: `标识重复：${body.key}` }), { status: 409 }),
        )
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({
            id: `new-${writes.length}`,
            key: body.key,
            name: body.name,
            kind: 'api',
            source: 'manual',
            status: 'manual',
            createdAt: 1,
          }),
          { status: 201 },
        ),
      )
    }
    return Promise.reject(new Error(`测试没有为 ${method} ${url} 准备响应`))
  })
  vi.stubGlobal('fetch', fn)
  return writes
}

/**
 * 专给"批量删除"用的假后端：每个权限点各自有自己的 holders，DELETE
 * 按 id 各自判断成功/失败——跟单条删除的 stubFetch 不一样，那个所有
 * holders 请求共用同一份数据，批量场景需要区分"这几个权限点各自被
 * 谁持有"，也需要表达"这一条删除失败、别的照样成功"。
 */
function stubFetchBatchDelete(initialPerms: PermissionPoint[], holdersById: Record<string, string[]>, failIds: string[] = []) {
  let perms = initialPerms
  const deletes: string[] = []
  const fn = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method === 'GET' && url === '/admin/api/applications/app-1/permissions') {
      return Promise.resolve(new Response(JSON.stringify(perms), { status: 200 }))
    }
    const holdersMatch = /^\/admin\/api\/permissions\/([^/]+)\/holders$/.exec(url)
    if (method === 'GET' && holdersMatch) {
      return Promise.resolve(new Response(JSON.stringify({ roles: holdersById[holdersMatch[1]] ?? [] }), { status: 200 }))
    }
    const deleteMatch = /^\/admin\/api\/permissions\/([^/]+)$/.exec(url)
    if (method === 'DELETE' && deleteMatch) {
      const id = deleteMatch[1]
      deletes.push(id)
      if (failIds.includes(id)) {
        return Promise.resolve(new Response(JSON.stringify({ code: 'INTERNAL', msg: '删除失败' }), { status: 500 }))
      }
      // 删成功要把这一项从下次 GET 的结果里摘掉——批量删除成功后会
      // perms.reload()，测试要能看到"真的少了一条"，不是重新拿到同一份。
      perms = perms.filter((p) => p.id !== id)
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    return Promise.reject(new Error(`测试没有为 ${method} ${url} 准备响应`))
  })
  vi.stubGlobal('fetch', fn)
  return deletes
}

// 【辨别力】删除前必须先问"谁在用它"，并把角色名写进确认框。
//
// 这是唯一的安全网：删除是真删，并且会连带删掉所有角色对它的授权
// （数据库外键级联），没有可逆的"停用"中间态。一个直接弹"确定删除吗"
// 的实现在界面上毫无异样，但管理员是在完全不知道会影响谁的情况下按下
// 确认的。所以断言两件事：确实发了 holders 请求，且角色名出现在文案里。
test('删除前先查持有者，并在确认框里点名', async () => {
  const fetchMock = stubFetch([point({})], ['商城管理员', '客服'])
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '删除' }))

  await waitFor(() => expect(screen.getByText(/商城管理员、客服/)).toBeTruthy())
  expect(screen.getByText(/2 个角色/)).toBeTruthy()
  expect(fetchMock.mock.calls.some(([u]) => String(u) === '/admin/api/permissions/p1/holders')).toBe(true)
})

// 没人持有时文案要说"不会让任何人掉权限"，而不是照样吓唬人。
test('没有角色持有时，确认框说明不会影响任何人', async () => {
  stubFetch([point({})], [])
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '删除' }))

  await waitFor(() => expect(screen.getByText(/没有角色持有它/)).toBeTruthy())
})

// 确认之后才真的发 DELETE。
test('确认后发出 DELETE', async () => {
  const writes: string[] = []
  stubFetch([point({})], [], (url, m) => writes.push(`${m} ${url}`))
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '删除' }))
  const confirm = await waitFor(() => screen.getByRole('button', { name: '确认删除' }))
  fireEvent.click(confirm)

  await waitFor(() => expect(writes).toContain('DELETE /admin/api/permissions/p1'))
})

// 【辨别力】「过渡中」必须说清楚"不代表已经没了"。
//
// SDK 上报的是全量快照，但快照里缺一条不等于它消失了——滚动发布时新旧
// 版本同时在跑、交替上报。一个把这个状态显示成"已删除"或不加解释的界面
// 会诱导管理员去删掉一个其实还在服务的接口，那会让线上掉权限。
test('有过渡中的权限点时给出解释，而不是只标一个状态', async () => {
  stubFetch([point({ status: 'stale', staleForMs: 3 * 3600 * 1000 })])
  renderPanel()

  await waitFor(() => expect(screen.getByText('过渡中')).toBeTruthy())
  expect(screen.getByText(/这不代表它们已经没了|不代表它们已经没了/)).toBeTruthy()
  // 已过渡时长要显示出来——它是人判断"该不该删"的唯一依据
  expect(screen.getByText('3 小时')).toBeTruthy()
})

test('没有过渡中的权限点时不显示那条提示', async () => {
  stubFetch([point({ status: 'normal' })])
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  expect(screen.queryByText(/不代表它们已经没了/)).toBeNull()
})

// 手动添加的权限点没有"最近上报"可言，不能显示成 1970 年。
test('手动添加的权限点不显示上报时间', async () => {
  stubFetch([point({ source: 'manual', status: 'manual', lastSeenAt: 0 })])
  renderPanel()

  await waitFor(() => expect(screen.getByText('手动')).toBeTruthy())
  expect(screen.getByText('（手动添加）')).toBeTruthy()
})

// 空表格统一显示"没有数据"，不再是权限点专属的那句背景说明——全站的空
// 表格现在都用这一句，不能这个页面单独例外。
test('一个权限点都没有时显示"没有数据"', async () => {
  stubFetch([])
  renderPanel()

  await waitFor(() => expect(screen.getByText('没有数据')).toBeTruthy())
})

// 【辨别力】添加权限要支持一次粘贴多行批量建，不能retreat回"一次只能填一条"。
//
// 格式是「标识 名称」以空格分割、名称可选、一行一条——这是最容易被简化掉
// 的地方：一个只解析第一行、或者把整段文本原样当 key 提交的实现，界面上
// 一样能弹出成功提示，只是实际建出来的权限点数量或内容不对。
test('添加权限支持多行批量：按行拆成 key/name 分别提交', async () => {
  const writes = stubFetchCreate([])
  renderPanel()

  fireEvent.click(screen.getByRole('button', { name: '添加权限' }))
  const textarea = await screen.findByLabelText('权限点')
  fireEvent.change(textarea, { target: { value: 'GET:/user/login 登录\nGET:/user/register' } })
  fireEvent.click(screen.getByRole('button', { name: '添加' }))

  await waitFor(() => expect(writes.length).toBe(2))
  expect(writes[0]).toEqual({ key: 'GET:/user/login', name: '登录', kind: 'api' })
  expect(writes[1]).toEqual({ key: 'GET:/user/register', name: '', kind: 'api' })
  // 全部成功后对话框应该自动关闭，不用再手动点"取消"。
  await waitFor(() => expect(screen.queryByLabelText('权限点')).toBeNull())
})

// 【辨别力】批量提交里有一行失败，不能让整批都白提交、也不能悄悄吞掉失败。
//
// 逐条提交而不是一个数组请求，失败的那一行必须留在输入框里方便改完重
// 试，已经成功的不该让人再重填一遍——一个用 Promise.all 一把梭的实现，
// 要么一失败就全部回滚（明明有几行已经真的建成功了），要么直接把原始
// 整段文本原样退回（连成功的也要重新打一遍）。
test('批量提交部分失败：成功的不用重填，失败的留在框里', async () => {
  const writes = stubFetchCreate([], ['GET:/user/register'])
  renderPanel()

  fireEvent.click(screen.getByRole('button', { name: '添加权限' }))
  const textarea = (await screen.findByLabelText('权限点')) as HTMLTextAreaElement
  fireEvent.change(textarea, { target: { value: 'GET:/user/login 登录\nGET:/user/register 注册' } })
  fireEvent.click(screen.getByRole('button', { name: '添加' }))

  await waitFor(() => expect(writes.length).toBe(2))
  // 对话框没关——还有失败的没处理完。
  expect(screen.getByLabelText('权限点')).toBeTruthy()
  await waitFor(() => expect((screen.getByLabelText('权限点') as HTMLTextAreaElement).value).toBe('GET:/user/register 注册'))
})

const perm2 = point({ id: 'p2', key: 'DELETE:/orders/{id}', name: '删除订单' })

// 【辨别力】批量删除前必须像单条删除一样先查一遍持有者，把角色名合并
// 去重写进确认框——不能因为是"批量"就退化成一句"确定删除吗"。
test('批量删除：勾选多条后显示已选数量，确认框列出去重后的角色，逐条发 DELETE', async () => {
  const deletes = stubFetchBatchDelete([point({}), perm2], { p1: ['商城管理员'], p2: ['商城管理员', '客服'] })
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('checkbox', { name: '选中 GET:/orders/{id}' }))
  fireEvent.click(screen.getByRole('checkbox', { name: '选中 DELETE:/orders/{id}' }))
  expect(screen.getByText('已选 2 项')).toBeTruthy()

  fireEvent.click(screen.getByRole('button', { name: '批量删除' }))
  // 两个角色都持有，但"商城管理员"两条都持有——确认框只该提一次。
  await waitFor(() => expect(screen.getByText(/商城管理员、客服|客服、商城管理员/)).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '确认删除' }))

  await waitFor(() => expect(deletes.sort()).toEqual(['p1', 'p2']))
})

// 全选表头勾选框要能一次选中/取消当前列表里的全部行。
test('表头的全选勾选框选中/取消当前显示的全部行', async () => {
  stubFetchBatchDelete([point({}), perm2], {})
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('checkbox', { name: '全选' }))
  expect(screen.getByText('已选 2 项')).toBeTruthy()

  fireEvent.click(screen.getByRole('checkbox', { name: '全选' }))
  expect(screen.queryByText(/已选/)).toBeNull()
})

// 【辨别力】批量删除有一条失败，不能让整批都当没发生过，也不能让已经
// 删成功的那条还留在选中状态里逼人再选一遍。
test('批量删除部分失败：成功的从选中里移除，失败的保留', async () => {
  const deletes = stubFetchBatchDelete([point({}), perm2], {}, ['p2'])
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('checkbox', { name: '全选' }))
  fireEvent.click(screen.getByRole('button', { name: '批量删除' }))
  fireEvent.click(await screen.findByRole('button', { name: '确认删除' }))

  await waitFor(() => expect(deletes.sort()).toEqual(['p1', 'p2']))
  // p1 删成功，重新拉取后的列表里它已经不在了，只剩 p2 这一行还选中。
  await waitFor(() => expect(screen.queryByText('GET:/orders/{id}')).toBeNull())
  expect(screen.getByText('已选 1 项')).toBeTruthy()
})

// 【辨别力】点整行也要能选中，不能逼人非得瞄准那个 16px 的小方块。
test('点击行内空白处（不是勾选框）也能选中/取消这一行', async () => {
  stubFetchBatchDelete([point({}), perm2], {})
  renderPanel()

  const row = (await screen.findByText('GET:/orders/{id}')).closest('tr') as HTMLElement
  fireEvent.click(row)
  expect(screen.getByText('已选 1 项')).toBeTruthy()
  expect(screen.getByRole('checkbox', { name: '选中 GET:/orders/{id}' })).toHaveProperty('ariaChecked', 'true')

  fireEvent.click(row)
  expect(screen.queryByText(/已选/)).toBeNull()
})

// 行本身现在也能点击选中了，行内的"编辑"/"删除"按钮必须挡住冒泡——
// 不然点它们会先把这一行选中/取消选中，跟按钮本来要做的事完全不沾边。
test('点行内的"编辑"/"删除"按钮不会顺带触发整行的选中', async () => {
  stubFetchBatchDelete([point({})], {})
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('button', { name: '编辑' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '保存' })).toBeTruthy())
  expect(screen.queryByText(/已选/)).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: '取消' }))

  fireEvent.click(screen.getByRole('button', { name: '删除' }))
  // 删除走的是"先查持有者再弹确认框"，等确认框出现，确保这次异步没有
  // 顺带把行选中。
  await waitFor(() => expect(screen.getByRole('button', { name: '确认删除' })).toBeTruthy())
  expect(screen.queryByText(/已选/)).toBeNull()
})

// 【辨别力】"批量删除"按钮和"已选 N 项"要挪到表格下方，且顺序是按钮在前、
// 文字在后——不能还留在表格上方的筛选框旁边。
test('批量删除按钮和已选数量显示在表格下方，按钮在前文字在后', async () => {
  stubFetchBatchDelete([point({})], {})
  renderPanel()

  await waitFor(() => expect(screen.getByText('GET:/orders/{id}')).toBeTruthy())
  fireEvent.click(screen.getByRole('checkbox', { name: '选中 GET:/orders/{id}' }))

  const table = document.querySelector('table') as HTMLElement
  const button = screen.getByRole('button', { name: '批量删除' })
  const label = screen.getByText('已选 1 项')
  // compareDocumentPosition：button 在 table 之后，label 在 button 之后。
  expect(table.compareDocumentPosition(button) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(button.compareDocumentPosition(label) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
})
