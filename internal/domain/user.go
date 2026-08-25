package domain

import "github.com/google/uuid"

// 用户状态。状态机见 CanTransitionUserStatus。
const (
	UserStatusActive        = "ACTIVE"
	UserStatusFrozen        = "FROZEN"
	UserStatusPendingDelete = "PENDING_DELETE" // 已提交注销，处于保护期
	UserStatusDeleted       = "DELETED"        // 终态
)

// userTransitions 是允许的状态迁移。缺省即不允许。
var userTransitions = map[string]map[string]bool{
	UserStatusActive: {
		UserStatusFrozen:        true,
		UserStatusPendingDelete: true,
	},
	UserStatusFrozen: {
		UserStatusActive: true,
	},
	UserStatusPendingDelete: {
		// 保护期内任意登录行为都会撤销注销申请
		UserStatusActive:  true,
		UserStatusDeleted: true,
	},
	// UserStatusDeleted 是终态，无出边。
}

// CanTransitionUserStatus 报告 from → to 是否为允许的状态迁移。
func CanTransitionUserStatus(from, to string) bool {
	return userTransitions[from][to]
}

// User 是一个全局唯一的用户。登录标识全部落在 Identity 上，
// 本结构只持有跨标识共享的资料与凭据。
type User struct {
	ID uuid.UUID
	// PasswordHash 是 password connector 的凭据，未设置时为空串。
	PasswordHash      string
	Nickname          string
	AvatarURL         string
	Gender            string
	Status            string
	DeleteSubmittedAt int64 // 0 表示未提交注销
	CreatedAt         int64
	UpdatedAt         int64
}

// CanLogin 报告该状态的用户是否允许登录。
// 注销保护期内允许登录，登录本身即撤销注销申请。
func (u User) CanLogin() bool {
	return u.Status == UserStatusActive || u.Status == UserStatusPendingDelete
}

// 登录日志事件类型。
const (
	LoginEventLogin  = "login"
	LoginEventLogout = "logout"
	LoginEventRotate = "rotate"
	LoginEventRevoke = "revoke"
)

// LoginLog 是一条登录审计记录。
// UserID / ApplicationID 用指针是因为登录失败时它们可能为空。
type LoginLog struct {
	ID            uuid.UUID
	UserID        *uuid.UUID
	ApplicationID *uuid.UUID
	IdentityType  string
	// Subject 是脱敏后的登录标识。完整值通过 UserID 关联查询。
	Subject   string
	Event     string
	Success   bool
	Reason    string
	IP        string
	UA        string
	SessionID string
	CreatedAt int64
}
