package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Policy 是 app 级的连接策略。
type Policy string

const (
	PolicyReplace Policy = "replace" // 新连接顶掉同 subject 的旧连接
	PolicyReject  Policy = "reject"  // 已有连接时拒绝新连接
	PolicyLimit   Policy = "limit"   // 最多 ConnLimit 条，超出拒新
	// PolicyNone 只在 Redis 重连后重登记时传给脚本：清残留、HSET、HEXPIRE，不判定。
	// 它不是 app 可配置的值。
	PolicyNone Policy = "none"
)

// Duration 是 JSON 里写成 "2s" 这种人类可读时长的配置项。
// 不直接用 time.Duration：它的 JSON 表示是纳秒整数，配置文件里写 2000000000
// 既难读又容易错一个数量级。
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		// 明确拒绝裸数字：写 2 是 2 秒还是 2 纳秒，没人说得清，
		// 与其猜一个不如让配置加载直接失败。
		return fmt.Errorf("model: 时长必须是带单位的字符串，例如 \"2s\"：%w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("model: 无法解析时长 %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// BizAuth 是业务方自有认证的回调配置。整组为空表示这个 app 不支持业务方令牌。
// 用嵌套而不是铺平成三个字段，是为了让"支不支持"能整块判断，
// 铺平之后就得靠"地址是不是空串"这种间接判断。
type BizAuth struct {
	VerifyURL string   `json:"verify_url"`
	Timeout   Duration `json:"timeout"`
	CacheSize int      `json:"cache_size"`
}

// AppConfig 是一个接入应用在 fp-im 里的全部配置。
// AppID/AppSecret 同时用于 fp-im 调 fp 验 token，和校验业务 server 连 fp-im 的凭据。
type AppConfig struct {
	AppID       string   `json:"app_id"`
	AppSecret   string   `json:"app_secret"`
	ConnPolicy  Policy   `json:"conn_policy"`
	ConnLimit   int      `json:"conn_limit"`
	AllowGuest  bool     `json:"allow_guest"`
	GuestIPRate int      `json:"guest_ip_rate"`
	BizAuth     *BizAuth `json:"biz_auth,omitempty"`
}

func (c AppConfig) Validate() error {
	if c.AppID == "" || c.AppSecret == "" {
		return fmt.Errorf("model: app %q 缺少 app_id 或 app_secret", c.AppID)
	}
	switch c.ConnPolicy {
	case PolicyReplace, PolicyReject:
	case PolicyLimit:
		if c.ConnLimit < 1 {
			return fmt.Errorf("model: app %q 策略为 limit 时 conn_limit 必须 >= 1", c.AppID)
		}
	default:
		return fmt.Errorf("model: app %q 未知 conn_policy %q", c.AppID, c.ConnPolicy)
	}
	if c.AllowGuest && c.GuestIPRate < 1 {
		return fmt.Errorf("model: app %q 允许访客时 guest_ip_rate 必须 >= 1", c.AppID)
	}
	if c.BizAuth != nil {
		if c.BizAuth.VerifyURL == "" {
			return fmt.Errorf("model: app %q 配了 biz_auth 但缺 verify_url", c.AppID)
		}
		// 必须 HTTPS：client 的令牌明文走在请求体里，明文传输等于把
		// 所有业务方令牌交给中间人。
		if !strings.HasPrefix(c.BizAuth.VerifyURL, "https://") {
			return fmt.Errorf("model: app %q 的 verify_url 必须是 https", c.AppID)
		}
		if c.BizAuth.Timeout <= 0 {
			return fmt.Errorf("model: app %q 的 biz_auth.timeout 必须大于 0", c.AppID)
		}
		if c.BizAuth.CacheSize <= 0 {
			return fmt.Errorf("model: app %q 的 biz_auth.cache_size 必须大于 0", c.AppID)
		}
	}
	return nil
}
