package domain_test

import (
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

func TestParseConfigYAML(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    map[string]any
		wantErr bool
	}{
		{"空字符串是空对象", "", map[string]any{}, false},
		{"只有空白也是空对象", "   \n\t  ", map[string]any{}, false},
		{"标量类型齐全", "b: true\ni: 3\nf: 0.02\ns: 商城\n",
			map[string]any{"b": true, "i": 3, "f": 0.02, "s": "商城"}, false},
		{"数组与嵌套对象", "arr:\n  - 1\n  - 2\nobj:\n  a: 1\n  b: 2\n",
			map[string]any{"arr": []any{1, 2}, "obj": map[string]any{"a": 1, "b": 2}}, false},
		// 注释不影响解析结果——它只在管理端展示原文时可见，不落进解析后的
		// 对象，这正是"注释取代备注字段"这个设计的直接后果：SDK 从来读不到
		// 备注，现在也读不到注释，符合预期，不是遗漏。
		{"注释被忽略", "timeout: 3000 # 超时时间（毫秒）\n", map[string]any{"timeout": 3000}, false},
		// 大整数必须原样保留精度：yaml.v3 把整数解成 int（64 位平台上是
		// int64），不会像"先转成 any 再转回 JSON"那样静默舍入到 float64 的
		// 53 位精度。
		{"雪花 ID 级别的大整数", "id: 9007199254740993\n", map[string]any{"id": 9007199254740993}, false},
		{"null 文档当空对象", "null", map[string]any{}, false},
		{"波浪号也是 null 文档", "~", map[string]any{}, false},

		{"顶层是裸标量应报错", "hello", nil, true},
		{"顶层是数组应报错", "- a\n- b\n", nil, true},
		{"缩进错误的非法 YAML 应报错", "a:\n  b: 1\n c: 2\n", nil, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.ParseConfigYAML(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("得到 %#v，期望 %#v", got, c.want)
			}
			for k, wantV := range c.want {
				gotV, ok := got[k]
				if !ok {
					t.Fatalf("缺少 key %q，得到 %#v", k, got)
				}
				if !deepEqualLoose(gotV, wantV) {
					t.Fatalf("key %q: 得到 %#v（%T），期望 %#v（%T）", k, gotV, gotV, wantV, wantV)
				}
			}
		})
	}
}

// deepEqualLoose 比较解析结果，array/object 递归比较。不用
// reflect.DeepEqual 直接比：yaml.v3 对 slice/map 里各元素的具体类型
// （int vs int64 之类）不一定和测试用例手写的字面量完全一致，这里只关心
// "值语义上相不相等"。
func deepEqualLoose(a, b any) bool {
	switch bv := b.(type) {
	case []any:
		av, ok := a.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range bv {
			if !deepEqualLoose(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		av, ok := a.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range bv {
			if !deepEqualLoose(av[k], v) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// 分区名不再局限于 DEFAULT/WEB 两个固定值——管理端可以建任意名字的分区，
// 校验只挡明显不合法的字符集（空、数字开头、带空白/斜杠这类会把 URL
// 查询参数弄乱的字符、超长）。
func TestIsConfigType(t *testing.T) {
	for _, s := range []string{
		domain.ConfigTypeDefault, domain.ConfigTypeWeb, "web", "MOBILE", "Admin_Panel", "a",
		strings.Repeat("a", 64), // 边界：正好 64 个字符
	} {
		if !domain.IsConfigType(s) {
			t.Fatalf("%q 应当是合法分区名", s)
		}
	}
	for _, s := range []string{
		"", "1web", "web app", "web/app", "web.app", "web-app",
		strings.Repeat("a", 65), // 边界：超过 64 个字符
	} {
		if domain.IsConfigType(s) {
			t.Fatalf("%q 不应当是合法分区名", s)
		}
	}
}

func TestNormalizeConfigYAML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"漏空格的标量值", "port:4379\n", "port: 4379\n"},
		{"已经有空格的不受影响", "port: 4379\n", "port: 4379\n"},
		{"多行都要补", "a:1\nb:2\n", "a: 1\nb: 2\n"},
		{"只补第一个冒号，值里的冒号不动", "url:http://x.com:8080\n", "url: http://x.com:8080\n"},
		{"列表项前缀不受影响", "- a:1\n", "- a: 1\n"},
		{"缩进保留", "  port:4379\n", "  port: 4379\n"},
		// 注释是自由文本，即使长得像"key:value"也不该被这条启发式规则误伤。
		{"注释行不动", "# see:http://x\nport:4379\n", "# see:http://x\nport: 4379\n"},
		{"空行不动", "a:1\n\nb:2\n", "a: 1\n\nb: 2\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := domain.NormalizeConfigYAML(c.in); got != c.want {
				t.Fatalf("得到 %q，期望 %q", got, c.want)
			}
		})
	}
}
