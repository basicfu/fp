package model

import "fmt"

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

// AppConfig 是一个接入应用在 fp-im 里的全部配置。
// AppID/AppSecret 同时用于 fp-im 调 fp 验 token，和校验业务 server 连 fp-im 的凭据。
type AppConfig struct {
	AppID       string `json:"app_id"`
	AppSecret   string `json:"app_secret"`
	ConnPolicy  Policy `json:"conn_policy"`
	ConnLimit   int    `json:"conn_limit"`
	AllowGuest  bool   `json:"allow_guest"`
	GuestIPRate int    `json:"guest_ip_rate"`
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
	return nil
}
