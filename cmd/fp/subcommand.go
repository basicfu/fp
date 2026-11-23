package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/basicfu/fp/internal/config"
	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/objstore"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
)

const usage = `用法：
  fp                  启动服务
  fp reset-password   重置管理员账号：用户名恢复为 admin，密码随机生成并打印，作废全部管理端会话
  fp avatar-migrate [-batch 100]
                      把存在数据库里的头像上传到对象存储（系统配置里的 oss 段）并回填地址，可重复执行
  fp session-bind-import -file <path>
                      给已有会话补设备公钥与 client_ref（每行 {session_id, device_pub, client_ref[, user_id]}），可重复执行
  fp wechat-nickname-import -file <path>
                      把微信昵称补到用户的微信身份上（每行 {user_id, nickname}），只补空的、不覆盖已有，可重复执行
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
	case "avatar-migrate":
		fs := flag.NewFlagSet("avatar-migrate", flag.ContinueOnError)
		fs.SetOutput(stderr)
		batch := fs.Int("batch", 100, "每批处理的头像数")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || *batch < 1 {
			fmt.Fprintf(stderr, "avatar-migrate 参数不合法\n\n%s", usage)
			return 2
		}
		if err := runAvatarMigrate(context.Background(), *batch, stdout); err != nil {
			fmt.Fprintf(stderr, "迁移失败：%v\n本命令可安全重跑，已迁移的不会重复上传。\n", err)
			return 1
		}
		return 0
	case "session-bind-import", "wechat-nickname-import":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		fs.SetOutput(stderr)
		file := fs.String("file", "", "导入文件，每行一个 JSON")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || *file == "" {
			fmt.Fprintf(stderr, "%s 参数不合法\n\n%s", args[0], usage)
			return 2
		}
		if err := runImport(context.Background(), args[0], *file, stdout); err != nil {
			fmt.Fprintf(stderr, "导入失败：%v\n本命令可安全重跑，已导入的不会重复修改。\n", err)
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

// runAvatarMigrate 只依赖 Postgres 与系统配置里的 oss 段，不连 Redis。
func runAvatarMigrate(ctx context.Context, batch int, out io.Writer) error {
	pgURL, err := config.RequireEnv("FP_POSTGRES_URL")
	if err != nil {
		return err
	}
	connCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	pool, err := store.OpenPostgres(connCtx, pgURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	sys, err := service.NewSystemConfigService(pool).Current(ctx)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(sys.Value, config.EnvOr("FP_ENV", "DEV"))
	if err != nil {
		return err
	}
	st, err := objstore.New(cfg.OSS)
	if err != nil {
		return err
	}
	if st == nil {
		return errors.New("系统配置里没有配置 oss（bucket 为空）")
	}
	return avatarMigrate(ctx, service.NewUserService(pool, service.WithObjectStore(st)), batch, out)
}

func avatarMigrate(ctx context.Context, users *service.UserService, batch int, out io.Writer) error {
	n, err := users.MigrateAvatars(ctx, batch)
	// 失败时也先报已完成的数量：重跑只会处理剩下的。
	fmt.Fprintf(out, "已迁移 %d 张头像\n", n)
	return err
}

// runImport 只依赖 Postgres 与 Redis，不读系统配置。会话导入必须在应用开启设备绑定之前跑完：
// 开启之后没记设备的会话一律按登录过期拒绝（见 DeviceGuard.Check），没补上的用户要重新登录。
func runImport(ctx context.Context, kind, path string, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	pgURL, err := config.RequireEnv("FP_POSTGRES_URL")
	if err != nil {
		return err
	}
	connCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	pool, err := store.OpenPostgres(connCtx, pgURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	var st service.ImportStats
	if kind == "wechat-nickname-import" {
		st, err = service.NewUserService(pool).ImportWechatNicknames(ctx, f)
	} else {
		redisURL, rerr := config.RequireEnv("FP_REDIS_URL")
		if rerr != nil {
			return rerr
		}
		rdb, rerr := store.OpenRedis(connCtx, redisURL)
		if rerr != nil {
			return rerr
		}
		defer rdb.Close()
		// 要应用的会话策略:导入顺带把会话按真实到期登记进到期集合。
		sessions := service.NewSessionService(store.NewSessionStore(rdb), store.NewRevokePublisher(rdb), store.NewEpochStore(rdb),
			service.WithSessionApps(service.NewApplicationService(pool, connector.NewRegistry())))
		st, err = sessions.ImportDeviceBindings(ctx, f)
	}
	printImportStats(out, st)
	return err
}

func printImportStats(out io.Writer, st service.ImportStats) {
	fmt.Fprintf(out, "更新 %d，不变 %d，跳过 %d，坏行 %d\n", st.Updated, st.Unchanged, st.Skipped, len(st.Bad))
	for _, b := range st.Bad {
		fmt.Fprintf(out, "  第 %d 行：%s\n", b.Line, b.Err)
	}
}
