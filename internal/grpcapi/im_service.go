package grpcapi

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// imServer 实现 IMGatewayService。只对 fp-caller-type: im 开放。
type imServer struct {
	fpv1.UnimplementedIMGatewayServiceServer
	apps *service.ApplicationService
}

func newIMServer(apps *service.ApplicationService) fpv1.IMGatewayServiceServer {
	return &imServer{apps: apps}
}

// requireIM 拒绝普通应用调用网关接口。与 requireNotIM 一起构成双向隔离：
// 两个方向各自能做的是**不同**的子集，谁都不是谁的超集。
func requireIM(ctx context.Context) error {
	if callerTypeFrom(ctx) != CallerTypeIM {
		return status.Error(codes.PermissionDenied, "该接口只对 IM 网关开放")
	}
	return nil
}

// imApp 取出 metadata 里那个应用，并要求它启用且打开了 IM。
//
// 应用不存在、已停用、没打开 IM 三种情况统一成 FailedPrecondition：对 fp-im
// 而言它们是同一种后果（这个应用现在不能接入），它据此给 client 关闭码 4002
// （策略拒绝，别重连）。给 4001 是错的方向——那会把 client 指去重新登录，
// 登录完还是连不上，变成死循环。
func (s *imServer) imApp(ctx context.Context) (*domain.Application, error) {
	appID, ok := appIDFrom(ctx)
	if !ok || appID == "" {
		return nil, status.Error(codes.Unauthenticated, "缺少 appId")
	}
	app, err := s.apps.GetActiveByAppID(ctx, appID)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "该应用不可用")
	}
	if !app.IM.Enabled {
		return nil, status.Error(codes.FailedPrecondition, "该应用未启用 IM 接入")
	}
	return app, nil
}

func (s *imServer) GetAppIMConfig(ctx context.Context, _ *fpv1.GetAppIMConfigRequest) (*fpv1.GetAppIMConfigResponse, error) {
	if err := requireIM(ctx); err != nil {
		return nil, err
	}
	app, err := s.imApp(ctx)
	if err != nil {
		return nil, err
	}
	resp := &fpv1.GetAppIMConfigResponse{
		AllowGuest:  app.IM.AllowGuest,
		ConnPolicy:  app.IM.ConnPolicy,
		ConnLimit:   app.IM.ConnLimit,
		GuestIpRate: app.IM.GuestIPRate,
	}
	// 没配 biz_auth 时**不设**这个字段，而不是回一个零值 message：后者会让
	// fp-im 以为"支持业务方令牌，但地址是空串"，于是 kind:biz 的握手走进一条
	// 永远失败的回调。
	if b := app.IM.BizAuth; b != nil {
		resp.BizAuth = &fpv1.IMBizAuth{
			VerifyUrl: b.VerifyURL,
			TimeoutMs: b.TimeoutMs,
			CacheSize: b.CacheSize,
		}
	}
	return resp, nil
}

func (s *imServer) VerifyAppCredential(ctx context.Context, req *fpv1.VerifyAppCredentialRequest) (*fpv1.VerifyAppCredentialResponse, error) {
	if err := requireIM(ctx); err != nil {
		return nil, err
	}
	appID, ok := appIDFrom(ctx)
	if !ok || appID == "" {
		return nil, status.Error(codes.Unauthenticated, "缺少 appId")
	}
	// **先验凭据，再看 im_enabled。** 顺序反了的话，一个手里没有 appSecret
	// 的人也能探出某个应用有没有开 IM。凭据验过之后再区分是安全的：能走到
	// 这一步的人本来就持有那份 secret，而运维需要这个区分才知道去翻开关。
	//
	// VerifySecret 内部对"应用不存在"与"secret 错"返回同一个错误，防 appId
	// 枚举——这里原样归成 Unauthenticated，不要试图区分。
	app, err := s.apps.VerifySecret(ctx, appID, req.GetSecret())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "应用凭据无效")
	}
	if app.Status != domain.ApplicationStatusActive {
		return nil, status.Error(codes.Unauthenticated, "应用凭据无效")
	}
	if !app.IM.Enabled {
		return nil, status.Error(codes.FailedPrecondition, "该应用未启用 IM 接入")
	}
	return &fpv1.VerifyAppCredentialResponse{}, nil
}
