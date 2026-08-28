package config

import (
	"testing"
)

// setRequiredEnv 把 Load 在生产环境下会校验的全部必填环境变量设成占位值，
// 供不关心这些字段本身的测试复用。各测试按需用 t.Setenv 覆盖它真正要测
// 的那一个，其余保持有效——不然每加一项必填校验，所有走成功路径的测试
// 都要跟着补一行，重复且容易漏改。
//
// 阿里云那四项只在 FP_ENV=PROD 时才是必填的（见 config.go 的 Load），
// 这里统一设上不影响非生产测试——非生产环境下这四项设或不设都应该
// Load 成功，专门验证"不设也行"的测试见 TestLoadAllowsMissingAliyunOutsideProd。
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

// TestLoadMissingAliyunRequiredInProd 钉住阿里云短信凭据在生产环境是必填项。
//
// 没有它的话，config.Load() 会允许生产环境下阿里云凭据全部留空——
// cmd/fp/main.go 会因此在生产环境悄悄退化成 notify.NewFakeProvider，
// 验证码只进内存、没有任何真实用户能收到，且不会有任何启动期报错，
// 只会在运营发现"用户投诉收不到验证码"时才暴露。
func TestLoadMissingAliyunRequiredInProd(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FP_ENV", "PROD")
	t.Setenv("FP_ALIYUN_ACCESS_KEY_ID", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for missing FP_ALIYUN_ACCESS_KEY_ID in PROD")
	}
}

// TestLoadAllowsMissingAliyunOutsideProd 钉住阿里云短信凭据在非生产环境
// 允许留空——config.Load 本身不应该因为缺它们而失败。
//
// notify.FakeProvider 的文档注释写着"用于测试与本地开发"：本机开发、CI
// 跑 cmd/fp 二进制，都不应该被要求先备齐一份连假的都算不上的阿里云凭据
// 才能启动。真正装配 FakeProvider 并打 WARN 日志的逻辑在 cmd/fp/main.go
// 里（package main 无测试基础设施，未覆盖到这条测试），这里只钉住
// "config.Load 不会在非生产环境替它做这个决定"。
func TestLoadAllowsMissingAliyunOutsideProd(t *testing.T) {
	t.Setenv("FP_POSTGRES_URL", "postgres://x/y")
	t.Setenv("FP_REDIS_URL", "redis://localhost:6379/0")
	// 显式清空，而不是干脆不调用 t.Setenv：本机 .env.local 为了 Step 5
	// 的手工验证配了阿里云占位值，scripts/test.sh 经 scripts/env.sh 把它们
	// 带进了整个测试进程的环境变量——不显式清空的话，这条测试在本机测的
	// 其实是"环境变量恰好没设"，而不是"config.Load 允许它们不设"，换一台
	// 干净的机器或者 CI 反而测不出这条测试原本想测的东西。
	t.Setenv("FP_ENV", "") // 显式清空，确保落在 envOr 的默认值 DEV 上
	t.Setenv("FP_ALIYUN_ACCESS_KEY_ID", "")
	t.Setenv("FP_ALIYUN_ACCESS_KEY_SECRET", "")
	t.Setenv("FP_ALIYUN_SMS_SIGN_NAME", "")
	t.Setenv("FP_ALIYUN_SMS_TEMPLATE_LOGIN_CODE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil（非生产环境阿里云凭据允许留空）", err)
	}
	if cfg.AliyunAccessKeyID != "" {
		t.Errorf("AliyunAccessKeyID = %q, want empty", cfg.AliyunAccessKeyID)
	}
}
