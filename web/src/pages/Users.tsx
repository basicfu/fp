import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Search } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LabelHint } from '@/components/ui/label-hint'
import { Badge } from '@/components/ui/badge'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import Pagination from '@/components/Pagination'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { buildUserQuery, normalizePage, PAGE_SIZE } from '@/lib/query'
import { toastFormErrors } from '@/lib/formErrors'
import { formatTime } from '@/lib/format'
import { statusLabels } from '@/lib/labels'
import { userStatusBadgeClassName, GRAY } from '@/lib/status-badge'
import type { User, UserListResponse, UserStatus } from '@/lib/types'

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
  const [creating, setCreating] = useState(false)

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
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          // 改搜索条件必须回到第 1 页：停在第 5 页搜一个只有 3 条结果的
          // 关键词，会得到一张空表，看起来像"搜不到"。
          update({ keyword: keywordInput, page: '' })
        }}
      >
        <Button type="button" onClick={() => setCreating(true)}>新建用户</Button>
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

      {/* 只在真正首次加载（还没有任何数据）时显示这行文字——翻页/搜索之后的
          reload() 也会把 loading 短暂置回 true，这时候表格已经有上一次的
          数据在显示，再插一行"加载中…"只会造成一次没必要的跳动。 */}
      {list.loading && !list.data && <p className="text-sm text-muted-foreground">加载中…</p>}
      {list.error && <p className="text-sm text-destructive">{list.error}</p>}

      {list.data && (
        <>
          <Table className="table-fixed">
            <colgroup>
              <col className="w-[6%]" />
              <col className="w-[20%]" />
              <col className="w-[39%]" />
              <col className="w-[15%]" />
              <col className="w-[20%]" />
            </colgroup>
            <TableHeader>
              <TableRow>
                <TableHead className="p-0 px-2">序号</TableHead>
                <TableHead className="p-0 px-2">昵称</TableHead>
                <TableHead className="p-0 px-2">登录标识</TableHead>
                <TableHead className="p-0 px-2">状态</TableHead>
                <TableHead className="p-0 px-2">注册时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.data.items.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-muted-foreground">没有匹配的用户</TableCell>
                </TableRow>
              )}
              {list.data.items.map((u, i) => (
                <TableRow key={u.id} className="h-12">
                  <TableCell className="p-0 px-2 text-muted-foreground">{i + 1}</TableCell>
                  <TableCell className="whitespace-normal p-0 px-2">
                    <Link to={`/users/${u.id}`} className="font-medium underline-offset-4 hover:underline">
                      {u.nickname || '（未设置）'}
                    </Link>
                  </TableCell>
                  <TableCell className="whitespace-normal break-all p-0 px-2 font-mono text-xs">
                    {u.identities.map((i) => `${i.type}:${i.subject}`).join('  ') || '-'}
                  </TableCell>
                  <TableCell className="p-0 px-2">
                    <Badge className={userStatusBadgeClassName[u.status] ?? GRAY}>
                      {statusLabels[u.status] ?? u.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="whitespace-normal p-0 px-2 text-muted-foreground">
                    {formatTime(u.createdAt)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>

          <Pagination
            page={page}
            total={list.data.total}
            pageSize={PAGE_SIZE}
            onPage={(p) => update({ page: String(p) })}
          />
        </>
      )}

      <CreateDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={() => {
          setCreating(false)
          list.reload()
        }}
      />
    </div>
  )
}

const createSchema = z.object({
  phone: z.string().regex(/^1\d{10}$/, '请输入 11 位手机号'),
  nickname: z.string(),
  password: z.string().refine((v) => v === '' || v.length >= 8, '密码至少 8 位，留空表示不设密码'),
})
type CreateValues = z.infer<typeof createSchema>

/**
 * CreateDialog 是管理端手动建号的唯一入口。普通用户永远通过登录流程
 * 隐式建号，这里是给线下开户、导入这类场景用的——手机号是唯一支持的
 * 登录标识，密码可留空（留空只能靠验证码等其它方式登录）。
 */
function CreateDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: () => void
}) {
  const { register, handleSubmit, formState, reset } = useForm<CreateValues>({
    resolver: zodResolver(createSchema),
    defaultValues: { phone: '', nickname: '', password: '' },
  })

  async function onSubmit(v: CreateValues) {
    try {
      await api.post<User>('/users', { phone: v.phone, nickname: v.nickname, password: v.password })
      toast.success('已创建')
      reset()
      onCreated()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建用户</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit, toastFormErrors)} className="space-y-4" noValidate>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="user-phone">手机号</Label>
              <LabelHint>作为这个用户的登录标识，创建后不能再改。</LabelHint>
            </div>
            <Input id="user-phone" className="font-mono" {...register('phone')} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="user-nickname">昵称</Label>
            <Input id="user-nickname" {...register('nickname')} />
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="user-password">密码</Label>
              <LabelHint>可留空。留空的话这个用户只能靠验证码等其它登录方式登录。</LabelHint>
            </div>
            <Input id="user-password" type="password" {...register('password')} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={formState.isSubmitting}>
              创建
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
