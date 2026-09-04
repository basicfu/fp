// Command im-demo 是 fp-im 的最小接入示例：一个回显服务。
//
// 它演示业务方接入 fp-im 需要写的全部代码：fpim.NewServer、OnMessage
// 原样推回去、OnEvent 打日志。如果这个文件变长了，说明该把重复的部分
// 挪进 sdk/im，而不是让每个业务方重复写胶水代码——与 examples/demo 的
// 定位是同一个道理。
//
// 用法与手工验收步骤见同目录下的 README.md。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	fpim "github.com/basicfu/fp/sdk/im"
)

func main() {
	srv, err := fpim.NewServer(fpim.ServerConfig{
		Addr:      envOr("FP_IM_ADDR", "localhost:9091"),
		AppID:     os.Getenv("FP_APP_ID"),
		AppSecret: os.Getenv("FP_APP_SECRET"),
		// 本地开发没有 TLS。生产环境绝不要开——appSecret 会随每个 RPC
		// 以明文发送，见 sdk/im/server.go ServerConfig.Insecure 的注释。
		Insecure: true,
	})
	if err != nil {
		slog.Error("连接 fp-im 失败", "err", err)
		os.Exit(1)
	}
	defer srv.Close()

	// 连接事件：上线/下线都打日志，方便手工验收时肉眼确认顶号、踢人、
	// 断线各自触发了预期的事件。
	srv.OnEvent(func(_ context.Context, ev fpim.Event) {
		kind := "Connected"
		if ev.Kind == fpim.EventDisconnected {
			kind = "Disconnected"
		}
		slog.Info(kind, "subject", ev.Subject, "conn", ev.ConnID, "os", ev.OS, "mobile", ev.Mobile, "reason", ev.Reason)
	})

	// 回显：收到什么推回什么。
	//
	// 必须在另一个 goroutine 里调 Push，不能直接在这个回调里同步等它
	// 返回：OnMessage 是在 Server 内部唯一一条读循环里同步执行的（见
	// sdk/im/server.go OnMessage 的注释），而 Push 要等的 Result 回执
	// 恰好也是从这同一条流、由同一条读循环读回来的——如果在回调里同步
	// 等 Push，读循环没法转回去读它自己在等的那个 Result，会把自己
	// 死锁到 RequestTimeout（默认 5 秒）超时：本例最初就是照这个"看起来
	// 最直白"的同步写法验收的，实测每一次回显都稳定卡满 5 秒后报
	// ErrUnavailable，而消息其实已经送达——这正是 OnMessage 注释里"慢
	// 操作应由业务方自己在回调里另起 goroutine"这条契约要防的情形，不是
	// 这个例子专属的坑。返回值恒为 nil：fp-im 不等 OnMessage 的返回值，
	// 也不会因为它非 nil 而重试，回调本身早已经把处理这件事交给了下面
	// 这个新协程。
	srv.OnMessage(func(ctx context.Context, in fpim.Inbound) error {
		go func() {
			res, err := srv.Push(ctx, in.Subject, in.Payload)
			slog.Info("回显", "subject", in.Subject, "status", res.Status, "err", err)
		}()
		return nil
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
}

func envOr(k, v string) string {
	if x := os.Getenv(k); x != "" {
		return x
	}
	return v
}
