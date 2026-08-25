package domain

import (
	"time"

	"github.com/google/uuid"
)

// Session 是一次登录产生的会话。
//
// 时间字段分工（三者不可互相替代）：
//   - FirstAuthAt    首次认证时刻，token 轮换时**不变**，是 max_lifetime 的基准
//   - IssuedAt       当前 token 的签发时刻，轮换时更新，是 rotate_interval 的基准
//   - LastExtendedAt 上次延期写的时刻，是 extend_interval 降频的基准
type Session struct {
	// ID 是会话标识，token 轮换时不变。撤销以它为单位。
	ID string
	// Token 是当前有效的 opaque token。
	Token          string
	UserID         uuid.UUID
	AppID          uuid.UUID
	FirstAuthAt    int64
	IssuedAt       int64
	LastExtendedAt int64
	// IdleExpiresAt 是空闲超时的绝对时刻。Redis key 的 TTL 与它保持一致，
	// 但校验以本字段为准——TTL 只是兜底清理，不承担判定职责。
	IdleExpiresAt int64
	IP            string
	UA            string
	// Mobile 决定适用哪一档空闲超时。
	Mobile bool
}

// MaxExpiresAt 返回绝对上限到期时刻（毫秒）。到达后必须重新认证，轮换无法延长它。
func (s Session) MaxExpiresAt(p SessionPolicy) int64 {
	return s.FirstAuthAt + int64(p.MaxLifetimeSeconds)*1000
}

// RemainingAt 返回在 now 时刻该会话还能存活多久。
// 取空闲超时与绝对上限中较早的一个；已过期时返回 0。
func (s Session) RemainingAt(now int64, p SessionPolicy) time.Duration {
	remain := s.IdleExpiresAt - now
	if maxRemain := s.MaxExpiresAt(p) - now; maxRemain < remain {
		remain = maxRemain
	}
	if remain <= 0 {
		return 0
	}
	return time.Duration(remain) * time.Millisecond
}
