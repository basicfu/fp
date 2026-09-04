package hub

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
)

func TestPushLocalShortCircuitUnderSingleConnPolicy(t *testing.T) {
	h, reg, _, pub := newHub(t) // a1 策略 replace
	c := &fakeConn{id: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	// 注册表故意放一条别的节点的条目：单连接策略下本地命中就不该再查表
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c9": {Node: "im-b"}}
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != PushSent || res.Nodes != 1 {
		t.Fatalf("本地命中应 Sent{1}，实际 %+v", res)
	}
	if len(c.sent) != 1 || len(pub.sent) != 0 {
		t.Fatalf("本地直投且不发 Redis：sent=%d pub=%d", len(c.sent), len(pub.sent))
	}
}

func TestPushUnderLimitPolicyStillFansOutToOtherNodes(t *testing.T) {
	h, reg, _, pub := newHub(t)
	h.apps = fakeApps{"a1": {AppID: "a1", AppSecret: "s", ConnPolicy: model.PolicyLimit, ConnLimit: 3}}
	c := &fakeConn{id: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-a"}, "c2": {Node: "im-b"}, "c3": {Node: "im-b"}}
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != PushSent || res.Nodes != 2 {
		t.Fatalf("本地 1 + im-b 1 = Sent{2}，实际 %+v", res)
	}
	if len(pub.sent) != 1 || pub.sent[0].node != "im-b" || pub.sent[0].env.Type != bus.TypeMsg {
		t.Fatalf("应只喊 im-b 一次且排除自己，实际 %+v", pub.sent)
	}
}

func TestPushNotOnline(t *testing.T) {
	h, _, _, pub := newHub(t)
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != PushNotOnline || len(pub.sent) != 0 {
		t.Fatalf("无连接应 NotOnline 且不喊，实际 %+v %+v", res, pub.sent)
	}
}

func TestPushManyOneLookup(t *testing.T) {
	h, reg, _, pub := newHub(t)
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b"}}
	reg.lookup["u:2"] = map[string]model.ConnMeta{"c2": {Node: "im-c"}, "c3": {Node: "im-b"}}
	res := h.PushMany(context.Background(), "a1", []model.Subject{model.User("1"), model.User("2"), model.User("3")}, []byte(`1`))
	if len(res) != 3 || res[0].Status != PushSent || res[1].Status != PushSent || res[1].Nodes != 2 || res[2].Status != PushNotOnline {
		t.Fatalf("PushMany 结果不对：%+v", res)
	}
	if len(pub.sent) != 3 {
		t.Fatalf("u:1→im-b、u:2→im-b、u:2→im-c 共 3 次喊，实际 %d", len(pub.sent))
	}
}

// TestPushManyExcludesDeadNodeFromCount 覆盖 Publish 返回订阅者数为 0 的情形：
// 目标节点已经崩溃（SPUBLISH 无人订阅），既不能把它算进 Nodes——那会让业务
// 方误以为真的送达了——也要调用 DropLocal 把它从本节点的存活快照里剔除，
// 避免下一次 PushMany 再选中这个死节点白打一次 Redis 请求。
func TestPushManyExcludesDeadNodeFromCount(t *testing.T) {
	h, reg, live, pub := newHub(t)
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b"}, "c2": {Node: "im-c"}}
	pub.subs = map[string]int64{"im-b": 0}
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != PushSent || res.Nodes != 1 {
		t.Fatalf("im-b 已崩溃应被剔除、只算 im-c 一个，实际 %+v", res)
	}
	found := false
	for _, n := range live.dropped {
		if n == "im-b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应调用 DropLocal(\"im-b\")，实际 dropped=%v", live.dropped)
	}
}

// TestPushManyAllDeadIsNotOnline 覆盖：全部目标节点都返回 0 订阅者、本地也没
// 投出去时，结果必须是 NotOnline 而不是 Sent{0}——后者会让业务方拿到一个
// "已送达"的假象。
func TestPushManyAllDeadIsNotOnline(t *testing.T) {
	h, reg, _, pub := newHub(t)
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b"}}
	pub.subs = map[string]int64{"im-b": 0}
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != PushNotOnline {
		t.Fatalf("唯一目标节点已崩溃、本地也没投出去，应是 NotOnline，实际 %+v", res)
	}
}

func TestSessionsFromRegistry(t *testing.T) {
	h, reg, _, _ := newHub(t)
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b", OS: "ios", Mobile: true, At: 7}}
	got, err := h.Sessions(context.Background(), "a1", model.User("1"))
	if err != nil || len(got) != 1 || got[0].ConnID != "c1" || got[0].Node != "im-b" || got[0].OS != "ios" || !got[0].Mobile || got[0].ConnectedAt != 7 {
		t.Fatalf("Sessions=%+v err=%v", got, err)
	}
}

func TestKickAllLocalAndRemote(t *testing.T) {
	h, reg, _, pub := newHub(t)
	local := &fakeConn{id: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), local, model.ConnMeta{Node: "im-a"}, "")
	reg.lookup["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-a"}, "c2": {Node: "im-b"}}
	if err := h.Kick(context.Background(), "a1", model.User("1")); err != nil {
		t.Fatal(err)
	}
	if local.closed == nil || local.closed.code != model.CloseKicked || local.closed.reason != model.ReasonKicked {
		t.Fatalf("本地连接应直接关闭 4003/kicked，实际 %+v", local.closed)
	}
	if len(pub.sent) != 1 || pub.sent[0].node != "im-b" || pub.sent[0].env.Type != bus.TypeKick || pub.sent[0].env.ConnID != "c2" || pub.sent[0].env.Extra != model.ReasonKicked {
		t.Fatalf("远端连接应发 KICK 信封，实际 %+v", pub.sent)
	}
}

func TestKickOneUnknownIsNoop(t *testing.T) {
	h, _, _, pub := newHub(t)
	if err := h.Kick(context.Background(), "a1", model.User("1"), "nope"); err != nil {
		t.Fatal(err)
	}
	if len(pub.sent) != 0 {
		t.Fatal("不存在的 connId 不该发任何信封")
	}
}

func TestEvictReplacedRefs(t *testing.T) {
	h, _, _, pub := newHub(t)
	local := &fakeConn{id: "old-local"}
	h.AddConn(context.Background(), "a1", model.User("1"), local, model.ConnMeta{Node: "im-a"}, "")
	h.Evict(context.Background(), "a1", model.User("1"), []registry.ConnRef{{ConnID: "old-local", Node: "im-a"}, {ConnID: "old-remote", Node: "im-c"}}, model.ReasonReplaced)
	if local.closed == nil || local.closed.reason != model.ReasonReplaced {
		t.Fatalf("本地被顶替的连接应以 replaced 关闭，实际 %+v", local.closed)
	}
	if len(pub.sent) != 1 || pub.sent[0].node != "im-c" || pub.sent[0].env.Extra != model.ReasonReplaced {
		t.Fatalf("远端被顶替的连接应发 KICK(replaced)，实际 %+v", pub.sent)
	}
}

// TestEvictDeadRemoteNodeIsNotAnError 覆盖：目标节点返回 0 订阅者（它已经
// 随进程崩溃，上面那条连接根本不存在了）时，Evict 不当失败处理——不需要
// 也无法再踢一个不存在的东西，记一条日志即可。
func TestEvictDeadRemoteNodeIsNotAnError(t *testing.T) {
	h, _, _, pub := newHub(t)
	pub.subs = map[string]int64{"im-c": 0}
	h.Evict(context.Background(), "a1", model.User("1"), []registry.ConnRef{{ConnID: "old-remote", Node: "im-c"}}, model.ReasonReplaced)
	if len(pub.sent) != 1 || pub.sent[0].node != "im-c" {
		t.Fatalf("即使目标节点已崩溃也应该尝试发一次 KICK，实际 %+v", pub.sent)
	}
}

func TestOnRevokedClosesConnsByToken(t *testing.T) {
	h, _, _, _ := newHub(t)
	c1 := &fakeConn{id: "c1", token: "t1"}
	c2 := &fakeConn{id: "c2", token: "t2"}
	h.AddConn(context.Background(), "a1", model.User("1"), c1, model.ConnMeta{Node: "im-a"}, "")
	h.AddConn(context.Background(), "a1", model.User("1"), c2, model.ConnMeta{Node: "im-a"}, "")
	h.OnRevoked(context.Background(), "a1", []string{"t1", "t-unknown"})
	if c1.closed == nil || c1.closed.code != model.CloseAuthFailed || c1.closed.reason != model.ReasonRevoked {
		t.Fatalf("持有被撤销 token 的连接应以 4001/revoked 关闭，实际 %+v", c1.closed)
	}
	if c2.closed != nil {
		t.Fatal("其他 token 的连接不能被误关")
	}
}
