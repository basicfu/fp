package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

func TestCurrentReturnsEmptyWhenNoVersion(t *testing.T) {
	_, svc, appID := newConfigFixture(t)

	got, err := svc.Current(context.Background(), appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if got.Seq != 0 {
		t.Fatalf("Seq = %d，期望 0", got.Seq)
	}
	if got.Value != "" {
		t.Fatalf("Value = %q，期望空字符串", got.Value)
	}
}

func TestVersionNotFound(t *testing.T) {
	_, svc, appID := newConfigFixture(t)

	_, err := svc.Version(context.Background(), appID, domain.ConfigTypeDefault, 7)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，期望包装了 domain.ErrNotFound", err)
	}
}

// 分区名不再局限于 DEFAULT/WEB——MOBILE 这类自定义名字现在是合法的，
// 这条测试改成断言字符集明显不合法的分区名（数字开头）仍然被拒绝。
func TestRejectsInvalidPartitionName(t *testing.T) {
	_, svc, appID := newConfigFixture(t)

	_, err := svc.Current(context.Background(), appID, "1mobile")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v，期望包装了 domain.ErrInvalidArgument", err)
	}
}

// 往返：直接用 SQL 插一行版本，再用 Current 读回来，核对原样返回。
// 这条是时间戳单位那个 bug 的守护——三个空路径测试永远测不到它。
func TestCurrentReadsBackInsertedVersion(t *testing.T) {
	pool, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	const yamlText = "fee_rate: 0.02 # 手续费率\n"
	if _, err := pool.Exec(ctx, `
		INSERT INTO config (application_id, type, seq, value)
		VALUES ($1, $2, 1, $3)`,
		appID, domain.ConfigTypeDefault, yamlText); err != nil {
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
	// 原样返回，包括注释——存储层不该在读的时候悄悄改写文本。
	if got.Value != yamlText {
		t.Fatalf("Value = %q，期望 %q", got.Value, yamlText)
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

// ListVersions 按 seq 降序，且元素的 Value 恒为空字符串（列表页不需要快照）。
func TestListVersionsIsDescendingWithoutValue(t *testing.T) {
	pool, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	for seq := 1; seq <= 3; seq++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO config (application_id, type, seq, value)
			VALUES ($1, $2, $3, 'a: 1')`,
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
		if v.Value != "" {
			t.Fatalf("v%d 的 Value 应当是空字符串，列表页不需要整份快照", v.Seq)
		}
	}
}

func TestSaveCreatesSequentialVersions(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	seq1, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, "fee_rate: 0.006\n", false)
	if err != nil {
		t.Fatalf("第一次保存失败: %v", err)
	}
	if seq1 != 1 {
		t.Fatalf("首个版本 seq = %d，期望 1", seq1)
	}

	seq2, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, "fee_rate: 0.02\n", false)
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
	if !strings.Contains(cur.Value, "0.02") {
		t.Fatalf("当前值 = %q，期望包含 0.02", cur.Value)
	}

	// 旧版本必须原样还在——回滚全靠它。
	old, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, 1)
	if err != nil {
		t.Fatalf("读 v1 失败: %v", err)
	}
	if !strings.Contains(old.Value, "0.006") {
		t.Fatalf("v1 的值 = %q，期望包含 0.006", old.Value)
	}
}

// 删除配置项就是"新版本的 YAML 里没有它这一行"。这条同时验证 Save 是全量
// 替换而不是合并——如果实现写成了 merge，被删的 key 会留在新版本里。
func TestSaveIsFullReplacement(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, "a: 1\nb: 2\n", false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, "a: 1\n", false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	cur, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	curFields, err := domain.ParseConfigYAML(cur.Value)
	if err != nil {
		t.Fatalf("解析当前版本失败: %v", err)
	}
	if _, ok := curFields["b"]; ok {
		t.Fatal("b 已被删除，不该出现在当前版本里")
	}

	old, _ := svc.Version(ctx, appID, domain.ConfigTypeDefault, 1)
	oldFields, err := domain.ParseConfigYAML(old.Value)
	if err != nil {
		t.Fatalf("解析 v1 失败: %v", err)
	}
	if _, ok := oldFields["b"]; !ok {
		t.Fatal("b 必须留在 v1 里，否则回滚恢复不了它")
	}
}

// Save 只校验"整份文本是不是合法的 YAML、顶层是不是映射"，语法错误
// 整份拒绝——不落库，一个版本都不生成。这是旧版"逐字段类型转换失败就
// 整批拒绝"这条原则在新模型下的延续：校验粒度从字段变成了整份文本，
// 但"一次保存要么整个生效、要么什么都不生效"没有变。
func TestSaveRejectsInvalidYAML(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	_, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, "a:\n  b: 1\n c: 2\n", false)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v，期望包装了 domain.ErrInvalidArgument", err)
	}

	// 关键：语法错误不该留下任何版本。
	cur, err := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("读当前版本失败: %v", err)
	}
	if cur.Seq != 0 {
		t.Fatalf("Seq = %d，期望 0——语法不合法不该留下任何版本", cur.Seq)
	}
}

// 顶层必须是映射，裸标量/数组同样整份拒绝——SDK 期待的是能按 key 查值的
// 对象，顶层不是映射就没有 key 可言。
func TestSaveRejectsNonMappingTopLevel(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	for _, yamlText := range []string{"hello", "- a\n- b\n"} {
		_, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, yamlText, false)
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("value=%q: err = %v，期望包装了 domain.ErrInvalidArgument", yamlText, err)
		}
	}
}

// 分区隔离：两个分区各有各的 seq，改一个不影响另一个。
// 【辨别力】两个分区的值必须**不同**，否则"分区对了"和"压根没分区"
// 产出同样的结果。
func TestSaveIsolatesPartitions(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, "site.title: 后端看到的\n", false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := svc.Save(ctx, appID, domain.ConfigTypeWeb, "site.title: 前端看到的\n", false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	def, _ := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	web, _ := svc.Current(ctx, appID, domain.ConfigTypeWeb)

	if !strings.Contains(def.Value, "后端看到的") {
		t.Fatalf("DEFAULT 分区的值 = %q", def.Value)
	}
	if !strings.Contains(web.Value, "前端看到的") {
		t.Fatalf("WEB 分区的值 = %q", web.Value)
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
		if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, fmt.Sprintf("n: %d\n", i), false); err != nil {
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
	if want := fmt.Sprintf("n: %d\n", total); cur.Value != want {
		t.Fatalf("当前值 = %q，期望 %q", cur.Value, want)
	}
}

// 并发保存同一分区时，抢输的那些必须拿到可被 errors.Is 识别的冲突错误，
// 而不是裸的 pgconn 错误——注释里"冲突了重试即可"靠的就是这个。
//
// 为什么要重试整场竞争：这条测试是那段 pgUniqueViolation → ErrConflict
// 转换的唯一回归守护，可它只有在**真的发生冲突**时才会执行到那段代码。
// 只断言"失败的都是 ErrConflict"的话，一次零冲突的运行会让测试空转着变绿，
// 守护悄悄失效。反过来，单跑一轮就硬性要求"必须冲突"又会把调度不确定性
// 变成随机红。重试有界次数两头都占：正常情况下第一轮就撞上，
// 而真的连 maxRounds 轮都撞不出冲突时，说明这段转换已经无法被测到，
// 那本身就该红。
func TestSaveConcurrentConflictIsRetryable(t *testing.T) {
	const (
		goroutines = 8
		maxRounds  = 5
	)

	for round := 1; round <= maxRounds; round++ {
		_, svc, appID := newConfigFixture(t)
		ctx := context.Background()

		errs := make(chan error, goroutines)
		var wg sync.WaitGroup
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, fmt.Sprintf("n: %d\n", i), false)
				errs <- err
			}(i)
		}
		wg.Wait()
		close(errs)

		var ok, conflicts int
		for err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, domain.ErrConflict):
				conflicts++
			default:
				// 一个漏网的裸 pg 错误都不许有——这条不受重试影响，
				// 任何一轮出现都立即失败。
				t.Fatalf("第 %d 轮：既不是成功也不是 ErrConflict 的错误: %v", round, err)
			}
		}
		if ok == 0 {
			t.Fatalf("第 %d 轮：至少要有一次保存成功", round)
		}
		if conflicts > 0 {
			t.Logf("第 %d 轮观察到冲突：成功 %d 次，冲突 %d 次", round, ok, conflicts)
			return // 转换路径已被执行到，测试目的达成
		}
	}
	t.Fatalf("跑满 %d 轮、每轮 %d 个并发，一次冲突都没观察到——"+
		"pgUniqueViolation → ErrConflict 那段转换没有被任何测试执行到", maxRounds, goroutines)
}

// 回滚生成新版本，不删历史。v1 的完整快照（含注释）必须原样复现。
func TestRollbackCopiesVersionForward(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	// v1：两项
	mustSave(t, svc, appID, "a: 1 # 甲\nb: x # 乙\n")
	// v2：删掉 b
	mustSave(t, svc, appID, "a: 1 # 甲\n")
	// v3：把 a 从标量改成映射（改"类型"在新模型下也只是普通的一次保存）
	mustSave(t, svc, appID, "a:\n  k: 1\n")

	newSeq, err := svc.Rollback(ctx, appID, domain.ConfigTypeDefault, 1, false)
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if newSeq != 4 {
		t.Fatalf("回滚生成的版本 = %d，期望 4", newSeq)
	}

	// v1..v3 必须原样都在——回滚是往前追加，不是往回删。
	for seq := int64(1); seq <= 3; seq++ {
		if _, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, seq); err != nil {
			t.Fatalf("v%d 应当仍在: %v", seq, err)
		}
	}

	v1, _ := svc.Version(ctx, appID, domain.ConfigTypeDefault, 1)
	v4, _ := svc.Version(ctx, appID, domain.ConfigTypeDefault, 4)
	// 原样复制，逐字节相同——包括注释、顺序、缩进，这正是"不经过对象转一圈"
	// 这条设计要保住的东西。
	if v4.Value != v1.Value {
		t.Fatalf("v4 = %q，期望与 v1 逐字节相同 %q", v4.Value, v1.Value)
	}
}

// 目标版本的内容和当前版本逐字节相同时，回滚不该凭空生出一个新版本号——
// 这种回滚不会让任何东西发生变化，历史记录不该因为点了一次"回滚"就比
// 实际发生过的变更还长。
//
// 特意让 v1 和当前版本（v3）内容相同、但 seq 不同（v3 是手动改回 v1 的
// 值），而不是直接回滚到当前版本自己那种平凡情形——这样断言的是"内容
// 相等"这条真正的判据，不是"seq 相等"这种更弱、可能蒙混过关的替代品。
func TestRollbackNoopWhenContentUnchanged(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	mustSave(t, svc, appID, "a: 1\n") // v1
	mustSave(t, svc, appID, "a: 2\n") // v2
	mustSave(t, svc, appID, "a: 1\n") // v3（当前）——手动改回了和 v1 一样的内容

	newSeq, err := svc.Rollback(ctx, appID, domain.ConfigTypeDefault, 1, true)
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if newSeq != 3 {
		t.Fatalf("目标版本 v1 与当前版本 v3 内容一致，回滚不该生成新版本，newSeq = %d，期望原样返回当前 seq 3", newSeq)
	}

	cur, err := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("查询当前版本失败: %v", err)
	}
	if cur.Seq != 3 {
		t.Fatalf("不该多出任何版本，当前 seq = %d，期望仍是 3", cur.Seq)
	}
}

func TestRollbackToMissingVersion(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	_, err := svc.Rollback(context.Background(), appID, domain.ConfigTypeDefault, 9, false)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，期望包装了 domain.ErrNotFound", err)
	}
}

// ListTypes 只列出真的保存过版本的分区——一个应用刚建好、什么都没存过时，
// 结果应当是空的（不是自动带上 DEFAULT，那条"DEFAULT 标签页永远展示"的
// UI 规则是前端自己兜底的，不是这层的职责），保存过的分区按字母序列出。
func TestListTypesOnlyReturnsTypesWithSavedVersions(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	empty, err := svc.ListTypes(ctx, appID)
	if err != nil {
		t.Fatalf("列分区失败: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("还没存过任何东西，分区列表应当是空的，得到 %v", empty)
	}

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeWeb, "a: 1\n", false); err != nil {
		t.Fatalf("保存 WEB 失败: %v", err)
	}
	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, "a: 1\n", false); err != nil {
		t.Fatalf("保存 DEFAULT 失败: %v", err)
	}

	got, err := svc.ListTypes(ctx, appID)
	if err != nil {
		t.Fatalf("列分区失败: %v", err)
	}
	// 按字母序：DEFAULT 在 WEB 前面。
	if len(got) != 2 || got[0] != domain.ConfigTypeDefault || got[1] != domain.ConfigTypeWeb {
		t.Fatalf("得到 %v，期望 [DEFAULT WEB]", got)
	}
}

// DeleteType 删掉的是**整个分区**的全部版本，不是新建一个空版本——删完
// 之后连 seq=1 都查不到了，与"保存一个空 value"（还会留一条新版本）是
// 两件不同的事。
// 【辨别力】必须造两个分区，只删一个，断言另一个不受影响——否则一个
// 把整个应用的配置全删了的实现同样会让"目标分区查不到"这条断言通过。
func TestDeleteTypeRemovesAllVersions(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	mustSave(t, svc, appID, "a: 1\n")
	mustSave(t, svc, appID, "a: 2\n")
	if _, err := svc.Save(ctx, appID, domain.ConfigTypeWeb, "b: 1\n", false); err != nil {
		t.Fatalf("保存 WEB 失败: %v", err)
	}

	if err := svc.DeleteType(ctx, appID, domain.ConfigTypeDefault); err != nil {
		t.Fatalf("删除分区失败: %v", err)
	}

	cur, err := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("Current 不该报错（一个版本都没有是正常状态）: %v", err)
	}
	if cur.Seq != 0 || cur.Value != "" {
		t.Fatalf("DEFAULT 应当已经没有任何版本，得到 seq=%d value=%q", cur.Seq, cur.Value)
	}
	if _, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("v1 应当也查不到了，err = %v", err)
	}
	if _, err := svc.Version(ctx, appID, domain.ConfigTypeDefault, 2); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("v2 应当也查不到了，err = %v", err)
	}

	// 另一个分区（WEB）不受影响。
	web, err := svc.Current(ctx, appID, domain.ConfigTypeWeb)
	if err != nil {
		t.Fatalf("读 WEB 失败: %v", err)
	}
	if web.Value != "b: 1\n" {
		t.Fatalf("WEB 的值不该受 DEFAULT 删除影响，得到 %q", web.Value)
	}
}

func TestDeleteTypeRejectsInvalidPartitionName(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	err := svc.DeleteType(context.Background(), appID, "1mobile")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v，期望包装了 domain.ErrInvalidArgument", err)
	}
}

// Save 提交的 "port:4379" 这种漏空格的写法要能存进去，且落库的是补完
// 空格之后的文本（回显不再保证与提交的原文逐字节相同，这是
// NormalizeConfigYAML 的已知取舍）。
func TestSaveNormalizesMissingColonSpace(t *testing.T) {
	_, svc, appID := newConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, appID, domain.ConfigTypeDefault, "port:4379\n", false); err != nil {
		t.Fatalf("漏空格的 YAML 应当能保存: %v", err)
	}
	cur, err := svc.Current(ctx, appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("读当前版本失败: %v", err)
	}
	if cur.Value != "port: 4379\n" {
		t.Fatalf("落库的值 = %q，期望补完空格后的 %q", cur.Value, "port: 4379\n")
	}
}

func mustSave(t *testing.T, svc *service.ConfigService, appID uuid.UUID, value string) {
	t.Helper()
	if _, err := svc.Save(context.Background(), appID, domain.ConfigTypeDefault, value, false); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
}
