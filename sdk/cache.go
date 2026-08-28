package fpsdk

import (
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// entry 是一次校验结果的缓存内容。
type entry struct {
	userID    string
	sessionID string
	// rotatedTo 非空表示这个 token 已被 fp 轮换。中间件每次命中都要把它
	// 回传给客户端——fp 在过渡期内会重复告知，SDK 也必须重复转达，
	// 否则缓存命中的那些请求反而成了交接的黑洞。
	rotatedTo string

	// cachedAt 与 ttl 分开存，过期判定在 get 里现算。
	// 合并成一个 expiresAt 就无法在读取时收紧窗口——见下方 get 的说明。
	cachedAt time.Time
	ttl      time.Duration
}

// cacheState 是一次查询的结果状态。
type cacheState int

const (
	// cacheMiss 没有可用条目，必须回源。
	cacheMiss cacheState = iota
	// cacheFresh 条目在有效期内，可以直接放行。
	cacheFresh
	// cacheStale 条目已过期但仍在陈旧窗口内。只有 fp 不可达且调用方
	// 显式允许时才可使用。
	cacheStale
)

// cache 是进程内的校验结果缓存。
//
// hashicorp/golang-lru/v2 自带锁，本类型无需额外同步。
type cache struct {
	lru *lru.Cache[string, entry]
	now func() time.Time
}

func newCache(size int, now func() time.Time) (*cache, error) {
	l, err := lru.New[string, entry](size)
	if err != nil {
		return nil, err
	}
	return &cache{lru: l, now: now}, nil
}

// put 写入一条校验结果。
//
// ttl <= 0 时**什么都不做**。fp 在会话剩余不足一毫秒时就下发 0，
// 语义是"不要缓存"而非"未设置"。把 0 当成缺省值去套本地默认值，
// 会让一个已经到达 max_lifetime 的会话被继续放行整整一个缓存窗口。
func (c *cache) put(token string, e entry, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	e.cachedAt = c.now()
	e.ttl = ttl
	c.lru.Add(token, e)
}

// get 查询一条缓存结果。
//
// maxTTL 是本次查询允许的有效期上限，0 表示不额外收紧。推送流断开时
// 调用方传入 DegradedCacheTTL——收紧因此作用于**已经在缓存里的条目**。
//
// 这一点是本类型全部设计的核心：把收紧写进 put 的话，它只影响之后新写入的
// 条目，而流断开那一瞬间缓存里装的全是流健康时按完整窗口写入的条目，
// 收紧在最需要它的时刻完全失效。
//
// maxStale 是过期之后仍可返回（标记为 cacheStale）的额外时长，0 表示
// 过期即 miss。
func (c *cache) get(token string, maxTTL, maxStale time.Duration) (entry, cacheState) {
	e, ok := c.lru.Get(token)
	if !ok {
		return entry{}, cacheMiss
	}

	effective := e.ttl
	// 只收紧、不放宽：写成 max 会让本地配置覆盖 fp 下发的 cache_ttl，
	// 直接违背「过期判断只由 fp 做」的前提。
	if maxTTL > 0 && maxTTL < effective {
		effective = maxTTL
	}

	age := c.now().Sub(e.cachedAt)
	switch {
	case age < effective:
		return e, cacheFresh
	case maxStale > 0 && age < effective+maxStale:
		return e, cacheStale
	default:
		// 顺手清掉：留着它只会占一个 LRU 槽位，把还有用的条目挤出去。
		c.lru.Remove(token)
		return entry{}, cacheMiss
	}
}

// drop 删除若干条目。撤销事件到达时调用。
func (c *cache) drop(tokens ...string) {
	for _, t := range tokens {
		c.lru.Remove(t)
	}
}

// purge 清空缓存。连接长时间断开后重连时调用——断开期间发生的撤销
// 一条都没收到，缓存里的任何条目都不再可信。
func (c *cache) purge() { c.lru.Purge() }
