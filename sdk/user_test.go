package fpsdk

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// userStub 是一个可编排的 UserService 桩：err 非 nil 时四个 RPC 都返回它，否则返回 profile / avatar，
// 并记下最后一次收到的请求。
type userStub struct {
	fpv1.UnimplementedUserServiceServer
	err     error
	profile *fpv1.UserProfile
	avatar  *fpv1.GetAvatarResponse

	calls      int
	lastUpdate *fpv1.UpdateUserRequest
	lastAvatar *fpv1.UpdateAvatarRequest
	lastHash   string
	lastUserID string
}

func (s *userStub) GetUser(_ context.Context, req *fpv1.GetUserRequest) (*fpv1.UserProfile, error) {
	s.calls++
	s.lastUserID = req.GetUserId()
	return s.profile, s.err
}

func (s *userStub) UpdateUser(_ context.Context, req *fpv1.UpdateUserRequest) (*fpv1.UserProfile, error) {
	s.calls++
	s.lastUpdate = req
	return s.profile, s.err
}

func (s *userStub) UpdateAvatar(_ context.Context, req *fpv1.UpdateAvatarRequest) (*fpv1.UserProfile, error) {
	s.calls++
	s.lastAvatar = req
	return s.profile, s.err
}

func (s *userStub) GetAvatar(_ context.Context, req *fpv1.GetAvatarRequest) (*fpv1.GetAvatarResponse, error) {
	s.calls++
	s.lastHash = req.GetHash()
	return s.avatar, s.err
}

// newUsersForTest 起一个只挂 UserService 桩的真实 gRPC 服务端，返回连着它的 Users。
// 不走 New()：New 会起 Watch 流等一堆与本测试无关的后台 goroutine。
func newUsersForTest(t *testing.T, stub *userStub) *Users {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	srv := grpc.NewServer()
	fpv1.RegisterUserServiceServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &Users{c: &Client{userRPC: fpv1.NewUserServiceClient(conn)}}
}

func TestUsersGetMapsEveryField(t *testing.T) {
	const created, updated, lastLogin = int64(1760000000123), int64(1760000111456), int64(1760000222789)
	stub := &userStub{profile: &fpv1.UserProfile{
		Id: "u-1", Nickname: "小明", AvatarHash: "0123456789abcdef0123456789abcdef", AvatarUrl: "https://wx/a.png",
		Gender: "FEMALE", Status: "ACTIVE", HasPassword: true,
		Identities: []*fpv1.UserIdentity{
			{Type: "phone", Subject: "13800138000", LastLoginAt: lastLogin},
			{Type: "email", Subject: "ming@example.com"},
		},
		CreatedAt: created, UpdatedAt: updated, RegisteredAppId: "app-a",
	}}
	u := newUsersForTest(t, stub)

	p, err := u.Get(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stub.lastUserID != "u-1" {
		t.Fatalf("发出去的 user_id = %q", stub.lastUserID)
	}
	if p.ID != "u-1" || p.Nickname != "小明" || p.AvatarHash != "0123456789abcdef0123456789abcdef" ||
		p.AvatarURL != "https://wx/a.png" || p.Gender != GenderFemale || p.Status != "ACTIVE" ||
		!p.HasPassword || p.RegisteredAppID != "app-a" {
		t.Fatalf("profile = %+v", p)
	}
	if !p.CreatedAt.Equal(time.UnixMilli(created)) || !p.UpdatedAt.Equal(time.UnixMilli(updated)) {
		t.Fatalf("CreatedAt = %v, UpdatedAt = %v", p.CreatedAt, p.UpdatedAt)
	}
	if len(p.Identities) != 2 {
		t.Fatalf("identities = %+v", p.Identities)
	}
	if id := p.Identities[0]; id.Type != "phone" || id.Subject != "13800138000" || !id.LastLoginAt.Equal(time.UnixMilli(lastLogin)) {
		t.Fatalf("identities[0] = %+v", id)
	}
	// 从未用它登录过：零值，而不是 1970 年。
	if id := p.Identities[1]; id.Type != "email" || !id.LastLoginAt.IsZero() {
		t.Fatalf("identities[1] = %+v，LastLoginAt 应为零值", id)
	}
}

// nil 的字段在线上必须是"没带"，而不是空串——服务端靠这个区分"不改"与"清空"。
func TestUsersUpdateSendsOnlyProvidedFields(t *testing.T) {
	stub := &userStub{profile: &fpv1.UserProfile{Id: "u-1", Nickname: "新昵称"}}
	u := newUsersForTest(t, stub)
	ctx := context.Background()
	name, empty := "新昵称", ""

	p, err := u.Update(ctx, "u-1", UserUpdate{Nickname: &name})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if p.Nickname != "新昵称" {
		t.Fatalf("Update 应返回改完的资料, got %+v", p)
	}
	if req := stub.lastUpdate; req.GetUserId() != "u-1" || req.Nickname == nil || req.GetNickname() != name || req.Gender != nil {
		t.Fatalf("只给昵称时的请求 = %v——gender 不该出现在线上", req)
	}

	if _, err := u.Update(ctx, "u-1", UserUpdate{Gender: &empty}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if req := stub.lastUpdate; req.Nickname != nil || req.Gender == nil || req.GetGender() != "" {
		t.Fatalf("清空性别时的请求 = %v——gender 应显式带上空串，nickname 不该出现", req)
	}

	if _, err := u.Update(ctx, "u-1", UserUpdate{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if req := stub.lastUpdate; req.Nickname != nil || req.Gender != nil {
		t.Fatalf("空更新的请求 = %v", req)
	}
}

func TestUsersUpdateAvatarAndClear(t *testing.T) {
	stub := &userStub{profile: &fpv1.UserProfile{Id: "u-1", AvatarHash: "0123456789abcdef0123456789abcdef"}}
	u := newUsersForTest(t, stub)
	ctx := context.Background()
	image := []byte("\x89PNG\r\n\x1a\nwhatever")

	p, err := u.UpdateAvatar(ctx, "u-1", image)
	if err != nil {
		t.Fatalf("UpdateAvatar: %v", err)
	}
	if p.AvatarHash != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("UpdateAvatar 应返回带新哈希的资料, got %+v", p)
	}
	if req := stub.lastAvatar; req.GetUserId() != "u-1" || !bytes.Equal(req.GetImage(), image) {
		t.Fatal("图片应原样发给 fp")
	}

	// nil 即清除：请求照发，image 为空。
	stub.profile = &fpv1.UserProfile{Id: "u-1"}
	p, err = u.UpdateAvatar(ctx, "u-1", nil)
	if err != nil {
		t.Fatalf("UpdateAvatar(nil): %v", err)
	}
	if p.AvatarHash != "" {
		t.Fatalf("清除后 AvatarHash = %q", p.AvatarHash)
	}
	if stub.calls != 2 || len(stub.lastAvatar.GetImage()) != 0 || stub.lastAvatar.GetUserId() != "u-1" {
		t.Fatalf("UpdateAvatar(nil) 应发出一个 image 为空的请求: calls=%d req=%v", stub.calls, stub.lastAvatar)
	}
}

// 超过上限的图不该发出去：超过 gRPC 消息上限时传输层会直接拒掉，拿不到 fp 的错误码。
func TestUsersUpdateAvatarRejectsOversizeLocally(t *testing.T) {
	stub := &userStub{profile: &fpv1.UserProfile{}}
	u := newUsersForTest(t, stub)
	ctx := context.Background()

	_, err := u.UpdateAvatar(ctx, "u-1", make([]byte, AvatarMaxBytes+1))
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeAvatarInvalid || !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want AVATAR_INVALID / ErrInvalidArgument", err)
	}
	if stub.calls != 0 {
		t.Fatalf("超限的图不该发出去, calls = %d", stub.calls)
	}
	// 恰好在上限上的照发，由 fp 判定。
	if _, err := u.UpdateAvatar(ctx, "u-1", make([]byte, AvatarMaxBytes)); err != nil {
		t.Fatalf("恰好 1 MB: %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("恰好 1 MB 的图应发给 fp, calls = %d", stub.calls)
	}
}

func TestUsersAvatar(t *testing.T) {
	image := []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}
	stub := &userStub{avatar: &fpv1.GetAvatarResponse{Image: image, ContentType: "image/jpeg"}}
	u := newUsersForTest(t, stub)

	got, contentType, err := u.Avatar(context.Background(), "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("Avatar: %v", err)
	}
	if stub.lastHash != "0123456789abcdef0123456789abcdef" || contentType != "image/jpeg" || !bytes.Equal(got, image) {
		t.Fatalf("hash=%q contentType=%q image=%v", stub.lastHash, contentType, got)
	}
}

// 错误码到哨兵的映射。NotFound / PermissionDenied 在这里是业务结论，
// 绝不能像 Login 那侧一样归进 ErrUnauthorized——业务方会因此清会话、跳登录页。
func TestUsersErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		code   string // 空串表示没有结构化错误
		wantIs error
	}{
		{"用户不存在", detailed(codes.NotFound, "USER_NOT_FOUND", "用户不存在"), CodeUserNotFound, ErrNotFound},
		{"头像不存在", detailed(codes.NotFound, "AVATAR_NOT_FOUND", "头像不存在"), CodeAvatarNotFound, ErrNotFound},
		{"昵称不合规", detailed(codes.InvalidArgument, "NICKNAME_INVALID", "昵称长度应为 1–32 个字符"), CodeNicknameInvalid, ErrInvalidArgument},
		{"头像不合规", detailed(codes.InvalidArgument, "AVATAR_INVALID", "头像只支持 JPEG、PNG、WebP 图片"), CodeAvatarInvalid, ErrInvalidArgument},
		{"性别不合规", detailed(codes.InvalidArgument, "INVALID_ARGUMENT", "性别只能是 MALE、FEMALE 或留空"), "INVALID_ARGUMENT", ErrInvalidArgument},
		{"应用凭据无效", detailed(codes.Unauthenticated, "APP_CREDENTIAL_INVALID", "无效"), "APP_CREDENTIAL_INVALID", ErrUnauthorized},
		{"应用已停用", detailed(codes.PermissionDenied, "APP_DISABLED", "应用已停用"), "APP_DISABLED", ErrForbidden},
		{"IM 凭据", status.Error(codes.PermissionDenied, "IM 网关凭据不能调用该接口"), "", ErrForbidden},
		{"fp 不可达", status.Error(codes.Unavailable, "down"), "", ErrUnavailable},
		{"fp 内部错误", status.Error(codes.Internal, "服务器内部错误"), "", ErrUnavailable},
		{"老服务端没有这个服务", status.Error(codes.Unimplemented, "unknown service"), "", nil},
	}
	sentinels := []error{ErrNotFound, ErrInvalidArgument, ErrUnauthorized, ErrForbidden, ErrUnavailable, ErrRateLimited}

	for _, c := range cases {
		stub := &userStub{err: c.err}
		u := newUsersForTest(t, stub)
		ctx := context.Background()
		name := "x"

		_, getErr := u.Get(ctx, "u-1")
		_, updateErr := u.Update(ctx, "u-1", UserUpdate{Nickname: &name})
		_, avatarErr := u.UpdateAvatar(ctx, "u-1", []byte("img"))
		_, _, downloadErr := u.Avatar(ctx, "h")

		for method, err := range map[string]error{"Get": getErr, "Update": updateErr, "UpdateAvatar": avatarErr, "Avatar": downloadErr} {
			if err == nil {
				t.Errorf("%s/%s: 应返回错误", c.name, method)
				continue
			}
			var fe *Error
			if c.code != "" {
				if !errors.As(err, &fe) || fe.Code != c.code || fe.Msg == "" {
					t.Errorf("%s/%s: err = %v, want 结构化错误 %s", c.name, method, err, c.code)
				}
			} else if errors.As(err, &fe) {
				t.Errorf("%s/%s: 不该凭空造出结构化错误: %+v", c.name, method, fe)
			}
			for _, s := range sentinels {
				if got, want := errors.Is(err, s), s == c.wantIs; got != want {
					t.Errorf("%s/%s: errors.Is(%v) = %v, want %v", c.name, method, s, got, want)
				}
			}
			// 原始的 gRPC status 仍然取得到，排查连接层问题时要用。
			if status.Code(err) != status.Code(c.err) {
				t.Errorf("%s/%s: status.Code = %v, want %v", c.name, method, status.Code(err), status.Code(c.err))
			}
		}
		if stub.calls != 4 {
			t.Errorf("%s: calls = %d, want 4（不重试）", c.name, stub.calls)
		}
	}
}
