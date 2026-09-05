import { NavLink, Outlet } from 'react-router'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/lib/auth'

const nav = [
  { to: '/applications', label: '应用' },
  { to: '/users', label: '用户' },
  { to: '/roles', label: '角色' },
]

export default function Layout() {
  const { username, logout } = useAuth()

  return (
    <div className="flex min-h-screen">
      <aside className="w-52 shrink-0 border-r bg-muted/20 p-4">
        <div className="mb-6 px-2 text-lg font-semibold">fp</div>
        <nav className="space-y-1">
          {nav.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              className={({ isActive }) =>
                `block rounded-md px-3 py-2 text-sm ${
                  isActive ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground hover:bg-accent/50'
                }`
              }
            >
              {n.label}
            </NavLink>
          ))}
        </nav>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center justify-end gap-3 border-b px-6">
          <span className="text-sm text-muted-foreground">{username}</span>
          <Button variant="outline" size="sm" onClick={() => void logout()}>
            退出
          </Button>
        </header>
        <main className="min-w-0 flex-1 p-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
