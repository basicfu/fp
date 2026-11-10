package fpim

import "testing"

// 本文件与 internal/im/model/subject_test.go 是同一份测试，只把
// ParseSubject 改名为 Parse——两份实现的一致性由
// internal/integration/im_parity_test.go 守着，这里只保证 sdk/im 自己
// 这份实现本身是对的。
func TestParse(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Subject
		ok   bool
	}{
		{"u:1001", Subject{Kind: KindUser, ID: "1001"}, true},
		{"g:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f", Subject{Kind: KindGuest, ID: "6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"}, true},
		{"g:not-a-uuid", Subject{}, false},
		{"x:1", Subject{}, false},
		{"u:", Subject{}, false},
		{"", Subject{}, false},
	} {
		got, err := Parse(tc.in)
		if (err == nil) != tc.ok {
			t.Fatalf("Parse(%q) err=%v，期望 ok=%v", tc.in, err, tc.ok)
		}
		if tc.ok && got != tc.want {
			t.Fatalf("Parse(%q)=%+v，期望 %+v", tc.in, got, tc.want)
		}
		if tc.ok && got.String() != tc.in {
			t.Fatalf("String() 必须还原输入：%q → %q", tc.in, got.String())
		}
	}
}

func TestParseBizSubject(t *testing.T) {
	got, err := Parse("b:1001")
	if err != nil {
		t.Fatalf("合法的业务方主体被拒：%v", err)
	}
	if got != Biz("1001") || got.String() != "b:1001" {
		t.Fatalf("Parse(\"b:1001\")=%+v String()=%q", got, got.String())
	}
	if _, err := Parse("b:"); err == nil {
		t.Fatal("空的业务方 id 必须被拒")
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

func TestBizGroupSubject(t *testing.T) {
	s := BizGroup("r", "a:b")
	if s.String() != "b.r:a:b" {
		t.Fatalf("String()=%q，期望 b.r:a:b", s.String())
	}
	got, err := Parse("b.r:a:b")
	if err != nil || got != s || got.Group != "r" || got.ID != "a:b" {
		t.Fatalf("Parse(b.r:a:b)=%+v err=%v", got, err)
	}
	if Biz("1001") != BizGroup("", "1001") {
		t.Fatal("Biz(id) 必须等于 BizGroup(\"\", id)")
	}
	if def, err := Parse("b:1001"); err != nil || def.Group != "" {
		t.Fatalf("默认分组解析变了：%+v err=%v", def, err)
	}
	for _, bad := range []string{"u.x:1", "g.x:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f", "b.:x", "b.R:x", "b.abcdefghijklmnopq:x", "b.1a:x", "b.a.b:x", "b.r:"} {
		if got, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) 应当被拒，实际 %+v", bad, got)
		}
	}
}

func TestIsDefaultBiz(t *testing.T) {
	if !Biz("1").IsDefaultBiz() || BizGroup("r", "1").IsDefaultBiz() || User("1").IsDefaultBiz() || Guest("6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f").IsDefaultBiz() {
		t.Fatal("IsDefaultBiz 只对 Kind==b 且 Group 为空成立")
	}
}
