package gen

import (
	"math"
	"math/rand/v2"
)

type style struct {
	name string
	draw func(c *canvas, r *rand.Rand)
}

var styles = []style{ // 其余风格在 scenes.go / abstract.go 里通过 init 追加
	{"山景", landscape},
	{"海面", seascape},
	{"城市夜景", city},
	{"低多边形", lowpoly},
	{"几何图形", shapes},
	{"光斑", bokeh},
}

func vgrad(c *canvas, top, bot rgb) {
	c.shade(func(x, y float64) rgb { return mix(top, bot, y/H) })
}

// 时段决定天空、太阳与整体色温。
type sky struct {
	top, bot, sun rgb
	night         bool
}

func pickSky(r *rand.Rand) sky {
	j := (r.Float64() - 0.5) * 30
	switch r.IntN(4) {
	case 0: // 清晨
		return sky{hsv(210+j, 0.45, 0.85), hsv(30+j, 0.45, 1), hsv(40, 0.3, 1), false}
	case 1: // 白天
		return sky{hsv(205+j, 0.65, 0.85), hsv(195+j, 0.25, 1), hsv(50, 0.1, 1), false}
	case 2: // 黄昏
		return sky{hsv(260+j, 0.55, 0.55), hsv(20+j, 0.75, 1), hsv(35, 0.6, 1), false}
	default: // 夜晚
		return sky{hsv(230+j, 0.7, 0.18), hsv(220+j, 0.55, 0.42), hsv(55, 0.08, 1), true}
	}
}

func stars(c *canvas, r *rand.Rand, maxY float64) {
	for i := 0; i < 180; i++ {
		x, y := r.Float64()*W, r.Float64()*maxY
		c.glow(x, y, 1+r.Float64()*1.8, rgb{255, 255, 240}, 0.4+r.Float64()*0.6, true)
	}
}

func sunOrMoon(c *canvas, r *rand.Rand, s sky, maxY float64) {
	x, y := 60+r.Float64()*(W-120), 30+r.Float64()*(maxY-60)
	rad := 16 + r.Float64()*16
	c.glow(x, y, rad*4, s.sun, 0.35, true)
	c.circle(x, y, rad, s.sun, 0.95)
	if s.night && r.IntN(2) == 0 { // 月牙
		c.circle(x+rad*0.45, y-rad*0.25, rad*0.9, mix(s.top, s.bot, y/H), 1)
	}
}

func clouds(c *canvas, r *rand.Rand, s sky, maxY float64) {
	col := mix(rgb{255, 255, 255}, s.bot, 0.25)
	for n := 2 + r.IntN(4); n > 0; n-- {
		cx, cy := r.Float64()*W, 20+r.Float64()*maxY*0.7
		w := 50 + r.Float64()*90
		for k := 0; k < 9; k++ {
			c.glow(cx+(r.Float64()-0.5)*w, cy+(r.Float64()-0.5)*w*0.2, w*(0.25+r.Float64()*0.2), col, 0.55, false)
		}
	}
}

// ridge 生成一条山脊/沙丘的轮廓多边形(含底部封口)。
func ridge(nz *noise, base, amp, freq float64, oct int, seed float64) []vec {
	pts := []vec{}
	for x := -4.0; x <= W+4; x += 3 {
		y := base - nz.fbm(x*freq+seed, seed, oct)*amp
		pts = append(pts, vec{x, y})
	}
	return append(pts, vec{W + 4, H + 4}, vec{-4, H + 4})
}

func landscape(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	nz := newNoise(r)
	vgrad(c, s.top, s.bot)
	if s.night {
		stars(c, r, H*0.6)
	}
	sunOrMoon(c, r, s, H*0.5)
	if !s.night {
		clouds(c, r, s, H*0.5)
	}
	layers := 4 + r.IntN(3)
	rough := r.IntN(3) != 0 // 否则是平缓的丘陵/沙丘
	hue := r.Float64() * 360
	if r.IntN(2) == 0 {
		hue = 200 + r.Float64()*80 // 偏冷的远山
	}
	near := hsv(hue, 0.45, 0.22)
	if s.night {
		near = hsv(hue, 0.4, 0.08)
	}
	var lastRidge []vec
	for i := 0; i < layers; i++ {
		t := float64(i+1) / float64(layers)
		base := H*0.42 + t*H*0.42
		amp := H * (0.35 - 0.15*t)
		oct := 2
		freq := 0.006 + r.Float64()*0.004
		if rough {
			oct, freq = 5, 0.01+r.Float64()*0.008
		}
		pts := ridge(nz, base, amp, freq, oct, float64(i)*37.1)
		col := mix(mix(s.bot, s.top, 0.2), near, math.Pow(t, 0.8))
		c.poly([][]vec{pts}, col, 1)
		lastRidge = pts
	}
	if r.IntN(2) == 0 { // 近景松树
		col := scale(near, 0.6)
		for x := r.Float64() * 20; x < W; x += 10 + r.Float64()*30 {
			gy := H + 4.0
			for _, p := range lastRidge[:len(lastRidge)-2] {
				if math.Abs(p.x-x) < 2 {
					gy = p.y
				}
			}
			h := 25 + r.Float64()*35
			for k := 0; k < 3; k++ {
				ty := gy - h + float64(k)*h*0.28
				w := h * (0.18 + 0.1*float64(k))
				c.poly([][]vec{{{x, ty}, {x + w, ty + h*0.45}, {x - w, ty + h*0.45}}}, col, 1)
			}
		}
	}
	if r.IntN(5) < 2 { // 湖面倒影
		lake := H * (0.68 + r.Float64()*0.12)
		ly := int(lake * K)
		water := hsv(210, 0.5, 0.35)
		for y := ly; y < c.h; y++ {
			for x := 0; x < c.w; x++ {
				ripple := math.Sin(float64(y)*0.7+nz.at(float64(x)*0.02, float64(y)*0.2)*6) * 3
				src := c.get(x+int(ripple), 2*ly-y)
				i := (y*c.w + x) * 3
				col := scale(mix(src, water, 0.35), 0.85)
				c.p[i], c.p[i+1], c.p[i+2] = col[0], col[1], col[2]
			}
		}
	}
}

func seascape(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	nz := newNoise(r)
	hz := H * (0.38 + r.Float64()*0.17)
	c.shade(func(x, y float64) rgb {
		if y < hz {
			return mix(s.top, s.bot, y/hz)
		}
		t := (y - hz) / (H - hz)
		deep := hsv(205+(r.Float64()-0.5)*0, 0.7, 0.35)
		if s.night {
			deep = hsv(225, 0.6, 0.12)
		}
		return mix(mix(s.bot, deep, 0.6), scale(deep, 0.6), t)
	})
	if s.night {
		stars(c, r, hz)
	}
	sx := 60 + r.Float64()*(W-120)
	sy := hz - 25 - r.Float64()*(hz*0.5)
	rad := 16 + r.Float64()*14
	c.glow(sx, sy, rad*4, s.sun, 0.35, true)
	c.circle(sx, sy, rad, s.sun, 0.95)
	if !s.night {
		clouds(c, r, s, hz*0.8)
	}
	// 波光:越靠近太阳正下方越亮
	for i := 0; i < 900; i++ {
		y := hz + 2 + math.Pow(r.Float64(), 1.6)*(H-hz)
		x := r.Float64() * W
		d := math.Abs(x-sx) / (40 + (y-hz)*0.8)
		w := 4 + (y-hz)*0.12
		a := clampf(1.2-d, 0.08, 0.9)
		c.poly([][]vec{{{x - w, y}, {x + w, y}, {x + w*0.7, y + 1.2}, {x - w*0.7, y + 1.2}}}, mix(s.sun, s.bot, 0.3), a*0.7)
	}
	// 近处浪
	for k := 0; k < 3; k++ {
		base := H - 20 + float64(k)*14
		col := scale(mix(s.bot, hsv(200, 0.6, 0.4), 0.7), 0.7-0.12*float64(k))
		c.poly([][]vec{ridge(nz, base, 22, 0.02, 3, float64(k)*11)}, col, 0.9)
	}
	if r.IntN(2) == 0 { // 远处帆船剪影
		bx, by := r.Float64()*(W-80)+40, hz+6+r.Float64()*10
		dark := scale(s.top, 0.3)
		c.poly([][]vec{{{bx - 18, by}, {bx + 18, by}, {bx + 12, by + 6}, {bx - 12, by + 6}}}, dark, 1)
		c.poly([][]vec{{{bx, by - 34}, {bx, by - 2}, {bx + 16, by - 2}}}, dark, 1)
		c.poly([][]vec{{{bx - 2, by - 28}, {bx - 2, by - 2}, {bx - 14, by - 2}}}, dark, 1)
	}
}

func city(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	if r.IntN(3) != 0 {
		s = sky{hsv(235, 0.7, 0.15), hsv(270+r.Float64()*40, 0.5, 0.45), hsv(55, 0.08, 1), true}
	}
	vgrad(c, s.top, s.bot)
	if s.night {
		stars(c, r, H*0.5)
	}
	sunOrMoon(c, r, s, H*0.4)
	for layer := 0; layer < 3; layer++ {
		t := float64(layer+1) / 3
		col := mix(mix(s.bot, s.top, 0.4), scale(s.top, 0.25), t)
		for x := -10.0; x < W; {
			w := 22 + r.Float64()*40
			h := H*0.18 + r.Float64()*H*(0.5-0.12*float64(layer))
			top := H - h
			c.poly([][]vec{{{x, top}, {x + w, top}, {x + w, H}, {x, H}}}, col, 1)
			if r.IntN(4) == 0 { // 天线
				c.poly([][]vec{{{x + w/2 - 1, top - 18}, {x + w/2 + 1, top - 18}, {x + w/2 + 1, top}, {x + w/2 - 1, top}}}, col, 1)
			}
			if layer > 0 && s.night {
				win := hsv(40+r.Float64()*20, 0.55, 1)
				for wy := top + 6; wy < H-6; wy += 9 {
					for wx := x + 4; wx < x+w-6; wx += 8 {
						if r.Float64() < 0.28 {
							c.poly([][]vec{{{wx, wy}, {wx + 4, wy}, {wx + 4, wy + 5}, {wx, wy + 5}}}, win, 0.6+0.4*t)
						}
					}
				}
			}
			x += w + r.Float64()*6
		}
	}
}

func lowpoly(c *canvas, r *rand.Rand) {
	cell := 45 + r.Float64()*45
	cols, rows := int(W/cell)+2, int(H/cell)+2
	pts := make([][]vec, rows+1)
	z := make([][]float64, rows+1)
	for j := 0; j <= rows; j++ {
		pts[j], z[j] = make([]vec, cols+1), make([]float64, cols+1)
		for i := 0; i <= cols; i++ {
			pts[j][i] = vec{float64(i)*cell - cell/2 + (r.Float64()-0.5)*cell*0.8, float64(j)*cell - cell/2 + (r.Float64()-0.5)*cell*0.8}
			z[j][i] = r.Float64() * cell * 0.9
		}
	}
	h0 := r.Float64() * 360
	span := 40 + r.Float64()*80
	ang := r.Float64() * 2 * math.Pi
	gdx, gdy := math.Cos(ang), math.Sin(ang)
	l := [3]float64{r.Float64() - 0.5, r.Float64() - 0.5, 1}
	ln := math.Sqrt(l[0]*l[0] + l[1]*l[1] + l[2]*l[2])
	tri := func(a, b, d vec, za, zb, zd float64) {
		cx, cy := (a.x+b.x+d.x)/3, (a.y+b.y+d.y)/3
		t := ((cx-W/2)*gdx+(cy-H/2)*gdy)/math.Hypot(W, H) + 0.5
		col := hsv(h0+t*span, 0.55+0.2*t, 0.75)
		ux, uy, uz := b.x-a.x, b.y-a.y, zb-za
		vx, vy, vz := d.x-a.x, d.y-a.y, zd-za
		nx, ny, nz := uy*vz-uz*vy, uz*vx-ux*vz, ux*vy-uy*vx
		if nz < 0 {
			nx, ny, nz = -nx, -ny, -nz
		}
		sh := 0.6 + 0.6*math.Max(0, (nx*l[0]+ny*l[1]+nz*l[2])/(math.Sqrt(nx*nx+ny*ny+nz*nz)*ln))
		c.poly([][]vec{{a, b, d}}, scale(col, sh), 1)
	}
	for j := 0; j < rows; j++ {
		for i := 0; i < cols; i++ {
			p00, p10, p01, p11 := pts[j][i], pts[j][i+1], pts[j+1][i], pts[j+1][i+1]
			z00, z10, z01, z11 := z[j][i], z[j][i+1], z[j+1][i], z[j+1][i+1]
			tri(p00, p10, p11, z00, z10, z11)
			tri(p00, p11, p01, z00, z11, z01)
		}
	}
}

func regular(cx, cy, rad, rot float64, n int) []vec {
	pts := make([]vec, n)
	for i := range pts {
		t := rot + float64(i)/float64(n)*2*math.Pi
		pts[i] = vec{cx + rad*math.Cos(t), cy + rad*math.Sin(t)}
	}
	return pts
}

func shapes(c *canvas, r *rand.Rand) {
	h0 := r.Float64() * 360
	vgrad(c, hsv(h0, 0.35, 0.95), hsv(h0+40, 0.45, 0.8))
	for n := 22 + r.IntN(14); n > 0; n-- {
		x, y := r.Float64()*W, r.Float64()*H
		sz := 12 + math.Pow(r.Float64(), 2)*70
		col := hsv(h0+[]float64{0, 30, 180, 210, 60}[r.IntN(5)]+(r.Float64()-0.5)*20, 0.5+r.Float64()*0.4, 0.6+r.Float64()*0.4)
		a := 0.55 + r.Float64()*0.4
		rot := r.Float64() * math.Pi
		var ct [][]vec
		switch r.IntN(6) {
		case 0:
			ct = [][]vec{circlePts(x, y, sz)}
		case 1: // 圆环
			ct = [][]vec{circlePts(x, y, sz), circlePts(x, y, sz*0.6)}
		case 2:
			ct = [][]vec{regular(x, y, sz, rot, 3)}
		case 3:
			ct = [][]vec{regular(x, y, sz, rot, 4)}
		case 4:
			ct = [][]vec{regular(x, y, sz, rot, 6)}
		default: // 细长条
			w := sz * 0.18
			dx, dy := math.Cos(rot)*sz*1.5, math.Sin(rot)*sz*1.5
			px, py := -math.Sin(rot)*w, math.Cos(rot)*w
			ct = [][]vec{{{x - dx - px, y - dy - py}, {x + dx - px, y + dy - py}, {x + dx + px, y + dy + py}, {x - dx + px, y - dy + py}}}
		}
		sh := make([][]vec, len(ct))
		for i, cc := range ct {
			sh[i] = make([]vec, len(cc))
			for j, p := range cc {
				sh[i][j] = vec{p.x + 4, p.y + 5}
			}
		}
		c.poly(sh, rgb{0, 0, 0}, 0.15)
		c.poly(ct, col, a)
	}
}

func bokeh(c *canvas, r *rand.Rand) {
	h0 := r.Float64() * 360
	vgrad(c, hsv(h0, 0.6, 0.18), hsv(h0+50, 0.6, 0.35))
	for n := 50 + r.IntN(40); n > 0; n-- {
		x, y := r.Float64()*W, r.Float64()*H
		rad := 6 + math.Pow(r.Float64(), 2)*55
		col := hsv(h0+(r.Float64()-0.5)*120, 0.5, 1)
		if r.IntN(2) == 0 {
			c.glow(x, y, rad, col, 0.5, true)
		} else {
			c.circle(x, y, rad, col, 0.12+r.Float64()*0.18)
		}
	}
}

// band 填充曲线 y=f(x) 以下直到底边的区域。
func band(c *canvas, f func(x float64) float64, col rgb, a float64) {
	pts := []vec{}
	for x := -4.0; x <= W+4; x += 2 {
		pts = append(pts, vec{x, f(x)})
	}
	pts = append(pts, vec{W + 4, H + 4}, vec{-4, H + 4})
	c.poly([][]vec{pts}, col, a)
}

func rect(c *canvas, x, y, w, h float64, col rgb, a float64) {
	c.poly([][]vec{{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}}, col, a)
}

// line 画粗线段(矩形四边形)。
func line(c *canvas, x1, y1, x2, y2, w float64, col rgb, a float64) {
	dx, dy := x2-x1, y2-y1
	l := math.Hypot(dx, dy)
	if l == 0 {
		return
	}
	px, py := -dy/l*w/2, dx/l*w/2
	c.poly([][]vec{{{x1 + px, y1 + py}, {x2 + px, y2 + py}, {x2 - px, y2 - py}, {x1 - px, y1 - py}}}, col, a)
}

func addStyles(ss ...style) { styles = append(styles, ss...) }
