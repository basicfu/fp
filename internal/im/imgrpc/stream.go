package imgrpc

import (
	"context"
	"fmt"
	"sync"

	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/model"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fpim/v1"
)

// maxSessionsMany 限制一次批量查询的 subject 数：再多就是一条巨大的 Redis 管道，
// 拖慢同节点其它请求；业务方分批调用即可。
const maxSessionsMany = 1000

// sender 给 gRPC 流加锁：gRPC 不允许并发 Send，而 hub 会从多个协程往同一条流写
// （处理请求的多个协程各自回 Result，同时路由器可能在别的协程往这条流写
// Inbound/Event）。这层锁是保证同一时刻只有一个协程在发的唯一手段。
type sender struct {
	mu sync.Mutex
	s  fpimv1.ImService_ConnectServer
}

func (s *sender) Send(r *fpimv1.ConnectResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s.Send(r)
}

// handle 执行一条请求并生成 Result。subject 非法或 body 未知都变成 Result.Error，
// 不断流：一条坏请求不该让整条流上其它请求跟着死。
func handle(ctx context.Context, h *hub.Hub, app string, req *fpimv1.ConnectRequest) *fpimv1.ConnectResponse {
	res := &fpimv1.Result{ReqId: req.GetReqId()}
	fail := func(msg string) *fpimv1.ConnectResponse {
		res.Error = msg
		return &fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Result{Result: res}}
	}
	switch b := req.GetBody().(type) {
	case *fpimv1.ConnectRequest_Push:
		sub, err := model.ParseSubject(b.Push.GetSubject())
		if err != nil {
			return fail(err.Error())
		}
		res.Pushes = []*fpimv1.PushResult{toProto(h.Push(ctx, app, sub, b.Push.GetPayload()))}
	case *fpimv1.ConnectRequest_PushMany:
		subs := make([]model.Subject, 0, len(b.PushMany.GetSubjects()))
		for _, s := range b.PushMany.GetSubjects() {
			sub, err := model.ParseSubject(s)
			if err != nil {
				return fail(err.Error())
			}
			subs = append(subs, sub)
		}
		for _, r := range h.PushMany(ctx, app, subs, b.PushMany.GetPayload()) {
			res.Pushes = append(res.Pushes, toProto(r))
		}
	case *fpimv1.ConnectRequest_Kick:
		sub, err := model.ParseSubject(b.Kick.GetSubject())
		if err != nil {
			return fail(err.Error())
		}
		if err := h.Kick(ctx, app, sub, b.Kick.GetConnIds()...); err != nil {
			return fail(err.Error())
		}
	case *fpimv1.ConnectRequest_Sessions:
		sub, err := model.ParseSubject(b.Sessions.GetSubject())
		if err != nil {
			return fail(err.Error())
		}
		list, err := h.Sessions(ctx, app, sub)
		if err != nil {
			return fail(err.Error())
		}
		for _, s := range list {
			res.Sessions = append(res.Sessions, &fpimv1.Session{ConnId: s.ConnID, NodeId: s.Node, Os: s.OS, Mobile: s.Mobile, ConnectedAtMs: s.ConnectedAt})
		}
	case *fpimv1.ConnectRequest_SessionsMany:
		raw := b.SessionsMany.GetSubjects()
		if len(raw) > maxSessionsMany {
			return fail(fmt.Sprintf("一次最多查 %d 个 subject", maxSessionsMany))
		}
		subs := make([]model.Subject, 0, len(raw))
		for _, s := range raw {
			sub, err := model.ParseSubject(s)
			if err != nil {
				return fail(err.Error())
			}
			subs = append(subs, sub)
		}
		many, err := h.SessionsMany(ctx, app, subs)
		if err != nil {
			return fail(err.Error())
		}
		for i, list := range many {
			ss := &fpimv1.SubjectSessions{Subject: raw[i]}
			for _, s := range list {
				ss.Sessions = append(ss.Sessions, &fpimv1.Session{ConnId: s.ConnID, NodeId: s.Node, Os: s.OS, Mobile: s.Mobile, ConnectedAtMs: s.ConnectedAt})
			}
			res.SessionsMany = append(res.SessionsMany, ss)
		}
	case *fpimv1.ConnectRequest_Ping:
		// 心跳只要一帧真正穿过代理的应答,空 Result 就够了。
	default:
		return fail("未知请求类型")
	}
	return &fpimv1.ConnectResponse{Body: &fpimv1.ConnectResponse_Result{Result: res}}
}

func toProto(r hub.PushResult) *fpimv1.PushResult {
	// 兜底到 UNAVAILABLE 而不是 UNSPECIFIED：hub.PushResult.Status 只有三个
	// 取值（PushSent/PushNotOnline/PushUnavailable，定义在 push.go），default
	// 分支对应的正是 hub.PushUnavailable 这个第三种取值，不是"遇到了未知
	// 状态"的防御性兜底，是有意为之的正常映射。
	st := fpimv1.PushStatus_PUSH_STATUS_UNAVAILABLE
	switch r.Status {
	case hub.PushSent:
		st = fpimv1.PushStatus_PUSH_STATUS_SENT
	case hub.PushNotOnline:
		st = fpimv1.PushStatus_PUSH_STATUS_NOT_ONLINE
	}
	return &fpimv1.PushResult{Subject: r.Subject, Status: st, Nodes: int32(r.Nodes)}
}
