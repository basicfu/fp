package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

const dingtalkBase = "https://oapi.dingtalk.com/robot/send"

var dingtalkSpec = TypeSpec{
	Type:    "dingtalk_bot",
	Channel: domain.NotifyChannelDingtalkBot,
	ConfigSchema: []domain.Field{
		{Key: "accessToken", Label: "Access Token", Type: domain.FieldTypeSecret, Required: true, Help: "Webhook 地址里 access_token= 后面的那一段"},
		{Key: "secret", Label: "加签密钥", Type: domain.FieldTypeSecret, Help: "机器人安全设置选了「加签」才填，以 SEC 开头"},
	},
	New: func(cfg Config) (Provider, error) {
		return &dingtalkProvider{baseURL: dingtalkBase, accessToken: cfg.String("accessToken"), secret: cfg.String("secret"), now: time.Now}, nil
	},
}

type dingtalkProvider struct {
	baseURL, accessToken, secret string
	now                          func() time.Time
}

// dingtalkSign 是钉钉自定义机器人的加签：base64(HMAC-SHA256(key=secret, msg=毫秒时间戳 + "\n" + secret))。
func dingtalkSign(secret string, tsMillis int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(tsMillis, 10) + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (p *dingtalkProvider) Send(ctx context.Context, d Delivery) error {
	q := url.Values{"access_token": {p.accessToken}}
	if p.secret != "" {
		ts := p.now().UnixMilli()
		q.Set("timestamp", strconv.FormatInt(ts, 10))
		q.Set("sign", dingtalkSign(p.secret, ts))
	}
	mobiles := d.Template.Content.AtMobiles
	if mobiles == nil {
		mobiles = []string{}
	}
	status, body, err := postJSON(ctx, p.baseURL+"?"+q.Encode(), map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": d.Rendered.Body},
		"at":      map[string]any{"atMobiles": mobiles, "isAtAll": d.Template.Content.IsAtAll},
	})
	if err != nil {
		return fmt.Errorf("dingtalk_bot: %w", err)
	}
	var out struct {
		// 指针：{}、null、WAF 拦截页的 JSON 解析下来都没有 errcode，按 0 算就成了"发送成功"。
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("dingtalk_bot: HTTP %d, 响应不是合法 JSON", status)
	}
	if out.ErrCode == nil {
		return fmt.Errorf("dingtalk_bot: HTTP %d, 响应缺少 errcode", status)
	}
	if status != http.StatusOK || *out.ErrCode != 0 {
		return fmt.Errorf("dingtalk_bot: HTTP %d errcode=%d %s", status, *out.ErrCode, out.ErrMsg)
	}
	return nil
}
