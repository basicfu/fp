package wsapi

import (
	"sync"
	"time"
)

// GuestLimiter 限制每个 (app, IP) 每分钟能带来多少个"新"访客 id。
// 这是 im 对自生成访客 id 的唯一防线：id 免费无限造，只能限制造它的人。
type GuestLimiter struct {
	mu      sync.Mutex
	buckets map[string]*guestBucket
}

type guestBucket struct {
	start time.Time
	ids   map[string]struct{}
}

func NewGuestLimiter() *GuestLimiter { return &GuestLimiter{buckets: map[string]*guestBucket{}} }

func (g *GuestLimiter) Allow(app, ip, guestID string, rate int, now time.Time) bool {
	k := app + "\x00" + ip
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.buckets[k]
	if b == nil || now.Sub(b.start) >= time.Minute {
		b = &guestBucket{start: now, ids: map[string]struct{}{}}
		g.buckets[k] = b
	}
	if _, seen := b.ids[guestID]; seen {
		return true
	}
	if len(b.ids) >= rate {
		return false
	}
	b.ids[guestID] = struct{}{}
	return true
}

// Sweep 清掉过期桶，cmd 每分钟调一次，否则 IP 数量无上限。
func (g *GuestLimiter) Sweep(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, b := range g.buckets {
		if now.Sub(b.start) >= time.Minute {
			delete(g.buckets, k)
		}
	}
}
