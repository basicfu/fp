package fpsdk

import (
	"reflect"
	"testing"
	"time"
)

func TestToSnake(t *testing.T) {
	cases := []struct{ in, want string }{
		{"FeeRate", "fee_rate"},
		{"APIKey", "api_key"}, // 连续大写的缩写不能拆成 a_p_i_key
		{"UserID", "user_id"}, // 结尾的缩写
		{"HTTPSProxy", "https_proxy"},
		{"ID", "id"},
		{"Timeout", "timeout"},
		{"MaxConns2", "max_conns2"},
	}
	for _, c := range cases {
		if got := toSnake(c.in); got != c.want {
			t.Errorf("toSnake(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

type upstreamCfg struct {
	Timeout time.Duration
	APIKey  string
}

type providerCfg struct {
	Name string
	Rate float64
}

type specCfg struct {
	FeeRate    float64
	Enabled    bool
	Name       string
	Limits     []int
	Extra      map[string]string
	Upstream   upstreamCfg   // 嵌套 struct = 分组
	Providers  []providerCfg // 切片一律 array，不递归展开
	Raw        providerCfg   `fp:"json"` // 标了 json 就整体当 object
	unexported int           //nolint:unused // 必须被跳过
}

func TestSpecsOf(t *testing.T) {
	specs, err := specsOf(reflect.TypeOf(specCfg{}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}

	got := map[string]fieldSpec{}
	for _, s := range specs {
		got[s.Key] = s
	}

	want := map[string]string{
		"fee_rate":         cfgTypeFloat,
		"enabled":          cfgTypeBool,
		"name":             cfgTypeString,
		"limits":           cfgTypeArray,
		"extra":            cfgTypeObject,
		"upstream.timeout": cfgTypeInt, // Duration 走 int（毫秒）
		"upstream.api_key": cfgTypeString,
		"providers":        cfgTypeArray,
		"raw":              cfgTypeObject,
	}
	if len(got) != len(want) {
		t.Fatalf("推导出 %d 项：%v，期望 %d 项", len(got), keysOf(got), len(want))
	}
	for k, wantType := range want {
		s, ok := got[k]
		if !ok {
			t.Fatalf("缺少 key %q", k)
		}
		if s.Type != wantType {
			t.Errorf("%q 的类型 = %q，期望 %q", k, s.Type, wantType)
		}
	}

	// 【辨别力】Duration 必须被标出来，且它的 fp 类型是 int。
	// 只断言"类型是 int"是不够的——一个先判 Kind（int64）后判具体类型的
	// 实现同样会给出 int，却丢掉了毫秒换算，值会变成纳秒。
	if !got["upstream.timeout"].Duration {
		t.Fatal("upstream.timeout 必须标记为 Duration，否则毫秒换算会丢")
	}
	if got["fee_rate"].Duration {
		t.Fatal("fee_rate 不是 Duration")
	}

	// 排序稳定：MissingConfigError 的清单靠它才不会每次启动顺序都不同。
	for i := 1; i < len(specs); i++ {
		if specs[i-1].Key >= specs[i].Key {
			t.Fatalf("specs 未按 key 升序：%q 在 %q 之前", specs[i-1].Key, specs[i].Key)
		}
	}
}

func TestSpecsOfRejectsUnsupported(t *testing.T) {
	type badPtr struct{ P *int }
	type badChan struct{ C chan int }
	type badFunc struct{ F func() }
	type badIface struct{ I any }

	for _, v := range []any{badPtr{}, badChan{}, badFunc{}, badIface{}} {
		if _, err := specsOf(reflect.TypeOf(v)); err == nil {
			t.Errorf("%T 应当被拒绝，不能静默跳过", v)
		}
	}
}

func TestSpecsOfRejectsNonStruct(t *testing.T) {
	if _, err := specsOf(reflect.TypeOf(42)); err == nil {
		t.Fatal("非 struct 应当被拒绝")
	}
}

// TestSpecsOfRejectsTimeTime 补一条 brief 给的测试没覆盖到的分支。
//
// collectSpecs 把 time.Time 从"嵌套 struct = 分组"里特意排除掉，注释里
// 交代原因：不排除的话会递归展开成一个字段全未导出的空分组，
// specsOf 返回 nil error、却一个 key 都不产出——这和"指针/interface/chan/func
// 必须报错、不能静默跳过"是同一类事故，只是入口不一样（这里入口是
// f.Type.Kind()==Struct 分支，不会走到 fpTypeOf 的 default 分支），必须
// 单独测，否则这个排除条件被删掉也不会有任何测试变红。
func TestSpecsOfRejectsTimeTime(t *testing.T) {
	type hasTimeTime struct{ At time.Time }
	if _, err := specsOf(reflect.TypeOf(hasTimeTime{})); err == nil {
		t.Fatal("At time.Time 应当报错，不能静默展开成一个空分组（一个 key 都不产出）")
	}
}

// TestSpecsOfIndexResolvesCorrectField 锁住 fieldSpec.Index 的正确性。
//
// brief 给的 TestSpecsOf 只断言 Key 与 Type，从未检查 Index——但 Index
// 才是 Task 10 的 Bind 真正用来把值写回业务方 struct 字段的东西
// （reflect.Value.FieldByIndex(spec.Index)），推错一个 Index 比推错一个
// Type 更隐蔽：编译期看不出来，运行期要么写错字段、要么直接 panic。
// collectSpecs 里"拷一份再 append"那行注释明说是为了防兄弟字段的路径
// 互相踩，但如果没有测试真的用 FieldByIndex 走一遍，这条防线本身也没有
// 回归保护。
func TestSpecsOfIndexResolvesCorrectField(t *testing.T) {
	specs, err := specsOf(reflect.TypeOf(specCfg{}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	byKey := map[string]fieldSpec{}
	for _, s := range specs {
		byKey[s.Key] = s
	}

	cfg := specCfg{}
	v := reflect.ValueOf(&cfg).Elem()

	// 顶层字段。
	v.FieldByIndex(byKey["fee_rate"].Index).SetFloat(1.5)
	if cfg.FeeRate != 1.5 {
		t.Fatalf("fee_rate 的 Index 没有定位到 FeeRate，写入后 FeeRate=%v", cfg.FeeRate)
	}

	// 嵌套分组：Index 必须是从 specCfg 出发的完整路径（父.子两级），
	// 不是只在 upstreamCfg 内部有效的相对路径。
	v.FieldByIndex(byKey["upstream.api_key"].Index).SetString("secret")
	if cfg.Upstream.APIKey != "secret" {
		t.Fatalf("upstream.api_key 的 Index 没有定位到 Upstream.APIKey，写入后 Upstream.APIKey=%q", cfg.Upstream.APIKey)
	}
	// 顺带确认没有踩到同一分组里的兄弟字段。
	if cfg.Upstream.Timeout != 0 {
		t.Fatalf("写 upstream.api_key 不该影响 upstream.timeout，实际 = %v", cfg.Upstream.Timeout)
	}
}

// TestSpecsOfIndexSurvivesDeepNesting 补审查发现的一个 Important：
// TestSpecsOfIndexResolvesCorrectField 对"append 复用底层数组导致兄弟
// 字段路径互相踩"这一类缺陷没有真正的辨别力，是"测试因为巧合而通过"。
//
// 【为什么必须嵌套到这个深度，且不能再浅一层】collectSpecs 里那行
//
//	path := append(append([]int{}, index...), i)
//
// 的双重拷贝看着像多余代码，容易被当"清理"简化成朴素的 append(index, i)。
// 但用 3 层嵌套（outer.mid.{X,Y}）验证过这条简化：测不出来，
// TestSpecsOfIndexResolvesCorrectField 那种深度同样测不出来——都是巧合通过。
// 用一段独立脚本按朴素 append 的路径重放 index 切片的增长过程，
// 逐层打印 len/cap 才找到真正的边界：
//
//	level0 (nil):      len 0 cap 0
//	level1 (append 1): len 1 cap 1   -- 恰好够，不会新分配时留出多余空间
//	level2 (append 0): len 2 cap 2   -- 同上
//	level3 (append 0): len 3 cap 4   -- 从这一层开始 cap > len，出现多余容量
//
// 多余容量出现在第 3 层的*结果*里，但真正的兄弟互踩要等*下一层*
// （第 4 层）两个字段共享这个已经带多余容量的 index 时才会发生：
// 二者都从同一个 len=3/cap=4 的切片 append，都落在同一个下标 3 上，
// 后写的直接覆盖先写的，且两个字段的 Index 切片头共享同一块底层数组，
// 读出来的内容都是最后一次写入的值。所以叶子字段必须是第 4 层
// （outer.mid.inner.{X,Y}），比先前 3 层的版本再深一层，才能撞上这个边界；
// 这也是为什么 Pad/Leaf 字段全用单字段 struct 逐层过渡、不能省略中间层。
type deepLeaf struct {
	X string
	Y string
}
type deepInner struct{ Leaf deepLeaf }
type deepMid struct{ Sub deepInner }
type deepOuter struct {
	Pad string // 占位，让 L 的下标不是 0，错位时更容易暴露
	L   deepMid
}

func TestSpecsOfIndexSurvivesDeepNesting(t *testing.T) {
	specs, err := specsOf(reflect.TypeOf(deepOuter{}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}

	byKey := map[string][]int{}
	for _, s := range specs {
		byKey[s.Key] = s.Index
	}
	xi, ok := byKey["l.sub.leaf.x"]
	if !ok {
		t.Fatalf("缺 l.sub.leaf.x，实际 keys: %v", specs)
	}
	yi, ok := byKey["l.sub.leaf.y"]
	if !ok {
		t.Fatalf("缺 l.sub.leaf.y，实际 keys: %v", specs)
	}
	if reflect.DeepEqual(xi, yi) {
		t.Fatalf("两个兄弟字段的 Index 相同（都是 %v）——append 复用了底层数组", xi)
	}

	// 光断言 Index 不同还不够，要真的按它写值、再读回来确认落在正确的字段上。
	var v deepOuter
	rv := reflect.ValueOf(&v).Elem()
	rv.FieldByIndex(xi).SetString("xx")
	rv.FieldByIndex(yi).SetString("yy")
	if v.L.Sub.Leaf.X != "xx" || v.L.Sub.Leaf.Y != "yy" {
		t.Fatalf("按 Index 写值落错了字段: X=%q Y=%q", v.L.Sub.Leaf.X, v.L.Sub.Leaf.Y)
	}
}

func keysOf(m map[string]fieldSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
