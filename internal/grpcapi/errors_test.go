package grpcapi

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/domain"
)

func TestStatusFromMapsEveryDomainError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"未找到", domain.Errorf(domain.ErrNotFound, "没有这个用户"), codes.NotFound},
		{"凭据错误", domain.Errorf(domain.ErrInvalidCredential, "密码不对"), codes.Unauthenticated},
		{"未授权", domain.Errorf(domain.ErrUnauthorized, "token 无效或已过期"), codes.Unauthenticated},
		{"禁止", domain.Errorf(domain.ErrForbidden, "应用已停用"), codes.PermissionDenied},
		{"冲突", domain.Errorf(domain.ErrConflict, "已存在"), codes.AlreadyExists},
		{"参数非法", domain.Errorf(domain.ErrInvalidArgument, "手机号格式不对"), codes.InvalidArgument},
		{"被限流", domain.Errorf(domain.ErrRateLimited, "发送太频繁"), codes.ResourceExhausted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := statusFrom(c.err)
			if c.want == codes.OK {
				if got != nil {
					t.Fatalf("nil 错误被映射成了 %v", got)
				}
				return
			}
			if status.Code(got) != c.want {
				t.Fatalf("映射成 %v，期望 %v", status.Code(got), c.want)
			}
		})
	}
}

// TestStatusFromHidesInternalDetail 守住"内部错误不外泄"。
//
// service 层的内部错误里带着 SQL 片段、连接串、表名、内部 ID。
// 这些经 gRPC 原样回给业务方（进而可能回给终端用户）就是信息泄露。
// 已识别的领域错误可以原样回——那些消息本来就是写给调用方看的。
func TestStatusFromHidesInternalDetail(t *testing.T) {
	secret := "pgx: connect postgres://postgres:hunter2@10.9.1.2:15432/fp"
	got := statusFrom(fmt.Errorf("service: 读取用户: %w", errors.New(secret)))

	if status.Code(got) != codes.Internal {
		t.Fatalf("未识别的错误映射成 %v，期望 Internal", status.Code(got))
	}
	if strings.Contains(status.Convert(got).Message(), "hunter2") ||
		strings.Contains(status.Convert(got).Message(), "10.9.1.2") {
		t.Fatalf("内部错误细节泄露到了 gRPC 响应里: %q", status.Convert(got).Message())
	}
}

// TestStatusFromKeepsDomainMessage 确认已识别错误的说明没被一起吞掉。
//
// 少了这条，一个"什么都返回 Internal 且消息为空"的实现也能让上面两个测试通过——
// 而那意味着业务方永远分不清"密码错了"和"服务挂了"。
func TestStatusFromKeepsDomainMessage(t *testing.T) {
	got := statusFrom(domain.Errorf(domain.ErrRateLimited, "验证码发送太频繁"))
	if !strings.Contains(status.Convert(got).Message(), "验证码发送太频繁") {
		t.Fatalf("领域错误的说明丢了: %q", status.Convert(got).Message())
	}
}
