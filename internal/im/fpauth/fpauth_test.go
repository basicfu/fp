package fpauth

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
	fpsdk "github.com/basicfu/fp/sdk"
)

type apps map[string]model.AppConfig

func (a apps) Get(app string) (model.AppConfig, bool) { c, ok := a[app]; return c, ok }
func (a apps) Apps() []string                         { return nil }

func TestUnknownAppIsUnauthorized(t *testing.T) {
	a, err := New(Config{FPAddr: "127.0.0.1:1", Insecure: true, Apps: apps{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Verify(context.Background(), "nope", "tok"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("未配置的 app 应 ErrUnauthorized，实际 %v", err)
	}
}

func TestTranslate(t *testing.T) {
	for _, tc := range []struct{ in, want error }{
		{fpsdk.ErrUnauthorized, auth.ErrUnauthorized},
		{fpsdk.ErrNoToken, auth.ErrUnauthorized},
		{fpsdk.ErrUnavailable, auth.ErrUnavailable},
		{errors.New("something else"), auth.ErrUnavailable},
	} {
		if got := translate(tc.in); !errors.Is(got, tc.want) {
			t.Errorf("translate(%v)=%v，期望 %v", tc.in, got, tc.want)
		}
	}
}
