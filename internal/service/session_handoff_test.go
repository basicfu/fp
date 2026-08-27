package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// TestRotationIsAnnouncedToEveryValidateDuringGrace 是本任务的核心。
//
// 修复前：第一次校验返回 Rotated=true，之后每一次都返回 Rotated=false，
// 新 token 就此失传，客户端在过渡期结束时被登出。
// 修复后：过渡期内每一次带旧 token 的校验都会被再次告知同一个新 token。
func TestRotationIsAnnouncedToEveryValidateDuringGrace(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()
	app := env.appWithPolicy(t, func(p *domain.SessionPolicy) {
		p.RotateIntervalSeconds = 1
	})

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: env.userID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	env.clock.Advance(2 * time.Second) // 越过 rotate_interval

	first, err := env.sessions.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("首次校验: %v", err)
	}
	if !first.Rotated || first.NewToken == "" {
		t.Fatalf("首次校验没有触发轮换: rotated=%v newToken=%q", first.Rotated, first.NewToken)
	}

	// 过渡期内再校验两次，每次都必须拿到同一个新 token。
	for i := 2; i <= 3; i++ {
		got, err := env.sessions.Validate(ctx, sess.Token, app)
		if err != nil {
			t.Fatalf("第 %d 次校验: %v", i, err)
		}
		if !got.Rotated {
			t.Fatalf("第 %d 次校验返回 Rotated=false——新 token 在过渡期内失传了，"+
				"客户端会在过渡期结束时被登出", i)
		}
		if got.NewToken != first.NewToken {
			t.Fatalf("第 %d 次校验给出的新 token 是 %q，首次是 %q——"+
				"重复告知不能触发第二次轮换", i, got.NewToken, first.NewToken)
		}
	}
}

// TestConcurrentValidatesConvergeOnOneNewToken 确认并发下只轮换一次。
//
// 断言分两段：并发阶段允许有请求落在"锁已拿到、映射未写完"的窄空隙里而
// 学不到新 token（那是可接受的降级，它们下一次就学到了）；但**凡是学到的，
// 必须是同一个 token**。串行阶段则要求全部学到——空隙已经过去了。
func TestConcurrentValidatesConvergeOnOneNewToken(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()
	app := env.appWithPolicy(t, func(p *domain.SessionPolicy) {
		p.RotateIntervalSeconds = 1
	})

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: env.userID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	env.clock.Advance(2 * time.Second)

	const n = 8
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := env.sessions.Validate(ctx, sess.Token, app)
			if err != nil {
				return
			}
			if got.Rotated {
				mu.Lock()
				seen[got.NewToken]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != 1 {
		t.Fatalf("并发校验产生了 %d 个不同的新 token，期望恰好 1 个：%v", len(seen), seen)
	}

	// 空隙已过，此后每一次都必须被告知。
	got, err := env.sessions.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("并发之后的校验: %v", err)
	}
	if !got.Rotated {
		t.Fatal("并发结束后的校验仍未收到告知")
	}
	for token := range seen {
		if got.NewToken != token {
			t.Fatalf("并发阶段的新 token 是 %q，之后拿到的是 %q", token, got.NewToken)
		}
	}
}

// TestExpiredOldTokenIsNotAnnounced 守住"不能复活死 token"。
//
// 过渡期结束后旧 token 的会话已被 Redis 清掉，此时的校验必须直接失败，
// 而不是从残留的映射里读出一个新 token 再告知——那等于给一个早该失效的
// 凭据开了一条后门。
func TestExpiredOldTokenIsNotAnnounced(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()
	app := env.appWithPolicy(t, func(p *domain.SessionPolicy) {
		p.RotateIntervalSeconds = 1
	})

	sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: env.userID, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	env.clock.Advance(2 * time.Second)
	if _, err := env.sessions.Validate(ctx, sess.Token, app); err != nil {
		t.Fatalf("触发轮换: %v", err)
	}

	// 越过过渡期。逻辑时钟推进不会让 Redis 的 TTL 到期，所以显式删掉旧会话，
	// 模拟 TTL 清理后的状态——校验必须止步于"会话不存在"。
	if err := env.store.Delete(ctx, sess.Token); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := env.sessions.Validate(ctx, sess.Token, app); err == nil {
		t.Fatal("会话已清理，校验却仍然放行")
	}
}
