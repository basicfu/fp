import { useState } from 'react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api } from '@/lib/api'
import { useResource, errorMessage } from '@/lib/useResource'
import type { Application, Role } from '@/lib/types'

/**
 * UserRolesCard 管一个用户的角色。
 *
 * 这里显示的是**显式分配**的角色。各应用的默认角色是叠加上去的、不写数据，
 * 所以不在这张表里——但必须在界面上说清楚，否则看到"没有角色"的人会以为
 * 这个用户什么都干不了，而实际上他在每个配了默认角色的应用里都有基础能力。
 */
export default function UserRolesCard({ userId }: { userId: string }) {
  const assigned = useResource(() => api.get<{ roles: string[] }>(`/users/${userId}/roles`), [userId])
  const all = useResource(() => api.get<Role[]>('/roles'), [])
  const apps = useResource(() => api.get<Application[]>('/applications'), [])
  const [saving, setSaving] = useState(false)

  const current = assigned.data?.roles ?? []
  const available = (all.data ?? []).filter((r) => !current.includes(r.key))
  const defaults = (apps.data ?? []).filter((a) => a.defaultRoleKey !== '')

  async function save(next: string[]) {
    setSaving(true)
    try {
      // 后端是全量替换（PUT），不是增量：一次发完整列表。
      await api.put(`/users/${userId}/roles`, { roles: next })
      toast.success('已保存，正在推送到接入方')
      assigned.reload()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">角色</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {assigned.error && <p className="text-sm text-destructive">{assigned.error}</p>}
        {all.error && <p className="text-sm text-destructive">{all.error}</p>}

        <div className="flex flex-wrap items-center gap-2">
          {current.length === 0 && <span className="text-sm text-muted-foreground">没有显式分配的角色</span>}
          {current.map((key) => (
            <Badge key={key} variant="secondary" className="gap-1 py-1 pl-2.5 pr-1">
              {key}
              <Button
                variant="ghost"
                size="sm"
                className="h-5 w-5 p-0 text-muted-foreground hover:text-foreground"
                aria-label={`移除 ${key}`}
                disabled={saving}
                onClick={() => void save(current.filter((k) => k !== key))}
              >
                ×
              </Button>
            </Badge>
          ))}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Select
            // value 恒为 undefined：这个 Select 是个"动作"而不是"状态"——
            // 选中一项就立刻把它加进去并清空，不该停在某个选中值上。
            value={undefined}
            disabled={saving || available.length === 0}
            onValueChange={(v) => {
              if (v) void save([...current, v])
            }}
          >
            <SelectTrigger className="w-56">
              <SelectValue placeholder={available.length === 0 ? '没有可添加的角色' : '添加角色…'} />
            </SelectTrigger>
            <SelectContent>
              {available.map((r) => (
                <SelectItem key={r.id} value={r.key}>
                  {r.key}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {defaults.length > 0 && (
          <p className="text-xs text-muted-foreground">
            除上面这些之外，他在以下应用里还自动拥有各自的默认角色（<strong>叠加</strong>，不是替换）：
            {defaults.map((a, i) => (
              <span key={a.id}>
                {i > 0 && '、'}
                <span>{a.name}</span>
                <span className="font-mono"> {a.defaultRoleKey}</span>
              </span>
            ))}
          </p>
        )}

        <p className="text-xs text-muted-foreground">
          角色刻在已签发的会话里。改动会立刻推送给接入方，让它们丢掉这个用户的缓存并重新校验，
          所以在线用户不需要重新登录也会生效。
        </p>
      </CardContent>
    </Card>
  )
}
