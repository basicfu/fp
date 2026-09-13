package domain

import (
	"net/netip"
	"time"

	"github.com/google/uuid"
)

const (
	AccessKeyStatusActive   = "ACTIVE"
	AccessKeyStatusDisabled = "DISABLED"
)

// AccessKeyState 是展示状态，由 Status 与 ExpiresAt 算出，不存库——过期不需要定时任务去改 status。
type AccessKeyState string

const (
	AccessKeyStateActive   AccessKeyState = "active"
	AccessKeyStateDisabled AccessKeyState = "disabled"
	AccessKeyStateExpired  AccessKeyState = "expired"
)

// MaxAllowedIPs 是单把 key 的 IP 白名单条数上限。
const MaxAllowedIPs = 50

// AccessKey 是一把访问密钥。时间字段为毫秒，0 表示没有（永不过期 / 从未使用）。
type AccessKey struct {
	ID          uuid.UUID
	AccessKeyID string
	// Secret 只在创建与 SDK 取校验材料时有值，其余查询清空。
	Secret     string
	Remark     string
	RoleKey    string // 空串表示未绑定
	AllowedIPs []netip.Prefix
	Status     string
	ExpiresAt  int64
	LastUsedAt int64
	CreatedAt  int64
	UpdatedAt  int64
}

// State 按 now 算出展示状态，停用优先于过期。
func (k AccessKey) State(now time.Time) AccessKeyState {
	if k.Status == AccessKeyStatusDisabled {
		return AccessKeyStateDisabled
	}
	if k.ExpiresAt > 0 && now.UnixMilli() >= k.ExpiresAt {
		return AccessKeyStateExpired
	}
	return AccessKeyStateActive
}
