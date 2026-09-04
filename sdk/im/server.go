package fpim

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// KeepaliveTime 是 Server 向 fp-im 发送 keepalive ping 的间隔。
//
// 必须**大于** internal/im/imgrpc 服务端的 KeepaliveMinTime（10 秒），否则
// 服务端会认为客户端 ping 过频，回一个 ENHANCE_YOUR_CALM 的 GOAWAY 把连接
// 掐掉——与 sdk/client.go 的 KeepaliveTime 是完全同构的配对关系，见那边的
// 注释。这条配对关系分处两个包（sdk 不得 import internal/，
// internal/im/imgrpc 也不该反过来依赖 sdk 的实现细节），任何一边单独看
// 都只是一个孤立的时长常量，改错了 go build/vet/全量测试照样全绿，只有
// internal/integration 里同时看得到两边的测试才能守住这条关系。
const KeepaliveTime = 30 * time.Second

var (
	// ErrUnavailable 表示与 fp-im 的流当前不可用：流还没建立、正在重连，
	// 或者调用发起之后流断开了。业务方应当把它当作"稍后重试"处理。
	ErrUnavailable = errors.New("fpim: 与 fp-im 的流不可用")
	// ErrBadPayload 表示传入的 payload 不是合法 JSON。
	//
	// fp-im 网关本身不解析 payload（原样透传），但 client 与网关之间的帧
	// 是 JSON 的，一个非法 JSON 的 payload 会让承载它的整条帧本身就不是
	// 合法 JSON，从而让 client 完全解不出这一帧——所以校验必须在 SDK 这层
	// 做在发送之前，而不是指望 fp-im 或 client 兜底。
	ErrBadPayload = errors.New("fpim: payload 必须是合法 JSON")
)

// ServerConfig 是 Server 的全部配置。用普通结构体按值传入，不用函数式
// 选项——与 sdk/options.go 的 Options 是同样的先例。
type ServerConfig struct {
	// Addr 是 fp-im 的 gRPC 地址，形如 "fp-im.internal:9090"。
	Addr string
	// AppID / AppSecret 是应用凭据，与身份平台（fpsdk）用同一对——业务方
	// 配一份凭据就能同时连身份平台和 fp-im 网关，metadata 键也完全相同
	// （fp-app-id / fp-app-secret）。
	AppID     string
	AppSecret string

	// Insecure 允许明文连接。生产绝不要开，见 sdk/options.go 里 Insecure
	// 字段的同一条理由：凭据会随每个 RPC 的 metadata 明文发送。
	Insecure bool
	// TLSConfig 自定义 TLS 配置。为 nil 且 Insecure 为 false 时用系统根证书。
	TLSConfig *tls.Config

	// RequestTimeout 是单次请求（Push/PushMany/Sessions/Kick）的超时。
	// 默认 5 秒。
	RequestTimeout time.Duration

	// Logger 是 SDK 内部日志。为 nil 时用 slog.Default()。
	Logger *slog.Logger
}

const defaultRequestTimeout = 5 * time.Second

func (c *ServerConfig) applyDefaults() {
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = defaultRequestTimeout
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

func (c ServerConfig) validate() error {
	switch {
	case c.Addr == "":
		return errors.New("fpim: ServerConfig.Addr 不能为空")
	case c.AppID == "":
		return errors.New("fpim: ServerConfig.AppID 不能为空")
	case c.AppSecret == "":
		return errors.New("fpim: ServerConfig.AppSecret 不能为空")
	case c.RequestTimeout < 0:
		return errors.New("fpim: ServerConfig.RequestTimeout 不能为负")
	}
	return nil
}

// PushStatus 是单个 subject 的推送结果状态。
type PushStatus int

const (
	Sent PushStatus = iota + 1
	NotOnline
	Unavailable
)

// PushResult 是对一个 subject 推送的结果。
type PushResult struct {
	Subject Subject
	Status  PushStatus
	// Nodes 是收到这条消息的节点数，Status 为 Sent 时 >= 1。
	Nodes int
}

// Session 是某个 subject 名下的一条连接。
type Session struct {
	ConnID      string
	Node        string
	OS          string
	Mobile      bool
	ConnectedAt int64
}

// Inbound 是 client 发上来的一条消息，Payload 原样透传。
type Inbound struct {
	Subject Subject
	ConnID  string
	Payload []byte
}

// EventKind 是连接生命周期事件的类型。
type EventKind int

const (
	EventConnected EventKind = iota + 1
	EventDisconnected
)

// Event 是连接生命周期事件。UA 只在 Connected 里有值，Reason 只在
// Disconnected 里有值。
type Event struct {
	Kind    EventKind
	Subject Subject
	ConnID  string
	OS      string
	Mobile  bool
	UA      string
	Reason  string
	At      int64
}

// appCreds 把应用凭据附加到每个 RPC 的 metadata 上。
//
// 键名与 fpsdk.appCredentials 完全相同（fp-app-id / fp-app-secret）：这两
// 个包不能互相 import（sdk/ 不得 import internal/，两者也没有互相依赖的
// 理由），键名一致是业务方能用同一对凭据同时接入身份平台与 fp-im 网关的
// 唯一保证，只能靠约定 + 两边各自的测试守住，见 ServerConfig.AppID 的注释。
type appCreds struct {
	id       string
	secret   string
	insecure bool
}

func (c appCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"fp-app-id": c.id, "fp-app-secret": c.secret}, nil
}

func (c appCreds) RequireTransportSecurity() bool { return !c.insecure }

// Server 是业务 server 接入 fp-im 网关的入口：发推送、踢人、查会话，
// 并通过 OnMessage/OnEvent 接收 client 发上来的消息与连接事件。
//
// 一个进程建一个即可，Server 本身是并发安全的。
type Server struct {
	cfg  ServerConfig
	conn *grpc.ClientConn
	rpc  fpimv1.ImServiceClient
	log  *slog.Logger

	// onMessage / onEvent 可能在任意时刻被业务方设置，而接收协程在读它，
	// 用 atomic.Pointer 而不是普通字段 + 互斥锁，是因为读取发生在每一条
	// 收到的帧上（热路径），原子指针读比每次都抢锁开销更低，且写入
	// （业务方调用 OnMessage/OnEvent，通常只在启动时发生一次）本来就
	// 不需要与其他写入互斥。
	onMessage atomic.Pointer[func(context.Context, Inbound) error]
	onEvent   atomic.Pointer[func(context.Context, Event)]

	// mu 保护 stream 与 pending：调用方协程（call）读 stream、写
	// pending，接收协程（runOnce）读/删 pending，断线清理（dropStream）
	// 批量清空两者——三处访问者，缺一处上锁都会在并发压测下现出竞态。
	mu      sync.Mutex
	stream  fpimv1.ImService_ConnectClient // 当前可用的流；nil 表示未就绪或已断开
	pending map[string]chan *fpimv1.Result

	// sendMu 序列化对同一条流的 Send 调用。
	//
	// grpc-go 明确要求：可以有一个 goroutine 发送、另一个接收，但不能有
	// 多个 goroutine 同时对同一条流调用 SendMsg。Push/PushMany/Sessions/
	// Kick 都可能被业务方从多个 goroutine 并发调用，且都要经过同一条
	// Connect 流发出请求帧，不加这把锁会在并发调用下损坏底层帧——这类
	// 损坏在本机回环的单元测试里几乎不可能触发（帧小、写入快，竞态窗口
	// 窄到测试很难撞上），但在生产的真实网络延迟下会现形，是这类"两个
	// 目标各自都对、放一起互相拆台"的经典陷阱之一。mu 只保护
	// stream/pending 这两个数据结构本身的一致性，不能顺带当作 Send 的
	// 互斥锁用（那会让收发之间也被迫串行，拖慢接收循环）。
	sendMu sync.Mutex

	up atomic.Bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewServer 建立与 fp-im 的连接并启动接入流。
func NewServer(cfg ServerConfig) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg.applyDefaults()

	transport := credentials.NewTLS(cfg.TLSConfig)
	if cfg.Insecure {
		cfg.Logger.Warn("fpim: 以明文连接 fp-im，appSecret 将以明文传输——只应在开发环境使用")
		transport = insecure.NewCredentials()
	}

	conn, err := grpc.NewClient(cfg.Addr,
		grpc.WithTransportCredentials(transport),
		grpc.WithPerRPCCredentials(appCreds{id: cfg.AppID, secret: cfg.AppSecret, insecure: cfg.Insecure}),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                KeepaliveTime,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:     cfg,
		conn:    conn,
		rpc:     fpimv1.NewImServiceClient(conn),
		log:     cfg.Logger,
		pending: map[string]chan *fpimv1.Result{},
		cancel:  cancel,
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runLoop(ctx)
	}()
	return s, nil
}

// OnMessage 注册接收 client 上行消息的回调。
//
// 回调在接收流的读循环里同步执行，不是另起 goroutine 派发：这保证同一个
// subject 的多条消息，业务侧处理的先后顺序与 fp-im 投递的顺序完全一致——
// 一旦异步派发，两条消息谁先被业务逻辑处理就不再有任何保证。慢操作应由
// 业务方自己在回调里另起 goroutine，回调本身必须快，否则会拖慢整条流后续
// 事件（包括其他 subject 的消息、Result 应答）的处理。
func (s *Server) OnMessage(fn func(context.Context, Inbound) error) { s.onMessage.Store(&fn) }

// OnEvent 注册接收连接生命周期事件的回调，语义与 OnMessage 相同：同步
// 执行，慢操作自行另起 goroutine。
func (s *Server) OnEvent(fn func(context.Context, Event)) { s.onEvent.Store(&fn) }

// StreamHealthy 报告当前接入流是否可用（已收到过 Ready）。
func (s *Server) StreamHealthy() bool { return s.up.Load() }

// Close 关闭连接并停止后台重连循环。
func (s *Server) Close() error {
	s.cancel()
	err := s.conn.Close()
	s.wg.Wait()
	return err
}

// healthyConnDuration 与 sdk/client.go 的同名常量同构：仅仅收到过 Ready
// 不足以断定这次连接是健康的，还要求它至少存活这么久，退避才有资格复位。
// 理由见 sdk/client.go healthyConnDuration 的注释，这里不再重复。
const healthyConnDuration = 5 * time.Second

// runLoop 维持接入流，断开后按退避重连，直到 ctx 取消。
//
// 与 fpsdk.Client.runWatch 同构：退避从 200ms 到 30s 翻倍增长，只有当
// 这次连接收到过 Ready 且存活超过 healthyConnDuration 才把退避复位到
// 最小值，否则继续按原节奏增长。
func (s *Server) runLoop(ctx context.Context) {
	const (
		minBackoff = 200 * time.Millisecond
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff

	for ctx.Err() == nil {
		start := time.Now()
		gotReady := s.runOnce(ctx)

		if ctx.Err() != nil {
			return
		}

		healthy := gotReady && time.Since(start) >= healthyConnDuration
		if healthy {
			backoff = minBackoff
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if !healthy {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// runOnce 建一次流并读到断开为止，返回本次连接是否曾经收到过 Ready。
func (s *Server) runOnce(ctx context.Context) (gotReady bool) {
	stream, err := s.rpc.Connect(ctx)
	if err != nil {
		s.log.Warn("fpim: 建流失败", "err", err)
		return false
	}
	defer s.dropStream()

	for {
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("fpim: 流断开", "err", err)
			}
			return gotReady
		}
		switch b := resp.GetBody().(type) {
		case *fpimv1.ConnectResponse_Ready:
			s.mu.Lock()
			s.stream = stream
			s.mu.Unlock()
			s.up.Store(true)
			gotReady = true
			s.log.Info("fpim: 流就绪", "node", b.Ready.GetNodeId())
		case *fpimv1.ConnectResponse_Result:
			s.mu.Lock()
			ch := s.pending[b.Result.GetReqId()]
			delete(s.pending, b.Result.GetReqId())
			s.mu.Unlock()
			if ch != nil {
				ch <- b.Result
			}
		case *fpimv1.ConnectResponse_Inbound:
			s.handleInbound(ctx, b.Inbound)
		case *fpimv1.ConnectResponse_Event:
			s.handleEvent(ctx, b.Event)
		}
	}
}

func (s *Server) handleInbound(ctx context.Context, in *fpimv1.Inbound) {
	fn := s.onMessage.Load()
	if fn == nil {
		return
	}
	sub, err := Parse(in.GetSubject())
	if err != nil {
		s.log.Warn("fpim: 收到的 Inbound.subject 解析失败", "subject", in.GetSubject(), "err", err)
		return
	}
	// 同步调用：保证同一 subject 的消息处理顺序与 fp-im 投递顺序一致，
	// 见 OnMessage 的注释。
	if err := (*fn)(ctx, Inbound{Subject: sub, ConnID: in.GetConnId(), Payload: in.GetPayload()}); err != nil {
		s.log.Warn("fpim: OnMessage 回调返回错误", "subject", sub, "err", err)
	}
}

func (s *Server) handleEvent(ctx context.Context, ev *fpimv1.Event) {
	fn := s.onEvent.Load()
	if fn == nil {
		return
	}
	sub, err := Parse(ev.GetSubject())
	if err != nil {
		s.log.Warn("fpim: 收到的 Event.subject 解析失败", "subject", ev.GetSubject(), "err", err)
		return
	}
	kind := EventDisconnected
	if ev.GetKind() == fpimv1.EventKind_EVENT_KIND_CONNECTED {
		kind = EventConnected
	}
	(*fn)(ctx, Event{
		Kind:    kind,
		Subject: sub,
		ConnID:  ev.GetConnId(),
		OS:      ev.GetOs(),
		Mobile:  ev.GetMobile(),
		UA:      ev.GetUa(),
		Reason:  ev.GetReason(),
		At:      ev.GetAtMs(),
	})
}

// dropStream 把流标为不可用，并让所有在途请求立刻失败——不能让调用方
// 干等到 RequestTimeout。关闭 channel 而不是往里塞一个哨兵值：调用方
// select 在一个已关闭的 channel 上会立刻返回零值而不是阻塞，天然保证
// "唤醒"这件事本身不会遗漏任何一个在途请求，不依赖调用方与这里谁先谁后。
func (s *Server) dropStream() {
	s.up.Store(false)
	s.mu.Lock()
	s.stream = nil
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	s.mu.Unlock()
}

// call 发一帧请求并等待与之配对的 Result。
func (s *Server) call(ctx context.Context, body func(reqID string) *fpimv1.ConnectRequest) (*fpimv1.Result, error) {
	id := uuid.NewString()
	ch := make(chan *fpimv1.Result, 1)

	s.mu.Lock()
	stream := s.stream
	if stream == nil {
		s.mu.Unlock()
		return nil, ErrUnavailable
	}
	s.pending[id] = ch
	s.mu.Unlock()

	// Send 必须序列化（sendMu），理由见该字段的注释；不能顺带用 s.mu
	// 保护，否则会把收发路径挤成串行。
	s.sendMu.Lock()
	sendErr := stream.Send(body(id))
	s.sendMu.Unlock()
	if sendErr != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, errors.Join(ErrUnavailable, sendErr)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()
	select {
	case res, ok := <-ch:
		if !ok {
			// channel 被 dropStream 关闭：流断开，在途请求立刻失败。
			return nil, ErrUnavailable
		}
		if res.GetError() != "" {
			return nil, errors.New("fpim: " + res.GetError())
		}
		return res, nil
	case <-timeoutCtx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, errors.Join(ErrUnavailable, timeoutCtx.Err())
	}
}

// Push 推送给单个 subject。内部委托给 PushMany，避免重复实现同一套
// 请求构造 / 校验 / 结果解析逻辑。
func (s *Server) Push(ctx context.Context, sub Subject, payload []byte) (PushResult, error) {
	results, err := s.PushMany(ctx, []Subject{sub}, payload)
	if err != nil {
		return PushResult{}, err
	}
	return results[0], nil
}

// PushMany 批量推送给多个 subject。
func (s *Server) PushMany(ctx context.Context, subs []Subject, payload []byte) ([]PushResult, error) {
	if !json.Valid(payload) {
		return nil, ErrBadPayload
	}
	names := make([]string, len(subs))
	for i, sub := range subs {
		names[i] = sub.String()
	}
	res, err := s.call(ctx, func(id string) *fpimv1.ConnectRequest {
		return &fpimv1.ConnectRequest{
			ReqId: id,
			Body: &fpimv1.ConnectRequest_PushMany{PushMany: &fpimv1.PushManyRequest{
				Subjects: names,
				Payload:  payload,
			}},
		}
	})
	if err != nil {
		return nil, err
	}
	out := make([]PushResult, len(res.GetPushes()))
	for i, p := range res.GetPushes() {
		sub, parseErr := Parse(p.GetSubject())
		if parseErr != nil {
			s.log.Warn("fpim: PushResult.subject 解析失败", "subject", p.GetSubject(), "err", parseErr)
		}
		status := Unavailable
		switch p.GetStatus() {
		case fpimv1.PushStatus_PUSH_STATUS_SENT:
			status = Sent
		case fpimv1.PushStatus_PUSH_STATUS_NOT_ONLINE:
			status = NotOnline
		}
		out[i] = PushResult{Subject: sub, Status: status, Nodes: int(p.GetNodes())}
	}
	return out, nil
}

// Sessions 查询某个 subject 名下当前的全部连接。
func (s *Server) Sessions(ctx context.Context, sub Subject) ([]Session, error) {
	res, err := s.call(ctx, func(id string) *fpimv1.ConnectRequest {
		return &fpimv1.ConnectRequest{
			ReqId: id,
			Body:  &fpimv1.ConnectRequest_Sessions{Sessions: &fpimv1.SessionsRequest{Subject: sub.String()}},
		}
	})
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(res.GetSessions()))
	for _, x := range res.GetSessions() {
		out = append(out, Session{
			ConnID:      x.GetConnId(),
			Node:        x.GetNodeId(),
			OS:          x.GetOs(),
			Mobile:      x.GetMobile(),
			ConnectedAt: x.GetConnectedAtMs(),
		})
	}
	return out, nil
}

// Kick 踢掉某个 subject 的连接。connIDs 为空表示踢掉该 subject 的全部连接。
func (s *Server) Kick(ctx context.Context, sub Subject, connIDs ...string) error {
	_, err := s.call(ctx, func(id string) *fpimv1.ConnectRequest {
		return &fpimv1.ConnectRequest{
			ReqId: id,
			Body: &fpimv1.ConnectRequest_Kick{Kick: &fpimv1.KickRequest{
				Subject: sub.String(),
				ConnIds: connIDs,
			}},
		}
	})
	return err
}
