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
type Subject struct {
	Kind SubjectKind
	ID   string
}

func User(id string) Subject  { return Subject{Kind: KindUser, ID: id} }
func Guest(id string) Subject { return Subject{Kind: KindGuest, ID: id} }
func Biz(id string) Subject   { return Subject{Kind: KindBiz, ID: id} }

func (s Subject) String() string { return string(s.Kind) + ":" + s.ID }

var ErrBadSubject = errors.New("model: subject 格式非法")

// ParseSubject 解析 "u:{uid}" / "g:{uuid}"。访客 id 必须是标准写法的 uuid v4，
// 否则前端拼一个可预测的字符串就能冒充别人。
func ParseSubject(s string) (Subject, error) {
	kind, id, ok := strings.Cut(s, ":")
	if !ok || id == "" {
		return Subject{}, fmt.Errorf("%w: %q", ErrBadSubject, s)
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
