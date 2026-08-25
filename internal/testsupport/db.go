// Package testsupport 提供集成测试所需的真实 PostgreSQL / Redis 连接。
package testsupport

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/store"
)

const missingPGHint = `
未设置 FP_TEST_POSTGRES_URL。fp 的测试需要真实的 PostgreSQL 18（uuidv7() 是 18 引入的）。

用仓库根目录的脚本跑测试，它会从 .env.local 载入连接串：

  ./scripts/test.sh

首次使用需要先从 .env.example 复制出 .env.local 并填入凭据。
`

var (
	dbOnce sync.Once
	dbPool *pgxpool.Pool
	dbErr  error
)

// NewTestDB 返回一个已完成迁移的连接池，并清空所有业务表。
// 连接池在整个测试二进制内复用；每次调用只做数据清理。
func NewTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("FP_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal(missingPGHint)
	}

	dbOnce.Do(func() {
		ctx := context.Background()
		dbPool, dbErr = store.OpenPostgres(ctx, url)
		if dbErr != nil {
			return
		}
		dbErr = store.Migrate(ctx, dbPool)
	})
	if dbErr != nil {
		t.Fatalf("testsupport: 准备测试库失败: %v", dbErr)
	}

	truncateAll(t, dbPool)
	return dbPool
}

// truncateAll 清空除 goose 版本表与 schema_note 之外的所有表。
func truncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	rows, err := pool.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public'
		  AND tablename NOT IN ('goose_db_version', 'schema_note')`)
	if err != nil {
		t.Fatalf("testsupport: 列出表失败: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("testsupport: 扫描表名失败: %v", err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("testsupport: 遍历表名失败: %v", err)
	}
	if len(tables) == 0 {
		return
	}

	stmt := "TRUNCATE TABLE "
	for i, name := range tables {
		if i > 0 {
			stmt += ", "
		}
		stmt += `"` + name + `"`
	}
	stmt += " RESTART IDENTITY CASCADE"

	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("testsupport: 清空表失败: %v", err)
	}
}
