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

// 阿里云 SDK 的超时。**必须显式设置**：SDK 默认不带超时，SendSms 又拿不到 ctx，
// 一个吊死的接入点会把调用它的 goroutine 永久挂住，降级到下一家供应商也就无从谈起。
//
// 两个值单位不同：SDK 默认的拨号器把 ConnectTimeout 按秒解释（只有走 SOCKS5 代理时才按毫秒，这里不用）；
// ReadTimeout 按毫秒设成 http.Client.Timeout，覆盖拨号、TLS 与读响应，是整次调用真正的上限。
const (
	aliyunConnectTimeoutSec = 5
	aliyunReadTimeoutMS     = 10_000
)

// PhoneNumbers 是逗号分隔的号码列表：不校验成单个号码，业务方传来的一个"收件人"就能扇出成多条短信。
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
		ConnectTimeout:  tea.Int(aliyunConnectTimeoutSec),
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
//
// 返回的错误进 notify_log（控制台可见）与服务端日志，所以失败时只带状态码、错误码与请求 ID，
// 不带回包里的 Message：它不保证不回显请求内容。
func (p *aliyunProvider) Send(_ context.Context, d Delivery) error {
	req, err := buildAliyunRequest(p.cfg, d)
	if err != nil {
		return err
	}
	resp, err := p.sendSms(req)
	if err != nil {
		var se *tea.SDKError
		if errors.As(err, &se) {
			// 4xx/5xx 的 Message/Data 是阿里云回显的请求内容（签名不符时含待签串：AK、手机号、验证码），只留状态码与错误码。
			return fmt.Errorf("调用阿里云短信接口: HTTP %d code=%s%s",
				tea.IntValue(se.StatusCode), tea.StringValue(se.Code), aliyunRequestID(aliyunErrorRequestID(se)))
		}
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
		return fmt.Errorf("阿里云短信失败 code=%s%s", code, aliyunRequestID(tea.StringValue(resp.Body.RequestId)))
	}
	return nil
}

// sendSms 把 SDK 里的 panic 变成错误：响应体不是 JSON 对象（空 body、null、数组）时 SDK 直接 panic，
// 而 gRPC 不替 handler 兜底，一条坏响应就能带走整个进程。panic 的值里有响应体原文，不往错误里放。
func (p *aliyunProvider) sendSms(req *dysmsapi.SendSmsRequest) (resp *dysmsapi.SendSmsResponse, err error) {
	defer func() {
		if recover() != nil {
			resp, err = nil, errors.New("响应无法解析")
		}
	}()
	return p.client.SendSms(req)
}

// aliyunRequestIDRE 限定请求 ID 的形状：它来自回包，不是这个形状就不往错误里放。
var aliyunRequestIDRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// aliyunRequestID 返回附在错误后面的请求 ID（提工单要用），不合形状时返回空串。
func aliyunRequestID(id string) string {
	if !aliyunRequestIDRE.MatchString(id) {
		return ""
	}
	return " request_id=" + id
}

// aliyunErrorRequestID 从 4xx/5xx 错误的 Data（阿里云回包的 JSON）里只取出 RequestId。
func aliyunErrorRequestID(se *tea.SDKError) string {
	var body struct{ RequestId string }
	_ = json.Unmarshal([]byte(tea.StringValue(se.Data)), &body)
	return body.RequestId
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
