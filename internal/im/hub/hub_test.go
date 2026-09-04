package hub

import (
	"context"
	"sync"
	"testing"
	"time"

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

// orderedStream 让测试精确控制"每一次 Send 调用被尝试的时刻"与"这次调用
// 什么时候真正完成"：Send 先把这次调用要发的内容报告到 calling（无缓冲，
// 逼着调用方等测试收下才能继续），再卡在 proceed 上等测试放行。
//
// 用它而不是简单的一次性 gate，是因为这条回归测试要验证的是"第二次尝试
// 发送的时刻"本身，而不是"最终 got 里的顺序"：如果只卡住第一次调用、
// 观察 got 的最终顺序，测试结果会依赖"删表之后、真正调用 Send 之前"这段
// 近乎零耗时的窗口里两个 goroutine 谁先被调度到，是真正的竞态，不确定性
// 构造不出来。orderedStream 把"是否已经尝试发送第二条事件"变成一个可以
// 直接 select 的 channel 事件，不依赖调度器的运气。
type orderedStream struct {
	mu      sync.Mutex
	got     []*fpimv1.ConnectResponse
	calling chan *fpimv1.ConnectResponse
	proceed chan struct{}
}

func (s *orderedStream) Send(r *fpimv1.ConnectResponse) error {
	s.calling <- r
	<-s.proceed
	s.mu.Lock()
	defer s.mu.Unlock()
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
	mu      sync.Mutex
	nodes   []string
	servers map[string][]string
	serving map[string]bool
	dropped []string // DropLocal 的调用记录，供测试断言
}

func (l *fakeLive) LiveNodes() []string             { return l.nodes }
func (l *fakeLive) ServerNodes(app string) []string { return l.servers[app] }
func (l *fakeLive) TrackApp(string)                 {}
func (l *fakeLive) SetServing(_ context.Context, app string, on bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.serving == nil {
		l.serving = map[string]bool{}
	}
	l.serving[app] = on
	return nil
}
func (l *fakeLive) DropLocal(node string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dropped = append(l.dropped, node)
}

// fakePub 模拟 bus.Bus.Publish：sent 记录每次调用，subs 控制每个目标节点
// "应该返回的订阅者数"——测试用它模拟"目标节点已崩溃"（返回 0）或
// "目标节点正常"（返回 >0，默认 1）。errNode 触发返回一个发布错误。
type fakePub struct {
	mu   sync.Mutex
	sent []struct {
		node string
		env  bus.Envelope
	}
	subs    map[string]int64
	errNode map[string]bool
}

func (p *fakePub) Publish(_ context.Context, node string, env bus.Envelope) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, struct {
		node string
		env  bus.Envelope
	}{node, env})
	if p.errNode != nil && p.errNode[node] {
		return 0, context.DeadlineExceeded
	}
	if p.subs != nil {
		if n, ok := p.subs[node]; ok {
			return n, nil
		}
	}
	return 1, nil // 默认目标有一个订阅者，转发成功
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

// TestRemoveConnWaitsForConnectedEventOrder 是 C2（连接事件与断开事件倒序）
// 的回归测试。
//
// 用 orderedStream 精确控制两件事各自"被尝试发送"与"真正完成"的时刻：
// 先确认 AddConn 已经在尝试发送 Connected（但还没完成），这时并发调用
// RemoveConn——这条连接此时已经对所有协程可见（AddConn 早已放锁），
// RemoveConn 能立刻从表里把它删掉。断言的关键在于：在 Connected 完成之前，
// 绝不能已经观察到第二次发送尝试（那就是 Disconnected 抢先发出）。
//
// 这条测试在改动被回退之后必须失败：旧实现里 RemoveConn 不等任何东西，
// 删表之后立刻调用 emit，会在 Connected 还卡着没完成的时候就抢先尝试发出
// Disconnected，被下面"不该收到第二次尝试"的 select 分支当场抓到。
func TestRemoveConnWaitsForConnectedEventOrder(t *testing.T) {
	h, _, live, _ := newHub(t)
	live.servers["a1"] = nil // 只走本地流，不需要转发

	s := &orderedStream{calling: make(chan *fpimv1.ConnectResponse), proceed: make(chan struct{})}
	h.AddStream(context.Background(), "a1", s)

	c := &fakeConn{id: "c1"}
	addDone := make(chan struct{})
	go func() {
		h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
		close(addDone)
	}()

	// 第一次尝试发送必然是 Connected：此刻只有 AddConn 这一个调用方在跑。
	var first *fpimv1.ConnectResponse
	select {
	case first = <-s.calling:
	case <-time.After(2 * time.Second):
		t.Fatal("AddConn 迟迟没有尝试发送 Connected")
	}
	if first.GetEvent() == nil || first.GetEvent().Kind != fpimv1.EventKind_EVENT_KIND_CONNECTED {
		t.Fatalf("第一次尝试发送的必须是 Connected，实际 %+v", first)
	}

	// Connected 还卡在 <-s.proceed，没有真正完成。现在并发调用 RemoveConn：
	// 这条连接已经在表里（AddConn 早就放锁了），RemoveConn 能立刻找到并
	// 删除它。
	removeDone := make(chan struct{})
	go func() {
		h.RemoveConn(context.Background(), "a1", model.User("1"), "c1", model.ReasonClient)
		close(removeDone)
	}()

	// 断言：在放行 Connected 之前，不应该已经观察到第二次发送尝试。
	select {
	case second := <-s.calling:
		t.Fatalf("Connected 还没处理完，RemoveConn 就已经尝试发送第二条事件（顺序倒挂）：%+v", second)
	case <-time.After(200 * time.Millisecond):
	}

	// 放行 Connected，确认 AddConn 完成。
	s.proceed <- struct{}{}
	select {
	case <-addDone:
	case <-time.After(2 * time.Second):
		t.Fatal("AddConn 迟迟不返回")
	}

	// 现在轮到 Disconnected 尝试发送。
	var second *fpimv1.ConnectResponse
	select {
	case second = <-s.calling:
	case <-time.After(2 * time.Second):
		t.Fatal("Connected 完成后，RemoveConn 应该发出 Disconnected，但迟迟没有尝试")
	}
	if second.GetEvent() == nil || second.GetEvent().Kind != fpimv1.EventKind_EVENT_KIND_DISCONNECTED {
		t.Fatalf("第二次尝试发送的必须是 Disconnected，实际 %+v", second)
	}
	s.proceed <- struct{}{}
	select {
	case <-removeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("RemoveConn 迟迟不返回")
	}
}

// TestRemoveConnSkipsDisconnectWhenConnectedNotDelivered 覆盖 C2 第 6 步：
// 建立事件投递失败（本地无流、全网也没有 server）时，RemoveConn 不应该再
// 发 Disconnected——业务 server 压根不知道这条连接存在，发断开只会制造一个
// 无法解释的孤儿消息。
func TestRemoveConnSkipsDisconnectWhenConnectedNotDelivered(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = nil // 本地无流，全网也没有 server：Connected 必然投递失败

	c := &fakeConn{id: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	if len(pub.sent) != 0 {
		t.Fatalf("候选为空，不该有任何发布尝试：%+v", pub.sent)
	}

	h.RemoveConn(context.Background(), "a1", model.User("1"), "c1", model.ReasonClient)
	if len(pub.sent) != 0 {
		t.Fatalf("Connected 事件投递失败后不该再发 Disconnected：%+v", pub.sent)
	}
}

// TestForEachConnCallbackRunsOutsideLock 覆盖 C3：回调必须在锁外执行，
// 否则回调里再调 hub 的其它方法（这里用 AddConn，需要写锁）会因为同一个
// goroutine 重复获取不可重入的 RWMutex 而永久阻塞。
func TestForEachConnCallbackRunsOutsideLock(t *testing.T) {
	h, _, _, _ := newHub(t)
	c := &fakeConn{id: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")

	done := make(chan int, 1)
	go func() {
		calls := 0
		h.ForEachConn(func(app string, sub model.Subject, connID string, meta model.ConnMeta) {
			calls++
			h.AddConn(context.Background(), "a1", model.User("2"), &fakeConn{id: "c2"}, model.ConnMeta{Node: "im-a"}, "")
		})
		done <- calls
	}()

	select {
	case calls := <-done:
		if calls != 1 {
			t.Fatalf("应该恰好回调 1 次，实际 %d", calls)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForEachConn 的回调里调用 hub 方法导致死锁：回调仍在持锁期间执行")
	}
}
