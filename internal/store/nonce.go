package store

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// devsigNoncePrefix 是设备头 nonce 去重的键前缀：fp:devsig:nonce:{sign_pub_hex}:{nonce}。
const devsigNoncePrefix = "fp:devsig:nonce:"

// NonceStore 记住时间窗内用过的 (设备公钥, nonce)，挡住设备头重放。
type NonceStore struct {
	rdb *redis.Client
}

// NewNonceStore 构造 NonceStore。
func NewNonceStore(rdb *redis.Client) *NonceStore {
	return &NonceStore{rdb: rdb}
}

// Use 登记一次使用。fresh 为 false 表示 ttl 内已经用过。
// ttl 要不短于签名时间窗的两倍：窗口两端的同一个 nonce 都得被认出来。
func (s *NonceStore) Use(ctx context.Context, signPubHex, nonce string, ttl time.Duration) (bool, error) {
	ok, err := s.rdb.SetNX(ctx, devsigNoncePrefix+signPubHex+":"+nonce, 1, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("store: 登记设备 nonce: %w", err)
	}
	return ok, nil
}
