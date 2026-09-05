package service_test

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

func (e *authzEnv) report(t *testing.T, keys ...string) {
	t.Helper()
	pts := make([]service.ReportedPoint, 0, len(keys))
	for _, k := range keys {
		pts = append(pts, service.ReportedPoint{Key: k, Kind: domain.PermissionKindAPI})
	}
	if err := e.svc.ReportPermissions(context.Background(), e.app.ID, pts); err != nil {
		t.Fatalf("ReportPermissions: %v", err)
	}
}

func (e *authzEnv) list(t *testing.T) map[string]service.PermissionListItem {
	t.Helper()
	items, err := e.svc.ListPermissions(context.Background(), e.app.ID)
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	out := map[string]service.PermissionListItem{}
	for _, it := range items {
		out[it.Permission.Key] = it
	}
	return out
}

func TestReportCreatesPoints(t *testing.T) {
	e := newAuthzEnv(t)
	e.report(t, "GET:/orders/{id}", "POST:/orders")

	got := e.list(t)
	if len(got) != 2 {
		t.Fatalf("权限点数 = %d, want 2", len(got))
	}
	for k, it := range got {
		if it.Permission.Source != domain.PermissionSourceApp {
			t.Fatalf("%s 的 source = %q, want app", k, it.Permission.Source)
		}
		if it.Status != domain.PermissionStatusNormal {
			t.Fatalf("%s 的状态 = %q, want normal", k, it.Status)
		}
	}
}

// 【辨别力】第二次上报少了一条，那一条**不能被删掉**。
//
// 10 个接口的应用发布后只剩 6 个——那 4 条要保留（连同它们的授权关系），
// 状态由 last_seen_at 算出来。一个"按快照删除缺失项"的实现会通过所有
// 只检查"新增的进来了"的测试。
func TestReportDoesNotDeleteMissingPoints(t *testing.T) {
	e := newAuthzEnv(t)

	e.report(t, "GET:/orders/{id}", "POST:/orders", "DELETE:/orders/{id}")
	e.report(t, "GET:/orders/{id}", "POST:/orders") // 少了 DELETE

	got := e.list(t)
	if len(got) != 3 {
		t.Fatalf("权限点数 = %d, want 3——缺失的那条被删掉了", len(got))
	}
	if _, ok := got["DELETE:/orders/{id}"]; !ok {
		t.Fatal("DELETE:/orders/{id} 被删掉了，它应当保留并转为过渡中")
	}
	// 刚上报完，还在阈值内，仍是正常——"过渡中"的判据是时间，不是"这次没报"。
	if got["DELETE:/orders/{id}"].Status != domain.PermissionStatusNormal {
		t.Fatalf("刚少报一次就判成 %q——判据应当是时间，而不是这次快照里有没有它",
			got["DELETE:/orders/{id}"].Status)
	}
}

// 【辨别力】人工填的 name 不能被后续上报覆盖。
//
// 必须**先人工改过再上报一次**才测得到。直接测"上报写入了 name"是测不到
// 覆盖问题的——那两种实现都会绿。漏了这条规则的后果是每次重启，运营辛苦
// 填的中文名就没了。
func TestReportDoesNotOverwriteManualName(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	e.report(t, "GET:/orders/{id}")
	p := e.list(t)["GET:/orders/{id}"].Permission

	if _, err := e.svc.UpdatePermission(ctx, p.ID, p.Key, "查看订单"); err != nil {
		t.Fatalf("UpdatePermission: %v", err)
	}
	e.report(t, "GET:/orders/{id}") // 再报一次，SDK 给不出 name

	if got := e.list(t)["GET:/orders/{id}"].Permission.Name; got != "查看订单" {
		t.Fatalf("name = %q, want 查看订单——上报覆盖了人工填的显示名", got)
	}
}

// 【辨别力】手动加的权限点永远不被上报触碰。
//
// 即使上报的 key 与它恰好重名，也不能把它降级成 source=app——那样它会
// 开始参与"消失"判定，而它本来就不在业务方的路由清单里。
func TestReportDoesNotTouchManualPoints(t *testing.T) {
	e := newAuthzEnv(t)

	manual := e.mustPerm(t, "GET:/orders/{id}") // source=manual
	e.report(t, "GET:/orders/{id}")             // 同名上报

	got := e.list(t)["GET:/orders/{id}"].Permission
	if got.ID != manual.ID {
		t.Fatal("上报新建了一条重复的权限点")
	}
	if got.Source != domain.PermissionSourceManual {
		t.Fatalf("source = %q, want manual——手动权限点被上报降级了", got.Source)
	}
	if e.list(t)["GET:/orders/{id}"].Status != domain.PermissionStatusManual {
		t.Fatal("手动权限点的状态应当恒为 manual")
	}
}

// 重新出现的权限点自动回到正常——误删接口、下个版本又加回来时不用人操作。
func TestReportRevivesPoint(t *testing.T) {
	e := newAuthzEnv(t)

	e.report(t, "GET:/orders/{id}")
	first := e.list(t)["GET:/orders/{id}"].Permission.LastSeenAt

	e.report(t, "POST:/orders")     // 不含它
	e.report(t, "GET:/orders/{id}") // 又回来了

	second := e.list(t)["GET:/orders/{id}"].Permission.LastSeenAt
	if second <= first {
		t.Fatalf("last_seen_at 没有被刷新: %d → %d", first, second)
	}
}

// 上报不改 key 已存在的权限点的授权关系——重启不该动任何人的权限。
func TestReportPreservesGrants(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	e.report(t, "GET:/orders/{id}")
	perm := e.list(t)["GET:/orders/{id}"].Permission
	role := e.mustRole(t, "普通用户", nil)
	if err := e.svc.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatalf("授权: %v", err)
	}

	e.report(t, "GET:/orders/{id}") // 再报一次

	pol, err := e.svc.CompilePolicy(ctx, e.app.ID)
	if err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}
	got := rolePolicyOf(t, pol, "普通用户")
	if len(got.Allow) != 1 {
		t.Fatalf("重新上报后授权关系变了: %+v", got)
	}
}

// 改权限点的 key：授权关系必须自动跟随（role_permission 按 id 引用）。
func TestUpdatePermissionKeyKeepsGrants(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	perm := e.mustPerm(t, "GET:/oders/{id}") // 路径里有拼写错误
	role := e.mustRole(t, "普通用户", nil)
	e.grant(t, role, perm, domain.EffectAllow)

	if _, err := e.svc.UpdatePermission(ctx, perm.ID, "GET:/orders/{id}", "查看订单"); err != nil {
		t.Fatalf("UpdatePermission: %v", err)
	}

	pol, err := e.svc.CompilePolicy(ctx, e.app.ID)
	if err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}
	got := rolePolicyOf(t, pol, "普通用户")
	if len(got.Allow) != 1 || got.Allow[0] != "GET:/orders/{id}" {
		t.Fatalf("改 key 后 allow = %v, want [GET:/orders/{id}]——授权没有跟随", got.Allow)
	}
}
