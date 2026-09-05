import { Navigate, Route, Routes } from 'react-router'
import Layout from '@/components/Layout'
import Login from '@/pages/Login'
import Applications from '@/pages/Applications'
import ApplicationDetail from '@/pages/ApplicationDetail'
import Users from '@/pages/Users'
import UserDetail from '@/pages/UserDetail'
import Roles from '@/pages/Roles'
import RoleDetail from '@/pages/RoleDetail'
import { useAuth } from '@/lib/auth'

/**
 * RequireAuth 把未登录的访问送回登录页。
 *
 * loading 期间渲染一个空白占位而不是直接跳转：首次进入时 /me 还没回来，
 * 直接跳转会让已登录用户先看到一次登录页闪烁。
 */
function RequireAuth({ children }: { children: React.ReactNode }) {
  const { status } = useAuth()
  if (status === 'loading') return <div className="p-8 text-sm text-muted-foreground">加载中…</div>
  if (status === 'anon') return <Navigate to="/login" replace />
  return <>{children}</>
}

export default function AppRoutes() {
  const { status } = useAuth()

  return (
    <Routes>
      <Route
        path="/login"
        element={status === 'authed' ? <Navigate to="/applications" replace /> : <Login />}
      />
      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route path="/" element={<Navigate to="/applications" replace />} />
        <Route path="/applications" element={<Applications />} />
        <Route path="/applications/:id" element={<ApplicationDetail />} />
        <Route path="/users" element={<Users />} />
        <Route path="/users/:id" element={<UserDetail />} />
        <Route path="/roles" element={<Roles />} />
        <Route path="/roles/:id" element={<RoleDetail />} />
      </Route>
      <Route path="*" element={<Navigate to="/applications" replace />} />
    </Routes>
  )
}
