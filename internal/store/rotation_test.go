package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestRotatedToReturnsEmptyWhenAbsent(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))

	got, err := st.RotatedTo(context.Background(), "没有记录的token")
	if err != nil {
		t.Fatalf("无记录时返回了错误: %v", err)
	}
	if got != "" {
		t.Fatalf("无记录时返回 %q，期望空串", got)
	}
}

func TestPutRotationRoundTrips(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if err := st.PutRotation(ctx, "old", "new", time.Minute); err != nil {
		t.Fatalf("PutRotation: %v", err)
	}
	got, err := st.RotatedTo(ctx, "old")
	if err != nil {
		t.Fatalf("RotatedTo: %v", err)
	}
	if got != "new" {
		t.Fatalf("RotatedTo 返回 %q，期望 \"new\"", got)
	}
}

// TestPutRotationRejectsNonPositiveTTL 守住一个 go-redis 的陷阱。
//
// SET 的 TTL 参数为 0 或负数时，go-redis 直接不发 EX/PX——写进去的是一个
// **永不过期**的键。轮换映射一旦不朽，一个早已死透的旧 token 会在往后的
// 任意时刻被"告知"成一个同样早已不存在的新 token，客户端照着切过去后登出。
//
// 第一阶段的 Put 已经有同样的防线，这里是同一个陷阱的第二个入口。
func TestPutRotationRejectsNonPositiveTTL(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()

	for _, ttl := range []time.Duration{0, -time.Second} {
		if err := st.PutRotation(ctx, "old", "new", ttl); err == nil {
			t.Fatalf("ttl=%v 时 PutRotation 没有报错", ttl)
		}
		got, err := st.RotatedTo(ctx, "old")
		if err != nil {
			t.Fatalf("RotatedTo: %v", err)
		}
		if got != "" {
			t.Fatalf("ttl=%v 被拒后仍写进了 %q", ttl, got)
		}
	}
}
