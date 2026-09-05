package authzcore_test

import (
	"testing"

	"github.com/basicfu/fp/sdk/authzcore"
)

func role(key string, allow, deny []string) authzcore.RolePolicy {
	return authzcore.RolePolicy{RoleKey: key, Allow: allow, Deny: deny}
}

// 判定规则的穷举：默认拒绝 + deny-override。
//
// 这是整个授权模块唯一有真实逻辑的地方，且服务端与 SDK 共用这一份实现，
// 所以测试也只需要一份。
func TestAllow(t *testing.T) {
	const pt = "GET:/orders/{id}"

	tests := []struct {
		name  string
		pol   []authzcore.RolePolicy
		roles []string
		want  bool
	}{
		{
			name: "没有任何角色 → 拒（默认拒绝）",
			// 策略里明明有 allow，但用户不持有那个角色。
			pol:   []authzcore.RolePolicy{role("普通用户", []string{pt}, nil)},
			roles: nil,
			want:  false,
		},
		{
			name:  "持有的角色里没有这条权限 → 拒",
			pol:   []authzcore.RolePolicy{role("普通用户", []string{"POST:/orders"}, nil)},
			roles: []string{"普通用户"},
			want:  false,
		},
		{
			name:  "持有的角色 allow 了 → 过",
			pol:   []authzcore.RolePolicy{role("普通用户", []string{pt}, nil)},
			roles: []string{"普通用户"},
			want:  true,
		},
		{
			name:  "持有一个不在策略表里的角色 → 拒",
			pol:   []authzcore.RolePolicy{role("普通用户", []string{pt}, nil)},
			roles: []string{"商城管理员"},
			want:  false,
		},
		{
			name: "多角色，其中一个 allow → 过",
			pol: []authzcore.RolePolicy{
				role("普通用户", []string{"POST:/orders"}, nil),
				role("商城管理员", []string{pt}, nil),
			},
			roles: []string{"普通用户", "商城管理员"},
			want:  true,
		},
		{
			name:  "同一角色同时 allow 与 deny → 拒",
			pol:   []authzcore.RolePolicy{role("怪角色", []string{pt}, []string{pt})},
			roles: []string{"怪角色"},
			want:  false,
		},
		{
			name: "【辨别力】allow 的角色在前、deny 的角色在后 → 拒",
			// 一个"找到 allow 就提前 return true"的实现会在这里放行——它永远
			// 看不到后面那个 deny。deny-override 要求把持有的角色全看完。
			pol: []authzcore.RolePolicy{
				role("普通用户", []string{pt}, nil),
				role("黑名单", nil, []string{pt}),
			},
			roles: []string{"普通用户", "黑名单"},
			want:  false,
		},
		{
			name: "deny 的角色在前、allow 的角色在后 → 拒",
			pol: []authzcore.RolePolicy{
				role("黑名单", nil, []string{pt}),
				role("普通用户", []string{pt}, nil),
			},
			roles: []string{"普通用户", "黑名单"},
			want:  false,
		},
		{
			name: "【辨别力】deny 只对它自己那条权限点生效，不波及别的",
			// 一个把 deny 当成"这个角色什么都不许"的实现会在这里误拒。
			pol:   []authzcore.RolePolicy{role("受限用户", []string{pt}, []string{"POST:/orders"})},
			roles: []string{"受限用户"},
			want:  true,
		},
		{
			name: "【辨别力】别人持有的 deny 不影响我",
			// 策略表里存在一条 deny，但当前用户并不持有那个角色。
			// 一个不检查 held 就扫全表 deny 的实现会在这里误拒。
			pol: []authzcore.RolePolicy{
				role("普通用户", []string{pt}, nil),
				role("黑名单", nil, []string{pt}),
			},
			roles: []string{"普通用户"},
			want:  true,
		},
		{
			name:  "空策略表 → 拒",
			pol:   nil,
			roles: []string{"普通用户"},
			want:  false,
		},
		{
			name: "重复持有同一角色不影响结果",
			// 全局角色 ∪ 应用默认角色时可能出现重复（显式分配的正好是默认角色）。
			pol:   []authzcore.RolePolicy{role("普通用户", []string{pt}, nil)},
			roles: []string{"普通用户", "普通用户"},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authzcore.Allow(tt.pol, tt.roles, pt); got != tt.want {
				t.Fatalf("Allow(%v, %q) = %v, want %v", tt.roles, pt, got, tt.want)
			}
		})
	}
}

func TestPermissionKey(t *testing.T) {
	if got := authzcore.PermissionKey("GET", "/orders/{id}"); got != "GET:/orders/{id}" {
		t.Fatalf("PermissionKey = %q", got)
	}
}
