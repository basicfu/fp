// Package hubtest 提供 hub 三个依赖接口（Registry/Liveness/Publisher）与
// Conn/Stream 的假实现，给 hub、wsapi、imgrpc、integration 的测试共用。
//
// 这些假实现原先各自散落、重复写在 hub 包内部测试文件里（package hub 的
// hub_test.go）。搬到这个独立包意味着它们不能再直接碰 hub.Hub 的未导出
// 字段：Task 7 的 push_test.go 曾经写过 `h.apps = fakeApps{...}` 来给某个
// 测试临时切换 app 的连接策略，这行代码在 hubtest 是外部包的前提下无法
// 编译。解决办法见 NewHubWithApps：把"用什么 apps 配置建 Hub"挪到构造时
// 决定，不再构造后绕过封装去改。
package hubtest

import (
	"context"
	"sync"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
)

// Conn 是 hub.Conn 的假实现。
//
// 字段名 ConnID/Tok 而不是 ID/Token：Go 不允许一个类型同时有字段和方法
// 用同一个名字，而 hub.Conn 接口要求的方法就叫 ID()/Token()。
type Conn struct {
	ConnID string
	Tok    string

	mu     sync.Mutex
	Sent   [][]byte
	Closed *Closure
	// Full 为 true 时 Send 总是返回 false，模拟发送队列已满。
	Full bool
}

// Closure 记录一次 Close(code, reason) 调用。
type Closure struct {
	Code   int
	Reason string
}

func (c *Conn) ID() string    { return c.ConnID }
func (c *Conn) Token() string { return c.Tok }

func (c *Conn) Send(p []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Full {
		return false
	}
	c.Sent = append(c.Sent, p)
	return true
}

func (c *Conn) Close(code int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Closed = &Closure{Code: code, Reason: reason}
}

// Stream 是 hub.Stream 的假实现。
type Stream struct {
	mu   sync.Mutex
	Got  []*fpimv1.ConnectResponse
	Fail bool
}

func (s *Stream) Send(r *fpimv1.ConnectResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail {
		return context.Canceled
	}
	s.Got = append(s.Got, r)
	return nil
}

// GotSnapshot 加锁返回 Got 的快照。Send 可能在 hub 所在的那个 goroutine
// 里被调用（比如 wsapi 里处理某条 ws 连接的 HTTP handler goroutine），
// 与测试自己的 goroutine（尤其是 waitUntil 里的轮询）并发——直接裸读
// Got 字段是一次真实的数据竞争，本包里的 Live 已经为同样的原因（DropLocal
// 记录）提供了 DroppedSnapshot，Stream 理应保持同样的标准。
func (s *Stream) GotSnapshot() []*fpimv1.ConnectResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*fpimv1.ConnectResponse(nil), s.Got...)
}

// Reg 是 hub.Registry 的假实现。
type Reg struct {
	// Table 是 subject → connId → meta 的假注册表数据。字段名不叫 Lookup：
	// hub.Registry 接口本身有一个叫 Lookup 的方法，类型不能同时有同名的
	// 字段和方法。
	Table map[string]map[string]model.ConnMeta

	// LookupManyCalls 记录 LookupMany 被调用的次数，供测试断言"多个主体
	// 合并成一次注册表查询"这条性质：如果调用方退化成逐个主体循环查询
	// （不管是循环调 Lookup 还是循环调只传一个主体的 LookupMany），这个
	// 计数器会随主体数一起涨上去，而不是恒为 1。
	LookupManyCalls int
}

func (r *Reg) Lookup(_ context.Context, _, subject string, _ []string) (map[string]model.ConnMeta, error) {
	return r.Table[subject], nil
}

func (r *Reg) LookupMany(ctx context.Context, app string, subjects []string, live []string) ([]map[string]model.ConnMeta, error) {
	r.LookupManyCalls++
	out := make([]map[string]model.ConnMeta, len(subjects))
	for i, s := range subjects {
		out[i], _ = r.Lookup(ctx, app, s, live)
	}
	return out, nil
}

func (r *Reg) KickAll(_ context.Context, _, subject string) ([]registry.ConnRef, error) {
	var refs []registry.ConnRef
	for cid, m := range r.Table[subject] {
		refs = append(refs, registry.ConnRef{ConnID: cid, Node: m.Node})
	}
	delete(r.Table, subject)
	return refs, nil
}

func (r *Reg) KickOne(_ context.Context, _, subject, connID string) (*registry.ConnRef, error) {
	m, ok := r.Table[subject][connID]
	if !ok {
		return nil, nil
	}
	delete(r.Table[subject], connID)
	return &registry.ConnRef{ConnID: connID, Node: m.Node}, nil
}

// Live 是 hub.Liveness 的假实现。
type Live struct {
	mu      sync.Mutex
	Nodes   []string
	Servers map[string][]string
	Serving map[string]bool
	// Dropped 记录 DropLocal 的调用参数，供测试断言 Deliver/PushMany 在
	// 目标节点返回 0 订阅者时确实剔除了本地快照。
	Dropped []string
}

func (l *Live) LiveNodes() []string             { return l.Nodes }
func (l *Live) ServerNodes(app string) []string { return l.Servers[app] }
func (l *Live) TrackApp(string)                 {}

func (l *Live) SetServing(_ context.Context, app string, on bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Serving == nil {
		l.Serving = map[string]bool{}
	}
	l.Serving[app] = on
	return nil
}

func (l *Live) DropLocal(node string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Dropped = append(l.Dropped, node)
}

// DroppedSnapshot 加锁返回 Dropped 的快照。DropLocal 可能被 Deliver/PushMany
// 在其他 goroutine 里并发调用，测试不能不加锁地直接读 Dropped 字段。
func (l *Live) DroppedSnapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.Dropped...)
}

// IsServing 加锁返回某个 app 当前的 serving 状态，避免测试不加锁地直接读
// Serving 这个 map。
func (l *Live) IsServing(app string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Serving[app]
}

// Published 记录 Pub.Publish 的一次调用。
type Published struct {
	Node string
	Env  bus.Envelope
}

// Pub 是 hub.Publisher 的假实现：Published 记录每次调用，Subs 控制每个
// 目标节点"应该返回的订阅者数"——测试用它模拟"目标节点已崩溃"（返回 0）
// 或"目标节点正常"（返回 >0，默认 1）。ErrNode 触发返回一个发布错误。
type Pub struct {
	mu        sync.Mutex
	Published []Published
	Subs      map[string]int64
	ErrNode   map[string]bool
}

func (p *Pub) Publish(_ context.Context, node string, env bus.Envelope) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Published = append(p.Published, Published{Node: node, Env: env})
	if p.ErrNode != nil && p.ErrNode[node] {
		return 0, context.DeadlineExceeded
	}
	if p.Subs != nil {
		if n, ok := p.Subs[node]; ok {
			return n, nil
		}
	}
	return 1, nil // 默认目标有一个订阅者，转发成功
}

// Apps 是 auth.AppConfigSource 的假实现。
type Apps map[string]model.AppConfig

func (a Apps) Load(context.Context, string) error     { return nil }
func (a Apps) Get(app string) (model.AppConfig, bool) { c, ok := a[app]; return c, ok }
func (a Apps) Apps() []string {
	var out []string
	for k := range a {
		out = append(out, k)
	}
	return out
}

// DefaultApps 返回默认的单 app（a1，replace 策略）配置，多数测试不需要
// 自定义 apps 时用它。
func DefaultApps() Apps {
	return Apps{"a1": {AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyReplace}}
}

// NewHub 用假实现装一个 nodeID 为 im-a、a1 策略为 replace 的 Hub。
func NewHub() (*hub.Hub, *Reg, *Live, *Pub, Apps) {
	apps := DefaultApps()
	h, reg, live, pub := NewHubWithApps(apps)
	return h, reg, live, pub, apps
}

// NewHubWithApps 与 NewHub 相同，但由调用方提供 apps 配置。存在的原因见
// 包注释：需要非默认策略（比如 limit）的测试用它，而不是绕过封装去改
// hub.Hub 的未导出字段。
func NewHubWithApps(apps Apps) (*hub.Hub, *Reg, *Live, *Pub) {
	reg := &Reg{Table: map[string]map[string]model.ConnMeta{}}
	live := &Live{Nodes: []string{"im-a", "im-b", "im-c"}, Servers: map[string][]string{}}
	pub := &Pub{}
	return hub.New("im-a", reg, live, pub, apps), reg, live, pub
}
