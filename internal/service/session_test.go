package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

// fakeClock 让测试可以精确控制"现在几点"，从而验证过期、降频与轮换的边界。
type fakeClock struct{ ms atomic.Int64 }

func newFakeClock(start int64) *fakeClock {
	c := &fakeClock{}
	c.ms.Store(start)
	return c
}
func (c *fakeClock) Now() int64              { return c.ms.Load() }
func (c *fakeClock) Advance(d time.Duration) { c.ms.Add(d.Milliseconds()) }

func newSessionService(t *testing.T) (*service.SessionService, *fakeClock) {
	t.Helper()
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	clk := newFakeClock(time.Now().UnixMilli())
	return service.NewSessionServiceWithClock(st, clk.Now), clk
}

// testApp 造一个不落库的应用，只用于携带会话策略。
func testApp(mutate ...func(*domain.SessionPolicy)) *domain.Application {
	p := domain.DefaultSessionPolicy()
	for _, m := range mutate {
		m(&p)
	}
	return &domain.Application{
		ID:      uuid.New(),
		Name:    "test",
		Status:  domain.ApplicationStatusActive,
		Session: p,
	}
}

func TestIssueCreatesSession(t *testing.T) {
	svc, clk := newSessionService(t)
	app := testApp()
	uid := uuid.New()

	sess, err := svc.Issue(context.Background(), service.IssueInput{
		UserID: uid, App: app, IP: "1.2.3.4", UA: "go-test",
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(sess.Token) < 32 {
		t.Fatalf("token 长度 = %d, 太短", len(sess.Token))
	}
	if sess.UserID != uid || sess.AppID != app.ID {
		t.Fatalf("sess = %+v", sess)
	}
	if sess.FirstAuthAt != clk.Now() || sess.IssuedAt != clk.Now() {
		t.Fatalf("时间字段 = %d/%d, want %d", sess.FirstAuthAt, sess.IssuedAt, clk.Now())
	}
	wantIdle := clk.Now() + int64(app.Session.IdleTimeoutSeconds)*1000
	if sess.IdleExpiresAt != wantIdle {
		t.Fatalf("IdleExpiresAt = %d, want %d", sess.IdleExpiresAt, wantIdle)
	}
}

func TestIssueUsesMobileTimeoutForMobileClients(t *testing.T) {
	svc, clk := newSessionService(t)
	app := testApp()

	sess, err := svc.Issue(context.Background(), service.IssueInput{
		UserID: uuid.New(), App: app, Mobile: true,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	wantIdle := clk.Now() + int64(app.Session.IdleTimeoutMobileSeconds)*1000
	if sess.IdleExpiresAt != wantIdle {
		t.Fatalf("移动端 IdleExpiresAt = %d, want %d", sess.IdleExpiresAt, wantIdle)
	}
}

// 最核心的一条：cache_ttl 取应用配置与剩余有效期的较小者。
func TestValidateReturnsConfiguredCacheTTL(t *testing.T) {
	svc, _ := newSessionService(t)
	app := testApp()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want := time.Duration(app.Session.TokenCacheTTLSeconds) * time.Second
	if res.CacheTTL != want {
		t.Fatalf("CacheTTL = %v, want %v", res.CacheTTL, want)
	}
	if res.Session.UserID != sess.UserID {
		t.Fatalf("Session.UserID 不匹配")
	}
}

// token 快到期时，cache_ttl 必须收缩到剩余有效期，否则 SDK 会在 token 过期后
// 继续放行最多一个完整的缓存窗口。
func TestCacheTTLNeverExceedsRemainingLifetime(t *testing.T) {
	svc, clk := newSessionService(t)
	// 空闲超时 60 秒，缓存窗口 30 秒
	app := testApp(func(p *domain.SessionPolicy) {
		p.IdleTimeoutSeconds = 60
		p.IdleTimeoutMobileSeconds = 60
		// 必须大于本用例的累计推进量（48s+10s=58s），而非单步推进量——
		// 延期窗口判定用的是 now-LastExtendedAt，从未延期过时就是累计耗时。
		// Task 9 写下这个值时 Validate 还没有真正的延期逻辑，50s 恰好不够。
		p.ExtendIntervalSeconds = 1000
		p.TokenCacheTTLSeconds = 30
	})
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// 推进 48 秒，只剩 12 秒
	clk.Advance(48 * time.Second)
	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.CacheTTL != 12*time.Second {
		t.Fatalf("CacheTTL = %v, want 12s", res.CacheTTL)
	}

	// 再推进 10 秒，只剩 2 秒
	clk.Advance(10 * time.Second)
	res, err = svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.CacheTTL != 2*time.Second {
		t.Fatalf("CacheTTL = %v, want 2s", res.CacheTTL)
	}
}

// 绝对上限也参与 cache_ttl 的约束，不只是空闲超时。
func TestCacheTTLBoundedByMaxLifetime(t *testing.T) {
	svc, clk := newSessionService(t)
	app := testApp(func(p *domain.SessionPolicy) {
		p.IdleTimeoutSeconds = 3600
		p.IdleTimeoutMobileSeconds = 3600
		p.MaxLifetimeSeconds = 100 // 绝对上限比空闲超时短得多
		p.RotateIntervalSeconds = 100
		p.ExtendIntervalSeconds = 3000
		p.TokenCacheTTLSeconds = 30
	})
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	clk.Advance(95 * time.Second) // 距绝对上限只剩 5 秒
	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.CacheTTL != 5*time.Second {
		t.Fatalf("CacheTTL = %v, want 5s（应受 max_lifetime 约束）", res.CacheTTL)
	}
}

func TestValidateRejectsUnknownToken(t *testing.T) {
	svc, _ := newSessionService(t)
	_, err := svc.Validate(context.Background(), "no-such-token", testApp())
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestValidateRejectsEmptyToken(t *testing.T) {
	svc, _ := newSessionService(t)
	_, err := svc.Validate(context.Background(), "", testApp())
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestValidateRejectsIdleExpired(t *testing.T) {
	svc, clk := newSessionService(t)
	app := testApp(func(p *domain.SessionPolicy) {
		p.IdleTimeoutSeconds = 10
		p.IdleTimeoutMobileSeconds = 10
		p.ExtendIntervalSeconds = 5
		p.TokenCacheTTLSeconds = 5
	})
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clk.Advance(11 * time.Second)

	if _, err := svc.Validate(ctx, sess.Token, app); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

// 一个应用签发的 token 绝不能在另一个应用上通过校验。
func TestValidateRejectsTokenFromAnotherApplication(t *testing.T) {
	svc, _ := newSessionService(t)
	appA := testApp()
	appB := testApp()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: appA})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Validate(ctx, sess.Token, appB); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("跨应用校验 err = %v, want ErrUnauthorized", err)
	}
	// 原应用仍然有效
	if _, err := svc.Validate(ctx, sess.Token, appA); err != nil {
		t.Fatalf("原应用校验失败: %v", err)
	}
}

func TestListByUser(t *testing.T) {
	svc, _ := newSessionService(t)
	app := testApp()
	uid := uuid.New()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.Issue(ctx, service.IssueInput{UserID: uid, App: app, UA: "ua"}); err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
	}
	list, err := svc.ListByUser(ctx, uid)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	for _, s := range list {
		if s.UserID != uid {
			t.Fatalf("混入了其他用户的会话: %+v", s)
		}
	}
}
