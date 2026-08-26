package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
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

// TestUserListPaginationSurvivesCreatedAtTies 直接构造"多行 created_at 完全相同"的场景——
// 用一条 INSERT 语句批量插入若干行，它们共享同一个 now() 快照，模拟批量导入/播种。
// 只按 created_at 排序不是全序：打平时 LIMIT/OFFSET 翻页可能让同一行重复出现在两页，
// 也可能一页都不出现。加了 u.id DESC 之后排序变成全序（uuidv7 唯一），必须稳定。
func TestUserListPaginationSurvivesCreatedAtTies(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	svc := service.NewUserService(pool)
	ctx := context.Background()

	const total = 5
	rows, err := pool.Query(ctx, `
		INSERT INTO app_user (created_at)
		SELECT now() FROM generate_series(1, 5)
		RETURNING id`)
	if err != nil {
		t.Fatalf("批量插入: %v", err)
	}
	seeded := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatalf("扫描 id: %v", err)
		}
		seeded[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历插入结果: %v", err)
	}
	if len(seeded) != total {
		t.Fatalf("播种行数 = %d, want %d", len(seeded), total)
	}

	seen := map[uuid.UUID]int{}
	for offset := 0; offset < total; offset += 2 {
		items, _, err := svc.List(ctx, service.UserListQuery{Limit: 2, Offset: offset})
		if err != nil {
			t.Fatalf("List(offset=%d): %v", offset, err)
		}
		for _, it := range items {
			seen[it.User.ID]++
		}
	}

	for id := range seeded {
		switch seen[id] {
		case 0:
			t.Errorf("用户 %s 在 created_at 打平的情况下被分页跳过了", id)
		case 1:
			// 正常
		default:
			t.Errorf("用户 %s 在 created_at 打平的情况下被分页重复返回了 %d 次", id, seen[id])
		}
	}
}
