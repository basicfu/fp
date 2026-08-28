package grpcapi

import (
	"context"
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
// 调用方（main.go）必须在开始 Serve 之前等待它，且要带超时：Watch 一旦
// 被 gRPC 分发到就会给客户端发 ready，SDK 把 ready 当作"此后的撤销不会
// 漏推"的承诺（proto WatchReady 的注释）。hub 自己还没订阅上 Redis 时，
// 这份承诺就是假的——期间发布的撤销事件永久丢失且没有任何信号提示
// SDK 收紧缓存窗口。fp 重启时全部 SDK 同时重连，这个窗口最容易被撞上。
// 完整推导见 RevokeHub.Ready。
//
// Serve 自己不等这个信号：它的职责只是"接受连接"，等不等、等多久超时、
// 失败了怎么办，是启动编排的决定，交给调用方。
func (s *Server) Ready() <-chan struct{} {
	return s.hub.Ready()
}

// Serve 开始接受连接，阻塞到服务停止。
func (s *Server) Serve(lis net.Listener) error {
	return s.grpc.Serve(lis)
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
