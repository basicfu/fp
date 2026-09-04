package hub

import (
	"context"
	"log/slog"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
)

type PushStatus int

const (
	PushSent PushStatus = iota + 1
	PushNotOnline
	PushUnavailable
)

type PushResult struct {
	Subject string
	Status  PushStatus
	Nodes   int
}

type Session struct {
	ConnID      string
	Node        string
	OS          string
	Mobile      bool
	ConnectedAt int64
}

// singleConn 判断该 app 是否全网最多一条连接：是则本地命中就不必再查注册表。
func (h *Hub) singleConn(app string) bool {
	cfg, ok := h.apps.Get(app)
	return ok && (cfg.ConnPolicy == model.PolicyReplace || cfg.ConnPolicy == model.PolicyReject)
}

func (h *Hub) Push(ctx context.Context, app string, sub model.Subject, payload []byte) PushResult {
	return h.PushMany(ctx, app, []model.Subject{sub}, payload)[0]
}

// PushMany 对每个 subject：先投本地，单连接策略下本地命中即返回；否则一次
// LookupMany 把所有还需要查表的 subject 合并成一次注册表往返，找其他节点。
// 不要退化成循环调 Push——那样每个 subject 各自一次注册表往返，PushMany
// 存在的唯一意义（把多次查询合并成一次）就没了。
func (h *Hub) PushMany(ctx context.Context, app string, subs []model.Subject, payload []byte) []PushResult {
	results := make([]PushResult, len(subs))
	localHits := make([]int, len(subs))
	var pending []int // 还需要查注册表的下标
	for i, s := range subs {
		results[i].Subject = s.String()
		localHits[i] = h.sendLocal(app, s.String(), payload)
		if localHits[i] > 0 && h.singleConn(app) {
			// replace/reject 策略下全网最多一条连接，本地命中就是唯一的一条，
			// 没有必要再为了"还有没有别的节点"去查一次注册表。
			results[i].Status, results[i].Nodes = PushSent, 1
			continue
		}
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		return results
	}
	subjects := make([]string, len(pending))
	for j, i := range pending {
		subjects[j] = subs[i].String()
	}
	lookups, err := h.reg.LookupMany(ctx, app, subjects, h.live.LiveNodes())
	if err != nil {
		slog.Warn("hub: 查注册表失败", "app", app, "err", err)
		for _, i := range pending {
			results[i].Status = PushUnavailable
		}
		return results
	}
	for j, i := range pending {
		nodes := map[string]bool{}
		for _, meta := range lookups[j] {
			// limit 策略允许多条连接，本地已经投过一份了，发布时要排除自己，
			// 否则同一条消息会在本节点再被处理一次。
			if meta.Node != h.nodeID {
				nodes[meta.Node] = true
			}
		}
		total := 0
		if localHits[i] > 0 {
			total++
		}
		for n := range nodes {
			env := bus.Envelope{Type: bus.TypeMsg, App: app, Subject: subs[i].String(), Payload: payload}
			got, err := h.pub.Publish(ctx, n, env)
			if err != nil {
				slog.Warn("hub: 喊节点失败", "node", n, "err", err)
				continue
			}
			if got == 0 {
				// 订阅者数为 0：目标节点大概率已经崩溃，注册表里记的那条
				// 连接其实已经不存在了。既不能算进 Nodes——那会让业务方
				// 误以为真的送达了——也要调用 DropLocal 把它从本节点的
				// 存活快照里剔除，避免下一次 PushMany 再选中同一个死节点、
				// 白打一次注定失败的 Redis 请求。
				h.live.DropLocal(n)
				continue
			}
			total++
		}
		if total == 0 {
			// 所有目标节点都没有订阅者、本地也没投出去：如果这里仍然返回
			// Sent{0}，业务方拿到的就是一个"已送达"的假象，所以必须落到
			// NotOnline。
			results[i].Status = PushNotOnline
		} else {
			results[i].Status, results[i].Nodes = PushSent, total
		}
	}
	return results
}

// Sessions 返回全网视角的会话列表：数据来自注册表，不是本地连接表——同一
// subject 的其它连接可能在别的节点上，只看本地看不全。
func (h *Hub) Sessions(ctx context.Context, app string, sub model.Subject) ([]Session, error) {
	conns, err := h.reg.Lookup(ctx, app, sub.String(), h.live.LiveNodes())
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(conns))
	for cid, m := range conns {
		out = append(out, Session{ConnID: cid, Node: m.Node, OS: m.OS, Mobile: m.Mobile, ConnectedAt: m.At})
	}
	return out, nil
}

// Kick 不传 connIDs 踢全部：先从注册表原子地取出并删除条目，再据此关闭
// 本地连接、给远端节点发踢人信封。踢人和顶号走同一个 Evict，只是原因串不同。
func (h *Hub) Kick(ctx context.Context, app string, sub model.Subject, connIDs ...string) error {
	var refs []registry.ConnRef
	if len(connIDs) == 0 {
		all, err := h.reg.KickAll(ctx, app, sub.String())
		if err != nil {
			return err
		}
		refs = all
	} else {
		for _, cid := range connIDs {
			ref, err := h.reg.KickOne(ctx, app, sub.String(), cid)
			if err != nil {
				return err
			}
			if ref != nil {
				refs = append(refs, *ref)
			}
		}
	}
	h.Evict(ctx, app, sub, refs, model.ReasonKicked)
	return nil
}

// Evict 关闭一组已从注册表删除的连接：本地的直接关，远端的发 KICK 信封。
// 握手脚本返回的被顶替列表和 Kick 都走这里。
func (h *Hub) Evict(ctx context.Context, app string, sub model.Subject, refs []registry.ConnRef, reason string) {
	for _, r := range refs {
		if r.Node == h.nodeID {
			h.closeLocal(app, sub.String(), r.ConnID, model.CloseKicked, reason)
			continue
		}
		env := bus.Envelope{Type: bus.TypeKick, App: app, Subject: sub.String(), ConnID: r.ConnID, Extra: reason}
		got, err := h.pub.Publish(ctx, r.Node, env)
		if err != nil {
			slog.Warn("hub: 发 KICK 失败", "node", r.Node, "err", err)
			continue
		}
		if got == 0 {
			// 订阅者数为 0：目标节点已经随进程一起崩溃，上面那条连接根本
			// 不存在了，不需要也无法再踢——记一条日志即可，不当失败处理，
			// 不重试、不返回错误（调用方 Kick/顶号场景都不该因为一个已经
			// 不在的节点而认为整体操作失败）。
			slog.Info("hub: 踢的目标节点已不在线，跳过", "node", r.Node, "connId", r.ConnID)
		}
	}
}

// OnRevoked 在 fp 撤销 token 时关闭持有这些 token 的本地连接。
// 用 4001 而不是 4003：对 client 来说这和 token 过期是同一件事，应去重新
// 登录而不是重连；4003/kicked 会让它以为只是被顶号，直接重连反而立刻再次
// 被拒。
func (h *Hub) OnRevoked(ctx context.Context, app string, tokens []string) {
	var victims []Conn
	h.mu.RLock()
	for _, t := range tokens {
		victims = append(victims, h.byToken[key(app, t)]...)
	}
	h.mu.RUnlock()
	for _, c := range victims {
		c.Close(model.CloseAuthFailed, model.ReasonRevoked)
	}
}
