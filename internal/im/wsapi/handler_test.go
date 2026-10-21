package wsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fpim/v1"
	"github.com/coder/websocket"
)

// fakeAuth 除了按 token 查表，还要记下最后一次收到的请求，这样测试才能
// 断言原始字节与令牌类型确实被传下来了——纯 map 做不到这一点。
// Verify 由 handler 协程写 last，waitUntil 与随后的断言由测试协程读，
// 中间必须有锁：本仓无 cgo 跑不了 -race，这类跨协程无同步的读写不会被
// 工具抓到，表现为一条偶发的、看起来毫无道理的失败。
type fakeAuth struct {
	byToken map[string]model.Subject
	errs    map[string]error // 按 token 指定的失败，没列出的未知 token 一律 ErrUnauthorized
	mu      sync.Mutex
	last    auth.VerifyRequest
}

func (a *fakeAuth) Verify(_ context.Context, req auth.VerifyRequest) (model.Subject, error) {
	a.mu.Lock()
	a.last = req
	a.mu.Unlock()
	if s, ok := a.byToken[req.Token]; ok {
		return s, nil
	}
	if err, ok := a.errs[req.Token]; ok {
		return model.Subject{}, err
	}
	return model.Subject{}, auth.ErrUnauthorized
}

// lastSnapshot 加锁返回 last 的快照，供测试协程安全读取。
func (a *fakeAuth) lastSnapshot() auth.VerifyRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

type fakeHandshaker struct {
	result  registry.HandshakeResult
	err     error
	removed []string
}

func (f *fakeHandshaker) Handshake(context.Context, string, string, string, model.ConnMeta, model.Policy, int, []string) (registry.HandshakeResult, error) {
	return f.result, f.err
}
func (f *fakeHandshaker) Remove(_ context.Context, _, _, connID string) error {
	f.removed = append(f.removed, connID)
	return nil
}

type fakeLiveness struct{}

func (fakeLiveness) NodeID() string      { return "im-a" }
func (fakeLiveness) LiveNodes() []string { return []string{"im-a"} }

type env struct {
	srv    *httptest.Server
	h      *hub.Hub
	stream *hubtest.Stream
	hs     *fakeHandshaker
	apps   hubtest.Apps
	auth   *fakeAuth // 让测试能断言认证器收到了什么（原始字节、令牌类型）
}

// TestNewFillsConfigDefaults 钉住 New 对零值 Config 的兜底：AuthTimeout/
// IdleTimeout/SendQueue/MaxFrame 任何一项没填，都应该落到设计约定的默认值，
// 而不是让零值直接生效——零值会把网关跑废（见 handler.go 里 New 的注释），
// 这条约束不该只靠调用方（本包自己的测试、以及未来 cmd 里的接线代码）
// 记得手填来保证。
func TestNewFillsConfigDefaults(t *testing.T) {
	h := New(Deps{})
	s, ok := h.(*server)
	if !ok {
		t.Fatalf("New 应返回 *server，实际 %T", h)
	}
	if s.Cfg.AuthTimeout != 5*time.Second {
		t.Fatalf("AuthTimeout 默认值应为 5s，实际 %v", s.Cfg.AuthTimeout)
	}
	if s.Cfg.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout 默认值应为 60s，实际 %v", s.Cfg.IdleTimeout)
	}
	if s.Cfg.SendQueue != 256 {
		t.Fatalf("SendQueue 默认值应为 256，实际 %d", s.Cfg.SendQueue)
	}
	if s.Cfg.MaxFrame != 1<<20 {
		t.Fatalf("MaxFrame 默认值应为 1<<20，实际 %d", s.Cfg.MaxFrame)
	}
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	h, _, _, _, apps := hubtest.NewHub()
	stream := &hubtest.Stream{}
	h.AddStream(context.Background(), "a1", stream)
	hs := &fakeHandshaker{}
	if cfg.AuthTimeout == 0 {
		cfg.AuthTimeout = 2 * time.Second
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 10 * time.Second
	}
	if cfg.SendQueue == 0 {
		cfg.SendQueue = 8
	}
	fa := &fakeAuth{
		byToken: map[string]model.Subject{"tok-1": model.User("1")},
		errs: map[string]error{
			"biz-rejected": &auth.RejectedError{Code: "device_banned", Result: []byte(bizRejectBody)},
			"down":         auth.ErrUnavailable,
		},
	}
	handler := New(Deps{Hub: h, Conns: hs, Live: fakeLiveness{}, Auth: fa, Apps: apps, Guests: NewGuestLimiter(), Cfg: cfg})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &env{srv: srv, h: h, stream: stream, hs: hs, apps: apps, auth: fa}
}

func (e *env) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, c *websocket.Conn) (model.Frame, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		return model.Frame{}, err
	}
	var f model.Frame
	_ = json.Unmarshal(b, &f)
	return f, nil
}

// bizRejectBody 模拟业务方的拒绝响应，故意带一个网关不认识的字段：
// 转发必须原样，不能只挑 Result 认识的几个字段重新序列化。
const bizRejectBody = `{"code":"device_banned","msg":"设备已被封禁","detail":"d-1","time":1700000000000,"extra":{"until":1}}`

// expectReject 断言握手被拒：先收到且只收到一帧 t=2（p.code 为 wantCode），
// 随后连接以 wantClose 关闭。返回 p 的原始字节供进一步断言。
func expectReject(t *testing.T, c *websocket.Conn, wantCode string, wantClose int) []byte {
	t.Helper()
	f, err := read(t, c)
	if err != nil {
		t.Fatalf("关闭前应先收到鉴权结果帧，实际直接 %v", err)
	}
	var r model.Result
	if f.T != model.FrameAuthResult || json.Unmarshal(f.P, &r) != nil || r.Code != wantCode {
		t.Fatalf("鉴权结果帧不对：期望 t=%d code=%s，实际 %+v p=%s", model.FrameAuthResult, wantCode, f, f.P)
	}
	if f.Conn != "" {
		t.Fatalf("拒绝帧不该带 conn，实际 %q", f.Conn)
	}
	if _, err := read(t, c); websocket.CloseStatus(err) != websocket.StatusCode(wantClose) {
		t.Fatalf("结果帧之后应以 %d 关闭，实际 %v", wantClose, err)
	}
	return f.P
}

// expectSuccess 断言握手成功：第一帧是 t=2、code=success、带 conn 与 time。
func expectSuccess(t *testing.T, c *websocket.Conn) model.Frame {
	t.Helper()
	f, err := read(t, c)
	var r model.Result
	if err != nil || f.T != model.FrameAuthResult || f.Conn == "" || json.Unmarshal(f.P, &r) != nil || r.Code != model.CodeSuccess || r.Time == 0 {
		t.Fatalf("应收到成功的鉴权结果帧，实际 %+v p=%s err=%v", f, f.P, err)
	}
	return f
}

func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHandshakeWithTokenThenMessageAndPing(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": "tok-1", "ua": "Mozilla/5.0 (iPhone)"})
	res := expectSuccess(t, c)
	waitUntil(t, func() bool { return len(e.stream.GotSnapshot()) >= 1 }, "server 流应收到 Connected")
	ev := e.stream.GotSnapshot()[0].GetEvent()
	if ev == nil || ev.Kind != fpimv1.EventKind_EVENT_KIND_CONNECTED || ev.Subject != "u:1" || ev.Os != "ios" || !ev.Mobile || ev.Ua == "" {
		t.Fatalf("Connected 事件不对：%+v", ev)
	}

	send(t, c, map[string]any{"t": model.FrameMsg, "p": map[string]any{"x": 1}})
	waitUntil(t, func() bool { return len(e.stream.GotSnapshot()) >= 2 }, "server 流应收到 Inbound")
	in := e.stream.GotSnapshot()[1].GetInbound()
	if in == nil || in.Subject != "u:1" || in.ConnId != res.Conn || string(in.Payload) != `{"x":1}` {
		t.Fatalf("Inbound 不对：%+v", in)
	}

	if err := c.Write(context.Background(), websocket.MessageText, []byte(`{"t":4}`)); err != nil {
		t.Fatal(err)
	}
	_, b, err := c.Read(context.Background())
	if err != nil || string(b) != `{"t":5}` {
		t.Fatalf("ping 应回 {\"t\":5}，实际 %s %v", b, err)
	}
}

// TestHandshakePassesRawFrameAndKind 钉住 Task 5 的核心行为：握手帧的原始
// 字节要逐字节转发给认证器，令牌类型也要一并传下去。business 验证器需要
// 把原始字节原样 POST 给业务方，解析后再序列化会丢掉 client 塞的未知字段
// （这里的 custom.deviceId），所以断言必须比对整条原始 JSON 字符串，而不是
// 只比对几个已知字段。
func TestHandshakePassesRawFrameAndKind(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	// 帧里故意带一个网关不认识的字段，它必须原样出现在 Raw 里
	raw := `{"t":1,"app":"a1","token":"tok-1","kind":"biz","custom":{"deviceId":"d-1"}}`
	if err := c.Write(context.Background(), websocket.MessageText, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return e.auth.lastSnapshot().Token != "" }, "认证器应当被调用")

	last := e.auth.lastSnapshot()
	if last.Kind != "biz" {
		t.Fatalf("令牌类型没传下去，实际 %q", last.Kind)
	}
	if string(last.Raw) != raw {
		t.Fatalf("原始帧字节不是逐字节转发的：\n收到 %s\n期望 %s", last.Raw, raw)
	}
	if last.App != "a1" {
		t.Fatalf("app 没传下去，实际 %q", last.App)
	}
}

// TestHandshakeDefaultsKindToEmpty 钉住向后兼容：老客户端的握手帧不带
// kind 字段，Kind 必须是空串（由 multiauth 当成 fp 处理），不能因为这次
// 改动而要求老客户端多传一个字段。
func TestHandshakeDefaultsKindToEmpty(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	// 不带 kind 的老客户端，Kind 应当是空串，由 multiauth 当成 fp 处理
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": "tok-1"})
	expectSuccess(t, c)
	if kind := e.auth.lastSnapshot().Kind; kind != "" {
		t.Fatalf("缺省的令牌类型应当是空串，实际 %q——老客户端一行不用改是这次改动的前提", kind)
	}
}

func TestNoAuthFrameRejectsBadRequest(t *testing.T) {
	e := newEnv(t, Config{AuthTimeout: 200 * time.Millisecond})
	c := e.dial(t)
	defer c.CloseNow()
	expectReject(t, c, model.CodeBadRequest, model.CloseAuthFailed)
}

func TestBadTokenCloses4001AndGuestRules(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": "bad"})
	expectReject(t, c, model.CodeAuthFailed, model.CloseAuthFailed)
	c.CloseNow()

	// a1 不允许访客
	c = e.dial(t)
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "guest": "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"})
	expectReject(t, c, model.CodeAuthFailed, model.CloseAuthFailed)
	c.CloseNow()

	e.apps["a1"] = model.AppConfig{AppID: "a1", ConnPolicy: model.PolicyReplace, AllowGuest: true, GuestIPRate: 20}
	c = e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "guest": "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"})
	expectSuccess(t, c)
	waitUntil(t, func() bool {
		got := e.stream.GotSnapshot()
		return len(got) >= 1 && got[0].GetEvent().Subject == "g:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"
	}, "访客的 Connected 应带 g: 前缀")
}

// TestGuestNonUUIDCloses4001 覆盖简报没有显式测到的一条：访客 id 就算
// app 开了访客，也必须是标准写法的 uuid v4，否则前端拼一个可预测的字符串
// 就能冒充别人（这条约束本身在 model.ParseSubject 的注释里也提到过）。
func TestGuestNonUUIDCloses4001(t *testing.T) {
	e := newEnv(t, Config{})
	e.apps["a1"] = model.AppConfig{AppID: "a1", ConnPolicy: model.PolicyReplace, AllowGuest: true, GuestIPRate: 20}
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "guest": "not-a-uuid"})
	expectReject(t, c, model.CodeAuthFailed, model.CloseAuthFailed)
}

func TestPolicyRejectSendsConnLimited(t *testing.T) {
	e := newEnv(t, Config{})
	e.hs.result = registry.HandshakeResult{Rejected: true}
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": "tok-1"})
	expectReject(t, c, model.CodeConnLimited, model.ClosePolicyRejected)
}

func TestKickCloses4003AndEmitsDisconnected(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": "tok-1"})
	res := expectSuccess(t, c)
	e.h.Evict(context.Background(), "a1", model.User("1"), []registry.ConnRef{{ConnID: res.Conn, Node: "im-a"}}, model.ReasonReplaced)
	if _, err := read(t, c); websocket.CloseStatus(err) != model.CloseKicked {
		t.Fatalf("被顶替应 4003，实际 %v", err)
	}
	waitUntil(t, func() bool {
		for _, g := range e.stream.GotSnapshot() {
			if ev := g.GetEvent(); ev != nil && ev.Kind == fpimv1.EventKind_EVENT_KIND_DISCONNECTED {
				return ev.Reason == model.ReasonReplaced
			}
		}
		return false
	}, "应发 Disconnected(replaced)")
	if len(e.hs.removed) != 1 || e.hs.removed[0] != res.Conn {
		t.Fatalf("断开时必须从注册表删自己，实际 %v", e.hs.removed)
	}
}

func TestIdleTimeoutDisconnects(t *testing.T) {
	e := newEnv(t, Config{IdleTimeout: 300 * time.Millisecond})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": "tok-1"})
	expectSuccess(t, c)
	// 断言收紧到具体关闭码，而不是"读会出错"：空闲超时和被踢/策略拒绝
	// 一样，必须让 client 能分辨出这是网关主动清理而不是网络抖断，否则
	// SDK 没法判断该退避还是立即重连。
	if _, err := read(t, c); websocket.CloseStatus(err) != model.CloseIdleTimeout {
		t.Fatalf("空闲超时应以 4005 关闭，实际 %v", err)
	}
	waitUntil(t, func() bool {
		for _, g := range e.stream.GotSnapshot() {
			if ev := g.GetEvent(); ev != nil && ev.Kind == fpimv1.EventKind_EVENT_KIND_DISCONNECTED {
				return ev.Reason == model.ReasonTimeout
			}
		}
		return false
	}, "应发 Disconnected(timeout)")
}

// TestClientIPUsesRightmostForwardedHop 是乙二的回归测试：信任代理时必须
// 取转发头的最右一跳。
//
// 取最左（原实现）的后果：nginx 默认的
// `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for` 是**追加**
// 而不是重写，client 自己发来的那一段会原样留在最左边。于是访客限流
// （设计文档第四节 4.1 称之为"im 对访客的唯一防线"）在标准部署下可以被
// 一个请求头绕过：每次握手换一个伪造值，限流桶就永远命不中同一个。
func TestClientIPUsesRightmostForwardedHop(t *testing.T) {
	cases := []struct {
		name       string
		xff        []string
		trustProxy bool
		want       string
	}{
		{
			// 这条是绕过限流的实际场景：client 自己带了 1.2.3.4，nginx 把
			// 真实来源 203.0.113.9 追加在右边。
			name: "客户端伪造的最左跳不能被采信", xff: []string{"1.2.3.4, 203.0.113.9"},
			trustProxy: true, want: "203.0.113.9",
		},
		{name: "单跳", xff: []string{"203.0.113.9"}, trustProxy: true, want: "203.0.113.9"},
		{
			// 多层代理：最右一跳是内层代理的地址，它后面的 client 共用一个
			// 限流桶——偏严，可接受；偏松（可被绕过）不可接受。
			name: "多层代理取最右", xff: []string{"1.2.3.4, 10.0.0.7, 10.0.0.8"},
			trustProxy: true, want: "10.0.0.8",
		},
		{
			// HTTP 允许同名头出现多次，语义等价于按顺序拼接，所以整体最右
			// 一跳在最后一个头里。用 Header.Get 只能看到第一个头，那恰好是
			// 最不可信的一段。
			name: "同名头出现多次时取最后一个头的最右跳", xff: []string{"1.2.3.4", "10.0.0.7, 10.0.0.8"},
			trustProxy: true, want: "10.0.0.8",
		},
		{name: "尾部空白项跳过", xff: []string{"10.0.0.8,  "}, trustProxy: true, want: "10.0.0.8"},
		{name: "全是空白等于没有转发头", xff: []string{"  "}, trustProxy: true, want: "192.0.2.5"},
		{name: "不信任代理时忽略转发头", xff: []string{"1.2.3.4, 203.0.113.9"}, trustProxy: false, want: "192.0.2.5"},
		{name: "没有转发头", xff: nil, trustProxy: true, want: "192.0.2.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/ws", nil)
			r.RemoteAddr = "192.0.2.5:44444"
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := clientIP(r, c.trustProxy); got != c.want {
				t.Fatalf("clientIP=%q，期望 %q：访客限流的键必须取 client 改不了的那一跳", got, c.want)
			}
		})
	}
}

// TestBizRejectionForwardedVerbatim 钉住业务方拒绝的转发契约：p 必须是业务方
// 响应体的原样字节（含网关不认识的字段），随后 4001。
func TestBizRejectionForwardedVerbatim(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": "biz-rejected", "kind": "biz"})
	if p := expectReject(t, c, "device_banned", model.CloseAuthFailed); string(p) != bizRejectBody {
		t.Fatalf("业务方的 Result 没有原样转发：\n收到 %s\n期望 %s", p, bizRejectBody)
	}
}

func TestAuthUnavailableSends4004(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": "down", "kind": "biz"})
	var r model.Result
	_ = json.Unmarshal(expectReject(t, c, model.CodeAuthUnavailable, model.CloseUnavailable), &r)
	if r.Msg != "服务器授权异常" || r.Time == 0 {
		t.Fatalf("fp-im 自己生成的结果要带文案与毫秒时间，实际 %+v", r)
	}
}

func TestRegistryErrorSendsAuthUnavailable(t *testing.T) {
	e := newEnv(t, Config{})
	e.hs.err = context.DeadlineExceeded
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": "tok-1"})
	expectReject(t, c, model.CodeAuthUnavailable, model.CloseUnavailable)
}

func TestUnknownAppSendsAppUnavailable(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "nope", "token": "tok-1"})
	expectReject(t, c, model.CodeAppUnavailable, model.ClosePolicyRejected)
}

// TestBadFirstFrameSendsBadRequest 覆盖首帧格式不对的几种形态，包括旧协议的
// 字符串帧类型——不兼容旧版本，旧客户端要拿到明确的 bad_request 而不是静默超时。
func TestBadFirstFrameSendsBadRequest(t *testing.T) {
	e := newEnv(t, Config{})
	for _, raw := range []string{
		`{"t":"auth","app":"a1","token":"tok-1"}`,
		`{"t":3,"app":"a1","token":"tok-1"}`,
		`{"t":1,"token":"tok-1"}`,
		`{"t":1,"app":"a1"}`,
		`not json`,
	} {
		t.Run(raw, func(t *testing.T) {
			c := e.dial(t)
			defer c.CloseNow()
			if err := c.Write(context.Background(), websocket.MessageText, []byte(raw)); err != nil {
				t.Fatal(err)
			}
			expectReject(t, c, model.CodeBadRequest, model.CloseAuthFailed)
		})
	}
}

func TestGuestRateLimited(t *testing.T) {
	e := newEnv(t, Config{})
	// GuestIPRate=0 让第一个新访客就超额，见 GuestLimiter.Allow。
	e.apps["a1"] = model.AppConfig{AppID: "a1", ConnPolicy: model.PolicyReplace, AllowGuest: true}
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "guest": "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"})
	expectReject(t, c, model.CodeRateLimited, model.CloseAuthFailed)
}

// TestHandshakePassesClientIP 钉住 client 地址透传给认证器：业务方回调靠它
// 做按 IP 的风控，来源必须与访客限流用的是同一个 clientIP。
func TestHandshakePassesClientIP(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": model.FrameAuth, "app": "a1", "token": "tok-1", "kind": "biz"})
	expectSuccess(t, c)
	if ip := e.auth.lastSnapshot().IP; ip != "127.0.0.1" {
		t.Fatalf("认证器收到的 IP = %q，期望 127.0.0.1", ip)
	}
}

// TestFrameLiteralsMatchConstants 守住 conn.go 里手拼的帧字面量与常量一致。
func TestFrameLiteralsMatchConstants(t *testing.T) {
	var f model.Frame
	if json.Unmarshal(msgFrame([]byte(`{"x":1}`)), &f) != nil || f.T != model.FrameMsg || string(f.P) != `{"x":1}` {
		t.Fatalf("msgFrame 与 FrameMsg 不一致：%+v", f)
	}
	f = model.Frame{}
	if json.Unmarshal(pongFrame, &f) != nil || f.T != model.FramePong {
		t.Fatalf("pongFrame 与 FramePong 不一致：%+v", f)
	}
}
