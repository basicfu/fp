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
