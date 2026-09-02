// 与后端 internal/httpapi 里的 DTO 一一对应。
// 字段名必须与 Go 结构体的 json tag 完全一致。

export interface SessionPolicy {
  idleTimeoutSeconds: number
  idleTimeoutMobileSeconds: number
  maxLifetimeSeconds: number
  rotateIntervalSeconds: number
  extendIntervalSeconds: number
  tokenCacheTtlSeconds: number
}

export type ApplicationStatus = 'ACTIVE' | 'DISABLED'

export interface Application {
  id: string
  name: string
  slug: string
  appId: string
  status: ApplicationStatus
  cookieDomain: string
  session: SessionPolicy
  createdAt: number
  updatedAt: number
}

export interface CreateApplicationResponse {
  application: Application
  /** 明文密钥，只在创建时返回这一次，之后无法读回。 */
  appSecret: string
}

export type FieldType = 'string' | 'int' | 'bool' | 'secret'

/** 与 internal/domain/field.go 的 Field 对应。管理 UI 靠它自动生成表单。 */
export interface Field {
  key: string
  label: string
  type: FieldType
  required: boolean
  default?: unknown
  help?: string
}

export interface ConnectorSchema {
  type: string
  fields: Field[]
}

export interface ConnectorConfig {
  type: string
  enabled: boolean
  config: Record<string, unknown>
}

export type UserStatus = 'ACTIVE' | 'FROZEN' | 'PENDING_DELETE' | 'DELETED'

export interface Identity {
  type: string
  subject: string
  lastLoginAt: number
}

export interface User {
  id: string
  nickname: string
  avatarUrl: string
  status: UserStatus
  hasPassword: boolean
  identities: Identity[]
  createdAt: number
}

export interface UserListResponse {
  items: User[]
  total: number
}

export interface UserSession {
  id: string
  appId: string
  ip: string
  ua: string
  mobile: boolean
  firstAuthAt: number
  idleExpiresAt: number
}

export interface LoginLog {
  id: string
  identityType: string
  subject: string
  event: string
  success: boolean
  reason: string
  ip: string
  ua: string
  createdAt: number
}
