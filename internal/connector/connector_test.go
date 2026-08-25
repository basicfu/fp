package connector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

// stubConnector 是只为测试注册表而存在的最小实现。
type stubConnector struct{ typ string }

func (s stubConnector) Type() string { return s.typ }
func (s stubConnector) ConfigSchema() []domain.Field {
	return []domain.Field{{Key: "k", Label: "K", Type: domain.FieldTypeString}}
}
func (s stubConnector) Authenticate(context.Context, map[string]any, connector.Credentials) (*connector.Result, error) {
	return &connector.Result{IdentityType: "stub", Subject: "s"}, nil
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := connector.NewRegistry()

	if err := r.Register(stubConnector{typ: "a"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Get("a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Type() != "a" {
		t.Fatalf("Type = %q, want a", got.Type())
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	r := connector.NewRegistry()
	if err := r.Register(stubConnector{typ: "a"}); err != nil {
		t.Fatalf("首次 Register: %v", err)
	}
	if err := r.Register(stubConnector{typ: "a"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestRegistryRejectsEmptyType(t *testing.T) {
	r := connector.NewRegistry()
	if err := r.Register(stubConnector{typ: ""}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	r := connector.NewRegistry()
	if _, err := r.Get("nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRegistryTypesIsSorted(t *testing.T) {
	r := connector.NewRegistry()
	for _, typ := range []string{"c", "a", "b"} {
		if err := r.Register(stubConnector{typ: typ}); err != nil {
			t.Fatalf("Register %s: %v", typ, err)
		}
	}
	got := r.Types()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("Types() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Types() = %v, want %v", got, want)
		}
	}
}

func TestRegistrySchemas(t *testing.T) {
	r := connector.NewRegistry()
	if err := r.Register(stubConnector{typ: "a"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	schemas := r.Schemas()
	if len(schemas["a"]) != 1 || schemas["a"][0].Key != "k" {
		t.Fatalf("Schemas() = %+v", schemas)
	}
}

func TestCredentialsGet(t *testing.T) {
	c := connector.Credentials{"a": " x "}
	if got := c.Get("a"); got != "x" {
		t.Fatalf("Get 应当去除首尾空白, got %q", got)
	}
	if got := c.Get("missing"); got != "" {
		t.Fatalf("缺失键应返回空串, got %q", got)
	}
}

func TestConfigHelpers(t *testing.T) {
	// 从 JSONB 读出的数字是 float64，助手必须能处理。
	cfg := map[string]any{
		"n":    float64(10),
		"nInt": 7,
		"b":    true,
		"s":    "hello",
	}
	if got := connector.ConfigInt(cfg, "n", 1); got != 10 {
		t.Errorf("ConfigInt(float64) = %d, want 10", got)
	}
	if got := connector.ConfigInt(cfg, "nInt", 1); got != 7 {
		t.Errorf("ConfigInt(int) = %d, want 7", got)
	}
	if got := connector.ConfigInt(cfg, "absent", 3); got != 3 {
		t.Errorf("ConfigInt(缺省) = %d, want 3", got)
	}
	if got := connector.ConfigInt(cfg, "s", 3); got != 3 {
		t.Errorf("ConfigInt(类型不符) = %d, want 3", got)
	}
	if got := connector.ConfigBool(cfg, "b", false); !got {
		t.Error("ConfigBool = false, want true")
	}
	if got := connector.ConfigBool(cfg, "absent", true); !got {
		t.Error("ConfigBool(缺省) = false, want true")
	}
	if got := connector.ConfigString(cfg, "s", "x"); got != "hello" {
		t.Errorf("ConfigString = %q, want hello", got)
	}
	if got := connector.ConfigString(nil, "s", "x"); got != "x" {
		t.Errorf("ConfigString(nil cfg) = %q, want x", got)
	}
}
