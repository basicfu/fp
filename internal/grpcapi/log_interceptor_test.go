package grpcapi

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/basicfu/fp/internal/domain"
)

func captureLogs(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func runFailing(err error) {
	info := &grpc.UnaryServerInfo{FullMethod: "/fp.v1.AuthService/SendLoginCode"}
	_, _ = logFailedUnary(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return nil, StatusFrom(err)
	})
}

// 线上出过：应用没开放 sms_code，接入方只拿到 CONNECTOR_DISABLED，fp 这边一行日志都没有。
func TestLogFailedUnaryRecordsWhyTheCallWasRefused(t *testing.T) {
	buf := captureLogs(t, slog.LevelInfo)
	runFailing(domain.Failf(domain.ErrForbidden, domain.CodeConnectorDisabled, "该应用未开放 %s 登录", "sms_code"))

	got := buf.String()
	for _, want := range []string{"level=WARN", "/fp.v1.AuthService/SendLoginCode", "CONNECTOR_DISABLED", "sms_code"} {
		if !strings.Contains(got, want) {
			t.Errorf("日志里没有 %q: %s", want, got)
		}
	}
}

// 密码错、token 过期这类终端用户造成的失败不能刷屏。
func TestLogFailedUnaryKeepsUserMistakesQuiet(t *testing.T) {
	buf := captureLogs(t, slog.LevelInfo)
	runFailing(domain.Fail(domain.ErrInvalidCredential, domain.CodeCredentialInvalid, "账号或密码不正确"))

	if buf.Len() != 0 {
		t.Errorf("凭据错误不该出现在 info 及以上的日志里: %s", buf.String())
	}
}

func TestLogFailedUnaryStaysSilentOnSuccess(t *testing.T) {
	buf := captureLogs(t, slog.LevelDebug)
	info := &grpc.UnaryServerInfo{FullMethod: "/fp.v1.AuthService/Login"}
	_, _ = logFailedUnary(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return "ok", nil
	})
	if buf.Len() != 0 {
		t.Errorf("成功的调用不该打日志: %s", buf.String())
	}
}
