// Package logging 初始化进程级结构化日志。
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Setup 按 level 创建 JSON 格式的 slog.Logger，并设为全局默认。
// level 无法识别时回退到 info。
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
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv})
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}
