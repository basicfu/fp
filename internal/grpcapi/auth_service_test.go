package grpcapi

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// TestCacheTTLIsInMilliseconds 是本任务最重要的一个测试。
//
// 服务端的 ValidateResult.CacheTTL 是 time.Duration，proto 字段是毫秒。
// 写成 int64(res.CacheTTL) 会下发纳秒（30 秒变成三百亿），写成
// int64(res.CacheTTL.Seconds()) 会下发 30 而字段名说的是毫秒——
// SDK 于是只缓存 30 毫秒，回源量放大一千倍，而**一切功能都正常**，
// 只有 fp 的负载图会莫名其妙地翻一千倍。没有测试能靠"跑通了"发现它。
func TestCacheTTLIsInMilliseconds(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())

	token := env.loginWithPassword(t, ctx)

	res, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token})
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}

	wantMs := int64(env.app.Session.TokenCacheTTLSeconds) * 1000
	if res.GetCacheTtlMs() != wantMs {
		t.Fatalf("cache_ttl_ms = %d，期望 %d（token_cache_ttl = %d 秒）",
			res.GetCacheTtlMs(), wantMs, env.app.Session.TokenCacheTTLSeconds)
	}
}

// TestCacheTTLIsNeverNegative 守住一个下限。
//
// 负的 TTL 经 SDK 会算出一个"已经过期"的缓存条目——最好的情况是每次都回源，
// 最坏的情况是某个 min/max 比较把它当成"很久以后"。
func TestCacheTTLIsNeverNegative(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	token := env.loginWithPassword(t, ctx)

	res, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token})
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if res.GetCacheTtlMs() < 0 {
		t.Fatalf("cache_ttl_ms 为负: %d", res.GetCacheTtlMs())
	}
}

// TestTokenFromAnotherAppIsRejected 守住跨应用隔离。
//
// appId 来自拦截器写进 ctx 的值，proto 里刻意没有 appId 字段。
// 但 handler 仍可能把空串传给 service（比如忘了取 ctx），那样
// activeApp 会失败——也可能有人"顺手"加个回退。这条测试钉死：
// A 应用签发的 token，拿 B 应用的凭据去校验必须失败。
func TestTokenFromAnotherAppIsRejected(t *testing.T) {
	env := newGRPCEnv(t)
	other := env.newApplication(t) // 同一个 fp，另一个应用

	token := env.loginWithPassword(t, env.authed(context.Background()))

	otherCtx := metadata.AppendToOutgoingContext(context.Background(),
		mdAppID, other.appID, mdAppSecret, other.secret)
	_, err := env.client.ValidateToken(otherCtx, &fpv1.ValidateTokenRequest{Token: token})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("跨应用校验返回 %v，期望 Unauthenticated", status.Code(err))
	}
}

func TestRPCsRequireCredentials(t *testing.T) {
	env := newGRPCEnv(t)
	bare := context.Background()

	calls := map[string]func() error{
		"SendLoginCode": func() error {
			_, err := env.client.SendLoginCode(bare, &fpv1.SendLoginCodeRequest{Phone: "13800138000"})
			return err
		},
		"Login": func() error {
			_, err := env.client.Login(bare, &fpv1.LoginRequest{ConnectorType: "password"})
			return err
		},
		"Logout": func() error {
			_, err := env.client.Logout(bare, &fpv1.LogoutRequest{Token: "x"})
			return err
		},
		"ValidateToken": func() error {
			_, err := env.client.ValidateToken(bare, &fpv1.ValidateTokenRequest{Token: "x"})
			return err
		},
	}
	for name, call := range calls {
		if code := status.Code(call()); code != codes.Unauthenticated {
			t.Errorf("%s 在无凭据时返回 %v，期望 Unauthenticated", name, code)
		}
	}
}

// TestSMSCodeLoginRoundTrip 走一遍发码→登录→校验。
func TestSMSCodeLoginRoundTrip(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	const phone = "13800138000"

	if _, err := env.client.SendLoginCode(ctx, &fpv1.SendLoginCodeRequest{Phone: phone}); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	code := env.sms.LastParam("code")
	if code == "" {
		t.Fatal("假短信供应商没有收到验证码")
	}

	login, err := env.client.Login(ctx, &fpv1.LoginRequest{
		ConnectorType: "sms_code",
		Credentials:   map[string]string{"phone": phone, "code": code},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login.GetToken() == "" || login.GetUser().GetId() == "" {
		t.Fatalf("登录响应缺字段: %+v", login)
	}

	res, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: login.GetToken()})
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if res.GetUserId() != login.GetUser().GetId() {
		t.Fatalf("校验返回的 userId %q 与登录返回的 %q 不一致",
			res.GetUserId(), login.GetUser().GetId())
	}
	if res.GetSessionId() != login.GetSessionId() {
		t.Fatalf("校验返回的 sessionId %q 与登录返回的 %q 不一致",
			res.GetSessionId(), login.GetSessionId())
	}
}

// TestLogoutInvalidatesToken 确认登出真的作废 token。
func TestLogoutInvalidatesToken(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	token := env.loginWithPassword(t, ctx)

	if _, err := env.client.Logout(ctx, &fpv1.LogoutRequest{Token: token}); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("登出后校验返回 %v，期望 Unauthenticated", status.Code(err))
	}
}

// TestRotationIsRelayedOverGRPC 确认轮换字段没在映射层丢掉。
//
// Rotated / NewToken 是两个很容易在"写映射代码"时漏掉的字段——漏掉不报错，
// 只是所有会话在轮换过渡期后集体登出，而那要等到 rotate_interval
// （默认 24 小时）之后才在线上显形。
func TestRotationIsRelayedOverGRPC(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())

	// 把应用改成 1 秒轮换，并让服务端时钟前进越过它。
	env.setRotateInterval(t, 1)
	token := env.loginWithPassword(t, ctx)
	env.clock.Advance(2 * time.Second)

	res, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token})
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if !res.GetRotated() {
		t.Fatal("越过 rotate_interval 后 rotated 仍为 false")
	}
	if res.GetNewToken() == "" {
		t.Fatal("rotated=true 但 new_token 为空——客户端将无从切换，过渡期后登出")
	}
	if res.GetNewToken() == token {
		t.Fatal("new_token 与旧 token 相同")
	}
}

// TestDisabledApplicationIsRejected 确认停用应用在 gRPC 入口同样生效。
//
// Task 4 的凭据缓存刻意只缓存"凭据有效"这个事实、不缓存 Application 对象，
// 就是为了让这条断言成立。谁把 *domain.Application 塞进缓存，
// 停用应用会在 TTL（5 分钟）内继续放行，而这条测试会失败。
func TestDisabledApplicationIsRejected(t *testing.T) {
	env := newGRPCEnv(t)
	ctx := env.authed(context.Background())
	token := env.loginWithPassword(t, ctx)

	env.disableApplication(t)

	if _, err := env.client.ValidateToken(ctx, &fpv1.ValidateTokenRequest{Token: token}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("停用应用后校验返回 %v，期望 PermissionDenied", status.Code(err))
	}
}

// TestWatchDeliversRevokeToClient 走一遍真实的流。
//
// 前面的 hub 测试都在进程内直接读 channel，绕过了 gRPC 编解码与
// oneof 的封装。这条测试确认 ready 与 revoke 两种事件都能正确到达对端。
func TestWatchDeliversRevokeToClient(t *testing.T) {
	env := newGRPCEnv(t)
	ctx, cancel := context.WithTimeout(env.authed(context.Background()), 10*time.Second)
	defer cancel()

	token := env.loginWithPassword(t, ctx)

	stream, err := env.client.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("首条消息: %v", err)
	}
	if first.GetReady() == nil {
		t.Fatalf("首条消息不是 ready: %+v", first)
	}

	// ready 之后再触发撤销，确保不是靠时序侥幸。
	if _, err := env.client.Logout(ctx, &fpv1.LogoutRequest{Token: token}); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("撤销消息: %v", err)
	}
	rev := msg.GetRevoke()
	if rev == nil {
		t.Fatalf("第二条消息不是 revoke: %+v", msg)
	}
	if len(rev.GetTokens()) == 0 || rev.GetTokens()[0] != token {
		t.Fatalf("撤销事件里的 token 是 %v，期望包含 %q", rev.GetTokens(), token)
	}
}
