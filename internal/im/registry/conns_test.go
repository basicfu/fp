package registry

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/basicfu/fp/internal/testsupport"
	"github.com/redis/go-redis/v9"
)

func newConns(t *testing.T, fieldTTL time.Duration) (*Conns, *redis.Client) {
	rdb := testsupport.NewTestRedis(t)
	run, err := redisx.NewRunner(rdb, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(run.Close)
	return NewConns(rdb, run, fieldTTL), rdb
}

func meta(node string) model.ConnMeta {
	return model.ConnMeta{Node: node, OS: "linux", At: 1}
}

func TestHandshakePolicyMatrix(t *testing.T) {
	ctx := context.Background()
	live := []string{"im-a", "im-b"}
	for _, tc := range []struct {
		name     string
		policy   model.Policy
		limit    int
		existing int // 先登记多少条活连接（都在 im-b）
		wantRej  bool
		wantKick int
	}{
		{"replace 无旧连接", model.PolicyReplace, 0, 0, false, 0},
		{"replace 顶掉 2 条", model.PolicyReplace, 0, 2, false, 2},
		{"reject 无旧连接", model.PolicyReject, 0, 0, false, 0},
		{"reject 有旧连接", model.PolicyReject, 0, 1, true, 0},
		{"limit 2 未满", model.PolicyLimit, 2, 1, false, 0},
		{"limit 2 已满", model.PolicyLimit, 2, 2, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newConns(t, 30*time.Minute)
			for i := 0; i < tc.existing; i++ {
				if _, err := c.Handshake(ctx, "a1", "u:1", fmt.Sprintf("old%d", i), meta("im-b"), model.PolicyNone, 0, live); err != nil {
					t.Fatalf("预置连接：%v", err)
				}
			}
			res, err := c.Handshake(ctx, "a1", "u:1", "new", meta("im-a"), tc.policy, tc.limit, live)
			if err != nil {
				t.Fatalf("Handshake: %v", err)
			}
			if res.Rejected != tc.wantRej {
				t.Fatalf("Rejected=%v，期望 %v", res.Rejected, tc.wantRej)
			}
			if len(res.Kicked) != tc.wantKick {
				t.Fatalf("Kicked=%d 条，期望 %d", len(res.Kicked), tc.wantKick)
			}
			for _, k := range res.Kicked {
				if k.Node != "im-b" {
					t.Fatalf("被顶替的连接应报告所在节点 im-b，实际 %q", k.Node)
				}
			}
			got, _ := c.Lookup(ctx, "a1", "u:1", live)
			if tc.wantRej {
				if _, ok := got["new"]; ok {
					t.Fatal("被拒绝的连接不能被登记")
				}
			} else if got["new"].Node != "im-a" {
				t.Fatalf("登记后应能查到自己，实际 %+v", got)
			}
		})
	}
}

func TestHandshakeCleansDeadNodeEntriesAndIgnoresThemForLimit(t *testing.T) {
	ctx := context.Background()
	c, _ := newConns(t, 30*time.Minute)
	// im-dead 的两条残留 + im-b 的一条活连接
	for i, n := range []string{"im-dead", "im-dead", "im-b"} {
		_, _ = c.Handshake(ctx, "a1", "u:1", fmt.Sprintf("%s-%d", n, i), meta(n), model.PolicyNone, 0, []string{"im-dead", "im-b"})
	}
	res, err := c.Handshake(ctx, "a1", "u:1", "new", meta("im-a"), model.PolicyLimit, 2, []string{"im-a", "im-b"})
	if err != nil || res.Rejected {
		t.Fatalf("残留不该算进 limit：err=%v rejected=%v", err, res.Rejected)
	}
	got, _ := c.Lookup(ctx, "a1", "u:1", []string{"im-a", "im-b", "im-dead"})
	if len(got) != 2 {
		t.Fatalf("脚本应已 HDEL 死节点残留，剩 im-b 与 new 两条，实际 %d 条：%+v", len(got), got)
	}
}

func TestLookupFiltersDeadNodes(t *testing.T) {
	ctx := context.Background()
	c, _ := newConns(t, 30*time.Minute)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c1", meta("im-a"), model.PolicyNone, 0, nil)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c2", meta("im-dead"), model.PolicyNone, 0, nil)
	got, err := c.Lookup(ctx, "a1", "u:1", []string{"im-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["c1"].Node != "im-a" {
		t.Fatalf("读路径必须过滤死节点条目，实际 %+v", got)
	}
}

func TestFieldTTLAndRenew(t *testing.T) {
	ctx := context.Background()
	c, rdb := newConns(t, 2*time.Second)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c1", meta("im-a"), model.PolicyReplace, 0, []string{"im-a"})
	ttl, err := rdb.HTTL(ctx, model.ConnKey("a1", "u:1"), "c1").Result()
	if err != nil || len(ttl) != 1 || ttl[0] < 1 || ttl[0] > 2 {
		t.Fatalf("握手后 field 应带 2 秒 TTL，实际 %v (%v)", ttl, err)
	}
	run, _ := redisx.NewRunner(rdb, 0, 1)
	defer run.Close()
	c2 := NewConns(rdb, run, 60*time.Second)
	if err := c2.Renew(ctx, "a1", "u:1", []string{"c1"}); err != nil {
		t.Fatal(err)
	}
	ttl, _ = rdb.HTTL(ctx, model.ConnKey("a1", "u:1"), "c1").Result()
	if ttl[0] < 55 {
		t.Fatalf("Renew 后 TTL 应接近 60 秒，实际 %d", ttl[0])
	}
}

func TestKickAllAndKickOne(t *testing.T) {
	ctx := context.Background()
	c, _ := newConns(t, 30*time.Minute)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c1", meta("im-a"), model.PolicyNone, 0, nil)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c2", meta("im-b"), model.PolicyNone, 0, nil)
	one, err := c.KickOne(ctx, "a1", "u:1", "c2")
	if err != nil || one == nil || one.Node != "im-b" {
		t.Fatalf("KickOne 应返回 c2 所在节点：%+v %v", one, err)
	}
	if again, _ := c.KickOne(ctx, "a1", "u:1", "c2"); again != nil {
		t.Fatal("已删除的 field 再踢应返回 nil")
	}
	all, err := c.KickAll(ctx, "a1", "u:1")
	if err != nil || len(all) != 1 || all[0].ConnID != "c1" {
		t.Fatalf("KickAll 应返回剩下的 c1：%+v %v", all, err)
	}
	if got, _ := c.Lookup(ctx, "a1", "u:1", []string{"im-a", "im-b"}); len(got) != 0 {
		t.Fatal("KickAll 后 hash 应为空")
	}
}

func TestRemoveDeletesKeyWhenEmpty(t *testing.T) {
	ctx := context.Background()
	c, rdb := newConns(t, time.Minute)
	_, _ = c.Handshake(ctx, "a1", "u:1", "c1", meta("im-a"), model.PolicyNone, 0, nil)
	if err := c.Remove(ctx, "a1", "u:1", "c1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.Exists(ctx, model.ConnKey("a1", "u:1")).Result(); n != 0 {
		t.Fatal("最后一个 field 删掉后 Redis 应自动删 key，在线 subject 数才等于 key 数")
	}
}
