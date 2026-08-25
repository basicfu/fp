package domain

import (
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
		return Errorf(ErrInvalidArgument, "idle_timeout 必须大于 0")
	}
	if p.IdleTimeoutMobileSeconds < 0 {
		return Errorf(ErrInvalidArgument, "idle_timeout_mobile 不能为负")
	}
	if p.MaxLifetimeSeconds <= 0 {
		return Errorf(ErrInvalidArgument, "max_lifetime 必须大于 0")
	}
	if p.RotateIntervalSeconds <= 0 {
		return Errorf(ErrInvalidArgument, "rotate_interval 必须大于 0")
	}
	if p.ExtendIntervalSeconds <= 0 {
		return Errorf(ErrInvalidArgument, "extend_interval 必须大于 0")
	}
	if p.TokenCacheTTLSeconds <= 0 {
		return Errorf(ErrInvalidArgument, "token_cache_ttl 必须大于 0")
	}
	if p.RotateIntervalSeconds > p.MaxLifetimeSeconds {
		return Errorf(ErrInvalidArgument, "rotate_interval 不能大于 max_lifetime")
	}
	// 降频间隔必须远小于空闲超时，否则用户会因为"少延"而意外掉线。
	if p.ExtendIntervalSeconds >= p.IdleTimeoutSeconds {
		return Errorf(ErrInvalidArgument, "extend_interval 必须小于 idle_timeout")
	}
	// 缓存窗口大于空闲超时意味着 token 过期后仍可能被 SDK 放行。
	if p.TokenCacheTTLSeconds > p.IdleTimeoutSeconds {
		return Errorf(ErrInvalidArgument, "token_cache_ttl 不能大于 idle_timeout")
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
	RedirectURIs []string // OIDC 预留
	GrantTypes   []string // OIDC 预留
	CreatedAt    int64
	UpdatedAt    int64
}

// ApplicationConnector 是某个应用对某种登录方式的启用状态与配置。
type ApplicationConnector struct {
	Type    string
	Enabled bool
	Config  map[string]any
}
