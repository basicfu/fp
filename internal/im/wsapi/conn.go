package wsapi

import (
	"context"
	"sync"

	"github.com/coder/websocket"
)

// wsConn 实现 hub.Conn。Send 非阻塞地进发送队列；队列满返回 false，由 hub 决定关闭。
//
// Close 只记录关闭意图（code/reason）并关闭 closed channel 唤醒写协程，
// 真正的 ws.Close 由写协程做——这是全文件唯一允许发起 ws 级别关闭的地方，
// 保证不会有第二个协程在写协程尚未察觉的情况下抢先操作同一条 ws。
//
// 之所以不能让调用 Close 的那个协程（可能是处理 KICK 信封的 goroutine，
// 与这条连接自己的读/写循环毫不相干）直接调 c.ws.Close：写协程此刻可能
// 正卡在 c.ws.Write 里，两个协程同时对同一个 *websocket.Conn 发起写/关闭
// 操作，即使库内部有锁保证不崩溃，也不是这里想要的时序——真正让读循环
// 立刻醒来（c.ws.Read 阻塞的那次调用）的手段就是让写协程亲自调用一次
// ws.Close：coder/websocket 文档保证 "Close will unblock all goroutines
// interacting with the connection once complete"，读循环会带着某个非 nil
// 错误从 Read 返回，再靠 closure() 里已经记好的 code/reason 判断这是一次
// 主动关闭，而不需要读循环自己再猜一次错误类型。
type wsConn struct {
	id, token string
	ws        *websocket.Conn
	sendq     chan []byte

	once   sync.Once
	closed chan struct{}
	code   int
	reason string
}

func newWsConn(id, token string, ws *websocket.Conn, queue int) *wsConn {
	return &wsConn{id: id, token: token, ws: ws, sendq: make(chan []byte, queue), closed: make(chan struct{})}
}

func (c *wsConn) ID() string    { return c.id }
func (c *wsConn) Token() string { return c.token }

// Send 实现 hub.Conn：hub 传进来的 payload 是不透明的业务消息体，client
// 侧要靠 model.Frame.T 分辨这是一条 msg 而不是 hello/pong，所以这里要先
// 用 msgFrame 包一层，而不是把裸 payload 直接丢上线。这是简报里定义了
// msgFrame 却没有任何地方调用它的一个遗漏：不补上的话，hub 通过 Deliver/
// Push 推给这条连接的每一条消息，client 收到的都是缺了 "t" 字段的裸
// JSON，没法按 model.Frame 解析出 t=="msg"。
func (c *wsConn) Send(payload []byte) bool {
	return c.enqueue(msgFrame(payload))
}

// enqueue 把已经是最终帧格式的字节塞进发送队列，供 wsapi 内部直接发送
// 已经封装好的控制帧（比如 pong）用，不经过 Send 的 msgFrame 包装——
// pongFrame 本身就是 `{"t":"pong"}`，再包一层会把它变成
// `{"t":"msg","p":{"t":"pong"}}`，client 会把它误判成一条业务消息。
func (c *wsConn) enqueue(frame []byte) bool {
	select {
	case c.sendq <- frame:
		return true
	default:
		return false
	}
}

// Close 幂等：kicked、背压、正常断开可能并发调用，only Do 保证只有第一次
// 调用的 code/reason 生效，后续调用直接返回。
func (c *wsConn) Close(code int, reason string) {
	c.once.Do(func() {
		c.code, c.reason = code, reason
		close(c.closed)
	})
}

// closure 返回 Close 记录的关闭码与原因；未被主动关闭时 ok=false。
func (c *wsConn) closure() (code int, reason string, ok bool) {
	select {
	case <-c.closed:
		return c.code, c.reason, true
	default:
		return 0, "", false
	}
}

// writeLoop 把队列里的 payload 包成 msg 帧写出去，直到被关闭或写失败。
//
// 唯一的写者：这个 goroutine 是本连接上下文里唯一调用 c.ws.Write/Close 的
// 地方（握手阶段的 hello 帧例外——那时 writeLoop 还没启动，见 handler.go
// 里先写 hello 再 go writeLoop 的顺序），读循环只读不写。
func (c *wsConn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			// 真正的 ws 级别关闭放在这里做，而不是 Close() 里：Close 可能被
			// 任意 goroutine 调用（处理 KICK/撤销/背压的那个 goroutine 与
			// 这条连接的读写循环毫不相干），如果谁调用 Close 谁就直接操作
			// ws，就会和这里的 Write 产生"两个协程同时碰同一条 ws"的
			// 竞争。把动作收敛到写协程自己身上，既满足"唯一写者"的约束，
			// 又能让阻塞在 c.ws.Read 里的读循环借着这次真正的关闭立刻
			// 醒来，不必等到下一次空闲超时。
			code, reason, _ := c.closure()
			_ = c.ws.Close(websocket.StatusCode(code), reason)
			return
		case p := <-c.sendq:
			if err := c.ws.Write(ctx, websocket.MessageText, p); err != nil {
				return
			}
		}
	}
}

// msgFrame 手拼 {"t":"msg","p":<payload>}：payload 已是 JSON，走 json.Marshal 会多一次拷贝与转义。
func msgFrame(payload []byte) []byte {
	b := make([]byte, 0, len(payload)+16)
	b = append(b, `{"t":"msg","p":`...)
	b = append(b, payload...)
	return append(b, '}')
}

var pongFrame = []byte(`{"t":"pong"}`)
