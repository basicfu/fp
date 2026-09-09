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
  /** 这个应用里每个人自动拥有的角色 key。空串表示不设。 */
  defaultRoleKey: string
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

// --- 授权 ---------------------------------------------------------------

/** 角色是**全局**的，不属于某个应用。应用归属靠它挂了哪些应用的权限点。 */
export interface Role {
  id: string
  /** key 是身份，创建后不可修改（user_role.roles[] 与已签发会话都按它引用）。 */
  key: string
  name: string
  /** 空串表示没有父角色。 */
  parentId: string
  createdAt: number
}

export type PermissionStatus = 'normal' | 'stale' | 'manual'
export type PermissionSource = 'app' | 'manual'

export interface PermissionPoint {
  id: string
  /** 形如 GET:/orders/{id}。app 上报的由路由自动生成，manual 的由人填。 */
  key: string
  name: string
  kind: string
  source: PermissionSource
  /** 算出来的，不存库：normal / stale（过渡中）/ manual。 */
  status: PermissionStatus
  /** "过渡中"已经持续了多久，毫秒。其余状态为 0。 */
  staleForMs: number
  lastSeenAt: number
  createdAt: number
}

/** 授权的效果。空串表示没有授权（收回时也传空串）。 */
export type Effect = 'allow' | 'deny' | ''

/** 某个角色**直接**持有的一条授权，不含从父角色继承来的。 */
export interface RoleGrant {
  permissionId: string
  effect: Exclude<Effect, ''>
}

// --- 配置中心 -------------------------------------------------------------

/**
 * 配置分区。同名 key 在两个分区下是两个独立的配置项，各有各的值与版本
 * 序列。不再局限于 DEFAULT/WEB 两个固定值——管理端可以给一个应用建
 * 任意名字的分区（后端 domain.IsConfigType 只挡明显不合法的字符集），
 * 'DEFAULT' 是唯一保留名：应用天然就有，UI 上永远展示它这个标签页。
 */
export type ConfigPartition = string

export const DEFAULT_PARTITION: ConfigPartition = 'DEFAULT'

export interface ConfigSnapshot {
  /** 0 表示该分区还没有任何版本。 */
  seq: number
  /** 管理端提交的 YAML 原文，原样存取——注释就是备注，不再单独有 desc 字段。 */
  value: string
}

/** 与 internal/httpapi/config.go 的 configVersionDTO 对应，供 Task 14 的版本历史页使用。 */
export interface ConfigVersion {
  seq: number
  createdAt: number
}

export interface SaveConfigResponse {
  seq: number
}
