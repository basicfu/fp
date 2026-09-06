package fpsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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

// ---------------------------------------------------------------------
// 以下是热更新编排（reload 的比较/换指针/回调，OnChange、OnError）的
// 测试。
//
// newTestBinding/newTestBindingWithLogger/applyForTest/sameValues 是本节
// 专用的测试辅助，绕开网络：直接构造一个 Binding[bindCfg]（specs 由
// specsOf 反射得到、snap 用 fillStruct 预置好），applyForTest 直接调
// applySnapshot——那是"拿到 values 之后做的全部事情"的内部方法，从
// reload() 里独立出来就是为了让这组测试不需要起 gRPC 桩也能跑。
// ---------------------------------------------------------------------

// baseValues 返回一份内容固定的合法配置，供本节测试反复起步用。
func baseValues() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"fee_rate":         json.RawMessage(`0.02`),
		"enabled":          json.RawMessage(`true`),
		"upstream.timeout": json.RawMessage(`3000`),
		"upstream.api_key": json.RawMessage(`"k"`),
		"limits":           json.RawMessage(`[]`),
	}
}

// newTestBinding 构造一份已经用 values 填好的 Binding[bindCfg]。ERROR
// 日志丢弃——本组测试大多数用例只关心 OnError 有没有被调用，不关心兜底
// 日志的内容；要断言日志的用 newTestBindingWithLogger。
func newTestBinding(t *testing.T, values map[string]json.RawMessage) *Binding[bindCfg] {
	t.Helper()
	c := &Client{opts: Options{Logger: slog.New(slog.DiscardHandler)}}
	return newTestBindingOn(t, c, values)
}

// newTestBindingWithLogger 和 newTestBinding 一样，但把 >= ERROR 级别的
// 日志记录转发给 logFn，供 TestReloadLogsWhenNoErrorHandler 断言"没挂
// OnError 时确实打了日志"。
func newTestBindingWithLogger(t *testing.T, values map[string]json.RawMessage, logFn func(msg string)) *Binding[bindCfg] {
	t.Helper()
	c := &Client{opts: Options{Logger: slog.New(&testErrorLogHandler{fn: logFn})}}
	return newTestBindingOn(t, c, values)
}

// newTestBindingOn 是 newTestBinding/newTestBindingWithLogger 共用的构造
// 逻辑：这一段与 Bind 首次加载做的事完全一样（specsOf 反射 + fillStruct
// 填值），只是不经过网络，直接把 snap 预置好。
func newTestBindingOn(t *testing.T, c *Client, values map[string]json.RawMessage) *Binding[bindCfg] {
	t.Helper()
	specs, err := specsOf(reflect.TypeOf(bindCfg{}))
	if err != nil {
		t.Fatalf("specsOf 失败: %v", err)
	}
	snap, missing, err := fillStruct[bindCfg](specs, values)
	if err != nil {
		t.Fatalf("fillStruct 失败: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("测试用的初始 values 不该缺项: %v", missing)
	}
	b := &Binding[bindCfg]{c: c, typ: ConfigTypeDefault, specs: specs}
	b.snap.Store(snap)
	return b
}

// testErrorLogHandler 是 newTestBindingWithLogger 用的最小 slog.Handler：
// 每条 >= Error 级别的记录都回调一次 fn，只转发消息文本，测试用不到更多。
type testErrorLogHandler struct{ fn func(msg string) }

func (h *testErrorLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelError
}

func (h *testErrorLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.fn(r.Message)
	return nil
}

func (h *testErrorLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *testErrorLogHandler) WithGroup(_ string) slog.Handler      { return h }

// applyForTest 直接调用 applySnapshot——"拿到 values 之后做的全部事情"
// 那个内部方法，绕开网络。
func (b *Binding[T]) applyForTest(t *testing.T, values map[string]json.RawMessage) {
	t.Helper()
	b.applySnapshot(values)
}

// sameValues 把一份已加载的快照重新序列化成 values map：内容与产生这份
// 快照的原始 values 完全相同，只是重新走了一遍独立的 JSON 编码——用来
// 模拟"同一分区里别人改了别的 key，fetchConfig 拉回来的仍是这份内容不变
// 的快照"这种场景，而不是简单地把同一个 map 字面量传两遍。
func sameValues(v *bindCfg) map[string]json.RawMessage {
	feeRate, _ := json.Marshal(v.FeeRate)
	enabled, _ := json.Marshal(v.Enabled)
	timeoutMs, _ := json.Marshal(v.Upstream.Timeout.Milliseconds())
	apiKey, _ := json.Marshal(v.Upstream.APIKey)
	limits, _ := json.Marshal(v.Limits)
	return map[string]json.RawMessage{
		"fee_rate":         feeRate,
		"enabled":          enabled,
		"upstream.timeout": timeoutMs,
		"upstream.api_key": apiKey,
		"limits":           limits,
	}
}

// 快照没变就不换指针、不触发 OnChange。
// 同分区里别人改了你不关心的 key 也会推给你——不比较的话，别人配置一次
// 你的连接池就重建一次。
func TestReloadWithoutChangeDoesNotFire(t *testing.T) {
	b := newTestBinding(t, map[string]json.RawMessage{
		"fee_rate":         json.RawMessage(`0.02`),
		"enabled":          json.RawMessage(`true`),
		"upstream.timeout": json.RawMessage(`3000`),
		"upstream.api_key": json.RawMessage(`"k"`),
		"limits":           json.RawMessage(`[]`),
	})
	before := b.Load()

	var calls int
	b.OnChange(func(_, _ *bindCfg) { calls++ })

	// 推一份内容完全相同的配置。
	b.applyForTest(t, sameValues(before))

	if calls != 0 {
		t.Fatalf("OnChange 被调用了 %d 次，内容没变时应当是 0 次", calls)
	}
	if b.Load() != before {
		t.Fatal("内容没变时不该换指针")
	}
}

func TestReloadFiresOnChangeWithOldAndNew(t *testing.T) {
	b := newTestBinding(t, baseValues())

	var gotOld, gotNew *bindCfg
	b.OnChange(func(o, n *bindCfg) { gotOld, gotNew = o, n })

	v := baseValues()
	v["fee_rate"] = json.RawMessage(`0.05`)
	b.applyForTest(t, v)

	if gotOld == nil || gotNew == nil {
		t.Fatal("OnChange 没被调用")
	}
	if gotOld.FeeRate != 0.02 || gotNew.FeeRate != 0.05 {
		t.Fatalf("old=%v new=%v，期望 0.02 → 0.05", gotOld.FeeRate, gotNew.FeeRate)
	}
	if b.Load().FeeRate != 0.05 {
		t.Fatal("快照没被替换")
	}
}

// key 消失（被删或回滚导致）→ 保持旧值 + OnError，**绝不清成零值**。
// 清零值是危险的：fee_rate=0 就是免手续费。
func TestReloadKeepsOldValueWhenKeyDisappears(t *testing.T) {
	b := newTestBinding(t, baseValues())

	var errs int
	b.OnError(func(error) { errs++ })

	v := baseValues()
	delete(v, "fee_rate")
	b.applyForTest(t, v)

	if got := b.Load().FeeRate; got != 0.02 {
		t.Fatalf("FeeRate = %v，key 消失时必须保持旧值 0.02", got)
	}
	if errs != 1 {
		t.Fatalf("OnError 被调用 %d 次，期望 1 次", errs)
	}
}

// 解析失败（有人把 int 改成了 object）→ 保持**整份**旧快照 + OnError，
// 进程不崩、也不半解析。
//
// 【辨别力】必须断言"值还是旧的"而不只是"报了错"——一个先逐字段写入、
// 遇到错误再返回的实现同样会报错，却已经把前面几个字段换掉了，
// 快照的跨字段一致性已经破了。
func TestReloadKeepsWholeSnapshotOnParseFailure(t *testing.T) {
	b := newTestBinding(t, baseValues())
	before := b.Load()

	var errs int
	b.OnError(func(error) { errs++ })

	v := baseValues()
	v["enabled"] = json.RawMessage(`true`)
	v["fee_rate"] = json.RawMessage(`{"a":1}`) // 类型对不上
	v["upstream.api_key"] = json.RawMessage(`"changed"`)
	b.applyForTest(t, v)

	if b.Load() != before {
		t.Fatal("解析失败时必须保持整份旧快照，指针都不该换")
	}
	if b.Load().Upstream.APIKey != "k" {
		t.Fatal("同一批里合法的字段也不该被写进去——那会破坏跨字段一致性")
	}
	if errs != 1 {
		t.Fatalf("OnError 被调用 %d 次，期望 1 次", errs)
	}
}

// 没挂 OnError 时不静默：打 ERROR 日志。
func TestReloadLogsWhenNoErrorHandler(t *testing.T) {
	var logged int
	b := newTestBindingWithLogger(t, baseValues(), func(msg string) { logged++ })

	v := baseValues()
	v["fee_rate"] = json.RawMessage(`{"a":1}`)
	b.applyForTest(t, v)

	if logged == 0 {
		t.Fatal("没挂 OnError 时必须打 ERROR 日志，不能静默")
	}
}

// TestReloadDoesNotMutateOldSnapshotSlice 守住：reload 换指针时不能就地
// 改写旧快照仍持有的切片底层数组。
//
// applyValues 用 `*out = *base` 做浅拷贝再往 out 上覆盖字段——slice 是
// 引用类型，浅拷贝之后 out 与 base 在被覆盖之前指向同一份底层数组。
// encoding/json 解码 slice 时若容量够用会原地复用旧数组，这样会连带
// 改写 base（也就是 Load() 早先返回给别的 goroutine、且被约定为不可变的
// 那份旧快照）。手工验证过：applyValues 里去掉"解码前置零"那段防护后，
// 本测试会把 old.Limits 从 [1 2 3] 眼看着被就地改写成 [9 9 3]。
//
// 【辨别力】新值的元素个数必须**不超过**旧值、内容却不同——这样才会
// 命中 slice 原地复用旧容量那个分支。只断言 new 快照正确的实现完全
// 遮不住这个问题：错误实现里 new 快照本身是对的，只是捎带手弄脏了 old。
func TestReloadDoesNotMutateOldSnapshotSlice(t *testing.T) {
	b := newTestBinding(t, baseValues())
	v1 := baseValues()
	v1["limits"] = json.RawMessage(`[1,2,3]`)
	b.applyForTest(t, v1) // 让旧快照的 Limits 有真实的底层数组可共享

	old := b.Load()
	oldLimitsCopy := append([]int(nil), old.Limits...)

	v2 := baseValues()
	v2["limits"] = json.RawMessage(`[9,9]`)
	b.applyForTest(t, v2)

	if !reflect.DeepEqual(old.Limits, oldLimitsCopy) {
		t.Fatalf("旧快照的 Limits 被就地改写: %v，期望仍是 %v（不可变快照被击穿）",
			old.Limits, oldLimitsCopy)
	}
}

// arrayOfSliceCfg 是 TestReloadDoesNotMutateOldSnapshotArrayOfSlice 专用
// 的探针类型。Grid 是"数组套 slice"（[2][]int）：数组本身是值类型，
// `*out = *base` 会把它整体复制，但复制数组是逐元素复制，每个元素
// （一个 []int）复制到的只是 slice header（ptr+len+cap），底层数组
// 仍然与 base 共享——这与顶层直接是 slice 字段（TestReloadDoesNotMutateOldSnapshotSlice
// 测的 Limits []int）是同一个别名 bug 的另一个入口，只是多包了一层
// 数组：field.Kind() 在这里是 Array 不是 Slice，只判断
// Kind()==Slice||Map 的实现完全遮不住它。
type arrayOfSliceCfg struct {
	Grid [2][]int
}

// TestReloadDoesNotMutateOldSnapshotArrayOfSlice 覆盖 Slice/Map 之外的
// 另一个别名入口：元素是引用类型的数组字段。
//
// 【辨别力】新值里 Grid[0] 的元素个数必须**不超过**旧值、内容却不同，
// 才会命中内层 slice 原地复用旧容量那个分支；Grid[1] 全程不变，确认
// "没受影响的那部分"不是靠巧合躲过去的，而是真的没被牵连。
func TestReloadDoesNotMutateOldSnapshotArrayOfSlice(t *testing.T) {
	specs, err := specsOf(reflect.TypeOf(arrayOfSliceCfg{}))
	if err != nil {
		t.Fatalf("specsOf 失败: %v", err)
	}
	snap, missing, err := fillStruct[arrayOfSliceCfg](specs, map[string]json.RawMessage{
		"grid": json.RawMessage(`[[1,2,3],[100]]`),
	})
	if err != nil || len(missing) != 0 {
		t.Fatalf("fillStruct 失败: err=%v missing=%v", err, missing)
	}
	b := &Binding[arrayOfSliceCfg]{
		c:     &Client{opts: Options{Logger: slog.New(slog.DiscardHandler)}},
		typ:   ConfigTypeDefault,
		specs: specs,
	}
	b.snap.Store(snap)

	old := b.Load()
	old0Copy := append([]int(nil), old.Grid[0]...)
	old1Copy := append([]int(nil), old.Grid[1]...)

	b.applySnapshot(map[string]json.RawMessage{
		"grid": json.RawMessage(`[[9],[100]]`),
	})

	if !reflect.DeepEqual(old.Grid[0], old0Copy) {
		t.Fatalf("旧快照的 Grid[0] 被就地改写: %v，期望仍是 %v（不可变快照被击穿）",
			old.Grid[0], old0Copy)
	}
	if !reflect.DeepEqual(old.Grid[1], old1Copy) {
		t.Fatalf("旧快照的 Grid[1] 被就地改写: %v，期望仍是 %v（未涉及的部分也不该受影响）",
			old.Grid[1], old1Copy)
	}
}

// ---------------------------------------------------------------------
// 以下两条是热更新编排的端到端覆盖：从真实的 Watch 推流收到事件，到
// Client 唯一的重载 goroutine 被唤醒、调用 fetchConfig 重新拉取、驱动
// 已注册 binding 的 OnChange。
//
// 上面的 TestReload* 全部经 newTestBinding/applyForTest 绕开了网络，直接
// 调 applySnapshot——这样测得快，但一条都没有验证 watchOnce 收到事件后
// 真的会调用 requestConfigReload、runConfigReload 真的会被唤醒并驱动到
// 已注册的 binding 上。这正是本任务开头"重载的编排"要保证的东西，用
// 既有的 stubServer/newStubEnvFull 补上这段链路。
// ---------------------------------------------------------------------

// waitGetConfigCallsSettle 等 calls（stub 的 getConfig 每被调用一次就加一）
// 在一段静默期内不再增长，再返回。
//
// 用来隔离"这条推送触发的重载"与"首次建流/Bind 顺带触发的重载"：
// watchOnce 收到首条 ready 会 requestConfigReload 一次（断线兜底，见
// client.go），这次重载和 Bind() 本身的首次加载之间没有同步点，纯靠
// 时间线判断"已经结束"并不可靠——如果测试在它还没跑完/还没被调度到
// 之前就改了服务端版本号再推事件，这次"意外"的重载有几率晚于改版本号
// 才真正执行，顺手拉到新版本、把本该由被测事件触发的效果提前"偷"走，
// 让测试即使被测的那条触发路径被删掉也还是绿的（审查已经实测复现过
// 这种假绿：删掉 ConfigChanged 分支的 requestConfigReload，仅凭 ready
// 那次意外重载，TestConfigChangedPushTriggersReload 仍然 8/8 通过）。
// 等调用次数稳定下来，才能确认后续的调用只可能来自测试接下来主动
// 触发的那条路径。
func waitGetConfigCallsSettle(t *testing.T, calls *atomic.Int64) {
	t.Helper()
	const (
		settleWindow = 300 * time.Millisecond
		pollInterval = 10 * time.Millisecond
		overallLimit = 5 * time.Second
	)
	deadline := time.Now().Add(overallLimit)
	last := calls.Load()
	stableSince := time.Now()
	for {
		if time.Now().After(deadline) {
			t.Fatalf("getConfig 调用次数在 %v 内始终没有稳定下来（当前 %d 次）",
				overallLimit, calls.Load())
		}
		time.Sleep(pollInterval)
		if cur := calls.Load(); cur != last {
			last = cur
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= settleWindow {
			return
		}
	}
}

// TestConfigChangedPushTriggersReload 守住：收到 ConfigChanged 推送之后，
// 已注册的 binding 会被自动重新拉取并触发 OnChange。
//
// 【隔离性】Bind() 之后、改服务端版本号之前，必须先用
// waitGetConfigCallsSettle 等"首次建流可能顺带触发的那次重载"彻底落定
// ——否则那次多余的重载可能晚于 version.Store(2) 才执行，顺手拉到新
// 版本，让这条测试即使被测的 ConfigChanged 分支被删掉也还是绿的（这不
// 是猜测：变异验证过，只删 client.go 里 GetConfigChanged 分支的
// requestConfigReload，不加这个等待时本测试仍然通过；加上之后才会
// 如预期变红，见 task-11-report.md）。仅仅在 Bind() 之前等
// StreamHealthy 不够——ready 触发的那次重载可能发生在 Bind()
// 注册 binding 之后，与 StreamHealthy 的时序无关。
func TestConfigChangedPushTriggersReload(t *testing.T) {
	var version atomic.Int64
	version.Store(1)
	var getConfigCalls atomic.Int64
	stub := &stubServer{
		getConfig: func(*fpv1.GetConfigRequest) (*fpv1.GetConfigResponse, error) {
			getConfigCalls.Add(1)
			v := version.Load()
			return &fpv1.GetConfigResponse{Version: v, Values: fmt.Sprintf(`{
				"fee_rate": %d,
				"enabled": true,
				"upstream.timeout": 3000,
				"upstream.api_key": "k",
				"limits": []
			}`, v)}, nil
		},
	}
	env := newStubEnvFull(t, stub)
	env.waitUntil(t, env.client.StreamHealthy, "初次建流应变为健康")

	b, err := Bind[bindCfg](env.client)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got := b.Load().FeeRate; got != 1 {
		t.Fatalf("初次加载 FeeRate = %v，期望 1", got)
	}

	// 等首次建流的 ready 可能顺带触发的重载彻底落定，见上面的隔离性说明
	// 与 waitGetConfigCallsSettle 的注释。这一步之后，version.Store(2)
	// 之前发生的 getConfig 调用已经不会再增加，接下来观察到的调用只可能
	// 来自下面主动推的 ConfigChanged。
	waitGetConfigCallsSettle(t, &getConfigCalls)

	var calls int
	var gotOld, gotNew *bindCfg
	b.OnChange(func(o, n *bindCfg) { calls++; gotOld, gotNew = o, n })

	// 服务端配置变了（比如别人在控制台改的），再推一条 ConfigChanged。
	version.Store(2)
	env.stub.events <- &fpv1.WatchResponse{
		Event: &fpv1.WatchResponse_ConfigChanged{ConfigChanged: &fpv1.ConfigChanged{
			Type: ConfigTypeDefault, Version: 2,
		}},
	}

	env.waitUntil(t, func() bool { return calls == 1 },
		"收到 ConfigChanged 推送后 OnChange 应该被触发一次")
	if gotOld == nil || gotNew == nil || gotOld.FeeRate != 1 || gotNew.FeeRate != 2 {
		t.Fatalf("old=%+v new=%+v，期望 FeeRate 1 → 2", gotOld, gotNew)
	}
	if got := b.Load().FeeRate; got != 2 {
		t.Fatalf("Load().FeeRate = %v，期望 2", got)
	}
}

// TestReconnectTriggersConfigReload 守住硬约束"收到 Watch 流的 ready 就
// 重拉一次配置"：断连期间发布的 ConfigChanged 一条都收不到（连接根本
// 不在线），配置侧要靠重连后的 ready 兜底，而不是无限期停在旧值上。
//
// 这是本任务五条硬约束里唯一没有被 Step 1 单测覆盖到的一条——那五条
// 测试全部用 newTestBinding 直接调 applySnapshot，根本碰不到 watchOnce
// 的 ready 分支，只能靠这条端到端测试补上。
func TestReconnectTriggersConfigReload(t *testing.T) {
	var version atomic.Int64
	version.Store(1)
	stub := &stubServer{
		getConfig: func(*fpv1.GetConfigRequest) (*fpv1.GetConfigResponse, error) {
			v := version.Load()
			return &fpv1.GetConfigResponse{Version: v, Values: fmt.Sprintf(`{
				"fee_rate": %d,
				"enabled": true,
				"upstream.timeout": 3000,
				"upstream.api_key": "k",
				"limits": []
			}`, v)}, nil
		},
	}
	env := newStubEnvFull(t, stub)
	env.waitUntil(t, env.client.StreamHealthy, "初次建流应变为健康")

	b, err := Bind[bindCfg](env.client)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	var calls int
	b.OnChange(func(_, _ *bindCfg) { calls++ })

	// 断线期间配置发生变化——这条变更没有、也不可能有对应的 ConfigChanged
	// 推送：客户端此刻已经不在线，fp 根本推不到它。
	env.stop()
	env.waitUntil(t, func() bool { return !env.client.StreamHealthy() }, "服务端停止后应变为不健康")
	version.Store(2)

	_, restartStop := startStub(t, env.addr, env.stub)
	t.Cleanup(restartStop)

	waitUntilTimeout(t, 20*time.Second, env.client.StreamHealthy,
		"服务端在原地址重启后，StreamHealthy() 在 20 秒内仍未重新变为 true")
	env.waitUntil(t, func() bool { return calls == 1 },
		"重连收到 ready 后应当重拉配置并触发一次 OnChange")
	if got := b.Load().FeeRate; got != 2 {
		t.Fatalf("重连后 FeeRate = %v，期望 2——断线期间发生的变更必须靠 ready 兜底补上", got)
	}
}
