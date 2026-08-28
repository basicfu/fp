package notify_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/notify"
)

// withCapturedLogs 临时把 slog 默认 logger 换成写入返回值的 handler，
// t.Cleanup 里恢复。notify 包内部（无论 Sender.Send 对供应商失败的记录，
// 还是 LoggingFakeProvider）用的都是包级 slog.Warn，只能借道默认 logger
// 才能在测试里观察到输出。
//
// 本包（含 notify_test.go）没有任何测试用 t.Parallel，临时替换全局默认
// logger 不会和其他测试产生竞态。
func withCapturedLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestLoggingFakeProviderLogsOnSuccess(t *testing.T) {
	buf := withCapturedLogs(t)
	p := notify.NewLoggingFakeProvider(notify.ChannelSMS, "fake")

	if err := p.Send(context.Background(), msg("13800138000")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// 委托必须生效：包一层之后，既有的 Sent()/LastParam() 读取方式不能失效
	// ——examples/demo 与其他测试都靠它们工作。
	if p.LastParam("code") != "123456" {
		t.Fatalf("LastParam(code) = %q, want 123456", p.LastParam("code"))
	}

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("日志级别不是 WARN，output = %q", out)
	}
	if !strings.Contains(out, "123456") {
		t.Fatalf("日志里没有验证码，output = %q", out)
	}
	if !strings.Contains(out, "13800138000") {
		t.Fatalf("日志里没有接收方，output = %q", out)
	}
}

func TestLoggingFakeProviderDoesNotLogOnFailure(t *testing.T) {
	buf := withCapturedLogs(t)
	p := notify.NewLoggingFakeProvider(notify.ChannelSMS, "fake")
	p.FailNext(errors.New("boom"))

	if err := p.Send(context.Background(), msg("13800138000")); err == nil {
		t.Fatal("Send 应返回 FailNext 预置的错误")
	}

	// 发送失败没有码可打，不该留下一条看起来像"已发出"的日志。
	if buf.Len() != 0 {
		t.Fatalf("发送失败不该打日志，output = %q", buf.String())
	}
}
