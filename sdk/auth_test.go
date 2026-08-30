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
	// 等首条 ready 到达之后再暖缓存：watchOnce 收到首条 ready 时会
	// 无条件 purge 一次缓存（首次连接也走这条路径），而 New() 在
	// watch goroutine 收到这条 ready 之前就已经返回给调用方——不等
	// StreamHealthy() 的话，这次预热可能落在 purge 之前，被那次
	// purge 冲掉，让下面的回源次数断言变得不确定。
	// Go 的内存模型保证：观察到 StreamHealthy()==true 时，那次 purge
	// 必然已经完成（同一 goroutine 内 purge 先于 streamUp.Store(true)）。
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

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
	// 等首条 ready 到达之后再缓存：理由同 TestPurgeDropsEverything——
	// 迟到的首条 ready 会无条件 purge 掉刚写入的条目，让下面的等值
	// 断言（calls==2）在某次迟到的 purge 之后被后续的额外回源越过、
	// 永远等不到。
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

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
	// 等首条 ready 到达之后再预热三个 token：watchOnce 的 Ready 分支对
	// 每次 ready（含首次连接）都无条件 purge，不等 StreamHealthy() 的话，
	// 这次预热可能落在首条 ready 之后才完成——那次迟到的 purge 会把刚
	// 预热好的三个条目全部冲掉，之后 calls 会被迫再多涨一轮，
	// 永远追不上下面这个等值断言（复现过，压力跑 25 次里出现 2 次）。
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

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
// watchOnce 的 Ready 分支对每一次 ready（含首次连接）都无条件 purge，
// 本测试通过 env.stop() 之后在原地址重启，制造一次真正的"曾经健康"
// 之后的重连，确认这条无条件 purge 在重连场景下确实按预期清空了
// 断开前写入的存量条目——而不是只在理论上"应该"这样做。
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

// TestNotFoundIsUnauthorizedNotUnavailable 守住"NotFound 是确定答案，不是
// 不可达"。
//
// GetActiveByAppID 找不到应用时，服务端回 codes.NotFound——多半是接入方
// 的 appId 配错了，或者应用被管理端删除了，fp 已经给出了明确的判定，
// 不是"够不着 fp"。此前的实现只把 Unauthenticated/PermissionDenied 判成
// 鉴权失败，NotFound 会落进"fp 不可达"的陈旧兜底分支。
func TestNotFoundIsUnauthorizedNotUnavailable(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return nil, status.Error(codes.NotFound, "application not found")
	})
	if _, err := env.auth.Validate(context.Background(), "tok"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("NotFound 时返回 %v，期望 ErrUnauthorized", err)
	}
}

// TestNotFoundDoesNotFallBackToStaleEvenWithCache 是上一条的关键补充。
//
// 只测"NotFound 返回 ErrUnauthorized"是不够的：一个"NotFound 仍然走陈旧
// 兜底分支、但这次测试恰好没有缓存条目可用，所以兜底分支自己也拒绝了"
// 的实现，一样能让上一条测试通过。这条测试专门堵死这个后门——开
// AllowStaleOnOutage 且缓存里确有一条陈旧条目可用时，NotFound 也必须
// 立刻拒绝，而不是被当作"fp 不可达"继续放行 MaxStaleness。
//
// 装配与 TestStaleFallbackServesLastKnownIdentity 相同：cache_ttl 设得很短
// （100ms），先成功校验一次并缓存，之后让桩服务端改口返回 NotFound，
// 睡过 cache_ttl 进入陈旧窗口，再次校验必须拿到 ErrUnauthorized。
func TestNotFoundDoesNotFallBackToStaleEvenWithCache(t *testing.T) {
	var notFound atomic.Bool
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		if notFound.Load() {
			return nil, status.Error(codes.NotFound, "application not found")
		}
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 100}, nil
	}, func(o *Options) {
		o.AllowStaleOnOutage = true
		o.MaxStaleness = 10 * time.Second
	})
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("首次校验: %v", err)
	}

	notFound.Store(true)
	time.Sleep(300 * time.Millisecond) // 越过 100ms 的 cache_ttl，进入陈旧窗口

	if _, err := env.auth.Validate(context.Background(), "tok"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("NotFound 且有陈旧缓存可用时返回了 %v，期望 ErrUnauthorized——"+
			"NotFound 不该走陈旧兜底，哪怕缓存里确有条目", err)
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

// newStubEnvFull 和 newStubEnv 一样起一个连着桩服务端的 Client，但接受
// 一个已经配好全部回调（含 login/logout）的 *stubServer，而不是只接受
// validate 一个回调。
//
// 不能用"newStubEnv 建完之后再给 env.stub.login 赋值"这种写法：newStubEnv
// 返回前 grpcServer.Serve(lis) 已经在跑，处理请求的 goroutine 随时可能
// 已经在飞。这之后再对 *stubServer 的字段普通赋值，赋值所在的 goroutine
// （测试主 goroutine）与读取字段的 goroutine（grpc-go 的请求处理循环）
// 之间没有任何 happens-before 关系，是一次真正的数据竞争——即使网络
// 往返在实践中几乎总能让它"凑巧"不出错。validate 字段之所以安全，是
// 因为它在 startStub 启动 Serve 的 go 语句**之前**就已经写进了完整的
// struct 字面量，受"go 语句先于新 goroutine 内代码执行"这条 Go 内存
// 模型保证的保护。本函数把 login/logout 也纳入这条安全路径：调用方在
// 传进来之前就把整个 *stubServer 配好，本函数才调用 startStub。
func newStubEnvFull(t *testing.T, stub *stubServer, opt ...func(*Options)) *stubEnv {
	t.Helper()
	if stub.watchReady == nil {
		stub.watchReady = make(chan struct{}, 1)
	}
	if stub.events == nil {
		stub.events = make(chan *fpv1.WatchResponse, 16)
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

// TestLoginSucceeds 确认 Login 成功路径把响应字段搬进 LoginResult，
// 且不经过 translate（该函数只在 err != nil 时才被调用）。
func TestLoginSucceeds(t *testing.T) {
	var gotConnectorType atomic.Value
	env := newStubEnvFull(t, &stubServer{
		validate: okValidate("u1", 1000),
		login: func(req *fpv1.LoginRequest) (*fpv1.LoginResponse, error) {
			gotConnectorType.Store(req.GetConnectorType())
			return &fpv1.LoginResponse{
				Token:     "new-session-token",
				SessionId: "s1",
				User:      &fpv1.UserInfo{Id: "u1", Nickname: "alice"},
			}, nil
		},
	})

	res, err := env.auth.Login(context.Background(), LoginInput{ConnectorType: "password"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got, _ := gotConnectorType.Load().(string); got != "password" {
		t.Fatalf("ConnectorType 传到服务端变成了 %q", got)
	}
	if res.Token != "new-session-token" {
		t.Errorf("Token = %q，期望 new-session-token", res.Token)
	}
	if res.SessionID != "s1" {
		t.Errorf("SessionID = %q，期望 s1", res.SessionID)
	}
	if res.User.GetId() != "u1" {
		t.Errorf("User.Id = %q，期望 u1", res.User.GetId())
	}
}

// TestLoginRejectedCredentialsReturnErrUnauthorized 钉住 translate 把
// Unauthenticated 映射成 ErrUnauthorized 这一分支，经 Login 这条路径——
// 这条路径此前零测试覆盖：删掉 translate 里的整个 switch 不会让任何
// 已有测试变红。
func TestLoginRejectedCredentialsReturnErrUnauthorized(t *testing.T) {
	env := newStubEnvFull(t, &stubServer{
		validate: okValidate("u1", 1000),
		login: func(*fpv1.LoginRequest) (*fpv1.LoginResponse, error) {
			return nil, status.Error(codes.Unauthenticated, "密码错误")
		},
	})

	_, err := env.auth.Login(context.Background(), LoginInput{ConnectorType: "password"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("凭据被拒绝时 Login 返回 %v，期望 ErrUnauthorized", err)
	}
}

// TestLoginUnavailableReturnsErrUnavailable 钉住 translate 把 Unavailable
// 映射成 ErrUnavailable 这一分支，经 Login 这条路径。
func TestLoginUnavailableReturnsErrUnavailable(t *testing.T) {
	env := newStubEnvFull(t, &stubServer{
		validate: okValidate("u1", 1000),
		login: func(*fpv1.LoginRequest) (*fpv1.LoginResponse, error) {
			return nil, status.Error(codes.Unavailable, "fp 挂了")
		},
	})

	_, err := env.auth.Login(context.Background(), LoginInput{ConnectorType: "password"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fp 不可达时 Login 返回 %v，期望 ErrUnavailable", err)
	}
}

// TestLoginInvalidArgumentReturnsErrInvalidArgument 钉住 translate 把
// InvalidArgument 映射成 ErrInvalidArgument 这一分支，经 Login 这条路径。
//
// 服务端对畸形凭据（比如手机号格式不对）返回这个码——这不是"凭据不对"，
// 是"请求本身就没法处理"。此前 translate 原样透传这个码（不匹配任何
// case），落进 WriteError 的 default 分支变成 401——用户手机号打错一位，
// 会收到"未授权"，而不是"参数不对"。必须能被 errors.Is 与 ErrUnauthorized
// 区分开，否则业务方没法对这两种情况做出不同的响应。
func TestLoginInvalidArgumentReturnsErrInvalidArgument(t *testing.T) {
	env := newStubEnvFull(t, &stubServer{
		validate: okValidate("u1", 1000),
		login: func(*fpv1.LoginRequest) (*fpv1.LoginResponse, error) {
			return nil, status.Error(codes.InvalidArgument, "手机号格式不正确")
		},
	})

	_, err := env.auth.Login(context.Background(), LoginInput{ConnectorType: "password"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("参数不合法时 Login 返回 %v，期望 ErrInvalidArgument", err)
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ErrInvalidArgument 不该同时满足 errors.Is(err, ErrUnauthorized)：%v", err)
	}
}

// TestLoginRateLimitedReturnsErrRateLimited 钉住 translate 把
// ResourceExhausted 映射成 ErrRateLimited 这一分支，经 Login 这条路径。
//
// translate 是纯粹按 gRPC status code 分类的函数，不关心具体是哪个 RPC
// 触发的——用 Login 覆盖这一分支，与 TestLoginUnavailableReturnsErrUnavailable
// /TestLoginInvalidArgumentReturnsErrInvalidArgument 保持同一种装配方式。
// 限流在真实系统里最常见于 SendLoginCode（验证码发送过于频繁），但那不
// 影响这里要验证的东西：只要 gRPC 返回 ResourceExhausted，无论出现在
// 哪条 RPC 上，都必须落到 ErrRateLimited 而不是 ErrUnauthorized。
func TestLoginRateLimitedReturnsErrRateLimited(t *testing.T) {
	env := newStubEnvFull(t, &stubServer{
		validate: okValidate("u1", 1000),
		login: func(*fpv1.LoginRequest) (*fpv1.LoginResponse, error) {
			return nil, status.Error(codes.ResourceExhausted, "请求过于频繁")
		},
	})

	_, err := env.auth.Login(context.Background(), LoginInput{ConnectorType: "password"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("被限流时 Login 返回 %v，期望 ErrRateLimited", err)
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ErrRateLimited 不该同时满足 errors.Is(err, ErrUnauthorized)：%v", err)
	}
}

// TestLogoutForcesRefetchOnSameToken 盯住 Logout 里的 cache.drop(token)。
//
// 撤销推送会异步到达，但同一进程内紧接着的请求可能在事件到达前就命中了
// 那条缓存——用户点了退出，下一个请求还是登录态。Logout 必须立即、
// 同步地清掉本地缓存，不能靠等撤销推送。
//
// 用直接断言而不是 waitUntil 轮询：cache.drop(token) 在给定实现里是
// Logout 返回前就同步执行的，不像撤销推送那样存在真正的异步间隙。用
// 轮询反而会掩盖"清缓存被做成异步/延迟"这类回归——那种实现在 5 秒的
// 轮询窗口内一样能让断言最终通过。
func TestLogoutForcesRefetchOnSameToken(t *testing.T) {
	var calls atomic.Int32
	env := newStubEnvFull(t, &stubServer{
		validate: func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
			calls.Add(1)
			return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 300_000}, nil
		},
		logout: func(*fpv1.LogoutRequest) (*fpv1.LogoutResponse, error) {
			return &fpv1.LogoutResponse{}, nil
		},
	})
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("首次校验: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("首次校验回源 %d 次，期望 1 次", got)
	}

	if err := env.auth.Logout(context.Background(), "tok"); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("Logout 后再次校验: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("Logout 之后紧接着的校验命中了旧缓存（回源 %d 次，期望 2 次）——"+
			"cache.drop(token) 没有生效，用户点了退出、下一个请求还是登录态", got)
	}
}

// TestFlightResponseDoesNotResurrectConcurrentlyDroppedToken 守住必修 2：
// 一次仍在飞行中的回源响应不能在落地时复活一个刚被 drop 的 token。
//
// TestLogoutForcesRefetchOnSameToken 与 TestRevokeEventDropsCachedToken
// 都是严格串行的——drop 发生在回源之前或之后，永远不会与飞行中的回源
// 重叠。而真实场景恰恰是并发的：singleflight 的领导者已经把 RPC 发出去
// （fp 侧看到的是"会话还在"），响应还在网络上飞，这时另一个 goroutine
// 的 Logout（或撤销推送）先一步完成，领导者的响应才姗姗来迟落地——按
// cache.put 的旧实现，这次回填会把一条 fp 已经不认的判定重新写回缓存，
// 且带着完整 cache_ttl（demo 里是 600 秒）。用户点了退出，之后十分钟
// 仍是登录态。
//
// 装配：validate 回调只在第一次调用时卡住（用 calls 计数区分第几次），
// 卡住期间执行一次真实的 Logout（同步 drop("tok")，抢在飞行中的响应
// 之前完成），再放行。断言该 token 之后仍然需要回源——如果那次迟到的
// put 生效了，第二次 Validate 会直接命中缓存，回源次数不会再涨到 2。
//
// 变异验证：删掉 cache.putIfGen 里的代际核对（`if c.gen.Load() != gen {
// return }`），本测试必须变红（已手工验证，见 final-fix-report.md）。
func TestFlightResponseDoesNotResurrectConcurrentlyDroppedToken(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	env := newStubEnvFull(t, &stubServer{
		validate: func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
			if calls.Add(1) == 1 {
				// 只让第一次回源卡住，模拟"响应还在路上"。
				entered <- struct{}{}
				<-release
			}
			return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 300_000}, nil
		},
		logout: func(*fpv1.LogoutRequest) (*fpv1.LogoutResponse, error) {
			return &fpv1.LogoutResponse{}, nil
		},
	})
	env.waitUntil(t, env.client.StreamHealthy, "建流后应变为健康")

	firstDone := make(chan error, 1)
	go func() {
		_, err := env.auth.Validate(context.Background(), "tok")
		firstDone <- err
	}()
	<-entered // 第一次回源已经在飞行中、卡在服务端回调里，gen 已经在此之前被抓取

	// 飞行期间执行 Logout：同步调用 RPC 并 drop("tok")，保证在第一次
	// Validate 的响应落地之前完成——不依赖 sleep，靠 entered/release 两个
	// channel 严格排出这个顺序。
	if err := env.auth.Logout(context.Background(), "tok"); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	close(release) // 放行卡住的第一次回源，让它的（陈旧）响应落地

	if err := <-firstDone; err != nil {
		t.Fatalf("首次 Validate: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("首次 Validate 回源 %d 次，期望 1 次", got)
	}

	if _, err := env.auth.Validate(context.Background(), "tok"); err != nil {
		t.Fatalf("Logout 之后再次校验: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("Logout 之后紧接着的校验命中了缓存（回源 %d 次，期望 2 次）——"+
			"一次与 drop 并发、飞行中的回源响应在 drop 之后落地，"+
			"把已撤销的判定重新写回了缓存，用户点了退出、之后一整个 "+
			"cache_ttl 仍是登录态", got)
	}
}

// TestFollowerUnaffectedByLeaderCtxCancellation 守住必修 3。
//
// singleflight 把并发 miss 合并成一次 RPC，闭包里发那次 RPC 用的 ctx
// 只来自"领导者"（第一个进入 sf.Do 的调用方）——其余调用方是"跟随者"，
// 它们自己的 ctx 从未参与这次 RPC。领导者的 HTTP 请求被取消（浏览器
// 导航、用户点停止）不代表 fp 不可达，更不该连累同一时刻在等同一个
// token 的其他健康请求。
//
// 装配：先单独发起一个用可取消 ctx 的调用，等它的 RPC 真正进入飞行状态
// （此时它必然是 singleflight 选中的领导者，因为此刻它是这个 token 唯一
// 在飞的调用），再追加若干个用 context.Background() 发起的并发调用——
// 它们必然合并成跟随者，不会各自另起一次执行。取消领导者的 ctx、放行
// RPC，断言全部调用（含领导者自己）都拿到成功结果。
//
// 领导者自己的结果也必须成功：共享调用只应该受 ValidateTimeout 支配，
// "领导者"只是 singleflight 内部的选举结果，不是一个调用方主动接受的
// 语义约定，取消自己的 ctx 不该让共享的 RPC 提前失败。
func TestFollowerUnaffectedByLeaderCtxCancellation(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 30_000}, nil
	})

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := env.auth.Validate(leaderCtx, "tok")
		leaderDone <- err
	}()
	<-entered // 领导者的 RPC 已经在飞行中：此刻它是这个 token 唯一在飞的调用。

	const followers = 5
	followerDone := make(chan error, followers)
	for i := 0; i < followers; i++ {
		go func() {
			_, err := env.auth.Validate(context.Background(), "tok")
			followerDone <- err
		}()
	}
	time.Sleep(100 * time.Millisecond) // 让 followers 都排进 singleflight 的等待队列（同 TestConcurrentMissesCollapseToOneCall 的手法）。

	cancelLeader() // 领导者自己的 ctx 死了——共享的 RPC 不该受此影响。

	close(release) // 放行飞行中的 RPC。

	if err := <-leaderDone; err != nil {
		t.Fatalf("领导者返回 %v，期望成功——领导者自己取消 ctx 不该影响共享的 RPC 结果", err)
	}
	for i := 0; i < followers; i++ {
		if err := <-followerDone; err != nil {
			t.Fatalf("跟随者返回 %v，期望成功——fp 完全健康，"+
				"不该因为领导者的 ctx 被取消就拿到 ErrUnavailable", err)
		}
	}
}
