import { NavLink, Outlet, useLocation } from 'react-router'
import type { LucideIcon } from 'lucide-react'
import {
  AppWindow,
  Bell,
  ChevronRight,
  Key,
  KeyRound,
  Lock,
  LogOut,
  Server,
  Settings,
  ShieldCheck,
  Users as UsersIcon,
} from 'lucide-react'
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
  DropdownMenuGroup,
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
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarProvider,
  SidebarTrigger,
  useSidebar,
} from '@/components/ui/sidebar'
import ThemeColorSwitcher from '@/components/ThemeColorSwitcher'
import ThemeModeToggle from '@/components/ThemeModeToggle'
import AppSwitcher from '@/components/AppSwitcher'
import { ChangeAccountDialog } from '@/components/ChangeAccountDialog'
import { useAuth } from '@/lib/auth'
import { buildBreadcrumb } from '@/lib/breadcrumb'
import { cn } from '@/lib/utils'

interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  /** 有子项的条目：当前路径落在它名下时才展开，展开与否完全由路由决定。 */
  children?: { to: string; label: string }[]
}

const nav: NavItem[] = [
  { to: '/applications', label: '应用列表', icon: AppWindow },
  { to: '/users', label: '用户管理', icon: UsersIcon },
  { to: '/access-keys', label: '访问密钥', icon: Key },
  { to: '/roles', label: '角色管理', icon: ShieldCheck },
  { to: '/permissions', label: '权限管理', icon: KeyRound },
  { to: '/config', label: '配置中心', icon: Settings },
  {
    to: '/notify',
    label: '通知中心',
    icon: Bell,
    children: [
      { to: '/notify/templates', label: '模板' },
      { to: '/notify/providers', label: '供应商' },
      { to: '/notify/logs', label: '发送记录' },
    ],
  },
  { to: '/system-config', label: '系统配置', icon: Server },
]

export default function Layout() {
  const { username, logout, setAccountDialogOpen } = useAuth()
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
                {nav.map((n) => {
                  const inGroup = location.pathname.startsWith(n.to)
                  const hitChild = n.children?.some((c) => location.pathname.startsWith(c.to))
                  return (
                    <SidebarMenuItem key={n.to}>
                      <SidebarMenuButton
                        render={<NavLink to={n.to} />}
                        // 高亮交给命中的子项；子项都没命中（如刚到 /notify、重定向还没生效）时才由父行兜底。
                        isActive={inGroup && !hitChild}
                        tooltip={n.label}
                      >
                        <n.icon />
                        <span>{n.label}</span>
                        {n.children && (
                          <ChevronRight
                            className={cn(
                              'ml-auto transition-transform group-data-[collapsible=icon]:hidden',
                              inGroup && 'rotate-90',
                            )}
                          />
                        )}
                      </SidebarMenuButton>
                      {n.children && inGroup && (
                        <SidebarMenuSub>
                          {n.children.map((c) => (
                            <SidebarMenuSubItem key={c.to}>
                              <SidebarMenuSubButton
                                render={<NavLink to={c.to} />}
                                isActive={location.pathname.startsWith(c.to)}
                              >
                                <span>{c.label}</span>
                              </SidebarMenuSubButton>
                            </SidebarMenuSubItem>
                          ))}
                        </SidebarMenuSub>
                      )}
                    </SidebarMenuItem>
                  )
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>
      </Sidebar>
      <SidebarInset className="min-w-0 overflow-auto">
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
                <DropdownMenuGroup>
                  <DropdownMenuLabel>{username}</DropdownMenuLabel>
                </DropdownMenuGroup>
                <DropdownMenuSeparator />
                <DropdownMenuItem onClick={() => setAccountDialogOpen(true)}>
                  <Lock />
                  修改密码
                </DropdownMenuItem>
                <DropdownMenuItem variant="destructive" onClick={() => void logout()}>
                  <LogOut />
                  退出
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>
        <Content />
      </SidebarInset>
      <ChangeAccountDialog />
    </SidebarProvider>
  )
}

/**
 * 内容区自己滚动，不再跟侧边栏一起被塞进同一个横向滚动条——侧边栏始终
 * 固定可见。1200px 是"侧边栏 + 内容"整体要保住的最小宽度，侧边栏本身
 * 会在展开/收起图标态之间变宽变窄，所以内容区的最小宽度要跟着
 * useSidebar() 的 state 动态减去当前侧边栏宽度，而不是写死一个数。
 */
function Content() {
  const { state } = useSidebar()
  const sidebarWidthVar = state === 'collapsed' ? 'var(--sidebar-width-icon)' : 'var(--sidebar-width)'
  return (
    <div className="flex-1 p-6" style={{ minWidth: `calc(1200px - ${sidebarWidthVar})` }}>
      <Outlet />
    </div>
  )
}
