// Package fpappcfg 是 auth.AppConfigSource 的 fp 实现：app 的准入与策略向
// fp 实时索取，进程内缓存，靠 fp 的推送失效。
//
// 它取代了早期的 internal/im/appcfg（读本地 JSON 文件）。那份文件里存着
// 每个 app 的 app_secret 明文，而 fp 只存 bcrypt 哈希——两边天然会漂移，
// 且 fp-im 根本不需要持有那些 secret。
package fpappcfg

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/basicfu/fp/internal/im/model"
)

// Fetcher 是本包对 fp 的全部依赖。
//
// 用窄接口而不是直接 import fpsdk：internal/im/** 里只有 fpauth 允许碰
// fpsdk 的手写部分（internal/im/arch_test.go 断言）。真正的调用在 fpauth
// 里实现并注入进来。这不是为测试凭空造的抽象，是那条架构约束的直接后果。
type Fetcher interface {
	Fetch(ctx context.Context, app string) (model.AppConfig, error)
}

// Source 实现 auth.AppConfigSource。
type Source struct {
	f Fetcher
	// onLoad 在**首次**成功加载某个 app 之后调用，让调用方同步自己那份派生
	// 状态。唯一的用途见 cmd/fp-im：新 app 必须立刻加进 srv 表的追踪集合并
	// 强制刷新一次，否则它在本节点的第一条上行消息或连接事件会因为转发候选
	// 列表为空而被静默丢弃、零日志。
	onLoad func(app string)

	// sf 合并同一个 app 的并发拉取。一个 app 的第一批 client 往往同时握手，
	// 不合并的话 fp 会被打上 N 次同样的请求。
	sf singleflight.Group

	mu   sync.RWMutex
	apps map[string]model.AppConfig
}

func New(f Fetcher, onLoad func(app string)) *Source {
	return &Source{f: f, onLoad: onLoad, apps: map[string]model.AppConfig{}}
}

// Load 确保 app 的配置已在缓存里。有网络 I/O，只在冷路径调用。
//
// **失败不入缓存**：缓存键来自 client 的握手帧（未认证输入），缓存失败等于
// 把这个 map 的大小交给攻击者——与 fp 的 appVerifier 同一条推理。副作用是
// 不存在的 app 每次握手都会回源一次，由 fp 侧的限流兜底。
func (s *Source) Load(ctx context.Context, app string) error {
	s.mu.RLock()
	_, ok := s.apps[app]
	s.mu.RUnlock()
	if ok {
		return nil
	}
	_, err, _ := s.sf.Do(app, func() (any, error) {
		// 双检：等在 singleflight 上的那批协程醒来时，赢家可能已经写好了。
		s.mu.RLock()
		_, ok := s.apps[app]
		s.mu.RUnlock()
		if ok {
			return nil, nil
		}
		cfg, err := s.f.Fetch(ctx, app)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.apps[app] = cfg
		fn := s.onLoad
		s.mu.Unlock()
		// 回调在锁外调用：它是调用方给的任意代码，拿着 s.mu 调用它意味着
		// 回调里任何一次 Get/Apps（都要 RLock）都会自锁死。
		if fn != nil {
			fn(app)
		}
		return nil, nil
	})
	return err
}

// Get 是纯内存读：握手与 Push 的热路径都会调它。
func (s *Source) Get(app string) (model.AppConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.apps[app]
	return c, ok
}

// Apps 返回已缓存的 app 列表。
func (s *Source) Apps() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.apps))
	for k := range s.apps {
		out = append(out, k)
	}
	return out
}

// Invalidate 丢掉某个 app 的缓存，下一次 Load 会重新回源。
// 由 fp 的 AppIMConfigChanged 推送触发。
//
// 丢弃而不是就地重拉：重拉要网络 I/O，而调用点在推送流的读循环里，一个慢
// 回调等于拖慢整条推送流（见 fpsdk.Options.OnRevoke 的注释，同一条约束）。
// 丢掉之后由下一次握手按需拉，代价只是那一次握手多一次往返。
func (s *Source) Invalidate(app string) {
	s.mu.Lock()
	delete(s.apps, app)
	s.mu.Unlock()
}

// InvalidateAll 丢掉全部缓存。
//
// fp 的推送流出现 Gap（Redis 订阅重建，漏读且不知道漏了哪些）时用它。
// 与 fp 侧 WatchPurge 的处理同一范式：无从分辨丢了什么，只能全部重来。
func (s *Source) InvalidateAll() {
	s.mu.Lock()
	s.apps = map[string]model.AppConfig{}
	s.mu.Unlock()
}
