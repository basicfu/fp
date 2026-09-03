package redisx

import (
	"context"
	"os"
	"testing"

	"github.com/basicfu/fp/internal/testsupport"
)

func TestOpenDetectsStandalone(t *testing.T) {
	testsupport.NewTestRedis(t) // 只为了触发"未设置 FP_TEST_REDIS_URL 就 Fatal"的统一指引
	c, mode, err := Open(context.Background(), os.Getenv("FP_TEST_REDIS_URL"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()
	if mode != ModeStandalone {
		t.Fatalf("测试环境是单机 Redis，探测结果却是 %s", mode)
	}
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("返回的客户端不可用：%v", err)
	}
}

func TestParseClusterEnabled(t *testing.T) {
	if !clusterEnabled("# Cluster\r\ncluster_enabled:1\r\n") {
		t.Fatal("cluster_enabled:1 应判为 Cluster")
	}
	if clusterEnabled("# Cluster\r\ncluster_enabled:0\r\n") || clusterEnabled("") {
		t.Fatal("cluster_enabled:0 或空串应判为单机")
	}
}
