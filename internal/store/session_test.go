package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

func sampleSession(token string, userID uuid.UUID) *domain.Session {
	now := time.Now().UnixMilli()
	return &domain.Session{
		ID:             uuid.NewString(),
		Token:          token,
		UserID:         userID,
		AppID:          uuid.New(),
		FirstAuthAt:    now,
		IssuedAt:       now,
		LastExtendedAt: now,
		IdleExpiresAt:  now + 60_000,
		IP:             "127.0.0.1",
		UA:             "go-test",
	}
}

func TestSessionStorePutGet(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()
	uid := uuid.New()
	want := sampleSession("tok-1", uid)

	if err := st.Put(ctx, want, time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := st.Get(ctx, "tok-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != want.ID || got.UserID != want.UserID || got.AppID != want.AppID {
		t.Fatalf("got = %+v, want = %+v", got, want)
	}
	if got.IdleExpiresAt != want.IdleExpiresAt || got.FirstAuthAt != want.FirstAuthAt {
		t.Fatalf("时间字段未正确往返: %+v", got)
	}
}

func TestSessionStoreGetMissing(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	if _, err := st.Get(context.Background(), "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSessionStoreDelete(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if err := st.Put(ctx, sampleSession("tok-1", uuid.New()), time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := st.Delete(ctx, "tok-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(ctx, "tok-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// 删除不存在的 token 不应报错
	if err := st.Delete(ctx, "tok-1"); err != nil {
		t.Fatalf("重复 Delete: %v", err)
	}
}

// 分批逻辑必须跨过真实的批次边界。
//
// 现有用例都只有 1-3 个 token，永远走单批路径——chunkStrings 里
// 一个 off-by-one 会静默丢 token 或重复 token，而这类错误只在
// 规模上来之后才显形，是最不该留白的地方。
//
// 这里越过 redisBatchSize 建 1200 个会话（跨 3 批），验证读、清理、
// 批量删除三条路径在多批下都完整。
func TestSessionStoreBatchesAcrossChunkBoundary(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	st := store.NewSessionStore(rdb)
	ctx := context.Background()
	uid := uuid.New()

	const total = 1200 // > 2 * redisBatchSize
	for i := 0; i < total; i++ {
		tok := fmt.Sprintf("tok-%04d", i)
		if err := st.Put(ctx, sampleSession(tok, uid), time.Minute); err != nil {
			t.Fatalf("Put %s: %v", tok, err)
		}
	}

	alive, err := st.ListUserTokens(ctx, uid)
	if err != nil {
		t.Fatalf("ListUserTokens: %v", err)
	}
	if len(alive) != total {
		t.Fatalf("存活 token 数 = %d, want %d——分批读取丢了数据", len(alive), total)
	}
	// 不能有重复
	seen := make(map[string]bool, len(alive))
	for _, tok := range alive {
		if seen[tok] {
			t.Fatalf("token %s 出现两次——分批切片有重叠", tok)
		}
		seen[tok] = true
	}

	// 制造跨批的悬挂项：删掉一半会话的主键，索引仍留着
	for i := 0; i < total; i += 2 {
		if err := rdb.Del(ctx, "fp:sess:"+fmt.Sprintf("tok-%04d", i)).Err(); err != nil {
			t.Fatalf("Del: %v", err)
		}
	}
	alive, err = st.ListUserTokens(ctx, uid)
	if err != nil {
		t.Fatalf("二次 ListUserTokens: %v", err)
	}
	if len(alive) != total/2 {
		t.Fatalf("存活 token 数 = %d, want %d", len(alive), total/2)
	}
	if card, err := rdb.SCard(ctx, "fp:usess:"+uid.String()).Result(); err != nil {
		t.Fatalf("SCard: %v", err)
	} else if card != int64(total/2) {
		t.Fatalf("索引基数 = %d, want %d——跨批清理不完整", card, total/2)
	}

	n, err := st.DeleteUserTokens(ctx, uid)
	if err != nil {
		t.Fatalf("DeleteUserTokens: %v", err)
	}
	if n != total/2 {
		t.Fatalf("删除数 = %d, want %d", n, total/2)
	}
	if card, err := rdb.SCard(ctx, "fp:usess:"+uid.String()).Result(); err != nil {
		t.Fatalf("SCard: %v", err)
	} else if card != 0 {
		t.Fatalf("删除后索引基数 = %d, want 0", card)
	}
}

// ttl <= 0 必须报错，绝不能写进 Redis。
//
// go-redis 对 expiration <= 0 会省略 TTL 参数，SET 出来是永不过期的键；
// 而 ListUserTokens 只清理"已消失"的键，会把它当活跃会话永远列下去。
// Task 10 的延期逻辑传的是"剩余有效期"，会话恰好过期时正是 0。
func TestSessionStorePutRejectsNonPositiveTTL(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	st := store.NewSessionStore(rdb)
	ctx := context.Background()

	for _, ttl := range []time.Duration{0, -time.Second} {
		err := st.Put(ctx, sampleSession("tok-1", uuid.New()), ttl)
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("ttl=%v err = %v, want ErrInvalidArgument", ttl, err)
		}
	}
	// 确认真的什么都没写进去
	if n, err := rdb.Exists(ctx, "fp:sess:tok-1").Result(); err != nil {
		t.Fatalf("Exists: %v", err)
	} else if n != 0 {
		t.Fatal("拒绝后仍然写入了会话键")
	}
}

func TestSessionStoreTTLExpires(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if err := st.Put(ctx, sampleSession("tok-1", uuid.New()), 300*time.Millisecond); err != nil {
		t.Fatalf("Put: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := st.Get(ctx, "tok-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("TTL 到期后 err = %v, want ErrNotFound", err)
	}
}

// 用户 token 索引用于在线设备列表与批量撤销。
func TestSessionStoreListUserTokens(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()
	uid := uuid.New()

	for _, tok := range []string{"a", "b", "c"} {
		if err := st.Put(ctx, sampleSession(tok, uid), time.Minute); err != nil {
			t.Fatalf("Put %s: %v", tok, err)
		}
	}
	tokens, err := st.ListUserTokens(ctx, uid)
	if err != nil {
		t.Fatalf("ListUserTokens: %v", err)
	}
	if len(tokens) != 3 {
		t.Fatalf("len = %d, want 3", len(tokens))
	}

	// 另一个用户互不干扰
	other, err := st.ListUserTokens(ctx, uuid.New())
	if err != nil {
		t.Fatalf("ListUserTokens(other): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("其他用户 len = %d, want 0", len(other))
	}
}

// 索引是超集：token 到期后 Redis 自动清掉主键，但索引里还留着。
// ListUserTokens 必须顺手清理这些悬挂项，否则在线设备列表会越积越多。
func TestSessionStoreListPrunesDanglingTokens(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	st := store.NewSessionStore(rdb)
	ctx := context.Background()
	uid := uuid.New()

	if err := st.Put(ctx, sampleSession("live", uid), time.Minute); err != nil {
		t.Fatalf("Put live: %v", err)
	}
	if err := st.Put(ctx, sampleSession("dying", uid), 200*time.Millisecond); err != nil {
		t.Fatalf("Put dying: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	tokens, err := st.ListUserTokens(ctx, uid)
	if err != nil {
		t.Fatalf("ListUserTokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0] != "live" {
		t.Fatalf("tokens = %v, want [live]", tokens)
	}

	// 直接查 Redis 集合的基数，而不是再调一次 ListUserTokens。
	//
	// 只看返回值是证明不了"清理"的：一个只过滤、从不 SREM 的实现，
	// 两次调用都会返回 [live]，断言全绿而索引一直在涨。必须越过被测函数
	// 去看它对存储的实际副作用。
	if card, err := rdb.SCard(ctx, "fp:usess:"+uid.String()).Result(); err != nil {
		t.Fatalf("SCard: %v", err)
	} else if card != 1 {
		t.Fatalf("索引基数 = %d, want 1——悬挂项只是被过滤掉了，没有真正 SREM", card)
	}
}

func TestSessionStoreDeleteUserTokens(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()
	uid := uuid.New()

	for _, tok := range []string{"a", "b"} {
		if err := st.Put(ctx, sampleSession(tok, uid), time.Minute); err != nil {
			t.Fatalf("Put %s: %v", tok, err)
		}
	}
	n, err := st.DeleteUserTokens(ctx, uid)
	if err != nil {
		t.Fatalf("DeleteUserTokens: %v", err)
	}
	if n != 2 {
		t.Fatalf("删除数 = %d, want 2", n)
	}
	for _, tok := range []string{"a", "b"} {
		if _, err := st.Get(ctx, tok); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s 仍存在", tok)
		}
	}
	tokens, err := st.ListUserTokens(ctx, uid)
	if err != nil {
		t.Fatalf("ListUserTokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("索引未清空: %v", tokens)
	}
}

func TestSessionStoreTryLock(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()

	ok, err := st.TryLock(ctx, "k", time.Second)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !ok {
		t.Fatal("首次加锁应成功")
	}
	ok, err = st.TryLock(ctx, "k", time.Second)
	if err != nil {
		t.Fatalf("二次 TryLock: %v", err)
	}
	if ok {
		t.Fatal("锁未释放时不应重复获得")
	}
}
