package domain_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

func TestFailCarriesCodeMsgAndSentinel(t *testing.T) {
	err := domain.Fail(domain.ErrForbidden, domain.CodeAccountFrozen, "账号已被冻结")

	if err.Code != domain.CodeAccountFrozen {
		t.Fatalf("Code = %q, want %q", err.Code, domain.CodeAccountFrozen)
	}
	if err.Msg != "账号已被冻结" {
		t.Fatalf("Msg = %q", err.Msg)
	}
	// Error() 返回 Msg：它面向终端用户，接入方直接展示的就是这句。
	if err.Error() != "账号已被冻结" {
		t.Fatalf("Error() = %q, want Msg", err.Error())
	}
	// 哨兵决定状态码映射，errors.Is 必须继续可用。
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("errors.Is(err, ErrForbidden) = false")
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatal("errors.Is 命中了不相干的哨兵")
	}
}

// 【辨别力】Detail 为空时 detail 必须是空串而不是 "{}"。
//
// 让"没有细节"在线上是一个明确的空值：接入方判断 `detail == ""` 即可，
// 不必先 JSON.parse 出一个空对象再判断它没有字段。写成 "{}" 的实现会
// 通过所有只检查"能不能解析"的测试。
func TestDetailJSONEmptyWhenNoFields(t *testing.T) {
	err := domain.Fail(domain.ErrNotFound, domain.CodeUserNotFound, "用户不存在")
	if got := err.DetailJSON(); got != "" {
		t.Fatalf("DetailJSON() = %q, want 空串", got)
	}
}

func TestWithDescWritesDescField(t *testing.T) {
	err := domain.Fail(domain.ErrForbidden, domain.CodeAccountFrozen, "账号已被冻结").
		WithDesc("user=%s status=%s", "01a0", "FROZEN")

	var got map[string]any
	if e := json.Unmarshal([]byte(err.DetailJSON()), &got); e != nil {
		t.Fatalf("detail 不是合法 JSON: %v（原文 %q）", e, err.DetailJSON())
	}
	if got["desc"] != "user=01a0 status=FROZEN" {
		t.Fatalf("desc = %v", got["desc"])
	}
}

// 默认详情字段叫 desc 而不是 detail——避免 detail.detail 这种嵌套。
func TestDefaultDetailFieldIsNamedDesc(t *testing.T) {
	err := domain.Fail(domain.ErrInternal, domain.CodeInternal, "服务器内部错误").WithDesc("x")

	var got map[string]any
	if e := json.Unmarshal([]byte(err.DetailJSON()), &got); e != nil {
		t.Fatalf("detail 不是合法 JSON: %v", e)
	}
	if _, ok := got["detail"]; ok {
		t.Fatal("detail 里出现了名为 detail 的字段，应当叫 desc")
	}
	if _, ok := got["desc"]; !ok {
		t.Fatalf("detail 里没有 desc 字段: %v", got)
	}
}

func TestWithFieldAddsArbitraryKeys(t *testing.T) {
	err := domain.Fail(domain.ErrRateLimited, domain.CodeRateLimited, "操作过于频繁，请稍后再试").
		WithDesc("短信发送限流").
		WithField("retryAfterMs", int64(42000))

	var got map[string]any
	if e := json.Unmarshal([]byte(err.DetailJSON()), &got); e != nil {
		t.Fatalf("detail 不是合法 JSON: %v", e)
	}
	if got["desc"] != "短信发送限流" {
		t.Fatalf("desc = %v", got["desc"])
	}
	// JSON 数字反序列化成 float64，比较时按数值比。
	if got["retryAfterMs"] != float64(42000) {
		t.Fatalf("retryAfterMs = %v (%T)", got["retryAfterMs"], got["retryAfterMs"])
	}
}

// 【辨别力】链式调用必须能叠加，不能后一次覆盖前一次的整个 Detail。
//
// 一个把 WithField 写成 e.Detail = map[string]any{key: value} 的实现，
// 单独测任何一个字段都会通过——只有同时设置两个字段才分得出来。
func TestChainedFieldsAccumulate(t *testing.T) {
	err := domain.Fail(domain.ErrInvalidArgument, domain.CodeConnectorConfigInvalid, "登录方式配置不合法").
		WithField("field", "allowPhone").
		WithDesc("需要布尔值，收到 string").
		WithField("connectorType", "password")

	var got map[string]any
	if e := json.Unmarshal([]byte(err.DetailJSON()), &got); e != nil {
		t.Fatalf("detail 不是合法 JSON: %v", e)
	}
	for _, k := range []string{"field", "desc", "connectorType"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("链式调用丢了字段 %q，实际 detail = %v", k, got)
		}
	}
}

// 【辨别力】errors.As 必须能从被包装的错误里取回 *domain.Error。
//
// service 层习惯用 fmt.Errorf("...: %w", err) 往上包，如果 Error 没有
// 正确参与 errors 链，传输层就取不到 code，退化成 INTERNAL——那正是
// 本次要修的缺陷的翻版。
func TestErrorSurvivesWrapping(t *testing.T) {
	base := domain.Fail(domain.ErrForbidden, domain.CodeAppDisabled, "应用已停用")
	wrapped := fmt.Errorf("service: 查询应用: %w", base)

	var got *domain.Error
	if !errors.As(wrapped, &got) {
		t.Fatal("errors.As 无法从包装后的错误里取回 *domain.Error")
	}
	if got.Code != domain.CodeAppDisabled {
		t.Fatalf("Code = %q", got.Code)
	}
	// 哨兵同样要穿过包装。
	if !errors.Is(wrapped, domain.ErrForbidden) {
		t.Fatal("包装后 errors.Is(ErrForbidden) = false")
	}
}

// 码表是对外契约：每个码都必须登记，且登记的哨兵与实际构造时用的一致。
func TestEveryCodeIsRegistered(t *testing.T) {
	for _, code := range domain.AllCodes() {
		sentinel, ok := domain.SentinelFor(code)
		if !ok {
			t.Errorf("码 %q 没有登记哨兵", code)
			continue
		}
		if sentinel == nil {
			t.Errorf("码 %q 登记的哨兵是 nil", code)
		}
	}
}

// 【辨别力】码不能重复登记，也不能有码只存在于常量而不在注册表里。
//
// 这条挡的是"加了个新码常量却忘了登记"——那样传输层拿到它会走 default
// 分支，静默降级成 500，而所有既有测试照绿。
func TestNoCodeIsMissingFromRegistry(t *testing.T) {
	all := domain.AllCodes()
	seen := map[string]bool{}
	for _, c := range all {
		if seen[c] {
			t.Errorf("码 %q 在注册表里出现了多次", c)
		}
		seen[c] = true
	}
	if len(all) == 0 {
		t.Fatal("注册表是空的")
	}
}
