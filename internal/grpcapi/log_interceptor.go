package grpcapi

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// logFailedUnary 把失败的一元 RPC 记下来：哪个应用、调的哪个方法、为什么没成。
//
// 业务错误（*domain.Error）在 StatusFrom 里原样回给调用方，fp 自己不留痕。接入方排查
// "为什么发不了验证码"时，fp 的日志里什么都没有，只能去翻对方的日志——而"应用没开放这种
// 登录方式"恰恰是要在 fp 控制台里改的事。
//
// 必须排在鉴权拦截器之后：appId 是它放进 ctx 的。
func logFailedUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	resp, err := handler(ctx, req)
	if err == nil {
		return resp, nil
	}
	st := status.Convert(err)
	appID, _ := appIDFrom(ctx)
	attrs := []any{"method", info.FullMethod, "appId", appID, "grpc", st.Code().String(), "msg", st.Message()}
	for _, d := range st.Details() {
		if ed, ok := d.(*fpv1.ErrorDetail); ok {
			attrs = append(attrs, "code", ed.GetCode())
		}
	}
	slog.Log(ctx, failureLevel(st.Code()), "grpcapi: 请求未成功", attrs...)
	return resp, err
}

// failureLevel 按"要不要有人去处理"分级。
//
// 终端用户自己造成的失败（密码错、token 过期、参数不对、被限流、查无此物）每天成千上万，
// 记成 warn 只会把真正要处理的淹掉；配置没到位、被策略拒绝、fp 自己出错才值得一眼看到。
func failureLevel(c codes.Code) slog.Level {
	switch c {
	case codes.Unauthenticated, codes.InvalidArgument, codes.NotFound,
		codes.ResourceExhausted, codes.Canceled:
		return slog.LevelDebug
	default:
		return slog.LevelWarn
	}
}
