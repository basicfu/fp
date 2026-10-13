package integration_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	fpsdk "github.com/basicfu/fp/sdk"
)

// SDK 不能 import internal，这些值在两边是各写一份的；任何一边单独改了，build 与各自的测试都照绿。
func TestSDKUserConstantsMatchServer(t *testing.T) {
	pairs := map[string][2]string{
		"CodeUserNotFound":    {fpsdk.CodeUserNotFound, domain.CodeUserNotFound},
		"CodeNicknameInvalid": {fpsdk.CodeNicknameInvalid, domain.CodeNicknameInvalid},
		"CodeAvatarInvalid":   {fpsdk.CodeAvatarInvalid, domain.CodeAvatarInvalid},
		"CodeAvatarNotFound":  {fpsdk.CodeAvatarNotFound, domain.CodeAvatarNotFound},
		"GenderMale":          {fpsdk.GenderMale, domain.GenderMale},
		"GenderFemale":        {fpsdk.GenderFemale, domain.GenderFemale},
	}
	for name, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("%s: SDK = %q, 服务端 = %q", name, p[0], p[1])
		}
	}
	if fpsdk.AvatarMaxBytes != domain.AvatarMaxBytes {
		t.Errorf("AvatarMaxBytes: SDK = %d, 服务端 = %d", fpsdk.AvatarMaxBytes, domain.AvatarMaxBytes)
	}
}

// TestUsersEndToEnd 走完整链路：SDK → 真实监听的 gRPC 服务 → UserService → Postgres。
//
// 各层的单元测试分别覆盖了自己的行为；这条守的是层与层之间的约定——凭据随每个 RPC 带上、
// 服务端的错误码一路回到 SDK 的哨兵、登录返回里带着头像哈希。
func TestUsersEndToEnd(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	users := e.sdk.Users()
	phone := randomPhase2Phone()
	_, userID, _ := e.loginWithPhone(t, phone)
	id := userID.String()

	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("e2e-avatar")...)

	t.Run("查询：标识完整，has_password 正确", func(t *testing.T) {
		p, err := users.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if p.ID != id || p.Status != domain.UserStatusActive || !p.HasPassword || p.AvatarHash != "" {
			t.Fatalf("profile = %+v", p)
		}
		if len(p.Identities) != 1 || p.Identities[0].Type != domain.IdentityTypePhone || p.Identities[0].Subject != phone {
			t.Fatalf("identities = %+v, want 完整手机号 %s", p.Identities, phone)
		}
		if p.Identities[0].LastLoginAt.IsZero() || p.CreatedAt.IsZero() {
			t.Fatalf("登录过的标识应有 LastLoginAt，用户应有 CreatedAt: %+v", p)
		}
	})

	t.Run("改资料：只改给了的字段", func(t *testing.T) {
		name, gender := "端到端", fpsdk.GenderMale
		if _, err := users.Update(ctx, id, fpsdk.UserUpdate{Gender: &gender}); err != nil {
			t.Fatalf("Update(gender): %v", err)
		}
		p, err := users.Update(ctx, id, fpsdk.UserUpdate{Nickname: &name})
		if err != nil {
			t.Fatalf("Update(nickname): %v", err)
		}
		if p.Nickname != name || p.Gender != fpsdk.GenderMale {
			t.Fatalf("profile = %+v", p)
		}
	})

	t.Run("头像：存、登录带出哈希、取回、清除", func(t *testing.T) {
		p, err := users.UpdateAvatar(ctx, id, png)
		if err != nil {
			t.Fatalf("UpdateAvatar: %v", err)
		}
		hash := p.AvatarHash
		if hash != domain.AvatarHash(png) {
			t.Fatalf("AvatarHash = %q, want %q", hash, domain.AvatarHash(png))
		}

		res, err := e.sdk.Auth().Login(ctx, fpsdk.LoginInput{
			ConnectorType: connector.TypePassword,
			Credentials:   map[string]string{"account": phone, "password": "hunter2hunter2"},
		})
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		if res.User.GetAvatarHash() != hash {
			t.Fatalf("LoginResult.User.AvatarHash = %q, want %q", res.User.GetAvatarHash(), hash)
		}

		image, contentType, err := users.Avatar(ctx, hash)
		if err != nil || contentType != "image/png" || !bytes.Equal(image, png) {
			t.Fatalf("Avatar: type=%q err=%v 字节相同=%v", contentType, err, bytes.Equal(image, png))
		}

		p, err = users.UpdateAvatar(ctx, id, nil)
		if err != nil {
			t.Fatalf("UpdateAvatar(nil): %v", err)
		}
		if p.AvatarHash != "" {
			t.Fatalf("清除后 AvatarHash = %q", p.AvatarHash)
		}
		_, _, err = users.Avatar(ctx, hash)
		wantSDKError(t, err, fpsdk.CodeAvatarNotFound, fpsdk.ErrNotFound)
	})

	t.Run("业务错误带着码与哨兵回到调用方", func(t *testing.T) {
		_, err := users.Get(ctx, uuid.NewString())
		wantSDKError(t, err, fpsdk.CodeUserNotFound, fpsdk.ErrNotFound)
		// "查无此人"不能被当成凭据失效——业务方会因此清会话。
		if errors.Is(err, fpsdk.ErrUnauthorized) {
			t.Fatal("USER_NOT_FOUND 被归进了 ErrUnauthorized")
		}

		blank := "  "
		_, err = users.Update(ctx, id, fpsdk.UserUpdate{Nickname: &blank})
		wantSDKError(t, err, fpsdk.CodeNicknameInvalid, fpsdk.ErrInvalidArgument)

		_, err = users.UpdateAvatar(ctx, id, []byte("GIF89a\x01\x00\x01\x00"))
		wantSDKError(t, err, fpsdk.CodeAvatarInvalid, fpsdk.ErrInvalidArgument)
	})

	t.Run("IM 网关凭据不能读写用户", func(t *testing.T) {
		im := newIMClient(t, e)
		_, err := im.Users().Get(ctx, id)
		if !errors.Is(err, fpsdk.ErrForbidden) {
			t.Fatalf("Get err = %v, want ErrForbidden", err)
		}
		name := "被网关改了"
		_, err = im.Users().Update(ctx, id, fpsdk.UserUpdate{Nickname: &name})
		if !errors.Is(err, fpsdk.ErrForbidden) {
			t.Fatalf("Update err = %v, want ErrForbidden", err)
		}
		p, err := users.Get(ctx, id)
		if err != nil || p.Nickname == name {
			t.Fatalf("被拒的调用改动了昵称: %+v, %v", p, err)
		}
	})
}

func wantSDKError(t *testing.T, err error, code string, sentinel error) {
	t.Helper()
	var fe *fpsdk.Error
	if !errors.As(err, &fe) || fe.Code != code {
		t.Fatalf("err = %v, want 码 %s", err, code)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v 的哨兵不是 %v", err, sentinel)
	}
}
