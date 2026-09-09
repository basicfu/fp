package grpcapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

// TestGlobalRevokeReachesEveryApp 守住规则 1。
//
// 改密与冻结产生的事件 AppID 是 uuid.Nil，含义是"跨全部应用"。
// 把它当成一个具体应用 ID 去做等值过滤，这两类撤销一条也推不出去——
// 而按应用过滤的单点踢下线照常工作，所以只测踢下线的话这个 bug 完全隐形。
// 后果：管理员改了密码，被盗用的会话在 SDK 缓存里继续畅通一整个 cache_ttl。
func TestGlobalRevokeReachesEveryApp(t *testing.T) {
	hub, pub := newTestHub(t)
	appA, appB := uuid.New(), uuid.New()

	chA, closeA := hub.Subscribe(appA)
	defer closeA()
	chB, closeB := hub.Subscribe(appB)
	defer closeB()

	pub.Publish(context.Background(), domain.RevokeEvent{
		Tokens:  []string{"tok"},
		UserIDs: []uuid.UUID{uuid.New()},
		AppID:   uuid.Nil, // 跨应用
		Reason:  domain.RevokeReasonPasswordChanged,
	})

	for name, ch := range map[string]<-chan HubEvent{"A": chA, "B": chB} {
		select {
		case ev := <-ch:
			if len(ev.Revoke.Tokens) != 1 {
				t.Fatalf("应用 %s 收到的事件 token 数为 %d", name, len(ev.Revoke.Tokens))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("应用 %s 没有收到跨应用撤销事件", name)
		}
	}
}

// TestScopedRevokeDoesNotLeakToOtherApps 是规则 1 的另一半。
//
// 少了它，一个"全部事件推给全部订阅者"的实现也能让上面那条通过——
// 而那意味着 A 应用能实时看到 B 应用的 token 列表。
func TestScopedRevokeDoesNotLeakToOtherApps(t *testing.T) {
	hub, pub := newTestHub(t)
	appA, appB := uuid.New(), uuid.New()

	chA, closeA := hub.Subscribe(appA)
	defer closeA()
	chB, closeB := hub.Subscribe(appB)
	defer closeB()

	pub.Publish(context.Background(), domain.RevokeEvent{
		Tokens: []string{"tok"}, UserIDs: []uuid.UUID{uuid.New()}, AppID: appA,
		Reason: domain.RevokeReasonKick,
	})

	select {
	case <-chA:
	case <-time.After(2 * time.Second):
		t.Fatal("目标应用没收到事件")
	}
	select {
	case ev := <-chB:
		t.Fatalf("其他应用收到了不属于它的撤销事件，泄露了 token: %v", ev.Revoke.Tokens)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestSlowSubscriberDoesNotBlockOthers 守住规则 2。
//
// 一条卡住的 gRPC 流（客户端进程被 SIGSTOP、网络黑洞、对端不读）会让写入
// 阻塞。若中继是同步写，这一条流就会把整个扇出堵死，所有其他应用的推送
// 全部停摆——一个客户端的故障演变成全平台的撤销推送失效。
func TestSlowSubscriberDoesNotBlockOthers(t *testing.T) {
	hub, pub := newTestHub(t)
	slowApp, fastApp := uuid.New(), uuid.New()

	// 订阅但**不读**，把它的缓冲撑满。
	_, closeSlow := hub.Subscribe(slowApp)
	defer closeSlow()
	fast, closeFast := hub.Subscribe(fastApp)
	defer closeFast()

	ctx := context.Background()
	for i := 0; i < revokeBufferSize*3; i++ {
		pub.Publish(ctx, domain.RevokeEvent{
			Tokens: []string{"tok"}, UserIDs: []uuid.UUID{uuid.New()}, AppID: slowApp,
			Reason: domain.RevokeReasonKick,
		})
	}
	// 慢订阅者已经溢出；此时给快订阅者发一条，必须照常送达。
	pub.Publish(ctx, domain.RevokeEvent{
		Tokens: []string{"live"}, UserIDs: []uuid.UUID{uuid.New()}, AppID: fastApp,
		Reason: domain.RevokeReasonKick,
	})

	select {
	case ev := <-fast:
		if ev.Revoke.Tokens[0] != "live" {
			t.Fatalf("快订阅者收到的是 %v", ev.Revoke.Tokens)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("一个卡住的订阅者把整个中继堵死了")
	}
}

// TestUnsubscribeRemovesSubscriber 守住规则 3。
func TestUnsubscribeRemovesSubscriber(t *testing.T) {
	hub, _ := newTestHub(t)
	appID := uuid.New()

	before := hub.subscriberCount()
	_, cancel := hub.Subscribe(appID)
	if hub.subscriberCount() != before+1 {
		t.Fatalf("Subscribe 后订阅者数为 %d，期望 %d", hub.subscriberCount(), before+1)
	}
	cancel()
	if got := hub.subscriberCount(); got != before {
		t.Fatalf("注销后订阅者数为 %d，期望回到 %d——每条断开的流都会永久占一个槽", got, before)
	}
	// 重复注销不应 panic 或把别人的槽删掉。
	cancel()
	if got := hub.subscriberCount(); got != before {
		t.Fatalf("重复注销后订阅者数变成 %d", got)
	}
}

// TestGapBecomesPurgeForEverySubscriber 守住 Step 2 的整条链路。
//
// Redis 订阅重建 → store 发出 RevokeSignalGap → hub 转成 Purge → 推给
// **所有**订阅者。少了最后一环，前面检测到重连也白搭。
//
// 断言"所有订阅者"而不是"某个订阅者"：触发 purge 的前提是不知道丢了
// 哪些事件，因而也不知道涉及哪些应用。按 appID 过滤 purge 是在没有依据的
// 情况下缩小范围——那种实现只测一个订阅者时完全看不出问题。
func TestGapBecomesPurgeForEverySubscriber(t *testing.T) {
	hub, signals := newTestHubWithFakeSignals(t) // 直接投喂信号，不依赖真实 Redis 断连
	appA, appB := uuid.New(), uuid.New()

	chA, closeA := hub.Subscribe(appA)
	defer closeA()
	chB, closeB := hub.Subscribe(appB)
	defer closeB()

	signals <- store.RevokeSignal{Kind: store.RevokeSignalGap}

	for name, ch := range map[string]<-chan HubEvent{"A": chA, "B": chB} {
		select {
		case ev := <-ch:
			if !ev.Purge {
				t.Fatalf("订阅者 %s 收到的不是 purge：%+v", name, ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("订阅者 %s 没有收到 purge——"+
				"订阅重建被检测到了，但没转成对 SDK 的指令", name)
		}
	}
}

// TestPurgeClosesStreamWhenBufferFull 守住"purge 不能被静默丢弃"。
//
// fanout 丢一条撤销是安全的（那个 token 最多多活一个 cache_ttl），
// 但丢一条 purge 会让 SDK 的缓存停在一个**已知不可信**的状态，
// 而且没有任何后续机制会纠正它。所以缓冲满时必须关掉这条流，
// 逼 SDK 重连并自行 purge。
//
// 复用 fanout 的 default 分支（直接丢弃）能让上一条测试通过，
// 却在这里失败——这正是两者代价不对等的地方。
func TestPurgeClosesStreamWhenBufferFull(t *testing.T) {
	hub, signals := newTestHubWithFakeSignals(t)
	appID := uuid.New()

	ch, cancel := hub.Subscribe(appID)
	defer cancel()

	// 不读，把缓冲灌满。
	for i := 0; i < revokeBufferSize+10; i++ {
		signals <- store.RevokeSignal{Kind: store.RevokeSignalEvent, Event: domain.RevokeEvent{
			Tokens: []string{"tok"}, UserIDs: []uuid.UUID{uuid.New()}, AppID: appID,
			Reason: domain.RevokeReasonKick,
		}}
	}
	signals <- store.RevokeSignal{Kind: store.RevokeSignalGap}

	// signals 是无缓冲的：run 是单个 goroutine，处理完 Gap（即 broadcastPurge
	// 对 sub.ch 的那次投递尝试）之后才会循环回 select 接收下一条。这里紧接着
	// 再喂一条不相关应用的哨兵事件——它的发送必须等 run 处理完 Gap、回到
	// select 才能完成，这就把"broadcastPurge 已经尝试过投递"这件事变成了
	// 可以同步等待的东西。
	//
	// 少了这一步会怎样：下面的排空循环会和 broadcastPurge 并发执行——排空
	// 先读走几个缓冲项，腾出空间，导致 purge 走了正常投递而不是"缓冲满，
	// 关闭流"那条路径。此后 ch 既不会再收到新值也不会关闭，排空循环会
	// 一直阻塞到超时，把"purge 被正常投递"误判成"purge 被丢弃"。
	// 这是实测触发过的真实竞态，不是理论上的可能性。
	signals <- store.RevokeSignal{Kind: store.RevokeSignalEvent, Event: domain.RevokeEvent{
		Tokens: []string{"sentinel"}, UserIDs: []uuid.UUID{uuid.New()}, AppID: uuid.New(),
		Reason: domain.RevokeReasonKick,
	}}

	// 排空缓冲，最终必须读到 channel 关闭，而不是一直读到耗尽后阻塞。
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel 已关闭，符合预期
			}
		case <-deadline:
			t.Fatal("缓冲满时 purge 被静默丢弃了——" +
				"SDK 的缓存会停在一个已知不可信的状态，且不会有任何机制纠正")
		}
	}
}

// TestHubUsesExactlyOneRedisSubscription 守住"只开一份订阅"。
//
// 每条流一份 Redis 订阅在功能上完全正确，只是把连接数变成流数的倍数。
// 断言写成"多个订阅者只对应一次 Subscribe 调用"。
func TestHubUsesExactlyOneRedisSubscription(t *testing.T) {
	hub, _ := newTestHub(t)
	for i := 0; i < 5; i++ {
		_, cancel := hub.Subscribe(uuid.New())
		defer cancel()
	}
	if got := hub.redisSubscribeCalls(); got != 1 {
		t.Fatalf("5 个订阅者触发了 %d 次 Redis 订阅，期望 1 次", got)
	}
}

// newTestHub 起一个已确认订阅 Redis 撤销频道的 RevokeHub，背后是真实的
// *store.RevokePublisher（testsupport 提供的局域网 Redis）。用于验证
// "事件真的经过 Redis 走了一遭"的测试。
func newTestHub(t *testing.T) (*RevokeHub, *store.RevokePublisher) {
	t.Helper()
	pub := store.NewRevokePublisher(testsupport.NewTestRedis(t))
	return startTestHub(t, pub), pub
}

// startTestHub 起一个接在 pub 上、已确认完成 Redis 订阅的 RevokeHub。
// newTestHub 与 env_test.go 的 newGRPCEnv 共用这个装配逻辑——后者需要
// 自己已有的 pub（同一个 *store.RevokePublisher 既用于 sessions 发布
// 撤销，也用于 hub 订阅撤销，与生产环境 cmd/fp/main.go 的装配一致）。
//
// 不直接 `go hub.Run(ctx)` 再返回：Run 内部的 h.subscribe(ctx) 是阻塞到
// SUBSCRIBE 确认才返回的，但"起一个协程调 Run"和"Run 内部完成订阅"之间
// 没有同步点——调用方如果紧接着做一次会触发撤销广播的操作（pub.Publish，
// 或者 newGRPCEnv 场景下的 Logout），就是在和"服务端到底订上了没"赛跑。
// 这里改成在调用方所在的 goroutine 里同步跑完订阅这一步（复用带计数的
// h.subscribe，顺带让 redisSubscribeCalls 有意义），再把纯分发循环 run
// 放到后台，保证函数返回时订阅已经确认建立。
func startTestHub(t *testing.T, pub *store.RevokePublisher) *RevokeHub {
	t.Helper()
	hub := NewRevokeHub(pub)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	signals, closeFn, err := hub.subscribe(ctx)
	if err != nil {
		t.Fatalf("hub 订阅撤销频道: %v", err)
	}
	t.Cleanup(closeFn)
	go func() { _ = hub.run(ctx, signals) }()

	return hub
}

// newTestHubWithFakeSignals 起一个 RevokeHub，但分发循环喂的是测试直接控制的
// signal channel，完全绕开 Redis。用于验证 hub 收到信号之后的行为本身
// （尤其 Gap → Purge），不依赖真实断连去制造 RevokeSignalGap——那既慢又不稳定，
// 而这里要测的是 hub 收到信号之后做了什么，不是信号怎么产生的。信号怎么
// 产生由 store 包的 TestSubscribeSurfacesResubscribeAsGap 单独验证。
func newTestHubWithFakeSignals(t *testing.T) (*RevokeHub, chan<- store.RevokeSignal) {
	t.Helper()
	hub := newRevokeHub()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	signals := make(chan store.RevokeSignal)
	go func() { _ = hub.run(ctx, signals) }()

	return hub, signals
}

// TestSubscribeAllReceivesEveryApp：type=im 的订阅者要收到所有应用的撤销。
//
// fp-im 只有一条到 fp 的连接、一条 Watch 流，服务的却是所有应用。按 app
// 过滤的话它只能收到一个应用的撤销，其余应用被踢下线的用户 ws 会一直挂到
// 空闲超时。
func TestSubscribeAllReceivesEveryApp(t *testing.T) {
	h := newRevokeHub()
	events, unsub := h.SubscribeAll()
	defer unsub()

	appA, appB := uuid.New(), uuid.New()
	h.fanout(domain.RevokeEvent{AppID: appA, Tokens: []string{"ta"}})
	h.fanout(domain.RevokeEvent{AppID: appB, Tokens: []string{"tb"}})

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case ev := <-events:
			got[ev.Revoke.Tokens[0]] = true
		case <-time.After(time.Second):
			t.Fatalf("只收到 %d 条事件，期望 2 条", i)
		}
	}
	if !got["ta"] || !got["tb"] {
		t.Fatalf("收到的事件 = %v，两个应用的都要收到", got)
	}
}

// TestSubscribeByAppStillFilters：通配订阅不能把既有的按 app 过滤弄坏。
// 业务方 SDK 绝不能收到别的应用的 token 列表。
func TestSubscribeByAppStillFilters(t *testing.T) {
	h := newRevokeHub()
	appA, appB := uuid.New(), uuid.New()
	events, unsub := h.Subscribe(appA)
	defer unsub()

	h.fanout(domain.RevokeEvent{AppID: appB, Tokens: []string{"tb"}})
	h.fanout(domain.RevokeEvent{AppID: appA, Tokens: []string{"ta"}})

	select {
	case ev := <-events:
		if ev.Revoke.Tokens[0] != "ta" {
			t.Fatalf("收到了别的应用的事件：%v", ev.Revoke.Tokens)
		}
	case <-time.After(time.Second):
		t.Fatal("本应用的事件没收到")
	}
}
