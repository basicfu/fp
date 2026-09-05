package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	fpsdk "github.com/basicfu/fp/sdk"

	"github.com/basicfu/fp/internal/domain"
)

// 端到端穿透：改一个人的角色，SDK 侧的 Allow 结果要跟着变。
//
// 这条对应第三阶段发现的那类缺陷——**只测服务端返回值是不够的**。那次的
// 问题正是服务端有信息、SDK 把它丢了，而两边各自的测试都绿。所以这里必须
// 从 SDK 的 Allow 出口断言，中间跨越 service → gRPC → SDK 三层。
func TestAuthzEndToEnd(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()

	// 1. 建两个角色与两个权限点，配好授权
	normal, err := e.authz.CreateRole(ctx, "普通用户", "普通用户", nil)
	if err != nil {
		t.Fatalf("建角色: %v", err)
	}
	admin, err := e.authz.CreateRole(ctx, "商城管理员", "商城管理员", nil)
	if err != nil {
		t.Fatalf("建角色: %v", err)
	}
	view, err := e.authz.CreatePermission(ctx, e.app.ID, "GET:/orders/{id}", "查看订单", domain.PermissionKindAPI)
	if err != nil {
		t.Fatalf("建权限点: %v", err)
	}
	del, err := e.authz.CreatePermission(ctx, e.app.ID, "DELETE:/orders/{id}", "删除订单", domain.PermissionKindAPI)
	if err != nil {
		t.Fatalf("建权限点: %v", err)
	}
	if err := e.authz.SetRolePermission(ctx, normal.ID, view.ID, domain.EffectAllow); err != nil {
		t.Fatalf("授权: %v", err)
	}
	if err := e.authz.SetRolePermission(ctx, admin.ID, del.ID, domain.EffectAllow); err != nil {
		t.Fatalf("授权: %v", err)
	}
	// 应用默认角色 = 普通用户，新用户零配置就能看订单
	if _, err := e.apps.SetDefaultRole(ctx, e.app.ID, "普通用户"); err != nil {
		t.Fatalf("设默认角色: %v", err)
	}

	// 2. 登录并校验一次，拿到带角色的身份
	phone := randomPhase2Phone()
	token, userID, _ := e.loginWithPhone(t, phone)
	identity, err := e.sdk.Auth().Validate(ctx, token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(identity.Roles) != 1 || identity.Roles[0] != "普通用户" {
		t.Fatalf("身份里的角色 = %v, want [普通用户]——角色没有随校验回来", identity.Roles)
	}

	// 3. SDK 侧判定：能看、不能删
	az := e.sdk.Authz()
	waitPolicy(t, az)

	if ok, err := az.AllowRoles(identity.Roles, "GET", "/orders/{id}"); err != nil || !ok {
		t.Fatalf("普通用户应当能看订单: ok=%v err=%v", ok, err)
	}
	if ok, err := az.AllowRoles(identity.Roles, "DELETE", "/orders/{id}"); err != nil || ok {
		t.Fatalf("普通用户不该能删订单: ok=%v err=%v", ok, err)
	}

	// 4. 给他加管理员角色。角色刻在会话里，所以要重新登录才带上新角色
	//（在线用户靠 UserRoleChanged 推送让 SDK 丢缓存重新校验，那条路径
	// 由 SDK 的事件处理负责，这里验的是角色本身的解析与穿透）。
	if err := e.authz.SetUserRoles(ctx, userID, []string{"商城管理员"}); err != nil {
		t.Fatalf("SetUserRoles: %v", err)
	}
	token2, sameUser, _ := e.loginWithPhone(t, phone)
	if sameUser != userID {
		t.Fatal("第二次登录拿到的不是同一个用户，测试构造失效")
	}
	after, err := e.sdk.Auth().Validate(ctx, token2)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// 有效角色 = 全局角色 ∪ 默认角色，所以两个都在——
	// 这条同时守住"加了管理员不会把普通用户的能力弄没"。
	if len(after.Roles) != 2 {
		t.Fatalf("加角色后 = %v, want [商城管理员 普通用户]——默认角色没有被并入", after.Roles)
	}

	// 5. 【关键断言】判定结果真的变了
	if ok, err := az.AllowRoles(after.Roles, "DELETE", "/orders/{id}"); err != nil || !ok {
		t.Fatalf("加了商城管理员之后应当能删订单: ok=%v err=%v", ok, err)
	}
	if ok, err := az.AllowRoles(after.Roles, "GET", "/orders/{id}"); err != nil || !ok {
		t.Fatalf("加管理员角色后仍应保留普通用户的能力: ok=%v err=%v", ok, err)
	}
}

// 权限点上报走完 SDK → gRPC → 库这条链路。
func TestReportPermissionsEndToEnd(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()

	err := e.sdk.ReportPermissions(ctx, []fpsdk.PermissionPoint{
		{Key: fpsdk.PermissionKey("GET", "/orders/{id}")},
		{Key: fpsdk.PermissionKey("POST", "/orders"), Name: "创建订单"},
	})
	if err != nil {
		t.Fatalf("ReportPermissions: %v", err)
	}

	items, err := e.authz.ListPermissions(ctx, e.app.ID)
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("上报后权限点数 = %d, want 2", len(items))
	}
	for _, it := range items {
		if it.Permission.Source != domain.PermissionSourceApp {
			t.Fatalf("%s 的 source = %q, want app", it.Permission.Key, it.Permission.Source)
		}
		if it.Status != domain.PermissionStatusNormal {
			t.Fatalf("%s 的状态 = %q, want normal", it.Permission.Key, it.Status)
		}
	}
}

// 【辨别力】没有本地策略时 Allow 必须返回**错误**而不是 false。
//
// 调用方要分得清"没权限"（403，业务照常）和"没能判定"（可用性事件，该按
// 自己的降级策略处理）。一个统一返回 false 的实现会让 fp 刚启动那几秒里
// 所有请求看起来像"权限配置错了"，排障方向直接跑偏。
func TestAllowReportsUnavailableWhenNoPolicy(t *testing.T) {
	var az fpsdk.Authz // 零值：还没拉到任何策略

	ok, err := az.AllowRoles([]string{"普通用户"}, "GET", "/orders/{id}")
	if ok {
		t.Fatal("没有策略时放行了")
	}
	if !errors.Is(err, fpsdk.ErrPolicyUnavailable) {
		t.Fatalf("err = %v, want ErrPolicyUnavailable——"+
			"没能判定被当成了明确拒绝", err)
	}
}

// waitPolicy 等 SDK 拉到策略。策略是在 Watch 流 ready 之后异步拉的。
func waitPolicy(t *testing.T, az *fpsdk.Authz) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if az.PolicyVersion() != 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 版本号可能恰好为 0（策略里一条角色都没有时），再确认一次判定能跑通。
	if _, err := az.AllowRoles(nil, "GET", "/x"); err == nil {
		return
	}
	t.Fatal("等待 SDK 拉到策略超时")
}
