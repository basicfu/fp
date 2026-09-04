package registry

import (
	"context"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/testsupport"
	"github.com/redis/go-redis/v9"
)

func TestLivenessSeesPeersAndDropsDead(t *testing.T) {
	ctx := context.Background()
	rdb := testsupport.NewTestRedis(t)
	now := time.UnixMilli(1_000_000)
	clock := func() time.Time { return now }
	a := NewLiveness(rdb, "im-a", 3*time.Second, 10*time.Second)
	b := NewLiveness(rdb, "im-b", 3*time.Second, 10*time.Second)
	a.now, b.now = clock, clock

	if err := a.Beat(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !b.IsLive("im-a") || !b.IsLive("im-b") {
		t.Fatalf("b 应看到 a 与自己：%v", b.LiveNodes())
	}
	now = now.Add(11 * time.Second) // a 不再心跳
	_ = b.Refresh(ctx)
	if b.IsLive("im-a") {
		t.Fatal("超过 dead_after 未心跳的节点必须被判死")
	}
	if !b.IsLive("im-b") {
		t.Fatal("自己永远算活的")
	}
}

// waitSrvField 轮询 fp:im:srv:{app} 里 node 的字段，直到其存在性等于 want，
// 或超时失败。不用 time.Sleep 断言状态，用轮询加超时。
func waitSrvField(t *testing.T, rdb *redis.Client, app, node string, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		v, err := rdb.HGet(context.Background(), model.SrvKey(app), node).Result()
		exists := err == nil && v != ""
		if exists == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %s 在 %s 的 srv 字段变为 exists=%v 超时，当前 err=%v val=%q", node, app, want, err, v)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitRunPrimed 轮询直到 node 在 fp:im:node 存活表里出现，用来确定性地等
// Liveness.Run 启动时那一次性的 Beat/Refresh/writeServingTable"预热轮"跑完。
//
// 复审实测过这里不等待会导致约 1/8 的假阳性：go l.Run(runCtx) 只是发起了
// 一个 goroutine，调度器什么时候真正执行它的预热轮不确定。如果这一轮
// 恰好被调度到测试后面 gate 已经放行、甲乙双方都已经决定了最终状态之后
// 才执行，它自己的 writeServingTable 调用会独立地把 Redis 状态写成
// l.serving 当时的值——这次"额外的"写跟测试要验证的那次交错完全无关，
// 却可能凑巧把断言撞对，掩盖掉被测代码其实还是旧实现的事实。这里先等
// 预热轮确认跑完（此时 everSrv 还是空的，它内部的 writeServingTable 会
// 直接空跑），再开始整个"甲乙"交错，Run 在测试窗口内就不会再有背景写
// 干扰（心跳周期设成了 1 小时，ticker 分支不会在测试期间触发）。
func waitRunPrimed(t *testing.T, rdb *redis.Client, node string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		v, err := rdb.HGet(context.Background(), model.KeyNodes, node).Result()
		if err == nil && v != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 Run 的启动预热轮完成超时：%s 一直没有出现在 %s 里，err=%v", node, model.KeyNodes, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLivenessServerNodes 用真正跑起来的 Run 消费 SetServing 的信号，
// 而不是像旧版本那样假设 SetServing 会同步写 Redis。
func TestLivenessServerNodes(t *testing.T) {
	ctx := context.Background()
	rdb := testsupport.NewTestRedis(t)
	a := NewLiveness(rdb, "im-a", 50*time.Millisecond, 10*time.Second)
	b := NewLiveness(rdb, "im-b", 50*time.Millisecond, 10*time.Second)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.Run(runCtx)

	if err := a.SetServing(ctx, "a1", true); err != nil {
		t.Fatal(err)
	}
	waitSrvField(t, rdb, "a1", "im-a", true)
	b.TrackApp("a1")
	_ = b.Refresh(ctx)
	if got := b.ServerNodes("a1"); len(got) != 1 || got[0] != "im-a" {
		t.Fatalf("b 应看到 im-a 持有 a1 的 server 流，实际 %v", got)
	}

	if err := a.SetServing(ctx, "a1", false); err != nil {
		t.Fatal(err)
	}
	waitSrvField(t, rdb, "a1", "im-a", false)
	_ = b.Refresh(ctx)
	if got := b.ServerNodes("a1"); len(got) != 0 {
		t.Fatalf("SetServing(false) 落地后应看不到 im-a，实际 %v", got)
	}
}

// TestSetServingReturnsImmediately 断言 SetServing 是纯内存操作：哪怕没有任何
// 消费者（没启动 Run）在消费信号 channel，调用也必须立即返回，不能卡在
// Redis 网络往返上。用一个已经取消的 ctx 调用：如果 SetServing 内部真的
// 尝试用这个 ctx 发 Redis 命令，会立即因 ctx 取消而返回错误；但规格要求
// "立即返回 nil"，验证这一点顺带验证了它确实没有走网络。
func TestSetServingReturnsImmediately(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	l := NewLiveness(rdb, "im-a", time.Hour, 10*time.Second) // 心跳周期设得很长：这条测试不需要 Run 消费信号
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- l.SetServing(cancelled, "a1", true) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetServing 应立即返回 nil，即使 ctx 已取消，实际 %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SetServing 一秒内没有返回：怀疑仍在尝试真正的 Redis 网络往返")
	}
}

// TestSetServingRapidSuccessionEndsAtRealState 快速连续多次 SetServing 之后，
// 最终 Redis 上的状态必须等于最后一次调用代表的真实状态，而不是中间某次。
// 信号 channel 容量为 1 会把这些通知合并成一次，这里验证合并不会把状态写错。
func TestSetServingRapidSuccessionEndsAtRealState(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	l := NewLiveness(rdb, "im-a", 20*time.Millisecond, 10*time.Second)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(runCtx)

	ctx := context.Background()
	// 快速翻转很多次，最后一次落在 true。
	for i := 0; i < 20; i++ {
		if err := l.SetServing(ctx, "a1", i%2 == 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.SetServing(ctx, "a1", true); err != nil {
		t.Fatal(err)
	}
	waitSrvField(t, rdb, "a1", "im-a", true)
}

// TestSetServingOutOfOrderRegression 是问题 2（serving 翻转乱序）的回归测试。
//
// 构造的时序对应 brief 里的叙述：甲摘掉最后一条流（决定"没有"），随后乙接进
// 新流（决定"有"）——乙的决定在时间上晚于甲，代表当下真实状态应该是"有"。
// 旧实现把"判定"和"写 Redis"分离在锁外各自独立发起：如果甲的写因为网络
// 延迟比乙的写更晚落地，就会用一个更旧的结论覆盖更新的结论，终态变成
// "没有"——节点持有活流却对外宣告没有。
//
// 用 delayHook 在真正执行 Redis 命令前卡住甲的写，直到乙的写确认完成后才
// 放行，这样不依赖真实网络延迟的运气，确定性地复现"甲的写比乙的写晚落地"
// 这个交错。放行之后：
//   - 若被测代码是本次改动前的旧实现，SetServing 会在锁外直接同步发起这条
//     被卡住的 HDel，放行后它真正执行并把状态覆盖回"没有"，断言失败。
//   - 若被测代码是新实现，SetServing 本身根本不用这个 ctx 发任何 Redis
//     命令（它只改内存、发信号），delayHook 从头到尾不会被这次调用触发，
//     真正的写由 Run 里的消费协程稍后统一发出，读到的是调用这一刻的
//     最终真实状态（true），断言通过。
func TestSetServingOutOfOrderRegression(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	started := make(chan struct{}, 1)
	rdb.AddHook(delayHook{started: started})

	// 心跳周期设得很长，避免 Run 的周期性无条件写在测试窗口内先一步把状态
	// 写对，从而掩盖掉这里真正要测的"由 SetServing 信号触发的写"这条路径。
	l := NewLiveness(rdb, "im-a", time.Hour, 10*time.Second)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(runCtx)

	// 先确定性地等 Run 的启动预热轮跑完，再开始下面的"甲乙"交错——见
	// waitRunPrimed 的注释，这一步是消除约 1/8 假阳性的关键。
	waitRunPrimed(t, rdb, "im-a")

	// 起点：已有一条流，状态是"有"，且已经落地。
	if err := l.SetServing(context.Background(), "a1", true); err != nil {
		t.Fatal(err)
	}
	waitSrvField(t, rdb, "a1", "im-a", true)

	gate := make(chan struct{})
	doneA := make(chan struct{})
	// 甲：摘最后一条流，决定关闭。
	go func() {
		delayCtx := context.WithValue(context.Background(), delayGateKey{}, gate)
		_ = l.SetServing(delayCtx, "a1", false)
		close(doneA)
	}()

	// 等甲"已经开始尝试写 Redis 并卡在 gate 上"的信号；新实现下这个信号永远
	// 不会来（SetServing 根本不碰 Redis），用一个宽裕的超时兜底，确保两种
	// 实现下这里都不会永久挂死。
	select {
	case <-started:
	case <-time.After(300 * time.Millisecond):
	}

	// 乙：接入新流，决定打开。这是决策链上更晚的一环，必须先落地。
	if err := l.SetServing(context.Background(), "a1", true); err != nil {
		t.Fatal(err)
	}
	waitSrvField(t, rdb, "a1", "im-a", true)

	// 放行甲被卡住的写（如果确实卡住了）。
	close(gate)
	select {
	case <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("甲的 SetServing 在放行后迟迟不返回")
	}

	// 断言：无论内部实现让谁先谁后真正落地，最终状态必须等于更晚的决定
	// （乙，"有"）。这条断言必须在改动被回退后失败。
	waitSrvField(t, rdb, "a1", "im-a", true)
}

// delayGateKey 是 delayHook 识别"这条命令需要被卡住"的 context 标记。
type delayGateKey struct{}

// delayHook 让打了 delayGateKey 标记的单条 Redis 命令，在真正执行前阻塞到
// gate 被关闭为止，用来确定性地控制"谁的写先真正落地"，不依赖真实网络延迟
// 的运气。只拦截单条命令（ProcessHook）：旧实现的 SetServing 直接调用
// HSet/HDel 走的就是这条路径；新实现的写全部走 Pipelined（ProcessPipelineHook），
// 不会被这个 hook 影响。
type delayHook struct {
	started chan struct{}
}

func (delayHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h delayHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if gate, ok := ctx.Value(delayGateKey{}).(chan struct{}); ok {
			select {
			case h.started <- struct{}{}:
			default:
			}
			<-gate
		}
		return next(ctx, cmd)
	}
}

func (delayHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error { return next(ctx, cmds) }
}

// TestHeartbeatBackstopHDelsStaleEntry 覆盖 B2 的心跳兜底，专门验证 HDEL
// 方向。
//
// 复审实测过一个假阳性：如果把心跳分支里的 writeServingTable(ctx) 换成
// 空语句，整个 registry 包的测试仍然全绿——因为 Beat() 本身就会对
// l.serving 集合里的每个 app 做 HSET，只要断言的是"条目被 HSET 回来"，
// Beat() 单独就能让测试通过，根本测不到 writeServingTable 是否被调用。
// writeServingTable 唯一无可替代的地方是 HDEL 方向：Beat() 从来不会
// HDEL 任何条目。这里构造的场景是：本节点已经决定不再服务 a1（serving
// 里没有它），但 Redis 里因为某种原因（另一个进程误写、失败重试后的
// 孤儿写入……）残留了一条本节点的条目。只有心跳分支里那次无条件的
// writeServingTable 会把它 HDEL 掉；Beat() 对着一个不在 l.serving 里的
// app 什么也不做。
func TestHeartbeatBackstopHDelsStaleEntry(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	l := NewLiveness(rdb, "im-a", 30*time.Millisecond, 10*time.Second)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(runCtx)

	// 先服务再撤销：让 a1 进入 everSrv 追踪范围，且最终真实状态落地为
	// "没有"。
	if err := l.SetServing(context.Background(), "a1", true); err != nil {
		t.Fatal(err)
	}
	waitSrvField(t, rdb, "a1", "im-a", true)
	if err := l.SetServing(context.Background(), "a1", false); err != nil {
		t.Fatal(err)
	}
	waitSrvField(t, rdb, "a1", "im-a", false)

	// 从外部把字段重新写回去，模拟一条不该存在的残留条目。l.serving["a1"]
	// 仍然是 false，没有任何本地事件会再次触发 SetServing 的信号——能纠正
	// 这条残留的，只有心跳分支的无条件重写。
	if err := rdb.HSet(context.Background(), model.SrvKey("a1"), "im-a", time.Now().UnixMilli()).Err(); err != nil {
		t.Fatal(err)
	}
	waitSrvField(t, rdb, "a1", "im-a", false)
}

// TestDropLocalHidesNodeUntilRefresh 覆盖 B3：DropLocal 之后 ServerNodes 与
// IsLive 不再包含该节点；下一次 Refresh 若该节点仍在心跳，又会自动回到快照。
func TestDropLocalHidesNodeUntilRefresh(t *testing.T) {
	ctx := context.Background()
	rdb := testsupport.NewTestRedis(t)
	a := NewLiveness(rdb, "im-a", time.Hour, 10*time.Second)
	b := NewLiveness(rdb, "im-b", time.Hour, 10*time.Second)

	if err := a.SetServing(ctx, "a1", true); err != nil {
		t.Fatal(err)
	}
	// 这条测试不跑 Run，直接调用内部写方法把当下状态落地，避免依赖消费协程
	// 的调度时机。
	a.writeServingTable(ctx)
	if err := a.Beat(ctx); err != nil {
		t.Fatal(err)
	}
	b.TrackApp("a1")
	if err := b.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !b.IsLive("im-a") {
		t.Fatal("Refresh 之后应看到 im-a")
	}
	if got := b.ServerNodes("a1"); len(got) != 1 || got[0] != "im-a" {
		t.Fatalf("Refresh 之后应看到 im-a 持有 a1，实际 %v", got)
	}

	b.DropLocal("im-a")
	if b.IsLive("im-a") {
		t.Fatal("DropLocal 之后不应再认为 im-a 活着")
	}
	if got := b.ServerNodes("a1"); len(got) != 0 {
		t.Fatalf("DropLocal 之后 ServerNodes 不应再包含 im-a，实际 %v", got)
	}

	if err := b.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !b.IsLive("im-a") {
		t.Fatal("下一次 Refresh 之后 im-a 仍在心跳，应该自动回到快照")
	}
	if got := b.ServerNodes("a1"); len(got) != 1 || got[0] != "im-a" {
		t.Fatalf("下一次 Refresh 之后应重新看到 im-a 持有 a1，实际 %v", got)
	}
}
