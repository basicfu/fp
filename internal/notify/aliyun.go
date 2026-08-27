package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	openapi "github.com/alibabacloud-go/darabonba-openapi/client"
	dysmsapi "github.com/alibabacloud-go/dysmsapi-20170525/v2/client"
	"github.com/alibabacloud-go/tea/tea"

	"github.com/basicfu/fp/internal/domain"
)

// defaultAliyunEndpoint 是阿里云短信服务的默认接入点。
const defaultAliyunEndpoint = "dysmsapi.aliyuncs.com"

// 阿里云 SDK 的超时（毫秒）。**必须显式设置**：SDK 默认不带超时，
// 一个吊死的接入点会把调用它的那个 goroutine 永久挂住。而 Send 拿不到 ctx
// （见下），fp 自己的请求截止时间救不了它，Sender 的降级也就永远轮不到下一家——
// 一家供应商的网络故障会直接变成"所有短信都发不出去"。
const (
	aliyunConnectTimeoutMS = 5_000
	aliyunReadTimeoutMS    = 10_000
)

// AliyunConfig 是阿里云短信供应商的配置。
type AliyunConfig struct {
	AccessKeyID     string
	AccessKeySecret string
	Endpoint        string
	SignName        string
	// Templates 把 fp 内部模板 key 映射到阿里云模板 ID。
	// 这层映射让业务代码只认 "login_code"，换供应商时只改配置不改代码。
	Templates map[string]string
}

// AliyunSMS 是阿里云短信 Provider。
type AliyunSMS struct {
	cfg    AliyunConfig
	client *dysmsapi.Client
}

// NewAliyunSMS 构造阿里云短信 Provider。
func NewAliyunSMS(cfg AliyunConfig) (*AliyunSMS, error) {
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "阿里云短信缺少 accessKeyId 或 accessKeySecret")
	}
	if cfg.SignName == "" {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "阿里云短信缺少 signName")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultAliyunEndpoint
	}

	client, err := dysmsapi.NewClient(&openapi.Config{
		AccessKeyId:     tea.String(cfg.AccessKeyID),
		AccessKeySecret: tea.String(cfg.AccessKeySecret),
		Endpoint:        tea.String(cfg.Endpoint),
		ConnectTimeout:  tea.Int(aliyunConnectTimeoutMS),
		ReadTimeout:     tea.Int(aliyunReadTimeoutMS),
	})
	if err != nil {
		return nil, fmt.Errorf("notify: 创建阿里云短信客户端: %w", err)
	}
	return &AliyunSMS{cfg: cfg, client: client}, nil
}

// Name 实现 Provider。
func (p *AliyunSMS) Name() string { return "aliyun" }

// Channel 实现 Provider。
func (p *AliyunSMS) Channel() Channel { return ChannelSMS }

// ConfigSchema 实现 Provider。
func (p *AliyunSMS) ConfigSchema() []domain.Field {
	return []domain.Field{
		{Key: "accessKeyId", Label: "AccessKey ID", Type: domain.FieldTypeString, Required: true},
		{Key: "accessKeySecret", Label: "AccessKey Secret", Type: domain.FieldTypeSecret, Required: true},
		{Key: "signName", Label: "短信签名", Type: domain.FieldTypeString, Required: true},
		{Key: "endpoint", Label: "接入点", Type: domain.FieldTypeString, Default: defaultAliyunEndpoint},
	}
}

// Send 实现 Provider。
//
// ctx 被刻意丢弃：阿里云的 dysmsapi SDK（v2.0.16）的 SendSms 不接受
// context.Context，没有任何办法把调用方的取消或截止时间传下去。参数写成 `_`
// 是对这个限制的如实交代，不是疏忽——真正兜底的是构造客户端时设的
// aliyunConnectTimeoutMS / aliyunReadTimeoutMS，它们保证这次调用最坏也会在
// 有限时间内返回，从而让 Sender 有机会降级到下一家供应商。
func (p *AliyunSMS) Send(_ context.Context, msg Message) error {
	phone, signName, templateCode, templateParam, err := BuildAliyunRequest(p.cfg, msg)
	if err != nil {
		return err
	}

	resp, err := p.client.SendSms(&dysmsapi.SendSmsRequest{
		PhoneNumbers:  tea.String(phone),
		SignName:      tea.String(signName),
		TemplateCode:  tea.String(templateCode),
		TemplateParam: tea.String(templateParam),
	})
	if err != nil {
		return fmt.Errorf("notify: 调用阿里云短信接口: %w", err)
	}
	if resp.Body == nil || resp.Body.Code == nil {
		return fmt.Errorf("notify: 阿里云短信返回体为空")
	}
	if code := tea.StringValue(resp.Body.Code); code != "OK" {
		return fmt.Errorf("notify: 阿里云短信失败 code=%s msg=%s",
			code, tea.StringValue(resp.Body.Message))
	}
	return nil
}

// BuildAliyunRequest 把一条 Message 翻译成阿里云 SendSms 的四个参数。
//
// 抽成纯函数是为了让模板映射与参数序列化可以单测——SDK 的网络调用测不了，
// 但这段翻译逻辑恰恰是最容易出错、也最需要锁死的部分。
func BuildAliyunRequest(cfg AliyunConfig, msg Message) (phone, signName, templateCode, templateParam string, err error) {
	templateCode, ok := cfg.Templates[msg.Template]
	if !ok || templateCode == "" {
		return "", "", "", "", domain.Errorf(domain.ErrNotFound,
			"模板 %q 未映射到阿里云模板 ID", msg.Template)
	}

	// 按键名排序后序列化，保证同一条消息每次生成的请求体完全一致，便于排障与比对。
	keys := make([]string, 0, len(msg.Params))
	for k := range msg.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf := make([]byte, 0, 64)
	buf = append(buf, '{')
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(msg.Params[k])
		buf = append(buf, kb...)
		buf = append(buf, ':')
		buf = append(buf, vb...)
	}
	buf = append(buf, '}')

	return msg.To, cfg.SignName, templateCode, string(buf), nil
}
