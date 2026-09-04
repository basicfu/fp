// Package grpcapi 是 SDK 的 gRPC 传输层。它只做协议转换与认证，不含业务逻辑。
package grpcapi

import (
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// CodeFor 把领域哨兵映射成 gRPC code。
//
// 导出的理由同 httpapi.StatusFor：它是协议契约，需要被跨包一致性测试对照。
//
// 与 httpapi.StatusFor 一一对应——同一个领域错误在两个传输层必须表达同一
// 件事，否则同一个失败经 HTTP 是 403、经 gRPC 是 NotFound，排障时没人能
// 把两边的日志对上。这条对应关系由 TestTransportsAgreeOnEveryCode 断言，
// 不再只靠注释约定。
func CodeFor(sentinel error) codes.Code {
	switch {
	case errors.Is(sentinel, domain.ErrNotFound):
		return codes.NotFound
	case errors.Is(sentinel, domain.ErrInvalidCredential):
		return codes.Unauthenticated
	case errors.Is(sentinel, domain.ErrUnauthorized):
		return codes.Unauthenticated
	case errors.Is(sentinel, domain.ErrForbidden):
		return codes.PermissionDenied
	case errors.Is(sentinel, domain.ErrConflict):
		return codes.AlreadyExists
	case errors.Is(sentinel, domain.ErrInvalidArgument):
		return codes.InvalidArgument
	case errors.Is(sentinel, domain.ErrRateLimited):
		return codes.ResourceExhausted
	default:
		return codes.Internal
	}
}

// StatusFrom 把领域错误映射成 gRPC status，并把 code/msg/detail 三元组
// 作为 details 附上。
//
// gRPC code 的映射与本次改动之前完全一致——老接入方拿到的状态码不变，
// 只是多了可选的 ErrorDetail，不升级 SDK 也不会坏。
//
// 取不到 *domain.Error 的错误（来自 pgx / redis / 标准库）一律 Internal 且
// **丢掉原始消息**：它们的消息里可能带连接串、SQL、表名。服务端日志留全文。
func StatusFrom(err error) error {
	if err == nil {
		return nil
	}

	var de *domain.Error
	if !errors.As(err, &de) {
		slog.Error("grpcapi: 未处理的内部错误", "err", err)
		return status.Error(codes.Internal, "服务器内部错误")
	}

	st := status.New(CodeFor(de.Unwrap()), de.Msg)
	withDetails, derr := st.WithDetails(&fpv1.ErrorDetail{
		Code:   de.Code,
		Msg:    de.Msg,
		Detail: de.DetailJSON(),
	})
	if derr != nil {
		// 附加 details 失败时退回纯 status：宁可少一份结构化信息，
		// 也不能因此把整个错误变成一个不相干的内部错误。
		slog.Error("grpcapi: 附加错误详情失败", "err", derr, "code", de.Code)
		return st.Err()
	}
	return withDetails.Err()
}
