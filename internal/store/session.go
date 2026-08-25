package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
)

// Redis 键前缀。
//
//	fp:sess:{token}   → 会话 JSON，TTL 与 IdleExpiresAt 保持一致（权威数据）
//	fp:usess:{userID} → 该用户的 token 集合（超集，用于在线设备与批量撤销）
const (
	sessionKeyPrefix   = "fp:sess:"
	userSessionsPrefix = "fp:usess:"
	sessionLockPrefix  = "fp:lock:"
)

// SessionStore 用 Redis 存放会话。
//
// 会话只存 Redis、不落 PostgreSQL：这沿用了 3s 的成熟做法，
// 代价是 Redis 数据丢失等同全体重新登录——这是 3s 现在就接受的风险。
type SessionStore struct {
	rdb *redis.Client
}

// NewSessionStore 构造 SessionStore。
func NewSessionStore(rdb *redis.Client) *SessionStore {
	return &SessionStore{rdb: rdb}
}

// Put 写入会话，并把 token 记进该用户的索引。
//
// 索引的 TTL 取会话 TTL 与 7 天中较大者：索引是超集，允许含已过期的 token，
// ListUserTokens 会在读取时清理，因此索引过期得晚一些是安全的。
func (s *SessionStore) Put(ctx context.Context, sess *domain.Session, ttl time.Duration) error {
	raw, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("store: 序列化会话: %w", err)
	}

	indexTTL := ttl
	if indexTTL < 7*24*time.Hour {
		indexTTL = 7 * 24 * time.Hour
	}

	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, sessionKeyPrefix+sess.Token, raw, ttl)
	pipe.SAdd(ctx, userSessionsPrefix+sess.UserID.String(), sess.Token)
	pipe.Expire(ctx, userSessionsPrefix+sess.UserID.String(), indexTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("store: 写入会话: %w", err)
	}
	return nil
}

// Get 按 token 读会话。不存在或已过期返回 domain.ErrNotFound。
func (s *SessionStore) Get(ctx context.Context, token string) (*domain.Session, error) {
	raw, err := s.rdb.Get(ctx, sessionKeyPrefix+token).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, domain.Errorf(domain.ErrNotFound, "会话不存在或已过期")
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取会话: %w", err)
	}
	var sess domain.Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("store: 解析会话: %w", err)
	}
	return &sess, nil
}

// Delete 删除一个会话。token 不存在时返回 nil。
func (s *SessionStore) Delete(ctx context.Context, token string) error {
	// 先读出会话拿到 userID，才能同步清理索引。读不到就只删主键。
	sess, err := s.Get(ctx, token)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}

	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, sessionKeyPrefix+token)
	if sess != nil {
		pipe.SRem(ctx, userSessionsPrefix+sess.UserID.String(), token)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("store: 删除会话: %w", err)
	}
	return nil
}

// Expire 重设会话主键的 TTL。用于延期。
func (s *SessionStore) Expire(ctx context.Context, token string, ttl time.Duration) error {
	if err := s.rdb.Expire(ctx, sessionKeyPrefix+token, ttl).Err(); err != nil {
		return fmt.Errorf("store: 延长会话 TTL: %w", err)
	}
	return nil
}

// ListUserTokens 返回该用户当前仍然存活的 token，并顺手把索引里的悬挂项移除。
func (s *SessionStore) ListUserTokens(ctx context.Context, userID uuid.UUID) ([]string, error) {
	indexKey := userSessionsPrefix + userID.String()
	tokens, err := s.rdb.SMembers(ctx, indexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("store: 读取用户会话索引: %w", err)
	}
	if len(tokens) == 0 {
		return []string{}, nil
	}

	keys := make([]string, len(tokens))
	for i, tok := range tokens {
		keys[i] = sessionKeyPrefix + tok
	}
	values, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("store: 批量读取会话: %w", err)
	}

	alive := make([]string, 0, len(tokens))
	var dangling []any
	for i, v := range values {
		if v == nil {
			dangling = append(dangling, tokens[i])
			continue
		}
		alive = append(alive, tokens[i])
	}
	if len(dangling) > 0 {
		if err := s.rdb.SRem(ctx, indexKey, dangling...).Err(); err != nil {
			return nil, fmt.Errorf("store: 清理悬挂 token: %w", err)
		}
	}
	return alive, nil
}

// DeleteUserTokens 撤销该用户的全部会话，返回实际删除的数量。
func (s *SessionStore) DeleteUserTokens(ctx context.Context, userID uuid.UUID) (int, error) {
	tokens, err := s.ListUserTokens(ctx, userID)
	if err != nil {
		return 0, err
	}
	if len(tokens) == 0 {
		return 0, nil
	}

	keys := make([]string, len(tokens))
	members := make([]any, len(tokens))
	for i, tok := range tokens {
		keys[i] = sessionKeyPrefix + tok
		members[i] = tok
	}

	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, keys...)
	pipe.SRem(ctx, userSessionsPrefix+userID.String(), members...)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("store: 批量删除会话: %w", err)
	}
	return len(tokens), nil
}

// TryLock 尝试获取一把带 TTL 的互斥锁，用于给延期写与轮换去重。
// 锁不需要显式释放，靠 TTL 自然过期即可——它的作用是限流而非临界区保护。
func (s *SessionStore) TryLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	ok, err := s.rdb.SetNX(ctx, sessionLockPrefix+key, 1, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("store: 获取锁: %w", err)
	}
	return ok, nil
}
