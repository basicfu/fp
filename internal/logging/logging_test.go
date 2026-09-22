package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// newTestLogger 复用 lineHandler，但把输出接到 buf 而不是 os.Stdout，
// 供测试断言具体格式——Setup 本身只负责选 level 和设全局默认，格式的
// 全部逻辑都在 lineHandler 里。
func newTestLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(&lineHandler{level: level, out: buf})
}

// TestHandleFormatsFourTabSeparatedFields 钉住整体格式：
// `[time]\tLEVEL\tfile:line\tmsg`，四段用制表符分隔，供日志采集器按固定
// 分隔符切分。
func TestHandleFormatsFourTabSeparatedFields(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf, slog.LevelDebug)

	log.Debug("推送订单进行完成", "orderID", 1)

	line := strings.TrimRight(buf.String(), "\n")
	fields := strings.Split(line, "\t")
	if len(fields) != 4 {
		t.Fatalf("字段数 = %d，期望 4，line = %q", len(fields), line)
	}
	if !strings.HasPrefix(fields[0], "[") || !strings.HasSuffix(fields[0], "]") {
		t.Errorf("第一段 = %q，期望被方括号包住的时间", fields[0])
	}
	if fields[1] != "DEBUG" {
		t.Errorf("第二段 = %q，期望 DEBUG", fields[1])
	}
	if !strings.HasSuffix(fields[2], "logging_test.go") && !strings.Contains(fields[2], "logging_test.go:") {
		t.Errorf("第三段 = %q，期望包含调用点所在文件 logging_test.go:行号", fields[2])
	}
}

// TestHandleJoinsMessageAndAttrValuesWithSpace 钉住"msg+空格组合"这条
// 明确要求：附加字段只拼值，不带 key，与 msg 之间用一个空格分隔——
// "推送订单进行完成 1"，不是 "推送订单进行完成 orderID=1"。
func TestHandleJoinsMessageAndAttrValuesWithSpace(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf, slog.LevelDebug)

	log.Debug("支付宝明细", "seq", 2)

	line := strings.TrimRight(buf.String(), "\n")
	fields := strings.Split(line, "\t")
	if fields[len(fields)-1] != "支付宝明细 2" {
		t.Fatalf("最后一段 = %q，期望 %q", fields[len(fields)-1], "支付宝明细 2")
	}
}

// TestHandleWithAttrsAppliesToLaterRecords 钉住 logger.With(...) 附加的
// 字段也按同一条"只拼值"的规则参与拼接，且顺序在 msg 之后、Record 自带
// 的字段之前。
func TestHandleWithAttrsAppliesToLaterRecords(t *testing.T) {
	var buf bytes.Buffer
	base := newTestLogger(&buf, slog.LevelDebug)
	log := base.With("module", "order")

	log.Debug("推送订单进行完成", "id", 1)

	line := strings.TrimRight(buf.String(), "\n")
	fields := strings.Split(line, "\t")
	if fields[len(fields)-1] != "推送订单进行完成 order 1" {
		t.Fatalf("最后一段 = %q，期望 %q", fields[len(fields)-1], "推送订单进行完成 order 1")
	}
}

// TestHandleNoAttrsHasNoTrailingSpace 没有附加字段时，msg 段就是消息本身，
// 不该留下多余的尾随空格。
func TestHandleNoAttrsHasNoTrailingSpace(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf, slog.LevelDebug)

	log.Info("数据库迁移完成")

	line := strings.TrimRight(buf.String(), "\n")
	fields := strings.Split(line, "\t")
	if fields[len(fields)-1] != "数据库迁移完成" {
		t.Fatalf("最后一段 = %q，期望 %q（无尾随空格）", fields[len(fields)-1], "数据库迁移完成")
	}
}

// TestEnabledRespectsLevel 钉住 Setup 的 level 映射对 lineHandler 依然
// 生效——debug 级别的记录在 info 阈值下不该被写出来。
func TestEnabledRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf, slog.LevelInfo)

	log.Debug("不该出现")
	log.Info("该出现")

	out := buf.String()
	if strings.Contains(out, "不该出现") {
		t.Errorf("debug 级别的记录不该在 info 阈值下被写出来，out = %q", out)
	}
	if !strings.Contains(out, "该出现") {
		t.Errorf("info 级别的记录该被写出来，out = %q", out)
	}
}
