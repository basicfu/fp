// Package hub 是 im 节点的内存路由器：本地连接表、本地 server 流表、跨节点转发判定。
// 它不直接碰 Redis：写由 wsapi 与 cmd 的循环通过 registry 完成，hub 只读快照与发布信封。
package hub

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
)

// Conn 是一条 ws 连接在 hub 眼里的样子。Send 必须非阻塞：返回 false 表示发送队列满。
type Conn interface {
	ID() string
	Token() string
	Send(payload []byte) bool
	Close(code int, reason string)
}

// Stream 是一条 server 流。实现要自己保证 Send 并发安全（gRPC 流不允许并发 Send）。
//
// 实现类型必须是指针（或其他可比较且每个逻辑流唯一的值）：AddStream 返回的
// remove 闭包按 `==` 从 h.streams[app] 里认出自己那一条并摘掉。若实现是不可
// 比较的类型（比如内部含 slice/map/func 字段的结构体值），这里的接口比较会
// 直接 panic；调用方登记时永远传指针即可满足这个约束。
type Stream interface {
	Send(*fpimv1.ConnectResponse) error
}

type Registry interface {
	Lookup(ctx context.Context, app, subject string, live []string) (map[string]model.ConnMeta, error)
	LookupMany(ctx context.Context, app string, subjects []string, live []string) ([]map[string]model.ConnMeta, error)
	KickAll(ctx context.Context, app, subject string) ([]registry.ConnRef, error)
	KickOne(ctx context.Context, app, subject, connID string) (*registry.ConnRef, error)
}

type Liveness interface {
	LiveNodes() []string
	ServerNodes(app string) []string
	TrackApp(app string)
	SetServing(ctx context.Context, app string, on bool) error
}

type Publisher interface {
	Publish(ctx context.Context, nodeID string, env bus.Envelope) error
}

type connEntry struct {
	conn Conn
	meta model.ConnMeta
}

type Hub struct {
	nodeID string
	reg    Registry
	live   Liveness
	pub    Publisher
	apps   auth.AppConfigSource

	mu      sync.RWMutex
	conns   map[string]map[string]*connEntry // app+"\x00"+subject → connId → entry
	byToken map[string][]Conn                // app+"\x00"+token → conns（撤销时用）
	streams map[string][]Stream              // app → streams
}

func New(nodeID string, reg Registry, live Liveness, pub Publisher, apps auth.AppConfigSource) *Hub {
	return &Hub{
		nodeID: nodeID, reg: reg, live: live, pub: pub, apps: apps,
		conns: map[string]map[string]*connEntry{}, byToken: map[string][]Conn{}, streams: map[string][]Stream{},
	}
}

func (h *Hub) NodeID() string { return h.nodeID }

func key(app, subject string) string { return app + "\x00" + subject }

// AddConn 登记连接并向 server 发 Connected 事件。调用前 registry 的握手脚本必须已成功。
func (h *Hub) AddConn(ctx context.Context, app string, sub model.Subject, c Conn, meta model.ConnMeta, ua string) {
	k := key(app, sub.String())
	h.mu.Lock()
	if h.conns[k] == nil {
		h.conns[k] = map[string]*connEntry{}
	}
	h.conns[k][c.ID()] = &connEntry{conn: c, meta: meta}
	if tok := c.Token(); tok != "" {
		h.byToken[key(app, tok)] = append(h.byToken[key(app, tok)], c)
	}
	h.mu.Unlock()
	h.emit(ctx, app, sub, c.ID(), model.EventBody{Kind: model.EventConnected, OS: meta.OS, Mobile: meta.Mobile, UA: ua, At: meta.At})
}

// RemoveConn 从表里删除并发 Disconnected 事件。幂等：重复删除不发第二次事件。
func (h *Hub) RemoveConn(ctx context.Context, app string, sub model.Subject, connID, reason string) {
	k := key(app, sub.String())
	h.mu.Lock()
	entry, ok := h.conns[k][connID]
	if ok {
		delete(h.conns[k], connID)
		if len(h.conns[k]) == 0 {
			delete(h.conns, k)
		}
		if tok := entry.conn.Token(); tok != "" {
			tk := key(app, tok)
			// 分配新底层数组而不是复用 h.byToken[tk][:0]：旧切片可能仍被
			// 别的 goroutine 通过早先读到的引用遍历（比如撤销 token 时
			// 先在锁外拿到切片快照再逐个 Close），原地改写会让它看到
			// 一半新一半旧的数据，甚至和这里的追加写发生数据竞争。
			old := h.byToken[tk]
			list := make([]Conn, 0, len(old))
			for _, c := range old {
				if c.ID() != connID {
					list = append(list, c)
				}
			}
			if len(list) == 0 {
				delete(h.byToken, tk)
			} else {
				h.byToken[tk] = list
			}
		}
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	h.emit(ctx, app, sub, connID, model.EventBody{Kind: model.EventDisconnected, OS: entry.meta.OS, Mobile: entry.meta.Mobile, Reason: reason, At: nowMs()})
}

// AddStream 登记一条 server 流；第一条时向 Redis 声明本节点在服务该 app。
// 返回的 remove 在流断开时调用；最后一条流移除时立即撤销声明。
func (h *Hub) AddStream(ctx context.Context, app string, s Stream) (remove func()) {
	h.mu.Lock()
	h.streams[app] = append(h.streams[app], s)
	first := len(h.streams[app]) == 1
	h.mu.Unlock()
	if first {
		if err := h.live.SetServing(ctx, app, true); err != nil {
			slog.Error("hub: 声明 server 流失败", "app", app, "err", err)
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			// 同 RemoveConn 里的 byToken：不用 h.streams[app][:0] 原地复用，
			// 因为 localStream 会在读锁下持有这个切片头做索引运算；这里改写
			// 底层数组会和那边的并发读撞车。分配新切片更贵，但这条路径只在
			// 流断开时走一次，不是热路径。
			old := h.streams[app]
			list := make([]Stream, 0, len(old))
			for _, x := range old {
				if x != s {
					list = append(list, x)
				}
			}
			last := len(list) == 0
			if last {
				delete(h.streams, app)
			} else {
				h.streams[app] = list
			}
			h.mu.Unlock()
			if last {
				if err := h.live.SetServing(context.Background(), app, false); err != nil {
					slog.Error("hub: 撤销 server 流声明失败", "app", app, "err", err)
				}
			}
		})
	}
}

// localStream 按 subject 哈希在本地流里选一条，同 subject 恒选同一条。
func (h *Hub) localStream(app, subject string) (Stream, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	list := h.streams[app]
	if len(list) == 0 {
		return nil, false
	}
	return list[hash32(subject)%uint32(len(list))], true
}

// sendLocal 把 payload 投给本地该 subject 的全部连接，返回投出的条数。
// 发送队列满的连接以 1013/backpressure 关闭：让它重连并向业务方拉历史，比无限堆积拖垮节点好。
func (h *Hub) sendLocal(app, subject string, payload []byte) int {
	h.mu.RLock()
	entries := make([]*connEntry, 0, len(h.conns[key(app, subject)]))
	for _, e := range h.conns[key(app, subject)] {
		entries = append(entries, e)
	}
	h.mu.RUnlock()
	// Send/Close 都在锁外调用：conn.Send 可能触发 ws 写、conn.Close 可能等
	// 写循环退出，两者都不是快操作。锁内只做"拷贝一份 entries 快照"这一件
	// 事，读锁的持有时间和连接数无关的 I/O 完全脱钩，不会因为某条连接慢
	// 而卡住其他 goroutine 对 conns/streams 的读写。
	n := 0
	for _, e := range entries {
		if e.conn.Send(payload) {
			n++
		} else {
			e.conn.Close(model.CloseBackpressure, model.ReasonBackpressure)
		}
	}
	return n
}

func (h *Hub) closeLocal(app, subject, connID string, code int, reason string) bool {
	h.mu.RLock()
	e, ok := h.conns[key(app, subject)][connID]
	h.mu.RUnlock()
	if ok {
		e.conn.Close(code, reason)
	}
	return ok
}

// HandleEnvelope 处理从节点频道收到的一条信封。
func (h *Hub) HandleEnvelope(ctx context.Context, env bus.Envelope) {
	switch env.Type {
	case bus.TypeMsg:
		h.sendLocal(env.App, env.Subject, env.Payload)
	case bus.TypeUp, bus.TypeEvt:
		h.Deliver(ctx, env)
	case bus.TypeKick:
		h.closeLocal(env.App, env.Subject, env.ConnID, model.CloseKicked, env.Extra)
	default:
		slog.Warn("hub: 未知信封类型", "type", env.Type)
	}
}

// ForEachConn 遍历本地连接，给续期与重登记用。回调里不得再调 hub 的方法。
func (h *Hub) ForEachConn(fn func(app string, sub model.Subject, connID string, meta model.ConnMeta)) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for k, m := range h.conns {
		app, subject := splitKey(k)
		sub, err := model.ParseSubject(subject)
		if err != nil {
			continue
		}
		for cid, e := range m {
			fn(app, sub, cid, e.meta)
		}
	}
}

// splitKey 拆 key() 拼出的 "app\x00subject"。app 来自静态配置、subject 来自
// token/uuid 派生的 model.Subject.String()，两者都不可能含 \x00：Go 字符串
// 字面量与 uuid/用户 id 的合法字符集都不包含这个控制字符，所以第一个 0 字节
// 必然就是 key() 写入的那个分隔符，不会把 subject 内容误切成 app 的一部分。
// 找不到分隔符时按 (k, "") 返回，调用方随后 ParseSubject("") 会报错并被
// ForEachConn 的循环跳过，不会 panic 也不会拿着半个 key 当 subject 用。
func splitKey(k string) (app, subject string) {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

func hash32(s string) uint32 {
	f := fnv.New32a()
	f.Write([]byte(s))
	return f.Sum32()
}
