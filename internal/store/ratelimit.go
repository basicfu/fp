package store

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// allowScript 是固定窗口计数器。
// 第一次计数时设置窗口 TTL，之后只累加，因此窗口不会被后续请求延长。
// 返回 {当前计数, 剩余毫秒}。
var allowScript = redis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if c == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return {c, redis.call('PTTL', KEYS[1])}
`)

// RateLimiter 是基于 Redis 的固定窗口频率限制。
type RateLimiter struct {
	rdb *redis.Client
}

// NewRateLimiter 构造 RateLimiter。
func NewRateLimiter(rdb *redis.Client) *RateLimiter {
	return &RateLimiter{rdb: rdb}
}

// Allow 在 window 内最多放行 limit 次。
// 被拒绝时 retryAfter 是当前窗口的剩余时间。
func (rl *RateLimiter) Allow(ctx context.Context, key string, window time.Duration, limit int) (bool, time.Duration, error) {
	res, err := allowScript.Run(ctx, rl.rdb, []string{"fp:rl:" + key}, window.Milliseconds()).Slice()
	if err != nil {
		return false, 0, fmt.Errorf("store: 执行限流脚本: %w", err)
	}
	if len(res) != 2 {
		return false, 0, fmt.Errorf("store: 限流脚本返回 %d 个值, want 2", len(res))
	}
	count, _ := res[0].(int64)
	pttl, _ := res[1].(int64)

	if count > int64(limit) {
		retryAfter := time.Duration(pttl) * time.Millisecond
		if retryAfter < 0 {
			retryAfter = 0
		}
		return false, retryAfter, nil
	}
	return true, 0, nil
}
