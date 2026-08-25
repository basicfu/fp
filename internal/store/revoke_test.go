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
