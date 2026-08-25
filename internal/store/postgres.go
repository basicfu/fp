// Package store 提供 fp 的持久化访问：PostgreSQL 与 Redis。
package store

import (
	"context"
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
		return nil, fmt.Errorf("store: 解析 postgres url: %w", err)
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
