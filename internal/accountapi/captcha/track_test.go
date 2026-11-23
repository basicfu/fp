package captcha

import "testing"

// human 造一条先加速后减速、y 有抖动的轨迹,终点 dx = x。
func human(x int) [][3]int {
	var tr [][3]int
	for i := 0; i <= 30; i++ {
		p := float64(i) / 30
		ease := p * p * (3 - 2*p)
		tr = append(tr, [3]int{int(ease * float64(x)), i % 3, i * 25})
	}
	return tr
}

func TestCheckTrack(t *testing.T) {
	if !CheckTrack(human(300), 300) {
		t.Fatal("正常轨迹被拒")
	}
	linear := func() [][3]int {
		var tr [][3]int
		for i := 0; i <= 30; i++ {
			tr = append(tr, [3]int{i * 10, i % 2, i * 20})
		}
		return tr
	}()
	flatY := human(300)
	for i := range flatY {
		flatY[i][1] = 0
	}
	cases := map[string][][3]int{
		"匀速":     linear,
		"点太少":    human(300)[:5],
		"y 全为 0": flatY,
		"太快": func() [][3]int {
			tr := human(300)
			for i := range tr {
				tr[i][2] /= 10
			}
			return tr
		}(),
		"终点对不上": human(200),
		"时间倒流": func() [][3]int {
			tr := human(300)
			tr[5][2] = 0
			return tr
		}(),
	}
	for name, tr := range cases {
		if CheckTrack(tr, 300) {
			t.Fatalf("%s: 应被拒", name)
		}
	}
	long := make([][3]int, 201)
	if CheckTrack(long, 0) {
		t.Fatal("超过 200 点应被拒")
	}
}
