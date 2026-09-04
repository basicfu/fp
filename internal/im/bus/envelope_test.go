package bus

import (
	"bytes"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	in := Envelope{Type: TypeUp, Hops: 1, App: "a1", Subject: "u:1001", ConnID: "c-1", Extra: "replaced", Payload: []byte(`{"x":1}`)}
	out, err := Decode(in.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != in.Type || out.Hops != in.Hops || out.App != in.App || out.Subject != in.Subject ||
		out.ConnID != in.ConnID || out.Extra != in.Extra || !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("往返不一致：%+v → %+v", in, out)
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
