package fpsdk

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// TestValidateCachesAndAvoidsRefetch 确认缓存真的挡住了回源。
//
// 这是整个方案存在的理由：没有本地缓存，业务方每个请求都要跨服务调一次
// fp，fp 于是成为每条请求路径上的强依赖——fp 一挂，全部接入方立刻全挂。
func TestValidateCachesAndAvoidsRefetch(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 30_000}, nil
	})

	for i := 0; i < 10; i++ {
		if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
			t.Fatalf("第 %d 次校验: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("10 次校验回源 %d 次，期望 1 次", got)
	}
}

// TestConcurrentMissesCollapseToOneCall 守住 singleflight。
//
// 缓存过期的瞬间，同一个 token 的并发请求会同时 miss。没有合并的话，
// 一个热门用户的 N 个并发请求会同时打到 fp——正是缓存要防的惊群。
// 流量越大放大越严重，而功能完全正常，只有 fp 的负载图会异常。
func TestConcurrentMissesCollapseToOneCall(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		<-release // 卡住，保证并发请求都落在同一个飞行窗口里
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 30_000}, nil
	})

	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := env.auth.Validate(context.Background(), "tok")
			errs <- err
		}()
	}
	time.Sleep(100 * time.Millisecond) // 让 n 个请求都进入飞行状态
	close(release)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("并发校验失败: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d 个并发 miss 回源 %d 次，期望 1 次（singleflight 未生效）", n, got)
	}
}

// TestZeroCacheTTLForcesRefetch 钉住交接契约 1 的端到端表现。
func TestZeroCacheTTLForcesRefetch(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 0}, nil
	})

	for i := 0; i < 3; i++ {
		if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
			t.Fatalf("第 %d 次: %v", i, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("cache_ttl_ms=0 时 3 次校验只回源 %d 次——0 被当成了默认值", got)
	}
}

// TestRejectedTokenIsNotCached 守住一条内存性质。
//
// 缓存有容量上限。把失败结果也塞进去的话，攻击者用海量随机 token
// 就能把真实条目全部挤出 LRU，逼得每个正常请求都回源——
// 一次廉价的攻击就能让 fp 承受全量鉴权流量。
func TestRejectedTokenIsNotCached(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return nil, status.Error(codes.Unauthenticated, "token 无效或已过期")
	})

	for i := 0; i < 3; i++ {
		if _, err := env.auth.Validate(context.Background(), "bad"); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("第 %d 次返回 %v，期望 ErrUnauthorized", i, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("失败结果被缓存了（只回源 %d 次）——攻击者可用随机 token 挤空缓存", got)
	}
}

// TestRevokeEventDropsCachedToken 是撤销推送的全部意义。
func TestRevokeEventDropsCachedToken(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 300_000}, nil
	})

	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("首次: %v", err)
	}
	env.pushRevoke(t, &fpv1.RevokeEvent{Tokens: []string{"tok"}, UserIds: []string{"u1"}})

	// 推送是异步的，等缓存被清掉。
	env.waitUntil(t, func() bool {
		_, err := env.auth.Validate(context.Background(), "tok")
		return err == nil && calls.Load() == 2
	}, "撤销事件到达后缓存未被清除——被踢下线的用户会继续通行整整一个 cache_ttl")
}

// TestPurgeDropsEverything 守住 SDK 侧对 WatchPurge 的处理。
//
// 服务端在确知漏读了撤销事件、却不知道漏了哪些时发这条指令。SDK 必须清掉
// **全部**条目——只清某一部分（比如按 appID）没有任何依据，因为服务端
// 恰恰是因为"不知道涉及谁"才发的它。
//
// 断言写成"多个不同 token 都要重新回源"：只清一条的实现能让单 token
// 的测试通过，而线上表现是缓存里绝大多数条目仍停在不可信状态。
func TestPurgeDropsEverything(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 300_000}, nil
	})

	tokens := []string{"tok-a", "tok-b", "tok-c"}
	for _, tok := range tokens {
		if _, err := env.auth.Validate(context.Background(), tok); err != nil {
			t.Fatalf("预热 %s: %v", tok, err)
		}
	}
	if got := calls.Load(); got != int32(len(tokens)) {
		t.Fatalf("预热阶段回源 %d 次，期望 %d 次", got, len(tokens))
	}

	env.pushPurge(t, "redis 订阅重建")

	env.waitUntil(t, func() bool {
		for _, tok := range tokens {
			if _, err := env.auth.Validate(context.Background(), tok); err != nil {
				return false
			}
		}
		// 三个 token 全部重新回源过，总次数应为 2 × len(tokens)。
		return calls.Load() == int32(2*len(tokens))
	}, "收到 purge 后缓存没有被全部清空——部分条目仍停在已知不可信的状态")
}

// TestReconnectPurgesCacheWrittenBeforeDisconnect 守住重连时的缓存清空。
//
// 这条给定测试列表里没有：给定的 TestPurgeDropsEverything /
// TestRevokeEventDropsCachedToken 只覆盖服务端主动下发的 WatchPurge /
// RevokeEvent 两种显式指令，都不经过 watchOnce 里 Ready 分支那条隐式
// purge（"断开又重连"）。这条隐式路径是 client.go 三处改动之一，
// 却没有任何给定测试真正走到"先健康、再断开、再重连"这个序列，
// 补上。
//
// 顺带钉住我在实现阶段发现并修的一个问题：Ready 分支现在只在"曾经
// 收到过 ready"时才 purge（跳过首次连接），本测试通过 env.stop() 之后
// 在原地址重启，制造一次真正的"曾经健康"之后的重连，走的正是需要
// purge 的那一分支——用以确认收紧到"只在重连时 purge"之后，重连该做
// 的事没有被连带跳过。
func TestReconnectPurgesCacheWrittenBeforeDisconnect(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 300_000}, nil
	})
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("首次校验: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("首次校验回源 %d 次，期望 1 次", got)
	}

	env.stop()
	env.waitUntil(t, func() bool { return !env.client.StreamHealthy() },
		"服务端停止后应变为不健康")

	_, restart := startStub(t, env.addr, env.stub)
	t.Cleanup(restart)
	waitUntilTimeout(t, 20*time.Second, env.client.StreamHealthy,
		"服务端在原地址重启后，StreamHealthy() 在 20 秒内仍未重新变为 true")

	env.waitUntil(t, func() bool {
		_, err := env.auth.Validate(context.Background(), "tok")
		return err == nil && calls.Load() == 2
	}, "重连后缓存没有被清空——断连期间可能发生的撤销无法感知，"+
		"存量条目本该在重连时随 purge 一起失效")
}

// TestStreamDownTightensCacheWindow 守住降级策略。
//
// 推送断开意味着撤销的"加速"能力消失，只剩 TTL 兜底。SDK 主动把窗口
// 收紧到 DegradedCacheTTL，把安全性拉回来——这正是 gRPC 流状态可感知
// 才做得到、SSE 方案给不了的东西。
//
// 装配：DegradedCacheTTL 设得很短（30ms）。流健康时缓存一条 cache_ttl
// 长达 5 分钟的结果，然后用 env.stop() 停掉整个桩服务端——这与本文件
// 其余测试及 client_test.go 里"制造断线"的手法一致，唯一后果是
// ValidateToken 这条一元 RPC 也会跟着不可达。这正好用来验证目标性质：
// 若窗口真的被收紧到 30ms，第二次 Validate 在等待超过 30ms 后必然判定
// 缓存过期、尝试回源，而回源必然因服务端已停而失败，返回
// ErrUnavailable；若窗口没有被收紧，600 秒的原始 TTL 仍然新鲜，
// Validate 会直接吃缓存成功返回，不会有错误。二者可靠区分，
// 不依赖"数回源次数"这种在服务端已停时无法使用的手段。
func TestStreamDownTightensCacheWindow(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 300_000}, nil
	}, func(o *Options) {
		o.DegradedCacheTTL = 30 * time.Millisecond
		o.ValidateTimeout = 500 * time.Millisecond // 界定失败回源的最坏等待时间
	})
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("首次校验: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("首次校验回源 %d 次，期望 1 次", got)
	}

	env.stop() // 停掉桩服务端：Watch 流断开，ValidateToken 也随之不可达。
	env.waitUntil(t, func() bool { return !env.client.StreamHealthy() },
		"服务端停止后 StreamHealthy() 应变为 false")

	// 让缓存条目的存续时间越过 DegradedCacheTTL（30ms），但仍远小于
	// 原始 cache_ttl（300s）——只有收紧真的作用于这条存量条目时，
	// 下面的查询才会判定过期。
	time.Sleep(200 * time.Millisecond)

	_, err := env.auth.Validate(context.Background(), "tok")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("流断开且已超过 DegradedCacheTTL 后返回 %v，期望 ErrUnavailable——"+
			"本该判定缓存过期并尝试回源（服务端已停，回源必然失败），"+
			"而不是继续吃原始长窗口下本还新鲜的缓存", err)
	}
}

// TestUnavailableWithoutStaleFallbackIsRejected 确认默认不放行。
func TestUnavailableWithoutStaleFallbackIsRejected(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return nil, status.Error(codes.Unavailable, "fp 挂了")
	})
	if _, err := env.auth.Validate(context.Background(), "tok"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fp 不可达且无缓存时返回 %v，期望 ErrUnavailable", err)
	}
}

// TestStaleFallbackServesLastKnownIdentity 确认陈旧兜底的语义。
//
// 它给出的是"刚才验过的那个身份"，不是"放行一个未经验证的 token"。
// 完全没有缓存条目时必须照样拒绝——那种情况下根本没有身份可用。
//
// 装配：开 AllowStaleOnOutage 的 client，cache_ttl 设得很短（100ms）、
// MaxStaleness 给一个远大于测试耗时的值（10s）。先成功校验一次并缓存；
// 用一个原子开关让桩服务端此后一律返回 Unavailable（不停服务端、不断开
// Watch 流——这样 StreamHealthy() 全程为 true，maxTTL 恒为 0，观测到的
// 陈旧回退完全由 AllowStaleOnOutage/MaxStaleness 决定，不与
// TestStreamDownTightensCacheWindow 测的那条路径混在一起）；睡过
// cache_ttl 让条目进入陈旧窗口；断言第二次校验仍返回同一个 UserID 且
// Identity.Stale 为 true；最后用一个从未验证过的 token 断言返回
// ErrUnavailable——没有任何缓存条目时，开关必须不起作用。
func TestStaleFallbackServesLastKnownIdentity(t *testing.T) {
	var failing atomic.Bool
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		if failing.Load() {
			return nil, status.Error(codes.Unavailable, "fp 挂了")
		}
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 100}, nil
	}, func(o *Options) {
		o.AllowStaleOnOutage = true
		o.MaxStaleness = 10 * time.Second
	})
	// 等首条 ready 到达之后再暖缓存：首条 ready 之后不会再有第二条
	// （直到真的重连），这样下面写入的条目不会与连接建立过程本身的
	// 任何内部状态转换产生时序上的不确定性。
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("首次校验: %v", err)
	}

	failing.Store(true)
	time.Sleep(300 * time.Millisecond) // 越过 100ms 的 cache_ttl，进入陈旧窗口

	id, err := env.auth.Validate(context.Background(), "tok")
	if err != nil {
		t.Fatalf("fp 不可达但有陈旧缓存可用时返回了错误: %v", err)
	}
	if id.UserID != "u1" {
		t.Fatalf("陈旧兜底返回的 UserID 是 %q，期望 u1", id.UserID)
	}
	if !id.Stale {
		t.Fatal("陈旧兜底返回的 Identity 未标记 Stale——业务方无法据此拒绝高危操作")
	}

	// 换一个从未验证过的 token：不存在任何缓存条目，无论 AllowStaleOnOutage
	// 如何都必须拒绝——此刻既验证不了 token，也拿不出身份，"放行"没有
	// 任何可以赋予的含义。
	if _, err := env.auth.Validate(context.Background(), "never-seen"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("从未缓存过的 token 在 fp 不可达时返回 %v，期望 ErrUnavailable", err)
	}
}

// TestEmptyTokenIsRejectedWithoutRoundTrip 确认空 token 不打 fp。
func TestEmptyTokenIsRejectedWithoutRoundTrip(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		calls.Add(1)
		return nil, nil
	})
	if _, err := env.auth.Validate(context.Background(), ""); !errors.Is(err, ErrNoToken) {
		t.Fatalf("空 token 返回 %v，期望 ErrNoToken", err)
	}
	if calls.Load() != 0 {
		t.Fatal("空 token 触发了一次回源——未登录的匿名流量会全部打到 fp")
	}
}

// TestRotationIsSurfacedOnCacheHit 守住轮换交接不被缓存吃掉。
//
// fp 在过渡期内会对每一次带旧 token 的校验重复告知新 token。但 SDK 一旦
// 把首次结果缓存下来，后续请求就不再回源——如果缓存条目丢掉了 rotatedTo，
// 重复告知在 SDK 这一层被截断，客户端仍然只有一次机会。
// fp 侧修好的交接会被 SDK 重新打破。
func TestRotationIsSurfacedOnCacheHit(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return &fpv1.ValidateTokenResponse{
			UserId: "u1", SessionId: "s1", CacheTtlMs: 30_000,
			Rotated: true, NewToken: "new-tok",
		}, nil
	})

	for i := 0; i < 3; i++ {
		id, err := env.auth.Validate(context.Background(), "old-tok")
		if err != nil {
			t.Fatalf("第 %d 次: %v", i, err)
		}
		if id.RotatedTo != "new-tok" {
			t.Fatalf("第 %d 次命中缓存后 RotatedTo 为 %q——"+
				"轮换交接在 SDK 缓存层被截断了", i, id.RotatedTo)
		}
	}
}
