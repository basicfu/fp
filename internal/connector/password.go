package connector

import (
	"context"
	"strings"

	"github.com/basicfu/fp/internal/domain"
)

// TypePassword 是密码登录方式的类型标识。
const TypePassword = "password"

// PasswordConnector 用「账号 + 密码」校验身份。
// 账号可以是手机号、用户名或邮箱，具体开放哪几种由应用配置决定。
type PasswordConnector struct {
	lookup UserLookup
}

// NewPassword 构造密码登录方式。
func NewPassword(lookup UserLookup) *PasswordConnector {
	return &PasswordConnector{lookup: lookup}
}

// Type 实现 Connector。
func (c *PasswordConnector) Type() string { return TypePassword }

// ConfigSchema 实现 Connector。
func (c *PasswordConnector) ConfigSchema() []domain.Field {
	return []domain.Field{
		{Key: "allowPhone", Label: "允许手机号登录", Type: domain.FieldTypeBool, Default: true},
		{Key: "allowUsername", Label: "允许用户名登录", Type: domain.FieldTypeBool, Default: true},
		{Key: "allowEmail", Label: "允许邮箱登录", Type: domain.FieldTypeBool, Default: false},
	}
}

// Authenticate 实现 Connector。
//
// 凭据键：account（手机号/用户名/邮箱）、password。
//
// 账号不存在、账号类型未开放、密码错误三种情况**返回同一个错误**，
// 避免攻击者据此枚举已注册账号。
func (c *PasswordConnector) Authenticate(ctx context.Context, cfg map[string]any, creds Credentials) (*Result, error) {
	account := creds.Get("account")
	password := creds.Get("password")
	if account == "" {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "account 不能为空")
	}
	if password == "" {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "password 不能为空")
	}

	// 统一的失败错误，三种失败原因共用，防止账号枚举。
	invalid := domain.Errorf(domain.ErrInvalidCredential, "账号或密码不正确")

	identityType := DetectIdentityType(account)
	if !identityTypeAllowed(cfg, identityType) {
		return nil, invalid
	}

	user, _, err := c.lookup.FindByIdentity(ctx, identityType, account)
	if err != nil {
		return nil, invalid
	}
	if err := c.lookup.VerifyPassword(ctx, user.ID, password); err != nil {
		return nil, invalid
	}

	return &Result{
		IdentityType: identityType,
		Subject:      account,
		// 密码登录不建号：账号都不存在，谈不上密码正确。
		AllowCreate: false,
	}, nil
}

func identityTypeAllowed(cfg map[string]any, identityType string) bool {
	switch identityType {
	case domain.IdentityTypePhone:
		return ConfigBool(cfg, "allowPhone", true)
	case domain.IdentityTypeUsername:
		return ConfigBool(cfg, "allowUsername", true)
	case domain.IdentityTypeEmail:
		return ConfigBool(cfg, "allowEmail", false)
	default:
		return false
	}
}

// DetectIdentityType 从账号字符串推断标识类型。
//
//	11 位、以 1 开头的纯数字 → phone
//	含 @                    → email
//	其余                    → username
func DetectIdentityType(account string) string {
	if strings.Contains(account, "@") {
		return domain.IdentityTypeEmail
	}
	if isChineseMobile(account) {
		return domain.IdentityTypePhone
	}
	return domain.IdentityTypeUsername
}

func isChineseMobile(s string) bool {
	if len(s) != 11 || s[0] != '1' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
