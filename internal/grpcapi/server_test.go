package grpcapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/basicfu/fp/internal/store"
)

// TestShutdownCompletesWithOpenWatchStream 守住关闭顺序。
//
// Watch 是永不主动结束的长流。先 GracefulStop 再关 hub 的话，
// GracefulStop 会等一条永远不返回的 RPC——进程在收到 SIGTERM 后
// 永远停不下来，只能被 SIGKILL。容器环境里表现为每次滚动更新都要
// 等满终止宽限期，且旧实例在此期间仍持有连接。
//
// 内层 Shutdown 故意不给超时（context.Background()）：如果 hub.Close()
// 与 GracefulStop() 的调用顺序被改坏，Shutdown 内部就没有任何 ctx-based
// 兜底能把它救回来，只靠下面外层的 time.After(10s) 兜底判定失败。这里
// 原本给了内层 5 秒超时，手工变异验证发现那样测不出问题：grpc-go 的
// GracefulStop/Stop 内部共享同一把锁（server.go 的 s.mu），GracefulStop
// 卡在 sync.Cond.Wait 等待连接排空时会释放这把锁，Stop 能抢到锁强行推平
// 连接、顺带唤醒卡住的 GracefulStop——Shutdown 自带的"优雅超时后强制
// Stop"兜底，会把"顺序反了"这个 bug 的后果从"挂死"降级成"变慢
// （5 秒多）+ 强制断开客户端连接"，10 秒内还是能返回，这条测试测不出来。
// 去掉内层超时后，顺序反了就真的没有任何东西能让 Shutdown 返回，只能
// 靠外层的 t.Fatal 兜底。
func TestShutdownCompletesWithOpenWatchStream(t *testing.T) {
	env := newGRPCEnv(t)
	ctx, cancel := context.WithCancel(env.authed(context.Background()))
	defer cancel()

	stream, err := env.client.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if _, err := stream.Recv(); err != nil { // 等 ready，确保流真的建起来了
		t.Fatalf("等待 ready: %v", err)
	}

	done := make(chan struct{})
	go func() {
		env.server.Shutdown(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("有 Watch 流打开时关闭挂死了——关闭顺序反了")
	}
}

// TestHubCloseUnblocksOpenWatchStream 补强上面那条测试，单独钉住关闭顺序
// 真正依赖的底层机制，绕开 Server.Shutdown 自己的 GracefulStop/Stop
// 编排——即使 Shutdown 内部的编排以后又出现类似 GracefulStop/Stop 共享锁
// 那样的意外互相掩盖，这条测试也不受影响：它只调用 hub.Close() 本身，
// 直接断言它足以让客户端已经打开的 Watch 流收到结束。Close() 被改坏
// （比如变成空函数）时，这条测试会因为客户端 Recv() 永远收不到结束而
// 超时失败。
func TestHubCloseUnblocksOpenWatchStream(t *testing.T) {
	env := newGRPCEnv(t)
	ctx, cancel := context.WithCancel(env.authed(context.Background()))
	defer cancel()

	stream, err := env.client.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if _, err := stream.Recv(); err != nil { // 等 ready
		t.Fatalf("等待 ready: %v", err)
	}

	env.server.hub.Close()

	done := make(chan struct{})
	go func() {
		_, _ = stream.Recv() // 期望服务端结束了流，而不是永远不发不关
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RevokeHub.Close() 之后，已打开的 Watch 流仍未收到结束——" +
			"Watch handler 不会返回，GracefulStop 会永远等它")
	}
}

// fakeSubscriber 是 revokeSubscriber 的测试替身，让测试能精确控制"订阅
// 几时完成"，用于证明 RevokeHub.Ready()（进而 Server.Ready、
// ServeWhenReady 依赖的就绪信号）确实等订阅真正建立才关闭。
//
// 不用真实 Redis 做这件事：局域网内一次 SUBSCRIBE 确认通常只要个位数
// 毫秒，测试没办法可靠地在它完成前后各观测一次状态——真做的话，
// "Run 调用后、订阅完成前 Ready() 还没关闭"这类断言会因为快慢不定而
// 时灵时不灵；哪怕重新引入这个任务要防的 bug（把 close(ready) 挪到
// subscribe 之前），真实 Redis 快到下游断言大概率照样通过，测试形同虚设。
type fakeSubscriber struct {
	// proceed 关闭之前，Subscribe 一直不返回，模拟"订阅还没确认建立"。
	proceed chan struct{}
}

func (f *fakeSubscriber) Subscribe(ctx context.Context) (<-chan store.RevokeSignal, func(), error) {
	select {
	case <-f.proceed:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	return make(chan store.RevokeSignal), func() {}, nil
}

// TestReadyWaitsForSubscriptionToComplete 守住 Task 7 补的那条启动顺序
// 保证在 RevokeHub 这一层的原语：Ready() 必须等 hub 真正订阅上 Redis 才
// 关闭。ServeWhenReady（进而 main.go）就是靠这个信号决定何时才能开始
// 接受连接，见 TestServeWhenReadyDoesNotAcceptBeforeReady——这条测试守
// 的是更底层的因果关系本身。
//
// 用 fakeSubscriber 卡住 Subscribe 不返回，先证明"订阅没完成时 Ready()
// 不会关闭"，再放行证明"订阅一完成 Ready() 立刻关闭"。变异验证：把
// RevokeHub.Run 里 close(h.ready) 挪到 h.subscribe(ctx) 之前（也就是
// 重新引入这条任务要防的 bug），第一段断言会立刻失败——fake.Subscribe
// 还卡在 <-f.proceed 上，Ready() 却已经关闭了。
func TestReadyWaitsForSubscriptionToComplete(t *testing.T) {
	fake := &fakeSubscriber{proceed: make(chan struct{})}
	hub := newRevokeHub()
	hub.pub = fake

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- hub.Run(ctx) }()

	select {
	case <-hub.Ready():
		t.Fatal("fake.Subscribe 还没返回，Ready() 就已经关闭了——" +
			"顺序反了，ready 没有真正等订阅建立")
	case <-time.After(200 * time.Millisecond):
		// 符合预期：订阅还卡着，不该 ready。
	}

	close(fake.proceed) // 放行，模拟 Redis 的 SUBSCRIBE 确认刚刚返回。

	select {
	case <-hub.Ready():
		// 符合预期。
	case <-time.After(2 * time.Second):
		t.Fatal("放行 fake.Subscribe 之后 Ready() 仍未关闭")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Run 没有返回")
	}
}

// failingSubscriber 让 Subscribe 立即失败，模拟 Redis 不可达之类的订阅
// 失败场景。
type failingSubscriber struct{ err error }

func (f failingSubscriber) Subscribe(context.Context) (<-chan store.RevokeSignal, func(), error) {
	return nil, nil, f.err
}

// TestReadyNeverClosesWhenSubscriptionFails 守住启动顺序要求的另一半：
// 订阅失败必须让启动失败，而不是带着一个永远收不到事件的中继继续跑。
//
// ServeWhenReady 靠 Ready() 与 Run 的返回错误二选一来判断能不能开始
// Serve（见 grpcapi/server.go 的 ServeWhenReady）。如果 Ready() 在订阅
// 失败之后还是被误关闭，ServeWhenReady 会误以为中继健康、正常开始接受
// 连接——推送从此对本实例永久失效，且没有任何报错，是比启动直接失败
// 更难排查的故障模式。
func TestReadyNeverClosesWhenSubscriptionFails(t *testing.T) {
	wantErr := errors.New("boom：模拟 redis 不可达")
	hub := newRevokeHub()
	hub.pub = failingSubscriber{err: wantErr}

	err := hub.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() 返回 %v，期望包装了 %v", err, wantErr)
	}

	select {
	case <-hub.Ready():
		t.Fatal("订阅失败后 Ready() 仍被关闭了")
	default:
	}
}

// TestServeWhenReadyDoesNotAcceptBeforeReady 把启动顺序这条保证钉在它
// 实际生效的位置：main.go 现在只调用 Server.ServeWhenReady，不再自己
// 编排 Run/Ready/Serve 三个方法——这段编排本身没有任何测试覆盖它在
// main.go 里的调用方式（cmd/fp 是 package main，这个仓库没有它的测试
// 基础设施），所以必须在这里、也就是编排代码实际所在的地方钉死它。
//
// 用 fakeSubscriber 卡住 Subscribe，断言这期间往 lis 拨号会超时——
// bufconn.Listener 的 Accept 与 Dial 之间是一个无缓冲 channel 的握手
// （见 google.golang.org/grpc/test/bufconn 的实现），Serve 没有开始跑
// Accept 循环，Dial 就永远等不到对端，会在 ctx 到期时返回超时错误而不是
// 连上。放行订阅之后，同一个监听器应该很快能被拨通，证明 Serve 确实是
// 在 Ready() 关闭之后才开始接受连接的。
//
// 变异验证：把 ServeWhenReady 改成不等 Ready、直接调 Serve，第一次拨号
// 会连上（err == nil），断言失败。
func TestServeWhenReadyDoesNotAcceptBeforeReady(t *testing.T) {
	fake := &fakeSubscriber{proceed: make(chan struct{})}
	hub := newRevokeHub()
	hub.pub = fake
	srv := &Server{grpc: grpc.NewServer(), hub: hub}

	lis := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.ServeWhenReady(ctx, lis, 5*time.Second) }()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	_, dialErr := lis.DialContext(dialCtx)
	dialCancel()
	if dialErr == nil {
		t.Fatal("hub 还没订阅上 Redis，ServeWhenReady 却已经开始接受连接了——启动顺序反了")
	}

	close(fake.proceed) // 放行订阅，模拟 Redis 的 SUBSCRIBE 确认刚刚返回。

	dialCtx2, dialCancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	conn, dialErr2 := lis.DialContext(dialCtx2)
	dialCancel2()
	if dialErr2 != nil {
		t.Fatalf("订阅就绪后仍然连不上: %v", dialErr2)
	}
	_ = conn.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)

	select {
	case <-serveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown 之后 ServeWhenReady 没有返回")
	}
}

// TestServeWhenReadyReportsSubscribeFailure 补上 ServeWhenReady 的
// runFailed 分支，钉住"订阅失败时返回的错误确实来自订阅失败这条路径"，
// 不是随便一个非 nil 错误。
//
// 把编排搬进 grpcapi 的初衷就是让这类分支能被 bufconn 覆盖到——搬完了
// 却只测 happy path（TestServeWhenReadyDoesNotAcceptBeforeReady）等于
// 没搬。这条测试现在还直接关系到 cmd/fp/main.go 的退出码修复能不能被
// 信任：main.go 靠 ServeWhenReady 返回非 nil 错误来判断要不要把进程的
// 退出码改成非零，错误得先被这里正确产出，main.go 那边的传播才有意义。
//
// 用 errors.Is 而不是只判 err != nil：即使把 ServeWhenReady 实现改成
// "任何分支都返回同一个固定错误"，只判 err != nil 也会通过，测不出走的
// 是不是这条分支。
func TestServeWhenReadyReportsSubscribeFailure(t *testing.T) {
	wantErr := errors.New("boom：模拟 redis 不可达")
	hub := newRevokeHub()
	hub.pub = failingSubscriber{err: wantErr}
	srv := &Server{grpc: grpc.NewServer(), hub: hub}

	lis := bufconn.Listen(1 << 20)
	err := srv.ServeWhenReady(context.Background(), lis, 5*time.Second)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ServeWhenReady() = %v，期望包装了 %v", err, wantErr)
	}
}

// TestServeWhenReadyReportsTimeout 补上 ServeWhenReady 的超时分支：
// hub 订阅迟迟不完成时，必须在 timeout 之后返回一个能让调用方分辨
// "是超时、不是别的失败"的错误，而不是一直卡着或者返回订阅失败那条
// 分支的错误。理由同上一条：main.go 的退出码修复要能被信任，这条分支
// 得先被证明真的会触发、返回的确实是超时错误。
//
// 用极短的 timeout（50ms）而不是等一个真实场景量级的超时：fake.Subscribe
// 靠 f.proceed 卡住不返回，只要 timeout 比测试进程的调度抖动大得多就够
// 了，不需要真的等到接近生产用的 10 秒——那样会把这条测试拖慢一万倍，
// 换不来任何额外的确定性。
func TestServeWhenReadyReportsTimeout(t *testing.T) {
	fake := &fakeSubscriber{proceed: make(chan struct{})}
	hub := newRevokeHub()
	hub.pub = fake
	srv := &Server{grpc: grpc.NewServer(), hub: hub}

	lis := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 让还卡在 fake.Subscribe 里的后台 goroutine 能退出，不泄漏

	start := time.Now()
	err := srv.ServeWhenReady(ctx, lis, 50*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("ServeWhenReady() 返回 nil，期望超时错误")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("ServeWhenReady() = %v，期望是超时错误，不是别的分支", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("ServeWhenReady() 用了 %v 才返回，超时分支不该等这么久", elapsed)
	}
}
