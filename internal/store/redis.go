package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/redis/go-redis/v9"
)

// OpenRedis 建立 Redis 客户端并验证连通性。
func OpenRedis(ctx context.Context, rawURL string) (*redis.Client, error) {
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		// *url.Error 的文本带着整条 URL（含密码），而最常见的成因恰恰是密码里没做百分号
		// 编码的 @ # % /；错误会进服务端日志、CLI 的 stderr 与 fp-dbclean 的输出，所以只
		// 说类别、不带原因。
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return nil, errors.New("store: 解析 redis url: 格式不正确（密码里的特殊字符要百分号编码）")
		}
		return nil, fmt.Errorf("store: 解析 redis url: %w", err)
	}
	c := redis.NewClient(opt)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("store: ping redis: %w", err)
	}
	return c, nil
}
