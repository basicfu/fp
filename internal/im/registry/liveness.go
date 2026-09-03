package registry

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/redis/go-redis/v9"
)

// Liveness 维护两张心跳表的本地快照：fp:im:node 与每个 app 的 fp:im:srv:{app}。
// 热路径只读快照，不碰 Redis；快照每 heartbeat 刷一次，最多滞后一个周期。
type Liveness struct {
	c         redis.UniversalClient
	nodeID    string
	heartbeat time.Duration
	deadAfter time.Duration
	now       func() time.Time

	mu      sync.RWMutex
	live    map[string]int64            // nodeId → 心跳毫秒
	srv     map[string]map[string]int64 // app → nodeId → 心跳毫秒
	serving map[string]bool             // 本节点正在为哪些 app 持有 server 流
	tracked map[string]bool             // 需要刷新 srv 表的 app
}

func NewLiveness(c redis.UniversalClient, nodeID string, heartbeat, deadAfter time.Duration) *Liveness {
	return &Liveness{
		c: c, nodeID: nodeID, heartbeat: heartbeat, deadAfter: deadAfter, now: time.Now,
		live: map[string]int64{}, srv: map[string]map[string]int64{}, serving: map[string]bool{}, tracked: map[string]bool{},
	}
}

func (l *Liveness) NodeID() string { return l.nodeID }

// Beat 写自己的心跳：节点表一条，每个正在服务的 app 各一条。
func (l *Liveness) Beat(ctx context.Context) error {
	ts := l.now().UnixMilli()
	l.mu.RLock()
	apps := make([]string, 0, len(l.serving))
	for a := range l.serving {
		apps = append(apps, a)
	}
	l.mu.RUnlock()
	_, err := l.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, model.KeyNodes, l.nodeID, ts)
		for _, a := range apps {
			p.HSet(ctx, model.SrvKey(a), l.nodeID, ts)
		}
		return nil
	})
	return err
}

// Refresh 重读节点表与所有被追踪 app 的 srv 表，过滤掉过期条目。
func (l *Liveness) Refresh(ctx context.Context) error {
	l.mu.RLock()
	apps := make([]string, 0, len(l.tracked))
	for a := range l.tracked {
		apps = append(apps, a)
	}
	l.mu.RUnlock()

	var nodeCmd *redis.MapStringStringCmd
	srvCmds := make([]*redis.MapStringStringCmd, len(apps))
	_, err := l.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		nodeCmd = p.HGetAll(ctx, model.KeyNodes)
		for i, a := range apps {
			srvCmds[i] = p.HGetAll(ctx, model.SrvKey(a))
		}
		return nil
	})
	if err != nil {
		return err
	}
	cutoff := l.now().UnixMilli() - l.deadAfter.Milliseconds()
	live := fresh(nodeCmd.Val(), cutoff)
	live[l.nodeID] = l.now().UnixMilli() // 自己永远算活的，哪怕 Beat 还没跑
	srv := map[string]map[string]int64{}
	for i, a := range apps {
		m := fresh(srvCmds[i].Val(), cutoff)
		for n := range m {
			if _, ok := live[n]; !ok {
				delete(m, n) // srv 表里的节点还得在节点表里活着
			}
		}
		srv[a] = m
	}
	l.mu.Lock()
	l.live, l.srv = live, srv
	l.mu.Unlock()
	return nil
}

func fresh(raw map[string]string, cutoff int64) map[string]int64 {
	out := map[string]int64{}
	for n, v := range raw {
		ts, err := strconv.ParseInt(v, 10, 64)
		if err == nil && ts >= cutoff {
			out[n] = ts
		}
	}
	return out
}

// Run 每 heartbeat 做一次 Beat + Refresh，直到 ctx 结束。
func (l *Liveness) Run(ctx context.Context) {
	t := time.NewTicker(l.heartbeat)
	defer t.Stop()
	for {
		_ = l.Beat(ctx)
		_ = l.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// TrackApp 让 Refresh 开始拉该 app 的 srv 表。hub 第一次需要某 app 的 ServerNodes 时调用。
func (l *Liveness) TrackApp(app string) {
	l.mu.Lock()
	l.tracked[app] = true
	l.mu.Unlock()
}

// SetServing 立即写或删 srv 表里自己的条目。删要立即：其他节点的缓存滞后 3 秒，
// 越早删越少消息被误发到这里。
func (l *Liveness) SetServing(ctx context.Context, app string, on bool) error {
	l.mu.Lock()
	if on {
		l.serving[app] = true
		l.tracked[app] = true
	} else {
		delete(l.serving, app)
	}
	l.mu.Unlock()
	if on {
		return l.c.HSet(ctx, model.SrvKey(app), l.nodeID, l.now().UnixMilli()).Err()
	}
	return l.c.HDel(ctx, model.SrvKey(app), l.nodeID).Err()
}

func (l *Liveness) LiveNodes() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0, len(l.live)+1)
	for n := range l.live {
		out = append(out, n)
	}
	if _, ok := l.live[l.nodeID]; !ok {
		out = append(out, l.nodeID)
	}
	sort.Strings(out)
	return out
}

func (l *Liveness) IsLive(node string) bool {
	if node == l.nodeID {
		return true
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.live[node]
	return ok
}

// ServerNodes 返回持有该 app server 流且活着的节点，含自己（若自己在服务）。
func (l *Liveness) ServerNodes(app string) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0, len(l.srv[app])+1)
	for n := range l.srv[app] {
		out = append(out, n)
	}
	if l.serving[app] && !contains(out, l.nodeID) {
		out = append(out, l.nodeID)
	}
	sort.Strings(out)
	return out
}
