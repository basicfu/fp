package grpcapi

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
)

// countingApps 是 AppAuthenticator 的计数桩。
type countingApps struct {
	calls  atomic.Int32
	appID  string
	secret string
}

func (c *countingApps) VerifySecret(_ context.Context, appID, secret string) (*domain.Application, error) {
	c.calls.Add(1)
	if appID != c.appID || secret != c.secret {
		return nil, domain.Errorf(domain.ErrInvalidCredential, "appId 或 appSecret 不正确")
	}
	return &domain.Application{AppID: appID}, nil
}

func ctxWith(appID, secret string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		mdAppID, appID,
		mdAppSecret, secret,
	))
}

func newTestVerifier() (*appVerifier, *countingApps) {
	apps := &countingApps{appID: "app-1", secret: "s3cret"}
	return newAppVerifier(apps, 5*time.Minute), apps
}

func TestUnaryRejectsMissingMetadata(t *testing.T) {
	v, _ := newTestVerifier()
	_, err := v.UnaryInterceptor(context.Background(), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("无 metadata 时返回 %v，期望 Unauthenticated", status.Code(err))
	}
}

func TestUnaryRejectsWrongSecret(t *testing.T) {
	v, _ := newTestVerifier()
	_, err := v.UnaryInterceptor(ctxWith("app-1", "wrong"), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("错误 secret 返回 %v，期望 Unauthenticated", status.Code(err))
	}
}

func TestUnaryInjectsAppID(t *testing.T) {
	v, _ := newTestVerifier()
	var seen string
	_, err := v.UnaryInterceptor(ctxWith("app-1", "s3cret"), nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, _ any) (any, error) {
			seen, _ = appIDFrom(ctx)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("合法凭据被拒: %v", err)
	}
	if seen != "app-1" {
		t.Fatalf("handler 看到的 appId 是 %q，期望 app-1", seen)
	}
}

// TestVerificationIsCached 是本任务存在的全部理由。
//
// VerifySecret 内部是 bcrypt，单次 50–100ms。回源目标量级是数百 QPS——
// 不缓存的话每个 ValidateToken 都要付一次 bcrypt，吞吐直接归零，
// 而且不会报任何错，只是慢到不可用。
func TestVerificationIsCached(t *testing.T) {
	v, apps := newTestVerifier()
	pass := func(ctx context.Context, _ any) (any, error) { return nil, nil }

	for i := 0; i < 5; i++ {
		if _, err := v.UnaryInterceptor(ctxWith("app-1", "s3cret"), nil, &grpc.UnaryServerInfo{}, pass); err != nil {
			t.Fatalf("第 %d 次调用被拒: %v", i, err)
		}
	}
	if got := apps.calls.Load(); got != 1 {
		t.Fatalf("5 次调用触发了 %d 次 bcrypt 校验，期望 1 次", got)
	}
}

// TestFailedVerificationIsNotCached 守住一条内存安全性质。
//
// 缓存键的一半（secret 的哈希）由调用方控制。缓存失败结果的话，
// 攻击者发一百万个不同的错误 secret 就能让这个 map 无界膨胀，把 fp OOM 掉。
// 只缓存成功，键的数量就被"真实存在的应用数"钉死——因为只有正确的
// secret 才进得来。
//
// 断言写成"底层被调用了 N 次"：失败若进了缓存，第二次起就不会再打到底层。
func TestFailedVerificationIsNotCached(t *testing.T) {
	v, apps := newTestVerifier()
	pass := func(ctx context.Context, _ any) (any, error) { return nil, nil }

	for i := 0; i < 3; i++ {
		if _, err := v.UnaryInterceptor(ctxWith("app-1", "wrong"), nil, &grpc.UnaryServerInfo{}, pass); err == nil {
			t.Fatal("错误 secret 被放行")
		}
	}
	if got := apps.calls.Load(); got != 3 {
		t.Fatalf("3 次错误凭据只打到底层 %d 次——失败被缓存了，"+
			"缓存键可被攻击者任意撑大", got)
	}
}

// TestCacheExpires 确认 TTL 真的生效，appSecret 轮换后旧值不会永远有效。
func TestCacheExpires(t *testing.T) {
	apps := &countingApps{appID: "app-1", secret: "s3cret"}
	v := newAppVerifier(apps, time.Millisecond)
	pass := func(ctx context.Context, _ any) (any, error) { return nil, nil }

	if _, err := v.UnaryInterceptor(ctxWith("app-1", "s3cret"), nil, &grpc.UnaryServerInfo{}, pass); err != nil {
		t.Fatalf("首次: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := v.UnaryInterceptor(ctxWith("app-1", "s3cret"), nil, &grpc.UnaryServerInfo{}, pass); err != nil {
		t.Fatalf("过期后: %v", err)
	}
	if got := apps.calls.Load(); got != 2 {
		t.Fatalf("TTL 过期后仍走缓存，底层只被调用了 %d 次", got)
	}
}

// TestCacheKeyDoesNotContainPlaintextSecret 守住"内存里不留明文凭据"。
//
// 直接拿 secret 当键是最省事的写法，也是最容易过审的——功能完全正确。
// 代价是 appSecret 明文常驻堆内存，崩溃 dump、调试器、内存分析工具都能读到。
func TestCacheKeyDoesNotContainPlaintextSecret(t *testing.T) {
	const secret = "非常独特的明文密钥-9f3a"
	if k := verifierCacheKey("app-1", secret); strings.Contains(k, secret) {
		t.Fatalf("缓存键里含明文 secret: %q", k)
	}
}

// TestStreamInterceptorAlsoInjectsAppID 守住"流没被漏掉"。
//
// 实现一元拦截器时很容易忘了流也要一份——而 Watch 是流。漏掉的表现是
// Watch 完全不做认证：任何人连上 :9090 就能订阅到全部应用的撤销事件，
// 拿到实时的 token 列表。这是本任务里后果最严重的一个疏漏。
func TestStreamInterceptorAlsoInjectsAppID(t *testing.T) {
	v, _ := newTestVerifier()

	var seen string
	err := v.StreamInterceptor(nil, &fakeServerStream{ctx: ctxWith("app-1", "s3cret")},
		&grpc.StreamServerInfo{}, func(_ any, ss grpc.ServerStream) error {
			seen, _ = appIDFrom(ss.Context())
			return nil
		})
	if err != nil {
		t.Fatalf("合法凭据的流被拒: %v", err)
	}
	if seen != "app-1" {
		t.Fatalf("流 handler 看到的 appId 是 %q，期望 app-1", seen)
	}

	err = v.StreamInterceptor(nil, &fakeServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{}, func(any, grpc.ServerStream) error { return nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("无凭据的流返回 %v，期望 Unauthenticated", status.Code(err))
	}
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }
