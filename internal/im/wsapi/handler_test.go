package wsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
	"github.com/coder/websocket"
)

// fakeAuth 除了按 token 查表，还要记下最后一次收到的请求，这样测试才能
// 断言原始字节与令牌类型确实被传下来了——纯 map 做不到这一点。
type fakeAuth struct {
	byToken map[string]model.Subject
	last    auth.VerifyRequest
}

func (a *fakeAuth) Verify(_ context.Context, req auth.VerifyRequest) (model.Subject, error) {
	a.last = req
	if s, ok := a.byToken[req.Token]; ok {
		return s, nil
	}
	return model.Subject{}, auth.ErrUnauthorized
}

type fakeHandshaker struct {
	result  registry.HandshakeResult
	removed []string
}

func (f *fakeHandshaker) Handshake(context.Context, string, string, string, model.ConnMeta, model.Policy, int, []string) (registry.HandshakeResult, error) {
	return f.result, nil
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
	fa := &fakeAuth{byToken: map[string]model.Subject{"tok-1": model.User("1")}}
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
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "tok-1", "ua": "Mozilla/5.0 (iPhone)"})
	hello, err := read(t, c)
	if err != nil || hello.T != "hello" || hello.Conn == "" {
		t.Fatalf("应收到 hello 帧，实际 %+v %v", hello, err)
	}
	waitUntil(t, func() bool { return len(e.stream.GotSnapshot()) >= 1 }, "server 流应收到 Connected")
	ev := e.stream.GotSnapshot()[0].GetEvent()
	if ev == nil || ev.Kind != fpimv1.EventKind_EVENT_KIND_CONNECTED || ev.Subject != "u:1" || ev.Os != "ios" || !ev.Mobile || ev.Ua == "" {
		t.Fatalf("Connected 事件不对：%+v", ev)
	}

	send(t, c, map[string]any{"t": "msg", "p": map[string]any{"x": 1}})
	waitUntil(t, func() bool { return len(e.stream.GotSnapshot()) >= 2 }, "server 流应收到 Inbound")
	in := e.stream.GotSnapshot()[1].GetInbound()
	if in == nil || in.Subject != "u:1" || in.ConnId != hello.Conn || string(in.Payload) != `{"x":1}` {
		t.Fatalf("Inbound 不对：%+v", in)
	}

	send(t, c, map[string]any{"t": "ping"})
	if f, _ := read(t, c); f.T != "pong" {
		t.Fatalf("ping 应回 pong，实际 %+v", f)
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
	raw := `{"t":"auth","app":"a1","token":"tok-1","kind":"biz","custom":{"deviceId":"d-1"}}`
	if err := c.Write(context.Background(), websocket.MessageText, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return e.auth.last.Token != "" }, "认证器应当被调用")

	if e.auth.last.Kind != "biz" {
		t.Fatalf("令牌类型没传下去，实际 %q", e.auth.last.Kind)
	}
	if string(e.auth.last.Raw) != raw {
		t.Fatalf("原始帧字节不是逐字节转发的：\n收到 %s\n期望 %s", e.auth.last.Raw, raw)
	}
	if e.auth.last.App != "a1" {
		t.Fatalf("app 没传下去，实际 %q", e.auth.last.App)
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
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "tok-1"})
	if f, err := read(t, c); err != nil || f.T != "hello" {
		t.Fatalf("不带 kind 的握手应当成功，实际 %+v %v", f, err)
	}
	if e.auth.last.Kind != "" {
		t.Fatalf("缺省的令牌类型应当是空串，实际 %q——老客户端一行不用改是这次改动的前提", e.auth.last.Kind)
	}
}

func TestNoAuthFrameCloses4001(t *testing.T) {
	e := newEnv(t, Config{AuthTimeout: 200 * time.Millisecond})
	c := e.dial(t)
	defer c.CloseNow()
	_, err := read(t, c)
	if websocket.CloseStatus(err) != model.CloseAuthFailed {
		t.Fatalf("超时不发握手帧应 4001，实际 %v", err)
	}
}

func TestBadTokenCloses4001AndGuestRules(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "bad"})
	if _, err := read(t, c); websocket.CloseStatus(err) != model.CloseAuthFailed {
		t.Fatalf("坏 token 应 4001，实际 %v", err)
	}
	c.CloseNow()

	// a1 不允许访客
	c = e.dial(t)
	send(t, c, map[string]any{"t": "auth", "app": "a1", "guest": "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"})
	if _, err := read(t, c); websocket.CloseStatus(err) != model.CloseAuthFailed {
		t.Fatalf("app 未开访客应 4001，实际 %v", err)
	}
	c.CloseNow()

	e.apps["a1"] = model.AppConfig{AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace, AllowGuest: true, GuestIPRate: 20}
	c = e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": "auth", "app": "a1", "guest": "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"})
	if f, err := read(t, c); err != nil || f.T != "hello" {
		t.Fatalf("开了访客应 hello，实际 %+v %v", f, err)
	}
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
	e.apps["a1"] = model.AppConfig{AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace, AllowGuest: true, GuestIPRate: 20}
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": "auth", "app": "a1", "guest": "not-a-uuid"})
	if _, err := read(t, c); websocket.CloseStatus(err) != model.CloseAuthFailed {
		t.Fatalf("访客 id 不是合法 uuid 应 4001，实际 %v", err)
	}
}

func TestPolicyRejectCloses4002(t *testing.T) {
	e := newEnv(t, Config{})
	e.hs.result = registry.HandshakeResult{Rejected: true}
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "tok-1"})
	if _, err := read(t, c); websocket.CloseStatus(err) != model.ClosePolicyRejected {
		t.Fatalf("策略拒绝应 4002，实际 %v", err)
	}
}

func TestKickCloses4003AndEmitsDisconnected(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "tok-1"})
	hello, _ := read(t, c)
	e.h.Evict(context.Background(), "a1", model.User("1"), []registry.ConnRef{{ConnID: hello.Conn, Node: "im-a"}}, model.ReasonReplaced)
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
	if len(e.hs.removed) != 1 || e.hs.removed[0] != hello.Conn {
		t.Fatalf("断开时必须从注册表删自己，实际 %v", e.hs.removed)
	}
}

func TestIdleTimeoutDisconnects(t *testing.T) {
	e := newEnv(t, Config{IdleTimeout: 300 * time.Millisecond})
	c := e.dial(t)
	defer c.CloseNow()
	send(t, c, map[string]any{"t": "auth", "app": "a1", "token": "tok-1"})
	_, _ = read(t, c)
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
