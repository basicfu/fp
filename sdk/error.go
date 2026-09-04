package fpsdk

import (
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// Error 是 fp 返回的结构化错误。
//
// 三个字段的分工：
//
//	Code   机器可读的稳定契约，用它分支。取值见 fp 的错误码表。
//	Msg    面向终端用户，可以直接展示，不含任何内部标识。
//	Detail JSON 字符串，可能为空。给你排查用——通常有 desc 字段，
//	       视错误类型可能带 retryAfterMs 等扩展字段。原样打进日志即可。
//
// 用法：
//
//	var fe *fpsdk.Error
//	if errors.As(err, &fe) && fe.Code == "ACCOUNT_FROZEN" {
//		showToUser(fe.Msg)   // "账号已被冻结"
//	}
//
// 老写法继续有效，不需要改：
//
//	if errors.Is(err, fpsdk.ErrUnauthorized) { ... }
type Error struct {
	Code   string
	Msg    string
	Detail string

	// sentinel 是本包的扁平哨兵（ErrUnauthorized 等），保证既有的
	// errors.Is 判定继续成立。
	sentinel error
	// cause 是原始的 gRPC error，保留给需要看 status code 的调用方。
	cause error
}

// Error 实现 error。返回 Msg——它就是给人看的那句话。
func (e *Error) Error() string { return e.Msg }

// Unwrap 返回哨兵与原始错误两条链，让 errors.Is 对二者都成立。
//
// 用多值 Unwrap 而不是只返回 sentinel：既要让业务方的
// errors.Is(err, ErrUnauthorized) 继续工作，又不能把原始的 gRPC status
// 藏起来——排查连接层问题时那才是有用的东西。
func (e *Error) Unwrap() []error { return []error{e.sentinel, e.cause} }

// errorFrom 尝试从 gRPC error 里取出 fp 的结构化错误。
//
// 取不到时返回 nil，调用方退回扁平哨兵——这条退路是**向后兼容的关键**：
// 老服务端不带 ErrorDetail，新 SDK 连上它时行为必须与升级前完全一致，
// 而不是把每个错误都变成一个 Code 为空串的结构化错误。
func errorFrom(err error, sentinel error) *Error {
	st, ok := status.FromError(err)
	if !ok {
		return nil
	}
	for _, d := range st.Details() {
		ed, ok := d.(*fpv1.ErrorDetail)
		if !ok || ed.GetCode() == "" {
			continue
		}
		return &Error{
			Code:     ed.GetCode(),
			Msg:      ed.GetMsg(),
			Detail:   ed.GetDetail(),
			sentinel: sentinel,
			cause:    err,
		}
	}
	return nil
}
