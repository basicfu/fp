package grpcapi

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// 自带一套精简环境（bufconn + 真实的认证拦截器），只装配通知需要的依赖；
// 不复用 env_test.go 的 grpcEnv，那套会起整个认证服务。

type recordingNotifyProvider struct {
	mu    sync.Mutex
	calls []notify.Delivery
}

func (p *recordingNotifyProvider) Send(_ context.Context, d notify.Delivery) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, d)
	return nil
}

func (p *recordingNotifyProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

type notifyGRPCEnv struct {
	client   fpv1.NotifyServiceClient
	notify   *service.NotifyService
	apps     *service.ApplicationService
	provider *recordingNotifyProvider
	appID    string
	appUUID  uuid.UUID
	secret   string
	imSecret string
}

func newNotifyGRPCEnv(t *testing.T) *notifyGRPCEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	users := service.NewUserService(pool)
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(users)); err != nil {
		t.Fatalf("注册 password: %v", err)
	}
	configPub := store.NewConfigPublisher(rdb)
	apps := service.NewApplicationService(pool, reg, service.WithIMConfigPublisher(configPub))
	imCreds := service.NewIMCredentialService(pool)

	rec := &recordingNotifyProvider{}
	nreg := notify.NewRegistry()
	if err := nreg.Register(notify.TypeSpec{
		Type: "fake_sms", Channel: domain.NotifyChannelSMS,
		ConfigSchema: []domain.Field{{Key: "name", Label: "名称", Type: domain.FieldTypeString, Required: true}},
		New:          func(notify.Config) (notify.Provider, error) { return rec, nil },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	notifySvc := service.NewNotifyService(pool, rdb, nreg)

	app, secret, err := apps.Create(context.Background(), "通知测试-"+uuid.NewString(), "n-"+uuid.NewString())
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	imSecret, err := imCreds.Rotate(context.Background())
	if err != nil {
		t.Fatalf("生成 IM 凭据: %v", err)
	}

	srv := New(Deps{
		Apps: apps, Pub: store.NewRevokePublisher(rdb), Configs: service.NewConfigService(pool, configPub),
		ConfigPub: configPub, IMCreds: imCreds, Notify: notifySvc,
	})
	runCtx, cancelRun := context.WithCancel(context.Background())
	t.Cleanup(cancelRun)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.ServeWhenReady(runCtx, lis, 5*time.Second) }()
	select {
	case <-srv.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("等待 gRPC 服务就绪超时")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	e := &notifyGRPCEnv{
		client: fpv1.NewNotifyServiceClient(conn), notify: notifySvc, apps: apps, provider: rec,
		appID: app.AppID, appUUID: app.ID, secret: secret, imSecret: imSecret,
	}
	p, err := notifySvc.CreateProvider(context.Background(), service.CreateNotifyProviderInput{Type: "fake_sms", Enabled: true, Config: map[string]any{"name": "a"}})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if _, err := notifySvc.CreateTemplate(context.Background(), service.CreateNotifyTemplateInput{
		Code: "login_sms", Channel: domain.NotifyChannelSMS, Mode: domain.NotifyModeVendor, Enabled: true,
		Content: domain.NotifyContent{Content: "验证码 ${code}", Variables: []string{"code"}},
	}); err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if err := notifySvc.SetTemplateProvider(context.Background(), "login_sms", p.ID, service.SetNotifyLinkInput{ProviderTemplateID: "SMS_1", Enabled: true}); err != nil {
		t.Fatalf("SetTemplateProvider: %v", err)
	}
	return e
}

func (e *notifyGRPCEnv) authed(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, mdAppID, e.appID, mdAppSecret, e.secret)
}

func (e *notifyGRPCEnv) imAuthed(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, mdAppID, e.appID, mdAppSecret, e.imSecret, MDCallerType, CallerTypeIM)
}

func errorDetailCode(t *testing.T, err error) string {
	t.Helper()
	st, _ := status.FromError(err)
	for _, d := range st.Details() {
		if ed, ok := d.(*fpv1.ErrorDetail); ok {
			return ed.GetCode()
		}
	}
	return ""
}

func TestNotifySendDeliversAndRecordsCallerApp(t *testing.T) {
	e := newNotifyGRPCEnv(t)
	ctx := e.authed(context.Background())
	if _, err := e.client.Send(ctx, &fpv1.SendRequest{Code: "login_sms", To: "13800138000", Params: map[string]string{"code": "123456"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if e.provider.count() != 1 {
		t.Fatalf("供应商调用数 = %d, want 1", e.provider.count())
	}
	logs, _, err := e.notify.ListLogs(context.Background(), service.NotifyLogFilter{Code: "login_sms"})
	if err != nil || len(logs) != 1 || logs[0].AppID != e.appID || !logs[0].Success {
		t.Fatalf("logs = %+v, err = %v：发送记录里应带调用方应用的 appId", logs, err)
	}
}

func TestNotifySendMapsDomainErrors(t *testing.T) {
	e := newNotifyGRPCEnv(t)
	ctx := e.authed(context.Background())

	cases := []struct {
		name     string
		req      *fpv1.SendRequest
		wantCode codes.Code
		wantErr  string
	}{
		{"模板不存在", &fpv1.SendRequest{Code: "nope"}, codes.NotFound, domain.CodeNotifyTemplateNotFound},
		{"变量不匹配", &fpv1.SendRequest{Code: "login_sms", To: "13800138000"}, codes.InvalidArgument, domain.CodeNotifyParamsInvalid},
		{"缺收件人", &fpv1.SendRequest{Code: "login_sms", Params: map[string]string{"code": "1"}}, codes.InvalidArgument, domain.CodeNotifyRecipientInvalid},
	}
	for _, c := range cases {
		_, err := e.client.Send(ctx, c.req)
		if status.Code(err) != c.wantCode || errorDetailCode(t, err) != c.wantErr {
			t.Errorf("%s: err = %v (detail code %q), want %v / %s", c.name, err, errorDetailCode(t, err), c.wantCode, c.wantErr)
		}
	}
	if e.provider.count() != 0 {
		t.Fatalf("失败的请求不该到达供应商, calls = %d", e.provider.count())
	}
}

func TestNotifySendIdempotencyKeyIsHonoured(t *testing.T) {
	e := newNotifyGRPCEnv(t)
	ctx := e.authed(context.Background())
	req := &fpv1.SendRequest{Code: "login_sms", To: "13800138000", Params: map[string]string{"code": "1"}, IdempotencyKey: "evt-1"}
	for i := 0; i < 2; i++ {
		if _, err := e.client.Send(ctx, req); err != nil {
			t.Fatalf("Send #%d: %v", i+1, err)
		}
	}
	if e.provider.count() != 1 {
		t.Fatalf("相同幂等键只该真正发送一次, calls = %d", e.provider.count())
	}
}

func TestNotifySendAuthBoundaries(t *testing.T) {
	e := newNotifyGRPCEnv(t)
	req := &fpv1.SendRequest{Code: "login_sms", To: "13800138000", Params: map[string]string{"code": "1"}}

	if _, err := e.client.Send(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Errorf("无凭据 err = %v, want Unauthenticated", err)
	}
	// fp-im 网关的凭据是收窄过的子集：不能发通知。
	if _, err := e.client.Send(e.imAuthed(context.Background()), req); status.Code(err) != codes.PermissionDenied {
		t.Errorf("IM 凭据 err = %v, want PermissionDenied", err)
	}
	if e.provider.count() != 0 {
		t.Fatalf("被拒绝的调用不该到达供应商")
	}
}

// 拦截器只校验 appSecret、不看应用状态，停用应用的凭据照样通过认证；
// 能不能发通知只能靠 handler 里的 GetActiveByAppID 拦（与 GetConfig 同一条规矩）。
func TestNotifySendRejectsDisabledApplication(t *testing.T) {
	e := newNotifyGRPCEnv(t)
	if _, err := e.apps.SetStatus(context.Background(), e.appUUID, domain.ApplicationStatusDisabled); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	req := &fpv1.SendRequest{Code: "login_sms", To: "13800138000", Params: map[string]string{"code": "1"}}
	_, err := e.client.Send(e.authed(context.Background()), req)
	if status.Code(err) != codes.PermissionDenied || errorDetailCode(t, err) != domain.CodeAppDisabled {
		t.Fatalf("err = %v (detail code %q), want PermissionDenied / %s", err, errorDetailCode(t, err), domain.CodeAppDisabled)
	}
	if e.provider.count() != 0 {
		t.Fatalf("停用应用的请求不该到达供应商, calls = %d", e.provider.count())
	}
}
