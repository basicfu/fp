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
