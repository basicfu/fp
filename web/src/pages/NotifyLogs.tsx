import { useEffect, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { NativeSelect } from '@/components/NativeSelect'
import Pagination from '@/components/Pagination'
import { api } from '@/lib/api'
import { formatTime } from '@/lib/format'
import { notifyChannelLabels } from '@/lib/notify'
import { PAGE_SIZE } from '@/lib/query'
import { useResource } from '@/lib/useResource'
import type { NotifyChannel, NotifyLog } from '@/lib/types'

export default function NotifyLogs() {
  // codeInput 是输入框里的字，code 才是用来查询的值。后端按 code 精确匹配，敲到一半的 code 一定匹配不到，
  // 每个字符发一次请求只会让表格在"没有记录"与结果之间来回跳，所以停顿一下再一起生效。
  const [codeInput, setCodeInput] = useState('')
  const [code, setCode] = useState('')
  const [success, setSuccess] = useState('')
  const [page, setPage] = useState(1)

  const logs = useResource(() => {
    const q = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String((page - 1) * PAGE_SIZE) })
    if (code) q.set('code', code)
    if (success) q.set('success', success)
    return api.get<{ items: NotifyLog[]; total: number }>(`/notify/logs?${q.toString()}`)
  }, [code, success, page])

  useEffect(() => {
    const next = codeInput.trim()
    if (next === code) return
    const t = setTimeout(() => {
      setCode(next)
      setPage(1)
    }, 300)
    return () => clearTimeout(t)
  }, [codeInput, code])

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label="按模板 code 筛选"
          className="w-64"
          placeholder="模板 code（精确匹配）"
          value={codeInput}
          onChange={(e) => setCodeInput(e.target.value)}
        />
        <NativeSelect
          aria-label="按结果筛选"
          className="w-32"
          value={success}
          onChange={(e) => {
            setSuccess(e.target.value)
            setPage(1)
          }}
        >
          <option value="">全部结果</option>
          <option value="true">成功</option>
          <option value="false">失败</option>
        </NativeSelect>
        <p className="text-sm text-muted-foreground">只记录结果，不记录变量取值与渲染后的内容。</p>
      </div>

      {logs.loading && !logs.data && <p className="text-sm text-muted-foreground">加载中…</p>}
      {logs.error && <p className="text-sm text-destructive">{logs.error}</p>}
      {logs.data && (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>时间</TableHead>
                <TableHead>模板</TableHead>
                <TableHead>渠道</TableHead>
                <TableHead>目标</TableHead>
                <TableHead>供应商</TableHead>
                <TableHead>结果</TableHead>
                <TableHead>错误</TableHead>
                <TableHead>调用方</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {logs.data.items.length === 0 && (
                <TableRow>
                  <TableCell colSpan={8} className="text-center text-muted-foreground">
                    没有记录
                  </TableCell>
                </TableRow>
              )}
              {logs.data.items.map((l) => (
                <TableRow key={l.id}>
                  <TableCell className="text-muted-foreground">{formatTime(l.createdAt)}</TableCell>
                  <TableCell className="font-mono text-xs">{l.code}</TableCell>
                  <TableCell>{notifyChannelLabels[l.channel as NotifyChannel] ?? l.channel}</TableCell>
                  <TableCell>{l.target || '-'}</TableCell>
                  <TableCell className="font-mono text-xs">{l.provider}</TableCell>
                  <TableCell>
                    <Badge variant={l.success ? 'default' : 'destructive'}>{l.success ? '成功' : '失败'}</Badge>
                  </TableCell>
                  <TableCell className="max-w-xs whitespace-normal break-words text-xs text-muted-foreground">{l.error}</TableCell>
                  <TableCell className="font-mono text-xs">{l.appId || '控制台'}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <Pagination page={page} total={logs.data.total} pageSize={PAGE_SIZE} onPage={setPage} />
        </>
      )}
    </div>
  )
}
