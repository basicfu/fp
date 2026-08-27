package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// epochKey 返回用户撤销纪元的 Redis 键。
func epochKey(userID uuid.UUID) string {
	return "fp:epoch:" + userID.String()
}

// EpochStore 维护每个用户的撤销纪元。
//
// 纪元关掉的是「冻结/改密」与「登录签发」之间的竞态：撤销时递增纪元，
// 此前读到旧纪元并据此签发的会话，校验时会因纪元不匹配被拒。
//
// **键不设 TTL。** 设 TTL 会引入一个更糟的故障模式：键过期后，所有携带
// 非零纪元的合法会话都会与 0 比对失败而被集体登出。增长量是
// 「曾被撤销过的用户数 × 一个几十字节的键」，可以忽略。
type EpochStore struct {
	rdb *redis.Client
}

// NewEpochStore 构造 EpochStore。
func NewEpochStore(rdb *redis.Client) *EpochStore {
	return &EpochStore{rdb: rdb}
}

// Current 返回用户当前的纪元。从未被撤销过的用户返回 0。
func (s *EpochStore) Current(ctx context.Context, userID uuid.UUID) (int64, error) {
	v, err := s.rdb.Get(ctx, epochKey(userID)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: 读取用户纪元: %w", err)
	}
	return v, nil
}

// Bump 递增用户纪元并返回新值。
func (s *EpochStore) Bump(ctx context.Context, userID uuid.UUID) (int64, error) {
	v, err := s.rdb.Incr(ctx, epochKey(userID)).Result()
	if err != nil {
		return 0, fmt.Errorf("store: 递增用户纪元: %w", err)
	}
	return v, nil
}
