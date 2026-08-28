package grpcapi

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// AuthServerDeps 是 gRPC 认证服务的依赖。
type AuthServerDeps struct {
	Auth *service.AuthService
}

// NewAuthServer 构造 gRPC 认证服务。
func NewAuthServer(d AuthServerDeps) fpv1.AuthServiceServer {
	return &authServer{auth: d.Auth}
}

type authServer struct {
	fpv1.UnimplementedAuthServiceServer
	auth *service.AuthService
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
