import { useCurrentApp } from '@/lib/current-app'
import PermissionsPanel from '@/components/PermissionsPanel'

export default function Permissions() {
  const { currentApp, apps, loading, reload } = useCurrentApp()

  if (loading) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (apps.length === 0) {
    return <p className="text-sm text-muted-foreground">还没有应用，请先在「应用列表」创建一个。</p>
  }
  if (!currentApp) return null

  return (
    <div className="space-y-4">
      <PermissionsPanel app={currentApp} onAppChanged={reload} />
    </div>
  )
}
