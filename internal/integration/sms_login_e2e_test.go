package integration_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/grpcapi"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
	fpsdk "github.com/basicfu/fp/sdk"
)

// smsCapture 是注册进通知服务的假短信供应商，留下最近一次投递，测试从里面取回验证码。
type smsCapture struct {
	mu   sync.Mutex
	last notify.Delivery
	n    int
}

func (c *smsCapture) Send(_ context.Context, d notify.Delivery) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last, c.n = d, c.n+1
	return nil
}

func (c *smsCapture) snapshot() (notify.Delivery, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last, c.n
}

// TestSMSCodeLoginEndToEnd 走完整链路：SDK → 真实监听的 gRPC 服务 → AuthService → 通知中心 → 供应商，
// 再用供应商收到的验证码经 SDK 登录。
//
// 单元测试各自守着自己那一层；这条守层与层之间的约定：通知模板 code 是 verify_code、变量是 code，
// 以及错误经 gRPC 到 SDK 之后归在正确的哨兵上——限流是 ErrRateLimited、手机号格式错是 ErrInvalidArgument，
// 通知中心没配好是 ErrUnavailable（WriteError 回 503），不能变成 ErrUnauthorized（业务方的登录页会显示"未授权"）。
func TestSMSCodeLoginEndToEnd(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	ctx := context.Background()

	users := service.NewUserService(pool)
	codes := service.NewLoginCodeService(rdb) // 默认限流：30 秒 1 条
	creg := connector.NewRegistry()
	if err := creg.Register(connector.NewSMSCode(codes)); err != nil {
		t.Fatalf("注册 sms_code: %v", err)
	}
	configPub := store.NewConfigPublisher(rdb)
	revokePub := store.NewRevokePublisher(rdb)
	apps := service.NewApplicationService(pool, creg, service.WithIMConfigPublisher(configPub))
	app, secret, err := apps.Create(ctx, "sms-e2e-"+uuid.NewString(), "se-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	if err := apps.SetConnector(ctx, app.ID, connector.TypeSMSCode, true, nil); err != nil {
		t.Fatalf("启用 sms_code: %v", err)
	}

	sms := &smsCapture{}
	nreg := notify.NewRegistry()
	if err := nreg.Register(notify.TypeSpec{
		Type: "capture", Channel: domain.NotifyChannelSMS,
		New: func(notify.Config) (notify.Provider, error) { return sms, nil },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	notifySvc := service.NewNotifyService(pool, rdb, nreg)

	auth := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users,
		Sessions: service.NewSessionService(store.NewSessionStore(rdb), revokePub, store.NewEpochStore(rdb)),
		Logs:     service.NewLoginLogService(pool),
		Registry: creg, Codes: codes, Notifier: notifySvc,
	})
	srv := grpcapi.New(grpcapi.Deps{
		Auth: auth, Apps: apps, Pub: revokePub, Configs: service.NewConfigService(pool, configPub),
		ConfigPub: configPub, IMCreds: service.NewIMCredentialService(pool), Notify: notifySvc,
	})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	t.Cleanup(cancelRun)
	go func() { _ = srv.ServeWhenReady(runCtx, lis, 5*time.Second) }()
	select {
	case <-srv.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("等待 gRPC 服务就绪超时")
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	})

	client, err := fpsdk.New(fpsdk.Options{Addr: "grpc://" + lis.Addr().String(), AppID: app.AppID, AppSecret: secret})
	if err != nil {
		t.Fatalf("fpsdk.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	const phone = "13800138000"

	// 管理员还没在通知中心建 verify_code 模板：发码失败，但不能被当成鉴权失败。
	err = client.Auth().SendLoginCode(ctx, phone)
	var fe *fpsdk.Error
	if !errors.As(err, &fe) || fe.Code != domain.CodeNotifySendFailed {
		t.Fatalf("没有模板时 err = %v, want 码 %s", err, domain.CodeNotifySendFailed)
	}
	if errors.Is(err, fpsdk.ErrUnauthorized) || !errors.Is(err, fpsdk.ErrUnavailable) {
		t.Fatalf("通知中心没配好应是 ErrUnavailable，不能被当成鉴权失败: %v", err)
	}
	// 业务方通常直接 fpsdk.WriteError(w, err)：这里必须是 503，不是 401。
	rec := httptest.NewRecorder()
	fpsdk.WriteError(rec, err)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("WriteError 状态码 = %d, want 503", rec.Code)
	}

	// 按文档配好：verify_code 模板，变量只有 code，关联一个短信供应商。
	prov, err := notifySvc.CreateProvider(ctx, service.CreateNotifyProviderInput{Type: "capture", Enabled: true, Config: map[string]any{}})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if _, err := notifySvc.CreateTemplate(ctx, service.CreateNotifyTemplateInput{
		Code: service.VerifyCodeTemplate, Channel: domain.NotifyChannelSMS, Mode: domain.NotifyModeVendor, Enabled: true,
		Content: domain.NotifyContent{Content: "验证码 ${code}", Variables: []string{"code"}},
	}); err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if err := notifySvc.SetTemplateProvider(ctx, service.VerifyCodeTemplate, prov.ID,
		service.SetNotifyLinkInput{ProviderTemplateID: "SMS_LOGIN", Enabled: true}); err != nil {
		t.Fatalf("SetTemplateProvider: %v", err)
	}

	// 上面那次失败已经占了这个号码 30 秒窗口里的额度，换一个号码发。
	const phone2 = "13900139000"
	if err := client.Auth().SendLoginCode(ctx, phone2); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	d, n := sms.snapshot()
	if n != 1 || d.To != phone2 || d.ProviderTemplateID != "SMS_LOGIN" || !regexp.MustCompile(`^\d{6}$`).MatchString(d.Params["code"]) {
		t.Fatalf("供应商收到的 = %+v（共 %d 次）", d, n)
	}

	t.Run("错误的验证码登录失败，正确的成功并自动建号", func(t *testing.T) {
		_, err := client.Auth().Login(ctx, fpsdk.LoginInput{
			ConnectorType: "sms_code", Credentials: map[string]string{"phone": phone2, "code": "000000"},
		})
		if !errors.Is(err, fpsdk.ErrUnauthorized) {
			t.Fatalf("错误验证码 err = %v, want ErrUnauthorized", err)
		}
		res, err := client.Auth().Login(ctx, fpsdk.LoginInput{
			ConnectorType: "sms_code", Credentials: map[string]string{"phone": phone2, "code": d.Params["code"]},
		})
		if err != nil || res.Token == "" {
			t.Fatalf("Login: %v", err)
		}
		id, err := client.Auth().Validate(ctx, res.Token)
		if err != nil || id.UserID != res.User.GetId() {
			t.Fatalf("Validate: %+v, %v", id, err)
		}
	})

	t.Run("窗口内再发：ErrRateLimited，且不再发短信", func(t *testing.T) {
		err := client.Auth().SendLoginCode(ctx, phone2)
		if !errors.Is(err, fpsdk.ErrRateLimited) || errors.Is(err, fpsdk.ErrUnauthorized) {
			t.Fatalf("err = %v, want ErrRateLimited", err)
		}
		if _, n := sms.snapshot(); n != 1 {
			t.Fatalf("被限流的请求不该再发短信: 供应商调用数 = %d", n)
		}
	})

	t.Run("手机号格式错：ErrInvalidArgument", func(t *testing.T) {
		err := client.Auth().SendLoginCode(ctx, "12345")
		if !errors.Is(err, fpsdk.ErrInvalidArgument) || errors.Is(err, fpsdk.ErrUnauthorized) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
}
