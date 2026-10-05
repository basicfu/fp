package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/basicfu/fp/internal/domain"
)

var webhookSpec = TypeSpec{
	Type:    "webhook",
	Channel: domain.NotifyChannelWebhook,
	ConfigSchema: []domain.Field{
		{Key: "url", Label: "URL", Type: domain.FieldTypeString, Required: true, Help: "http:// 或 https:// 开头，不能带 #；GET 模板渲染出的 query 会追加在它后面"},
		{Key: "secret", Label: "签名密钥", Type: domain.FieldTypeSecret, Help: "填了就在请求头 X-Fp-Signature 里带 sha256=HMAC-SHA256(secret, 被签内容)：POST 签 body，GET 只签模板渲染出的那段 query，不含 url 里原有的参数"},
	},
	New: func(cfg Config) (Provider, error) {
		raw := cfg.String("url")
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("url 必须是 http:// 或 https:// 开头的完整地址")
		}
		// GET 渲染出的 query 会被拼进 # 后面的片段，接收端拿不到参数却照样回 2xx。
		// 按字符查：只有 # 没有片段时 u.Fragment 也是空串。
		if strings.Contains(raw, "#") {
			return nil, errors.New("url 不能带 #")
		}
		return &webhookProvider{url: raw, secret: cfg.String("secret")}, nil
	},
}

type webhookProvider struct{ url, secret string }

// appendQuery 把渲染出的 query 接到 base 后面：base 已经带 ? 就用 &，以 ? 或 & 结尾就直接接。
func appendQuery(base, query string) string {
	switch {
	case query == "":
		return base
	case strings.HasSuffix(base, "?"), strings.HasSuffix(base, "&"):
		return base + query
	case strings.Contains(base, "?"):
		return base + "&" + query
	default:
		return base + "?" + query
	}
}

func (p *webhookProvider) Send(ctx context.Context, d Delivery) error {
	c := d.Template.Content
	var (
		req *http.Request
		err error
	)
	if c.Method == "GET" {
		req, err = newRequest(http.MethodGet, appendQuery(p.url, d.Rendered.Body), nil)
	} else {
		req, err = newRequest(http.MethodPost, p.url, strings.NewReader(d.Rendered.Body))
		if err == nil {
			ct := c.ContentType
			if ct == "" {
				ct = "application/json"
			}
			req.Header.Set("Content-Type", ct)
		}
	}
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	if p.secret != "" {
		mac := hmac.New(sha256.New, []byte(p.secret))
		mac.Write([]byte(d.Rendered.Body))
		req.Header.Set("X-Fp-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	status, _, err := doHTTP(ctx, req)
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("webhook: HTTP %d", status)
	}
	return nil
}
