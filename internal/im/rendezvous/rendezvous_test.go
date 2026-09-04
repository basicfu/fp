package rendezvous

import "testing"

func TestPickDeterministic(t *testing.T) {
	nodes := []string{"im-a", "im-b", "im-c"}
	a, ok := Pick(nodes, "u:1001")
	if !ok {
		t.Fatal("非空列表必须选出节点")
	}
	for i := 0; i < 100; i++ {
		if b, _ := Pick([]string{"im-c", "im-a", "im-b"}, "u:1001"); b != a {
			t.Fatalf("同一 key 在同一节点集合上必须恒选同节点，且与输入顺序无关：%s vs %s", a, b)
		}
	}
	if _, ok := Pick(nil, "u:1001"); ok {
		t.Fatal("空列表必须返回 false")
	}
}

// 删掉一个节点后，原本不在该节点上的 key 一个都不许挪。这就是用 rendezvous 而不是取模的理由。
func TestPickMinimalDisruption(t *testing.T) {
	nodes := []string{"im-a", "im-b", "im-c", "im-d"}
	before := map[string]string{}
	for i := 0; i < 2000; i++ {
		k := "u:" + string(rune('a'+i%26)) + string(rune('0'+i/26%10)) + string(rune('0'+i/260))
		before[k], _ = Pick(nodes, k)
	}
	after := []string{"im-a", "im-b", "im-d"}
	moved := 0
	for k, was := range before {
		now, _ := Pick(after, k)
		if was != "im-c" && now != was {
			t.Fatalf("key %s 原在 %s，删除 im-c 后不应移动，却到了 %s", k, was, now)
		}
		if was == "im-c" {
			moved++
		}
	}
	if moved == 0 {
		t.Fatal("测试数据没有覆盖到被删节点，样本太小")
	}
}

// TestRankFirstMatchesPick 断言 Rank 排出的第一名与 Pick 单独选出的结果一致：
// 两者共用同一套打破平局规则，任何一处改了都必须同步改另一处。
func TestRankFirstMatchesPick(t *testing.T) {
	nodes := []string{"im-a", "im-b", "im-c", "im-d", "im-e"}
	for _, key := range []string{"u:1001", "u:2002", "u:3003", "u:xyz"} {
		picked, ok := Pick(nodes, key)
		if !ok {
			t.Fatalf("Pick 应选出节点：key=%s", key)
		}
		ranked := Rank(nodes, key)
		if len(ranked) == 0 || ranked[0] != picked {
			t.Fatalf("key=%s：Rank 首选 %v 与 Pick 选出的 %s 不一致", key, ranked, picked)
		}
	}
}

// TestRankOrderIndependentOfInput 断言排序结果只取决于节点集合本身，
// 与传入时的先后顺序无关——转发要靠这个保证首发节点和下游节点各自独立
// 计算出的候选列表是同一个顺序。
func TestRankOrderIndependentOfInput(t *testing.T) {
	a := Rank([]string{"im-a", "im-b", "im-c", "im-d"}, "u:1001")
	b := Rank([]string{"im-d", "im-c", "im-b", "im-a"}, "u:1001")
	c := Rank([]string{"im-c", "im-a", "im-d", "im-b"}, "u:1001")
	if len(a) != len(b) || len(a) != len(c) {
		t.Fatalf("长度不一致：%v %v %v", a, b, c)
	}
	for i := range a {
		if a[i] != b[i] || a[i] != c[i] {
			t.Fatalf("排序结果必须与输入顺序无关：%v vs %v vs %v", a, b, c)
		}
	}
}

// TestRankDoesNotMutateInput 断言 Rank 返回的是副本，不改写调用方传入的切片
// （hub 会把 live.ServerNodes 返回的快照直接传给 Rank，若地址被复用会污染
// 那份快照，被别的调用方复用时读到错乱数据）。
func TestRankDoesNotMutateInput(t *testing.T) {
	in := []string{"im-z", "im-a", "im-m"}
	before := append([]string(nil), in...)
	_ = Rank(in, "u:1001")
	for i := range in {
		if in[i] != before[i] {
			t.Fatalf("Rank 不能修改入参：调用前 %v，调用后 %v", before, in)
		}
	}
}

// TestRankEmptyAndSingle 覆盖边界输入：空列表、单节点列表。
func TestRankEmptyAndSingle(t *testing.T) {
	if got := Rank(nil, "u:1"); len(got) != 0 {
		t.Fatalf("空列表应返回空结果，实际 %v", got)
	}
	if got := Rank([]string{"im-a"}, "u:1"); len(got) != 1 || got[0] != "im-a" {
		t.Fatalf("单节点列表应原样返回，实际 %v", got)
	}
}
