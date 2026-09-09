package grpcapi

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// enableIM 给 env 的主应用打开 IM 并套用给定配置，返回它。
func enableIM(t *testing.T, e *grpcEnv, mut func(*domain.IMConfig)) domain.IMConfig {
	t.Helper()
	cfg := domain.DefaultIMConfig()
	cfg.Enabled = true
	if mut != nil {
		mut(&cfg)
	}
	if _, err := e.apps.SetIMConfig(context.Background(), e.app.ID, cfg); err != nil {
		t.Fatalf("SetIMConfig: %v", err)
	}
	return cfg
}

// TestGetAppIMConfigRequiresIMCaller：普通应用不能调网关接口。
// 与 requireNotIM 一起构成双向隔离。
func TestGetAppIMConfigRequiresIMCaller(t *testing.T) {
	e := newGRPCEnv(t)
	enableIM(t, e, nil)
	_, err := e.imClient.GetAppIMConfig(e.authed(context.Background()), &fpv1.GetAppIMConfigRequest{})
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", got)
	}
}

func TestVerifyAppCredentialRequiresIMCaller(t *testing.T) {
	e := newGRPCEnv(t)
	enableIM(t, e, nil)
	_, err := e.imClient.VerifyAppCredential(e.authed(context.Background()),
		&fpv1.VerifyAppCredentialRequest{Secret: e.secret})
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", got)
	}
}

// TestGetAppIMConfigReturnsEveryField 逐字段断言：漏映射一个字段不会让任何
// 别的测试变红，而 fp-im 会拿到一个静默的零值——比如 conn_limit 变成 0，
// limit 策略下所有连接都被拒。
func TestGetAppIMConfigReturnsEveryField(t *testing.T) {
	e := newGRPCEnv(t)
	enableIM(t, e, func(c *domain.IMConfig) {
		c.ConnPolicy = domain.IMConnPolicyLimit
		c.ConnLimit = 3
		c.AllowGuest = true
		c.GuestIPRate = 42
		c.BizAuth = &domain.IMBizAuth{VerifyURL: "https://biz/v", TimeoutMs: 1500, CacheSize: 77}
	})

	resp, err := e.imClient.GetAppIMConfig(
		e.imAuthed(context.Background(), e.appID), &fpv1.GetAppIMConfigRequest{})
	if err != nil {
		t.Fatalf("GetAppIMConfig: %v", err)
	}
	if resp.GetConnPolicy() != domain.IMConnPolicyLimit {
		t.Errorf("conn_policy = %q", resp.GetConnPolicy())
	}
	if resp.GetConnLimit() != 3 {
		t.Errorf("conn_limit = %d, want 3", resp.GetConnLimit())
	}
	if !resp.GetAllowGuest() {
		t.Error("allow_guest = false, want true")
	}
	if resp.GetGuestIpRate() != 42 {
		t.Errorf("guest_ip_rate = %d, want 42", resp.GetGuestIpRate())
	}
	b := resp.GetBizAuth()
	if b == nil {
		t.Fatal("biz_auth 缺失")
	}
	if b.GetVerifyUrl() != "https://biz/v" || b.GetTimeoutMs() != 1500 || b.GetCacheSize() != 77 {
		t.Errorf("biz_auth = %+v", b)
	}
}

// TestGetAppIMConfigOmitsBizAuthWhenUnset：没配 biz_auth 时必须不设这个字段，
// 而不是回一个零值 message——后者会让 fp-im 以为"支持业务方令牌，但地址是
// 空串"，于是 kind:biz 的握手走进一条永远失败的回调。
func TestGetAppIMConfigOmitsBizAuthWhenUnset(t *testing.T) {
	e := newGRPCEnv(t)
	enableIM(t, e, nil)
	resp, err := e.imClient.GetAppIMConfig(
		e.imAuthed(context.Background(), e.appID), &fpv1.GetAppIMConfigRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetBizAuth() != nil {
		t.Fatalf("biz_auth = %+v，未配置时必须不设", resp.GetBizAuth())
	}
}

// TestGetAppIMConfigRejectsDisabledIM：im_enabled 关着时必须是
// FailedPrecondition——fp-im 据此给关闭码 4002 而不是 4001。给错了的话
// client 会被指去重新登录，登录完还是连不上，变成死循环。
func TestGetAppIMConfigRejectsDisabledIM(t *testing.T) {
	e := newGRPCEnv(t) // 主应用默认 im_enabled=false
	_, err := e.imClient.GetAppIMConfig(
		e.imAuthed(context.Background(), e.appID), &fpv1.GetAppIMConfigRequest{})
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition", got)
	}
}

// TestVerifyAppCredentialChecksSecretBeforeIMEnabled 钉住检查顺序。
//
// 先 bcrypt 验凭据、再看 im_enabled：顺序反了的话，一个手里没有 appSecret
// 的人也能探出某个应用有没有开 IM。而凭据验过之后再告诉它"IM 没开"是安全
// 的——能走到这一步的人本来就持有那份 secret，且运维需要这个区分才知道去
// 翻开关。
func TestVerifyAppCredentialChecksSecretBeforeIMEnabled(t *testing.T) {
	e := newGRPCEnv(t) // 主应用默认 im_enabled=false
	ctx := e.imAuthed(context.Background(), e.appID)

	// 凭据错 + IM 没开 → Unauthenticated，不泄露 im_enabled。
	_, err := e.imClient.VerifyAppCredential(ctx, &fpv1.VerifyAppCredentialRequest{Secret: "wrong"})
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("secret 错时 code = %v, want Unauthenticated", got)
	}
	// 凭据对 + IM 没开 → FailedPrecondition，明确指向那个开关。
	_, err = e.imClient.VerifyAppCredential(ctx, &fpv1.VerifyAppCredentialRequest{Secret: e.secret})
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("secret 对但 IM 未启用时 code = %v, want FailedPrecondition", got)
	}
}

func TestVerifyAppCredentialAcceptsValidSecret(t *testing.T) {
	e := newGRPCEnv(t)
	enableIM(t, e, nil)
	if _, err := e.imClient.VerifyAppCredential(
		e.imAuthed(context.Background(), e.appID),
		&fpv1.VerifyAppCredentialRequest{Secret: e.secret}); err != nil {
		t.Fatalf("合法凭据被拒：%v", err)
	}
}

// TestIMConfigChangePushesToIMWatcher 钉住热更新链路：控制台改完 IM 配置，
// fp-im 的 Watch 流上要收到通知，而不是等它自己轮询。
//
// 这条链路替掉了 fp-im 早期那套"每 10 秒看一次 apps 文件 mtime"。
func TestIMConfigChangePushesToIMWatcher(t *testing.T) {
	e := newGRPCEnv(t)
	enableIM(t, e, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := e.client.Watch(e.imAuthed(ctx, e.appID))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if ev, err := stream.Recv(); err != nil || ev.GetReady() == nil {
		t.Fatalf("首帧应当是 ready，got %+v err=%v", ev, err)
	}

	cfg := domain.DefaultIMConfig()
	cfg.Enabled = true
	cfg.GuestIPRate = 99
	if _, err := e.apps.SetIMConfig(context.Background(), e.app.ID, cfg); err != nil {
		t.Fatal(err)
	}

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	changed := ev.GetAppImConfigChanged()
	if changed == nil {
		t.Fatalf("收到的事件不是 AppIMConfigChanged：%+v", ev)
	}
	if changed.GetAppId() != e.appID {
		t.Fatalf("app_id = %q, want %q", changed.GetAppId(), e.appID)
	}
}

// TestIMConfigChangeNotPushedToNormalWatcher：普通业务方 SDK 不该收到这类
// 事件——它们不认识这个 oneof 分支，也不该被无关的通知打扰。
func TestIMConfigChangeNotPushedToNormalWatcher(t *testing.T) {
	e := newGRPCEnv(t)
	enableIM(t, e, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := e.client.Watch(e.authed(ctx))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if ev, err := stream.Recv(); err != nil || ev.GetReady() == nil {
		t.Fatalf("首帧应当是 ready，got %+v err=%v", ev, err)
	}

	cfg := domain.DefaultIMConfig()
	cfg.Enabled = true
	cfg.GuestIPRate = 88
	if _, err := e.apps.SetIMConfig(context.Background(), e.app.ID, cfg); err != nil {
		t.Fatal(err)
	}

	// 用一条随后发生的、普通订阅者**确实**该收到的事件当路标：如果 IM 事件
	// 被错误地推给了普通流，它会排在这条前面先被收到。光靠"等一会儿没东西"
	// 是会撒谎的假通过——机器慢一点就等不到本该来的那条。
	if _, err := e.configs.Save(context.Background(), e.app.ID, "DEFAULT", "a: 1\n", true); err != nil {
		t.Fatal(err)
	}
	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.GetAppImConfigChanged() != nil {
		t.Fatalf("普通订阅者收到了 IM 配置变更：%+v", ev)
	}
	if ev.GetConfigChanged() == nil {
		t.Fatalf("期望收到配置中心变更，got %+v", ev)
	}
}
