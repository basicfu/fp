package model

import (
	"errors"
	"strings"
	"testing"
)

func TestParseSubject(t *testing.T) {
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

func TestParseSubjectBiz(t *testing.T) {
	got, err := ParseSubject("b:1001")
	if err != nil {
		t.Fatalf("合法的业务方主体被拒：%v", err)
	}
	if got != (Subject{Kind: KindBiz, ID: "1001"}) {
		t.Fatalf("ParseSubject(\"b:1001\")=%+v", got)
	}
	if got.String() != "b:1001" {
		t.Fatalf("String() 必须还原输入，实际 %q", got.String())
	}
	// 业务方主体的 id 不做格式约束（业务方的用户体系我们不理解），
	// 但空 id 必须拒绝，否则会生成 "b:" 这种没有主体的 Redis key。
	if _, err := ParseSubject("b:"); err == nil {
		t.Fatal("空的业务方 id 必须被拒")
	}
	// ValidateUserID 还拦两种会造成问题的情况，必须通过 ParseSubject 路径测试其调用
	if _, err := ParseSubject("b:" + strings.Repeat("x", 129)); err == nil {
		t.Fatal("超长业务方 id 必须被拒绝：会生成超长 Redis key")
	}
	if _, err := ParseSubject("b:10\x0001"); err == nil {
		t.Fatal("含空字节的业务方 id 必须被拒绝：会撞上路由表内存键的分隔符")
	}
}

func TestValidateUserID(t *testing.T) {
	if err := ValidateUserID("1001"); err != nil {
		t.Fatalf("普通 id 被拒：%v", err)
	}
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"空串", ""},
		{"含空字节", "10\x0001"},
		{"超长", strings.Repeat("x", 129)},
	} {
		if err := ValidateUserID(tc.id); err == nil {
			t.Errorf("%s 必须被拒绝：uid 会成为 Redis key 的一部分，空字节还会撞上路由表内存键的分隔符", tc.name)
		}
	}
	// 恰好 128 字节是允许的，边界不能少一个
	if err := ValidateUserID(strings.Repeat("x", 128)); err != nil {
		t.Fatalf("128 字节应当放行：%v", err)
	}
}

func TestBizGroupSubject(t *testing.T) {
	s := BizGroup("r", "a:b")
	if s.String() != "b.r:a:b" {
		t.Fatalf("String()=%q，期望 b.r:a:b", s.String())
	}
	// uid 不约束字符集，可以含冒号：只按第一个冒号切，kind 段里的 . 才是分组
	got, err := ParseSubject("b.r:a:b")
	if err != nil || got != s || got.Kind != KindBiz || got.Group != "r" || got.ID != "a:b" {
		t.Fatalf("ParseSubject(b.r:a:b)=%+v err=%v", got, err)
	}
	if Biz("1001") != BizGroup("", "1001") {
		t.Fatal("Biz(id) 必须等于 BizGroup(\"\", id)：默认分组的写法逐字不变")
	}
	def, err := ParseSubject("b:1001")
	if err != nil || def.Group != "" || def.String() != "b:1001" {
		t.Fatalf("默认分组解析变了：%+v err=%v", def, err)
	}
	// 同名 uid 在默认分组与分组里是两个主体，连接表 key 天然隔离
	if ConnKey("a1", Biz("1001").String()) == ConnKey("a1", BizGroup("r", "1001").String()) {
		t.Fatal("b:1001 与 b.r:1001 不能共用连接表")
	}
}

func TestParseSubjectRejectsBadGroup(t *testing.T) {
	for _, s := range []string{
		"u.x:1", // 只有 b 能带分组
		"g.x:6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f", // 同上
		"b.:x",                                // 空分组
		"b.R:x",                               // 大写
		"b." + strings.Repeat("a", 17) + ":x", // 超长
		"b.1a:x",                              // 数字开头
		"b.a-b:x",                             // 非法字符
		"b.a.b:x",                             // 分组名里不能再有点
		"b.r:",                                // 空 uid
		"b.r:" + strings.Repeat("x", 129),     // uid 超长，与默认分组同一条规则
	} {
		if got, err := ParseSubject(s); err == nil {
			t.Errorf("ParseSubject(%q) 应当被拒，实际 %+v", s, got)
		}
	}
	for _, s := range []string{"b." + strings.Repeat("a", 16) + ":x", "b.a_1:x", "b.z9:1001"} {
		got, err := ParseSubject(s)
		if err != nil || got.String() != s {
			t.Errorf("ParseSubject(%q) 应当接受且往返一致，实际 %+v err=%v", s, got, err)
		}
	}
}

func TestValidateGroup(t *testing.T) {
	for _, g := range []string{"r", "web", "a_1", strings.Repeat("a", 16)} {
		if err := ValidateGroup(g); err != nil {
			t.Errorf("ValidateGroup(%q) 被拒：%v", g, err)
		}
	}
	for _, g := range []string{"", "R", "1a", "_a", "a-b", "a.b", "a:b", strings.Repeat("a", 17)} {
		if err := ValidateGroup(g); !errors.Is(err, ErrBadGroup) {
			t.Errorf("ValidateGroup(%q) 应返回 ErrBadGroup，实际 %v", g, err)
		}
	}
}

func TestIsDefaultBiz(t *testing.T) {
	for _, tc := range []struct {
		s    Subject
		want bool
	}{
		{Biz("1001"), true},
		{BizGroup("r", "1001"), false},
		{User("1001"), false},
		{Guest("6f1c3c2e-4b1a-4d2e-9f0e-7a8b9c0d1e2f"), false},
	} {
		if got := tc.s.IsDefaultBiz(); got != tc.want {
			t.Errorf("%s.IsDefaultBiz()=%v，期望 %v", tc.s, got, tc.want)
		}
	}
}
