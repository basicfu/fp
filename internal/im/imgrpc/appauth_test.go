package imgrpc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/im/auth"
)

// fakeVerifier 计数并按一张表判定。
type fakeVerifier struct {
	mu    sync.Mutex
	calls int
	ok    map[string]string // app → 正确的 secret
	err   error             // 非 nil 时一律返回它（模拟 fp 不可达）
}

func (f *fakeVerifier) VerifyAppCredential(_ context.Context, app, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	if f.ok[app] != secret {
		return errors.New("凭据无效")
	}
	return nil
}

func (f *fakeVerifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestCredCacheOnlyCachesSuccess：凭据校验从"比对本地明文"变成"转给 fp"，
// 于是 fp 不可达时业务 server 接不进来——这是本改动新增的依赖。缓存把 fp
// 的短暂抖动挡在外面。
//
// **只缓存成功**：键的一半（secret）来自调用方，缓存失败等于把 map 的大小
// 交给攻击者——一百万个不同的错误 secret 就能把进程 OOM 掉。
func TestCredCacheOnlyCachesSuccess(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{"a1": "s1"}}
	c := newCredCache(v, time.Minute)

	for i := 0; i < 3; i++ {
		if err := c.verify(context.Background(), "a1", "s1"); err != nil {
			t.Fatal(err)
		}
	}
	if n := v.count(); n != 1 {
		t.Fatalf("成功结果回源了 %d 次，应当只有 1 次", n)
	}

	v.mu.Lock()
	v.calls = 0
	v.mu.Unlock()
	for i := 0; i < 3; i++ {
		if err := c.verify(context.Background(), "a1", "wrong"); err == nil {
			t.Fatal("错误凭据必须被拒")
		}
	}
	if n := v.count(); n != 3 {
		t.Fatalf("失败结果回源了 %d 次，失败不缓存意味着每次都要回源", n)
	}
}

// TestCredCacheKeyIsAppScoped：拿 A 的正确 secret 冒充 B 不能命中缓存。
func TestCredCacheKeyIsAppScoped(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{"a1": "s1", "a2": "s2"}}
	c := newCredCache(v, time.Minute)

	if err := c.verify(context.Background(), "a1", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := c.verify(context.Background(), "a2", "s1"); err == nil {
		t.Fatal("拿 a1 的 secret 冒充 a2 必须被拒")
	}
}

// TestCredCacheRejectsEmpty：空 app 或空 secret 直接拒，不必惊动 fp。
func TestCredCacheRejectsEmpty(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{"a1": "s1"}}
	c := newCredCache(v, time.Minute)
	if err := c.verify(context.Background(), "", "s1"); err == nil {
		t.Fatal("空 app 必须被拒")
	}
	if err := c.verify(context.Background(), "a1", ""); err == nil {
		t.Fatal("空 secret 必须被拒")
	}
	if n := v.count(); n != 0 {
		t.Fatalf("空凭据不该回源，实际 %d 次", n)
	}
}

// TestCredCacheSurvivesFPOutage：命中缓存时 fp 挂了也不影响业务 server 重连。
// 这正是加这层缓存的理由。
func TestCredCacheSurvivesFPOutage(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{"a1": "s1"}}
	c := newCredCache(v, time.Minute)
	if err := c.verify(context.Background(), "a1", "s1"); err != nil {
		t.Fatal(err)
	}
	v.mu.Lock()
	v.err = errors.New("fp 不可达")
	v.mu.Unlock()

	if err := c.verify(context.Background(), "a1", "s1"); err != nil {
		t.Fatalf("已缓存的凭据在 fp 不可达时仍应放行：%v", err)
	}
	if err := c.verify(context.Background(), "a2", "s2"); err == nil {
		t.Fatal("没缓存过的凭据在 fp 不可达时必须被拒")
	}
}

// TestCredCachePropagatesIMNotEnabled：凭据有效但应用没开 IM 时，错误必须
// 能被上层区分出来。
//
// 压成"凭据无效"会让运维去查 secret，而实际要做的是去控制台翻一个开关——
// fp 侧的 VerifyAppCredential 特意先验凭据再看开关就是为了保住这个区分，
// 在这里压掉等于把那份用心扔了。
func TestCredCachePropagatesIMNotEnabled(t *testing.T) {
	v := &fakeVerifier{ok: map[string]string{}, err: auth.ErrIMNotEnabled}
	c := newCredCache(v, time.Minute)
	err := c.verify(context.Background(), "a1", "s1")
	if !errors.Is(err, auth.ErrIMNotEnabled) {
		t.Fatalf("err = %v，必须能被 errors.Is 认出是 ErrIMNotEnabled", err)
	}
	// 而且这类失败同样不入缓存：开关一打开，下一次连接就该通。
	v.mu.Lock()
	v.err = nil
	v.ok["a1"] = "s1"
	v.mu.Unlock()
	if err := c.verify(context.Background(), "a1", "s1"); err != nil {
		t.Fatalf("开关打开之后应当立刻放行：%v", err)
	}
}

// fakeStream 是 streamAuth 需要的最小 grpc.ServerStream。
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }

// TestStreamAuthStatusCodes 钉住业务 server 从 fp-im 收到的**状态码**。
//
// sdk/README.md 明确教业务方按这两个码分诊：FailedPrecondition 去控制台翻
// 开关，Unauthenticated 才去查凭据 / 查 fp 健康。合并成一个码等于把那段
// 文档变成谎言，而且是最难查的那种——运维会拿着一份完全正确的 secret 找
// 一整天。
//
// 反向也要钉：其余失败**必须**都塌成 Unauthenticated。区分"应用不存在"和
// "secret 不对"会泄露某个 appId 是否存在。
func TestStreamAuthStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verr    error
		want    codes.Code
		wantMsg string
	}{
		{"没开 IM 接入", auth.ErrIMNotEnabled, codes.FailedPrecondition, "该应用未在 fp 控制台启用 IM 接入"},
		{"凭据无效", auth.ErrUnauthorized, codes.Unauthenticated, "应用凭据无效"},
		{"fp 不可达", auth.ErrUnavailable, codes.Unauthenticated, "应用凭据无效"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCredCache(&fakeVerifier{ok: map[string]string{}, err: tc.verr}, time.Minute)
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				MDAppID, "a1", MDAppSecret, "s1"))

			called := false
			err := streamAuth(c)(nil, &fakeStream{ctx: ctx}, nil,
				func(any, grpc.ServerStream) error { called = true; return nil })

			if called {
				t.Fatal("校验失败时绝不能进 handler")
			}
			st, _ := status.FromError(err)
			if st.Code() != tc.want {
				t.Fatalf("code = %v，期望 %v", st.Code(), tc.want)
			}
			if st.Message() != tc.wantMsg {
				t.Fatalf("msg = %q，期望 %q", st.Message(), tc.wantMsg)
			}
		})
	}
}

// TestStreamAuthPassesAppIDToHandler：校验通过之后，handler 必须能从 ctx
// 里拿到 app——下游全部按它做租户隔离，丢了就是跨应用串数据。
func TestStreamAuthPassesAppIDToHandler(t *testing.T) {
	c := newCredCache(&fakeVerifier{ok: map[string]string{"a1": "s1"}}, time.Minute)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		MDAppID, "a1", MDAppSecret, "s1"))

	var got string
	err := streamAuth(c)(nil, &fakeStream{ctx: ctx}, nil, func(_ any, ss grpc.ServerStream) error {
		got = appFrom(ss.Context())
		return nil
	})
	if err != nil {
		t.Fatalf("正确凭据必须放行：%v", err)
	}
	if got != "a1" {
		t.Fatalf("handler 拿到的 app = %q，期望 %q", got, "a1")
	}
}
