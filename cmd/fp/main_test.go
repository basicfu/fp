package main

import (
	"testing"

	"github.com/basicfu/fp/internal/config"
)

func TestResolveBootstrapAdminDefaultsWhenEmpty(t *testing.T) {
	got := resolveBootstrapAdmin(config.BootstrapAdmin{})
	if got.User != "admin" || got.Password != "admin" {
		t.Fatalf("got = %+v，期望两项都空时落到内置默认 admin/admin", got)
	}
}

func TestResolveBootstrapAdminKeepsExplicitValue(t *testing.T) {
	want := config.BootstrapAdmin{User: "root", Password: "s3cret"}
	got := resolveBootstrapAdmin(want)
	if got != want {
		t.Fatalf("got = %+v，期望原样返回 %+v", got, want)
	}
}

// 只填了一项也不该被当成"两项都空"去覆盖——沿用 EnsureBootstrap 自己
// "任一为空就跳过"的判断，这里只负责"两项都空"这一种情况的默认值。
func TestResolveBootstrapAdminKeepsPartiallyFilledValue(t *testing.T) {
	want := config.BootstrapAdmin{User: "root"}
	got := resolveBootstrapAdmin(want)
	if got != want {
		t.Fatalf("got = %+v，期望原样返回 %+v", got, want)
	}
}
