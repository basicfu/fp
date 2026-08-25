package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestRateLimiterAllowsUpToLimit(t *testing.T) {
	rl := store.NewRateLimiter(testsupport.NewTestRedis(t))
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		allowed, _, err := rl.Allow(ctx, "k", time.Minute, 3)
		if err != nil {
			t.Fatalf("第 %d 次 Allow: %v", i, err)
		}
		if !allowed {
			t.Fatalf("第 %d 次应当放行", i)
		}
	}

	allowed, retryAfter, err := rl.Allow(ctx, "k", time.Minute, 3)
	if err != nil {
		t.Fatalf("第 4 次 Allow: %v", err)
	}
	if allowed {
		t.Fatal("第 4 次应当拒绝")
	}
	if retryAfter <= 0 || retryAfter > time.Minute {
		t.Fatalf("retryAfter = %v, 应在 (0, 1m] 内", retryAfter)
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	rl := store.NewRateLimiter(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if allowed, _, _ := rl.Allow(ctx, "a", time.Minute, 1); !allowed {
		t.Fatal("a 首次应放行")
	}
	if allowed, _, _ := rl.Allow(ctx, "b", time.Minute, 1); !allowed {
		t.Fatal("b 首次应放行")
	}
	if allowed, _, _ := rl.Allow(ctx, "a", time.Minute, 1); allowed {
		t.Fatal("a 第二次应拒绝")
	}
}

func TestRateLimiterWindowExpires(t *testing.T) {
	rl := store.NewRateLimiter(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if allowed, _, _ := rl.Allow(ctx, "k", 300*time.Millisecond, 1); !allowed {
		t.Fatal("首次应放行")
	}
	if allowed, _, _ := rl.Allow(ctx, "k", 300*time.Millisecond, 1); allowed {
		t.Fatal("窗口内第二次应拒绝")
	}

	time.Sleep(400 * time.Millisecond)

	if allowed, _, _ := rl.Allow(ctx, "k", 300*time.Millisecond, 1); !allowed {
		t.Fatal("窗口过期后应重新放行")
	}
}
