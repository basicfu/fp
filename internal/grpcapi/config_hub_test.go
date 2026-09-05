package grpcapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
)

// 一条信号只送给它自己那个应用的订阅者。
// 【辨别力】必须有第二个应用的订阅者在场并断言它**没收到**，
// 否则一个"广播给所有人"的实现照样会绿。
func TestConfigHubRoutesByApp(t *testing.T) {
	h, signals := newTestConfigHub(t)

	appA, appB := uuid.New(), uuid.New()
	chA, stopA := h.Subscribe(appA)
	defer stopA()
	chB, stopB := h.Subscribe(appB)
	defer stopB()

	signals <- store.ConfigSignal{AppID: appA, Type: domain.ConfigTypeWeb, Seq: 3}

	select {
	case ev := <-chA:
		if ev.Type != domain.ConfigTypeWeb || ev.Seq != 3 {
			t.Fatalf("A 收到 %+v，期望 {WEB 3}", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("A 没收到事件")
	}

	select {
	case ev := <-chB:
		t.Fatalf("B 不该收到任何事件，却收到了 %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// Gap 广播给全部订阅者，且 Type 为空串——语义是"分区未知，重拉全部"。
func TestConfigHubFansOutGapToEveryone(t *testing.T) {
	h, signals := newTestConfigHub(t)

	chA, stopA := h.Subscribe(uuid.New())
	defer stopA()
	chB, stopB := h.Subscribe(uuid.New())
	defer stopB()

	signals <- store.ConfigSignal{Gap: true}

	for name, ch := range map[string]<-chan ConfigEvent{"A": chA, "B": chB} {
		select {
		case ev := <-ch:
			if ev.Type != "" {
				t.Fatalf("%s 收到的 Type = %q，Gap 事件必须是空串（重拉全部）", name, ev.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s 没收到 Gap 事件", name)
		}
	}
}

// 缓冲满时摘掉订阅者：channel 被关闭，Watch handler 因此正常结束，
// SDK 重连后走 ready 重拉，不会漏配置。
func TestConfigHubDropsSaturatedSubscriber(t *testing.T) {
	h, signals := newTestConfigHub(t)
	appID := uuid.New()
	ch, stop := h.Subscribe(appID)
	defer stop()

	// 灌满缓冲再多推几条，且**不消费**。
	for i := 0; i < configBufferSize+5; i++ {
		signals <- store.ConfigSignal{AppID: appID, Type: domain.ConfigTypeDefault, Seq: int64(i)}
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel 已关闭，正是期望
			}
		case <-deadline:
			t.Fatal("缓冲满之后订阅者仍未被摘掉")
		}
	}
}

// newTestConfigHub 返回一个只吃假信号的 hub，完全绕开 Redis。
// 与 watch.go 的 newTestHubWithFakeSignals 同一手法：测的是分发逻辑本身，
// 不是 go-redis 的重连行为——后者既慢又不稳定。
func newTestConfigHub(t *testing.T) (*ConfigHub, chan store.ConfigSignal) {
	t.Helper()
	h := newConfigHub()
	signals := make(chan store.ConfigSignal, 64)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); h.Close() })
	go h.run(ctx, signals)
	return h, signals
}

// fakeConfigSubscriber 是 configSubscriber 的测试替身，让测试能精确控制
// "订阅几时完成"，用于证明 ConfigHub.Ready()（进而 Server.Ready 依赖的
// 就绪信号）确实等订阅真正建立才关闭。手法与 server_test.go 的
// fakeSubscriber（RevokeHub 版）完全一致，理由同样适用：真实 Redis 局域网
// 内一次 SUBSCRIBE 确认通常只要个位数毫秒，测试没办法可靠地在它完成前后
// 各观测一次状态——真做的话，"Run 调用后、订阅完成前 Ready() 还没关闭"
// 这类断言会因为快慢不定而时灵时不灵；哪怕重新引入这条测试要防的 bug，
// 真实 Redis 快到下游断言大概率照样通过，测试形同虚设。
type fakeConfigSubscriber struct {
	// proceed 关闭之前，Subscribe 一直不返回，模拟"订阅还没确认建立"。
	proceed chan struct{}
}

func (f *fakeConfigSubscriber) Subscribe(ctx context.Context) (<-chan store.ConfigSignal, func(), error) {
	select {
	case <-f.proceed:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	return make(chan store.ConfigSignal), func() {}, nil
}

// TestConfigHubReadyWaitsForSubscriptionToComplete 守住 ConfigHub 这一层
// 的启动顺序原语，与 server_test.go 的
// TestReadyWaitsForSubscriptionToComplete（RevokeHub 版）是同一件事的
// 两处独立证明：Ready() 必须等 configHub 真正订阅上 Redis 才关闭，不能
// 只靠"Run 被调用了"这件事本身。
//
// 这条测试此前缺失：ConfigHub 落地时只有 config_hub_test.go 里那三条
// 分发逻辑测试（newTestConfigHub 直接绕开 Run，喂假 signals channel），
// 没有任何测试真正调用过 h.Run 本身，close(h.ready) 挪到
// h.pub.Subscribe(ctx) 之前这类回归可以在 48/48 全绿的情况下悄悄发生。
//
// 用 fakeConfigSubscriber 卡住 Subscribe 不返回，先证明"订阅没完成时
// Ready() 不会关闭"，再放行证明"订阅一完成 Ready() 立刻关闭"。变异验证：
// 把 ConfigHub.Run 里 close(h.ready) 挪到 h.pub.Subscribe(ctx) 之前（字面
// 重现 RevokeHub 注释警告的那个 bug），第一段断言会立刻失败——
// fake.Subscribe 还卡在 <-f.proceed 上，Ready() 却已经关闭了。
func TestConfigHubReadyWaitsForSubscriptionToComplete(t *testing.T) {
	fake := &fakeConfigSubscriber{proceed: make(chan struct{})}
	h := newConfigHub()
	h.pub = fake

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- h.Run(ctx) }()

	select {
	case <-h.Ready():
		t.Fatal("fake.Subscribe 还没返回，Ready() 就已经关闭了——" +
			"顺序反了，ready 没有真正等订阅建立")
	case <-time.After(200 * time.Millisecond):
		// 符合预期：订阅还卡着，不该 ready。
	}

	close(fake.proceed) // 放行，模拟 Redis 的 SUBSCRIBE 确认刚刚返回。

	select {
	case <-h.Ready():
		// 符合预期。
	case <-time.After(2 * time.Second):
		t.Fatal("放行 fake.Subscribe 之后 Ready() 仍未关闭")
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("ctx 取消后 Run 返回了非 nil 错误: %v，期望 nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Run 没有返回")
	}
}

// failingConfigSubscriber 让 Subscribe 立即失败，模拟 Redis 不可达之类的
// 订阅失败场景。
type failingConfigSubscriber struct{ err error }

func (f failingConfigSubscriber) Subscribe(context.Context) (<-chan store.ConfigSignal, func(), error) {
	return nil, nil, f.err
}

// TestConfigHubReadyNeverClosesWhenSubscriptionFails 守住启动顺序要求的
// 另一半：订阅失败必须让启动失败，而不是带着一个永远收不到事件的中继
// 继续跑。与 server_test.go 的 TestReadyNeverClosesWhenSubscriptionFails
// （RevokeHub 版）同一逻辑：grpcapi.Server.Run 靠两条中继各自的返回值
// 判断启动是否失败，如果 Ready() 在订阅失败之后还是被误关闭，
// ServeWhenReady 会误以为配置中继健康、正常开始接受连接——配置推送从此
// 对本实例永久失效，且没有任何报错，是比启动直接失败更难排查的故障模式。
func TestConfigHubReadyNeverClosesWhenSubscriptionFails(t *testing.T) {
	wantErr := errors.New("boom：模拟 redis 不可达")
	h := newConfigHub()
	h.pub = failingConfigSubscriber{err: wantErr}

	err := h.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() 返回 %v，期望包装了 %v", err, wantErr)
	}

	select {
	case <-h.Ready():
		t.Fatal("订阅失败后 Ready() 仍被关闭了")
	default:
	}
}

// TestConfigHubRunReturnsNilWhenCanceledDuringSubscribe 补充覆盖（非审查
// 明确要求的两条之一）：ctx 在 h.pub.Subscribe 成功返回之前就被取消，
// Run 必须返回 nil，不能把这次取消当成订阅失败往上报。
//
// ConfigHub.Run 里这个 ctx.Err() 判定本身是相对 brief 给的示例代码新加的
// 一处修正（照抄 RevokeHub.Run 同款契约，见 task-7-report.md"与 brief 的
// 出入"第 2 条）：grpcapi.Server.Run 把两条中继的失败合并成一个返回值，
// 如果这里对 ctx 取消导致的失败原样透传，一次正常的 SIGTERM 只要撞上
// "订阅确认还没收到"这个窗口，就会被误报成致命错误。加了判定却没有测试
// 守护等于白加，补上。
//
// 与上面 TestConfigHubReadyWaitsForSubscriptionToComplete 的区别：那条
// 测试的 cancel() 发生在 fake.Subscribe 已经成功返回之后（先等到 Ready()
// 关闭），命中的是 h.run 分发循环里 <-ctx.Done() 分支；这条测试的
// cancel() 发生在 fake.Subscribe 还卡着的时候，命中的是 Run 里
// h.pub.Subscribe 失败分支——正是那个新加判定要处理的路径。
func TestConfigHubRunReturnsNilWhenCanceledDuringSubscribe(t *testing.T) {
	fake := &fakeConfigSubscriber{proceed: make(chan struct{})}
	h := newConfigHub()
	h.pub = fake

	ctx, cancel := context.WithCancel(context.Background())

	runDone := make(chan error, 1)
	go func() { runDone <- h.Run(ctx) }()

	select {
	case <-h.Ready():
		t.Fatal("fake.Subscribe 还没放行，Ready() 就已经关闭了")
	case <-time.After(200 * time.Millisecond):
	}

	cancel() // 在 fake.Subscribe 还卡着的时候取消 ctx，而不是先放行它。

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("ctx 在订阅确认之前被取消，Run() 返回了 %v，期望 nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Run 没有返回")
	}
}
