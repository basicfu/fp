package registry

import (
	"context"
	"log/slog"
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
	serving map[string]bool             // 本节点正在为哪些 app 持有 server 流（当下真实状态）
	tracked map[string]bool             // 需要刷新 srv 表的 app
	everSrv map[string]bool             // 曾经服务过哪些 app：全断之后仍要有机会去 HDEL 它的条目

	// servingSig 是 SetServing 通知 Run 里的消费协程"serving 变了，去重写一次
	// 服务表"的信号。容量必须是 1 且塞不进就丢弃：
	//   - 不能是无缓冲 channel：SetServing 在持有 l.mu 期间之外发送，但如果
	//     消费协程恰好没在 select 上等待（比如正卡在一次 HSET 的网络往返
	//     里），无缓冲发送会阻塞调用方，等于让"增删一条本地流"这个本该是纯
	//     内存操作的调用被 Redis 的网络延迟拖住。
	//   - 不能是大缓冲：流在几百毫秒内密集增减时会连续触发很多次信号，若都
	//     被塞进去，消费协程要排空一整串"已经过时"的通知才能追上当下状态，
	//     等于失去了合并效果，Redis 写次数和不合并时一样多。
	//   - 容量 1 恰好实现"多次通知合并成一次"：不管信号被触发几次，消费协程
	//     只需要知道"有变化待处理"这一个 bit，真正要写的内容永远是消费时
	//     去读 l.serving 的当下值，不是信号里带的值——所以合并绝不会写错。
	servingSig chan struct{}
}

func NewLiveness(c redis.UniversalClient, nodeID string, heartbeat, deadAfter time.Duration) *Liveness {
	return &Liveness{
		c: c, nodeID: nodeID, heartbeat: heartbeat, deadAfter: deadAfter, now: time.Now,
		live: map[string]int64{}, srv: map[string]map[string]int64{}, serving: map[string]bool{},
		tracked: map[string]bool{}, everSrv: map[string]bool{},
		servingSig: make(chan struct{}, 1),
	}
}

func (l *Liveness) NodeID() string { return l.nodeID }

// nodeFieldTTLFactor 是心跳字段过期时长相对判死阈值的倍数，
// 字段 TTL = deadAfter × 这个倍数（默认判死 10 秒 → 字段 100 秒）。
//
// 为什么是"若干倍"而不是等于判死阈值：字段过期不是判死机制——判死由
// Refresh 读到的时间戳与 cutoff 比较决定，一条过期但还没被删掉的字段
// 早就不算活的了。字段过期只是垃圾回收：让崩溃/被 kill 的进程（来不及
// 走 Deregister）留下的条目最终自己消失，否则节点标识每次启动都是新值，
// 每一次发版、每一次崩溃都往表里永久加一个字段，而每个节点每个心跳周期
// 还要把这张不断增长的表整个读一遍。
//
// 取十倍的理由：每次心跳（默认 3 秒）都会把 TTL 重新刷满，所以只要明显
// 大于心跳间隔就不会误删活节点；留出十个判死窗口的余量，是为了让"Redis
// 连续写失败但进程仍然健康"这种情况（写失败时 TTL 刷不上）不会把一个活
// 节点的条目删掉——真删了也只是让它在别人眼里提前消失，但那属于用一个
// 垃圾回收机制去影响正确性路径，不值得。倍数再大就失去回收意义了。
const nodeFieldTTLFactor = 10

// fieldTTL 是写进两张心跳表的字段过期时长。
func (l *Liveness) fieldTTL() time.Duration { return l.deadAfter * nodeFieldTTLFactor }

// Beat 写自己的心跳：节点表一条，每个正在服务的 app 各一条，都带字段级过期。
//
// 字段级过期（HEXPIRE）与连接表用的是同一套机制，但这两张表的键结构与连接
// 表不同：连接表的键带 Cluster hash tag（fp:im:{app:subject}:conn，每个
// subject 一个键），而 fp:im:node 与 fp:im:srv:{app} 是不带 hash tag 的
// 普通键、全集群共用一个。这对 HEXPIRE 没有影响：它和 HSET 一样是单键
// 命令，Cluster 下只需要这一个键所在的槽，不存在跨槽问题；这里也没有把
// 两张表的写放进同一个事务/脚本的需求，管道里各自独立发出即可。
func (l *Liveness) Beat(ctx context.Context) error {
	ts := l.now().UnixMilli()
	l.mu.RLock()
	apps := make([]string, 0, len(l.serving))
	for a := range l.serving {
		apps = append(apps, a)
	}
	l.mu.RUnlock()
	ttl := l.fieldTTL()
	_, err := l.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, model.KeyNodes, l.nodeID, ts)
		p.HExpire(ctx, model.KeyNodes, ttl, l.nodeID)
		for _, a := range apps {
			p.HSet(ctx, model.SrvKey(a), l.nodeID, ts)
			p.HExpire(ctx, model.SrvKey(a), ttl, l.nodeID)
		}
		return nil
	})
	return err
}

// SelfPresent 报告 Redis 的节点表里当前还有没有本节点的条目。
//
// 给 Gap（Redis 断线重连）后的重登记判定用：设计文档第七节写的前置条件是
// "若发现自己在 fp:im:node 里的条目消失（Redis 重启过、数据没了）才需要
// 全量重登记"。调用方必须在补写心跳**之前**调用它，否则那次心跳会把条目
// 重新创建出来，这个判断就永远返回 true。
func (l *Liveness) SelfPresent(ctx context.Context) (bool, error) {
	return l.c.HExists(ctx, model.KeyNodes, l.nodeID).Result()
}

// Deregister 删掉本节点在两张心跳表里的条目，优雅关闭时调用。
//
// 与字段过期是互补关系而不是重复：字段过期覆盖所有退出方式（含崩溃、被
// kill），但要等满一个 TTL；这里是正常退出时的即时清理，让别的节点下一次
// Refresh 就看不到我，不必等判死阈值。everSrv（而不是当下的 serving）是
// 正确的遍历集合：一个 app 的最后一条流断开后 serving 里已经没有它，只看
// serving 会漏删它的服务表条目。
func (l *Liveness) Deregister(ctx context.Context) error {
	l.mu.RLock()
	apps := make([]string, 0, len(l.everSrv))
	for a := range l.everSrv {
		apps = append(apps, a)
	}
	l.mu.RUnlock()
	_, err := l.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.HDel(ctx, model.KeyNodes, l.nodeID)
		for _, a := range apps {
			p.HDel(ctx, model.SrvKey(a), l.nodeID)
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

// Run 同时等待三个来源，直到 ctx 结束：
//   - 心跳 ticker：写节点存活表心跳、刷新本地快照，并且无条件重写一次服务表
//     （写的是此刻 l.serving 的真实内容，不是"哪个事件触发的")。
//     这条心跳兜底是必需项，不是优化：DropLocal（见 C3/B3）可能误剔一个仍然
//     健康、只是订阅者数暂时为 0 的节点，误剔之后该节点自己的服务表条目要
//     靠这条无条件的周期写自己长回来——纯事件驱动的 SetServing 不会有任何
//     事件触发这次"没有变化但需要重写"的写入。
//   - serving 信号：只重写服务表，不刷新快照、不写存活表，因为触发它的只是
//     本地 serving 集合变了，与"我是否还活着"和"其它节点的快照"无关。
//   - ctx.Done()：退出循环。
func (l *Liveness) Run(ctx context.Context) {
	t := time.NewTicker(l.heartbeat)
	defer t.Stop()
	// 启动时先跑一轮，和旧实现一样：节点不必等满一个心跳周期才第一次
	// 在存活表里露面。这一轮同时把服务表按当下真实状态写一遍，覆盖"Run
	// 启动前已经调过 SetServing"的情形。
	l.beatAndRefresh(ctx)
	l.writeServingTable(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.beatAndRefresh(ctx)
			l.writeServingTable(ctx)
		case <-l.servingSig:
			l.writeServingTable(ctx)
		}
	}
}

// beatAndRefresh 跑一轮心跳与快照刷新，失败留日志。
//
// 这两处错误原先是被 `_ =` 直接丢掉的（交接文档已把它列进"已知不足"）：
// Redis 故障时存活视图会静默冻结——本节点看到的还是故障前那一份快照，
// 转发照旧发给早已死掉的节点，而运维这边没有任何信号。
//
// 不做退避、不做重试：下一个心跳周期本来就会再跑一次，这里重试只会在
// Redis 已经不健康的时候雪上加霜。ctx 已经结束时不记日志——那是正常的
// 进程关闭，此时 Beat/Refresh 必然因为 context canceled 失败，为此喊一声
// 只会让每次发版的日志里都出现一条无意义的告警。
func (l *Liveness) beatAndRefresh(ctx context.Context) {
	if err := l.Beat(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("registry: 写心跳失败，本节点可能被其它节点判死", "node", l.nodeID, "err", err)
	}
	if err := l.Refresh(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("registry: 刷新存活视图失败，本节点的存活视图已陈旧", "node", l.nodeID, "err", err)
	}
}

// writeServingTable 是写服务表的唯一入口。它每次都重新读 l.serving 的当前
// 内容，绝不使用调用方（心跳 ticker 还是 serving 信号）传来的任何结论——
// 这是消除"两次写顺序倒挂"的关键：无论触发源是谁、触发了多少次、以什么
// 顺序到达，写出去的永远是调用这一刻的真实状态，旧的写入不可能覆盖新的
// 状态，因为"新旧"由这里读到 l.serving 的时刻决定，不是由信号发出的时刻
// 决定。
//
// 对每个曾经服务过的 app：在 serving 集合里就 HSET 自己的条目并刷新心跳
// 时间戳，不在集合里就 HDEL。用 everSrv 记住"曾经服务过哪些 app"是必要的：
// 只看当前 serving 集合的话，一个 app 的流全部断开后 serving 里已经没有它
// 的踪迹，再也不会有机会对它发出 HDEL，Redis 里的条目会永久残留。
func (l *Liveness) writeServingTable(ctx context.Context) {
	l.mu.RLock()
	apps := make([]string, 0, len(l.everSrv))
	for a := range l.everSrv {
		apps = append(apps, a)
	}
	on := make(map[string]bool, len(l.serving))
	for a := range l.serving {
		on[a] = true
	}
	l.mu.RUnlock()
	if len(apps) == 0 {
		return
	}
	ts := l.now().UnixMilli()
	ttl := l.fieldTTL()
	if _, err := l.c.Pipelined(ctx, func(p redis.Pipeliner) error {
		for _, a := range apps {
			if on[a] {
				p.HSet(ctx, model.SrvKey(a), l.nodeID, ts)
				// 与 Beat 里同样的字段级过期：这条路径（serving 信号触发）
				// 可能在两次心跳之间单独写一次服务表，不挂 TTL 的话，
				// 一个 HSET 出去而下一次心跳之前就崩掉的进程，会在服务表
				// 里留下一个永不过期的条目。
				p.HExpire(ctx, model.SrvKey(a), ttl, l.nodeID)
			} else {
				p.HDel(ctx, model.SrvKey(a), l.nodeID)
			}
		}
		return nil
	}); err != nil {
		// 不能静默吞掉：这正是这批改动要消灭的"节点持有活流却对外不宣告、
		// 该 app 上行被静默丢弃"症状，只是把触发原因从"锁外写序倒挂"换成了
		// "Redis 持续写失败"。至少要留一条日志，让运维能定位到是这里在丢写，
		// 而不是无声无息。下一次心跳或下一次 serving 信号会重试，这里不用
		// 自己做退避重试。
		slog.Warn("registry: 写服务表失败", "err", err, "apps", apps)
	}
}

// TrackApp 让 Refresh 开始拉该 app 的 srv 表。hub 第一次需要某 app 的 ServerNodes 时调用。
func (l *Liveness) TrackApp(app string) {
	l.mu.Lock()
	l.tracked[app] = true
	l.mu.Unlock()
}

// SetServing 是纯内存操作：修改 l.serving，发一个信号给 Run 里的消费协程，
// 立即返回。不再在这个方法里直接写 Redis。
//
// 旧实现在锁外直接 HSET/HDEL：两个协程各自"锁内判定、锁外写 Redis"，网络
// 往返的耗时不确定，后发出的写可能先落地，把状态倒挂成与任何一次调用的
// 参数都对不上的结果（完整推演见 fix-batch-1-brief.md 问题 2）。改成这里
// 只落内存、真正的写交给单一消费者协程串行处理，从根上消除"多个写者互相
// 竞争"这个前提，不需要在 Redis 侧加锁或用 CAS。
func (l *Liveness) SetServing(ctx context.Context, app string, on bool) error {
	l.mu.Lock()
	if on {
		l.serving[app] = true
		l.tracked[app] = true
	} else {
		delete(l.serving, app)
	}
	l.everSrv[app] = true
	l.mu.Unlock()
	select {
	case l.servingSig <- struct{}{}:
	default: // 已经有一个待处理的信号，合并即可：Run 消费时读到的是最新状态
	}
	return nil
}

// DropLocal 把某节点从本节点的内存快照里临时剔除。
// 用于转发时发现目标节点没有订阅（已崩溃）后，避免自己反复选中它。
// 只影响本节点的内存视图，不动 Redis，也不通知任何人：订阅者数为 0 不完全
// 等于崩溃，目标节点的 Redis 连接正在重连时也会返回 0，此时它活得好好的。
// 只剔除本地视图的话，误判的代价被限制在本节点，且下一次 Refresh 若该节点
// 仍在心跳会自动把它放回快照——这也是为什么 B2 的心跳兜底是必需的：这里被
// 误剔的健康节点，它自己的服务表条目在被剔掉期间不会因为任何人的动作而
// 消失，因为没人碰过 Redis；等它自己的心跳 ticker 走到下一轮，会用真实状态
// 把条目重新写一遍——不需要靠"被剔掉的一方"或"剔它的一方"做任何补偿。
func (l *Liveness) DropLocal(node string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.live, node)
	for _, m := range l.srv {
		delete(m, node)
	}
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
