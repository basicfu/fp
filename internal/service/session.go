package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
)

// SessionService 负责令牌的签发与校验。
//
// 设计文档 4.5.1 的核心约束在 Validate 里：**返回 cache_ttl 而非 expiry**。
// SDK 只按 cache_ttl 缓存判定结果，永远不自己推断 token 是否过期，
// 因此不存在「一处延期、另一处按旧 expiry 误判」的竞态。
type SessionService struct {
	store *store.SessionStore
	// now 返回当前毫秒时间戳。可注入，便于测试过期与轮换边界。
	now func() int64
}

// NewSessionService 构造使用真实时钟的 SessionService。
func NewSessionService(st *store.SessionStore) *SessionService {
	return NewSessionServiceWithClock(st, func() int64 { return time.Now().UnixMilli() })
}

// NewSessionServiceWithClock 构造使用自定义时钟的 SessionService。
func NewSessionServiceWithClock(st *store.SessionStore, now func() int64) *SessionService {
	return &SessionService{store: st, now: now}
}

// IssueInput 描述一次签发请求。
type IssueInput struct {
	UserID uuid.UUID
	App    *domain.Application
	IP     string
	UA     string
	// Mobile 决定适用哪一档空闲超时。
	Mobile bool
}

// Issue 为一次成功的认证签发新会话。
func (s *SessionService) Issue(ctx context.Context, in IssueInput) (*domain.Session, error) {
	if in.App == nil {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "签发会话缺少应用信息")
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}

	now := s.now()
	idle := in.App.Session.IdleTimeoutFor(in.Mobile)

	sess := &domain.Session{
		ID:             uuid.NewString(),
		Token:          token,
		UserID:         in.UserID,
		AppID:          in.App.ID,
		FirstAuthAt:    now,
		IssuedAt:       now,
		LastExtendedAt: now,
		IdleExpiresAt:  now + idle.Milliseconds(),
		IP:             in.IP,
		UA:             in.UA,
		Mobile:         in.Mobile,
	}
	if err := s.store.Put(ctx, sess, idle); err != nil {
		return nil, err
	}
	return sess, nil
}

// ValidateResult 是一次校验的结果。
type ValidateResult struct {
	Session *domain.Session
	// CacheTTL 是 SDK 可以缓存本次判定结果的时长，
	// 等于 min(应用配置的 token_cache_ttl, token 剩余有效期)。
	CacheTTL time.Duration
	// Rotated 为 true 时 NewToken 非空，调用方须把新 token 下发给客户端。
	// 轮换逻辑在 Task 10 接入，本任务恒为 false。
	Rotated  bool
	NewToken string
}

// Validate 校验 token 并给出缓存时长。
//
// 校验失败一律返回 domain.ErrUnauthorized 的包装，不区分「不存在」「已过期」
// 「不属于本应用」——这些差异对调用方没有意义，却会给攻击者提供信息。
func (s *SessionService) Validate(ctx context.Context, token string, app *domain.Application) (*ValidateResult, error) {
	if app == nil {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "校验 token 缺少应用信息")
	}
	if token == "" {
		return nil, domain.Errorf(domain.ErrUnauthorized, "缺少 token")
	}

	sess, err := s.store.Get(ctx, token)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, domain.Errorf(domain.ErrUnauthorized, "token 无效或已过期")
	}
	if err != nil {
		return nil, err
	}

	// token 与应用必须匹配：A 应用签发的 token 不能在 B 应用上使用。
	if sess.AppID != app.ID {
		return nil, domain.Errorf(domain.ErrUnauthorized, "token 无效或已过期")
	}

	now := s.now()
	remaining := sess.RemainingAt(now, app.Session)
	if remaining <= 0 {
		// Redis TTL 是兜底清理，这里以 IdleExpiresAt / MaxExpiresAt 为准，
		// 避免时钟精度或 TTL 取整导致的放行。
		return nil, domain.Errorf(domain.ErrUnauthorized, "token 无效或已过期")
	}

	return &ValidateResult{
		Session:  sess,
		CacheTTL: cacheTTL(remaining, app.Session),
	}, nil
}

// ListByUser 返回该用户当前存活的全部会话，用于在线设备列表。
func (s *SessionService) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.Session, error) {
	tokens, err := s.store.ListUserTokens(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Session, 0, len(tokens))
	for _, tok := range tokens {
		sess, err := s.store.Get(ctx, tok)
		if errors.Is(err, domain.ErrNotFound) {
			// 读取与索引清理之间存在窗口，跳过即可。
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	return out, nil
}

// cacheTTL 计算 SDK 可缓存的时长：不超过应用配置，也不超过 token 剩余有效期。
//
// 后半条是整个方案的安全底线——它保证「token 越接近过期，SDK 回源越频繁」，
// 从而杜绝过期 token 被本地缓存继续放行。
func cacheTTL(remaining time.Duration, p domain.SessionPolicy) time.Duration {
	configured := time.Duration(p.TokenCacheTTLSeconds) * time.Second
	if remaining < configured {
		return remaining
	}
	return configured
}
