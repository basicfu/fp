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
	st, pub, clk := newSessionParts(t)
	svc := service.NewSessionServiceWithClock(st, pub, clk.Now)
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
		case ev := <-events:
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
	svc := service.NewSessionServiceWithClock(st, pub, func() int64 { return time.Now().UnixMilli() })
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
	case ev := <-events:
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
