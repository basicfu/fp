package store_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/basicfu/fp/internal/store/migrations"
)

// newScratchSchema 在测试库里开一个临时 schema，返回 search_path 指向它的连接池。
//
// 迁移的 Down 不能在共用的 public 上跑：测试库是所有测试包（以及别的工作区）共用的，
// 中途少了列会让并行的任何东西一起坏掉。迁移文件里的表名都不带 schema，换个 search_path 就整套隔离。
func newScratchSchema(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("FP_TEST_POSTGRES_URL")
	if url == "" {
		t.Fatal("未设置 FP_TEST_POSTGRES_URL，请用 ./scripts/test.sh 跑测试")
	}
	ctx := context.Background()
	schema := "mig_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("解析 FP_TEST_POSTGRES_URL 失败")
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatalf("连接测试库: %v", err)
	}
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatalf("建临时 schema: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		admin.Close()
		t.Fatalf("连接临时 schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("清理临时 schema %s: %v", schema, err)
		}
		admin.Close()
	})
	return pool
}

func TestUserAvatarMigrationUpDownRoundTrip(t *testing.T) {
	pool := newScratchSchema(t)
	ctx := context.Background()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatalf("goose.NewProvider: %v", err)
	}

	if _, err := provider.UpTo(ctx, 17); err != nil {
		t.Fatalf("升到 00017: %v", err)
	}

	// 存量数据：
	//   early  先登录 A、后登录 B        → A 是注册来源
	//   tied   A、B 两行时间完全相同      → 恰好标一行，迁移不能因并列而失败
	//   single 只登录过 B                → B
	//   never  从没登录过任何应用         → 没有来源
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	newID := func(sql string, args ...any) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	const insertApp = `INSERT INTO application (name, code, app_id, app_secret_hash) VALUES ($1, $1, $1, 'x') RETURNING id`
	appA, appB := newID(insertApp, "app-a"), newID(insertApp, "app-b")
	const insertUser = `INSERT INTO app_user (nickname) VALUES ($1) RETURNING id`
	early, tied, single, never := newID(insertUser, "early"), newID(insertUser, "tied"), newID(insertUser, "single"), newID(insertUser, "never")
	const insertExtra = `INSERT INTO user_extra (user_id, application_id, first_login_at) VALUES ($1, $2, $3::timestamptz)`
	mustExec(insertExtra, early, appB, "2026-02-01T00:00:00Z")
	mustExec(insertExtra, early, appA, "2026-01-01T00:00:00Z")
	mustExec(insertExtra, tied, appA, "2026-03-01T00:00:00Z")
	mustExec(insertExtra, tied, appB, "2026-03-01T00:00:00Z")
	mustExec(insertExtra, single, appB, "2026-04-01T00:00:00Z")

	registrations := func(user uuid.UUID) []uuid.UUID {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT application_id FROM user_extra WHERE user_id = $1 AND is_registration`, user)
		if err != nil {
			t.Fatalf("查注册来源: %v", err)
		}
		defer rows.Close()
		var out []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out = append(out, id)
		}
		return out
	}
	checkBackfill := func() {
		t.Helper()
		if got := registrations(early); len(got) != 1 || got[0] != appA {
			t.Fatalf("early 的注册来源 = %v, want 最早登录的应用 A", got)
		}
		if got := registrations(tied); len(got) != 1 {
			t.Fatalf("tied 的注册来源有 %d 个, want 恰好 1 个", len(got))
		}
		if got := registrations(single); len(got) != 1 || got[0] != appB {
			t.Fatalf("single 的注册来源 = %v, want 应用 B", got)
		}
		if got := registrations(never); len(got) != 0 {
			t.Fatalf("never 的注册来源 = %v, want 无", got)
		}
	}
	countColumn := func(table, column string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&n); err != nil {
			t.Fatalf("查列: %v", err)
		}
		return n
	}
	countTable := func(table string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = $1`, table).Scan(&n); err != nil {
			t.Fatalf("查表: %v", err)
		}
		return n
	}

	// Up
	if _, err := provider.UpTo(ctx, 18); err != nil {
		t.Fatalf("00018 Up: %v", err)
	}
	checkBackfill()
	var hash string
	if err := pool.QueryRow(ctx, `SELECT avatar_hash FROM app_user WHERE id = $1`, early).Scan(&hash); err != nil || hash != "" {
		t.Fatalf("存量用户的 avatar_hash = %q, %v；want 空串", hash, err)
	}
	mustExec(`INSERT INTO user_avatar (user_id, hash, content_type, data) VALUES ($1, 'h', 'image/png', '\x89504e47')`, early)
	mustExec(`UPDATE app_user SET avatar_hash = 'h' WHERE id = $1`, early)
	// 部分唯一索引真的在：同一用户标第二个来源必须被拒。
	if _, err := pool.Exec(ctx, `UPDATE user_extra SET is_registration = true WHERE user_id = $1`, early); err == nil {
		t.Fatal("同一用户标出两个注册来源应被唯一索引拒绝")
	}

	// Down：新东西全部消失，原有数据原样留下。
	if _, err := provider.DownTo(ctx, 17); err != nil {
		t.Fatalf("00018 Down: %v", err)
	}
	if n := countTable("user_avatar"); n != 0 {
		t.Fatal("Down 之后 user_avatar 表还在")
	}
	if n := countColumn("app_user", "avatar_hash") + countColumn("user_extra", "is_registration"); n != 0 {
		t.Fatalf("Down 之后还剩 %d 个新增列", n)
	}
	var users, extras int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM app_user), (SELECT count(*) FROM user_extra)`).Scan(&users, &extras); err != nil {
		t.Fatalf("count: %v", err)
	}
	if users != 4 || extras != 5 {
		t.Fatalf("Down 之后 app_user = %d 行、user_extra = %d 行，want 4 / 5——回滚不该动原有数据", users, extras)
	}

	// 再 Up：回滚过的库能重新升上来，回填结果一致。
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("再次 Up: %v", err)
	}
	checkBackfill()
	if n := countTable("user_avatar"); n != 1 {
		t.Fatal("再次 Up 之后没有 user_avatar 表")
	}
}
