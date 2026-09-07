package bizauth

import (
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/model"
)

func TestCacheHitAndExpiry(t *testing.T) {
	s := newCacheSet()
	now := time.UnixMilli(1_000_000)
	s.put("a1", "tok", model.Biz("u1"), 60*time.Second, 10, now)

	got, ok := s.get("a1", "tok", now.Add(59*time.Second))
	if !ok || got != model.Biz("u1") {
		t.Fatalf("未过期时应命中，实际 %+v ok=%v", got, ok)
	}
	if _, ok := s.get("a1", "tok", now.Add(61*time.Second)); ok {
		t.Fatal("过期后不能再命中：撤销的令牌不该无限期地继续放行")
	}
}

func TestCacheIsolatesApps(t *testing.T) {
	s := newCacheSet()
	now := time.UnixMilli(1_000_000)
	s.put("a1", "tok", model.Biz("u1"), time.Minute, 10, now)

	// 同一个 token 串在另一个 app 下必须是未命中：两个 app 的令牌体系毫不相干，
	// 串了就意味着 A 应用的令牌能在 B 应用下登录成另一个人。
	if _, ok := s.get("a2", "tok", now); ok {
		t.Fatal("不同 app 的同名令牌不能互相命中")
	}
}

func TestCacheKeyHasNoConcatenationAmbiguity(t *testing.T) {
	s := newCacheSet()
	now := time.UnixMilli(1_000_000)
	// 拼接成字符串的话，("a", "1:b") 与 ("a:1", "b") 会拼出同一个键。
	s.put("a", "1:b", model.Biz("first"), time.Minute, 10, now)
	s.put("a:1", "b", model.Biz("second"), time.Minute, 10, now)

	got1, _ := s.get("a", "1:b", now)
	got2, _ := s.get("a:1", "b", now)
	if got1 != model.Biz("first") || got2 != model.Biz("second") {
		t.Fatalf("两个键被混成了一个：got1=%+v got2=%+v", got1, got2)
	}
}

func TestCacheEvictsBeyondCapacity(t *testing.T) {
	s := newCacheSet()
	now := time.UnixMilli(1_000_000)
	for i := 0; i < 5; i++ {
		s.put("a1", string(rune('a'+i)), model.Biz("u"), time.Minute, 3, now)
	}
	// 容量 3，写了 5 条，最早的两条应当被淘汰
	live := 0
	for i := 0; i < 5; i++ {
		if _, ok := s.get("a1", string(rune('a'+i)), now); ok {
			live++
		}
	}
	if live != 3 {
		t.Fatalf("容量上限没生效，存活 %d 条，期望 3。无界缓存会在令牌高基数时吃光内存", live)
	}
}
