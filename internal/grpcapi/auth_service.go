package grpcapi

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// AppLookup 按对外 appId 取应用。用于把 metadata 里的 appId 换成内部 UUID。
// *service.ApplicationService 满足它。
type AppLookup interface {
	GetByAppID(ctx context.Context, appID string) (*domain.Application, error)
}

// AuthServerDeps 是 gRPC 认证服务的依赖。
type AuthServerDeps struct {
	Auth *service.AuthService
	// Hub 承担 Watch 流的撤销事件中继，进程内所有流共用同一份。
	Hub *RevokeHub
	// Apps 把 Watch 的调用方 appId（字符串）换成内部 UUID，用于按应用订阅。
	Apps AppLookup
}

// NewAuthServer 构造 gRPC 认证服务。
func NewAuthServer(d AuthServerDeps) fpv1.AuthServiceServer {
	return &authServer{auth: d.Auth, hub: d.Hub, apps: d.Apps}
}

type authServer struct {
	fpv1.UnimplementedAuthServiceServer
	auth *service.AuthService
	hub  *RevokeHub
	apps AppLookup
}

// callerAppID 取出拦截器已认证的 appId。
//
// 取不到只可能是拦截器没挂上——那是装配错误，不是调用方的错，
// 所以返回 Internal 而不是 Unauthenticated：后者会让人以为是凭据问题，
// 排障方向直接跑偏。
func callerAppID(ctx context.Context) (string, error) {
	appID, ok := appIDFrom(ctx)
	if !ok || appID == "" {
		return "", status.Error(codes.Internal, "internal error")
	}
	return appID, nil
}

func (s *authServer) SendLoginCode(ctx context.Context, req *fpv1.SendLoginCodeRequest) (*fpv1.SendLoginCodeResponse, error) {
	appID, err := callerAppID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.auth.SendLoginCode(ctx, appID, req.GetPhone()); err != nil {
		return nil, statusFrom(err)
	}
	return &fpv1.SendLoginCodeResponse{}, nil
}

func (s *authServer) Login(ctx context.Context, req *fpv1.LoginRequest) (*fpv1.LoginResponse, error) {
	appID, err := callerAppID(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.auth.Login(ctx, service.LoginInput{
		AppID:         appID,
		ConnectorType: req.GetConnectorType(),
		Credentials:   connector.Credentials(req.GetCredentials()),
		IP:            req.GetIp(),
		UA:            req.GetUserAgent(),
		Mobile:        req.GetMobile(),
	})
	if err != nil {
		return nil, statusFrom(err)
	}
	return &fpv1.LoginResponse{
		Token:     res.Session.Token,
		SessionId: res.Session.ID,
		User:      userInfo(res.User),
	}, nil
}

func (s *authServer) Logout(ctx context.Context, req *fpv1.LogoutRequest) (*fpv1.LogoutResponse, error) {
	if _, err := callerAppID(ctx); err != nil {
		return nil, err
	}
	if err := s.auth.Logout(ctx, req.GetToken()); err != nil {
		return nil, statusFrom(err)
	}
	return &fpv1.LogoutResponse{}, nil
}

func (s *authServer) ValidateToken(ctx context.Context, req *fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
	appID, err := callerAppID(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.auth.ValidateToken(ctx, appID, req.GetToken())
	if err != nil {
		return nil, statusFrom(err)
	}
	return &fpv1.ValidateTokenResponse{
		UserId:    res.Session.UserID.String(),
		SessionId: res.Session.ID,
		// Milliseconds() 对 time.Duration 是整除，天然向下取整。
		// 绝不能改成基于 Seconds() 的四舍五入——那会把 800ms 进位成 1s，
		// 重新引入 min(配置, 剩余) 本来要防的过度缓存。
		CacheTtlMs: res.CacheTTL.Milliseconds(),
		Rotated:    res.Rotated,
		NewToken:   res.NewToken,
	}, nil
}

// userInfo 把领域用户转成传输对象。
func userInfo(u *domain.User) *fpv1.UserInfo {
	if u == nil {
		return nil
	}
	return &fpv1.UserInfo{
		Id:        u.ID.String(),
		Nickname:  u.Nickname,
		AvatarUrl: u.AvatarURL,
		Gender:    u.Gender,
		Status:    u.Status,
	}
}

// Watch 把撤销事件推给 SDK。
//
// 流的生命周期就是订阅的生命周期。返回即注销——中继不需要知道流为什么结束。
func (s *authServer) Watch(stream grpc.BidiStreamingServer[fpv1.WatchRequest, fpv1.WatchResponse]) error {
	ctx := stream.Context()
	appIDStr, err := callerAppID(ctx)
	if err != nil {
		return err
	}
	app, err := s.apps.GetByAppID(ctx, appIDStr)
	if err != nil {
		return statusFrom(err)
	}

	// 先订阅再发 ready：反过来的话，客户端收到 ready 就认为推送通道健康、
	// 从而放宽本地缓存窗口，而此刻服务端还没订上，这段时间的撤销全丢。
	events, unsubscribe := s.hub.Subscribe(app.ID)
	defer unsubscribe()

	if err := stream.Send(&fpv1.WatchResponse{
		Event: &fpv1.WatchResponse_Ready{Ready: &fpv1.WatchReady{}},
	}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				// hub 关闭（进程退出），或本订阅者因缓冲满且需要 purge
				// 而被摘掉。两种情况都是正常结束——SDK 会重连，
				// 重连后按 Task 10 的设计自行 purge。
				return nil
			}
			var msg *fpv1.WatchResponse
			if ev.Purge {
				msg = &fpv1.WatchResponse{
					Event: &fpv1.WatchResponse_Purge{
						Purge: &fpv1.WatchPurge{Reason: ev.Reason},
					},
				}
			} else {
				msg = &fpv1.WatchResponse{
					Event: &fpv1.WatchResponse_Revoke{Revoke: revokeEvent(ev.Revoke)},
				}
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// revokeEvent 把领域事件转成传输对象。
func revokeEvent(ev domain.RevokeEvent) *fpv1.RevokeEvent {
	out := &fpv1.RevokeEvent{
		Tokens:  ev.Tokens,
		UserIds: make([]string, 0, len(ev.UserIDs)),
		Reason:  ev.Reason,
		AtMs:    ev.At,
	}
	for _, id := range ev.UserIDs {
		out.UserIds = append(out.UserIds, id.String())
	}
	// uuid.Nil 表示跨全部应用，映射成空串——绝不能写成 "00000000-0000-…"，
	// SDK 那边会把它当成一个真实的应用 ID。
	if ev.AppID != uuid.Nil {
		out.AppId = ev.AppID.String()
	}
	return out
}
