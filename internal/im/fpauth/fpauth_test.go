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

// TestVerifyAfterCloseDoesNotRebuildClient 守住"Close 之后必须硬失败，
// 不能悄悄重建 fp 连接"：一个在途的握手协程如果在 Close() 之后才调
// Verify，且它要找的 app 仍在配置里，如果 client() 不检查关闭状态，
// 就会静默地为这个已经语义上死掉的 Authenticator 新建一条到 fp 的连接
// ——这在进程正在退出时尤其糟糕。
func TestVerifyAfterCloseDoesNotRebuildClient(t *testing.T) {
	a, err := New(Config{
		FPAddr: "127.0.0.1:1", Insecure: true,
		Apps: apps{"a1": model.AppConfig{AppID: "a1", AppSecret: "s1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Verify(context.Background(), "a1", "tok"); !errors.Is(err, auth.ErrUnavailable) {
		t.Fatalf("Close 之后 Verify 应返回 ErrUnavailable，实际 %v", err)
	}
	a.mu.Lock()
	n := len(a.clients)
	a.mu.Unlock()
	if n != 0 {
		t.Fatalf("Close 之后不应该悄悄建立新的 fp 客户端，实际 clients=%d", n)
	}
}
