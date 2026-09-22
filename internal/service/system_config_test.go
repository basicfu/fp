package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func newSystemConfigFixture(t *testing.T) *service.SystemConfigService {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	return service.NewSystemConfigService(pool)
}

func TestSystemConfigCurrentReturnsEmptyWhenNoVersion(t *testing.T) {
	svc := newSystemConfigFixture(t)
	got, err := svc.Current(context.Background())
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

func TestSystemConfigVersionNotFound(t *testing.T) {
	svc := newSystemConfigFixture(t)
	_, err := svc.Version(context.Background(), 7)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，期望 domain.ErrNotFound", err)
	}
}

func TestSystemConfigSaveIncrementsSeqAndIsFullReplacement(t *testing.T) {
	svc := newSystemConfigFixture(t)
	ctx := context.Background()

	seq1, err := svc.Save(ctx, "a: 1\nb: 2\n")
	if err != nil {
		t.Fatalf("首次保存: %v", err)
	}
	if seq1 != 1 {
		t.Fatalf("seq1 = %d，期望 1", seq1)
	}

	seq2, err := svc.Save(ctx, "a: 1\n")
	if err != nil {
		t.Fatalf("二次保存: %v", err)
	}
	if seq2 != 2 {
		t.Fatalf("seq2 = %d，期望 2", seq2)
	}

	cur, err := svc.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur.Value != "a: 1\n" {
		t.Fatalf("当前值 = %q，期望 %q（全量替换，b 应当消失）", cur.Value, "a: 1\n")
	}
}

func TestSystemConfigSaveRejectsInvalidYAML(t *testing.T) {
	svc := newSystemConfigFixture(t)
	_, err := svc.Save(context.Background(), "- a\n- b\n")
	if err == nil {
		t.Fatal("顶层不是映射应当被拒")
	}
}

func TestSystemConfigRollback(t *testing.T) {
	svc := newSystemConfigFixture(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, "a: 1\n"); err != nil {
		t.Fatalf("v1: %v", err)
	}
	if _, err := svc.Save(ctx, "a: 2\n"); err != nil {
		t.Fatalf("v2: %v", err)
	}

	newSeq, err := svc.Rollback(ctx, 1)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if newSeq != 3 {
		t.Fatalf("回滚后 seq = %d，期望 3（回滚是往前追加，不是往回删）", newSeq)
	}

	cur, err := svc.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur.Value != "a: 1\n" {
		t.Fatalf("回滚后值 = %q，期望 %q", cur.Value, "a: 1\n")
	}
}

// 回滚到内容与当前完全一致的版本不该凭空多出一个版本号。
func TestSystemConfigRollbackToSameContentProducesNoNewVersion(t *testing.T) {
	svc := newSystemConfigFixture(t)
	ctx := context.Background()

	seq1, err := svc.Save(ctx, "a: 1\n")
	if err != nil {
		t.Fatalf("v1: %v", err)
	}

	newSeq, err := svc.Rollback(ctx, seq1)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if newSeq != seq1 {
		t.Fatalf("newSeq = %d，期望原样返回 %d（内容没变，不该产生新版本）", newSeq, seq1)
	}
}

func TestSystemConfigListVersionsDescending(t *testing.T) {
	svc := newSystemConfigFixture(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if _, err := svc.Save(ctx, fmt.Sprintf("n: %d\n", i)); err != nil {
			t.Fatalf("保存第 %d 版: %v", i, err)
		}
	}

	vs, err := svc.ListVersions(ctx, 20)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("版本数 = %d，期望 3", len(vs))
	}
	if vs[0].Seq != 3 {
		t.Fatalf("第一条 seq = %d，期望 3（降序）", vs[0].Seq)
	}
}

// 超过 SystemConfigMaxVersions 版之后，最老的会被修剪掉。
func TestSystemConfigPrunesOldVersions(t *testing.T) {
	svc := newSystemConfigFixture(t)
	ctx := context.Background()

	for i := 1; i <= service.SystemConfigMaxVersions+5; i++ {
		if _, err := svc.Save(ctx, fmt.Sprintf("n: %d\n", i)); err != nil {
			t.Fatalf("保存第 %d 版: %v", i, err)
		}
	}

	if _, err := svc.Version(ctx, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("第 1 版应该已被修剪掉，err = %v", err)
	}
	if _, err := svc.Version(ctx, 6); err != nil {
		t.Fatalf("第 6 版不该被修剪: %v", err)
	}
}
