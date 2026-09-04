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

// TestGuestLimiterRateZeroRejectsAllNewGuests 钉住 rate<=0 时的行为：
// app 没配置 GuestIPRate（零值）或者显式配了 0，都应该拒绝这个 (app, IP)
// 下的所有新访客 id，而不是把"没配额度"当成"不限额度"放行。已见过的
// id 不受影响——这条限流只挡"新造 id"，不挡"已经放行过的访客重连"。
func TestGuestLimiterRateZeroRejectsAllNewGuests(t *testing.T) {
	g := NewGuestLimiter()
	now := time.UnixMilli(0)
	if g.Allow("a1", "1.2.3.4", "g1", 0, now) {
		t.Fatal("rate=0 时任何新访客都应被拒绝")
	}
	if g.Allow("a1", "1.2.3.4", "g2", 0, now) {
		t.Fatal("rate=0 时第二个新访客同样应被拒绝")
	}
}
