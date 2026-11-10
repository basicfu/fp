package registry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/testsupport"
	"github.com/redis/go-redis/v9"
)

// fakeGroups 模拟 hub 的分组计数：改完计数再 MarkDirty，与 hub 的约定一致。
type fakeGroups struct {
	mu  sync.Mutex
	n   map[[2]string]map[string]int // {app, group} → uid → 连接数
	idx *GroupIndex
	// afterSnapshot 在 GroupSnapshot 取完快照、返回之前调用，用来制造
	// "快照之后又变了"的交错。
	afterSnapshot func()
}

func newFakeGroups() *fakeGroups { return &fakeGroups{n: map[[2]string]map[string]int{}} }

func (f *fakeGroups) add(app, group, uid string, delta int) {
	f.mu.Lock()
	k := [2]string{app, group}
	if f.n[k] == nil {
		f.n[k] = map[string]int{}
	}
	f.n[k][uid] += delta
	if f.n[k][uid] <= 0 {
		delete(f.n[k], uid)
		if len(f.n[k]) == 0 {
			delete(f.n, k)
		}
	}
	f.mu.Unlock()
	f.idx.MarkDirty(app, group, uid)
}

func (f *fakeGroups) GroupLocalCount(app, group, uid string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n[[2]string{app, group}][uid]
}

func (f *fakeGroups) GroupSnapshot(app, group string) []string {
	f.mu.Lock()
	var out []string
	for uid := range f.n[[2]string{app, group}] {
		out = append(out, uid)
	}
	hook := f.afterSnapshot
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return out
}

func (f *fakeGroups) GroupsOf() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][2]string
	for k := range f.n {
		out = append(out, k)
	}
	return out
}

func (f *fakeGroups) members(app, group string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for uid := range f.n[[2]string{app, group}] {
		out = append(out, uid)
	}
	slices.Sort(out)
	return out
}

// faultHook 在 fail 为 true 时让每条命令和管道都失败，模拟 Redis 不可用。
// 同时记下每条发出去的命令名，供"快照为空只发 DEL"这类断言用。
type faultHook struct {
	fail atomic.Bool
	mu   sync.Mutex
	cmds []string
}

func (h *faultHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *faultHook) record(cmds ...redis.Cmder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range cmds {
		h.cmds = append(h.cmds, c.Name())
	}
}

func (h *faultHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.fail.Load() {
			cmd.SetErr(errFault)
			return errFault
		}
		h.record(cmd)
		return next(ctx, cmd)
	}
}

func (h *faultHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		if h.fail.Load() {
			for _, c := range cmds {
				c.SetErr(errFault)
			}
			return errFault
		}
		h.record(cmds...)
		return next(ctx, cmds)
	}
}

func (h *faultHook) take() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.cmds
	h.cmds = nil
	return out
}

var errFault = errors.New("模拟 Redis 故障")

// newGroupIndex 建一个写协程：写用带故障钩子的独立客户端，断言用干净的 rdb。
// 独立客户端是必须的：testsupport 的 rdb 是进程内共享单例，往它上面挂钩子会
// 串到别的测试。
func newGroupIndex(t *testing.T) (*GroupIndex, *fakeGroups, *faultHook, *redis.Client) {
	t.Helper()
	rdb := testsupport.NewTestRedis(t)
	w := redis.NewClient(rdb.Options())
	hook := &faultHook{}
	w.AddHook(hook)
	t.Cleanup(func() { w.Close() })
	st := newFakeGroups()
	g := NewGroupIndex(w, "n1", 30*time.Minute, st)
	// 测试里把退避缩短，不然一次失败就要等满 1 秒
	g.backoffMin, g.backoffMax = 10*time.Millisecond, 40*time.Millisecond
	st.idx = g
	return g, st, hook, rdb
}

func runIndex(t *testing.T, g *GroupIndex) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func redisMembers(t *testing.T, rdb *redis.Client, key string) []string {
	t.Helper()
	m, err := rdb.SMembers(context.Background(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(m)
	return m
}

// waitMembers 轮询直到 Redis 里的成员等于 want（都已排序）。
func waitMembers(t *testing.T, rdb *redis.Client, key string, want func() []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, w := redisMembers(t, rdb, key), want()
		if slices.Equal(got, w) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s 成员 %v，期望 %v", key, got, w)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fixed(v ...string) func() []string {
	slices.Sort(v)
	return func() []string { return v }
}

// TestGroupIndexAddWritesWithTTL 守住"第一次 SADD 新建的 key 必须有 TTL"：
// 节点崩溃后没人续期，没有 TTL 的 key 会永久残留。
func TestGroupIndexAddWritesWithTTL(t *testing.T) {
	g, st, _, rdb := newGroupIndex(t)
	runIndex(t, g)
	key := model.GroupIdxKey("a1", "r", "n1")

	st.add("a1", "r", "x", 1)
	waitMembers(t, rdb, key, fixed("x"))
	if ttl := rdb.PTTL(context.Background(), key).Val(); ttl <= 0 {
		t.Fatalf("新建的索引 key 必须带 TTL，PTTL=%v", ttl)
	}

	st.add("a1", "r", "x", -1)
	waitMembers(t, rdb, key, fixed())
}

// TestGroupIndexRapidAddRemoveConverges：add/remove 快速交替、标脏被合并时，
// Redis 最终与本地计数一致——写的永远是写协程读到的当前状态。
func TestGroupIndexRapidAddRemoveConverges(t *testing.T) {
	g, st, _, rdb := newGroupIndex(t)
	runIndex(t, g)
	key := model.GroupIdxKey("a1", "r", "n1")

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 1))
			held := map[string]int{}
			for i := 0; i < 300; i++ {
				uid := fmt.Sprintf("u%d", r.IntN(6))
				if held[uid] > 0 && r.IntN(2) == 0 {
					held[uid]--
					st.add("a1", "r", uid, -1)
				} else {
					held[uid]++
					st.add("a1", "r", uid, 1)
				}
			}
			// 收尾时放掉一半 uid 的全部连接：只增不减的实现也能"一致"于
			// 全员在线的终态，必须让终态里真有被删掉的成员
			for uid, n := range held {
				if uid < "u3" {
					for ; n > 0; n-- {
						st.add("a1", "r", uid, -1)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	waitMembers(t, rdb, key, func() []string { return st.members("a1", "r") })
	if m := st.members("a1", "r"); len(m) == 0 || slices.Contains(m, "u0") {
		t.Fatalf("终态应是 u3~u5 的子集且非空，实际 %v", m)
	}
}

// TestGroupIndexRetriesAfterWriteFailure：写失败的那批要重新标脏、退避后补写，
// 不能因为一次失败就永远缺一个成员。
func TestGroupIndexRetriesAfterWriteFailure(t *testing.T) {
	g, st, hook, rdb := newGroupIndex(t)
	runIndex(t, g)
	key := model.GroupIdxKey("a1", "r", "n1")

	hook.fail.Store(true)
	st.add("a1", "r", "x", 1)
	st.add("a1", "r", "y", 1)
	time.Sleep(100 * time.Millisecond) // 让它至少失败、退避几轮
	if got := redisMembers(t, rdb, key); len(got) != 0 {
		t.Fatalf("故障期间不该写进去，实际 %v", got)
	}
	st.add("a1", "r", "y", -1) // 故障期间又变了：恢复后要写的是现在的状态
	hook.fail.Store(false)
	waitMembers(t, rdb, key, fixed("x"))
	if ttl := rdb.PTTL(context.Background(), key).Val(); ttl <= 0 {
		t.Fatalf("重试写入也必须带 TTL，PTTL=%v", ttl)
	}
}

// TestGroupIndexRewriteAllReplacesStale：整份重写要清掉 Redis 里本节点已经
// 没有的成员（比如 Redis 主从切换回滚出来的旧数据），并且带 TTL。
func TestGroupIndexRewriteAllReplacesStale(t *testing.T) {
	g, st, _, rdb := newGroupIndex(t)
	runIndex(t, g)
	ctx := context.Background()
	key := model.GroupIdxKey("a1", "r", "n1")

	st.add("a1", "r", "x", 1)
	waitMembers(t, rdb, key, fixed("x"))
	rdb.SAdd(ctx, key, "ghost")
	rdb.Persist(ctx, key)

	if err := g.RewriteAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := redisMembers(t, rdb, key); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("重写后应只剩本地真实成员，实际 %v", got)
	}
	if ttl := rdb.PTTL(ctx, key).Val(); ttl <= 0 {
		t.Fatalf("重写后的 key 必须带 TTL，PTTL=%v", ttl)
	}
}

// TestGroupIndexRewriteDoesNotOverwriteNewerState 守住"重写不覆盖更新的状态"：
// 快照之后发生的变化已经标脏，要在重写之后再写一遍。
func TestGroupIndexRewriteDoesNotOverwriteNewerState(t *testing.T) {
	g, st, _, rdb := newGroupIndex(t)
	runIndex(t, g)
	ctx := context.Background()
	key := model.GroupIdxKey("a1", "r", "n1")

	st.add("a1", "r", "gone", 1)
	waitMembers(t, rdb, key, fixed("gone"))

	var once sync.Once
	st.afterSnapshot = func() {
		// 快照里有 gone、没有 late；快照之后两者都变了
		once.Do(func() {
			go func() {
				st.add("a1", "r", "late", 1)
				st.add("a1", "r", "gone", -1)
			}()
			time.Sleep(20 * time.Millisecond)
		})
	}
	if err := g.RewriteAll(ctx); err != nil {
		t.Fatal(err)
	}
	waitMembers(t, rdb, key, fixed("late"))
}

// TestGroupIndexRewriteConcurrentConverges：重写与增量并发、反复交错，最终一致。
func TestGroupIndexRewriteConcurrentConverges(t *testing.T) {
	g, st, _, rdb := newGroupIndex(t)
	runIndex(t, g)
	ctx := context.Background()
	key := model.GroupIdxKey("a1", "r", "n1")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := g.RewriteAll(ctx); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	r := rand.New(rand.NewPCG(7, 7))
	held := map[string]int{}
	for i := 0; i < 400; i++ {
		uid := fmt.Sprintf("u%d", r.IntN(8))
		if held[uid] > 0 && r.IntN(2) == 0 {
			held[uid]--
			st.add("a1", "r", uid, -1)
		} else {
			held[uid]++
			st.add("a1", "r", uid, 1)
		}
	}
	close(stop)
	wg.Wait()
	waitMembers(t, rdb, key, func() []string { return st.members("a1", "r") })
}

// TestGroupIndexRewriteEmptySnapshotOnlyDeletes 守住"快照为空时只 DEL"：
// 空 MULTI{DEL; EXPIRE} 没意义，SADD 一个空列表更是语法错误。
func TestGroupIndexRewriteEmptySnapshotOnlyDeletes(t *testing.T) {
	g, st, hook, rdb := newGroupIndex(t)
	runIndex(t, g)
	ctx := context.Background()
	key := model.GroupIdxKey("a1", "r", "n1")

	st.add("a1", "r", "x", 1)
	waitMembers(t, rdb, key, fixed("x"))
	rdb.SAdd(ctx, key, "ghost") // 本节点已经没有、但 Redis 里还留着
	st.add("a1", "r", "x", -1)
	waitMembers(t, rdb, key, fixed("ghost"))

	hook.take()
	if err := g.RewriteAll(ctx); err != nil {
		t.Fatal(err)
	}
	if n := rdb.Exists(ctx, key).Val(); n != 0 {
		t.Fatalf("快照为空时 key 应被删掉，实际还在：%v", redisMembers(t, rdb, key))
	}
	if cmds := hook.take(); !slices.Equal(cmds, []string{"del"}) {
		t.Fatalf("快照为空时只应发 DEL，实际 %v", cmds)
	}
}

// TestGroupIndexDrainAfterRunStopped：关闭时 Run 已经随 serve 的 ctx 退出，
// Drain 仍要把剩下的脏集合写完——否则本节点的成员要等 30 分钟 TTL 才消失。
func TestGroupIndexDrainAfterRunStopped(t *testing.T) {
	g, st, hook, rdb := newGroupIndex(t)
	cancel := runIndex(t, g)
	key := model.GroupIdxKey("a1", "r", "n1")

	st.add("a1", "r", "x", 1)
	st.add("a1", "r", "y", 1)
	waitMembers(t, rdb, key, fixed("x", "y"))
	cancel()

	st.add("a1", "r", "x", -1)
	st.add("a1", "r", "y", -1)
	// 第一次写失败也要在预算内重试
	hook.fail.Store(true)
	go func() { time.Sleep(30 * time.Millisecond); hook.fail.Store(false) }()
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := g.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := redisMembers(t, rdb, key); len(got) != 0 {
		t.Fatalf("Drain 后本节点索引应为空，实际 %v", got)
	}
}

func TestListGroupMergesLiveNodesAndVerifies(t *testing.T) {
	ctx := context.Background()
	c, rdb := newConns(t, 30*time.Minute)
	live := []string{"n1", "n2"}
	rdb.SAdd(ctx, model.GroupIdxKey("a1", "r", "n1"), "a", "b", "ghost")
	rdb.SAdd(ctx, model.GroupIdxKey("a1", "r", "n2"), "b", "c")
	rdb.SAdd(ctx, model.GroupIdxKey("a1", "r", "n3"), "d") // 死节点：整份跳过
	rdb.SAdd(ctx, model.GroupIdxKey("a1", "w", "n1"), "a") // 别的分组不串
	for _, x := range []struct{ uid, conn, node string }{
		{"a", "ca", "n1"}, {"b", "cb1", "n1"}, {"b", "cb2", "n2"}, {"c", "cc", "n2"}, {"d", "cd", "n3"},
	} {
		if _, err := c.Handshake(ctx, "a1", model.BizGroup("r", x.uid).String(), x.conn, meta(x.node), model.PolicyNone, 0, []string{"n1", "n2", "n3"}); err != nil {
			t.Fatal(err)
		}
	}
	// c 在 n3 上还有一条残留连接：连接表核实时也要按存活表滤掉
	if _, err := c.Handshake(ctx, "a1", model.BizGroup("r", "c").String(), "cc3", meta("n3"), model.PolicyNone, 0, []string{"n1", "n2", "n3"}); err != nil {
		t.Fatal(err)
	}

	got, err := ListGroup(ctx, rdb, c, "a1", "r", live)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, s := range got {
		var ids []string
		for cid := range s.Conns {
			ids = append(ids, cid)
		}
		slices.Sort(ids)
		lines = append(lines, s.UID+"="+strings.Join(ids, ","))
	}
	slices.Sort(lines)
	want := []string{"a=ca", "b=cb1,cb2", "c=cc"}
	if !slices.Equal(lines, want) {
		t.Fatalf("ListGroup=%v，期望 %v（ghost 只在索引里没连接，d 在死节点上）", lines, want)
	}
}

func TestListGroupRejectsBadGroupAndTooLarge(t *testing.T) {
	ctx := context.Background()
	c, rdb := newConns(t, 30*time.Minute)
	if _, err := ListGroup(ctx, rdb, c, "a1", "", []string{"n1"}); !errors.Is(err, model.ErrBadGroup) {
		t.Fatalf("空分组应报 ErrBadGroup（默认分组不建索引），实际 %v", err)
	}
	if _, err := ListGroup(ctx, rdb, c, "a1", "R", []string{"n1"}); !errors.Is(err, model.ErrBadGroup) {
		t.Fatalf("非法分组应报 ErrBadGroup，实际 %v", err)
	}

	// 两个节点各 5001 个、互不重复：合并后 10002 个，超上限
	for _, node := range []string{"n1", "n2"} {
		members := make([]interface{}, 0, 5001)
		for i := 0; i < 5001; i++ {
			members = append(members, fmt.Sprintf("%s-%d", node, i))
		}
		rdb.SAdd(ctx, model.GroupIdxKey("a1", "r", node), members...)
	}
	if _, err := ListGroup(ctx, rdb, c, "a1", "r", []string{"n1", "n2"}); !errors.Is(err, ErrGroupTooLarge) {
		t.Fatalf("超过 %d 个应报 ErrGroupTooLarge，实际 %v", MaxGroupMembers, err)
	}
	// 只算存活节点：只剩 n1 的 5001 个时不超限，且全都没连接 → 空结果
	got, err := ListGroup(ctx, rdb, c, "a1", "r", []string{"n1"})
	if err != nil || len(got) != 0 {
		t.Fatalf("只有索引没有连接时应返回空，实际 %d 条 err=%v", len(got), err)
	}
}
