package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// SessionPolicy 是一个应用的会话参数：设计文档 4.4 的会话三参数
// （idle_timeout / max_lifetime / rotate_interval）加上 4.5 的缓存与降频窗口。
type SessionPolicy struct {
	// IdleTimeoutSeconds 多久不访问就失效，即滑动过期的窗口。
	IdleTimeoutSeconds int32
	// IdleTimeoutMobileSeconds 移动端的空闲超时。为 0 时回落到 IdleTimeoutSeconds。
	IdleTimeoutMobileSeconds int32
	// MaxLifetimeSeconds 从首次认证起最长活多久，到期必须重新认证。
	// 轮换不能替代它：轮换只换 token 的值，会话仍是同一个。
	MaxLifetimeSeconds int32
	// RotateIntervalSeconds 多久换一次 token 值。
	RotateIntervalSeconds int32
	// ExtendIntervalSeconds 延期写的时间窗降频间隔（设计文档 4.5.2）。
	ExtendIntervalSeconds int32
	// TokenCacheTTLSeconds SDK 侧缓存校验结果的上限秒数。
	// fp 下发的 cache_ttl = min(本值, token 剩余有效期)。
	TokenCacheTTLSeconds int32
}

// DefaultSessionPolicy 返回新建应用的默认会话策略。
func DefaultSessionPolicy() SessionPolicy {
	return SessionPolicy{
		IdleTimeoutSeconds:       7 * 24 * 60 * 60,
		IdleTimeoutMobileSeconds: 30 * 24 * 60 * 60,
		MaxLifetimeSeconds:       90 * 24 * 60 * 60,
		RotateIntervalSeconds:    24 * 60 * 60,
		ExtendIntervalSeconds:    10 * 60,
		TokenCacheTTLSeconds:     30,
	}
}

// Validate 校验策略的内部一致性。
func (p SessionPolicy) Validate() error {
	if p.IdleTimeoutSeconds <= 0 {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "idle_timeout 必须大于 0")
	}
	if p.IdleTimeoutMobileSeconds < 0 {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "idle_timeout_mobile 不能为负")
	}
	if p.MaxLifetimeSeconds <= 0 {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "max_lifetime 必须大于 0")
	}
	if p.RotateIntervalSeconds <= 0 {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "rotate_interval 必须大于 0")
	}
	if p.ExtendIntervalSeconds <= 0 {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "extend_interval 必须大于 0")
	}
	if p.TokenCacheTTLSeconds <= 0 {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "token_cache_ttl 必须大于 0")
	}
	if p.RotateIntervalSeconds > p.MaxLifetimeSeconds {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "rotate_interval 不能大于 max_lifetime")
	}
	// 降频间隔必须远小于空闲超时，否则用户会因为"少延"而意外掉线。
	if p.ExtendIntervalSeconds >= p.IdleTimeoutSeconds {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "extend_interval 必须小于 idle_timeout")
	}
	// 缓存窗口大于空闲超时意味着 token 过期后仍可能被 SDK 放行。
	if p.TokenCacheTTLSeconds > p.IdleTimeoutSeconds {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "token_cache_ttl 不能大于 idle_timeout")
	}
	return nil
}

// IdleTimeoutFor 返回该端类型适用的空闲超时。
func (p SessionPolicy) IdleTimeoutFor(mobile bool) time.Duration {
	if mobile && p.IdleTimeoutMobileSeconds > 0 {
		return time.Duration(p.IdleTimeoutMobileSeconds) * time.Second
	}
	return time.Duration(p.IdleTimeoutSeconds) * time.Second
}

// MaxLifetime 返回绝对上限时长。
func (p SessionPolicy) MaxLifetime() time.Duration {
	return time.Duration(p.MaxLifetimeSeconds) * time.Second
}

// RotateInterval 返回 token 轮换间隔。
func (p SessionPolicy) RotateInterval() time.Duration {
	return time.Duration(p.RotateIntervalSeconds) * time.Second
}

// ExtendInterval 返回延期写的降频间隔。
func (p SessionPolicy) ExtendInterval() time.Duration {
	return time.Duration(p.ExtendIntervalSeconds) * time.Second
}

// 应用状态。
const (
	ApplicationStatusActive   = "ACTIVE"
	ApplicationStatusDisabled = "DISABLED"
)

// Application 是一个接入端。多个应用挂在同一个 fp 部署下即共享同一套用户体系。
type Application struct {
	ID           uuid.UUID
	Name         string
	Slug         string
	AppID        string
	Status       string
	Session      SessionPolicy
	CookieDomain string
	// DefaultRoleKey 是该应用的默认角色。有效角色 = 用户的全局角色 ∪ 它。
	// 空串表示不设默认角色。
	DefaultRoleKey string
	RedirectURIs   []string // OIDC 预留
	GrantTypes     []string // OIDC 预留
	// IM 是该应用在 fp-im 里的接入配置。默认关。
	IM        IMConfig
	CreatedAt int64
	UpdatedAt int64
}

// ApplicationConnector 是某个应用对某种登录方式的启用状态与配置。
type ApplicationConnector struct {
	Type    string
	Enabled bool
	Config  map[string]any
}

// IM 连接策略。取值必须与 internal/im/model.Policy 逐字相同——那两组常量
// 分处 internal/domain 与 internal/im/model（后者不得依赖前者），任何一边
// 单独看都只是孤立的字符串字面量，改错了 go build/vet/全量测试照样全绿。
// 配对关系由 internal/integration 的测试守护。
const (
	IMConnPolicyReplace = "replace" // 新连接顶掉同 subject 的旧连接
	IMConnPolicyReject  = "reject"  // 已有连接时拒绝新连接
	IMConnPolicyLimit   = "limit"   // 最多 ConnLimit 条，超出拒新
)

// IMBizAuth 是业务方自有认证的回调配置。整组为 nil 表示这个应用不支持
// 业务方令牌。
//
// 用嵌套（指针）而不是铺平成三个字段：铺平之后"支不支持"就得靠"地址是不是
// 空串"这种间接判断。落库时对应可空的 jsonb 列，SQL 的 NULL 是同一个语义。
type IMBizAuth struct {
	VerifyURL string `json:"verify_url"`
	// TimeoutMs 是整个回调请求的超时。用毫秒而不是 time.Duration：它要原样
	// 落进 jsonb，而 time.Duration 的 JSON 表示是纳秒整数，既难读又容易错
	// 一个数量级。
	TimeoutMs int32 `json:"timeout_ms"`
	CacheSize int32 `json:"cache_size"`
}

// IMConfig 是一个应用在 fp-im 里的接入配置。
type IMConfig struct {
	// Enabled 关着时这个应用连不上 fp-im：业务 server 接不进来，client
	// 握手也拒。默认 false。
	Enabled     bool       `json:"enabled"`
	ConnPolicy  string     `json:"conn_policy"`
	ConnLimit   int32      `json:"conn_limit"`
	AllowGuest  bool       `json:"allow_guest"`
	GuestIPRate int32      `json:"guest_ip_rate"`
	BizAuth     *IMBizAuth `json:"biz_auth,omitempty"`
}

// DefaultIMConfig 返回新建应用的默认 IM 配置。
func DefaultIMConfig() IMConfig {
	return IMConfig{
		Enabled:     false,
		ConnPolicy:  IMConnPolicyReplace,
		ConnLimit:   5,
		AllowGuest:  false,
		GuestIPRate: 20,
	}
}

// Validate 校验 IM 配置。
//
// Enabled 为 false 时直接放行：还没打开就先拦人，等于逼人一次填全才能存
// 草稿。真正会被 fp-im 读到的只有打开之后的配置。
func (c IMConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	switch c.ConnPolicy {
	case IMConnPolicyReplace, IMConnPolicyReject:
	case IMConnPolicyLimit:
		if c.ConnLimit < 1 {
			return Failf(ErrInvalidArgument, CodeInvalidArgument, "策略为 limit 时 conn_limit 必须 >= 1")
		}
	default:
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "未知的 conn_policy %q", c.ConnPolicy)
	}
	if c.AllowGuest && c.GuestIPRate < 1 {
		return Failf(ErrInvalidArgument, CodeInvalidArgument, "允许访客时 guest_ip_rate 必须 >= 1")
	}
	if c.BizAuth != nil {
		if c.BizAuth.VerifyURL == "" {
			return Failf(ErrInvalidArgument, CodeInvalidArgument, "配了 biz_auth 但缺 verify_url")
		}
		// 必须 HTTPS：client 的令牌明文走在请求体里，明文传输等于把所有
		// 业务方令牌交给中间人。
		if !strings.HasPrefix(c.BizAuth.VerifyURL, "https://") {
			return Failf(ErrInvalidArgument, CodeInvalidArgument, "biz_auth.verify_url 必须是 https")
		}
		if c.BizAuth.TimeoutMs <= 0 {
			return Failf(ErrInvalidArgument, CodeInvalidArgument, "biz_auth.timeout_ms 必须大于 0")
		}
		if c.BizAuth.CacheSize <= 0 {
			return Failf(ErrInvalidArgument, CodeInvalidArgument, "biz_auth.cache_size 必须大于 0")
		}
	}
	return nil
}
