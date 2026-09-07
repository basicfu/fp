import { AppWindow } from 'lucide-react'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useCurrentApp } from '@/lib/current-app'

export default function AppSwitcher() {
  const { apps, currentAppId, setCurrentAppId, loading } = useCurrentApp()

  if (!loading && apps.length === 0) {
    return <p className="px-2 text-xs text-muted-foreground">还没有应用</p>
  }

  return (
    <Select value={currentAppId} onValueChange={(v) => v && setCurrentAppId(v)}>
      <SelectTrigger aria-label="切换当前应用" className="w-full">
        <AppWindow className="size-4 shrink-0 text-muted-foreground" />
        <SelectValue placeholder={loading ? '加载中…' : '选择应用'} />
      </SelectTrigger>
      <SelectContent>
        {apps.map((a) => (
          <SelectItem key={a.id} value={a.id}>
            {a.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
