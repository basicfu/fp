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

func TestRevokeSingleToken(t *testing.T) {
	svc, _ := newSessionService(t)
	app := testApp()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := svc.Revoke(ctx, sess.Token, domain.RevokeReasonLogout); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := svc.Validate(ctx, sess.Token, app); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	// 撤销不存在的 token 不应报错（重复登出、并发登出都会走到这里）
	if err := svc.Revoke(ctx, sess.Token, domain.RevokeReasonLogout); err != nil {
		t.Fatalf("重复 Revoke: %v", err)
	}
}

func TestRevokeUserRevokesAllSessions(t *testing.T) {
	svc, _ := newSessionService(t)
	app := testApp()
	uid := uuid.New()
	ctx := context.Background()

	var tokens []string
	for i := 0; i < 3; i++ {
		sess, err := svc.Issue(ctx, service.IssueInput{UserID: uid, App: app})
		if err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
		tokens = append(tokens, sess.Token)
	}
	// 另一个用户不受影响
	otherUID := uuid.New()
	other, err := svc.Issue(ctx, service.IssueInput{UserID: otherUID, App: app})
	if err != nil {
		t.Fatalf("Issue other: %v", err)
	}

	n, err := svc.RevokeUser(ctx, uid, domain.RevokeReasonFreeze)
	if err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}
	if n != 3 {
		t.Fatalf("撤销数 = %d, want 3", n)
	}
	for i, tok := range tokens {
		if _, err := svc.Validate(ctx, tok, app); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("第 %d 个 token 仍有效", i)
		}
	}
	if _, err := svc.Validate(ctx, other.Token, app); err != nil {
		t.Fatalf("其他用户的会话被误伤: %v", err)
	}
}

// 撤销一个会话必须把它在轮换过渡期里的旧 token 一并作废，
// 否则「踢下线」之后旧 token 还能再用一个过渡期。
func TestRevokeSessionCoversTokensFromRotation(t *testing.T) {
	svc, clk := newSessionService(t)
	app := rotateApp()
	uid := uuid.New()
	ctx := context.Background()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uid, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	oldToken := sess.Token

	clk.Advance(31 * time.Second)
	res, err := svc.Validate(ctx, oldToken, app)
	if err != nil {
		t.Fatalf("触发轮换: %v", err)
	}
	if !res.Rotated {
		t.Fatal("未触发轮换")
	}
	newToken := res.NewToken

	n, err := svc.RevokeSession(ctx, uid, sess.ID, domain.RevokeReasonKick)
	if err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if n != 2 {
		t.Fatalf("撤销数 = %d, want 2（新旧 token 都要作废）", n)
	}
	for name, tok := range map[string]string{"旧": oldToken, "新": newToken} {
		if _, err := svc.Validate(ctx, tok, app); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("%s token 仍有效", name)
		}
	}
}

// 多应用共享用户体系时，只撤销某一个应用的会话不能影响其他应用。
func TestRevokeUserInApp(t *testing.T) {
	svc, _ := newSessionService(t)
	appA := testApp()
	appB := testApp()
	uid := uuid.New()
	ctx := context.Background()

	sessA, err := svc.Issue(ctx, service.IssueInput{UserID: uid, App: appA})
	if err != nil {
		t.Fatalf("Issue A: %v", err)
	}
	sessB, err := svc.Issue(ctx, service.IssueInput{UserID: uid, App: appB})
	if err != nil {
		t.Fatalf("Issue B: %v", err)
	}

	n, err := svc.RevokeUserInApp(ctx, uid, appA.ID, domain.RevokeReasonKick)
	if err != nil {
		t.Fatalf("RevokeUserInApp: %v", err)
	}
	if n != 1 {
		t.Fatalf("撤销数 = %d, want 1", n)
	}
	if _, err := svc.Validate(ctx, sessA.Token, appA); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("应用 A 的会话应已撤销")
	}
	if _, err := svc.Validate(ctx, sessB.Token, appB); err != nil {
		t.Fatalf("应用 B 的会话被误伤: %v", err)
	}
}

func TestRevokeSessionOfAnotherUserDoesNothing(t *testing.T) {
	svc, _ := newSessionService(t)
	app := testApp()
	ctx := context.Background()

	victim, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// 用别人的 userID 去撤销该会话，必须无效
	n, err := svc.RevokeSession(ctx, uuid.New(), victim.ID, domain.RevokeReasonKick)
	if err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if n != 0 {
		t.Fatalf("撤销数 = %d, want 0", n)
	}
	if _, err := svc.Validate(ctx, victim.Token, app); err != nil {
		t.Fatalf("会话被越权撤销: %v", err)
	}
}

// 撤销必须广播事件，计划二的 gRPC 流靠它把撤销推给 SDK。
func TestRevokePublishesEvent(t *testing.T) {
	st, pub, ep, clk := newSessionParts(t)
	svc := service.NewSessionServiceWithClock(st, pub, ep, clk.Now)
	app := testApp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, closeFn, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer closeFn()

	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		if err := svc.Revoke(ctx, sess.Token, domain.RevokeReasonKick); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		select {
		case sig := <-events:
			if sig.Kind != store.RevokeSignalEvent {
				t.Fatalf("Kind = %v, want RevokeSignalEvent", sig.Kind)
			}
			ev := sig.Event
			if len(ev.Tokens) == 0 || ev.Tokens[0] != sess.Token {
				t.Fatalf("事件 Tokens = %v, want [%s]", ev.Tokens, sess.Token)
			}
			if ev.Reason != domain.RevokeReasonKick {
				t.Fatalf("Reason = %q", ev.Reason)
			}
			return
		case <-time.After(100 * time.Millisecond):
			// 第一次 Revoke 已把 token 删掉，后续 Revoke 不会再发事件，
			// 因此这里重新签发一个再试。
			s2, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
			if err != nil {
				t.Fatalf("重新 Issue: %v", err)
			}
			sess = s2
		case <-deadline:
			t.Fatal("3 秒内未收到撤销事件")
		}
	}
}

// revokeMatching 中途出错时，已经删掉的 token 必须照样广播出去——它们在
// Redis 里已经真的没了（权威撤销已完成），如果因为报错就跳过 announce，
// SDK 会继续拿本地缓存放行这些其实已经失效的 token，直到最长一个 cache_ttl。
//
// 用直接把某个 token 的 Redis 值改成非法 JSON 的方式，让 store.Get 在处理
// 到它时返回一个"非 ErrNotFound"的真实错误（ErrNotFound 会被 revokeMatching
// 当成正常跳过，不会走到出错分支）。哪个 token 会被枚举到不受这次破坏影响
// ——损坏的是它的会话内容，不是 fp:usess:{uid} 这个成员集合本身——但
// SMEMBERS 返回成员的顺序不保证等于插入顺序，所以这里先用一次探测调用
// 记录真实顺序，再破坏顺序里的最后一个：这样前面几个必然会在它之前被
// revokeMatching 成功处理，不依赖猜测哪个先被枚举到。
func TestRevokeAnnouncesPartialProgressOnError(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	st := store.NewSessionStore(rdb)
	pub := store.NewRevokePublisher(rdb)
	ep := store.NewEpochStore(rdb)
	svc := service.NewSessionServiceWithClock(st, pub, ep, func() int64 { return time.Now().UnixMilli() })
	app := testApp()
	uid := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, closeFn, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer closeFn()

	for i := 0; i < 3; i++ {
		if _, err := svc.Issue(ctx, service.IssueInput{UserID: uid, App: app}); err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
	}

	order, err := st.ListUserTokens(ctx, uid)
	if err != nil {
		t.Fatalf("ListUserTokens: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("len(order) = %d, want 3", len(order))
	}
	badToken := order[len(order)-1]
	goodTokens := order[:len(order)-1]

	// fp:sess: 是 internal/store 包内 sessionKeyPrefix 常量的值，未导出，
	// 测试只能照抄字面量——同样的做法也出现在别处需要直接摆弄 Redis 键的用例里。
	if err := rdb.Set(ctx, "fp:sess:"+badToken, "not-json", time.Minute).Err(); err != nil {
		t.Fatalf("破坏 token: %v", err)
	}

	n, err := svc.RevokeUser(ctx, uid, domain.RevokeReasonFreeze)
	if err == nil {
		t.Fatal("err = nil, want 解析损坏会话产生的错误")
	}
	if n != len(goodTokens) {
		t.Fatalf("撤销数 = %d, want %d（应等于损坏的那个之前已成功删除的条数）", n, len(goodTokens))
	}
	for _, tok := range goodTokens {
		if _, err := svc.Validate(ctx, tok, app); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("token %s 仍有效，应已被删除", tok)
		}
	}

	deadline := time.After(3 * time.Second)
	select {
	case sig := <-events:
		if sig.Kind != store.RevokeSignalEvent {
			t.Fatalf("Kind = %v, want RevokeSignalEvent", sig.Kind)
		}
		ev := sig.Event
		if len(ev.Tokens) != len(goodTokens) {
			t.Fatalf("事件 Tokens 数 = %d, want %d", len(ev.Tokens), len(goodTokens))
		}
		got := make(map[string]bool, len(ev.Tokens))
		for _, tok := range ev.Tokens {
			got[tok] = true
		}
		for _, tok := range goodTokens {
			if !got[tok] {
				t.Fatalf("事件缺少 token %s", tok)
			}
		}
		if ev.Reason != domain.RevokeReasonFreeze {
			t.Fatalf("Reason = %q", ev.Reason)
		}
	case <-deadline:
		t.Fatal("3 秒内未收到撤销事件——中途出错时，已删除的部分也应该照样广播")
	}
}

// 登出必须连带作废轮换过渡期里的兄弟 token。
//
// 过渡期内同一会话有新旧两个 token 都有效。只删调用方递上来的那个，
// 另一个还能再用最多 GraceDuration，而且不在撤销事件里——SDK 缓存也照样放行。
// 用户点了"退出登录"却还能被另一个 token 访问。
func TestRevokeCoversRotationGraceSibling(t *testing.T) {
	svc, clk := newSessionService(t)
	app := rotateApp()
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
	if !res.Rotated {
		t.Fatal("应当触发轮换")
	}
	newToken := res.NewToken

	// 用新 token 登出
	if err := svc.Revoke(ctx, newToken, domain.RevokeReasonLogout); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	for name, tok := range map[string]string{"新": newToken, "过渡期内的旧": oldToken} {
		if _, err := svc.Validate(ctx, tok, app); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("%s token 登出后仍有效, err = %v", name, err)
		}
	}
}

// revokeMatching 的 defer 必须用 context.WithoutCancel 剥掉调用方 ctx 的取消信号，
// 否则触发 defer 的最常见原因——ctx 被取消（管理员批量撤销撞上 handler 超时、
// 或客户端断开）——会连累 announce 本身：go-redis 在取连接阶段就会拒掉一个
// 已取消 ctx 上的 PUBLISH，token 已经从 Redis 删了，事件却发不出去。
//
// 用真实的 goroutine 竞态去触发"循环中途 ctx 被取消"是不确定的：Redis 往返
// 耗时不可控，没有办法可靠地卡在"删完第 N 个、还没删第 N+1 个"这个时间点上，
// 勉强用轮询去猜会得到一个偶发失败的测试，不符合这个代码库里其它用例
// （假时钟、显式占锁）一直坚持的确定性要求。
//
// 这里换一个完全确定、不依赖真实并发的构造：把"当前时间"这个已经是测试可控
// 依赖的东西，变成第二个同步点。revokeMatching 的 defer 里 s.now() 只会被
// 调用一次——用来给即将广播的事件盖时间戳——而且严格发生在整个循环（所有
// Get/Delete）已经跑完之后、announce 的 Publish 真正执行之前。把这次 s.now()
// 调用本身接上 cancel()，就能不多不少地精确复现"ctx 在 announce 即将发布时
// 已经被取消"，而不必依赖任何真实的时间竞赛。
func TestRevokeAnnounceSurvivesCtxCancellation(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	st := store.NewSessionStore(rdb)
	pub := store.NewRevokePublisher(rdb)
	ep := store.NewEpochStore(rdb)
	app := testApp()
	uid := uuid.New()

	// 订阅方用独立、不会被取消的 ctx——我们要验证的是 Publish 这一端能不能
	// 扛住 ctx 取消，不希望订阅连接自己也被牵连着关掉。
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	events, closeFn, err := pub.Subscribe(subCtx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer closeFn()

	// 签发用普通时钟，不触发取消。
	issueSvc := service.NewSessionServiceWithClock(st, pub, ep, func() int64 { return time.Now().UnixMilli() })
	sess, err := issueSvc.Issue(context.Background(), service.IssueInput{UserID: uid, App: app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	revokeCtx, cancel := context.WithCancel(context.Background())
	// 这个时钟只会在 revokeMatching 的 defer 里被调用那一次用到——
	// 此时 Delete 已经成功、循环已经跑完，cancel() 在这里触发不会打断
	// 任何一次 Redis 操作，只会让随后的 announce 拿到一个已取消的 ctx。
	revokeSvc := service.NewSessionServiceWithClock(st, pub, ep, func() int64 {
		cancel()
		return time.Now().UnixMilli()
	})

	if err := revokeSvc.Revoke(revokeCtx, sess.Token, domain.RevokeReasonLogout); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// 撤销本身（删 Redis 会话）必须已经生效，不受 ctx 后来被取消影响。
	if _, err := issueSvc.Validate(context.Background(), sess.Token, app); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("token 撤销后仍有效, err = %v", err)
	}

	deadline := time.After(3 * time.Second)
	select {
	case sig := <-events:
		if sig.Kind != store.RevokeSignalEvent {
			t.Fatalf("Kind = %v, want RevokeSignalEvent", sig.Kind)
		}
		ev := sig.Event
		if len(ev.Tokens) != 1 || ev.Tokens[0] != sess.Token {
			t.Fatalf("事件 Tokens = %v, want [%s]", ev.Tokens, sess.Token)
		}
	case <-deadline:
		t.Fatal("3 秒内未收到撤销事件——ctx 在 announce 前被取消时，事件不该丢")
	}
}
