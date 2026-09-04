package domain_test

import (
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

func policy(roles ...domain.RolePolicy) domain.AppPolicy {
	return domain.AppPolicy{Roles: roles}
}

// 判定规则的穷举：默认拒绝 + deny-override。
func TestAppPolicyAllow(t *testing.T) {
	const pt = "GET:/orders/{id}"

	tests := []struct {
		name  string
		pol   domain.AppPolicy
		roles []string
		want  bool
	}{
		{
			name: "没有任何角色 → 拒（默认拒绝）",
			pol:  policy(domain.RolePolicy{RoleKey: "普通用户", Allow: []string{pt}}),
			// 关键：策略里明明有 allow，但用户不持有那个角色。
			roles: nil,
			want:  false,
		},
		{
			name:  "持有的角色里没有这条权限 → 拒",
			pol:   policy(domain.RolePolicy{RoleKey: "普通用户", Allow: []string{"POST:/orders"}}),
			roles: []string{"普通用户"},
			want:  false,
		},
		{
			name:  "持有的角色 allow 了 → 过",
			pol:   policy(domain.RolePolicy{RoleKey: "普通用户", Allow: []string{pt}}),
			roles: []string{"普通用户"},
			want:  true,
		},
		{
			name:  "持有一个不在策略表里的角色 → 拒",
			pol:   policy(domain.RolePolicy{RoleKey: "普通用户", Allow: []string{pt}}),
			roles: []string{"商城管理员"},
			want:  false,
		},
		{
			name: "多角色，其中一个 allow → 过",
			pol: policy(
				domain.RolePolicy{RoleKey: "普通用户", Allow: []string{"POST:/orders"}},
				domain.RolePolicy{RoleKey: "商城管理员", Allow: []string{pt}},
			),
			roles: []string{"普通用户", "商城管理员"},
			want:  true,
		},
		{
			name:  "同一角色同时 allow 与 deny → 拒（deny-override）",
			pol:   policy(domain.RolePolicy{RoleKey: "怪角色", Allow: []string{pt}, Deny: []string{pt}}),
			roles: []string{"怪角色"},
			want:  false,
		},
		{
			name: "【辨别力】allow 的角色在前、deny 的角色在后 → 拒",
			// 一个"找到 allow 就提前 return true"的实现会在这里放行——
			// 它永远看不到后面那个 deny。deny-override 要求把持有的角色全看完。
			pol: policy(
				domain.RolePolicy{RoleKey: "普通用户", Allow: []string{pt}},
				domain.RolePolicy{RoleKey: "黑名单", Deny: []string{pt}},
			),
			roles: []string{"普通用户", "黑名单"},
			want:  false,
		},
		{
			name: "deny 的角色在前、allow 的角色在后 → 拒",
			pol: policy(
				domain.RolePolicy{RoleKey: "黑名单", Deny: []string{pt}},
				domain.RolePolicy{RoleKey: "普通用户", Allow: []string{pt}},
			),
			roles: []string{"普通用户", "黑名单"},
			want:  false,
		},
		{
			name: "【辨别力】deny 只对它自己那条权限点生效，不波及别的",
			// 一个把 deny 当成"这个角色什么都不许"的实现会在这里拒绝。
			pol: policy(
				domain.RolePolicy{RoleKey: "受限用户", Allow: []string{pt}, Deny: []string{"POST:/orders"}},
			),
			roles: []string{"受限用户"},
			want:  true,
		},
		{
			name: "【辨别力】别人持有的 deny 不影响我",
			// 策略表里存在一条 deny，但当前用户并不持有那个角色。
			// 一个不检查 held 就扫全表 deny 的实现会在这里误拒。
			pol: policy(
				domain.RolePolicy{RoleKey: "普通用户", Allow: []string{pt}},
				domain.RolePolicy{RoleKey: "黑名单", Deny: []string{pt}},
			),
			roles: []string{"普通用户"},
			want:  true,
		},
		{
			name:  "空策略表 → 拒",
			pol:   policy(),
			roles: []string{"普通用户"},
			want:  false,
		},
		{
			name: "重复持有同一角色不影响结果",
			pol:  policy(domain.RolePolicy{RoleKey: "普通用户", Allow: []string{pt}}),
			// 全局角色 ∪ 应用默认角色时可能出现重复（显式分配的正好就是默认角色）。
			roles: []string{"普通用户", "普通用户"},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pol.Allow(tt.roles, pt); got != tt.want {
				t.Fatalf("Allow(%v, %q) = %v, want %v", tt.roles, pt, got, tt.want)
			}
		})
	}
}

func TestPermissionStatus(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	const staleAfter = 30 * time.Minute

	tests := []struct {
		name string
		p    domain.Permission
		want domain.PermissionStatus
	}{
		{
			name: "手动加的，永远是手动",
			// 关键：LastSeenAt 为 0（从没被上报过）也不算过渡中——
			// 手动权限点本来就不在业务方的路由清单里。
			p:    domain.Permission{Source: domain.PermissionSourceManual},
			want: domain.PermissionStatusManual,
		},
		{
			name: "刚被上报过 → 正常",
			p: domain.Permission{
				Source:     domain.PermissionSourceApp,
				LastSeenAt: now.Add(-time.Minute).UnixMilli(),
			},
			want: domain.PermissionStatusNormal,
		},
		{
			name: "【辨别力】阈值内还没被报过 → 仍然正常",
			// 滚动发布时旧实例报旧清单、新实例报新清单，交替上报。
			// 阈值就是为了容忍这个窗口——差一点点就判过渡中的实现，
			// 会让发布期间的权限点反复横跳。
			p: domain.Permission{
				Source:     domain.PermissionSourceApp,
				LastSeenAt: now.Add(-29 * time.Minute).UnixMilli(),
			},
			want: domain.PermissionStatusNormal,
		},
		{
			name: "【辨别力】恰好等于阈值 → 仍然正常",
			// 边界值。29/31 分钟这种取样跳过了边界，> 与 >= 两种实现
			// 都会绿——变异测试发现了这个缺口后补的这条。
			p: domain.Permission{
				Source:     domain.PermissionSourceApp,
				LastSeenAt: now.Add(-staleAfter).UnixMilli(),
			},
			want: domain.PermissionStatusNormal,
		},
		{
			name: "超过阈值 → 过渡中",
			p: domain.Permission{
				Source:     domain.PermissionSourceApp,
				LastSeenAt: now.Add(-31 * time.Minute).UnixMilli(),
			},
			want: domain.PermissionStatusStale,
		},
		{
			name: "app 来源但从未被上报过 → 过渡中",
			p:    domain.Permission{Source: domain.PermissionSourceApp, LastSeenAt: 0},
			want: domain.PermissionStatusStale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.Status(now, staleAfter); got != tt.want {
				t.Fatalf("Status = %q, want %q", got, tt.want)
			}
		})
	}
}
