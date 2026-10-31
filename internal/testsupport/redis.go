package testsupport

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/store"
)

// tempRedisDB 是测试用的 Redis DB。测试每次都 FLUSHDB，所以只借 FP_REDIS_URL 的
// 地址和账号，DB 号一律换成它，绝不碰变量指向的运行库。
const tempRedisDB = 1

const missingRedisHint = `
未设置 FP_REDIS_URL。fp 的测试需要真实的 Redis。

把它设到系统环境变量里再跑测试：

  ./scripts/test.sh

测试只取它的地址和账号，DB 号固定用 1，不碰它指向的运行库。
`

var (
	rdbOnce sync.Once
	rdb     *redis.Client
	rdbErr  error
)

// RedisURL 返回测试 Redis 的连接串：FP_REDIS_URL 的地址和账号 + DB 1。
func RedisURL(t *testing.T) string {
	t.Helper()

	raw := os.Getenv("FP_REDIS_URL")
	if raw == "" {
		t.Fatal(missingRedisHint)
	}
	u, err := url.Parse(raw)
	if err != nil {
		// 不带原错误：url.Parse 的错误会回显整条连接串，里面有密码。
		t.Fatal("testsupport: FP_REDIS_URL 格式不正确")
	}
	db := strconv.Itoa(tempRedisDB)
	if strings.Trim(u.Path, "/") == db {
		t.Fatalf("testsupport: FP_REDIS_URL 指向的就是 DB %s，测试会清空运行库，请换一个 DB", db)
	}
	u.Path = "/" + db
	return u.String()
}

// NewTestRedis 返回一个已清空的 Redis 客户端。
func NewTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	testURL := RedisURL(t)

	rdbOnce.Do(func() {
		rdb, rdbErr = store.OpenRedis(context.Background(), testURL)
	})
	if rdbErr != nil {
		t.Fatalf("testsupport: 连接测试 Redis 失败: %v", rdbErr)
	}

	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("testsupport: FLUSHDB 失败: %v", err)
	}
	return rdb
}
