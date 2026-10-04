package notify

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/basicfu/fp/internal/domain"
)

// logSpec 是开发/测试用的假供应商：只打日志、不真发，让整条链路在本地不依赖真实账号就能跑通。
// DevOnly：prod 环境不可建，因为它会把变量取值打进日志。
var logSpec = TypeSpec{
	Type:    "log",
	DevOnly: true,
	ConfigSchema: []domain.Field{
		{Key: "channel", Label: "模拟的渠道", Type: domain.FieldTypeString, Required: true, Help: "sms / email / telegram / wecom_bot / dingtalk_bot / webhook"},
	},
	New: func(cfg Config) (Provider, error) {
		ch := domain.NotifyChannel(cfg.String("channel"))
		if !ch.Valid() {
			return nil, fmt.Errorf("channel 必须是 sms / email / telegram / wecom_bot / dingtalk_bot / webhook 之一")
		}
		return &logProvider{channel: ch}, nil
	},
}

type logProvider struct{ channel domain.NotifyChannel }

func (p *logProvider) Send(_ context.Context, d Delivery) error {
	slog.Info("notify[log]: 只记录、不真发（仅开发/测试环境可用）",
		"channel", p.channel, "code", d.Template.Code, "to", d.To,
		"providerTemplateId", d.ProviderTemplateID, "params", d.Params,
		"subject", d.Rendered.Subject, "body", d.Rendered.Body)
	return nil
}
