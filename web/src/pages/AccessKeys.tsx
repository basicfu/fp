import { useState } from 'react'
import { useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LabelHint } from '@/components/ui/label-hint'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { IP_ALLOWLIST_HINT, IpTextarea, ROLE_SELECT_HINT, RoleSelect, parseIps } from '@/components/AccessKeyFields'
import { DeleteConfirm, StatusConfirm } from '@/components/AccessKeyDialogs'
import { api } from '@/lib/api'
import { copyToClipboard } from '@/lib/clipboard'
import { useResource, errorMessage } from '@/lib/useResource'
import { formatMinute } from '@/lib/format'
import { accessKeyStateLabels } from '@/lib/labels'
import type { AccessKey, CreateAccessKeyResponse, Role } from '@/lib/types'

/** 复制文本到剪贴板，失败时给出提示而不是悄悄没反应。 */
async function copyText(text: string) {
  if (await copyToClipboard(text)) toast.success('已复制')
  else toast.error('复制失败，请手动选中复制')
}

export default function AccessKeys() {
  const [sp, setSp] = useSearchParams()
  const role = sp.get('role') ?? ''
  const keys = useResource(
    () => api.get<AccessKey[]>(role ? `/access-keys?roleKey=${encodeURIComponent(role)}` : '/access-keys'),
    [role],
  )
  const roles = useResource(() => api.get<Role[]>('/roles'), [])
  const [keyword, setKeyword] = useState('')
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<CreateAccessKeyResponse | null>(null)
  const [editing, setEditing] = useState<AccessKey | null>(null)
  const [toggling, setToggling] = useState<AccessKey | null>(null)
  const [deleting, setDeleting] = useState<AccessKey | null>(null)

  const kw = keyword.trim().toLowerCase()
  const list = (keys.data ?? []).filter(
    (k) => !kw || k.accessKeyId.toLowerCase().includes(kw) || k.remark.toLowerCase().includes(kw),
  )

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={() => setCreating(true)}>新建访问密钥</Button>
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

      {/* 只在真正首次加载（还没有任何数据）时显示这行文字——启用/停用/删除
          之后的 reload() 也会把 loading 短暂置回 true，这时候表格已经有
          上一次的数据在显示，再插一行"加载中…"只会造成一次没必要的跳动。 */}
      {keys.loading && !keys.data && <p className="text-sm text-muted-foreground">加载中…</p>}
      {keys.error && <p className="text-sm text-destructive">{keys.error}</p>}
      {keys.data && (
        <Table className="table-fixed">
          {/* 操作列固定 196px（180px 按钮预留区 + 单元格左右各 8px padding），
              其余 7 列按百分比分配——跟 Applications 表格是同一套做法。 */}
          <colgroup>
            <col className="w-[5%]" />
            <col className="w-[16%]" />
            <col className="w-[15%]" />
            <col className="w-[10%]" />
            <col className="w-[8%]" />
            <col className="w-[8%]" />
            <col className="w-[13%]" />
            <col className="w-[13%]" />
            <col className="w-[196px]" />
          </colgroup>
          <TableHeader>
            <TableRow>
              <TableHead className="p-0 px-2">序号</TableHead>
              <TableHead className="p-0 px-2">AccessKey</TableHead>
              <TableHead className="p-0 px-2">备注</TableHead>
              <TableHead className="p-0 px-2">角色</TableHead>
              <TableHead className="p-0 px-2">IP 白名单</TableHead>
              <TableHead className="p-0 px-2">状态</TableHead>
              <TableHead className="p-0 px-2">到期时间</TableHead>
              <TableHead className="p-0 px-2">最后使用</TableHead>
              <TableHead className="p-0 px-2">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.length === 0 && (
              <TableRow>
                <TableCell colSpan={9} className="text-center text-muted-foreground">
                  没有数据
                </TableCell>
              </TableRow>
            )}
            {list.map((k, i) => (
              <TableRow key={k.id} className="h-12">
                <TableCell className="p-0 px-2 text-muted-foreground">{i + 1}</TableCell>
                <TableCell className="whitespace-normal break-all p-0 px-2 font-mono text-xs">{k.accessKeyId}</TableCell>
                <TableCell className="whitespace-normal break-words p-0 px-2">{k.remark}</TableCell>
                <TableCell className="whitespace-normal break-all p-0 px-2 text-muted-foreground">
                  {k.roleKey || '未绑定'}
                </TableCell>
                <TableCell className="p-0 px-2 text-muted-foreground">
                  {k.allowedIps.length === 0 ? (
                    '不限制'
                  ) : (
                    <Tooltip>
                      <TooltipTrigger className="cursor-default underline decoration-dotted underline-offset-4">
                        {k.allowedIps.length} 条
                      </TooltipTrigger>
                      <TooltipContent className="max-w-sm items-start" side="bottom">
                        <div className="space-y-1.5">
                          <div className="max-h-48 max-w-[240px] overflow-y-auto font-mono whitespace-pre-wrap break-all">
                            {k.allowedIps.join('\n')}
                          </div>
                          <Button
                            variant="outline"
                            size="sm"
                            className="h-6 px-2 text-xs"
                            onClick={() => void copyText(k.allowedIps.join('\n'))}
                          >
                            复制
                          </Button>
                        </div>
                      </TooltipContent>
                    </Tooltip>
                  )}
                </TableCell>
                <TableCell className="p-0 px-2">
                  <Badge variant={k.state === 'active' ? 'default' : 'secondary'}>
                    {accessKeyStateLabels[k.state] ?? k.state}
                  </Badge>
                </TableCell>
                <TableCell className="whitespace-normal p-0 px-2 text-muted-foreground">
                  {formatMinute(k.expiresAt, '永不过期')}
                </TableCell>
                <TableCell className="whitespace-normal p-0 px-2 text-muted-foreground">
                  {formatMinute(k.lastUsedAt, '从未使用')}
                </TableCell>
                <TableCell className="p-0 px-2">
                  <div className="flex w-[180px] gap-2">
                    <Button variant="outline" size="sm" onClick={() => setEditing(k)}>
                      编辑
                    </Button>
                    <Button
                      variant={k.status === 'ACTIVE' ? 'destructive' : 'default'}
                      size="sm"
                      onClick={() => setToggling(k)}
                    >
                      {k.status === 'ACTIVE' ? '停用' : '启用'}
                    </Button>
                    <Button variant="destructive" size="sm" onClick={() => setDeleting(k)}>
                      删除
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
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
      {editing && (
        <EditDialog
          k={editing}
          roles={roles.data ?? []}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            keys.reload()
          }}
        />
      )}
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
            <Input id="ak-remark" value={remark} onChange={(e) => setRemark(e.target.value)} />
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="ak-role">角色</Label>
              <LabelHint>{ROLE_SELECT_HINT}</LabelHint>
            </div>
            <RoleSelect id="ak-role" roles={roles} value={roleKey} onChange={setRoleKey} />
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="ak-days">有效期（天）</Label>
              <LabelHint>0 表示永不过期。</LabelHint>
            </div>
            <Input id="ak-days" type="number" min={0} value={validDays} onChange={(e) => setValidDays(e.target.value)} />
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="ak-ips">IP 白名单</Label>
              <LabelHint>{IP_ALLOWLIST_HINT}</LabelHint>
            </div>
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
 * 编辑访问密钥。直接是个弹窗，跟新建一样不用跳到单独的详情页——
 * 备注/角色/有效期/IP 白名单都能改。有效期留空表示不改：只改备注时
 * 不该顺手把到期时间重算一遍。
 */
function EditDialog({
  k,
  roles,
  onClose,
  onSaved,
}: {
  k: AccessKey
  roles: Role[]
  onClose: () => void
  onSaved: () => void
}) {
  const [remark, setRemark] = useState(k.remark)
  const [roleKey, setRoleKey] = useState(k.roleKey)
  const [ips, setIps] = useState(k.allowedIps.join('\n'))
  const [validDays, setValidDays] = useState('')
  const [saving, setSaving] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    const body: Record<string, unknown> = { remark: remark.trim(), roleKey, allowedIps: parseIps(ips) }
    if (validDays.trim() !== '') {
      const days = Number(validDays)
      if (!Number.isInteger(days) || days < 0) {
        toast.error('有效期必须是不小于 0 的整数')
        return
      }
      body.validDays = days
    }
    setSaving(true)
    try {
      await api.patch(`/access-keys/${k.id}`, body)
      toast.success('已生效，正在推送到业务方')
      onSaved()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>编辑访问密钥</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="edit-ak-remark">备注</Label>
            <Input id="edit-ak-remark" value={remark} onChange={(e) => setRemark(e.target.value)} />
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="edit-ak-role">角色</Label>
              <LabelHint>{ROLE_SELECT_HINT}</LabelHint>
            </div>
            <RoleSelect id="edit-ak-role" roles={roles} value={roleKey} onChange={setRoleKey} />
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="edit-ak-days">重新设置有效期（天）</Label>
              <LabelHint>填 0 改为永不过期，填 N 从现在起算 N 天。</LabelHint>
            </div>
            <Input
              id="edit-ak-days"
              type="number"
              min={0}
              value={validDays}
              onChange={(e) => setValidDays(e.target.value)}
              placeholder="留空表示不修改"
            />
            <p className="text-xs text-muted-foreground">当前到期时间：{formatMinute(k.expiresAt, '永不过期')}。</p>
          </div>
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="edit-ak-ips">IP 白名单</Label>
              <LabelHint>{IP_ALLOWLIST_HINT}</LabelHint>
            </div>
            <IpTextarea id="edit-ak-ips" value={ips} onChange={setIps} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              取消
            </Button>
            <Button type="submit" disabled={saving}>
              保存
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
                  <Button variant="outline" size="sm" onClick={() => void copyText(text)}>
                    复制
                  </Button>
                </div>
              </div>
            ))}
            <p className="text-sm text-destructive">Secret 只显示这一次，关闭后无法再查看。</p>
          </div>
        )}
        <DialogFooter>
          <Button onClick={onClose}>我已保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
