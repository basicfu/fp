package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// 用户状态。状态机见 CanTransitionUserStatus。
const (
	UserStatusActive        = "ACTIVE"
	UserStatusFrozen        = "FROZEN"
	UserStatusPendingDelete = "PENDING_DELETE" // 已提交注销，处于保护期
	UserStatusDeleted       = "DELETED"        // 终态
)

// userTransitions 是允许的状态迁移。缺省即不允许。
var userTransitions = map[string]map[string]bool{
	UserStatusActive: {
		UserStatusFrozen:        true,
		UserStatusPendingDelete: true,
	},
	UserStatusFrozen: {
		UserStatusActive: true,
	},
	UserStatusPendingDelete: {
		// 保护期内任意登录行为都会撤销注销申请
		UserStatusActive:  true,
		UserStatusDeleted: true,
	},
	// UserStatusDeleted 是终态，无出边。
}

// CanTransitionUserStatus 报告 from → to 是否为允许的状态迁移。
func CanTransitionUserStatus(from, to string) bool {
	return userTransitions[from][to]
}

// User 是一个全局唯一的用户。登录标识全部落在 Identity 上，
// 本结构只持有跨标识共享的资料与凭据。
type User struct {
	ID uuid.UUID
	// PasswordHash 是 password connector 的凭据，未设置时为空串。
	PasswordHash string
	Nickname     string
	// AvatarURL 留给三方登录带来的外部地址。AvatarHash 非空时以 AvatarHash 为准。
	AvatarURL string
	// AvatarHash 是存在 fp 里的头像的内容哈希（见 AvatarHash 函数），没有头像时为空串。
	AvatarHash        string
	Gender            string
	Status            string
	DeleteSubmittedAt int64 // 0 表示未提交注销
	CreatedAt         int64
	UpdatedAt         int64
}

// 性别取值。空串表示未设置。
const (
	GenderMale   = "MALE"
	GenderFemale = "FEMALE"
)

// ValidGender 报告 g 是否为合法的性别取值（含表示未设置的空串）。
func ValidGender(g string) bool {
	return g == "" || g == GenderMale || g == GenderFemale
}

// NicknameMaxLen 是昵称的最大长度，按字符（rune）计。
const NicknameMaxLen = 32

// NormalizeNickname 去掉首尾空白并校验昵称，返回可以落库的值。
//
// 控制字符与换行一律拒绝：昵称会被各业务方原样渲染进界面、通知与日志，
// 一个换行就能伪造出一行日志或把列表撑乱。
func NormalizeNickname(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", Fail(ErrInvalidArgument, CodeNicknameInvalid, "昵称含有无法识别的字符")
	}
	n := utf8.RuneCountInString(s)
	if n < 1 || n > NicknameMaxLen {
		return "", Failf(ErrInvalidArgument, CodeNicknameInvalid, "昵称长度应为 1–%d 个字符", NicknameMaxLen)
	}
	for _, r := range s {
		// Zl / Zp 是 Unicode 的行、段分隔符，不属于控制字符类，但同样会折行。
		if unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return "", Fail(ErrInvalidArgument, CodeNicknameInvalid, "昵称不能包含换行或控制字符")
		}
	}
	return s, nil
}

// CanLogin 报告该状态的用户是否允许登录。
// 注销保护期内允许登录，登录本身即撤销注销申请。
func (u User) CanLogin() bool {
	return u.Status == UserStatusActive || u.Status == UserStatusPendingDelete
}

// 登录日志事件类型。
//
// 这里**只登记真正会被写入的事件**。曾经还有一个 LoginEventRotate，已删除：
// token 轮换是纯粹的令牌保鲜机制，不是账号生命周期事件，而且频率按
// rotate_interval 算——每个活跃会话每天若干条，会把一条 Redis 上的热路径
// 变成一次 Postgres 写，审计表也会被这些无人查询的行淹没。真要排查轮换问题，
// 撤销事件流（domain.RevokeEvent）比审计表合适得多。
const (
	LoginEventLogin  = "login"
	LoginEventLogout = "logout"
	// LoginEventRevoke 是管理员发起的撤销：冻结、改密、踢下线。
	LoginEventRevoke = "revoke"
)

// LoginLog 是一条登录审计记录。
// UserID / ApplicationID 用指针是因为登录失败时它们可能为空。
type LoginLog struct {
	ID            uuid.UUID
	UserID        *uuid.UUID
	ApplicationID *uuid.UUID
	IdentityType  string
	// Subject 是脱敏后的登录标识。完整值通过 UserID 关联查询。
	Subject   string
	Event     string
	Success   bool
	Reason    string
	IP        string
	UA        string
	SessionID string
	CreatedAt int64
}
