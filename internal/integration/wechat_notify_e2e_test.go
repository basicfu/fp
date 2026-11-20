package integration_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
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

// TestWechatNotifyEndToEnd 走完整链路：SDK 要二维码 → 服务号推送扫码事件 → SDK 查到已开启 →
// SDK 发通知、fp 换成 openid 交给（假）微信 → SDK 关掉通知后再发拿到 NOTIFY_RECIPIENT_UNREACHABLE。
// 守的是 gRPC 接线与 SDK 认得服务端错误码（两边各写一份字符串）。
func TestWechatNotifyEndToEnd(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	ctx := context.Background()

	var (
		mu     sync.Mutex
		scenes []string
		sentTo []string
	)
	mp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			_, _ = io.WriteString(w, `{"access_token":"at","expires_in":7200}`)
		case "/cgi-bin/qrcode/create":
			scenes = append(scenes, body["action_info"].(map[string]any)["scene"].(map[string]any)["scene_str"].(string))
			_, _ = io.WriteString(w, `{"ticket":"tk","expire_seconds":600,"url":"http://weixin.qq.com/q/x"}`)
		case "/cgi-bin/message/template/send":
			sentTo = append(sentTo, body["touser"].(string))
			_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(mp.Close)

	users := service.NewUserService(pool)
	creg := connector.NewRegistry()
	if err := creg.Register(connector.NewPassword(users)); err != nil {
		t.Fatal(err)
	}
	configPub := store.NewConfigPublisher(rdb)
	apps := service.NewApplicationService(pool, creg, service.WithIMConfigPublisher(configPub))
	app, secret, err := apps.Create(ctx, "wxmp-e2e-"+uuid.NewString(), "wm-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	user, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypePhone, Subject: "13900000001"})
	if err != nil {
		t.Fatal(err)
	}

	nreg := notify.NewRegistry()
	if err := nreg.Register(notify.WechatMPSpec(mp.URL)); err != nil {
		t.Fatal(err)
	}
	wechat := service.NewWechatMPService(pool, rdb, service.WithWechatMPAPIBase(mp.URL))
	notifySvc := service.NewNotifyService(pool, rdb, nreg, service.WithNotifyRecipients(domain.NotifyChannelWechatMP, wechat.ResolveRecipient))

	srv := grpcapi.New(grpcapi.Deps{
		Apps: apps, Pub: store.NewRevokePublisher(rdb), Configs: service.NewConfigService(pool, configPub),
		ConfigPub: configPub, IMCreds: service.NewIMCredentialService(pool), Notify: notifySvc, WechatMP: wechat,
	})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	n := client.Notify()

	wantCode := func(t *testing.T, err error, code string) {
		t.Helper()
		var fe *fpsdk.Error
		if !errors.As(err, &fe) || fe.Code != code {
			t.Fatalf("err = %v, want 码 %s", err, code)
		}
	}

	// 还没配服务号：整体不可用，业务方据此隐藏入口。
	_, err = n.WechatSubscribeQR(ctx, user.ID.String())
	wantCode(t, err, fpsdk.CodeWechatMPUnavailable)

	prov, err := notifySvc.CreateProvider(ctx, service.CreateNotifyProviderInput{
		Type: notify.TypeWechatMP, Enabled: true, Config: map[string]any{"appId": "wxmp", "appSecret": "s", "token": "tk"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := notifySvc.CreateTemplate(ctx, service.CreateNotifyTemplateInput{
		Code: "device_connected", Channel: domain.NotifyChannelWechatMP, Mode: domain.NotifyModeVendor, Enabled: true,
		Content: domain.NotifyContent{Content: "{{thing1.DATA}}", Variables: []string{"thing1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := notifySvc.SetTemplateProvider(ctx, "device_connected", prov.ID, service.SetNotifyLinkInput{ProviderTemplateID: "TPL", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	qr, err := n.WechatSubscribeQR(ctx, user.ID.String())
	if err != nil || !strings.HasPrefix(qr.ImageURL, "https://mp.weixin.qq.com/") || qr.ExpiresIn != 10*time.Minute {
		t.Fatalf("qr = %+v, err = %v", qr, err)
	}
	if sub, err := n.WechatSubscription(ctx, user.ID.String()); err != nil || sub.Receivable() {
		t.Fatalf("扫码前 sub = %+v, err = %v", sub, err)
	}

	// 微信推扫码事件（明文模式）。
	parts := []string{"tk", "1", "n"}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	mu.Lock()
	scene := scenes[len(scenes)-1]
	mu.Unlock()
	body := fmt.Sprintf(`<xml><ToUserName>gh_1</ToUserName><FromUserName>o-e2e</FromUserName><CreateTime>100</CreateTime>`+
		`<MsgType>event</MsgType><Event>SCAN</Event><EventKey>%s</EventKey></xml>`, scene)
	wechat.HandlePush(ctx, url.Values{"signature": {hex.EncodeToString(sum[:])}, "timestamp": {"1"}, "nonce": {"n"}}, []byte(body))

	sub, err := n.WechatSubscription(ctx, user.ID.String())
	if err != nil || !sub.Receivable() || sub.BoundAt.IsZero() {
		t.Fatalf("扫码后 sub = %+v, err = %v", sub, err)
	}
	if err := n.Send(ctx, "device_connected", user.ID.String(), map[string]string{"thing1": "办公室电脑"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), sentTo...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "o-e2e" {
		t.Fatalf("sentTo = %v", got)
	}

	if err := n.DeleteWechatSubscription(ctx, user.ID.String()); err != nil {
		t.Fatal(err)
	}
	err = n.Send(ctx, "device_connected", user.ID.String(), map[string]string{"thing1": "x"})
	wantCode(t, err, fpsdk.CodeNotifyRecipientUnreachable)
}
