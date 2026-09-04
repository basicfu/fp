// Package wsapi 是 client 一侧的传输层：ws 升级、握手、读写循环、连接生命周期。
package wsapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type Handshaker interface {
	Handshake(ctx context.Context, app, subject, connID string, meta model.ConnMeta, policy model.Policy, limit int, live []string) (registry.HandshakeResult, error)
	Remove(ctx context.Context, app, subject, connID string) error
}

type Liveness interface {
	NodeID() string
	LiveNodes() []string
}

type Config struct {
	AuthTimeout time.Duration // 连接后等第一帧的时间
	IdleTimeout time.Duration // 无任何帧则关闭
	SendQueue   int
	MaxFrame    int64
	TrustProxy  bool // 是否信任 X-Forwarded-For 的第一跳
}

type Deps struct {
	Hub    *hub.Hub
	Conns  Handshaker
	Live   Liveness
	Auth   auth.Authenticator
	Apps   auth.AppConfigSource
	Guests *GuestLimiter
	Cfg    Config
}

type server struct{ Deps }

func New(d Deps) http.Handler {
	if d.Cfg.MaxFrame == 0 {
		d.Cfg.MaxFrame = 1 << 20
	}
	return &server{Deps: d}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 不校验 Origin：身份靠第一帧里的 token，不靠 cookie，跨站 ws 拿不到 token 也就没有 CSRF 面。
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(s.Cfg.MaxFrame)
	ctx := r.Context()

	af, ok := s.readAuthFrame(ctx, ws)
	if !ok {
		_ = ws.Close(websocket.StatusCode(model.CloseAuthFailed), "auth frame")
		return
	}
	appCfg, ok := s.Apps.Get(af.App)
	if !ok {
		_ = ws.Close(websocket.StatusCode(model.CloseAuthFailed), "unknown app")
		return
	}
	sub, code := s.resolveSubject(ctx, af, appCfg, clientIP(r, s.Cfg.TrustProxy))
	if code != 0 {
		_ = ws.Close(websocket.StatusCode(code), "auth")
		return
	}
	osName, mobile := af.OS, af.Mobile != nil && *af.Mobile
	if osName == "" {
		osName, mobile = model.ParseUA(af.UA)
	}
	connID, err := uuid.NewV7()
	if err != nil {
		_ = ws.Close(websocket.StatusCode(model.CloseUnavailable), "id")
		return
	}
	meta := model.ConnMeta{Node: s.Live.NodeID(), OS: osName, Mobile: mobile, At: time.Now().UnixMilli()}
	res, err := s.Conns.Handshake(ctx, af.App, sub.String(), connID.String(), meta, appCfg.ConnPolicy, appCfg.ConnLimit, s.Live.LiveNodes())
	if err != nil {
		slog.Warn("wsapi: 握手脚本失败", "app", af.App, "err", err)
		_ = ws.Close(websocket.StatusCode(model.CloseUnavailable), "registry")
		return
	}
	if res.Rejected {
		_ = ws.Close(websocket.StatusCode(model.ClosePolicyRejected), "policy")
		return
	}
	// 顺序：先跑握手脚本拿到裁决，再顶掉旧连接，再登记进路由器，最后回 hello 帧。
	s.Hub.Evict(ctx, af.App, sub, res.Kicked, model.ReasonReplaced)

	c := newWsConn(connID.String(), af.Token, ws, s.Cfg.SendQueue)
	s.Hub.AddConn(ctx, af.App, sub, c, meta, af.UA)
	hello, _ := json.Marshal(model.Frame{T: model.FrameHello, Conn: c.id})
	// 这一次 ws.Write 是安全的：writeLoop 还没启动（下面才 go c.writeLoop），
	// 此刻只有当前协程会碰这条 ws，不存在两个协程同时写的风险。
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		s.teardown(af.App, sub, c, model.ReasonClient)
		return
	}
	wctx, cancelWrite := context.WithCancel(ctx)
	go c.writeLoop(wctx)

	reason := s.readLoop(ctx, af.App, sub, c)
	cancelWrite()
	s.teardown(af.App, sub, c, reason)
}

// readAuthFrame 等第一帧，超过 AuthTimeout 没等到就返回 ok=false。
//
// 没有直接把 AuthTimeout 套在 ws.Read 的 ctx 上：coder/websocket 对"ctx
// 到期"的实现是 setupReadTimeout 里注册的 context.AfterFunc(ctx, c.close)
// ——到期时直接把整条底层连接强制关掉（不发送任何关闭帧），而不是只让
// 这一次 Read 调用返回错误、连接本身还活着。真给它套一个会到期的 ctx，
// 一旦真的超时，ServeHTTP 里紧接着那次"优雅关闭并带上 4001"的 ws.Close
// 调用就已经晚了：底层连接已经被强制断开，Close 会因为
// c.isClosed()==true 直接短路成 no-op，client 只会看到连接被硬中断，
// 读不到任何状态码——这个和"握手超时要以 4001 拒绝"的要求直接冲突。
//
// 改成:用不带 deadline 的 ctx 在后台读，这里只是等一个定时器；定时器先到
// 就直接返回"没读到"，真正的关闭（带 4001）交给 ServeHTTP 里统一的
// `if !ok { ws.Close(...) }` 分支去做——那次 Close 本身会让还卡着的
// 后台读 goroutine 解除阻塞退出，不会泄漏，也不会有第二个协程抢着关闭
// 同一条 ws（读 goroutine 只读不写/不关）。
func (s *server) readAuthFrame(ctx context.Context, ws *websocket.Conn) (model.AuthFrame, bool) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		_, data, err := ws.Read(ctx)
		done <- result{data, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return model.AuthFrame{}, false
		}
		var af model.AuthFrame
		// 握手帧必须带 app：fp 签发的 token 不透明，没有 claim，网关要先知道
		// 用哪个 app 的凭据去验它，缺 app 直接拒。
		if json.Unmarshal(r.data, &af) != nil || af.T != model.FrameAuth || af.App == "" {
			return model.AuthFrame{}, false
		}
		return af, true
	case <-time.After(s.Cfg.AuthTimeout):
		return model.AuthFrame{}, false
	}
}

// resolveSubject 返回 subject，或非零的关闭码。
func (s *server) resolveSubject(ctx context.Context, af model.AuthFrame, cfg model.AppConfig, ip string) (model.Subject, int) {
	switch {
	case af.Token != "":
		sub, err := s.Auth.Verify(ctx, af.App, af.Token)
		if errors.Is(err, auth.ErrUnavailable) {
			return model.Subject{}, model.CloseUnavailable
		}
		if err != nil {
			return model.Subject{}, model.CloseAuthFailed
		}
		return sub, 0
	case af.Guest != "":
		if !cfg.AllowGuest || !model.IsUUIDv4(af.Guest) {
			return model.Subject{}, model.CloseAuthFailed
		}
		if !s.Guests.Allow(af.App, ip, af.Guest, cfg.GuestIPRate, time.Now()) {
			return model.Subject{}, model.CloseAuthFailed
		}
		return model.Guest(af.Guest), 0
	}
	return model.Subject{}, model.CloseAuthFailed
}

// readLoop 读到连接结束，返回断开原因。
//
// 每轮读都给 ctx 单独套一个 IdleTimeout 的 deadline，不额外起定时器：
// 一旦读超时，ws.Read 自己就会带着 context.DeadlineExceeded 返回。
//
// 读到错误时先看 c.closure()：如果这条连接是被 hub 主动关闭的（踢、撤销、
// 背压——见 conn.go 里写协程唯一发起 ws.Close 的那个分支），
// c.closure() 会先于超时/协议错误观察到，用它记录的原因；否则再区分是
// 超时还是 client 自己断开/协议错误。
func (s *server) readLoop(ctx context.Context, app string, sub model.Subject, c *wsConn) string {
	for {
		rctx, cancel := context.WithTimeout(ctx, s.Cfg.IdleTimeout)
		_, data, err := c.ws.Read(rctx)
		cancel()
		if err != nil {
			if _, reason, ok := c.closure(); ok {
				return reason // 被 hub 主动关闭（踢、撤销、背压）
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return model.ReasonTimeout
			}
			return model.ReasonClient
		}
		var f model.Frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch f.T {
		case model.FrameMsg:
			if len(f.P) == 0 {
				continue
			}
			s.Hub.Deliver(ctx, bus.Envelope{Type: bus.TypeUp, App: app, Subject: sub.String(), ConnID: c.id, Payload: []byte(f.P)})
		case model.FramePing:
			// pongFrame 已经是完整帧，走 enqueue 而不是 Send：Send 会再用
			// msgFrame 包一层，把 {"t":"pong"} 变成
			// {"t":"msg","p":{"t":"pong"}}，client 就认不出这是心跳回复了。
			c.enqueue(pongFrame)
		}
	}
}

// teardown 的顺序：先从注册表删（别的节点尽早停止喊我），再从 hub 删并发事件，最后关 ws。
func (s *server) teardown(app string, sub model.Subject, c *wsConn, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Conns.Remove(ctx, app, sub.String(), c.id); err != nil {
		slog.Warn("wsapi: 注册表删除失败", "app", app, "conn", c.id, "err", err)
	}
	s.Hub.RemoveConn(ctx, app, sub, c.id, reason)
	// 这里的 ws.Close 多数情况下是第二次调用（写协程已经在 conn.go 里替
	// hub 触发的关闭做过一次真正的 ws.Close 了）：coder/websocket 的 Close
	// 对重复调用是幂等的（"Additional calls to Close are no-ops"），
	// 忽略它的返回值是有意的，不是漏掉了错误处理。只有 client 自己断开/
	// 空闲超时/协议错误这几种场景下 c.closure() 的 ok 会是 false，这次
	// 调用才是这条连接第一次、也是唯一一次真正的 ws.Close。
	code, _, ok := c.closure()
	if !ok {
		code = int(websocket.StatusNormalClosure)
	}
	_ = c.ws.Close(websocket.StatusCode(code), reason)
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
