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
	s.mu.Unlock()
	return nil
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
			s.mu.Unlock()
			continue
		}
		slog.Info("appcfg: 已重载", "path", s.path, "apps", len(s.Apps()))
	}
}
