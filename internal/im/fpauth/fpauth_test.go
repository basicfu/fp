package fpauth

import (
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/basicfu/fp/internal/im/auth"
	fpsdk "github.com/basicfu/fp/sdk"
)

// newTestAuthenticator 构造一个 Authenticator。
//
// fpsdk.New 内部用的是 grpc.NewClient，它是**惰性**的——不会在这里真的拨号，
// 所以一个必然不通的地址足够让这些纯逻辑测试跑起来。
func newTestAuthenticator(t *testing.T, cfg Config) *Authenticator {
	t.Helper()
	if cfg.FPAddr == "" {
		cfg.FPAddr = "127.0.0.1:1"
	}
	if cfg.Secret == "" {
		cfg.Secret = "im-secret"
	}
	cfg.Insecure = true
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// TestCrossAppRevokeFansOutToAllApps 是本阶段最重要的一条测试。
//
// RevokeEvent.AppID 为空串表示**跨全部应用**的撤销（改密、冻结）。
//
// 早期 fp-im 每个 app 一个 client，这条事件会被 N 个客户端各收一份，每个用
// 自己闭包里的 app 调 OnRevoke——扇出是靠连接数量天然做到的。收敛成一条流
// 之后只收到一份，照抄旧写法（OnRevoke(ev.AppID, ...)）会拿空串去查 hub 的
// 连接表（键是 app + "\x00" + token），一条也查不到——**改密码、冻结用户
// 之后 ws 全都不会被关，而且零报错、零日志**。
//
// 这属于"删掉也不会让任何现有测试变红"的那类，必须显式钉住。
func TestCrossAppRevokeFansOutToAllApps(t *testing.T) {
	var mu sync.Mutex
	var got []string
	a := newTestAuthenticator(t, Config{
		AllApps:  func() []string { return []string{"a1", "a2", "a3"} },
		OnRevoke: func(app string, _ []string) { mu.Lock(); got = append(got, app); mu.Unlock() },
	})

	a.dispatchRevoke(fpsdk.RevokeEvent{AppID: "", Tokens: []string{"t1"}})

	mu.Lock()
	defer mu.Unlock()
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"a1", "a2", "a3"}) {
		t.Fatalf("跨应用撤销扇出到 %v，必须覆盖本节点持有的每个 app", got)
	}
}

// TestScopedRevokeGoesToOneApp：带 app 的撤销只投给那一个，不能因为补了
// 扇出就把所有 app 都扫一遍。
func TestScopedRevokeGoesToOneApp(t *testing.T) {
	var mu sync.Mutex
	var got []string
	a := newTestAuthenticator(t, Config{
		AllApps:  func() []string { return []string{"a1", "a2", "a3"} },
		OnRevoke: func(app string, _ []string) { mu.Lock(); got = append(got, app); mu.Unlock() },
	})

	a.dispatchRevoke(fpsdk.RevokeEvent{AppID: "a2", Tokens: []string{"t1"}})

	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(got, []string{"a2"}) {
		t.Fatalf("带 app 的撤销扇出到 %v，期望只有 a2", got)
	}
}

// TestEmptyRevokeIsIgnored：没有 token 的事件不该惊动任何人。
func TestEmptyRevokeIsIgnored(t *testing.T) {
	var calls int
	a := newTestAuthenticator(t, Config{
		AllApps:  func() []string { return []string{"a1"} },
		OnRevoke: func(string, []string) { calls++ },
	})
	a.dispatchRevoke(fpsdk.RevokeEvent{AppID: "", Tokens: nil})
	if calls != 0 {
		t.Fatalf("空 token 列表触发了 %d 次回调", calls)
	}
}

// TestNewRequiresAddrAndSecret：两项都必须在装配阶段就失败。
//
// internal/im/config 按"谁用谁校验"不管这两项，这里是唯一的挡板；而 client
// 是启动时建的，所以配错了不会等到第一个用户握手才暴露。
func TestNewRequiresAddrAndSecret(t *testing.T) {
	if _, err := New(Config{Secret: "s"}); err == nil {
		t.Fatal("FPAddr 为空必须报错")
	}
	if _, err := New(Config{FPAddr: "127.0.0.1:1"}); err == nil {
		t.Fatal("Secret 为空必须报错")
	}
}

func TestTranslate(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   error
		want error
	}{
		{"nil", nil, nil},
		{"无 token", fpsdk.ErrNoToken, auth.ErrUnauthorized},
		{"未授权", fpsdk.ErrUnauthorized, auth.ErrUnauthorized},
		// 应用不存在 / 已停用 / 没开 IM 是**确定**的拒绝，不是"够不着"——
		// 退避重连不会让它变好，所以归到 ErrUnauthorized 而不是 ErrUnavailable。
		{"IM 不可用", fpsdk.ErrIMNotAvailable, auth.ErrUnauthorized},
		{"fp 不可达", fpsdk.ErrUnavailable, auth.ErrUnavailable},
		{"其它错误", errors.New("boom"), auth.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := translate(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("translate(nil) = %v，期望 nil", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("translate(%v) = %v，期望 %v", tc.in, got, tc.want)
			}
		})
	}
}
