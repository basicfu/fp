package grpcapi

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// userServer 实现 fpv1.UserServiceServer。
//
// 用户全局互通：调用方应用只用来认证，不参与任何过滤——任何应用都可以按 user_id
// 查询、修改任何用户，不检查该用户是否在本应用登录过。
type userServer struct {
	fpv1.UnimplementedUserServiceServer
	svc      *service.UserService
	accounts *service.AccountService
	// auth 承载账号管理（发码、换绑、绑微信、设密码）：它们要用到验证码、通知与扫码登录的同一套状态。
	auth *service.AuthService
	apps AppLookup
}

func newUserServer(svc *service.UserService, accounts *service.AccountService, auth *service.AuthService, apps AppLookup) *userServer {
	return &userServer{svc: svc, accounts: accounts, auth: auth, apps: apps}
}

// authorize 是全部 RPC 共用的准入：必须是处于启用状态的业务应用。
// fp-im 网关的凭据是另一个收窄过的子集，读不到手机号、也改不了任何人的资料。
func (s *userServer) authorize(ctx context.Context) error {
	if err := requireNotIM(ctx); err != nil {
		return err
	}
	appID, err := callerAppID(ctx)
	if err != nil {
		return err
	}
	// 拦截器只校验 appSecret、不看应用状态；不在这里查一次，停用的应用凭据还能继续读写用户资料。
	if _, err := s.apps.GetActiveByAppID(ctx, appID); err != nil {
		return StatusFrom(err)
	}
	return nil
}

// domainCode 取领域错误的码，不是领域错误时为空。
func domainCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// parseUserID 把请求里的 user_id 解析成 UUID。写错的 id 就是一个不存在的用户，
// 与"格式对但查不到"归为同一个码，调用方只需处理一种情况。
func parseUserID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, StatusFrom(domain.Fail(domain.ErrNotFound, domain.CodeUserNotFound, "用户不存在"))
	}
	return id, nil
}

func (s *userServer) GetUser(ctx context.Context, req *fpv1.GetUserRequest) (*fpv1.UserProfile, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}
	p, err := s.svc.GetProfile(ctx, id)
	if err != nil {
		return nil, StatusFrom(err)
	}
	return userProfile(p), nil
}

func (s *userServer) UpdateUser(ctx context.Context, req *fpv1.UpdateUserRequest) (*fpv1.UserProfile, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}
	// 直接传生成代码里的指针：proto3 optional 的"没带"就是 nil，正好是"不改"。
	p, err := s.svc.UpdateProfile(ctx, id, service.UserProfileUpdate{Nickname: req.Nickname, Gender: req.Gender})
	if err != nil {
		return nil, StatusFrom(err)
	}
	return userProfile(p), nil
}

func (s *userServer) UpdateAvatar(ctx context.Context, req *fpv1.UpdateAvatarRequest) (*fpv1.UserProfile, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}
	p, err := s.svc.UpdateAvatar(ctx, id, req.GetImage())
	if err != nil {
		return nil, StatusFrom(err)
	}
	return userProfile(p), nil
}

func (s *userServer) GetAvatar(ctx context.Context, req *fpv1.GetAvatarRequest) (*fpv1.GetAvatarResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	// PG 里还有字节就字节、地址一起给：只认 image 的老 SDK 照常能用；新上传的头像只在对象存储里，只有地址。
	contentType, image, err := s.svc.GetAvatar(ctx, req.GetHash())
	if err != nil && domainCode(err) != domain.CodeAvatarNotFound {
		return nil, StatusFrom(err)
	}
	notFound := err
	url, err := s.svc.AvatarURLByHash(ctx, req.GetHash())
	if err != nil {
		if c := domainCode(err); c != domain.CodeAvatarNotFound && c != domain.CodeObjectStoreUnavailable {
			return nil, StatusFrom(err)
		}
		if image == nil {
			return nil, StatusFrom(notFound)
		}
	}
	return &fpv1.GetAvatarResponse{Image: image, ContentType: contentType, Url: url}, nil
}

// RequestDeletion 走 AccountService 而不是 UserService：改状态必须连带撤销会话、写审计，
// 那条耦合只在 AccountService 里实现一次。
func (s *userServer) RequestDeletion(ctx context.Context, req *fpv1.RequestDeletionRequest) (*fpv1.RequestDeletionResponse, error) {
	if s.accounts == nil {
		return nil, status.Error(codes.Unimplemented, "RequestDeletion 未启用")
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	id, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}
	purgeAt, err := s.accounts.RequestDeletion(ctx, id)
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.RequestDeletionResponse{PurgeAt: purgeAt}, nil
}

// account 是账号管理 RPC 共用的前置：准入、解析 user_id，返回调用方的 app_id。
func (s *userServer) account(ctx context.Context, rawUserID string) (string, uuid.UUID, error) {
	if s.auth == nil {
		return "", uuid.Nil, status.Error(codes.Unimplemented, "账号管理未启用")
	}
	if err := s.authorize(ctx); err != nil {
		return "", uuid.Nil, err
	}
	appID, err := callerAppID(ctx)
	if err != nil {
		return "", uuid.Nil, err
	}
	id, err := parseUserID(rawUserID)
	if err != nil {
		return "", uuid.Nil, err
	}
	return appID, id, nil
}

func (s *userServer) SendVerifyCode(ctx context.Context, req *fpv1.SendVerifyCodeRequest) (*fpv1.SendVerifyCodeResponse, error) {
	appID, id, err := s.account(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.auth.SendVerifyCode(ctx, appID, id, req.GetChannel(), req.GetTarget()); err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.SendVerifyCodeResponse{}, nil
}

func (s *userServer) VerifyCode(ctx context.Context, req *fpv1.VerifyCodeRequest) (*fpv1.VerifyCodeResponse, error) {
	appID, id, err := s.account(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.auth.VerifyCode(ctx, appID, id, req.GetChannel(), req.GetTarget(), req.GetCode()); err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.VerifyCodeResponse{}, nil
}

func (s *userServer) BindIdentity(ctx context.Context, req *fpv1.BindIdentityRequest) (*fpv1.UserProfile, error) {
	appID, id, err := s.account(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	p, err := s.auth.BindIdentity(ctx, appID, id, req.GetType(), req.GetSubject(), req.GetCode())
	if err != nil {
		return nil, StatusFrom(err)
	}
	return userProfile(p), nil
}

func (s *userServer) UnbindIdentity(ctx context.Context, req *fpv1.UnbindIdentityRequest) (*fpv1.UserProfile, error) {
	appID, id, err := s.account(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	p, err := s.auth.UnbindIdentity(ctx, appID, id, req.GetType())
	if err != nil {
		return nil, StatusFrom(err)
	}
	return userProfile(p), nil
}

func (s *userServer) StartBindOAuth(ctx context.Context, req *fpv1.StartBindOAuthRequest) (*fpv1.StartBindOAuthResponse, error) {
	appID, id, err := s.account(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	st, err := s.auth.StartBindOAuth(ctx, service.OAuthStartInput{
		AppID: appID, ConnectorType: req.GetConnectorType(), Mode: req.GetMode(), ReturnURL: req.GetReturnUrl(),
	}, id)
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.StartBindOAuthResponse{
		State: st.State, AuthUrl: st.AuthURL, ProviderAppId: st.ProviderAppID,
		RedirectUri: st.RedirectURI, ExpiresInSec: st.ExpiresInSec,
	}, nil
}

func (s *userServer) PollBindOAuth(ctx context.Context, req *fpv1.PollBindOAuthRequest) (*fpv1.PollBindOAuthResponse, error) {
	appID, id, err := s.account(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	out, err := s.auth.PollBindOAuth(ctx, appID, id, req.GetState())
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.PollBindOAuthResponse{Result: bindOAuthResult(out)}, nil
}

func (s *userServer) ExchangeBindOAuthTicket(ctx context.Context, req *fpv1.ExchangeBindOAuthTicketRequest) (*fpv1.ExchangeBindOAuthTicketResponse, error) {
	appID, id, err := s.account(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	out, err := s.auth.ExchangeBindOAuth(ctx, appID, id, req.GetTicket())
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.ExchangeBindOAuthTicketResponse{Result: bindOAuthResult(out)}, nil
}

func (s *userServer) SetPassword(ctx context.Context, req *fpv1.SetPasswordRequest) (*fpv1.SetPasswordResponse, error) {
	appID, id, err := s.account(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := s.auth.SetPassword(ctx, appID, id, req.GetPassword()); err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.SetPasswordResponse{}, nil
}

func bindOAuthResult(out *service.BindOAuthOutcome) *fpv1.BindOAuthResult {
	r := &fpv1.BindOAuthResult{Status: out.Status, Nickname: out.Nickname, AvatarUrl: out.AvatarURL}
	if out.Profile != nil {
		r.User = userProfile(out.Profile)
	}
	return r
}

// userProfile 把资料转成传输对象。密码哈希与 identity.credential 绝不出现在响应里，
// 只暴露"是否设过密码"。
func userProfile(p *service.UserProfile) *fpv1.UserProfile {
	out := &fpv1.UserProfile{
		Id:              p.User.ID.String(),
		Nickname:        p.User.Nickname,
		AvatarHash:      p.User.AvatarHash,
		AvatarUrl:       p.User.AvatarURL,
		Gender:          p.User.Gender,
		Status:          p.User.Status,
		HasPassword:     p.User.PasswordHash != "",
		Identities:      make([]*fpv1.UserIdentity, 0, len(p.Identities)),
		CreatedAt:       p.User.CreatedAt,
		UpdatedAt:       p.User.UpdatedAt,
		RegisteredAppId: p.RegisteredAppID,
	}
	for _, i := range p.Identities {
		out.Identities = append(out.Identities, &fpv1.UserIdentity{
			Type: i.Type, Subject: i.Subject, LastLoginAt: i.LastLoginAt,
		})
	}
	return out
}
