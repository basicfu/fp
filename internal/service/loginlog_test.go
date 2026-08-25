package service_test

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestMaskSubject(t *testing.T) {
	tests := []struct {
		typ, in, want string
	}{
		{domain.IdentityTypePhone, "13800138000", "138****8000"},
		{domain.IdentityTypeEmail, "alice@example.com", "a****e@example.com"},
		{domain.IdentityTypeEmail, "a@example.com", "*@example.com"},
		{domain.IdentityTypeUsername, "alice", "al***"},
		{domain.IdentityTypeUsername, "ab", "**"},
		{domain.IdentityTypePhone, "", ""},
		{domain.IdentityTypeWechatMP, "openid-xyz", "op********"},
	}
	for _, tt := range tests {
		if got := service.MaskSubject(tt.typ, tt.in); got != tt.want {
			t.Errorf("MaskSubject(%q, %q) = %q, want %q", tt.typ, tt.in, got, tt.want)
		}
	}
}

func TestLoginLogWriteAndList(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	logs := service.NewLoginLogService(pool)
	users := service.NewUserService(pool)
	ctx := context.Background()

	u, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := logs.Write(ctx, domain.LoginLog{
			UserID:       &u.ID,
			IdentityType: domain.IdentityTypePhone,
			Subject:      "138****8000",
			Event:        domain.LoginEventLogin,
			Success:      true,
			IP:           "1.2.3.4",
			UA:           "go-test",
		}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	list, err := logs.ListByUser(ctx, u.ID, 10)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	if list[0].Subject != "138****8000" || !list[0].Success {
		t.Fatalf("log = %+v", list[0])
	}
}

// 失败的登录没有 user_id，也必须能写进去。
func TestLoginLogWriteWithoutUser(t *testing.T) {
	logs := service.NewLoginLogService(testsupport.NewTestDB(t))
	if err := logs.Write(context.Background(), domain.LoginLog{
		IdentityType: domain.IdentityTypePhone,
		Subject:      "138****8000",
		Event:        domain.LoginEventLogin,
		Success:      false,
		Reason:       "账号或密码不正确",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
}
