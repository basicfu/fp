package captcha

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"github.com/basicfu/fp/internal/accountapi/captcha/gen"
)

// 写死,后期按用户量再调(见规格"题库"一节)。
const (
	poolSize     = 200
	maxUses      = 5
	emptyWait    = 50 * time.Millisecond
	emptyTimeout = 3 * time.Second
)

var ErrUnavailable = errors.New("captcha: 题库暂时为空")

type entry struct {
	p    gen.Puzzle
	uses int
}

// Pool 是本实例的成品题库。只有 Run 这一个协程出图,CPU 占用封顶一个核;请求从不现场出图。
type Pool struct {
	gen    func(style int) (gen.Puzzle, error)
	styles int

	size, maxUses int
	wait, timeout time.Duration

	mu    sync.Mutex
	items []*entry
	rnd   *mrand.Rand
	room  chan struct{} // 题被删掉时投一个信号,唤醒等空位的 Run
}

func NewPool(numStyles int, g func(style int) (gen.Puzzle, error)) *Pool {
	var seed [32]byte
	_, _ = rand.Read(seed[:])
	return &Pool{gen: g, styles: numStyles, size: poolSize, maxUses: maxUses,
		wait: emptyWait, timeout: emptyTimeout, rnd: mrand.New(mrand.NewChaCha8(seed)),
		room: make(chan struct{}, 1)}
}

func (p *Pool) Run(ctx context.Context) {
	for ctx.Err() == nil {
		style, full := p.next()
		if full {
			select {
			case <-ctx.Done():
				return
			case <-p.room:
			}
			continue
		}
		pz, err := p.safeGen(style)
		if err != nil {
			slog.Warn("captcha: 出图失败", "style", style, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		p.mu.Lock()
		// 只剩一题时 take 不删它,uses 会超过上限;有新题了就清掉,否则题库再见底时它又被接着发,刷题者能攒样本。
		kept := p.items[:0]
		for _, e := range p.items {
			if e.uses < p.maxUses {
				kept = append(kept, e)
			}
		}
		clear(p.items[len(kept):])
		p.items = append(kept, &entry{p: pz})
		p.mu.Unlock()
	}
}

// safeGen 把出图 panic 当成一次出图失败:Run 是本实例唯一的出图协程,它一挂题库就再也不补。
func (p *Pool) safeGen(style int) (pz gen.Puzzle, err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			err = fmt.Errorf("panic: %v", r)
			slog.Error("captcha: 出图 panic", "style", style, "err", err, "stack", string(stack[:min(len(stack), 2048)]))
		}
	}()
	pz, err = p.gen(style)
	if err == nil && (pz.Style < 0 || pz.Style >= p.styles) {
		err = fmt.Errorf("风格 %d 越界", pz.Style) // 不丢掉的话 next() 按风格计数会越界 panic
	}
	return pz, err
}

// next 选库里数量最少的风格(并列随机),题库满了返回 full。
func (p *Pool) next() (style int, full bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.items) >= p.size {
		return 0, true
	}
	count := make([]int, p.styles)
	for _, e := range p.items {
		count[e.p.Style]++
	}
	best, ties := -1, 0
	for s, c := range count {
		switch {
		case best < 0 || c < count[best]:
			best, ties = s, 1
		case c == count[best]:
			ties++
			if p.rnd.IntN(ties) == 0 {
				best = s
			}
		}
	}
	return best, false
}

func (p *Pool) Take(ctx context.Context) (gen.Puzzle, error) {
	deadline := time.Now().Add(p.timeout)
	for {
		if pz, ok := p.take(); ok {
			return pz, nil
		}
		if time.Now().After(deadline) {
			return gen.Puzzle{}, ErrUnavailable
		}
		select {
		case <-ctx.Done():
			return gen.Puzzle{}, ctx.Err()
		case <-time.After(p.wait):
		}
	}
}

// take 在使用次数最少的题里随机挑一题。用满就删,但只剩最后一题时不删。
func (p *Pool) take() (gen.Puzzle, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.items) == 0 {
		return gen.Puzzle{}, false
	}
	minUses := p.items[0].uses
	for _, e := range p.items[1:] {
		minUses = min(minUses, e.uses)
	}
	pick, seen := -1, 0
	for i, e := range p.items {
		if e.uses == minUses {
			seen++
			if p.rnd.IntN(seen) == 0 {
				pick = i
			}
		}
	}
	e := p.items[pick]
	e.uses++
	if e.uses >= p.maxUses && len(p.items) > 1 {
		p.items[pick] = p.items[len(p.items)-1]
		p.items = p.items[:len(p.items)-1]
		select {
		case p.room <- struct{}{}:
		default:
		}
	}
	return e.p, true
}
