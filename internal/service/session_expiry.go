package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
)

// SessionExpiryOptions 是过期扫描的参数，零值取默认。
type SessionExpiryOptions struct {
	// Interval 是两轮之间的间隔，默认 30 秒。一批取满时不等它，立刻接着取。
	Interval time.Duration
	// Batch 是每批领取的成员数，默认 500。
	Batch int
	// Lease 是领走的成员多久没处理完就可以被别的扫描器重新领，默认 60 秒。
	Lease time.Duration
	// Now 返回当前毫秒时间戳，默认真实时钟；测试注入可控时钟。
	Now func() int64
}

// SessionExpiry 给空闲超时或到达绝对上限的会话发 logged_out(expired)。
//
// 业务方没法预知会话何时过期（空闲超时随使用后延），只能由 fp 发。不用 Redis 过期通知：
// 它发出即丢、没有订阅者时丢，惰性过期还会迟到。改为签发、延期时把到期时刻记进有序集合，
// 这里定期领取到点的成员逐个复核。
type SessionExpiry struct {
	store  *store.SessionStore
	epochs EpochStore
	events AppEventAppender
	apps   SessionApps
	opts   SessionExpiryOptions
}

// NewSessionExpiry 构造过期扫描。
func NewSessionExpiry(st *store.SessionStore, epochs EpochStore, events AppEventAppender, apps SessionApps, opts SessionExpiryOptions) *SessionExpiry {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Batch <= 0 {
		opts.Batch = 500
	}
	if opts.Lease <= 0 {
		opts.Lease = time.Minute
	}
	if opts.Now == nil {
		opts.Now = func() int64 { return time.Now().UnixMilli() }
	}
	return &SessionExpiry{store: st, epochs: epochs, events: events, apps: apps, opts: opts}
}

// Run 立即扫一轮，之后每 Interval 一轮，直到 ctx 取消。多实例都跑：领取是原子的，不会重复发。
func (s *SessionExpiry) Run(ctx context.Context) error {
	t := time.NewTicker(s.opts.Interval)
	defer t.Stop()
	for {
		if n, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.Error("service: 会话过期扫描失败，下一轮重试", "err", err, "expired", n)
		} else if n > 0 {
			slog.Info("service: 会话过期扫描", "expired", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Sweep 跑一轮：一批取满就接着取，直到取不满。返回发出的 logged_out 条数。
//
// 处理失败的成员原样留在领取时推后的分值上，租期一到会被重新领到，不会丢。
func (s *SessionExpiry) Sweep(ctx context.Context) (int, error) {
	sent := 0
	policies := map[uuid.UUID]*domain.SessionPolicy{}
	for {
		now := s.opts.Now()
		members, err := s.store.ClaimExpired(ctx, now, s.opts.Batch, now+s.opts.Lease.Milliseconds())
		if err != nil {
			return sent, err
		}
		for _, m := range members {
			expired, err := s.settle(ctx, m, now, policies)
			if err != nil {
				return sent, err
			}
			if expired {
				sent++
			}
		}
		if len(members) < s.opts.Batch {
			return sent, nil
		}
	}
}

// settle 复核一个到点的成员：会话其实延期了就按新的到期时刻放回；纪元已变就补 revoked 并删掉；
// 确实过期（或会话 key 已被 TTL 清掉）就发事件后摘除。
func (s *SessionExpiry) settle(ctx context.Context, member string, now int64, policies map[uuid.UUID]*domain.SessionPolicy) (bool, error) {
	appID, userID, sessionID, clientRef, ok := store.ParseExpiryMember(member)
	if !ok {
		return false, s.store.UntrackExpiry(ctx, member)
	}
	sess, err := s.store.FindSession(ctx, userID, sessionID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		// 会话不在了而成员还在才是 TTL 清掉的；撤销删会话时同一事务摘成员，那边已经写过事件。
		if _, tracked, err := s.store.ExpiryScore(ctx, member); err != nil || !tracked {
			return false, err
		}
	case err != nil:
		return false, err
	default:
		// 纪元类撤销递增了纪元、却没写完事件就中断时，会话留在这里。客户端不再校验就没人发现，在这里补上。
		epoch, err := s.epochs.Current(ctx, userID)
		if err != nil {
			return false, err
		}
		if epoch != sess.Epoch {
			return true, s.dropRevoked(ctx, appID, userID, sessionID, clientRef)
		}
		p, err := s.policy(ctx, appID, policies)
		if err != nil {
			return false, err
		}
		if p != nil {
			if exp := SessionExpiresAt(sess, *p); exp > now {
				return false, s.store.TrackExpiry(ctx, member, exp)
			}
		}
	}
	// 先写事件再摘：写失败就留着，租期过后重来。
	if err := s.events.Append(ctx, appID, userID, domain.EventLoggedOut, LoggedOutPayload{
		SessionID: sessionID, ClientRef: clientRef, Reason: LoggedOutExpired,
	}); err != nil {
		return false, err
	}
	return true, s.store.UntrackExpiry(ctx, member)
}

// dropRevoked 给纪元失配的会话补 logged_out(revoked)，再删掉它的全部 token（轮换过渡期内有新旧两个）。
// 删会话会顺带摘掉成员；写事件失败就什么都不删，租期过后重来。
func (s *SessionExpiry) dropRevoked(ctx context.Context, appID, userID uuid.UUID, sessionID, clientRef string) error {
	if err := s.events.Append(ctx, appID, userID, domain.EventLoggedOut, LoggedOutPayload{
		SessionID: sessionID, ClientRef: clientRef, Reason: domain.LoggedOutRevoked,
	}); err != nil {
		return err
	}
	for {
		sess, err := s.store.FindSession(ctx, userID, sessionID)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.store.Delete(ctx, sess.Token); err != nil {
			return err
		}
	}
}

// policy 取应用的会话策略，一轮内按应用缓存。应用已删除时返回 nil：它的会话已经校验不过，按过期处理。
func (s *SessionExpiry) policy(ctx context.Context, appID uuid.UUID, cache map[uuid.UUID]*domain.SessionPolicy) (*domain.SessionPolicy, error) {
	if p, ok := cache[appID]; ok {
		return p, nil
	}
	app, err := s.apps.GetByID(ctx, appID)
	if errors.Is(err, domain.ErrNotFound) {
		cache[appID] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cache[appID] = &app.Session
	return &app.Session, nil
}
