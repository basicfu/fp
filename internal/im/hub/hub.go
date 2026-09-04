// Package hub 是 im 节点的内存路由器：本地连接表、本地 server 流表、跨节点转发判定。
// 它不直接碰 Redis：写由 wsapi 与 cmd 的循环通过 registry 完成，hub 只读快照与发布信封。
package hub

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

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
	// DropLocal 把某节点从本节点的内存快照里临时剔除。Deliver 转发时发现目标
	// 节点 SPUBLISH 返回 0（没有订阅者，大概率已崩溃）就调用它，避免自己在
	// 同一批候选里反复选中一个已经死掉的节点。
	DropLocal(node string)
}

// Publisher 发布一条信封到目标节点的私有频道，返回收到它的订阅者数量。
// 0 意味着目标节点没有订阅（大概率已崩溃或者 Redis 连接正在重连），
// Deliver 靠这个数字判断要不要继续沿候选列表往下试。
type Publisher interface {
	Publish(ctx context.Context, nodeID string, env bus.Envelope) (int64, error)
}

// connEntry 是一条连接在 hub 眼里的完整状态，外加 C2 需要的"建立事件是否已
// 处理完、处理结果如何"这两样东西。
type connEntry struct {
	conn Conn
	meta model.ConnMeta

	// established 在 AddConn 发出的 Connected 事件处理完（不管成功与否）时
	// 关闭。RemoveConn 需要等它，确保业务 server 一定先收到"建立"再收到
	// "断开"：这条连接一旦写进 h.conns，就对所有协程可见、可以被并发地
	// RemoveConn 掉，而 AddConn 发建立事件这个动作本身有 I/O 延迟，两者之间
	// 存在窗口。等待发生在锁外，不阻塞任何路由；close(established) 之前对
	// delivered 的写入，凭 channel 关闭自带的 happens-before，对之后所有
	// 因这次 close 而从 <-established 返回的协程都可见，不需要额外加锁或
	// atomic。
	established chan struct{}
	// delivered 记录建立事件是否成功投递到了业务 server（本地写流成功，
	// 或转发成功）。只有在 established 关闭之后读它才是安全的。
	delivered bool
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

// removeConnWait 是等待"建立事件处理完"信号的超时上限。卡满这个时长说明
// 系统已严重异常（emit 依赖的 Deliver 正常情况下几乎立即返回），宁可少发
// 一条断开事件，也不要在业务方那边制造一个无法解释的孤儿消息。
const removeConnWait = 30 * time.Second

// AddConn 登记连接并向 server 发 Connected 事件。调用前 registry 的握手脚本必须已成功。
//
// 建立事件的"是否投递成功"要记下来，供之后可能到来的 RemoveConn 判断要不要
// 发断开事件（见 C2）：如果连接在事件发出前就被摘掉，RemoveConn 会等这里的
// established 信号，读这里写的 delivered。
func (h *Hub) AddConn(ctx context.Context, app string, sub model.Subject, c Conn, meta model.ConnMeta, ua string) {
	k := key(app, sub.String())
	entry := &connEntry{conn: c, meta: meta, established: make(chan struct{})}
	h.mu.Lock()
	if h.conns[k] == nil {
		h.conns[k] = map[string]*connEntry{}
	}
	h.conns[k][c.ID()] = entry
	if tok := c.Token(); tok != "" {
		h.byToken[key(app, tok)] = append(h.byToken[key(app, tok)], c)
	}
	h.mu.Unlock()
	delivered := h.emit(ctx, app, sub, c.ID(), model.EventBody{Kind: model.EventConnected, OS: meta.OS, Mobile: meta.Mobile, UA: ua, At: meta.At})
	entry.delivered = delivered
	close(entry.established)
}

// RemoveConn 从表里删除并发 Disconnected 事件。幂等：重复删除不发第二次事件。
//
// 建立事件与断开事件的顺序保证（C2）：锁内摘除连接之后、真正决定要不要发
// 断开事件之前，先等 AddConn 那边的建立事件处理完。这个等待必须发生在锁外
// ——锁内只做"从表里摘除"这一件事，等待可能长达 30 秒，锁在手里等于把整个
// 节点的路由（所有连接的增删）都锁死。
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
		// 这条连接已经被删过（重复调用、或握手替换时先一步被踢掉）：保持
		// 现有的幂等语义，不发任何事件。这条判断本身不受本次改动影响，
		// 新加的等待逻辑必须放在它之后，不能抢在它前面。
		return
	}

	select {
	case <-entry.established:
	case <-time.After(removeConnWait):
		slog.Error("hub: 等待建立事件处理完超时，跳过断开事件", "app", app, "subject", sub.String(), "connId", connID)
		return
	}
	if !entry.delivered {
		// 建立事件没有送达业务 server：它的在线表里根本没有这条连接的记录，
		// 不存在"泄漏一条连接"的问题。此时仍然发断开事件，只会给业务方一个
		// 找不到对应建立记录的孤儿消息——大概率被当成"未知连接，忽略"，
		// 但这已经是在制造看起来像 bug 的行为，不如干脆不发。
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
			// 同 RemoveConn 里的 byToken：不用 h.streams[app][:0] 原地复用。
			// localStream 的整个索引运算都在读锁释放之前完成，与这里的写锁
			// 互斥，此刻并不存在真正的并发读者。改成新分配是为将来可能
			// 出现的"锁外持有旧切片头的读者"预留：一旦将来加了这样的调用方
			// （比如某处先在锁外拿到 streams 快照再逐个处理），原地改写就会
			// 让它看到一半新一半旧的数据，甚至和这里的追加写发生数据竞争。
			// 提前按 byToken 的写法处理，不必等竞争真正出现再改。
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

// ForEachConn 遍历本地连接，给续期与重登记用。
//
// 先在锁内把需要的信息拷成一份切片，放锁后再逐个调用 fn：fn 唯一已知的
// 调用方会在回调里做 Redis I/O（续 TTL），如果像最初那样在持读锁期间直接
// 调用回调，十万级连接逐条处理意味着连接表被锁死数秒，期间所有握手和
// 断开清理全部堵住。这里的写法和 sendLocal 一致：锁内只拷贝，锁外做慢操作。
func (h *Hub) ForEachConn(fn func(app string, sub model.Subject, connID string, meta model.ConnMeta)) {
	type item struct {
		app, subject, connID string
		meta                 model.ConnMeta
	}
	h.mu.RLock()
	items := make([]item, 0, 64)
	for k, m := range h.conns {
		app, subject := splitKey(k)
		for cid, e := range m {
			items = append(items, item{app: app, subject: subject, connID: cid, meta: e.meta})
		}
	}
	h.mu.RUnlock()
	for _, it := range items {
		sub, err := model.ParseSubject(it.subject)
		if err != nil {
			continue
		}
		fn(it.app, sub, it.connID, it.meta)
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
