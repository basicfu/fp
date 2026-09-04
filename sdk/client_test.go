package fpsdk

import (
	"context"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// stubServer 是一个可编排的 AuthService 桩。
type stubServer struct {
	fpv1.UnimplementedAuthServiceServer

	validate   func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error)
	login      func(*fpv1.LoginRequest) (*fpv1.LoginResponse, error)
	logout     func(*fpv1.LogoutRequest) (*fpv1.LogoutResponse, error)
	watchReady chan struct{}            // 每次有流建立就发一个信号
	events     chan *fpv1.WatchResponse // 测试往这里塞事件
}

func (s *stubServer) ValidateToken(_ context.Context, req *fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
	return s.validate(req)
}

// Login/Logout 未被赋值时按 UnimplementedAuthServiceServer 处理
// （返回 codes.Unimplemented）——只有明确需要这两个 RPC 的测试才配置它们，
// 其余测试的桩服务端保持和之前完全一样的行为。
func (s *stubServer) Login(_ context.Context, req *fpv1.LoginRequest) (*fpv1.LoginResponse, error) {
	if s.login == nil {
		return s.UnimplementedAuthServiceServer.Login(context.Background(), req)
	}
	return s.login(req)
}

func (s *stubServer) Logout(_ context.Context, req *fpv1.LogoutRequest) (*fpv1.LogoutResponse, error) {
	if s.logout == nil {
		return s.UnimplementedAuthServiceServer.Logout(context.Background(), req)
	}
	return s.logout(req)
}

func (s *stubServer) Watch(stream grpc.BidiStreamingServer[fpv1.WatchRequest, fpv1.WatchResponse]) error {
	if err := stream.Send(&fpv1.WatchResponse{
		Event: &fpv1.WatchResponse_Ready{Ready: &fpv1.WatchReady{}},
	}); err != nil {
		return err
	}
	select {
	case s.watchReady <- struct{}{}:
	default:
	}
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev := <-s.events:
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// stubEnv 是一个连着桩服务端的 Client。Task 9~11 的测试全部复用它。
type stubEnv struct {
	stub   *stubServer
	client *Client
	// auth 是 client.Auth() 的缓存值，测试直接用它调 Validate/Login 等，
	// 不必每次都写 env.client.Auth()。
	auth *Auth
	// addr 是桩服务端的真实监听地址，重连测试要用它在原端口重启。
	addr string
	// stop 停掉桩服务端，模拟 fp 宕机。
	stop func()
}

// startStub 在 addr 上起一个绑定了 srv 的真实 gRPC 服务端：真实回环监听
// （net.Listen("tcp", ...)），不是 bufconn——重连测试需要"停掉、在原端口
// 重新监听"，bufconn 模拟不了"服务端消失又回来"。addr 为空串时监听系统
// 分配的随机端口。
//
// 返回真实监听地址（addr 为空串时由系统分配，调用方需要它才能在原地址
// 重启）与 stop 函数；stop 是同步的，等 Serve 的 goroutine 真正退出才返回。
func startStub(t *testing.T, addr string, srv fpv1.AuthServiceServer) (realAddr string, stop func()) {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	var lis net.Listener
	var err error
	// 重连测试要在同一端口上重新监听：上一个监听器刚关闭，操作系统释放
	// 端口可能有极短的滞后（尤其是 Windows）。重试几次而不是一次性
	// net.Listen，避免这个系统级时序缝隙偶发地把测试拖成假失败。
	for attempt := 0; attempt < 20; attempt++ {
		lis, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("net.Listen(%q): %v", addr, err)
	}

	grpcServer := grpc.NewServer()
	fpv1.RegisterAuthServiceServer(grpcServer, srv)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = grpcServer.Serve(lis)
	}()

	return lis.Addr().String(), func() {
		grpcServer.Stop()
		<-done
	}
}

// newStubEnv 起一个桩服务端并连上去。
//
// opt 用于调整 Options（例如 Task 9、10 加入连接层之外的字段后，用它们把
// 某个 TTL 改短、打开某个开关）。
//
// 注意：桩服务端**不挂认证拦截器**——本层测的是 SDK 的行为，服务端的
// 认证已由 Task 4 覆盖。
func newStubEnv(t *testing.T, validate func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error), opt ...func(*Options)) *stubEnv {
	t.Helper()

	stub := &stubServer{
		validate:   validate,
		watchReady: make(chan struct{}, 1),
		events:     make(chan *fpv1.WatchResponse, 16),
	}
	addr, stop := startStub(t, "", stub)

	opts := Options{
		Addr:      addr,
		AppID:     "t",
		AppSecret: "t",
		Insecure:  true,
	}
	for _, o := range opt {
		o(&opts)
	}

	client, err := New(opts)
	if err != nil {
		stop()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		stop()
	})

	return &stubEnv{stub: stub, client: client, auth: client.Auth(), addr: addr, stop: stop}
}

// pushRevoke 让桩服务端往流里推一条撤销事件。
func (e *stubEnv) pushRevoke(t *testing.T, ev *fpv1.RevokeEvent) {
	t.Helper()
	e.stub.events <- &fpv1.WatchResponse{Event: &fpv1.WatchResponse_Revoke{Revoke: ev}}
}

// pushPurge 让桩服务端往流里推一条清空指令。
func (e *stubEnv) pushPurge(t *testing.T, reason string) {
	t.Helper()
	e.stub.events <- &fpv1.WatchResponse{
		Event: &fpv1.WatchResponse_Purge{Purge: &fpv1.WatchPurge{Reason: reason}},
	}
}

// waitUntil 轮询到 cond 为真，超时则以 msg 失败。
//
// 推送是异步的，断言必须轮询而不是 sleep 一个固定时长——固定 sleep 要么
// 太短导致偶发失败，要么太长把测试拖慢，而且两者都会被人用"加长 sleep"糊过去。
func (e *stubEnv) waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// waitUntilTimeout 类似 waitUntil，但允许调用方指定超时。
//
// 重连测试需要比默认 5 秒更长的窗口：断线重连不仅要走 SDK 应用层自己的
// backoff（本文件测的对象），还要先等 grpc-go ClientConn 自身对断开连接
// 的内部重连退避（默认约 1 秒起、指数增长，是与应用层 backoff 完全独立的
// 一套机制）转回来，两者叠加在较慢的机器上可能超过 5 秒。
func waitUntilTimeout(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// okValidate 返回一个总是成功的 validate 回调。
func okValidate(userID string, cacheTTLMs int64) func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
	return func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return &fpv1.ValidateTokenResponse{UserId: userID, SessionId: "s1", CacheTtlMs: cacheTTLMs}, nil
	}
}

// failValidate 返回一个总是拒绝的 validate 回调。
func failValidate() func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
	return func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return nil, status.Error(codes.Unauthenticated, "token 无效或已过期")
	}
}

// gatedReadyServer 是只给 TestStreamHealthyOnlyAfterReady 用的最小 AuthService
// 实现：Watch 建立后卡住不发 ready，直到 releaseReady 被关闭。
//
// 不复用 stubServer：stubServer.Watch 建流后立刻发 ready，本机回环网络下
// "建流"到"收到 ready"的间隔通常不到 1 毫秒，观测窗口太窄——"只有收到
// ready 才健康"和"建流即健康"这两种实现在这个窗口里几乎无法被区分开。
// 这个类型的唯一目的就是把这段间隔撑开到测试能可靠采样的量级。
type gatedReadyServer struct {
	fpv1.UnimplementedAuthServiceServer
	releaseReady chan struct{}
}

func (s *gatedReadyServer) Watch(stream grpc.BidiStreamingServer[fpv1.WatchRequest, fpv1.WatchResponse]) error {
	select {
	case <-s.releaseReady:
	case <-stream.Context().Done():
		return nil
	}
	if err := stream.Send(&fpv1.WatchResponse{
		Event: &fpv1.WatchResponse_Ready{Ready: &fpv1.WatchReady{}},
	}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return nil
}

// TestStreamHealthyOnlyAfterReady 守住"ready 才算健康"。
//
// 光靠"建流没报错"是不够的：流建立了但服务端还没订上 Redis 的那段时间里
// 撤销事件会丢，而 SDK 却以为推送可用、不收紧缓存窗口。
// ready 是服务端"我已经订上了"的显式承诺。
//
// 变异验证：把 client.go watchOnce 里的 c.streamUp.Store(true) 从"收到
// ready 分支"挪到"c.rpc.Watch(ctx) 刚返回、还没进 Recv 循环"那里——这正是
// 这条性质要防的 bug。挪动之后，下面第一段循环会在服务端还卡在
// releaseReady 上（还没发 ready）时就观测到 StreamHealthy()==true，测试
// 失败。
func TestStreamHealthyOnlyAfterReady(t *testing.T) {
	gated := &gatedReadyServer{releaseReady: make(chan struct{})}
	addr, stop := startStub(t, "", gated)
	t.Cleanup(stop)

	client, err := New(Options{Addr: addr, AppID: "t", AppSecret: "t", Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// 服务端此刻已经在处理这次 Watch（否则不会卡在 releaseReady 上），
	// 但故意不发 ready。这段时间里 StreamHealthy() 必须一直是 false。
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if client.StreamHealthy() {
			t.Fatal("服务端还没发送 ready，StreamHealthy() 就已经是 true 了")
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(gated.releaseReady) // 放行，模拟服务端订阅完成、发出 ready。

	waitUntilTimeout(t, 5*time.Second, client.StreamHealthy,
		"放行 ready 之后，StreamHealthy() 仍未变为 true")
}

// TestStreamHealthyGoesFalseOnDisconnect 守住断开可感知。
//
// 这是整个降级策略的前提：感知不到断开，就无从收紧缓存窗口，
// "推送不可用时把安全性拉回来"就是一句空话。
func TestStreamHealthyGoesFalseOnDisconnect(t *testing.T) {
	env := newStubEnv(t, okValidate("u1", 1000))
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

	env.stop()

	env.waitUntil(t, func() bool { return !env.client.StreamHealthy() },
		"服务端停止后，StreamHealthy() 应变为 false")
}

// TestWatchReconnects 守住重连。
//
// fp 重启是常规操作。SDK 若不重连，推送就此永久失效，
// 而回源仍然正常——所以症状是"踢下线要等一个 cache_ttl 才生效"，
// 隐蔽且只在生产偶发。
//
// 必须用真实回环监听在原端口重启：bufconn 的监听器一旦关闭无法重新
// Listen 出一个"同地址"的新实例，模拟不出"fp 进程重启，SDK 沿用同一个
// 地址重连"这个场景。
func TestWatchReconnects(t *testing.T) {
	env := newStubEnv(t, okValidate("u1", 1000))
	env.waitUntil(t, env.client.StreamHealthy, "初次建流应变为健康")

	env.stop()
	env.waitUntil(t, func() bool { return !env.client.StreamHealthy() },
		"服务端停止后应变为不健康")

	_, restartStop := startStub(t, env.addr, env.stub)
	t.Cleanup(restartStop)

	waitUntilTimeout(t, 20*time.Second, env.client.StreamHealthy,
		"服务端在原地址重启后，StreamHealthy() 在 20 秒内仍未重新变为 true")
}

// TestCloseStopsWatchLoop 守住不泄漏。
//
// 断言 Close() 之后 goroutine 数回落到基线。业务方可能在测试里
// 反复 New/Close，每次泄漏一个重连循环的话会越积越多。
func TestCloseStopsWatchLoop(t *testing.T) {
	// 预热一轮：grpc-go 有些内部机制是进程级懒加载的，第一次建连接会
	// 多出这些一次性 goroutine。先建立并关闭一个客户端，让这类懒加载在
	// 测量基线之前完成，避免把它们误记成本测试要抓的泄漏。
	warm := newStubEnv(t, okValidate("u1", 1000))
	warm.waitUntil(t, warm.client.StreamHealthy, "预热建流应变为健康")
	if err := warm.client.Close(); err != nil {
		t.Fatalf("预热 Close: %v", err)
	}
	warm.stop()

	before := goroutineCountAfterSettling()

	const n = 5
	envs := make([]*stubEnv, n)
	for i := range envs {
		envs[i] = newStubEnv(t, okValidate("u1", 1000))
	}
	for _, e := range envs {
		e.waitUntil(t, e.client.StreamHealthy, "建流应变为健康")
	}
	for _, e := range envs {
		if err := e.client.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		e.stop()
	}

	after := goroutineCountAfterSettling()
	if after > before {
		t.Fatalf("%d 个客户端全部 Close 之后 goroutine 数为 %d，Close 之前的基线为 %d"+
			"——runWatch 循环疑似未随 Close 退出", n, after, before)
	}
}

// rejectingWatchServer 是只给 TestWatchBackoffResetsAfterReady 用的
// AuthService 实现：Watch 每次都立刻失败、从不发 ready，并用原子计数器
// 记录被调用的次数，供测试判断"已经连续失败了几次"，不必靠猜测的
// sleep 时长来确认退避确实已经涨起来了。
type rejectingWatchServer struct {
	fpv1.UnimplementedAuthServiceServer
	attempts atomic.Int64
}

func (s *rejectingWatchServer) Watch(grpc.BidiStreamingServer[fpv1.WatchRequest, fpv1.WatchResponse]) error {
	s.attempts.Add(1)
	return status.Error(codes.Unavailable, "模拟连接失败：从不发 ready")
}

// TestWatchBackoffResetsAfterReady 守住退避复位。
//
// 简报明确要求（且不是可选优化）：runWatch 的退避必须区分"从未收到过
// ready 就断开"（fp 本身没起来，继续按原节奏增长）与"收到过 ready、且
// 这次连接存活超过 healthyConnDuration 才断开"（曾经真正健康过，这次
// 断开大概率只是短暂抖动，退避该回到最小值）。不区分的话，一次长时间的
// fp 故障会把退避顶到高位，此后哪怕只是被 MaxConnectionAge 正常回收之类
// 的短暂抖动，SDK 也要按顶到的退避傻等，而不是立刻重连。
//
// 这条不是简报给的四条之一，是补的第五条：手工验证过，把 client.go
// runWatch 里的复位逻辑整个删掉（也就是简报正文给的原始版本，退避
// 只增不减）之后，简报点名的四条测试全部照样通过——TestWatchReconnects
// 只经历一次"从未健康过"的断开，never 触及 backoff 已经涨起来之后
// 又被复位这条路径，测不出复位是否发生。这条测试专门补上这个盲区。
//
// 装配：先指向一个只会立刻拒绝 Watch、从不发 ready 的桩服务端，逼 SDK
// 连续失败几次、把 backoff 顶到远高于 minBackoff（用原子计数器判断
// "已经失败了几次"，不靠 sleep 一个猜测的时长）；换上真实桩服务端，
// 等它连上并收到一次 ready；**让这次连接存活超过 healthyConnDuration**
// （仅仅收到 ready 不够，见该常量的注释——这是本测试与
// TestWatchBackoffDoesNotResetForShortLivedConnection 的唯一区别）；
// 立刻停掉制造一次"曾经健康"之后的断开——这正是复位应该发生的地方；
// 最后立刻重启，断言重连发生得很快。只有退避真的被复位，这次重连才
// 可能落在几百毫秒量级，否则会被顶到秒级的退避拖住——两者相差接近
// 一个数量级，用居中的超时就能可靠区分，不依赖精确计时。这个"重连耗时
// 远小于被推高后的退避值"的时长比较是本测试的核心，加健康门槛之后
// 依然保留，只是多了一步等待门槛的前置条件。
//
// 变异验证：删掉 client.go runWatch 里 `if healthy { backoff = minBackoff }`
// 那几行，只留无条件 `backoff *= 2`，本测试最后一步会因为重连没能在
// 宽限时间内完成而失败（已手工验证，见 cleanup-report.md）。
func TestWatchBackoffResetsAfterReady(t *testing.T) {
	rejecter := &rejectingWatchServer{}
	addr, stopRejecter := startStub(t, "", rejecter)
	t.Cleanup(func() { stopRejecter() })

	client, err := New(Options{Addr: addr, AppID: "t", AppSecret: "t", Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// 逼退避涨到"第 4 次失败"对应的等待值（1.6s 这一档），远高于
	// minBackoff=200ms，才能在下面制造出可靠区分两种实现的差距。
	waitUntilTimeout(t, 15*time.Second, func() bool { return rejecter.attempts.Load() >= 4 },
		"退避增长阶段：Watch 未能在 15 秒内累计失败 4 次")
	stopRejecter()

	stub := &stubServer{
		validate:   okValidate("u1", 1000),
		watchReady: make(chan struct{}, 1),
		events:     make(chan *fpv1.WatchResponse, 16),
	}
	_, stopStub := startStub(t, addr, stub)
	t.Cleanup(stopStub)

	// 给足时间：这次重连要先等掉退避增长阶段剩下的那段等待（最多约
	// 1.6 秒）加上一次真实的传输层重连，都完成之后才会第一次收到 ready。
	waitUntilTimeout(t, 15*time.Second, client.StreamHealthy,
		"换上真实服务端后，StreamHealthy 未能在 15 秒内变为 true")

	// 必须让这次连接存活超过 healthyConnDuration，退避复位的门槛才会
	// 满足——仅仅收到过 ready 不够（见该常量的注释）。stub 收到 ready 后
	// 只是空转等下一个事件，不会自己断开，这段时间里连接会一直存活。
	time.Sleep(healthyConnDuration)

	stopStub() // 制造一次"曾经健康"之后的断开——退避复位应该在这里生效。

	// 必须先确认 StreamHealthy 真的翻到了 false，才能开始计时重连——
	// 否则下面的最终断言可能读到 stopStub 之前留下的、还没来得及被
	// runWatch 更新的陈旧 true 值，第一次轮询就"通过"，根本没有真的
	// 等过一次重连。这不是理论风险：初版就是这么写的，实测里两种实现
	// （复位/不复位）耗时一模一样，就是因为最终断言从未真正等待过。
	waitUntilTimeout(t, 5*time.Second, func() bool { return !client.StreamHealthy() },
		"停止服务端后，StreamHealthy 未能变为 false")

	_, restartStub := startStub(t, addr, stub)
	t.Cleanup(restartStub)

	// 退避已复位的实现应在小几百毫秒内重连成功；若退避在这里被顶到了
	// 秒级以上（简报原文给的、退避只增不减的版本，实测约 3.2 秒起），
	// 2.5 秒的宽限时间会让这条断言可靠地失败。
	waitUntilTimeout(t, 2500*time.Millisecond, client.StreamHealthy,
		"曾经健康过一次之后的重连未能在 2.5 秒内完成——退避疑似没有复位")
}

// flappingReadyServer 是只给
// TestWatchBackoffDoesNotResetForShortLivedConnection 用的 AuthService
// 实现：Watch 建立后立刻发一次 ready，Send 一成功就直接返回——服务端
// 主动关闭这条流，从不像 stubServer 那样停留等待。用于制造"连上→吐一次
// ready→立刻断开"这种典型抖动，连接存活时间是一次本机回环 RPC 的量级
// （通常远低于 1 毫秒），远低于 healthyConnDuration（5 秒）。
type flappingReadyServer struct {
	fpv1.UnimplementedAuthServiceServer
	attempts atomic.Int64
}

func (s *flappingReadyServer) Watch(stream grpc.BidiStreamingServer[fpv1.WatchRequest, fpv1.WatchResponse]) error {
	s.attempts.Add(1)
	return stream.Send(&fpv1.WatchResponse{
		Event: &fpv1.WatchResponse_Ready{Ready: &fpv1.WatchReady{}},
	})
	// Send 成功后直接 return nil：服务端主动结束这个 RPC，客户端的下一次
	// stream.Recv() 会拿到 io.EOF。gotReady 已经是 true，但连接寿命只有
	// 这次 Send 的耗时。
}

// TestWatchBackoffDoesNotResetForShortLivedConnection 守住新加的健康
// 门槛：仅仅"收到过 ready"不足以复位退避，连接还必须存活超过
// healthyConnDuration。
//
// 少了这个门槛，一个"连上→吐一次 ready→立刻断开"式抖动的服务端会让
// SDK 反复把退避打回 minBackoff，以约 1/minBackoff（200ms，即约每秒 5
// 次）的频率持续冲击一个本就不稳的服务端，而不是随着连续失败退避增长。
//
// 装配：先用 rejectingWatchServer 把退避顶到远高于 minBackoff（与
// TestWatchBackoffResetsAfterReady 完全相同的手法）；换上
// flappingReadyServer——它会发 ready 但立刻断开，连接存活时间远低于
// healthyConnDuration；记录它第一次被连接的时刻，再等它第二次被连接，
// 用两次连接之间的真实间隔来判断退避是否被复位：
//
//   - 若退避被这次"收到 ready 但秒断"的连接错误复位，下一次重连会在
//     minBackoff（200ms）量级重新发生；
//   - 若退避正确地没有被复位，ramp-up 阶段已经把它顶到至少 1.6 秒
//     （见 TestWatchBackoffResetsAfterReady 的推导），中途没有任何一次
//     真正健康的长连接把它打回去，间隔至少还是这个量级。
//
// 1.2 秒的门槛在两者之间留了足够裕量，不依赖精确计时。
func TestWatchBackoffDoesNotResetForShortLivedConnection(t *testing.T) {
	rejecter := &rejectingWatchServer{}
	addr, stopRejecter := startStub(t, "", rejecter)
	t.Cleanup(func() { stopRejecter() })

	client, err := New(Options{Addr: addr, AppID: "t", AppSecret: "t", Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	waitUntilTimeout(t, 15*time.Second, func() bool { return rejecter.attempts.Load() >= 4 },
		"退避增长阶段：Watch 未能在 15 秒内累计失败 4 次")
	stopRejecter()

	flapper := &flappingReadyServer{}
	_, stopFlapper := startStub(t, addr, flapper)
	t.Cleanup(func() { stopFlapper() })

	waitUntilTimeout(t, 15*time.Second, func() bool { return flapper.attempts.Load() >= 1 },
		"换上会立刻断开的服务端后，15 秒内未见到第一次连接尝试")
	firstAttemptAt := time.Now()

	waitUntilTimeout(t, 15*time.Second, func() bool { return flapper.attempts.Load() >= 2 },
		"15 秒内未见到第二次连接尝试")
	gap := time.Since(firstAttemptAt)

	if gap < 1200*time.Millisecond {
		t.Fatalf("换服务端后两次连接尝试间隔只有 %s，退避疑似被短命连接错误复位——"+
			"这次连接存活时间远低于 healthyConnDuration=%s，不该让退避回到 minBackoff",
			gap, healthyConnDuration)
	}
}

// TestOnRevokeCallbackReceivesEvent 守住 Options.OnRevoke 被调用、且事件
// 字段与 fp.v1.RevokeEvent 一一对应地传递过去——这是 fp-im 关闭被撤销
// token 对应 ws 连接的唯一入口，回调收不到事件或字段对不上，fp-im 那边
// 就什么都做不了。
func TestOnRevokeCallbackReceivesEvent(t *testing.T) {
	got := make(chan RevokeEvent, 1)
	env := newStubEnv(t, okValidate("u1", 1000), func(o *Options) {
		o.OnRevoke = func(ev RevokeEvent) { got <- ev }
	})
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

	env.pushRevoke(t, &fpv1.RevokeEvent{
		Tokens: []string{"tok-1"}, AppId: "app-1", Reason: "logout", AtMs: 123,
	})

	select {
	case ev := <-got:
		if len(ev.Tokens) != 1 || ev.Tokens[0] != "tok-1" || ev.AppID != "app-1" ||
			ev.Reason != "logout" || ev.AtMs != 123 {
			t.Fatalf("回调收到的事件字段不对：%+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("5 秒内回调没被调用")
	}
}

// TestOnRevokeNilCallbackDoesNotPanic 守住"OnRevoke 为 nil 时不崩"。
//
// 绝大多数 SDK 使用方不会设置这个新增的可选字段——旧调用方的 Options
// 字面量里根本不会出现它，零值就是 nil。onRevoke 在 watch 协程里同步
// 执行，对 nil 回调发起裸调用会直接 panic，崩掉的是整条推送流的读
// 循环乃至宿主进程；而且只有在真的收到一条撤销事件时才会触发，
// 本地冒烟测试很容易碰不到，带着这个隐患一路到生产。
//
// 不直接用 recover 去抓 panic，而是断言撤销仍然生效（缓存被清掉、
// 触发了一次重新回源）：如果 onRevoke 因为 nil 回调 panic，整个 watch
// 协程会退出，缓存也就再也不会被清掉，下面的轮询会一直等不到，
// 用它来间接确认"没有崩"，测试本身不必比被测代码更复杂。
func TestOnRevokeNilCallbackDoesNotPanic(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 300_000}, nil
	})
	// 不设置 OnRevoke，保持零值 nil——这是绝大多数调用方的真实场景。
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("预热: %v", err)
	}

	env.pushRevoke(t, &fpv1.RevokeEvent{Tokens: []string{"tok"}})

	env.waitUntil(t, func() bool {
		_, err := env.auth.Validate(context.Background(), "tok")
		return err == nil && calls.Load() == 2
	}, "nil 回调场景下撤销似乎没有生效——watch 协程可能已经因 panic 退出")
}

// goroutineCountAfterSettling 反复采样 runtime.NumGoroutine()，等它连续
// 200ms 不再变化后返回，用于在后台 goroutine 正处于退出过程中的间隙里
// 拿到一个稳定值，而不是被"还没退出完"的中间态污染基线或结果。
func goroutineCountAfterSettling() int {
	runtime.GC()
	last := runtime.NumGoroutine()
	stableSince := time.Now()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		runtime.GC()
		cur := runtime.NumGoroutine()
		if cur != last {
			last = cur
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= 200*time.Millisecond {
			return cur
		}
	}
	return last
}
