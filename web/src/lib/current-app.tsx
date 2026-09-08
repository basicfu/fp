import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { api } from './api'
import { useResource } from './useResource'
import type { Application } from './types'

const STORAGE_KEY = 'fp-current-app-id'

export interface CurrentAppValue {
  apps: Application[]
  currentAppId: string
  currentApp: Application | null
  setCurrentAppId: (id: string) => void
  loading: boolean
  error: string
  reload: () => void
}

export const CurrentAppContext = createContext<CurrentAppValue | null>(null)

/** 拿全局"当前应用"状态。必须在 CurrentAppProvider 内使用，否则直接报错——同 useAuth 的风格，不悄悄退化。 */
export function useCurrentApp(): CurrentAppValue {
  const v = useContext(CurrentAppContext)
  if (!v) throw new Error('useCurrentApp 必须在 CurrentAppProvider 内使用')
  return v
}

function readStoredId(): string {
  try {
    return localStorage.getItem(STORAGE_KEY) ?? ''
  } catch {
    return ''
  }
}

function persistId(id: string) {
  try {
    localStorage.setItem(STORAGE_KEY, id)
  } catch {
    // 隐私模式/配额满：不持久化，本次会话内仍然生效
  }
}

export function CurrentAppProvider({ children }: { children: ReactNode }) {
  const apps = useResource(() => api.get<Application[]>('/applications'), [])
  const [currentAppId, setCurrentAppIdState] = useState<string>(readStoredId)

  // 应用列表变化时（首次拉到、或者后面 reload 拉到新的一份）才重新校验
  // currentAppId 是否还在列表里——不把 currentAppId 放进依赖数组：否则
  // "用户刚选中一个还没进到最新列表里的应用"（比如新建应用后立刻
  // setCurrentAppId 到新 id，此时 reload() 的响应还在路上，apps.data
  // 还是旧列表）会被这个效应误判成"记住的 id 已经不存在了"，把选择
  // 错误地纠正回列表第一项。只在 apps.data 真正变化时才做这个校验，
  // 每次校验用的都是当次渲染里最新的 currentAppId，不是陈旧值。
  useEffect(() => {
    if (!apps.data) return
    const stillExists = apps.data.some((a) => a.id === currentAppId)
    if (stillExists) return
    const fallback = apps.data[0]?.id ?? ''
    setCurrentAppIdState(fallback)
    persistId(fallback)
  }, [apps.data])

  const setCurrentAppId = useCallback((id: string) => {
    setCurrentAppIdState(id)
    persistId(id)
  }, [])

  const currentApp = useMemo(
    () => apps.data?.find((a) => a.id === currentAppId) ?? null,
    [apps.data, currentAppId],
  )

  const value = useMemo<CurrentAppValue>(
    () => ({
      apps: apps.data ?? [],
      currentAppId,
      currentApp,
      setCurrentAppId,
      loading: apps.loading,
      error: apps.error,
      reload: apps.reload,
    }),
    [apps.data, apps.loading, apps.error, apps.reload, currentAppId, currentApp, setCurrentAppId],
  )

  return <CurrentAppContext.Provider value={value}>{children}</CurrentAppContext.Provider>
}
