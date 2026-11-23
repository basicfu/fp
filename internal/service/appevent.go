package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
)

// AppEventRetention 是已确认事件的保留期，也是「多久没确认就告警」的门槛。
const AppEventRetention = 7 * 24 * time.Hour

// AppEventApps 是 AppEvents 对应用的全部依赖，*ApplicationService 满足它。
// 按 id 取走缓存：每次登录、撤销、过期都要写事件，没开事件的应用也不该每次白查一趟库。
type AppEventApps interface {
	GetByIDCached(ctx context.Context, id uuid.UUID) (*domain.Application, error)
	List(ctx context.Context) ([]domain.Application, error)
}

// AppEventNotifier 在事件写入后唤醒投递方，*store.AppEventSignal 满足它。
type AppEventNotifier interface {
	Publish(ctx context.Context, appID uuid.UUID) error
}

// AppEvents 是业务事件表：写入、按序读未确认的、确认、清理。投递在 grpcapi。
type AppEvents struct {
	pool   *pgxpool.Pool
	apps   AppEventApps
	notify AppEventNotifier
}

// NewAppEvents 构造 AppEvents。notify 可为 nil，投递方只靠轮询发现新事件。
func NewAppEvents(pool *pgxpool.Pool, apps AppEventApps, notify AppEventNotifier) *AppEvents {
	return &AppEvents{pool: pool, apps: apps, notify: notify}
}

// AppEventAppender 是写业务事件的最小能力，*AppEvents 满足它。会话、登录、微信订阅各处只依赖它。
type AppEventAppender interface {
	Append(ctx context.Context, appID, userID uuid.UUID, kind string, payload any) error
}

// 各种事件的 payload，字段名是对业务方的线上格式（见 sdk 的 Event* 常量）。
type (
	LoginSucceededPayload struct {
		SessionID string `json:"session_id"`
		ClientRef string `json:"client_ref"`
	}
	LoggedOutPayload struct {
		SessionID string `json:"session_id"`
		ClientRef string `json:"client_ref"`
		// Reason 是 domain.RevokeReason* 之一，或 domain.LoggedOutExpired / domain.LoggedOutRevoked。
		Reason string `json:"reason"`
	}
	WechatSubscriptionPayload struct {
		Subscribed bool `json:"subscribed"`
	}
)

// LoggedOutExpired 是会话空闲超时或到达绝对上限时 logged_out 的 reason。
const LoggedOutExpired = domain.LoggedOutExpired

// Append 给一个应用写一条事件。应用还没有流声明过 app_events（见 ApplicationService.EnableAccountEvents）、
// 或应用已删除时什么都不做。
//
// 调用方要「先写事件再改状态」（如先 Append 再撤销会话）：两者不在一个事务里，
// 中途失败时宁可业务方多收一条事件，也不能让状态已变、业务方却永远不知道。
func (s *AppEvents) Append(ctx context.Context, appID, userID uuid.UUID, kind string, payload any) error {
	app, err := s.apps.GetByIDCached(ctx, appID)
	if errors.Is(err, domain.ErrNotFound) {
		// 应用删了就没有投递对象；报错的话，它名下残留会话的撤销会被一直卡住。
		return nil
	}
	if err != nil {
		return err
	}
	if !app.Account.Events {
		return nil
	}
	if err := s.insert(ctx, s.pool, appID, userID, kind, payload); err != nil {
		return err
	}
	s.Wake(ctx, []uuid.UUID{appID})
	return nil
}

// AppendForUserApps 给所有收事件的启用应用各写一条，用于账号删除这类不属于某个应用的事件。
func (s *AppEvents) AppendForUserApps(ctx context.Context, userID uuid.UUID, kind string, payload any) error {
	written, err := s.AppendForUserAppsTx(ctx, s.pool, userID, kind, payload)
	s.Wake(ctx, written)
	return err
}

// AppendForUserAppsTx 同 AppendForUserApps，但写在调用方的事务里，让事件与状态变更一起提交或一起回滚；
// 返回写了哪些应用，调用方提交后对它们 Wake。
func (s *AppEvents) AppendForUserAppsTx(ctx context.Context, db AppEventDB, userID uuid.UUID, kind string, payload any) ([]uuid.UUID, error) {
	apps, err := s.apps.List(ctx)
	if err != nil {
		return nil, err
	}
	var written []uuid.UUID
	for _, app := range apps {
		if !app.Account.Events || app.Status != domain.ApplicationStatusActive {
			continue
		}
		if err := s.insert(ctx, db, app.ID, userID, kind, payload); err != nil {
			return written, err
		}
		written = append(written, app.ID)
	}
	return written, nil
}

// AppEventDB 是写事件用的连接：*pgxpool.Pool 或 pgx.Tx。
type AppEventDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Wake 唤醒投递方去取这些应用的新事件。只是加速，投递方有轮询兜底，失败只记日志。
func (s *AppEvents) Wake(ctx context.Context, appIDs []uuid.UUID) {
	if s.notify == nil {
		return
	}
	for _, id := range appIDs {
		if err := s.notify.Publish(context.WithoutCancel(ctx), id); err != nil {
			slog.Warn("service: 业务事件唤醒失败，等投递方轮询", "err", err, "appId", id)
		}
	}
}

func (s *AppEvents) insert(ctx context.Context, db AppEventDB, appID, userID uuid.UUID, kind string, payload any) error {
	raw := []byte("{}")
	if payload != nil {
		var err error
		if raw, err = json.Marshal(payload); err != nil {
			return fmt.Errorf("service: 序列化事件内容: %w", err)
		}
	}
	// 发号与写入在一条语句里：发号行的行锁持有到语句结束，同一应用的并发写入因此串行，seq 连续无洞。
	_, err := db.Exec(ctx, `
		WITH s AS (
			INSERT INTO app_event_seq (app_id, next) VALUES ($1, 1)
			ON CONFLICT (app_id) DO UPDATE SET next = app_event_seq.next + 1
			RETURNING next
		)
		INSERT INTO app_event (app_id, seq, user_id, kind, payload)
		SELECT $1, s.next, $2, $3, $4 FROM s`,
		appID, uuid.NullUUID{UUID: userID, Valid: userID != uuid.Nil}, kind, raw)
	if err != nil {
		return fmt.Errorf("service: 写入业务事件: %w", err)
	}
	return nil
}

// Pending 按 seq 升序返回 seq > afterSeq 的未确认事件，最多 limit 条。
func (s *AppEvents) Pending(ctx context.Context, appID uuid.UUID, afterSeq int64, limit int) ([]domain.AppEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, seq, user_id, kind, payload, created_at FROM app_event
		WHERE app_id = $1 AND seq > $2 AND acked_at IS NULL
		ORDER BY seq LIMIT $3`, appID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("service: 查询未确认事件: %w", err)
	}
	defer rows.Close()
	out := []domain.AppEvent{}
	for rows.Next() {
		ev := domain.AppEvent{AppID: appID}
		var user uuid.NullUUID
		if err := rows.Scan(&ev.ID, &ev.Seq, &user, &ev.Kind, &ev.Payload, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("service: 扫描事件: %w", err)
		}
		ev.UserID = user.UUID
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历事件: %w", err)
	}
	return out, nil
}

// Ack 确认一批事件。别的应用的事件 id 不会被确认，重复确认无害。
func (s *AppEvents) Ack(ctx context.Context, appID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE app_event SET acked_at = now()
		WHERE app_id = $1 AND id = ANY($2) AND acked_at IS NULL`, appID, ids); err != nil {
		return fmt.Errorf("service: 确认事件: %w", err)
	}
	return nil
}

// Prune 删除 before 之前确认的事件，返回删除条数；顺带对 before 之前就写入、至今没确认的事件告警。
func (s *AppEvents) Prune(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM app_event WHERE acked_at IS NOT NULL AND acked_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("service: 清理已确认事件: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT app_id, count(*) FROM app_event
		WHERE acked_at IS NULL AND created_at < $1 GROUP BY app_id`, before)
	if err != nil {
		return int(tag.RowsAffected()), fmt.Errorf("service: 统计久未确认的事件: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var app uuid.UUID
		var n int64
		if err := rows.Scan(&app, &n); err != nil {
			return int(tag.RowsAffected()), fmt.Errorf("service: 扫描久未确认的事件: %w", err)
		}
		slog.Warn("service: 应用长期没有确认业务事件", "appId", app, "count", n, "before", before)
	}
	return int(tag.RowsAffected()), rows.Err()
}

// RunPrune 每 interval 清理一次，直到 ctx 取消。多实例同时跑只是重复删同一批，无害。
func (s *AppEvents) RunPrune(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.Prune(ctx, time.Now().Add(-AppEventRetention)); err != nil {
				slog.Error("service: 清理业务事件失败", "err", err)
			} else if n > 0 {
				slog.Info("service: 清理已确认的业务事件", "count", n)
			}
		}
	}
}
