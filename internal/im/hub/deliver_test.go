package hub

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/rendezvous"
)

func up(subject string) bus.Envelope {
	return bus.Envelope{Type: bus.TypeUp, App: "a1", Subject: subject, ConnID: "c1", Payload: []byte(`{"m":1}`)}
}

func TestDeliverLocalStreamWins(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-a", "im-b"}
	s := &fakeStream{}
	h.AddStream(context.Background(), "a1", s)
	if !h.Deliver(context.Background(), up("u:1")) {
		t.Fatal("本地有流应投递成功")
	}
	if len(s.got) != 1 || s.got[0].GetInbound() == nil || s.got[0].GetInbound().Subject != "u:1" {
		t.Fatalf("本地有流必须本地直投，实际 %+v", s.got)
	}
	if len(pub.sent) != 0 {
		t.Fatal("本地直投不该发 Redis")
	}
}

// TestDeliverAdvancesToNextCandidateWhenFirstHasNoSubscriber 覆盖问题 1 的核心
// 修复：首个候选发布后返回 0（没有订阅者，代表大概率已崩溃），Deliver 必须
// 沿候选列表往下试，而不是发出去就不管。
func TestDeliverAdvancesToNextCandidateWhenFirstHasNoSubscriber(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-b", "im-c"}
	ranked := rendezvous.Rank([]string{"im-b", "im-c"}, "u:1")
	first, second := ranked[0], ranked[1]
	pub.subs = map[string]int64{first: 0, second: 1}

	if !h.Deliver(context.Background(), up("u:1")) {
		t.Fatal("第二候选还有订阅者，应投递成功")
	}
	if len(pub.sent) != 2 {
		t.Fatalf("应该先试 first 再试 second，共发布 2 次，实际 %d 次：%+v", len(pub.sent), pub.sent)
	}
	if pub.sent[0].node != first || pub.sent[1].node != second {
		t.Fatalf("发布顺序应严格按 rendezvous 排序推进：%+v", pub.sent)
	}
}

// TestDeliverDropsLocalOnZeroSubscribers 覆盖"收到 0 之后要 DropLocal"：
// 转发时发现目标没有订阅者，必须把它从本地快照里剔除，避免后续消息反复
// 选中同一个已经崩溃的节点。
func TestDeliverDropsLocalOnZeroSubscribers(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-b", "im-c"}
	ranked := rendezvous.Rank([]string{"im-b", "im-c"}, "u:1")
	first := ranked[0]
	pub.subs = map[string]int64{first: 0}

	h.Deliver(context.Background(), up("u:1"))

	live.mu.Lock()
	dropped := append([]string(nil), live.dropped...)
	live.mu.Unlock()
	if len(dropped) != 1 || dropped[0] != first {
		t.Fatalf("应该对第一个无订阅者的候选调用 DropLocal，实际 %v", dropped)
	}
}

// TestDeliverExhaustsRouteAndFails 覆盖"沿列表走完仍失败"：所有候选都返回
// 0，Deliver 必须静默返回 false，不能 panic，也不能凭空再造一个候选出来。
func TestDeliverExhaustsRouteAndFails(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-b", "im-c"}
	ranked := rendezvous.Rank([]string{"im-b", "im-c"}, "u:1")
	pub.subs = map[string]int64{ranked[0]: 0, ranked[1]: 0}

	ok := h.Deliver(context.Background(), up("u:1"))
	if ok {
		t.Fatal("全部候选都没有订阅者时应返回 false")
	}
	if len(pub.sent) != 2 {
		t.Fatalf("应该试完全部候选，实际发布 %d 次", len(pub.sent))
	}
}

// TestDeliverNoServerAnywhereDropsSilently 覆盖"全网无 server"：候选列表为
// 空，Deliver 直接返回 false，不发布任何东西。
func TestDeliverNoServerAnywhereDropsSilently(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = nil
	if h.Deliver(context.Background(), up("u:1")) {
		t.Fatal("全网无 server 应返回 false")
	}
	if len(pub.sent) != 0 {
		t.Fatal("全网无 server 不该发布任何信封")
	}
}

func TestDeliverSameSubjectSameLocalStream(t *testing.T) {
	h, _, _, _ := newHub(t)
	s1, s2 := &fakeStream{}, &fakeStream{}
	h.AddStream(context.Background(), "a1", s1)
	h.AddStream(context.Background(), "a1", s2)
	for i := 0; i < 10; i++ {
		h.Deliver(context.Background(), up("u:1"))
	}
	if !(len(s1.got) == 10 && len(s2.got) == 0) && !(len(s1.got) == 0 && len(s2.got) == 10) {
		t.Fatalf("同 subject 的消息必须落在同一条本地流：s1=%d s2=%d", len(s1.got), len(s2.got))
	}
}

func TestDeliverExcludesSelfWhenForwarding(t *testing.T) {
	// 自己在 srv 表里（缓存里还没来得及删）但本地已无流：不能发给自己
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-a", "im-b"}
	h.Deliver(context.Background(), up("u:1"))
	if len(pub.sent) != 1 || pub.sent[0].node != "im-b" {
		t.Fatalf("应排除自己转发给 im-b，实际 %+v", pub.sent)
	}
}

// TestDeliverComputesRouteOnceAtOrigin 覆盖"候选列表只在首发节点算一次"：
// 首发节点算出 Route 之后，下游节点（这里用同一个 Hub 模拟"收到带 Route 的
// 转发信封"）必须直接沿用，即使此时 live.servers 的快照已经变了，也不能
// 重算——重算会让不同节点在拓扑不一致时各自算出不同顺序，消息在节点间打转。
func TestDeliverComputesRouteOnceAtOrigin(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-b", "im-c"}
	pub.subs = map[string]int64{"im-b": 0, "im-c": 0} // 让首发这次必然转发到 Route 里

	h.Deliver(context.Background(), up("u:1"))
	if len(pub.sent) == 0 {
		t.Fatal("应该发生至少一次转发")
	}
	origRoute := append([]string(nil), pub.sent[0].env.Route...)
	if len(origRoute) == 0 {
		t.Fatal("首发节点必须把算出的候选列表写进 Route")
	}

	// 模拟拓扑发生变化：换一套完全不同的存活节点集合。
	live.servers["a1"] = []string{"im-x", "im-y", "im-z"}
	pub.sent = nil

	// 用首发算出的信封（带着 Hop 与 Route）模拟"下游节点收到转发信封后继续
	// 处理"：这里直接把最后一次发布出去的信封重新喂给 Deliver。
	forwarded := bus.Envelope{Type: bus.TypeUp, App: "a1", Subject: "u:1", ConnID: "c1", Payload: []byte(`{"m":1}`), Route: origRoute, Hop: 1}
	h.Deliver(context.Background(), forwarded)

	for _, s := range pub.sent {
		if s.node == "im-x" || s.node == "im-y" || s.node == "im-z" {
			t.Fatalf("下游不该按新拓扑重算候选，应该沿用原 Route，实际发布到了 %s", s.node)
		}
	}
}

// TestDeliverRouteOrderStableAcrossCalls 覆盖"同一 subject 在拓扑不变时，
// 候选顺序完全一致"：顺序不稳定会让同一个 client 的消息在不同流之间跳，
// 破坏顺序性。
func TestDeliverRouteOrderStableAcrossCalls(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-b", "im-c", "im-d"}
	pub.subs = map[string]int64{"im-b": 0, "im-c": 0, "im-d": 0} // 强制走完整个 Route，把顺序暴露在 pub.sent 里

	var first []string
	for i := 0; i < 5; i++ {
		pub.sent = nil
		h.Deliver(context.Background(), up("u:1"))
		var order []string
		for _, s := range pub.sent {
			order = append(order, s.node)
		}
		if i == 0 {
			first = order
			continue
		}
		if len(order) != len(first) {
			t.Fatalf("第 %d 次候选顺序长度变了：%v vs %v", i, first, order)
		}
		for j := range order {
			if order[j] != first[j] {
				t.Fatalf("第 %d 次候选顺序变了：%v vs %v", i, first, order)
			}
		}
	}
}
