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
