package fpsdk

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/sdk/aksign"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

const (
	testAK = "FPAKTEST0000000000000000"
	testSK = "test-secret"
)

func okAccessKey(ips ...string) *fpv1.GetAccessKeyResponse {
	return &fpv1.GetAccessKeyResponse{Secret: testSK, Remark: "顺丰", Roles: []string{"partner"}, AllowedIps: ips, CacheTtlMs: 30_000}
}

// akStub 起一个桩服务端：GetAccessKey 返回 res 或 err，并统计调用次数。等推送流就绪后才返回，
// 否则 ready 触发的 purge 会冲掉测试刚写进去的缓存。
func akStub(t *testing.T, res *fpv1.GetAccessKeyResponse, err error, opt ...func(*Options)) (*stubEnv, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	stub := &stubServer{
		validate:   okValidate("u1", 30_000),
		watchReady: make(chan struct{}, 1),
		events:     make(chan *fpv1.WatchResponse, 16),
		getAccessKey: func(*fpv1.GetAccessKeyRequest) (*fpv1.GetAccessKeyResponse, error) {
			calls.Add(1)
			return res, err
		},
	}
	addr, stop := startStub(t, "", stub)
	opts := Options{Addr: addr, AppID: "t", AppSecret: "t", Insecure: true}
	for _, o := range opt {
		o(&opts)
	}
	client, nerr := New(opts)
	if nerr != nil {
		stop()
		t.Fatalf("New: %v", nerr)
	}
	t.Cleanup(func() {
		_ = client.Close()
		stop()
	})
	env := &stubEnv{stub: stub, client: client, auth: client.Auth(), addr: addr, stop: stop}
	env.waitUntil(t, client.StreamHealthy, "推送流没有就绪")
	return env, calls
}

// signedRequest 造一个按规则签好名的服务端请求（RemoteAddr 是 httptest 默认的 192.0.2.1）。
func signedRequest(t *testing.T, method, target, body string, ts int64, nonce, secret string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	path, query := aksign.SplitRequestURI(r.RequestURI)
	r.Header.Set(aksign.HeaderAccessKey, testAK)
	r.Header.Set(aksign.HeaderTimestamp, strconv.FormatInt(ts, 10))
	r.Header.Set(aksign.HeaderNonce, nonce)
	r.Header.Set(aksign.HeaderSignature,
		aksign.Signature(secret, aksign.StringToSign(method, path, query, ts, nonce, []byte(body))))
	return r
}

func TestAccessKeyRequestPasses(t *testing.T) {
	env, calls := akStub(t, okAccessKey(), nil)
	var got *Identity
	var body string
	h := env.auth.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFrom(r.Context())
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	now := time.Now().Unix()
	for i, nonce := range []string{"n1", "n2"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, signedRequest(t, "POST", "/orders?x=1", `{"a":1}`, now, nonce, testSK))
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次: code=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	if got == nil || got.AccessKeyID != testAK || got.AccessKeyRemark != "顺丰" || len(got.Roles) != 1 || got.UserID != "" {
		t.Fatalf("身份不对: %+v", got)
	}
	if body != `{"a":1}` {
		t.Fatalf("handler 读到的 body = %q", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("key 信息应被缓存，GetAccessKey 调了 %d 次", calls.Load())
	}
}

func TestAccessKeyRejections(t *testing.T) {
	now := time.Now().Unix()
	fpDenied := func(c codes.Code, code string) error {
		st, _ := status.New(c, code).WithDetails(&fpv1.ErrorDetail{Code: code, Msg: code})
		return st.Err()
	}
	sign := func(method, body string, ts int64, nonce, secret string) func(*testing.T) *http.Request {
		return func(t *testing.T) *http.Request { return signedRequest(t, method, "/x", body, ts, nonce, secret) }
	}
	cases := []struct {
		name     string
		res      *fpv1.GetAccessKeyResponse
		fpErr    error
		opt      func(*Options)
		req      func(*testing.T) *http.Request
		wantHTTP int
		wantCode string
	}{
		{"缺签名头", okAccessKey(), nil, nil, func(t *testing.T) *http.Request {
			r := signedRequest(t, "GET", "/x", "", now, "n", testSK)
			r.Header.Del(aksign.HeaderSignature)
			return r
		}, 401, CodeSignatureInvalid},
		{"nonce 含非法字符", okAccessKey(), nil, nil, sign("GET", "", now, "a b", testSK), 401, CodeSignatureInvalid},
		{"时间戳早于窗口", okAccessKey(), nil, nil, sign("GET", "", now-16*60, "n", testSK), 401, CodeTimestampExpired},
		{"时间戳晚于窗口", okAccessKey(), nil, nil, sign("GET", "", now+16*60, "n", testSK), 401, CodeTimestampExpired},
		{"AK 不存在", nil, fpDenied(codes.Unauthenticated, CodeAccessKeyInvalid), nil, sign("GET", "", now, "n", testSK), 401, CodeAccessKeyInvalid},
		{"key 已停用", nil, fpDenied(codes.PermissionDenied, CodeAccessKeyDisabled), nil, sign("GET", "", now, "n", testSK), 403, CodeAccessKeyDisabled},
		{"key 已过期", nil, fpDenied(codes.PermissionDenied, CodeAccessKeyExpired), nil, sign("GET", "", now, "n", testSK), 403, CodeAccessKeyExpired},
		{"签名不匹配", okAccessKey(), nil, nil, sign("GET", "", now, "n", "wrong"), 401, CodeSignatureMismatch},
		{"body 超过上限", okAccessKey(), nil, func(o *Options) { o.MaxSignedBodyBytes = 4 }, sign("POST", "12345", now, "n", testSK), 413, CodeBodyTooLarge},
		{"IP 不在白名单", okAccessKey("10.0.0.0/8"), nil, nil, sign("GET", "", now, "n", testSK), 403, CodeIPDenied},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var opts []func(*Options)
			if c.opt != nil {
				opts = append(opts, c.opt)
			}
			env, _ := akStub(t, c.res, c.fpErr, opts...)
			called := false
			h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, c.req(t))
			if rec.Code != c.wantHTTP || called || !strings.Contains(rec.Body.String(), c.wantCode) {
				t.Fatalf("code=%d called=%v body=%s，want %d %s", rec.Code, called, rec.Body.String(), c.wantHTTP, c.wantCode)
			}
		})
	}
}

// 签名不匹配时附上 SDK 算出的待签名串，供第三方逐行对照；响应里不能出现 SK。
func TestSignatureMismatchIncludesStringToSign(t *testing.T) {
	env, _ := akStub(t, okAccessKey(), nil)
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	now := time.Now().Unix()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, "GET", "/x?b=2", "", now, "n1", "wrong"))
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if want := aksign.StringToSign("GET", "/x", "b=2", now, "n1", nil); body["code"] != CodeSignatureMismatch || body["stringToSign"] != want {
		t.Fatalf("body = %v, want stringToSign %q", body, want)
	}
	if strings.Contains(rec.Body.String(), testSK) {
		t.Fatal("响应里出现了 SK")
	}
}

// 【辨别力】nonce 在签名通过后才记录：伪造请求用过的 nonce，正确请求仍能用。
func TestNonceRecordedOnlyAfterSignaturePasses(t *testing.T) {
	env, _ := akStub(t, okAccessKey(), nil)
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	now := time.Now().Unix()
	serve := func(secret string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, signedRequest(t, "GET", "/x", "", now, "same-nonce", secret))
		return rec
	}
	if rec := serve("wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("错签名应 401，got %d", rec.Code)
	}
	if rec := serve(testSK); rec.Code != http.StatusOK {
		t.Fatalf("正确签名应放行，got %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(testSK); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), CodeNonceUsed) {
		t.Fatalf("重放应 401 NONCE_USED，got %d %s", rec.Code, rec.Body.String())
	}
}

func TestClientIPFromXForwardedFor(t *testing.T) {
	env, _ := akStub(t, okAccessKey("10.0.0.0/8"), nil)
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r := signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK)
	r.Header.Set("X-Forwarded-For", "10.1.2.3, 192.0.2.9") // RemoteAddr 192.0.2.1 不在白名单
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("应取 X-Forwarded-For 的第一个地址，got %d %s", rec.Code, rec.Body.String())
	}
}

// 【辨别力】带了访问密钥头就只走访问密钥：签名错时，即使同时带着 token 也不放行。
func TestAccessKeyHeaderNeverFallsBackToToken(t *testing.T) {
	env, _ := akStub(t, okAccessKey(), nil)
	called := false
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", "wrong")
	r.Header.Set("Authorization", "Bearer valid-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("code=%d called=%v", rec.Code, called)
	}
}

func TestAccessKeyFpUnavailableIs503(t *testing.T) {
	env, _ := akStub(t, nil, status.Error(codes.Unavailable, "down"))
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fp 不可达应 503 而不是 401，got %d", rec.Code)
	}
}

// fp 回源返回的访问密钥材料缺 secret 时必须拒绝，不能拿空串当 HMAC 密钥用——
// 空密钥的 HMAC 谁都能复现，一旦真的发生（proto 字段号漂移、数据不完整），
// 校验还会"成功"，且没有任何异常信号。fp 现在的实现不会返回空 secret，
// 这是防御性加固，不是修复现有 bug。
func TestAccessKeyEmptySecretIsRejected(t *testing.T) {
	res := okAccessKey()
	res.Secret = ""
	env, _ := akStub(t, res, nil)
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("空 secret 应 503 而不是放行，got %d %s", rec.Code, rec.Body.String())
	}
}

// 【辨别力】访问密钥的缓存条目绝不能被 token 校验当成一次成功的登录判定复用。
//
// verifyAccessKey 为了取 SK 会在验签**之前**回源并把 "ak:<id>" 这个键的角色
// 信息写进缓存——哪怕这次请求最终因为签名不对被拒。cache 是 token 与访问
// 密钥共用的同一张表，如果 Validate 的缓存命中判断只看 appID、不看
// entry.accessKey，攻击者只需要知道目标 AK 的 ID（AK 本身不是秘密，业务方
// 日志、URL 里都可能出现），发一个签名随便填的访问密钥请求把缓存写热，
// 再把 "ak:<目标AK>" 原样当 token 发过来，就能在同一个 app 作用域下
// （单 app 部署恒成立）冒充该 AK 绑定的角色——全程不需要 SK、不需要正确签名。
//
// 装配：先用错误的密钥发一次访问密钥请求（预期 401，但副作用是把
// ak:<testAK> 这个缓存键写热）；再把这个键的字面值原样当 Bearer token
// 发一次普通请求。断言拿到的身份不带 okAccessKey() 绑定的 "partner" 角色——
// 如果 auth.go 的两处 Validate 缓存命中判断少了 e.accessKey == nil，
// 这里会命中被污染的缓存条目，泄漏 AK 的角色。
func TestAccessKeyCacheEntryNeverAuthenticatesAsToken(t *testing.T) {
	env, _ := akStub(t, okAccessKey(), nil)

	akHandler := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	akHandler.ServeHTTP(rec, signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", "wrong-secret"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("前置条件：伪造签名的访问密钥请求应 401（但仍会把缓存写热），got %d", rec.Code)
	}

	var got *Identity
	tokenHandler := env.auth.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/y", nil)
	req.Header.Set("Authorization", "Bearer "+accessKeyCacheKey(testAK))
	rec2 := httptest.NewRecorder()
	tokenHandler.ServeHTTP(rec2, req)

	if got == nil {
		t.Fatalf("请求未被放行：code=%d body=%s", rec2.Code, rec2.Body.String())
	}
	for _, role := range got.Roles {
		if role == "partner" {
			t.Fatalf("token 校验命中了访问密钥的缓存条目，冒充了它绑定的角色：code=%d identity=%+v",
				rec2.Code, got)
		}
	}
}
