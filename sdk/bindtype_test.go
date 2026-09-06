package fpsdk

import (
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// newTestTypeBinding 直接构造一份已用 values 填好的 TypeBinding，不走
// 网络——与 config_test.go 的 newTestBinding 同构。ERROR 日志丢弃；要
// 断言日志的用 newTestTypeBindingWithLogger。
func newTestTypeBinding(t *testing.T, values map[string]json.RawMessage) *TypeBinding {
	t.Helper()
	c := &Client{opts: Options{Logger: slog.New(slog.DiscardHandler)}}
	return newTestTypeBindingOn(t, c, values)
}

// newTestTypeBindingWithLogger 和 newTestTypeBinding 一样，但把 >= ERROR
// 级别的日志记录转发给 logFn，供断言"没挂 OnError 时确实打了日志"用。
// testErrorLogHandler 定义在 config_test.go，同包直接复用。
func newTestTypeBindingWithLogger(t *testing.T, values map[string]json.RawMessage, logFn func(msg string)) *TypeBinding {
	t.Helper()
	c := &Client{opts: Options{Logger: slog.New(&testErrorLogHandler{fn: logFn})}}
	return newTestTypeBindingOn(t, c, values)
}

// newTestTypeBindingOn 是上面两个构造函数共用的逻辑：直接调 applySnapshot
// 把 snap 预置好——TypeBinding 没有 Binding[T] 那种"首次加载/热重载各一条
// 路径"的区分（没有缺值概念，见 bindtype.go 的注释），BindType 本身首次
// 加载用的也是同一个 applySnapshot，这里如实复用它，不另起一套。
func newTestTypeBindingOn(t *testing.T, c *Client, values map[string]json.RawMessage) *TypeBinding {
	t.Helper()
	b := &TypeBinding{c: c, typ: ConfigTypeWeb}
	b.applySnapshot(values)
	return b
}

// applyForTest 直接调用 applySnapshot——"拿到 values 之后做的全部事情"
// 那个内部方法，绕开网络。与 Binding[T] 的同名辅助同构。
func (b *TypeBinding) applyForTest(t *testing.T, values map[string]json.RawMessage) {
	t.Helper()
	b.applySnapshot(values)
}

// 值解析成真正的 JSON 类型，不是一堆待解析的字符串——业务方
// json.Encode 出去直接就是 {"feature.new": true, "limits": [1,2,3]}。
func TestTypeBindingDecodesNativeJSON(t *testing.T) {
	b := newTestTypeBinding(t, map[string]json.RawMessage{
		"feature.new": json.RawMessage(`true`),
		"limits":      json.RawMessage(`[1,2,3]`),
		"site":        json.RawMessage(`{"title":"商城"}`),
		"title":       json.RawMessage(`"商城"`),
		"n":           json.RawMessage(`3`),
	})

	got := b.Load()
	if got["feature.new"] != true {
		t.Fatalf("feature.new = %#v，期望 bool true 而不是字符串", got["feature.new"])
	}
	if !reflect.DeepEqual(got["limits"], []any{float64(1), float64(2), float64(3)}) {
		t.Fatalf("limits = %#v，期望 []any", got["limits"])
	}
	site, ok := got["site"].(map[string]any)
	if !ok || site["title"] != "商城" {
		t.Fatalf("site = %#v，期望 map[string]any", got["site"])
	}
	if got["title"] != "商城" {
		t.Fatalf("title = %#v", got["title"])
	}
	if got["n"] != float64(3) {
		t.Fatalf("n = %#v", got["n"])
	}
}

// 内容没变不回调，与 Binding[T] 同一条规则。
func TestTypeBindingWithoutChangeDoesNotFire(t *testing.T) {
	v := map[string]json.RawMessage{"a": json.RawMessage(`1`)}
	b := newTestTypeBinding(t, v)

	var calls int
	b.OnChange(func(_, _ map[string]any) { calls++ })
	b.applyForTest(t, map[string]json.RawMessage{"a": json.RawMessage(`1`)})

	if calls != 0 {
		t.Fatalf("OnChange 被调用 %d 次，内容没变时应当是 0 次", calls)
	}
}

// 内容没变时也不该换底层指针——config_test.go 的 TestReloadWithoutChangeDoesNotFire
// 用 Load() 返回值的 != 直接验证指针恒等；TypeBinding.Load() 返回的是 map
// 值本身（Go 不允许两个非 nil map 用 != 比较），只能绕到内部字段 snap 上
// 验证底层 *map 指针没被换掉。
func TestTypeBindingWithoutChangeDoesNotSwapPointer(t *testing.T) {
	b := newTestTypeBinding(t, map[string]json.RawMessage{"a": json.RawMessage(`1`)})
	before := b.snap.Load()

	b.applyForTest(t, map[string]json.RawMessage{"a": json.RawMessage(`1`)})

	if b.snap.Load() != before {
		t.Fatal("内容没变时不该换指针")
	}
}

// 内容真的变了：OnChange 必须带上新旧两份完整快照，且 Load() 反映新值。
func TestTypeBindingFiresOnChangeWithOldAndNewOnRealChange(t *testing.T) {
	b := newTestTypeBinding(t, map[string]json.RawMessage{"a": json.RawMessage(`1`)})

	var gotOld, gotNew map[string]any
	b.OnChange(func(o, n map[string]any) { gotOld, gotNew = o, n })

	b.applyForTest(t, map[string]json.RawMessage{"a": json.RawMessage(`2`)})

	if gotOld == nil || gotNew == nil {
		t.Fatal("OnChange 没被调用")
	}
	if gotOld["a"] != float64(1) || gotNew["a"] != float64(2) {
		t.Fatalf("old=%v new=%v，期望 1 → 2", gotOld["a"], gotNew["a"])
	}
	if got := b.Load()["a"]; got != float64(2) {
		t.Fatalf("Load()[\"a\"] = %v，期望 2", got)
	}
}

// 解析失败 → 整份保持旧快照 + OnError，绝不给半成品：即便 next 里已经有
// 别的 key 解析成功，也不能让它们半路替换掉旧快照的对应值。与
// Binding[T] 的 TestReloadKeepsWholeSnapshotOnParseFailure 同一条规则。
func TestTypeBindingParseFailureKeepsWholeSnapshotAndFiresOnError(t *testing.T) {
	b := newTestTypeBinding(t, map[string]json.RawMessage{
		"a": json.RawMessage(`1`),
		"b": json.RawMessage(`2`),
	})
	before := b.Load()

	var changeCalls int
	b.OnChange(func(_, _ map[string]any) { changeCalls++ })
	var gotErr error
	b.OnError(func(err error) { gotErr = err })

	// a 能解析出新值，但 b 是非法 JSON——必须整份保持旧的，而不是
	// "a 更新到 99、b 保留旧值"这种半成品。
	b.applyForTest(t, map[string]json.RawMessage{
		"a": json.RawMessage(`99`),
		"b": json.RawMessage(`{invalid`),
	})

	if gotErr == nil {
		t.Fatal("解析失败应当触发 OnError")
	}
	if changeCalls != 0 {
		t.Fatalf("解析失败不该触发 OnChange，实际 %d 次", changeCalls)
	}
	if got := b.Load(); !reflect.DeepEqual(got, before) {
		t.Fatalf("解析失败应当整份保持旧快照，got=%#v want=%#v", got, before)
	}
}

// 没挂 OnError 时打 ERROR 日志，不静默——与 Binding[T] 的
// TestReloadLogsWhenNoErrorHandler 同一条规则。
func TestTypeBindingLogsWhenNoErrorHandler(t *testing.T) {
	var logged int
	b := newTestTypeBindingWithLogger(t,
		map[string]json.RawMessage{"a": json.RawMessage(`1`)},
		func(string) { logged++ })

	b.applyForTest(t, map[string]json.RawMessage{"a": json.RawMessage(`{invalid`)})

	if logged != 1 {
		t.Fatalf("未注册 OnError 时应打 1 条 ERROR 日志，实际 %d 条", logged)
	}
}

// Load() 在从未成功应用过任何快照时返回非 nil 的空 map，调用方可以直接
// range，不需要先判空——文档承诺的边界情况必须有测试钉住。
func TestTypeBindingLoadReturnsNonNilEmptyMapBeforeAnySnapshot(t *testing.T) {
	c := &Client{opts: Options{Logger: slog.New(slog.DiscardHandler)}}
	b := &TypeBinding{c: c, typ: ConfigTypeWeb}

	got := b.Load()
	if got == nil {
		t.Fatal("snap 为 nil 时 Load() 应返回非 nil 的空 map，而不是 nil")
	}
	if len(got) != 0 {
		t.Fatalf("Load() = %#v，期望空 map", got)
	}
}

func TestBindTypeRejectsEmptyPartition(t *testing.T) {
	// 分区必填：分区之间同名 key 是不同的配置项，没有"不传就是全部"
	// 这种语义——那会逼调用方回答"撞了算谁的"。校验必须先于对 c 的
	// 任何解引用：这里故意传 nil，若实现顺序反了会直接 panic 而不是
	// 拿到 error。
	if _, err := BindType(nil, ""); err == nil {
		t.Fatal("空分区应当被拒绝")
	}
}

// ---------------------------------------------------------------------
// 以下两条经过真实（回环）gRPC 服务端，补上 BindType 自身
// "fetchConfig → applySnapshot → registerBinding" 这段胶水代码——上面
// 全部用 newTestTypeBinding/applyForTest 绕开了网络，一条都没有真正调用
// 过 BindType（TestBindTypeRejectsEmptyPartition 传的是 nil，走的是最早
// 的分区校验分支，同样碰不到这段胶水）。与 config_test.go 里 Bind[T] 的
// TestBindFailsWithoutRegisteringWhenRPCFails 同构，复用同一套
// stubServer/newStubEnvFull 桩。
// ---------------------------------------------------------------------

// TestBindTypeFetchesConfigAndRegistersBinding 守住 BindType 成功路径：
// 真的把 typ 传给了 GetConfig、真的把返回值解析进了 Load()、也真的把
// binding 注册进了 Client（收得到后续的 ConfigChanged 推送）。
func TestBindTypeFetchesConfigAndRegistersBinding(t *testing.T) {
	stub := &stubServer{
		getConfig: func(req *fpv1.GetConfigRequest) (*fpv1.GetConfigResponse, error) {
			if req.GetType() != ConfigTypeWeb {
				t.Errorf("GetConfig 收到的 Type = %q，期望 %q", req.GetType(), ConfigTypeWeb)
			}
			return &fpv1.GetConfigResponse{
				Version: 1,
				Values:  `{"feature.new":true,"limits":[1,2,3]}`,
			}, nil
		},
	}
	env := newStubEnvFull(t, stub)

	b, err := BindType(env.client, ConfigTypeWeb)
	if err != nil {
		t.Fatalf("BindType: %v", err)
	}

	got := b.Load()
	if got["feature.new"] != true {
		t.Fatalf("feature.new = %#v，期望 true", got["feature.new"])
	}
	if !reflect.DeepEqual(got["limits"], []any{float64(1), float64(2), float64(3)}) {
		t.Fatalf("limits = %#v", got["limits"])
	}

	env.client.bindingsMu.Lock()
	n := len(env.client.bindings)
	env.client.bindingsMu.Unlock()
	if n != 1 {
		t.Fatalf("BindType 成功后应当注册 1 个 binding，实际 %d 个", n)
	}
}

// TestBindTypeFailsWithoutRegisteringWhenRPCFails 与 Binding[T] 的
// TestBindFailsWithoutRegisteringWhenRPCFails 同构：RPC 彻底失败时既要
// 报错，也不能留下一个"看似可用"的 binding 占着 bindings 列表。
func TestBindTypeFailsWithoutRegisteringWhenRPCFails(t *testing.T) {
	stub := &stubServer{
		getConfig: func(*fpv1.GetConfigRequest) (*fpv1.GetConfigResponse, error) {
			return nil, status.Error(codes.Unavailable, "模拟 fp 不可达")
		},
	}
	env := newStubEnvFull(t, stub)

	b, err := BindType(env.client, ConfigTypeWeb)
	if b != nil {
		t.Fatal("RPC 彻底失败时不该返回可用的 binding")
	}
	if err == nil {
		t.Fatal("RPC 失败必须报错")
	}

	env.client.bindingsMu.Lock()
	n := len(env.client.bindings)
	env.client.bindingsMu.Unlock()
	if n != 0 {
		t.Fatalf("RPC 失败时不该注册 binding，实际注册了 %d 个", n)
	}
}
