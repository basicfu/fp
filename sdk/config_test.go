package fpsdk

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type bindCfg struct {
	FeeRate  float64
	Enabled  bool
	Upstream bindUpstream
	Limits   []int
}

type bindUpstream struct {
	Timeout time.Duration
	APIKey  string
}

func TestFillStructAppliesValues(t *testing.T) {
	specs, err := specsOf(reflect.TypeOf(bindCfg{}))
	if err != nil {
		t.Fatalf("specsOf 失败: %v", err)
	}
	values := map[string]json.RawMessage{
		"fee_rate":         json.RawMessage(`0.02`),
		"enabled":          json.RawMessage(`true`),
		"upstream.timeout": json.RawMessage(`3000`),
		"upstream.api_key": json.RawMessage(`"sk-live"`),
		"limits":           json.RawMessage(`[1,2,3]`),
	}

	got, missing, err := fillStruct[bindCfg](specs, values)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("不该有缺失项: %v", missing)
	}
	if got.FeeRate != 0.02 || !got.Enabled || got.Upstream.APIKey != "sk-live" {
		t.Fatalf("填充结果不对: %+v", got)
	}
	// 【辨别力】3000 必须变成 3 秒而不是 3000 纳秒。断言具体时长，
	// 不要只断言"非零"——丢掉毫秒换算的实现同样是非零。
	if got.Upstream.Timeout != 3*time.Second {
		t.Fatalf("Timeout = %v，期望 3s（3000 毫秒）", got.Upstream.Timeout)
	}
	if !reflect.DeepEqual(got.Limits, []int{1, 2, 3}) {
		t.Fatalf("Limits = %v", got.Limits)
	}
}

// 缺失项一次列全，且缺失的字段留 Go 零值。
// 【辨别力】必须缺**两项**：只缺一项的话，"报第一个就 return"的实现也会绿。
func TestFillStructReportsAllMissing(t *testing.T) {
	specs, _ := specsOf(reflect.TypeOf(bindCfg{}))
	values := map[string]json.RawMessage{
		"fee_rate": json.RawMessage(`0.02`),
		"enabled":  json.RawMessage(`true`),
		"limits":   json.RawMessage(`[]`),
	}

	got, missing, err := fillStruct[bindCfg](specs, values)
	if err != nil {
		t.Fatalf("缺值不是错误，应当由调用方决定: %v", err)
	}
	want := []string{"upstream.api_key", "upstream.timeout"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %v，期望 %v（升序、两项都在）", missing, want)
	}
	if got.FeeRate != 0.02 {
		t.Fatal("已配置的字段仍应被填上")
	}
	if got.Upstream.Timeout != 0 || got.Upstream.APIKey != "" {
		t.Fatal("缺失的字段应当留 Go 零值")
	}
}

// 类型对不上是错误，不是缺失——旧代码遇到"有人把 int 改成了 object"
// 必须报错，不能当成没配。
func TestFillStructRejectsMismatchedType(t *testing.T) {
	specs, _ := specsOf(reflect.TypeOf(bindCfg{}))
	values := map[string]json.RawMessage{
		"fee_rate":         json.RawMessage(`{"a":1}`), // 该是数字
		"enabled":          json.RawMessage(`true`),
		"upstream.timeout": json.RawMessage(`3000`),
		"upstream.api_key": json.RawMessage(`"x"`),
		"limits":           json.RawMessage(`[]`),
	}
	if _, _, err := fillStruct[bindCfg](specs, values); err == nil {
		t.Fatal("类型对不上必须报错")
	}
}

func TestMissingConfigErrorMessageListsEveryKeyWithType(t *testing.T) {
	err := &MissingConfigError{
		Keys:  []string{"fee_rate", "upstream.api_key"},
		Types: map[string]string{"fee_rate": cfgTypeFloat, "upstream.api_key": cfgTypeString},
	}
	msg := err.Error()
	for _, want := range []string{"fee_rate", "float", "upstream.api_key", "string"} {
		if !contains(msg, want) {
			t.Errorf("错误信息里缺少 %q：\n%s", want, msg)
		}
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && strings.Contains(s, sub) }
