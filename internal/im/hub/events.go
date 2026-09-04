package hub

import (
	"context"
	"encoding/json"
	"time"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
)

func nowMs() int64 { return time.Now().UnixMilli() }

// emit 把连接事件当成一条 Evt 信封走 Deliver，路由规则与 client 消息完全一样。
func (h *Hub) emit(ctx context.Context, app string, sub model.Subject, connID string, body model.EventBody) {
	if body.At == 0 {
		body.At = nowMs()
	}
	payload, _ := json.Marshal(body)
	h.Deliver(ctx, bus.Envelope{Type: bus.TypeEvt, App: app, Subject: sub.String(), ConnID: connID, Payload: payload})
}
