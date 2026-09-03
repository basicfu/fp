package redisx

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Runner 是所有 Redis 写命令的入口。默认每次 Run 立即执行一次管道；
// 配置了攒批后，多个 Run 合并成一次往返，按"时长或条数先到"刷新。
// 把开关放在这里而不是各调用方，是为了业务代码永远只写 Run(fn)，不感知攒批。
type Runner struct {
	c        redis.UniversalClient
	interval time.Duration
	size     int

	mu      sync.Mutex
	pending []job
	timer   *time.Timer
	closed  bool
}

type job struct {
	fn   func(redis.Pipeliner)
	done chan error
}

var ErrClosed = errors.New("redisx: runner 已关闭")

// NewRunner 的参数对应配置 pipeline.flush_interval / pipeline.flush_size。
// size>1 而 interval=0 是配置错误：单条命令会永远凑不满，这里拒绝而不是运行期挂死。
func NewRunner(c redis.UniversalClient, flushInterval time.Duration, flushSize int) (*Runner, error) {
	if flushSize > 1 && flushInterval <= 0 {
		return nil, errors.New("redisx: pipeline.flush_size > 1 时必须设置 pipeline.flush_interval")
	}
	return &Runner{c: c, interval: flushInterval, size: flushSize}, nil
}

func (r *Runner) batching() bool { return r.interval > 0 }

// Run 把 fn 里排进管道的命令发出去，返回后 fn 捕获的 Cmd 都已填好结果。
// 返回值是管道级错误；单条命令的错误（如 redis.Nil）由调用方看各自的 Cmd.Err()。
func (r *Runner) Run(ctx context.Context, fn func(redis.Pipeliner)) error {
	if !r.batching() {
		_, err := r.c.Pipelined(ctx, func(p redis.Pipeliner) error { fn(p); return nil })
		return ignoreNil(err)
	}
	j := job{fn: fn, done: make(chan error, 1)}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	r.pending = append(r.pending, j)
	if r.size > 1 && len(r.pending) >= r.size {
		batch := r.take()
		r.mu.Unlock()
		r.flush(batch)
	} else {
		if len(r.pending) == 1 {
			r.timer = time.AfterFunc(r.interval, r.flushByTimer)
		}
		r.mu.Unlock()
	}
	select {
	case err := <-j.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) take() []job {
	batch := r.pending
	r.pending = nil
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	return batch
}

func (r *Runner) flushByTimer() {
	r.mu.Lock()
	batch := r.take()
	r.mu.Unlock()
	r.flush(batch)
}

// flush 用独立的 5 秒超时而不是某个调用方的 ctx：一批里的命令来自不同调用方，
// 谁的 ctx 都不该决定别人的命运。
func (r *Runner) flush(batch []job) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		for _, j := range batch {
			j.fn(p)
		}
		return nil
	})
	err = ignoreNil(err)
	for _, j := range batch {
		j.done <- err
	}
}

// Close 把还没刷的批刷出去。之后的 Run 返回 ErrClosed。
func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	batch := r.take()
	r.mu.Unlock()
	r.flush(batch)
}

// Pipelined 会把第一条返回 redis.Nil 的命令当成整批的错误返回，
// 但 Nil 只是"key 不存在"，不是故障，调用方要看各自的 Cmd。
func ignoreNil(err error) error {
	if errors.Is(err, redis.Nil) {
		return nil
	}
	return err
}
