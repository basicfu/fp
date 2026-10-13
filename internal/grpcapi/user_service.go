package grpcapi

import (
	"context"

	"github.com/google/uuid"

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
	svc  *service.UserService
	apps AppLookup
}

func newUserServer(svc *service.UserService, apps AppLookup) *userServer {
	return &userServer{svc: svc, apps: apps}
}

// authorize 是四个 RPC 共用的准入：必须是处于启用状态的业务应用。
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
	contentType, image, err := s.svc.GetAvatar(ctx, req.GetHash())
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.GetAvatarResponse{Image: image, ContentType: contentType}, nil
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
