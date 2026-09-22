package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestLogger 复用 lineHandler，但把输出接到 buf 而不是 os.Stdout，
// 供测试断言具体格式——Setup 本身只负责选 level 和设全局默认，格式的
// 全部逻辑都在 lineHandler 里。
func newTestLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(&lineHandler{level: level, out: buf, mu: &sync.Mutex{}})
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

// withFileLogDir 把包级变量 fileLogDir 换成 dir，测试结束后换回去——
// Setup 里硬编码的是 /logs，测试不能真的写这个目录，只能通过这个包级
// 变量把它指到 t.TempDir()。
func withFileLogDir(t *testing.T, dir string) {
	t.Helper()
	old := fileLogDir
	fileLogDir = dir
	t.Cleanup(func() { fileLogDir = old })
}

// TestSetupDevDoesNotTouchFileLogDir 钉住 dev 环境只打 stdout，不碰文件
// 日志目录——本机开发不该在磁盘上留痕，目录都不该被创建出来。
func TestSetupDevDoesNotTouchFileLogDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	withFileLogDir(t, dir)

	Setup("info", "dev")
	slog.Info("不该落盘")

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dev 环境下 fileLogDir 不该被创建，stat err = %v", err)
	}
}

// TestSetupNonDevAlsoWritesToTodayFile 钉住"同时写入 /logs 目录，文件名
// 按当天日期"这条明确要求，env 大小写不敏感（EqualFold 判断是不是 dev）。
func TestSetupNonDevAlsoWritesToTodayFile(t *testing.T) {
	dir := t.TempDir()
	withFileLogDir(t, dir)

	Setup("info", "PROD")
	t.Cleanup(closeLastFileWriter)
	slog.Info("落盘的消息")

	today := time.Now().Format("2006-01-02")
	data, err := os.ReadFile(filepath.Join(dir, today+".log"))
	if err != nil {
		t.Fatalf("读今天的日志文件失败: %v", err)
	}
	if !strings.Contains(string(data), "落盘的消息") {
		t.Fatalf("文件内容 = %q，应该包含刚打的那条消息", string(data))
	}
}

// TestSetupFallsBackToStdoutWhenLogDirUnwritable 钉住"写不了文件不该拦住
// 启动"：把 fileLogDir 指向一个不可能成为目录的路径（拿一个普通文件当
// 父目录），Setup 应该照样返回一个能用的 logger，而不是 panic 或返回错误
// （它的签名压根没有 error 返回值）。
func TestSetupFallsBackToStdoutWhenLogDirUnwritable(t *testing.T) {
	blockingFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("建阻挡文件失败: %v", err)
	}
	withFileLogDir(t, filepath.Join(blockingFile, "logs"))

	l := Setup("info", "prod")
	if l == nil {
		t.Fatal("Setup 应该始终返回一个可用的 logger")
	}
}

// TestDailyFileWriterWritesToTodayFile 钉住基本写入行为：内容落进
// `<dir>/<今天日期>.log`。
func TestDailyFileWriterWritesToTodayFile(t *testing.T) {
	dir := t.TempDir()
	w, err := newDailyFileWriter(dir, 30)
	if err != nil {
		t.Fatalf("newDailyFileWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	data, err := os.ReadFile(filepath.Join(dir, today+".log"))
	if err != nil {
		t.Fatalf("读today文件失败: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("文件内容 = %q，期望 %q", string(data), "hello\n")
	}
}

// TestDailyFileWriterRotateSwitchesToNewDatedFile 直接测 rotate 本身
// （不经 Write）：调用后应该切到新日期对应的文件，旧文件的内容原样留着
// 不受影响。
//
// 不通过 Write 触发：Write 内部是拿**真实的** time.Now() 跟 w.date 比较
// 决定要不要 rotate，如果这里先手工 rotate(tomorrow) 把 w.date 拨到明天，
// 紧接着再调 Write，Write 自己又会用真实的"今天"跟明天一比不相等，
// 反手把它 rotate 回今天——测的就不是 rotate 这个方法本身了。所以这里
// 验证 rotate 效果时，写入直接落在 w.file 上，绕开 Write 那层自动判断。
func TestDailyFileWriterRotateSwitchesToNewDatedFile(t *testing.T) {
	dir := t.TempDir()
	w, err := newDailyFileWriter(dir, 30)
	if err != nil {
		t.Fatalf("newDailyFileWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if _, err := w.file.Write([]byte("day one\n")); err != nil {
		t.Fatalf("写第一天: %v", err)
	}
	firstDate := w.date

	other := time.Now().AddDate(0, 0, 5)
	if err := w.rotate(other); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if w.date == firstDate {
		t.Fatalf("rotate 之后 w.date 还是 %q，应该已经切到新日期", w.date)
	}
	if _, err := w.file.Write([]byte("day two\n")); err != nil {
		t.Fatalf("写第二天: %v", err)
	}

	firstData, err := os.ReadFile(filepath.Join(dir, firstDate+".log"))
	if err != nil {
		t.Fatalf("读第一天的文件失败: %v", err)
	}
	if string(firstData) != "day one\n" {
		t.Fatalf("第一天文件内容 = %q，期望 %q（不该被第二天的写入影响）", string(firstData), "day one\n")
	}

	secondData, err := os.ReadFile(filepath.Join(dir, other.Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatalf("读第二天的文件失败: %v", err)
	}
	if string(secondData) != "day two\n" {
		t.Fatalf("第二天文件内容 = %q，期望 %q", string(secondData), "day two\n")
	}
}

// TestDailyFileWriterPrunesFilesOlderThanRetention 预先放几份不同日期的
// 旧文件，构造 dailyFileWriter（构造时会 rotate 一次，顺带清理）之后，
// 超过保留期的应该被删掉，没超过的原样留着。
func TestDailyFileWriterPrunesFilesOlderThanRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	old := now.AddDate(0, 0, -40).Format("2006-01-02") + ".log"
	recent := now.AddDate(0, 0, -10).Format("2006-01-02") + ".log"
	notLog := "readme.txt"
	for _, name := range []string{old, recent, notLog} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("建 %s 失败: %v", name, err)
		}
	}

	w, err := newDailyFileWriter(dir, 30)
	if err != nil {
		t.Fatalf("newDailyFileWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Errorf("超过 30 天保留期的 %s 应该已被删除，stat err = %v", old, err)
	}
	if _, err := os.Stat(filepath.Join(dir, recent)); err != nil {
		t.Errorf("没超过保留期的 %s 不该被删，stat err = %v", recent, err)
	}
	if _, err := os.Stat(filepath.Join(dir, notLog)); err != nil {
		t.Errorf("不是 .log 结尾的文件不该被当成日志清掉，stat err = %v", err)
	}
}
