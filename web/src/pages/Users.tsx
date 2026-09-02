import { Link, useSearchParams } from 'react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import Pagination from '@/components/Pagination'
import { api } from '@/lib/api'
import { useResource } from '@/lib/useResource'
import { buildUserQuery, normalizePage, PAGE_SIZE } from '@/lib/query'
import { formatTime } from '@/lib/format'
import { statusLabels } from '@/lib/labels'
import type { UserListResponse, UserStatus } from '@/lib/types'

const ALL = '__all__'

export default function Users() {
  // 查询条件放进 URL：刷新、后退、把链接发给同事，都能回到同一个视图。
  const [sp, setSp] = useSearchParams()
  // normalizePage 同时也是 buildUserQuery 算 offset 用的那个函数——两处
  // 用同一套钳制逻辑，URL 上的页码和实际发给后端的 offset 就不会因为
  // 各自实现一遍而悄悄对不上。sp.get('page') 在参数缺失时是 null（不是
  // undefined），被手动改成 "abc"/"-1"/"2.7" 时 normalizePage 也会兜底
  // 到 1，不会把 NaN/负数/小数透传给下面的 Pagination 组件。
  const page = normalizePage(sp.get('page'))
  const keyword = sp.get('keyword') ?? ''
  const status = sp.get('status') ?? ''

  const list = useResource(
    () => api.get<UserListResponse>(`/users?${buildUserQuery({ page, keyword, status })}`),
    [page, keyword, status],
  )

  function update(next: Record<string, string>) {
    const q = new URLSearchParams(sp)
    for (const [k, v] of Object.entries(next)) {
      if (v) q.set(k, v)
      else q.delete(k)
    }
    setSp(q)
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">用户</h1>

      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          const v = new FormData(e.currentTarget).get('keyword')
          // 改搜索条件必须回到第 1 页：停在第 5 页搜一个只有 3 条结果的
          // 关键词，会得到一张空表，看起来像"搜不到"。
          update({ keyword: String(v ?? ''), page: '' })
        }}
      >
        {/*
          key={keyword}：这个输入框是非受控的（defaultValue，不是
          value+onChange），故意的——键入过程不必每敲一下就触发整个页面
          重渲染。但非受控组件的 defaultValue 只在"挂载那一刻"生效，
          之后就不再跟随 props 更新。问题是：提交搜索、点分页、切状态
          筛选走的都是 setSp()，这类只改查询参数的导航在同一个
          <Route path="/users"> 上只会重渲染 Users，不会重新挂载它——
          浏览器"后退/前进"到一个不同的 ?keyword= 时同样如此。不加 key
          的话，后退到"没有关键词"的历史记录，地址栏和表格数据都变了，
          这个输入框里却还留着后退前敲的字，用户会以为筛选没生效。
          key 随 keyword 变化，等于是在"关键词真的变了"这一刻强制卸载
          重挂输入框，让新的 defaultValue 重新生效——按下搜索按钮时
          keyword 恰好等于用户刚敲的内容，这次重挂不会造成任何可见跳变。
        */}
        <Input key={keyword} name="keyword" defaultValue={keyword} placeholder="手机号 / 用户名 / 昵称" className="w-64" />
        <Button type="submit" variant="secondary">搜索</Button>

        <Select
          value={status || ALL}
          // @base-ui/react 的 Select.Root#onValueChange 签名里 value 是
          // `string | null`（单选模式下允许 null，虽然这里受控取值恒来自
          // SelectItem 不会真的传出 null）。update() 收的是
          // Record<string, string>，直接把 v 塞进去在 tsc -b 下报
          // TS2322，所以把 v === null 与 v === ALL 一并归一成空字符串。
          onValueChange={(v) => update({ status: v === ALL || v === null ? '' : v, page: '' })}
        >
          <SelectTrigger className="w-40"><SelectValue placeholder="全部状态" /></SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>全部状态</SelectItem>
            {(Object.keys(statusLabels) as UserStatus[]).map((s) => (
              <SelectItem key={s} value={s}>{statusLabels[s]}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </form>

      {list.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {list.error && <p className="text-sm text-destructive">{list.error}</p>}

      {list.data && (
        <>
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>昵称</TableHead>
                  <TableHead>登录标识</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>注册时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.data.items.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={4} className="text-center text-muted-foreground">没有匹配的用户</TableCell>
                  </TableRow>
                )}
                {list.data.items.map((u) => (
                  <TableRow key={u.id}>
                    <TableCell>
                      <Link to={`/users/${u.id}`} className="font-medium underline-offset-4 hover:underline">
                        {u.nickname || '（未设置）'}
                      </Link>
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {u.identities.map((i) => `${i.type}:${i.subject}`).join('  ') || '-'}
                    </TableCell>
                    <TableCell>
                      <Badge variant={u.status === 'ACTIVE' ? 'default' : 'secondary'}>
                        {statusLabels[u.status] ?? u.status}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground">{formatTime(u.createdAt)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          <Pagination
            page={page}
            total={list.data.total}
            pageSize={PAGE_SIZE}
            onPage={(p) => update({ page: String(p) })}
          />
        </>
      )}
    </div>
  )
}
