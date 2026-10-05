package main

import (
	"context"
	"fmt"
	"io"
	"time"

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
		// --help / --dry-run 这类探路参数若被静默忽略，就会真的重置账号、作废全部会话。
		if len(args) > 1 {
			fmt.Fprintf(stderr, "reset-password 不接受参数 %q\n\n%s", args[1], usage)
			return 2
		}
		if err := runResetPassword(context.Background(), stdout); err != nil {
			fmt.Fprintf(stderr, "重置失败：%v\n本命令可安全重跑；若失败发生在写库之后，旧密码已失效，只有重跑才能拿到新密码。\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "未知参数 %q\n\n%s", args[0], usage)
		return 2
	}
}

// 期限做成变量只为让测试能缩到毫秒级，生产值不变。没有期限的话，对端黑洞时要等操作系统的
// TCP 超时（约两分钟），而 docker exec 不带 -t 时没法 Ctrl-C。
var (
	// connectTimeout 管连 Postgres 与连 Redis，两者共用。
	connectTimeout = 15 * time.Second
	// resetTimeout 管重置本身。超时若落在提交之后、INCR 之前，runSubcommand 的"可安全重跑"
	// 提示已经覆盖。
	resetTimeout = 30 * time.Second
)

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
	// 两个连接都先建好再 Migrate：Redis 不通时，不该先在库上把迁移跑完。
	connCtx, cancelConn := context.WithTimeout(ctx, connectTimeout)
	defer cancelConn()
	pool, err := store.OpenPostgres(connCtx, pgURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	rdb, err := store.OpenRedis(connCtx, redisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	// Migrate 不设期限：旧库上的迁移可能很久，中途取消会让每次重跑都卡在同一处。
	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	return resetPassword(ctx, service.NewAdminService(pool, rdb), out)
}

// passwordResetter 是 resetPassword 对服务层的全部依赖，拆出来让输出格式与期限不连库也能测。
type passwordResetter interface {
	ResetPassword(ctx context.Context) (username, password string, disabled int, err error)
}

func resetPassword(ctx context.Context, svc passwordResetter, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, resetTimeout)
	defer cancel()
	username, password, disabled, err := svc.ResetPassword(ctx)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "管理员账号已重置，所有管理端会话已作废：\n  用户名：%s\n  密码：%s\n", username, password); err != nil {
		return err
	}
	// 放在凭据块之后，不改变用户名、密码两行原有的格式与位置。
	if disabled > 0 {
		_, err = fmt.Fprintf(out, "另有 %d 个旧版本遗留的管理员账号已停用。\n", disabled)
	}
	return err
}
