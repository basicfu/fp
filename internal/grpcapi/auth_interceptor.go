package grpcapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/basicfu/fp/internal/domain"
)

// metadata 里携带应用凭据的键。gRPC 要求全部小写。
const (
	mdAppID     = "fp-app-id"
	mdAppSecret = "fp-app-secret"
)

// AppAuthenticator 是拦截器需要的最小能力。*service.ApplicationService 满足它。
type AppAuthenticator interface {
	VerifySecret(ctx context.Context, appID, plainSecret string) (*domain.Application, error)
}

type appIDCtxKey struct{}

// appIDFrom 返回本次调用已认证的 appId。
func appIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(appIDCtxKey{}).(string)
	return v, ok
}

// verifierCacheKey 是凭据缓存的键。
//
// 用 secret 的 SHA-256 而不是 secret 本身：直接拿明文当键功能上完全正确，
// 代价是 appSecret 明文常驻堆内存，崩溃 dump / 调试器 / 内存分析都能读到。
// 键里同时含 appID 与哈希，所以"拿 A 的正确 secret 冒充 B"不会命中缓存。
func verifierCacheKey(appID, secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return appID + ":" + hex.EncodeToString(sum[:])
}

// appVerifier 校验 appId/appSecret，并缓存**成功**的校验结果。
//
// 缓存是必需品不是优化：VerifySecret 内部是 bcrypt，单次 50–100ms，
// 而 ValidateToken 的目标量级是数百 QPS。不缓存的话吞吐直接归零，
// 且不会报任何错——只是慢到不可用。
//
// **只缓存成功。** 缓存键的一半来自调用方，缓存失败结果等于把这个 map
// 的大小交给攻击者：一百万个不同的错误 secret 就能把进程 OOM 掉。
// 只收成功结果的话，键的数量被真实应用数钉死。
//
// 缓存里**不存 *domain.Application**，只存"这对凭据有效"这个事实：
// 缓存一份 Status 快照会让"停用应用"在 TTL 内形同虚设。应用是否可用
// 由 AuthService.activeApp 每次重新判定，保持单一执行点。
type appVerifier struct {
	apps AppAuthenticator
	ttl  time.Duration

	mu    sync.RWMutex
	cache map[string]int64 // key → 过期时刻（UnixMilli）
}

// newAppVerifier 构造 appVerifier。
func newAppVerifier(apps AppAuthenticator, ttl time.Duration) *appVerifier {
	return &appVerifier{apps: apps, ttl: ttl, cache: make(map[string]int64)}
}

// maxCacheEntries 是触发清理过期项的阈值。
//
// 正常情况下缓存大小等于应用数，远低于这个值；能长到这里说明有大量
// appSecret 轮换残留。纯属卫生措施，不是安全边界——安全边界是"不缓存失败"。
const maxCacheEntries = 256

func (v *appVerifier) verify(ctx context.Context, appID, secret string) error {
	if appID == "" || secret == "" {
		return domain.Errorf(domain.ErrInvalidCredential, "缺少 appId 或 appSecret")
	}

	key := verifierCacheKey(appID, secret)
	now := time.Now().UnixMilli()

	v.mu.RLock()
	expiresAt, ok := v.cache[key]
	v.mu.RUnlock()
	if ok && expiresAt > now {
		return nil
	}

	if _, err := v.apps.VerifySecret(ctx, appID, secret); err != nil {
		return err // 失败不入缓存
	}

	v.mu.Lock()
	if len(v.cache) >= maxCacheEntries {
		for k, exp := range v.cache {
			if exp <= now {
				delete(v.cache, k)
			}
		}
	}
	v.cache[key] = now + v.ttl.Milliseconds()
	v.mu.Unlock()
	return nil
}

// authenticate 从 metadata 取凭据、校验，并把 appId 放进 ctx。
func (v *appVerifier) authenticate(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, domain.Errorf(domain.ErrInvalidCredential, "缺少 appId 或 appSecret")
	}
	appID := first(md, mdAppID)
	secret := first(md, mdAppSecret)
	if err := v.verify(ctx, appID, secret); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, appIDCtxKey{}, appID), nil
}

func first(md metadata.MD, key string) string {
	if vs := md.Get(key); len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// UnaryInterceptor 校验一元调用的应用凭据。
func (v *appVerifier) UnaryInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	authed, err := v.authenticate(ctx)
	if err != nil {
		return nil, statusFrom(err)
	}
	return handler(authed, req)
}

// StreamInterceptor 校验流式调用的应用凭据。
//
// 一元和流必须都拦。只拦一元的话 Watch 完全不做认证——任何人连上 :9090
// 就能订阅到全部应用的撤销事件，实时拿到 token 列表。
func (v *appVerifier) StreamInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	authed, err := v.authenticate(ss.Context())
	if err != nil {
		return statusFrom(err)
	}
	return handler(srv, &authedStream{ServerStream: ss, ctx: authed})
}

// authedStream 用带 appId 的 ctx 覆盖流的 Context()。
type authedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authedStream) Context() context.Context { return s.ctx }
