package service_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

type authzEnv struct {
	svc  *service.AuthzService
	apps *service.ApplicationService
	app  *domain.Application
	pool *pgxpool.Pool
}

func newAuthzEnv(t *testing.T) *authzEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	apps := newAppServiceWith(t, pool)
	app, _, err := apps.Create(context.Background(), "商城", "mall")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	return &authzEnv{svc: service.NewAuthzService(pool), apps: apps, app: app, pool: pool}
}

// mustRole 建一个角色。
func (e *authzEnv) mustRole(t *testing.T, key string, parent *uuid.UUID) *domain.Role {
	t.Helper()
	r, err := e.svc.CreateRole(context.Background(), key, key, parent)
	if err != nil {
		t.Fatalf("创建角色 %s: %v", key, err)
	}
	return r
}

// mustPerm 手动建一个权限点。
func (e *authzEnv) mustPerm(t *testing.T, key string) *domain.Permission {
	t.Helper()
	p, err := e.svc.CreatePermission(context.Background(), e.app.ID, key, key, domain.PermissionKindAPI)
	if err != nil {
		t.Fatalf("创建权限点 %s: %v", key, err)
	}
	return p
}

func (e *authzEnv) grant(t *testing.T, role *domain.Role, perm *domain.Permission, effect string) {
	t.Helper()
	if err := e.svc.SetRolePermission(context.Background(), role.ID, perm.ID, effect); err != nil {
		t.Fatalf("授权: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 策略编译
// ---------------------------------------------------------------------------

// 【辨别力】继承必须被展开：子角色要拿到父角色的权限。
//
// 只断言"父角色自己的条目对"是不够的——那在完全不实现继承时也成立。
// 必须去看**子角色**那一条里有没有父角色的权限点。
func TestCompilePolicyExpandsInheritance(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	normal := e.mustRole(t, "普通用户", nil)
	admin := e.mustRole(t, "商城管理员", &normal.ID)

	view := e.mustPerm(t, "GET:/orders/{id}")
	del := e.mustPerm(t, "DELETE:/orders/{id}")
	e.grant(t, normal, view, domain.EffectAllow)
	e.grant(t, admin, del, domain.EffectAllow)

	pol, err := e.svc.CompilePolicy(ctx, e.app.ID)
	if err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}

	got := rolePolicyOf(t, pol, "商城管理员")
	sort.Strings(got.Allow)
	want := []string{"DELETE:/orders/{id}", "GET:/orders/{id}"}
	if len(got.Allow) != 2 || got.Allow[0] != want[0] || got.Allow[1] != want[1] {
		t.Fatalf("商城管理员的 allow = %v, want %v——继承没有展开", got.Allow, want)
	}

	// 父角色不该反过来拿到子角色的权限。
	n := rolePolicyOf(t, pol, "普通用户")
	if len(n.Allow) != 1 || n.Allow[0] != "GET:/orders/{id}" {
		t.Fatalf("普通用户的 allow = %v——继承方向反了", n.Allow)
	}
}

// 【辨别力】子角色的直接授权优先于祖先。
//
// 「商城管理员」继承「普通用户」，但显式 deny 了后者 allow 的一条权限。
// 一个"祖先覆盖子代"的实现会让这条变成 allow——那样"用继承表达基线、
// 用 deny 做减法"这个用法就不成立了。
func TestCompilePolicyChildOverridesParent(t *testing.T) {
	e := newAuthzEnv(t)

	normal := e.mustRole(t, "普通用户", nil)
	limited := e.mustRole(t, "受限用户", &normal.ID)

	post := e.mustPerm(t, "POST:/orders")
	e.grant(t, normal, post, domain.EffectAllow)
	e.grant(t, limited, post, domain.EffectDeny)

	pol, err := e.svc.CompilePolicy(context.Background(), e.app.ID)
	if err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}

	got := rolePolicyOf(t, pol, "受限用户")
	if len(got.Deny) != 1 || got.Deny[0] != "POST:/orders" {
		t.Fatalf("受限用户的 deny = %v, allow = %v——子角色的 deny 被祖先覆盖了", got.Deny, got.Allow)
	}
	if len(got.Allow) != 0 {
		t.Fatalf("受限用户不该有 allow: %v", got.Allow)
	}
}

// 【辨别力】在本应用没有任何权限的角色，不进这个应用的策略表。
//
// 这是"角色全局、应用归属由权限点决定"这个设计能成立的关键：用户带着
// 「视频管理员」去商城时，那个角色在商城的策略表里根本不存在、等同于没有。
// 一个把全部角色都塞进每个应用策略表的实现会通过其余所有测试。
func TestCompilePolicyExcludesRolesWithoutPermissionsHere(t *testing.T) {
	e := newAuthzEnv(t)

	e.mustRole(t, "视频管理员", nil) // 只建角色，不给商城的权限
	normal := e.mustRole(t, "普通用户", nil)
	e.grant(t, normal, e.mustPerm(t, "GET:/orders/{id}"), domain.EffectAllow)

	pol, err := e.svc.CompilePolicy(context.Background(), e.app.ID)
	if err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}
	for _, rp := range pol.Roles {
		if rp.RoleKey == "视频管理员" {
			t.Fatalf("视频管理员不该出现在商城的策略表里: %+v", rp)
		}
	}
	if len(pol.Roles) != 1 {
		t.Fatalf("策略表应当只有普通用户一条，实际 %d 条", len(pol.Roles))
	}
}

func rolePolicyOf(t *testing.T, p *domain.AppPolicy, key string) domain.RolePolicy {
	t.Helper()
	for _, rp := range p.Roles {
		if rp.RoleKey == key {
			return rp
		}
	}
	t.Fatalf("策略表里没有角色 %q，实际有 %+v", key, p.Roles)
	return domain.RolePolicy{}
}

// ---------------------------------------------------------------------------
// 有效角色：全局角色 ∪ 应用默认角色
// ---------------------------------------------------------------------------

// 【辨别力】默认角色必须**并入**，不是"没有显式角色才用"。
//
// 前置刻意让显式角色与默认角色不同：如果实现写成"有显式角色就不用默认"，
// 张三会只剩「商城管理员」——而那正是会让他在别的应用里丢掉基础能力的
// 那个 bug。两者相同的话这条测试分不出对错。
func TestEffectiveRolesUnionsDefault(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	e.mustRole(t, "普通用户", nil)
	e.mustRole(t, "商城管理员", nil)
	if _, err := e.apps.SetDefaultRole(ctx, e.app.ID, "普通用户"); err != nil {
		t.Fatalf("设置默认角色: %v", err)
	}

	userID := testsupport.NewUserID(t, e.pool)
	if err := e.svc.SetUserRoles(ctx, userID, []string{"商城管理员"}); err != nil {
		t.Fatalf("SetUserRoles: %v", err)
	}

	got, err := e.svc.EffectiveRoles(ctx, userID, e.app.ID)
	if err != nil {
		t.Fatalf("EffectiveRoles: %v", err)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "商城管理员" || got[1] != "普通用户" {
		t.Fatalf("有效角色 = %v, want [商城管理员 普通用户]——默认角色没有被并入", got)
	}
}

func TestEffectiveRolesFallsBackToDefaultWhenNoExplicit(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	e.mustRole(t, "普通用户", nil)
	if _, err := e.apps.SetDefaultRole(ctx, e.app.ID, "普通用户"); err != nil {
		t.Fatalf("设置默认角色: %v", err)
	}
	userID := testsupport.NewUserID(t, e.pool)

	got, err := e.svc.EffectiveRoles(ctx, userID, e.app.ID)
	if err != nil {
		t.Fatalf("EffectiveRoles: %v", err)
	}
	if len(got) != 1 || got[0] != "普通用户" {
		t.Fatalf("有效角色 = %v, want [普通用户]", got)
	}
}

// 显式分配的角色恰好就是默认角色时，不该出现两份。
func TestEffectiveRolesDeduplicates(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	e.mustRole(t, "普通用户", nil)
	if _, err := e.apps.SetDefaultRole(ctx, e.app.ID, "普通用户"); err != nil {
		t.Fatalf("设置默认角色: %v", err)
	}
	userID := testsupport.NewUserID(t, e.pool)
	if err := e.svc.SetUserRoles(ctx, userID, []string{"普通用户"}); err != nil {
		t.Fatalf("SetUserRoles: %v", err)
	}

	got, err := e.svc.EffectiveRoles(ctx, userID, e.app.ID)
	if err != nil {
		t.Fatalf("EffectiveRoles: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("有效角色 = %v，应当去重", got)
	}
}

// ---------------------------------------------------------------------------
// 删角色的连带清理
// ---------------------------------------------------------------------------

// 【辨别力】删角色必须从所有用户的 roles 数组里摘掉它。
//
// user_role.roles 是 text[]，**没有外键**，数据库不会替我们级联。漏了这一步
// 的话没有任何报错——那些用户带着一个不存在的角色，在策略表里查不到、等同于
// 没有，直到有人建了一个同名角色，他们**突然获得那个角色的权限**。
//
// 断言必须去读 UserRoles 的实际内容，只断言"删角色没报错"是测不到的。
func TestDeleteRoleCleansUserRoles(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	admin := e.mustRole(t, "商城管理员", nil)
	e.mustRole(t, "普通用户", nil)
	userID := testsupport.NewUserID(t, e.pool)
	if err := e.svc.SetUserRoles(ctx, userID, []string{"商城管理员", "普通用户"}); err != nil {
		t.Fatalf("SetUserRoles: %v", err)
	}

	if err := e.svc.DeleteRole(ctx, admin.ID); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	got, err := e.svc.UserRoles(ctx, userID)
	if err != nil {
		t.Fatalf("UserRoles: %v", err)
	}
	if len(got) != 1 || got[0] != "普通用户" {
		t.Fatalf("删角色后用户的 roles = %v, want [普通用户]——残留了已删除的角色", got)
	}
}

// 删角色也要清掉把它当默认角色的应用，否则那些应用的用户会拿到一个不存在的角色。
func TestDeleteRoleClearsApplicationDefault(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	normal := e.mustRole(t, "普通用户", nil)
	if _, err := e.apps.SetDefaultRole(ctx, e.app.ID, "普通用户"); err != nil {
		t.Fatalf("设置默认角色: %v", err)
	}
	if err := e.svc.DeleteRole(ctx, normal.ID); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	userID := testsupport.NewUserID(t, e.pool)
	got, err := e.svc.EffectiveRoles(ctx, userID, e.app.ID)
	if err != nil {
		t.Fatalf("EffectiveRoles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("有效角色 = %v，被删角色仍作为默认角色残留", got)
	}
}

// 删权限点由数据库级联删掉授权关系。
func TestDeletePermissionCascadesGrants(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	role := e.mustRole(t, "普通用户", nil)
	perm := e.mustPerm(t, "GET:/orders/{id}")
	e.grant(t, role, perm, domain.EffectAllow)

	holders, err := e.svc.RolesHolding(ctx, perm.ID)
	if err != nil || len(holders) != 1 {
		t.Fatalf("删除前 RolesHolding = %v, err = %v", holders, err)
	}

	if err := e.svc.DeletePermission(ctx, perm.ID); err != nil {
		t.Fatalf("DeletePermission: %v", err)
	}
	pol, err := e.svc.CompilePolicy(ctx, e.app.ID)
	if err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}
	if len(pol.Roles) != 0 {
		t.Fatalf("删权限点后策略表非空: %+v——授权关系没有被级联删除", pol.Roles)
	}
}

// 角色继承成环必须在写入时被拦下，而不是等到编译策略时才炸。
func TestUpdateRoleRejectsCycle(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()

	a := e.mustRole(t, "A", nil)
	b := e.mustRole(t, "B", &a.ID)

	// 把 A 的父角色设成 B → A→B→A 成环
	_, err := e.svc.UpdateRole(ctx, a.ID, "A", &b.ID)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeRoleCycle {
		t.Fatalf("err = %v, want CodeRoleCycle", err)
	}
	// 自己当自己的父角色
	_, err = e.svc.UpdateRole(ctx, a.ID, "A", &a.ID)
	if !errors.As(err, &de) || de.Code != domain.CodeRoleCycle {
		t.Fatalf("自环 err = %v, want CodeRoleCycle", err)
	}
}

// 不许给用户分配不存在的角色——那等于给他一个会在有人建同名角色那天
// 突然生效的权限。
func TestSetUserRolesRejectsUnknownRole(t *testing.T) {
	e := newAuthzEnv(t)
	userID := testsupport.NewUserID(t, e.pool)

	err := e.svc.SetUserRoles(context.Background(), userID, []string{"不存在的角色"})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeRoleNotFound {
		t.Fatalf("err = %v, want CodeRoleNotFound", err)
	}
}
