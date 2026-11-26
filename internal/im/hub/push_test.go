package hub_test

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
)

func TestPushLocalShortCircuitUnderSingleConnPolicy(t *testing.T) {
	h, reg, _, pub, _ := hubtest.NewHub() // a1 策略 replace
	c := &hubtest.Conn{ConnID: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	// 注册表故意放一条别的节点的条目：单连接策略下本地命中就不该再查表
	reg.Table["u:1"] = map[string]model.ConnMeta{"c9": {Node: "im-b"}}
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != hub.PushSent || res.Nodes != 1 {
		t.Fatalf("本地命中应 Sent{1}，实际 %+v", res)
	}
	if len(c.Sent) != 1 || len(pub.Published) != 0 {
		t.Fatalf("本地直投且不发 Redis：sent=%d pub=%d", len(c.Sent), len(pub.Published))
	}
}

func TestPushUnderLimitPolicyStillFansOutToOtherNodes(t *testing.T) {
	// 这条测试需要 limit 策略而不是 hubtest.NewHub() 默认的 replace，用
	// NewHubWithApps 在构造时就把 apps 配好，而不是构造后去改
	// hub.Hub 的未导出 apps 字段（那样做在 hub 包外部根本编译不过）。
	apps := hubtest.Apps{"a1": {AppID: "a1", ConnPolicy: model.PolicyLimit, ConnLimit: 3}}
	h, reg, _, pub := hubtest.NewHubWithApps(apps)
	c := &hubtest.Conn{ConnID: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), c, model.ConnMeta{Node: "im-a"}, "")
	reg.Table["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-a"}, "c2": {Node: "im-b"}, "c3": {Node: "im-b"}}
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != hub.PushSent || res.Nodes != 2 {
		t.Fatalf("本地 1 + im-b 1 = Sent{2}，实际 %+v", res)
	}
	if len(pub.Published) != 1 || pub.Published[0].Node != "im-b" || pub.Published[0].Env.Type != bus.TypeMsg {
		t.Fatalf("应只喊 im-b 一次且排除自己，实际 %+v", pub.Published)
	}
}

func TestPushNotOnline(t *testing.T) {
	h, _, _, pub, _ := hubtest.NewHub()
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != hub.PushNotOnline || len(pub.Published) != 0 {
		t.Fatalf("无连接应 NotOnline 且不喊，实际 %+v %+v", res, pub.Published)
	}
}

func TestPushManyOneLookup(t *testing.T) {
	h, reg, _, pub, _ := hubtest.NewHub()
	reg.Table["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b"}}
	reg.Table["u:2"] = map[string]model.ConnMeta{"c2": {Node: "im-c"}, "c3": {Node: "im-b"}}
	res := h.PushMany(context.Background(), "a1", []model.Subject{model.User("1"), model.User("2"), model.User("3")}, []byte(`1`))
	if len(res) != 3 || res[0].Status != hub.PushSent || res[1].Status != hub.PushSent || res[1].Nodes != 2 || res[2].Status != hub.PushNotOnline {
		t.Fatalf("PushMany 结果不对：%+v", res)
	}
	if len(pub.Published) != 3 {
		t.Fatalf("u:1→im-b、u:2→im-b、u:2→im-c 共 3 次喊，实际 %d", len(pub.Published))
	}
	// PushMany 存在的理由就是把多个主体的查询合并成一次注册表往返：只看
	// 最终结果和发布次数保护不了这条性质——退化成逐主体循环查询（甚至
	// 退化成循环调 Push）时，结果和发布次数完全不变，唯独往返次数会从
	// 1 涨到 3。必须直接断言调用次数。
	if reg.LookupManyCalls != 1 {
		t.Fatalf("PushMany 必须把 3 个主体合并成一次注册表查询，退化成逐个查询会让每次批量推送多花 N 次往返；实际调用了 %d 次", reg.LookupManyCalls)
	}
}

// TestPushManyExcludesDeadNodeFromCount 覆盖 Publish 返回订阅者数为 0 的情形：
// 目标节点已经崩溃（SPUBLISH 无人订阅），既不能把它算进 Nodes——那会让业务
// 方误以为真的送达了——也要调用 DropLocal 把它从本节点的存活快照里剔除，
// 避免下一次 PushMany 再选中这个死节点白打一次 Redis 请求。
func TestPushManyExcludesDeadNodeFromCount(t *testing.T) {
	h, reg, live, pub, _ := hubtest.NewHub()
	reg.Table["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b"}, "c2": {Node: "im-c"}}
	pub.Subs = map[string]int64{"im-b": 0}
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != hub.PushSent || res.Nodes != 1 {
		t.Fatalf("im-b 已崩溃应被剔除、只算 im-c 一个，实际 %+v", res)
	}
	found := false
	for _, n := range live.DroppedSnapshot() {
		if n == "im-b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应调用 DropLocal(\"im-b\")，实际 dropped=%v", live.DroppedSnapshot())
	}
}

// TestPushManyAllDeadIsNotOnline 覆盖：全部目标节点都返回 0 订阅者、本地也没
// 投出去时，结果必须是 NotOnline 而不是 Sent{0}——后者会让业务方拿到一个
// "已送达"的假象。
func TestPushManyAllDeadIsNotOnline(t *testing.T) {
	h, reg, _, pub, _ := hubtest.NewHub()
	reg.Table["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b"}}
	pub.Subs = map[string]int64{"im-b": 0}
	res := h.Push(context.Background(), "a1", model.User("1"), []byte(`1`))
	if res.Status != hub.PushNotOnline {
		t.Fatalf("唯一目标节点已崩溃、本地也没投出去，应是 NotOnline，实际 %+v", res)
	}
}

func TestSessionsFromRegistry(t *testing.T) {
	h, reg, _, _, _ := hubtest.NewHub()
	reg.Table["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b", OS: "ios", Mobile: true, At: 7}}
	got, err := h.Sessions(context.Background(), "a1", model.User("1"))
	if err != nil || len(got) != 1 || got[0].ConnID != "c1" || got[0].Node != "im-b" || got[0].OS != "ios" || !got[0].Mobile || got[0].ConnectedAt != 7 {
		t.Fatalf("Sessions=%+v err=%v", got, err)
	}
}

// 多个 subject 合成一次注册表查询, 结果与入参顺序一一对应。
func TestSessionsManyOneLookup(t *testing.T) {
	h, reg, _, _, _ := hubtest.NewHub()
	reg.Table["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b", At: 7}}
	got, err := h.SessionsMany(context.Background(), "a1", []model.Subject{model.User("2"), model.User("1")})
	if err != nil || len(got) != 2 || len(got[0]) != 0 || len(got[1]) != 1 || got[1][0].ConnID != "c1" || got[1][0].ConnectedAt != 7 {
		t.Fatalf("SessionsMany=%+v err=%v", got, err)
	}
	if reg.LookupManyCalls != 1 {
		t.Fatalf("应只查一次注册表, 实际 %d 次", reg.LookupManyCalls)
	}
}

func TestKickAllLocalAndRemote(t *testing.T) {
	h, reg, _, pub, _ := hubtest.NewHub()
	local := &hubtest.Conn{ConnID: "c1"}
	h.AddConn(context.Background(), "a1", model.User("1"), local, model.ConnMeta{Node: "im-a"}, "")
	reg.Table["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-a"}, "c2": {Node: "im-b"}}
	if err := h.Kick(context.Background(), "a1", model.User("1")); err != nil {
		t.Fatal(err)
	}
	if local.Closed == nil || local.Closed.Code != model.CloseKicked || local.Closed.Reason != model.ReasonKicked {
		t.Fatalf("本地连接应直接关闭 4003/kicked，实际 %+v", local.Closed)
	}
	if len(pub.Published) != 1 || pub.Published[0].Node != "im-b" || pub.Published[0].Env.Type != bus.TypeKick || pub.Published[0].Env.ConnID != "c2" || pub.Published[0].Env.Extra != model.ReasonKicked {
		t.Fatalf("远端连接应发 KICK 信封，实际 %+v", pub.Published)
	}
}

func TestKickOneUnknownIsNoop(t *testing.T) {
	h, _, _, pub, _ := hubtest.NewHub()
	if err := h.Kick(context.Background(), "a1", model.User("1"), "nope"); err != nil {
		t.Fatal(err)
	}
	if len(pub.Published) != 0 {
		t.Fatal("不存在的 connId 不该发任何信封")
	}
}

func TestEvictReplacedRefs(t *testing.T) {
	h, _, _, pub, _ := hubtest.NewHub()
	local := &hubtest.Conn{ConnID: "old-local"}
	h.AddConn(context.Background(), "a1", model.User("1"), local, model.ConnMeta{Node: "im-a"}, "")
	h.Evict(context.Background(), "a1", model.User("1"), []registry.ConnRef{{ConnID: "old-local", Node: "im-a"}, {ConnID: "old-remote", Node: "im-c"}}, model.ReasonReplaced)
	if local.Closed == nil || local.Closed.Reason != model.ReasonReplaced {
		t.Fatalf("本地被顶替的连接应以 replaced 关闭，实际 %+v", local.Closed)
	}
	if len(pub.Published) != 1 || pub.Published[0].Node != "im-c" || pub.Published[0].Env.Extra != model.ReasonReplaced {
		t.Fatalf("远端被顶替的连接应发 KICK(replaced)，实际 %+v", pub.Published)
	}
}

// TestEvictDeadRemoteNodeIsNotAnError 覆盖：目标节点返回 0 订阅者（它已经
// 随进程崩溃，上面那条连接根本不存在了）时，Evict 不当失败处理——不需要
// 也无法再踢一个不存在的东西，记一条日志即可。
//
// 覆盖边界：Evict 没有返回值可断言，这条测试实际只验证了"针对该节点发过
// 一次 KICK 尝试"，并不能感知"收到 0 订阅者之后走的是哪条分支"——把
// push.go 里 `if got == 0 { slog.Info(...) }` 这段特判整个删掉，这个测试
// 依然全绿。这是 Evict 返回 void 这个接口形状带来的固有局限，不是测试
// 缺陷；如果将来要真正保护这条分支，需要给 Evict 加可观察的输出（比如
// 返回值或注入的 logger）。
func TestEvictDeadRemoteNodeIsNotAnError(t *testing.T) {
	h, _, _, pub, _ := hubtest.NewHub()
	pub.Subs = map[string]int64{"im-c": 0}
	h.Evict(context.Background(), "a1", model.User("1"), []registry.ConnRef{{ConnID: "old-remote", Node: "im-c"}}, model.ReasonReplaced)
	if len(pub.Published) != 1 || pub.Published[0].Node != "im-c" {
		t.Fatalf("即使目标节点已崩溃也应该尝试发一次 KICK，实际 %+v", pub.Published)
	}
}

func TestOnRevokedClosesConnsByToken(t *testing.T) {
	h, _, _, _, _ := hubtest.NewHub()
	c1 := &hubtest.Conn{ConnID: "c1", Tok: "t1"}
	c2 := &hubtest.Conn{ConnID: "c2", Tok: "t2"}
	h.AddConn(context.Background(), "a1", model.User("1"), c1, model.ConnMeta{Node: "im-a"}, "")
	h.AddConn(context.Background(), "a1", model.User("1"), c2, model.ConnMeta{Node: "im-a"}, "")
	h.OnRevoked(context.Background(), "a1", []string{"t1", "t-unknown"})
	if c1.Closed == nil || c1.Closed.Code != model.CloseAuthFailed || c1.Closed.Reason != model.ReasonRevoked {
		t.Fatalf("持有被撤销 token 的连接应以 4001/revoked 关闭，实际 %+v", c1.Closed)
	}
	if c2.Closed != nil {
		t.Fatal("其他 token 的连接不能被误关")
	}
}

// 每条各推各的 payload,结果与入参一一对应:调用方按下标把结果对回自己的回执。
func TestPushEachAlignsResults(t *testing.T) {
	h, reg, _, pub, _ := hubtest.NewHub()
	reg.Table["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b"}}
	reg.Table["u:3"] = map[string]model.ConnMeta{"c3": {Node: "im-c"}}
	res := h.PushEach(context.Background(), "a1", []hub.PushItem{
		{Sub: model.User("1"), Payload: []byte(`1`)},
		{Sub: model.User("2"), Payload: []byte(`2`)},
		{Sub: model.User("3"), Payload: []byte(`3`)},
	})
	if len(res) != 3 || res[0].Subject != "u:1" || res[0].Status != hub.PushSent ||
		res[1].Subject != "u:2" || res[1].Status != hub.PushNotOnline ||
		res[2].Subject != "u:3" || res[2].Status != hub.PushSent {
		t.Fatalf("PushEach 结果不对：%+v", res)
	}
	if len(pub.Published) != 2 {
		t.Fatalf("u:1→im-b、u:3→im-c 共 2 次喊，实际 %+v", pub.Published)
	}
	for _, p := range pub.Published {
		if want := map[string]string{"im-b": "1", "im-c": "3"}[p.Node]; string(p.Env.Payload) != want {
			t.Fatalf("发给 %s 的应是自己那条的 payload %q，实际 %q", p.Node, want, p.Env.Payload)
		}
	}
}

// 与 PushMany 同理:批量推送存在的意义就是把查表合成一次往返。
func TestPushEachSingleLookup(t *testing.T) {
	h, reg, _, _, _ := hubtest.NewHub()
	reg.Table["u:1"] = map[string]model.ConnMeta{"c1": {Node: "im-b"}}
	h.PushEach(context.Background(), "a1", []hub.PushItem{
		{Sub: model.User("1"), Payload: []byte(`1`)},
		{Sub: model.User("2"), Payload: []byte(`2`)},
		{Sub: model.User("3"), Payload: []byte(`3`)},
	})
	if reg.LookupManyCalls != 1 {
		t.Fatalf("3 条应合并成一次注册表查询，实际 %d 次", reg.LookupManyCalls)
	}
}

// TestPushSkipsKickedConnAwaitingTeardown 复现切网重连丢推送：replace 下新连接在
// im-b 握手、给 im-a 发了 KICK，im-a 的旧连接已 Close 但读循环还没退出（对端已死，
// 等关闭握手超时），条目仍在连接表里。此时从 im-a Push 不能把旧条目当成唯一命中，
// 必须查注册表喊到 im-b。
func TestPushSkipsKickedConnAwaitingTeardown(t *testing.T) {
	ctx := context.Background()
	hA, reg, live, pubA, apps := hubtest.NewHub()
	hB := hub.New("im-b", reg, live, &hubtest.Pub{}, apps)
	sub := model.User("1")

	old := &hubtest.Conn{ConnID: "old"}
	hA.AddConn(ctx, "a1", sub, old, model.ConnMeta{Node: "im-a"}, "")
	fresh := &hubtest.Conn{ConnID: "new"}
	hB.AddConn(ctx, "a1", sub, fresh, model.ConnMeta{Node: "im-b"}, "")
	// 握手脚本已把旧条目换成新条目
	reg.Table["u:1"] = map[string]model.ConnMeta{"new": {Node: "im-b"}}

	hA.HandleEnvelope(ctx, bus.Envelope{Type: bus.TypeKick, App: "a1", Subject: "u:1", ConnID: "old", Extra: model.ReasonReplaced})
	if old.Closed == nil || hA.LocalConnCount() != 1 {
		t.Fatalf("前置：旧连接应已 Close 但仍在连接表，closed=%v count=%d", old.Closed, hA.LocalConnCount())
	}

	res := hA.Push(ctx, "a1", sub, []byte(`1`))
	if res.Status != hub.PushSent || res.Nodes != 1 {
		t.Fatalf("应喊到 im-b 得 Sent{1}，实际 %+v", res)
	}
	if old.SentCount() != 0 || old.Closed.Reason != model.ReasonReplaced {
		t.Fatalf("旧连接不该再收消息、关闭原因不该被改写：sent=%d closed=%+v", old.SentCount(), old.Closed)
	}
	if len(pubA.Published) != 1 || pubA.Published[0].Node != "im-b" {
		t.Fatalf("应向 im-b 发一次 MSG，实际 %+v", pubA.Published)
	}
	hB.HandleEnvelope(ctx, pubA.Published[0].Env)
	if fresh.SentCount() != 1 {
		t.Fatalf("im-b 的新连接应收到 1 条，实际 %d", fresh.SentCount())
	}
}

// TestPushSameNodeReplaceDeliversToNewConn 同节点顶号：新旧连接并存于连接表，
// 旧的已被 Close，消息只投新连接，且仍走本地短路不查表。
func TestPushSameNodeReplaceDeliversToNewConn(t *testing.T) {
	ctx := context.Background()
	h, reg, _, pub, _ := hubtest.NewHub()
	sub := model.User("1")
	old := &hubtest.Conn{ConnID: "old"}
	fresh := &hubtest.Conn{ConnID: "new"}
	h.AddConn(ctx, "a1", sub, old, model.ConnMeta{Node: "im-a"}, "")
	h.AddConn(ctx, "a1", sub, fresh, model.ConnMeta{Node: "im-a"}, "")
	h.Evict(ctx, "a1", sub, []registry.ConnRef{{ConnID: "old", Node: "im-a"}}, model.ReasonReplaced)

	res := h.Push(ctx, "a1", sub, []byte(`1`))
	if res.Status != hub.PushSent || res.Nodes != 1 {
		t.Fatalf("应 Sent{1}，实际 %+v", res)
	}
	if fresh.SentCount() != 1 || old.SentCount() != 0 {
		t.Fatalf("只投新连接：new=%d old=%d", fresh.SentCount(), old.SentCount())
	}
	if len(pub.Published) != 0 || reg.LookupManyCalls != 0 {
		t.Fatalf("本地命中应短路：pub=%d lookup=%d", len(pub.Published), reg.LookupManyCalls)
	}
}

// TestPushOnlyKickedLocalConnIsNotOnline 本地只剩已关闭的旧连接、注册表里也没有别的：
// 不能报 Sent。
func TestPushOnlyKickedLocalConnIsNotOnline(t *testing.T) {
	ctx := context.Background()
	h, _, _, _, _ := hubtest.NewHub()
	old := &hubtest.Conn{ConnID: "old"}
	h.AddConn(ctx, "a1", model.User("1"), old, model.ConnMeta{Node: "im-a"}, "")
	old.Close(model.CloseKicked, model.ReasonKicked)
	if res := h.Push(ctx, "a1", model.User("1"), []byte(`1`)); res.Status != hub.PushNotOnline {
		t.Fatalf("应 NotOnline，实际 %+v", res)
	}
}
