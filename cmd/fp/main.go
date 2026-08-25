// Command fp 是 Foundation Platform 的服务端进程。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/basicfu/fp/internal/config"
	"github.com/basicfu/fp/internal/logging"
	"github.com/basicfu/fp/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fp 启动失败", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := store.OpenPostgres(ctx, cfg.PostgresURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	log.Info("数据库迁移完成")

	rdb, err := store.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	log.Info("fp 启动", "env", cfg.Env, "http", cfg.HTTPAddr, "grpc", cfg.GRPCAddr)

	<-ctx.Done()
	log.Info("fp 收到退出信号，正在关闭")
	return nil
}
