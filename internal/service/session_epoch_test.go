package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// TestSessionIssuedBeforeBumpIsRejected 是纪元的核心性质。
func TestSessionIssuedBeforeBumpIsRejected(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := env.sessions.Validate(ctx, sess.Token, env.app); err != nil {
		t.Fatalf("刚签发的会话就校验失败: %v", err)
	}

	if _, err := env.epochs.Bump(ctx, user.ID); err != nil {
		t.Fatalf("Bump: %v", err)
	}

	_, err = env.sessions.Validate(ctx, sess.Token, env.app)
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("纪元递增后校验返回 %v，期望 ErrUnauthorized", err)
	}
}

// TestSessionIssuedAfterBumpIsAccepted 确认纪元不是单向开关。
//
// 少了它，一个"永远返回不匹配"的实现也能让上一个测试通过——
// 而那意味着任何被冻结过一次的用户从此再也无法登录。
func TestSessionIssuedAfterBumpIsAccepted(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	if _, err := env.epochs.Bump(ctx, user.ID); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := env.sessions.Validate(ctx, sess.Token, env.app); err != nil {
		t.Fatalf("纪元递增后新签发的会话被拒: %v", err)
	}
}

// TestRotationInheritsEpoch 守住"轮换不洗白纪元"。
//
// 轮换里的 newSess := *sess 天然继承 Epoch。谁把它改成重新读一次当前纪元，
// 一个本该被拒的会话只要熬到轮换点就复活了——而且从此永久有效。
func TestRotationInheritsEpoch(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	// 用一个 rotate_interval 极短的策略，让下一次校验必定触发轮换。
	app := env.appWithPolicy(t, func(p *domain.SessionPolicy) { p.RotateIntervalSeconds = 1 })

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := env.epochs.Bump(ctx, user.ID); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	env.clock.Advance(2000) // 越过 rotate_interval

	_, err = env.sessions.Validate(ctx, sess.Token, app)
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("纪元失配的会话在轮换点被放行了: %v", err)
	}
}

// TestEpochMismatchDeletesSession 确认失配的会话被清掉。
func TestEpochMismatchDeletesSession(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := env.epochs.Bump(ctx, user.ID); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	if _, err := env.sessions.Validate(ctx, sess.Token, env.app); err == nil {
		t.Fatal("期望校验失败")
	}
	if _, err := env.sessions.SessionByToken(ctx, sess.Token); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("纪元失配的会话仍在存储里: %v", err)
	}
}

// TestFreezeBumpsEpoch / TestResetPasswordBumpsEpoch 确认撤销路径确实递增。
func TestFreezeBumpsEpoch(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	before, _ := env.epochs.Current(ctx, user.ID)
	if _, err := env.accounts.SetStatus(ctx, user.ID, domain.UserStatusFrozen); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	after, _ := env.epochs.Current(ctx, user.ID)
	if after <= before {
		t.Fatalf("冻结后纪元从 %d 变成 %d，期望递增", before, after)
	}
}

func TestResetPasswordBumpsEpoch(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	before, _ := env.epochs.Current(ctx, user.ID)
	if err := env.accounts.ResetPassword(ctx, user.ID, "newpassword123"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	after, _ := env.epochs.Current(ctx, user.ID)
	if after <= before {
		t.Fatalf("改密后纪元从 %d 变成 %d，期望递增", before, after)
	}
}

// TestRevokeSingleSessionDoesNotBumpEpoch 是本任务最重要的一个测试。
//
// 纪元是**用户级**的。给"踢掉这一台设备"加上递增，会把该用户所有设备
// 一起踢下线——手机、电脑、平板全掉。这个错误极易犯（三个撤销方法看起来
// 是一类操作），而症状是"踢一台掉一片"，用户报障时几乎不可能对上因果。
//
// 断言写成"另一台设备仍然可用"，而不是"纪元没变"：前者是我们真正在乎的
// 性质，后者会在实现换成别的机制时误报。
func TestRevokeSingleSessionDoesNotBumpEpoch(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	phone, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app, UA: "phone"})
	if err != nil {
		t.Fatalf("Issue phone: %v", err)
	}
	laptop, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app, UA: "laptop"})
	if err != nil {
		t.Fatalf("Issue laptop: %v", err)
	}

	if _, err := env.accounts.RevokeSession(ctx, user.ID, phone.ID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	if _, err := env.sessions.Validate(ctx, phone.Token, env.app); err == nil {
		t.Fatal("被踢掉的那台设备仍然有效")
	}
	if _, err := env.sessions.Validate(ctx, laptop.Token, env.app); err != nil {
		t.Fatalf("踢掉一台设备后，另一台也失效了——纪元被误用在单设备撤销上: %v", err)
	}
}
