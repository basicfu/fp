package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/store"
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

// pgx 的解析错误会回显连接串，却只抹掉 URL 用户信息段里的密码与紧贴等号的 password=：没编码的 # %
// 会让密码前缀落进内部错误（invalid port、invalid URL escape），写在查询参数里或 password = 带空格的
// 密码原样回显，整串都不是连接串时整串回显。这条错误会进服务端日志、CLI 的 stderr 与 fp-dbclean 的
// 输出，不能把密码带出去。解析在联网之前就失败，所以这个用例不碰任何库。
func TestOpenPostgresParseErrorDoesNotLeakPassword(t *testing.T) {
	cases := []struct {
		name, url string
		secrets   []string // 错误文本里不许出现的密码片段
	}{
		{"密码里有坏的百分号转义", "postgres://u:Pw%zzZq9@127.0.0.1:5432/db", []string{"Pw", "%zz", "Zq9"}},
		{"密码里有没编码的 #", "postgres://u:Pw#Zq9@127.0.0.1:5432/db", []string{"Pw", "Zq9"}},
		{"密码写在查询参数里，别的参数写错", "postgres://u@127.0.0.1:5432/db?password=Zq9secret&sslmode=bogus", []string{"Zq9secret"}},
		{"keyword/value 写法里 password 带空格", "host=127.0.0.1 user=u password = Zq9secret sslmode=bogus", []string{"Zq9secret"}},
		{"keyword/value 写法里引号没闭合", "host=127.0.0.1 user=u password='Pw Zq9 dbname=db", []string{"Pw", "Zq9"}},
		{"整串只有密码", "Zq9secret", []string{"Zq9secret"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := store.OpenPostgres(context.Background(), c.url)
			if err == nil {
				t.Fatal("坏的连接串应当报错")
			}
			for _, secret := range c.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("错误里带出了密码片段 %q: %v", secret, err)
				}
			}
			if !strings.Contains(err.Error(), "postgres://") || !strings.Contains(err.Error(), "百分号编码") {
				t.Errorf("错误应给出整体格式 postgres://... 并提示密码里的特殊字符要百分号编码: %v", err)
			}
		})
	}
}
