package config

import (
	"strings"
	"testing"
)

func TestRequireEnvReturnsValue(t *testing.T) {
	t.Setenv("FP_TEST_REQUIRE_ENV", "postgres://x/y")
	v, err := RequireEnv("FP_TEST_REQUIRE_ENV")
	if err != nil {
		t.Fatalf("RequireEnv() error = %v", err)
	}
	if v != "postgres://x/y" {
		t.Errorf("v = %q, want postgres://x/y", v)
	}
}

func TestRequireEnvErrorsWhenMissing(t *testing.T) {
	t.Setenv("FP_TEST_REQUIRE_ENV_MISSING", "")
	_, err := RequireEnv("FP_TEST_REQUIRE_ENV_MISSING")
	if err == nil {
		t.Fatal("环境变量为空必须报错")
	}
	if !strings.Contains(err.Error(), "FP_TEST_REQUIRE_ENV_MISSING") {
		t.Errorf("错误信息 %q 里应当出现变量名", err)
	}
}

func TestEnvOrReturnsValueWhenSet(t *testing.T) {
	t.Setenv("FP_TEST_ENV_OR", "PROD")
	if v := EnvOr("FP_TEST_ENV_OR", "DEV"); v != "PROD" {
		t.Errorf("v = %q, want PROD", v)
	}
}

func TestEnvOrReturnsDefaultWhenMissing(t *testing.T) {
	t.Setenv("FP_TEST_ENV_OR_MISSING", "")
	if v := EnvOr("FP_TEST_ENV_OR_MISSING", "DEV"); v != "DEV" {
		t.Errorf("v = %q, want DEV", v)
	}
}
