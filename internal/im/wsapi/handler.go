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

// New 建 wsapi 的 http.Handler。Cfg 里没显式配置的字段给出保守但可用的
// 默认值，而不是让零值直接生效——这几个零值不是"关闭某个功能"这种无害
// 的默认，是能直接把网关跑废的地雷：SendQueue=0 会建出无缓冲的发送队列，
// 一条连接能不能收到第一条推送取决于写协程此刻是否正好卡在 select 上
// 等着接，失败了 hub 会立刻以背压关掉这条连接，等效于"随机踢人"；
// AuthTimeout=0 会让 readAuthFrame 里的定时器立刻到期，所有连接握手都
// 秒拒；IdleTimeout=0 同理会让所有连接秒断。目前测不出这几条是因为唯一
// 的调用方是本包自己的测试，测试自己已经把这三个字段填好了——一旦接线
// 任务（cmd 那边组装 Deps）漏填任何一项，会得到"服务起来了但一条连接
// 都活不下来"这种极难定位的现象，所以在这里兜底而不是等到线上再排查。
func New(d Deps) http.Handler {
	if d.Cfg.MaxFrame == 0 {
		d.Cfg.MaxFrame = 1 << 20
	}
	if d.Cfg.AuthTimeout == 0 {
		d.Cfg.AuthTimeout = 5 * time.Second
	}
	if d.Cfg.IdleTimeout == 0 {
		d.Cfg.IdleTimeout = 60 * time.Second
	}
	if d.Cfg.SendQueue == 0 {
		d.Cfg.SendQueue = 256
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
// 读操作放在后台 goroutine 里连续进行，不给 c.ws.Read 的 ctx 加 deadline：
// 和 readAuthFrame 踩过的坑一样，coder/websocket 对 ctx 到期的实现是
// setupReadTimeout 里注册的 context.AfterFunc(ctx, c.close)——到期时直接把
// 整条底层连接强制关掉，不发送任何关闭帧。空闲超时如果直接套在这次 Read
// 的 ctx 上，client 只会看到连接被硬中断，读不到任何状态码，和"被踢
// 4003/策略拒绝 4002"等其它几条拒绝路径比起来语义不自洽。
//
// 改成:空闲计时器到期时，走和 hub 触发的踢人/撤销/背压完全一样的机制——
// 调用 c.Close(CloseIdleTimeout, ReasonTimeout) 发信号给写协程，由写协程
// 去做真正的、优雅的 ws.Close（见 conn.go），读循环借着这次真正的关闭
// 解除阻塞。这样"读循环退出后区分断开原因"完全收敛成一条路径：查
// c.closure()——不管是 hub 从外部踢的，还是这里自己判定空闲超时，都是
// 同一个信号、同一次真正的关闭、同一次 closure() 读取。
func (s *server) readLoop(ctx context.Context, app string, sub model.Subject, c *wsConn) string {
	type readResult struct {
		data []byte
		err  error
	}
	reads := make(chan readResult)
	// done 是后台读协程的退出出口（乙四）。没有它会有一处永久泄漏：
	// 空闲计时器到期与一帧成功读取同时就绪时，select 可能选中计时器分支，
	// 那个分支里的 `<-reads` 消费掉的是那条**成功**的数据而不是错误；
	// 读协程于是继续循环、再读一次、这次拿到关闭带来的错误，然后卡在
	// 对无缓冲 channel 的发送上——readLoop 早已返回，没有任何人会再接收，
	// 这个协程连同它持有的连接对象永久留在内存里。触发窗口是微秒级，
	// 但泄漏是永久的、没有上限：一个跑几个月的节点会攒下不确定的一堆。
	//
	// 不改成带缓冲的 channel：缓冲只是把窗口挪走（连续两帧同样能填满），
	// 而且会让"读到的帧一定被处理"这件事变得含糊。给发送加一条退出分支
	// 才是真正把出口补上。
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			_, data, err := c.ws.Read(ctx)
			select {
			case reads <- readResult{data, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	timer := time.NewTimer(s.Cfg.IdleTimeout)
	defer timer.Stop()
	for {
		select {
		case r := <-reads:
			if r.err != nil {
				if _, reason, ok := c.closure(); ok {
					return reason // 被 hub 主动关闭（踢、撤销、背压），或本函数自己判定的空闲超时
				}
				return model.ReasonClient
			}
			// 收到一帧，空闲计时器重新计时。标准的 Timer.Reset 用法：先
			// Stop，Stop 返回 false 说明 timer 已经触发过或者正好在触发，
			// 这种情况下要把 C 里可能已经放进去的值排空，否则下一轮
			// select 会立刻在 <-timer.C 上误判超时。
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(s.Cfg.IdleTimeout)

			var f model.Frame
			if json.Unmarshal(r.data, &f) != nil {
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
		case <-timer.C:
			c.Close(model.CloseIdleTimeout, model.ReasonTimeout)
			<-reads // 等后台读 goroutine 因为写协程做的那次真正关闭而解除阻塞退出，避免泄漏
			// c.Close 是幂等的：如果 hub 在本地计时器到期的这个极窄窗口里
			// 恰好先一步用别的原因（踢/撤销/背压）关闭了这条连接，上面那次
			// 调用就是空操作，实际生效的仍然是 hub 那次的 code/reason。这里
			// 不能沿用写死的 model.ReasonTimeout，必须和读分支一样重新查一次
			// closure() 记录的真实原因，否则业务方会收到一条原因写错的
			// 断开事件（明明是被踢却报成 timeout）。
			_, reason, _ := c.closure()
			return reason
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
	// 忽略它的返回值是有意的，不是漏掉了错误处理。空闲超时现在也走
	// c.Close(...) 这条信号路径（见 readLoop 的注释），所以只有 client
	// 自己断开/协议错误这一种场景下 c.closure() 的 ok 会是 false，这次
	// 调用才是这条连接第一次、也是唯一一次真正的 ws.Close。
	code, _, ok := c.closure()
	if !ok {
		code = int(websocket.StatusNormalClosure)
	}
	_ = c.ws.Close(websocket.StatusCode(code), reason)
}

// clientIP 取用于访客限流的客户端地址。
//
// 信任代理时取转发头的**最右**一跳，不是最左（乙二）。最左那一跳是
// client 自己写进请求头的内容：nginx 的默认写法
// `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for` 是
// **追加**而不是重写，client 发来的 `X-Forwarded-For: 1.2.3.4` 会原样
// 留在最左边，代理把真实来源追加在它右边。取最左等于把限流键的选择权
// 交给被限流的人：每个请求换一个伪造值就绕过了按 IP 的访客限流，而设计
// 文档第四节 4.1 把这条限流称为"im 对访客的唯一防线"。
//
// 最右一跳是"直接连上本网关的那一跳"亲手追加的、client 改不了的值，
// 所以它是安全的选择。代价是多层代理时它是内层代理的地址而不是真实
// 客户端地址：那一层后面的所有 client 会共用同一个限流桶，结果是**偏严**
// （可能误伤，不会被绕过），这个方向的错误可以接受，反过来不行。要拿到
// 多层代理下真实的客户端地址，正确做法是配置"可信代理跳数/网段"再从右
// 往左剥，那是另一个功能，本版不做——交接文档里写明了部署条件。
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		// 用 Values 而不是 Get：HTTP 允许同名头出现多次，语义上等价于按
		// 顺序逗号拼接，Get 只会返回第一个头——那恰好是最不可信的那一段。
		if ip := rightmostForwarded(r.Header.Values("X-Forwarded-For")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rightmostForwarded 返回若干个 X-Forwarded-For 头里整体最右的那一跳，
// 全都是空白时返回空串。
func rightmostForwarded(values []string) string {
	for i := len(values) - 1; i >= 0; i-- {
		hops := strings.Split(values[i], ",")
		for j := len(hops) - 1; j >= 0; j-- {
			if hop := strings.TrimSpace(hops[j]); hop != "" {
				return hop
			}
		}
	}
	return ""
}
