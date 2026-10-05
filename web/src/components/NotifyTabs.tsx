import { NavLink } from 'react-router'
import { cn } from '@/lib/utils'

const tabs = [
  { to: '/notify/templates', label: '模板' },
  { to: '/notify/providers', label: '供应商' },
  { to: '/notify/logs', label: '发送记录' },
]

/** 通知中心三个页面共用的页内导航。 */
export default function NotifyTabs() {
  return (
    <nav className="flex gap-1 border-b" aria-label="通知中心">
      {tabs.map((t) => (
        <NavLink
          key={t.to}
          to={t.to}
          className={({ isActive }) =>
            cn(
              '-mb-px border-b-2 px-3 py-2 text-sm',
              isActive
                ? 'border-primary font-medium text-foreground'
                : 'border-transparent text-muted-foreground hover:text-foreground',
            )
          }
        >
          {t.label}
        </NavLink>
      ))}
    </nav>
  )
}
