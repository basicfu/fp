package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/basicfu/fp/internal/domain"
)

const wecomBase = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send"

var wecomSpec = TypeSpec{
	Type:    "wecom_bot",
	Channel: domain.NotifyChannelWecomBot,
	ConfigSchema: []domain.Field{
		{Key: "key", Label: "机器人 Key", Type: domain.FieldTypeSecret, Required: true, Help: "Webhook 地址里 key= 后面的那一段"},
	},
	New: func(cfg Config) (Provider, error) {
		return &wecomProvider{baseURL: wecomBase, key: cfg.String("key")}, nil
	},
}

type wecomProvider struct{ baseURL, key string }

func (p *wecomProvider) Send(ctx context.Context, d Delivery) error {
	text := map[string]any{"content": d.Rendered.Body}
	if l := d.Template.Content.MentionedList; len(l) > 0 {
		text["mentioned_list"] = l
	}
	if l := d.Template.Content.MentionedMobileList; len(l) > 0 {
		text["mentioned_mobile_list"] = l
	}
	status, body, err := postJSON(ctx, p.baseURL+"?key="+url.QueryEscape(p.key),
		map[string]any{"msgtype": "text", "text": text})
	if err != nil {
		return fmt.Errorf("wecom_bot: %w", err)
	}
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("wecom_bot: HTTP %d, 响应不是合法 JSON", status)
	}
	if status != http.StatusOK || out.ErrCode != 0 {
		return fmt.Errorf("wecom_bot: HTTP %d errcode=%d %s", status, out.ErrCode, out.ErrMsg)
	}
	return nil
}
