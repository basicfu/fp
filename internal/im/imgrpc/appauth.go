package imgrpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/im/auth"
)

// 与 fp 的 grpcapi 使用相同的 metadata 键，业务方的 SDK 配置同一对
// app_id/app_secret 就能同时连 fp 和 fp-im。
const (
	MDAppID     = "fp-app-id"
	MDAppSecret = "fp-app-secret"
)

type appCtxKey struct{}

func appFrom(ctx context.Context) string {
	v, _ := ctx.Value(appCtxKey{}).(string)
	return v
}

// CredentialVerifier 是 imgrpc 对"核实业务 server 凭据"的全部依赖。
// *fpauth.Authenticator 满足它。
type CredentialVerifier interface {
	VerifyAppCredential(ctx context.Context, app, secret string) error
}

// DefaultCredCacheTTL 是凭据校验结果的缓存时长，与 fp 侧应用凭据缓存的
// 默认值一致。
const DefaultCredCacheTTL = 5 * time.Minute

// maxCredCacheEntries 是触发清理过期项的阈值。正常情况下缓存大小等于接入的
// 应用数，远低于这个值。纯属卫生措施，不是安全边界——安全边界是"不缓存失败"。
const maxCredCacheEntries = 256

// credCache 给凭据校验加一层**只缓存成功**的缓存。
//
// 校验从"比对本地明文"变成了"转给 fp"（fp-im 不再持有任何应用的 secret，
// 而 fp 只存 bcrypt 哈希），于是 fp 不可达时业务 server 接不进来——这是本
// 改动新增的依赖。缓存把 fp 的短暂抖动挡在外面：业务 server 重连时命中缓存
// 即可，不必等 fp 恢复。
//
// **只缓存成功。** 键的一半（secret）来自调用方，缓存失败结果等于把这个
// map 的大小交给攻击者：一百万个不同的错误 secret 就能把进程 OOM 掉。只收
// 成功结果的话，键的数量被真实应用数钉死。与 fp 的 appVerifier 是同一段
// 推理，那边的注释写得更全。
//
// 键里存 secret 的 SHA-256 而不是明文：明文常驻堆内存的话，崩溃 dump、
// 调试器、内存分析都能读到。键里同时含 app 与哈希，所以"拿 A 的正确 secret
// 冒充 B"不会命中缓存。
type credCache struct {
	v   CredentialVerifier
	ttl time.Duration

	mu    sync.RWMutex
	cache map[string]int64 // app + ":" + sha256(secret) → 过期时刻（UnixMilli）
}

func newCredCache(v CredentialVerifier, ttl time.Duration) *credCache {
	if ttl <= 0 {
		ttl = DefaultCredCacheTTL
	}
	return &credCache{v: v, ttl: ttl, cache: make(map[string]int64)}
}

func credCacheKey(app, secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return app + ":" + hex.EncodeToString(sum[:])
}

func (c *credCache) verify(ctx context.Context, app, secret string) error {
	if app == "" || secret == "" {
		return status.Error(codes.Unauthenticated, "应用凭据无效")
	}
	key := credCacheKey(app, secret)
	now := time.Now().UnixMilli()

	c.mu.RLock()
	expiresAt, ok := c.cache[key]
	c.mu.RUnlock()
	if ok && expiresAt > now {
		return nil
	}

	if err := c.v.VerifyAppCredential(ctx, app, secret); err != nil {
		return err // 失败不入缓存
	}

	c.mu.Lock()
	if len(c.cache) >= maxCredCacheEntries {
		for k, exp := range c.cache {
			if exp <= now {
				delete(c.cache, k)
			}
		}
	}
	c.cache[key] = now + c.ttl.Milliseconds()
	c.mu.Unlock()
	return nil
}

// streamAuth 校验业务 server 连入时递上来的凭据。
//
// 每条流一次，不是每条消息一次——业务 server 通常一个进程一条长流，所以这
// 条回源极其稀疏。
func streamAuth(c *credCache) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		md, _ := metadata.FromIncomingContext(ss.Context())
		id, secret := first(md, MDAppID), first(md, MDAppSecret)
		if err := c.verify(ss.Context(), id, secret); err != nil {
			// "凭据有效但这个应用没开 IM 接入"要单独告诉调用方：它已经证明
			// 自己持有那份 appSecret，所以说真话不泄露任何东西；压成"凭据无效"
			// 会让运维去查 secret，而实际要做的是去控制台翻一个开关。
			if errors.Is(err, auth.ErrIMNotEnabled) {
				return status.Error(codes.FailedPrecondition, "该应用未在 fp 控制台启用 IM 接入")
			}
			// 其余一律不区分："凭据不对"的细节会泄露 appId 是否存在，而
			// "fp 不可达"对业务 server SDK 而言同样是"这次连不上，重试"。
			return status.Error(codes.Unauthenticated, "应用凭据无效")
		}
		return handler(srv, &wrapped{ServerStream: ss, ctx: context.WithValue(ss.Context(), appCtxKey{}, id)})
	}
}

func first(md metadata.MD, k string) string {
	if v := md.Get(k); len(v) > 0 {
		return v[0]
	}
	return ""
}

type wrapped struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrapped) Context() context.Context { return w.ctx }
