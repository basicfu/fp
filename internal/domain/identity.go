package domain

import "github.com/google/uuid"

// 登录标识类型。新增登录方式时在此登记。
const (
	IdentityTypePhone    = "phone"
	IdentityTypeUsername = "username"
	IdentityTypeEmail    = "email"
	IdentityTypeWechatMP = "wechat_mp"
)

// mergeableIdentityTypes 是"本地标识"集合。
// 这类标识由 fp 自己校验（短信验证码 / 密码），同一个 subject 必然是同一个人，
// 因此可以直接按 (type, subject) 归并到同一个 User。
// 第三方标识（微信等）的 subject 是对方系统的 openid，只能通过 union_key 归并。
var mergeableIdentityTypes = map[string]bool{
	IdentityTypePhone:    true,
	IdentityTypeUsername: true,
	IdentityTypeEmail:    true,
}

// IsMergeableIdentityType 报告该类型是否为可按 subject 归并的本地标识。
func IsMergeableIdentityType(t string) bool {
	return mergeableIdentityTypes[t]
}

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
