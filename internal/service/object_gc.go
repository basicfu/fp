package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/objstore"
)

const objectGCBatch = 100

// objectGCLease 是领走的一批多久没处理完就可以被别的实例重新领：领取时把 due_at 推后这么久。
const objectGCLease = 10 * time.Minute

// ObjectGC 删除 object_gc 里登记的对象。
//
// 多实例同时跑各领各的：领取在一条语句里把到期的行推后一个租期（SKIP LOCKED 跳过别人正在领的），
// 同一个 key 不会被两处同时删；领走的实例崩了，租期过后别人接着删。
type ObjectGC struct {
	pool *pgxpool.Pool
	st   objstore.Store
}

// NewObjectGC 构造 ObjectGC。st 不能为 nil（没配对象存储就不该启动它）。
func NewObjectGC(pool *pgxpool.Pool, st objstore.Store) *ObjectGC {
	return &ObjectGC{pool: pool, st: st}
}

// RunOnce 处理一批到期的对象，返回删除成功的个数。失败的退避重试：1 分钟起翻倍，封顶 1 小时，
// 免得对象存储故障时每分钟对同一批 key 重复轰炸。
func (g *ObjectGC) RunOnce(ctx context.Context) (int, error) {
	rows, err := g.pool.Query(ctx, `
		UPDATE object_gc SET due_at = now() + make_interval(secs => $2)
		WHERE key IN (
			SELECT key FROM object_gc WHERE due_at <= now() ORDER BY due_at LIMIT $1 FOR UPDATE SKIP LOCKED
		) RETURNING key`, objectGCBatch, objectGCLease.Seconds())
	if err != nil {
		return 0, fmt.Errorf("service: 领取待删对象: %w", err)
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return 0, fmt.Errorf("service: 扫描待删对象: %w", err)
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("service: 遍历待删对象: %w", err)
	}

	done := 0
	for _, k := range keys {
		if err := g.st.Delete(ctx, k); err != nil {
			slog.Warn("service: 删除对象失败，稍后重试", "key", k, "err", err)
			// least 先于 power 的结果被截断：attempts 很大时 2^attempts 会溢出 float，
			// 所以先把指数压到 6（2^6 分钟 > 1 小时）。
			if _, err := g.pool.Exec(ctx, `
				UPDATE object_gc
				SET attempts = attempts + 1,
				    due_at = now() + least(interval '1 hour', interval '1 minute' * power(2, least(attempts, 6)))
				WHERE key = $1`, k); err != nil {
				return done, fmt.Errorf("service: 记录删除失败: %w", err)
			}
			continue
		}
		if _, err := g.pool.Exec(ctx, `DELETE FROM object_gc WHERE key = $1`, k); err != nil {
			return done, fmt.Errorf("service: 移出待删队列: %w", err)
		}
		done++
	}
	return done, nil
}

// Run 起动后立即跑一轮，之后每 every 一轮，直到 ctx 取消。在独立 goroutine 里调用。
func (g *ObjectGC) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		// 一批取满说明还有积压，不等下一个周期。
		for {
			n, err := g.RunOnce(ctx)
			if err != nil {
				slog.Error("service: 对象清理失败", "err", err)
				break
			}
			if n < objectGCBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
