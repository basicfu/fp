package fpsdk

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

func okHandler(seen *Identity) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := IdentityFrom(r.Context()); ok {
			*seen = *id
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddlewareRejectsMissingToken(t *testing.T) {
	env := newStubEnv(t, okValidate("u1", 30_000))
	called := false
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 返回 %d，期望 401", rec.Code)
	}
	if called {
		t.Fatal("无 token 时业务 handler 仍被调用了")
	}
}

func TestMiddlewareAcceptsBearerAndCookie(t *testing.T) {
	env := newStubEnv(t, okValidate("u1", 30_000))
	var seen Identity
	h := env.auth.Middleware(okHandler(&seen))

	t.Run("Bearer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || seen.UserID != "u1" {
			t.Fatalf("code=%d userID=%q", rec.Code, seen.UserID)
		}
	})
	t.Run("Cookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || seen.UserID != "u1" {
			t.Fatalf("code=%d userID=%q", rec.Code, seen.UserID)
		}
	})
}

// TestMiddlewareRelaysRotatedTokenOnEveryRequest 是本任务的核心。
//
// fp 在过渡期内对每一次带旧 token 的校验都重复告知新 token（Task 3），
// SDK 缓存条目也带着 rotatedTo（Task 10）。中间件是最后一棒——
// 只在首次回源时交付、缓存命中时不交付的话，前两个任务全部作废，
// 因为绝大多数请求都是缓存命中。
//
// 症状要等到 rotate_interval（默认 24 小时）之后才显形：
// 一批用户在毫无操作的情况下集体掉线，日志里看不出任何异常。
func TestMiddlewareRelaysRotatedTokenOnEveryRequest(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return &fpv1.ValidateTokenResponse{
			UserId: "u1", SessionId: "s1", CacheTtlMs: 30_000,
			Rotated: true, NewToken: "new-tok",
		}, nil
	})
	var seen Identity
	h := env.auth.Middleware(okHandler(&seen))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer old-tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		var got string
		for _, c := range rec.Result().Cookies() {
			if c.Name == DefaultCookieName {
				got = c.Value
			}
		}
		if got != "new-tok" {
			t.Fatalf("第 %d 次请求没有回写新 token 的 cookie（得到 %q）——"+
				"缓存命中时交接被截断，会话将在过渡期后死亡", i, got)
		}
		if h := rec.Header().Get(RotatedTokenHeader); h != "new-tok" {
			t.Fatalf("第 %d 次请求缺少 %s 响应头（得到 %q）——"+
				"非浏览器客户端无从得知新 token", i, RotatedTokenHeader, h)
		}
	}
}

// TestRotationCookieIsWrittenBeforeHandlerWrites 守住一个 net/http 陷阱。
//
// Set-Cookie 只有在 WriteHeader 之前写进 Header() 才会真的发出去。
// 中间件若在 next.ServeHTTP 之后再回写 cookie，对任何已经写过响应的
// handler 都是静默失效——而"handler 写响应"就是 handler 的全部工作。
func TestRotationCookieIsWrittenBeforeHandlerWrites(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return &fpv1.ValidateTokenResponse{
			UserId: "u1", CacheTtlMs: 30_000, Rotated: true, NewToken: "new-tok",
		}, nil
	})
	// 这个 handler 立刻写响应头并写 body，之后再改 Header() 就没用了。
	h := env.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer old-tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("handler 写过响应后 cookie 就丢了——回写发生在 next 之后")
	}
}

// TestSecureCookieIsOptIn 记录一个刻意的默认值。
//
// Secure 默认 false：SDK 无从知道业务方跑在 HTTP 还是 HTTPS 后面，
// 默认打开会让所有本地开发环境的 cookie 静默失效——而"cookie 没生效"
// 是最难查的一类问题。生产必须显式设 true，demo 与文档都要写明。
//
// 分别用默认 opts 与 CookieSecure: true 各触发一次轮换交接——两次用不同
// token，避免任何缓存交叉影响这条纯粹关于 Secure 属性的断言——
// 断言回写 cookie 的 Secure 属性符合配置。
func TestSecureCookieIsOptIn(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return &fpv1.ValidateTokenResponse{
			UserId: "u1", CacheTtlMs: 30_000, Rotated: true, NewToken: "new-tok",
		}, nil
	})

	rotationCookie := func(t *testing.T, h http.Handler, token string) *http.Cookie {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		for _, c := range rec.Result().Cookies() {
			if c.Name == DefaultCookieName {
				return c
			}
		}
		t.Fatal("响应里没有回写 cookie——本断言的前提（触发一次轮换交接）没有成立")
		return nil
	}

	t.Run("默认opts不带Secure", func(t *testing.T) {
		h := env.auth.Middleware(okHandler(new(Identity)))
		c := rotationCookie(t, h, "secure-opt-in-default")
		if c.Secure {
			t.Fatal("默认 MiddlewareOptions 下 cookie 带了 Secure——" +
				"本地 HTTP 开发环境的会话 cookie 会被浏览器静默丢弃")
		}
	})

	t.Run("CookieSecure为true时带Secure", func(t *testing.T) {
		h := env.auth.MiddlewareWith(MiddlewareOptions{CookieSecure: true})(okHandler(new(Identity)))
		c := rotationCookie(t, h, "secure-opt-in-explicit")
		if !c.Secure {
			t.Fatal("CookieSecure: true 时回写的 cookie 没有 Secure 属性")
		}
	})
}

// TestMiddlewareDoesNotEchoToken 守住不把凭据写进响应体。
//
// 把 token 拼进错误信息（"token xxx 无效"）会让它进入前端日志、
// 浏览器控制台、错误上报平台——一个仍然有效的凭据就此四处流传。
func TestMiddlewareDoesNotEchoToken(t *testing.T) {
	env := newStubEnv(t, failValidate())
	h := env.auth.Middleware(okHandler(new(Identity)))

	const token = "非常独特的令牌值-7c1e"
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), token) {
		t.Fatalf("响应体里回显了 token: %s", rec.Body.String())
	}
}

// TestMiddlewareUnavailableReturns503 补一条简报正文里点名、但 Step 1 给定
// 测试列表没有覆盖的性质："ErrUnavailable 要回 503 而不是 401"。
//
// 认证服务不可用不是"你没通过认证"。回 401 会让客户端（尤其是会在收到
// 401 时主动清掉本地凭据的前端逻辑）清掉一个其实完全有效的 token，
// 把一次 fp 抖动放大成全体用户重新登录。这条要求在简报正文里单独成节，
// 分量与"交付顺序""每次交付""不回显 token"这三条相当，却没有对应的
// 给定测试——手工验证过：把 defaultOnError 里 ErrUnavailable 那个 case
// 删掉（落入 default 分支变成 401）之后，Step 1 给定的六条测试全部照样
// 通过，测不出这个回归。
func TestMiddlewareUnavailableReturns503(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return nil, status.Error(codes.Unavailable, "fp 挂了")
	})
	h := env.auth.Middleware(okHandler(new(Identity)))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	// 从未见过的 token：没有任何缓存条目可以陈旧兜底，Validate 必然
	// 返回 ErrUnavailable（而不是走到 stale-fallback 分支）。
	req.Header.Set("Authorization", "Bearer never-cached-tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fp 不可达时返回 %d，期望 %d(503)——"+
			"回 401 会让客户端清掉一个仍然有效的 token，把一次 fp 抖动"+
			"放大成全体用户重新登录", rec.Code, http.StatusServiceUnavailable)
	}
}
