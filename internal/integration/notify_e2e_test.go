package integration_test

import (
	"context"
	"errors"
	"net"
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

// notifyE2ERecorder 是注册进通知服务的假供应商：记录投递次数；gate 非 nil 时卡在里面直到它被关闭。
type notifyE2ERecorder struct {
	mu      sync.Mutex
	calls   int
	gate    chan struct{}
	entered chan struct{}
}

func (r *notifyE2ERecorder) Send(_ context.Context, _ notify.Delivery) error {
	r.mu.Lock()
	r.calls++
	gate, entered := r.gate, r.entered
	r.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		<-gate
	}
	return nil
}

func (r *notifyE2ERecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// TestNotifySendEndToEnd 走完整链路：SDK → 真实监听的 gRPC 服务 → NotifyService → 供应商。
//
// 各层的单元测试已经分别覆盖了自己的行为；这条守的是层与层之间的约定——
// 凭据随每个 RPC 带上、幂等键一路透传、SDK 认得服务端的 NOTIFY_IN_PROGRESS 码
// （SDK 不能 import internal，这个码在两边是各写一份的字符串）。
func TestNotifySendEndToEnd(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	ctx := context.Background()

	users := service.NewUserService(pool)
	creg := connector.NewRegistry()
	if err := creg.Register(connector.NewPassword(users)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	configPub := store.NewConfigPublisher(rdb)
	apps := service.NewApplicationService(pool, creg, service.WithIMConfigPublisher(configPub))
	app, secret, err := apps.Create(ctx, "notify-e2e-"+uuid.NewString(), "ne-"+uuid.NewString())
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}

	rec := &notifyE2ERecorder{}
	nreg := notify.NewRegistry()
	if err := nreg.Register(notify.TypeSpec{
		Type: "recorder", Channel: domain.NotifyChannelSMS,
		New: func(notify.Config) (notify.Provider, error) { return rec, nil },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	notifySvc := service.NewNotifyService(pool, rdb, nreg)
	prov, err := notifySvc.CreateProvider(ctx, service.CreateNotifyProviderInput{Type: "recorder", Enabled: true, Config: map[string]any{}})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if _, err := notifySvc.CreateTemplate(ctx, service.CreateNotifyTemplateInput{
		Code: "login_sms", Channel: domain.NotifyChannelSMS, Mode: domain.NotifyModeVendor, Enabled: true,
		Content: domain.NotifyContent{Content: "验证码 ${code}", Variables: []string{"code"}},
	}); err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if err := notifySvc.SetTemplateProvider(ctx, "login_sms", prov.ID, service.SetNotifyLinkInput{ProviderTemplateID: "SMS_1", Enabled: true}); err != nil {
		t.Fatalf("SetTemplateProvider: %v", err)
	}

	srv := grpcapi.New(grpcapi.Deps{
		Apps: apps, Pub: store.NewRevokePublisher(rdb), Configs: service.NewConfigService(pool, configPub),
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
	params := map[string]string{"code": "123456"}

	t.Run("发送成功，记录里带调用方应用", func(t *testing.T) {
		if err := client.Notify().Send(ctx, "login_sms", "13800138000", params); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if rec.count() != 1 {
			t.Fatalf("供应商调用数 = %d, want 1", rec.count())
		}
		logs, _, err := notifySvc.ListLogs(ctx, service.NotifyLogFilter{Code: "login_sms"})
		if err != nil || len(logs) != 1 || logs[0].AppID != app.AppID {
			t.Fatalf("logs = %+v, err = %v", logs, err)
		}
	})

	t.Run("相同幂等键只发一次", func(t *testing.T) {
		before := rec.count()
		for range 2 {
			if err := client.Notify().Send(ctx, "login_sms", "13800138000", params, fpsdk.WithIdempotencyKey("evt-1")); err != nil {
				t.Fatalf("Send: %v", err)
			}
		}
		if got := rec.count() - before; got != 1 {
			t.Fatalf("相同幂等键的投递数 = %d, want 1", got)
		}
	})

	t.Run("业务错误带着码回到调用方", func(t *testing.T) {
		err := client.Notify().Send(ctx, "nope", "13800138000", params)
		var fe *fpsdk.Error
		if !errors.As(err, &fe) || fe.Code != domain.CodeNotifyTemplateNotFound {
			t.Fatalf("err = %v, want 码 %s", err, domain.CodeNotifyTemplateNotFound)
		}
	})

	// 同键的第一个请求还卡在供应商里：SDK 收到 NOTIFY_IN_PROGRESS 应当退避重试，
	// 等第一个完成后拿到"已完成"，而不是把错误抛给调用方、更不能再发一遍。
	t.Run("同键处理中：SDK 退避重试，最终不重复发送", func(t *testing.T) {
		gate := make(chan struct{})
		entered := make(chan struct{}, 1)
		rec.mu.Lock()
		rec.gate, rec.entered = gate, entered
		rec.mu.Unlock()
		before := rec.count()

		first := make(chan error, 1)
		go func() {
			// 带上与 SDK 相同的应用：幂等键按应用隔离，不同应用的同名键互不相干。
			first <- notifySvc.Send(ctx, service.NotifySendInput{
				AppID: app.AppID, Code: "login_sms", To: "13800138000", Params: params, IdempotencyKey: "evt-2",
			})
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("第一个请求没有进入供应商")
		}
		time.AfterFunc(100*time.Millisecond, func() { close(gate) })

		if err := client.Notify().Send(ctx, "login_sms", "13800138000", params, fpsdk.WithIdempotencyKey("evt-2")); err != nil {
			t.Fatalf("SDK 重试后应成功: %v", err)
		}
		if err := <-first; err != nil {
			t.Fatalf("第一个请求: %v", err)
		}
		if got := rec.count() - before; got != 1 {
			t.Fatalf("同键投递数 = %d, want 1", got)
		}
	})
}
