package redisx

import (
	"context"
	"os"
	"strings"
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

// redis.ParseURL 失败时的错误会把 URL 片段（含没做百分号编码的密码）原样带出来，而这条错误会进
// fp-im 的日志。两个输入在解析阶段就失败，不会拨号，所以这个用例不碰 Redis。
// 前者是 go-redis 自己的解析错误（invalid URL path），后者是 *url.Error（带整条 URL）。
func TestOpenParseErrorDoesNotLeakPassword(t *testing.T) {
	cases := []struct {
		name, url string
		secrets   []string // 错误文本里不许出现的密码片段
	}{
		{"密码以 / 开头", "redis://:/SeCrEt@127.0.0.1:6379/0", []string{"SeCrEt"}},
		{"密码里有坏的百分号转义", "redis://:Pw%zzZq9@127.0.0.1:6379/0", []string{"Pw", "Zq9", "%zz"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Open(context.Background(), c.url)
			if err == nil {
				t.Fatal("坏的连接串应当报错")
			}
			for _, secret := range c.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("错误里带出了密码片段 %q: %v", secret, err)
				}
			}
			if !strings.Contains(err.Error(), "redis://") {
				t.Errorf("错误应给出整体格式 redis://...: %v", err)
			}
		})
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
