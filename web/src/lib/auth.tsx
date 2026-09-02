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
    } catch {
      // /logout 挂在 requireAdmin 中间件之后：管理端会话 cookie 一旦过期，
      // 这个请求本身就会先被中间件拦成 401（正是下面 finally 注释里"cookie
      // 可能已经过期"预设的场景）。这里必须接住这个异常——logout() 是给
      // Layout.tsx 用 void logout() 调用的"发射后不管"写法，void 不会消费
      // rejection，不接住就会在控制台留一条 Uncaught (in promise) ApiError。
      // 放在这个函数内部而不是留给调用方处理：能一次性保护所有未来调用方，
      // 不用要求每个调用点都记得自己去 catch。
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
