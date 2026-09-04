// fp-im 是独立于 fp 的连接网关二进制。装配顺序与 cmd/fp 一致：配置 → 日志 → 存储 → 服务 → 监听 → 等信号。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/basicfu/fp/internal/im/appcfg"
	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/config"
	"github.com/basicfu/fp/internal/im/fpauth"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/imgrpc"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/basicfu/fp/internal/im/registry"
	"github.com/basicfu/fp/internal/im/wsapi"
	"github.com/basicfu/fp/internal/logging"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fp-im 启动失败", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	host, _ := os.Hostname()
	nodeID := model.NewNodeID(host, time.Now())

	rdb, mode, err := redisx.Open(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()
	runner, err := redisx.NewRunner(rdb, cfg.Pipeline.FlushInterval, cfg.Pipeline.FlushSize)
	if err != nil {
		return err
	}
	defer runner.Close()

	apps, err := appcfg.LoadFile(cfg.AppsFile)
	if err != nil {
		return err
	}
	go apps.Watch(ctx, 10*time.Second)

	live := registry.NewLiveness(rdb, nodeID, cfg.Node.Heartbeat, cfg.Node.DeadAfter)
	conns := registry.NewConns(rdb, runner, cfg.Conn.FieldTTL)
	b := bus.New(rdb, runner)

	// h 先声明后赋值，打破"撤销回调需要 h"与"h 的构造需要撤销回调"之间的
	// 循环：fpauth.New 只是把这个闭包存起来，回调只会在 fpsdk 的撤销流上
	// 真的收到事件时才会被调用——那必然发生在下面 h = hub.New(...) 执行
	// 完、run() 早已往后走了很远（起了 HTTP/gRPC 监听）之后，不存在
	// "回调被调用时 h 还是 nil" 的窗口。
	var h *hub.Hub
	authn, err := fpauth.New(fpauth.Config{
		FPAddr: cfg.FPAddr, Insecure: cfg.FPInsecure, Apps: apps, Logger: log,
		OnRevoke: func(app string, tokens []string) { h.OnRevoked(context.Background(), app, tokens) },
	})
	if err != nil {
		return err
	}
	defer authn.Close()
	h = hub.New(nodeID, conns, live, b, apps)

	// 节点频道：先订阅、再登记心跳，保证别的节点看到我的心跳、开始往我的
	// 频道投递时，我已经在监听，不会丢消息。
	if err := runBusLoop(ctx, b, nodeID, h, conns, live, log); err != nil {
		return err
	}
	if err := live.Beat(ctx); err != nil {
		return fmt.Errorf("首个心跳失败: %w", err)
	}
	// 债务 1（注册表复审 Important）：这里必须再同步跑一次 live.Refresh，
	// 不能只写心跳就把刷新交给下面 `go live.Run(ctx)` 的首轮去做。
	// live.Run 内部虽然也会在循环开始前先跑一次 Beat+Refresh，但那一轮
	// 是在独立的 goroutine 里异步执行的：run() 这个函数会继续往下走，
	// 紧接着就起 HTTP 监听、开始接受 client 握手。如果 Refresh 的首轮还
	// 没落地，本节点此刻的存活视图里只有自己一个节点；这段窗口期里，
	// 握手脚本（registry.Conns.Handshake）会把"存活列表之外的节点"上、
	// 同一 subject 的连接全部当成死节点残留清掉——但那些连接的底层
	// socket 其实还开着，只是从此收不到任何路由到它们的消息，直到 client
	// 自己因为空闲超时或下一次业务动作触发重连。同步调用一次 Refresh，
	// 确保 HTTP 服务开始接受连接之前，本节点看到的就是 Redis 里当下真实
	// 的全量存活节点，彻底关掉这个窗口。
	if err := live.Refresh(ctx); err != nil {
		return fmt.Errorf("首次存活视图刷新失败: %w", err)
	}
	go live.Run(ctx)
	go renewLoop(ctx, h, conns, cfg.Conn.FieldRenew)
	guests := wsapi.NewGuestLimiter()
	go sweepLoop(ctx, guests)

	mux := http.NewServeMux()
	mux.Handle("/ws", wsapi.New(wsapi.Deps{
		Hub: h, Conns: conns, Live: live, Auth: authn, Apps: apps, Guests: guests,
		Cfg: wsapi.Config{AuthTimeout: cfg.Conn.AuthTimeout, IdleTimeout: cfg.Conn.IdleTimeout, SendQueue: cfg.Conn.SendQueue, TrustProxy: cfg.TrustProxy},
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	httpSrv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux}
	grpcSrv := imgrpc.New(imgrpc.Deps{Hub: h, Apps: apps})
	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

	// fatal 收 HTTP/gRPC 两个后台服务的致命错误，容量 2 保证两个 goroutine
	// 都不会因为没人接收而卡在发送上。与 cmd/fp/main.go 里 fatalErr 的
	// 理由一致：只让 stop() 触发关闭而不把错误带出 run()，会让"后台服务
	// 把自己搞挂"和"收到 SIGTERM 的正常关闭"在退出码上无法区分。
	fatal := make(chan error, 2)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal <- fmt.Errorf("http: %w", err)
			stop()
		}
	}()
	go func() {
		if err := grpcSrv.Serve(grpcLis); err != nil {
			fatal <- fmt.Errorf("grpc: %w", err)
			stop()
		}
	}()
	log.Info("fp-im 启动", "node", nodeID, "redis", mode, "http", cfg.HTTPAddr, "grpc", cfg.GRPCAddr)

	<-ctx.Done()
	log.Info("fp-im 收到退出信号，正在关闭")

	// 与 cmd/fp/main.go 相同：gRPC 与 HTTP 各给独立的 10 秒关闭预算，不共用
	// 一个 ctx。gRPC 那边要等所有 Connect 长流的 in-flight 请求处理完
	// （imgrpc.Server.Connect 里的 wg.Wait()）才能返回，关闭复杂度比 HTTP
	// （只需等存量请求跑完）更高，更容易把预算用满；共用一个 ctx 的话，
	// gRPC 一用满，HTTP 拿到的就是一个已经过期的 ctx，Shutdown 立刻返回
	// 超时错误——排障会被引去查 HTTP，而 HTTP 本身毫无问题，真正原因在
	// gRPC 那边。
	//
	// 顺序也不能反：先停 gRPC 再停 HTTP。gRPC 这一侧连着业务 server（通过
	// ImService.Connect 长流推事件/收上行），先把它停掉意味着业务 server
	// 不再能收到任何新事件、也无法再下发 Push；随后再关 HTTP，断开 client
	// 的 ws 连接。反过来的话，HTTP 先断，client 连接全部消失，但业务 server
	// 那边的流还开着、可能正在尝试对着已经空无一人的 hub 发 Push，纯属浪费
	// ；更重要的是，先断 client 会让它们立刻发起重连，重连握手落在一个
	// gRPC 侧还没停、看似一切正常的节点上，随后这个节点自己也要关闭，
	// 这些刚重连上来的连接会经历一次不必要的二次断开。
	gctx, gcancel := context.WithTimeout(context.Background(), 10*time.Second)
	grpcSrv.Stop(gctx)
	gcancel()
	hctx, hcancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := httpSrv.Shutdown(hctx); err != nil {
		log.Error("HTTP 优雅关闭超时", "err", err)
	}
	hcancel()

	// 排空而不是只读一次，理由与 cmd/fp/main.go 的 fatalErr 排空一致：
	// HTTP 与 gRPC 若同时失败，只读一次会让另一个错误永久丢失。
	var errs []error
drain:
	for {
		select {
		case err := <-fatal:
			errs = append(errs, err)
		default:
			break drain
		}
	}
	return errors.Join(errs...)
}

// runBusLoop 订阅节点频道并起协程分发。收到 Gap（Redis 重连过）时把本地连接重新登记一遍。
//
// 消费循环必须写成 `for sig := range ch`，不能写成裸的 `<-ch`：bus.SignalKind
// 的零值就是 bus.SignalEnvelope（"收到一条信封"），channel 关闭后裸接收会
// 读到这个零值 Signal{}，与一条真实的、类型为 TypeMsg 且字段全部为空的信封
// 在类型层面完全无法区分，于是会把"channel 已关闭"这件事误当成一条合法的
// 空信封继续往 h.HandleEnvelope 送——range 在 channel 关闭且排空后会正常
// 结束循环，不会有这个歧义。
func runBusLoop(ctx context.Context, b *bus.Bus, nodeID string, h *hub.Hub, conns *registry.Conns, live *registry.Liveness, log *slog.Logger) error {
	ch, closeFn, err := b.Subscribe(ctx, nodeID)
	if err != nil {
		return err
	}
	go func() {
		defer closeFn()
		for sig := range ch {
			switch sig.Kind {
			case bus.SignalEnvelope:
				h.HandleEnvelope(ctx, sig.Env)
			case bus.SignalGap:
				log.Warn("节点频道重订阅，重新登记本地连接")
				_ = live.Beat(ctx)
				reregister(ctx, h, conns, live)
			}
		}
	}()
	return nil
}

// reregister 用 policy=none 跑握手脚本：只清残留、HSET、HEXPIRE。限速每秒
// 2000 条，避免 Redis 重连后一次性把本节点全部连接重新登记造成的写风暴
// 打爆 Redis——十万级连接的节点如果不限速，会在几十毫秒内发出十万条
// EVALSHA，直接打满 Redis 的单线程处理能力，殃及同一个 Redis 上其它节点
// 正常的握手/心跳请求。
func reregister(ctx context.Context, h *hub.Hub, conns *registry.Conns, live *registry.Liveness) {
	type item struct {
		app, connID string
		sub         model.Subject
		meta        model.ConnMeta
	}
	var items []item
	h.ForEachConn(func(app string, sub model.Subject, connID string, meta model.ConnMeta) {
		items = append(items, item{app, connID, sub, meta})
	})
	tick := time.NewTicker(time.Second / 2000)
	defer tick.Stop()
	for _, it := range items {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		_, _ = conns.Handshake(ctx, it.app, it.sub.String(), it.connID, it.meta, model.PolicyNone, 0, live.LiveNodes())
	}
}

// renewLoop 每 renew 周期按 subject 分组给本地连接续 field TTL。
func renewLoop(ctx context.Context, h *hub.Hub, conns *registry.Conns, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		type k struct{ app, sub string }
		groups := map[k][]string{}
		h.ForEachConn(func(app string, sub model.Subject, connID string, _ model.ConnMeta) {
			groups[k{app, sub.String()}] = append(groups[k{app, sub.String()}], connID)
		})
		for g, ids := range groups {
			_ = conns.Renew(ctx, g.app, g.sub, ids)
		}
	}
}

// sweepLoop 每分钟清一次访客限流器里的过期桶（债务 3）：GuestLimiter.Sweep
// 不被定期调用的话，见过的 (app, IP) 桶只会随时间无限增长，永远不会被回收。
func sweepLoop(ctx context.Context, g *wsapi.GuestLimiter) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Sweep(time.Now())
		}
	}
}
