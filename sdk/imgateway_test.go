package fpsdk

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"
)

// TestWithAppIDAttachesScope：im 客户端的作用域是逐调用附上的，不在连接
// 凭据里。
func TestWithAppIDAttachesScope(t *testing.T) {
	ctx := WithAppID(context.Background(), "app-x")
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("outgoing metadata 缺失")
	}
	if v := md.Get("fp-app-id"); len(v) != 1 || v[0] != "app-x" {
		t.Fatalf("fp-app-id = %v, want [app-x]", v)
	}
}

// TestEffectiveAppIDFallsBackToOptions：普通客户端不调 WithAppID，作用域
// 恒为 Options.AppID——这保证给缓存分桶的那个键对它来说是恒定的，行为与
// 加入分桶之前完全一致。
func TestEffectiveAppIDFallsBackToOptions(t *testing.T) {
	c := &Client{opts: Options{AppID: "fixed"}}
	if got := c.effectiveAppID(context.Background()); got != "fixed" {
		t.Fatalf("effectiveAppID = %q, want fixed", got)
	}
	if got := c.effectiveAppID(WithAppID(context.Background(), "per-call")); got != "per-call" {
		t.Fatalf("逐调用作用域应当覆盖 Options.AppID，got %q", got)
	}
}

// TestIMClientSkipsPolicyRefresh：IM 网关没有 app 作用域，也用不到任何
// 应用的授权策略，而 fp 对它关着 GetPolicy。不跳过的话每次推送流重连都会
// 刷一条 PermissionDenied 的 WARN，把真正的问题淹掉。
func TestIMClientSkipsPolicyRefresh(t *testing.T) {
	c := &Client{opts: Options{CallerType: CallerTypeIM}}
	// rpc 为 nil：一旦没有提前返回，这里会立刻 nil 解引用 panic。
	c.refreshPolicy(context.Background())
}
