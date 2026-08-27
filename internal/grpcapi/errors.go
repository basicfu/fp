// Package grpcapi 是 SDK 的 gRPC 传输层。它只做协议转换与认证，不含业务逻辑。
package grpcapi

import (
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
)

// statusFrom 把领域错误映射成 gRPC status。
//
// 分支顺序与取值刻意与 httpapi.writeError 一一对应——同一个领域错误在
// 两个传输层必须表达同一件事，否则同一个失败经 HTTP 是 403、经 gRPC 是
// NotFound，排障时没人能把两边的日志对上。
//
// 未识别的错误一律 Internal 且**丢掉原始消息**：它们来自 pgx / redis /
// 标准库，消息里可能带连接串、SQL、表名。服务端日志留全文，线上只回一句
// 通用说明。已识别的领域错误则原样回传——那些消息本来就是写给调用方看的。
func statusFrom(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrInvalidCredential):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, domain.ErrUnauthorized):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, domain.ErrForbidden):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, domain.ErrConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, domain.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, domain.ErrRateLimited):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		slog.Error("grpcapi: 未处理的内部错误", "err", err)
		return status.Error(codes.Internal, "internal error")
	}
}
