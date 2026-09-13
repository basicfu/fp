package fpsdk

import (
	"context"
	"testing"

	"github.com/basicfu/fp/sdk/authzcore"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

func TestAllowMergesGuest(t *testing.T) {
	a := &Authz{}
	a.setPolicy(&fpv1.AppPolicy{Version: 1, Roles: []*fpv1.RolePolicy{
		{RoleKey: authzcore.GuestRoleKey, Allow: []string{"GET:/pub"}},
		{RoleKey: "partner", Allow: []string{"GET:/orders/{id}"}},
	}})
	cases := []struct {
		name    string
		id      *Identity
		pattern string
		want    bool
	}{
		{"匿名请求能调 GUEST 的接口", &Identity{}, "/pub", true},
		{"匿名请求调不了别的接口", &Identity{}, "/orders/{id}", false},
		// 【辨别力】会话里的角色不含 GUEST，登录用户照样拥有它。
		{"登录用户并入 GUEST", &Identity{UserID: "u1", Roles: []string{"普通用户"}}, "/pub", true},
		// 【辨别力】访问密钥不拥有 GUEST。
		{"没绑角色的访问密钥调不了 GUEST 的接口", &Identity{AccessKeyID: "FPAK1"}, "/pub", false},
		{"访问密钥按绑定的角色放行", &Identity{AccessKeyID: "FPAK1", Roles: []string{"partner"}}, "/orders/{id}", true},
	}
	for _, c := range cases {
		got, err := a.Allow(WithIdentity(context.Background(), c.id), "GET", c.pattern)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v err %v, want %v", c.name, got, err, c.want)
		}
	}
}

func TestIdentityKinds(t *testing.T) {
	if !(&Identity{}).IsAnonymous() || !(&Identity{GuestID: "g"}).IsAnonymous() {
		t.Fatal("没有用户也不是访问密钥的身份应算匿名（访客也算）")
	}
	if (&Identity{UserID: "u"}).IsAnonymous() || (&Identity{AccessKeyID: "k"}).IsAnonymous() {
		t.Fatal("登录用户与访问密钥不算匿名")
	}
	if !(&Identity{AccessKeyID: "k"}).IsAccessKey() || (&Identity{UserID: "u"}).IsAccessKey() {
		t.Fatal("IsAccessKey 判断不对")
	}
}
