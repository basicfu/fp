package fpim

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// 与 internal/im/model/frame.go 的同名常量值必须完全一致：这份是给第三方
// 接入方用的 client SDK，sdk/ 不得 import internal/（见 sdk/arch_test.go），
// 所以关闭码只能在这个包里再定义一份，一致性靠两边各自的测试守着，没有
// 编译器能替我们查这件事。
//
// 每个码后面的注释是"收到之后 client 该怎么做"，这是上一轮定案的契约，
// 不是随便写写：这几条判断直接决定 runLoop 是否要重连、要不要退避。
const (
	// CloseAuthFailed：认证失败（token 无效/过期）。不自动重连：拿着旧
	// token 或旧访客身份立刻重连只会再被拒一次，交给调用方重新登录换新
	// 凭据。
	CloseAuthFailed = 4001
	// ClosePolicyRejected：被这个 app 的连接策略拒绝（reject 下已有连接、
	// 或 limit 下连接数已满）。不自动重连：策略状态在服务端，不重新登录
	// 也不会变，立即重试大概率仍是同样的拒绝。
	ClosePolicyRejected = 4002
	// CloseKicked：被顶替或被业务方主动踢下线。不自动重连：连接本身没有
	// 错，是否重连、要不要提示用户属于业务语义，交给调用方决定，不替它
	// 做主。
	CloseKicked = 4003
	// CloseUnavailable：网关依赖的后端（身份服务/注册表）暂时不可用。
	// 退避后重连：和 client 自身无关，等一等大概率恢复。
	CloseUnavailable = 4004
	// CloseIdleTimeout：连接空闲太久被网关主动清理，服务端一切正常。
	// 立即重连，不要退避：这不是故障，没有理由像遇到 4004/1013 那样等待。
	CloseIdleTimeout = 4005
	// CloseBackpressure：发送队列跟不上，网关主动断开避免无限堆积。
	// 退避后重连，且这条关闭码本身意味着断连期间有消息没送达——调用方
	// 应该在 OnClose 回调里看到这个码之后，向业务服务拉一次历史补漏，
	// SDK 自己不知道"历史"是什么、该从哪个 REST/gRPC 接口拉，做不了这件
	// 事，只能把码原样交出去。
	CloseBackpressure = 1013
)

// PingInterval 是 client 在一条已建立的连接上发心跳帧的间隔。
//
// 它与网关的空闲超时（FP_IM_CONN_IDLE_TIMEOUT，默认 60 秒）是一对配对
// 常量，关系与 KeepaliveTime/imgrpc.KeepaliveMinTime 完全同构：空闲超时
// 必须明显大于这个间隔，否则全网每个 client 都会被周期性地空闲超时踢下线
// ——而 4005 的契约恰恰是"立即重连、不退避"，于是形成一场稳定的重连风暴。
// 把 20 秒配进空闲超时就足以触发这件事。
//
// 这条配对关系分处 sdk/im（不得 import internal/）与 internal/im/config
// 两个包，任何一边单独看都只是一个孤立的时长常量，改错了 go build/vet/
// 各自包的测试照样全绿；只有 internal/integration 里同时看得见两边的
// TestClientPingAndIdleTimeoutPairing 能守住它。导出它而不是留成 runLoop
// 里的一个字面量，正是为了让那条配对测试有东西可断言。
const PingInterval = 25 * time.Second

// noAutoReconnect 报告收到某个关闭码之后是否应该放弃自动重连，交由调用方
// 决定下一步（重新登录 / 认下被踢 / 检查连接策略），而不是反复撞门。
func noAutoReconnect(code int) bool {
	return code == CloseAuthFailed || code == ClosePolicyRejected || code == CloseKicked
}

// ClientConfig 是 Dial 的全部配置。
type ClientConfig struct {
	// URL 是网关地址，形如 "wss://host/ws"。
	URL string
	// App 必须给：身份平台签发的 token 不透明、没有声明字段，网关要先
	// 知道用哪个 app 的凭据去验它，握手帧缺 app 会被网关直接拒绝
	// （见 internal/im/wsapi/handler.go 的 readAuthFrame）。
	App string
	// Token / Guest 二选一。Guest 必须是标准写法的 uuid v4，由调用方自己
	// 生成并持久化。
	Token string
	Guest string
	// UA / OS / Mobile 是设备标识，进网关的 hub 事件供业务方观测。OS 留空
	// 时网关会从 UA 反推，两者都可以只给一个。
	UA     string
	OS     string
	Mobile bool
	// Logger 为 nil 时用 slog.Default()。
	Logger *slog.Logger
}

func (cfg ClientConfig) validate() error {
	switch {
	case cfg.URL == "":
		return errors.New("fpim: ClientConfig.URL 不能为空")
	case cfg.App == "":
		return errors.New("fpim: ClientConfig.App 不能为空")
	case cfg.Token == "" && cfg.Guest == "":
		return errors.New("fpim: ClientConfig.Token 与 Guest 必须给一个")
	}
	return nil
}

// frame 是握手之后的通用帧，与 internal/im/model.Frame 字节级一致。
type frame struct {
	T    string          `json:"t"`
	Conn string          `json:"conn,omitempty"`
	P    json.RawMessage `json:"p,omitempty"`
}

// Client 是设备/原生程序接入 fp-im 网关的连接：握手、心跳、断线退避重连
// 都在内部完成，调用方只需要 OnMessage/OnClose/Send。
//
// 一个 Client 对应一条逻辑连接（断线后重连复用同一个 Client），不是并发
// 安全地给多个逻辑连接共用；但 Client 本身的方法可以被任意 goroutine
// 并发调用。
type Client struct {
	cfg ClientConfig
	log *slog.Logger

	// onMessage / onClose 可能在任意时刻被调用方设置，读循环与心跳协程在
	// 读——与 sdk/im/server.go 的 Server.onMessage 同样的理由：读发生在
	// 热路径上，原子指针比每次读都抢锁更便宜，写入通常只在启动时发生
	// 一次，不需要与谁互斥。
	onMessage atomic.Pointer[func([]byte)]
	onClose   atomic.Pointer[func(int)]

	// wsMu 序列化对底层 ws 的写入，并保护 ws 字段本身的读写：调用方的
	// Send、心跳协程的 ping、重连时替换连接，三处都会碰 ws，任何一处
	// 漏锁都会在"重连时调用方正好在发送"这类场景下把两条写操作交叉
	// 写进底层 TCP 帧，产生一条对端解不出的损坏帧——这与 sdk/im/server.go
	// Server.sendMu 是同一类坑，写在这里而不是拆成两把锁，是因为这里的
	// "替换连接"和"写入连接"本来就必须互斥（不能有人拿着旧连接的指针
	// 还在写，同时另一个协程已经把 c.ws 换成新连接），拆成两把锁反而要
	// 多一层协调。
	wsMu sync.Mutex
	ws   *websocket.Conn

	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed atomic.Bool
	// gaveUp 记录"自动重连已被永久放弃"。调用方自己 Close 不算（那是它
	// 自己的决定），只有撞上 noAutoReconnect 的关闭码才算。见 GaveUp。
	gaveUp atomic.Bool
}

// Dial 拨号、发握手帧、等 hello 回执，三步都同步完成——任何一步失败都
// 直接返回错误，不进重连循环，调用方能立刻分辨是配置错了还是网络不通，
// 而不是先拿到一个"看起来成功"的 *Client，过一会儿才通过 OnClose 得知
// 握手其实失败了。
//
// 握手成功之后的断线由内部按 CloseXxx 常量的契约决定是否重连、要不要
// 退避，调用方不需要关心。
func Dial(ctx context.Context, cfg ClientConfig) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	c := &Client{cfg: cfg, log: cfg.Logger}
	ws, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	// 用独立于调用方传入的 ctx 的后台 ctx：Dial 的 ctx 通常只是"这次拨号
	// 的超时/取消范围"，调用方拨完号往往会让它过期或取消，但连接本身
	// 应该继续活着直到调用方显式 Close，不能被 Dial 那个 ctx 的生命周期
	// 误伤。
	rctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.setWS(ws)
	c.wg.Add(1)
	go c.runLoop(rctx, ws)
	return c, nil
}

// OnMessage 注册收到推送时的回调，参数是原样透传的 payload（对应网关帧
// 里的 p 字段）。可以在任意时刻调用，包括在读循环已经在跑的时候。
func (c *Client) OnMessage(fn func([]byte)) { c.onMessage.Store(&fn) }

// OnClose 注册每次连接结束时的回调，参数是关闭码（见 CloseXxx 常量；读
// 不出关闭码时是 -1）。同一个 Client 生命周期里可能被调用多次——每次
// 断线重连都会触发一次，不止第一次。
func (c *Client) OnClose(fn func(code int)) { c.onClose.Store(&fn) }

// GaveUp 报告 client 是否已经永久放弃自动重连。
//
// 为真只有一个原因：收到了 noAutoReconnect 的关闭码（4001 认证失败、
// 4002 策略拒绝、4003 被踢），SDK 按契约不再重试，等调用方处理（重新
// 登录换凭据 / 认下被踢 / 检查连接策略）。调用方自己 Close 不算——那是
// 它自己的决定，不需要 SDK 再报告一次。
//
// 为什么需要这个查询方法（甲四）：撞上这三个码时最要命的一种情形是它
// 发生在**重连尝试**里。那次失败不属于任何一条已建立的连接，调用方在
// OnClose 里看到的只会是上一次断开的码（节点崩溃时是 -1），而 client
// 已经永久停了：之后 Send 只会返回底层 ws 库的原始错误，没有任何一处
// 能告诉调用方"我不会再连回来了"。现在这条放弃会同时通过 OnClose 报出
// 具体的码、并把这个状态位置真，两条路径调用方用哪条都行。
func (c *Client) GaveUp() bool { return c.gaveUp.Load() }

// giveUp 把"永久放弃自动重连"变成调用方能观察到的事实：置状态位、记
// 一条警告日志，notify 为真时再通过 OnClose 把码报出去。
//
// notify 的取舍：已建立连接被以这三个码关闭时，runLoop 上面已经用同一个
// 码回调过一次 OnClose，这里再报一次等于同一件事通知两遍，调用方要么
// 重复处理要么得自己去重；而重连失败这条路径此前一次都没通知过，必须
// 补上。所以由调用点决定，而不是无条件通知。
func (c *Client) giveUp(code int, notify bool) {
	c.gaveUp.Store(true)
	c.log.Warn("fpim: 收到不可重连的关闭码，放弃自动重连，等待调用方处理", "code", code)
	if notify {
		if fn := c.onClose.Load(); fn != nil && !c.closed.Load() {
			(*fn)(code)
		}
	}
}

func (c *Client) setWS(ws *websocket.Conn) {
	c.wsMu.Lock()
	c.ws = ws
	c.wsMu.Unlock()
}

// connect 拨号、发握手帧、等 hello。任何一步失败都返回错误并关掉半成品
// 连接，不留给调用方一条已经废掉但没关闭的 ws。
func (c *Client) connect(ctx context.Context) (*websocket.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, c.cfg.URL, nil)
	if err != nil {
		return nil, err
	}
	// 握手帧必须带 app：身份平台签发的是不透明令牌，没有声明字段，网关
	// 必须先知道用哪个 app 的凭据去验它，缺 app 网关会直接以 4001 拒绝
	// （见 internal/im/wsapi/handler.go 的 readAuthFrame）。
	auth := map[string]any{"t": "auth", "app": c.cfg.App}
	if c.cfg.Token != "" {
		auth["token"] = c.cfg.Token
	} else {
		auth["guest"] = c.cfg.Guest
	}
	if c.cfg.UA != "" {
		auth["ua"] = c.cfg.UA
	}
	if c.cfg.OS != "" {
		auth["os"] = c.cfg.OS
	}
	// mobile 显式带上而不是靠零值——false 也是一个明确的系统标识（"这是
	// 桌面/非移动端"），不带这个字段网关会退回到从 UA 反推，那是给不了
	// OS 的调用方准备的兜底路径，不该在给了 OS 的情况下也去猜。
	if c.cfg.OS != "" || c.cfg.UA != "" {
		auth["mobile"] = c.cfg.Mobile
	}
	b, err := json.Marshal(auth)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	if err := ws.Write(dctx, websocket.MessageText, b); err != nil {
		ws.CloseNow()
		return nil, err
	}
	_, data, err := ws.Read(dctx)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	var f frame
	if json.Unmarshal(data, &f) != nil || f.T != "hello" {
		ws.CloseNow()
		return nil, errors.New("fpim: 握手未收到 hello")
	}
	return ws, nil
}

// runLoop 读到断开、通知 OnClose，再按关闭码的契约决定是否重连、要不要
// 退避，直到 ctx 被取消（Close 调用）或撞上 noAutoReconnect 的码。
func (c *Client) runLoop(ctx context.Context, ws *websocket.Conn) {
	defer c.wg.Done()
	const (
		minBackoff = 200 * time.Millisecond
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff

	for {
		code := c.readUntilClosed(ctx, ws)
		if fn := c.onClose.Load(); fn != nil && !c.closed.Load() {
			(*fn)(code)
		}
		if c.closed.Load() || ctx.Err() != nil {
			return
		}
		if noAutoReconnect(code) {
			// 这条路径上 OnClose 刚刚已经带着同一个 code 回调过了，所以
			// 只置状态位、不再通知一次。
			c.giveUp(code, false)
			return
		}

		// 4005（空闲超时）第一次尝试立即重连、不退避：这不是故障，见
		// CloseIdleTimeout 的注释。第一次失败之后仍然按正常节奏退避——
		// 空闲超时只说明"上一条连接该重连了"，不代表这次重连本身也会
		// 立刻成功。
		wait := backoff
		if code == CloseIdleTimeout {
			wait = 0
		}
		for {
			if wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			if ctx.Err() != nil {
				return
			}
			next, err := c.connect(ctx)
			if err == nil {
				ws = next
				c.setWS(ws)
				backoff = minBackoff
				break
			}
			if st := int(websocket.CloseStatus(err)); noAutoReconnect(st) {
				// 这次放弃发生在重连尝试里，不属于任何一条已建立的连接：
				// 调用方此前只收到过上一次断开的码（节点崩溃时是 -1），
				// 如果这里也一声不吭地 return，它永远不会知道 client 已经
				// 停了。必须通知（notify=true），同时置 GaveUp 状态位。
				c.giveUp(st, true)
				return
			}
			c.log.Warn("fpim: 重连失败", "err", err)
			wait = backoff
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// readUntilClosed 读消息、按需回 ping，直到连接结束，返回关闭码（读不出
// 关闭码时是 -1）。
func (c *Client) readUntilClosed(ctx context.Context, ws *websocket.Conn) int {
	// 心跳协程的生命周期绑定这一条连接：pingCtx 派生自本次调用的 ctx，
	// defer stopPing() 在 readUntilClosed 返回（即这条连接断开）时立刻
	// 取消它。下一轮重连会再起一个新的心跳协程绑定新连接，旧的这个已经
	// 退出——不会在重连多次之后累积出一堆还在跑的心跳协程。Close() 调用
	// 时外层 ctx（rctx）被取消，pingCtx 作为它的派生 ctx 也会一起结束。
	pingCtx, stopPing := context.WithCancel(ctx)
	var pingWG sync.WaitGroup
	pingWG.Add(1)
	// defer 的顺序很关键：先 stopPing() 让心跳协程的 select 命中
	// pingCtx.Done() 分支退出，再 pingWG.Wait() 等它真正退出之后
	// readUntilClosed 才返回——这样"这条连接的心跳协程已经退出"这件事
	// 不依赖调度器的运气，是可以观察到的事实，而不是"取消了应该很快
	// 就会退出"这种一厢情愿。
	defer func() {
		stopPing()
		pingWG.Wait()
	}()
	go func() {
		defer pingWG.Done()
		t := time.NewTicker(PingInterval)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
				c.wsMu.Lock()
				_ = ws.Write(pingCtx, websocket.MessageText, []byte(`{"t":"ping"}`))
				c.wsMu.Unlock()
			}
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return int(websocket.CloseStatus(err))
		}
		var f frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.T == "msg" {
			if fn := c.onMessage.Load(); fn != nil {
				(*fn)([]byte(f.P))
			}
		}
	}
}

// Send 只表示 payload 已经写入底层 ws 连接，不代表对端（网关，更不用说
// 最终的业务服务）已经收到——网络中间任何一段都可能丢包，ws 写成功只是
// "交给了本机内核的发送缓冲区"。可靠投递（"对端确实处理过"这件事）不是
// 这层能保证的，需要业务层自己在 payload 里带上唯一 id，按"发送方记 id、
// 超时未确认就重发、接收方按 id 去重"的配方实现，SDK 不替业务做这个决定
// ——重发策略、超时多久算超时、要不要限制重试次数，都是业务语义。
func (c *Client) Send(ctx context.Context, payload []byte) error {
	if !json.Valid(payload) {
		return ErrBadPayload
	}
	// Close() 之后 c.ws 仍然指向那条已经被关掉的连接（Close 没有把它置
	// nil，见 Close 的注释），不专门判断 closed 的话，这里会把 ws 库对
	// "写一条已关闭连接"给出的原始错误直接透传出去——那是库的实现细节，
	// 不是这层想暴露的稳定契约。显式查 closed，保证关闭之后的 Send 总是
	// 确定地返回 ErrUnavailable，不多不少。
	if c.closed.Load() {
		return ErrUnavailable
	}
	b := make([]byte, 0, len(payload)+16)
	b = append(b, `{"t":"msg","p":`...)
	b = append(b, payload...)
	b = append(b, '}')

	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.ws == nil {
		return ErrUnavailable
	}
	return c.ws.Write(ctx, websocket.MessageText, b)
}

// Close 关闭连接并停止后台重连循环，等所有内部协程（读循环、心跳）退出
// 之后返回。之后再调用 Send 会返回 ErrUnavailable，不会 panic 也不会
// 挂起。
func (c *Client) Close() error {
	c.closed.Store(true)
	c.cancel()
	c.wsMu.Lock()
	ws := c.ws
	c.wsMu.Unlock()
	var err error
	if ws != nil {
		err = ws.Close(websocket.StatusNormalClosure, "bye")
	}
	c.wg.Wait()
	return err
}
