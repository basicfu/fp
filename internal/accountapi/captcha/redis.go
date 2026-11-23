package captcha

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	mrand "math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/accountapi/captcha/gen"
)

const (
	usedKeyPrefix = "fp:acct:captcha:used:"
	secretKey     = "fp:acct:captcha:secret"
)

// RedisUsed 记烧过的票。过期由 Redis TTL 清,不需要 PurgeCaptchaUsed。
type RedisUsed struct{ C redis.Cmdable }

func (u RedisUsed) MarkCaptchaUsed(ctx context.Context, idHash []byte, expMs int64) (bool, error) {
	ttl := time.Until(time.UnixMilli(expMs))
	if ttl < time.Second {
		ttl = time.Second
	}
	return u.C.SetNX(ctx, usedKeyPrefix+hex.EncodeToString(idHash), 1, ttl).Result()
}

func (RedisUsed) PurgeCaptchaUsed(context.Context, int64) error { return nil }

// SharedSecret 取全部实例共用的票据密钥,没有就生成一份。fp 没有可派生的部署级密钥,
// 而票据要能在 A 实例出题、B 实例校验,所以放 Redis;Redis 被清空只会让在途的题作废。
func SharedSecret(ctx context.Context, c redis.Cmdable) (string, error) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	if err := c.SetNX(ctx, secretKey, hex.EncodeToString(b[:]), 0).Err(); err != nil {
		return "", fmt.Errorf("captcha: 写入票据密钥: %w", err)
	}
	s, err := c.Get(ctx, secretKey).Result()
	if err != nil {
		return "", fmt.Errorf("captcha: 读取票据密钥: %w", err)
	}
	return s, nil
}

// NewService 装配生产用的出题服务;调用方负责 go Pool.Run。
func NewService(ctx context.Context, c redis.Cmdable) (*Service, error) {
	secret, err := SharedSecret(ctx, c)
	if err != nil {
		return nil, err
	}
	s, err := NewSealer(secret)
	if err != nil {
		return nil, err
	}
	return &Service{Pool: NewPool(gen.NumStyles(), newPuzzleGen()), Sealer: s, Used: RedisUsed{C: c}, Now: time.Now,
		Secret: func(ctx context.Context) (string, error) { return SharedSecret(ctx, c) }}, nil
}

// newPuzzleGen 只在 Pool.Run 那一个协程里调,rand 不用加锁。
func newPuzzleGen() func(style int) (gen.Puzzle, error) {
	var seed [32]byte
	_, _ = rand.Read(seed[:])
	r := mrand.New(mrand.NewChaCha8(seed))
	return func(style int) (gen.Puzzle, error) { return gen.Generate(r, style) }
}
