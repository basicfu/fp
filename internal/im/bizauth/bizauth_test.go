package bizauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
)

type apps map[string]model.AppConfig

func (a apps) Get(app string) (model.AppConfig, bool) { c, ok := a[app]; return c, ok }
func (a apps) Apps() []string                         { return nil }

// newEnv 起一个假的业务方验证服务，返回配好的认证器与请求计数器。
// handler 里可以断言收到的请求，也可以按需返回不同响应。
func newEnv(t *testing.T, h http.HandlerFunc) (*Authenticator, *httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	// 配置校验要求 https，但测试用的是 httptest 的 http 地址，
	// 所以这里直接构造 AppConfig 而不经过 Validate——被测的是回调行为，
	// 不是配置校验（那条由 model 包的测试守着）。
	a, err := New(Config{
		Apps: apps{"a1": {
			AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace,
			BizAuth: &model.BizAuth{VerifyURL: srv.URL, Timeout: model.Duration(2 * time.Second), CacheSize: 100},
		}},
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, srv, &calls
}

func req(app, token string, raw string) auth.VerifyRequest {
	return auth.VerifyRequest{App: app, Kind: model.TokenKindBiz, Token: token, Raw: []byte(raw)}
}

func TestVerifyForwardsRawFrameVerbatim(t *testing.T) {
	const raw = `{"t":"auth","app":"a1","token":"tok","kind":"biz","custom":{"deviceId":"d-1"}}`
	var got string
	a, _, _ := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Write([]byte(`{"user_id":"u1"}`))
	})
	if _, err := a.Verify(context.Background(), req("a1", "tok", raw)); err != nil {
		t.Fatal(err)
	}
	// 逐字节相同：client 塞的自定义字段必须原样到业务方手里，
	// 网关不解析后重新序列化（那会丢掉未知字段）。
	if got != raw {
		t.Fatalf("转发的不是原始字节：\n收到 %s\n期望 %s", got, raw)
	}
}

func TestVerifySuccessMapsToBizSubject(t *testing.T) {
	a, _, _ := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user_id":"1001"}`))
	})
	sub, err := a.Verify(context.Background(), req("a1", "tok", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if sub != model.Biz("1001") {
		t.Fatalf("主体应是 b:1001，实际 %s", sub)
	}
}

func TestVerifyErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{"401 无效令牌", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }, auth.ErrUnauthorized},
		{"200 但 user_id 为空", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"user_id":""}`)) }, auth.ErrUnauthorized},
		{"200 但 user_id 含空字节", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{\"user_id\":\"a\\u0000b\"}")) }, auth.ErrUnauthorized},
		{"500", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, auth.ErrUnavailable},
		{"200 但响应体不是 JSON", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`not json`)) }, auth.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := newEnv(t, tc.handler)
			_, err := a.Verify(context.Background(), req("a1", "tok", `{}`))
			if !errors.Is(err, tc.want) {
				t.Fatalf("错误映射不对：得到 %v，期望 %v", err, tc.want)
			}
		})
	}
}

func TestVerifyUnreachableIsUnavailable(t *testing.T) {
	a, srv, _ := newEnv(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close() // 关掉服务端，制造连不上
	_, err := a.Verify(context.Background(), req("a1", "tok", `{}`))
	if !errors.Is(err, auth.ErrUnavailable) {
		t.Fatalf("连不上必须是 ErrUnavailable（client 应退避重连），实际 %v", err)
	}
}

func TestVerifyAppWithoutBizAuthIsUnauthorized(t *testing.T) {
	a, err := New(Config{Apps: apps{"a1": {AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace}}})
	if err != nil {
		t.Fatal(err)
	}
	_, verr := a.Verify(context.Background(), req("a1", "tok", `{}`))
	if !errors.Is(verr, auth.ErrUnauthorized) {
		t.Fatalf("app 没配 biz_auth 时必须拒绝，实际 %v", verr)
	}
}

func TestVerifyCachesWhenServerAsksFor(t *testing.T) {
	a, _, calls := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user_id":"u1","cache_seconds":60}`))
	})
	for i := 0; i < 3; i++ {
		if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("业务方要求缓存 60 秒，三次握手应当只调一次，实际调了 %d 次", n)
	}
}

func TestVerifyDoesNotCacheWithoutTTL(t *testing.T) {
	a, _, calls := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user_id":"u1"}`))
	})
	for i := 0; i < 3; i++ {
		if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("不带 cache_seconds 就不该缓存，三次握手应当调三次，实际 %d 次", n)
	}
}

func TestVerifyReCallsAfterCacheExpires(t *testing.T) {
	// 假时钟：缓存过期这条性质靠等真实时间来测的话，要么让测试跑几十秒，
	// 要么把 TTL 调到毫秒级而变得不稳定。注入时钟是唯一可靠的办法。
	now := time.UnixMilli(1_000_000)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"user_id":"u1","cache_seconds":60}`))
	}))
	t.Cleanup(srv.Close)
	a, err := New(Config{
		Apps: apps{"a1": {
			AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace,
			BizAuth: &model.BizAuth{VerifyURL: srv.URL, Timeout: model.Duration(2 * time.Second), CacheSize: 10},
		}},
		Client: srv.Client(),
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Second) // 还没过期
	if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("59 秒时缓存仍应命中，实际调了 %d 次", n)
	}

	now = now.Add(2 * time.Second) // 越过 60 秒
	if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("过期后必须重新调用业务方，实际总共调了 %d 次——不重新调意味着撤销的令牌能无限期继续放行", n)
	}
}

func TestVerifyDoesNotCacheFailures(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	a, _, calls := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte(`{"user_id":"u1","cache_seconds":60}`))
	})
	if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err == nil {
		t.Fatal("第一次应当失败")
	}
	fail.Store(false)
	// 业务方接口恢复之后，client 应当立刻能连上，而不是等一个负缓存过期
	if _, err := a.Verify(context.Background(), req("a1", "tok", `{}`)); err != nil {
		t.Fatalf("接口恢复后应当立刻成功，实际 %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("失败不该进缓存，应当调两次，实际 %d 次", n)
	}
}

func TestVerifyRespectsTimeout(t *testing.T) {
	// 一直不返回，直到客户端因为超时取消请求。用请求自己的 ctx 而不是
	// 外部 channel：httptest.Server.Close() 会等所有未完成的请求结束，
	// 如果 handler 卡在一个只由 t.Cleanup 关闭的外部 channel 上，
	// t.Cleanup 是后进先出——后注册的 srv.Close 会先于 close(block) 执行，
	// 于是 Close 等请求结束、请求等 block 关闭、block 等 Close 返回，三方互相等死。
	//
	// 光等 r.Context().Done() 还不够：net/http 服务端只有在请求体被读完（命中 EOF）
	// 之后才会启动"后台探测连接是否已断开"的 goroutine，进而在客户端断开时取消
	// r.Context()。这里的请求体（`{}`）没人读，若不先排空它，客户端超时断开后
	// r.Context() 永远不会被取消，本质是同一种死锁换了个马甲——已经用一个独立的
	// 最小复现验证过：不排空 body 时 8 秒强制超时会命中，排空后 200ms 左右就返回。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done() // 一直不返回，直到客户端超时取消请求
	}))
	t.Cleanup(srv.Close)
	a, err := New(Config{
		Apps: apps{"a1": {
			AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace,
			BizAuth: &model.BizAuth{VerifyURL: srv.URL, Timeout: model.Duration(200 * time.Millisecond), CacheSize: 10},
		}},
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, verr := a.Verify(context.Background(), req("a1", "tok", `{}`))
	el := time.Since(start)
	if !errors.Is(verr, auth.ErrUnavailable) {
		t.Fatalf("超时必须是 ErrUnavailable，实际 %v", verr)
	}
	// 上界放宽到 2 秒是为了容忍慢机器；关键是它必须明显小于握手的 5 秒上限，
	// 否则 client 会先被握手超时踢掉，拿到的关闭码是错的。
	if el > 2*time.Second {
		t.Fatalf("超时没生效，耗时 %v", el)
	}
}
