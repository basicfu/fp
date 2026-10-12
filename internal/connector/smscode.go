package connector

import (
	"context"

	"github.com/basicfu/fp/internal/domain"
)

// TypeSMSCode 是短信验证码登录方式的类型标识。
const TypeSMSCode = "sms_code"

// CodeVerifier 是 sms_code connector 需要的最小验证码校验能力。
type CodeVerifier interface {
	Verify(ctx context.Context, phone, code string) error
}

// SMSCodeConnector 用「手机号 + 短信验证码」校验身份。
type SMSCodeConnector struct {
	codes CodeVerifier
}

// NewSMSCode 构造短信验证码登录方式。
func NewSMSCode(codes CodeVerifier) *SMSCodeConnector {
	return &SMSCodeConnector{codes: codes}
}

// Type 实现 Connector。
func (c *SMSCodeConnector) Type() string { return TypeSMSCode }

// ConfigSchema 实现 Connector。
func (c *SMSCodeConnector) ConfigSchema() []domain.Field {
	return []domain.Field{
		{
			Key: "autoRegister", Label: "首次登录自动注册",
			Type: domain.FieldTypeBool, Default: true,
			Help: "关闭后，未注册的手机号即使验证码正确也无法登录",
		},
	}
}

// Authenticate 实现 Connector。
//
// 凭据键：phone、code。
//
// 手机号格式在调用验证码校验**之前**检查：格式不合法就去校验，
// 会白白消耗掉该号码的验证尝试次数（见 service.MaxVerifyAttempts）。
func (c *SMSCodeConnector) Authenticate(ctx context.Context, cfg map[string]any, creds Credentials) (*Result, error) {
	phone := creds.Get("phone")
	code := creds.Get("code")

	if !isChineseMobile(phone) {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodePhoneInvalid, "手机号格式不正确")
	}
	if code == "" {
		return nil, domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "验证码不能为空")
	}

	if err := c.codes.Verify(ctx, phone, code); err != nil {
		return nil, err
	}

	return &Result{
		IdentityType: domain.IdentityTypePhone,
		Subject:      phone,
		// 验证码校验通过即证明该手机号归申请人所有，可以据此建号。
		AllowCreate: ConfigBool(cfg, "autoRegister", true),
	}, nil
}

// SubjectFrom 实现 Connector。
func (c *SMSCodeConnector) SubjectFrom(creds Credentials) (string, string) {
	phone := creds.Get("phone")
	if phone == "" {
		return "", ""
	}
	return domain.IdentityTypePhone, phone
}
