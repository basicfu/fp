import { useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { IpTextarea, RoleSelect, parseIps } from '@/components/AccessKeyFields'
import { DeleteConfirm, StatusConfirm } from '@/components/AccessKeyDialogs'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatMinute } from '@/lib/format'
import { accessKeyStateLabels } from '@/lib/labels'
import type { AccessKey, CreateAccessKeyResponse, Role } from '@/lib/types'

export default function AccessKeys() {
  const [sp, setSp] = useSearchParams()
  const role = sp.get('role') ?? ''
  const keys = useResource(
    () => api.get<AccessKey[]>(role ? `/access-keys?roleKey=${encodeURIComponent(role)}` : '/access-keys'),
    [role],
  )
  const roles = useResource(() => api.get<Role[]>('/roles'), [])
  const navigate = useNavigate()
  const [keyword, setKeyword] = useState('')
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<CreateAccessKeyResponse | null>(null)
  const [toggling, setToggling] = useState<AccessKey | null>(null)
  const [deleting, setDeleting] = useState<AccessKey | null>(null)

  const kw = keyword.trim().toLowerCase()
  const list = (keys.data ?? []).filter(
    (k) => !kw || k.accessKeyId.toLowerCase().includes(kw) || k.remark.toLowerCase().includes(kw),
  )

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">访问密钥</h1>
        <Button onClick={() => setCreating(true)}>新建访问密钥</Button>
      </div>
      <p className="text-sm text-muted-foreground">
        给第三方程序签名调用业务方接口。key 是<strong>全局</strong>的，能调哪些接口完全由绑定的角色决定。
      </p>

      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          placeholder="按 AccessKey 或备注筛选"
          className="w-72"
        />
        {role && (
          <Button variant="outline" size="sm" onClick={() => setSp(new URLSearchParams())}>
            只看角色「{role}」 ✕
          </Button>
        )}
      </div>

      {keys.loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {keys.error && <p className="text-sm text-destructive">{keys.error}</p>}
      {keys.data && (
        <div className="overflow-x-auto rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>AccessKey</TableHead>
                <TableHead>备注</TableHead>
                <TableHead>角色</TableHead>
                <TableHead>IP 白名单</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>到期时间</TableHead>
                <TableHead>最后使用</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.length === 0 && (
                <TableRow>
                  <TableCell colSpan={8} className="text-center text-muted-foreground">
                    没有访问密钥
                  </TableCell>
                </TableRow>
              )}
              {list.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-mono text-xs">
                    <Link to={`/access-keys/${k.id}`} className="underline-offset-4 hover:underline">
                      {k.accessKeyId}
                    </Link>
                  </TableCell>
                  <TableCell>{k.remark}</TableCell>
                  <TableCell className="text-muted-foreground">{k.roleKey || '未绑定'}</TableCell>
                  <TableCell className="text-muted-foreground">
                    {k.allowedIps.length === 0 ? '不限制' : `${k.allowedIps.length} 条`}
                  </TableCell>
                  <TableCell>
                    <Badge variant={k.state === 'active' ? 'default' : 'secondary'}>
                      {accessKeyStateLabels[k.state] ?? k.state}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{formatMinute(k.expiresAt, '永不过期')}</TableCell>
                  <TableCell className="text-muted-foreground">{formatMinute(k.lastUsedAt, '从未使用')}</TableCell>
                  <TableCell className="space-x-2 text-right">
                    <Button variant="outline" size="sm" onClick={() => navigate(`/access-keys/${k.id}`)}>
                      编辑
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => setToggling(k)}>
                      {k.status === 'ACTIVE' ? '停用' : '启用'}
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => setDeleting(k)}>
                      删除
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <CreateDialog
        open={creating}
        onOpenChange={setCreating}
        roles={roles.data ?? []}
        onCreated={(res) => {
          setCreating(false)
          setCreated(res)
          keys.reload()
        }}
      />
      <SecretDialog value={created} onClose={() => setCreated(null)} />
      <StatusConfirm target={toggling} onClose={() => setToggling(null)} onDone={keys.reload} />
      <DeleteConfirm target={deleting} onClose={() => setDeleting(null)} onDone={keys.reload} />
    </div>
  )
}

function CreateDialog({
  open,
  onOpenChange,
  roles,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  roles: Role[]
  onCreated: (res: CreateAccessKeyResponse) => void
}) {
  const [remark, setRemark] = useState('')
  const [roleKey, setRoleKey] = useState('')
  const [validDays, setValidDays] = useState('0')
  const [ips, setIps] = useState('')
  const [saving, setSaving] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!remark.trim()) {
      toast.error('请输入备注')
      return
    }
    const days = Number(validDays)
    if (!Number.isInteger(days) || days < 0) {
      toast.error('有效期必须是不小于 0 的整数')
      return
    }
    setSaving(true)
    try {
      const res = await api.post<CreateAccessKeyResponse>('/access-keys', {
        remark: remark.trim(),
        roleKey,
        validDays: days,
        allowedIps: parseIps(ips),
      })
      setRemark('')
      setRoleKey('')
      setValidDays('0')
      setIps('')
      onCreated(res)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建访问密钥</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="ak-remark">备注</Label>
            <Input id="ak-remark" value={remark} onChange={(e) => setRemark(e.target.value)} placeholder="哪个合作方在用" />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ak-role">角色</Label>
            <RoleSelect id="ak-role" roles={roles} value={roleKey} onChange={setRoleKey} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ak-days">有效期（天）</Label>
            <Input id="ak-days" type="number" min={0} value={validDays} onChange={(e) => setValidDays(e.target.value)} />
            <p className="text-xs text-muted-foreground">0 表示永不过期。</p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="ak-ips">IP 白名单</Label>
            <IpTextarea id="ak-ips" value={ips} onChange={setIps} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={saving}>
              创建
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * SecretDialog 展示刚创建的 AK 与 SK。SK 只在创建响应里出现这一次，所以与
 * Applications.tsx 的同名弹窗一样：onOpenChange 不响应任何内部关闭请求（Esc、X 按钮），
 * disablePointerDismissal 拦掉点遮罩，只能点「我已保存」关闭。
 */
function SecretDialog({ value, onClose }: { value: CreateAccessKeyResponse | null; onClose: () => void }) {
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      toast.success('已复制')
    } catch {
      toast.error('复制失败，请手动选中复制')
    }
  }
  return (
    <Dialog open={value !== null} onOpenChange={() => {}} disablePointerDismissal>
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>访问密钥已创建</DialogTitle>
        </DialogHeader>
        {value && (
          <div className="space-y-3">
            {[
              ['AccessKey ID', value.accessKey.accessKeyId],
              ['AccessKey Secret', value.secret],
            ].map(([label, text]) => (
              <div key={label} className="space-y-1">
                <Label>{label}</Label>
                <div className="flex items-center gap-2">
                  <div className="flex-1 rounded-md border bg-muted/40 p-2 font-mono text-sm break-all">{text}</div>
                  <Button variant="outline" size="sm" onClick={() => void copy(text)}>
                    复制
                  </Button>
                </div>
              </div>
            ))}
            <p className="text-sm text-destructive">Secret 只显示这一次，关闭后无法再查看。请立刻复制并交给合作方。</p>
            <p className="text-xs text-muted-foreground">签名规则见仓库里的 docs/access-key.md。</p>
          </div>
        )}
        <DialogFooter>
          <Button onClick={onClose}>我已保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
