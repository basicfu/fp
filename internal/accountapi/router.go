package accountapi

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/accountapi/captcha"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// LoginChecker 是登录前经 Watch 流问应用"认不认这台设备"。*service.LoginCheck 满足它。
// 错误为 *domain.Error 时 Code 是应用回的码(如 version_too_low),超时、没有在线的流为 unavailable。
type LoginChecker interface {
	Ask(ctx context.Context, app *domain.Application, deviceHeader, clientIP string) (clientRef string, err error)
}

// Captcha 是滑块验证,*captcha.Service 满足它。
type Captcha interface {
	Issue(ctx context.Context) (*captcha.Captcha, error)
	Verify(ctx context.Context, id string, x int, track [][3]int) error
}

// Deps 是账号接口的全部依赖。WechatMP 为空时 /notify/* 回不可用、user 不带 notify。
type Deps struct {
	Apps     *service.ApplicationService
	Auth     *service.AuthService
	Users    *service.UserService
	Accounts *service.AccountService
	WechatMP *service.WechatMPService
	Sessions *service.SessionService
	Guard    *service.DeviceGuard
	// Check 只在应用开了设备绑定时用到;为空时这类应用登录一律 unavailable。
	Check   LoginChecker
	Captcha Captcha
	Redis   *redis.Client
	// TrustedProxies 里来的请求才认 X-Forwarded-For(取最后一跳);非空时别处来的请求一律 404(防绕过网关伪造 Host)。
	// 空表示一律用直连地址、不拦来源。
	TrustedProxies []netip.Prefix
	// Now 可注入,测试用;为空取 time.Now。
	Now func() time.Time
}

type handler struct {
	d       Deps
	clock   func() time.Time
	tickets phoneTickets
}

// APIHeader 是新客户端调新接口时带的头(值为 APIVersion)。老客户端调的路径大多与新接口重名,
// 请求体分不出新老(短信、密码登录一模一样),只能靠它;不带就按老格式回。老路径整层删除后这个头不再有意义。
const (
	APIHeader  = "X-Account-Api"
	APIVersion = "2"
)

func split(cur, legacy http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(APIHeader) == APIVersion {
			cur(w, r)
			return
		}
		legacy(w, r)
	}
}

// NewRouter 返回挂在 /api/account 下的处理器(调用方 Mount,路径里不含前缀)。
// 新接口与老路径别名共用一棵路由,见 legacy.go。
func NewRouter(d Deps) http.Handler {
	h := &handler{d: d, clock: d.Now, tickets: redisTickets{c: d.Redis}}
	r := chi.NewRouter()
	r.Use(h.withTrustedPeer, h.withClientIP, h.withApp)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { h.writeErr(w, "not_found", "接口不存在") })
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusMethodNotAllowed, errBody{Code: "bad_request", Msg: "请求方法不正确", Time: h.now().UnixMilli()})
	})

	// 与老路径重名的接口按 X-Account-Api 分流:老客户端不带这个头,响应必须原样是老格式。
	r.Get("/captcha", h.handleCaptcha)
	r.Post("/code", split(h.handleCode, h.legacyCode))
	r.Post("/code/verify", h.handleCodeVerify)
	r.Post("/login", split(h.handleLogin, h.legacyLogin))
	r.Post("/logout", h.handleLogout)
	r.Post("/wechat/start", split(h.handleWechatStart, h.legacyWechatStart))
	r.Get("/me", split(h.handleMe, h.legacyMe))
	r.Put("/profile", split(h.handleProfile, h.legacyProfile))
	r.Put("/avatar", split(h.handleAvatar, h.legacyAvatar))
	r.Post("/password", split(h.handlePassword, h.legacyPassword))
	r.Post("/identity/bind", h.handleIdentityBind)
	r.Post("/identity/unbind", h.handleIdentityUnbind)
	r.Get("/notify/qr", h.handleNotifyQR)
	r.Post("/notify/off", split(h.handleNotifyOff, h.legacyNotifyOff))
	r.Post("/delete", h.handleDelete)

	h.legacyRoutes(r)
	return r
}
