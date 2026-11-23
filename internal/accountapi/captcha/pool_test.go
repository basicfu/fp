package captcha

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/accountapi/captcha/gen"
)

func fakeGen(n *atomic.Int64) func(style int) (gen.Puzzle, error) {
	return func(style int) (gen.Puzzle, error) {
		v := n.Add(1)
		return gen.Puzzle{Style: style, X: int(v)}, nil
	}
}

func newTestPool(size, uses int, g func(int) (gen.Puzzle, error)) *Pool {
	p := NewPool(3, g)
	p.size, p.maxUses, p.wait, p.timeout = size, uses, 5*time.Millisecond, 200*time.Millisecond
	return p
}

func TestPoolFillsAndStops(t *testing.T) {
	var n atomic.Int64
	p := newTestPool(4, 5, fakeGen(&n))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	if got := n.Load(); got != 4 {
		t.Fatalf("满了还在生成: 生成了 %d", got)
	}
	// 风格均衡: 3 种风格 4 题,每种至少 1 题。
	seen := map[int]int{}
	p.mu.Lock()
	for _, e := range p.items {
		seen[e.p.Style]++
	}
	p.mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("风格分布 %v", seen)
	}
}

func TestPoolRetiresAfterMaxUses(t *testing.T) {
	var n atomic.Int64
	p := newTestPool(2, 2, fakeGen(&n))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	time.Sleep(30 * time.Millisecond)
	for i := 0; i < 4; i++ { // 2 题各 2 次,用完就删,后台补新题
		if _, err := p.Take(ctx); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if got := n.Load(); got < 3 {
		t.Fatalf("用满的题没被替换: 共生成 %d", got)
	}
}

// 只剩最后一题时不按寿命删,题库首题生成后就永远不空。
func TestPoolKeepsLastPuzzle(t *testing.T) {
	p := newTestPool(1, 2, nil) // size=1:库满后 Run 不再出图,库里永远只有这一题
	p.items = []*entry{{p: gen.Puzzle{X: 7}}}
	for i := 0; i < 10; i++ {
		pz, err := p.Take(context.Background())
		if err != nil || pz.X != 7 {
			t.Fatalf("第 %d 次: %v %v", i, pz, err)
		}
	}
}

// 被刷超过寿命的最后一题,在新题补进来时要清掉。
func TestPoolDropsOverusedLastPuzzleOnRefill(t *testing.T) {
	release := make(chan struct{})
	p := newTestPool(3, 2, func(style int) (gen.Puzzle, error) {
		<-release
		return gen.Puzzle{Style: style, X: 8}, nil
	})
	p.items = []*entry{{p: gen.Puzzle{X: 7}}}
	for i := 0; i < 5; i++ {
		if pz, _ := p.Take(context.Background()); pz.X != 7 {
			t.Fatalf("第 %d 次: %v", i, pz)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	release <- struct{}{} // 只放行一题
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		n, first := len(p.items), p.items[0].p.X
		p.mu.Unlock()
		if n == 1 && first == 8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("旧题没清掉: %d 题, 首题 X=%d", n, first)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
}

// 出图 panic 不能带走 Run;越界风格的题要丢掉。
func TestPoolSurvivesGenPanic(t *testing.T) {
	var calls atomic.Int64
	p := newTestPool(2, 5, func(style int) (gen.Puzzle, error) {
		switch calls.Add(1) {
		case 1:
			panic("boom")
		case 2:
			return gen.Puzzle{Style: 99}, nil
		}
		return gen.Puzzle{Style: style, X: 1}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	if _, err := p.Take(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("panic 退避期间应无题: %v", err)
	}
	p.timeout = 3 * time.Second
	if _, err := p.Take(ctx); err != nil {
		t.Fatalf("panic 后没恢复出图: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.items {
		if e.p.Style == 99 {
			t.Fatal("越界风格的题进了库")
		}
	}
}

func TestPoolEmptyWaitsThenTimesOut(t *testing.T) {
	p := newTestPool(1, 5, nil) // 不启动 Run: 题库永远是空的
	start := time.Now()
	if _, err := p.Take(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Fatalf("没等够就返回: %v", d)
	}
}

func TestPoolEmptyWaitsForFirst(t *testing.T) {
	var n atomic.Int64
	slow := func(style int) (gen.Puzzle, error) {
		time.Sleep(40 * time.Millisecond) // 模拟启动后首题还在生成
		return fakeGen(&n)(style)
	}
	p := newTestPool(2, 5, slow)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	if _, err := p.Take(ctx); err != nil { // 立刻来的请求要等到首题,而不是失败
		t.Fatal(err)
	}
}
