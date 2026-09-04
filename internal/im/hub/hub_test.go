package hub

import (
	"context"
	"sync"
	"testing"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
)

type fakeConn struct {
	id, token string
	mu        sync.Mutex
	sent      [][]byte
	closed    *struct {
		code   int
		reason string
	}
	full bool
}

func (c *fakeConn) ID() string    { return c.id }
func (c *fakeConn) Token() string { return c.token }
func (c *fakeConn) Send(p []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.full {
		return false
	}
	c.sent = append(c.sent, p)
	return true
}
func (c *fakeConn) Close(code int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = &struct {
		code   int
		reason string
	}{code, reason}
}

type fakeStream struct {
	mu   sync.Mutex
	got  []*fpimv1.ConnectResponse
	fail bool
}

func (s *fakeStream) Send(r *fpimv1.ConnectResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return context.Canceled
	}
	s.got = append(s.got, r)
	return nil
}

type fakeReg struct {
	lookup map[string]map[string]model.ConnMeta // subject → connId → meta
	kicked []registry.ConnRef
}

func (r *fakeReg) Lookup(_ context.Context, _, subject string, _ []string) (map[string]model.ConnMeta, error) {
	return r.lookup[subject], nil
}
func (r *fakeReg) LookupMany(ctx context.Context, app string, subjects []string, live []string) ([]map[string]model.ConnMeta, error) {
	out := make([]map[string]model.ConnMeta, len(subjects))
	for i, s := range subjects {
		out[i], _ = r.Lookup(ctx, app, s, live)
	}
	return out, nil
}
func (r *fakeReg) KickAll(_ context.Context, _, subject string) ([]registry.ConnRef, error) {
	var refs []registry.ConnRef
	for cid, m := range r.lookup[subject] {
		refs = append(refs, registry.ConnRef{ConnID: cid, Node: m.Node})
	}
	delete(r.lookup, subject)
	return refs, nil
}
func (r *fakeReg) KickOne(_ context.Context, _, subject, connID string) (*registry.ConnRef, error) {
	m, ok := r.lookup[subject][connID]
	if !ok {
		return nil, nil
	}
	delete(r.lookup[subject], connID)
	return &registry.ConnRef{ConnID: connID, Node: m.Node}, nil
}

type fakeLive struct {
	nodes   []string
	servers map[string][]string
	serving map[string]bool
}

func (l *fakeLive) LiveNodes() []string             { return l.nodes }
func (l *fakeLive) ServerNodes(app string) []string { return l.servers[app] }
func (l *fakeLive) TrackApp(string)                 {}
func (l *fakeLive) SetServing(_ context.Context, app string, on bool) error {
	if l.serving == nil {
		l.serving = map[string]bool{}
	}
	l.serving[app] = on
	return nil
}

type fakePub struct {
	mu   sync.Mutex
	sent []struct {
		node string
		env  bus.Envelope
	}
}

func (p *fakePub) Publish(_ context.Context, node string, env bus.Envelope) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, struct {
		node string
		env  bus.Envelope
	}{node, env})
	return nil
}

type fakeApps map[string]model.AppConfig

func (a fakeApps) Get(app string) (model.AppConfig, bool) { c, ok := a[app]; return c, ok }
func (a fakeApps) Apps() []string {
	var out []string
	for k := range a {
		out = append(out, k)
	}
	return out
}

func newHub(t *testing.T) (*Hub, *fakeReg, *fakeLive, *fakePub) {
	t.Helper()
	reg := &fakeReg{lookup: map[string]map[string]model.ConnMeta{}}
	live := &fakeLive{nodes: []string{"im-a", "im-b", "im-c"}, servers: map[string][]string{}}
	pub := &fakePub{}
	apps := fakeApps{"a1": {AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace}}
	return New("im-a", reg, live, pub, apps), reg, live, pub
}

func TestAddConnEmitsConnectedEventToLocalStream(t *testing.T) {
	h, _, live, _ := newHub(t)
	s := &fakeStream{}
	h.AddStream(context.Background(), "a1", s)
	if !live.serving["a1"] {
		t.Fatal("第一条 server 流出现时必须 SetServing(true)")
	}
	c := &fakeConn{id: "c1", token: "tok"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a", OS: "ios", Mobile: true, At: 5}, "UA/1")
	if len(s.got) != 1 || s.got[0].GetEvent() == nil {
		t.Fatalf("应收到 1 条 Connected 事件，实际 %+v", s.got)
	}
	ev := s.got[0].GetEvent()
	if ev.Kind != fpimv1.EventKind_EVENT_KIND_CONNECTED || ev.Subject != "u:1" || ev.ConnId != "c1" || ev.Ua != "UA/1" || ev.Os != "ios" || !ev.Mobile {
		t.Fatalf("Connected 事件字段不对：%+v", ev)
	}
	h.RemoveConn(context.Background(), "a1", model.User("1"), "c1", model.ReasonClient)
	if len(s.got) != 2 || s.got[1].GetEvent().Kind != fpimv1.EventKind_EVENT_KIND_DISCONNECTED || s.got[1].GetEvent().Reason != "client" {
		t.Fatalf("应收到 Disconnected(client)，实际 %+v", s.got)
	}
}

func TestRemoveLastStreamClearsServing(t *testing.T) {
	h, _, live, _ := newHub(t)
	rm1 := h.AddStream(context.Background(), "a1", &fakeStream{})
	rm2 := h.AddStream(context.Background(), "a1", &fakeStream{})
	rm1()
	if !live.serving["a1"] {
		t.Fatal("还有一条流时不能 SetServing(false)")
	}
	rm2()
	if live.serving["a1"] {
		t.Fatal("最后一条流断开必须立即 SetServing(false)")
	}
}

func TestHandleEnvelopeMsgFansOutToLocalConns(t *testing.T) {
	h, _, _, _ := newHub(t)
	c1 := &fakeConn{id: "c1"}
	c2 := &fakeConn{id: "c2"}
	h.AddConn(context.Background(), "a1", model.User("1"), c1, model.ConnMeta{Node: "im-a"}, "")
	h.AddConn(context.Background(), "a1", model.User("1"), c2, model.ConnMeta{Node: "im-a"}, "")
	h.HandleEnvelope(context.Background(), bus.Envelope{Type: bus.TypeMsg, App: "a1", Subject: "u:1", Payload: []byte(`{"a":1}`)})
	if len(c1.sent) != 1 || len(c2.sent) != 1 {
		t.Fatalf("同 subject 的每条本地连接都应收到：c1=%d c2=%d", len(c1.sent), len(c2.sent))
	}
	// 本地没有的 subject：静默丢弃，不 panic
	h.HandleEnvelope(context.Background(), bus.Envelope{Type: bus.TypeMsg, App: "a1", Subject: "u:404", Payload: []byte(`1`)})
}

func TestHandleEnvelopeKickClosesLocalConn(t *testing.T) {
	h, _, _, _ := newHub(t)
	c := &fakeConn{id: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	h.HandleEnvelope(context.Background(), bus.Envelope{Type: bus.TypeKick, App: "a1", Subject: "u:1", ConnID: "c1", Extra: model.ReasonReplaced})
	if c.closed == nil || c.closed.code != model.CloseKicked || c.closed.reason != model.ReasonReplaced {
		t.Fatalf("KICK 应以 4003/replaced 关闭本地连接，实际 %+v", c.closed)
	}
}

func TestSendQueueFullClosesWithBackpressure(t *testing.T) {
	h, _, _, _ := newHub(t)
	c := &fakeConn{id: "c1", full: true}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	h.HandleEnvelope(context.Background(), bus.Envelope{Type: bus.TypeMsg, App: "a1", Subject: "u:1", Payload: []byte(`1`)})
	if c.closed == nil || c.closed.code != model.CloseBackpressure || c.closed.reason != model.ReasonBackpressure {
		t.Fatalf("发送队列满应以 1013/backpressure 关闭，实际 %+v", c.closed)
	}
}
