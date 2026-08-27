package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("FP_POSTGRES_URL", "postgres://x/y")
	t.Setenv("FP_REDIS_URL", "redis://localhost:6379/0")

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
	t.Setenv("FP_ENV", "PROD")
	t.Setenv("FP_HTTP_ADDR", ":18080")
	t.Setenv("FP_POSTGRES_URL", "postgres://x/y")
	t.Setenv("FP_REDIS_URL", "redis://localhost:6379/0")

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
	t.Setenv("FP_POSTGRES_URL", "")
	t.Setenv("FP_REDIS_URL", "redis://localhost:6379/0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for missing FP_POSTGRES_URL")
	}
}
