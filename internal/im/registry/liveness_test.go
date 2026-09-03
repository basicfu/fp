package registry

import (
	"context"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/testsupport"
)

func TestLivenessSeesPeersAndDropsDead(t *testing.T) {
	ctx := context.Background()
	rdb := testsupport.NewTestRedis(t)
	now := time.UnixMilli(1_000_000)
	clock := func() time.Time { return now }
	a := NewLiveness(rdb, "im-a", 3*time.Second, 10*time.Second)
	b := NewLiveness(rdb, "im-b", 3*time.Second, 10*time.Second)
	a.now, b.now = clock, clock

	if err := a.Beat(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !b.IsLive("im-a") || !b.IsLive("im-b") {
		t.Fatalf("b 应看到 a 与自己：%v", b.LiveNodes())
	}
	now = now.Add(11 * time.Second) // a 不再心跳
	_ = b.Refresh(ctx)
	if b.IsLive("im-a") {
		t.Fatal("超过 dead_after 未心跳的节点必须被判死")
	}
	if !b.IsLive("im-b") {
		t.Fatal("自己永远算活的")
	}
}

func TestLivenessServerNodes(t *testing.T) {
	ctx := context.Background()
	rdb := testsupport.NewTestRedis(t)
	a := NewLiveness(rdb, "im-a", 3*time.Second, 10*time.Second)
	b := NewLiveness(rdb, "im-b", 3*time.Second, 10*time.Second)
	if err := a.SetServing(ctx, "a1", true); err != nil {
		t.Fatal(err)
	}
	_ = a.Beat(ctx)
	b.TrackApp("a1")
	_ = b.Refresh(ctx)
	if got := b.ServerNodes("a1"); len(got) != 1 || got[0] != "im-a" {
		t.Fatalf("b 应看到 im-a 持有 a1 的 server 流，实际 %v", got)
	}
	_ = a.SetServing(ctx, "a1", false)
	_ = b.Refresh(ctx)
	if got := b.ServerNodes("a1"); len(got) != 0 {
		t.Fatalf("SetServing(false) 应立即 HDEL，实际 %v", got)
	}
}
