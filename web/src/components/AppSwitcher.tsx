import { Select as SelectPrimitive } from '@base-ui/react/select'
import { ChevronsUpDown, Command } from 'lucide-react'
import { SelectContent, SelectItem, SelectValue } from '@/components/ui/select'
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem } from '@/components/ui/sidebar'
import { useCurrentApp } from '@/lib/current-app'

/**
 * 视觉上照抄 shadcn 那套"team switcher"表头（如 shadcn-admin 的侧边栏顶部）：
 * SidebarMenuButton size="lg" 提供尺寸/间距，套壳到 base-ui 的
 * Select.Trigger 上获得下拉行为——同 nav 项用 render={<NavLink/>} 是
 * 同一种组合方式，不是重新发明一套按钮样式。
 */
export default function AppSwitcher() {
  const { apps, currentApp, currentAppId, setCurrentAppId, loading } = useCurrentApp()

  if (!loading && apps.length === 0) {
    return (
      <SidebarMenu>
        <SidebarMenuItem>
          <p className="px-2 py-1.5 text-xs text-sidebar-foreground/70">还没有应用</p>
        </SidebarMenuItem>
      </SidebarMenu>
    )
  }

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <SelectPrimitive.Root
          // 加载完成前 apps 是空数组，items 里查不到 currentAppId 对应的
          // 标签——这时如果仍然把 currentAppId 传给 value，Select.Value 会
          // 先把 UUID 原样显示出来，等应用列表拉到后才闪成应用名。加载中
          // 传 null 让它退回空 placeholder，避免这一闪。
          value={loading ? null : currentAppId}
          onValueChange={(v) => v && setCurrentAppId(v)}
          // items 让 Select.Value 能把受控 value（应用 UUID）映射回应用名；
          // 缺了它，收起状态会直接显示 UUID 而不是应用名。
          items={apps.map((a) => ({ value: a.id, label: a.name }))}
        >
          <SidebarMenuButton size="lg" render={<SelectPrimitive.Trigger aria-label="切换当前应用" />}>
            {/* 照抄 shadcn-admin 的 team switcher 图标：黑白两色，跟"主题色"
                下拉（科技蓝/紫罗兰/翡翠绿/极简黑白）完全脱钩——用
                --foreground/--background 而不是会被 [data-theme] 改写的
                --sidebar-primary，浅色模式下永远是黑底白图标，深色模式下
                永远是白底黑图标，四个主题色下长得都一样。 */}
            <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-foreground text-background">
              <Command className="size-4" />
            </div>
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-semibold">fp</span>
              <span className="truncate text-xs text-sidebar-foreground/70">
                <SelectValue placeholder="">{currentApp?.name}</SelectValue>
              </span>
            </div>
            <ChevronsUpDown className="ml-auto size-4" />
          </SidebarMenuButton>
          <SelectContent>
            {apps.map((a) => (
              <SelectItem key={a.id} value={a.id}>
                {a.name}
              </SelectItem>
            ))}
          </SelectContent>
        </SelectPrimitive.Root>
      </SidebarMenuItem>
    </SidebarMenu>
  )
}
