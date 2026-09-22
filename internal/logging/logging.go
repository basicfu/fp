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
	"sync"
	"time"
)

// fileLogDir 是非 dev 环境下额外写日志文件的目录。声明成变量（不是
// const）只是为了让测试能把它换成 t.TempDir()，不用真的写 /logs——
// 生产环境下这个值不会被改。
var fileLogDir = "/logs"

// fileLogRetention 是日志文件保留的天数，超过这个天数的按日期文件名删掉。
const fileLogRetention = 30

// lastFileWriter 只供测试用：Setup 每次成功开出文件日志时把 writer 记在
// 这里，测试跑完调 closeLastFileWriter 把底层文件句柄关掉。Windows 上
// t.TempDir() 的自动清理会在还有进程持有句柄时报错，生产环境这个包级
// 变量不影响任何行为——没人在进程存活期间关它，文件就一直开到进程退出。
var lastFileWriter *dailyFileWriter

// closeLastFileWriter 只供测试调用。
func closeLastFileWriter() {
	if lastFileWriter != nil {
		_ = lastFileWriter.Close()
		lastFileWriter = nil
	}
}

// Setup 按 level 创建 lineHandler 并设为全局默认。level 无法识别时回退到
// info。env 不是 "dev"（大小写不敏感）时，日志会**同时**写一份到
// fileLogDir 下按天滚动的文件（`2026-06-11.log` 这种文件名，超过
// fileLogRetention 天的旧文件自动删除）——dev 环境只打 stdout，本机开发
// 不需要在磁盘上留痕。
//
// 输出格式是 `[time]\tLEVEL\t file:line\tmsg`（制表符分隔，四段），供日志
// 采集器按固定分隔符切分，不是 JSON——msg 这一段把消息文本与所有附加字段
// 的值用空格拼接在一起，不带 key（"推送订单进行完成 1" 而不是
// "推送订单进行完成 orderID=1"），这是采集端明确要的格式，字段名因此不
// 出现在输出里。
func Setup(level string, env string) *slog.Logger {
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

	out := io.Writer(os.Stdout)
	if !strings.EqualFold(env, "dev") {
		fw, err := newDailyFileWriter(fileLogDir, fileLogRetention)
		if err != nil {
			// 写不了文件不该拦住进程启动——服务能跑比日志全乎更要紧。
			// 用默认 logger（这个函数还没跑完，slog.SetDefault 还没调）
			// 打一条 WARN 到 stdout，不静默。
			slog.Warn("logging: 打不开日志目录，本次启动只打 stdout，不写文件",
				"dir", fileLogDir, "err", err)
		} else {
			out = io.MultiWriter(os.Stdout, fw)
			lastFileWriter = fw
		}
	}

	l := slog.New(&lineHandler{level: lv, out: out, mu: &sync.Mutex{}})
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

	// mu 序列化对 out 的写入。slog.Handler 的契约要求实现自己保证并发
	// 安全——没有这把锁，两个 goroutine 同时打日志时，Fprintf 拼出来的
	// 两整行可能在 out（尤其是 out 是 MultiWriter、内部依次调用多个
	// Writer 时）里交错穿插，日志文件里会看到断成两截又拼错位置的行。
	//
	// 用指针不是值：WithAttrs 会 `next := *h` 复制出一份新 lineHandler
	// （只是 attrs 不同，out 还是同一个），复制出来的那份必须和原来的
	// 共用同一把锁——如果 mu 是值字段，复制时会连锁一起复制出一把新的、
	// 互不相干的锁，两份 handler 各锁各的，等于没锁；go vet 也会直接把
	// "复制包含 sync.Mutex 的结构体"这个坑标成编译期错误。
	mu *sync.Mutex
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

	line := fmt.Sprintf("[%s]\t%s\t%s\t%s\n",
		r.Time.Format("2006-01-02 15:04:05.000"), r.Level.String(), source, msg.String())

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, line)
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

// dailyFileWriter 是按日期滚动的日志文件：当前这天的日志全部写进
// `<dir>/2026-06-11.log`，日期一变就切到新文件，旧文件原样留着直到超过
// 保留期被删。自己管一把锁而不是指望上层（lineHandler）已经序列化好了
// 调用——这样单独测试/单独使用这个类型时也不会因为并发写坏 w.file。
type dailyFileWriter struct {
	mu        sync.Mutex
	dir       string
	retention time.Duration
	date      string
	file      *os.File
}

// newDailyFileWriter 建目录（不存在就建）、打开今天这份文件、顺手清一遍
// 过期的旧文件。
func newDailyFileWriter(dir string, retentionDays int) (*dailyFileWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("logging: 创建日志目录 %s: %w", dir, err)
	}
	w := &dailyFileWriter{dir: dir, retention: time.Duration(retentionDays) * 24 * time.Hour}
	if err := w.rotate(time.Now()); err != nil {
		return nil, err
	}
	return w, nil
}

// Close 关掉当前打开的文件。进程正常运行期间没人调用它——文件跟着进程
// 一直开到退出；只有测试需要在 t.TempDir() 清理之前主动关掉，Windows 上
// 还占着句柄的文件删不掉。
func (w *dailyFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *dailyFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now()
	if now.Format("2006-01-02") != w.date {
		if err := w.rotate(now); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

// rotate 切到 now 对应日期的文件，并顺带清一遍过期文件。调用方必须已经
// 持有 w.mu。
func (w *dailyFileWriter) rotate(now time.Time) error {
	date := now.Format("2006-01-02")
	f, err := os.OpenFile(filepath.Join(w.dir, date+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("logging: 打开日志文件 %s.log: %w", date, err)
	}
	if w.file != nil {
		_ = w.file.Close()
	}
	w.file = f
	w.date = date
	w.prune(now)
	return nil
}

// prune 删掉 dir 下文件名形如 2026-06-11.log、日期早于 now-retention 的
// 文件。删不掉（权限问题之类）就跳过那一个，不影响其余文件、也不影响
// 正常写日志——清理旧文件从不是关键路径。
func (w *dailyFileWriter) prune(now time.Time) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-w.retention)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		date, ok := strings.CutSuffix(e.Name(), ".log")
		if !ok {
			continue
		}
		t, err := time.Parse("2006-01-02", date)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			_ = os.Remove(filepath.Join(w.dir, e.Name()))
		}
	}
}
