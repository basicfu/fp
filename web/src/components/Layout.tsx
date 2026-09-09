import { NavLink, Outlet, useLocation } from 'react-router'
import type { LucideIcon } from 'lucide-react'
import { AppWindow, KeyRound, LogOut, Settings, ShieldCheck, Users as UsersIcon } from 'lucide-react'
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
} from '@/components/ui/sidebar'
import ThemeColorSwitcher from '@/components/ThemeColorSwitcher'
import ThemeModeToggle from '@/components/ThemeModeToggle'
import AppSwitcher from '@/components/AppSwitcher'
import { useAuth } from '@/lib/auth'
import { buildBreadcrumb } from '@/lib/breadcrumb'

const nav: { to: string; label: string; icon: LucideIcon }[] = [
  { to: '/applications', label: '应用列表', icon: AppWindow },
  { to: '/users', label: '用户管理', icon: UsersIcon },
  { to: '/roles', label: '角色管理', icon: ShieldCheck },
  { to: '/permissions', label: '权限管理', icon: KeyRound },
  { to: '/config', label: '配置中心', icon: Settings },
]

export default function Layout() {
  const { username, logout } = useAuth()
  const location = useLocation()
  const segments = buildBreadcrumb(location.pathname)

  return (
    <SidebarProvider>
      <Sidebar collapsible="icon">
        <SidebarHeader>
          <AppSwitcher />
        </SidebarHeader>
        <SidebarContent>
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                {nav.map((n) => (
                  <SidebarMenuItem key={n.to}>
                    <SidebarMenuButton
                      render={<NavLink to={n.to} />}
                      isActive={location.pathname.startsWith(n.to)}
                      tooltip={n.label}
                    >
                      <n.icon />
                      <span>{n.label}</span>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>
      </Sidebar>
      <SidebarInset>
        <header className="sticky top-0 z-20 flex h-14 shrink-0 items-center justify-between gap-3 border-b bg-background/95 px-4 backdrop-blur supports-[backdrop-filter]:bg-background/60">
          <div className="flex items-center gap-2">
            <SidebarTrigger />
            <Breadcrumb>
              <BreadcrumbList>
                {segments.flatMap((s, i) => {
                  const item = (
                    <BreadcrumbItem key={`item-${i}`}>
                      {s.to ? (
                        <BreadcrumbLink render={<NavLink to={s.to} />}>{s.label}</BreadcrumbLink>
                      ) : (
                        <BreadcrumbPage>{s.label}</BreadcrumbPage>
                      )}
                    </BreadcrumbItem>
                  )
                  return i === 0 ? [item] : [<BreadcrumbSeparator key={`sep-${i}`} />, item]
                })}
              </BreadcrumbList>
            </Breadcrumb>
          </div>
          <div className="flex items-center gap-1">
            <ThemeColorSwitcher />
            <ThemeModeToggle />
            <DropdownMenu>
              <DropdownMenuTrigger render={<Button variant="ghost" className="gap-2 px-2" />}>
                <Avatar size="sm">
                  <AvatarFallback>{(username ?? '?').slice(0, 1).toUpperCase()}</AvatarFallback>
                </Avatar>
                <span className="text-sm">{username}</span>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuLabel>{username}</DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onClick={() => void logout()}>
                  <LogOut />
                  退出
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>
        <div className="min-w-0 flex-1 p-6">
          <Outlet />
        </div>
      </SidebarInset>
    </SidebarProvider>
  )
}
