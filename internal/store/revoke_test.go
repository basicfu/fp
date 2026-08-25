package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestRevokePublishSubscribe(t *testing.T) {
	pub := store.NewRevokePublisher(testsupport.NewTestRedis(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, closeFn, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer closeFn()

	uid := uuid.New()
	want := domain.RevokeEvent{
		Tokens: []string{"tok-a", "tok-b"},
		UserID: uid,
		Reason: domain.RevokeReasonKick,
		At:     time.Now().UnixMilli(),
	}

	// 订阅建立需要一个往返，重试几次直到收到。
	deadline := time.After(3 * time.Second)
	for {
		if err := pub.Publish(ctx, want); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		select {
		case got := <-events:
			if got.UserID != uid {
				t.Fatalf("UserID = %v, want %v", got.UserID, uid)
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
		Tokens: []string{"tok"}, UserID: uuid.New(), Reason: domain.RevokeReasonLogout,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

// ctx 取消必须真正关掉底层订阅，而不是只在有消息流入时才顺带生效。
//
// Subscribe 返回的 out channel 由内部 reader goroutine 在
// `for msg := range sub.Channel()` 上驱动；这个 range 只在 sub.Channel()
// 关闭（即 sub.Close() 被调用）时才退出。之前的实现只在 select 里放了一个
// <-ctx.Done() 分支去争抢一次发送，完全没有消息流入时 goroutine 根本走不到
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
