package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/sdk/authzcore"
)

func TestGuestRoleRestrictions(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	guest := e.mustRole(t, authzcore.GuestRoleKey, nil)
	normal := e.mustRole(t, "普通用户", nil)
	perm := e.mustPerm(t, "GET:/pub")

	_, err := e.svc.UpdateRole(ctx, guest.ID, "访客", &normal.ID)
	wantDomainCode(t, err, domain.CodeRoleBuiltin)
	wantDomainCode(t, e.svc.SetRolePermission(ctx, guest.ID, perm.ID, domain.EffectDeny), domain.CodeRoleBuiltin)
	if err := e.svc.SetRolePermission(ctx, guest.ID, perm.ID, domain.EffectAllow); err != nil {
		t.Fatalf("GUEST 配「允许」应当成功: %v", err)
	}
	wantDomainCode(t, e.svc.DeleteRole(ctx, guest.ID), domain.CodeRoleBuiltin)
	if _, err := e.svc.UpdateRole(ctx, normal.ID, "普通用户", &guest.ID); err != nil {
		t.Fatalf("普通角色继承 GUEST 不受限制: %v", err)
	}
}

func TestDeleteRoleBoundToAccessKey(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	partner := e.mustRole(t, "合作方A", nil)
	for i := 0; i < 2; i++ {
		if _, err := e.pool.Exec(ctx,
			`INSERT INTO access_key (access_key_id, secret, remark, role_key) VALUES ($1, 's', 'r', $2)`,
			fmt.Sprintf("FPAKTEST%016d", i), partner.Code); err != nil {
			t.Fatalf("插入 key: %v", err)
		}
	}
	err := e.svc.DeleteRole(ctx, partner.ID)
	wantDomainCode(t, err, domain.CodeRoleInUse)
	if !strings.Contains(err.Error(), "2 把") {
		t.Fatalf("提示里应带绑定数量: %v", err)
	}
	// 【辨别力】直接删行也必须被外键拦下，而不只靠应用层判断。
	_, err = e.pool.Exec(ctx, `DELETE FROM role WHERE id = $1`, partner.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("直接删除被绑定的角色应违反外键，err = %v", err)
	}
}

func TestAuthzWritesPublishPolicyChanged(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	pub := &recordingPublisher{}
	svc := service.NewAuthzService(e.pool, service.WithAuthzPublisher(pub))
	role := e.mustRole(t, "角色", nil)
	perm := e.mustPerm(t, "GET:/x")

	eventCount := func() int {
		pub.mu.Lock()
		defer pub.mu.Unlock()
		return len(pub.events)
	}
	// checked 记录上一次 check 通过时的事件总数：每一步都要求恰好新增一条，
	// 不能只看 pub.last(t)返回了什么——events 是只追加、从不重置的切片，
	// 如果某一步的生产代码漏调 announcePolicy，pub.last(t) 会原样吐出上一步
	// 遗留的旧事件；这条测试里连续三步的 wantApp 都是 e.app.ID，旧事件的
	// AppID 恰好对得上，断言会假通过，测试起不到回归护栏的作用。
	checked := 0
	check := func(step string, wantApp uuid.UUID) {
		t.Helper()
		if got := eventCount(); got != checked+1 {
			t.Fatalf("%s: 事件数 = %d，want %d（这一步应当恰好新增一条 PolicyChanged，不能是上一步遗留的）", step, got, checked+1)
		}
		checked++
		if ev := pub.last(t); ev.Kind != domain.EventKindPolicyChanged || ev.AppID != wantApp {
			t.Fatalf("%s: 事件 = %+v，want kind=%s app=%v", step, ev, domain.EventKindPolicyChanged, wantApp)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(svc.SetRolePermission(ctx, role.ID, perm.ID, domain.EffectAllow))
	check("授权", e.app.ID)
	must(svc.SetRolePermission(ctx, role.ID, perm.ID, ""))
	check("收回", e.app.ID)
	_, err := svc.UpdatePermission(ctx, perm.ID, "GET:/y", "y")
	must(err)
	check("改权限点", e.app.ID)
	_, err = svc.UpdateRole(ctx, role.ID, "角色2", nil)
	must(err)
	check("改角色", uuid.Nil)
	must(svc.DeletePermission(ctx, perm.ID))
	check("删权限点", e.app.ID)
	must(svc.DeleteRole(ctx, role.ID))
	check("删角色", uuid.Nil)
}

func TestRolePermissionsByApp(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	base := e.mustRole(t, "基线", nil)
	child := e.mustRole(t, "合作方", &base.ID)
	view := e.mustPerm(t, "GET:/orders/{id}")
	del := e.mustPerm(t, "DELETE:/orders/{id}")
	e.grant(t, base, view, domain.EffectAllow)
	e.grant(t, base, del, domain.EffectAllow)
	e.grant(t, child, del, domain.EffectDeny)

	got, err := e.svc.RolePermissionsByApp(ctx, child.Code)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AppID != e.app.ID || len(got[0].Points) != 1 || got[0].Points[0].Key != "GET:/orders/{id}" {
		t.Fatalf("got %+v，want 只有该应用的 GET:/orders/{id}（继承来的允许在，被子角色拒绝的不在）", got)
	}
	if empty, _ := e.svc.RolePermissionsByApp(ctx, ""); len(empty) != 0 {
		t.Fatalf("未绑定角色应返回空: %+v", empty)
	}
}
