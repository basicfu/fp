package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

// sessionEnv 是批量撤销测试需要的一套依赖：一个可用的 SessionService、一个
// 不落库的测试应用，以及建号与抓事件的辅助方法。
//
// 与 accountEnv 的区别：这里的测试只关心"发了几条事件、每条带了什么"，
// 不涉及 AccountService 的账号状态耦合。
//
// clock 用可控假时钟驱动（而不是真实时钟）：轮换交接的测试
// （session_handoff_test.go）需要在"签发"与"校验"之间精确推进时间来
// 越过 rotate_interval，本文件既有的批量撤销用例都不依赖真实时间流逝，
// 切换到假时钟对它们零影响——这与 accountEnv 当初做同样切换时的理由一致。
//
// store 是 sessions 内部使用的同一个 *store.SessionStore：轮换交接的测试
// 需要绕过 SessionService 直接摆弄 Redis 状态（比如显式删除会话模拟 TTL
// 清理后的状态），光拿到 *service.SessionService 做不到这一点。
//
// userID 是一个合成的 UUID：会话相关的测试不要求 userID 对应真实的
// app_user 行（SessionStore 只认 Redis 里的 token/索引），不必每个用例
// 都经过 UserService/Postgres 建号。
type sessionEnv struct {
	sessions *service.SessionService
	users    *service.UserService
	pub      *store.RevokePublisher
	store    *store.SessionStore
	app      *domain.Application
	userID   uuid.UUID
	clock    *fakeClock
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	pub := store.NewRevokePublisher(rdb)
	sessStore := store.NewSessionStore(rdb)
	clock := newFakeClock(time.Now().UnixMilli())
	return &sessionEnv{
		sessions: service.NewSessionServiceWithClock(sessStore, pub, store.NewEpochStore(rdb), clock.Now),
		users:    service.NewUserService(pool),
		pub:      pub,
		store:    sessStore,
		app:      testApp(),
		userID:   uuid.New(),
		clock:    clock,
	}
}

// newActiveUser 建一个状态为 ACTIVE 的新用户，实现与 accountEnv.newActiveUser
// 共享（见 account_test.go 的 newActiveUserWith），避免两份环境各写一套建号逻辑。
func (e *sessionEnv) newActiveUser(t *testing.T) *domain.User {
	t.Helper()
	return newActiveUserWith(t, e.users)
}

// appWithPolicy 造一个不落库的应用，套用给定的会话策略改动。
// 与 e.app 的区别是每次调用都返回一个独立实例，互不干扰。
//
// 实现与 accountEnv.appWithPolicy 相同（见 account_test.go），两份 env
// 各自持有一份是因为它们服务不同的测试文件分组，而不是行为有分歧。
func (e *sessionEnv) appWithPolicy(t *testing.T, mutate func(*domain.SessionPolicy)) *domain.Application {
	t.Helper()
	return testApp(mutate)
}

// seedSessions 造出至少 n 个会话，用少量用户各开多个会话——比逐个建 DB 用户
// 快得多。撤销与纪元都不要求 userID 对应真实的 app_user 行（SessionStore
// 只认 Redis 里的 token/索引，纪元键也只是按 UUID 计数），所以这里直接用
// 合成的 UUID，不必经过 UserService/Postgres。
func (e *sessionEnv) seedSessions(t *testing.T, n int) ([]uuid.UUID, int) {
	t.Helper()
	ctx := context.Background()
	const userCount = 10
	perUser := (n + userCount - 1) / userCount // 向上取整，保证总数 >= n

	userIDs := make([]uuid.UUID, userCount)
	total := 0
	for i := range userIDs {
		userIDs[i] = uuid.New()
		for j := 0; j < perUser; j++ {
			if _, err := e.sessions.Issue(ctx, service.IssueInput{UserID: userIDs[i], App: e.app}); err != nil {
				t.Fatalf("seedSessions: Issue: %v", err)
			}
			total++
		}
	}
	return userIDs, total
}

// eventCapture 收集一次测试期间到达的撤销事件。
type eventCapture struct {
	events <-chan store.RevokeSignal
}

// captureEvents 订阅撤销频道。清理交给 t.Cleanup，测试本身不用管收尾。
func (e *sessionEnv) captureEvents(t *testing.T) *eventCapture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	events, closeFn, err := e.pub.Subscribe(ctx)
	if err != nil {
		cancel()
		t.Fatalf("captureEvents: Subscribe: %v", err)
	}
	t.Cleanup(func() {
		closeFn()
		cancel()
	})
	return &eventCapture{events: events}
}

// collectSettle 是"收满期望条数"之后，再多等一会看是否有多余事件到达的静默期。
// 局域网 Redis 的 pub/sub 往返在亚毫秒级，300ms 足够放大到能稳定观察，
// 又远小于下面的硬超时，不会显著拖慢测试。
const collectSettle = 300 * time.Millisecond

// collect 收集事件，直到"已收满 want 条、且又过了 collectSettle 静默期无新事件到达"
// 或 2 秒硬超时，返回已收到的全部。
//
// 不能一收满 want 条就立刻返回：一个把 5 个用户拆成 5 条广播的错误实现，
// 收到第 1 条就会让 collect(t, 1) 立即返回，"多发了 4 条"这个事实永远没有
// 机会被观察到——而少发广播正是批量撤销唯一的意义。
func (c *eventCapture) collect(t *testing.T, want int) []domain.RevokeEvent {
	t.Helper()
	var got []domain.RevokeEvent
	hardDeadline := time.Now().Add(2 * time.Second)
	for {
		wait := time.Until(hardDeadline)
		if wait <= 0 {
			return got
		}
		if len(got) >= want && wait > collectSettle {
			wait = collectSettle
		}
		select {
		case sig, ok := <-c.events:
			if !ok {
				return got
			}
			// 批量撤销测试期间不该出现订阅重建（Gap）；出现的话说明测试
			// 跑在一个不稳定的 Redis 连接上，静默吞掉只会让下面的条数断言
			// 变得难以解释，所以这里直接跳过、不计入——它不是本文件要测的
			// 内容（Gap → Purge 的行为由 internal/grpcapi 的 watch_test.go
			// 专门覆盖）。
			if sig.Kind != store.RevokeSignalEvent {
				continue
			}
			got = append(got, sig.Event)
		case <-time.After(wait):
			return got
		}
	}
}

// TestRevokeUsersEmitsOneEventForManyUsers 是批量的全部意义。
//
// 逐用户发的话，一次踢 500 个用户 = 500 条广播 × 扇出到每个 fp 实例 ×
// 每个 SDK 处理 500 次。合并之后是 1 条。
//
// 断言"事件条数"而不是"token 都被撤销了"：一个内部 for 循环逐个调
// RevokeUser 的实现，功能完全正确、所有会话也确实失效了，
// 唯独没有减少任何广播——而减少广播正是本改动的唯一目的。
func TestRevokeUsersEmitsOneEventForManyUsers(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()

	var userIDs []uuid.UUID
	var tokens []string
	for i := 0; i < 5; i++ {
		u := env.newActiveUser(t)
		userIDs = append(userIDs, u.ID)
		sess, err := env.sessions.Issue(ctx, service.IssueInput{UserID: u.ID, App: env.app})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		tokens = append(tokens, sess.Token)
	}

	events := env.captureEvents(t) // 订阅撤销频道，收集广播

	n, err := env.sessions.RevokeUsers(ctx, userIDs, domain.RevokeReasonKick)
	if err != nil {
		t.Fatalf("RevokeUsers: %v", err)
	}
	if n != len(tokens) {
		t.Fatalf("撤销了 %d 个 token，期望 %d 个", n, len(tokens))
	}

	got := events.collect(t, 1) // 等最多 2 秒，收齐已到达的事件
	if len(got) != 1 {
		t.Fatalf("5 个用户产生了 %d 条广播，期望 1 条——"+
			"实现多半是内部 for 循环逐个调 RevokeUser，功能对但没省下任何广播", len(got))
	}
	if len(got[0].Tokens) != len(tokens) {
		t.Fatalf("事件里带了 %d 个 token，期望 %d 个", len(got[0].Tokens), len(tokens))
	}
	if len(got[0].UserIDs) != len(userIDs) {
		t.Fatalf("事件里带了 %d 个 userID，期望 %d 个", len(got[0].UserIDs), len(userIDs))
	}

	// 所有会话都必须真的失效。
	for _, tok := range tokens {
		if _, err := env.sessions.Validate(ctx, tok, env.app); err == nil {
			t.Fatalf("token %q 仍然有效", tok)
		}
	}
}

// TestRevokeUserDelegatesToBatch 守住"批量是唯一实现"。
//
// 单用户路径必须调批量方法传一个元素，而不是各写一套。两套实现意味着
// 修一个 bug 要改两处，而漏改的那一处不会有任何测试变红——
// 因为两条路径各有各的测试，都是绿的。
//
// 断言写成"单用户撤销产生的事件形状与批量一致"：
// 独立实现的那一版多半还在用旧的单值 UserID 形状。
func TestRevokeUserDelegatesToBatch(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()
	user := env.newActiveUser(t)

	// 简报给出的原始测试体把 Issue 的返回值存进 sess 却从未使用——Go 对
	// "声明但未使用的局部变量"是硬编译错误，这里用 _ 丢弃，不额外发挥。
	if _, err := env.sessions.Issue(ctx, service.IssueInput{UserID: user.ID, App: env.app}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	events := env.captureEvents(t)

	if _, err := env.sessions.RevokeUser(ctx, user.ID, domain.RevokeReasonKick); err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}

	got := events.collect(t, 1)
	if len(got) != 1 {
		t.Fatalf("单用户撤销产生了 %d 条事件", len(got))
	}
	if len(got[0].UserIDs) != 1 || got[0].UserIDs[0] != user.ID {
		t.Fatalf("事件的 UserIDs 是 %v，期望恰好 [%v]", got[0].UserIDs, user.ID)
	}
}

// TestLargeRevocationIsSplitIntoBoundedEvents 守住单条事件的体积上限。
//
// 一万个用户 × 每人 3 个会话 = 三万个 token，序列化后约 1 MB。广播给 20 个
// fp 实例是 20 MB，SDK 侧还要吃一个逼近 gRPC 默认 4 MB 接收上限的消息。
// 不切片的话，"批量"就从优化变成了新的故障源。
//
// 用 maxTokensPerEvent + 1 个 token 触发切片，断言产生 2 条且每条都不超上限。
func TestLargeRevocationIsSplitIntoBoundedEvents(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()

	// 造出 maxTokensPerEvent + 1 个会话。用少量用户各开多个会话最省时间。
	userIDs, total := env.seedSessions(t, service.MaxTokensPerEventForTest+1)

	events := env.captureEvents(t)
	if _, err := env.sessions.RevokeUsers(ctx, userIDs, domain.RevokeReasonKick); err != nil {
		t.Fatalf("RevokeUsers: %v", err)
	}

	got := events.collect(t, 2)
	if len(got) < 2 {
		t.Fatalf("%d 个 token 只产生了 %d 条事件——没有切片，"+
			"单条消息会随撤销规模无界增长", total, len(got))
	}
	sum := 0
	for i, ev := range got {
		if len(ev.Tokens) > service.MaxTokensPerEventForTest {
			t.Fatalf("第 %d 条事件带了 %d 个 token，超过上限 %d",
				i, len(ev.Tokens), service.MaxTokensPerEventForTest)
		}
		sum += len(ev.Tokens)
	}
	if sum != total {
		t.Fatalf("切片后 token 总数为 %d，期望 %d——切片过程中丢了", sum, total)
	}
}

// TestRevokeUsersSkipsUsersWithoutSessions 确认空用户不产生噪声。
//
// 批量踢 500 个用户，其中大多数本来就不在线是常态。给它们各发一条空事件
// 等于把刚省下的广播又加回来。
func TestRevokeUsersSkipsUsersWithoutSessions(t *testing.T) {
	env := newSessionEnv(t)
	ctx := context.Background()

	online := env.newActiveUser(t)
	if _, err := env.sessions.Issue(ctx, service.IssueInput{UserID: online.ID, App: env.app}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	offline := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	events := env.captureEvents(t)
	if _, err := env.sessions.RevokeUsers(ctx, append(offline, online.ID), domain.RevokeReasonKick); err != nil {
		t.Fatalf("RevokeUsers: %v", err)
	}

	got := events.collect(t, 1)
	if len(got) != 1 {
		t.Fatalf("产生了 %d 条事件，期望 1 条", len(got))
	}
	if len(got[0].UserIDs) != 1 || got[0].UserIDs[0] != online.ID {
		t.Fatalf("事件的 UserIDs 是 %v，期望只含有会话的那一个用户", got[0].UserIDs)
	}
}

// TestRevokeUsersIsAtomicPerUserForEpoch 确认批量也逐个递增纪元。
//
// 纪元是用户级的，批量撤销必须给**每个**用户都递增，不能只递增第一个
// 或者引入一个"批次纪元"。漏递增的那些用户，正在签发路上的会话拦不住。
func TestRevokeUsersIsAtomicPerUserForEpoch(t *testing.T) {
	env := newAccountEnv(t)
	ctx := context.Background()

	var ids []uuid.UUID
	before := map[uuid.UUID]int64{}
	for i := 0; i < 3; i++ {
		u := env.newActiveUser(t)
		ids = append(ids, u.ID)
		before[u.ID], _ = env.epochs.Current(ctx, u.ID)
	}

	if _, err := env.accounts.RevokeUsersSessions(ctx, ids); err != nil {
		t.Fatalf("RevokeUsersSessions: %v", err)
	}
	for _, id := range ids {
		after, _ := env.epochs.Current(ctx, id)
		if after <= before[id] {
			t.Fatalf("用户 %v 的纪元没有递增（%d → %d）", id, before[id], after)
		}
	}
}
