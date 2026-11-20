package notify

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/wechatmp"
)

// TypeWechatMP 是微信服务号。一个 fp 部署只能有一个这种实例（数据库唯一索引守着）：
// 它同时是模板消息的供应商，也是服务号订阅（带参二维码、事件推送）用的那份凭据。
const TypeWechatMP = "wechat_mp"

// 服务号实例的配置键。
const (
	WechatMPKeyAppID       = "appId"
	WechatMPKeyAppSecret   = "appSecret"
	WechatMPKeyToken       = "token"
	WechatMPKeyAESKey      = "encodingAESKey"
	WechatMPKeyBindReply   = "bindReply"
	WechatMPKeyFollowReply = "followReply"
)

// WechatMPCallbackPath 是服务号事件推送在 fp 上的路径，拼在 public_url 后面填进公众平台的「服务器配置」。
const WechatMPCallbackPath = "/wechat/mp/callback"

// WechatMPSpec 返回服务号的供应商类型。apiBase 为空时用微信的正式域名，测试传 httptest 地址。
func WechatMPSpec(apiBase string) TypeSpec {
	return TypeSpec{
		Type:    TypeWechatMP,
		Channel: domain.NotifyChannelWechatMP,
		ConfigSchema: []domain.Field{
			{Key: WechatMPKeyAppID, Label: "AppID", Type: domain.FieldTypeString, Required: true,
				Help: "认证服务号的 AppID。fp 的出口 IP 要加进公众平台「IP 白名单」，否则取不到调用凭证"},
			{Key: WechatMPKeyAppSecret, Label: "AppSecret", Type: domain.FieldTypeSecret, Required: true},
			{Key: WechatMPKeyToken, Label: "Token", Type: domain.FieldTypeSecret, Required: true,
				Help: "与公众平台「服务器配置」里的令牌一致；服务器地址填 <public_url>" + WechatMPCallbackPath},
			{Key: WechatMPKeyAESKey, Label: "EncodingAESKey", Type: domain.FieldTypeSecret,
				Help: "消息加解密方式选「安全模式」时填（推荐）；留空表示明文模式"},
			{Key: WechatMPKeyBindReply, Label: "扫码开启通知后的回复", Type: domain.FieldTypeString,
				Help: "用户扫业务方给的二维码开启通知后，服务号回给他的文字。留空用默认文案"},
			{Key: WechatMPKeyFollowReply, Label: "直接关注时的回复", Type: domain.FieldTypeString,
				Help: "用户自己搜索关注时回复的文字，留空不回复。开启服务器配置后，公众平台里设置的自动回复不再生效"},
		},
		New: func(cfg Config) (Provider, error) {
			return &wechatMPProvider{client: NewWechatMPAccount(cfg, apiBase).Client}, nil
		},
	}
}

// WechatMPAccount 是一个服务号实例的全部能力：接口客户端、事件推送的校验参数与回复文案。
type WechatMPAccount struct {
	Client      *wechatmp.Client
	Server      wechatmp.Server
	BindReply   string
	FollowReply string
}

// DefaultWechatMPBindReply 是没配回复文案时，扫码开启通知后的回复。
const DefaultWechatMPBindReply = "已开启微信通知，账号的重要动态会在这里提醒你。"

// NewWechatMPAccount 从实例配置构造。接口请求走通知模块的 HTTP 客户端：开发环境要经出口代理，
// 服务号接口按出口 IP 白名单放行。
func NewWechatMPAccount(cfg Config, apiBase string) *WechatMPAccount {
	appID := strings.TrimSpace(cfg.String(WechatMPKeyAppID))
	bind := strings.TrimSpace(cfg.String(WechatMPKeyBindReply))
	if bind == "" {
		bind = DefaultWechatMPBindReply
	}
	return &WechatMPAccount{
		Client: wechatmp.New(wechatmp.Config{
			AppID: appID, AppSecret: cfg.String(WechatMPKeyAppSecret), APIBase: apiBase, HTTP: httpClient,
		}),
		Server: wechatmp.Server{
			AppID: appID, Token: cfg.String(WechatMPKeyToken), EncodingAESKey: strings.TrimSpace(cfg.String(WechatMPKeyAESKey)),
		},
		BindReply:   bind,
		FollowReply: strings.TrimSpace(cfg.String(WechatMPKeyFollowReply)),
	}
}

type wechatMPProvider struct {
	client *wechatmp.Client
}

// ErrWechatMPNotFollowing 是用户已取关服务号：消息发不出去，换哪家供应商都一样。
var ErrWechatMPNotFollowing = errors.New("wechat_mp: 用户没有关注服务号")

// Send 发模板消息。d.To 是调用方已经换好的 openid。
func (p *wechatMPProvider) Send(ctx context.Context, d Delivery) error {
	err := p.client.SendTemplate(ctx, wechatmp.TemplateMessage{
		ToUser: d.To, TemplateID: d.ProviderTemplateID, URL: d.Template.Content.Link, Data: fitTemplateValues(d.Params),
	})
	if wechatmp.IsNotFollowing(err) {
		return ErrWechatMPNotFollowing
	}
	return err
}

// 模板字段按名字前缀限长，超长时微信整条拒发（47003）。设备名、用户名这类业务方没法预知长度的值，
// 截断后照样发出去，比整条通知丢掉有用。只截长度明确、截了不改变含义的两类。
var templateValueLimits = map[string]int{
	"thing":            20,
	"character_string": 32,
}

func fitTemplateValues(params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[k] = v
		for prefix, limit := range templateValueLimits {
			if strings.HasPrefix(k, prefix) && isFieldIndex(k[len(prefix):]) {
				out[k] = truncateRunes(v, limit)
			}
		}
	}
	return out
}

// isFieldIndex 判断字段名前缀之后是不是纯数字：thing1 是 thing 类型，things 不是。
func isFieldIndex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// HTTPClient 是通知模块对外的 HTTP 客户端（开发环境走出口代理），服务号订阅调接口也用它。
func HTTPClient() *http.Client { return httpClient }
