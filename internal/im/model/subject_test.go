package model

import "testing"

func TestParseSubject(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Subject
		ok   bool
	}{
		{"u:1001", Subject{KindUser, "1001"}, true},
		{"g:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f", Subject{KindGuest, "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"}, true},
		{"g:not-a-uuid", Subject{}, false},
		{"x:1", Subject{}, false},
		{"u:", Subject{}, false},
		{"", Subject{}, false},
	} {
		got, err := ParseSubject(tc.in)
		if (err == nil) != tc.ok {
			t.Fatalf("ParseSubject(%q) err=%v，期望 ok=%v", tc.in, err, tc.ok)
		}
		if tc.ok && got != tc.want {
			t.Fatalf("ParseSubject(%q)=%+v，期望 %+v", tc.in, got, tc.want)
		}
		if tc.ok && got.String() != tc.in {
			t.Fatalf("String() 必须还原输入：%q → %q", tc.in, got.String())
		}
	}
}

func TestIsUUIDv4(t *testing.T) {
	if !IsUUIDv4("6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f") {
		t.Fatal("合法 v4 被拒")
	}
	// 版本位是 1，不是 4：访客 id 必须是随机 uuid，时间型 uuid 可被推测
	if IsUUIDv4("6f1c3c2e-4b1a-1d2e-9f0e-7a8b9c0d1e2f") {
		t.Fatal("v1 uuid 不能当访客 id")
	}
	if IsUUIDv4("6F1C3C2E4B1A4D2E9F0E7A8B9C0D1E2F") {
		t.Fatal("只接受带连字符的小写标准写法，避免同一访客有两种 key")
	}
}
