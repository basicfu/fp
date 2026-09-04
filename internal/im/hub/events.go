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
// 返回 Deliver 的投递结果：AddConn 需要记下"建立事件是否成功投递"，供之后
// 可能到来的 RemoveConn 判断要不要发断开事件（C2）。
func (h *Hub) emit(ctx context.Context, app string, sub model.Subject, connID string, body model.EventBody) bool {
	if body.At == 0 {
		body.At = nowMs()
	}
	payload, _ := json.Marshal(body)
	return h.Deliver(ctx, bus.Envelope{Type: bus.TypeEvt, App: app, Subject: sub.String(), ConnID: connID, Payload: payload})
}
