package store_test

import (
	"context"
	"errors"
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

func TestSessionStoreExpireExtends(t *testing.T) {
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if err := st.Put(ctx, sampleSession("tok-1", uuid.New()), 300*time.Millisecond); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := st.Expire(ctx, "tok-1", 5*time.Second); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := st.Get(ctx, "tok-1"); err != nil {
		t.Fatalf("延长 TTL 后仍应存在: %v", err)
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
	st := store.NewSessionStore(testsupport.NewTestRedis(t))
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

	// 再查一次，确认悬挂项已被真正移除而不是每次都过滤。
	tokens, err = st.ListUserTokens(ctx, uid)
	if err != nil {
		t.Fatalf("二次 ListUserTokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("tokens = %v", tokens)
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
