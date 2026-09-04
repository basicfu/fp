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
	"sync/atomic"
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

// run 只做"进程级"的三件事：读配置、装日志、把信号接成一个 ctx，随后把
// 全部装配与服务交给 serve。
//
// 拆出 serve 是为了可测性：整个 cmd/fp-im 包此前零测试，而最终复审指出的
// 三个缺陷（甲一启动时没有预先追踪 app、甲二重登记占住消费协程、乙一优雅
// 关闭抛弃 ws 连接）全部落在这段装配代码里；集成测试则是把装配顺序**手抄**
// 了一份，所以这里任何顺序退化都不会让它变红。测试要能起一个真实节点，就
// 必须能自己构造 config.Config（不经环境变量）、能把端口配成 :0 并知道真正
// 分配到的端口、能用一个自己可以取消的 ctx 触发优雅关闭——这三件事正是
// run 与 serve 的分界线。
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, cfg, log, serveOptions{})
}

// serveOptions 是只为可测性存在的注入点。生产路径（run）传零值，行为与
// 没有这个结构体时完全一致。
type serveOptions struct {
	// ready 在 HTTP 与 gRPC 两个监听都已建立、进程开始对外服务之后被调用
	// 一次，参数是两个监听器的真实地址。测试把端口配成 :0，只有监听建立
	// 之后才知道操作系统分配了哪个端口；生产路径为 nil。
	ready func(httpAddr, grpcAddr string)
	// nodeID 覆盖节点标识。同一个测试进程里起两个节点时，
	// model.NewNodeID 的"主机名-进程号-启动毫秒"三元组只剩毫秒时间戳能区
	// 分两者，同一毫秒内起第二个节点会算出完全相同的标识（后果见
	// model.NewNodeID 的注释：两张表交替写同一字段、消息被静默丢弃）。
	// 生产路径为空串，仍走 model.NewNodeID。
	nodeID string
}

// shutdownBudget 是关闭阶段每个独立步骤各自的时间预算。
const shutdownBudget = 10 * time.Second

func serve(ctx context.Context, cfg *config.Config, log *slog.Logger, opt serveOptions) error {
	// 派生一个可取消的 ctx：后台服务（HTTP/gRPC）把自己搞挂时要主动触发
	// 关闭流程，而调用方给的 ctx 的取消函数不在这里。cancel 与信号取消是
	// 同一个效果，下面 <-ctx.Done() 之后的关闭流程对两者一视同仁。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	nodeID := opt.nodeID
	if nodeID == "" {
		host, _ := os.Hostname()
		// 拼上进程号：同主机同一毫秒启动的两个 fp-im 进程（本地开发、单机
		// 双节点预发环境、进程管理器并行拉起多实例都会真的撞上）如果只用
		// 主机名+时间戳会算出完全相同的节点标识，见 model.NewNodeID 的注释。
		nodeID = model.NewNodeID(host, os.Getpid(), time.Now())
	}

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

	live := registry.NewLiveness(rdb, nodeID, cfg.Node.Heartbeat, cfg.Node.DeadAfter)
	conns := registry.NewConns(rdb, runner, cfg.Conn.FieldTTL)
	b := bus.New(rdb, runner)

	// 甲一：装配时就把配置里的每个 app 加进 srv 表的追踪集合，位置必须在
	// 下面那次同步 live.Refresh 之前。
	//
	// 不这样做的后果不是"慢一点"，是每个 app 在本节点上的**第一条**上行
	// 消息或连接事件必定被静默丢弃、零日志：hub.Deliver 在需要跨节点转发
	// 时先调 live.TrackApp 再读 live.ServerNodes(app)，而 TrackApp 只是把
	// app 加进待刷新集合，真正的内容要等下一次 Refresh（默认 3 秒）才有。
	// 于是节点起来后第一个连上的 client，它的连接建立事件因为候选列表为空
	// 被丢掉，业务方永远收不到这条上线通知——它不会被重发，也没有任何
	// 日志说明发生过这件事。
	//
	// 热重载引入的新 app 同样需要补追踪，理由一模一样，所以这里用
	// appcfg 的重载回调再跑一遍，而不是只在启动时跑一次。回调必须在
	// 启动 Watch 之前注册，否则第一次热重载可能赶在注册之前发生。
	trackApps(live, apps)
	apps.OnReload(func() { trackApps(live, apps) })
	go apps.Watch(ctx, 10*time.Second)

	// h 先声明后赋值，打破"撤销回调需要 h"与"h 的构造需要撤销回调"之间的
	// 循环：fpauth.New 只是把这个闭包存起来，回调只会在 fpsdk 的撤销流上
	// 真的收到事件时才会被调用——那必然发生在下面 h = hub.New(...) 执行
	// 完、serve() 早已往后走了很远（起了 HTTP/gRPC 监听）之后，不存在
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
	rr := &reregistrar{conns: conns, live: live, log: log, rate: reregisterInterval}
	if err := runBusLoop(ctx, b, nodeID, h, rr, log); err != nil {
		return err
	}
	if err := live.Beat(ctx); err != nil {
		return fmt.Errorf("首个心跳失败: %w", err)
	}
	// 债务 1（注册表复审 Important）：这里必须再同步跑一次 live.Refresh，
	// 不能只写心跳就把刷新交给下面 `go live.Run(ctx)` 的首轮去做。
	// live.Run 内部虽然也会在循环开始前先跑一次 Beat+Refresh，但那一轮
	// 是在独立的 goroutine 里异步执行的：serve() 这个函数会继续往下走，
	// 紧接着就起 HTTP 监听、开始接受 client 握手。如果 Refresh 的首轮还
	// 没落地，本节点此刻的存活视图里只有自己一个节点；这段窗口期里，
	// 握手脚本（registry.Conns.Handshake）会把"存活列表之外的节点"上、
	// 同一 subject 的连接全部当成死节点残留清掉——但那些连接的底层
	// socket 其实还开着，只是从此收不到任何路由到它们的消息，直到 client
	// 自己因为空闲超时或下一次业务动作触发重连。同步调用一次 Refresh，
	// 确保 HTTP 服务开始接受连接之前，本节点看到的就是 Redis 里当下真实
	// 的全量存活节点，彻底关掉这个窗口。
	//
	// 这次 Refresh 也是甲一那次 trackApps 生效的地方：被追踪的 app 的 srv
	// 表在这一次同步刷新里就被读全，第一条上行/事件就已经有候选列表可用。
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
	// HTTP 与 gRPC 都先显式 net.Listen 再 Serve，而不是用
	// httpSrv.ListenAndServe：端口配成 :0 时只有监听建立之后才知道操作
	// 系统分配了哪个端口，测试要连上来就必须能读到真实地址（opt.ready）。
	// 顺带把"端口被占用"这类错误变成 serve 的同步返回值，而不是从一个
	// 后台 goroutine 里异步冒出来。
	httpLis, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		httpLis.Close()
		return err
	}

	// fatal 收 HTTP/gRPC 两个后台服务的致命错误，容量 2 保证两个 goroutine
	// 都不会因为没人接收而卡在发送上。与 cmd/fp/main.go 里 fatalErr 的
	// 理由一致：只让 cancel() 触发关闭而不把错误带出 serve()，会让"后台服务
	// 把自己搞挂"和"收到 SIGTERM 的正常关闭"在退出码上无法区分。
	fatal := make(chan error, 2)
	go func() {
		if err := httpSrv.Serve(httpLis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal <- fmt.Errorf("http: %w", err)
			cancel()
		}
	}()
	go func() {
		if err := grpcSrv.Serve(grpcLis); err != nil {
			fatal <- fmt.Errorf("grpc: %w", err)
			cancel()
		}
	}()
	if opt.ready != nil {
		opt.ready(httpLis.Addr().String(), grpcLis.Addr().String())
	}
	log.Info("fp-im 启动", "node", nodeID, "redis", mode, "http", httpLis.Addr().String(), "grpc", grpcLis.Addr().String())

	<-ctx.Done()
	log.Info("fp-im 收到退出信号，正在关闭")
	shutdown(h, httpSrv, grpcSrv, live, log)

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

// shutdown 是收到退出信号之后的全部关闭动作。四步各给一个独立的时间
// 预算，不共用同一个 ctx。
//
// 独立预算的理由与 cmd/fp/main.go 相同：gRPC 那边要等所有 Connect 长流的
// in-flight 请求处理完（imgrpc.Server.Connect 里的 wg.Wait()）才能返回，
// 关闭复杂度比其它几步高，更容易把预算用满；共用一个 ctx 的话，谁先用满，
// 后面几步拿到的就是一个已经过期的 ctx，立刻返回超时错误——排障会被引去
// 查错的地方。
//
// 顺序（乙一之后重新定的）：
//
//	① httpSrv.Shutdown：关掉监听、不再接受新的 ws 握手。它对已经建立的
//	   ws 连接不起任何作用——按 Go 的契约，Shutdown"不等待也不关闭被劫持
//	   的连接"，而 ws 走的正是劫持，所以这一步返回得很快，也不会打断②。
//	② 主动关闭本地全部 ws（4004 服务不可用：与 client 自身无关，退避后
//	   重连，语义正好对应节点下线），然后等它们各自的拆连接流程走完。
//	   走正常拆连接路径而不是直接砍连接，注册表条目与断开事件才会被正确
//	   处理。
//	③ 停 gRPC。必须排在②之后：②发出的断开事件要经业务 server 的
//	   ImService.Connect 长流才能送出去，先停 gRPC 的话这些事件就没有出口，
//	   业务方的在线表里会留下一批永远不下线的连接。
//	④ 删掉自己在两张心跳表里的条目。
//
// 这个顺序推翻了本文件此前"先 gRPC 后 HTTP"的写法。当时的理由是"先断
// client 会让它们立刻重连、落在一个 gRPC 侧还没停的节点上，经历一次不必要
// 的二次断开"——那条顾虑在这里已经不成立：①已经把本节点的 HTTP 监听关掉
// 了，重连的 client 根本落不回来，它们会被负载均衡送到别的节点。而"先停
// gRPC"会直接让②发不出断开事件，代价比那条顾虑大得多。
func shutdown(h *hub.Hub, httpSrv *http.Server, grpcSrv *imgrpc.Server, live *registry.Liveness, log *slog.Logger) {
	hctx, hcancel := context.WithTimeout(context.Background(), shutdownBudget)
	if err := httpSrv.Shutdown(hctx); err != nil {
		log.Error("HTTP 优雅关闭超时", "err", err)
	}
	hcancel()

	if n := h.CloseLocalConns(model.CloseUnavailable, model.ReasonShutdown); n > 0 {
		log.Info("正在断开本地 ws 连接", "count", n)
		wctx, wcancel := context.WithTimeout(context.Background(), shutdownBudget)
		if left := waitConnsDrained(wctx, h); left > 0 {
			log.Warn("仍有连接没有在预算内完成拆连接，它们的注册表条目要等 field TTL 过期", "left", left)
		}
		wcancel()
	}

	gctx, gcancel := context.WithTimeout(context.Background(), shutdownBudget)
	grpcSrv.Stop(gctx)
	gcancel()

	// 甲三：优雅关闭时删掉自己在节点表与服务表里的条目。
	//
	// 不删的话每一次发版都往这两张表里永久加一个字段（节点标识每次启动都
	// 是新值），而每个节点每 3 秒还要把整张表读一遍——正确性靠时间戳过滤
	// 保住了，但这是一条会随时间恶化的热路径。心跳写入时挂的字段级过期
	// （registry.Liveness）是崩溃场景的兜底，这里的主动删除是优雅关闭场景
	// 的即时清理，两者互补：前者慢但覆盖所有退出方式，后者快但只在正常
	// 退出时生效。
	dctx, dcancel := context.WithTimeout(context.Background(), shutdownBudget)
	if err := live.Deregister(dctx); err != nil {
		log.Warn("删除本节点心跳条目失败，等字段过期兜底", "err", err)
	}
	dcancel()
}

// waitConnsDrained 等本地连接彻底了结，返回还剩几条（0 表示全部拆完）。
//
// 判据用 UnsettledConns 而不是 LocalConnCount：后者在 RemoveConn 摘表的
// 那一刻就归零了，而断开事件要等建立事件处理完之后才发得出去（最长
// hub.removeConnWait）。用连接表当判据的话，一批刚建立、建立事件还没被
// 业务 server 处理完的连接会让这里立刻返回，紧接着停 gRPC 把长流掐掉，
// 那些断开事件最后落到一条死流上——业务方在线表里留下永不下线的连接，
// 正是乙一要消灭的症状。详见 hub.teardowns 字段的注释。
//
// 轮询而不是等一个信号：拆连接由每条连接自己的读循环各自完成，hub 里没有
// "最后一条走完了"这样一个天然的汇合点，为了关闭这一次性动作在热路径的
// 连接表上加一个计数信号并不划算。20 毫秒一次，十万连接的节点上拆完通常
// 也就是几百毫秒，轮询开销可以忽略。
func waitConnsDrained(ctx context.Context, h *hub.Hub) int {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		if n := h.UnsettledConns(); n == 0 {
			return 0
		}
		select {
		case <-ctx.Done():
			return h.UnsettledConns()
		case <-t.C:
		}
	}
}

// trackApps 把配置里当前的每个 app 都加进存活视图的 srv 表追踪集合。
// 幂等（TrackApp 只是往一个 set 里写），可以在启动时和每次热重载后重复调用。
func trackApps(live *registry.Liveness, apps *appcfg.Source) {
	for _, app := range apps.Apps() {
		live.TrackApp(app)
	}
}

// runBusLoop 订阅节点频道并起协程分发。收到 Gap（Redis 重连过）时交给
// reregistrar 处理，本协程立刻回到消费循环。
//
// 消费循环必须写成 `for sig := range ch`，不能写成裸的 `<-ch`：bus.SignalKind
// 的零值就是 bus.SignalEnvelope（"收到一条信封"），channel 关闭后裸接收会
// 读到这个零值 Signal{}，与一条真实的、类型为 TypeMsg 且字段全部为空的信封
// 在类型层面完全无法区分，于是会把"channel 已关闭"这件事误当成一条合法的
// 空信封继续往 h.HandleEnvelope 送——range 在 channel 关闭且排空后会正常
// 结束循环，不会有这个歧义。
func runBusLoop(ctx context.Context, b *bus.Bus, nodeID string, h *hub.Hub, rr *reregistrar, log *slog.Logger) error {
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
				log.Warn("节点频道重订阅，检查是否需要重新登记本地连接")
				rr.onGap(ctx, h)
			}
		}
	}()
	return nil
}

// reregisterInterval 是重登记两条连接之间的间隔，等于每秒 2000 条。
// 限速的理由：Redis 重连后一次性把本节点全部连接重新登记会造成写风暴，
// 十万级连接的节点如果不限速，会在几十毫秒内发出十万条 EVALSHA，直接打满
// Redis 的单线程处理能力，殃及同一个 Redis 上其它节点正常的握手/心跳请求。
const reregisterInterval = time.Second / 2000

// handshaker 是 reregistrar 需要的 registry.Conns 子集。
// livenessPort 是它需要的 registry.Liveness 子集。抽成接口只为让"失败要
// 计数并留下日志"和"不占住消费协程"这两条性质能在不连 Redis 的情况下被
// 测到——本包此前零测试，甲二的两个缺陷正是因此长期没被发现。
type handshaker interface {
	Handshake(ctx context.Context, app, subject, connID string, meta model.ConnMeta, policy model.Policy, limit int, live []string) (registry.HandshakeResult, error)
}

type livenessPort interface {
	Beat(ctx context.Context) error
	SelfPresent(ctx context.Context) (bool, error)
	LiveNodes() []string
}

// reregistrar 处理节点频道的 Gap（Redis 断线重连过）信号。
//
// 甲二修的是同一段代码上叠加的两个问题：
//
//  1. 重登记原先直接跑在节点频道的消费协程里。按每秒 2000 条限速，十万
//     连接的节点会独占这个协程约 50 秒，这期间本节点频道上所有的推送、
//     上行、踢人信封**全部停止处理**；总线的出通道容量 1024，满了之后
//     Redis 客户端库会在超时后丢弃并只打一行库内日志。所以真正的重登记
//     必须挪到独立协程里跑，onGap 只负责起协程并立刻返回。
//  2. 重登记的返回错误被完全忽略。在 Redis 数据真丢过的场景下，失败的
//     那条连接会**永久**从注册表消失：client 每 25 秒发一次心跳，空闲
//     超时不会触发，套接字一直开着，但推送对它恒为"不在线"，无日志、
//     无重试。所以失败必须计数并留一条汇总日志。
//
// 顺带实现了设计文档第七节写的前置条件：只有发现自己在节点表里的条目已经
// 消失（Redis 重启过、数据没了）时才需要全量重登记。多数 Gap 只是一次
// 普通的订阅重连，Redis 里的数据都还在，这一层判断能让"全量重登记"这条
// 昂贵的路径本身变得罕见。
type reregistrar struct {
	conns handshaker
	live  livenessPort
	log   *slog.Logger
	// rate 是两条连接之间的间隔，<=0 表示不限速（仅测试这样用）。
	rate time.Duration

	// busy 防重入：Gap 可能在短时间内连续来好几次（Redis 抖动），几轮
	// 重登记同时在跑只会互相争抢 Redis，且它们做的是同一件事，合并成一次
	// 即可——真正要登记的内容是跑的时候现从 hub 里读的，不是信号里带的，
	// 所以合并绝不会漏掉后来新增的连接。
	busy atomic.Bool

	// onDone 仅供测试观察一轮重登记的结果（总数、失败数），生产为 nil。
	// 用回调而不是让测试睡一会儿再断言：这一轮跑在独立协程里，没有回调
	// 就只能靠睡眠猜它跑完没有，那是会撒谎的假通过。
	onDone func(total, failed int)
}

// onGap 处理一次 Gap 信号，立刻返回。返回值表示本次是否真的起了一轮重登记
// （false 表示上一轮还在跑，本次被合并掉）。
func (r *reregistrar) onGap(ctx context.Context, h *hub.Hub) bool {
	if !r.busy.CompareAndSwap(false, true) {
		r.log.Warn("上一轮重新登记仍在进行，本次 Gap 合并处理")
		return false
	}
	go func() {
		defer r.busy.Store(false)
		r.run(ctx, h)
	}()
	return true
}

// run 跑一轮：先判断需不需要重登记，需要的话逐条用 policy=none 跑握手脚本
// （只清残留、HSET、HEXPIRE，不做策略判定）。
func (r *reregistrar) run(ctx context.Context, h *hub.Hub) {
	// 顺序不能反：必须先查自己的条目还在不在，再写心跳。反过来的话这次
	// 心跳会把条目重新创建出来，查询永远返回"还在"，"Redis 数据丢了要
	// 全量重登记"这条路径就永远不会被触发。
	present, err := r.live.SelfPresent(ctx)
	if err != nil {
		// 查不出来就按最坏情况（条目已经没了）处理：多做一次重登记的代价
		// 是一批限速的写入，漏做一次的代价是这些连接永久对外不可见。
		r.log.Warn("查询本节点存活条目失败，按需要重登记处理", "err", err)
		present = false
	}
	if err := r.live.Beat(ctx); err != nil {
		r.log.Warn("Gap 之后补写心跳失败", "err", err)
	}
	if present {
		r.log.Info("本节点存活条目仍在，跳过全量重登记")
		if r.onDone != nil {
			r.onDone(0, 0)
		}
		return
	}

	type item struct {
		app, connID string
		sub         model.Subject
		meta        model.ConnMeta
	}
	var items []item
	h.ForEachConn(func(app string, sub model.Subject, connID string, meta model.ConnMeta) {
		items = append(items, item{app, connID, sub, meta})
	})
	var tick *time.Ticker
	if r.rate > 0 {
		tick = time.NewTicker(r.rate)
		defer tick.Stop()
	}
	total, failed := 0, 0
	var lastErr error
	for _, it := range items {
		if tick != nil {
			select {
			case <-ctx.Done():
				r.log.Warn("重新登记被中断", "done", total, "total", len(items))
				if r.onDone != nil {
					r.onDone(total, failed)
				}
				return
			case <-tick.C:
			}
		}
		total++
		if _, err := r.conns.Handshake(ctx, it.app, it.sub.String(), it.connID, it.meta, model.PolicyNone, 0, r.live.LiveNodes()); err != nil {
			failed++
			lastErr = err
		}
	}
	if failed > 0 {
		// 汇总一条而不是每条一行：十万连接的节点在 Redis 持续故障时会
		// 逐条失败，逐条打日志等于自己制造一场日志风暴。失败的连接不会
		// 自动重试——它们会从注册表里永久消失直到 client 自己重连，所以
		// 这条日志是运维唯一能发现这件事的入口，必须有。
		r.log.Warn("重新登记有失败条目，这些连接在注册表里将不可见直到 client 重连",
			"total", total, "failed", failed, "lastErr", lastErr)
	} else {
		r.log.Info("重新登记完成", "total", total)
	}
	if r.onDone != nil {
		r.onDone(total, failed)
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
