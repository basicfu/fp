import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { GUEST_ROLE_KEY } from '@/lib/roles'
import type { Role } from '@/lib/types'

/** 「不绑定角色」在 Select 里的占位值。base-ui 的 SelectItem 不接受空串。 */
const NO_ROLE = '__none__'

/** selectableRoles 去掉 GUEST：访问密钥不能绑定它（后端同样拒绝）。 */
export function selectableRoles(roles: Role[]): Role[] {
  return roles.filter((r) => r.key !== GUEST_ROLE_KEY)
}

/** parseIps 把多行文本拆成 IP 列表，空行忽略。 */
export function parseIps(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
}

export function RoleSelect({
  id,
  roles,
  value,
  onChange,
}: {
  id: string
  roles: Role[]
  value: string
  onChange: (v: string) => void
}) {
  return (
    <>
      <Select value={value || NO_ROLE} onValueChange={(v) => onChange(v === NO_ROLE || v === null ? '' : v)}>
        <SelectTrigger id={id} className="w-full">
          {/* 显式给出显示文字：拿不到 item 标签时 base-ui 会直接渲染 value。 */}
          <SelectValue placeholder="不绑定">{value || '不绑定'}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={NO_ROLE}>不绑定</SelectItem>
          {selectableRoles(roles).map((r) => (
            <SelectItem key={r.id} value={r.key}>
              {r.key}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="text-xs text-muted-foreground">不绑定角色的 key 调任何接口都是 403；访问密钥不拥有 GUEST。</p>
    </>
  )
}

export function IpTextarea({ id, value, onChange }: { id: string; value: string; onChange: (v: string) => void }) {
  return (
    <>
      <textarea
        id={id}
        rows={4}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={'每行一个 IP 或网段，例如\n203.0.113.7\n10.0.0.0/8'}
        className="w-full rounded-md border bg-transparent px-3 py-2 font-mono text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
      />
      <p className="text-xs text-muted-foreground">留空表示不校验 IP；最多 50 条。依赖最外层代理正确配置来源 IP，配置不当会导致白名单静默失效。</p>
    </>
  )
}
