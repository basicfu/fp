package store_test

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/testsupport"
)

func TestMigrateCreatesGooseVersionTable(t *testing.T) {
	pool := testsupport.NewTestDB(t)

	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = 'goose_db_version'`).Scan(&n)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("goose_db_version 表数量 = %d, want 1", n)
	}
}

func TestUUIDv7Available(t *testing.T) {
	pool := testsupport.NewTestDB(t)

	var id string
	if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		t.Fatalf("uuidv7() 不可用，确认 PostgreSQL 版本 >= 18: %v", err)
	}
	if len(id) != 36 {
		t.Fatalf("uuidv7() = %q, 长度 want 36", id)
	}
}
