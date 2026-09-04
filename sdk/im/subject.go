package fpim

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// SubjectKind 是连接主体的类型前缀。
// 登录用户和访客用不同前缀，是为了两个命名空间在 Redis key 上永远不可能相撞。
//
// 本文件是 internal/im/model/subject.go 的复制品：sdk/ 不得 import
// internal/（见 sdk/arch_test.go），所以主体解析逻辑必须在这个包里
// 再写一份，行为必须与 internal/im/model 那份字节级一致——这份重复由
// internal/integration/im_parity_test.go 守着，函数名的唯一差异是这边叫
// Parse，那边叫 ParseSubject。
type SubjectKind string

const (
	KindUser  SubjectKind = "u"
	KindGuest SubjectKind = "g"
)

// Subject 是一条连接归属的主体。同一 Subject 可以有多条连接（多终端）。
type Subject struct {
	Kind SubjectKind
	ID   string
}

func User(id string) Subject  { return Subject{Kind: KindUser, ID: id} }
func Guest(id string) Subject { return Subject{Kind: KindGuest, ID: id} }

func (s Subject) String() string { return string(s.Kind) + ":" + s.ID }

var ErrBadSubject = errors.New("fpim: subject 格式非法")

// Parse 解析 "u:{uid}" / "g:{uuid}"。访客 id 必须是标准写法的 uuid v4，
// 否则前端拼一个可预测的字符串就能冒充别人。
func Parse(s string) (Subject, error) {
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
