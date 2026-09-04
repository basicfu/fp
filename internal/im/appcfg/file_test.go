package appcfg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/model"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFileDefaultsAndValidation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "apps.json")
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1"},{"app_id":"a2","app_secret":"s2","conn_policy":"limit","conn_limit":3,"allow_guest":true}]}`)
	src, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	a1, ok := src.Get("a1")
	if !ok || a1.ConnPolicy != model.PolicyReplace || a1.ConnLimit != 5 || a1.GuestIPRate != 20 || a1.AllowGuest {
		t.Fatalf("未写的字段应取 spec 默认值，实际 %+v", a1)
	}
	if a2, _ := src.Get("a2"); a2.ConnPolicy != model.PolicyLimit || a2.ConnLimit != 3 || !a2.AllowGuest {
		t.Fatalf("显式字段应生效，实际 %+v", a2)
	}
	if _, ok := src.Get("nope"); ok {
		t.Fatal("未配置的 app 必须返回 false")
	}
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1","conn_policy":"bogus"}]}`)
	if _, err := LoadFile(p); err == nil {
		t.Fatal("非法策略必须在加载时报错，而不是握手时才发现")
	}
}

// TestLoadFileRejectsDuplicateAppID 守住 Reload 里的去重检查：配置文件里
// 两条记录用了同一个 app_id 必须在加载时就报错，而不是让后写的一条静默
// 覆盖前一条——那样运维在文件里犯的一个复制粘贴错误会在没有任何提示的
// 情况下丢掉一个 app 的配置。
func TestLoadFileRejectsDuplicateAppID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "apps.json")
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1"},{"app_id":"a1","app_secret":"s2"}]}`)
	if _, err := LoadFile(p); err == nil {
		t.Fatal("重复的 app_id 必须在加载时报错")
	}
}

func TestWatchReloadsOnChangeAndKeepsOldOnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "apps.json")
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1"}]}`)
	src, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Watch(ctx, 20*time.Millisecond)

	time.Sleep(30 * time.Millisecond) // 让 mtime 变化可被观察到（文件系统时间精度）
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1"},{"app_id":"a2","app_secret":"s2"}]}`)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := src.Get("a2"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("文件变化 3 秒内没有被重载")
		}
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(30 * time.Millisecond) // 让 mtime 变化可被观察到（文件系统时间精度）
	write(t, p, `not json`)
	// 轮询到"这次失败重载确实被尝试过"，而不是固定睡一段时间就断言：
	// 固定睡眠是会撒谎的假通过——机器负载重时，监视协程可能在睡眠窗口内
	// 还没来得及跑这次失败的 Reload，配置从头到尾没变过，测试会照样通过，
	// 却把"坏文件清空了配置"这个回归完全掩盖掉。
	deadline = time.Now().Add(3 * time.Second)
	for src.failedReloadCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("3 秒内没有观察到一次失败的重载尝试")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := src.Get("a2"); !ok {
		t.Fatal("坏文件不能把已加载的配置清空，要保留上一份")
	}
}
