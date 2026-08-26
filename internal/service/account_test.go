package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func newAccountEnv(t *testing.T) (*service.AccountService, *service.UserService, *service.SessionService, *service.ApplicationService) {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	users := service.NewUserService(pool)
	sessions := service.NewSessionService(store.NewSessionStore(rdb), store.NewRevokePublisher(rdb))
	return service.NewAccountService(users, sessions), users, sessions, service.NewApplicationService(pool)
}

// 冻结必须连带作废会话——Validate 不看用户状态，撤销就是唯一的执行点。
func TestAccountSetStatusRevokesSessionsWhenNotLoginable(t *testing.T) {
	accounts, users, sessions, apps := newAccountEnv(t)
	ctx := context.Background()

	app, _, err := apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	u, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	sess, err := sessions.Issue(ctx, service.IssueInput{UserID: u.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if _, err := accounts.SetStatus(ctx, u.ID, domain.UserStatusFrozen); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := sessions.Validate(ctx, sess.Token, app); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("冻结后会话仍有效, err = %v", err)
	}
}

// 迁移到仍可登录的状态时不该误伤会话。
func TestAccountSetStatusKeepsSessionsWhenStillLoginable(t *testing.T) {
	accounts, users, sessions, apps := newAccountEnv(t)
	ctx := context.Background()

	app, _, err := apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	u, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	sess, err := sessions.Issue(ctx, service.IssueInput{UserID: u.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// ACTIVE → PENDING_DELETE 仍然可登录（保护期内登录会撤销注销）
	if _, err := accounts.SetStatus(ctx, u.ID, domain.UserStatusPendingDelete); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := sessions.Validate(ctx, sess.Token, app); err != nil {
		t.Fatalf("注销保护期内的会话被误伤: %v", err)
	}
}

// 改密必须作废全部会话，否则"密码泄露就改密码"等于没做。
func TestAccountResetPasswordRevokesSessions(t *testing.T) {
	accounts, users, sessions, apps := newAccountEnv(t)
	ctx := context.Background()

	app, _, err := apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	u, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	sess, err := sessions.Issue(ctx, service.IssueInput{UserID: u.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := accounts.ResetPassword(ctx, u.ID, "newpassword123"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if _, err := sessions.Validate(ctx, sess.Token, app); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("改密后旧会话仍有效, err = %v", err)
	}
	if err := users.VerifyPassword(ctx, u.ID, "newpassword123"); err != nil {
		t.Fatalf("新密码不可用: %v", err)
	}
}

// 密码不合法时不得作废会话——操作整体失败，不该留下副作用。
func TestAccountResetPasswordDoesNotRevokeOnInvalidPassword(t *testing.T) {
	accounts, users, sessions, apps := newAccountEnv(t)
	ctx := context.Background()

	app, _, err := apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	u, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	sess, err := sessions.Issue(ctx, service.IssueInput{UserID: u.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := accounts.ResetPassword(ctx, u.ID, "short"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	if _, err := sessions.Validate(ctx, sess.Token, app); err != nil {
		t.Fatalf("密码校验失败时不该撤销会话: %v", err)
	}
}
