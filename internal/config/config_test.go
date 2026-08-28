package config

import (
	"testing"
)

// setRequiredEnv 把 Load 校验的全部必填环境变量设成占位值，供不关心这些
// 字段本身的测试复用。各测试按需用 t.Setenv 覆盖它真正要测的那一个，
// 其余保持有效——不然每加一项必填校验，所有走成功路径的测试都要跟着
// 补一行，重复且容易漏改。
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FP_POSTGRES_URL", "postgres://x/y")
	t.Setenv("FP_REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("FP_ALIYUN_ACCESS_KEY_ID", "test-key-id")
	t.Setenv("FP_ALIYUN_ACCESS_KEY_SECRET", "test-key-secret")
	t.Setenv("FP_ALIYUN_SMS_SIGN_NAME", "测试签名")
	t.Setenv("FP_ALIYUN_SMS_TEMPLATE_LOGIN_CODE", "SMS_TEST0001")
}

func TestLoadDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != "DEV" {
		t.Errorf("Env = %q, want DEV", cfg.Env)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.GRPCAddr != ":9090" {
		t.Errorf("GRPCAddr = %q, want :9090", cfg.GRPCAddr)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
}

func TestLoadOverrides(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FP_ENV", "PROD")
	t.Setenv("FP_HTTP_ADDR", ":18080")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Env != "PROD" {
		t.Errorf("Env = %q, want PROD", cfg.Env)
	}
	if cfg.HTTPAddr != ":18080" {
		t.Errorf("HTTPAddr = %q, want :18080", cfg.HTTPAddr)
	}
}

// IsProd 决定管理端 cookie 带不带 Secure，是个真正有安全后果的判定，
// 必须钉住它的大小写不敏感与默认值。
func TestIsProd(t *testing.T) {
	tests := []struct {
		env  string
		want bool
	}{
		{"PROD", true},
		{"prod", true},
		{"Prod", true},
		{"DEV", false},
		{"", false},
		{"PRODUCTION", false}, // 只认 PROD，不做前缀匹配
	}
	for _, tt := range tests {
		c := &Config{Env: tt.env}
		if got := c.IsProd(); got != tt.want {
			t.Errorf("Env=%q IsProd() = %v, want %v", tt.env, got, tt.want)
		}
	}
}

func TestLoadMissingRequired(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FP_POSTGRES_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for missing FP_POSTGRES_URL")
	}
}

// TestLoadMissingAliyunRequired 钉住阿里云短信凭据同样是必填项。
//
// 没有它的话，config.Load() 允许阿里云凭据全部留空——cmd/fp/main.go 会
// 拿着空的 AccessKeyID/AccessKeySecret/SignName 去构造 notify.AliyunSMS，
// 这一步确实会在 NewAliyunSMS 里失败并让启动失败，效果上"凑巧"正确；
// 但错误信息会指向 notify 包深处而不是"少配了哪个环境变量"，且如果
// NewAliyunSMS 未来放宽校验，这里就是唯一还在守住"不能没配置就启动"的地方。
func TestLoadMissingAliyunRequired(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FP_ALIYUN_ACCESS_KEY_ID", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for missing FP_ALIYUN_ACCESS_KEY_ID")
	}
}
