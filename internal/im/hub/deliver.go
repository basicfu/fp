package hub

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/rendezvous"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fp/im/v1"
)

// Deliver 把 client 的消息或连接事件送到 server。
// 返回是否成功投递：写进了本地某条 server 流，或成功发布给了另一个还在订阅的节点。
//
// 流程：
//  1. 本地有该 app 的 server 流 → 按 subject 哈希选一条写入。
//  2. 本地没有流、且信封还没算过候选列表（下游转发来的信封已经带着算好的
//     Route，直接沿用）→ 现算一个：从 live.ServerNodes(app) 取出候选，排除
//     自己，按 subject 做 rendezvous 权重降序排序，写进 env.Route。
//     候选列表只在首发节点算这一次：如果每一跳都各自重算，不同节点的
//     存活快照哪怕只差一个心跳周期就可能不一致，消息会在算出不同顺序的
//     节点之间来回打转，永远到不了。
//  3. 从 env.Hop 指向的位置开始，沿 env.Route 逐个尝试发布：SPUBLISH 的
//     返回值就是收到消息的订阅者数，0 意味着目标没有订阅（大概率已崩溃，
//     或者它的 Redis 连接正在重连），这种情况下把它从本地快照里剔掉
//     （DropLocal），游标继续往后试下一个候选；游标只增不减、Route 长度
//     固定，最多试完全部候选，不可能无限循环。
//  4. 候选试完仍未成功，或者一开始候选就是空 → 静默丢弃，返回 false。
//     静默是有意的：server 的故障对 client 不可见，client 靠自己的超时
//     重发。
func (h *Hub) Deliver(ctx context.Context, env bus.Envelope) bool {
	if s, ok := h.localStream(env.App, env.Subject); ok {
		resp := toResponse(env)
		if resp == nil {
			// toResponse 已经记了警告日志：事件 payload 解析失败，没有可
			// 投递的内容，按失败处理，不要把 nil 传给 s.Send。
			return false
		}
		if err := s.Send(resp); err != nil {
			slog.Warn("hub: 写 server 流失败，消息丢弃", "app", env.App, "subject", env.Subject, "err", err)
			return false
		}
		return true
	}

	if env.Route == nil {
		h.live.TrackApp(env.App)
		nodes := h.live.ServerNodes(env.App)
		// nodes[:0:0] 而不是 nodes[:0]：后者的 cap 仍然是原切片的 cap，append 会
		// 在原底层数组上写，等于污染 live.ServerNodes 缓存返回的那份数据（调用方
		// 可能把它当只读快照多次复用）。[:0:0] 把 cap 也截成 0，append 第一次
		// 写入时必然重新分配，永远不会碰 nodes 的底层数组。
		candidates := nodes[:0:0]
		for _, n := range nodes {
			if n != h.nodeID {
				candidates = append(candidates, n)
			}
		}
		// rendezvous.Rank 对空 candidates 返回空切片（非 nil），下面的循环
		// 长度为 0，天然实现"候选为空则静默丢弃"，不需要额外的分支。
		env.Route = rendezvous.Rank(candidates, env.Subject)
		env.Hop = 0
	}

	for i := int(env.Hop); i < len(env.Route); i++ {
		target := env.Route[i]
		env.Hop = uint16(i + 1) // 先把游标推进到下一位再发出去，下游拿到的是"从这里继续"
		n, err := h.pub.Publish(ctx, target, env)
		if err != nil {
			slog.Warn("hub: 发布到节点失败，尝试下一候选", "target", target, "err", err)
			continue
		}
		if n > 0 {
			return true
		}
		// 订阅者数为 0：目标节点大概率已崩溃。剔出本地快照，避免自己在
		// 后续消息里反复选中同一个死节点；只影响本节点视图，不动 Redis，
		// 不广播（DropLocal 自身的理由见 registry 包）。
		h.live.DropLocal(target)
	}
	return false
}

func toResponse(env bus.Envelope) *fpimv1.ConnectResponse {
	if env.Type == bus.TypeEvt {
		var body model.EventBody
		if err := json.Unmarshal(env.Payload, &body); err != nil {
			// 不能给 kind 一个"断开"的默认值再将错就错地发出去：调用方一旦
			// 这么做，就会向 server 投一条"某条真实存在的连接断开了"的假
			// 事件，业务方会把一条其实还活着的连接从在线表里摘掉。解析
			// 失败时只记警告、返回 nil，由调用方（Deliver）跳过这次投递。
			slog.Warn("hub: 连接事件 payload 解析失败，丢弃", "app", env.App, "subject", env.Subject, "err", err)
			return nil
		}
		kind := fpimv1.EventKind_EVENT_KIND_DISCONNECTED
		if body.Kind == model.EventConnected {
			kind = fpimv1.EventKind_EVENT_KIND_CONNECTED
		}
		return &fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Event{Event: &fpimv1.Event{
			Kind: kind, Subject: env.Subject, ConnId: env.ConnID,
			Os: body.OS, Mobile: body.Mobile, Ua: body.UA, Reason: body.Reason, AtMs: body.At,
		}}}
	}
	return &fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Inbound{Inbound: &fpimv1.Inbound{
		Subject: env.Subject, ConnId: env.ConnID, Payload: env.Payload,
	}}}
}
