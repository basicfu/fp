package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// OpenRedis 建立 Redis 客户端并验证连通性。
func OpenRedis(ctx context.Context, rawURL string) (*redis.Client, error) {
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		// ParseURL 的错误文本会把 URL 片段原样带出来：*url.Error 带整条 URL，go-redis 自己的
		// 错误（invalid URL path / invalid database number / unexpected option）带从密码里
		// 的 / ? 处截出的尾巴。最常见的成因恰恰是密码里没做百分号编码的 @ # % / ?，而错误会进
		// 服务端日志、CLI 的 stderr 与 fp-dbclean 的输出，所以任何失败都只说类别、不带原因。
		// 文案同时给出整体格式：缺 scheme、写成 http:// 这类错误与密码无关，只提编码会把人带偏。
		return nil, errors.New("store: 解析 redis url: 格式不正确（应为 redis://[:密码@]主机:端口/库号；密码里的特殊字符要百分号编码）")
	}
	c := redis.NewClient(opt)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("store: ping redis: %w", err)
	}
	return c, nil
}
