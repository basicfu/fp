package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

// 白盒测试：writeError 与 errorBody 都未导出。同目录的 static_test.go
// 已是同样的做法。

// writeErrorAndDecode 走一遍真实的 writeError，返回状态码与响应体。
// 刻意经 httptest 而不是直接看入参——要测的正是"写出去的是什么"。
func writeErrorAndDecode(t *testing.T, err error) (int, errorBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	writeError(rec, err)

	var body errorBody
	if e := json.NewDecoder(rec.Body).Decode(&body); e != nil {
		t.Fatalf("响应体不是合法 JSON: %v", e)
	}
	return rec.Code, body
}

func TestWriteErrorCarriesCodeMsgDetail(t *testing.T) {
	err := domain.Fail(domain.ErrForbidden, domain.CodeAccountFrozen, "账号已被冻结").
		WithDesc("user=01a0 status=FROZEN")

	status, body := writeErrorAndDecode(t, err)

	if status != http.StatusForbidden {
		t.Fatalf("状态码 = %d, want 403", status)
	}
	if body.Code != domain.CodeAccountFrozen {
		t.Fatalf("code = %q", body.Code)
	}
	if body.Msg != "账号已被冻结" {
		t.Fatalf("msg = %q", body.Msg)
	}

	var detail map[string]any
	if e := json.Unmarshal([]byte(body.Detail), &detail); e != nil {
		t.Fatalf("detail 不是合法 JSON: %v（原文 %q）", e, body.Detail)
	}
	if detail["desc"] != "user=01a0 status=FROZEN" {
		t.Fatalf("desc = %v", detail["desc"])
	}
}

// 【辨别力】没有细节时 detail 字段整个不出现，而不是出现一个空串或 "{}"。
//
// 这条守的是 omitempty 与 DetailJSON 的空值约定合起来的效果：接入方判断
// "有没有细节"只需看字段在不在。一个把 DetailJSON 写成永远返回 "{}" 的
// 实现，会通过所有只检查"detail 能不能解析"的测试。
func TestWriteErrorOmitsEmptyDetail(t *testing.T) {
	err := domain.Fail(domain.ErrNotFound, domain.CodeUserNotFound, "用户不存在")

	rec := httptest.NewRecorder()
	writeError(rec, err)

	raw := rec.Body.String()
	if strings.Contains(raw, "detail") {
		t.Fatalf("没有细节时响应体不该出现 detail 字段，实际 %s", raw)
	}
}

// 【辨别力】未识别的内部错误不许把原始消息带出去。
//
// 断言的是"响应里**不含**那段敏感文本"，而不是"响应等于某个固定字符串"
// ——后者在实现改了通用文案时会误红，而真正要守住的性质是连接串、SQL、
// 表名不出现在响应里。
func TestWriteErrorHidesUnrecognizedError(t *testing.T) {
	const secret = `pq: password authentication failed for user "postgres" host=10.9.1.2`

	rec := httptest.NewRecorder()
	writeError(rec, errors.New(secret))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "postgres") || strings.Contains(rec.Body.String(), "10.9.1.2") {
		t.Fatalf("响应泄露了原始错误消息: %s", rec.Body.String())
	}

	var body errorBody
	if e := json.NewDecoder(rec.Body).Decode(&body); e != nil {
		t.Fatalf("响应体不是合法 JSON: %v", e)
	}
	if body.Code != domain.CodeInternal {
		t.Fatalf("code = %q, want %q", body.Code, domain.CodeInternal)
	}
	if body.Detail != "" {
		t.Fatalf("未识别错误的 detail 应当为空，实际 %q", body.Detail)
	}
}

// 【辨别力】被 fmt.Errorf 包装过的领域错误仍然要能取到码。
//
// service 层习惯往上包一层上下文。如果 writeError 用类型断言而不是
// errors.As，包装后就取不到 *domain.Error，全部降级成 500——那正是
// 本次要修的缺陷的翻版，而且只有包装过的调用点才会暴露。
func TestWriteErrorUnwrapsWrappedDomainError(t *testing.T) {
	base := domain.Fail(domain.ErrConflict, domain.CodeSlugTaken, "slug 已被占用")
	wrapped := errors.Join(errors.New("service: 创建应用"), base)

	status, body := writeErrorAndDecode(t, wrapped)

	if status != http.StatusConflict {
		t.Fatalf("状态码 = %d, want 409", status)
	}
	if body.Code != domain.CodeSlugTaken {
		t.Fatalf("code = %q, want %q", body.Code, domain.CodeSlugTaken)
	}
}

func TestDecodeJSONReturnsInvalidArgumentCode(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"bogus":`))
	var dst struct{ Name string }

	err := decodeJSON(req, &dst)

	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("decodeJSON 返回的不是 *domain.Error: %v", err)
	}
	if de.Code != domain.CodeInvalidArgument {
		t.Fatalf("code = %q", de.Code)
	}
	// 解析器的原始错误进 detail 而不是 msg：msg 要给终端用户看。
	if strings.Contains(de.Msg, "unexpected EOF") {
		t.Fatalf("解析器的原始错误漏进了 msg: %q", de.Msg)
	}
}
