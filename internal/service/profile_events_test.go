package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/objstore"
	"github.com/basicfu/fp/internal/service"
)

func profileEvents(t *testing.T, env *appEventEnv, app, user uuid.UUID) int {
	t.Helper()
	got, err := env.events.Pending(context.Background(), app, 0, 1000)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	n := 0
	for _, ev := range got {
		if ev.UserID == user && ev.Kind == domain.EventProfileChanged {
			n++
		}
	}
	return n
}

// 每种改资料的操作成功后各写一条 profile_changed，失败的不写：业务方靠它让同账号别的设备即时跟上。
func TestProfileChangesAppendProfileChanged(t *testing.T) {
	env := newAppEventEnv(t)
	ctx := context.Background()
	app := env.newApp(t, true)
	users := service.NewUserService(env.pool, service.WithObjectStore(objstore.NewMemory()))
	users.SetProfileEvents(env.events)
	u, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "137" + uuid.NewString()[:8], Nickname: "",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if n := profileEvents(t, env, app.ID, u.ID); n != 0 {
		t.Fatalf("建号不该写资料事件，写了 %d 条", n)
	}

	nick := "新名字"
	bad := "\x00"
	email := uuid.NewString()[:8] + "@example.com"
	want := 0
	for _, c := range []struct {
		name string
		ok   bool
		do   func() error
	}{
		{"补昵称", true, func() error { _, err := users.FillEmptyNickname(ctx, u.ID, "微信名"); return err }},
		{"已有昵称不补", false, func() error { _, err := users.FillEmptyNickname(ctx, u.ID, "又一个"); return err }},
		{"改昵称", true, func() error { _, err := users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{Nickname: &nick}); return err }},
		{"昵称不合法", false, func() error { _, _ = users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{Nickname: &bad}); return nil }},
		{"换头像", true, func() error { _, err := users.UpdateAvatar(ctx, u.ID, testPNG); return err }},
		{"设密码", true, func() error { return users.SetPassword(ctx, u.ID, "abc12345") }},
		{"绑邮箱", true, func() error { _, err := users.ReplaceIdentity(ctx, u.ID, domain.IdentityTypeEmail, email); return err }},
		{"解绑邮箱", true, func() error { return users.RemoveIdentities(ctx, u.ID, domain.IdentityTypeEmail) }},
		{"没绑的解绑", false, func() error { _ = users.RemoveIdentities(ctx, u.ID, domain.IdentityTypeEmail); return nil }},
	} {
		if err := c.do(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.ok {
			want++
		}
		if n := profileEvents(t, env, app.ID, u.ID); n != want {
			t.Fatalf("%s 之后有 %d 条 profile_changed，期望 %d", c.name, n, want)
		}
	}
}
