package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestEpochStartsAtZero(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	es := store.NewEpochStore(rdb)
	ctx := context.Background()

	got, err := es.Current(ctx, uuid.New())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got != 0 {
		t.Fatalf("从未撤销过的用户纪元为 %d，期望 0", got)
	}
}

func TestBumpIsMonotonic(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	es := store.NewEpochStore(rdb)
	ctx := context.Background()
	uid := uuid.New()

	for want := int64(1); want <= 3; want++ {
		got, err := es.Bump(ctx, uid)
		if err != nil {
			t.Fatalf("Bump: %v", err)
		}
		if got != want {
			t.Fatalf("第 %d 次 Bump 返回 %d", want, got)
		}
		cur, err := es.Current(ctx, uid)
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if cur != want {
			t.Fatalf("Bump 后 Current 为 %d，期望 %d", cur, want)
		}
	}
}

// TestEpochIsPerUser 守住键的作用域。
//
// 谁把键写成常量（漏掉 userID），所有用户会共用一个纪元——
// 冻结任意一个用户会把全站的会话全部作废。那种故障在单用户测试里
// 完全看不出来，只有并排比较两个用户才暴露。
func TestEpochIsPerUser(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	es := store.NewEpochStore(rdb)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()

	if _, err := es.Bump(ctx, a); err != nil {
		t.Fatalf("Bump a: %v", err)
	}
	got, err := es.Current(ctx, b)
	if err != nil {
		t.Fatalf("Current b: %v", err)
	}
	if got != 0 {
		t.Fatalf("递增用户 a 的纪元后，用户 b 的纪元变成了 %d", got)
	}
}

// TestEpochHasNoExpiry 守住"纪元键不设 TTL"。
//
// 设 TTL 会引入一个比它想解决的问题更糟的故障：键过期后，所有携带
// 非零纪元的会话都会与 0 比对失败而被登出——而这些会话本身完全合法。
// 表现是"一批用户在某个时刻集体掉线"，且无法从任何日志里看出原因。
func TestEpochHasNoExpiry(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	es := store.NewEpochStore(rdb)
	ctx := context.Background()
	uid := uuid.New()

	if _, err := es.Bump(ctx, uid); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	ttl, err := rdb.TTL(ctx, "fp:epoch:"+uid.String()).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	// go-redis 对"存在但无 TTL"的键返回 -1。
	if ttl != -1 {
		t.Fatalf("纪元键的 TTL 是 %v，期望无过期（-1）", ttl)
	}
}
