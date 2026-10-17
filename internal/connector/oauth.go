package connector

import (
	"context"
	"strings"
)

// OAuthProfile 是三方授权换回来的身份。
type OAuthProfile struct {
	IdentityType string
	Subject      string
	UnionKey     string
	Nickname     string
	AvatarURL    string
}

// OAuthConnector 是「跳到三方授权页、回调带 code」这一类登录方式。
//
// 它们不接受 Login 直接提交 code：code 必须经 fp 发起的 state 会话回调进来，
// 否则 state 防 CSRF / 防钓鱼那一层就被绕过了。见 AuthService.StartOAuth。
type OAuthConnector interface {
	Connector
	// ProviderAppID 是三方平台上的公开应用标识（微信 AppID），供业务方嵌入官方登录组件。
	ProviderAppID(cfg map[string]any) string
	// AuthorizeURL 拼出三方授权页地址。
	AuthorizeURL(cfg map[string]any, redirectURI, state string) string
	// Exchange 用回调带回的 code 换身份。失败返回 domain.Error。
	Exchange(ctx context.Context, cfg map[string]any, code string) (*OAuthProfile, error)
}

// 三方扫码登录共用的交付配置键。各 OAuthConnector 都要在 ConfigSchema 里声明它们。
const (
	OAuthKeyReturnURLs    = "returnUrls"
	OAuthKeyDesktopPoll   = "desktopPoll"
	OAuthKeyDesktopScheme = "desktopScheme"
)

// OAuthReturnURLs 读出网页端回跳白名单，逗号或换行分隔，去空项。
func OAuthReturnURLs(cfg map[string]any) []string {
	raw := ConfigString(cfg, OAuthKeyReturnURLs, "")
	var out []string
	for _, s := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ReturnURLAllowed 判断 u 是否落在某个白名单前缀之下。
//
// 前缀之后只能是结束或 / ? #：否则 https://a.com 会放行 https://a.com.evil.com。
func ReturnURLAllowed(allowed []string, u string) bool {
	for _, p := range allowed {
		if !strings.HasPrefix(u, p) {
			continue
		}
		rest := u[len(p):]
		if rest == "" || strings.HasSuffix(p, "/") || strings.ContainsAny(rest[:1], "/?#") {
			return true
		}
	}
	return false
}
