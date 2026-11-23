package hub

import "sync/atomic"

// stats 是本节点自启动以来的累计计数,只增不减;定时日志取两次快照的差值,
// 不在这里清零,免得多个读者互相把对方的数清掉。
type stats struct {
	opened, closed, rejected        atomic.Int64
	pushSent, pushOffline, pushFail atomic.Int64
}

// StatsSnapshot 是某一时刻的累计计数,加上当时的本地在线连接数。
type StatsSnapshot struct {
	Online                          int
	Opened, Closed, Rejected        int64
	PushSent, PushOffline, PushFail int64
}

// Sub 返回相对 prev 的增量;Online 是时点值,取当前的。
func (s StatsSnapshot) Sub(prev StatsSnapshot) StatsSnapshot {
	return StatsSnapshot{
		Online:      s.Online,
		Opened:      s.Opened - prev.Opened,
		Closed:      s.Closed - prev.Closed,
		Rejected:    s.Rejected - prev.Rejected,
		PushSent:    s.PushSent - prev.PushSent,
		PushOffline: s.PushOffline - prev.PushOffline,
		PushFail:    s.PushFail - prev.PushFail,
	}
}

// NoteRejected 记一次握手被拒。握手在 wsapi 里做,被拒的连接从没进过 hub。
func (h *Hub) NoteRejected() { h.stats.rejected.Add(1) }

// StatsSnapshot 返回当前累计计数与本地在线连接数。
func (h *Hub) StatsSnapshot() StatsSnapshot {
	return StatsSnapshot{
		Online:      h.LocalConnCount(),
		Opened:      h.stats.opened.Load(),
		Closed:      h.stats.closed.Load(),
		Rejected:    h.stats.rejected.Load(),
		PushSent:    h.stats.pushSent.Load(),
		PushOffline: h.stats.pushOffline.Load(),
		PushFail:    h.stats.pushFail.Load(),
	}
}

// countPush 按每个主体的投递结果计数(一次 PushMany 推 N 个主体算 N 次)。
func (h *Hub) countPush(results []PushResult) {
	for _, r := range results {
		switch r.Status {
		case PushSent:
			h.stats.pushSent.Add(1)
		case PushNotOnline:
			h.stats.pushOffline.Add(1)
		case PushUnavailable:
			h.stats.pushFail.Add(1)
		}
	}
}
