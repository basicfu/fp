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

// 固定窗口：TTL 只在第一次计数时设置，后续请求不得延长它。
//
// 时间点刻意错开，否则测不出区别：如果三次调用都挤在 t≈0 附近，
// 那么"正确的固定窗口"和"被错误延长的滑动窗口"到期时刻几乎重合，
// 删掉 allowScript 里的 `if c == 1` 守卫测试照样通过——那就不是测试了。
//
//	窗口 600ms，limit 1
//	t=0     放行，窗口到 600
//	t=500   拒绝。若实现错成滑动窗口，此刻会把窗口续到 1100
//	t=700   固定窗口应放行；滑动窗口仍在封锁期内 → 能区分两者
func TestRateLimiterWindowIsFixedNotSliding(t *testing.T) {
	rl := store.NewRateLimiter(testsupport.NewTestRedis(t))
	ctx := context.Background()
	const window = 600 * time.Millisecond

	if allowed, _, _ := rl.Allow(ctx, "k", window, 1); !allowed {
		t.Fatal("t=0 首次应放行")
	}

	time.Sleep(500 * time.Millisecond)
	if allowed, _, _ := rl.Allow(ctx, "k", window, 1); allowed {
		t.Fatal("t=500 窗口内应拒绝")
	}

	time.Sleep(200 * time.Millisecond) // t=700，已过原窗口
	if allowed, _, _ := rl.Allow(ctx, "k", window, 1); !allowed {
		t.Fatal("t=700 原窗口已到期应放行；仍被拒说明 TTL 被第二次请求延长了，" +
			"固定窗口退化成了滑动窗口——持续打压下用户会被无限锁死")
	}
}
