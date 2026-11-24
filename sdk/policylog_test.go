package fpsdk

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

type recHandler struct {
	mu     sync.Mutex
	levels []slog.Level
}

func (h *recHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.levels = append(h.levels, r.Level)
	h.mu.Unlock()
	return nil
}
func (h *recHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recHandler) WithGroup(string) slog.Handler      { return h }

// 策略周期刷新、版本没变时只打 Debug; 版本变了才 Info("策略已更新"才名副其实)。
func TestLogPolicyRefreshedOnlyInfoOnVersionChange(t *testing.T) {
	h := &recHandler{}
	c := &Client{opts: Options{Logger: slog.New(h)}}
	c.logPolicyRefreshed(7, 3)
	c.logPolicyRefreshed(7, 3)
	c.logPolicyRefreshed(7, 3)
	c.logPolicyRefreshed(8, 3)
	want := []slog.Level{slog.LevelInfo, slog.LevelDebug, slog.LevelDebug, slog.LevelInfo}
	if len(h.levels) != len(want) {
		t.Fatalf("levels = %v", h.levels)
	}
	for i := range want {
		if h.levels[i] != want[i] {
			t.Fatalf("第 %d 条 = %v, 想要 %v", i, h.levels[i], want[i])
		}
	}
}
