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
	// Roles 是签发时该用户在本应用的**有效角色**（全局角色 ∪ 应用默认角色）。
	//
	// 刻在会话里而不是每次判定时现查：判定路径因此完全不碰数据库。角色变更
	// 靠主动推送让 SDK 丢缓存重新校验，届时这里会带回新值。
	//
	// 与 Epoch 的语义相反：Epoch 不一致是**拒绝**（撤销），角色变了只是**刷新**，
	// 不影响登录态——给某人加个权限不该把他踢下线。
	Roles []string

	// Epoch 是签发时该用户的撤销纪元。校验时与当前纪元比对，不一致即拒。
	//
	// 它关掉的是「管理员冻结/改密」与「登录签发」之间的竞态：撤销的清扫
	// 只能扫到已存在的会话，扫不到正在签发路上的那一个。轮换时原样继承——
	// 轮换只换 token 的值，不是新的一次认证。
	Epoch int64
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

// 撤销原因。写入撤销事件与登录日志，用于排障与审计。
const (
	RevokeReasonLogout          = "logout"
	RevokeReasonKick            = "kick"
	RevokeReasonFreeze          = "freeze"
	RevokeReasonPasswordChanged = "password_changed"
)

// RevokeEvent 是一次撤销的广播消息。
//
// SDK 按 token 缓存校验结果，因此事件必须携带具体的 token 列表，
// 而不能只给 userID——否则 SDK 无从知道该清哪些缓存条目。
type RevokeEvent struct {
	// Tokens 是本次被撤销的全部 token，可能跨多个用户。
	Tokens []string `json:"tokens"`
	// UserIDs 是这些 token 所属的用户，**仅用于日志与排障**。
	//
	// 是列表而非单值：批量撤销（一次踢 500 个用户）合并成一条事件，
	// 否则 500 次操作会产生 500 条广播、每条都要扇出到全部实例。
	UserIDs []uuid.UUID `json:"userIds"`
	// AppID 为 uuid.Nil 表示跨全部应用的撤销。
	//
	// 一条事件只有一个 AppID：合并只在「相同 reason + 相同 app 范围」内进行。
	// 把跨应用撤销（Nil）与限定应用的撤销混进同一条，必然有一半的过滤是错的。
	AppID  uuid.UUID `json:"appId"`
	Reason string    `json:"reason"`
	At     int64     `json:"at"`
}
