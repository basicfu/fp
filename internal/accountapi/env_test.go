package accountapi_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/accountapi"
	"github.com/basicfu/fp/internal/accountapi/captcha"
	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/devicesig"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/objstore"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

const (
	testHost = "api.test.local"
	// deviceHeader 是固定的设备签名头,写死在测试里守住对外约定。
	deviceHeader  = "X-Sign"
	returnURL     = "myapp://wechat-login"
	fakeClientRef = "dev-1"
)

type device struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newDevice(t *testing.T) device {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return device{pub: pub, priv: priv}
}

func (d device) pubHex() string { return hex.EncodeToString(d.pub) }

// header 同客户端:每次请求重签,新 nonce、当前时间。
func (d device) header(t *testing.T, now time.Time) string {
	t.Helper()
	var n [12]byte
	_, _ = rand.Read(n[:])
	tok := devicesig.Token{SignPub: d.pub, HS: []byte("hs"), TS: now.Unix(), Nonce: hex.EncodeToString(n[:])}
	tok.Sig = ed25519.Sign(d.priv, devicesig.SigningInput(tok))
	raw, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// fakeCheck 顶替经 Watch 流的登录校验。
type fakeCheck struct {
	mu    sync.Mutex
	ref   string
	err   error
	calls []string // 收到的客户端 IP
}

func (f *fakeCheck) Ask(_ context.Context, _ *domain.Application, header, ip string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if header == "" {
		return "", errors.New("没带设备头")
	}
	f.calls = append(f.calls, ip)
	return f.ref, f.err
}

// fakeCaptcha:Verify 只认 id "ok","exp" 是过期,其余失败。
type fakeCaptcha struct{ verified atomic.Int64 }

func (*fakeCaptcha) Issue(context.Context) (*captcha.Captcha, error) {
	return &captcha.Captcha{ID: "ok", TileY: 40, W: 600, H: 320, Tile: 88, Pad: 6, Bg: []byte{1}, TilePNG: []byte{2}}, nil
}

func (f *fakeCaptcha) Verify(_ context.Context, id string, _ int, _ [][3]int) error {
	f.verified.Add(1)
	switch id {
	case "ok":
		return nil
	case "exp":
		return captcha.ErrExpired
	}
	return captcha.ErrFailed
}

type fakeClock struct{ ms atomic.Int64 }

func (c *fakeClock) Now() int64              { return c.ms.Load() }
func (c *fakeClock) Advance(d time.Duration) { c.ms.Add(d.Milliseconds()) }

type env struct {
	h        http.Handler
	app      *domain.Application
	apps     *service.ApplicationService
	auth     *service.AuthService
	users    *service.UserService
	sessions *service.SessionService
	wechat   *service.WechatMPService
	sms      *testsupport.FakeLoginSMS
	check    *fakeCheck
	captcha  *fakeCaptcha
	dev      device
	clock    *fakeClock
	objects  *objstore.Memory
	rdbDel   func(key string) error
	db       dbExec
}

type dbExec func(ctx context.Context, sql string, args ...any) error

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	wx := testsupport.NewFakeWechat(t)
	mpSrv := newFakeMP(t)

	objects := objstore.NewMemory()
	users := service.NewUserService(pool, service.WithObjectStore(objects))
	codes := service.NewLoginCodeService(rdb, service.WithLoginCodeRateRules())
	reg := connector.NewRegistry()
	for _, c := range []connector.Connector{
		connector.NewPassword(users), connector.NewSMSCode(codes), connector.NewWechatWeb(connector.WithWechatAPIBase(wx.URL)),
	} {
		if err := reg.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	apps := service.NewApplicationService(pool, reg)
	clk := &fakeClock{}
	clk.ms.Store(time.Now().UnixMilli())
	epochs := store.NewEpochStore(rdb)
	sessions := service.NewSessionServiceWithClock(store.NewSessionStore(rdb), store.NewRevokePublisher(rdb), epochs, clk.Now)
	logs := service.NewLoginLogService(pool)
	sms := &testsupport.FakeLoginSMS{}
	auth := service.NewAuthService(service.AuthDeps{
		Apps: apps, Users: users, Sessions: sessions, Logs: logs, Registry: reg, Codes: codes, Notifier: sms,
		OAuth: service.NewOAuthStore(rdb), PublicURL: "https://id.example.com",
	})
	nreg := notify.NewRegistry()
	if err := nreg.Register(notify.WechatMPSpec(mpSrv.URL)); err != nil {
		t.Fatal(err)
	}
	ns := service.NewNotifyService(pool, rdb, nreg)
	if _, err := ns.CreateProvider(ctx, service.CreateNotifyProviderInput{Type: notify.TypeWechatMP, Enabled: true,
		Config: map[string]any{"appId": "wxmp", "appSecret": "sec", "token": "tk", "followReply": "hi"}}); err != nil {
		t.Fatal(err)
	}
	wechat := service.NewWechatMPService(pool, rdb, service.WithWechatMPAPIBase(mpSrv.URL))

	app, _, err := apps.Create(ctx, "账号接口", "acct-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{connector.TypePassword, connector.TypeSMSCode} {
		if err := apps.SetConnector(ctx, app.ID, typ, true, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := apps.SetConnector(ctx, app.ID, connector.TypeWechatWeb, true,
		map[string]any{"appId": "wxapp", "appSecret": "s3cret", "returnUrls": returnURL}); err != nil {
		t.Fatal(err)
	}
	e := &env{apps: apps, auth: auth, users: users, sessions: sessions, wechat: wechat, sms: sms,
		check: &fakeCheck{ref: fakeClientRef}, captcha: &fakeCaptcha{}, dev: newDevice(t), clock: clk, objects: objects}
	e.db = func(ctx context.Context, sql string, args ...any) error {
		_, err := pool.Exec(ctx, sql, args...)
		return err
	}
	e.rdbDel = func(key string) error { return rdb.Del(ctx, key).Err() }
	e.app = app
	e.setAccount(t, func(c *domain.AccountConfig) {})
	e.h = accountapi.NewRouter(accountapi.Deps{
		Apps: apps, Auth: auth, Users: users, Accounts: service.NewAccountService(users, sessions, epochs, logs),
		WechatMP: wechat, Sessions: sessions, Guard: service.NewDeviceGuard(store.NewNonceStore(rdb)),
		Check: e.check, Captcha: e.captcha, Redis: rdb,
	})
	return e
}

// setAccount 在开了验签的账号配置上改几项。
func (e *env) setAccount(t *testing.T, mod func(*domain.AccountConfig)) {
	t.Helper()
	cfg := domain.AccountConfig{DeviceBinding: true}
	mod(&cfg)
	app, err := e.apps.SetAccount(context.Background(), e.app.ID, []string{testHost}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.app = app
}

// setConnector 改应用某种登录方式的启停与配置。微信的 config 传 nil 时沿用测试默认的那份。
func (e *env) setConnector(t *testing.T, typ string, on bool, cfg map[string]any) {
	t.Helper()
	if cfg == nil && typ == connector.TypeWechatWeb {
		cfg = map[string]any{"appId": "wxapp", "appSecret": "s3cret", "returnUrls": returnURL}
	}
	if err := e.apps.SetConnector(context.Background(), e.app.ID, typ, on, cfg); err != nil {
		t.Fatal(err)
	}
}

type reqOpt func(*http.Request)

func withToken(tok string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

// legacy 不带新接口头,走老格式。
func legacy() reqOpt { return func(r *http.Request) { r.Header.Del(accountapi.APIHeader) } }

func noDevice() reqOpt { return func(r *http.Request) { r.Header.Del(deviceHeader) } }

func withDevice(d device, t *testing.T) reqOpt {
	return func(r *http.Request) { r.Header.Set(deviceHeader, d.header(t, time.Now())) }
}

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

// do 默认是新客户端:带新接口头、带本机设备头。
func (e *env) do(t *testing.T, method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	t.Helper()
	return e.doBytes(t, method, path, []byte(body), opts...)
}

func (e *env) doBytes(t *testing.T, method, path string, body []byte, opts ...reqOpt) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	r.Host = testHost
	r.Header.Set(accountapi.APIHeader, accountapi.APIVersion)
	r.Header.Set(deviceHeader, e.dev.header(t, time.Now()))
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v (%d %q)", err, w.Code, w.Body.String())
	}
	return m
}

func wantErr(t *testing.T, w *httptest.ResponseRecorder, status int, code, msg string) {
	t.Helper()
	m := decode(t, w)
	if w.Code != status || m["code"] != code {
		t.Fatalf("got %d %v, want %d %s (body %s)", w.Code, m["code"], status, code, w.Body.String())
	}
	if s, _ := m["msg"].(string); s == "" {
		t.Fatalf("错误体必须带可展示的文案: %s", w.Body.String())
	}
	if tm, _ := m["time"].(float64); tm <= 0 {
		t.Fatalf("错误体必须带服务端时间: %s", w.Body.String())
	}
	if msg != "" && m["msg"] != msg {
		t.Fatalf("msg = %v, want %s", m["msg"], msg)
	}
}

func wantOK(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	return decode(t, w)
}

func userOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	u, _ := m["user"].(map[string]any)
	if u == nil {
		t.Fatalf("响应里没有 user: %v", m)
	}
	return u
}

var phoneSeq atomic.Int64

func newPhone() string { return fmt.Sprintf("1380000%04d", phoneSeq.Add(1)%10000) }

// newUser 建一个只有手机号的用户。
func (e *env) newUser(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	phone := newPhone()
	u, _, _, err := e.users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{Type: domain.IdentityTypePhone, Subject: phone})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID, phone
}

// issue 直接签一个绑在本机设备上的会话。
func (e *env) issue(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	s, err := e.sessions.Issue(context.Background(), service.IssueInput{UserID: userID, App: e.app,
		DevicePub: e.dev.pubHex(), ClientRef: fakeClientRef})
	if err != nil {
		t.Fatal(err)
	}
	return s.Token
}

// loggedIn 建用户并登录,返回用户、手机号、token。
func (e *env) loggedIn(t *testing.T) (uuid.UUID, string, string) {
	t.Helper()
	id, phone := e.newUser(t)
	return id, phone, e.issue(t, id)
}

// lastCode 是最近一次发出去的验证码。
func (e *env) lastCode(t *testing.T) string {
	t.Helper()
	c := e.sms.LastCode()
	if c == "" {
		t.Fatal("还没发过验证码")
	}
	return c
}

func (e *env) lastTo() string {
	sent := e.sms.Sent()
	if len(sent) == 0 {
		return ""
	}
	return sent[len(sent)-1].To
}

// wechatTicket 走完一次扫码回调,返回 Location 里的票据。code 见 testsupport.NewFakeWechat。
func (e *env) wechatTicket(t *testing.T, state, code string) string {
	t.Helper()
	cb := e.auth.HandleOAuthCallback(context.Background(), state, code)
	u, err := url.Parse(cb.Redirect)
	if err != nil || u.Query().Get("fpTicket") == "" {
		t.Fatalf("回调没给票据: %+v", cb)
	}
	return u.Query().Get("fpTicket")
}

func smsBody(phone, captchaID string) string {
	return `{"purpose":"login","channel":"sms","target":"` + phone + `","captcha_id":"` + captchaID + `","x":300,"track":[[0,0,0],[300,1,500]]}`
}

func newFakeMP(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			_, _ = io.WriteString(w, `{"access_token":"at","expires_in":7200}`)
		case "/cgi-bin/qrcode/create":
			_, _ = io.WriteString(w, `{"ticket":"tk","expire_seconds":600,"url":"http://weixin.qq.com/q/x"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
