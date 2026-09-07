// Command fp-dbclean 清空 fp 的数据库。破坏性操作，默认需要人工确认。
//
// 为什么是一个 Go 命令而不是一段 psql 脚本：整个仓库不依赖 psql 客户端
// （开发机上未必装），而 fp 本身就带着 pgx 和迁移。用 go run 跑还能直接
// 复用 store.OpenPostgres/OpenRedis 的连接串解析，不必在 shell 里手工
// 拆 URL——那正是密码泄进日志的经典途径。
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/store"
)

// truncateSQL 清空全部业务表，保留表结构。
//
// goose_db_version 必须排除：把它清了，fp 下次启动会从头重跑所有迁移，
// 撞上依然存在的表直接失败。schema_note 排除是与 testsupport.truncateAll
// 保持一致。
const truncateSQL = `
DO $$
DECLARE stmt text;
BEGIN
    SELECT 'TRUNCATE TABLE ' || string_agg(format('%I.%I', schemaname, tablename), ', ')
           || ' RESTART IDENTITY CASCADE'
      INTO stmt
      FROM pg_tables
     WHERE schemaname = 'public'
       AND tablename NOT IN ('goose_db_version', 'schema_note');
    IF stmt IS NOT NULL THEN EXECUTE stmt; END IF;
END $$;
`

// dropAllSQL 删掉 public 下的全部表（含 goose_db_version），fp 下次启动
// 会完整重跑迁移。
//
// 刻意不用 DROP SCHEMA public CASCADE：重建 schema 后属主变成执行清理的
// 那个角色，若 fp 用另一个账号连库就丢了 CREATE 权限。而 fp 的迁移只建表
// 和索引——没有自定义类型、函数、视图、序列、触发器，也没有 serial 列——
// 所以删表已经删干净，绕开 schema 也就绕开了整个属主问题。
const dropAllSQL = `
DO $$
DECLARE stmt text;
BEGIN
    SELECT 'DROP TABLE IF EXISTS ' || string_agg(format('%I.%I', schemaname, tablename), ', ')
           || ' CASCADE'
      INTO stmt
      FROM pg_tables
     WHERE schemaname = 'public';
    IF stmt IS NOT NULL THEN EXECUTE stmt; END IF;
END $$;
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fp-dbclean:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		assumeYes = flag.Bool("y", false, "跳过确认（供 CI 使用）")
		skipRedis = flag.Bool("no-redis", false, "只清 Postgres，不动 Redis")
	)
	flag.Usage = usage
	flag.Parse()

	mode := flag.Arg(0)
	if mode != "truncate" && mode != "reset" {
		usage()
		return errors.New("请指定模式：truncate 或 reset")
	}

	pgURL := os.Getenv("FP_POSTGRES_URL")
	if pgURL == "" {
		return errors.New("未设置 FP_POSTGRES_URL（用 ./scripts/db-clean.sh 跑，它会从 .env.local 载入）")
	}
	pgCfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		return fmt.Errorf("解析 FP_POSTGRES_URL: %w", err)
	}
	conn := pgCfg.ConnConfig
	dbName := conn.Database

	redisURL := os.Getenv("FP_REDIS_URL")
	var redisOpt *redis.Options
	if !*skipRedis {
		if redisURL == "" {
			// 不静默跳过：只清 Postgres 会留下指向已删数据的会话、撤销
			// epoch 与配置推送信号，是个很难查的中间态。
			return errors.New("未设置 FP_REDIS_URL。确实只想清 Postgres 请显式加 -no-redis")
		}
		if redisOpt, err = redis.ParseURL(redisURL); err != nil {
			return fmt.Errorf("解析 FP_REDIS_URL: %w", err)
		}
	}

	// 打印目标时只用解析后的字段，绝不回显整条 URL——里面有密码。
	fmt.Println("即将清理：")
	fmt.Printf("  Postgres  %s@%s:%d/%s\n", conn.User, conn.Host, conn.Port, dbName)
	if redisOpt != nil {
		fmt.Printf("  Redis     %s DB %d（FLUSHDB）\n", redisOpt.Addr, redisOpt.DB)
	} else {
		fmt.Println("  Redis     跳过（-no-redis）")
	}
	if mode == "truncate" {
		fmt.Println("  模式      truncate —— 清空全部业务表，保留表结构与迁移版本")
	} else {
		fmt.Println("  模式      reset —— 删除 public 下全部表，fp 下次启动重跑迁移")
	}

	if !*assumeYes {
		ok, err := confirm(dbName)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("已取消，未做任何改动。")
			return nil
		}
	}

	ctx := context.Background()
	pool, err := store.OpenPostgres(ctx, pgURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	stmt := truncateSQL
	if mode == "reset" {
		stmt = dropAllSQL
	}
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("清理 Postgres: %w", err)
	}
	var tables int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM pg_tables WHERE schemaname = 'public'`).Scan(&tables); err != nil {
		return fmt.Errorf("统计剩余表: %w", err)
	}
	fmt.Printf("Postgres 已清理，public 下现有 %d 张表。\n", tables)

	if redisOpt != nil {
		rdb, err := store.OpenRedis(ctx, redisURL)
		if err != nil {
			return err
		}
		defer rdb.Close()
		// FLUSHDB 而不是 FLUSHALL：这个 Redis 实例的其他 DB 可能另有用途。
		if err := rdb.FlushDB(ctx).Err(); err != nil {
			return fmt.Errorf("清理 Redis: %w", err)
		}
		fmt.Printf("Redis DB %d 已清空。\n", redisOpt.DB)
	}

	if mode == "reset" {
		fmt.Println("下次启动 fp 会重跑全部迁移重建表结构。")
	}
	fmt.Println("引导管理员已被清除，重启 fp 时会按 FP_BOOTSTRAP_ADMIN_USER/PASSWORD 重新创建。")
	return nil
}

// confirm 要求把库名原样敲一遍。用 y/N 太容易顺手按下去，而这个操作
// 不可撤销；敲库名同时也逼调用者确认自己清的是哪个库。
func confirm(dbName string) (bool, error) {
	fmt.Printf("\n此操作不可撤销。请输入库名 %q 确认：", dbName)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		// 没有 tty（管道、CI）时读到 EOF：当作拒绝，绝不当作同意。
		return false, nil
	}
	return strings.TrimSpace(line) == dbName, nil
}

func usage() {
	fmt.Fprint(os.Stderr, `用法: fp-dbclean [-y] [-no-redis] <truncate|reset>

  truncate  清空全部业务表，保留表结构与 goose 迁移版本。
            适合"把环境的数据倒干净重来一遍"。
  reset     删除 public 下全部表（含 goose_db_version）。
            fp 下次启动会从 00001 完整重跑迁移。适合库结构被改坏了。

连接串取自 FP_POSTGRES_URL / FP_REDIS_URL，与 fp 本身一致。
一般通过 ./scripts/db-clean.sh 调用，它会从 .env.local 载入这两个变量。

`)
	flag.PrintDefaults()
}
