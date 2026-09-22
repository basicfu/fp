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
sms:
  aliyun:
    access_key_id: ak
    access_key_secret: sk
    sign_name: 测试签名
    template_login_code: SMS_0001
    endpoint: dysmsapi.example.com
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
		{"sms.aliyun.access_key_id", cfg.SMS.Aliyun.AccessKeyID, "ak"},
		{"sms.aliyun.access_key_secret", cfg.SMS.Aliyun.AccessKeySecret, "sk"},
		{"sms.aliyun.sign_name", cfg.SMS.Aliyun.SignName, "测试签名"},
		{"sms.aliyun.template_login_code", cfg.SMS.Aliyun.TemplateLoginCode, "SMS_0001"},
		{"sms.aliyun.endpoint", cfg.SMS.Aliyun.Endpoint, "dysmsapi.example.com"},
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

// TestParseAllowsMissingAliyunInAnyEnv 钉住阿里云短信凭据在任何环境
// （包括 prod）都允许留空——不该由 fp 自己的系统配置在启动时强制卡它，
// 阿里云只是短信这一种通知渠道的其中一个供应商，后续会挪进统一的通知
// 中心配置。真正装配假供应商并打 WARN 的逻辑在 cmd/fp/main.go 里，这里
// 只钉住 Parse 不替它做这个决定。
func TestParseAllowsMissingAliyunInAnyEnv(t *testing.T) {
	for _, env := range []string{"dev", "prod", "PROD"} {
		cfg, err := Parse("", env)
		if err != nil {
			t.Fatalf("Parse(env=%q) error = %v，任何环境阿里云凭据都允许留空", env, err)
		}
		if cfg.SMS.Aliyun.AccessKeyID != "" {
			t.Errorf("env=%q: SMS.Aliyun.AccessKeyID = %q, want empty", env, cfg.SMS.Aliyun.AccessKeyID)
		}
	}
}
