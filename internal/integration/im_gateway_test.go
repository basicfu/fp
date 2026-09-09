package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	fpsdk "github.com/basicfu/fp/sdk"
)

// newIMClient 起一个 CallerType=im 的 SDK 客户端，凭据是本环境生成的 IM
// secret，连接级不带 app_id——与生产环境里 fp-im 的形状一致。
func newIMClient(t *testing.T, e *phase2Env) *fpsdk.Client {
	t.Helper()
	secret, err := e.imCreds.Rotate(context.Background())
	if err != nil {
		t.Fatalf("生成 IM 凭据: %v", err)
	}
	c, err := fpsdk.New(fpsdk.Options{
		Addr:       e.addr,
		AppSecret:  secret,
		CallerType: fpsdk.CallerTypeIM,
		Insecure:   true,
	})
	if err != nil {
		t.Fatalf("建 IM 客户端: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// enableIMFor 给指定应用打开 IM 接入。
func enableIMFor(t *testing.T, e *phase2Env, id uuid.UUID) {
	t.Helper()
	cfg := domain.DefaultIMConfig()
	cfg.Enabled = true
	cfg.AllowGuest = true
	cfg.GuestIPRate = 25
	if _, err := e.apps.SetIMConfig(context.Background(), id, cfg); err != nil {
		t.Fatalf("SetIMConfig: %v", err)
	}
}

// TestIMGatewayValidatesPerCallAppScope 是本阶段最重要的端到端断言。
//
// **一条连接、逐调用切换 app 作用域。** fp-im 只有一个 fpsdk.Client，服务
// 所有应用；app 作用域来自每个 client 的 ws 握手帧，用 WithAppID 附上。
// 正是这一点让 ValidateToken 一个字段都不用改。
//
// 同时钉住那条安全边界没有被这个方案挪动：别的应用的 token 拿到本应用上
// 验必须失败——sess.AppID != app.ID 那条检查仍然在 fp 侧执行。
func TestIMGatewayValidatesPerCallAppScope(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	enableIMFor(t, e, e.app.ID)

	token, _, _ := e.loginWithPhone(t, "13800000001")

	im := newIMClient(t, e)

	id, err := im.Auth().Validate(fpsdk.WithAppID(ctx, e.appID), token)
	if err != nil {
		t.Fatalf("IM 身份验本应用的 token 失败：%v", err)
	}
	if id.UserID == "" {
		t.Fatal("没解析出 user_id")
	}

	// 换一个作用域：另一个真实存在、也开了 IM 的应用。同一条连接，
	// 只是 WithAppID 换了。
	other, _, err := e.apps.Create(ctx, "im-other-"+uuid.NewString(), "im-other-"+uuid.NewString())
	if err != nil {
		t.Fatalf("创建第二个应用: %v", err)
	}
	enableIMFor(t, e, other.ID)

	if _, err := im.Auth().Validate(fpsdk.WithAppID(ctx, other.AppID), token); err == nil {
		t.Fatal("本应用的 token 在别的应用上验必须失败——" +
			"sess.AppID != app.ID 那条检查不能因为走了 IM 通道就失效")
	}
}

// TestIMGatewayCannotLogin 在真实 gRPC 上再钉一次爆炸半径边界。
//
// 一份泄露的 IM 凭据不能签发任何会话——这是"双向隔离而不是提权"这条设计
// 的全部意义。
func TestIMGatewayCannotLogin(t *testing.T) {
	e := newPhase2Env(t)
	enableIMFor(t, e, e.app.ID)
	im := newIMClient(t, e)

	_, err := im.Auth().Login(fpsdk.WithAppID(context.Background(), e.appID), fpsdk.LoginInput{
		ConnectorType: "password",
		Credentials:   map[string]string{"username": "x", "password": "y"},
	})
	if !errors.Is(err, fpsdk.ErrUnauthorized) {
		t.Fatalf("IM 凭据必须不能 Login，got %v", err)
	}
}

// TestIMGatewayConfigAndCredential 走网关那两个新 RPC。
func TestIMGatewayConfigAndCredential(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	im := newIMClient(t, e)

	// 还没打开 IM：两个 RPC 都要给出可区分的"该应用不能接入"。fp-im 靠这个
	// 区分给关闭码 4002 而不是 4001。
	if _, err := im.IMGateway().GetAppIMConfig(ctx, e.appID); !errors.Is(err, fpsdk.ErrIMNotAvailable) {
		t.Fatalf("未启用 IM 时 GetAppIMConfig 应返回 ErrIMNotAvailable，got %v", err)
	}
	if err := im.IMGateway().VerifyAppCredential(ctx, e.appID, e.appSecret); !errors.Is(err, fpsdk.ErrIMNotAvailable) {
		t.Fatalf("未启用 IM 时 VerifyAppCredential 应返回 ErrIMNotAvailable，got %v", err)
	}

	enableIMFor(t, e, e.app.ID)

	cfg, err := im.IMGateway().GetAppIMConfig(ctx, e.appID)
	if err != nil {
		t.Fatalf("GetAppIMConfig: %v", err)
	}
	if !cfg.AllowGuest || cfg.GuestIPRate != 25 || cfg.ConnPolicy != domain.IMConnPolicyReplace {
		t.Fatalf("配置没传对：%+v", cfg)
	}
	if err := im.IMGateway().VerifyAppCredential(ctx, e.appID, e.appSecret); err != nil {
		t.Fatalf("合法凭据被拒：%v", err)
	}
	if err := im.IMGateway().VerifyAppCredential(ctx, e.appID, "wrong"); !errors.Is(err, fpsdk.ErrUnauthorized) {
		t.Fatalf("错误凭据应返回 ErrUnauthorized，got %v", err)
	}
}

// TestNormalSDKUnaffected 钉住"新版 fp 发布后对现网零影响"。
//
// 不带 CallerType 的客户端，登录与校验两条路径与改动前完全一致，而且这个
// 应用的 im_enabled 是**关着**的——那正是现网所有应用在新版 fp 发布后的
// 默认状态。
func TestNormalSDKUnaffected(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()

	token, _, _ := e.loginWithPhone(t, "13800000003")
	id, err := e.sdk.Auth().Validate(ctx, token)
	if err != nil {
		t.Fatalf("普通 SDK 校验失败：%v", err)
	}
	if id.UserID == "" {
		t.Fatal("普通 SDK 没解析出 user_id")
	}
	// 网关接口对普通凭据关闭。
	if _, err := e.sdk.IMGateway().GetAppIMConfig(ctx, e.appID); !errors.Is(err, fpsdk.ErrUnauthorized) {
		t.Fatalf("普通凭据调网关接口应被拒，got %v", err)
	}
}
