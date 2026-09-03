package redisx

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/testsupport"
	"github.com/redis/go-redis/v9"
)

type countHook struct{ pipelines atomic.Int64 }

func (h *countHook) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (h *countHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (h *countHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.pipelines.Add(1)
		return next(ctx, cmds)
	}
}

func TestRunnerImmediateByDefault(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	h := &countHook{}
	rdb.AddHook(h)
	r, _ := NewRunner(rdb, 0, 1)
	defer r.Close()
	for i := 0; i < 3; i++ {
		var cmd *redis.IntCmd
		if err := r.Run(context.Background(), func(p redis.Pipeliner) { cmd = p.Incr(context.Background(), "fp:im:test:ctr") }); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if cmd.Val() != int64(i+1) {
			t.Fatalf("命令结果应在 Run 返回后可读：第 %d 次 Incr 得 %d", i+1, cmd.Val())
		}
	}
	if got := h.pipelines.Load(); got != 3 {
		t.Fatalf("默认配置每条命令一次往返，3 条应是 3 次管道执行，实际 %d", got)
	}
}

func TestRunnerBatchesBySizeOrInterval(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	h := &countHook{}
	rdb.AddHook(h)
	r, _ := NewRunner(rdb, 50*time.Millisecond, 3)
	defer r.Close()

	// 三条并发进来，凑满 size=3 立刻刷，只有 1 次管道
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.Run(context.Background(), func(p redis.Pipeliner) { p.Incr(context.Background(), "fp:im:test:ctr") })
		}()
	}
	wg.Wait()
	if got := h.pipelines.Load(); got != 1 {
		t.Fatalf("凑满 flush_size 应合成 1 次管道，实际 %d", got)
	}

	// 单独一条，凑不满 size，靠 interval 刷出去；Run 要在 interval 附近返回而不是永远等
	start := time.Now()
	if err := r.Run(context.Background(), func(p redis.Pipeliner) { p.Incr(context.Background(), "fp:im:test:ctr") }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if el := time.Since(start); el < 30*time.Millisecond || el > 2*time.Second {
		t.Fatalf("单条命令应由 interval 刷出，耗时 %v 不合理", el)
	}
	if got := h.pipelines.Load(); got != 2 {
		t.Fatalf("interval 刷新后应是 2 次管道，实际 %d", got)
	}
}

func TestRunnerRejectsSizeWithoutInterval(t *testing.T) {
	if _, err := NewRunner(nil, 0, 2); err == nil {
		t.Fatal("flush_size>1 且 flush_interval=0 会让单条命令永远等不到刷新，NewRunner 必须拒绝")
	}
}
