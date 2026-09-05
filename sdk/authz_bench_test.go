package fpsdk

import (
	"fmt"
	"testing"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// benchPolicy 造一份接近真实规模的策略：nRoles 个角色，每个 permsPerRole 条权限。
func benchPolicy(nRoles, permsPerRole int) *fpv1.AppPolicy {
	p := &fpv1.AppPolicy{Version: 1}
	for i := 0; i < nRoles; i++ {
		r := &fpv1.RolePolicy{RoleKey: fmt.Sprintf("role%d", i)}
		for j := 0; j < permsPerRole; j++ {
			r.Allow = append(r.Allow, fmt.Sprintf("GET:/res%d/{id}", j))
		}
		p.Roles = append(p.Roles, r)
	}
	return p
}

// 判定的实际开销。命中的是最后一条权限——最坏情况，因为实现是线性扫描。
func BenchmarkAllowRoles(b *testing.B) {
	cases := []struct{ roles, perms, held int }{
		{5, 50, 1},   // 小项目
		{20, 200, 3}, // 中等
		{50, 500, 5}, // 大项目
	}
	for _, c := range cases {
		name := fmt.Sprintf("策略%d角色x%d权限_用户持有%d个", c.roles, c.perms, c.held)
		b.Run(name, func(b *testing.B) {
			a := &Authz{}
			a.setPolicy(benchPolicy(c.roles, c.perms))
			held := make([]string, c.held)
			for i := range held {
				held[i] = fmt.Sprintf("role%d", i)
			}
			target := fmt.Sprintf("/res%d/{id}", c.perms-1) // 最后一条

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ok, err := a.AllowRoles(held, "GET", target)
				if err != nil || !ok {
					b.Fatalf("ok=%v err=%v", ok, err)
				}
			}
		})
	}
}
