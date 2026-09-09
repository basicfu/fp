package imgrpc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeVerifier 计数并按一张表判定。
type fakeVerifier struct {
	mu    sync.Mutex
	calls int
	ok    map[string]string // app → 正确的 secret
	err   error             // 非 nil 时一律返回它（模拟 fp 不可达）
}

func (f *fakeVerifier) VerifyAppCredential(_ context.Context, app, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	if f.ok[app] != secret {
		return errors.New("凭据无效")
	}
	return nil
}

func (f *fakeVerifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestCredCacheOnlyCachesSuccess：凭据校验从"比对本地明文"变成"转给 fp"，
// 于是 fp 不可达时业务 server 接不进来——这是本改动新增的依赖。缓存把 fp
// 的短暂抖动挡在外面。
//
// **只缓存成功**：键的一半（secret）来自调用方，缓存失败等于把 map 的大小
// 交给攻击者——一百万个不同的错误 secret 就能把进程 OOM 掉。
func TestCredCacheOnlyCachesSuccess(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{"a1": "s1"}}
	c := newCredCache(v, time.Minute)

	for i := 0; i < 3; i++ {
		if err := c.verify(context.Background(), "a1", "s1"); err != nil {
			t.Fatal(err)
		}
	}
	if n := v.count(); n != 1 {
		t.Fatalf("成功结果回源了 %d 次，应当只有 1 次", n)
	}

	v.mu.Lock()
	v.calls = 0
	v.mu.Unlock()
	for i := 0; i < 3; i++ {
		if err := c.verify(context.Background(), "a1", "wrong"); err == nil {
			t.Fatal("错误凭据必须被拒")
		}
	}
	if n := v.count(); n != 3 {
		t.Fatalf("失败结果回源了 %d 次，失败不缓存意味着每次都要回源", n)
	}
}

// TestCredCacheKeyIsAppScoped：拿 A 的正确 secret 冒充 B 不能命中缓存。
func TestCredCacheKeyIsAppScoped(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{"a1": "s1", "a2": "s2"}}
	c := newCredCache(v, time.Minute)

	if err := c.verify(context.Background(), "a1", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := c.verify(context.Background(), "a2", "s1"); err == nil {
		t.Fatal("拿 a1 的 secret 冒充 a2 必须被拒")
	}
}

// TestCredCacheRejectsEmpty：空 app 或空 secret 直接拒，不必惊动 fp。
func TestCredCacheRejectsEmpty(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{"a1": "s1"}}
	c := newCredCache(v, time.Minute)
	if err := c.verify(context.Background(), "", "s1"); err == nil {
		t.Fatal("空 app 必须被拒")
	}
	if err := c.verify(context.Background(), "a1", ""); err == nil {
		t.Fatal("空 secret 必须被拒")
	}
	if n := v.count(); n != 0 {
		t.Fatalf("空凭据不该回源，实际 %d 次", n)
	}
}

// TestCredCacheSurvivesFPOutage：命中缓存时 fp 挂了也不影响业务 server 重连。
// 这正是加这层缓存的理由。
func TestCredCacheSurvivesFPOutage(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{"a1": "s1"}}
	c := newCredCache(v, time.Minute)
	if err := c.verify(context.Background(), "a1", "s1"); err != nil {
		t.Fatal(err)
	}
	v.mu.Lock()
	v.err = errors.New("fp 不可达")
	v.mu.Unlock()

	if err := c.verify(context.Background(), "a1", "s1"); err != nil {
		t.Fatalf("已缓存的凭据在 fp 不可达时仍应放行：%v", err)
	}
	if err := c.verify(context.Background(), "a2", "s2"); err == nil {
		t.Fatal("没缓存过的凭据在 fp 不可达时必须被拒")
	}
}
