package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

// accountEnv 是 AccountService 的一套完整依赖，测试按需取用。
//
// sessions 用可控的假时钟驱动（而不是 newAccountEnv 早先版本里的真实时钟）：
// 纪元相关的用例需要在"签发"与"校验"之间推进时间来触发轮换边界
// （见 session_epoch_test.go 的 TestRotationInheritsEpoch），本包既有的
// account_test.go 用例都不依赖真实时间流逝，切换到假时钟对它们零影响。
type accountEnv struct {
	accounts *service.AccountService
	users    *service.UserService
	sessions *service.SessionService
	apps     *service.ApplicationService
	logs     *service.LoginLogService
	epochs   *store.EpochStore
	// app 是一个不落库的测试应用，供不需要真实 DB 应用记录的用例直接用。
	app   *domain.Application
	clock *fakeClock
}

func newAccountEnv(t *testing.T) *accountEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	users := service.NewUserService(pool)
	epochs := store.NewEpochStore(rdb)
	clock := newFakeClock(time.Now().UnixMilli())
	sessions := service.NewSessionServiceWithClock(
		store.NewSessionStore(rdb), store.NewRevokePublisher(rdb), epochs, clock.Now)
	logs := service.NewLoginLogService(pool)
	return &accountEnv{
		accounts: service.NewAccountService(users, sessions, epochs, logs),
		users:    users,
		sessions: sessions,
		apps:     service.NewApplicationService(pool),
		logs:     logs,
		epochs:   epochs,
		app:      testApp(),
		clock:    clock,
	}
}

// newActiveUser 建一个状态为 ACTIVE 的新用户。每次调用用一个随机登录标识，
// 这样测试里循环建多个用户不会因 identity 撞车而互相覆盖成同一行。
func (e *accountEnv) newActiveUser(t *testing.T) *domain.User {
	t.Helper()
	return newActiveUserWith(t, e.users)
}

// appWithPolicy 造一个不落库的应用，套用给定的会话策略改动。
// 与 e.app 的区别是每次调用都返回一个独立实例，互不干扰。
func (e *accountEnv) appWithPolicy(t *testing.T, mutate func(*domain.SessionPolicy)) *domain.Application {
	t.Helper()
	return testApp(mutate)
}

// newActiveUserWith 是 accountEnv.newActiveUser 与 sessionEnv.newActiveUser
// 共用的实现：两者都需要"每次调用给出一个全新的、状态 ACTIVE 的用户"，
// 分别各写一份的话，一份改了行为另一份很容易忘记同步。
func newActiveUserWith(t *testing.T, users *service.UserService) *domain.User {
	t.Helper()
	u, _, _, err := users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("newActiveUser: %v", err)
	}
	return u
}

// seedUserWithSession 建一个用户并给它签发一个会话，返回两者。
func (e *accountEnv) seedUserWithSession(t *testing.T) (*domain.User, *domain.Application, *domain.Session) {
	t.Helper()
	ctx := context.Background()

	app, _, err := e.apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	u, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	sess, err := e.sessions.Issue(ctx, service.IssueInput{UserID: u.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return u, app, sess
}

// revokeLogs 返回该用户名下全部 revoke 事件的审计记录。
func (e *accountEnv) revokeLogs(t *testing.T, userID uuid.UUID) []domain.LoginLog {
	t.Helper()
	all, err := e.logs.ListByUser(context.Background(), userID, 50)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	out := []domain.LoginLog{}
	for _, l := range all {
		if l.Event == domain.LoginEventRevoke {
			out = append(out, l)
		}
	}
	return out
}

// 冻结必须连带作废会话——Validate 不看用户状态，撤销就是唯一的执行点。
func TestAccountSetStatusRevokesSessionsWhenNotLoginable(t *testing.T) {
	e := newAccountEnv(t)
	ctx := context.Background()
	u, app, sess := e.seedUserWithSession(t)

	if _, err := e.accounts.SetStatus(ctx, u.ID, domain.UserStatusFrozen); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := e.sessions.Validate(ctx, sess.Token, app); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("冻结后会话仍有效, err = %v", err)
	}
}

// 迁移到仍可登录的状态时不该误伤会话。
func TestAccountSetStatusKeepsSessionsWhenStillLoginable(t *testing.T) {
	e := newAccountEnv(t)
	ctx := context.Background()
	u, app, sess := e.seedUserWithSession(t)

	// ACTIVE → PENDING_DELETE 仍然可登录（保护期内登录会撤销注销）
	if _, err := e.accounts.SetStatus(ctx, u.ID, domain.UserStatusPendingDelete); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := e.sessions.Validate(ctx, sess.Token, app); err != nil {
		t.Fatalf("注销保护期内的会话被误伤: %v", err)
	}
	// 没有撤销就不该有撤销审计
	if got := e.revokeLogs(t, u.ID); len(got) != 0 {
		t.Fatalf("没发生撤销却写了 %d 条撤销审计: %+v", len(got), got)
	}
}

// 改密必须作废全部会话，否则"密码泄露就改密码"等于没做。
func TestAccountResetPasswordRevokesSessions(t *testing.T) {
	e := newAccountEnv(t)
	ctx := context.Background()
	u, app, sess := e.seedUserWithSession(t)

	if err := e.accounts.ResetPassword(ctx, u.ID, "newpassword123"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if _, err := e.sessions.Validate(ctx, sess.Token, app); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("改密后旧会话仍有效, err = %v", err)
	}
	if err := e.users.VerifyPassword(ctx, u.ID, "newpassword123"); err != nil {
		t.Fatalf("新密码不可用: %v", err)
	}
}

// 密码不合法时不得作废会话——操作整体失败，不该留下副作用。
func TestAccountResetPasswordDoesNotRevokeOnInvalidPassword(t *testing.T) {
	e := newAccountEnv(t)
	ctx := context.Background()
	u, app, sess := e.seedUserWithSession(t)

	if err := e.accounts.ResetPassword(ctx, u.ID, "short"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	if _, err := e.sessions.Validate(ctx, sess.Token, app); err != nil {
		t.Fatalf("密码校验失败时不该撤销会话: %v", err)
	}
	if got := e.revokeLogs(t, u.ID); len(got) != 0 {
		t.Fatalf("操作失败却写了 %d 条撤销审计: %+v", len(got), got)
	}
}

// 冻结、改密、踢下线三种管理员发起的撤销都必须留下审计记录。
//
// 没有它们，审计表在"最后一次登录"和"下一次登录"之间是彻底空白的：
// 管理员冻了号、改了密码、踢光了全部设备，事后一条都查不到——而
// domain 里 LoginEventRevoke 这个常量早就定义好了，一直没人写入。
func TestAccountRevocationsWriteAuditLog(t *testing.T) {
	tests := []struct {
		name       string
		act        func(t *testing.T, e *accountEnv, u *domain.User, sess *domain.Session)
		wantReason string
		wantSessID func(sess *domain.Session) string
	}{
		{
			name: "冻结",
			act: func(t *testing.T, e *accountEnv, u *domain.User, _ *domain.Session) {
				if _, err := e.accounts.SetStatus(context.Background(), u.ID, domain.UserStatusFrozen); err != nil {
					t.Fatalf("SetStatus: %v", err)
				}
			},
			wantReason: domain.RevokeReasonFreeze,
			wantSessID: func(*domain.Session) string { return "" },
		},
		{
			name: "改密",
			act: func(t *testing.T, e *accountEnv, u *domain.User, _ *domain.Session) {
				if err := e.accounts.ResetPassword(context.Background(), u.ID, "newpassword123"); err != nil {
					t.Fatalf("ResetPassword: %v", err)
				}
			},
			wantReason: domain.RevokeReasonPasswordChanged,
			wantSessID: func(*domain.Session) string { return "" },
		},
		{
			name: "踢下线全部设备",
			act: func(t *testing.T, e *accountEnv, u *domain.User, _ *domain.Session) {
				if _, err := e.accounts.RevokeAllSessions(context.Background(), u.ID); err != nil {
					t.Fatalf("RevokeAllSessions: %v", err)
				}
			},
			wantReason: domain.RevokeReasonKick,
			wantSessID: func(*domain.Session) string { return "" },
		},
		{
			name: "踢下线单台设备",
			act: func(t *testing.T, e *accountEnv, u *domain.User, sess *domain.Session) {
				n, err := e.accounts.RevokeSession(context.Background(), u.ID, sess.ID)
				if err != nil {
					t.Fatalf("RevokeSession: %v", err)
				}
				if n != 1 {
					t.Fatalf("撤销数 = %d, want 1", n)
				}
			},
			wantReason: domain.RevokeReasonKick,
			// 踢单台设备时必须记下是哪一台，否则审计只能看出"踢过"
			wantSessID: func(sess *domain.Session) string { return sess.ID },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAccountEnv(t)
			u, _, sess := e.seedUserWithSession(t)

			tt.act(t, e, u, sess)

			got := e.revokeLogs(t, u.ID)
			if len(got) != 1 {
				t.Fatalf("撤销审计记录数 = %d, want 1", len(got))
			}
			log := got[0]
			if !log.Success {
				t.Fatalf("撤销审计 Success = false: %+v", log)
			}
			if log.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", log.Reason, tt.wantReason)
			}
			if want := tt.wantSessID(sess); log.SessionID != want {
				t.Fatalf("SessionID = %q, want %q", log.SessionID, want)
			}
			if log.UserID == nil || *log.UserID != u.ID {
				t.Fatalf("UserID = %v, want %v", log.UserID, u.ID)
			}
		})
	}
}

// 踢一个不属于该用户的会话什么都没发生，就不该留下审计——
// 否则任意 sessionID 都能往别人的审计里塞一条假记录。
func TestAccountRevokeSessionOfAnotherUserWritesNoAuditLog(t *testing.T) {
	e := newAccountEnv(t)
	u, _, _ := e.seedUserWithSession(t)

	n, err := e.accounts.RevokeSession(context.Background(), u.ID, "not-this-users-session")
	if err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if n != 0 {
		t.Fatalf("撤销数 = %d, want 0", n)
	}
	if got := e.revokeLogs(t, u.ID); len(got) != 0 {
		t.Fatalf("什么都没撤销却写了 %d 条审计: %+v", len(got), got)
	}
}
