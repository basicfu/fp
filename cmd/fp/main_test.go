package main

import (
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/config"
	"github.com/basicfu/fp/internal/domain"
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

// TestSeedDefaultsYAMLSeedsWhenNoVersion 钉住系统配置表还没有任何版本时
// （Seq=0，全新库必然如此）该把默认值写成第一版——「系统配置」页面第一次
// 打开就有真实内容可以改，不是空白 + placeholder。
func TestSeedDefaultsYAMLSeedsWhenNoVersion(t *testing.T) {
	cfg, err := config.Parse("", "dev")
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	cfg.BootstrapAdmin = resolveBootstrapAdmin(cfg.BootstrapAdmin)

	yamlText, ok, err := seedDefaultsYAML(domain.SystemConfig{Seq: 0}, cfg)
	if err != nil {
		t.Fatalf("seedDefaultsYAML() error = %v", err)
	}
	if !ok {
		t.Fatal("Seq=0 时应该要写种子版本，ok = false")
	}
	if !strings.Contains(yamlText, "admin") {
		t.Errorf("种子 YAML 里应当包含兜底后的 bootstrap_admin，得到 %q", yamlText)
	}

	// 种子文本本身必须是能被 Parse 读回来的合法 YAML——写进数据库之前
	// 先在这里验一遍，比等真的存库失败更早暴露问题。
	got, err := config.Parse(yamlText, "dev")
	if err != nil {
		t.Fatalf("种子 YAML 不能被 Parse 读回：%v，yaml = %s", err, yamlText)
	}
	if got.BootstrapAdmin != cfg.BootstrapAdmin {
		t.Errorf("种子 YAML 读回后 BootstrapAdmin = %+v，期望 %+v", got.BootstrapAdmin, cfg.BootstrapAdmin)
	}
}

// TestSeedDefaultsYAMLSkipsWhenVersionExists 钉住已经有人存过版本时不能
// 覆盖——管理员可能已经改过内容，不能被启动流程悄悄冲掉。
func TestSeedDefaultsYAMLSkipsWhenVersionExists(t *testing.T) {
	cfg, err := config.Parse("", "dev")
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}

	yamlText, ok, err := seedDefaultsYAML(domain.SystemConfig{Seq: 1, Value: "log:\n  level: debug\n"}, cfg)
	if err != nil {
		t.Fatalf("seedDefaultsYAML() error = %v", err)
	}
	if ok {
		t.Fatal("Seq!=0 时不该要求写种子版本，ok = true")
	}
	if yamlText != "" {
		t.Errorf("ok=false 时 yamlText 应当是空字符串，得到 %q", yamlText)
	}
}
