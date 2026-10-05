package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestRedisRoundTrip(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	ctx := context.Background()

	if err := rdb.Set(ctx, "k", "v", 0).Err(); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := rdb.Get(ctx, "k").Result()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "v" {
		t.Fatalf("Get = %q, want v", got)
	}
}

func TestRedisFlushedBetweenTests(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	ctx := context.Background()

	n, err := rdb.Exists(ctx, "k").Result()
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if n != 0 {
		t.Fatal("上一个测试的 key 未被清理，NewTestRedis 应当 FLUSHDB")
	}
}

// redis.ParseURL 失败时返回的 *url.Error 带着整条 URL；密码里没做百分号编码的 # % 等字符
// 正是最常见的成因。这条错误会进服务端日志与 CLI 的 stderr，不能把密码带出去。
// 解析在联网之前就失败，所以这个用例不碰任何库。
func TestOpenRedisParseErrorDoesNotLeakPassword(t *testing.T) {
	cases := []struct{ name, url string }{
		{"密码里有没编码的 #", "redis://:Pw#Zq9@127.0.0.1:6379/0"},
		{"密码里有坏的百分号转义", "redis://:Pw%zzZq9@127.0.0.1:6379/0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := store.OpenRedis(context.Background(), c.url)
			if err == nil {
				t.Fatal("坏的连接串应当报错")
			}
			for _, secret := range []string{"Pw", "Zq9"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("错误里带出了密码片段 %q: %v", secret, err)
				}
			}
			if !strings.Contains(err.Error(), "百分号编码") {
				t.Errorf("错误应提示密码里的特殊字符要百分号编码: %v", err)
			}
		})
	}
}
