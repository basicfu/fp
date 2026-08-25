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

func TestLoadMissingRequired(t *testing.T) {
	t.Setenv("FP_POSTGRES_URL", "")
	t.Setenv("FP_REDIS_URL", "redis://localhost:6379/0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for missing FP_POSTGRES_URL")
	}
}
