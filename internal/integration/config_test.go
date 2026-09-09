// config_test.go 是配置中心模块的端到端穿透测试（Task 15）。
//
// 对应第三阶段最要命的那个教训：服务端有信息、传输层丢了，而所有测试
// 照绿——因为没有一条测试跨越边界。下面每条都必须穿到 **SDK 出口**
// （Binding[T].Load() / TypeBinding.Load() 的返回值），只断言服务端
// 内部状态是不够的。
package integration_test

import (
	"sync/atomic"
	"testing"
	"time"

	fpsdk "github.com/basicfu/fp/sdk"
)

// TestConfigChangePropagatesToSDK 覆盖场景 1：改值 → 推送 → SDK 的 Load()
// 真的变了。
//
// 走 e.saveConfig 直接调 service.ConfigService.Save（而不是绕过 service
// 直接操作 Postgres/Redis），断言的是 service→Redis→gRPC→SDK 这一整条
// 链路——phase2Env 没有 HTTP 层，Task 8 的 httpapi 测试已经覆盖了
// HTTP→service 那一段，见 phase2Services.configs 的注释。
func TestConfigChangePropagatesToSDK(t *testing.T) {
	e := newPhase2Env(t)
	waitUntil(t, e.sdk.StreamHealthy, "建流后应变为健康")

	// 这里的 push 用 false：Bind 首次加载走的是独立的同步 GetConfig
	// 调用，不依赖任何推送，push 与否对它没有分别；用 true 只会多留下一条
	// 没人处理的 ConfigChanged 信号，白白让后面的时序更复杂
	// （TestTypeChangeBothPaths 就因为同样的种子写法偶发触发过一次
	// 不相关的 OnError，见该测试里的详细分析）。
	e.saveConfig(t, "DEFAULT", "fee_rate: 0.02\n", false)

	type shopCfg struct{ FeeRate float64 }
	cfg, err := fpsdk.Bind[shopCfg](e.sdk)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}
	if cfg.Load().FeeRate != 0.02 {
		t.Fatalf("初始值 = %v，期望 0.02", cfg.Load().FeeRate)
	}

	changed := make(chan float64, 1)
	cfg.OnChange(func(_, n *shopCfg) { changed <- n.FeeRate })

	e.saveConfig(t, "DEFAULT", "fee_rate: 0.05\n", true)

	select {
	case v := <-changed:
		if v != 0.05 {
			t.Fatalf("推送后的值 = %v，期望 0.05", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待配置推送超时——变更没能穿到 SDK 出口")
	}
	if cfg.Load().FeeRate != 0.05 {
		t.Fatalf("Load() = %v，期望 0.05", cfg.Load().FeeRate)
	}
}

// TestSaveWithoutPushIsInvisibleUntilRebind 覆盖场景 2：「仅落库」的两条腿——
// 不推送（运行中的绑定不变）且新起一次 Bind 能拿到新值。
//
// 【辨别力】必须同时断言这两件事：只断言"没推送"的话，一个根本没存的
// 实现也会绿；只断言"新 Bind 拿到了"的话，一个照样推送的实现也会绿。
//
// 两处时序注意点——本测试压测（-count 多次跑）时都实际暴露过偶发失败，
// 不是纸上谈兵：
//
//  1. newPhase2Env 之后必须先 waitUntil(StreamHealthy) 再往下走。SDK 连上
//     后收到的**第一条** ready 会无条件触发一次配置重拉（client.go 的
//     watchOnce 对 ready 的处理，注释里写明"首次连接也走这条……无害"）。
//     "无害"的前提是这次重拉在测试改完值**之前**完成；建流是异步的，
//     不等它稳定就往下走的话，这次首连重拉有不小概率被延迟到下面
//     「仅落库」那次保存**之后**才真正执行——它读的永远是"当前值"，
//     那时当前值已经是保存之后的新值，会把这条本该测"没推送就不该
//     触发"的断言变成误报。
//  2. 种子保存（下面这一条）必须用 push=false。它发生在任何绑定注册
//     之前，广播了也没人听，用 true 唯一的效果是给 cfgReload 那个缓冲
//     为 1 的信号 channel 多留一次"迟早要被消费"的信号，与上一条是同一
//     类风险：这条信号真正被消费的时刻不受这里控制，一样可能晚于下面
//     「仅落库」的保存。
func TestSaveWithoutPushIsInvisibleUntilRebind(t *testing.T) {
	e := newPhase2Env(t)
	waitUntil(t, e.sdk.StreamHealthy, "建流后应变为健康")

	e.saveConfig(t, "DEFAULT", "n: 1\n", false)

	type cfgT struct{ N int }
	cfg, err := fpsdk.Bind[cfgT](e.sdk)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}
	fired := make(chan struct{}, 1)
	cfg.OnChange(func(_, _ *cfgT) { fired <- struct{}{} })

	e.saveConfig(t, "DEFAULT", "n: 2\n", false)

	select {
	case <-fired:
		t.Fatal("选了「仅落库」，运行中的绑定不该收到变更")
	case <-time.After(time.Second):
	}
	if got := cfg.Load().N; got != 1 {
		t.Fatalf("运行中的绑定 N = %d，期望仍是 1", got)
	}

	// 另一条腿：新起一次 Bind（模拟 pod 重启）必须拿到新值。
	fresh, err := fpsdk.Bind[cfgT](e.dial(t))
	if err != nil {
		t.Fatalf("重新 Bind 失败: %v", err)
	}
	if got := fresh.Load().N; got != 2 {
		t.Fatalf("新绑定 N = %d，期望 2——值必须真的落库了", got)
	}
}

// TestTypeChangeBothPaths 覆盖场景 3：改类型（int → object）的两条路，
// 穿到 SDK 出口（设计文档测试策略第 5 条）。
//
// 场景：v2 代码把某项从 int 改成了 object，发版前得先把控制台上的值改成
// JSON，而这时 v1 还在跑、要的还是那个 int。
func TestTypeChangeBothPaths(t *testing.T) {
	// Retries 是特意添加的第二个、类型不变的字段，键为 "retries"。仅有
	// Timeout 一个字段不足以验证"值还是旧的"这条辨别力：int 解析失败时
	// encoding/json 对标量目标是全有全无——不会写入半个 int——所以哪怕
	// 拿一个"逐字段解析、遇错就地丢一份已经改了一半的副本"的错误实现来
	// 跑，唯一那个字段 Timeout 本身也测不出差别（该实现里 Timeout 恰好
	// 还是没被碰过）。加上一个不出错的 Retries 字段、且它按字母序排在
	// Timeout 之前会被先处理并"成功"写入新值，这样的错误实现就会表现
	// 成 Retries 已经跳到新值、只有 Timeout 保持旧值的半新半旧状态；
	// 只有对整份快照做原子替换（本包 applySnapshot 的真实做法）才会让
	// Retries 也停在旧值上。下面按这个思路真的做过一次变异验证（临时把
	// applyValues 出错时改为返回已构建的部分副本、并让 applySnapshot 出错
	// 后不再提前 return），Retries 断言按预期变红，验证完已还原，不留在
	// 产品代码里。
	type v1Cfg struct {
		Timeout int
		Retries int
	}
	// Timeout 字段带 fp:"json"：不加这个 tag 的话，SDK 会把这个未打标签的
	// 嵌套 struct 当成"分组"展开成 timeout.ms 这个二级 key，读的就不是
	// 控制台上那个名叫 timeout、值为 {"ms":5000} 的单一配置项了，断言会
	// 因为找不到 timeout.ms 这个 key 而失败在"缺失"上，而不是真的验证到
	// 类型迁移这条路径。
	type v2Cfg struct {
		Timeout struct{ Ms int } `fp:"json"`
	}

	// —— 路径一：选「仅落库」，v1 完全不受影响。
	e := newPhase2Env(t)
	waitUntil(t, e.sdk.StreamHealthy, "建流后应变为健康")
	// 种子保存用 push=false：见 TestSaveWithoutPushIsInvisibleUntilRebind
	// 顶部注释里对种子信号时序问题的完整分析——这条测试早前压测下就是
	// 因为这个问题偶发把 errs 从期望的 0 计成过 1。
	e.saveConfig(t, "DEFAULT", "timeout: 3000\nretries: 1\n", false)

	cfg, err := fpsdk.Bind[v1Cfg](e.sdk)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}
	var errs int32
	cfg.OnError(func(error) { atomic.AddInt32(&errs, 1) })

	// retries 原样带过去（不变）：这次保存只关心 timeout 的类型迁移，
	// value 是全量替换（见 service.ConfigService.Save 的文档），漏写
	// retries 会让它在新版本里直接消失，变成一个不相关的"缺失"信号。
	e.saveConfig(t, "DEFAULT", "timeout:\n  ms: 5000\nretries: 1\n", false)
	time.Sleep(time.Second)

	if got := cfg.Load().Timeout; got != 3000 {
		t.Fatalf("「仅落库」路径：v1 的 Timeout = %d，期望仍是 3000", got)
	}
	if n := atomic.LoadInt32(&errs); n != 0 {
		t.Fatalf("「仅落库」路径：OnError 被调用 %d 次，期望 0 次", n)
	}

	// 新版本代码依然能拿到新值：值已经落库，push 只影响是否广播、不影响
	// 持久化。必须在这里、创建 e2 之前完成这个断言——newPhase2Env 会
	// TRUNCATE 共享的 Postgres 表（testsupport.NewTestDB 的约定：整个测试
	// 二进制共用一个连接池，每次调用都清空业务表），下面创建 e2 那一刻
	// e 的应用行就没了，e.dial 会因为"应用不存在"直接失败。
	rollbackClient := e.dial(t)
	freshFromRollback, err := fpsdk.Bind[v2Cfg](rollbackClient)
	if err != nil {
		t.Fatalf("路径一（仅落库）v2 Bind 失败: %v", err)
	}
	if got := freshFromRollback.Load().Timeout.Ms; got != 5000 {
		t.Fatalf("路径一（仅落库）v2 拿到 %d，期望 5000", got)
	}
	// 断言完立刻关掉：这个客户端只为这一次断言而生，不关的话它的后台
	// watch/reload goroutine 会一直跑到整个测试函数结束才被 t.Cleanup
	// 收尾，期间若恰好撞上下面 e2 的 newPhase2Env TRUNCATE 共享 Postgres
	// 表，会打一条无害但容易让人误会成真实缺陷的"配置项缺失"错误日志。
	// Close 是幂等的，t.Cleanup 稍后重复调用不会出问题。
	_ = rollbackClient.Close()

	// —— 路径二：误选「立即推送」，v1 保持旧值 + OnError，且**不崩**。
	// 用一个全新的 phase2Env（而不是 e.spawnPeer，那个会复用同一个应用/
	// 同一份 config 历史）：路径二要的是一个"当前值仍是 int 3000"的干净
	// 起点，而 e 上的 timeout 经路径一改造后，落库的当前值已经是 object。
	e2 := newPhase2Env(t)
	waitUntil(t, e2.sdk.StreamHealthy, "建流后应变为健康")
	// 同上，种子保存不推送——这里即便种子信号迟到也不会造成误判（迟到的
	// 重载和下面第二次 push=true 的重载读到的都是同一份 object 值，两者
	// 都会正确地报错并保持旧值），但保持两条路径的种子写法一致更清楚。
	e2.saveConfig(t, "DEFAULT", "timeout: 3000\nretries: 1\n", false)

	cfg2, err := fpsdk.Bind[v1Cfg](e2.sdk)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}
	raised := make(chan struct{}, 1)
	cfg2.OnError(func(error) {
		select {
		case raised <- struct{}{}:
		default:
		}
	})

	// retries 在同一次保存里也从 1 改成 2：字母序上 "retries" 排在
	// "timeout" 之前，一个逐字段写入、遇到 timeout 解析失败才返回的错误
	// 实现，会先把 retries 的新值 2 提交下去——这正是下面第二条断言要
	// 抓的东西。
	e2.saveConfig(t, "DEFAULT", "timeout:\n  ms: 5000\nretries: 2\n", true)

	select {
	case <-raised:
	case <-time.After(5 * time.Second):
		t.Fatal("「立即推送」路径：解析失败必须触发 OnError")
	}
	// <-raised 只保证 OnError 已经被调用过（fpsdk.Binding[T].applySnapshot
	// 里 raise(err) 与本测试的 channel 发送之间有 happens-before 关系）,
	// 不保证同一个重载 goroutine 后续几行（missing 检查、DeepEqual 比较、
	// 要不要真的换指针）也跑完了。真实实现在错误分支里 raise 之后立刻
	// return，这段真空期是空的；但如果拿一个"raise 之后不提前 return、
	// 继续往下换指针"的坏实现来跑，紧接着立刻读 Load() 会有实测约六成
	// 概率抢在它完成换指针之前读到——抓获率因此从确定性的 100% 掉到
	// 约 40%（详见任务报告"审查回合"一节）。轮询到快照连续 150ms 不再
	// 变化，才真正等完这段真空期，而不是在一个偶然的时间点抢答。
	final := waitStable(t, 2*time.Second, 150*time.Millisecond,
		func() v1Cfg { return *cfg2.Load() })
	// 【辨别力】必须断言"值还是旧的"而不只是"报了错"——一个逐字段写入、
	// 遇错才返回的实现同样会报错，却已经把快照改坏了。只看 Timeout 不够
	// （标量解析失败时 encoding/json 本就不会碰这个字段，任何实现都测不
	// 出差别，见 v1Cfg 定义处的注释）：Retries 才是真正有辨别力的一半——
	// 它按字母序排在 Timeout 之前、这次保存里也真的变了，一个非原子的
	// 实现会先把它提交成新值 2。
	if final.Timeout != 3000 {
		t.Fatalf("「立即推送」路径：Timeout = %d，解析失败时必须保持旧值 3000", final.Timeout)
	}
	if final.Retries != 1 {
		t.Fatalf("「立即推送」路径：Retries = %d，解析失败时必须保持旧值 1"+
			"（哪怕 Retries 自己解析没问题）——整份快照必须原子替换", final.Retries)
	}

	// 路径二同样要能让新版本代码拿到新值（路径一的这条断言已经在创建 e2
	// 之前做过，见上面的 freshFromRollback）。
	freshFromPush, err := fpsdk.Bind[v2Cfg](e2.dial(t))
	if err != nil {
		t.Fatalf("路径二（立即推送）v2 Bind 失败: %v", err)
	}
	if got := freshFromPush.Load().Timeout.Ms; got != 5000 {
		t.Fatalf("路径二（立即推送）v2 拿到 %d，期望 5000", got)
	}
}

// TestReadyRepullsConfigMissedWhileDisconnected 覆盖场景 4：断线期间的
// 变更，靠"收到 ready 就重拉"补上。
//
// 【辨别力】变更必须发生在断线**期间**：断线前改（重连前就拉到了）
// 或重连后改（有 ConfigChanged 推送）都测不到这个缺口。
func TestReadyRepullsConfigMissedWhileDisconnected(t *testing.T) {
	e := newPhase2Env(t)
	waitUntil(t, e.sdk.StreamHealthy, "建流后应变为健康")
	// 种子保存不推送，理由同 TestSaveWithoutPushIsInvisibleUntilRebind：
	// Bind 首次加载不依赖推送，用 true 只会留下一条无谓的 ConfigChanged。
	e.saveConfig(t, "DEFAULT", "n: 1\n", false)

	type cfgT struct{ N int }
	cfg, err := fpsdk.Bind[cfgT](e.sdk)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}

	// 掐断 gRPC 服务端，让 SDK 的 Watch 流断开。
	e.stopFp(t)
	// 断线**期间**改值：这次的 ConfigChanged 谁也收不到——发布时压根没有
	// 任何 fp 实例订阅着这个频道。
	e.saveConfig(t, "DEFAULT", "n: 42\n", true)
	// 重新起服务端（监听同一端口），SDK 会退避重连并收到 ready。
	e.restartFp(t)

	// 退避最长 30 秒，这里给足重试窗口，但不到让整条测试拖很久的地步。
	waitUntilTimeout(t, 15*time.Second, func() bool { return cfg.Load().N == 42 },
		"重连后收到 ready 应触发一次配置重拉、补上断线期间的变更，但等待超时 N 仍不是 42")
}

// TestPartitionIsolationAtSDKBoundary 覆盖场景 5：分区隔离穿到 SDK 出口。
//
// 【辨别力】两个分区的值必须不同，否则"分区对了"和"压根没分区"同结果。
func TestPartitionIsolationAtSDKBoundary(t *testing.T) {
	e := newPhase2Env(t)
	e.saveConfig(t, "DEFAULT", "site.title: 后端\n", false)
	e.saveConfig(t, "WEB", "site.title: 前端\n", false)

	// siteT 必须先于 cfgT 声明：函数体内的局部类型标识符从声明处才进入
	// 作用域，cfgT 若先声明会在类型检查阶段就因为引用了尚未声明的 siteT
	// 编译失败（这与包级类型声明可以互相前向引用不同）。
	type siteT struct{ Title string }
	type cfgT struct{ Site siteT }

	cfg, err := fpsdk.Bind[cfgT](e.sdk)
	if err != nil {
		t.Fatalf("Bind 失败: %v", err)
	}
	if got := cfg.Load().Site.Title; got != "后端" {
		t.Fatalf("Bind 拿到 %q，期望 后端", got)
	}

	web, err := fpsdk.BindType(e.sdk, "WEB")
	if err != nil {
		t.Fatalf("BindType 失败: %v", err)
	}
	if got := web.Load()["site.title"]; got != "前端" {
		t.Fatalf("BindType 拿到 %v，期望 前端", got)
	}
}
