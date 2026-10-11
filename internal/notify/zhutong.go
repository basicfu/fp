package notify

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

// 助通按账号分节点，默认给上海节点，别的节点在实例配置里改。
const (
	defaultZhutongEndpoint = "https://api-shss.zthysms.com"
	zhutongSendPath        = "/v2/sendSmsTp"
)

var zhutongPhoneRegex = regexp.MustCompile(`^\d{5,20}$`)

var zhutongSpec = TypeSpec{
	Type:    "zhutong",
	Channel: domain.NotifyChannelSMS,
	ConfigSchema: []domain.Field{
		{Key: "username", Label: "账号", Type: domain.FieldTypeString, Required: true},
		{Key: "password", Label: "密码", Type: domain.FieldTypeSecret, Required: true},
		{Key: "signature", Label: "短信签名", Type: domain.FieldTypeString, Required: true, Help: "要带上【】，如【示例签名】"},
		{Key: "endpoint", Label: "接入点", Type: domain.FieldTypeString, Default: defaultZhutongEndpoint},
	},
	New: newZhutong,
}

type zhutongProvider struct {
	endpoint, username, password, signature string
	now                                     func() time.Time
}

func newZhutong(cfg Config) (Provider, error) {
	p := &zhutongProvider{
		endpoint:  strings.TrimRight(cfg.String("endpoint"), "/"),
		username:  cfg.String("username"),
		password:  cfg.String("password"),
		signature: cfg.String("signature"),
		now:       time.Now,
	}
	if p.username == "" || p.password == "" || p.signature == "" {
		return nil, errors.New("助通短信需要 username、password 与 signature")
	}
	if p.endpoint == "" {
		p.endpoint = defaultZhutongEndpoint
	}
	return p, nil
}

// zhutongSign 是助通的鉴权串 md5(md5(password)+tKey)：密码本身不上线，tKey 是秒级时间戳。
func zhutongSign(password string, tKey int64) string {
	inner := md5.Sum([]byte(password))
	outer := md5.Sum([]byte(hex.EncodeToString(inner[:]) + strconv.FormatInt(tKey, 10)))
	return hex.EncodeToString(outer[:])
}

func (p *zhutongProvider) Send(ctx context.Context, d Delivery) error {
	if d.ProviderTemplateID == "" {
		return errors.New("缺少助通短信模板 ID")
	}
	if !zhutongPhoneRegex.MatchString(d.To) {
		return errors.New("收件人手机号不合法")
	}
	// nil map 会序列化成 null，助通要的是对象。
	params := d.Params
	if params == nil {
		params = map[string]string{}
	}
	tKey := p.now().Unix()
	status, body, err := postJSON(ctx, p.endpoint+zhutongSendPath, map[string]any{
		"username":  p.username,
		"password":  zhutongSign(p.password, tKey),
		"tKey":      tKey,
		"signature": p.signature,
		"tpId":      d.ProviderTemplateID,
		"records":   []map[string]any{{"mobile": d.To, "tpContent": params}},
	})
	if err != nil {
		return fmt.Errorf("zhutong: %w", err)
	}
	var out struct {
		// RawMessage：code 在文档里是数字，实际回包也见过字符串；缺失时不能按 0 值当成功。
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("zhutong: HTTP %d, 响应不是合法 JSON", status)
	}
	code := strings.Trim(string(out.Code), `"`)
	if code == "" || code == "null" {
		return fmt.Errorf("zhutong: HTTP %d, 响应缺少 code", status)
	}
	if status != http.StatusOK || code != "200" {
		return fmt.Errorf("zhutong: HTTP %d code=%s %s", status, code, out.Msg)
	}
	return nil
}
