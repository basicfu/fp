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

// redis.ParseURL 失败时的错误会把 URL 的片段原样带出来：密码里没做百分号编码的 # % / ? 等
// 字符正是最常见的成因。这条错误会进服务端日志与 CLI 的 stderr，不能把密码带出去。
// 前两条走 *url.Error，后三条是 go-redis 自己的解析错误（invalid URL path、invalid database
// number、unexpected option），不是 *url.Error，只擦前者挡不住它们。
// 解析在联网之前就失败，所以这个用例不碰任何库。
func TestOpenRedisParseErrorDoesNotLeakPassword(t *testing.T) {
	cases := []struct {
		name, url string
		secrets   []string // 错误文本里不许出现的密码片段
	}{
		{"密码里有没编码的 #", "redis://:Pw#Zq9@127.0.0.1:6379/0", []string{"Pw", "Zq9"}},
		{"密码里有坏的百分号转义", "redis://:Pw%zzZq9@127.0.0.1:6379/0", []string{"Pw", "Zq9"}},
		{"密码以 / 开头（invalid URL path）", "redis://:/SeCrEt@127.0.0.1:6379/0", []string{"SeCrEt"}},
		{"密码以 / 开头且没有库号（invalid database number）", "redis://:/SeCrEt@127.0.0.1:6379", []string{"SeCrEt"}},
		{"密码里有 ?（unexpected option）", "redis://:12?SeCrEt@127.0.0.1:6379/0", []string{"SeCrEt"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := store.OpenRedis(context.Background(), c.url)
			if err == nil {
				t.Fatal("坏的连接串应当报错")
			}
			for _, secret := range c.secrets {
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
