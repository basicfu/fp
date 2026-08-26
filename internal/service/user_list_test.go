package service_test

import (
	"context"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

func seedUsers(t *testing.T, svc *service.UserService) {
	t.Helper()
	ctx := context.Background()

	u1, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000", Nickname: "阿里斯",
	})
	if err != nil {
		t.Fatalf("u1: %v", err)
	}
	if _, err := svc.AttachIdentity(ctx, u1.ID, service.EnsureIdentityInput{
		Type: domain.IdentityTypeUsername, Subject: "alice",
	}); err != nil {
		t.Fatalf("u1 username: %v", err)
	}

	if _, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13900139000", Nickname: "鲍勃",
	}); err != nil {
		t.Fatalf("u2: %v", err)
	}
	u3, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13700137000", Nickname: "查理",
	})
	if err != nil {
		t.Fatalf("u3: %v", err)
	}
	if _, err := svc.SetStatus(ctx, u3.ID, domain.UserStatusFrozen); err != nil {
		t.Fatalf("冻结 u3: %v", err)
	}
}

func TestUserListReturnsAllWithIdentities(t *testing.T) {
	svc := newUserService(t)
	seedUsers(t, svc)

	items, total, err := svc.List(context.Background(), service.UserListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(items) != 3 {
		t.Fatalf("len = %d, want 3", len(items))
	}

	var found bool
	for _, it := range items {
		if it.User.Nickname == "阿里斯" {
			found = true
			if len(it.Identities) != 2 {
				t.Fatalf("阿里斯 的 identity 数 = %d, want 2", len(it.Identities))
			}
		}
	}
	if !found {
		t.Fatal("未找到 阿里斯")
	}
}

func TestUserListSearchesAcrossIdentitiesAndNickname(t *testing.T) {
	svc := newUserService(t)
	seedUsers(t, svc)
	ctx := context.Background()

	tests := []struct {
		keyword string
		want    int
	}{
		{"13800138000", 1}, // 精确手机号
		{"1380", 1},        // 手机号前缀
		{"alice", 1},       // 用户名
		{"鲍勃", 1},          // 昵称
		{"138", 1},
		{"137", 1},
		{"nonexistent", 0},
	}
	for _, tt := range tests {
		items, total, err := svc.List(ctx, service.UserListQuery{Keyword: tt.keyword, Limit: 10})
		if err != nil {
			t.Fatalf("List(%q): %v", tt.keyword, err)
		}
		if total != tt.want || len(items) != tt.want {
			t.Errorf("List(%q) total=%d len=%d, want %d", tt.keyword, total, len(items), tt.want)
		}
	}
}

// 同一个用户有多条 identity 命中关键词时，结果里不能出现重复行。
func TestUserListDeduplicatesMultiIdentityMatches(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()

	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	if _, err := svc.AttachIdentity(ctx, u.ID, service.EnsureIdentityInput{
		Type: domain.IdentityTypeUsername, Subject: "138user",
	}); err != nil {
		t.Fatalf("加 identity: %v", err)
	}

	// "138" 同时命中手机号与用户名
	items, total, err := svc.List(ctx, service.UserListQuery{Keyword: "138", Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len=%d, want 1（不应重复）", total, len(items))
	}
}

func TestUserListFiltersByStatus(t *testing.T) {
	svc := newUserService(t)
	seedUsers(t, svc)
	ctx := context.Background()

	items, total, err := svc.List(ctx, service.UserListQuery{Status: domain.UserStatusFrozen, Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len=%d, want 1", total, len(items))
	}
	if items[0].User.Nickname != "查理" {
		t.Fatalf("nickname = %q", items[0].User.Nickname)
	}
}

func TestUserListPaginates(t *testing.T) {
	svc := newUserService(t)
	seedUsers(t, svc)
	ctx := context.Background()

	first, total, err := svc.List(ctx, service.UserListQuery{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3（total 应为过滤后的总数，不受分页影响）", total)
	}
	if len(first) != 2 {
		t.Fatalf("len = %d, want 2", len(first))
	}

	second, _, err := svc.List(ctx, service.UserListQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("第二页 len = %d, want 1", len(second))
	}
	if second[0].User.ID == first[0].User.ID {
		t.Fatal("两页出现了同一个用户")
	}
}

func TestUserListEmptyResultIsNotNil(t *testing.T) {
	svc := newUserService(t)
	items, total, err := svc.List(context.Background(), service.UserListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if items == nil {
		t.Fatal("应返回空切片而非 nil，否则 JSON 序列化成 null")
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
}
