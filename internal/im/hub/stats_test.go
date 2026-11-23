package hub_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
)

func TestStatsCountConnsRejectsAndPushes(t *testing.T) {
	h, reg, _, _, _ := hubtest.NewHub()
	ctx := context.Background()
	c1 := &hubtest.Conn{ConnID: "c1"}
	h.AddConn(ctx, "a1", model.User("1"), c1, model.ConnMeta{Node: "im-a"}, "")
	// 同一条连接重复 AddConn 不算新连接。
	h.AddConn(ctx, "a1", model.User("1"), c1, model.ConnMeta{Node: "im-a"}, "")
	h.AddConn(ctx, "a1", model.User("2"), &hubtest.Conn{ConnID: "c2"}, model.ConnMeta{Node: "im-a"}, "")
	h.RemoveConn(ctx, "a1", model.User("2"), "c2", "bye")
	// 重复删除不算两次断开。
	h.RemoveConn(ctx, "a1", model.User("2"), "c2", "bye")
	h.NoteRejected()

	h.Push(ctx, "a1", model.User("1"), []byte(`1`)) // 本地命中
	h.Push(ctx, "a1", model.User("9"), []byte(`1`)) // 不在线
	reg.LookupErr = errors.New("redis down")
	h.Push(ctx, "a1", model.User("8"), []byte(`1`)) // 查表失败

	got := h.StatsSnapshot()
	want := hub.StatsSnapshot{Online: 1, Opened: 2, Closed: 1, Rejected: 1, PushSent: 1, PushOffline: 1, PushFail: 1}
	if got != want {
		t.Fatalf("快照 = %+v, want %+v", got, want)
	}
}

func TestStatsSnapshotSub(t *testing.T) {
	prev := hub.StatsSnapshot{Online: 5, Opened: 10, Closed: 4, Rejected: 1, PushSent: 7, PushOffline: 2, PushFail: 0}
	cur := hub.StatsSnapshot{Online: 6, Opened: 13, Closed: 6, Rejected: 1, PushSent: 20, PushOffline: 3, PushFail: 1}
	got := cur.Sub(prev)
	want := hub.StatsSnapshot{Online: 6, Opened: 3, Closed: 2, Rejected: 0, PushSent: 13, PushOffline: 1, PushFail: 1}
	if got != want {
		t.Fatalf("增量 = %+v, want %+v(Online 取当前值)", got, want)
	}
}
