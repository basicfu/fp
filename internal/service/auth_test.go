package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

// authEnv 是一套完整装配好的登录环境，测试用它跑端到端流程。
type authEnv struct {
	auth     *service.AuthService
	apps     *service.ApplicationService
	users    *service.UserService
	accounts *service.AccountService
	sess     *service.SessionService
	logs     *service.LoginLogService
	sms      *notify.FakeProvider
	codes    *notify.CodeService
	app      *domain.Application
	// pool 用于少数需要绕过 service 层直接改库的用例（例如制造"应用已停用"这种
	// 第一阶段还没有管理接口可以到达的状态）。
	pool *pgxpool.Pool
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	return newAuthEnvWithClock(t, func() int64 { return time.Now().UnixMilli() })
}

// newAuthEnvWithClock 用给定的时钟构造 SessionService。
//
// 时钟是这套代码里唯一一个"能精确插在某个内部步骤上"的可注入依赖，
// 用它当同步点可以确定地复现并发交错，不必去赌真实 goroutine 的时序——
// session_revoke_test.go 的 TestRevokeAnnounceSurvivesCtxCancellation 是同一手法。
func newAuthEnvWithClock(t *testing.T, now func() int64) *authEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	users := service.NewUserService(pool)
	epochs := store.NewEpochStore(rdb)
	sessions := service.NewSessionServiceWithClock(
		store.NewSessionStore(rdb), store.NewRevokePublisher(rdb), epochs, now)
	logs := service.NewLoginLogService(pool)
	codes := notify.NewCodeService(rdb)

	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(users)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	if err := reg.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}
	apps := service.NewApplicationService(pool, reg)

	sms := notify.NewFakeProvider(notify.ChannelSMS, "fake")
	// 显式关闭频率限制（[]RateRule{} 而不是 nil——nil 会套用默认的
	// "30 秒 1 条"）：下面好几个用例要对同一个手机号连发几次验证码，
	// 真实限制会把它们卡死。要测限制本身的用例自己传规则进来。
	sender := notify.NewSender(pool, store.NewRateLimiter(rdb), []notify.RateRule{})
	sender.AddProvider(sms)

	app, _, err := apps.Create(context.Background(), "测试应用", "test-app")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	ctx := context.Background()
	for _, typ := range []string{connector.TypePassword, connector.TypeSMSCode} {
		if err := apps.SetConnector(ctx, app.ID, typ, true, nil); err != nil {
			t.Fatalf("启用 %s: %v", typ, err)
		}
	}

	return &authEnv{
		auth: service.NewAuthService(service.AuthDeps{
			Apps: apps, Users: users, Sessions: sessions, Logs: logs,
			Registry: reg, Notifier: sender, Codes: codes,
		}),
		apps: apps, users: users, sess: sessions, logs: logs,
		accounts: service.NewAccountService(users, sessions, epochs, logs),
		sms:      sms, codes: codes, app: app, pool: pool,
	}
}

func (e *authEnv) smsLogin(t *testing.T, phone string) *service.LoginResult {
	t.Helper()
	ctx := context.Background()
	if err := e.auth.SendLoginCode(ctx, e.app.AppID, phone); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	code := e.sms.LastParam("code")
	if code == "" {
		t.Fatal("未从假供应商取到验证码")
	}
	res, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": phone, "code": code},
		IP:            "1.2.3.4", UA: "go-test",
	})
	if err != nil {
		t.Fatalf("Login(sms_code): %v", err)
	}
	return res
}

func TestSMSCodeLoginCreatesUser(t *testing.T) {
	e := newAuthEnv(t)
	res := e.smsLogin(t, "13800138000")

	if res.User == nil || res.Session == nil {
		t.Fatal("返回结果不完整")
	}
	if res.Session.Token == "" {
		t.Fatal("token 为空")
	}
	if res.User.Status != domain.UserStatusActive {
		t.Fatalf("Status = %q", res.User.Status)
	}

	// 签发的 token 立刻可用
	vr, err := e.auth.ValidateToken(context.Background(), e.app.AppID, res.Session.Token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if vr.Session.UserID != res.User.ID {
		t.Fatal("token 指向的用户不对")
	}
	if vr.CacheTTL <= 0 {
		t.Fatalf("CacheTTL = %v", vr.CacheTTL)
	}
}

// 端到端验证账号归并：短信建的号，设完密码后用手机号+密码登录，必须是同一个人。
func TestPasswordAndSMSLoginResolveToSameUser(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	first := e.smsLogin(t, "13800138000")
	if err := e.users.SetPassword(ctx, first.User.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	second, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "13800138000", "password": "hunter2hunter2"},
	})
	if err != nil {
		t.Fatalf("Login(password): %v", err)
	}
	if second.User.ID != first.User.ID {
		t.Fatalf("归并失败: %v vs %v", second.User.ID, first.User.ID)
	}
	if second.Session.Token == first.Session.Token {
		t.Fatal("两次登录应签发不同的 token")
	}
}

// 密码登录不建号：账号不存在时必须拒绝，而不是悄悄注册一个。
func TestPasswordLoginDoesNotCreateUser(t *testing.T) {
	e := newAuthEnv(t)
	_, err := e.auth.Login(context.Background(), service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "13800138000", "password": "hunter2hunter2"},
	})
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

// 应用没启用的登录方式必须在校验凭据之前就被拒绝。
func TestLoginRejectsDisabledConnector(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypePassword, false, nil); err != nil {
		t.Fatalf("停用 password: %v", err)
	}
	_, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "13800138000", "password": "x"},
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// 停用登录方式后，一次被拒绝的登录尝试不能消费掉验证码——这个断言比
// "返回 ErrForbidden" 更严格：两种实现（开关检查在 Authenticate 之前或之后）
// 在被拒绝这件事上表现一致，只有验证码是否被消费能区分它们。
func TestDisabledConnectorRejectsBeforeConsumingCode(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	// 先在启用状态下拿到一个真实验证码
	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	code := e.sms.LastParam("code")
	if code == "" {
		t.Fatal("未取到验证码")
	}

	// 再停用该登录方式
	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, false, nil); err != nil {
		t.Fatalf("停用 sms_code: %v", err)
	}

	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": code},
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}

	// 关键断言：验证码必须**没有被消费**。
	// 重新启用后它应当仍然可用——若已被消费，说明 Authenticate 被调用了，
	// 也就是说开关检查发生在凭据校验之后。
	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, true, nil); err != nil {
		t.Fatalf("重新启用 sms_code: %v", err)
	}
	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": code},
	}); err != nil {
		t.Fatalf("验证码在被拒绝的那次登录中已被消费——说明应用级开关检查"+
			"发生在 Authenticate 之后，顺序错了: %v", err)
	}
}

func TestLoginRejectsUnconfiguredConnector(t *testing.T) {
	e := newAuthEnv(t)
	_, err := e.auth.Login(context.Background(), service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: "wechat_mp", // 已注册但该应用未配置
		Credentials:   connector.Credentials{},
	})
	if !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrForbidden 或 ErrNotFound", err)
	}
}

func TestLoginRejectsUnknownApplication(t *testing.T) {
	e := newAuthEnv(t)
	_, err := e.auth.Login(context.Background(), service.LoginInput{
		AppID:         "no-such-app",
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "a", "password": "b"},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestLoginRejectsFrozenUser(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")
	if _, err := e.users.SetStatus(ctx, res.User.ID, domain.UserStatusFrozen); err != nil {
		t.Fatalf("冻结: %v", err)
	}

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	_, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": e.sms.LastParam("code")},
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// 在"状态检查通过"和"会话写入"之间提交的冻结，必须把这次登录拦下来。
//
// 这是 TestLoginRejectsFrozenUser 盖不到的那一半：那条用例是严格串行的，
// 冻结发生在登录开始之前。真正危险的是下面这个交错——
//
//	T1（登录）                        T2（管理员冻结）
//	CanLogin() -> true
//	                                  SetStatus -> FROZEN 已提交
//	                                  RevokeUser -> 此刻一个 token 都枚举不到
//	Sessions.Issue -> 写入新 token
//
// 冻结接口返回 200，管理员以为生效了，而攻击者手上那个 token 在整个空闲窗口
// （默认 7 天）内一直有效，Validate 又刻意不看用户状态，没有任何东西会拦它。
//
// 用真实 goroutine 去撞这个时间点是不可复现的。这里改用确定的构造：
// SessionService 的时钟是可注入的，而 Issue 里 s.now() 恰好在写 Redis 之前
// 被调用一次——把冻结挂到那一次调用上，它就严格落在检查与写入之间。
// 钩子只触发一次（触发前先置空），否则冻结内部的 RevokeUser 会再次调到它。
func TestLoginRejectsFreezeCommittedBetweenCheckAndSessionWrite(t *testing.T) {
	var hook func()
	e := newAuthEnvWithClock(t, func() int64 {
		if hook != nil {
			f := hook
			hook = nil
			f()
		}
		return time.Now().UnixMilli()
	})
	ctx := context.Background()

	// 直接建号，不走登录——这样后面数在线会话时，只会数到这次登录签发的那一个。
	u, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}

	// 用 AccountService 而不是 UserService：要复现的是管理员那条完整路径
	// （改状态 + 撤销全部会话），而不只是把状态列改掉。
	hook = func() {
		if _, err := e.accounts.SetStatus(ctx, u.ID, domain.UserStatusFrozen); err != nil {
			t.Errorf("冻结: %v", err)
		}
	}

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	_, err = e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": e.sms.LastParam("code")},
		IP:            "5.5.5.5", UA: "go-test",
	})

	if hook != nil {
		t.Fatal("钩子没有触发——测试构造已失效，下面的断言不再有意义")
	}
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden——冻结与签发之间的竞态没有被拦住", err)
	}

	// 最关键的一条：那个已经写进 Redis 的 token 必须被撤销掉。
	// 只断言返回错误是不够的——Login 返回 error 时不给调用方 token，但
	// 会话仍然躺在 Redis 里，攻击者那一侧照样握着它。
	live, err := e.sess.ListByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("冻结后仍有 %d 个存活会话——被拒绝的那次登录留下了一个可用 token", len(live))
	}

	// 失败也要留痕，和其他认证后失败路径一致。
	list, err := e.logs.ListByUser(ctx, u.ID, 10)
	if err != nil {
		t.Fatalf("ListByUser(logs): %v", err)
	}
	var found bool
	for _, l := range list {
		if l.Event == domain.LoginEventLogin && !l.Success && l.IP == "5.5.5.5" {
			found = true
		}
	}
	if !found {
		t.Fatalf("竞态被拦下时没有写失败审计: %+v", list)
	}
}

// 在"凭据校验通过"和"会话写入"之间提交的改密，同样必须把这次登录拦下来。
//
// 这是与冻结完全同形状的另一半竞态，而且**光看用户状态是看不见它的**——
// 改密不改 status，CanLogin() 照样为真：
//
//	T1（登录）                        T2（管理员改密）
//	resolveUser -> U{ACTIVE, hash=旧}
//	CanLogin() -> true
//	                                  SetPassword -> hash=新，已提交
//	                                  RevokeUser -> 此刻一个 token 都枚举不到
//	Sessions.Issue -> 写入新 token
//
// "密码泄露了赶紧改密码"这个动作的全部意义就是把已经落在攻击者手里的会话作废掉。
// 一次校验过**旧密码**的登录在 RevokeUser 之后落地，等于让改密对这个正在飞的
// 请求完全失效——恰恰是最该拦住的那一个。
//
// 两种登录方式都要覆盖：password 是最直观的场景，而 sms_code 压根没碰过密码，
// 它能被拦住靠的完全是 password_hash 比对这条通用规则——注释里既然写了
// "对每一种登录方式都成立"，就必须有用例钉住它，不能只留一句断言在注释里。
func TestLoginRejectsPasswordResetCommittedBetweenCheckAndSessionWrite(t *testing.T) {
	const (
		oldPassword = "hunter2hunter2"
		newPassword = "newpassword123"
	)

	tests := []struct {
		name  string
		login func(t *testing.T, e *authEnv) error
	}{
		{
			name: "密码登录_校验的是旧密码",
			login: func(t *testing.T, e *authEnv) error {
				_, err := e.auth.Login(context.Background(), service.LoginInput{
					AppID:         e.app.AppID,
					ConnectorType: connector.TypePassword,
					Credentials:   connector.Credentials{"account": "13800138000", "password": oldPassword},
					IP:            "6.6.6.6", UA: "go-test",
				})
				return err
			},
		},
		{
			name: "短信登录_压根没碰密码",
			login: func(t *testing.T, e *authEnv) error {
				ctx := context.Background()
				if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
					t.Fatalf("SendLoginCode: %v", err)
				}
				_, err := e.auth.Login(ctx, service.LoginInput{
					AppID:         e.app.AppID,
					ConnectorType: connector.TypeSMSCode,
					Credentials:   connector.Credentials{"phone": "13800138000", "code": e.sms.LastParam("code")},
					IP:            "6.6.6.6", UA: "go-test",
				})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 与冻结那条用例同一手法：SessionService 的时钟可注入，而 Issue 里
			// s.now() 恰好在写 Redis 之前被调用一次，把改密挂上去就严格落在
			// 凭据校验与会话写入之间。钩子触发前先置空——改密内部的 RevokeUser
			// （revokeMatching 的 defer 同样调 s.now()）否则会再次触发它。
			var hook func()
			e := newAuthEnvWithClock(t, func() int64 {
				if hook != nil {
					f := hook
					hook = nil
					f()
				}
				return time.Now().UnixMilli()
			})
			ctx := context.Background()

			// 直接建号，不走登录——这样后面数在线会话时，只会数到这次登录签发的那一个。
			u, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
				Type: domain.IdentityTypePhone, Subject: "13800138000",
			})
			if err != nil {
				t.Fatalf("建号: %v", err)
			}
			if err := e.users.SetPassword(ctx, u.ID, oldPassword); err != nil {
				t.Fatalf("设初始密码: %v", err)
			}

			// 走 AccountService 而不是 UserService：要复现的是管理员那条完整路径
			// （改密 + 撤销全部会话），而不只是把 password_hash 列改掉。
			hook = func() {
				if err := e.accounts.ResetPassword(ctx, u.ID, newPassword); err != nil {
					t.Errorf("改密: %v", err)
				}
			}

			err = tt.login(t, e)

			if hook != nil {
				t.Fatal("钩子没有触发——测试构造已失效，下面的断言不再有意义")
			}
			// 归 TOKEN_INVALID 而不是单独一个"凭据已变更"码：对使用者而言
			// 改密导致的强制下线和普通的登录过期是同一件事——都要重新登录。
			// 断言 Code 而不只是哨兵，这样"错误分类"本身也被固定住。
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeTokenInvalid {
				t.Fatalf("err = %v, want CodeTokenInvalid——改密与签发之间的竞态没有被拦住", err)
			}

			// 最关键的一条：那个已经写进 Redis 的 token 必须被撤销掉。
			// 只断言返回错误是不够的——Login 出错时不给调用方 token，
			// 但会话仍然躺在 Redis 里，攻击者那一侧照样握着它。
			live, err := e.sess.ListByUser(ctx, u.ID)
			if err != nil {
				t.Fatalf("ListByUser: %v", err)
			}
			if len(live) != 0 {
				t.Fatalf("改密后仍有 %d 个存活会话——被拒绝的那次登录留下了一个可用 token", len(live))
			}

			// 失败也要留痕，和其他认证后失败路径一致。
			logs, err := e.logs.ListByUser(ctx, u.ID, 10)
			if err != nil {
				t.Fatalf("ListByUser(logs): %v", err)
			}
			var found bool
			for _, l := range logs {
				if l.Event == domain.LoginEventLogin && !l.Success && l.IP == "6.6.6.6" {
					found = true
				}
			}
			if !found {
				t.Fatalf("竞态被拦下时没有写失败审计: %+v", logs)
			}

			// 账号没有被弄坏：用**新**密码可以正常登录。
			// 这条把"我们因为哈希变了而拒绝"和"我们把登录整个搞挂了"区分开。
			res, err := e.auth.Login(ctx, service.LoginInput{
				AppID:         e.app.AppID,
				ConnectorType: connector.TypePassword,
				Credentials:   connector.Credentials{"account": "13800138000", "password": newPassword},
			})
			if err != nil {
				t.Fatalf("改密后用新密码登录失败: %v", err)
			}
			if res.Session.Token == "" {
				t.Fatal("新密码登录没有拿到 token")
			}
		})
	}
}

// 注销保护期内登录会撤销注销申请——沿用 3s 的行为。
func TestLoginCancelsPendingDeletion(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	first := e.smsLogin(t, "13800138000")
	if _, err := e.users.SetStatus(ctx, first.User.ID, domain.UserStatusPendingDelete); err != nil {
		t.Fatalf("提交注销: %v", err)
	}

	second := e.smsLogin(t, "13800138000")
	if second.User.ID != first.User.ID {
		t.Fatal("不是同一个用户")
	}
	if second.User.Status != domain.UserStatusActive {
		t.Fatalf("Status = %q, want ACTIVE（登录应撤销注销申请）", second.User.Status)
	}
	if second.User.DeleteSubmittedAt != 0 {
		t.Fatalf("DeleteSubmittedAt = %d, 应被清零", second.User.DeleteSubmittedAt)
	}
}

// 登录必须真的写下注册关系，并且幂等。
//
// 只断言"两次登录是同一个用户"是不够的：把 EnsureRegistration 整行删掉，
// 那种写法照样全绿——没有任何断言去看 user_application 里到底有没有行。
func TestLoginRecordsRegistrationIdempotently(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	first := e.smsLogin(t, "13800138000")

	var n int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM user_application WHERE user_id = $1 AND application_id = $2`,
		first.User.ID, e.app.ID).Scan(&n); err != nil {
		t.Fatalf("查询注册关系: %v", err)
	}
	if n != 1 {
		t.Fatalf("注册关系数 = %d, want 1——登录没有写入 user_application", n)
	}

	again := e.smsLogin(t, "13800138000")
	if again.User.ID != first.User.ID {
		t.Fatal("两次登录不是同一用户")
	}
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM user_application WHERE user_id = $1 AND application_id = $2`,
		first.User.ID, e.app.ID).Scan(&n); err != nil {
		t.Fatalf("二次查询注册关系: %v", err)
	}
	if n != 1 {
		t.Fatalf("重复登录后注册关系数 = %d, want 仍为 1", n)
	}
}

// 登录必须更新该登录标识的最后使用时间。
//
// 没有这条断言的话，把 TouchIdentityLogin 整行删掉全套测试照过——
// 没有任何用例在登录之后去读 identity.last_login_at。
func TestLoginTouchesIdentityLastLoginAt(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")

	ids, err := e.users.ListIdentities(ctx, res.User.ID)
	if err != nil {
		t.Fatalf("ListIdentities: %v", err)
	}
	var phone *domain.Identity
	for i := range ids {
		if ids[i].Type == domain.IdentityTypePhone {
			phone = &ids[i]
		}
	}
	if phone == nil {
		t.Fatal("未找到手机号标识")
	}
	if phone.LastLoginAt == 0 {
		t.Fatal("登录后 identity.last_login_at 仍为 0——TouchIdentityLogin 没被调用")
	}
}

func TestLoginWritesAuditLog(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")
	list, err := e.logs.ListByUser(ctx, res.User.ID, 10)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("登录成功未写审计日志")
	}
	last := list[0]
	if !last.Success || last.Event != domain.LoginEventLogin {
		t.Fatalf("log = %+v", last)
	}
	if last.IP != "1.2.3.4" || last.UA != "go-test" {
		t.Fatalf("设备信息未记录: %+v", last)
	}
	// 审计日志里必须是脱敏后的手机号
	if last.Subject != "138****8000" {
		t.Fatalf("Subject = %q, want 138****8000", last.Subject)
	}
	if last.SessionID != res.Session.ID {
		t.Fatalf("SessionID = %q, want %q", last.SessionID, res.Session.ID)
	}
}

// 登录失败也要留痕，否则爆破攻击无从追溯。
func TestFailedLoginWritesAuditLog(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	// 先建号并设密码，这样失败记录能挂到 user_id 上，便于按用户查询
	res := e.smsLogin(t, "13800138000")
	if err := e.users.SetPassword(ctx, res.User.ID, "hunter2hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypePassword,
		Credentials:   connector.Credentials{"account": "13800138000", "password": "wrong"},
		IP:            "9.9.9.9",
	}); err == nil {
		t.Fatal("应当登录失败")
	}

	list, err := e.logs.ListByUser(ctx, res.User.ID, 10)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	var failed *domain.LoginLog
	for i := range list {
		if !list[i].Success && list[i].IP == "9.9.9.9" {
			failed = &list[i]
		}
	}
	if failed == nil {
		t.Fatalf("未找到失败的审计记录: %+v", list)
	}
	if failed.Reason == "" {
		t.Fatal("失败记录没有 reason")
	}
	// 手机号必须是脱敏的。少了这条断言，即便 logFailure 写的是明文
	// 13800138000，整套测试也全绿，审计表会静静地存着未脱敏的标识。
	if failed.Subject != "138****8000" {
		t.Fatalf("失败记录 Subject = %q, want 138****8000（未脱敏）", failed.Subject)
	}
	// 失败记录也必须落上应用归属，否则多应用共用一套用户时无从区分
	if failed.ApplicationID == nil || *failed.ApplicationID != e.app.ID {
		t.Fatalf("失败记录的 ApplicationID = %v, want %v", failed.ApplicationID, e.app.ID)
	}
}

// 被冻结的账号尝试登录也要留痕，而且要挂到该用户名下。
//
// 这条路径走的是 logFailureWithUser，此前零覆盖：把那行调用删掉，
// TestLoginRejectsFrozenUser 只断言错误类型，照样全绿。
func TestFrozenUserLoginWritesAuditLog(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")
	if _, err := e.users.SetStatus(ctx, res.User.ID, domain.UserStatusFrozen); err != nil {
		t.Fatalf("冻结: %v", err)
	}

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": e.sms.LastParam("code")},
		IP:            "7.7.7.7",
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}

	list, err := e.logs.ListByUser(ctx, res.User.ID, 10)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	var found bool
	for _, l := range list {
		if !l.Success && l.IP == "7.7.7.7" && l.Subject == "138****8000" {
			found = true
		}
	}
	if !found {
		t.Fatalf("冻结账号的登录尝试未留下审计记录: %+v", list)
	}
}

// 登出要写审计记录。domain 里定义了 LoginEventLogout 常量——
// 定义了却从不写入就是死字段，本任务在别处正反对这种模式。
func TestLogoutWritesAuditLog(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")
	if err := e.auth.Logout(ctx, e.app.AppID, res.Session.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	list, err := e.logs.ListByUser(ctx, res.User.ID, 10)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	var found bool
	for _, l := range list {
		if l.Event == domain.LoginEventLogout && l.Success && l.SessionID == res.Session.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("登出未留下审计记录: %+v", list)
	}

	// 重复登出不应报错，也不该再记一条
	if err := e.auth.Logout(ctx, e.app.AppID, res.Session.Token); err != nil {
		t.Fatalf("重复 Logout: %v", err)
	}
}

// 应用被停用后，登录、发码、token 校验三条入口必须同时失效。
// 第一阶段没有停用应用的管理接口，因此这里直接改库来制造该状态。
func TestDisabledApplicationBlocksAllAuthEntryPoints(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")

	if _, err := e.pool.Exec(ctx,
		`UPDATE application SET status = $2 WHERE id = $1`,
		e.app.ID, domain.ApplicationStatusDisabled); err != nil {
		t.Fatalf("停用应用: %v", err)
	}

	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("SendLoginCode err = %v, want ErrForbidden", err)
	}
	if _, err := e.auth.Login(ctx, service.LoginInput{
		AppID:         e.app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": "123456"},
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("Login err = %v, want ErrForbidden", err)
	}
	// 已签发的 token 也必须立刻失效
	if _, err := e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("ValidateToken err = %v, want ErrForbidden", err)
	}
}

func TestSendLoginCodeRequiresEnabledConnector(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	if err := e.apps.SetConnector(ctx, e.app.ID, connector.TypeSMSCode, false, nil); err != nil {
		t.Fatalf("停用 sms_code: %v", err)
	}
	if err := e.auth.SendLoginCode(ctx, e.app.AppID, "13800138000"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestSendLoginCodeRejectsMalformedPhone(t *testing.T) {
	e := newAuthEnv(t)
	if err := e.auth.SendLoginCode(context.Background(), e.app.AppID, "12345"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	if len(e.sms.Sent()) != 0 {
		t.Fatal("格式非法时不应真的发短信")
	}
}

// 命名加 AuthService 前缀以区别于 admin_test.go 里同名但测 AdminService 的用例。
func TestAuthServiceLogoutInvalidatesToken(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	res := e.smsLogin(t, "13800138000")
	if err := e.auth.Logout(ctx, e.app.AppID, res.Session.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestValidateTokenRejectsWrongApp(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	other, _, err := e.apps.Create(ctx, "另一个应用", "other-app")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	res := e.smsLogin(t, "13800138000")

	if _, err := e.auth.ValidateToken(ctx, other.AppID, res.Session.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

// 被限流的重发绝不能作废用户手里已经收到的验证码。
//
// 这是个用户侧的死锁：如果 Issue 无条件覆盖，窗口内第二次点"发送"会用新码
// 顶掉旧码，而新码又因限流发不出去——用户手上的码作废了、补发没到、
// 再点又滚一次，整个窗口内都登录不了。
func TestRateLimitedResendDoesNotInvalidateDeliveredCode(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	users := service.NewUserService(pool)
	codes := notify.NewCodeService(rdb)
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册: %v", err)
	}
	apps := service.NewApplicationService(pool, reg)
	sms := notify.NewFakeProvider(notify.ChannelSMS, "fake")
	sender := notify.NewSender(pool, store.NewRateLimiter(rdb),
		[]notify.RateRule{{Name: "30s", Window: 30 * time.Second, Limit: 1}})
	sender.AddProvider(sms)

	ctx := context.Background()
	app, _, err := apps.Create(ctx, "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	if err := apps.SetConnector(ctx, app.ID, connector.TypeSMSCode, true, nil); err != nil {
		t.Fatalf("启用: %v", err)
	}
	auth := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users,
		Sessions: service.NewSessionService(store.NewSessionStore(rdb), store.NewRevokePublisher(rdb), store.NewEpochStore(rdb)),
		Logs:     service.NewLoginLogService(pool),
		Registry: reg, Notifier: sender, Codes: codes,
	})

	if err := auth.SendLoginCode(ctx, app.AppID, "13800138000"); err != nil {
		t.Fatalf("首次发送: %v", err)
	}
	delivered := sms.LastParam("code")
	if delivered == "" {
		t.Fatal("未取到已送达的验证码")
	}

	// 窗口内再点一次，必然被限流
	if err := auth.SendLoginCode(ctx, app.AppID, "13800138000"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("二次发送 err = %v, want ErrRateLimited", err)
	}

	// 关键断言：用户手里那个码必须仍然可用
	if _, err := auth.Login(ctx, service.LoginInput{
		AppID:         app.AppID,
		ConnectorType: connector.TypeSMSCode,
		Credentials:   connector.Credentials{"phone": "13800138000", "code": delivered},
	}); err != nil {
		t.Fatalf("被限流的重发作废了用户已收到的验证码，用户被锁在窗口外: %v", err)
	}
}

func TestSendLoginCodeIsRateLimitedPerPhone(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	users := service.NewUserService(pool)
	codes := notify.NewCodeService(rdb)
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册: %v", err)
	}
	apps := service.NewApplicationService(pool, reg)
	sms := notify.NewFakeProvider(notify.ChannelSMS, "fake")
	// 启用 30 秒一次的限制
	sender := notify.NewSender(pool, store.NewRateLimiter(rdb),
		[]notify.RateRule{{Name: "30s", Window: 30 * time.Second, Limit: 1}})
	sender.AddProvider(sms)

	app, _, err := apps.Create(context.Background(), "A", "a")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	ctx := context.Background()
	if err := apps.SetConnector(ctx, app.ID, connector.TypeSMSCode, true, nil); err != nil {
		t.Fatalf("启用: %v", err)
	}

	auth := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users,
		Sessions: service.NewSessionService(store.NewSessionStore(rdb), store.NewRevokePublisher(rdb), store.NewEpochStore(rdb)),
		Logs:     service.NewLoginLogService(pool),
		Registry: reg, Notifier: sender, Codes: codes,
	})

	if err := auth.SendLoginCode(ctx, app.AppID, "13800138000"); err != nil {
		t.Fatalf("首次: %v", err)
	}
	if err := auth.SendLoginCode(ctx, app.AppID, "13800138000"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("二次 err = %v, want ErrRateLimited", err)
	}
}
