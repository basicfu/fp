package wsapi

import (
	"testing"
	"time"
)

func TestGuestLimiterCountsDistinctIDsPerIPPerMinute(t *testing.T) {
	g := NewGuestLimiter()
	now := time.UnixMilli(0)
	for i := 0; i < 3; i++ {
		if !g.Allow("a1", "1.2.3.4", "g"+string(rune('a'+i)), 3, now) {
			t.Fatalf("前 3 个新访客应放行，第 %d 个被拒", i+1)
		}
	}
	if g.Allow("a1", "1.2.3.4", "gz", 3, now) {
		t.Fatal("第 4 个新访客应被拒")
	}
	if !g.Allow("a1", "1.2.3.4", "ga", 3, now) {
		t.Fatal("已见过的访客 id 重连不占额度")
	}
	if !g.Allow("a1", "5.6.7.8", "gz", 3, now) {
		t.Fatal("额度按 IP 分，另一个 IP 不受影响")
	}
	if !g.Allow("a1", "1.2.3.4", "gz", 3, now.Add(61*time.Second)) {
		t.Fatal("一分钟窗口过后额度应重置")
	}
}
