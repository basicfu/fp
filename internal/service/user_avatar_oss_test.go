package service_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/objstore"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

type ossEnv struct {
	*profileEnv
	store *objstore.Memory
	gc    *service.ObjectGC
}

func newOSSEnv(t *testing.T) *ossEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	st := objstore.NewMemory()
	return &ossEnv{
		profileEnv: &profileEnv{users: service.NewUserService(pool, service.WithObjectStore(st)), pool: pool},
		store:      st,
		gc:         service.NewObjectGC(pool, st),
	}
}

func (e *ossEnv) objectKey(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var k string
	if err := e.pool.QueryRow(context.Background(), `SELECT object_key FROM user_avatar WHERE user_id = $1`, id).Scan(&k); err != nil {
		t.Fatalf("读 object_key: %v", err)
	}
	return k
}

func (e *ossEnv) gcKeys(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT key FROM object_gc ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		out = append(out, k)
	}
	return out
}

var avatarKeyRe = regexp.MustCompile(`^avatar/[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}\.(png|jpg|webp)$`)

func TestUpdateAvatarUploadsToObjectStore(t *testing.T) {
	e := newOSSEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "小明")

	p, err := e.users.UpdateAvatar(ctx, u.ID, testPNG)
	if err != nil {
		t.Fatalf("UpdateAvatar: %v", err)
	}
	key := e.objectKey(t, u.ID)
	if !avatarKeyRe.MatchString(key) || key[len(key)-3:] != "png" {
		t.Fatalf("key = %q, 应为 avatar/{uuidv7}.png", key)
	}
	if want := "https://memory.test/" + key; p.AvatarURL != want {
		t.Fatalf("AvatarURL = %q, want %q", p.AvatarURL, want)
	}
	obj, ok := e.store.Get(key)
	if !ok || string(obj.Body) != string(testPNG) || obj.ContentType != "image/png" ||
		obj.CacheControl != "public, max-age=31536000, immutable" {
		t.Fatalf("对象 = %+v, %v", obj, ok)
	}
	// hash 仍写入（老路径兼容），data 被清空。
	userHash, rows, rowHash, _, data := e.storedAvatar(t, u.ID)
	if userHash != domain.AvatarHash(testPNG) || rows != 1 || rowHash != userHash || len(data) != 0 {
		t.Fatalf("落库: app_user=%q rows=%d hash=%q data=%d 字节", userHash, rows, rowHash, len(data))
	}
	// 读回资料时同样带 URL。
	got, err := e.users.GetProfile(ctx, u.ID)
	if err != nil || got.AvatarURL != p.AvatarURL {
		t.Fatalf("GetProfile.AvatarURL = %q, %v", got.AvatarURL, err)
	}
}

func TestUpdateAvatarExtensionByContentType(t *testing.T) {
	e := newOSSEnv(t)
	u := e.newUser(t, "13800138000", "小明")
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), []byte("x")...)
	for img, ext := range map[string]string{string(testJPEG): "jpg", string(webp): "webp"} {
		if _, err := e.users.UpdateAvatar(context.Background(), u.ID, []byte(img)); err != nil {
			t.Fatal(err)
		}
		if k := e.objectKey(t, u.ID); k[len(k)-len(ext):] != ext {
			t.Fatalf("key = %q, 扩展名应为 %s", k, ext)
		}
	}
}

func TestUpdateAvatarReplaceQueuesOldKeyForGC(t *testing.T) {
	e := newOSSEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "小明")
	if _, err := e.users.UpdateAvatar(ctx, u.ID, testPNG); err != nil {
		t.Fatal(err)
	}
	oldKey := e.objectKey(t, u.ID)
	if _, err := e.users.UpdateAvatar(ctx, u.ID, testJPEG); err != nil {
		t.Fatal(err)
	}
	newKey := e.objectKey(t, u.ID)
	if newKey == oldKey {
		t.Fatal("每次上传都应生成新 key")
	}
	if got := e.gcKeys(t); len(got) != 1 || got[0] != oldKey {
		t.Fatalf("object_gc = %v, want [%s]", got, oldKey)
	}
	// 事务提交后旧对象还在，直到 GC 跑过：先登记后删，崩溃也不漏。
	if _, ok := e.store.Get(oldKey); !ok {
		t.Fatal("GC 之前旧对象不该被删")
	}
	n, err := e.gc.RunOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("RunOnce = %d, %v", n, err)
	}
	if _, ok := e.store.Get(oldKey); ok {
		t.Fatal("GC 后旧对象应已删除")
	}
	if _, ok := e.store.Get(newKey); !ok {
		t.Fatal("新对象不能被误删")
	}
	if got := e.gcKeys(t); len(got) != 0 {
		t.Fatalf("删除成功后应出队: %v", got)
	}
}

func TestClearAvatarQueuesKeyAndRemovesURL(t *testing.T) {
	e := newOSSEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "小明")
	if _, err := e.users.UpdateAvatar(ctx, u.ID, testPNG); err != nil {
		t.Fatal(err)
	}
	key := e.objectKey(t, u.ID)
	p, err := e.users.UpdateAvatar(ctx, u.ID, nil)
	if err != nil || p.AvatarURL != "" {
		t.Fatalf("清除后 AvatarURL = %q, %v", p.AvatarURL, err)
	}
	if got := e.gcKeys(t); len(got) != 1 || got[0] != key {
		t.Fatalf("object_gc = %v, want [%s]", got, key)
	}
}

func TestUpdateAvatarWithoutStoreIsUnavailable(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	e := &profileEnv{users: service.NewUserService(pool), pool: pool}
	u := e.newUser(t, "13800138000", "小明")
	_, err := e.users.UpdateAvatar(context.Background(), u.ID, testPNG)
	wantCode(t, err, domain.CodeObjectStoreUnavailable, domain.ErrInternal)
	// 图片本身不合法时仍先报图片的错，别让调用方以为是平台问题。
	_, err = e.users.UpdateAvatar(context.Background(), u.ID, []byte("not image"))
	wantCode(t, err, domain.CodeAvatarInvalid, domain.ErrInvalidArgument)
}

func TestUpdateAvatarPutFailureLeavesOldAvatar(t *testing.T) {
	e := newOSSEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "小明")
	if _, err := e.users.UpdateAvatar(ctx, u.ID, testPNG); err != nil {
		t.Fatal(err)
	}
	old := e.objectKey(t, u.ID)
	e.store.FailPut = errors.New("oss down")
	if _, err := e.users.UpdateAvatar(ctx, u.ID, testJPEG); err == nil {
		t.Fatal("上传失败应返回错误")
	}
	// 只留着新 key 的预登记（上传可能其实成功了，到期删掉）；原头像的 key 不能登记删除。
	if gc := e.gcKeys(t); e.objectKey(t, u.ID) != old || len(gc) != 1 || gc[0] == old {
		t.Fatalf("上传失败不能改动原头像，也不该登记删除它: gc=%v", gc)
	}
}

func TestUpdateAvatarDBFailureDeletesUploadedObject(t *testing.T) {
	e := newOSSEnv(t)
	// 用户不存在 → 事务内 RowsAffected=0 报错，已 Put 的对象必须回收，否则成孤儿。
	if _, err := e.users.UpdateAvatar(context.Background(), uuid.New(), testPNG); err == nil {
		t.Fatal("应报用户不存在")
	}
	if keys := e.store.Keys(); len(keys) != 0 {
		t.Fatalf("失败后不该残留对象: %v", keys)
	}
}

func TestObjectGCBackoffOnFailure(t *testing.T) {
	e := newOSSEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO object_gc (key) VALUES ('avatar/x.png')`); err != nil {
		t.Fatal(err)
	}
	e.store.FailDelete = errors.New("oss down")
	for i, wantMin := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		// 把到期时间拨到过去，模拟时间流逝。
		if _, err := e.pool.Exec(ctx, `UPDATE object_gc SET due_at = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if n, err := e.gc.RunOnce(ctx); err != nil || n != 0 {
			t.Fatalf("round %d: RunOnce = %d, %v", i, n, err)
		}
		var attempts int
		var wait time.Duration
		if err := e.pool.QueryRow(ctx,
			`SELECT attempts, due_at - now() FROM object_gc WHERE key = 'avatar/x.png'`).Scan(&attempts, &wait); err != nil {
			t.Fatal(err)
		}
		if attempts != i+1 || wait < wantMin-5*time.Second || wait > wantMin+5*time.Second {
			t.Fatalf("round %d: attempts=%d 退避=%v, want %d 次、约 %v", i, attempts, wait, i+1, wantMin)
		}
	}
	// 未到期的不会被取。
	if n, _ := e.gc.RunOnce(ctx); n != 0 {
		t.Fatalf("未到期不应处理, n = %d", n)
	}
	// 退避封顶 1 小时。
	if _, err := e.pool.Exec(ctx, `UPDATE object_gc SET attempts = 30, due_at = now()`); err != nil {
		t.Fatal(err)
	}
	e.gc.RunOnce(ctx)
	var wait time.Duration
	e.pool.QueryRow(ctx, `SELECT due_at - now() FROM object_gc`).Scan(&wait)
	if wait > time.Hour+5*time.Second {
		t.Fatalf("退避 %v 超过封顶 1 小时", wait)
	}
	// 恢复后成功出队。
	e.store.FailDelete = nil
	e.pool.Exec(ctx, `UPDATE object_gc SET due_at = now() - interval '1 second'`)
	if n, err := e.gc.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("恢复后 RunOnce = %d, %v", n, err)
	}
}

func TestObjectGCBatchLimit(t *testing.T) {
	e := newOSSEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO object_gc (key) SELECT 'k/' || g FROM generate_series(1, 150) g`); err != nil {
		t.Fatal(err)
	}
	if n, err := e.gc.RunOnce(ctx); err != nil || n != 100 {
		t.Fatalf("一轮应取 100 条: %d, %v", n, err)
	}
	if n, _ := e.gc.RunOnce(ctx); n != 50 {
		t.Fatalf("第二轮应取剩下 50 条: %d", n)
	}
}

func TestAvatarURLByHash(t *testing.T) {
	e := newOSSEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "小明")
	if _, err := e.users.UpdateAvatar(ctx, u.ID, testPNG); err != nil {
		t.Fatal(err)
	}
	got, err := e.users.AvatarURLByHash(ctx, domain.AvatarHash(testPNG))
	if err != nil || got != "https://memory.test/"+e.objectKey(t, u.ID) {
		t.Fatalf("AvatarURLByHash = %q, %v", got, err)
	}
	_, err = e.users.AvatarURLByHash(ctx, domain.AvatarHash(testJPEG))
	wantCode(t, err, domain.CodeAvatarNotFound, domain.ErrNotFound)
	_, err = e.users.AvatarURLByHash(ctx, "bad")
	wantCode(t, err, domain.CodeAvatarNotFound, domain.ErrNotFound)
}

func seedLegacyAvatar(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, image []byte) {
	t.Helper()
	ct, err := domain.CheckAvatar(image)
	if err != nil {
		t.Fatal(err)
	}
	h := domain.AvatarHash(image)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE app_user SET avatar_hash = $2 WHERE id = $1`, id, h); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_avatar (user_id, hash, content_type, data) VALUES ($1, $2, $3, $4)`, id, h, ct, image); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateAvatarsIsRepeatable(t *testing.T) {
	e := newOSSEnv(t)
	ctx := context.Background()
	a := e.newUser(t, "13800138000", "甲")
	b := e.newUser(t, "13800138001", "乙")
	seedLegacyAvatar(t, e.pool, a.ID, testPNG)
	seedLegacyAvatar(t, e.pool, b.ID, testJPEG)

	n, err := e.users.MigrateAvatars(ctx, 1)
	if err != nil || n != 2 {
		t.Fatalf("第一次 = %d, %v, want 2（batch=1 要分批取完）", n, err)
	}
	ka, kb := e.objectKey(t, a.ID), e.objectKey(t, b.ID)
	if !avatarKeyRe.MatchString(ka) || !avatarKeyRe.MatchString(kb) || ka == kb {
		t.Fatalf("keys = %q, %q", ka, kb)
	}
	if obj, ok := e.store.Get(ka); !ok || string(obj.Body) != string(testPNG) {
		t.Fatal("对象内容不对")
	}
	// data 保留：老路径与回滚靠它，收尾另删。
	if _, _, _, _, data := e.storedAvatar(t, a.ID); string(data) != string(testPNG) {
		t.Fatal("迁移不应清 data")
	}

	n, err = e.users.MigrateAvatars(ctx, 100)
	if err != nil || n != 0 {
		t.Fatalf("第二次 = %d, %v, want 0", n, err)
	}
	if e.objectKey(t, a.ID) != ka || len(e.store.Keys()) != 2 || len(e.gcKeys(t)) != 0 {
		t.Fatal("重复执行不该改 key、多传对象或登记删除")
	}
	// 迁移后老 hash 取得到 URL，取字节也仍可用。
	if u, err := e.users.AvatarURLByHash(ctx, domain.AvatarHash(testPNG)); err != nil || u != "https://memory.test/"+ka {
		t.Fatalf("AvatarURLByHash = %q, %v", u, err)
	}
}

func TestMigrateAvatarsPutFailureStops(t *testing.T) {
	e := newOSSEnv(t)
	u := e.newUser(t, "13800138000", "甲")
	seedLegacyAvatar(t, e.pool, u.ID, testPNG)
	e.store.FailPut = errors.New("oss down")
	if n, err := e.users.MigrateAvatars(context.Background(), 10); err == nil || n != 0 {
		t.Fatalf("= %d, %v, 应报错", n, err)
	}
	if e.objectKey(t, u.ID) != "" {
		t.Fatal("失败不应回填 key")
	}
}
