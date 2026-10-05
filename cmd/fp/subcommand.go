package main

import (
	"context"
	"fmt"
	"io"

	"github.com/basicfu/fp/internal/config"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
)

const usage = `用法：
  fp                  启动服务
  fp reset-password   重置管理员账号：用户名恢复为 admin，密码随机生成并打印，作废全部管理端会话
`

// runSubcommand 返回进程退出码。多余参数一律报错而不是静默忽略：
// 敲错子命令却把服务起起来，比报错更糟。
func runSubcommand(args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case "reset-password":
		if err := runResetPassword(context.Background(), stdout); err != nil {
			fmt.Fprintf(stderr, "重置失败：%v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "未知参数 %q\n\n%s", args[0], usage)
		return 2
	}
}

// runResetPassword 只依赖 Postgres 与 Redis（与服务进程同一组环境变量），不读系统配置：
// 系统配置 YAML 写坏了也不该妨碍找回账号。先 Migrate，保证新版本二进制对旧库也能跑。
func runResetPassword(ctx context.Context, out io.Writer) error {
	pgURL, err := config.RequireEnv("FP_POSTGRES_URL")
	if err != nil {
		return err
	}
	redisURL, err := config.RequireEnv("FP_REDIS_URL")
	if err != nil {
		return err
	}
	pool, err := store.OpenPostgres(ctx, pgURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	rdb, err := store.OpenRedis(ctx, redisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()
	return resetPassword(ctx, service.NewAdminService(pool, rdb), out)
}

func resetPassword(ctx context.Context, svc *service.AdminService, out io.Writer) error {
	username, password, err := svc.ResetPassword(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "管理员账号已重置，所有管理端会话已作废：\n  用户名：%s\n  密码：%s\n", username, password)
	return err
}
