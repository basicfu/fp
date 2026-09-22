import { useParams } from 'react-router'
import ApplicationSettings from '@/components/ApplicationSettings'
import { api } from '@/lib/api'
import { useResource } from '@/lib/useResource'
import type { Application } from '@/lib/types'

/**
 * 独立路由而不是弹窗：这样浏览器地址栏、刷新、前进后退都天然work——
 * 弹窗是页面内状态，一刷新就丢，用户改到一半的表单也跟着没了。
 */
export default function ApplicationDetail() {
  const { id = '' } = useParams()
  const app = useResource(() => api.get<Application>(`/applications/${id}`), [id])

  if (app.loading && !app.data) return <p className="text-sm text-muted-foreground">加载中…</p>
  if (app.error) return <p className="text-sm text-destructive">{app.error}</p>
  if (!app.data) return null

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-semibold">{app.data.name}</h1>
      <ApplicationSettings app={app.data} onSaved={app.reload} />
    </div>
  )
}
