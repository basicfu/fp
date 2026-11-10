// Package model 是 fp-im 的纯类型层：没有 I/O，不依赖仓库内任何其他包。
package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// SubjectKind 是连接主体的类型前缀。
// 登录用户和访客用不同前缀，是为了两个命名空间在 Redis key 上永远不可能相撞。
type SubjectKind string

const (
	KindUser  SubjectKind = "u"
	KindGuest SubjectKind = "g"
	// KindBiz 是业务方自己的认证体系里的用户。与 KindUser 分开，是为了让
	// fp 的用户 1001 和业务方的用户 1001 在 Redis key 上天然隔离——
	// 合成一个前缀的话，两个毫不相干的人会共用同一张连接表，互相顶号、
	// 互相收到对方的消息。
	KindBiz SubjectKind = "b"
)

// Subject 是一条连接归属的主体。同一 Subject 可以有多条连接（多终端）。
//
// Group 只对业务方主体有意义，由业务方鉴权回调决定；空串是默认分组。
// 不同分组是不同的主体：b:1001 与 b.r:1001 各有各的连接表，互不顶号。
type Subject struct {
	Kind  SubjectKind
	Group string
	ID    string
}

func User(id string) Subject  { return Subject{Kind: KindUser, ID: id} }
func Guest(id string) Subject { return Subject{Kind: KindGuest, ID: id} }
func Biz(id string) Subject   { return BizGroup("", id) }

// BizGroup 构造分组业务方主体。不在这里校验分组名：构造函数没有错误返回，
// 外部输入的分组名在 bizauth 与 ParseSubject 两个入口校验。
func BizGroup(group, id string) Subject { return Subject{Kind: KindBiz, Group: group, ID: id} }

// IsDefaultBiz 判断是否默认分组的业务方主体。只判 Kind==KindBiz 不够：
// uid 是纯数字的分组主体会被误当成默认分组。
func (s Subject) IsDefaultBiz() bool { return s.Kind == KindBiz && s.Group == "" }

// String 把分组写在冒号前面（b.<group>:<uid>）：uid 不约束字符集、可能含
// 冒号，写在后面会与现有 uid 撞车；而现有前缀都不含点，kind 段带点就一定
// 是分组主体。
func (s Subject) String() string {
	if s.Group != "" {
		return string(s.Kind) + "." + s.Group + ":" + s.ID
	}
	return string(s.Kind) + ":" + s.ID
}

var ErrBadSubject = errors.New("model: subject 格式非法")

// ParseSubject 解析 "u:{uid}" / "g:{uuid}" / "b:{uid}" / "b.{group}:{uid}"。
// 访客 id 必须是标准写法的 uuid v4，否则前端拼一个可预测的字符串就能冒充别人。
func ParseSubject(s string) (Subject, error) {
	kind, id, ok := strings.Cut(s, ":")
	if !ok || id == "" {
		return Subject{}, fmt.Errorf("%w: %q", ErrBadSubject, s)
	}
	if base, group, dotted := strings.Cut(kind, "."); dotted {
		// fp 用户与访客的身份不由业务方决定，只有 b 能带分组
		if SubjectKind(base) != KindBiz {
			return Subject{}, fmt.Errorf("%w: 只有业务方主体能带分组: %q", ErrBadSubject, s)
		}
		if err := ValidateGroup(group); err != nil {
			return Subject{}, fmt.Errorf("%w: %v", ErrBadSubject, err)
		}
		if err := ValidateUserID(id); err != nil {
			return Subject{}, fmt.Errorf("%w: %v", ErrBadSubject, err)
		}
		return BizGroup(group, id), nil
	}
	switch SubjectKind(kind) {
	case KindUser:
		return User(id), nil
	case KindGuest:
		if !IsUUIDv4(id) {
			return Subject{}, fmt.Errorf("%w: 访客 id 不是 uuid v4: %q", ErrBadSubject, id)
		}
		return Guest(id), nil
	case KindBiz:
		if err := ValidateUserID(id); err != nil {
			return Subject{}, fmt.Errorf("%w: %v", ErrBadSubject, err)
		}
		return Biz(id), nil
	}
	return Subject{}, fmt.Errorf("%w: 未知前缀 %q", ErrBadSubject, kind)
}

// IsUUIDv4 只接受 8-4-4-4-12 小写带连字符的 v4。
// 大写或无连字符的写法虽然是同一个 uuid，但会生成不同的 Redis key，
// 同一访客就成了两个人，所以直接拒绝而不是归一化。
func IsUUIDv4(s string) bool {
	if len(s) != 36 || strings.ToLower(s) != s {
		return false
	}
	u, err := uuid.Parse(s)
	return err == nil && u.Version() == 4
}

// MaxUserIDLen 是业务方用户标识的长度上限。
// 它会成为 Redis key 的一部分（fp:im:{app:b:1001}:conn），
// 没有上限的话，一个恶意或有 bug 的业务方能造出超长 key 撑爆 Redis 内存。
const MaxUserIDLen = 128

var ErrBadUserID = errors.New("model: 用户标识非法")

// ValidateUserID 校验业务方回调返回的用户标识。
// 不约束字符集：业务方的用户体系我们不理解，可能是 uuid、雪花 id、邮箱。
// 只拦三种一定会出事的情况。
func ValidateUserID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: 不能为空", ErrBadUserID)
	}
	if len(id) > MaxUserIDLen {
		return fmt.Errorf("%w: 超过 %d 字节", ErrBadUserID, MaxUserIDLen)
	}
	// 路由器的内存键用 \x00 拼接 app 与 subject，id 里含它会让两个
	// 不同的 (app, subject) 组合切分成同一个键。
	if strings.ContainsRune(id, 0) {
		return fmt.Errorf("%w: 不能含空字节", ErrBadUserID)
	}
	return nil
}

// MaxGroupLen 是分组名的长度上限。分组名会进 Redis key 和每条事件的
// subject，短小才不拖累每一次投递。
const MaxGroupLen = 16

var ErrBadGroup = errors.New("model: 分组名非法")

// ValidateGroup 校验分组名：^[a-z][a-z0-9_]{0,15}$。只收小写：大小写不同
// 会生成不同的 key，同一个分组就成了两个。
func ValidateGroup(g string) error {
	if g == "" || len(g) > MaxGroupLen {
		return fmt.Errorf("%w: 长度必须在 1~%d: %q", ErrBadGroup, MaxGroupLen, g)
	}
	for i := 0; i < len(g); i++ {
		c := g[i]
		ok := c >= 'a' && c <= 'z'
		if i > 0 {
			ok = ok || c >= '0' && c <= '9' || c == '_'
		}
		if !ok {
			return fmt.Errorf("%w: 只能是小写字母开头的小写字母、数字、下划线: %q", ErrBadGroup, g)
		}
	}
	return nil
}
