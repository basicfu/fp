// Package domain 定义 fp 的领域类型与错误，不依赖任何传输层或存储实现。
package domain

import (
	"errors"
	"fmt"
)

// 哨兵错误。service 层返回这些错误的包装，传输层用 errors.Is 判定并映射成协议错误。
var (
	ErrNotFound          = errors.New("not found")
	ErrInvalidCredential = errors.New("invalid credential")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrConflict          = errors.New("conflict")
	ErrInvalidArgument   = errors.New("invalid argument")
	ErrRateLimited       = errors.New("rate limited")
	ErrForbidden         = errors.New("forbidden")
)

// Errorf 用 base 哨兵错误包装一条带上下文的消息。
// 返回值满足 errors.Is(err, base)，同时携带面向用户的说明。
func Errorf(base error, format string, a ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, a...), base)
}
