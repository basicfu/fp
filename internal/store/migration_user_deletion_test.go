package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/basicfu/fp/internal/store/migrations"
)

func TestUserDeletionArchiveMigrationUpDownRoundTrip(t *testing.T) {
	pool := newScratchSchema(t)
	ctx := context.Background()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatalf("goose.NewProvider: %v", err)
	}
	exists := func(kind, name string) bool {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_class c JOIN pg_namespace ns ON ns.oid = c.relnamespace
			WHERE ns.nspname = current_schema() AND c.relname = $1 AND c.relkind = $2`, name, kind).Scan(&n); err != nil {
			t.Fatalf("查 %s: %v", name, err)
		}
		return n == 1
	}

	if _, err := provider.UpTo(ctx, 19); err != nil {
		t.Fatalf("00019 Up: %v", err)
	}
	if !exists("r", "user_deletion_archive") || !exists("i", "app_user_pending_delete_idx") {
		t.Fatal("Up 之后缺归档表或冷静期索引")
	}
	// 数组列的默认值是空数组而不是 NULL：清理任务按"没有手机号"写入时不必特判。
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_deletion_archive (user_id, submitted_at, deleted_at, expires_at)
		VALUES (uuidv7(), now(), now(), now() + interval '6 months')`); err != nil {
		t.Fatalf("写归档: %v", err)
	}
	var phones, unions int
	if err := pool.QueryRow(ctx,
		`SELECT cardinality(phones), cardinality(wechat_union_ids) FROM user_deletion_archive`).Scan(&phones, &unions); err != nil {
		t.Fatalf("读归档: %v", err)
	}
	if phones != 0 || unions != 0 {
		t.Fatalf("默认值 phones=%d unions=%d, want 空数组", phones, unions)
	}

	if _, err := provider.DownTo(ctx, 18); err != nil {
		t.Fatalf("00019 Down: %v", err)
	}
	if exists("r", "user_deletion_archive") || exists("i", "app_user_pending_delete_idx") {
		t.Fatal("Down 之后归档表或索引还在")
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("再次 Up: %v", err)
	}
	if !exists("r", "user_deletion_archive") {
		t.Fatal("再次 Up 之后没有归档表")
	}
}
