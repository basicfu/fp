package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// 业务事件种类。写进事件表、经 Watch 流可靠投递给开了事件的应用。
//
// 与撤销频道上的 EventKind* 不同：那些是缓存失效信号，丢了有 TTL 兜底；这些是业务方要据此改自己数据的事实，
// 必须持久化、确认、补发。
const (
	EventLoginSucceeded     = "login_succeeded"
	EventLoggedOut          = "logged_out"
	EventAccountDeleted     = "account_deleted"
	EventWechatSubscription = "wechat_subscription"
	// EventProfileChanged：昵称、头像、密码或登录标识改了。只是"去重取资料"的提示，不带改了什么。
	EventProfileChanged = "profile_changed"
)

// logged_out 的 reason 除了 RevokeReason* 之外的两种：这两种不经撤销接口，是 fp 自己发现会话没了。
const (
	// LoggedOutExpired：会话空闲超时或到达绝对上限。
	LoggedOutExpired = "expired"
	// LoggedOutRevoked：撤销递增了纪元、却没能写完事件就中断，之后校验才发现纪元不一致而删掉的会话。
	// 当初是哪种撤销已不可考，业务方把不认识的 reason 按 kick 处理。
	LoggedOutRevoked = "revoked"
)

// AppEvent 是事件表里的一条业务事件。
type AppEvent struct {
	ID    uuid.UUID
	AppID uuid.UUID
	// Seq 是应用内连续递增的序号，投递按它排序。
	Seq int64
	// UserID 为 uuid.Nil 表示事件不属于某个用户。
	UserID    uuid.UUID
	Kind      string
	Payload   json.RawMessage
	CreatedAt time.Time
}
