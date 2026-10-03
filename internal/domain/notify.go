package domain

import (
	"time"

	"github.com/google/uuid"
)

// NotifyChannel 是通知渠道。
type NotifyChannel string

const (
	NotifyChannelSMS         NotifyChannel = "sms"
	NotifyChannelEmail       NotifyChannel = "email"
	NotifyChannelTelegram    NotifyChannel = "telegram"
	NotifyChannelWecomBot    NotifyChannel = "wecom_bot"
	NotifyChannelDingtalkBot NotifyChannel = "dingtalk_bot"
	NotifyChannelWebhook     NotifyChannel = "webhook"
)

// NotifyMode 区分模板内容由谁渲染。
type NotifyMode string

const (
	// NotifyModeVendor 是供应商侧已审核的模板：fp 只存原文供核对，发送时传
	// 供应商模板 ID + 变量，由供应商替换。
	NotifyModeVendor NotifyMode = "vendor"
	// NotifyModeCustom 是 fp 自己渲染后发送的模板。
	NotifyModeCustom NotifyMode = "custom"
)

// Valid 报告 c 是否是已知渠道。
func (c NotifyChannel) Valid() bool {
	switch c {
	case NotifyChannelSMS, NotifyChannelEmail, NotifyChannelTelegram,
		NotifyChannelWecomBot, NotifyChannelDingtalkBot, NotifyChannelWebhook:
		return true
	}
	return false
}

// AllowsMode 报告该渠道是否允许某种模板模式：短信只有供应商侧审核过的模板；
// 邮件两种都行（云厂商邮件要模板、自建 SMTP 不要）；其余渠道没有供应商侧模板。
func (c NotifyChannel) AllowsMode(m NotifyMode) bool {
	switch c {
	case NotifyChannelSMS:
		return m == NotifyModeVendor
	case NotifyChannelEmail:
		return m == NotifyModeVendor || m == NotifyModeCustom
	case NotifyChannelTelegram, NotifyChannelWecomBot, NotifyChannelDingtalkBot, NotifyChannelWebhook:
		return m == NotifyModeCustom
	}
	return false
}

// NeedsRecipient 报告该渠道是否发给某个具体的人（手机号/邮箱）。
// IM 与 webhook 发给供应商实例里配置好的固定目标，没有收件人。
func (c NotifyChannel) NeedsRecipient() bool {
	return c == NotifyChannelSMS || c == NotifyChannelEmail
}

// NotifyContent 是模板的内容部分，对应 notify_template.template 这一列的 JSON。
// 各渠道只用其中一部分字段，校验见 notify.ValidateContent。
type NotifyContent struct {
	Subject             string   `json:"subject,omitempty"`
	Content             string   `json:"content"`
	ContentType         string   `json:"contentType,omitempty"`
	Method              string   `json:"method,omitempty"`
	Variables           []string `json:"variables"`
	MentionedList       []string `json:"mentionedList,omitempty"`
	MentionedMobileList []string `json:"mentionedMobileList,omitempty"`
	AtMobiles           []string `json:"atMobiles,omitempty"`
	IsAtAll             bool     `json:"isAtAll,omitempty"`
}

// NotifyProvider 是一个供应商实例：一份凭据。
type NotifyProvider struct {
	ID          uuid.UUID
	Type        string
	Description string
	Enabled     bool
	Config      map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NotifyTemplate 是业务代码按 code 引用的一条通知。
type NotifyTemplate struct {
	Code        string
	Channel     NotifyChannel
	Mode        NotifyMode
	Content     NotifyContent
	Description string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NotifyTemplateProvider 是模板与供应商实例的关联。
type NotifyTemplateProvider struct {
	Code               string
	ProviderID         uuid.UUID
	ProviderTemplateID string
	Enabled            bool
	Priority           int
}

// NotifyLog 是一次发送尝试的记录。绝不包含变量取值与渲染后的内容。
type NotifyLog struct {
	ID         uuid.UUID
	Channel    string
	Target     string
	Code       string
	Provider   string
	ProviderID *uuid.UUID
	AppID      string
	Success    bool
	Error      string
	CreatedAt  time.Time
}
