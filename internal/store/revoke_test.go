package store_test

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestRevokePublishSubscribe(t *testing.T) {
	pub := store.NewRevokePublisher(testsupport.NewTestRedis(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals, closeFn, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer closeFn()

	uid := uuid.New()
	want := domain.RevokeEvent{
		Tokens:  []string{"tok-a", "tok-b"},
		UserIDs: []uuid.UUID{uid},
		Reason:  domain.RevokeReasonKick,
		At:      time.Now().UnixMilli(),
	}

	// 订阅建立需要一个往返，重试几次直到收到。
	deadline := time.After(3 * time.Second)
	for {
		if err := pub.Publish(ctx, want); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		select {
		case sig := <-signals:
			if sig.Kind != store.RevokeSignalEvent {
				t.Fatalf("Kind = %v, want RevokeSignalEvent", sig.Kind)
			}
			got := sig.Event
			if len(got.UserIDs) != 1 || got.UserIDs[0] != uid {
				t.Fatalf("UserIDs = %v, want [%v]", got.UserIDs, uid)
			}
			if len(got.Tokens) != 2 || got.Tokens[0] != "tok-a" {
				t.Fatalf("Tokens = %v", got.Tokens)
			}
			if got.Reason != domain.RevokeReasonKick {
				t.Fatalf("Reason = %q", got.Reason)
			}
			return
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatal("3 秒内未收到撤销事件")
		}
	}
}

func TestRevokePublishNoSubscriberIsNotAnError(t *testing.T) {
	pub := store.NewRevokePublisher(testsupport.NewTestRedis(t))
	// 没有订阅者时发布不应报错——推送只是加速手段，不能因此拖垮撤销本身。
	if err := pub.Publish(context.Background(), domain.RevokeEvent{
		Tokens: []string{"tok"}, UserIDs: []uuid.UUID{uuid.New()}, Reason: domain.RevokeReasonLogout,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

// closeFn 必须能独立完成清理，不依赖调用方去取消那个传进来的 ctx。
//
// 一个既返回 channel 又返回 close 函数的 API，调用方传 context.Background()
// 再用 closeFn 收尾是完全合理的用法。但 Subscribe 内部那个「等 ctx.Done() 再
// sub.Close()」的看门狗 goroutine 如果直接盯着调用方的 ctx，这种用法下它就
// 永远等不到 Done，一路阻塞到进程退出。out 关不关得掉是看不出问题的——
// closeFn 里的 sub.Close() 会让 reader 那条 goroutine 正常退出，channel 照样关闭，
// 泄漏的是另一条。所以这里必须直接数 goroutine：
//
//	20 轮 subscribe→closeFn，看门狗若泄漏就是 20 条常驻 goroutine。
//
// 先做一轮热身再取基线，把 go-redis 连接池那些一次性创建的 goroutine 排除在外。
func TestSubscribeCloseFnReleasesWatchdogWithNonCancellableCtx(t *testing.T) {
	pub := store.NewRevokePublisher(testsupport.NewTestRedis(t))

	subscribeAndClose := func() {
		// 刻意用不可取消的 ctx：清理只能靠 closeFn。
		events, closeFn, err := pub.Subscribe(context.Background())
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		closeFn()
		select {
		case _, ok := <-events:
			if ok {
				t.Fatal("closeFn 之后收到了一个值，预期 channel 应该关闭且为空")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("closeFn 之后 2 秒内 channel 仍未关闭")
		}
	}

	subscribeAndClose() // 热身：让连接池等一次性 goroutine 先建好
	baseline := settledGoroutines()

	const rounds = 20
	for i := 0; i < rounds; i++ {
		subscribeAndClose()
	}

	// 留一点余量给 go-redis 自己的收尾 goroutine，但远小于 rounds：
	// 看门狗泄漏时增量必然 >= 20。
	const slack = 5
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := settledGoroutines()
		if got <= baseline+slack {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d 轮 subscribe/closeFn 之后 goroutine 数 = %d，基线 %d——"+
				"closeFn 没能叫醒等 ctx.Done() 的看门狗，每次订阅泄漏一条 goroutine",
				rounds, got, baseline)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// settledGoroutines 给已经在退出路上的 goroutine 一点时间，再报当前数量。
func settledGoroutines() int {
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	return runtime.NumGoroutine()
}

// ctx 取消必须真正关掉底层订阅，而不是只在有消息流入时才顺带生效。
//
// Subscribe 返回的 out channel 由内部 reader goroutine 在
// `for msg := range sub.ChannelWithSubscriptions()` 上驱动；这个 range 只在
// 该 channel 关闭（即 sub.Close() 被调用）时才退出。之前的实现只在 select 里放了
// 一个 <-ctx.Done() 分支去争抢一次发送，完全没有消息流入时 goroutine 根本走不到
// 那个 select——必须有一个专门等 ctx.Done() 再调用 sub.Close() 的 goroutine，
// 这才是让订阅连接真正释放的唯一途径。这里刻意不发布任何消息，只验证
// "no message traffic" 这条最容易被忽略的路径：cancel 之后 out 必须关闭。
func TestSubscribeClosesOnContextCancellation(t *testing.T) {
	pub := store.NewRevokePublisher(testsupport.NewTestRedis(t))
	ctx, cancel := context.WithCancel(context.Background())

	events, _, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	cancel()

	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("cancel 之后收到了一个值，预期 channel 应该关闭且为空")
		}
		// ok == false：channel 已关闭，符合预期。
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 2 秒内 channel 仍未关闭——订阅连接被泄漏了")
	}
}

// TestSubscribeSurfacesResubscribeAsGap 守住"丢事件必须可观测"。
//
// go-redis 会在连接抖动时静默重连并重发 SUBSCRIBE，既不报错也不关 channel。
// 没有这个信号的话，丢事件这件事在整个系统里不留任何痕迹——
// 日志里没有、监控里没有、SDK 看到的流状态也一切正常。
//
// 制造重连的方式：用 CLIENT KILL 掐掉订阅连接（拿 CLIENT LIST TYPE pubsub 找到它）。
// 断开后必须在合理时间内收到一条 Kind == RevokeSignalGap。
func TestSubscribeSurfacesResubscribeAsGap(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	pub := store.NewRevokePublisher(rdb)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 先拍一张"订阅前"的 pubsub 连接快照，subscribe 之后用差集找出新出现的
	// 那一条——而不是假设列表里只有一条。前一个测试的连接如果还没被服务端
	// 完全回收，"只取唯一一条"就可能抓错、杀错连接。
	before := pubsubClientIDs(t, rdb)

	signals, closeFn, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer closeFn()

	id := waitForNewPubsubClientID(t, rdb, before)

	// 用 ID 过滤的新式 CLIENT KILL：即使因为时序问题 0 条匹配也不报错，
	// 但这里显式检查杀掉的连接数，确保我们真的打中了目标。
	killed, err := rdb.ClientKillByFilter(ctx, "ID", id).Result()
	if err != nil {
		t.Fatalf("CLIENT KILL ID %s: %v", id, err)
	}
	if killed == 0 {
		t.Fatalf("CLIENT KILL ID %s 没有杀掉任何连接", id)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case sig := <-signals:
			if sig.Kind == store.RevokeSignalGap {
				return // 收到缺口信号，符合预期
			}
			// 理论上此时不该收到别的信号；忽略并继续等待，避免测试因为
			// 无关消息的到达顺序而误判。
		case <-deadline:
			t.Fatal("5 秒内没有收到 RevokeSignalGap——订阅重建没有被检测到，" +
				"丢事件这件事在整个系统里将不留任何痕迹")
		}
	}
}

// pubsubClientIDs 返回 rdb 当前所有 pubsub 类型连接的 client id 集合。
//
// 只看 TYPE pubsub：不加这个过滤会把会话存储用的普通连接也列进来，
// waitForNewPubsubClientID 万一算错差集，CLIENT KILL 就可能连带把它也杀了。
func pubsubClientIDs(t *testing.T, rdb *redis.Client) map[string]bool {
	t.Helper()
	out, err := rdb.Do(context.Background(), "CLIENT", "LIST", "TYPE", "pubsub").Text()
	if err != nil {
		t.Fatalf("CLIENT LIST TYPE pubsub: %v", err)
	}
	ids := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		for _, field := range strings.Fields(line) {
			if id, ok := strings.CutPrefix(field, "id="); ok {
				ids[id] = true
			}
		}
	}
	return ids
}

// waitForNewPubsubClientID 轮询直到出现一个不在 before 里的 pubsub 连接 id，
// 即本次 pub.Subscribe 刚建立的那一条。
func waitForNewPubsubClientID(t *testing.T, rdb *redis.Client, before map[string]bool) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for id := range pubsubClientIDs(t, rdb) {
			if !before[id] {
				return id
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("2 秒内没有出现新的 pubsub 连接——订阅可能没有成功建立")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
