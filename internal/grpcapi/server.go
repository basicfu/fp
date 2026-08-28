package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// Deps 是 gRPC 服务需要的全部依赖。
type Deps struct {
	Auth *service.AuthService
	Apps *service.ApplicationService
	Pub  *store.RevokePublisher

	// AppSecretCacheTTL 是应用凭据验证结果的缓存时长，也是 appSecret
	// 轮换的生效上限。为 0 时取 5 分钟。
	AppSecretCacheTTL time.Duration
}

// Server 是 fp 面向 SDK 的 gRPC 服务。
type Server struct {
	grpc *grpc.Server
	hub  *RevokeHub
}

// New 装配 gRPC 服务。
func New(d Deps) *Server {
	ttl := d.AppSecretCacheTTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	verifier := newAppVerifier(d.Apps, ttl)
	hub := NewRevokeHub(d.Pub)

	srv := grpc.NewServer(
		grpc.UnaryInterceptor(verifier.UnaryInterceptor),
		grpc.StreamInterceptor(verifier.StreamInterceptor),

		// MinTime 必须**小于**客户端的 keepalive Time，否则服务端会认为
		// 客户端 ping 过频，回一个 ENHANCE_YOUR_CALM 的 GOAWAY 把连接掐掉。
		// SDK 侧用 30 秒，这里留 10 秒余量。这是 gRPC 最经典的自伤配置：
		// 双方都"配了 keepalive"，结果连接反而被周期性掐断。
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,

			// MaxConnectionAge 让每条连接活满 30 分钟后被优雅回收（先 GOAWAY，
			// 给 5 分钟宽限期收尾进行中的 RPC），客户端随即重连。
			//
			// 没有它的话，**fp 扩容等于白扩**：gRPC 连接是长连接，L4 LB 按连接
			// 分流，存量 SDK 连接会永远钉在老实例上。新加的实例只能接到新启动的
			// SDK 进程——而 SDK 进程的重启频率是按周算的。老实例继续过载、
			// 新实例长期空转，且没有任何报错。
			//
			// grpc-go 会自动给 MaxConnectionAge 加 ±10% 抖动，避免所有连接
			// 同时到期造成重连风暴。
			MaxConnectionAge:      30 * time.Minute,
			MaxConnectionAgeGrace: 5 * time.Minute,
		}),
	)
	fpv1.RegisterAuthServiceServer(srv, NewAuthServer(AuthServerDeps{
		Auth: d.Auth,
		Apps: d.Apps,
		Hub:  hub,
	}))

	return &Server{grpc: srv, hub: hub}
}

// Run 启动撤销事件中继，阻塞到 ctx 取消。在独立 goroutine 里调用。
func (s *Server) Run(ctx context.Context) error {
	return s.hub.Run(ctx)
}

// Ready 在 hub 确认完成 Redis 订阅后关闭。
//
// 调用方必须在开始 Serve 之前等待它：Watch 一旦被 gRPC 分发到就会给
// 客户端发 ready，SDK 把 ready 当作"此后的撤销不会漏推"的承诺（proto
// WatchReady 的注释）。hub 自己还没订阅上 Redis 时，这份承诺就是假的——
// 期间发布的撤销事件永久丢失且没有任何信号提示 SDK 收紧缓存窗口。fp
// 重启时全部 SDK 同时重连，这个窗口最容易被撞上。完整推导见
// RevokeHub.Ready。
//
// Serve 自己不等这个信号：它的职责只是"接受连接"。等不等、等多久超时、
// 失败了怎么办，是 ServeWhenReady 的职责——直接调 Serve 的调用方要自己
// 负责先等这个信号，或者干脆用 ServeWhenReady。
func (s *Server) Ready() <-chan struct{} {
	return s.hub.Ready()
}

// Serve 开始接受连接，阻塞到服务停止。
func (s *Server) Serve(lis net.Listener) error {
	return s.grpc.Serve(lis)
}

// ServeWhenReady 等撤销中继就绪（或提前失败、或超时、或 ctx 被取消）
// 之后再开始接受连接，阻塞到服务停止。
//
// 顺序不能反：Serve 一旦开始接受连接，Watch 就会给新连上来的 SDK 发
// ready，而 ready 是"此后的撤销不会漏推"的承诺（见 Ready 的注释）。这个
// 承诺只有在 hub 已经真正订阅上 Redis 之后才成立——没订阅上时提前开始
// 接受连接，恰好落在这段窗口里的撤销事件就会无声丢失，SDK 却毫不知情、
// 不会收紧本地缓存窗口。fp 重启时全部 SDK 同时重连，这个窗口最容易被
// 撞上。
//
// 这段编排原本直接写在 cmd/fp/main.go 里（起 Run 的 goroutine、等
// Ready()/Run 失败/超时三选一、再起 Serve 的 goroutine），但 cmd/fp 是
// package main，这个仓库里没有任何测试基础设施覆盖它——把它改回"两个
// goroutine 各自起、谁都不等谁"，build/vet/全量测试依然全绿，没有任何
// 信号能告诉任何人这条本任务新增的关键安全性质被静默破坏了。挪进
// grpcapi.Server 之后，这段编排落进了已经有完整 bufconn 测试设施的包里：
// TestServeWhenReadyDoesNotAcceptBeforeReady 用 fakeSubscriber 卡住
// Subscribe，确定性地断言 Ready() 关闭之前 lis 不会被 Accept。
//
// case <-ctx.Done() 必须和 Ready()/runFailed/超时同级放在 select 里，
// 不能省略：main.go 传进来的 ctx 是进程级的可取消 ctx（signal.NotifyContext
// 来的），如果没有这一条，SIGTERM 恰好落在"订阅确认还没收到"这个窗口内
// （网络慢、Redis 抖动时能拉长到接近 timeout 前的任意时刻）就会有两个
// 后果：一是 Run 的 ctx 取消会经 h.subscribe 的失败分支被当成"订阅失败"
// 报给 runFailed（RevokeHub.Run 已经把这类失败统一成返回 nil，但如果
// 调用方自己不等 ctx.Done()，这个"nil"跟"ctx 还没到超时前一直没收到
// 任何信号"没有区别，只能傻等到 timeout 才返回），二是即使 Run 已经在
// ctx 取消后返回了 nil（不往 runFailed 送任何东西），ServeWhenReady
// 还是会一直卡到 timeout 才返回——一次本该毫秒级的关闭被拖成 10 秒。
// 加上这一条，两个问题一起解决：ctx 一旦结束就立刻返回 nil，不等
// timeout，也不会被误判成失败。
func (s *Server) ServeWhenReady(ctx context.Context, lis net.Listener, timeout time.Duration) error {
	// runFailed 只在 Run 未能撑到 ctx 取消、提前带错误退出时才会收到一个
	// 值（缓冲为 1，不会阻塞这个 goroutine）。Run 正常结束（ctx 取消，
	// 或 ctx 在订阅确认之前就被取消）永远返回 nil，什么都不会往这个
	// channel 里送——"一切正常"这件事已经由 Ready() 或者上面新加的
	// ctx.Done() 分支表达了，不需要 runFailed 重复表达一遍。
	runFailed := make(chan error, 1)
	go func() {
		if err := s.Run(ctx); err != nil {
			runFailed <- err
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case <-s.Ready():
	case err := <-runFailed:
		return fmt.Errorf("撤销事件中继启动失败: %w", err)
	case <-time.After(timeout):
		return errors.New("等待撤销事件中继就绪超时")
	}

	return s.Serve(lis)
}

// Shutdown 优雅关闭。
//
// 顺序不能变：先关 hub 让所有 Watch handler 返回，GracefulStop 才可能结束。
// 反过来的话 GracefulStop 会等一条永不结束的长流，进程永远停不下来。
func (s *Server) Shutdown(ctx context.Context) {
	s.hub.Close()

	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("grpcapi: 优雅关闭超时，强制停止")
		s.grpc.Stop()
	}
}
