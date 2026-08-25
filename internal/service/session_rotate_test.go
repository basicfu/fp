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
// 分布式锁是降频的第二层，必须单独可测。
//
// 不能靠"连着调两次 Validate"来验证：第一次调用已经把 LastExtendedAt 推到 now，
// 第二次在同一个假时钟时刻上跑，光靠时间窗判断就不会写——把 tryExtend 里的
// TryLock 整个删掉，那种写法照样全绿。要真正验证锁，必须先把锁占住，
// 再让时间窗成立，然后断言延期没有发生。
func TestValidateExtendIsDeduplicatedByLock(t *testing.T) {
	st, pub, clk := newSessionParts(t)
	svc := service.NewSessionServiceWithClock(st, pub, clk.Now)
	app := extendApp()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	originalIdle := sess.IdleExpiresAt

	// 抢先占住这个 token 的延期锁，模拟"另一个并发请求正在延期"
	ok, err := st.TryLock(ctx, "ext:"+sess.Token, time.Minute)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !ok {
		t.Fatal("测试自身应当能拿到锁")
	}

	// 时间窗已成立，但锁被占着，不该产生延期写
	clk.Advance(11 * time.Second)
	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.Session.IdleExpiresAt != originalIdle {
		t.Fatalf("锁被占用时仍然延期了: IdleExpiresAt %d → %d",
			originalIdle, res.Session.IdleExpiresAt)
	}
	if res.Session.LastExtendedAt != sess.LastExtendedAt {
		t.Fatal("锁被占用时仍然更新了 LastExtendedAt")
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
	rdb := testsupport.NewTestRedis(t)
	st := store.NewSessionStore(rdb)
	clk := newFakeClock(time.Now().UnixMilli())
	svc := service.NewSessionServiceWithClock(st, store.NewRevokePublisher(rdb), clk.Now)
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
	// 连续三次，每次推进 31 秒：31s、62s、93s。
	//
	// 每轮必须手动清掉 rot: 锁。该锁的 TTL 走真实墙钟（Redis SETNX），
	// 而服务用的是注入的假时钟——测试整体只跑几十毫秒，锁一旦拿走就会
	// 一直握到测试结束，后两轮的轮换根本不会发生。不清锁的话这个用例
	// 名义上轮换三次、实际只轮换一次，"多次轮换后 FirstAuthAt 仍不变"
	// 这个性质就没被验证到。
	for i := 0; i < 3; i++ {
		if err := rdb.Del(ctx, "fp:lock:rot:"+sess.ID).Err(); err != nil {
			t.Fatalf("清理轮换锁: %v", err)
		}
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

	// 如实断言 ListByUser 的行为：过渡期内新旧 token 并存，所以它返回 **2 条**，
	// 两条共享同一个会话 ID。
	//
	// 这里刻意不在测试里先按 ID 去重再断言"只有一个"——那样写的话，
	// 无论 ListByUser 返回几条都必然通过，是个自我实现的断言。
	// 面向展示的去重是 HTTP 层的职责（管理端的在线设备列表按 Session.ID 去重），
	// 不是这一层的。
	if len(list) != 2 {
		t.Fatalf("过渡期内 ListByUser 返回 %d 条, want 2（新旧 token 各一条）", len(list))
	}
	if list[0].ID != list[1].ID {
		t.Fatalf("两条应共享同一会话 ID: %q vs %q", list[0].ID, list[1].ID)
	}
	if list[0].ID != sess.ID {
		t.Fatalf("会话 ID 变了: %q → %q", sess.ID, list[0].ID)
	}
}

// 轮换必须把 IssuedAt 重置为当前时刻。
//
// 不重置的话，新 token 一签发就已经"到轮换期"了——之后每一次校验都会
// 再轮换一次，token 无限翻新，写放大且每次都要把新 token 回传客户端。
//
// 这条性质与"FirstAuthAt 不变"是**正交**的：
// TestRotationDoesNotResetMaxLifetime 只钉住后者，删掉 IssuedAt 的重置
// 它照样全绿。必须单独立一个用例。
func TestRotationResetsIssuedAt(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	st := store.NewSessionStore(rdb)
	clk := newFakeClock(time.Now().UnixMilli())
	svc := service.NewSessionServiceWithClock(st, store.NewRevokePublisher(rdb), clk.Now)
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
		t.Fatal("应当触发轮换")
	}
	if res.Session.IssuedAt != clk.Now() {
		t.Fatalf("轮换后 IssuedAt = %d, want %d", res.Session.IssuedAt, clk.Now())
	}

	// 清掉轮换锁，排除"锁挡住了第二次轮换"这个混淆因素，
	// 让断言真正落在 IssuedAt 上而不是落在锁上。
	if err := rdb.Del(ctx, "fp:lock:rot:"+sess.ID).Err(); err != nil {
		t.Fatalf("清理轮换锁: %v", err)
	}

	// 只推进 1 秒，远未到 30 秒的轮换间隔——新 token 不该再次轮换
	clk.Advance(1 * time.Second)
	again, err := svc.Validate(ctx, res.NewToken, app)
	if err != nil {
		t.Fatalf("二次 Validate: %v", err)
	}
	if again.Rotated {
		t.Fatal("新 token 刚签发 1 秒就又轮换了——IssuedAt 没被重置，" +
			"结果是每次校验都翻新一次 token")
	}
}

// 移动端有独立的空闲超时档位，轮换与延期都必须沿用它。
//
// 其余用例里 IdleTimeoutSeconds 与 IdleTimeoutMobileSeconds 取值相同，
// 把 IdleTimeoutFor(sess.Mobile) 改成 IdleTimeoutFor(false) 一个都测不出来。
func TestRotationUsesMobileIdleTimeout(t *testing.T) {
	svc, clk := newSessionService(t)
	app := testApp(func(p *domain.SessionPolicy) {
		p.IdleTimeoutSeconds = 100
		p.IdleTimeoutMobileSeconds = 5000 // 与 web 档位拉开差距
		p.MaxLifetimeSeconds = 100000
		p.RotateIntervalSeconds = 30
		p.ExtendIntervalSeconds = 10
		p.TokenCacheTTLSeconds = 20
	})
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{
		UserID: uuid.New(), App: app, Mobile: true,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if sess.IdleExpiresAt != clk.Now()+5000*1000 {
		t.Fatalf("签发时未用移动端档位: IdleExpiresAt = %d", sess.IdleExpiresAt)
	}

	clk.Advance(31 * time.Second)
	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.Rotated {
		t.Fatal("应当触发轮换")
	}
	if !res.Session.Mobile {
		t.Fatal("轮换后 Mobile 标记丢失")
	}
	if want := clk.Now() + 5000*1000; res.Session.IdleExpiresAt != want {
		t.Fatalf("轮换后 IdleExpiresAt = %d, want %d（应沿用移动端档位，不是 web 的 100s）",
			res.Session.IdleExpiresAt, want)
	}
}
