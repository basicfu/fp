package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/redis/go-redis/v9"
)

// GroupState 是写协程回读本地真实状态的入口，由 hub 实现。
type GroupState interface {
	GroupLocalCount(app, group, uid string) int
	GroupSnapshot(app, group string) []string
	GroupsOf() [][2]string
}

const (
	groupBackoffMin = time.Second
	groupBackoffMax = 30 * time.Second
	// groupSAddBatch 是整份重写时每条 SADD 带的成员数：一条命令塞几万个参数
	// 会让 Redis 单次执行过长，拆开但仍在同一个 MULTI 里，原子性不变。
	groupSAddBatch = 1000
)

// GroupIndex 维护本节点各分组的在线索引 fp:im:{app:grp:group:node}:idx。
//
// 做法与 Liveness 写服务表相同：hub 在锁里只记"谁变了"，这里写"现在是什么"。
// 写出去的永远是写入那一刻回读的本地计数，之后再变一定会再标脏、再写一次，
// 所以命令之间谁先落地都收敛到本地状态，也不需要会溢出的操作队列。
//
// 所有写 Redis 的动作（增量、整份重写、排空）都持 wmu 串行执行：重写与增量
// 交错时，快照之后的变化都已标脏，必然在这次重写之后再写一遍，不会被覆盖。
// 用锁而不是把重写投递进 Run 协程，是因为优雅关闭时 Run 已随 serve 的 ctx
// 退出，Drain 仍要能写。
type GroupIndex struct {
	c     redis.UniversalClient
	node  string
	ttl   time.Duration
	state GroupState

	backoffMin, backoffMax time.Duration

	mu    sync.Mutex
	dirty map[[2]string]map[string]struct{} // {app, group} → uid
	wake  chan struct{}                     // 容量 1，多次标脏合并成一次唤醒

	wmu sync.Mutex
	// known 是写过的分组：GroupsOf 不再返回已经空了的分组，重写时要靠它找到
	// 这些分组去 DEL 残留。只在 wmu 下读写。
	known map[[2]string]struct{}
}

func NewGroupIndex(c redis.UniversalClient, nodeID string, ttl time.Duration, state GroupState) *GroupIndex {
	return &GroupIndex{
		c: c, node: nodeID, ttl: ttl, state: state,
		backoffMin: groupBackoffMin, backoffMax: groupBackoffMax,
		dirty: map[[2]string]map[string]struct{}{}, wake: make(chan struct{}, 1),
		known: map[[2]string]struct{}{},
	}
}

// MarkDirty 实现 hub.GroupIndex：纯内存操作，非阻塞唤醒写协程。
func (g *GroupIndex) MarkDirty(app, group, uid string) {
	g.mu.Lock()
	g.markLocked([2]string{app, group}, uid)
	g.mu.Unlock()
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

func (g *GroupIndex) markLocked(k [2]string, uid string) {
	if g.dirty[k] == nil {
		g.dirty[k] = map[string]struct{}{}
	}
	g.dirty[k][uid] = struct{}{}
}

// Run 是增量写协程，直到 ctx 结束。写失败时这批重新标脏，按 1s 起翻倍、
// 封顶 30s 退避；退避期间来的唤醒不立刻写，免得 Redis 故障时被标脏频率打爆。
func (g *GroupIndex) Run(ctx context.Context) {
	var retry <-chan time.Time
	var backoff time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.wake:
			if retry != nil {
				continue
			}
		case <-retry:
			retry = nil
		}
		if err := g.flush(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			backoff = min(max(backoff*2, g.backoffMin), g.backoffMax)
			retry = time.After(backoff)
			slog.Warn("registry: 写分组索引失败，退避后重试", "node", g.node, "backoff", backoff, "err", err)
			continue
		}
		backoff = 0
	}
}

func (g *GroupIndex) takeDirty() map[[2]string]map[string]struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.dirty) == 0 {
		return nil
	}
	d := g.dirty
	g.dirty = map[[2]string]map[string]struct{}{}
	return d
}

// flush 写一批脏成员。失败时整批放回：写的是当前状态，重复写无害。
func (g *GroupIndex) flush(ctx context.Context) error {
	g.wmu.Lock()
	defer g.wmu.Unlock()
	batch := g.takeDirty()
	if batch == nil {
		return nil
	}
	_, err := g.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		for k, uids := range batch {
			key := model.GroupIdxKey(k[0], k[1], g.node)
			for uid := range uids {
				// 计数在取走脏集合之后才读：读到的不早于标脏那次变化
				if g.state.GroupLocalCount(k[0], k[1], uid) > 0 {
					p.SAdd(ctx, key, uid)
				} else {
					p.SRem(ctx, key, uid)
				}
			}
			// 每次写都续 TTL：第一次 SADD 建出来的 key 也就带上了过期，
			// 节点崩溃后没人续期，它那份最多一个 TTL 后自己消失
			p.Expire(ctx, key, g.ttl)
		}
		return nil
	})
	if err != nil {
		g.mu.Lock()
		for k, uids := range batch {
			for uid := range uids {
				g.markLocked(k, uid)
			}
		}
		g.mu.Unlock()
		return err
	}
	for k := range batch {
		g.known[k] = struct{}{}
	}
	return nil
}

// RewriteAll 按本地快照整份重写本节点的每个分组索引，返回前完成。
//
// 兜底 Redis 重启、主从切换丢掉已写成功的数据：这些不一定伴随 Gap，增量
// 写入也不会知道。失败只记日志并返回错误，下一个周期会再来；Redis 里留着
// 的仍是增量维护的状态。
func (g *GroupIndex) RewriteAll(ctx context.Context) error {
	g.wmu.Lock()
	defer g.wmu.Unlock()
	groups := map[[2]string]struct{}{}
	for k := range g.known {
		groups[k] = struct{}{}
	}
	for _, k := range g.state.GroupsOf() {
		groups[k] = struct{}{}
	}
	var errs []error
	for k := range groups {
		if err := g.rewriteOne(ctx, k); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		slog.Warn("registry: 整份重写分组索引失败", "node", g.node, "err", err)
		return err
	}
	return nil
}

func (g *GroupIndex) rewriteOne(ctx context.Context, k [2]string) error {
	key := model.GroupIdxKey(k[0], k[1], g.node)
	uids := g.state.GroupSnapshot(k[0], k[1])
	if len(uids) == 0 {
		if err := g.c.Del(ctx, key).Err(); err != nil {
			return err
		}
		delete(g.known, k)
		return nil
	}
	_, err := g.c.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Del(ctx, key)
		for chunk := range slices.Chunk(uids, groupSAddBatch) {
			members := make([]interface{}, len(chunk))
			for i, u := range chunk {
				members[i] = u
			}
			p.SAdd(ctx, key, members...)
		}
		p.Expire(ctx, key, g.ttl)
		return nil
	})
	if err != nil {
		return err
	}
	g.known[k] = struct{}{}
	return nil
}

// Drain 把剩下的脏集合写完，优雅关闭拆完全部 ws 之后调用。失败按退避重试
// 直到写空或 ctx 用完；用完时返回 ctx 的错误，剩下的等 TTL 兜底。
func (g *GroupIndex) Drain(ctx context.Context) error {
	backoff := time.Duration(0)
	for {
		err := g.flush(ctx)
		if err == nil {
			g.mu.Lock()
			empty := len(g.dirty) == 0
			g.mu.Unlock()
			if empty {
				return nil
			}
			continue
		}
		backoff = min(max(backoff*2, g.backoffMin), g.backoffMax)
		select {
		case <-ctx.Done():
			return fmt.Errorf("registry: 排空分组索引: %w (最后一次: %v)", ctx.Err(), err)
		case <-time.After(backoff):
		}
	}
}

const (
	// MaxGroupMembers 是 ListGroup 一次返回的主体上限。到这个量级就该分页了，
	// 返回部分结果反而让调用方误以为拿全了。
	MaxGroupMembers = 10000
	// listGroupBatch 与 imgrpc 的 SessionsMany 上限一致：一条管道再长就会拖慢
	// 同节点的其它请求。
	listGroupBatch = 1000
)

var ErrGroupTooLarge = fmt.Errorf("registry: 分组在线主体超过 %d 个", MaxGroupMembers)

// SubjectConns 是分组里一个在线主体及其连接（已按存活表过滤）。
type SubjectConns struct {
	UID   string
	Conns map[string]model.ConnMeta
}

// ListGroup 列出某分组此刻在线的主体。
//
// 索引只是"去哪儿找"的提示：合并存活节点的索引后，逐个用连接表核实，没有活
// 连接的丢掉，所以结果与 SessionsMany 同一口径。死节点的索引整份跳过，不用
// 等它的 TTL。
func ListGroup(ctx context.Context, c redis.UniversalClient, conns *Conns, app, group string, live []string) ([]SubjectConns, error) {
	if err := model.ValidateGroup(group); err != nil {
		return nil, err
	}
	cmds := make([]*redis.StringSliceCmd, len(live))
	if _, err := c.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, n := range live {
			cmds[i] = p.SMembers(ctx, model.GroupIdxKey(app, group, n))
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("registry: SMEMBERS: %w", err)
	}
	seen := map[string]struct{}{}
	for _, cmd := range cmds {
		for _, uid := range cmd.Val() {
			seen[uid] = struct{}{}
		}
	}
	if len(seen) > MaxGroupMembers {
		return nil, ErrGroupTooLarge
	}
	uids := make([]string, 0, len(seen))
	for uid := range seen {
		uids = append(uids, uid)
	}
	slices.Sort(uids)

	var out []SubjectConns
	for chunk := range slices.Chunk(uids, listGroupBatch) {
		subs := make([]string, len(chunk))
		for i, uid := range chunk {
			subs[i] = model.BizGroup(group, uid).String()
		}
		many, err := conns.LookupMany(ctx, app, subs, live)
		if err != nil {
			return nil, err
		}
		for i, m := range many {
			if len(m) > 0 {
				out = append(out, SubjectConns{UID: chunk[i], Conns: m})
			}
		}
	}
	return out, nil
}
