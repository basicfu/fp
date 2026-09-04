package fpsdk

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// DefaultCookieName 是默认的会话 cookie 名。
const DefaultCookieName = "fp_token"

// RotatedTokenHeader 是 token 轮换时回传新 token 的响应头。
//
// 除了 cookie 还要发一个响应头：移动端 / 服务间调用这类非浏览器客户端
// 不处理 Set-Cookie，光靠 cookie 交接对它们完全无效。
const RotatedTokenHeader = "X-Fp-New-Token"

// GuestIDHeader 与 fp-im 网关握手帧里承载访客标识的字段是同一个值：
// 前端生成并持久化的一个 uuid v4，不登录也能用它连 WebSocket。业务方的
// 普通 HTTP 接口（上传图片、拉历史消息……）要用同一个值认出同一个访客，
// 就靠这个请求头传过来。
const GuestIDHeader = "X-Guest-Id"

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

	// AllowGuest 开启后，请求没带 token 但带了合法的 GuestIDHeader 时，
	// 以访客身份放行（Identity.GuestID 非空，UserID 为空），具体权限由
	// 业务方自己按 fp-im 的 guest 角色再判一次。默认 false：不开的时候，
	// 带访客头的请求必须和现在完全一样地被拒，不能有任何行为变化。
	//
	// 访客标识不经过 fp 签发、也不带签名——它就是前端生成并持久化的一个
	// 随机 uuid v4，本来就没有"验证"这一步可做。这里只做格式校验。
	// 它的安全性来自 122 位随机熵：能猜中或偷到别人的访客 id，代价
	// 跟偷一个真正的登录 token 是同一个量级，所以不需要额外的签名层。
	// 真正防"客户端批量塞造假 id 骗过统计/占用资源"的防线在 fp-im 网关
	// 握手处按来源 IP 限流，不是这里——这里只负责"这串字符是不是一个
	// 合法的 uuid v4"，不负责"这个 id 是不是真的对应一个曾经握手过的
	// 访客"，因为访客身份的定义本来就是"没有身份提供方能替它作答"。
	AllowGuest bool
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
			token := opts.TokenFrom(r)

			// 令牌优先：没有 token 才考虑访客头。同时带两者时（比如客户端
			// 刚登录但还没来得及清掉本地存的访客 id）必须走 token 这条路，
			// 不能把一个已登录用户悄悄降级成访客。
			if token == "" && opts.AllowGuest {
				if gid := r.Header.Get(GuestIDHeader); gid != "" {
					// 只做格式校验，不做签名验证——理由见
					// MiddlewareOptions.AllowGuest 的注释。
					if !isUUIDv4(gid) {
						// 格式不合法直接拒绝，而不是当成"没带凭据"放过去
						// 继续匿名处理：格式错误意味着调用方（前端或
						// fp-im 网关）本身有 bug，静默降级会让这个 bug
						// 混进正常的匿名流量里，很难被人发现。
						opts.OnError(w, r, ErrUnauthorized)
						return
					}
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityCtxKey{}, &Identity{GuestID: gid})))
					return
				}
			}

			id, err := a.Validate(r.Context(), token)
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

// WriteError 把 SDK 的哨兵错误映射成 HTTP 状态码：
// ErrNoToken/ErrUnauthorized → 401，ErrUnavailable → 503，
// ErrInvalidArgument → 400，ErrRateLimited → 429，其余 → 401。
//
// 绝不要把 err 的内容拼进响应体：它可能带着 token、手机号等敏感信息，
// 一旦写进响应会流进前端日志、浏览器控制台、错误上报平台。因此全部
// 分支（含 default）都只写固定的提示文案，从不回显 err.Error()——
// 这条性质本身就是本函数存在、取代各接入方自己拼错误信息的理由之一，
// 见 TestWriteErrorNeverEchoesUnderlyingError。
//
// 供不经过 Middleware 的路由复用——SendLoginCode/Login 发生在鉴权中间件
// 之前，拿不到 defaultOnError 的实现（defaultOnError 本身也是靠这个函数
// 实现的，是唯一实现，不留第二份分类逻辑）。每个接入方原本都要自己重写
// 一遍这个分类，而写反的方向是危险的一边：把 ErrUnavailable（fp 抖动）
// 误判成 401 会让全体接入方的用户被强制登出，比把普通鉴权失败误判成 503
// 后果重得多；同理，把 ErrInvalidArgument（用户手机号打错了一位数字）
// 或 ErrRateLimited（验证码发送被限流）误判成 401，会让客户端把一次
// 纯粹的输入问题或频率问题当成鉴权失败去清 cookie、跳登录页。
func WriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoToken), errors.Is(err, ErrUnauthorized):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case errors.Is(err, ErrUnavailable):
		// 503 而不是 401：认证服务不可用不是"你没通过认证"。
		// 回 401 会让客户端清掉一个其实完全有效的 token，把一次
		// fp 抖动放大成全体用户重新登录。
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	case errors.Is(err, ErrInvalidArgument):
		// 400：请求本身没法处理（手机号格式错、验证码格式错），不是
		// 凭据问题。回 401 会让客户端误以为要清会话/跳登录页，而用户
		// 可能只是手机号少打了一位。
		http.Error(w, "bad request", http.StatusBadRequest)
	case errors.Is(err, ErrRateLimited):
		// 429：被服务端限流（验证码发送过于频繁），用户的凭据没有任何
		// 问题，同样不该被当成鉴权失败。
		http.Error(w, "too many requests", http.StatusTooManyRequests)
	default:
		// 服务端返回了一个 SDK 还没有对应哨兵错误的 gRPC code——保守
		// 起见按拒绝处理，而不是放行一个无法归类的错误。这不是"正常
		// 路径"，出现意味着 SDK 的哨兵词汇表需要再扩充一条。
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

// defaultOnError 是 Middleware 的默认错误响应，语义见 WriteError。
func defaultOnError(w http.ResponseWriter, _ *http.Request, err error) {
	WriteError(w, err)
}

// isUUIDv4 只接受 8-4-4-4-12、小写、带连字符的标准写法，且版本位必须是 4。
//
// 这是 sdk/im/subject.go 里 IsUUIDv4 的复制品，字节级同一份逻辑，故意不
// import fpim 来复用：fpsdk 是被所有接入方依赖的认证 SDK，fpim 是
// WebSocket 网关专用的库，反过来依赖会让每一个只想要"认证中间件"的
// 业务方都被迫拉进整个 WebSocket 依赖树。这是第三份同样的实现（另两份
// 是 internal/im/model 与 sdk/im）。三份的一致性由
// internal/integration/im_parity_test.go 的 TestSubjectFormatParity（比对
// 前两份）与 TestGuestUUIDValidationParityViaMiddleware（这第三份是未导出
// 函数，测不到它本身，改为通过 a.MiddlewareWith(MiddlewareOptions{AllowGuest:
// true}) 这层可观察的 HTTP 行为间接验证）共同覆盖；改这个函数时请一并检查
// 另外两处，并确认那两条测试仍然通过。
//
// 大写或去掉连字符虽然和标准写法是同一个 uuid，但落到存储层（Redis key /
// 数据库主键）会生成不同的键，同一个访客就变成了两个人——所以这里直接
// 拒绝而不是先归一化再解析。
func isUUIDv4(s string) bool {
	if len(s) != 36 || strings.ToLower(s) != s {
		return false
	}
	u, err := uuid.Parse(s)
	return err == nil && u.Version() == 4
}
