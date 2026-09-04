package hub_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/bus"
	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/rendezvous"
)

func up(subject string) bus.Envelope {
	return bus.Envelope{Type: bus.TypeUp, App: "a1", Subject: subject, ConnID: "c1", Payload: []byte(`{"m":1}`)}
}

func TestDeliverLocalStreamWins(t *testing.T) {
	h, _, live, pub, _ := hubtest.NewHub()
	live.Servers["a1"] = []string{"im-a", "im-b"}
	s := &hubtest.Stream{}
	h.AddStream(context.Background(), "a1", s)
	if !h.Deliver(context.Background(), up("u:1")) {
		t.Fatal("本地有流应投递成功")
	}
	if len(s.Got) != 1 || s.Got[0].GetInbound() == nil || s.Got[0].GetInbound().Subject != "u:1" {
		t.Fatalf("本地有流必须本地直投，实际 %+v", s.Got)
	}
	if len(pub.Published) != 0 {
		t.Fatal("本地直投不该发 Redis")
	}
}

// TestDeliverAdvancesToNextCandidateWhenFirstHasNoSubscriber 覆盖问题 1 的核心
// 修复：首个候选发布后返回 0（没有订阅者，代表大概率已崩溃），Deliver 必须
// 沿候选列表往下试，而不是发出去就不管。
func TestDeliverAdvancesToNextCandidateWhenFirstHasNoSubscriber(t *testing.T) {
	h, _, live, pub, _ := hubtest.NewHub()
	live.Servers["a1"] = []string{"im-b", "im-c"}
	ranked := rendezvous.Rank([]string{"im-b", "im-c"}, "u:1")
	first, second := ranked[0], ranked[1]
	pub.Subs = map[string]int64{first: 0, second: 1}

	if !h.Deliver(context.Background(), up("u:1")) {
		t.Fatal("第二候选还有订阅者，应投递成功")
	}
	if len(pub.Published) != 2 {
		t.Fatalf("应该先试 first 再试 second，共发布 2 次，实际 %d 次：%+v", len(pub.Published), pub.Published)
	}
	if pub.Published[0].Node != first || pub.Published[1].Node != second {
		t.Fatalf("发布顺序应严格按 rendezvous 排序推进：%+v", pub.Published)
	}
}

// TestDeliverDropsLocalOnZeroSubscribers 覆盖"收到 0 之后要 DropLocal"：
// 转发时发现目标没有订阅者，必须把它从本地快照里剔除，避免后续消息反复
// 选中同一个已经崩溃的节点。
func TestDeliverDropsLocalOnZeroSubscribers(t *testing.T) {
	h, _, live, pub, _ := hubtest.NewHub()
	live.Servers["a1"] = []string{"im-b", "im-c"}
	ranked := rendezvous.Rank([]string{"im-b", "im-c"}, "u:1")
	first := ranked[0]
	pub.Subs = map[string]int64{first: 0}

	h.Deliver(context.Background(), up("u:1"))

	dropped := live.DroppedSnapshot()
	if len(dropped) != 1 || dropped[0] != first {
		t.Fatalf("应该对第一个无订阅者的候选调用 DropLocal，实际 %v", dropped)
	}
}

// TestDeliverExhaustsRouteAndFails 覆盖"沿列表走完仍失败"：所有候选都返回
// 0，Deliver 必须静默返回 false，不能 panic，也不能凭空再造一个候选出来。
func TestDeliverExhaustsRouteAndFails(t *testing.T) {
	h, _, live, pub, _ := hubtest.NewHub()
	live.Servers["a1"] = []string{"im-b", "im-c"}
	ranked := rendezvous.Rank([]string{"im-b", "im-c"}, "u:1")
	pub.Subs = map[string]int64{ranked[0]: 0, ranked[1]: 0}

	ok := h.Deliver(context.Background(), up("u:1"))
	if ok {
		t.Fatal("全部候选都没有订阅者时应返回 false")
	}
	if len(pub.Published) != 2 {
		t.Fatalf("应该试完全部候选，实际发布 %d 次", len(pub.Published))
	}
}

// TestDeliverNoServerAnywhereDropsSilently 覆盖"全网无 server"：候选列表为
// 空，Deliver 直接返回 false，不发布任何东西。
func TestDeliverNoServerAnywhereDropsSilently(t *testing.T) {
	h, _, live, pub, _ := hubtest.NewHub()
	live.Servers["a1"] = nil
	if h.Deliver(context.Background(), up("u:1")) {
		t.Fatal("全网无 server 应返回 false")
	}
	if len(pub.Published) != 0 {
		t.Fatal("全网无 server 不该发布任何信封")
	}
}

func TestDeliverSameSubjectSameLocalStream(t *testing.T) {
	h, _, _, _, _ := hubtest.NewHub()
	s1, s2 := &hubtest.Stream{}, &hubtest.Stream{}
	h.AddStream(context.Background(), "a1", s1)
	h.AddStream(context.Background(), "a1", s2)
	for i := 0; i < 10; i++ {
		h.Deliver(context.Background(), up("u:1"))
	}
	if !(len(s1.Got) == 10 && len(s2.Got) == 0) && !(len(s1.Got) == 0 && len(s2.Got) == 10) {
		t.Fatalf("同 subject 的消息必须落在同一条本地流：s1=%d s2=%d", len(s1.Got), len(s2.Got))
	}
}

func TestDeliverExcludesSelfWhenForwarding(t *testing.T) {
	// 自己在 srv 表里（缓存里还没来得及删）但本地已无流：不能发给自己
	h, _, live, pub, _ := hubtest.NewHub()
	live.Servers["a1"] = []string{"im-a", "im-b"}
	h.Deliver(context.Background(), up("u:1"))
	if len(pub.Published) != 1 || pub.Published[0].Node != "im-b" {
		t.Fatalf("应排除自己转发给 im-b，实际 %+v", pub.Published)
	}
}

// TestDeliverComputesRouteOnceAtOrigin 覆盖"候选列表只在首发节点算一次"：
// 首发节点算出 Route 之后，下游节点（这里用同一个 Hub 模拟"收到带 Route 的
// 转发信封"）必须直接沿用，即使此时 live.servers 的快照已经变了，也不能
// 重算——重算会让不同节点在拓扑不一致时各自算出不同顺序，消息在节点间打转。
func TestDeliverComputesRouteOnceAtOrigin(t *testing.T) {
	h, _, live, pub, _ := hubtest.NewHub()
	live.Servers["a1"] = []string{"im-b", "im-c"}
	pub.Subs = map[string]int64{"im-b": 0, "im-c": 0} // 让首发这次必然转发到 Route 里

	h.Deliver(context.Background(), up("u:1"))
	if len(pub.Published) == 0 {
		t.Fatal("应该发生至少一次转发")
	}
	origRoute := append([]string(nil), pub.Published[0].Env.Route...)
	if len(origRoute) == 0 {
		t.Fatal("首发节点必须把算出的候选列表写进 Route")
	}

	// 模拟拓扑发生变化：换一套完全不同的存活节点集合。
	live.Servers["a1"] = []string{"im-x", "im-y", "im-z"}
	pub.Published = nil

	// 用首发算出的信封（带着 Hop 与 Route）模拟"下游节点收到转发信封后继续
	// 处理"：这里直接把最后一次发布出去的信封重新喂给 Deliver。
	forwarded := bus.Envelope{Type: bus.TypeUp, App: "a1", Subject: "u:1", ConnID: "c1", Payload: []byte(`{"m":1}`), Route: origRoute, Hop: 1}
	h.Deliver(context.Background(), forwarded)

	for _, s := range pub.Published {
		if s.Node == "im-x" || s.Node == "im-y" || s.Node == "im-z" {
			t.Fatalf("下游不该按新拓扑重算候选，应该沿用原 Route，实际发布到了 %s", s.Node)
		}
	}
}

// TestDeliverRouteOrderStableAcrossCalls 覆盖"同一 subject 在拓扑不变时，
// 候选顺序完全一致"：顺序不稳定会让同一个 client 的消息在不同流之间跳，
// 破坏顺序性。
func TestDeliverRouteOrderStableAcrossCalls(t *testing.T) {
	h, _, live, pub, _ := hubtest.NewHub()
	live.Servers["a1"] = []string{"im-b", "im-c", "im-d"}
	pub.Subs = map[string]int64{"im-b": 0, "im-c": 0, "im-d": 0} // 强制走完整个 Route，把顺序暴露在 pub.Published 里

	var first []string
	for i := 0; i < 5; i++ {
		pub.Published = nil
		h.Deliver(context.Background(), up("u:1"))
		var order []string
		for _, s := range pub.Published {
			order = append(order, s.Node)
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

// TestDeliverRouteLargerThan255DoesNotWrapAround 是"游标溢出"缺陷的回归
// 测试。复审实测过：Hop 若是 uint8，候选数达到 256 时第 256 次发布会把
// Hop 从 255 加到 256、回绕成 0，下游会把游标读成"从 Route[0] 重新开始"，
// 导致整条候选列表被反复重试，消息在节点间打转，还会把同一条消息重复
// 投给已经成功接收过的那条 server 流。
//
// 这里构造 300 个候选节点，只有最后一个（按 rendezvous 顺序排到最后的那个）
// 返回非 0 订阅者数，逼着 Deliver 把整条列表试完。用一个后台 goroutine 加
// 超时保护：如果游标真的回绕，循环会永不终止，测试会在超时后失败而不是
// 挂起整个测试进程。
func TestDeliverRouteLargerThan255DoesNotWrapAround(t *testing.T) {
	h, _, live, pub, _ := hubtest.NewHub()

	const n = 300
	nodes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, fmt.Sprintf("im-node-%03d", i))
	}
	live.Servers["a1"] = nodes

	ranked := rendezvous.Rank(nodes, "u:1")
	last := ranked[len(ranked)-1]
	subs := make(map[string]int64, len(ranked))
	for _, node := range ranked {
		subs[node] = 0
	}
	subs[last] = 1 // 只有排在最后的候选有订阅者，逼着走完整条 Route
	pub.Subs = subs

	done := make(chan bool, 1)
	go func() {
		done <- h.Deliver(context.Background(), up("u:1"))
	}()

	select {
	case ok := <-done:
		if !ok {
			t.Fatal("最后一个候选有订阅者，Deliver 应该返回 true")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Deliver 3 秒内没有返回：游标很可能回绕，陷入了对候选列表的无限重试")
	}

	if len(pub.Published) != len(ranked) {
		t.Fatalf("应该恰好把全部 %d 个候选试一遍（每个只试一次），实际发布了 %d 次：可能因为游标回绕重复尝试了同一个候选", len(ranked), len(pub.Published))
	}
	seen := map[string]int{}
	for _, s := range pub.Published {
		seen[s.Node]++
	}
	for node, cnt := range seen {
		if cnt != 1 {
			t.Fatalf("候选 %s 被发布了 %d 次，游标应该只增不减，每个候选只应该被试一次", node, cnt)
		}
	}
	for i, node := range ranked {
		if pub.Published[i].Node != node {
			t.Fatalf("发布顺序在第 %d 位应该是 %s，实际是 %s：候选顺序被打乱", i, node, pub.Published[i].Node)
		}
		// 关键断言：写进信封、真正发到网络上的 Hop 必须等于 i+1（下一跳
		// 该从哪里继续），不能因为字段宽度不够而回绕。i=255 时 i+1=256，
		// 这正是 uint8 会在这里绕回 0 的临界点——加宽到 uint16 之前，这个
		// 断言在这个用例上会在 i=255 处失败。
		if int(pub.Published[i].Env.Hop) != i+1 {
			t.Fatalf("第 %d 次发布的信封里 Hop 应该是 %d，实际是 %d：游标回绕了", i, i+1, pub.Published[i].Env.Hop)
		}
	}
}
