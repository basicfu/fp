import { useState } from 'react'
import { useParams } from 'react-router'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatTime } from '@/lib/format'
import { statusLabels } from '@/lib/labels'
import type { LoginLog, User, UserSession } from '@/lib/types'

export default function UserDetail() {
  const { id = '' } = useParams()
  const user = useResource(() => api.get<User>(`/users/${id}`), [id])
  const sessions = useResource(() => api.get<UserSession[]>(`/users/${id}/sessions`), [id])
  const logs = useResource(() => api.get<LoginLog[]>(`/users/${id}/login-logs?limit=50`), [id])
  const [resetting, setResetting] = useState(false)

  if (user.loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (user.error) return <p className="text-sm text-destructive">{user.error}</p>
  if (!user.data) return null
  const u = user.data

  async function toggleFrozen() {
    const next = u.status === 'ACTIVE' ? 'FROZEN' : 'ACTIVE'
    try {
      await api.patch(`/users/${id}/status`, { status: next })
      toast.success(next === 'FROZEN' ? '已冻结' : '已解冻')
      user.reload()
      // 冻结会连带撤销全部会话（后端 AccountService 里的安全耦合），
      // 所以在线设备列表也要刷新，否则界面上还挂着已经失效的设备。
      sessions.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  async function revokeOne(sid: string) {
    try {
      await api.del(`/users/${id}/sessions/${sid}`)
      toast.success('已下线该设备')
      sessions.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  async function revokeAll() {
    try {
      const res = await api.del<{ revoked: number }>(`/users/${id}/sessions`)
      toast.success(`已下线 ${res.revoked} 个设备`)
      sessions.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">{u.nickname || '（未设置昵称）'}</h1>
        <Badge variant={u.status === 'ACTIVE' ? 'default' : 'secondary'}>
          {statusLabels[u.status] ?? u.status}
        </Badge>
        <div className="flex-1" />
        <Button variant="outline" onClick={() => setResetting(true)}>重置密码</Button>
        {(u.status === 'ACTIVE' || u.status === 'FROZEN') && (
          <Button variant={u.status === 'ACTIVE' ? 'destructive' : 'default'} onClick={() => void toggleFrozen()}>
            {u.status === 'ACTIVE' ? '冻结账号' : '解除冻结'}
          </Button>
        )}
      </div>

      <Card>
        <CardHeader><CardTitle className="text-base">身份</CardTitle></CardHeader>
        <CardContent className="space-y-2 text-sm">
          <div className="text-muted-foreground">用户 ID：<span className="font-mono">{u.id}</span></div>
          <div className="text-muted-foreground">注册时间：{formatTime(u.createdAt)}</div>
          <div className="text-muted-foreground">是否设过密码：{u.hasPassword ? '是' : '否'}</div>
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow><TableHead>类型</TableHead><TableHead>标识</TableHead><TableHead>最近登录</TableHead></TableRow>
              </TableHeader>
              <TableBody>
                {u.identities.length === 0 && (
                  <TableRow><TableCell colSpan={3} className="text-center text-muted-foreground">没有已绑定的身份</TableCell></TableRow>
                )}
                {u.identities.map((i) => (
                  <TableRow key={`${i.type}:${i.subject}`}>
                    <TableCell className="font-mono text-xs">{i.type}</TableCell>
                    <TableCell className="font-mono text-xs">{i.subject}</TableCell>
                    <TableCell className="text-muted-foreground">{formatTime(i.lastLoginAt)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center space-y-0">
          <CardTitle className="text-base">在线设备</CardTitle>
          <div className="flex-1" />
          <Button
            variant="outline"
            size="sm"
            disabled={!sessions.data || sessions.data.length === 0}
            onClick={() => void revokeAll()}
          >
            全部下线
          </Button>
        </CardHeader>
        <CardContent>
          {sessions.error && <p className="text-sm text-destructive">{sessions.error}</p>}
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>端</TableHead><TableHead>IP</TableHead><TableHead>User-Agent</TableHead>
                  <TableHead>首次认证</TableHead><TableHead>空闲到期</TableHead><TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {sessions.data?.length === 0 && (
                  <TableRow><TableCell colSpan={6} className="text-center text-muted-foreground">没有在线设备</TableCell></TableRow>
                )}
                {sessions.data?.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell>{s.mobile ? '移动端' : '桌面端'}</TableCell>
                    <TableCell className="font-mono text-xs">{s.ip || '-'}</TableCell>
                    <TableCell className="max-w-xs truncate text-xs text-muted-foreground" title={s.ua}>{s.ua || '-'}</TableCell>
                    <TableCell className="text-muted-foreground">{formatTime(s.firstAuthAt)}</TableCell>
                    <TableCell className="text-muted-foreground">{formatTime(s.idleExpiresAt)}</TableCell>
                    <TableCell>
                      <Button variant="ghost" size="sm" onClick={() => void revokeOne(s.id)}>下线</Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="text-base">登录日志（最近 50 条）</CardTitle></CardHeader>
        <CardContent>
          {logs.error && <p className="text-sm text-destructive">{logs.error}</p>}
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>时间</TableHead><TableHead>事件</TableHead><TableHead>标识</TableHead>
                  <TableHead>结果</TableHead><TableHead>IP</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {logs.data?.length === 0 && (
                  <TableRow><TableCell colSpan={5} className="text-center text-muted-foreground">没有登录记录</TableCell></TableRow>
                )}
                {logs.data?.map((l) => (
                  <TableRow key={l.id}>
                    <TableCell className="text-muted-foreground">{formatTime(l.createdAt)}</TableCell>
                    <TableCell className="font-mono text-xs">{l.event}</TableCell>
                    {/* subject 是后端脱敏过的（service.MaskSubject），这里原样显示 */}
                    <TableCell className="font-mono text-xs">{l.identityType}:{l.subject}</TableCell>
                    <TableCell>
                      {l.success ? (
                        <Badge variant="default">成功</Badge>
                      ) : (
                        <Badge variant="destructive" title={l.reason}>失败</Badge>
                      )}
                    </TableCell>
                    <TableCell className="font-mono text-xs">{l.ip || '-'}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <ResetPasswordDialog
        userId={id}
        open={resetting}
        onOpenChange={setResetting}
        onDone={() => {
          setResetting(false)
          user.reload()
          // 重置密码会连带撤销全部会话（后端的安全耦合），设备列表必须刷新。
          sessions.reload()
        }}
      />
    </div>
  )
}

function ResetPasswordDialog({
  userId, open, onOpenChange, onDone,
}: {
  userId: string
  open: boolean
  onOpenChange: (v: boolean) => void
  onDone: () => void
}) {
  const [pwd, setPwd] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit() {
    setBusy(true)
    try {
      // 返回 204 无响应体——api.put 已经处理，不要在这里 .json()
      await api.put(`/users/${userId}/password`, { password: pwd })
      toast.success('密码已重置，该用户全部设备已下线')
      setPwd('')
      onDone()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>重置密码</DialogTitle></DialogHeader>
        <div className="space-y-2">
          <Label htmlFor="newpwd">新密码</Label>
          <Input id="newpwd" type="password" value={pwd} onChange={(e) => setPwd(e.target.value)} />
          <p className="text-sm text-muted-foreground">
            重置后该用户的全部设备会立即下线，需要用新密码重新登录。
          </p>
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              // 取消也要清空 pwd：不清的话，有人输入过密码但没提交就点了取消，
              // 下一次打开这个弹窗（哪怕是别的管理员）会看到一个已经填好的
              // 密码框。
              setPwd('')
              onOpenChange(false)
            }}
          >
            取消
          </Button>
          <Button disabled={busy || pwd === ''} onClick={() => void submit()}>确认重置</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
