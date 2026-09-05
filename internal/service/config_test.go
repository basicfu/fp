package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestSaveCreatesSequentialVersions(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	seq1, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"fee_rate": field(domain.ConfigValueFloat, "手续费率", `0.006`),
	}, false)
	if err != nil {
		t.Fatalf("第一次保存失败: %v", err)
	}
	if seq1 != 1 {
		t.Fatalf("首个版本 seq = %d，期望 1", seq1)
	}

	seq2, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"fee_rate": field(domain.ConfigValueFloat, "手续费率", `0.02`),
	}, false)
	if err != nil {
		t.Fatalf("第二次保存失败: %v", err)
	}
	if seq2 != 2 {
		t.Fatalf("第二个版本 seq = %d，期望 2", seq2)
	}

	cur, err := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("读当前版本失败: %v", err)
	}
	if got := string(cur.Fields["fee_rate"].Value); got != "0.02" {
		t.Fatalf("当前值 = %s，期望 0.02", got)
	}

	// 旧版本必须原样还在——回滚全靠它。
	old, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, 1)
	if err != nil {
		t.Fatalf("读 v1 失败: %v", err)
	}
	if got := string(old.Fields["fee_rate"].Value); got != "0.006" {
		t.Fatalf("v1 的值 = %s，期望 0.006", got)
	}
}

// 删除配置项就是"新版本的 fields 里没有它"。这条同时验证 Save 是全量替换
// 而不是合并——如果实现写成了 merge，被删的 key 会留在新版本里。
func TestSaveIsFullReplacement(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"a": field(domain.ConfigValueInt, "", `1`),
		"b": field(domain.ConfigValueInt, "", `2`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"a": field(domain.ConfigValueInt, "", `1`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	cur, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if _, ok := cur.Fields["b"]; ok {
		t.Fatal("b 已被删除，不该出现在当前版本里")
	}
	old, _ := svc.Version(ctx, appID, domain.ConfigTypeDefault, 1)
	if _, ok := old.Fields["b"]; !ok {
		t.Fatal("b 必须留在 v1 里，否则回滚恢复不了它")
	}
}

// 未配置（value 是 JSON null）与已删除（key 不在 map 里）是两件事。
// 【辨别力】两种情形必须同时造出来：只造一种的话，把两者混为一谈的实现
// （比如保存时顺手丢掉 value 为 null 的项）照样会绿。
func TestSaveKeepsUnsetFieldsDistinctFromDeleted(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"unset":   field(domain.ConfigValueString, "还没配", `null`),
		"deleted": field(domain.ConfigValueInt, "马上删", `1`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"unset": field(domain.ConfigValueString, "还没配", `null`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	cur, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	f, ok := cur.Fields["unset"]
	if !ok {
		t.Fatal("未配置的项必须仍然存在——它是控制台上待填的那一行")
	}
	if f.IsSet() {
		t.Fatal("unset 不该被判成已配置")
	}
	if _, ok := cur.Fields["deleted"]; ok {
		t.Fatal("已删除的项不该出现")
	}
}

// 保存时按类型转换，转不过去才报错（弱约束）。
func TestSaveCoercesValues(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"n": field(domain.ConfigValueInt, "", `"3"`),
	}, false); err != nil {
		t.Fatalf("字符串 3 应当能存进 int 项: %v", err)
	}
	cur, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if got := string(cur.Fields["n"].Value); got != "3" {
		t.Fatalf("存进去的值 = %s，期望规范化成 3", got)
	}

	_, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"n": field(domain.ConfigValueInt, "", `"abc"`),
	}, false)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v，期望包装了 domain.ErrInvalidArgument", err)
	}
}

// 分区隔离：两个分区各有各的 seq，改一个不影响另一个。
// 【辨别力】两个分区的值必须**不同**，否则"分区对了"和"压根没分区"
// 产出同样的结果。
func TestSaveIsolatesPartitions(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
		"site.title": field(domain.ConfigValueString, "", `"后端看到的"`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := svc.Save(ctx, appID, domain.ConfigTypeWeb, map[string]domain.ConfigField{
		"site.title": field(domain.ConfigValueString, "", `"前端看到的"`),
	}, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	def, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	web, _ := svc.Current(ctx, appID, domain.ConfigTypeWeb)

	if got := string(def.Fields["site.title"].Value); got != `"后端看到的"` {
		t.Fatalf("DEFAULT 分区的值 = %s", got)
	}
	if got := string(web.Fields["site.title"].Value); got != `"前端看到的"` {
		t.Fatalf("WEB 分区的值 = %s", got)
	}
	// seq 是**分区内**自增：两个分区各写了一次，各自都该是 1。
	if def.Seq != 1 || web.Seq != 1 {
		t.Fatalf("DEFAULT seq=%d, WEB seq=%d，期望各自都是 1", def.Seq, web.Seq)
	}
}

// 造满上限再多 5 版，断言最老的 5 版被删、其余都在、当前配置不受影响。
func TestSavePrunesOldVersions(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	total := service.ConfigMaxVersions + 5
	for i := 1; i <= total; i++ {
		if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, map[string]domain.ConfigField{
			"n": field(domain.ConfigValueInt, "", fmt.Sprintf("%d", i)),
		}, false); err != nil {
			t.Fatalf("第 %d 次保存失败: %v", i, err)
		}
	}

	for seq := int64(1); seq <= 5; seq++ {
		if _, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, seq); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("v%d 应当已被修剪，err = %v", seq, err)
		}
	}
	if _, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, 6); err != nil {
		t.Fatalf("v6 应当还在: %v", err)
	}
	cur, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if cur.Seq != int64(total) {
		t.Fatalf("当前版本 seq = %d，期望 %d", cur.Seq, total)
	}
	if got := string(cur.Fields["n"].Value); got != fmt.Sprintf("%d", total) {
		t.Fatalf("当前值 = %s，期望 %d", got, total)
	}
}
