package bus

import (
	"context"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestPublishReachesOnlyTargetNode(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	run, _ := redisx.NewRunner(rdb, 0, 1)
	defer run.Close()
	b := New(rdb, run)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chA, closeA, err := b.Subscribe(ctx, "im-a")
	if err != nil {
		t.Fatal(err)
	}
	defer closeA()
	chB, closeB, err := b.Subscribe(ctx, "im-b")
	if err != nil {
		t.Fatal(err)
	}
	defer closeB()

	env := Envelope{Type: TypeMsg, App: "a1", Subject: "u:1", Payload: []byte(`1`)}
	n, err := b.Publish(ctx, "im-a", env)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("im-a 恰有一个订阅者，SPUBLISH 应返回 1，实际 %d", n)
	}
	select {
	case sig := <-chA:
		if sig.Kind != SignalEnvelope || sig.Env.Subject != "u:1" {
			t.Fatalf("im-a 收到的不是发出的信封：%+v", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("im-a 5 秒内没收到")
	}
	select {
	case sig := <-chB:
		t.Fatalf("im-b 不该收到发给 im-a 的消息：%+v", sig)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestPublishReturnsSubscriberCount 覆盖 Publish 返回值的两种情形：
// 无人订阅的频道返回 0（hub.Deliver 靠这个判断目标节点已崩溃），
// 有订阅者的频道返回实际订阅者数。
func TestPublishReturnsSubscriberCount(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	run, _ := redisx.NewRunner(rdb, 0, 1)
	defer run.Close()
	b := New(rdb, run)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env := Envelope{Type: TypeMsg, App: "a1", Subject: "u:1", Payload: []byte(`1`)}
	if n, err := b.Publish(ctx, "im-nobody", env); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("无人订阅的频道应返回 0，实际 %d", n)
	}

	ch, closeFn, err := b.Subscribe(ctx, "im-somebody")
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if n, err := b.Publish(ctx, "im-somebody", env); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("有一个订阅者的频道应返回 1，实际 %d", n)
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("订阅者 5 秒内没收到消息")
	}
}

// TestSubscribeSignalsGapOnReconnect 用 CLIENT KILL 制造一次 go-redis 静默重连，
// 验证 Subscribe 能把它翻译成 SignalGap。
//
// 这条测试的必要性来自一次实测（完整过程见 task-6-report.md）：
// sub.Receive(ctx) 会阻塞到订阅确认返回并把这条确认从流里读走，之后
// ChannelWithSubscriptions() 上出现的第一条 *redis.Subscription 就已经是
// 重连产生的重订阅确认，不是"初次订阅"的重复。简报草稿里"跳过第一条"的
// first 标志会把这第一次真实重连误判成初次订阅而吞掉，导致 Redis 抖动后
// 第一次断线不会触发 SignalGap——上层不会重新登记连接，连接静默失联。
// 这里不设 first 特判，直接断言重连后能收到 SignalGap。
func TestSubscribeSignalsGapOnReconnect(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	run, _ := redisx.NewRunner(rdb, 0, 1)
	defer run.Close()
	b := New(rdb, run)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, closeFn, err := b.Subscribe(ctx, "im-gap")
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	if err := rdb.Do(ctx, "CLIENT", "KILL", "TYPE", "pubsub").Err(); err != nil {
		t.Fatalf("CLIENT KILL: %v", err)
	}

	select {
	case sig := <-ch:
		if sig.Kind != SignalGap {
			t.Fatalf("期望重连后收到 SignalGap，实际 %+v", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("5 秒内没收到 SignalGap：CLIENT KILL 之后的重连信号被吞掉了")
	}

	// 重连之后订阅要仍然有效，不能只是"报了一次 Gap 就死了"。
	env := Envelope{Type: TypeMsg, App: "a1", Subject: "u:1", Payload: []byte(`1`)}
	if _, err := b.Publish(ctx, "im-gap", env); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-ch:
		if sig.Kind != SignalEnvelope || sig.Env.Subject != "u:1" {
			t.Fatalf("重连后收到的不是发出的信封：%+v", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("重连后 5 秒内没收到消息")
	}
}
