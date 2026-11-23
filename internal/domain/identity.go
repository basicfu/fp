package domain

import "github.com/google/uuid"

// 登录标识类型。新增登录方式时在此登记。
const (
	IdentityTypePhone    = "phone"
	IdentityTypeUsername = "username"
	IdentityTypeEmail    = "email"
	IdentityTypeWechatMP = "wechat_mp"
	// IdentityTypeWechatWeb 是微信开放平台「网站应用」扫码登录：subject=openid，union_key=unionid。
	IdentityTypeWechatWeb = "wechat_web"
	// IdentityTypeWechatApp 是微信开放平台「移动应用」登录：subject=openid，union_key=unionid。
	// 同一个人在网站应用、移动应用下的 openid 不同，靠 unionid 归并到同一个用户。
	IdentityTypeWechatApp = "wechat_app"
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
	// Nickname 是三方身份自带的昵称（微信昵称），登录、绑定时刷新；本地标识为空串。
	// 与 User.Nickname 不同：那是用户自己的昵称，这里只用来告诉用户"绑的是哪个微信"。
	Nickname string
}
