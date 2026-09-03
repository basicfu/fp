package model

import "testing"

func TestConnMetaRoundTrip(t *testing.T) {
	m := ConnMeta{Node: "im-a", OS: "android", Mobile: true, At: 1756800000000}
	got, err := DecodeMeta(m.Encode())
	if err != nil || got != m {
		t.Fatalf("往返不一致：%+v → %q → %+v (%v)", m, m.Encode(), got, err)
	}
	// 字段名必须是短名，这是 Redis 里每条连接都要存的东西
	if s := m.Encode(); s != `{"n":"im-a","os":"android","m":true,"ts":1756800000000}` {
		t.Fatalf("编码格式变了：%s", s)
	}
}
