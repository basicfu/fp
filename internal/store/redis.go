package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// OpenRedis 建立 Redis 客户端并验证连通性。
func OpenRedis(ctx context.Context, url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("store: 解析 redis url: %w", err)
	}
	c := redis.NewClient(opt)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("store: ping redis: %w", err)
	}
	return c, nil
}
