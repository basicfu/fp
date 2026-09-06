package fpsdk

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
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

// ---------------------------------------------------------------------
// 以下是 Bind / fetchConfig / registerBinding 的端到端测试。
//
// 上面几个 TestFillStruct* 只测了"给定 values map 怎么填字段"这一层，
// Bind 本身（拉取失败要不要注册、缺配置时 binding 是不是仍然可用、
// registerBinding 的调用时机）此前一条测试都没有——这正是审查指出的
// 缺口。这里用 sdk/client_test.go 既有的 stubServer/newStubEnvFull
// 真实 gRPC 桩起一个连着 Client 的环境，而不是只测 fillStruct 这个
// 内部函数。
// ---------------------------------------------------------------------

// TestBindReturnsUsableBindingWithMissingConfigError 守住"空分区时
// Bind 也必须返回可用 binding"。
//
// 【辨别力】必须断言 binding **非 nil**：一次列全的 MissingConfigError
// 只解决了"漏项"，如果 Bind 干脆返回 nil binding，业务方拿到的 err
// 类型完全正确，程序却会在第一次 Load() 时 nil 解引用崩溃——这与
// "SDK 不替业务方决定能不能带伤启动"这条硬约束直接冲突。只断言 err
// 类型对的实现会放过这个缺陷，必须同时断言 binding 非 nil。
func TestBindReturnsUsableBindingWithMissingConfigError(t *testing.T) {
	stub := &stubServer{
		getConfig: func(*fpv1.GetConfigRequest) (*fpv1.GetConfigResponse, error) {
			return &fpv1.GetConfigResponse{Version: 1, Values: `{}`}, nil
		},
	}
	env := newStubEnvFull(t, stub)

	b, err := Bind[bindCfg](env.client)
	if b == nil {
		t.Fatal("一个字段都没配置时 Bind 也必须返回可用的 binding——" +
			"SDK 不替业务方决定能不能带伤启动")
	}
	var miss *MissingConfigError
	if !errors.As(err, &miss) {
		t.Fatalf("err = %v，期望能 errors.As 出 *MissingConfigError", err)
	}
	want := []string{"enabled", "fee_rate", "limits", "upstream.api_key", "upstream.timeout"}
	if !reflect.DeepEqual(miss.Keys, want) {
		t.Fatalf("Keys = %v，期望 %v（升序、全部列出）", miss.Keys, want)
	}
	for _, k := range want {
		if miss.Types[k] == "" {
			t.Fatalf("Types[%q] 为空——控制台建配置项时不知道该建成什么类型", k)
		}
	}

	got := b.Load()
	if got.FeeRate != 0 || got.Enabled || got.Upstream.Timeout != 0 ||
		got.Upstream.APIKey != "" || got.Limits != nil {
		t.Fatalf("全部缺失时 Load() 应为 Go 零值: %+v", got)
	}

	env.client.bindingsMu.Lock()
	n := len(env.client.bindings)
	env.client.bindingsMu.Unlock()
	if n != 1 {
		t.Fatalf("缺配置也应当注册 binding（供 Task 11 热更新用），实际注册了 %d 个", n)
	}
}

// TestBindFillsAllValuesWhenFullyConfigured 守住"全部已配置时 err 为
// nil、值经过网络这一趟往返仍然正确"，包含 Duration 字段的毫秒换算——
// fillStruct 单测过这一点，这里额外确认经过真实 GetConfig RPC 之后
// 结论不变（比如 JSON 数字类型在序列化/反序列化两端没有被谁悄悄改写）。
func TestBindFillsAllValuesWhenFullyConfigured(t *testing.T) {
	stub := &stubServer{
		getConfig: func(*fpv1.GetConfigRequest) (*fpv1.GetConfigResponse, error) {
			return &fpv1.GetConfigResponse{Version: 1, Values: `{
				"fee_rate": 0.02,
				"enabled": true,
				"upstream.timeout": 3000,
				"upstream.api_key": "sk-live",
				"limits": [1,2,3]
			}`}, nil
		},
	}
	env := newStubEnvFull(t, stub)

	b, err := Bind[bindCfg](env.client)
	if err != nil {
		t.Fatalf("全部已配置时不该报错: %v", err)
	}
	if b == nil {
		t.Fatal("Bind 不该返回 nil binding")
	}
	got := b.Load()
	if got.FeeRate != 0.02 || !got.Enabled || got.Upstream.APIKey != "sk-live" {
		t.Fatalf("填充结果不对: %+v", got)
	}
	if got.Upstream.Timeout != 3*time.Second {
		t.Fatalf("Timeout = %v，期望 3s（3000 毫秒）", got.Upstream.Timeout)
	}
	if !reflect.DeepEqual(got.Limits, []int{1, 2, 3}) {
		t.Fatalf("Limits = %v", got.Limits)
	}
}

// TestBindFailsWithoutRegisteringWhenRPCFails 守住"彻底连不上"与
// "部分缺配置"必须被区分开：前者 Bind 该返回 nil binding + 非
// MissingConfigError 的 err，且不能注册进 Client——一个连配置都没拉
// 成功的 binding 混进 Task 11 的推送触发列表毫无意义，也可能在推送
// 到达时对着一个从未成功初始化过的快照做比较。
func TestBindFailsWithoutRegisteringWhenRPCFails(t *testing.T) {
	stub := &stubServer{
		getConfig: func(*fpv1.GetConfigRequest) (*fpv1.GetConfigResponse, error) {
			return nil, status.Error(codes.Unavailable, "模拟 fp 不可达")
		},
	}
	env := newStubEnvFull(t, stub)

	b, err := Bind[bindCfg](env.client)
	if b != nil {
		t.Fatal("RPC 彻底失败时不该返回可用的 binding")
	}
	if err == nil {
		t.Fatal("RPC 失败必须报错")
	}
	var miss *MissingConfigError
	if errors.As(err, &miss) {
		t.Fatalf("err 不该是 *MissingConfigError——这是彻底连不上，不是缺配置: %v", err)
	}

	env.client.bindingsMu.Lock()
	n := len(env.client.bindings)
	env.client.bindingsMu.Unlock()
	if n != 0 {
		t.Fatalf("RPC 失败时不该注册 binding，实际注册了 %d 个", n)
	}
}

// TestRegisterBindingIsConcurrencySafe 守住 registerBinding 的并发安全。
//
// Bind 可能在业务方进程启动阶段被多个 goroutine 同时调用（不同模块各自
// Bind 自己的配置 struct）。registerBinding 内部用 bindingsMu 保护
// bindings 切片，这里直接验证：并发 Bind N 次之后，bindings 长度必须
// 精确等于 N，一次更新都不能因为并发 append 而丢失。
func TestRegisterBindingIsConcurrencySafe(t *testing.T) {
	stub := &stubServer{
		getConfig: func(*fpv1.GetConfigRequest) (*fpv1.GetConfigResponse, error) {
			return &fpv1.GetConfigResponse{Version: 1, Values: `{}`}, nil
		},
	}
	env := newStubEnvFull(t, stub)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := Bind[bindCfg](env.client); err == nil {
				t.Error("空分区应当返回 *MissingConfigError")
			}
		}()
	}
	wg.Wait()

	env.client.bindingsMu.Lock()
	got := len(env.client.bindings)
	env.client.bindingsMu.Unlock()
	if got != n {
		t.Fatalf("并发 Bind %d 次后 bindings 长度为 %d，期望 %d——"+
			"registerBinding 丢了更新", n, got, n)
	}
}

// fakeConfigRPC 是只实现 GetConfig 的最小 fpv1.ConfigServiceClient 桩，
// 直接测 fetchConfig 对 Values 字符串的边界处理。不需要起真实的 gRPC
// 服务端——fetchConfig 的职责就是"调 GetConfig、把 Values 解析成
// map[string]json.RawMessage"，桩到 RPC 客户端这一层就够钉住它的行为，
// 也是审查建议里"可以直接构造 Client 调内部方法"的做法。
type fakeConfigRPC struct {
	values string
	err    error
}

func (f *fakeConfigRPC) GetConfig(context.Context, *fpv1.GetConfigRequest, ...grpc.CallOption) (*fpv1.GetConfigResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &fpv1.GetConfigResponse{Version: 1, Values: f.values}, nil
}

// TestFetchConfigJSONBoundaries 守住 fetchConfig 对畸形/边界 Values
// 字符串的处理：该报错的都要报错（不能 panic，也不能静默吞掉当成没有
// 任何配置项），"{}"（真的什么都没配）与 "null"（服务端理论上不会发，
// 但 fetchConfig 不该假设这一点）各自的行为都要有断言钉住。
func TestFetchConfigJSONBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		values  string
		wantErr bool
		check   func(t *testing.T, got map[string]json.RawMessage)
	}{
		{name: "空字符串", values: "", wantErr: true},
		{name: "JSON数组不是对象", values: "[]", wantErr: true},
		{name: "裸数字不是对象", values: "3", wantErr: true},
		{name: "不是合法JSON", values: "{not json}", wantErr: true},
		{
			name: "空对象是合法的什么都没配置", values: "{}", wantErr: false,
			check: func(t *testing.T, got map[string]json.RawMessage) {
				if len(got) != 0 {
					t.Fatalf("空对象应解析成空 map，实际 %v", got)
				}
			},
		},
		{
			name: "null", values: "null", wantErr: false,
			check: func(t *testing.T, got map[string]json.RawMessage) {
				// encoding/json 对顶层 null 解到 map 的既定行为是把 map
				// 置为 nil（而不是保留调用前的空 map）。fetchConfig 没有
				// 特殊处理这个情况，这里把这条标准库行为钉住：调用方
				// （fillStruct）对 nil map 取值是安全的（返回零值+ok=false），
				// 不会 panic，所以不需要 fetchConfig 额外兜底。
				if got != nil {
					t.Fatalf(`Values="null" 应解析成 nil map，实际 %v`, got)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := &Client{cfgRPC: &fakeConfigRPC{values: c.values}}
			got, err := cl.fetchConfig(ConfigTypeDefault)
			if c.wantErr {
				if err == nil {
					t.Fatalf("Values=%q 应该报错，不能 panic 也不能静默接受", c.values)
				}
				return
			}
			if err != nil {
				t.Fatalf("Values=%q 不该报错: %v", c.values, err)
			}
			if c.check != nil {
				c.check(t, got)
			}
		})
	}
}
