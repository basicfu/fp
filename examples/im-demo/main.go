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
		// 与姊妹示例 examples/demo 用同一个开关（FP_INSECURE=1），而不是
		// 硬编码 true：这份文件的头注释自称"照抄就是接入网关要写的全部
		// 代码"，硬编码会让照抄的人把明文传密钥的写法一起带进生产——见
		// sdk/im/server.go ServerConfig.Insecure 的注释，appSecret 会随
		// 每个 RPC 以明文发送，生产环境绝不能开。
		Insecure: os.Getenv("FP_INSECURE") == "1",
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
	// 直接在回调里同步调用 Push、不开 goroutine——这就是接入 fp-im 要写
	// 的最直白代码。早期实现下这样写会死锁（OnMessage 曾经是在 Server
	// 内部唯一一条读循环里同步执行的，而 Push 要等的 Result 回执也只能
	// 由那同一条读循环读回来，回调里同步等 Push 就会把读循环自己堵死，
	// 卡满 RequestTimeout 才报 ErrUnavailable，消息其实已经送达）；现在
	// OnMessage/OnEvent 由 Server 内部一个独立的消费协程按入队顺序调用，
	// 不会再和读等 Result 抢同一条路径，回调里可以放心同步调用 Push。
	// 返回值只会被 SDK 记一条警告日志：fp-im 网关本身不知道这个回调
	// 存不存在，更不会等它的返回值、也不会因为它非 nil 而重试。
	srv.OnMessage(func(ctx context.Context, in fpim.Inbound) error {
		res, err := srv.Push(ctx, in.Subject, in.Payload)
		slog.Info("回显", "subject", in.Subject, "status", res.Status, "err", err)
		return err
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
