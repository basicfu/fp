package fpappcfg

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"

	"github.com/basicfu/fp/internal/im/model"
)

type fakeFetcher struct {
	mu    sync.Mutex
	calls map[string]int
	cfg   map[string]model.AppConfig
	err   error
	// block 非 nil 时，Fetch 会等它关闭再返回，用来制造确定性的并发窗口。
	block chan struct{}
}

func (f *fakeFetcher) Fetch(_ context.Context, app string) (model.AppConfig, error) {
	f.mu.Lock()
	f.calls[app]++
	blk, err := f.block, f.err
	f.mu.Unlock()
	if blk != nil {
		<-blk
	}
	if err != nil {
		return model.AppConfig{}, err
	}
	f.mu.Lock()
	c, ok := f.cfg[app]
	f.mu.Unlock()
	if !ok {
		return model.AppConfig{}, errors.New("no such app")
	}
	return c, nil
}

func (f *fakeFetcher) count(app string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[app]
}

func newFake(cfg map[string]model.AppConfig) *fakeFetcher {
	return &fakeFetcher{calls: map[string]int{}, cfg: cfg}
}

func oneApp() map[string]model.AppConfig {
	return map[string]model.AppConfig{
		"a1": {AppID: "a1", ConnPolicy: model.PolicyReplace, ConnLimit: 5},
	}
}

// TestLoadCachesAndGetIsPureMemory：Get 在握手与 Push 的热路径上，必须是
// 纯内存读——第二次 Load 不能再回源。
func TestLoadCachesAndGetIsPureMemory(t *testing.T) {
	f := newFake(oneApp())
	s := New(f, nil)
	ctx := context.Background()

	if _, ok := s.Get("a1"); ok {
		t.Fatal("Load 之前 Get 不该命中")
	}
	if err := s.Load(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("a1"); !ok {
		t.Fatal("Load 之后 Get 必须命中")
	}
	if err := s.Load(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if n := f.count("a1"); n != 1 {
		t.Fatalf("回源了 %d 次，缓存命中时不该回源", n)
	}
}

// TestLoadFailureIsNotCached：拉取失败不能进缓存。
//
// 缓存键来自 client 的握手帧（未认证输入），缓存失败等于把 map 的大小交给
// 攻击者——与 fp 的 appVerifier 只缓存成功是同一条推理。副作用是不存在的
// app 每次握手都会回源一次，由 fp 侧的限流兜底。
func TestLoadFailureIsNotCached(t *testing.T) {
	f := newFake(nil)
	f.err = errors.New("fp 不可达")
	s := New(f, nil)

	for i := 0; i < 3; i++ {
		if err := s.Load(context.Background(), "ghost"); err == nil {
			t.Fatal("拉取失败必须返回错误")
		}
	}
	if _, ok := s.Get("ghost"); ok {
		t.Fatal("失败的拉取不能留下缓存条目")
	}
	if n := f.count("ghost"); n != 3 {
		t.Fatalf("回源 %d 次，失败不缓存意味着每次都要回源", n)
	}
}

// TestInvalidateForcesRefetch：收到 fp 的变更推送后，下一次 Load 必须回源。
func TestInvalidateForcesRefetch(t *testing.T) {
	f := newFake(oneApp())
	s := New(f, nil)
	ctx := context.Background()
	if err := s.Load(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	s.Invalidate("a1")
	if _, ok := s.Get("a1"); ok {
		t.Fatal("Invalidate 之后 Get 不该再命中")
	}
	if err := s.Load(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if n := f.count("a1"); n != 2 {
		t.Fatalf("回源 %d 次，Invalidate 之后应当重新拉一次", n)
	}
}

// TestOnLoadFiresForNewAppOnly：onLoad 是给调用方同步派生状态用的
// （cmd/fp-im 要在这里把新 app 加进 srv 追踪集合并强制刷新一次）。缓存
// 命中时不该触发——那会让每次握手都多做一次无谓的 Redis 刷新。
func TestOnLoadFiresForNewAppOnly(t *testing.T) {
	f := newFake(oneApp())
	var mu sync.Mutex
	var got []string
	s := New(f, func(app string) { mu.Lock(); got = append(got, app); mu.Unlock() })
	ctx := context.Background()
	_ = s.Load(ctx, "a1")
	_ = s.Load(ctx, "a1")

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "a1" {
		t.Fatalf("onLoad 触发情况 = %v，期望只在首次加载时触发一次", got)
	}
}

// TestConcurrentLoadFetchesOnce：一个 app 的第一批 client 往往同时握手，
// 不合并的话 fp 会被打上 N 次同样的请求。
//
// 用 block 制造确定性的并发窗口：不阻塞的话，赢家可能在别人进来之前就已经
// 写好缓存，测出来的"只回源一次"是撞对的，不是合并生效。
func TestConcurrentLoadFetchesOnce(t *testing.T) {
	f := newFake(oneApp())
	f.block = make(chan struct{})
	s := New(f, nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Load(context.Background(), "a1") }()
	}
	// 等第一个协程真的进到 Fetch 里，再放行。
	for f.count("a1") == 0 {
		runtime.Gosched()
	}
	close(f.block)
	wg.Wait()

	if n := f.count("a1"); n != 1 {
		t.Fatalf("并发 Load 回源了 %d 次，应当合并成 1 次", n)
	}
}
