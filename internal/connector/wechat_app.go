package connector

import (
	"context"

	"github.com/basicfu/fp/internal/domain"
)

// TypeWechatApp 是微信开放平台「移动应用」登录：App 集成微信 OpenSDK，拉起微信授权后拿到 code，
// 业务后端把 code 转交 fp 换结论。
const TypeWechatApp = "wechat_app"

// WechatAppConnector 实现 AppSDKConnector。
type WechatAppConnector struct {
	api wechatOpenAPI
}

// NewWechatApp 构造微信移动应用登录方式。
func NewWechatApp(opts ...WechatOption) *WechatAppConnector {
	return &WechatAppConnector{api: newWechatOpenAPI(opts)}
}

// Type 实现 Connector。
func (c *WechatAppConnector) Type() string { return TypeWechatApp }

// ConfigSchema 实现 Connector。
func (c *WechatAppConnector) ConfigSchema() []domain.Field {
	return []domain.Field{
		{Key: "appId", Label: "AppID", Type: domain.FieldTypeString, Required: true,
			Help: "微信开放平台「移动应用」的 AppID，与网站应用的不是同一个；两者要挂在同一个开放平台账号下才能按 unionId 归并成一个账号"},
		{Key: "appSecret", Label: "AppSecret", Type: domain.FieldTypeSecret, Required: true},
	}
}

// Authenticate 实现 Connector：code 走 ExchangeAppCode，不走通用登录。
func (c *WechatAppConnector) Authenticate(context.Context, map[string]any, Credentials) (*Result, error) {
	return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
		"微信 App 登录需经 ExchangeAppCode 提交 code，不能直接提交凭据")
}

// SubjectFrom 实现 Connector。
func (c *WechatAppConnector) SubjectFrom(Credentials) (string, string) { return "", "" }

// Exchange 实现 ThirdPartyConnector。
func (c *WechatAppConnector) Exchange(ctx context.Context, cfg map[string]any, code string) (*OAuthProfile, error) {
	return c.api.exchange(ctx, cfg, code, domain.IdentityTypeWechatApp)
}

// AppSDK 实现 AppSDKConnector。
func (c *WechatAppConnector) AppSDK() {}
