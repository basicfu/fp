package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// extendApp 造一个便于观察延期行为的应用：空闲 100s，降频窗口 10s，
// 轮换间隔设得很大以免干扰。
func extendApp() *domain.Application {
	return testApp(func(p *domain.SessionPolicy) {
		p.IdleTimeoutSeconds = 100
		p.IdleTimeoutMobileSeconds = 100
		p.MaxLifetimeSeconds = 100000
		p.RotateIntervalSeconds = 100000
		p.ExtendIntervalSeconds = 10
		p.TokenCacheTTLSeconds = 5
	})
}

// 降频窗口内的重复校验不应产生任何写入。
func TestValidateDoesNotExtendWithinInterval(t *testing.T) {
	svc, clk := newSessionService(t)
	app := extendApp()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	originalIdle := sess.IdleExpiresAt

	// 推进 9 秒，未达 10 秒的降频窗口
	clk.Advance(9 * time.Second)
	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.Session.IdleExpiresAt != originalIdle {
		t.Fatalf("窗口内不应延期: IdleExpiresAt %d → %d", originalIdle, res.Session.IdleExpiresAt)
	}
	if res.Session.LastExtendedAt != sess.LastExtendedAt {
		t.Fatal("窗口内不应更新 LastExtendedAt")
	}
}

func TestValidateExtendsAfterInterval(t *testing.T) {
	svc, clk := newSessionService(t)
	app := extendApp()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	originalIdle := sess.IdleExpiresAt

	clk.Advance(11 * time.Second)
	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	wantIdle := clk.Now() + 100*1000
	if res.Session.IdleExpiresAt != wantIdle {
		t.Fatalf("IdleExpiresAt = %d, want %d（原值 %d）",
			res.Session.IdleExpiresAt, wantIdle, originalIdle)
	}
	if res.Session.LastExtendedAt != clk.Now() {
		t.Fatalf("LastExtendedAt = %d, want %d", res.Session.LastExtendedAt, clk.Now())
	}

	// 延期必须真的落到存储里，而不只是返回值上改了改
	again, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("再次 Validate: %v", err)
	}
	if again.Session.IdleExpiresAt != wantIdle {
		t.Fatalf("延期未持久化: %d", again.Session.IdleExpiresAt)
	}
}

// 分布式锁保证同一时刻并发校验只产生一次延期写。
func TestValidateExtendIsDeduplicatedByLock(t *testing.T) {
	svc, clk := newSessionService(t)
	app := extendApp()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	clk.Advance(11 * time.Second)
	first, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("首次 Validate: %v", err)
	}
	extendedAt := first.Session.LastExtendedAt

	// 同一毫秒再来一次：降频窗口已被上一次重置，本次本就不该写；
	// 即使窗口判断失效，锁也会拦住。
	second, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("二次 Validate: %v", err)
	}
	if second.Session.LastExtendedAt != extendedAt {
		t.Fatalf("重复延期写: %d → %d", extendedAt, second.Session.LastExtendedAt)
	}
}

// 延期不能把会话推过绝对上限。
func TestValidateExtendCappedByMaxLifetime(t *testing.T) {
	svc, clk := newSessionService(t)
	app := testApp(func(p *domain.SessionPolicy) {
		p.IdleTimeoutSeconds = 1000
		p.IdleTimeoutMobileSeconds = 1000
		p.MaxLifetimeSeconds = 60 // 绝对上限远小于空闲超时
		p.RotateIntervalSeconds = 60
		p.ExtendIntervalSeconds = 10
		p.TokenCacheTTLSeconds = 5
	})
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	clk.Advance(50 * time.Second) // 距绝对上限只剩 10 秒
	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// 延期把 IdleExpiresAt 推到了 now+1000s，但剩余有效期仍由绝对上限决定
	if got := res.Session.RemainingAt(clk.Now(), app.Session); got != 10*time.Second {
		t.Fatalf("剩余有效期 = %v, want 10s（应受 max_lifetime 约束）", got)
	}
	if res.CacheTTL != 5*time.Second {
		t.Fatalf("CacheTTL = %v, want 5s", res.CacheTTL)
	}
}

// rotateApp 造一个便于观察轮换的应用：轮换间隔 30s，缓存窗口 20s
// （因此过渡期 = max(15s, 20s) = 20s）。
func rotateApp() *domain.Application {
	return testApp(func(p *domain.SessionPolicy) {
		p.IdleTimeoutSeconds = 1000
		p.IdleTimeoutMobileSeconds = 1000
		p.MaxLifetimeSeconds = 100000
		p.RotateIntervalSeconds = 30
		p.ExtendIntervalSeconds = 10
		p.TokenCacheTTLSeconds = 20
	})
}

func TestGraceDuration(t *testing.T) {
	// cache_ttl 小于 15s 时取 15s 下限
	small := domain.DefaultSessionPolicy()
	small.TokenCacheTTLSeconds = 5
	if got := service.GraceDuration(small); got != service.MinRotateGrace {
		t.Errorf("GraceDuration(5s) = %v, want %v", got, service.MinRotateGrace)
	}
	// cache_ttl 大于 15s 时取 cache_ttl，否则 SDK 缓存里的旧 token 会先于过渡期失效
	big := domain.DefaultSessionPolicy()
	big.TokenCacheTTLSeconds = 60
	if got := service.GraceDuration(big); got != 60*time.Second {
		t.Errorf("GraceDuration(60s) = %v, want 60s", got)
	}
}

func TestValidateRotatesAfterInterval(t *testing.T) {
	svc, clk := newSessionService(t)
	app := rotateApp()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	clk.Advance(31 * time.Second)
	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.Rotated {
		t.Fatal("Rotated = false, want true")
	}
	if res.NewToken == "" {
		t.Fatal("NewToken 为空")
	}
	if res.NewToken == sess.Token {
		t.Fatal("NewToken 与旧 token 相同")
	}
	if res.Session.Token != res.NewToken {
		t.Fatalf("返回的 Session.Token = %q, want %q", res.Session.Token, res.NewToken)
	}

	// 新 token 立即可用
	if _, err := svc.Validate(ctx, res.NewToken, app); err != nil {
		t.Fatalf("新 token 校验失败: %v", err)
	}
}

// 会话身份在轮换后保持不变——撤销以 session ID 为单位，它不能变。
func TestRotationPreservesSessionIdentity(t *testing.T) {
	svc, clk := newSessionService(t)
	app := rotateApp()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{
		UserID: uuid.New(), App: app, IP: "1.2.3.4", UA: "go-test",
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	clk.Advance(31 * time.Second)
	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.Session.ID != sess.ID {
		t.Fatalf("会话 ID 变了: %q → %q", sess.ID, res.Session.ID)
	}
	if res.Session.UserID != sess.UserID || res.Session.AppID != sess.AppID {
		t.Fatal("用户或应用归属变了")
	}
	if res.Session.IP != "1.2.3.4" || res.Session.UA != "go-test" {
		t.Fatalf("设备信息丢失: %+v", res.Session)
	}
}

// 最关键的一条：轮换不重置绝对上限。
// 否则只要用户一直活跃，同一个会话可以无限续命，max_lifetime 形同虚设。
func TestRotationDoesNotResetMaxLifetime(t *testing.T) {
	svc, clk := newSessionService(t)
	app := testApp(func(p *domain.SessionPolicy) {
		p.IdleTimeoutSeconds = 1000
		p.IdleTimeoutMobileSeconds = 1000
		// 绝对上限 90s，轮换间隔 31s：第三次校验时累计 93s > 90s，必被拒绝
		p.MaxLifetimeSeconds = 90
		p.RotateIntervalSeconds = 30
		p.ExtendIntervalSeconds = 10
		p.TokenCacheTTLSeconds = 5
	})
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	firstAuthAt := sess.FirstAuthAt

	token := sess.Token
	// 连续三次，每次推进 31 秒：31s、62s、93s
	for i := 0; i < 3; i++ {
		clk.Advance(31 * time.Second)
		res, err := svc.Validate(ctx, token, app)
		if err != nil {
			// 第三次时已超过 100 秒的绝对上限，应被拒绝
			if i == 2 && errors.Is(err, domain.ErrUnauthorized) {
				return
			}
			t.Fatalf("第 %d 次 Validate: %v", i+1, err)
		}
		if res.Session.FirstAuthAt != firstAuthAt {
			t.Fatalf("第 %d 次轮换后 FirstAuthAt 被重置: %d → %d",
				i+1, firstAuthAt, res.Session.FirstAuthAt)
		}
		if res.Rotated {
			token = res.NewToken
		}
	}
	t.Fatal("超过 max_lifetime 后仍未被拒绝")
}

// 过渡期内旧 token 必须继续有效：并发请求与 SDK 本地缓存里都可能还有它。
func TestOldTokenValidDuringGrace(t *testing.T) {
	svc, clk := newSessionService(t)
	app := rotateApp() // 过渡期 = max(15s, 20s) = 20s
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	oldToken := sess.Token

	clk.Advance(31 * time.Second)
	res, err := svc.Validate(ctx, oldToken, app)
	if err != nil {
		t.Fatalf("触发轮换: %v", err)
	}
	newToken := res.NewToken

	// 过渡期内旧 token 仍可用，但不应再次触发轮换
	clk.Advance(10 * time.Second)
	oldRes, err := svc.Validate(ctx, oldToken, app)
	if err != nil {
		t.Fatalf("过渡期内旧 token 校验失败: %v", err)
	}
	if oldRes.Rotated {
		t.Fatal("旧 token 不应再次触发轮换")
	}
	// 旧 token 的缓存时长必须被过渡期约束，不能按完整窗口下发
	if oldRes.CacheTTL > 10*time.Second {
		t.Fatalf("旧 token CacheTTL = %v, 应不超过过渡期剩余 10s", oldRes.CacheTTL)
	}

	// 过渡期结束后旧 token 失效，新 token 仍有效
	clk.Advance(11 * time.Second)
	if _, err := svc.Validate(ctx, oldToken, app); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("过渡期后旧 token err = %v, want ErrUnauthorized", err)
	}
	if _, err := svc.Validate(ctx, newToken, app); err != nil {
		t.Fatalf("新 token 应仍有效: %v", err)
	}
}

// 轮换后新旧 token 都挂在同一用户名下，在线设备列表不应把它们算成两台设备。
func TestRotationDoesNotDuplicateDeviceEntry(t *testing.T) {
	svc, clk := newSessionService(t)
	app := rotateApp()
	uid := uuid.New()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uid, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	clk.Advance(31 * time.Second)
	if _, err := svc.Validate(ctx, sess.Token, app); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	list, err := svc.ListByUser(ctx, uid)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	// 过渡期内新旧 token 并存，但会话 ID 只有一个
	ids := map[string]bool{}
	for _, s := range list {
		ids[s.ID] = true
	}
	if len(ids) != 1 {
		t.Fatalf("会话 ID 数 = %d, want 1（token 有 %d 个）", len(ids), len(list))
	}
}
