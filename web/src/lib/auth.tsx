import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { api, setUnauthorizedHandler } from './api'

type Status = 'loading' | 'authed' | 'anon'

interface MeResponse {
  id: string
  username: string
}

interface AuthValue {
  status: Status
  username: string | null
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthValue | null>(null)

export function useAuth(): AuthValue {
  const v = useContext(AuthContext)
  if (!v) throw new Error('useAuth 必须在 AuthProvider 内使用')
  return v
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status>('loading')
  const [username, setUsername] = useState<string | null>(null)

  // 任何一次请求拿到 401 都直接把状态打回未登录：管理端会话有效期两小时，
  // 用户很可能在某个页面上停留到过期，这时不该等他点到下一个按钮才发现。
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setStatus('anon')
      setUsername(null)
    })
    return () => setUnauthorizedHandler(() => {})
  }, [])

  // 首次挂载探测当前会话。
  //
  // 失败（401、网络断、后端 500）一律落到 anon，绝不留在 loading：
  // 留在 loading 的界面是一个永远转不完的圈，用户既看不到登录页也无从重试。
  useEffect(() => {
    let alive = true
    api
      .get<MeResponse>('/me')
      .then((me) => {
        if (!alive) return
        setUsername(me.username)
        setStatus('authed')
      })
      .catch(() => {
        if (!alive) return
        setUsername(null)
        setStatus('anon')
      })
    return () => {
      alive = false
    }
  }, [])

  const login = useCallback(async (u: string, p: string) => {
    const res = await api.post<{ username: string }>('/login', { username: u, password: p })
    setUsername(res.username)
    setStatus('authed')
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.post('/logout')
    } finally {
      // 后端登出失败也要把前端状态清掉：cookie 可能已经过期，
      // 留在"已登录"状态只会让用户在每个页面上撞 401。
      setUsername(null)
      setStatus('anon')
    }
  }, [])

  const value = useMemo<AuthValue>(
    () => ({ status, username, login, logout }),
    [status, username, login, logout],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
