package grpcapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/store"
)

// TestShutdownCompletesWithOpenWatchStream 守住关闭顺序。
//
// Watch 是永不主动结束的长流。先 GracefulStop 再关 hub 的话，
// GracefulStop 会等一条永远不返回的 RPC——进程在收到 SIGTERM 后
// 永远停不下来，只能被 SIGKILL。容器环境里表现为每次滚动更新都要
// 等满终止宽限期，且旧实例在此期间仍持有连接。
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
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		env.server.Shutdown(shutdownCtx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("有 Watch 流打开时关闭挂死了——关闭顺序反了")
	}
}

// TestHubCloseUnblocksOpenWatchStream 更直接地守住关闭顺序背后真正依赖的
// 那份保证，绕开 Server.Shutdown 自己的超时兜底。
//
// 手工变异验证发现一个值得记录的坑：把 Server.Shutdown 里 hub.Close() 和
// GracefulStop() 的调用顺序反过来之后，TestShutdownCompletesWithOpenWatchStream
// 仍然会在 5 秒多之后通过——不是顺序其实无所谓，而是 grpc-go 的
// GracefulStop 与 Stop 内部共享同一把锁，Stop 在 GracefulStop 卡在
// sync.Cond.Wait 等待连接排空时能拿到锁、强制关掉连接，反过来把卡住的
// GracefulStop 也唤醒了——Shutdown 自带的"优雅超时后强制 Stop"兜底，
// 无意中把"顺序反了"这个 bug 的后果从"挂死"降级成了"变慢+强制断开"，
// 而给定测试的断言（10 秒内返回）并不区分这两者，于是测不出问题。
//
// 这条测试不给兜底任何介入机会：不经过 Server.Shutdown / GracefulStop /
// Stop，只调用 hub.Close() 本身，直接断言它足以让客户端已经打开的 Watch
// 流收到结束——这才是 Shutdown 之所以能快速返回、GracefulStop 之所以
// 不会永远卡住所依赖的那个底层机制。Close() 被改坏（比如变成空函数）时，
// 这条测试会因为客户端 Recv() 永远收不到结束而超时失败；上面那条给定
// 测试在同样的改坏下则可能因为兜底介入而继续通过。
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
// 几时完成"，用于证明 RevokeHub.Ready()（进而 Server.Ready、main.go 依赖
// 的就绪信号）确实等订阅真正建立才关闭。
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
// 保证：Ready() 必须等 hub 真正订阅上 Redis 才关闭，main.go 据此决定
// 何时才能开始 Serve（接受 SDK 连接）。
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
// main.go 靠 Ready() 与 Run 的返回错误二选一来判断能不能开始 Serve
// （见 cmd/fp/main.go 的 select）。如果 Ready() 在订阅失败之后还是被
// 误关闭，main.go 会误以为中继健康、正常开始接受连接——推送从此对
// 本实例永久失效，且没有任何报错，是比启动直接失败更难排查的故障模式。
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
