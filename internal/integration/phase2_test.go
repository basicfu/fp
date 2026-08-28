package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
	fpsdk "github.com/basicfu/fp/sdk"
)

// TestSDKInitAndReject 覆盖验收项 4：SDK 用 appId/appSecret 初始化。
//
// 正确的凭据能建立连接并完成调用；错误的必须被拒。
//
// 陷阱：grpc.NewClient 是惰性的，凭据错误不会在 New 时报错，只在第一次 RPC
// 时暴露。断言必须放在 RPC 上——放在 New 上的断言会永远通过，看起来在测
// 却什么都没测。
func TestSDKInitAndReject(t *testing.T) {
	e := newPhase2Env(t)
	waitUntil(t, e.sdk.StreamHealthy, "正确凭据应能建立连接并进入健康状态")

	ctx := context.Background()
	token, _, _ := e.login(t)
	if _, err := e.sdk.Auth().Validate(ctx, token); err != nil {
		t.Fatalf("正确凭据校验刚登录的 token 失败: %v", err)
	}

	bad := e.dial(t, func(o *fpsdk.Options) { o.AppSecret = "definitely-wrong-secret" })
	// 断言必须放在 RPC 调用上（fpsdk.New 本身不会报错，见上面的陷阱说明）。
	if _, err := bad.Auth().Validate(ctx, token); !errors.Is(err, fpsdk.ErrUnauthorized) {
		t.Fatalf("错误的 appSecret 调用 RPC 返回 %v，期望 ErrUnauthorized", err)
	}
}

// TestMiddlewareEndToEnd 覆盖验收项 6：带 token 放行、无 token 401。
func TestMiddlewareEndToEnd(t *testing.T) {
	e := newPhase2Env(t)
	waitUntil(t, e.sdk.StreamHealthy, "建流后应变为健康")

	token, userID, _ := e.login(t)

	var gotUserID string
	h := e.sdk.Auth().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := fpsdk.IdentityFrom(r.Context()); ok {
			gotUserID = id.UserID
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	t.Run("带合法token放行", func(t *testing.T) {
		gotUserID = ""
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatalf("构造请求: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200", resp.StatusCode)
		}
		if gotUserID != userID.String() {
			t.Fatalf("handler 拿到的 userID = %q, want %q", gotUserID, userID.String())
		}
	})

	t.Run("不带token拒绝", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("状态码 = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("乱码token拒绝", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatalf("构造请求: %v", err)
		}
		req.Header.Set("Authorization", "Bearer 乱码-not-a-real-token")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("状态码 = %d, want 401", resp.StatusCode)
		}
	})
}

// TestKickIsPushedToSDKImmediately 覆盖验收项 7，是撤销推送的全部价值所在。
//
// 关键在"立即"：把 cache_ttl 配成很长（600 秒），这样只要断言"踢完之后
// 很快就被拒"，就只可能是推送起了作用——TTL 兜底在这个时间尺度上根本
// 来不及。cache_ttl 配短的话，一个什么都没做的实现也会通过。
func TestKickIsPushedToSDKImmediately(t *testing.T) {
	e := newPhase2Env(t)
	e.updateSessionPolicy(t, func(p *domain.SessionPolicy) { p.TokenCacheTTLSeconds = 600 })
	waitUntil(t, e.sdk.StreamHealthy, "建流后应变为健康")

	ctx := context.Background()
	token, userID, _ := e.login(t)
	if _, err := e.sdk.Auth().Validate(ctx, token); err != nil {
		t.Fatalf("首次校验: %v", err)
	}

	if _, err := e.accounts.RevokeAllSessions(ctx, userID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	waitUntilTimeout(t, 5*time.Second, func() bool {
		_, err := e.sdk.Auth().Validate(ctx, token)
		return errors.Is(err, fpsdk.ErrUnauthorized)
	}, "管理端踢下线后，SDK 在 5 秒内未开始拒绝该 token——"+
		"cache_ttl 长达 600 秒，5 秒内的失效只可能来自推送")
}

// TestSDKSurvivesFpOutage 覆盖验收项 8——"fp 一挂，业务不中断"。
//
// 这是整个 opaque token + 本地缓存方案存在的理由。若不成立，fp 就成了每个
// 接入方每条请求路径上的强依赖，比各自实现一套用户体系更糟。
func TestSDKSurvivesFpOutage(t *testing.T) {
	e := newPhase2Env(t)
	// cache_ttl 调小（而不是用默认的 30 秒）：既够 (a) 用——立即校验一次
	// 远在窗口内——又能让 (c) 的"等它过期"这段真实等待保持在数秒级。
	const cacheTTL = 4 * time.Second
	e.updateSessionPolicy(t, func(p *domain.SessionPolicy) {
		p.TokenCacheTTLSeconds = int32(cacheTTL.Seconds())
	})
	waitUntil(t, e.sdk.StreamHealthy, "建流后应变为健康")

	stale := e.dial(t, func(o *fpsdk.Options) {
		o.AllowStaleOnOutage = true
		o.MaxStaleness = 30 * time.Second
	})
	waitUntil(t, stale.StreamHealthy, "陈旧兜底客户端建流后应变为健康")

	ctx := context.Background()
	token, userID, _ := e.login(t)

	if _, err := e.sdk.Auth().Validate(ctx, token); err != nil {
		t.Fatalf("默认客户端首次校验: %v", err)
	}
	if _, err := stale.Auth().Validate(ctx, token); err != nil {
		t.Fatalf("陈旧兜底客户端首次校验: %v", err)
	}

	e.stopFp(t)

	// (a) cache_ttl 内：默认客户端（未开 AllowStaleOnOutage）仍应成功。
	// fp 此刻已确认停止监听，任何真正的回源都不可能成功——若这里返回成功，
	// 唯一的解释就是命中了本地缓存、完全没有产生网络调用：cache.get 在
	// fresh 分支是同步直接返回，连 singleflight/RPC 都不会进入。
	id, err := e.sdk.Auth().Validate(ctx, token)
	if err != nil {
		t.Fatalf("fp 不可用但仍在 cache_ttl 内的校验失败: %v", err)
	}
	if id.UserID != userID.String() {
		t.Fatalf("缓存命中返回的 UserID = %q, want %q", id.UserID, userID.String())
	}

	// (b) 没缓存过的 token：必须是 ErrUnavailable，不能是 ErrUnauthorized——
	// 业务方据此回 503 而不是 401，否则会把一次 fp 抖动放大成清掉一个其实
	// 有效的 token。
	_, err = e.sdk.Auth().Validate(ctx, "从未出现过的-token-fp-outage-test")
	if !errors.Is(err, fpsdk.ErrUnavailable) {
		t.Fatalf("未缓存 token 在 fp 不可达时返回 %v，期望 ErrUnavailable", err)
	}
	if errors.Is(err, fpsdk.ErrUnauthorized) {
		t.Fatalf("未缓存 token 在 fp 不可达时被判定为 ErrUnauthorized——" +
			"业务方会因此清掉一个其实可能有效的 token")
	}

	// (c) 等 cache_ttl 过期。这里没有可轮询的异步事件——"这条缓存条目已经
	// 过期"就是时钟本身走过了一个阈值，所以只能真等；cache_ttl 特意配成
	// 4 秒就是为了让这一步保持在秒级。
	time.Sleep(cacheTTL + 500*time.Millisecond)

	id, err = stale.Auth().Validate(ctx, token)
	if err != nil {
		t.Fatalf("开了 AllowStaleOnOutage 的客户端在陈旧窗口内校验失败: %v", err)
	}
	if id.UserID != userID.String() {
		t.Fatalf("陈旧兜底返回的 UserID = %q, want %q", id.UserID, userID.String())
	}
	if !id.Stale {
		t.Fatal("陈旧兜底返回的 Identity 未标记 Stale——业务方无法据此拒绝高危操作")
	}
}

// TestRotationHandoffSurvivesLostResponse 是 Task 3 + Task 10 + Task 11 的
// 合验。模拟真实的失败模式：轮换发生时，拿到 new_token 的那个响应被丢弃
// （请求取消 / 页面忽略响应体）。客户端仍握着旧 token 继续请求，必须能
// 重新拿到新 token，而不是在过渡期结束时静默登出。
//
// 走 HTTP 中间件而不是直接调 Auth().Validate：这条测试要合验的第三块
// （Task 11）恰恰是"中间件在每一次请求上都要交付 RotatedTo"，只调
// Validate 测不到这一棒。
//
// 简报建议 cache_ttl 配 0（强制每次回源，隔离出 fp 侧的重复告知能力，
// 不与 SDK 缓存carries-rotatedTo 那条单独的能力——sdk.TestRotationIsSurfacedOnCacheHit
// 已经覆盖——混在一起）。但 domain.SessionPolicy.Validate 要求它必须为正，
// 0 会被拒绝——这是简报的一处笔误。这里改用 schema 允许的最小值 1 秒，
// 配合每次请求之间 sleep 过 1 秒，达到完全相同的效果：强制每一次请求都真的
// 回源。这不会削弱"隔离出重复告知能力"这条设计意图：GraceDuration =
// max(15s, cfg) 恒为 15 秒，不受 cfg 取 1 还是 0 影响，过渡期依旧充裕。
func TestRotationHandoffSurvivesLostResponse(t *testing.T) {
	e := newPhase2Env(t)
	e.updateSessionPolicy(t, func(p *domain.SessionPolicy) {
		p.RotateIntervalSeconds = 1
		p.TokenCacheTTLSeconds = 1
	})
	waitUntil(t, e.sdk.StreamHealthy, "建流后应变为健康")

	oldToken, _, _ := e.login(t)

	h := e.sdk.Auth().Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	doRequest := func() string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatalf("构造请求: %v", err)
		}
		// 客户端自始至终只握着旧 token——绝不切换到任何一次响应里带回的新
		// token，这正是"响应被丢弃"这个失败模式的核心。
		req.Header.Set("Authorization", "Bearer "+oldToken)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200（旧 token 在过渡期内应继续有效）", resp.StatusCode)
		}
		return resp.Header.Get(fpsdk.RotatedTokenHeader)
	}

	time.Sleep(1200 * time.Millisecond) // 越过 rotate_interval（1 秒）。

	// 第一次请求触发轮换、拿到 newToken——随后丢弃它：不读它去更新任何状态。
	first := doRequest()
	if first == "" {
		t.Fatal("第一次请求没有触发轮换——测试前提不成立")
	}

	for i := 2; i <= 3; i++ {
		time.Sleep(1200 * time.Millisecond) // 越过 token_cache_ttl，强制这次请求真的回源。
		got := doRequest()
		if got != first {
			t.Fatalf("第 %d 次请求（仍带旧 token）收到的新 token 是 %q，首次是 %q——"+
				"过渡期内的重复告知没有生效，客户端会在过渡期结束时静默登出", i, got, first)
		}
	}
}

// TestStreamOutageTightensCacheWindow 验证降级策略端到端生效：cache_ttl 配
// 600 秒、DegradedCacheTTL 配 1 秒；登录并缓存后 stopFp()，等
// StreamHealthy() 变 false，断言 1 秒后同一 token 不再命中缓存——这条证明
// 收紧对存量条目生效，是 Task 9 那条设计的端到端体现。
func TestStreamOutageTightensCacheWindow(t *testing.T) {
	e := newPhase2Env(t, func(o *fpsdk.Options) {
		o.DegradedCacheTTL = time.Second
	})
	e.updateSessionPolicy(t, func(p *domain.SessionPolicy) { p.TokenCacheTTLSeconds = 600 })
	waitUntil(t, e.sdk.StreamHealthy, "建流后应变为健康")

	ctx := context.Background()
	token, _, _ := e.login(t)
	if _, err := e.sdk.Auth().Validate(ctx, token); err != nil {
		t.Fatalf("首次校验: %v", err)
	}

	e.stopFp(t)
	waitUntil(t, func() bool { return !e.sdk.StreamHealthy() },
		"服务端停止后 StreamHealthy 应变为 false")

	// 让缓存条目的存续时间越过 DegradedCacheTTL（1 秒），但仍远小于原始
	// cache_ttl（600 秒）——只有收紧真的作用于这条存量条目时，下面的查询
	// 才会判定过期、尝试回源；回源必然因服务端已停而失败。
	time.Sleep(1200 * time.Millisecond)

	if _, err := e.sdk.Auth().Validate(ctx, token); !errors.Is(err, fpsdk.ErrUnavailable) {
		t.Fatalf("流断开超过 DegradedCacheTTL 后返回 %v，期望 ErrUnavailable——"+
			"本该判定缓存过期并尝试回源（服务端已停，回源必然失败），"+
			"而不是继续吃原始 600 秒长窗口下本还新鲜的缓存", err)
	}
}

// TestRevokeCrossesFpInstances 是多实例部署的核心验证（设计决策 4.5）。
//
// 两个 fp 实例，各自监听不同端口，但共用同一套 PG + Redis。SDK 只连
// instanceA，撤销从 instanceB 触发——必须走 instanceB：这是本测试存在的
// 全部意义。走 instanceA 的话就退化成 TestKickIsPushedToSDKImmediately，
// 一个只通知本进程的实现也能通过。
func TestRevokeCrossesFpInstances(t *testing.T) {
	instanceA := newPhase2Env(t)
	instanceB := instanceA.spawnPeer(t)

	// cache_ttl 配成 600 秒，确保后面的失效只可能来自推送而非 TTL 到期。
	instanceA.updateSessionPolicy(t, func(p *domain.SessionPolicy) { p.TokenCacheTTLSeconds = 600 })
	waitUntil(t, instanceA.sdk.StreamHealthy, "建流后应变为健康")

	ctx := context.Background()
	token, userID, _ := instanceA.login(t)
	if _, err := instanceA.sdk.Auth().Validate(ctx, token); err != nil {
		t.Fatalf("首次校验: %v", err)
	}

	if _, err := instanceB.accounts.RevokeAllSessions(ctx, userID); err != nil {
		t.Fatalf("经 instanceB 撤销: %v", err)
	}

	waitUntilTimeout(t, 5*time.Second, func() bool {
		_, err := instanceA.sdk.Auth().Validate(ctx, token)
		return errors.Is(err, fpsdk.ErrUnauthorized)
	}, "经 instanceB 撤销后，连在 instanceA 上的 SDK 5 秒内未开始拒绝——"+
		"跨实例的撤销传播（Redis pub/sub 桥接）未生效")
}

// TestSDKWorksAgainstAnyInstance 确认 fp 对会话确实无状态："不需要连接
// 亲和"这条结论的全部依据就是它。
func TestSDKWorksAgainstAnyInstance(t *testing.T) {
	instanceA := newPhase2Env(t)
	instanceB := instanceA.spawnPeer(t)
	instanceA.updateSessionPolicy(t, func(p *domain.SessionPolicy) { p.TokenCacheTTLSeconds = 600 })
	waitUntil(t, instanceA.sdk.StreamHealthy, "建流后应变为健康")

	ctx := context.Background()
	token, userID, sessionID := instanceA.login(t)

	clientB := instanceB.dial(t)
	waitUntil(t, clientB.StreamHealthy, "连 instanceB 的客户端建流后应变为健康")

	id, err := clientB.Auth().Validate(ctx, token)
	if err != nil {
		t.Fatalf("连 instanceB 的客户端校验 instanceA 签发的 token 失败: %v", err)
	}
	if id.UserID != userID.String() {
		t.Fatalf("instanceB 给出的 UserID = %q, want %q——两个实例对同一个 token 给出了不一致的身份",
			id.UserID, userID.String())
	}
	if id.SessionID != sessionID {
		t.Fatalf("instanceB 给出的 SessionID = %q, want %q", id.SessionID, sessionID)
	}

	if err := instanceA.sdk.Auth().Logout(ctx, token); err != nil {
		t.Fatalf("经 instanceA 登出: %v", err)
	}

	waitUntilTimeout(t, 5*time.Second, func() bool {
		_, err := clientB.Auth().Validate(ctx, token)
		return errors.Is(err, fpsdk.ErrUnauthorized)
	}, "经 instanceA 登出后，连在 instanceB 上的 SDK 5 秒内未开始拒绝")
}

// TestRedisSubscriptionBlipDoesNotSilentlyLoseRevocations 是"让丢事件可
// 观测"这条改动的验收标准，改造前必然失败。
//
// 复现的是生产上真实会发生的一幕：Redis 故障转移或网络抖动打断了 fp 的
// 订阅连接。go-redis 会静默重连并重发 SUBSCRIBE——不报错、不关
// channel——那个窗口里发布的撤销事件对这台 fp 永久丢失，而 SDK 看到的流
// 一直是健康的。
//
// 改造前：SDK 在整个 cache_ttl 内继续放行一个已被踢下线的用户。
// 改造后：fp 从"第二次 SUBSCRIBE 确认"察觉到缺口，广播 Purge，SDK 清空
// 缓存，下一次请求回源被拒。
//
// 用 canary token 而不是只看目标 token 有没有被拒——这一点是本测试能不能
// 真正测出 Purge 缺失的关键，是自查时用变异测试（临时让 broadcastPurge 不
// 被调用）实测发现的：go-redis 的自动重连+重订阅经常快到目标 token 自己
// 那条撤销事件仍能通过*正常*的 fanout 路径（RevokeSignalEvent，不经过
// Gap/Purge）及时送达——那种情况下，即便 Purge 分支被整个删掉，目标
// token 照样会被拒绝，是巧合而不是 Purge 生效，一个"掐了订阅但什么都不做"
// 的坏实现能靠这份巧合把断言蒙混过去。
//
// canary 从未被任何撤销事件点名过（它属于一个完全不同、从未被撤销的
// 用户），且它的失效方式刻意绕开 announce/Publish——直接在 store 层删它
// 的会话。它能否继续被 SDK 缓存放行，因此只取决于本地那份缓存有没有被
// **整体**清空过；而清空整份缓存的唯一触发源就是 Purge。这就把"是不是
// Purge 起的作用"从一场没法控制的时序赌博，变成了一个结构上确定的判据。
func TestRedisSubscriptionBlipDoesNotSilentlyLoseRevocations(t *testing.T) {
	e := newPhase2Env(t)
	// cache_ttl 配成很长：让 TTL 兜底彻底来不及，这样"很快被拒"就只可能是
	// Purge 带来的。cache_ttl 配短的话，一个什么都没做的实现也会通过。
	e.updateSessionPolicy(t, func(p *domain.SessionPolicy) { p.TokenCacheTTLSeconds = 600 })
	waitUntil(t, e.sdk.StreamHealthy, "建流后应变为健康")

	ctx := context.Background()
	token, userID, _ := e.login(t)
	if _, err := e.sdk.Auth().Validate(ctx, token); err != nil {
		t.Fatalf("首次校验: %v", err)
	}

	// canary：另一个用户、另一个 token，同样先缓存下来，但此后不会有任何
	// 撤销事件提到它。
	canaryToken, _, _ := e.login(t)
	if _, err := e.sdk.Auth().Validate(ctx, canaryToken); err != nil {
		t.Fatalf("canary 首次校验: %v", err)
	}

	killPubSubConnection(t, e.rdb)

	// 在 go-redis 重连之前触发撤销。这条事件注定送不到 fp——时序不好控，
	// 所以断开后立刻撤销，接受偶发的"其实没丢"：即便这次没丢，重连本身
	// 也会无条件触发 Purge（悲观策略），下面对 canary 的断言依然成立，
	// 不依赖精确时序（也不依赖这条事件到底丢没丢）。
	if _, err := e.accounts.RevokeAllSessions(ctx, userID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	// 不经过 announce，直接在 store 层删掉 canary 的会话：服务端权威数据
	// 已经不认这个 token 了，但没有任何撤销事件为它发布过。
	if err := store.NewSessionStore(e.rdb).Delete(ctx, canaryToken); err != nil {
		t.Fatalf("直接删除 canary 会话: %v", err)
	}

	waitUntilTimeout(t, 5*time.Second, func() bool {
		_, err := e.sdk.Auth().Validate(ctx, canaryToken)
		return errors.Is(err, fpsdk.ErrUnauthorized)
	}, "Redis 订阅抖动之后，从未被任何撤销事件点名的 canary token 在 5 秒内未被拒绝——"+
		"它唯一可能失效的途径是缓存被 Purge 整体清空；cache_ttl 长达 600 秒，"+
		"5 秒内的自然过期不可能是原因，所以这里没有变红就说明 Purge 没有发生")

	// 目标 token 本身也应当被拒绝——这是这条链路在真实场景下的可见后果，
	// 与上面的 canary 断言互补：canary 证明"Purge 确实发生了"，这里确认
	// "确实发生的后果里包含了它"。
	waitUntilTimeout(t, 5*time.Second, func() bool {
		_, err := e.sdk.Auth().Validate(ctx, token)
		return errors.Is(err, fpsdk.ErrUnauthorized)
	}, "Redis 订阅抖动之后，SDK 在 5 秒内未开始拒绝该 token")
}
