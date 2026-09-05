package domain_test

import (
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

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
