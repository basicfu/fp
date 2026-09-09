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
		return nil, domain.Failf(domain.ErrInvalidCredential, domain.CodeCredentialInvalid, "appId 或 appSecret 不正确")
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
	return newAppVerifier(apps, 5*time.Minute, &countingIMCreds{secret: "im-secret"}, 10*time.Second), apps
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
	v := newAppVerifier(apps, time.Millisecond, &countingIMCreds{secret: "im-secret"}, time.Millisecond)
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

// countingIMCreds 是 IMCredentialVerifier 的计数桩。
type countingIMCreds struct {
	calls  atomic.Int32
	secret string
}

func (c *countingIMCreds) Verify(_ context.Context, secret string) error {
	c.calls.Add(1)
	if secret != c.secret {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeAppCredentialInvalid, "IM 凭据无效")
	}
	return nil
}

func newTestVerifierWithIM() (*appVerifier, *countingApps, *countingIMCreds) {
	apps := &countingApps{appID: "app-1", secret: "s3cret"}
	imc := &countingIMCreds{secret: "im-secret"}
	return newAppVerifier(apps, 5*time.Minute, imc, 10*time.Second), apps, imc
}

func ctxWithType(appID, secret, callerType string) context.Context {
	md := metadata.Pairs(mdAppID, appID, mdAppSecret, secret)
	if callerType != "" {
		md.Set(MDCallerType, callerType)
	}
	return metadata.NewIncomingContext(context.Background(), md)
}

// TestCallerTypeIsAuthoritative 钉住这条设计：fp-caller-type 决定**只**比对
// 哪一份凭据，不是"提示先试哪个、失败再试另一个"。
//
// 声明本身不授予任何东西——声明 im 却拿着 app secret 一样失败——所以让它
// 权威在安全上零损失，却把最坏情况从两次 bcrypt（各 50–100ms）砍到一次。
func TestCallerTypeIsAuthoritative(t *testing.T) {
	for _, tc := range []struct {
		name       string
		callerType string
		appID      string
		secret     string
		wantErr    bool
	}{
		{"普通调用用 app secret", "", "app-1", "s3cret", false},
		{"普通调用拿 IM secret", "", "app-1", "im-secret", true},
		{"im 调用用 IM secret", CallerTypeIM, "app-1", "im-secret", false},
		{"im 调用拿 app secret", CallerTypeIM, "app-1", "s3cret", true},
		{"未知的 caller type", "gateway", "app-1", "im-secret", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, _, _ := newTestVerifierWithIM()
			_, err := v.authenticate(ctxWithType(tc.appID, tc.secret, tc.callerType))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v，wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestCallerTypeOnlyOneBcrypt 钉住"只比对一份"：im 调用不能顺带去验
// app secret，反之亦然。两次 bcrypt 各 50–100ms，热路径上翻倍不可接受。
func TestCallerTypeOnlyOneBcrypt(t *testing.T) {
	v, apps, imc := newTestVerifierWithIM()
	if _, err := v.authenticate(ctxWithType("app-1", "im-secret", CallerTypeIM)); err != nil {
		t.Fatal(err)
	}
	if n := apps.calls.Load(); n != 0 {
		t.Errorf("im 调用触碰了应用凭据校验 %d 次，应当为 0", n)
	}
	if n := imc.calls.Load(); n != 1 {
		t.Errorf("IM 凭据校验 %d 次，应当恰好 1 次", n)
	}

	v2, apps2, imc2 := newTestVerifierWithIM()
	if _, err := v2.authenticate(ctxWithType("app-1", "s3cret", "")); err != nil {
		t.Fatal(err)
	}
	if n := imc2.calls.Load(); n != 0 {
		t.Errorf("普通调用触碰了 IM 凭据校验 %d 次，应当为 0", n)
	}
	if n := apps2.calls.Load(); n != 1 {
		t.Errorf("应用凭据校验 %d 次，应当恰好 1 次", n)
	}
}

// TestNoCallerTypeBehavesExactlyAsBefore 钉住"新版 fp 发布后对现网零影响"：
// 不带 fp-caller-type 的调用，从 metadata 解析到 ctx 内容，行为必须与改动前
// 逐字相同。
func TestNoCallerTypeBehavesExactlyAsBefore(t *testing.T) {
	v, _, _ := newTestVerifierWithIM()
	ctx, err := v.authenticate(ctxWith("app-1", "s3cret"))
	if err != nil {
		t.Fatalf("既有调用方式必须原样可用：%v", err)
	}
	if got, ok := appIDFrom(ctx); !ok || got != "app-1" {
		t.Fatalf("appId 未放进 ctx，got %q ok=%v", got, ok)
	}
	if got := callerTypeFrom(ctx); got != "" {
		t.Fatalf("未声明时 callerType 应为空，got %q", got)
	}
}

// TestIMCallerAllowsEmptyAppID：im 调用方**不要求** app-id。
//
// 它的连接级凭据里没有作用域——每次调用从 client 的 ws 握手帧取、用
// WithAppID 附上。而 Watch 那条流是 SDK 在 New 里自己起的，压根没有作用域
// 可言：它对 im 调用方就是通配的。
//
// 拦截器一刀切要求 appId 会把 Watch 直接掐死，而那是 fp-im 收撤销事件的
// 唯一通道——掐掉之后被踢下线的用户 ws 会一直挂到空闲超时，零报错。
func TestIMCallerAllowsEmptyAppID(t *testing.T) {
	v, _, _ := newTestVerifierWithIM()
	md := metadata.Pairs(mdAppSecret, "im-secret", MDCallerType, CallerTypeIM)
	ctx, err := v.authenticate(metadata.NewIncomingContext(context.Background(), md))
	if err != nil {
		t.Fatalf("im 调用不带 fp-app-id 应当通过认证：%v", err)
	}
	if got := callerTypeFrom(ctx); got != CallerTypeIM {
		t.Fatalf("callerType = %q, want im", got)
	}
}

// TestIMSecretCacheOnlyCachesSuccess：与应用凭据同一纪律。
func TestIMSecretCacheOnlyCachesSuccess(t *testing.T) {
	v, _, imc := newTestVerifierWithIM()
	for i := 0; i < 3; i++ {
		if _, err := v.authenticate(ctxWithType("app-1", "im-secret", CallerTypeIM)); err != nil {
			t.Fatal(err)
		}
	}
	if n := imc.calls.Load(); n != 1 {
		t.Fatalf("成功结果回源 %d 次，应当只有 1 次", n)
	}

	imc.calls.Store(0)
	for i := 0; i < 3; i++ {
		if _, err := v.authenticate(ctxWithType("app-1", "wrong", CallerTypeIM)); err == nil {
			t.Fatal("错误的 IM 凭据必须被拒")
		}
	}
	if n := imc.calls.Load(); n != 3 {
		t.Fatalf("失败结果回源 %d 次，失败不缓存意味着每次都要回源", n)
	}
}
