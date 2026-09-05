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

func TestConfigPublishSubscribe(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	pub := store.NewConfigPublisher(rdb)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, closeSub, err := pub.Subscribe(ctx)
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	defer closeSub()

	appID := uuid.New()
	if err := pub.Publish(ctx, appID, domain.ConfigTypeWeb, 7); err != nil {
		t.Fatalf("广播失败: %v", err)
	}

	select {
	case sig := <-ch:
		if sig.Gap {
			t.Fatal("这是一条正常事件，不该带 Gap")
		}
		if sig.AppID != appID {
			t.Fatalf("AppID = %s，期望 %s", sig.AppID, appID)
		}
		if sig.Type != domain.ConfigTypeWeb {
			t.Fatalf("Type = %q，期望 %q", sig.Type, domain.ConfigTypeWeb)
		}
		if sig.Seq != 7 {
			t.Fatalf("Seq = %d，期望 7", sig.Seq)
		}
	case <-ctx.Done():
		t.Fatal("等待事件超时")
	}
}

// 没有订阅者不算错误：推送只是把生效延迟从"下次重启"压到近乎实时的加速
// 手段，值已经落库了，那才是权威事实。与 RevokePublisher 同一逻辑。
func TestConfigPublishNoSubscriberIsNotAnError(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	pub := store.NewConfigPublisher(rdb)

	if err := pub.Publish(context.Background(), uuid.New(), domain.ConfigTypeDefault, 1); err != nil {
		t.Fatalf("无订阅者时广播不该报错: %v", err)
	}
}
