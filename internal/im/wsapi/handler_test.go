package wsapi

import (
	"context"
	"encoding/json"
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

type fakeAuth map[string]model.Subject

func (a fakeAuth) Verify(_ context.Context, _, token string) (model.Subject, error) {
	if s, ok := a[token]; ok {
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
	handler := New(Deps{Hub: h, Conns: hs, Live: fakeLiveness{}, Auth: fakeAuth{"tok-1": model.User("1")}, Apps: apps, Guests: NewGuestLimiter(), Cfg: cfg})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &env{srv: srv, h: h, stream: stream, hs: hs, apps: apps}
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
