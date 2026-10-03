package config

import (
	"strings"
	"testing"
)

// TestParseReadsEveryField 逐字段断言，是 yaml tag 的护栏：漏写 tag 的
// 多词字段会被 yaml.v3 按"字段名整个小写"的默认规则映射错位，
// KnownFields(true) 会把它报成"未知键"，只有逐字段断言才抓得住。
//
// env 不在这份 YAML 里——它现在只能来自 Parse 的 env 参数（对应
// FP_ENV 环境变量），YAML 里再写 env: 会被当成未知键拒绝，见
// TestParseRejectsEnvInYAML。
func TestParseReadsEveryField(t *testing.T) {
	cfg, err := Parse(`
log:
  level: debug
http:
  addr: ":18080"
grpc:
  addr: ":19090"
bootstrap_admin:
  user: root
  password: s3cret
`, "prod")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"env", cfg.Env, "prod"},
		{"log.level", cfg.Log.Level, "debug"},
		{"http.addr", cfg.HTTP.Addr, ":18080"},
		{"grpc.addr", cfg.GRPC.Addr, ":19090"},
		{"bootstrap_admin.user", cfg.BootstrapAdmin.User, "root"},
		{"bootstrap_admin.password", cfg.BootstrapAdmin.Password, "s3cret"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestParseRejectsEnvInYAML 钉住 env 这个键彻底从系统配置 YAML 的字段树
// 里挪走了——写了就当未知键报错，逼着改用 FP_ENV 环境变量，不留一个
// "两处都能改、谁说了算说不清楚"的口子。
func TestParseRejectsEnvInYAML(t *testing.T) {
	_, err := Parse("env: prod\n", "dev")
	if err == nil {
		t.Fatal("YAML 里的 env: 键应该被当成未知键拒绝")
	}
	if !strings.Contains(err.Error(), "env") {
		t.Errorf("错误信息 %q 里应当出现 env", err)
	}
}

// TestParseEmptyTextUsesAllDefaults 钉住"系统配置表还没有任何版本"这个
// 首次启动的正常状态：空文本不是错误，其余字段全部用零值默认；env 由
// 参数决定，不受这条"默认值"规则影响。
func TestParseEmptyTextUsesAllDefaults(t *testing.T) {
	cfg, err := Parse("", "dev")
	if err != nil {
		t.Fatalf(`Parse("") error = %v`, err)
	}
	if cfg.Env != "dev" {
		t.Errorf("Env = %q, want dev", cfg.Env)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want info", cfg.Log.Level)
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("HTTP.Addr = %q, want :8080", cfg.HTTP.Addr)
	}
	if cfg.GRPC.Addr != ":9090" {
		t.Errorf("GRPC.Addr = %q, want :9090", cfg.GRPC.Addr)
	}
}

// TestParseDefaultsEmptyEnvToDEV 是 Parse 自己的防御性兜底：正常情况下
// 调用方（cmd/fp/main.go）已经用 config.EnvOr("FP_ENV", "DEV") 保证 env
// 参数不会是空串，这里只是确认 Parse 自己也不依赖那个保证。
func TestParseDefaultsEmptyEnvToDEV(t *testing.T) {
	cfg, err := Parse("", "")
	if err != nil {
		t.Fatalf(`Parse("", "") error = %v`, err)
	}
	if cfg.Env != "DEV" {
		t.Errorf("Env = %q, want DEV", cfg.Env)
	}
}

// TestParseRejectsUnknownField 是从文件迁到数据库依然保留的能力：拼错的
// 键当场报错，不静默回落到默认值。
func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse("log:\n  lvel: debug\n", "dev")
	if err == nil {
		t.Fatal("拼错的键必须报错，不能静默用默认值")
	}
	if !strings.Contains(err.Error(), "lvel") {
		t.Errorf("错误信息 %q 里应当出现拼错的那个键名 lvel", err)
	}
}

// TestIsProd 钉住大小写不敏感与默认值。IsProd 决定管理端 cookie 带不带
// Secure，是个真正有安全后果的判定。
func TestIsProd(t *testing.T) {
	for _, tt := range []struct {
		env  string
		want bool
	}{
		{"prod", true},
		{"PROD", true},
		{"Prod", true},
		{"dev", false},
		{"", false},
		{"production", false}, // 只认 prod，不做前缀匹配
	} {
		if got := (&Config{Env: tt.env}).IsProd(); got != tt.want {
			t.Errorf("Env=%q IsProd() = %v, want %v", tt.env, got, tt.want)
		}
	}
}

// TestToYAMLRoundTrips 钉住 ToYAML 产出的文本能被 Parse 读回来，值不丢——
// cmd/fp/main.go 靠这一点把首次启动解析出来的默认值写回系统配置表。
func TestToYAMLRoundTrips(t *testing.T) {
	cfg, err := Parse(`
log:
  level: debug
http:
  addr: ":18080"
grpc:
  addr: ":19090"
bootstrap_admin:
  user: admin
  password: admin
`, "prod")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	yamlText, err := ToYAML(cfg)
	if err != nil {
		t.Fatalf("ToYAML() error = %v", err)
	}

	got, err := Parse(yamlText, "prod")
	if err != nil {
		t.Fatalf("Parse(ToYAML()) error = %v, yaml = %s", err, yamlText)
	}
	if got.Log.Level != cfg.Log.Level || got.HTTP.Addr != cfg.HTTP.Addr || got.GRPC.Addr != cfg.GRPC.Addr {
		t.Errorf("往返后 log/http/grpc = %+v/%+v/%+v，原始 = %+v/%+v/%+v",
			got.Log, got.HTTP, got.GRPC, cfg.Log, cfg.HTTP, cfg.GRPC)
	}
	if got.BootstrapAdmin != cfg.BootstrapAdmin {
		t.Errorf("往返后 BootstrapAdmin = %+v，期望 %+v", got.BootstrapAdmin, cfg.BootstrapAdmin)
	}
}

// TestToYAMLOmitsEnv 钉住 env 不出现在 ToYAML 的输出里——它只能来自
// FP_ENV，写进系统配置 YAML 会误导人以为改它有用（而且 Parse 现在会把
// YAML 里的 env: 当未知键拒绝，见 TestParseRejectsEnvInYAML）。
func TestToYAMLOmitsEnv(t *testing.T) {
	cfg, err := Parse("", "prod")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	yamlText, err := ToYAML(cfg)
	if err != nil {
		t.Fatalf("ToYAML() error = %v", err)
	}
	if strings.Contains(yamlText, "env:") {
		t.Errorf("输出里不该出现 env:，得到 %q", yamlText)
	}
}

// 存量库里的系统配置第一版带着 sms: 段，删掉 SMS 配置后它们仍必须能被解析。
func TestParseAcceptsLegacySMSBlock(t *testing.T) {
	yamlText := "log:\n  level: info\nsms:\n  aliyun:\n    access_key_id: \"\"\n    sign_name: x\n"
	if _, err := Parse(yamlText, "dev"); err != nil {
		t.Fatalf("Parse 应接受历史版本里的 sms 段: %v", err)
	}
}

func TestToYAMLNoLongerEmitsSMS(t *testing.T) {
	cfg, err := Parse("", "dev")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := ToYAML(cfg)
	if err != nil {
		t.Fatalf("ToYAML: %v", err)
	}
	if strings.Contains(out, "sms") {
		t.Fatalf("新生成的系统配置不该再带 sms 段:\n%s", out)
	}
}
