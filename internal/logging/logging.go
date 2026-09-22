// Package logging 初始化进程级结构化日志。
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Setup 按 level 创建 lineHandler 并设为全局默认。level 无法识别时回退到 info。
//
// 输出格式是 `[time]\tLEVEL\t file:line\tmsg`（制表符分隔，四段），供日志
// 采集器按固定分隔符切分，不是 JSON——msg 这一段把消息文本与所有附加字段
// 的值用空格拼接在一起，不带 key（"推送订单进行完成 1" 而不是
// "推送订单进行完成 orderID=1"），这是采集端明确要的格式，字段名因此不
// 出现在输出里。
func Setup(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	l := slog.New(&lineHandler{level: lv, out: os.Stdout})
	slog.SetDefault(l)
	return l
}

// lineHandler 是 slog.Handler 的自定义实现，产出
// `[2026-09-22 16:31:47.016]	DEBUG	task/order.go:65	推送订单进行完成 1`
// 这种制表符分隔的单行格式。
type lineHandler struct {
	level slog.Level
	out   io.Writer
	// attrs 是 WithAttrs 累积下来的字段，只取它们的 Value——见 Setup 上方
	// 注释里"字段名不出现在输出里"这条。
	attrs []slog.Attr
}

func (h *lineHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *lineHandler) Handle(_ context.Context, r slog.Record) error {
	var source string
	// r.PC 由 slog.Logger 在调用 Info/Debug 等方法时无条件采集（不依赖
	// HandlerOptions.AddSource——那个开关只影响内置的 Text/JSONHandler
	// 要不要把它写进输出），自定义 Handler 可以直接用。
	if r.PC != 0 {
		frame, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
		if frame.File != "" {
			source = fmt.Sprintf("%s:%d", shortSource(frame.File), frame.Line)
		}
	}

	var msg strings.Builder
	msg.WriteString(r.Message)
	for _, a := range h.attrs {
		msg.WriteByte(' ')
		msg.WriteString(a.Value.String())
	}
	r.Attrs(func(a slog.Attr) bool {
		msg.WriteByte(' ')
		msg.WriteString(a.Value.String())
		return true
	})

	_, err := fmt.Fprintf(h.out, "[%s]\t%s\t%s\t%s\n",
		r.Time.Format("2006-01-02 15:04:05.000"), r.Level.String(), source, msg.String())
	return err
}

func (h *lineHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

// WithGroup 不支持分组——输出里本来就不带字段名，组名同样不会出现在任何
// 地方，仓库里也没有任何调用方用到 slog 的分组，没有必要为它多维护一层
// 前缀状态。
func (h *lineHandler) WithGroup(_ string) slog.Handler {
	return h
}

// shortSource 把绝对路径缩成"上级目录/文件名"，比如
// `D:/fp/internal/task/order.go` -> `task/order.go`——足够定位到文件，又不会
// 把本机的绝对路径打进日志。只有一层目录时（理论上不会在这个仓库出现）
// 原样返回文件名本身。
func shortSource(file string) string {
	file = filepath.ToSlash(file)
	parts := strings.Split(file, "/")
	if len(parts) < 2 {
		return file
	}
	return strings.Join(parts[len(parts)-2:], "/")
}
