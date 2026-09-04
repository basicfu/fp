package connector

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

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
//
// 但只有这三种。数据库不可用之类的真实故障会原样上报，不伪装成凭据错误。
func (c *PasswordConnector) Authenticate(ctx context.Context, cfg map[string]any, creds Credentials) (*Result, error) {
	account := creds.Get("account")
	password := creds.Get("password")
	if account == "" {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "account 不能为空")
	}
	if password == "" {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "password 不能为空")
	}

	// 统一的失败错误，三种失败原因共用，防止账号枚举。
	invalid := domain.Failf(domain.ErrInvalidCredential, domain.CodeCredentialInvalid, "账号或密码不正确")

	identityType := DetectIdentityType(account)
	if !identityTypeAllowed(cfg, identityType) {
		return nil, invalid
	}

	// 无论账号是否存在都要走一次口令校验。
	//
	// 账号不存在时如果直接返回，就跳过了 bcrypt——而 bcrypt 是这条路径上唯一
	// 昂贵的一步，跳没跳会在响应时间上差一到两个数量级。攻击者不看响应内容、
	// 只掐表，就能把"哪些手机号注册过"问出来，上面 invalid 那套同错误设计
	// 也就形同虚设。VerifyPassword 对不存在的用户会跑一次哑哈希比对来抹平时序。
	user, _, lookupErr := c.lookup.FindByIdentity(ctx, identityType, account)

	// 只有"确实查不到这个登录标识"才等价于凭据错误。
	//
	// 把查询的**任何**错误都折成 invalid，等于让一次 Postgres 抖动表现成
	// "全站所有人的密码都错了"：调用方拿到 401 而不是 5xx，监控看不出是故障，
	// 而审计表会被灌满一批凭据失败记录——事后排查时它们与真正的爆破尝试
	// 无法区分。故障要如实上报，靠错误链上的哨兵区分，而不是靠"反正都是失败"。
	//
	// 这不削弱防枚举：抹平时序的哑哈希在 VerifyPassword 里，而数据库故障
	// 对所有账号一视同仁，攻击者无法用它区分某个账号存不存在。
	if lookupErr != nil && !errors.Is(lookupErr, domain.ErrNotFound) {
		return nil, lookupErr
	}

	userID := uuid.Nil
	if lookupErr == nil {
		userID = user.ID
	}

	// 同理：VerifyPassword 也可能因为读不到库而失败，那同样是故障不是密码错。
	verifyErr := c.lookup.VerifyPassword(ctx, userID, password)
	if verifyErr != nil && !errors.Is(verifyErr, domain.ErrInvalidCredential) {
		return nil, verifyErr
	}
	if lookupErr != nil || verifyErr != nil {
		return nil, invalid
	}

	return &Result{
		IdentityType: identityType,
		Subject:      account,
		// 密码登录不建号：账号都不存在，谈不上密码正确。
		AllowCreate: false,
	}, nil
}

// SubjectFrom 实现 Connector。
func (c *PasswordConnector) SubjectFrom(creds Credentials) (string, string) {
	account := creds.Get("account")
	if account == "" {
		return "", ""
	}
	return DetectIdentityType(account), account
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
//
// **调用方必须先去掉首尾空白**（Credentials.Get 已经做了）。
// 手机号判定要求长度恰好 11，"  13800138000  " 会被判成 username。
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
