package accountapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

type ctxKey int

const (
	keyApp ctxKey = iota
	keyIP
)

func appOf(r *http.Request) *domain.Application {
	return r.Context().Value(keyApp).(*domain.Application)
}
func ipOf(r *http.Request) string { return r.Context().Value(keyIP).(string) }

// withApp 按 Host 认应用。认不出、或应用没开账号接口一律 404:不让外部据此探测域名归属。
// 这组接口只经网关对外,Host 可信的前提由 withTrustedPeer 把住(配了可信代理时)。
func (h *handler) withApp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app, err := h.d.Apps.GetActiveByHost(r.Context(), r.Host)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			h.writeErr(w, "not_found", "接口不存在")
			return
		case err != nil:
			slog.Error("accountapi: 按域名取应用失败", "host", r.Host, "err", err)
			h.writeErr(w, "unavailable", "")
			return
		case !app.Account.Enabled:
			h.writeErr(w, "not_found", "接口不存在")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyApp, app)))
	})
}

// withTrustedPeer 在配了可信代理时只放行从代理来的请求，其余一律 404：withApp 按 Host 认应用，
// 绕过网关直连 fp 就能随意填 Host，冒充任何应用的域名。没配时不设防（直连部署、测试）。
func (h *handler) withTrustedPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(h.d.TrustedProxies) > 0 {
			peer, ok := peerAddr(r)
			if !ok || !h.trusted(peer) {
				h.writeErr(w, "not_found", "接口不存在")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func peerAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// withClientIP 只在直连方是可信代理时才认 X-Forwarded-For,且只取最后一跳:前面的跳是客户端自己能写的。
func (h *handler) withClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyIP, h.clientIP(r))))
	})
}

func (h *handler) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if peer, ok := peerAddr(r); !ok || !h.trusted(peer) {
		return host
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return host
	}
	parts := strings.Split(xff[len(xff)-1], ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	if a, err := netip.ParseAddr(last); err == nil {
		return a.Unmap().String()
	}
	return host
}

func (h *handler) trusted(a netip.Addr) bool {
	for _, p := range h.d.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// caller 是一次通过校验的请求。Rotated 非空表示 token 被轮换了,响应要带回给客户端。
type caller struct {
	UserID  uuid.UUID
	Session *domain.Session
	Token   string
	Rotated string
}

func bearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "Bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

// requireUser 校验 token 与设备头,失败时已写好响应。
//
// 设备先于 token:时钟偏导致的签名失败不该被报成登录过期(客户端见 unauthorized 就清登录态)。
// 所以先无副作用地读出会话比对设备,再走会轮换/延期的 Validate——否则拿着偷来的 token、
// 没有设备私钥的人也能触发轮换,把真设备手里的 token 挤到过渡期之后失效。
func (h *handler) requireUser(w http.ResponseWriter, r *http.Request) (*caller, bool) {
	ctx, app := r.Context(), appOf(r)
	token := bearer(r)
	if token == "" {
		h.writeErr(w, "unauthorized", "")
		return nil, false
	}
	if app.Account.DeviceBinding {
		header := r.Header.Get(app.Account.DeviceHeader)
		if header == "" {
			// 老路径照业务方原实现:已登录接口缺设备头是 device_unverified,只有登录才报 version_too_low。
			if r.Header.Get(APIHeader) != APIVersion {
				h.writeErr(w, "device_unverified", "")
			} else {
				h.writeErr(w, "version_too_low", "")
			}
			return nil, false
		}
		sess, err := h.d.Sessions.SessionByToken(ctx, token)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			// 交给 Validate 给出确定的结论(未登录 / 被移出账号)。
		case err != nil:
			slog.Warn("accountapi: 读会话失败", "err", err)
			h.writeErr(w, "unavailable", "")
			return nil, false
		case sess.AppID == app.ID:
			if err := h.d.Guard.Check(ctx, app, sess, header, h.now()); err != nil {
				h.deviceErr(w, err)
				return nil, false
			}
		}
	}
	res, err := h.d.Sessions.Validate(ctx, token, app)
	if err != nil {
		code, _, de := asDomain(err)
		switch {
		case code == domain.CodeKicked:
			h.writeErr(w, "kicked", "")
		// 先判不可达: 回 401 会让客户端清掉一个其实有效的登录态。
		case de == nil || errors.Is(err, domain.ErrInternal):
			slog.Warn("accountapi: 校验 token 失败", "err", err)
			h.writeErr(w, "unavailable", "")
		default:
			h.writeErr(w, "unauthorized", "")
		}
		return nil, false
	}
	c := &caller{UserID: res.Session.UserID, Session: res.Session, Token: token}
	if res.Rotated {
		c.Rotated = res.NewToken
	}
	return c, true
}

// deviceErr:设备校验失败统一 device_unverified;Redis 故障不能说成设备不对。
func (h *handler) deviceErr(w http.ResponseWriter, err error) {
	switch code, _, _ := asDomain(err); code {
	case domain.CodeDeviceUnverified:
		h.writeErr(w, "device_unverified", "")
		return
	case domain.CodeTokenInvalid:
		// 会话没绑设备：让客户端清掉登录态重新登录，登录时就绑上了。
		h.writeErr(w, "unauthorized", "")
		return
	}
	slog.Warn("accountapi: 设备校验出错", "err", err)
	h.writeErr(w, "unavailable", "")
}

// loginDevice 是登录前的设备校验:验签、时间窗、nonce,再经 Watch 流问应用拿 client_ref。
// 应用没开设备绑定时什么都不做。失败时已写好响应。
func (h *handler) loginDevice(w http.ResponseWriter, r *http.Request) (devicePub, clientRef string, ok bool) {
	app := appOf(r)
	if !app.Account.DeviceBinding {
		return "", "", true
	}
	header := r.Header.Get(app.Account.DeviceHeader)
	if header == "" {
		h.writeErr(w, "version_too_low", "")
		return "", "", false
	}
	devicePub, err := h.d.Guard.VerifyHeader(r.Context(), header, h.now())
	if err != nil {
		h.deviceErr(w, err)
		return "", "", false
	}
	if h.d.Check == nil {
		slog.Error("accountapi: 应用开了设备绑定但没有登录校验", "app", app.AppID)
		h.writeErr(w, "unavailable", "")
		return "", "", false
	}
	clientRef, err = h.d.Check.Ask(r.Context(), app, header, ipOf(r))
	if err != nil {
		h.checkErr(w, app, err)
		return "", "", false
	}
	if clientRef == "" {
		slog.Warn("accountapi: 应用放行登录却没给设备标识", "app", app.AppID)
		h.writeErr(w, "unavailable", "")
		return "", "", false
	}
	return devicePub, clientRef, true
}

// checkErr 把应用拒绝登录的码原样给客户端(version_too_low 等它们认得);不认识的码按登录失败,文案用本表的:
// 应用回的文案未必是给终端用户看的。
func (h *handler) checkErr(w http.ResponseWriter, app *domain.Application, err error) {
	code, _, de := asDomain(err)
	code = strings.ToLower(code)
	switch {
	case de == nil, code == "unavailable", errors.Is(err, domain.ErrInternal):
		slog.Warn("accountapi: 登录校验不可用", "app", app.AppID, "err", err)
		h.writeErr(w, "unavailable", "")
	case code == "device_unverified":
		h.writeErr(w, "device_unverified", "")
	default:
		if _, known := errTable[code]; known {
			h.writeErr(w, code, "")
			return
		}
		slog.Info("accountapi: 应用拒绝登录", "app", app.AppID, "code", code)
		h.writeErr(w, "login_failed", "")
	}
}
