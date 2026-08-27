package domain

import "github.com/google/uuid"

// 登录标识类型。新增登录方式时在此登记。
const (
	IdentityTypePhone    = "phone"
	IdentityTypeUsername = "username"
	IdentityTypeEmail    = "email"
	IdentityTypeWechatMP = "wechat_mp"
)

// Identity 是一条登录凭据记录。一个 User 可以有多条。
type Identity struct {
	ID      uuid.UUID
	UserID  uuid.UUID
	Type    string
	Subject string
	// UnionKey 用于跨 Type 归并（微信 unionId）。本地标识为空串。
	UnionKey string
	// Credential 存第三方 token 等。密码存在 User.PasswordHash。
	Credential  string
	LastLoginAt int64
	CreatedAt   int64
}
