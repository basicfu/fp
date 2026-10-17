package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/grpcapi"
	"github.com/basicfu/fp/internal/httpapi"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
	fpsdk "github.com/basicfu/fp/sdk"
)

// TestWechatLoginEndToEnd 走完整链路：业务后端挂 OAuthHandler → SDK → gRPC → fp →（假）微信，
// 浏览器打 fp 的 /oauth/callback，再回业务后端换票据、绑手机。守的是各层之间的约定：
// 回调带回的 fpTicket、OAuthHandler 的 JSON 契约、错误码到 HTTP 状态码。
func TestWechatLoginEndToEnd(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	ctx := context.Background()
	wx := testsupport.NewFakeWechat(t)

	users := service.NewUserService(pool)
	codes := service.NewLoginCodeService(rdb, service.WithLoginCodeRateRules())
	creg := connector.NewRegistry()
	for _, c := range []connector.Connector{connector.NewSMSCode(codes), connector.NewWechatWeb(connector.WithWechatAPIBase(wx.URL))} {
		if err := creg.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	configPub := store.NewConfigPublisher(rdb)
	revokePub := store.NewRevokePublisher(rdb)
	apps := service.NewApplicationService(pool, creg, service.WithIMConfigPublisher(configPub))
	app, secret, err := apps.Create(ctx, "wx-e2e-"+uuid.NewString(), "wx-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}

	// 业务后端：网页回跳页就是它自己的 /login/wechat。
	var bizMux http.ServeMux
	biz := httptest.NewServer(&bizMux)
	t.Cleanup(biz.Close)
	returnURL := biz.URL + "/login/wechat"
	if err := apps.SetConnector(ctx, app.ID, connector.TypeWechatWeb, true, map[string]any{
		"appId": "wxapp", "appSecret": "s3cret", "returnUrls": returnURL, "desktopPoll": true,
	}); err != nil {
		t.Fatal(err)
	}

	sms := &testsupport.FakeLoginSMS{}
	auth := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users,
		Sessions: service.NewSessionService(store.NewSessionStore(rdb), revokePub, store.NewEpochStore(rdb)),
		Logs:     service.NewLoginLogService(pool),
		Registry: creg, Codes: codes, Notifier: sms,
		OAuth: service.NewOAuthStore(rdb), PublicURL: "https://fp.example.com",
	})
	fpHTTP := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{Auth: auth}))
	t.Cleanup(fpHTTP.Close)

	srv := grpcapi.New(grpcapi.Deps{
		Auth: auth, Apps: apps, Pub: revokePub, Configs: service.NewConfigService(pool, configPub),
		ConfigPub: configPub, IMCreds: service.NewIMCredentialService(pool),
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
	bizMux.Handle("/auth/wechat/", client.Auth().OAuthHandler(fpsdk.OAuthHandlerOptions{ReturnURL: returnURL}))

	post := func(t *testing.T, op string, body any) (*http.Response, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		resp, err := http.Post(biz.URL+"/auth/wechat/"+op, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}
	get := func(t *testing.T, path string) map[string]any {
		t.Helper()
		resp, err := http.Get(biz.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	// callback 模拟微信把浏览器带回 fp，返回 fp 给出的 302 地址（没有 302 时为空）。
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	callback := func(t *testing.T, authURL, code string) string {
		t.Helper()
		u, err := url.Parse(authURL)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := noFollow.Get(fpHTTP.URL + "/oauth/callback?code=" + url.QueryEscape(code) + "&state=" + u.Query().Get("state"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.Header.Get("Location")
	}

	const phone = "13800138000"

	t.Run("网页端：首次扫码绑手机", func(t *testing.T) {
		_, st := post(t, "start", map[string]string{"mode": "web"})
		loc := callback(t, st["authUrl"].(string), "o1:u1:小明")
		lu, _ := url.Parse(loc)
		if lu == nil || lu.Path != "/login/wechat" || lu.Query().Get("fpTicket") == "" {
			t.Fatalf("回跳 = %q", loc)
		}

		_, out := post(t, "exchange", map[string]string{"ticket": lu.Query().Get("fpTicket")})
		if out["status"] != fpsdk.OAuthBindPhone || out["nickname"] != "小明" || out["bindTicket"] == "" {
			t.Fatalf("exchange = %v", out)
		}

		if resp, _ := post(t, "send-code", map[string]string{"phone": phone}); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("send-code = %d", resp.StatusCode)
		}
		resp, bad := post(t, "bind", map[string]string{"bindTicket": out["bindTicket"].(string), "phone": phone, "code": "000000x"})
		if resp.StatusCode != http.StatusUnauthorized || bad["code"] != "CODE_INVALID" || bad["msg"] == "" {
			t.Fatalf("错误验证码 = %d %v", resp.StatusCode, bad)
		}

		resp, ok := post(t, "bind", map[string]string{"bindTicket": out["bindTicket"].(string), "phone": phone, "code": sms.LastCode()})
		if resp.StatusCode != http.StatusOK || ok["status"] != fpsdk.OAuthSuccess {
			t.Fatalf("bind = %d %v", resp.StatusCode, ok)
		}
		var cookie *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == fpsdk.DefaultCookieName {
				cookie = c
			}
		}
		if cookie == nil || cookie.Value != ok["token"] || !cookie.HttpOnly {
			t.Fatalf("cookie = %+v", cookie)
		}
		if _, err := client.Auth().Validate(ctx, cookie.Value); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("桌面端轮询：老用户直接登录", func(t *testing.T) {
		_, st := post(t, "start", map[string]string{"mode": "desktop"})
		state := st["state"].(string)
		if out := get(t, "/auth/wechat/poll?state="+state); out["status"] != fpsdk.OAuthPending {
			t.Fatalf("回调前 poll = %v", out)
		}
		if loc := callback(t, st["authUrl"].(string), "o1:u1:小明"); loc != "" {
			t.Fatalf("轮询模式不该 302：%q", loc)
		}
		if out := get(t, "/auth/wechat/poll?state="+state); out["status"] != fpsdk.OAuthSuccess || out["token"] == "" {
			t.Fatalf("回调后 poll = %v", out)
		}
		if out := get(t, "/auth/wechat/poll?state="+state); out["code"] != "OAUTH_STATE_INVALID" {
			t.Fatalf("取走后再 poll = %v", out)
		}
	})

	t.Run("参数错误回 400 和错误码", func(t *testing.T) {
		resp, out := post(t, "start", map[string]string{"mode": "tv"})
		if resp.StatusCode != http.StatusBadRequest || out["code"] != "INVALID_ARGUMENT" {
			t.Fatalf("start = %d %v", resp.StatusCode, out)
		}
	})
}
