// Package redisx 是 fp-im 对 go-redis 的两处薄封装：按 INFO 探测模式的打开函数，
// 以及所有写命令共用的管道。
package redisx

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

type Mode string

const (
	ModeStandalone Mode = "standalone"
	ModeCluster    Mode = "cluster"
)

// Open 先按单机连上去，用 INFO cluster 判断是不是 Cluster，是就换成 ClusterClient。
// 不做配置项：同一个地址不可能既是单机又是 Cluster，让人填只会填错。
// 无论哪种模式，上层都只用 SSUBSCRIBE/SPUBLISH，单机 Redis 7 起同样支持。
func Open(ctx context.Context, url string) (redis.UniversalClient, Mode, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, "", fmt.Errorf("redisx: 解析 redis url: %w", err)
	}
	single := redis.NewClient(opt)
	info, err := single.Info(ctx, "cluster").Result()
	if err != nil {
		_ = single.Close()
		return nil, "", fmt.Errorf("redisx: INFO cluster: %w", err)
	}
	if !clusterEnabled(info) {
		return single, ModeStandalone, nil
	}
	_ = single.Close()
	cc := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:     []string{opt.Addr},
		Username:  opt.Username,
		Password:  opt.Password,
		TLSConfig: opt.TLSConfig,
	})
	if err := cc.Ping(ctx).Err(); err != nil {
		_ = cc.Close()
		return nil, "", fmt.Errorf("redisx: ping cluster: %w", err)
	}
	return cc, ModeCluster, nil
}

func clusterEnabled(info string) bool {
	for _, line := range strings.Split(info, "\n") {
		if strings.TrimSpace(line) == "cluster_enabled:1" {
			return true
		}
	}
	return false
}
