package grpcapi

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// 只有文件头是真的：fp 不解码图片，后面的字节无所谓。
var (
	grpcTestPNG  = append([]byte("\x89PNG\r\n\x1a\n"), []byte("grpc-avatar-one")...)
	grpcTestWebP = append([]byte("RIFF\x24\x00\x00\x00WEBP"), []byte("grpc-avatar-two")...)
)

// authed 返回带这个应用凭据的 ctx。
func (a *testApplication) authed(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, mdAppID, a.appID, mdAppSecret, a.secret)
}

// newBareUser 直接在库里建一个用户：他没有在任何应用登录过。
func (e *grpcEnv) newBareUser(t *testing.T, nickname string) *domain.User {
	t.Helper()
	u, _, _, err := e.users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: randomPhone(), Nickname: nickname,
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	return u
}

func wantStatus(t *testing.T, what string, err error, code codes.Code, detailCode string) {
	t.Helper()
	if status.Code(err) != code || errorDetailCode(t, err) != detailCode {
		t.Fatalf("%s: err = %v (detail code %q), want %v / %q", what, err, errorDetailCode(t, err), code, detailCode)
	}
}

func TestGetUserReturnsFullProfileWithoutCredentials(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := context.Background()
	const phone, wxCredential = "13912345678", "wx-refresh-token-do-not-leak"

	u, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: phone, Nickname: "小明",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if _, err := e.users.AttachIdentity(ctx, u.ID, service.EnsureIdentityInput{
		Type: domain.IdentityTypeWechatMP, Subject: "openid-1", UnionKey: "union-1", Credential: wxCredential,
	}); err != nil {
		t.Fatalf("挂微信: %v", err)
	}

	p, err := e.userClient.GetUser(e.authed(ctx), &fpv1.GetUserRequest{UserId: u.ID.String()})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if p.GetId() != u.ID.String() || p.GetNickname() != "小明" || p.GetStatus() != domain.UserStatusActive {
		t.Fatalf("profile = %+v", p)
	}
	if p.GetHasPassword() {
		t.Fatal("没设过密码，has_password 应为 false")
	}
	if p.GetCreatedAt() == 0 || p.GetUpdatedAt() == 0 {
		t.Fatalf("created_at = %d, updated_at = %d，应为 unix 毫秒", p.GetCreatedAt(), p.GetUpdatedAt())
	}
	got := map[string]string{}
	for _, id := range p.GetIdentities() {
		got[id.GetType()] = id.GetSubject()
	}
	if len(got) != 2 || got[domain.IdentityTypePhone] != phone || got[domain.IdentityTypeWechatMP] != "openid-1" {
		t.Fatalf("identities = %v——标识应齐全，手机号返回完整值不脱敏", got)
	}

	if err := e.users.SetPassword(ctx, u.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	p, err = e.userClient.GetUser(e.authed(ctx), &fpv1.GetUserRequest{UserId: u.ID.String()})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !p.GetHasPassword() {
		t.Fatal("设过密码后 has_password 应为 true")
	}

	// 【辨别力】不看字段名、直接搜上线的字节：谁往 UserProfile 里加了带凭据的字段，这里就红。
	fresh, err := e.users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	wire, err := proto.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for name, secret := range map[string]string{"密码哈希": fresh.PasswordHash, "第三方凭据": wxCredential, "unionKey": "union-1"} {
		if secret == "" {
			t.Fatalf("测试前提不成立：%s 为空", name)
		}
		if bytes.Contains(wire, []byte(secret)) {
			t.Fatalf("GetUser 的响应里出现了%s", name)
		}
	}
}

// 用户全局互通：应用能查、改一个从未在它这里登录过的用户；一个应用改了，别的应用随之变化。
func TestUsersAreSharedAcrossApplications(t *testing.T) {
	e := newGRPCEnv(t)
	appB := e.newApplication(t)
	ctxA, ctxB := e.authed(context.Background()), appB.authed(context.Background())
	u := e.newBareUser(t, "原昵称")

	if _, err := e.userClient.GetUser(ctxA, &fpv1.GetUserRequest{UserId: u.ID.String()}); err != nil {
		t.Fatalf("应用 A 查一个从未在 A 登录过的用户: %v", err)
	}
	updated, err := e.userClient.UpdateUser(ctxA, &fpv1.UpdateUserRequest{UserId: u.ID.String(), Nickname: proto.String("A 改的")})
	if err != nil {
		t.Fatalf("应用 A 改昵称: %v", err)
	}
	if updated.GetNickname() != "A 改的" {
		t.Fatalf("UpdateUser 应返回改完的资料, nickname = %q", updated.GetNickname())
	}
	fromB, err := e.userClient.GetUser(ctxB, &fpv1.GetUserRequest{UserId: u.ID.String()})
	if err != nil {
		t.Fatalf("应用 B 查: %v", err)
	}
	if fromB.GetNickname() != "A 改的" {
		t.Fatalf("应用 B 看到的昵称 = %q, want A 改的新值", fromB.GetNickname())
	}

	// 头像同理：B 传的图，A 按哈希取得到。
	withAvatar, err := e.userClient.UpdateAvatar(ctxB, &fpv1.UpdateAvatarRequest{UserId: u.ID.String(), Image: grpcTestPNG})
	if err != nil {
		t.Fatalf("应用 B 传头像: %v", err)
	}
	img, err := e.userClient.GetAvatar(ctxA, &fpv1.GetAvatarRequest{Hash: withAvatar.GetAvatarHash()})
	if err != nil || !bytes.Equal(img.GetImage(), grpcTestPNG) {
		t.Fatalf("应用 A 取应用 B 传的头像: %v", err)
	}
	// 用户没有因此与任何应用产生关联。
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM user_extra WHERE user_id = $1`, u.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("查询与修改不该写 user_extra, 实际 %d 行", n)
	}
}

func TestUpdateUserOnlyTouchesProvidedFields(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := e.authed(context.Background())
	u := e.newBareUser(t, "原昵称")
	id := u.ID.String()

	p, err := e.userClient.UpdateUser(ctx, &fpv1.UpdateUserRequest{UserId: id, Gender: proto.String(domain.GenderFemale)})
	if err != nil {
		t.Fatalf("只改性别: %v", err)
	}
	if p.GetNickname() != "原昵称" || p.GetGender() != domain.GenderFemale {
		t.Fatalf("只改性别后 nickname=%q gender=%q", p.GetNickname(), p.GetGender())
	}
	p, err = e.userClient.UpdateUser(ctx, &fpv1.UpdateUserRequest{UserId: id, Nickname: proto.String(" 新昵称 ")})
	if err != nil {
		t.Fatalf("只改昵称: %v", err)
	}
	if p.GetNickname() != "新昵称" || p.GetGender() != domain.GenderFemale {
		t.Fatalf("只改昵称后 nickname=%q gender=%q", p.GetNickname(), p.GetGender())
	}
	// 显式的空串是"清空性别"，与没带这个字段不同——optional 就是为了在线上分清这两件事。
	p, err = e.userClient.UpdateUser(ctx, &fpv1.UpdateUserRequest{UserId: id, Gender: proto.String("")})
	if err != nil {
		t.Fatalf("清空性别: %v", err)
	}
	if p.GetNickname() != "新昵称" || p.GetGender() != "" {
		t.Fatalf("清空性别后 nickname=%q gender=%q", p.GetNickname(), p.GetGender())
	}
}

func TestUserServiceMapsDomainErrors(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := e.authed(context.Background())
	id := e.newBareUser(t, "小明").ID.String()
	ghost := uuid.NewString()

	_, err := e.userClient.GetUser(ctx, &fpv1.GetUserRequest{UserId: ghost})
	wantStatus(t, "查不存在的用户", err, codes.NotFound, domain.CodeUserNotFound)
	_, err = e.userClient.GetUser(ctx, &fpv1.GetUserRequest{UserId: "not-a-uuid"})
	wantStatus(t, "user_id 不是 UUID", err, codes.NotFound, domain.CodeUserNotFound)
	_, err = e.userClient.GetUser(ctx, &fpv1.GetUserRequest{})
	wantStatus(t, "user_id 为空", err, codes.NotFound, domain.CodeUserNotFound)
	_, err = e.userClient.UpdateUser(ctx, &fpv1.UpdateUserRequest{UserId: ghost, Nickname: proto.String("x")})
	wantStatus(t, "改不存在的用户", err, codes.NotFound, domain.CodeUserNotFound)
	_, err = e.userClient.UpdateAvatar(ctx, &fpv1.UpdateAvatarRequest{UserId: ghost, Image: grpcTestPNG})
	wantStatus(t, "给不存在的用户传头像", err, codes.NotFound, domain.CodeUserNotFound)

	_, err = e.userClient.UpdateUser(ctx, &fpv1.UpdateUserRequest{UserId: id, Nickname: proto.String("   ")})
	wantStatus(t, "空白昵称", err, codes.InvalidArgument, domain.CodeNicknameInvalid)
	_, err = e.userClient.UpdateUser(ctx, &fpv1.UpdateUserRequest{UserId: id, Nickname: proto.String(strings.Repeat("字", 33))})
	wantStatus(t, "超长昵称", err, codes.InvalidArgument, domain.CodeNicknameInvalid)
	_, err = e.userClient.UpdateUser(ctx, &fpv1.UpdateUserRequest{UserId: id, Gender: proto.String("OTHER")})
	wantStatus(t, "非法性别", err, codes.InvalidArgument, domain.CodeInvalidArgument)

	_, err = e.userClient.UpdateAvatar(ctx, &fpv1.UpdateAvatarRequest{UserId: id, Image: []byte("GIF89a\x01\x00\x01\x00")})
	wantStatus(t, "GIF 头像", err, codes.InvalidArgument, domain.CodeAvatarInvalid)
	tooBig := make([]byte, domain.AvatarMaxBytes+1)
	copy(tooBig, grpcTestPNG)
	_, err = e.userClient.UpdateAvatar(ctx, &fpv1.UpdateAvatarRequest{UserId: id, Image: tooBig})
	wantStatus(t, "超过 1 MB 的头像", err, codes.InvalidArgument, domain.CodeAvatarInvalid)

	_, err = e.userClient.GetAvatar(ctx, &fpv1.GetAvatarRequest{Hash: strings.Repeat("0", 32)})
	wantStatus(t, "没有这个哈希", err, codes.NotFound, domain.CodeAvatarNotFound)
	_, err = e.userClient.GetAvatar(ctx, &fpv1.GetAvatarRequest{})
	wantStatus(t, "哈希为空", err, codes.NotFound, domain.CodeAvatarNotFound)
}

func TestAvatarRoundTripOverGRPC(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := e.authed(context.Background())
	id := e.newBareUser(t, "小明").ID.String()

	// 恰好 1 MB：上限之内的图必须能完整走过 gRPC 的消息大小限制，取回时逐字节相同。
	big := make([]byte, domain.AvatarMaxBytes)
	copy(big, grpcTestPNG)
	p, err := e.userClient.UpdateAvatar(ctx, &fpv1.UpdateAvatarRequest{UserId: id, Image: big})
	if err != nil {
		t.Fatalf("UpdateAvatar(1 MB): %v", err)
	}
	bigHash := p.GetAvatarHash()
	if bigHash != domain.AvatarHash(big) {
		t.Fatalf("avatar_hash = %q, want %q", bigHash, domain.AvatarHash(big))
	}
	img, err := e.userClient.GetAvatar(ctx, &fpv1.GetAvatarRequest{Hash: bigHash})
	if err != nil {
		t.Fatalf("GetAvatar: %v", err)
	}
	if img.GetContentType() != "image/png" || !bytes.Equal(img.GetImage(), big) {
		t.Fatalf("取回 %d 字节、类型 %q——应与存入的逐字节相同", len(img.GetImage()), img.GetContentType())
	}

	// 换图：资料里是新哈希，旧哈希取不到了。
	p, err = e.userClient.UpdateAvatar(ctx, &fpv1.UpdateAvatarRequest{UserId: id, Image: grpcTestWebP})
	if err != nil {
		t.Fatalf("换头像: %v", err)
	}
	if p.GetAvatarHash() == bigHash || p.GetAvatarHash() == "" {
		t.Fatalf("换头像后 avatar_hash = %q", p.GetAvatarHash())
	}
	img, err = e.userClient.GetAvatar(ctx, &fpv1.GetAvatarRequest{Hash: p.GetAvatarHash()})
	if err != nil || img.GetContentType() != "image/webp" || !bytes.Equal(img.GetImage(), grpcTestWebP) {
		t.Fatalf("取新头像: type=%q err=%v", img.GetContentType(), err)
	}
	_, err = e.userClient.GetAvatar(ctx, &fpv1.GetAvatarRequest{Hash: bigHash})
	wantStatus(t, "按旧哈希取图", err, codes.NotFound, domain.CodeAvatarNotFound)

	// image 为空即清除。
	newHash := p.GetAvatarHash()
	p, err = e.userClient.UpdateAvatar(ctx, &fpv1.UpdateAvatarRequest{UserId: id})
	if err != nil {
		t.Fatalf("清除头像: %v", err)
	}
	if p.GetAvatarHash() != "" {
		t.Fatalf("清除后 avatar_hash = %q", p.GetAvatarHash())
	}
	_, err = e.userClient.GetAvatar(ctx, &fpv1.GetAvatarRequest{Hash: newHash})
	wantStatus(t, "清除后取图", err, codes.NotFound, domain.CodeAvatarNotFound)
}

// userServiceCalls 是 UserService 的全部四个 RPC，供准入类测试逐个打一遍。
func (e *grpcEnv) userServiceCalls(userID string) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"GetUser": func(ctx context.Context) error {
			_, err := e.userClient.GetUser(ctx, &fpv1.GetUserRequest{UserId: userID})
			return err
		},
		"UpdateUser": func(ctx context.Context) error {
			_, err := e.userClient.UpdateUser(ctx, &fpv1.UpdateUserRequest{UserId: userID, Nickname: proto.String("被改了")})
			return err
		},
		"UpdateAvatar": func(ctx context.Context) error {
			_, err := e.userClient.UpdateAvatar(ctx, &fpv1.UpdateAvatarRequest{UserId: userID, Image: grpcTestWebP})
			return err
		},
		"GetAvatar": func(ctx context.Context) error {
			_, err := e.userClient.GetAvatar(ctx, &fpv1.GetAvatarRequest{Hash: domain.AvatarHash(grpcTestPNG)})
			return err
		},
	}
}

// assertUntouched 断言被拒的调用没有留下任何改动。
func (e *grpcEnv) assertUntouched(t *testing.T, userID uuid.UUID) {
	t.Helper()
	fresh, err := e.users.GetByID(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fresh.Nickname != "小明" || fresh.AvatarHash != domain.AvatarHash(grpcTestPNG) {
		t.Fatalf("被拒的调用改动了资料: nickname=%q avatar_hash=%q", fresh.Nickname, fresh.AvatarHash)
	}
}

func TestUserServiceAuthBoundaries(t *testing.T) {
	e := newGRPCEnv(t)
	u := e.newBareUser(t, "小明")
	if _, err := e.users.UpdateAvatar(context.Background(), u.ID, grpcTestPNG); err != nil {
		t.Fatalf("准备头像: %v", err)
	}

	for name, call := range e.userServiceCalls(u.ID.String()) {
		// 先确认带应用凭据时这次调用本身是通的，否则下面的"被拒"说明不了任何事。
		if name == "GetUser" || name == "GetAvatar" {
			if err := call(e.authed(context.Background())); err != nil {
				t.Fatalf("%s 带应用凭据: %v", name, err)
			}
		}
		if err := call(context.Background()); status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s 无凭据 err = %v, want Unauthenticated", name, err)
		}
		// fp-im 网关的凭据是收窄过的子集：读不到手机号，也改不了任何人的资料。
		if err := call(e.imAuthed(context.Background(), e.appID)); status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s IM 凭据 err = %v, want PermissionDenied", name, err)
		}
	}
	e.assertUntouched(t, u.ID)
}

// 拦截器只校验 appSecret、不看应用状态；停用应用的凭据能不能读写用户，只能靠 handler 拦。
func TestUserServiceRejectsDisabledApplication(t *testing.T) {
	e := newGRPCEnv(t)
	u := e.newBareUser(t, "小明")
	if _, err := e.users.UpdateAvatar(context.Background(), u.ID, grpcTestPNG); err != nil {
		t.Fatalf("准备头像: %v", err)
	}
	e.disableApplication(t)

	for name, call := range e.userServiceCalls(u.ID.String()) {
		err := call(e.authed(context.Background()))
		if status.Code(err) != codes.PermissionDenied || errorDetailCode(t, err) != domain.CodeAppDisabled {
			t.Errorf("%s: err = %v (detail code %q), want PermissionDenied / %s", name, err, errorDetailCode(t, err), domain.CodeAppDisabled)
		}
	}
	e.assertUntouched(t, u.ID)
}

// 登录当下就能拿到头像哈希与注册来源，业务方不必为此多查一次。
func TestLoginCarriesAvatarHashAndRecordsRegistrationSource(t *testing.T) {
	e := newGRPCEnv(t)
	ctx := e.authed(context.Background())
	phone := randomPhone()

	smsLogin := func(ctx context.Context) *fpv1.LoginResponse {
		t.Helper()
		if _, err := e.client.SendLoginCode(ctx, &fpv1.SendLoginCodeRequest{Phone: phone}); err != nil {
			t.Fatalf("SendLoginCode: %v", err)
		}
		res, err := e.client.Login(ctx, &fpv1.LoginRequest{
			ConnectorType: connector.TypeSMSCode,
			Credentials:   map[string]string{"phone": phone, "code": e.sms.LastCode()},
		})
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		return res
	}

	first := smsLogin(ctx)
	if first.GetUser().GetAvatarHash() != "" {
		t.Fatalf("新用户的 avatar_hash = %q, want 空", first.GetUser().GetAvatarHash())
	}
	userID := first.GetUser().GetId()
	p, err := e.userClient.UpdateAvatar(ctx, &fpv1.UpdateAvatarRequest{UserId: userID, Image: grpcTestPNG})
	if err != nil {
		t.Fatalf("UpdateAvatar: %v", err)
	}
	if p.GetRegisteredAppId() != e.appID {
		t.Fatalf("registered_app_id = %q, want 自动建号时的应用 %q", p.GetRegisteredAppId(), e.appID)
	}

	appB := e.newApplication(t)
	second := smsLogin(appB.authed(context.Background()))
	if second.GetUser().GetId() != userID {
		t.Fatal("同一手机号登录两个应用应是同一个用户")
	}
	if got := second.GetUser().GetAvatarHash(); got != domain.AvatarHash(grpcTestPNG) {
		t.Fatalf("登录返回的 avatar_hash = %q, want %q", got, domain.AvatarHash(grpcTestPNG))
	}
	p, err = e.userClient.GetUser(appB.authed(context.Background()), &fpv1.GetUserRequest{UserId: userID})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if p.GetRegisteredAppId() != e.appID {
		t.Fatalf("登录应用 B 之后 registered_app_id = %q, want 仍是 %q", p.GetRegisteredAppId(), e.appID)
	}
}
