package fpsdk

import (
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// entry 是一次校验结果的缓存内容。
type entry struct {
	// appID 是这条判定所属的应用作用域。
	//
	// 缓存键是 token，而 token 全局唯一——单看这一点会以为不需要这个字段。
	// 但一条连接可以服务多个应用（CallerType 为 im 的客户端就是这样），
	// 那时"同一个 token 在不同应用作用域下的判定"是**两个不同的问题**：
	// fp 侧的 sess.AppID != app.ID 会拒掉跨应用的那次，而缓存如果只按
	// token 命中，就会拿应用 Y 的判定去放行一个声称属于应用 X 的握手——
	// 正是那条检查要防的串号，而且完全绕过了 fp。
	//
	// 普通客户端的作用域恒定，这个字段恒等，不改变任何行为。
	appID     string
	userID    string
	sessionID string
	// rotatedTo 非空表示这个 token 已被 fp 轮换。中间件每次命中都要把它
	// 回传给客户端——fp 在过渡期内会重复告知，SDK 也必须重复转达，
	// 否则缓存命中的那些请求反而成了交接的黑洞。
	rotatedTo string
	// roles 是该用户在本应用的有效角色，鉴权判定用它。
	// 跟着身份一起缓存：判定因此完全不碰网络。
	roles []string

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

	// gen 是缓存的代际。drop/purge 递增它。
	//
	// 它防的是一次时序竞态：一个回源响应可能在飞行途中被 drop/purge 越过，
	// 落地时把一条服务端已经不认的判定重新写回缓存。put 若发现代际已变，
	// 就丢弃这次写入——最坏代价是多一次回源，而反过来的代价是
	// "用户点了退出，之后一整个 cache_ttl 仍是登录态"。
	gen atomic.Uint64
}

func newCache(size int, now func() time.Time) (*cache, error) {
	l, err := lru.New[string, entry](size)
	if err != nil {
		return nil, err
	}
	return &cache{lru: l, now: now}, nil
}

// generation 返回当前代际，供回源前抓取一个基准点，回源完成后传给
// putIfGen 核对。
func (c *cache) generation() uint64 {
	return c.gen.Load()
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

// putIfGen 只在代际仍等于调用方发起回源之前记下的 gen 时才写入，否则
// 原样丢弃——中途发生过 drop/purge，说明这条判定可能已经过期，把它写
// 进缓存反而会复活一个服务端已经不认的 token（见 gen 字段的注释）。
//
// Validate 必须用这个方法而不是 put 来回填 singleflight 的回源结果：
// put 本身不知道"这次回源是什么时候发起的"，没有能力做这个核对。
func (c *cache) putIfGen(token string, e entry, ttl time.Duration, gen uint64) {
	if c.gen.Load() != gen {
		return
	}
	c.put(token, e, ttl)
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
//
// 递增 gen 在前还是在后不影响正确性（Remove 本身已经把这些 token 从缓存
// 里拿掉了），放在前面只是让"代际已变"尽早对并发的 putIfGen 可见。
func (c *cache) drop(tokens ...string) {
	c.gen.Add(1)
	for _, t := range tokens {
		c.lru.Remove(t)
	}
}

// purge 清空缓存。连接长时间断开后重连时调用——断开期间发生的撤销
// 一条都没收到，缓存里的任何条目都不再可信。
func (c *cache) purge() {
	c.gen.Add(1)
	c.lru.Purge()
}

// dropUser 丢掉某个用户的全部缓存条目。
//
// 用于角色变更：与撤销不同，这里只是让下次校验回源拿新角色，**不影响
// 登录态**——给某人加个权限不该把他踢下线。
func (c *cache) dropUser(userID string) {
	if userID == "" {
		return
	}
	for _, k := range c.lru.Keys() {
		if e, ok := c.lru.Peek(k); ok && e.userID == userID {
			c.lru.Remove(k)
		}
	}
}
