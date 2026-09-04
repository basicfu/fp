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

// Allow 对全新的 guestID 计数；已见过的 id 不占额度（同一个访客反复重连
// 不该被限流卡住）。rate<=0（app 没配置 GuestIPRate，或显式配了 0）时，
// len(b.ids) >= rate 对第一个全新 id 就已经成立，效果是拒绝这个 (app, IP)
// 下的所有新访客——这是有意的选择：配置缺失时保守拒绝，好过把
// "没配额度" 悄悄解释成 "不限额度" 而放行未知数量的访客连接。
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
