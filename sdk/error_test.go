package fpsdk

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// statusWithDetail 造一个带 ErrorDetail 的 gRPC error，模拟新版 fp 的返回。
func statusWithDetail(t *testing.T, c codes.Code, code, msg, detail string) error {
	t.Helper()
	st, err := status.New(c, msg).WithDetails(&fpv1.ErrorDetail{
		Code: code, Msg: msg, Detail: detail,
	})
	if err != nil {
		t.Fatalf("构造 status: %v", err)
	}
	return st.Err()
}

func TestTranslateExposesStructuredError(t *testing.T) {
	in := statusWithDetail(t, codes.PermissionDenied,
		"ACCOUNT_FROZEN", "账号已被冻结", `{"desc":"user=01a0 status=FROZEN"}`)

	got := translate(in)

	var fe *Error
	if !errors.As(got, &fe) {
		t.Fatalf("errors.As 取不到 *fpsdk.Error: %v", got)
	}
	if fe.Code != "ACCOUNT_FROZEN" {
		t.Fatalf("Code = %q", fe.Code)
	}
	if fe.Msg != "账号已被冻结" {
		t.Fatalf("Msg = %q", fe.Msg)
	}
	if fe.Detail != `{"desc":"user=01a0 status=FROZEN"}` {
		t.Fatalf("Detail = %q", fe.Detail)
	}
	// Error() 返回的就是可以直接展示给用户的那句话。
	if got.Error() != "账号已被冻结" {
		t.Fatalf("Error() = %q", got.Error())
	}
}

// 【辨别力】老接入方的 errors.Is 必须继续成立。
//
// 这是"零改动升级"这个承诺的唯一守卫。一个把 Unwrap 写成只返回 cause
// （或干脆不实现 Unwrap）的实现，会通过上面那条 errors.As 测试，却让
// 所有既有接入方的 if errors.Is(err, ErrUnauthorized) 在升级后静默失效
// ——他们的鉴权分支会走进 else，把未授权当成别的错误处理。
func TestStructuredErrorStillMatchesLegacySentinel(t *testing.T) {
	cases := []struct {
		name     string
		grpcCode codes.Code
		want     error
	}{
		{"未授权", codes.Unauthenticated, ErrUnauthorized},
		{"禁止", codes.PermissionDenied, ErrUnauthorized},
		{"未找到", codes.NotFound, ErrUnauthorized},
		{"参数非法", codes.InvalidArgument, ErrInvalidArgument},
		{"限流", codes.ResourceExhausted, ErrRateLimited},
		{"不可达", codes.Unavailable, ErrUnavailable},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			in := statusWithDetail(t, tt.grpcCode, "SOME_CODE", "某某错误", "")
			got := translate(in)

			if !errors.Is(got, tt.want) {
				t.Fatalf("errors.Is(err, %v) = false——老接入方的判定失效了", tt.want)
			}
			// 原始 gRPC status 也要还能取到，排查连接层问题时要用。
			if status.Code(got) != tt.grpcCode {
				t.Fatalf("status.Code = %v, want %v", status.Code(got), tt.grpcCode)
			}
		})
	}
}

// 【辨别力】老服务端不带 ErrorDetail 时，行为必须与升级前完全一致。
//
// 没有这条的话，一个"总是构造 *Error"的实现会让每个错误都变成 Code 为
// 空串的结构化错误——接入方按 Code 分支时会全部落进 default，比不给
// 结构化错误更糟。
func TestFallsBackToFlatSentinelWithoutDetail(t *testing.T) {
	plain := status.Error(codes.Unauthenticated, "token 无效或已过期")

	got := translate(plain)

	var fe *Error
	if errors.As(got, &fe) {
		t.Fatalf("服务端没带 ErrorDetail 时不该构造 *fpsdk.Error，实际拿到 %+v", fe)
	}
	if !errors.Is(got, ErrUnauthorized) {
		t.Fatal("退回扁平哨兵后 errors.Is 也不成立了")
	}
	// 原始消息不能丢——那是升级前就有的行为。
	if got.Error() == "" {
		t.Fatal("错误消息丢了")
	}
}

// ErrorDetail 里 code 为空串时同样按"没带"处理：一个空 Code 对调用方
// 没有任何价值，让它退回扁平哨兵比给出一个分支不了的结构化错误更好。
func TestEmptyCodeIsTreatedAsAbsent(t *testing.T) {
	in := statusWithDetail(t, codes.Unauthenticated, "", "某某错误", "")

	var fe *Error
	if errors.As(translate(in), &fe) {
		t.Fatalf("code 为空串时不该构造 *fpsdk.Error，实际拿到 %+v", fe)
	}
}

func TestTranslateNilIsNil(t *testing.T) {
	if got := translate(nil); got != nil {
		t.Fatalf("translate(nil) = %v, want nil", got)
	}
}
