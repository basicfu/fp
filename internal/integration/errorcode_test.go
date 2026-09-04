package integration_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/grpcapi"
	"github.com/basicfu/fp/internal/httpapi"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// wantTransport 是每个哨兵在两个传输层上的期望取值。
//
// 这张表是**协议契约本身**，不是从实现抄来的——改实现不改这里，测试就该红。
var wantTransport = map[error]struct {
	httpStatus int
	grpcCode   codes.Code
}{
	domain.ErrNotFound:          {http.StatusNotFound, codes.NotFound},
	domain.ErrInvalidCredential: {http.StatusUnauthorized, codes.Unauthenticated},
	domain.ErrUnauthorized:      {http.StatusUnauthorized, codes.Unauthenticated},
	domain.ErrForbidden:         {http.StatusForbidden, codes.PermissionDenied},
	domain.ErrConflict:          {http.StatusConflict, codes.AlreadyExists},
	domain.ErrInvalidArgument:   {http.StatusBadRequest, codes.InvalidArgument},
	domain.ErrRateLimited:       {http.StatusTooManyRequests, codes.ResourceExhausted},
	domain.ErrInternal:          {http.StatusInternalServerError, codes.Internal},
}

// TestTransportsAgreeOnEveryCode 遍历全部已登记的错误码，断言同一个错误
// 经 HTTP 与 gRPC 两条路径出来时，code 相同、且状态码互相对应。
//
// 【辨别力】这条测试的价值在于**同时**走两条路径。此前 httpapi 与 grpcapi
// 各写一份 switch，靠注释约定"分支顺序与取值刻意一一对应"——那句注释是对
// 的，但没有任何测试守住它：任何一边加一个分支、或改一个取值，另一边不动
// 也照样全绿，直到线上同一个失败经 HTTP 是 403、经 gRPC 是 NotFound，
// 排障时两边日志对不上。
func TestTransportsAgreeOnEveryCode(t *testing.T) {
	for _, code := range domain.AllCodes() {
		sentinel, ok := domain.SentinelFor(code)
		if !ok {
			t.Errorf("码 %q 没有登记哨兵", code)
			continue
		}
		want, ok := wantTransport[sentinel]
		if !ok {
			t.Errorf("码 %q 的哨兵 %v 不在传输层期望表里——"+
				"新增哨兵时必须同时在这里登记它的 HTTP 状态码与 gRPC code", code, sentinel)
			continue
		}

		gotHTTP := httpapi.StatusFor(sentinel)
		if gotHTTP != want.httpStatus {
			t.Errorf("码 %q: HTTP 状态码 = %d, want %d", code, gotHTTP, want.httpStatus)
		}

		gotGRPC := grpcapi.CodeFor(sentinel)
		if gotGRPC != want.grpcCode {
			t.Errorf("码 %q: gRPC code = %v, want %v", code, gotGRPC, want.grpcCode)
		}
	}
}

// TestGRPCCarriesErrorDetail 断言 code/msg/detail 三元组真的随 gRPC status
// 传出去了，而不是只在服务端结构体里存在。
//
// 【辨别力】这正是本次要修的那个缺陷的形状：服务端有信息、传输层丢了，
// 而只测服务端返回值的测试照样全绿。所以这条必须从 statusFrom 的**输出**
// 反解，不能去读输入的 domain.Error。
func TestGRPCCarriesErrorDetail(t *testing.T) {
	in := domain.Fail(domain.ErrForbidden, domain.CodeAccountFrozen, "账号已被冻结").
		WithDesc("user=01a0 status=FROZEN")

	st, ok := status.FromError(grpcapi.StatusFrom(in))
	if !ok {
		t.Fatal("statusFrom 的返回值不是一个 gRPC status")
	}
	if st.Code() != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", st.Code())
	}
	if st.Message() != "账号已被冻结" {
		t.Fatalf("message = %q", st.Message())
	}

	var detail *fpv1.ErrorDetail
	for _, d := range st.Details() {
		if ed, ok := d.(*fpv1.ErrorDetail); ok {
			detail = ed
			break
		}
	}
	if detail == nil {
		t.Fatal("gRPC status 里没有 ErrorDetail——三元组没有传出去")
	}
	if detail.GetCode() != domain.CodeAccountFrozen {
		t.Fatalf("detail.code = %q, want %q", detail.GetCode(), domain.CodeAccountFrozen)
	}
	if detail.GetMsg() != "账号已被冻结" {
		t.Fatalf("detail.msg = %q", detail.GetMsg())
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(detail.GetDetail()), &got); err != nil {
		t.Fatalf("detail 不是合法 JSON: %v（原文 %q）", err, detail.GetDetail())
	}
	if got["desc"] != "user=01a0 status=FROZEN" {
		t.Fatalf("desc = %v", got["desc"])
	}
}

// TestUnrecognizedErrorLeaksNothingOverGRPC 断言未识别的内部错误经 gRPC
// 出去时不泄露原始消息，也不带 ErrorDetail。HTTP 那一半见
// internal/httpapi/respond_test.go。
//
// 【辨别力】断言的是"响应里**不含**那段敏感文本"，而不是"响应等于某个
// 固定字符串"——后者在实现改了通用文案时会误红，而真正要守住的性质是
// 连接串、SQL、表名不出现在响应里。
func TestUnrecognizedErrorLeaksNothingOverGRPC(t *testing.T) {
	const secret = `pq: password authentication failed for user "postgres" host=10.9.1.2`
	raw := errors.New(secret)

	// HTTP 那一半在 internal/httpapi 的白盒测试里（writeError 未导出）。
	// 这里只验 gRPC。
	st, _ := status.FromError(grpcapi.StatusFrom(raw))
	if st.Code() != codes.Internal {
		t.Fatalf("gRPC code = %v, want Internal", st.Code())
	}
	if st.Message() == secret {
		t.Fatal("gRPC status message 泄露了原始错误消息")
	}
	for _, d := range st.Details() {
		if ed, ok := d.(*fpv1.ErrorDetail); ok {
			t.Fatalf("未识别错误不该带 ErrorDetail，实际带了 %+v", ed)
		}
	}
}
