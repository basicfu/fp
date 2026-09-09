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

// MDCallerType 是声明调用方类型的 metadata 键。
//
// **它是权威声明，不是提示**：值决定**只**比对哪一份凭据，不会"先试一个
// 失败再试另一个"。这在安全上零损失——声明本身不授予任何东西，声明 im
// 却没有 IM secret 一样失败——却把最坏情况从两次 bcrypt（各 50-100ms）
// 砍到一次。
//
// 空值（不带这个键）就是既有行为，已接入的 SDK 无需任何改动。
const MDCallerType = "fp-caller-type"

// CallerTypeIM 表示调用方是 fp-im 网关，凭据是 IM secret 而不是某个应用的
// appSecret。
const CallerTypeIM = "im"

// AppAuthenticator 是拦截器需要的最小能力。*service.ApplicationService 满足它。
type AppAuthenticator interface {
	VerifySecret(ctx context.Context, appID, plainSecret string) (*domain.Application, error)
}

// IMCredentialVerifier 是拦截器对 IM 凭据的全部依赖。
// *service.IMCredentialService 满足它。
type IMCredentialVerifier interface {
	Verify(ctx context.Context, plainSecret string) error
}

type appIDCtxKey struct{}

type callerTypeCtxKey struct{}

// callerTypeFrom 返回本次调用已认证的调用方类型，空串表示普通应用。
func callerTypeFrom(ctx context.Context) string {
	v, _ := ctx.Value(callerTypeCtxKey{}).(string)
	return v
}

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

	// imCreds 与 imTTL 是 IM 凭据那一路，与上面的应用凭据完全独立。
	// 缓存退化成"一份哈希 + 一个过期时刻"：IM 凭据全库只有一条，连 map
	// 都不需要。
	imCreds IMCredentialVerifier
	imTTL   time.Duration
	imMu    sync.RWMutex
	imHash  string // 已验证通过的 secret 的 SHA-256，空表示无缓存
	imExp   int64
}

// DefaultIMSecretCacheTTL 是 IM 凭据校验结果的缓存时长。
//
// 刻意远短于应用凭据的 5 分钟（Deps.AppSecretCacheTTL）：IM 凭据全库只有
// 一条，bcrypt 每 10 秒一次的开销可以忽略，而换来的是"轮换后旧 secret 最多
// 再活 10 秒"。
//
// 为什么不做成"轮换时主动清缓存"：fp 可以多实例部署，主动清只清得掉本
// 实例的，别的实例照样留满 TTL——要做对就得再走一遍 Redis 广播，为一条
// 凭据引入一整条中继不划算。短 TTL 零新增管道、跨实例天然一致。
const DefaultIMSecretCacheTTL = 10 * time.Second

// newAppVerifier 构造 appVerifier。
func newAppVerifier(apps AppAuthenticator, ttl time.Duration, imCreds IMCredentialVerifier, imTTL time.Duration) *appVerifier {
	return &appVerifier{apps: apps, ttl: ttl, cache: make(map[string]int64), imCreds: imCreds, imTTL: imTTL}
}

// maxCacheEntries 是触发清理过期项的阈值。
//
// 正常情况下缓存大小等于应用数，远低于这个值；能长到这里说明有大量
// appSecret 轮换残留。纯属卫生措施，不是安全边界——安全边界是"不缓存失败"。
const maxCacheEntries = 256

func (v *appVerifier) verify(ctx context.Context, appID, secret string) error {
	if appID == "" || secret == "" {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "缺少 appId 或 appSecret")
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

// verifyIM 校验 IM 凭据，缓存成功结果。
//
// 与应用凭据同一纪律：**只缓存成功**。这里的键空间只有一个（IM 凭据全库
// 一条），所以缓存退化成一份哈希加一个过期时刻。
//
// 存 secret 的 SHA-256 而不是明文：明文常驻堆内存的话，崩溃 dump、调试器、
// 内存分析都能读到。
func (v *appVerifier) verifyIM(ctx context.Context, secret string) error {
	if secret == "" {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "缺少 IM 凭据")
	}
	sum := sha256.Sum256([]byte(secret))
	h := hex.EncodeToString(sum[:])
	now := time.Now().UnixMilli()

	v.imMu.RLock()
	hit := v.imHash == h && v.imExp > now
	v.imMu.RUnlock()
	if hit {
		return nil
	}
	if err := v.imCreds.Verify(ctx, secret); err != nil {
		return err // 失败不入缓存
	}
	v.imMu.Lock()
	v.imHash, v.imExp = h, now+v.imTTL.Milliseconds()
	v.imMu.Unlock()
	return nil
}

// authenticate 从 metadata 取凭据、按调用方类型校验，并把 appId 与
// callerType 放进 ctx。
func (v *appVerifier) authenticate(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "缺少 appId 或 appSecret")
	}
	appID := first(md, mdAppID)
	secret := first(md, mdAppSecret)

	switch callerType := first(md, MDCallerType); callerType {
	case "":
		if err := v.verify(ctx, appID, secret); err != nil {
			return nil, err
		}
		return context.WithValue(ctx, appIDCtxKey{}, appID), nil
	case CallerTypeIM:
		// **不要求 appId。** im 调用方的连接级凭据里没有它——作用域由每次
		// 调用从 client 的 ws 握手帧取、用 WithAppID 附上。而 Watch 这条流是
		// SDK 在 New 里自己起的，压根没有作用域可言：它对 im 调用方就是
		// 通配的（一条流服务所有应用）。
		//
		// "这个 RPC 需不需要 app 作用域"是每个 RPC 自己的事，由它们各自
		// 用 appIDFrom 判断；拦截器一刀切会把 Watch 直接掐死，而那正是
		// fp-im 收撤销事件的唯一通道——掐掉之后被踢下线的用户 ws 会一直
		// 挂到空闲超时，零报错。
		if err := v.verifyIM(ctx, secret); err != nil {
			return nil, err
		}
		authed := context.WithValue(ctx, appIDCtxKey{}, appID)
		return context.WithValue(authed, callerTypeCtxKey{}, CallerTypeIM), nil
	default:
		return nil, domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid,
			"未知的调用方类型 %q", callerType)
	}
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
		return nil, StatusFrom(err)
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
		return StatusFrom(err)
	}
	return handler(srv, &authedStream{ServerStream: ss, ctx: authed})
}

// authedStream 用带 appId 的 ctx 覆盖流的 Context()。
type authedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authedStream) Context() context.Context { return s.ctx }
