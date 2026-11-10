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
	// KindBiz 是业务方自己的认证体系里的用户。与 KindUser 分开，是为了让
	// fp 的用户 1001 和业务方的用户 1001 在网关的 Redis key 上天然隔离。
	// 这份定义与 internal/im/model 的那份必须逐字一致，由
	// internal/integration 的配对测试守着——sdk 不能 import internal，
	// 所以这个重复是架构约束下的必然。
	KindBiz SubjectKind = "b"
)

// Subject 是一条连接归属的主体。同一 Subject 可以有多条连接（多终端）。
//
// Group 只对业务方主体有意义，由业务方鉴权回调决定；空串是默认分组。
// 不同分组是不同的主体：b:1001 与 b.r:1001 互不顶号，推送、踢人也要用
// 各自的写法。
type Subject struct {
	Kind  SubjectKind
	Group string
	ID    string
}

func User(id string) Subject  { return Subject{Kind: KindUser, ID: id} }
func Guest(id string) Subject { return Subject{Kind: KindGuest, ID: id} }
func Biz(id string) Subject   { return BizGroup("", id) }

// BizGroup 构造分组业务方主体，写成 "b.<group>:<uid>"。
func BizGroup(group, id string) Subject { return Subject{Kind: KindBiz, Group: group, ID: id} }

// IsDefaultBiz 判断是否默认分组的业务方主体。只判 Kind==KindBiz 不够：
// uid 是纯数字的分组主体会被误当成默认分组。
func (s Subject) IsDefaultBiz() bool { return s.Kind == KindBiz && s.Group == "" }

func (s Subject) String() string {
	if s.Group != "" {
		return string(s.Kind) + "." + s.Group + ":" + s.ID
	}
	return string(s.Kind) + ":" + s.ID
}

var ErrBadSubject = errors.New("fpim: subject 格式非法")

// Parse 解析 "u:{uid}" / "g:{uuid}" / "b:{uid}" / "b.{group}:{uid}"。
// 访客 id 必须是标准写法的 uuid v4，否则前端拼一个可预测的字符串就能冒充别人。
func Parse(s string) (Subject, error) {
	kind, id, ok := strings.Cut(s, ":")
	if !ok || id == "" {
		return Subject{}, fmt.Errorf("%w: %q", ErrBadSubject, s)
	}
	if base, group, dotted := strings.Cut(kind, "."); dotted {
		if SubjectKind(base) != KindBiz {
			return Subject{}, fmt.Errorf("%w: 只有业务方主体能带分组: %q", ErrBadSubject, s)
		}
		if !validGroup(group) {
			return Subject{}, fmt.Errorf("%w: 分组名非法: %q", ErrBadSubject, group)
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
		// 下面这个空值判断实际到不了：函数入口的 `!ok || id == ""` 已经
		// 先拦住了空 id，走到这里 id 必然非空。保留它是为了与
		// internal/im/model 那份网关侧实现保持结构对称（那边的 KindBiz
		// 分支也有同样的判断），也为将来有人改动入口检查、让空 id 有可能
		// 漏到这里时留一道防线。不像网关侧 ValidateUserID 那样再查长度与
		// 空字节：那是给"业务方回调返回的值"用的入口校验，这里解析的是
		// 网关已经发下来的主体串，网关不会发出非法值。128 字节以内且不含
		// 空字节的输入在两边行为相同，配对测试的输入集也只覆盖这一范围。
		if id == "" {
			return Subject{}, fmt.Errorf("%w: 业务方 id 不能为空", ErrBadSubject)
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

// validGroup 与网关侧 model.ValidateGroup 同一规则：^[a-z][a-z0-9_]{0,15}$。
func validGroup(g string) bool {
	if g == "" || len(g) > 16 {
		return false
	}
	for i := 0; i < len(g); i++ {
		c := g[i]
		ok := c >= 'a' && c <= 'z'
		if i > 0 {
			ok = ok || c >= '0' && c <= '9' || c == '_'
		}
		if !ok {
			return false
		}
	}
	return true
}
