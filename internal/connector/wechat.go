package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

// TypeWechatWeb 是微信开放平台「网站应用」扫码登录。
//
// 网页端和桌面 App 都用它：官方登录页自己会检测本机已登录的 PC 微信并切到「快捷登录」，
// fp 不需要做任何事。
const TypeWechatWeb = "wechat_web"

const (
	wechatOpenBase = "https://open.weixin.qq.com"
	wechatAPIBase  = "https://api.weixin.qq.com"
)

// WechatWebConnector 实现 OAuthConnector。
type WechatWebConnector struct {
	apiBase string
	client  *http.Client
}

// WechatOption 调整 WechatWebConnector。
type WechatOption func(*WechatWebConnector)

// WithWechatAPIBase 替换 api.weixin.qq.com，测试指向 httptest 服务器。
func WithWechatAPIBase(base string) WechatOption {
	return func(c *WechatWebConnector) { c.apiBase = base }
}

// NewWechatWeb 构造微信网站应用登录方式。
func NewWechatWeb(opts ...WechatOption) *WechatWebConnector {
	c := &WechatWebConnector{apiBase: wechatAPIBase, client: &http.Client{Timeout: 10 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Type 实现 Connector。
func (c *WechatWebConnector) Type() string { return TypeWechatWeb }

// ConfigSchema 实现 Connector。
func (c *WechatWebConnector) ConfigSchema() []domain.Field {
	return []domain.Field{
		{Key: "appId", Label: "AppID", Type: domain.FieldTypeString, Required: true,
			Help: "微信开放平台「网站应用」的 AppID；授权回调域填 fp 的公网域名"},
		{Key: "appSecret", Label: "AppSecret", Type: domain.FieldTypeSecret, Required: true},
		{Key: OAuthKeyReturnURLs, Label: "允许的网页回跳地址", Type: domain.FieldTypeString,
			Help: "网页端登录后回到的业务页面地址前缀，多个用逗号分隔，如 https://app.example.com/login/wechat"},
		{Key: OAuthKeyDesktopScheme, Label: "桌面 App 自定义协议", Type: domain.FieldTypeString,
			Help: "如 myapp。配置后桌面端登录完成跳回 myapp://oauth?fpTicket=…，并且不再允许轮询"},
		{Key: OAuthKeyDesktopPoll, Label: "允许桌面端轮询", Type: domain.FieldTypeBool, Default: false,
			Help: "给没有自定义协议的桌面 App 用。轮询有扫码钓鱼风险，只给确有桌面客户端的应用开"},
	}
}

// Authenticate 实现 Connector：微信登录不接受直接提交凭据。
func (c *WechatWebConnector) Authenticate(context.Context, map[string]any, Credentials) (*Result, error) {
	return nil, domain.Fail(domain.ErrInvalidArgument, domain.CodeInvalidArgument,
		"微信登录需经 StartOAuth 发起，不能直接提交凭据")
}

// SubjectFrom 实现 Connector。
func (c *WechatWebConnector) SubjectFrom(Credentials) (string, string) { return "", "" }

// ProviderAppID 实现 OAuthConnector。
func (c *WechatWebConnector) ProviderAppID(cfg map[string]any) string {
	return ConfigString(cfg, "appId", "")
}

// AuthorizeURL 实现 OAuthConnector。
func (c *WechatWebConnector) AuthorizeURL(cfg map[string]any, redirectURI, state string) string {
	// 手拼而不用 url.Values.Encode：后者按键名排序，微信对授权链接的参数顺序做强匹配，
	// 顺序不对授权页打不开。末尾的锚点同样是官方要求。
	return wechatOpenBase + "/connect/qrconnect?appid=" + url.QueryEscape(c.ProviderAppID(cfg)) +
		"&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&response_type=code&scope=snsapi_login&state=" + url.QueryEscape(state) + "#wechat_redirect"
}

type wechatTokenResp struct {
	AccessToken string `json:"access_token"`
	OpenID      string `json:"openid"`
	UnionID     string `json:"unionid"`
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
}

type wechatUserResp struct {
	Nickname   string `json:"nickname"`
	HeadImgURL string `json:"headimgurl"`
	UnionID    string `json:"unionid"`
	ErrCode    int    `json:"errcode"`
	ErrMsg     string `json:"errmsg"`
}

// Exchange 实现 OAuthConnector。
//
// 取用户信息失败不算登录失败：openid / unionid 已经拿到，昵称头像只是建号时的初始资料。
func (c *WechatWebConnector) Exchange(ctx context.Context, cfg map[string]any, code string) (*OAuthProfile, error) {
	q := url.Values{}
	q.Set("appid", ConfigString(cfg, "appId", ""))
	q.Set("secret", ConfigString(cfg, "appSecret", ""))
	q.Set("code", code)
	q.Set("grant_type", "authorization_code")
	var tok wechatTokenResp
	if err := c.getJSON(ctx, "/sns/oauth2/access_token?"+q.Encode(), &tok); err != nil {
		return nil, upstreamFailed("换取 access_token: %v", err)
	}
	if tok.ErrCode != 0 || tok.OpenID == "" {
		return nil, upstreamFailed("换取 access_token: errcode=%d errmsg=%s", tok.ErrCode, tok.ErrMsg)
	}

	p := &OAuthProfile{IdentityType: domain.IdentityTypeWechatWeb, Subject: tok.OpenID, UnionKey: tok.UnionID}

	uq := url.Values{}
	uq.Set("access_token", tok.AccessToken)
	uq.Set("openid", tok.OpenID)
	uq.Set("lang", "zh_CN")
	var u wechatUserResp
	err := c.getJSON(ctx, "/sns/userinfo?"+uq.Encode(), &u)
	switch {
	case err != nil:
		slog.Warn("connector: 取微信用户信息失败，按无昵称头像继续", "err", err)
	case u.ErrCode != 0:
		slog.Warn("connector: 取微信用户信息失败，按无昵称头像继续", "errcode", u.ErrCode, "errmsg", u.ErrMsg)
	default:
		p.Nickname, p.AvatarURL = u.Nickname, u.HeadImgURL
		if p.UnionKey == "" {
			p.UnionKey = u.UnionID
		}
	}
	return p, nil
}

func (c *WechatWebConnector) getJSON(ctx context.Context, pathAndQuery string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+pathAndQuery, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// upstreamFailed 的细节（含 errmsg）只进 Desc，不进 Msg：Msg 会展示给终端用户。
func upstreamFailed(format string, args ...any) error {
	return domain.Fail(domain.ErrInternal, domain.CodeWechatUpstreamFailed, "微信登录失败，请重试").
		WithDesc(format, args...)
}
