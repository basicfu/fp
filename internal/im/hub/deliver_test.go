package hub

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/im/bus"
)

func up(subject string) bus.Envelope {
	return bus.Envelope{Type: bus.TypeUp, App: "a1", Subject: subject, ConnID: "c1", Payload: []byte(`{"m":1}`)}
}

func TestDeliverLocalStreamWins(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-a", "im-b"}
	s := &fakeStream{}
	h.AddStream(context.Background(), "a1", s)
	h.Deliver(context.Background(), up("u:1"))
	if len(s.got) != 1 || s.got[0].GetInbound() == nil || s.got[0].GetInbound().Subject != "u:1" {
		t.Fatalf("本地有流必须本地直投，实际 %+v", s.got)
	}
	if len(pub.sent) != 0 {
		t.Fatal("本地直投不该发 Redis")
	}
}

func TestDeliverForwardsOneHopByRendezvous(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-b", "im-c"}
	h.Deliver(context.Background(), up("u:1"))
	if len(pub.sent) != 1 || pub.sent[0].env.Hops != 1 || pub.sent[0].env.Type != bus.TypeUp {
		t.Fatalf("本地无流应转发一跳且 hops=1，实际 %+v", pub.sent)
	}
	first := pub.sent[0].node
	for i := 0; i < 20; i++ {
		h.Deliver(context.Background(), up("u:1"))
		if pub.sent[len(pub.sent)-1].node != first {
			t.Fatal("同 subject 必须恒选同一目标节点，否则顺序会乱")
		}
	}
}

func TestDeliverDropsAfterOneHopOrWhenNoServer(t *testing.T) {
	h, _, live, pub := newHub(t)
	live.servers["a1"] = []string{"im-b"}
	e := up("u:1")
	e.Hops = 1
	h.Deliver(context.Background(), e)
	if len(pub.sent) != 0 {
		t.Fatal("hops>=1 且本地无流必须静默丢弃，不能再转")
	}
	live.servers["a1"] = nil
	h.Deliver(context.Background(), up("u:2"))
	if len(pub.sent) != 0 {
		t.Fatal("全网无 server 必须静默丢弃")
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
