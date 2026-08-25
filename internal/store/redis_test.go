package store_test

import (
	"context"
	"testing"

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
