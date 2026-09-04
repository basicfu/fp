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
// 本地有流 → 本地直投；否则按 subject 做 rendezvous 转一跳；已转过一跳还没有流 → 静默丢弃。
// 静默是有意的：server 的故障对 client 不可见，client 靠自己的超时重发。
func (h *Hub) Deliver(ctx context.Context, env bus.Envelope) {
	if s, ok := h.localStream(env.App, env.Subject); ok {
		if err := s.Send(toResponse(env)); err != nil {
			slog.Warn("hub: 写 server 流失败，消息丢弃", "app", env.App, "subject", env.Subject, "err", err)
		}
		return
	}
	if env.Hops >= 1 {
		return
	}
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
	target, ok := rendezvous.Pick(candidates, env.Subject)
	if !ok {
		return
	}
	env.Hops++
	if err := h.pub.Publish(ctx, target, env); err != nil {
		slog.Warn("hub: 转发到节点失败，消息丢弃", "target", target, "err", err)
	}
}

func toResponse(env bus.Envelope) *fpimv1.ConnectResponse {
	if env.Type == bus.TypeEvt {
		var body model.EventBody
		_ = json.Unmarshal(env.Payload, &body)
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
