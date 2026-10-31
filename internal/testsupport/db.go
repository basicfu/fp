// Package testsupport 提供集成测试所需的真实 PostgreSQL / Redis 连接。
package testsupport

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/store"
)

// tempDBName 是测试用的库。测试每次都清全表，所以只借 FP_POSTGRES_URL 的地址和
// 账号，库名一律换成它，绝不碰变量指向的运行库。
const tempDBName = "fp_tmp"

const missingPGHint = `
未设置 FP_POSTGRES_URL。fp 的测试需要真实的 PostgreSQL 18（uuidv7() 是 18 引入的）。

把它设到系统环境变量里再跑测试：

  ./scripts/test.sh

测试只取它的地址和账号，库名固定用 fp_tmp（不存在会自动创建），不碰它指向的运行库。
`

var (
	dbOnce sync.Once
	dbPool *pgxpool.Pool
	dbErr  error

	tempDBOnce sync.Once
	tempDBErr  error
)

// PostgresURL 返回测试库的连接串：FP_POSTGRES_URL 的地址和账号 + fp_tmp 库。
func PostgresURL(t *testing.T) string {
	t.Helper()

	raw := os.Getenv("FP_POSTGRES_URL")
	if raw == "" {
		t.Fatal(missingPGHint)
	}
	u, err := url.Parse(raw)
	if err != nil {
		// 不带原错误：url.Parse 的错误会回显整条连接串，里面有密码。
		t.Fatal("testsupport: FP_POSTGRES_URL 格式不正确")
	}
	if strings.Trim(u.Path, "/") == tempDBName {
		t.Fatalf("testsupport: FP_POSTGRES_URL 指向的就是 %s，测试会清空运行库，请换一个运行库", tempDBName)
	}
	tempDBOnce.Do(func() { tempDBErr = ensureTempDB(context.Background(), raw) })
	if tempDBErr != nil {
		t.Fatalf("testsupport: 准备 %s 失败: %v", tempDBName, tempDBErr)
	}
	u.Path = "/" + tempDBName
	return u.String()
}

// ensureTempDB 经运行库的连接建出 fp_tmp，这样换一台实例跑测试不用手工建库。
func ensureTempDB(ctx context.Context, adminURL string) error {
	pool, err := store.OpenPostgres(ctx, adminURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, tempDBName).Scan(&exists); err != nil {
		return fmt.Errorf("查询库是否存在: %w", err)
	}
	if exists {
		return nil
	}
	if _, err := pool.Exec(ctx, `CREATE DATABASE `+tempDBName); err != nil {
		return fmt.Errorf("建库: %w", err)
	}
	return nil
}

// NewTestDB 返回一个已完成迁移的连接池，并清空所有业务表。
// 连接池在整个测试二进制内复用；每次调用只做数据清理。
func NewTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	testURL := PostgresURL(t)

	dbOnce.Do(func() {
		ctx := context.Background()
		dbPool, dbErr = store.OpenPostgres(ctx, testURL)
		if dbErr != nil {
			return
		}
		dbErr = store.Migrate(ctx, dbPool)
	})
	if dbErr != nil {
		t.Fatalf("testsupport: 准备测试库失败: %v", dbErr)
	}

	truncateAll(t, dbPool)
	// 与 fp 启动后的状态保持一致：TRUNCATE 把内置角色也清掉了。
	if err := store.EnsureBuiltinRoles(context.Background(), dbPool); err != nil {
		t.Fatalf("testsupport: %v", err)
	}
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
