package service_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/objstore"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

// hookStore 包住内存对象存储：onPut 在上传时调，删除可以放慢并计数。
type hookStore struct {
	*objstore.Memory
	onPut    func(key string)
	delDelay time.Duration
	deletes  atomic.Int64
}

func (s *hookStore) Put(ctx context.Context, key, ct string, body []byte, cc string) error {
	if s.onPut != nil {
		s.onPut(key)
	}
	return s.Memory.Put(ctx, key, ct, body, cc)
}

func (s *hookStore) Delete(ctx context.Context, key string) error {
	s.deletes.Add(1)
	time.Sleep(s.delDelay)
	return s.Memory.Delete(ctx, key)
}

// 多实例同时跑清理：同一批 key 只能被一个实例领走，不能各删一遍。
func TestObjectGCConcurrentRunnersClaimDisjoint(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	ctx := context.Background()
	st := &hookStore{Memory: objstore.NewMemory(), delDelay: 20 * time.Millisecond}
	const n = 10
	for i := 0; i < n; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO object_gc (key) VALUES ($1)`, fmt.Sprintf("avatar/%d.png", i)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := service.NewObjectGC(pool, st).RunOnce(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := st.deletes.Load(); got != n {
		t.Fatalf("删除调用 %d 次, want %d", got, n)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_gc`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("还剩 %d 行, %v", left, err)
	}
}

// 上传之前新 key 已以较晚的期限登记待删：上传成功、事务没提交就崩，对象也会被回收，不成孤儿；
// 事务提交时同事务摘掉登记。
func TestUpdateAvatarRegistersNewKeyBeforeUpload(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	ctx := context.Background()
	st := &hookStore{Memory: objstore.NewMemory()}
	e := &profileEnv{users: service.NewUserService(pool, service.WithObjectStore(st)), pool: pool}
	u := e.newUser(t, "13800138000", "甲")
	var registered bool
	st.onPut = func(key string) {
		var wait time.Duration
		err := pool.QueryRow(ctx, `SELECT due_at - now() FROM object_gc WHERE key = $1`, key).Scan(&wait)
		registered = err == nil && wait > 30*time.Minute
	}
	if _, err := e.users.UpdateAvatar(ctx, u.ID, testPNG); err != nil {
		t.Fatal(err)
	}
	if !registered {
		t.Fatal("上传时新 key 没有以较晚期限登记待删")
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_gc`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("提交后登记没摘掉: %d, %v", left, err)
	}

	// 迁移同理。
	registered = false
	if _, err := pool.Exec(ctx, `UPDATE user_avatar SET object_key = '', data = $2 WHERE user_id = $1`, u.ID, testPNG); err != nil {
		t.Fatal(err)
	}
	if n, err := e.users.MigrateAvatars(ctx, 10); err != nil || n != 1 {
		t.Fatalf("MigrateAvatars = %d, %v", n, err)
	}
	if !registered {
		t.Fatal("迁移上传时新 key 没有登记待删")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_gc`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("迁移回填后登记没摘掉: %d, %v", left, err)
	}
}
