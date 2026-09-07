package bizauth

import (
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/basicfu/fp/internal/im/model"
)

// cacheKey 用结构体而不是拼接字符串。拼接会引入歧义：
// app "a" 加令牌 "1:b" 与 app "a:1" 加令牌 "b" 拼出来是同一个串，
// 两个应用的令牌就串了。
//
// verifyURL 也并进键里：热重载把某个 app 的验证地址切到另一个后端
// （灰度、迁移、修配错的地址）之后，旧后端对同一个 token 给出的结论
// 不该继续生效——不带这个字段的话，最长 cache_seconds 之内还是旧后端
// 说了算。
type cacheKey struct {
	app       string
	token     string
	verifyURL string
}

type cacheEntry struct {
	sub       model.Subject
	expiresAt time.Time
}

// cacheSet 按 app 各持一个 LRU：容量是 app 级配置，合成一个全局缓存的话
// 一个高流量应用会把其它应用的条目挤光。
type cacheSet struct {
	mu    sync.Mutex
	byApp map[string]*lru.Cache[cacheKey, cacheEntry]
}

func newCacheSet() *cacheSet {
	return &cacheSet{byApp: map[string]*lru.Cache[cacheKey, cacheEntry]{}}
}

func (s *cacheSet) get(app, token, verifyURL string, now time.Time) (model.Subject, bool) {
	s.mu.Lock()
	c, ok := s.byApp[app]
	s.mu.Unlock()
	if !ok {
		return model.Subject{}, false
	}
	e, ok := c.Get(cacheKey{app, token, verifyURL})
	if !ok || !now.Before(e.expiresAt) {
		return model.Subject{}, false
	}
	return e.sub, true
}

// put 在 ttl 大于零时写入。size 是该 app 配置的容量上限，
// 首次为该 app 建缓存时生效；之后改配置不会缩容，重启才生效——
// 缓存容量不值得为热更新引入重建逻辑。
func (s *cacheSet) put(app, token, verifyURL string, sub model.Subject, ttl time.Duration, size int, now time.Time) {
	if ttl <= 0 {
		return
	}
	s.mu.Lock()
	c, ok := s.byApp[app]
	if !ok {
		var err error
		c, err = lru.New[cacheKey, cacheEntry](size)
		if err != nil {
			// size 已在配置校验里保证大于零，走不到这里。
			s.mu.Unlock()
			return
		}
		s.byApp[app] = c
	}
	s.mu.Unlock()
	c.Add(cacheKey{app, token, verifyURL}, cacheEntry{sub: sub, expiresAt: now.Add(ttl)})
}
