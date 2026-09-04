// Package appcfg 是 AppConfigSource 的 JSON 文件实现。
// fp 还没有配置中心，先从文件读；文件变化后重载，坏文件保留上一份。
package appcfg

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/basicfu/fp/internal/im/model"
)

type file struct {
	Apps []model.AppConfig `json:"apps"`
}

type Source struct {
	path  string
	mu    sync.RWMutex
	apps  map[string]model.AppConfig
	mtime time.Time
	// failedReloads 统计 Watch 里累计遇到的重载失败次数。它只服务于测试：
	// "坏文件不清空已加载配置"这条断言必须先确定性地等到一次失败重载真的
	// 被尝试过，再去检查配置还在不在——固定睡一段时间再断言是会撒谎的
	// 假通过：机器负载重时，监视协程可能还没来得及跑那次失败的 Reload，
	// 配置从头到尾没变过，测试照样绿，却什么都没验证到。
	failedReloads int

	// onReload 在每次成功重载之后被调用，让调用方同步自己那份派生状态。
	// 目前唯一的用途见 cmd/fp-im：热重载引入一个新 app 之后必须立刻给它
	// 补上 srv 表追踪，否则这个新 app 的第一条上行消息或连接事件会因为
	// 转发候选列表为空而被静默丢弃（甲一）。
	onReload func()
}

func LoadFile(path string) (*Source, error) {
	s := &Source{path: path}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload 读文件、填默认值、逐个校验；任何一个 app 非法整份拒绝。
//
// 不是"跳过坏的、加载好的"：半份配置会让某些 app 神秘地连不上，
// 比整份失败难查得多——加载失败至少能在启动/重载时就地暴露问题。
func (s *Source) Reload() error {
	st, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("appcfg: %w", err)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("appcfg: %w", err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("appcfg: 解析 %s: %w", s.path, err)
	}
	apps := make(map[string]model.AppConfig, len(f.Apps))
	for _, a := range f.Apps {
		if a.ConnPolicy == "" {
			a.ConnPolicy = model.PolicyReplace
		}
		if a.ConnLimit == 0 {
			a.ConnLimit = 5
		}
		if a.GuestIPRate == 0 {
			a.GuestIPRate = 20
		}
		if err := a.Validate(); err != nil {
			return err
		}
		if _, dup := apps[a.AppID]; dup {
			return fmt.Errorf("appcfg: app_id %q 重复", a.AppID)
		}
		apps[a.AppID] = a
	}
	s.mu.Lock()
	s.apps, s.mtime = apps, st.ModTime()
	fn := s.onReload
	s.mu.Unlock()
	// 回调在锁外调用：它是调用方给的任意代码，拿着 s.mu 调用它意味着
	// 回调里任何一次 Get/Apps（都要 RLock）都会自锁死。
	// LoadFile 首次加载时 fn 必然为 nil——调用方那时还拿不到 *Source，
	// 也就没机会注册回调，所以"首次加载不触发"是这个写法的自然结果，
	// 不需要额外的标志位。
	if fn != nil {
		fn()
	}
	return nil
}

// OnReload 注册一个在每次成功重载之后被调用的回调，用来同步调用方那份
// 由配置派生的状态。同一时刻只有一个回调，重复调用会覆盖前一个。
func (s *Source) OnReload(fn func()) {
	s.mu.Lock()
	s.onReload = fn
	s.mu.Unlock()
}

func (s *Source) Get(app string) (model.AppConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.apps[app]
	return c, ok
}

func (s *Source) Apps() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.apps))
	for k := range s.apps {
		out = append(out, k)
	}
	return out
}

// Watch 每 interval 看一次 mtime，变了就 Reload。不用 fsnotify：多一个
// 依赖换来的只是几秒的及时性。
func (s *Source) Watch(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st, err := os.Stat(s.path)
		if err != nil {
			continue
		}
		s.mu.RLock()
		changed := !st.ModTime().Equal(s.mtime)
		s.mu.RUnlock()
		if !changed {
			continue
		}
		if err := s.Reload(); err != nil {
			slog.Error("appcfg: 重载失败，保留上一份配置", "path", s.path, "err", err)
			// 记住这个坏版本的 mtime，避免每个周期重复报同一条错；
			// 不清空 s.apps——坏文件不能把已加载的配置清掉。
			s.mu.Lock()
			s.mtime = st.ModTime()
			s.failedReloads++
			s.mu.Unlock()
			continue
		}
		slog.Info("appcfg: 已重载", "path", s.path, "apps", len(s.Apps()))
	}
}

// failedReloadCount 返回 Watch 累计遇到的重载失败次数。
// 仅供测试用于确定性地等待"一次失败重载确实被尝试过"，不是公开接口。
func (s *Source) failedReloadCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.failedReloads
}
