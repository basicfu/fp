package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// TestRotationCarriesEpochForward 守住"轮换不丢纪元"。
//
// 轮换出的新会话必须继承旧会话的 Epoch（tryRotate 里的 newSess := *sess
// 天然做到这一点）。若 tryRotate 改成从头构造新会话而漏掉 Epoch，新会话的
// Epoch 会是零值，与当前（非零）纪元不符——用户会在 token 轮换那一刻被
// 无声登出，而他什么都没做错。
//
// **纪元必须先推到非零值**：用零值的话，"继承了旧值"与"被清成零值"根本
// 无法区分，测试会对这个 bug 完全失明。
//
// 本测试替换了原先的 TestRotationInheritsEpoch。原测试想守住的性质——
// "轮换不能把一个已经失配的纪元洗白"——在当前实现下结构上不可达：Validate
// 里纪元比对严格发生在轮换判断之前，纪元不匹配的会话在到达 tryRotate 之前
// 就已经被拒绝、删除了，根本进不了轮换分支。残留的只有一个微秒级竞态
// （纪元检查通过后、tryRotate 执行前恰好发生一次 Bump），没有可靠的注入点，
// 不值得为它单独造 hook。原测试还有第二个独立问题：它用 env.clock.Advance(2000)
// 推进时间——env.clock 是 *fakeClock，Advance 接收 time.Duration，裸整数 2000
// 会被解释成 2000 纳秒，Milliseconds() 取整后是 0，时钟其实纹丝不动；即使
// 改成 2 * time.Second，由于上面那条结构性原因，测试依然测不到 tryRotate。
func TestRotationCarriesEpochForward(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	// 纪元先推到非零值（推两次，具体值不重要，只要非零）。
	if _, err := env.epochs.Bump(ctx, user.ID); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	if _, err := env.epochs.Bump(ctx, user.ID); err != nil {
		t.Fatalf("Bump: %v", err)
	}

	// 用一个 rotate_interval 极短的策略，让下一次校验必定触发轮换。
	app := env.appWithPolicy(t, func(p *domain.SessionPolicy) { p.RotateIntervalSeconds = 1 })

	// Issue 时纪元已经是非零值，这个值会被刻进会话。
	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	env.clock.Advance(2 * time.Second) // 越过 rotate_interval——务必带 time.Second 单位

	res, err := env.sessions.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate(旧 token): %v", err)
	}
	// 护栏：确认轮换真的发生了。少了这一条，测试可能在轮换从未触发的情况下
	// 也全绿——那样就又退化成"什么都没测但绿了"。
	if !res.Rotated || res.NewToken == "" {
		t.Fatalf("期望这次校验触发轮换，Rotated=%v NewToken=%q", res.Rotated, res.NewToken)
	}

	// 新 token 必须仍然有效：tryRotate 若把 Epoch 丢了或清零，新会话的纪元
	// 就会与当前纪元不符，这里会被 Validate 拒绝。
	if _, err := env.sessions.Validate(ctx, res.NewToken, app); err != nil {
		t.Fatalf("轮换后的新 token 校验失败（纪元在轮换过程中被丢弃或清零了）: %v", err)
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
