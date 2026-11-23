package cluster

import "testing"

func TestKeysFormat(t *testing.T) {
	k := Keys{Prefix: "fp:im"}
	if got := k.Nodes(); got != "fp:im:node" {
		t.Fatalf("Nodes=%q", got)
	}
	if got := k.Srv("a1"); got != "fp:im:srv:a1" {
		t.Fatalf("Srv=%q", got)
	}
	if got := k.NodeChannel("n1"); got != "fp:im:node:n1" {
		t.Fatalf("NodeChannel=%q", got)
	}
}

// fp 核心与 fp-im 共用同一个 Redis 时，两组键不能撞。
func TestKeysPrefixesDoNotOverlap(t *testing.T) {
	im, core := Keys{Prefix: "fp:im"}, Keys{Prefix: "fp:core"}
	if got := core.Nodes(); got != "fp:core:node" {
		t.Fatalf("core Nodes=%q", got)
	}
	if got := core.Srv("a1"); got != "fp:core:srv:a1" {
		t.Fatalf("core Srv=%q", got)
	}
	if got := core.NodeChannel("n1"); got != "fp:core:node:n1" {
		t.Fatalf("core NodeChannel=%q", got)
	}
	// 同一 app / 节点 id 下，两组键两两不等
	imKeys := []string{im.Nodes(), im.Srv("x"), im.NodeChannel("x")}
	coreKeys := []string{core.Nodes(), core.Srv("x"), core.NodeChannel("x")}
	for _, a := range imKeys {
		for _, b := range coreKeys {
			if a == b {
				t.Fatalf("键重叠：%q", a)
			}
		}
	}
}
