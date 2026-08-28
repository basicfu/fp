package fpsdk

import (
	"testing"
	"time"
)

// fakeClock 让测试精确控制时间，不用 sleep。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestCache(t *testing.T, size int) (*cache, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	c, err := newCache(size, clk.now)
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}
	return c, clk
}

func TestCacheHitBeforeTTL(t *testing.T) {
	c, clk := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 30*time.Second)

	clk.advance(29 * time.Second)
	got, state := c.get("tok", 0, 0)
	if state != cacheFresh {
		t.Fatalf("TTL 内查询返回 %v，期望 cacheFresh", state)
	}
	if got.userID != "u1" {
		t.Fatalf("取回的 userID 是 %q", got.userID)
	}
}

func TestCacheMissAfterTTL(t *testing.T) {
	c, clk := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 30*time.Second)

	clk.advance(31 * time.Second)
	if _, state := c.get("tok", 0, 0); state == cacheFresh {
		t.Fatal("超过 TTL 后仍判定为新鲜")
	}
}

// TestZeroTTLIsNotCached 钉住交接契约 1。
//
// fp 在会话剩余不足一毫秒时下发 cache_ttl_ms = 0，含义是"不要缓存"。
// 把它当成"没给，用默认值"会缓存一个本该立刻失效的放行判定——
// 一个已经到达 max_lifetime 的会话会被继续放行整整一个缓存窗口。
func TestZeroTTLIsNotCached(t *testing.T) {
	c, _ := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 0)

	if _, state := c.get("tok", 0, 0); state == cacheFresh {
		t.Fatal("ttl=0 的条目被缓存了——0 表示不要缓存，不是未设置")
	}
}

func TestNegativeTTLIsNotCached(t *testing.T) {
	c, _ := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, -time.Second)
	if _, state := c.get("tok", 0, 0); state == cacheFresh {
		t.Fatal("负 TTL 的条目被缓存了")
	}
}

// TestDegradedCapAppliesToExistingEntries 是本任务最重要的一个测试。
//
// 推送流断开时要把缓存窗口收紧到 DegradedCacheTTL。收紧若写在 put 里，
// 这个测试会失败——因为条目是在"流还健康"时以完整窗口写入的，
// 而流断开那一瞬间缓存里装的**全是**这种条目。
// 也就是说：写入时收紧的实现，在最需要收紧的时刻完全不起作用，
// 却能让"断开后新写入的条目 TTL 变短"这类测试顺利通过。
func TestDegradedCapAppliesToExistingEntries(t *testing.T) {
	c, clk := newTestCache(t, 16)

	// 流健康时写入，完整窗口 60 秒。
	c.put("tok", entry{userID: "u1"}, 60*time.Second)

	// 10 秒后流断开，收紧到 5 秒。这条 10 秒前写入的条目必须立即失效。
	clk.advance(10 * time.Second)
	if _, state := c.get("tok", 5*time.Second, 0); state == cacheFresh {
		t.Fatal("流断开后，存量缓存条目仍按完整窗口放行——" +
			"收紧被写在了 put 里，对存量条目无效")
	}

	// 反向确认：3 秒时收紧到 5 秒，应当仍然命中，否则就是把上限当成了"立刻失效"。
	c2, clk2 := newTestCache(t, 16)
	c2.put("tok", entry{userID: "u1"}, 60*time.Second)
	clk2.advance(3 * time.Second)
	if _, state := c2.get("tok", 5*time.Second, 0); state != cacheFresh {
		t.Fatal("收紧到 5 秒后，第 3 秒的条目也失效了——上限被当成了立即过期")
	}
}

// TestMaxTTLNeverExtends 守住上限只能收紧、不能放宽。
//
// min 写反成 max 的话，fp 下发的 cache_ttl 会被本地配置覆盖放大——
// 一个 fp 明确说"只能缓存 2 秒"（会话快到期了）的判定被缓存 30 秒，
// 直接违背 4.5.1「过期判断只由 fp 做」的整个前提。
func TestMaxTTLNeverExtends(t *testing.T) {
	c, clk := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 2*time.Second)

	clk.advance(3 * time.Second)
	if _, state := c.get("tok", 60*time.Second, 0); state == cacheFresh {
		t.Fatal("传入更大的 maxTTL 把条目的有效期放大了——min 写成了 max")
	}
}

// TestStaleWindow 确认陈旧兜底的三档状态。
func TestStaleWindow(t *testing.T) {
	c, clk := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 10*time.Second)

	clk.advance(5 * time.Second)
	if _, s := c.get("tok", 0, time.Minute); s != cacheFresh {
		t.Fatalf("TTL 内应为 cacheFresh，得到 %v", s)
	}
	clk.advance(10 * time.Second) // 已过期 5 秒，仍在 1 分钟的陈旧窗口内
	if _, s := c.get("tok", 0, time.Minute); s != cacheStale {
		t.Fatalf("过期但在陈旧窗口内应为 cacheStale，得到 %v", s)
	}
	clk.advance(2 * time.Minute) // 超出陈旧窗口
	if _, s := c.get("tok", 0, time.Minute); s != cacheMiss {
		t.Fatalf("超出陈旧窗口应为 cacheMiss，得到 %v", s)
	}
}

// TestStaleIsNeverReturnedWhenMaxStaleIsZero 确认默认不吐陈旧数据。
func TestStaleIsNeverReturnedWhenMaxStaleIsZero(t *testing.T) {
	c, clk := newTestCache(t, 16)
	c.put("tok", entry{userID: "u1"}, 10*time.Second)
	clk.advance(11 * time.Second)

	if _, s := c.get("tok", 0, 0); s != cacheMiss {
		t.Fatalf("maxStale=0 时过期条目返回 %v，期望 cacheMiss", s)
	}
}

func TestCacheEvictsByCapacity(t *testing.T) {
	c, _ := newTestCache(t, 2)
	c.put("a", entry{userID: "ua"}, time.Minute)
	c.put("b", entry{userID: "ub"}, time.Minute)
	c.put("c", entry{userID: "uc"}, time.Minute)

	if _, s := c.get("a", 0, 0); s == cacheFresh {
		t.Fatal("容量为 2 却装下了 3 条——缓存无界增长会拖垮业务方进程")
	}
	if _, s := c.get("c", 0, 0); s != cacheFresh {
		t.Fatal("最新写入的条目被淘汰了")
	}
}

// TestDropRemovesEntries 确认撤销事件能清掉缓存。
func TestDropRemovesEntries(t *testing.T) {
	c, _ := newTestCache(t, 16)
	c.put("a", entry{userID: "u"}, time.Minute)
	c.put("b", entry{userID: "u"}, time.Minute)

	c.drop("a", "b")
	for _, tok := range []string{"a", "b"} {
		if _, s := c.get(tok, 0, 0); s != cacheMiss {
			t.Fatalf("drop 后 %q 仍在缓存里——撤销推送将不起作用", tok)
		}
	}
}

// TestZeroTTLPutDoesNotEvictOtherEntries 补一个 TestZeroTTLIsNotCached 没盯住的角度。
//
// get 里 age < effective 的严格小于比较，让 ttl<=0 的条目在被查询时天然读回
// cacheMiss——即使 put 完全不做 ttl<=0 的早退判断，TestZeroTTLIsNotCached 和
// TestNegativeTTLIsNotCached 依然会通过。但早退判断真正把住的是另一件事：
// 不该发生的 Add 调用。少了它，一个"不要缓存"的 token 仍会真的写进 LRU、
// 占掉一个槽位，容量吃紧时可能顶掉一条仍然新鲜、有用的条目——被顶掉的那个
// token 下一次请求就要多绕一趟回源，而这一切只是因为一个语义上"根本不该
// 被缓存"的条目先占了位置。
func TestZeroTTLPutDoesNotEvictOtherEntries(t *testing.T) {
	c, _ := newTestCache(t, 2)
	c.put("a", entry{userID: "ua"}, time.Minute) // 合法条目，最先写入、最久未被访问
	c.put("b", entry{userID: "ub"}, time.Minute) // 合法条目

	// ttl=0 语义是"不要缓存"。容量已满(2)：如果 put 没有早退，
	// 这次 Add 会挤掉 LRU 意义上最久未用的 "a"。
	c.put("z", entry{userID: "uz"}, 0)

	if _, s := c.get("a", 0, 0); s != cacheFresh {
		t.Fatal("ttl=0 的条目顶掉了一个仍然有效的旧条目——" +
			"put 没有在 ttl<=0 时提前返回，而是先写入了 LRU")
	}
}

// TestExpiredEntryDoesNotCrowdOutFreshEntry 补另一个没被给定测试盯住的角度。
//
// get 对过期条目命中时要"顺手 Remove"。少了这一步不只是"留着占位"那么
// 温和：底层 hashicorp/golang-lru 的 Get 只要键存在就会把它提升为最近使用，
// 不管这条数据是不是已经过期。于是一个刚被判定为过期、马上要返回 miss 的
// 条目，反而因为这次查询变成了 LRU 意义上最新鲜的一条——比什么都不做更糟：
// 容量吃紧时，它不再是最先被淘汰的那个，会继续占着槽位，把真正新鲜、
// 后写入的条目顶出去。
func TestExpiredEntryDoesNotCrowdOutFreshEntry(t *testing.T) {
	c, clk := newTestCache(t, 2)
	c.put("a", entry{userID: "ua"}, time.Second) // 很快过期
	c.put("b", entry{userID: "ub"}, time.Minute) // 仍然新鲜

	clk.advance(2 * time.Second) // a 过期，b 仍新鲜
	if _, s := c.get("a", 0, 0); s != cacheMiss {
		t.Fatalf("a 应该已过期，得到 %v", s)
	}

	// 容量已满(2)。c 是新条目，理应挤掉"已过期、没用"的 a，而不是仍然
	// 新鲜的 b。
	c.put("c", entry{userID: "uc"}, time.Minute)

	if _, s := c.get("b", 0, 0); s != cacheFresh {
		t.Fatal("仍然新鲜的 b 被挤掉了——get 命中过期条目时没有顺手 Remove，" +
			"底层 lru.Get 反而把这个过期条目提升成了最近使用")
	}
}
