package grpcapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
)

// 一条信号只送给它自己那个应用的订阅者。
// 【辨别力】必须有第二个应用的订阅者在场并断言它**没收到**，
// 否则一个"广播给所有人"的实现照样会绿。
func TestConfigHubRoutesByApp(t *testing.T) {
	h, signals := newTestConfigHub(t)

	appA, appB := uuid.New(), uuid.New()
	chA, stopA := h.Subscribe(appA)
	defer stopA()
	chB, stopB := h.Subscribe(appB)
	defer stopB()

	signals <- store.ConfigSignal{AppID: appA, Type: domain.ConfigTypeWeb, Seq: 3}

	select {
	case ev := <-chA:
		if ev.Type != domain.ConfigTypeWeb || ev.Seq != 3 {
			t.Fatalf("A 收到 %+v，期望 {WEB 3}", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("A 没收到事件")
	}

	select {
	case ev := <-chB:
		t.Fatalf("B 不该收到任何事件，却收到了 %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// Gap 广播给全部订阅者，且 Type 为空串——语义是"分区未知，重拉全部"。
func TestConfigHubFansOutGapToEveryone(t *testing.T) {
	h, signals := newTestConfigHub(t)

	chA, stopA := h.Subscribe(uuid.New())
	defer stopA()
	chB, stopB := h.Subscribe(uuid.New())
	defer stopB()

	signals <- store.ConfigSignal{Gap: true}

	for name, ch := range map[string]<-chan ConfigEvent{"A": chA, "B": chB} {
		select {
		case ev := <-ch:
			if ev.Type != "" {
				t.Fatalf("%s 收到的 Type = %q，Gap 事件必须是空串（重拉全部）", name, ev.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s 没收到 Gap 事件", name)
		}
	}
}

// 缓冲满时摘掉订阅者：channel 被关闭，Watch handler 因此正常结束，
// SDK 重连后走 ready 重拉，不会漏配置。
func TestConfigHubDropsSaturatedSubscriber(t *testing.T) {
	h, signals := newTestConfigHub(t)
	appID := uuid.New()
	ch, stop := h.Subscribe(appID)
	defer stop()

	// 灌满缓冲再多推几条，且**不消费**。
	for i := 0; i < configBufferSize+5; i++ {
		signals <- store.ConfigSignal{AppID: appID, Type: domain.ConfigTypeDefault, Seq: int64(i)}
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel 已关闭，正是期望
			}
		case <-deadline:
			t.Fatal("缓冲满之后订阅者仍未被摘掉")
		}
	}
}

// newTestConfigHub 返回一个只吃假信号的 hub，完全绕开 Redis。
// 与 watch.go 的 newTestHubWithFakeSignals 同一手法：测的是分发逻辑本身，
// 不是 go-redis 的重连行为——后者既慢又不稳定。
func newTestConfigHub(t *testing.T) (*ConfigHub, chan store.ConfigSignal) {
	t.Helper()
	h := newConfigHub()
	signals := make(chan store.ConfigSignal, 64)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); h.Close() })
	go h.run(ctx, signals)
	return h, signals
}
