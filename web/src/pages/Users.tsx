import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { Search } from 'lucide-react'
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
import { userStatusBadgeClassName, GRAY } from '@/lib/status-badge'
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

  // 搜索框是受控组件，本地状态是"真身"，keyword（来自 URL）只在它变化时
  // 单向同步进来——见下面 Input 旁边的注释，这是为了不让提交搜索时的
  // 卸载重挂把输入框焦点弄丢。
  const [keywordInput, setKeywordInput] = useState(keyword)
  useEffect(() => {
    setKeywordInput(keyword)
  }, [keyword])

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
          // 改搜索条件必须回到第 1 页：停在第 5 页搜一个只有 3 条结果的
          // 关键词，会得到一张空表，看起来像"搜不到"。
          update({ keyword: keywordInput, page: '' })
        }}
      >
        {/*
          这个输入框曾经是非受控的（key={keyword} + defaultValue），靠
          key 随 keyword 变化去强制卸载重挂来让后退/前进时的回填生效——
          但提交搜索同样会让 keyword 变化，一按 Enter，输入框就被卸载
          重挂一次，光标/焦点随之丢失，键盘用户会看到焦点跳出输入框。
          现在改成受控：keywordInput 这个本地 state 才是输入框的"真身"，
          下面的 useEffect 只在 keyword（来自 URL）变化时把它同步过去。
          提交搜索时 keywordInput 已经等于新 keyword，effect 是
          no-op，节点不会被卸载，焦点保得住；浏览器后退导致 keyword
          变回旧值时，effect 把 keywordInput 更新回去，输入框跟着回填，
          节点同样没有被卸载重挂过。
        */}
        <div className="relative w-64">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            name="keyword"
            value={keywordInput}
            onChange={(e) => setKeywordInput(e.target.value)}
            placeholder="手机号 / 用户名 / 昵称"
            className="w-64 pl-8"
          />
        </div>
        <Button type="submit" variant="secondary">搜索</Button>

        <Select
          value={status || ALL}
          // @base-ui/react 的 Select.Root#onValueChange 签名里 value 是
          // `string | null`（单选模式下允许 null，虽然这里受控取值恒来自
          // SelectItem 不会真的传出 null）。update() 收的是
          // Record<string, string>，直接把 v 塞进去在 tsc -b 下报
          // TS2322，所以把 v === null 与 v === ALL 一并归一成空字符串。
          onValueChange={(v) => update({ status: v === ALL || v === null ? '' : v, page: '' })}
          // items 让 Select.Value 能把受控 value 映射回中文标签；缺了它，
          // 收起状态会直接显示 value 本身（如 "active"）而不是"已启用"。
          items={[{ value: ALL, label: '全部状态' }, ...(Object.keys(statusLabels) as UserStatus[]).map((s) => ({ value: s, label: statusLabels[s] }))]}
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
                      <Badge className={userStatusBadgeClassName[u.status] ?? GRAY}>
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
