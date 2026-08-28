package fpsdk

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// DefaultCookieName 是默认的会话 cookie 名。
const DefaultCookieName = "fp_token"

// RotatedTokenHeader 是 token 轮换时回传新 token 的响应头。
//
// 除了 cookie 还要发一个响应头：移动端 / 服务间调用这类非浏览器客户端
// 不处理 Set-Cookie，光靠 cookie 交接对它们完全无效。
const RotatedTokenHeader = "X-Fp-New-Token"

type identityCtxKey struct{}

// IdentityFrom 从请求上下文里取出已认证身份。
func IdentityFrom(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(*Identity)
	return id, ok
}

// MiddlewareOptions 定制中间件行为。零值即默认行为。
type MiddlewareOptions struct {
	// TokenFrom 自定义 token 提取。为 nil 时先看 Authorization: Bearer，
	// 再看名为 CookieName 的 cookie。
	TokenFrom func(*http.Request) string
	// CookieName 是会话 cookie 名，为空时用 DefaultCookieName。
	CookieName string
	// CookieSecure 决定回写的 cookie 是否带 Secure。
	//
	// 默认 false：SDK 无从知道业务方跑在 HTTP 还是 HTTPS 后面，默认打开
	// 会让所有本地开发环境的 cookie 静默失效。**生产必须显式设为 true**，
	// 否则会话 cookie 会在任何一段明文 HTTP 上原样出现。
	CookieSecure bool
	// CookiePath / CookieDomain / CookieSameSite 是回写 cookie 的其余属性。
	CookiePath     string
	CookieDomain   string
	CookieSameSite http.SameSite

	// OnRotate 覆盖默认的新 token 交付方式。
	OnRotate func(w http.ResponseWriter, r *http.Request, newToken string)
	// OnError 覆盖默认的失败响应（默认写 401，响应体不含 token）。
	OnError func(w http.ResponseWriter, r *http.Request, err error)
}

// Middleware 用默认配置返回鉴权中间件。
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return a.MiddlewareWith(MiddlewareOptions{})(next)
}

// MiddlewareWith 返回一个可定制的鉴权中间件构造器。
func (a *Auth) MiddlewareWith(opts MiddlewareOptions) func(http.Handler) http.Handler {
	if opts.CookieName == "" {
		opts.CookieName = DefaultCookieName
	}
	if opts.CookiePath == "" {
		opts.CookiePath = "/"
	}
	if opts.TokenFrom == nil {
		opts.TokenFrom = defaultTokenFrom(opts.CookieName)
	}
	if opts.OnRotate == nil {
		opts.OnRotate = defaultOnRotate(opts)
	}
	if opts.OnError == nil {
		opts.OnError = defaultOnError
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := a.Validate(r.Context(), opts.TokenFrom(r))
			if err != nil {
				opts.OnError(w, r, err)
				return
			}

			// 交付必须在 next 之前：Set-Cookie 只有写在 WriteHeader 之前
			// 才会真的发出去，而 handler 的第一件事往往就是写响应。
			//
			// 每次都交付，不只在回源时——缓存条目带着 rotatedTo，
			// 而绝大多数请求是缓存命中。只在回源时交付等于把 fp 侧
			// "过渡期内重复告知"的修复在最后一棒重新打破。
			if id.RotatedTo != "" {
				opts.OnRotate(w, r, id.RotatedTo)
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityCtxKey{}, id)))
		})
	}
}

func defaultTokenFrom(cookieName string) func(*http.Request) string {
	return func(r *http.Request) string {
		if h := r.Header.Get("Authorization"); h != "" {
			if v, ok := strings.CutPrefix(h, "Bearer "); ok {
				return strings.TrimSpace(v)
			}
		}
		if c, err := r.Cookie(cookieName); err == nil {
			return c.Value
		}
		return ""
	}
}

func defaultOnRotate(opts MiddlewareOptions) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, _ *http.Request, newToken string) {
		http.SetCookie(w, &http.Cookie{
			Name:     opts.CookieName,
			Value:    newToken,
			Path:     opts.CookiePath,
			Domain:   opts.CookieDomain,
			HttpOnly: true,
			Secure:   opts.CookieSecure,
			SameSite: opts.CookieSameSite,
		})
		// 非浏览器客户端不处理 Set-Cookie，必须另给一条明路。
		w.Header().Set(RotatedTokenHeader, newToken)
	}
}

// defaultOnError 写一个不含任何凭据的 401。
//
// 绝不要把 token 拼进错误信息：它会流进前端日志、浏览器控制台、
// 错误上报平台——一个仍然有效的凭据就此四处流传。
func defaultOnError(w http.ResponseWriter, _ *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNoToken), errors.Is(err, ErrUnauthorized):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case errors.Is(err, ErrUnavailable):
		// 503 而不是 401：认证服务不可用不是"你没通过认证"。
		// 回 401 会让客户端清掉一个其实完全有效的 token，把一次
		// fp 抖动放大成全体用户重新登录。
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}
