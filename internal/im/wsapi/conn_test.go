package wsapi

import (
	"testing"

	"github.com/basicfu/fp/internal/im/model"
)

func TestWsConnSendAfterCloseNotDelivered(t *testing.T) {
	c := newWsConn("c1", "t", nil, 4)
	if !c.Send([]byte(`1`)) {
		t.Fatal("未关闭时应入队成功")
	}
	c.Close(model.CloseKicked, model.ReasonReplaced)
	if c.Send([]byte(`2`)) || c.enqueue(pongFrame) {
		t.Fatal("关闭后不应再报投递成功")
	}
	if code, reason, _ := c.closure(); code != model.CloseKicked || reason != model.ReasonReplaced {
		t.Fatalf("关闭原因不应被改写：%d %s", code, reason)
	}
}
