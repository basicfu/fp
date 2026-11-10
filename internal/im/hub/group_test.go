package hub_test

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/basicfu/fp/internal/im/hub"
	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
)

// dirtyLog 是 hub.GroupIndex 的假实现，记录每次 MarkDirty。
type dirtyLog struct {
	mu   sync.Mutex
	got  []string
	hub  *hub.Hub
	seen []int // 每次 MarkDirty 时读到的本地计数：必须已经是更新后的值
}

func (d *dirtyLog) MarkDirty(app, group, uid string) {
	// 在回调里回读 hub：如果 hub 持锁调用，这里会死锁；读到的计数也
	// 必须已经更新，否则写协程可能把旧状态写进 Redis 后不再被唤醒。
	n := d.hub.GroupLocalCount(app, group, uid)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(d.got, app+"/"+group+"/"+uid)
	d.seen = append(d.seen, n)
}

func (d *dirtyLog) snapshot() ([]string, []int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.got), slices.Clone(d.seen)
}

// TestForEachConnVisitsGroupSubject 守住续期与重登记不漏分组连接：
// ForEachConn 对 Parse 失败的主体是静默跳过的，分组写法一旦认不出来，
// 这些连接 30 分钟后就会从全网"消失"，而且没有任何报错。
func TestForEachConnVisitsGroupSubject(t *testing.T) {
	h, _, _, _, _ := hubtest.NewHub()
	h.AddConn(context.Background(), "a1", model.BizGroup("r", "x"), &hubtest.Conn{ConnID: "c1"}, model.ConnMeta{Node: "im-a"}, "")

	var got []model.Subject
	h.ForEachConn(func(app string, sub model.Subject, connID string, meta model.ConnMeta) {
		got = append(got, sub)
	})
	if len(got) != 1 || got[0] != model.BizGroup("r", "x") {
		t.Fatalf("ForEachConn 应遍历到 b.r:x，实际 %v", got)
	}
}

// TestGroupCountMarksDirtyOnlyOnZeroCrossings 守住"只在 0↔1 时标脏"：本节点
// 上同一 subject 还有别的连接时，索引里的成员不该变，标脏只是白写一次。
func TestGroupCountMarksDirtyOnlyOnZeroCrossings(t *testing.T) {
	ctx := context.Background()
	h, _, _, _, _ := hubtest.NewHub()
	d := &dirtyLog{hub: h}
	h.SetGroupIndex(d)
	sub := model.BizGroup("r", "x")

	h.AddConn(ctx, "a1", sub, &hubtest.Conn{ConnID: "c1"}, model.ConnMeta{Node: "im-a"}, "")
	h.AddConn(ctx, "a1", sub, &hubtest.Conn{ConnID: "c2"}, model.ConnMeta{Node: "im-a"}, "")
	if n := h.GroupLocalCount("a1", "r", "x"); n != 2 {
		t.Fatalf("两条连接后计数应为 2，实际 %d", n)
	}
	h.RemoveConn(ctx, "a1", sub, "c1", model.ReasonClient)
	h.RemoveConn(ctx, "a1", sub, "c1", model.ReasonClient) // 重复删除不能多减
	if got, _ := d.snapshot(); len(got) != 1 {
		t.Fatalf("断一条、还剩一条时不该再标脏，实际 %v", got)
	}
	if n := h.GroupLocalCount("a1", "r", "x"); n != 1 {
		t.Fatalf("断一条后计数应为 1，实际 %d", n)
	}
	h.RemoveConn(ctx, "a1", sub, "c2", model.ReasonClient)

	got, seen := d.snapshot()
	if !slices.Equal(got, []string{"a1/r/x", "a1/r/x"}) || !slices.Equal(seen, []int{1, 0}) {
		t.Fatalf("应在 0→1 与 1→0 各标脏一次且计数已更新，实际 %v 计数 %v", got, seen)
	}
	if gs := h.GroupsOf(); len(gs) != 0 {
		t.Fatalf("分组空了要从计数里摘掉，否则会一直占着 %d 个名额：%v", hub.MaxGroupsPerApp, gs)
	}

	// 默认分组与 fp 用户不建索引
	h.AddConn(ctx, "a1", model.Biz("x"), &hubtest.Conn{ConnID: "c3"}, model.ConnMeta{Node: "im-a"}, "")
	h.AddConn(ctx, "a1", model.User("x"), &hubtest.Conn{ConnID: "c4"}, model.ConnMeta{Node: "im-a"}, "")
	if got, _ := d.snapshot(); len(got) != 2 || len(h.GroupsOf()) != 0 {
		t.Fatalf("非分组主体不该计数或标脏，实际 %v %v", got, h.GroupsOf())
	}
}

func TestGroupCountWithoutIndex(t *testing.T) {
	h, _, _, _, _ := hubtest.NewHub()
	// 没注入索引时照样计数（分组上限要用），只是不标脏
	h.AddConn(context.Background(), "a1", model.BizGroup("r", "x"), &hubtest.Conn{ConnID: "c1"}, model.ConnMeta{Node: "im-a"}, "")
	if n := h.GroupLocalCount("a1", "r", "x"); n != 1 {
		t.Fatalf("计数应为 1，实际 %d", n)
	}
}

func TestGroupSnapshotAndGroupsOf(t *testing.T) {
	ctx := context.Background()
	h, _, _, _, _ := hubtest.NewHub()
	for i, s := range []struct{ app, group, uid string }{
		{"a1", "r", "x"}, {"a1", "r", "y"}, {"a1", "web", "x"}, {"a2", "r", "z"},
	} {
		h.AddConn(ctx, s.app, model.BizGroup(s.group, s.uid), &hubtest.Conn{ConnID: fmt.Sprint("c", i)}, model.ConnMeta{Node: "im-a"}, "")
	}
	snap := h.GroupSnapshot("a1", "r")
	slices.Sort(snap)
	if !slices.Equal(snap, []string{"x", "y"}) {
		t.Fatalf("GroupSnapshot(a1,r)=%v", snap)
	}
	if s := h.GroupSnapshot("a1", "nope"); len(s) != 0 {
		t.Fatalf("不存在的分组应为空，实际 %v", s)
	}
	gs := h.GroupsOf()
	slices.SortFunc(gs, func(a, b [2]string) int { return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1])) })
	if !slices.Equal(gs, [][2]string{{"a1", "r"}, {"a1", "web"}, {"a2", "r"}}) {
		t.Fatalf("GroupsOf()=%v", gs)
	}
}

// TestGroupAdmitCapsGroupsPerApp 守住每应用每节点的分组数上限：防的是回调
// 有 bug、每个连接回一个不同分组，造出无数个索引 key。
func TestGroupAdmitCapsGroupsPerApp(t *testing.T) {
	ctx := context.Background()
	h, _, _, _, _ := hubtest.NewHub()
	for i := 0; i < hub.MaxGroupsPerApp; i++ {
		if !h.GroupAdmit("a1", fmt.Sprintf("g%d", i)) {
			t.Fatalf("第 %d 个分组就被拒了", i+1)
		}
		h.AddConn(ctx, "a1", model.BizGroup(fmt.Sprintf("g%d", i), "u"), &hubtest.Conn{ConnID: fmt.Sprint("c", i)}, model.ConnMeta{Node: "im-a"}, "")
	}
	if h.GroupAdmit("a1", "extra") {
		t.Fatalf("第 %d 个分组应被拒", hub.MaxGroupsPerApp+1)
	}
	if !h.GroupAdmit("a1", "g0") {
		t.Fatal("已有的分组不受上限影响")
	}
	if !h.GroupAdmit("a2", "extra") {
		t.Fatal("上限按应用算，别的应用不受影响")
	}
	h.RemoveConn(ctx, "a1", model.BizGroup("g0", "u"), "c0", model.ReasonClient)
	if !h.GroupAdmit("a1", "extra") {
		t.Fatal("某个分组的连接全断之后应让出名额")
	}
}
