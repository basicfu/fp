package gen

import (
	"math"
	"math/rand/v2"
)

type gapShape struct {
	name   string
	inside func(r *rand.Rand) func(x, y float64) bool // 返回 TS 坐标系下的判定函数
}

// norm:非拼图形状统一在 [-1,1] 里定义,随机旋转后按 rad 映射进拼块框中心。
func norm(rot, rad float64, f func(u, v float64) bool) func(x, y float64) bool {
	cs, sn := math.Cos(rot), math.Sin(rot)
	return func(x, y float64) bool {
		u, v := (x-TS/2)/rad, (y-TS/2)/rad
		return f(u*cs+v*sn, -u*sn+v*cs)
	}
}

func inPoly(pts []vec, u, v float64) bool {
	in := false
	for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
		if (pts[i].y > v) != (pts[j].y > v) && u < (pts[j].x-pts[i].x)*(v-pts[i].y)/(pts[j].y-pts[i].y)+pts[i].x {
			in = !in
		}
	}
	return in
}

func starPts(n int, inner float64) []vec {
	pts := make([]vec, 2*n)
	for i := range pts {
		rr := 1.0
		if i%2 == 1 {
			rr = inner
		}
		t := -math.Pi/2 + float64(i)*math.Pi/float64(n)
		pts[i] = vec{rr * math.Cos(t), rr * math.Sin(t)}
	}
	return pts
}

var gapShapes = []gapShape{
	{"拼图", puzzleShape},
	{"圆形", func(r *rand.Rand) func(x, y float64) bool {
		return norm(0, 32, func(u, v float64) bool { return u*u+v*v <= 1 })
	}},
	{"五角星", func(r *rand.Rand) func(x, y float64) bool {
		p := starPts(5, 0.48)
		return norm((r.Float64()-0.5)*0.6, 38, func(u, v float64) bool { return inPoly(p, u, v+0.08) })
	}},
	{"爱心", func(r *rand.Rand) func(x, y float64) bool {
		return norm((r.Float64()-0.5)*0.5, 33, func(u, v float64) bool {
			x, y := u*1.15, -v*1.15+0.25 // 心形线 (x²+y²-1)³ - x²y³ ≤ 0
			a := x*x + y*y - 1
			return a*a*a-x*x*y*y*y <= 0
		})
	}},
	{"六边形", func(r *rand.Rand) func(x, y float64) bool {
		p := regular(0, 0, 1, r.Float64(), 6)
		return norm(0, 34, func(u, v float64) bool { return inPoly(p, u, v) })
	}},
	{"盾牌", func(r *rand.Rand) func(x, y float64) bool {
		return norm((r.Float64()-0.5)*0.4, 34, func(u, v float64) bool {
			if v < 0 {
				return math.Abs(u) <= 0.85 && v >= -0.9
			}
			return math.Abs(u) <= 0.85*math.Sqrt(math.Max(0, 1-v*v))
		})
	}},
	{"菱形", func(r *rand.Rand) func(x, y float64) bool {
		return norm((r.Float64()-0.5)*0.4, 36, func(u, v float64) bool { return math.Abs(u)/0.8+math.Abs(v) <= 1 })
	}},
	{"云朵", func(r *rand.Rand) func(x, y float64) bool {
		return norm(0, 36, func(u, v float64) bool {
			c := func(cx, cy, rr float64) bool { return (u-cx)*(u-cx)+(v-cy)*(v-cy) <= rr*rr }
			return c(-0.45, 0.15, 0.42) || c(0.1, -0.15, 0.55) || c(0.55, 0.2, 0.38) || (v >= 0.1 && v <= 0.55 && math.Abs(u) <= 0.7)
		})
	}},
	{"十字", func(r *rand.Rand) func(x, y float64) bool {
		return norm(r.Float64()*math.Pi/4, 36, func(u, v float64) bool {
			return (math.Abs(u) <= 0.33 && math.Abs(v) <= 1) || (math.Abs(v) <= 0.33 && math.Abs(u) <= 1)
		})
	}},
}

// puzzleShape:方块主体 + 四边各自随机凸起/凹口/平边,至少一边不是平的。
func puzzleShape(r *rand.Rand) func(x, y float64) bool {
	const pad = 16.0
	body := float64(TS) - 2*pad
	kr := 9.5 + r.Float64()*2
	type knob struct {
		cx, cy float64
		out    int // 1 凸 -1 凹 0 平
	}
	var ks []knob
	sides := [4][2]float64{{0.5, 0}, {1, 0.5}, {0.5, 1}, {0, 0.5}}
	nonFlat := 0
	for s := 0; s < 4; s++ {
		o := []int{1, 1, -1, 0}[r.IntN(4)]
		if o != 0 {
			nonFlat++
		}
		off := 0.35 + r.Float64()*0.3
		cx, cy := pad+sides[s][0]*body, pad+sides[s][1]*body
		if s%2 == 0 {
			cx = pad + off*body
		} else {
			cy = pad + off*body
		}
		ks = append(ks, knob{cx, cy, o})
	}
	if nonFlat == 0 {
		ks[1].out = 1
	}
	return func(x, y float64) bool {
		in := x >= pad && x <= pad+body && y >= pad && y <= pad+body
		for _, k := range ks {
			d := math.Hypot(x-k.cx, y-k.cy)
			if k.out == 1 && d <= kr {
				in = true
			}
			if k.out == -1 && d <= kr {
				in = false
			}
		}
		return in
	}
}

const maskSS = 4 // 遮罩超采样,边缘抗锯齿

func mask(inside func(x, y float64) bool) []float64 {
	m := make([]float64, TS*TS)
	for y := 0; y < TS; y++ {
		for x := 0; x < TS; x++ {
			c := 0
			for sy := 0; sy < maskSS; sy++ {
				for sx := 0; sx < maskSS; sx++ {
					if inside(float64(x)+(float64(sx)+0.5)/maskSS, float64(y)+(float64(sy)+0.5)/maskSS) {
						c++
					}
				}
			}
			m[y*TS+x] = float64(c) / (maskSS * maskSS)
		}
	}
	return m
}
