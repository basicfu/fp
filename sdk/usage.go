package fpsdk

import (
	"context"
	"sync"
	"time"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// usageRecorder 按分钟记录每把 key 最后一次校验通过的时间，由 Client 定期批量上报。
type usageRecorder struct {
	mu   sync.Mutex
	last map[string]int64 // AK → 按分钟截断的 Unix 毫秒
}

func newUsageRecorder() *usageRecorder { return &usageRecorder{last: make(map[string]int64)} }

func (u *usageRecorder) record(akID string, at time.Time) {
	m := at.Truncate(time.Minute).UnixMilli()
	u.mu.Lock()
	if m > u.last[akID] {
		u.last[akID] = m
	}
	u.mu.Unlock()
}

// take 取走当前这一批。
func (u *usageRecorder) take() map[string]int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	batch := u.last
	u.last = make(map[string]int64)
	return batch
}

// restore 把上报失败的批次并回去，保留较新的时间。
func (u *usageRecorder) restore(batch map[string]int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for id, ms := range batch {
		if ms > u.last[id] {
			u.last[id] = ms
		}
	}
}

// flushUsage 上报一批使用时间；失败的并入下一批。
func (c *Client) flushUsage(ctx context.Context) {
	batch := c.usage.take()
	if len(batch) == 0 {
		return
	}
	req := &fpv1.ReportAccessKeyUsageRequest{Usages: make([]*fpv1.AccessKeyUsage, 0, len(batch))}
	for id, ms := range batch {
		req.Usages = append(req.Usages, &fpv1.AccessKeyUsage{AccessKeyId: id, LastUsedAtMs: ms})
	}
	if _, err := c.rpc.ReportAccessKeyUsage(ctx, req); err != nil {
		c.usage.restore(batch)
		c.opts.Logger.Warn("fpsdk: 上报访问密钥使用时间失败，并入下一批", "err", err)
	}
}

// runEvery 每隔 every 调一次 fn，直到 ctx 取消。
func runEvery(ctx context.Context, every time.Duration, fn func(context.Context)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}
