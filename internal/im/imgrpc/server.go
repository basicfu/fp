// Package imgrpc 是业务 server 一侧的传输层：ImService.Connect 双向流。
package imgrpc

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/basicfu/fp/internal/im/hub"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fpim/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// KeepaliveMinTime 与 fpim.KeepaliveTime(30s) 配对：客户端 ping 间隔必须大于这里，否则被 GOAWAY。
// 配对关系由 internal/integration/im_parity_test.go 守着，不要改名或改值。
const KeepaliveMinTime = 10 * time.Second

// defaultWorkers 是 Deps.Workers 为 0 时每条流并发处理请求的上限。
const defaultWorkers = 64

var errStopping = status.Error(codes.Unavailable, "节点正在关闭")

type Deps struct {
	Hub *hub.Hub
	// Creds 核实业务 server 连入时的凭据。fp-im 不再持有任何应用的
	// secret（fp 只存 bcrypt 哈希），只能转给 fp 核实。
	Creds CredentialVerifier
	// CredCacheTTL 为 0 时取 DefaultCredCacheTTL（5 分钟）。
	CredCacheTTL time.Duration
	// Workers 是每条流并发处理请求的上限，0 表示用默认值。Push 会等 Redis，
	// 串行处理会让一条慢请求拖住整条流上后面所有请求。
	Workers int
}

type Server struct {
	fpimv1.UnimplementedImServiceServer
	deps Deps
	grpc *grpc.Server
	// stopping 在 Stop 时关闭，让每条 Connect 长流自己返回。
	stopping chan struct{}
	stopOnce sync.Once
}

func New(d Deps) *Server {
	if d.Workers <= 0 {
		d.Workers = defaultWorkers
	}
	s := &Server{deps: d, stopping: make(chan struct{})}
	s.grpc = grpc.NewServer(
		// 只装流拦截器：这个服务只有一个双向流方法 Connect，没有一元方法，
		// 一元拦截器装了也永远不会被触发。
		grpc.StreamInterceptor(streamAuth(newCredCache(d.Creds, d.CredCacheTTL))),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: KeepaliveMinTime, PermitWithoutStream: true}),
	)
	fpimv1.RegisterImServiceServer(s.grpc, s)
	return s
}

func (s *Server) Serve(lis net.Listener) error { return s.grpc.Serve(lis) }

// Stop 先优雅停，超时就硬停。
//
// GracefulStop 只发 GOAWAY、等在途 RPC 自己结束，而 Connect 是长流，业务 server
// 不会因为 GOAWAY 就收掉它——不主动让它们返回的话，每次关闭都要干等到 ctx 超时。
func (s *Server) Stop(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.grpc.GracefulStop(); close(done) }()
	s.stopOnce.Do(func() { close(s.stopping) })
	select {
	case <-done:
	case <-ctx.Done():
		s.grpc.Stop()
	}
}

// Connect 是唯一的 RPC：第一帧必须是 Ready（业务 server 的 SDK 收到它才认为
// 流可用），此后每条请求各起一个协程并发处理、通过 reqId 配对响应，顺序不保证。
func (s *Server) Connect(stream fpimv1.ImService_ConnectServer) error {
	ctx := stream.Context()
	app := appFrom(ctx)
	snd := &sender{s: stream}
	// GOAWAY 到达业务 server 之前它可能已经在原连接上重建了流，不能再接进来。
	select {
	case <-s.stopping:
		return errStopping
	default:
	}
	// 先发 Ready，再注册进路由器——顺序不能反。AddStream 一返回，这条流就能
	// 被路由器的其它协程（比如别的连接触发的事件、别的请求处理协程）通过
	// h.streams[app] 找到并调用 Send；如果先注册再发 Ready，注册完成到这里
	// 的 Send(Ready) 调用之间存在一个窗口，其它协程可能抢在 Ready 之前抢到
	// sender 的锁，把一条 Event/Inbound 先写上去。proto 里 Ready 是"流建立后
	// 的第一帧"这个契约就被破坏了。触发场景不是理论上的：业务 server 重启
	// 重连时，该 app 下大量在线连接正持续产生事件，很容易撞上这个窗口。
	// 反过来，注册不依赖"Ready 已发出"这件事——Ready 发送失败直接返回错误，
	// 连注册都不必做，交换顺序是安全的。
	if err := snd.Send(&fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Ready{Ready: &fpimv1.Ready{NodeId: s.deps.Hub.NodeID()}}}); err != nil {
		return err
	}
	// remove 必须可靠地在流结束时被调用——不管是 Recv 出错、client 主动关闭、
	// 还是这个函数以任何路径返回，否则路由器里会残留一条死流，之后投给它
	// 的消息全部落空。defer 是唯一能覆盖所有返回路径（含 panic）的写法。
	remove := s.deps.Hub.AddStream(ctx, app, snd)
	defer remove()
	// sem 是并发上限的信号量：拿不到令牌就阻塞等待而不是丢弃请求。
	sem := make(chan struct{}, s.deps.Workers)
	// wg 保证 Connect 返回前，所有已经 sem<-struct{}{} 成功、正在处理请求的
	// 协程都已经跑完——否则 Connect 返回、defer remove() 之后这些协程还在
	// 用已经失效的 snd 往流里写，属于向已关闭的流泄漏写入。
	//
	// closed 与 wg.Add 在同一把锁下：读循环跑在独立协程里，没有这把锁的话它可能
	// 在 wg.Wait 已经开始之后才 Add。
	var (
		mu     sync.Mutex
		closed bool
		wg     sync.WaitGroup
	)
	defer func() {
		mu.Lock()
		closed = true
		mu.Unlock()
		wg.Wait()
	}()
	// 读循环放进独立协程：Recv 没法被打断，而节点关闭时这个函数必须能自己返回。
	// 函数返回后流的 ctx 被取消，卡在 Recv 或 sem 上的读循环随之退出。
	recvErr := make(chan error, 1)
	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				recvErr <- err
				return
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				recvErr <- ctx.Err()
				return
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				return
			}
			wg.Add(1)
			mu.Unlock()
			go func(req *fpimv1.ConnectRequest) {
				defer wg.Done()
				defer func() { <-sem }()
				_ = snd.Send(handle(ctx, s.deps.Hub, app, req))
			}(req)
		}
	}()
	select {
	case err := <-recvErr:
		return err
	case <-s.stopping:
		return errStopping
	}
}
