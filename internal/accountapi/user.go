package accountapi

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// anonymousName 是什么标识都没有时的显示名:Name 必须非空(客户端拿它判断"已登录"),文案不带产品名。
const anonymousName = "用户"

// wechatBoundName 是绑着微信、但没记下微信昵称时显示的:空串在契约里是"未绑定"。
const wechatBoundName = "已绑定"

// accountUser 是新接口里所有返回用户资料的地方共用的形状。
// Name 不在计划列的字段里,保留它是因为客户端拿它判断已登录、且显示名规则只在服务端一处。
type accountUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Nickname string `json:"nickname"`
	// Avatar 是头像完整 URL,空 = 没有头像。
	Avatar string `json:"avatar"`
	// Phone、Email 已脱敏, 空 = 未绑定; Wechat 是微信昵称, 空 = 未绑定。
	Phone       string `json:"phone"`
	Email       string `json:"email"`
	Wechat      string `json:"wechat"`
	HasPassword bool   `json:"has_password"`
	// Notify 查不到订阅状态时省略。
	Notify *bool `json:"notify,omitempty"`
}

func identity(p *service.UserProfile, types ...string) string {
	for _, i := range p.Identities {
		for _, t := range types {
			if i.Type == t && i.Subject != "" {
				return i.Subject
			}
		}
	}
	return ""
}

var wechatTypes = []string{domain.IdentityTypeWechatWeb, domain.IdentityTypeWechatApp, domain.IdentityTypeWechatMP}

// displayName 是界面上显示的名字: 昵称 → 脱敏手机号 → 用户名 → 邮箱 → fallback。
func displayName(p *service.UserProfile, fallback string) string {
	if p.User.Nickname != "" {
		return p.User.Nickname
	}
	switch {
	case identity(p, domain.IdentityTypePhone) != "":
		return maskPhone(identity(p, domain.IdentityTypePhone))
	case identity(p, domain.IdentityTypeUsername) != "":
		return identity(p, domain.IdentityTypeUsername)
	case identity(p, domain.IdentityTypeEmail) != "":
		return identity(p, domain.IdentityTypeEmail)
	}
	return fallback
}

// wechatName 是微信那一栏:有微信身份就是它的昵称,没记下昵称显示"已绑定"。
func wechatName(p *service.UserProfile) string {
	bound := false
	for _, i := range p.Identities {
		for _, t := range wechatTypes {
			if i.Type != t {
				continue
			}
			bound = true
			if i.Nickname != "" {
				return i.Nickname
			}
		}
	}
	if bound {
		return wechatBoundName
	}
	return ""
}

func (h *handler) accountUser(ctx context.Context, p *service.UserProfile) accountUser {
	out := accountUser{
		ID: p.User.ID.String(), Name: displayName(p, anonymousName), Nickname: p.User.Nickname,
		Avatar: p.AvatarURL, HasPassword: p.User.PasswordHash != "", Wechat: wechatName(p),
	}
	if ph := identity(p, domain.IdentityTypePhone); ph != "" {
		out.Phone = maskPhone(ph)
	}
	if e := identity(p, domain.IdentityTypeEmail); e != "" {
		out.Email = maskEmail(e)
	}
	out.Notify = h.notifyState(ctx, p.User.ID)
	return out
}

// notifyState 查不到时返回 nil(响应里省略),服务号没配按未订阅:谁都订阅不了。
func (h *handler) notifyState(ctx context.Context, userID uuid.UUID) *bool {
	if h.d.WechatMP == nil {
		return nil
	}
	sub, err := h.d.WechatMP.Subscription(ctx, userID)
	if err != nil {
		if code, _, _ := asDomain(err); code == domain.CodeWechatMPUnavailable {
			v := false
			return &v
		}
		slog.Warn("accountapi: 查公众号订阅失败", "err", err)
		return nil
	}
	v := sub.Bound && sub.Subscribed
	return &v
}

func digits(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func validPhone(s string) bool { return digits(s, 11) && s[0] == '1' }

// maskPhone 把手机号脱敏成 138****8000: 留前三后四。带国家码的写法(+86…、86…)先去掉前缀。
func maskPhone(p string) string {
	p = strings.TrimPrefix(p, "+")
	if len(p) == 13 && strings.HasPrefix(p, "86") {
		p = p[2:]
	}
	if len(p) < 8 {
		return p
	}
	return p[:3] + "****" + p[len(p)-4:]
}

// maskEmail 留本地部分前两个字符: zhangwei@example.com → zh***@example.com。
func maskEmail(e string) string {
	local, dom, ok := strings.Cut(e, "@")
	if !ok {
		return e
	}
	keep := 2
	if utf8.RuneCountInString(local) <= 2 {
		keep = 1
	}
	n := 0
	for i := range local {
		if n == keep {
			local = local[:i]
			break
		}
		n++
	}
	return local + "***@" + dom
}

// validEmail 只拦明显不对的, 细则由服务层判。
func validEmail(e string) bool {
	if len(e) > 254 || strings.ContainsAny(e, " \t\r\n") {
		return false
	}
	local, dom, ok := strings.Cut(e, "@")
	return ok && local != "" && !strings.Contains(dom, "@") &&
		strings.Contains(dom, ".") && !strings.HasPrefix(dom, ".") && !strings.HasSuffix(dom, ".")
}

// 与登录页输入框的上限一致。
const (
	maxAccountLen  = 48
	maxPasswordLen = 32
)

// validPassword: 8–32 个字符, 只用 ASCII 可见字符(不含空格), 大写 / 小写 / 数字 / 特殊字符四类至少两种。
// 与客户端规则清单同一份。
func validPassword(p string) bool {
	if len(p) < 8 || len(p) > maxPasswordLen {
		return false
	}
	var upper, lower, digit, other bool
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c < 0x21 || c > 0x7e:
			return false
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= '0' && c <= '9':
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, b := range []bool{upper, lower, digit, other} {
		if b {
			n++
		}
	}
	return n >= 2
}

func validHash(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
