package imgrpc

import (
	"context"
	"crypto/subtle"

	"github.com/basicfu/fp/internal/im/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// 与 fp 的 grpcapi 使用相同的 metadata 键，业务方的 SDK 配置同一对 app_id/app_secret 就能同时连 fp 和 fp-im。
const (
	MDAppID     = "fp-app-id"
	MDAppSecret = "fp-app-secret"
)

type appCtxKey struct{}

func appFrom(ctx context.Context) string {
	v, _ := ctx.Value(appCtxKey{}).(string)
	return v
}

// streamAuth 用常量时间比较校验凭据。fp-im 没有数据库也不做 bcrypt：apps 文件里就是明文 secret。
func streamAuth(apps auth.AppConfigSource) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		md, _ := metadata.FromIncomingContext(ss.Context())
		id, secret := first(md, MDAppID), first(md, MDAppSecret)
		cfg, ok := apps.Get(id)
		if !ok || subtle.ConstantTimeCompare([]byte(cfg.AppSecret), []byte(secret)) != 1 {
			return status.Error(codes.Unauthenticated, "应用凭据无效")
		}
		return handler(srv, &wrapped{ServerStream: ss, ctx: context.WithValue(ss.Context(), appCtxKey{}, id)})
	}
}

func first(md metadata.MD, k string) string {
	if v := md.Get(k); len(v) > 0 {
		return v[0]
	}
	return ""
}

type wrapped struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrapped) Context() context.Context { return w.ctx }
