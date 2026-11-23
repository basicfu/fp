package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestFillEmptyAvatarStoresWhenEmpty(t *testing.T) {
	e := newOSSEnv(t)
	u := e.newUser(t, "13800138300", "")

	filled, err := e.users.FillEmptyAvatar(context.Background(), u.ID, testPNG)
	if err != nil || !filled {
		t.Fatalf("filled=%v err=%v", filled, err)
	}
	key := e.objectKey(t, u.ID)
	if !avatarKeyRe.MatchString(key) {
		t.Fatalf("key = %q", key)
	}
	if obj, ok := e.store.Get(key); !ok || obj.CacheControl == "" {
		t.Fatalf("对象 = %+v, %v", obj, ok)
	}
	if hash, _, _, _, _ := e.storedAvatar(t, u.ID); hash != domain.AvatarHash(testPNG) {
		t.Fatalf("avatar_hash = %q", hash)
	}
	if keys := e.gcKeys(t); len(keys) != 0 {
		t.Fatalf("入库成功不该留在 object_gc：%v", keys)
	}
}

// 模拟「读到没头像 → 下载期间用户自己上传了」：FillEmptyAvatar 不预读，能挡住覆盖的只有写入时的条件。
func TestFillEmptyAvatarLosesToExistingAvatarAndDiscardsObject(t *testing.T) {
	e := newOSSEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138301", "")
	if _, err := e.users.UpdateAvatar(ctx, u.ID, testJPEG); err != nil {
		t.Fatal(err)
	}
	ownKey := e.objectKey(t, u.ID)

	filled, err := e.users.FillEmptyAvatar(ctx, u.ID, testPNG)
	if err != nil || filled {
		t.Fatalf("已有头像不该补：filled=%v err=%v", filled, err)
	}
	if hash, _, _, _, _ := e.storedAvatar(t, u.ID); hash != domain.AvatarHash(testJPEG) || e.objectKey(t, u.ID) != ownKey {
		t.Fatalf("已有头像被覆盖：hash=%q", hash)
	}
	var discarded string
	if err := e.pool.QueryRow(ctx, `SELECT key FROM object_gc WHERE due_at <= now()`).Scan(&discarded); err != nil {
		t.Fatalf("丢弃的对象应立即到期待删：%v（object_gc=%v）", err, e.gcKeys(t))
	}
	if _, ok := e.store.Get(discarded); !ok || discarded == ownKey {
		t.Fatalf("丢弃的 key = %q", discarded)
	}
	if n, err := e.gc.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce = %d, %v", n, err)
	}
	if _, ok := e.store.Get(discarded); ok {
		t.Fatal("丢弃的对象应被后台删掉")
	}
	if _, ok := e.store.Get(ownKey); !ok {
		t.Fatal("用户自己的头像不该被删")
	}
}

func TestFillEmptyAvatarPutFailureQueuesKey(t *testing.T) {
	e := newOSSEnv(t)
	u := e.newUser(t, "13800138302", "")
	e.store.FailPut = errors.New("oss down")
	if filled, err := e.users.FillEmptyAvatar(context.Background(), u.ID, testPNG); err == nil || filled {
		t.Fatalf("filled=%v err=%v", filled, err)
	}
	if hash, _, _, _, _ := e.storedAvatar(t, u.ID); hash != "" {
		t.Fatalf("avatar_hash = %q", hash)
	}
	if keys := e.gcKeys(t); len(keys) != 1 {
		t.Fatalf("上传失败的 key 也要进 object_gc：%v", keys)
	}
}

func TestFillEmptyAvatarWithoutStoreSkips(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	e := &profileEnv{users: service.NewUserService(pool), pool: pool}
	u := e.newUser(t, "13800138303", "")
	if filled, err := e.users.FillEmptyAvatar(context.Background(), u.ID, testPNG); err != nil || filled {
		t.Fatalf("没配对象存储应跳过：filled=%v err=%v", filled, err)
	}
}

func TestFillEmptyAvatarRejectsInvalidImage(t *testing.T) {
	e := newOSSEnv(t)
	u := e.newUser(t, "13800138304", "")
	if _, err := e.users.FillEmptyAvatar(context.Background(), u.ID, []byte("<html>")); err == nil {
		t.Fatal("非图片应报错")
	}
	if keys := e.store.Keys(); len(keys) != 0 {
		t.Fatalf("不该上传：%v", keys)
	}
}
