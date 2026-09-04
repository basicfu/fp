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

// TestOnReloadFiresOncePerSuccessfulReload 守住热重载回调这条机制（复审
// 建议 2）。
//
// 它服务的是甲一的另一半：配置热重载引入一个新 app 之后，必须立刻给它补上
// srv 表追踪（cmd/fp-im 在这个回调里调 trackApps），否则这个新 app 的第一条
// 上行/连接事件会因为转发候选列表为空被静默丢弃、永不重发——与启动时不
// 预追踪的缺陷完全同形，只是触发条件从进程启动换成了热重载。
//
// 三条性质各自对应一种真实的错法：
//   - 成功重载恰好触发一次：漏触发＝新 app 追踪不上；多触发本身无害
//     （TrackApp 幂等），但说明触发点选错了地方。
//   - 首次加载不触发：LoadFile 那一刻调用方还拿不到 *Source，也就没机会
//     注册回调；这条断言钉住的是"不需要额外的标志位"这个前提确实成立。
//   - 回调在锁外调用：回调里做的第一件事就是 s.Apps()（要 RLock），若在
//     持写锁期间调用回调，这里会直接死锁。用带超时的 select 收结果，
//     死锁会变成一条确定的失败而不是把测试挂死。
func TestOnReloadFiresOncePerSuccessfulReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "apps.json")
	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1"}]}`)
	src, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	// 回调里读一次配置：这既是生产用法（trackApps 就是遍历 Apps()），
	// 也是"回调在锁外调用"的探针。
	seen := make(chan []string, 8)
	src.OnReload(func() { seen <- src.Apps() })
	if len(seen) != 0 {
		t.Fatalf("首次加载（LoadFile）不应触发回调，实际触发了 %d 次", len(seen))
	}

	write(t, p, `{"apps":[{"app_id":"a1","app_secret":"s1"},{"app_id":"a2","app_secret":"s2"}]}`)
	done := make(chan error, 1)
	go func() { done <- src.Reload() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("重载应成功：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Reload 5 秒没有返回：回调若在持有写锁时调用，回调里的 Apps()（RLock）会与它死锁")
	}
	select {
	case apps := <-seen:
		if len(apps) != 2 {
			t.Fatalf("回调里读到的应是重载后的配置（2 个 app），实际 %v", apps)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("成功重载之后必须触发一次回调：不触发的话，热重载引入的新 app 拿不到 srv 表追踪，" +
			"它的第一条上行/连接事件会因为转发候选列表为空被静默丢弃")
	}
	if len(seen) != 0 {
		t.Fatalf("一次成功重载只应触发一次回调，实际多触发了 %d 次", len(seen))
	}

	// 失败的重载不触发：配置没有变，下游没有任何需要同步的东西。
	write(t, p, `{`)
	if err := src.Reload(); err == nil {
		t.Fatal("坏文件必须重载失败")
	}
	if len(seen) != 0 {
		t.Fatalf("重载失败不应触发回调，实际触发了 %d 次", len(seen))
	}
}
