// Package imgrpc 是业务 server 一侧的传输层：ImService.Connect 双向流。
package imgrpc

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/hub"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// KeepaliveMinTime 与 fpim.KeepaliveTime(30s) 配对：客户端 ping 间隔必须大于这里，否则被 GOAWAY。
// 配对关系由 internal/integration/im_parity_test.go 守着，不要改名或改值。
const KeepaliveMinTime = 10 * time.Second

// defaultWorkers 是 Deps.Workers 为 0 时每条流并发处理请求的上限。
const defaultWorkers = 64

type Deps struct {
	Hub  *hub.Hub
	Apps auth.AppConfigSource
	// Workers 是每条流并发处理请求的上限，0 表示用默认值。Push 会等 Redis，
	// 串行处理会让一条慢请求拖住整条流上后面所有请求。
	Workers int
}

type Server struct {
	fpimv1.UnimplementedImServiceServer
	deps Deps
	grpc *grpc.Server
}

func New(d Deps) *Server {
	if d.Workers <= 0 {
		d.Workers = defaultWorkers
	}
	s := &Server{deps: d}
	s.grpc = grpc.NewServer(
		// 只装流拦截器：这个服务只有一个双向流方法 Connect，没有一元方法，
		// 一元拦截器装了也永远不会被触发。
		grpc.StreamInterceptor(streamAuth(d.Apps)),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: KeepaliveMinTime, PermitWithoutStream: true}),
	)
	fpimv1.RegisterImServiceServer(s.grpc, s)
	return s
}

func (s *Server) Serve(lis net.Listener) error { return s.grpc.Serve(lis) }

// Stop 先优雅停，超时就硬停。
func (s *Server) Stop(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.grpc.GracefulStop(); close(done) }()
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
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		req, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(req *fpimv1.ConnectRequest) {
			defer wg.Done()
			defer func() { <-sem }()
			_ = snd.Send(handle(ctx, s.deps.Hub, app, req))
		}(req)
	}
}
