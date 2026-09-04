package domain

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// Error 是 fp 对外的统一错误。所有面向调用方的错误都必须是它。
//
// 三个字段的分工：
//
//	Code   机器可读，稳定契约，调用方据此分支。一经发布不得更名，
//	       也不得复用于其他语义。
//	Msg    面向终端用户，接入方可以直接展示。不含任何内部标识。
//	       改动措辞等同于对外行为变更，要当作 API 变动对待。
//	Detail 面向接入方开发者，排查用。序列化成 JSON 字符串上线，
//	       字段可以增删，不承担契约约束。
//
// 为什么 Msg 和 Detail 要分开：接入方需要一句能直接弹给用户的话，
// 同时又需要足以定位问题的上下文，而后者往往含 userID、配置项名之类
// 不该出现在用户界面上的东西。合成一个字段的话，接入方要么泄露内部
// 标识，要么就得自己写一套文案映射表。
type Error struct {
	Code   string
	Msg    string
	Detail map[string]any

	// sentinel 决定 HTTP 状态码与 gRPC code，并让 errors.Is 继续可用。
	// 它是七个哨兵之一，不对外暴露——调用方要分支就用 Code。
	sentinel error
}

// Error 实现 error。返回 Msg 而不是拼接后的长串：这个值会被接入方
// 直接展示给终端用户，掺进 code 或 detail 会让用户看到内部信息。
func (e *Error) Error() string { return e.Msg }

// Unwrap 让 errors.Is(err, domain.ErrXxx) 继续成立。
func (e *Error) Unwrap() error { return e.sentinel }

// DetailJSON 把 Detail 序列化成上线用的字符串。
//
// Detail 为空时返回空串而不是 "{}"：让"没有细节"在线上是一个明确的
// 空值，接入方判断 detail == "" 即可，不必先解析出一个空对象再判断
// 它没有字段。
//
// 序列化失败时返回空串并记日志——detail 是诊断信息，为它让整个错误
// 响应失败是本末倒置。实践中 Detail 只会装字符串与数字，走不到这里。
func (e *Error) DetailJSON() string {
	if len(e.Detail) == 0 {
		return ""
	}
	b, err := json.Marshal(e.Detail)
	if err != nil {
		slog.Error("domain: 序列化错误详情失败", "err", err, "code", e.Code)
		return ""
	}
	return string(b)
}

// Fail 构造一个领域错误。三个参数都是必填。
//
// code 必须是本包 codes.go 里登记过的常量——注册表的完整性由
// TestEveryCodeIsRegistered 守住。
func Fail(sentinel error, code, msg string) *Error {
	return &Error{Code: code, Msg: msg, sentinel: sentinel}
}

// Failf 是 Fail 的格式化版本，用于 msg 需要带入参数的场合
// （例如"密码长度不能少于 %d 位"）。
func Failf(sentinel error, code, format string, a ...any) *Error {
	return Fail(sentinel, code, fmt.Sprintf(format, a...))
}

// WithDesc 写入 Detail["desc"]，即默认的自由文本详情字段。
//
// 叫 desc 而不是 detail，是为了避免线上出现 detail.detail 这种嵌套。
func (e *Error) WithDesc(format string, a ...any) *Error {
	return e.WithField("desc", fmt.Sprintf(format, a...))
}

// WithField 写入任意扩展字段，例如 retryAfterMs、field。
//
// 就地累加而不是重建 map：链式调用要能叠加多个字段。
func (e *Error) WithField(key string, value any) *Error {
	if e.Detail == nil {
		e.Detail = make(map[string]any, 2)
	}
	e.Detail[key] = value
	return e
}
