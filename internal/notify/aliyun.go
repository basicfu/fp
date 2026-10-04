package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	openapi "github.com/alibabacloud-go/darabonba-openapi/client"
	dysmsapi "github.com/alibabacloud-go/dysmsapi-20170525/v2/client"
	"github.com/alibabacloud-go/tea/tea"

	"github.com/basicfu/fp/internal/domain"
)

const defaultAliyunEndpoint = "dysmsapi.aliyuncs.com"

// 阿里云 SDK 的超时（毫秒）。**必须显式设置**：SDK 默认不带超时，SendSms 又拿不到 ctx，
// 一个吊死的接入点会把调用它的 goroutine 永久挂住，降级到下一家供应商也就无从谈起。
const (
	aliyunConnectTimeoutMS = 5_000
	aliyunReadTimeoutMS    = 10_000
)

var aliyunPhoneRegex = regexp.MustCompile(`^\+?\d{5,20}$`)

var aliyunSpec = TypeSpec{
	Type:    "aliyun",
	Channel: domain.NotifyChannelSMS,
	ConfigSchema: []domain.Field{
		{Key: "accessKeyId", Label: "AccessKey ID", Type: domain.FieldTypeString, Required: true},
		{Key: "accessKeySecret", Label: "AccessKey Secret", Type: domain.FieldTypeSecret, Required: true},
		{Key: "signName", Label: "短信签名", Type: domain.FieldTypeString, Required: true},
		{Key: "endpoint", Label: "接入点", Type: domain.FieldTypeString, Default: defaultAliyunEndpoint},
	},
	New: newAliyun,
}

type aliyunConfig struct {
	accessKeyID, accessKeySecret, signName, endpoint string
}

type aliyunProvider struct {
	cfg    aliyunConfig
	client *dysmsapi.Client
}

func newAliyun(cfg Config) (Provider, error) {
	c := aliyunConfig{
		accessKeyID:     cfg.String("accessKeyId"),
		accessKeySecret: cfg.String("accessKeySecret"),
		signName:        cfg.String("signName"),
		endpoint:        cfg.String("endpoint"),
	}
	if c.accessKeyID == "" || c.accessKeySecret == "" || c.signName == "" {
		return nil, errors.New("阿里云短信需要 accessKeyId、accessKeySecret 与 signName")
	}
	if c.endpoint == "" {
		c.endpoint = defaultAliyunEndpoint
	}
	client, err := dysmsapi.NewClient(&openapi.Config{
		AccessKeyId:     tea.String(c.accessKeyID),
		AccessKeySecret: tea.String(c.accessKeySecret),
		Endpoint:        tea.String(c.endpoint),
		ConnectTimeout:  tea.Int(aliyunConnectTimeoutMS),
		ReadTimeout:     tea.Int(aliyunReadTimeoutMS),
	})
	if err != nil {
		return nil, fmt.Errorf("创建阿里云短信客户端: %w", err)
	}
	return &aliyunProvider{cfg: c, client: client}, nil
}

// Send 实现 Provider。ctx 被刻意丢弃：dysmsapi 的 SendSms 不接受 context，没有任何办法把
// 取消或截止时间传下去；真正兜底的是构造客户端时设的两个超时，它们保证这次调用最坏也会在
// 有限时间内返回。
func (p *aliyunProvider) Send(_ context.Context, d Delivery) error {
	req, err := buildAliyunRequest(p.cfg, d)
	if err != nil {
		return err
	}
	resp, err := p.client.SendSms(req)
	if err != nil {
		// 阿里云 SDK 的 *url.Error 包含请求 URL（带上了手机号和参数），不能暴露给日志。
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("调用阿里云短信接口: %w", err)
	}
	if resp.Body == nil || resp.Body.Code == nil {
		return errors.New("阿里云短信返回体为空")
	}
	if code := tea.StringValue(resp.Body.Code); code != "OK" {
		return fmt.Errorf("阿里云短信失败 code=%s msg=%s", code, tea.StringValue(resp.Body.Message))
	}
	return nil
}

// buildAliyunRequest 把一次 Delivery 翻译成 SendSms 请求。抽成纯函数是因为 SDK 的网络调用
// 测不了，而这段翻译恰恰是最容易出错、最需要锁死的部分。
func buildAliyunRequest(cfg aliyunConfig, d Delivery) (*dysmsapi.SendSmsRequest, error) {
	if d.ProviderTemplateID == "" {
		return nil, errors.New("缺少阿里云短信模板 ID")
	}
	if !aliyunPhoneRegex.MatchString(d.To) {
		return nil, errors.New("收件人手机号不合法")
	}
	// encoding/json 对 map 按键排序：同一条消息每次生成的请求体完全一致，便于排障。
	param := []byte("{}")
	if len(d.Params) > 0 {
		var err error
		if param, err = json.Marshal(d.Params); err != nil {
			return nil, fmt.Errorf("序列化模板变量: %w", err)
		}
	}
	return &dysmsapi.SendSmsRequest{
		PhoneNumbers:  tea.String(d.To),
		SignName:      tea.String(cfg.signName),
		TemplateCode:  tea.String(d.ProviderTemplateID),
		TemplateParam: tea.String(string(param)),
	}, nil
}
