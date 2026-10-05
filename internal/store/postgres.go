// Package store 提供 fp 的持久化访问：PostgreSQL 与 Redis。
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/basicfu/fp/internal/store/migrations"
)

// OpenPostgres 建立连接池并验证连通性。
func OpenPostgres(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// 不带原错误、不回显输入：pgx 只抹掉用户信息段里的密码——没编码的 # % 会让密码前缀落进
		// 内部错误，写在查询参数里的 password= 则原样回显——而这条错误会进服务端日志与 CLI 的
		// stderr（同 OpenRedis）。只擦解析这一步：拨号阶段的错误只含用户名、库名与地址。
		return nil, errors.New("store: 解析 postgres url: 格式不正确（URL 形如 postgres://用户:密码@主机:端口/库名；密码里的特殊字符要百分号编码）")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: 建立连接池: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping postgres: %w", err)
	}
	return pool, nil
}

// Migrate 把数据库升级到嵌入迁移的最新版本。可重复调用。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("store: goose dialect: %w", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("store: 执行迁移: %w", err)
	}
	return nil
}
