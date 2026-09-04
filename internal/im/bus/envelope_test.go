package bus

import (
	"bytes"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	in := Envelope{Type: TypeUp, Hop: 1, App: "a1", Subject: "u:1001", ConnID: "c-1", Extra: "replaced", Payload: []byte(`{"x":1}`)}
	out, err := Decode(in.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != in.Type || out.Hop != in.Hop || out.App != in.App || out.Subject != in.Subject ||
		out.ConnID != in.ConnID || out.Extra != in.Extra || !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("往返不一致：%+v → %+v", in, out)
	}
}

// TestEnvelopeRoundTripWithRoute 覆盖 Route 非空的信封：转发路径会给
// TypeUp/TypeEvt 写入候选节点列表，编解码必须原样保留顺序（Route 的顺序就是
// rendezvous 权重降序，乱了会破坏转发的确定性）。
func TestEnvelopeRoundTripWithRoute(t *testing.T) {
	in := Envelope{
		Type: TypeUp, Hop: 2, App: "a1", Subject: "u:1001", ConnID: "c-1", Extra: "",
		Route:   []string{"im-b", "im-a", "im-c"},
		Payload: []byte(`{"x":1}`),
	}
	out, err := Decode(in.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if out.Hop != in.Hop || len(out.Route) != len(in.Route) {
		t.Fatalf("往返不一致：%+v → %+v", in, out)
	}
	for i := range in.Route {
		if out.Route[i] != in.Route[i] {
			t.Fatalf("Route 顺序必须原样保留：%v → %v", in.Route, out.Route)
		}
	}
	if !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("payload 不一致：%v → %v", in.Payload, out.Payload)
	}
}

func TestEnvelopeEmptyRouteRoundTrips(t *testing.T) {
	in := Envelope{Type: TypeMsg, App: "a1", Subject: "u:1", Payload: []byte(`1`)}
	out, err := Decode(in.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Route) != 0 {
		t.Fatalf("未设置 Route 的信封解码后应仍是空，实际 %v", out.Route)
	}
}

func TestEnvelopeNoBase64Overhead(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1000)
	e := Envelope{Type: TypeMsg, App: "a1", Subject: "u:1", Payload: payload}
	if n := len(e.Encode()); n > 1000+64 {
		t.Fatalf("信封开销应是常数级，1000 字节 payload 编码后 %d 字节", n)
	}
}

func TestDecodeRejectsTruncated(t *testing.T) {
	e := Envelope{Type: TypeMsg, App: "a1", Subject: "u:1", Payload: []byte("hello")}
	b := e.Encode()
	for cut := 0; cut < len(b); cut++ {
		if _, err := Decode(b[:cut]); err == nil {
			t.Fatalf("截断到 %d 字节仍能解码，会把损坏的消息投给 client", cut)
		}
	}
}

// TestDecodeRejectsTruncatedWithRoute 把截断测试扩展到 Route 非空的信封：
// A2 在 Encode 布局里新插入了变长的 Route 段，截断测试如果只覆盖空 Route，
// Route 段自身的长度前缀、以及 Route 元素内部被截断的情形都测不到。
func TestDecodeRejectsTruncatedWithRoute(t *testing.T) {
	e := Envelope{
		Type: TypeUp, Hop: 1, App: "a1", Subject: "u:1", ConnID: "c1", Extra: "e",
		Route:   []string{"im-a", "im-b", "im-c"},
		Payload: []byte("hello"),
	}
	b := e.Encode()
	for cut := 0; cut < len(b); cut++ {
		if _, err := Decode(b[:cut]); err == nil {
			t.Fatalf("截断到 %d 字节仍能解码，会把损坏的消息投给 client", cut)
		}
	}
}

// TestEnvelopeRoundTripHopAbove255 是"游标溢出"缺陷的编解码层回归测试。
//
// hub 包里的 TestDeliverRouteLargerThan255DoesNotWrapAround 只断言内存里的
// Envelope.Hop 字段（它用的假发布器不做任何编码，只是把结构体存起来），
// 测不到线路格式本身。如果将来有人把 Encode/Decode 里的 hop 字段改回一个
// 字节（比如"优化"信封体积），hub 那条测试依然会通过——它根本没有经过
// Encode/Decode 这一步——我们刚修掉的回绕缺陷会在编解码层悄悄回来，且现有
// 测试集不会变红。这里直接在往返测试里放一个超过 255 的游标值，把"线路上
// 的游标必须能表示超过 255"这条约束钉在编解码层。
func TestEnvelopeRoundTripHopAbove255(t *testing.T) {
	in := Envelope{
		Type: TypeUp, Hop: 300, App: "a1", Subject: "u:1001", ConnID: "c-1", Extra: "",
		Route:   []string{"im-b", "im-a", "im-c"},
		Payload: []byte(`{"x":1}`),
	}
	out, err := Decode(in.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if out.Hop != 300 {
		t.Fatalf("游标在线路上必须能表示超过 255 的值，否则候选列表超过 255 个时会回绕成 0，导致下游从头重试整条列表、消息在节点间无限打转：编码前 Hop=300，解码后却是 %d", out.Hop)
	}
	if len(out.Route) != len(in.Route) {
		t.Fatalf("Route 长度不应该受 Hop 编码方式影响：编码前 %d 个，解码后 %d 个", len(in.Route), len(out.Route))
	}
	for i := range in.Route {
		if out.Route[i] != in.Route[i] {
			t.Fatalf("Route 顺序必须原样保留：%v → %v", in.Route, out.Route)
		}
	}
}
