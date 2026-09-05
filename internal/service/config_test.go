package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

// newConfigFixture 建一个应用并返回 pool、应用 id 与一个 ConfigService。
// pub 传 nil：本任务只测读，推送在 Task 5。
//
// **两个顺序陷阱**：
//  1. testsupport.NewTestDB 每次调用都会 TRUNCATE 全部业务表。所以拿 pool
//     和调 newAppService（它内部又调一次 NewTestDB）都必须发生在建应用
//     **之前**——反过来的话，刚建好的应用会被下一次 TRUNCATE 冲掉，
//     而报错会是一句与真实原因毫无关系的外键失败。
//  2. ApplicationService.Create 返回**三个**值 (*domain.Application, secret, error)，
//     明文密钥只在创建时返回这一次。
//
// newAppService 是 application_test.go 里已有的辅助（同属 package service_test），
// 直接用，不要另建一个 registry。
func newConfigFixture(t *testing.T) (*pgxpool.Pool, *service.ConfigService, uuid.UUID) {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	apps := newAppService(t)
	app, _, err := apps.Create(context.Background(), "商城", "shop")
	if err != nil {
		t.Fatalf("建应用失败: %v", err)
	}
	return pool, service.NewConfigService(pool, nil), app.ID
}

func field(typ, desc, value string) domain.ConfigField {
	return domain.ConfigField{Type: typ, Desc: desc, Value: json.RawMessage(value)}
}

func TestCurrentReturnsEmptyWhenNoVersion(t *testing.T) {
	_, svc, appID := newConfigFixture(t)

	got, err := svc.Current(context.Background(), appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if got.Seq != 0 {
		t.Fatalf("Seq = %d，期望 0", got.Seq)
	}
	// 必须是非 nil 的空 map：调用方会直接 range 它，返回 nil 会让
	// "还没有任何版本"和"这一版是空的"在下游产生不同的分支。
	if got.Fields == nil {
		t.Fatal("Fields 是 nil，期望非 nil 的空 map")
	}
	if len(got.Fields) != 0 {
		t.Fatalf("Fields 有 %d 项，期望 0", len(got.Fields))
	}
}

func TestVersionNotFound(t *testing.T) {
	_, svc, appID := newConfigFixture(t)

	_, err := svc.Version(context.Background(), appID, domain.ConfigTypeDefault, 7)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，期望包装了 domain.ErrNotFound", err)
	}
}

func TestRejectsUnknownPartition(t *testing.T) {
	_, svc, appID := newConfigFixture(t)

	_, err := svc.Current(context.Background(), appID, "MOBILE")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v，期望包装了 domain.ErrInvalidArgument", err)
	}
}

// 往返：直接用 SQL 插一行版本，再用 Current 读回来，逐字段核对。
// 这条是时间戳单位那个 bug 的守护——三个空路径测试永远测不到它。
func TestCurrentReadsBackInsertedVersion(t *testing.T) {
	pool, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO config (application_id, type, seq, fields)
		VALUES ($1, $2, 1, $3)`,
		appID, domain.ConfigTypeDefault,
		`{"fee_rate":{"type":"float","desc":"手续费率","value":0.02}}`); err != nil {
		t.Fatalf("插入版本失败: %v", err)
	}

	got, err := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("读当前版本失败: %v", err)
	}
	if got.Seq != 1 {
		t.Fatalf("Seq = %d，期望 1", got.Seq)
	}
	if got.ApplicationID != appID {
		t.Fatalf("ApplicationID = %s，期望 %s", got.ApplicationID, appID)
	}
	if got.Type != domain.ConfigTypeDefault {
		t.Fatalf("Type = %q，期望 DEFAULT", got.Type)
	}
	f, ok := got.Fields["fee_rate"]
	if !ok {
		t.Fatal("fee_rate 没被解出来")
	}
	if f.Type != domain.ConfigValueFloat || f.Desc != "手续费率" || string(f.Value) != "0.02" {
		t.Fatalf("fee_rate = %+v，期望 {float 手续费率 0.02}", f)
	}

	// CreatedAt 必须是**毫秒**。用数据库自己的时钟做基准，避免本机与
	// 局域网 Postgres 的时钟偏移干扰判断。
	var nowMs int64
	if err := pool.QueryRow(ctx, `SELECT (extract(epoch FROM now()) * 1000)::bigint`).Scan(&nowMs); err != nil {
		t.Fatalf("取数据库时间失败: %v", err)
	}
	if diff := nowMs - got.CreatedAt; diff < 0 || diff > 60_000 {
		t.Fatalf("CreatedAt = %d，与数据库当前毫秒 %d 相差 %d ——单位不是毫秒？", got.CreatedAt, nowMs, diff)
	}
}

// ListVersions 按 seq 降序，且元素的 Fields 恒为 nil（列表页不需要快照）。
func TestListVersionsIsDescendingWithoutFields(t *testing.T) {
	pool, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	for seq := 1; seq <= 3; seq++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO config (application_id, type, seq, fields)
			VALUES ($1, $2, $3, '{"a":{"type":"int","desc":"","value":1}}')`,
			appID, domain.ConfigTypeDefault, seq); err != nil {
			t.Fatalf("插入 v%d 失败: %v", seq, err)
		}
	}

	vs, err := svc.ListVersions(ctx, appID, domain.ConfigTypeDefault, 20)
	if err != nil {
		t.Fatalf("列版本失败: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("版本数 = %d，期望 3", len(vs))
	}
	// 降序：最新的在最前
	if vs[0].Seq != 3 || vs[1].Seq != 2 || vs[2].Seq != 1 {
		t.Fatalf("顺序 = %d,%d,%d，期望 3,2,1", vs[0].Seq, vs[1].Seq, vs[2].Seq)
	}
	for _, v := range vs {
		if v.Fields != nil {
			t.Fatalf("v%d 的 Fields 应当是 nil，列表页不需要整份快照", v.Seq)
		}
	}
}
