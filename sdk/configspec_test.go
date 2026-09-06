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

func keysOf(m map[string]fieldSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
