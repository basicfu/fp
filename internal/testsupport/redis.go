package testsupport

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/store"
)

const missingRedisHint = `
未设置 FP_TEST_REDIS_URL。fp 的测试需要真实的 Redis。

用仓库根目录的脚本跑测试，它会从 .env.local 载入连接串：

  ./scripts/test.sh

注意：测试会对该 Redis DB 执行 FLUSHDB，务必使用独立的 DB index（脚本已指定 /1）。
`

var (
	rdbOnce sync.Once
	rdb     *redis.Client
	rdbErr  error
)

// NewTestRedis 返回一个已清空的 Redis 客户端。
func NewTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	url := os.Getenv("FP_TEST_REDIS_URL")
	if url == "" {
		t.Fatal(missingRedisHint)
	}

	rdbOnce.Do(func() {
		rdb, rdbErr = store.OpenRedis(context.Background(), url)
	})
	if rdbErr != nil {
		t.Fatalf("testsupport: 连接测试 Redis 失败: %v", rdbErr)
	}

	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("testsupport: FLUSHDB 失败: %v", err)
	}
	return rdb
}
