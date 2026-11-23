package gen

import (
	"math"
	"math/rand/v2"
)

func init() {
	addStyles(
		style{"沙漠", desert},
		style{"雪山", snowPeaks},
		style{"雾林", foggyForest},
		style{"草原", meadow},
		style{"极光", aurora},
		style{"银河", milkyWay},
		style{"海岛", island},
		style{"峡谷", canyon},
		style{"冰川", glacier},
		style{"花田", flowerField},
		style{"水下", underwater},
		style{"行星", planets},
		style{"云海", cloudSea},
		style{"梯田", terraces},
		style{"灯塔", lighthouse},
	)
}

func pine(c *canvas, x, gy, h float64, col rgb) {
	line(c, x, gy, x, gy-h*0.2, h*0.06, col, 1)
	for k := 0; k < 3; k++ {
		ty := gy - h + float64(k)*h*0.26
		w := h * (0.16 + 0.1*float64(k))
		c.poly([][]vec{{{x, ty}, {x + w, ty + h*0.42}, {x - w, ty + h*0.42}}}, col, 1)
	}
}

func roundTree(c *canvas, r *rand.Rand, x, gy, h float64, col rgb) {
	line(c, x, gy, x, gy-h*0.5, h*0.08, scale(col, 0.7), 1)
	for k := 0; k < 4; k++ {
		c.circle(x+(r.Float64()-0.5)*h*0.35, gy-h*0.62+(r.Float64()-0.5)*h*0.3, h*(0.22+r.Float64()*0.1), scale(col, 0.9+r.Float64()*0.2), 1)
	}
}

// curveY 按 x 在折线上取 y,用于把物体立在地形上。
func curveY(f func(x float64) float64, x float64) float64 { return f(x) }

func desert(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	if s.night {
		s = sky{hsv(25, 0.5, 0.95), hsv(40, 0.35, 1), hsv(45, 0.4, 1), false}
	}
	vgrad(c, s.top, s.bot)
	sunOrMoon(c, r, s, H*0.45)
	nz := newNoise(r)
	hue := 25 + r.Float64()*15
	n := 4 + r.IntN(2)
	var near func(x float64) float64
	for i := 0; i < n; i++ {
		t := float64(i+1) / float64(n)
		base := H*0.5 + t*H*0.4
		ph, fr, amp := r.Float64()*10, 0.006+r.Float64()*0.006, 18+r.Float64()*25
		f := func(x float64) float64 {
			return base - amp*math.Abs(math.Sin(x*fr+ph)) - nz.fbm(x*0.01, float64(i), 2)*12
		}
		col := hsv(hue+t*8, 0.45+0.2*t, 0.95-0.35*t)
		band(c, f, col, 1)
		// 沙丘背光面:沿脊线下方压一道暗色
		band(c, func(x float64) float64 { return f(x) + 3 + 6*math.Max(0, math.Cos(x*fr+ph)) }, scale(col, 0.82), 0.5)
		near = f
	}
	for k := r.IntN(4); k > 0; k-- { // 仙人掌
		x := 40 + r.Float64()*(W-80)
		gy, h := curveY(near, x)+6, 30+r.Float64()*30
		col := hsv(110, 0.45, 0.3)
		line(c, x, gy, x, gy-h, h*0.16, col, 1)
		c.circle(x, gy-h, h*0.08, col, 1)
		for _, sd := range []float64{-1, 1} {
			ay := gy - h*(0.4+r.Float64()*0.25)
			ax := x + sd*h*0.28
			line(c, x, ay, ax, ay, h*0.12, col, 1)
			line(c, ax, ay, ax, ay-h*0.3, h*0.12, col, 1)
			c.circle(ax, ay-h*0.3, h*0.06, col, 1)
		}
	}
}

func snowPeaks(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	vgrad(c, s.top, s.bot)
	if s.night {
		stars(c, r, H*0.5)
	}
	sunOrMoon(c, r, s, H*0.35)
	nz := newNoise(r)
	snow := mix(rgb{250, 252, 255}, s.bot, 0.15)
	for i := 0; i < 3; i++ {
		t := float64(i+1) / 3
		base := H*0.55 + t*H*0.25
		amp := H * (0.5 - 0.12*float64(i))
		seed := float64(i) * 13.7
		f := func(x float64) float64 {
			v := nz.fbm(x*0.008+seed, seed, 5)
			return base - math.Pow(v, 1.6)*amp*1.6
		}
		rock := mix(mix(s.top, s.bot, 0.5), hsv(215, 0.2, 0.35), 0.45+0.45*t)
		f = c.memo(f)
		snowLine := base - amp*(0.35+r.Float64()*0.2)
		hasSnow := i < 2
		// 按坡向分明暗面;雪线大致水平,用竖向拉长的噪声做出沟壑里的雪痕
		c.shade2Rect(0, 0, W, H, func(x, y float64, cur rgb) (rgb, float64) {
			top := f(x)
			if y < top {
				return cur, 0
			}
			slope := (f(x+10) - f(x-10)) / 20                                 // 宽窗口取坡度,避免高频噪声把明暗切成竖条
			lit := 0.9 + clampf(slope*0.6, -0.16, 0.16)*math.Exp(-(y-top)/30) // 坡向明暗只作用在山脊附近,往下淡出,否则整列成竖条
			if hasSnow && y < snowLine+(nz.at(x*0.05, y*0.02)-0.5)*60 {
				return scale(snow, 0.88+0.12*lit), 1
			}
			return scale(rock, lit*(0.9+0.2*nz.at(x*0.05, y*0.05))), 1
		})
	}
	for i := 0; i < 2; i++ { // 山脚林带
		gy := H*0.88 + float64(i)*14
		col := hsv(150, 0.4, 0.22-0.06*float64(i))
		for x := r.Float64() * 10; x < W; x += 6 + r.Float64()*10 {
			pine(c, x, gy+r.Float64()*8, 18+r.Float64()*16, col)
		}
		rect(c, 0, gy+4, W, H, col, 1)
	}
}

func foggyForest(c *canvas, r *rand.Rand) {
	hue := 100 + r.Float64()*120
	fog := hsv(hue, 0.15, 0.9)
	vgrad(c, hsv(hue, 0.2, 0.95), fog)
	if r.IntN(2) == 0 {
		c.glow(r.Float64()*W, H*0.2, 160, rgb{255, 245, 220}, 0.5, true)
	}
	layers := 5
	for i := 0; i < layers; i++ {
		t := float64(i+1) / float64(layers)
		col := mix(fog, hsv(hue, 0.45, 0.18), math.Pow(t, 1.2))
		gy := H*0.45 + t*H*0.5
		for x := -10 + r.Float64()*10; x < W+10; x += 5 + r.Float64()*12*(1-t*0.5) {
			pine(c, x, gy+r.Float64()*10, (30+r.Float64()*40)*(0.6+t), col)
		}
		rect(c, 0, gy, W, H, col, 1)
		c.shade2Rect(0, gy-70, W, gy+70, func(x, y float64, cur rgb) (rgb, float64) { // 层间雾
			return fog, 0.18 * clampf(1-(y-gy+60)/120, 0, 1) * (1 - t)
		})
	}
}

func meadow(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	if s.night {
		s = pickSky(r)
	}
	vgrad(c, s.top, s.bot)
	sunOrMoon(c, r, s, H*0.3)
	clouds(c, r, s, H*0.35)
	nz := newNoise(r)
	hue := 80 + r.Float64()*50
	var near func(x float64) float64
	for i := 0; i < 4; i++ {
		t := float64(i+1) / 4
		base := H*0.5 + t*H*0.38
		seed := float64(i) * 5.3
		f := func(x float64) float64 { return base - nz.fbm(x*0.004+seed, seed, 3)*70 }
		col := hsv(hue+(r.Float64()-0.5)*30, 0.5, 0.85-0.3*t)
		band(c, f, col, 1)
		if i == 1 || i == 2 { // 田垄条纹
			for k := 0; k < 14; k++ {
				off := float64(k) * 7
				g := func(x float64) float64 { return f(x) + off + 3 }
				band(c, g, scale(col, 0.92+0.16*float64(k%2)), 0.5)
			}
		}
		near = f
	}
	for k := 2 + r.IntN(5); k > 0; k-- {
		x := r.Float64() * W
		roundTree(c, r, x, near(x)+8, 30+r.Float64()*30, hsv(hue+10, 0.6, 0.35))
	}
	if r.IntN(2) == 0 { // 风车
		x := 80 + r.Float64()*(W-160)
		gy := near(x) - 10
		col := rgb{245, 245, 240}
		c.poly([][]vec{{{x - 7, gy}, {x + 7, gy}, {x + 3, gy - 70}, {x - 3, gy - 70}}}, col, 1)
		a0 := r.Float64() * math.Pi
		for k := 0; k < 3; k++ {
			a := a0 + float64(k)*2*math.Pi/3
			line(c, x, gy-70, x+math.Cos(a)*45, gy-70+math.Sin(a)*45, 4, col, 1)
		}
	}
}

func aurora(c *canvas, r *rand.Rand) {
	vgrad(c, hsv(230, 0.7, 0.08), hsv(210, 0.6, 0.25))
	stars(c, r, H)
	nz := newNoise(r)
	h0 := []float64{130, 150, 170, 280}[r.IntN(4)]
	for b := 0; b < 2+r.IntN(2); b++ {
		baseY := H*0.2 + r.Float64()*H*0.25
		amp, fr, ph := 30+r.Float64()*40, 0.004+r.Float64()*0.006, r.Float64()*10
		col := hsv(h0+float64(b)*25, 0.7, 0.9)
		bb := float64(b)
		center := c.memo(func(x float64) float64 { return baseY + math.Sin(x*fr+ph)*amp + nz.fbm(x*0.01, bb, 3)*30 })
		stripe := c.memo(func(x float64) float64 { return 0.6 + 0.4*nz.at(x*0.15, bb*7) })
		c.shade2(func(x, y float64, cur rgb) (rgb, float64) {
			cy := center(x)
			d := cy - y // 光帘向上拖尾
			if d < -6 || d > 110 {
				return cur, 0
			}
			stripes := stripe(x)
			a := math.Exp(-math.Max(d, 0)/45) * stripes
			if d < 0 {
				a *= 1 + d/6
			}
			return clampAdd(cur, col, a*0.7), 1
		})
	}
	mt := hsv(220, 0.4, 0.06)
	band(c, func(x float64) float64 { return H*0.8 - nz.fbm(x*0.01, 9, 5)*80 }, mt, 1)
	for x := 0.0; x < W; x += 5 + r.Float64()*9 {
		pine(c, x, H+2, 20+r.Float64()*25, scale(mt, 0.5))
	}
}

func clampAdd(a, b rgb, t float64) rgb {
	return rgb{clampf(a[0]+b[0]*t, 0, 255), clampf(a[1]+b[1]*t, 0, 255), clampf(a[2]+b[2]*t, 0, 255)}
}

func milkyWay(c *canvas, r *rand.Rand) {
	hue := 220 + r.Float64()*50
	vgrad(c, hsv(hue, 0.7, 0.07), hsv(hue+20, 0.5, 0.28))
	nz := newNoise(r)
	ang := (r.Float64() - 0.5) * 1.4
	cx, cy := W*(0.3+r.Float64()*0.4), H*0.4
	ca, sa := math.Cos(ang), math.Sin(ang)
	c.shade2(func(x, y float64, cur rgb) (rgb, float64) {
		d := math.Abs(-(x-cx)*sa + (y-cy)*ca) // 到银河中轴的距离
		g := math.Exp(-d*d/(2*55*55)) * (0.4 + 0.8*nz.fbm(x*0.012, y*0.012, 4))
		dust := nz.fbm(x*0.03+50, y*0.03, 3)
		col := mix(hsv(hue+40, 0.35, 1), hsv(30, 0.4, 1), dust)
		return clampAdd(cur, col, g*0.55*(1-0.6*math.Max(0, dust-0.55)*2)), 1
	})
	for i := 0; i < 500; i++ {
		c.glow(r.Float64()*W, r.Float64()*H, 0.8+r.Float64()*1.4, rgb{255, 255, 245}, 0.3+r.Float64()*0.7, true)
	}
	ground := hsv(hue, 0.3, 0.05)
	band(c, func(x float64) float64 { return H*0.85 - nz.fbm(x*0.006, 3, 3)*50 }, ground, 1)
	if r.IntN(2) == 0 {
		x := r.Float64() * W
		gy := H*0.85 - nz.fbm(x*0.006, 3, 3)*50
		roundTree(c, r, x, gy+10, 70+r.Float64()*30, ground)
	}
}

func palm(c *canvas, r *rand.Rand, x, gy, h float64, col rgb) {
	lean := (r.Float64() - 0.5) * 0.6
	px, py := x, gy
	for k := 1; k <= 10; k++ { // 弯曲树干
		t := float64(k) / 10
		nx, ny := x+lean*h*t*t, gy-h*t
		line(c, px, py, nx, ny, h*0.06*(1.2-t*0.5), col, 1)
		px, py = nx, ny
	}
	for k := 0; k < 7; k++ {
		a := -math.Pi/2 + (float64(k)-3)*0.5 + (r.Float64()-0.5)*0.3
		l := h * (0.45 + r.Float64()*0.2)
		pts := []vec{{px, py}}
		for j := 1; j <= 6; j++ {
			t := float64(j) / 6
			droop := t * t * l * 0.5
			w := math.Sin(t*math.Pi) * h * 0.06
			ex, ey := px+math.Cos(a)*l*t, py+math.Sin(a)*l*t+droop
			pts = append(pts, vec{ex - math.Sin(a)*w, ey + math.Cos(a)*w})
		}
		for j := 5; j >= 1; j-- {
			t := float64(j) / 6
			droop := t * t * l * 0.5
			w := math.Sin(t*math.Pi) * h * 0.06
			ex, ey := px+math.Cos(a)*l*t, py+math.Sin(a)*l*t+droop
			pts = append(pts, vec{ex + math.Sin(a)*w, ey - math.Cos(a)*w})
		}
		c.poly([][]vec{pts}, col, 1)
	}
}

func island(c *canvas, r *rand.Rand) {
	s := sky{hsv(260+r.Float64()*30, 0.5, 0.5), hsv(15+r.Float64()*25, 0.7, 1), hsv(40, 0.6, 1), false}
	if r.IntN(2) == 0 {
		s = sky{hsv(200, 0.6, 0.8), hsv(185, 0.3, 1), hsv(50, 0.1, 1), false}
	}
	hz := H * (0.5 + r.Float64()*0.1)
	c.shade(func(x, y float64) rgb {
		if y < hz {
			return mix(s.top, s.bot, y/hz)
		}
		return mix(mix(s.bot, hsv(190, 0.7, 0.55), 0.6), hsv(200, 0.7, 0.3), (y-hz)/(H-hz))
	})
	sx := 80 + r.Float64()*(W-160)
	c.glow(sx, hz-20, 120, s.sun, 0.4, true)
	c.circle(sx, hz-20-r.Float64()*40, 24, s.sun, 0.95)
	for i := 0; i < 400; i++ {
		y := hz + 2 + math.Pow(r.Float64(), 1.5)*(H-hz)
		x := sx + (r.Float64()-0.5)*(60+(y-hz)*2)
		w := 3 + (y-hz)*0.08
		line(c, x-w, y, x+w, y, 1.2, s.sun, 0.35)
	}
	ix := r.Float64() * W
	iw := 140 + r.Float64()*120
	dark := scale(mix(s.top, rgb{20, 30, 30}, 0.7), 0.6)
	sand := hsv(40, 0.35, 0.75)
	ih := 14 + r.Float64()*14
	mound := []vec{}
	for k := 0; k <= 40; k++ {
		t := float64(k) / 40 * math.Pi
		mound = append(mound, vec{ix - math.Cos(t)*iw/2, hz + 2 - math.Sin(t)*ih})
	}
	c.poly([][]vec{mound}, sand, 1)
	c.poly([][]vec{mound}, dark, 0.15)
	for k := 0; k < 2+r.IntN(3); k++ {
		palm(c, r, ix+(r.Float64()-0.5)*iw*0.5, hz-ih*0.6, 60+r.Float64()*50, dark)
	}
}

func canyon(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	if s.night {
		s = sky{hsv(205, 0.55, 0.85), hsv(35, 0.3, 1), hsv(45, 0.2, 1), false}
	}
	vgrad(c, s.top, s.bot)
	sunOrMoon(c, r, s, H*0.3)
	nz := newNoise(r)
	hue := 10 + r.Float64()*20
	for i := 0; i < 4; i++ {
		t := float64(i+1) / 4
		top := H*0.3 + t*H*0.4 + (r.Float64()-0.5)*20
		seed := float64(i) * 9.1
		// 台地:平顶 + 陡壁,用阈值把噪声切成方台
		f := func(x float64) float64 {
			v := nz.fbm(x*0.006+seed, seed, 3)
			if v > 0.5 {
				return top - 10*nz.at(x*0.05, seed)
			}
			return top + (0.5-v)*260
		}
		base := mix(mix(s.bot, hsv(hue, 0.6, 0.75), 0.6+0.4*t), hsv(hue, 0.7, 0.3), t*0.6)
		f = c.memo(f)
		c.shade2Rect(0, top-20, W, H, func(x, y float64, cur rgb) (rgb, float64) {
			if y < f(x) {
				return cur, 0
			}
			strata := 0.88 + 0.12*math.Sin(y*0.45+nz.at(x*0.02, y*0.05)*4) // 岩层横纹
			return scale(base, strata), 1
		})
	}
}

func glacier(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	hz := H * (0.42 + r.Float64()*0.12)
	water := hsv(200, 0.6, 0.45)
	c.shade(func(x, y float64) rgb {
		if y < hz {
			return mix(s.top, s.bot, y/hz)
		}
		return mix(mix(s.bot, water, 0.6), scale(water, 0.55), (y-hz)/(H-hz))
	})
	if s.night {
		stars(c, r, hz)
	}
	sunOrMoon(c, r, s, hz*0.8)
	for k := 0; k < 3+r.IntN(4); k++ {
		cx := r.Float64() * W
		w := 30 + r.Float64()*120
		h := 20 + r.Float64()*70
		base := hz + 4 + r.Float64()*(H-hz)*0.6
		n := 5 + r.IntN(4)
		pts := []vec{{cx - w/2, base}}
		for j := 1; j < n; j++ {
			t := float64(j) / float64(n)
			pts = append(pts, vec{cx - w/2 + t*w + (r.Float64()-0.5)*w*0.15, base - h*math.Sin(t*math.Pi)*(0.6+r.Float64()*0.5)})
		}
		pts = append(pts, vec{cx + w/2, base})
		ice := hsv(195, 0.15, 0.97)
		c.poly([][]vec{pts}, ice, 1)
		// 背光面
		half := append([]vec{}, pts[len(pts)/2:]...)
		half = append(half, vec{cx, base})
		c.poly([][]vec{half}, hsv(205, 0.35, 0.8), 0.7)
		// 水下倒影
		ref := make([]vec, len(pts))
		for j, p := range pts {
			ref[j] = vec{p.x, 2*base - p.y*0.4 - base*0.6}
		}
		c.poly([][]vec{ref}, hsv(195, 0.3, 0.8), 0.25)
	}
}

func flowerField(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	if s.night {
		s = pickSky(r)
	}
	vgrad(c, s.top, s.bot)
	clouds(c, r, s, H*0.3)
	nz := newNoise(r)
	fh := []float64{330, 280, 50, 0, 20}[r.IntN(5)]
	for i := 0; i < 4; i++ {
		t := float64(i+1) / 4
		base := H*0.45 + t*H*0.42
		seed := float64(i) * 3.3
		f := func(x float64) float64 { return base - nz.fbm(x*0.005+seed, seed, 3)*60 }
		band(c, f, hsv(100+r.Float64()*30, 0.5, 0.7-0.25*t), 1)
		// 花点:越近越大越稀
		n := int(900 * (1.2 - t))
		for k := 0; k < n; k++ {
			x := r.Float64() * W
			y := f(x) + 4 + r.Float64()*(H-f(x))
			rad := 0.8 + t*3*r.Float64()
			c.circle(x, y, rad, hsv(fh+(r.Float64()-0.5)*30, 0.55+r.Float64()*0.3, 0.95), 0.9)
		}
	}
}

func underwater(c *canvas, r *rand.Rand) {
	hue := 185 + r.Float64()*30
	vgrad(c, hsv(hue, 0.6, 0.75), hsv(hue+15, 0.85, 0.2))
	nz := newNoise(r)
	// 光柱
	for k := 0; k < 6+r.IntN(5); k++ {
		x := r.Float64() * W
		w := 10 + r.Float64()*30
		sk := (r.Float64() - 0.5) * 120
		c.poly([][]vec{{{x, -5}, {x + w, -5}, {x + w + sk + 40, H}, {x + sk - 10, H}}}, hsv(hue-20, 0.2, 1), 0.08+r.Float64()*0.08)
	}
	// 焦散网纹
	c.shade2(func(x, y float64, cur rgb) (rgb, float64) {
		v := math.Abs(nz.fbm(x*0.02, y*0.04, 3)-0.5) * 2
		return clampAdd(cur, rgb{200, 255, 255}, math.Pow(1-v, 8)*0.25*(1-y/H)), 1
	})
	// 海底与水草
	band(c, func(x float64) float64 { return H*0.88 - nz.fbm(x*0.01, 4, 3)*30 }, hsv(40, 0.4, 0.35), 1)
	for k := 0; k < 12+r.IntN(10); k++ {
		x := r.Float64() * W
		h := 40 + r.Float64()*90
		col := hsv(100+r.Float64()*60, 0.6, 0.35+r.Float64()*0.2)
		px, py := x, H+2.0
		for j := 1; j <= 12; j++ {
			t := float64(j) / 12
			nx := x + math.Sin(t*5+x)*10*t
			ny := H + 2 - h*t
			line(c, px, py, nx, ny, 6*(1-t*0.7), col, 1)
			px, py = nx, ny
		}
	}
	for k := 0; k < 25; k++ { // 气泡
		x, y, rr := r.Float64()*W, r.Float64()*H*0.8, 1.5+r.Float64()*5
		c.circle(x, y, rr, rgb{220, 250, 255}, 0.25)
		c.circle(x-rr*0.3, y-rr*0.3, rr*0.3, rgb{255, 255, 255}, 0.6)
	}
}

func planets(c *canvas, r *rand.Rand) {
	hue := r.Float64() * 360
	vgrad(c, hsv(hue, 0.6, 0.06), hsv(hue+30, 0.5, 0.18))
	nz := newNoise(r)
	c.shade2(func(x, y float64, cur rgb) (rgb, float64) {
		v := nz.fbm(x*0.006, y*0.006, 4)
		return clampAdd(cur, hsv(hue+v*80, 0.6, 1), math.Pow(v, 3)*0.5), 1
	})
	for i := 0; i < 300; i++ {
		c.glow(r.Float64()*W, r.Float64()*H, 0.8+r.Float64()*1.5, rgb{255, 255, 250}, 0.3+r.Float64()*0.7, true)
	}
	lx, ly := (r.Float64()-0.5)*2, -0.6
	for k := 0; k < 1+r.IntN(3); k++ {
		px, py := r.Float64()*W, r.Float64()*H
		pr := 20 + r.Float64()*80
		if k > 0 {
			pr *= 0.35
		}
		ph := r.Float64() * 360
		ring := r.IntN(3) == 0
		tilt := (r.Float64() - 0.5) * 0.6
		drawRing := func(front bool) {
			if !ring {
				return
			}
			c.shade2Rect(px-pr*2.2, py-pr*2.2, px+pr*2.2, py+pr*2.2, func(x, y float64, cur rgb) (rgb, float64) {
				dx, dy := x-px, y-py
				u := dx*math.Cos(tilt) + dy*math.Sin(tilt)
				v := (-dx*math.Sin(tilt) + dy*math.Cos(tilt)) / 0.25
				d := math.Hypot(u, v) / pr
				if d < 1.3 || d > 2.1 || (v > 0) != front {
					return cur, 0
				}
				return hsv(ph+20, 0.3, 0.9), 0.55 * (0.7 + 0.3*math.Sin(d*40))
			})
		}
		drawRing(false)
		c.shade2Rect(px-pr-1, py-pr-1, px+pr+1, py+pr+1, func(x, y float64, cur rgb) (rgb, float64) {
			dx, dy := (x-px)/pr, (y-py)/pr
			d2 := dx*dx + dy*dy
			if d2 > 1 {
				return cur, 0
			}
			nzv := math.Sqrt(1 - d2)
			lit := clampf(dx*lx+dy*ly+nzv*0.6, 0, 1)
			bands := nz.fbm(dy*3+float64(k)*10, dx*0.5, 4) // 气态行星条带
			col := scale(hsv(ph+bands*40, 0.45, 0.9), 0.15+0.95*lit)
			return col, clampf((1-d2)*pr, 0, 1)
		})
		drawRing(true)
	}
}

func cloudSea(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	if s.night {
		s = sky{hsv(220, 0.5, 0.6), hsv(30, 0.5, 1), hsv(40, 0.5, 1), false}
	}
	vgrad(c, s.top, s.bot)
	sunOrMoon(c, r, s, H*0.35)
	nz := newNoise(r)
	for i := 0; i < 2; i++ {
		base := H*0.62 - float64(i)*20
		band(c, func(x float64) float64 { return base - math.Pow(nz.fbm(x*0.007+float64(i)*7, 1, 5), 2)*240 }, mix(mix(s.top, s.bot, 0.5), rgb{40, 50, 70}, 0.5+0.2*float64(i)), 1)
	}
	cloud := mix(rgb{255, 255, 255}, s.bot, 0.3)
	for layer := 0; layer < 6; layer++ {
		y0 := H*0.58 + float64(layer)*26
		for x := -20.0; x < W+20; x += 18 + r.Float64()*20 + float64(layer)*6 {
			rr := (20 + r.Float64()*26) * (1 + float64(layer)*0.25)
			c.circle(x, y0+(r.Float64()-0.5)*14, rr, scale(cloud, 0.8+0.035*float64(layer)), 1)
			c.circle(x-rr*0.2, y0-rr*0.25, rr*0.6, cloud, 0.8)
		}
		rect(c, 0, y0+22, W, H, scale(cloud, 0.8+0.035*float64(layer)), 1)
	}
}

func terraces(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	if s.night {
		s = pickSky(r)
	}
	nz := newNoise(r)
	vgrad(c, s.top, s.bot)
	band(c, func(x float64) float64 { return H*0.25 - nz.fbm(x*0.006, 2, 4)*60 }, mix(s.bot, hsv(150, 0.3, 0.4), 0.5), 1)
	water := r.IntN(2) == 0 // 灌水期反天光,否则是稻绿/金黄
	hue := []float64{95, 75, 45}[r.IntN(3)]
	for k := 0; k < 22; k++ {
		base := H*0.22 + float64(k)*13
		seed := float64(k) * 0.37
		f := func(x float64) float64 {
			return base + math.Sin(x*0.012+seed*3)*18 + nz.fbm(x*0.008, seed, 3)*14
		}
		col := hsv(hue+(r.Float64()-0.5)*15, 0.55, 0.65+0.2*r.Float64())
		if water {
			col = mix(s.bot, s.top, r.Float64()*0.6)
		}
		band(c, f, hsv(30, 0.35, 0.45), 1) // 田埂
		band(c, func(x float64) float64 { return f(x) + 1.6 }, col, 1)
	}
}

func lighthouse(c *canvas, r *rand.Rand) {
	s := pickSky(r)
	hz := H * (0.5 + r.Float64()*0.1)
	sea := hsv(205, 0.6, 0.45)
	if s.night {
		sea = hsv(225, 0.6, 0.15)
	}
	c.shade(func(x, y float64) rgb {
		if y < hz {
			return mix(s.top, s.bot, y/hz)
		}
		return mix(mix(s.bot, sea, 0.6), scale(sea, 0.6), (y-hz)/(H-hz))
	})
	if s.night {
		stars(c, r, hz)
	} else {
		clouds(c, r, s, hz*0.6)
	}
	nz := newNoise(r)
	left := r.IntN(2) == 0
	cliffX := W * (0.3 + r.Float64()*0.2)
	edge := func(x float64) float64 {
		d := x
		if !left {
			d = W - x
		}
		if d > cliffX {
			return H + 10
		}
		return hz - 40 - nz.fbm(x*0.02, 1, 4)*30 + math.Max(0, d-cliffX+40)*3
	}
	rock := hsv(30, 0.25, 0.3)
	if s.night {
		rock = hsv(220, 0.25, 0.1)
	}
	edge = c.memo(edge)
	grass := hsv(100, 0.4, 0.4)
	if s.night {
		grass = hsv(130, 0.3, 0.15)
	}
	c.shade2(func(x, y float64, cur rgb) (rgb, float64) {
		e := edge(x)
		if y < e {
			return cur, 0
		}
		if y < e+6+nz.at(x*0.2, 3)*4 {
			return grass, 1
		}
		v := nz.fbm(x*0.03, y*0.08, 3) // 竖向拉长的岩层裂纹
		return scale(rock, 0.75+0.5*v), 1
	})
	lx := cliffX * 0.5
	if !left {
		lx = W - lx
	}
	gy := edge(lx) + 2
	h := 90.0
	for k := 0; k < 4; k++ { // 红白相间塔身
		y0, y1 := gy-h*float64(k)/4, gy-h*float64(k+1)/4
		w0, w1 := 12-float64(k)*1.5, 12-float64(k+1)*1.5
		col := rgb{240, 240, 235}
		if k%2 == 1 {
			col = hsv(5, 0.7, 0.8)
		}
		c.poly([][]vec{{{lx - w0, y0}, {lx + w0, y0}, {lx + w1, y1}, {lx - w1, y1}}}, col, 1)
	}
	rect(c, lx-8, gy-h-14, 16, 14, rgb{60, 60, 60}, 1)
	c.poly([][]vec{{{lx - 11, gy - h - 14}, {lx + 11, gy - h - 14}, {lx, gy - h - 26}}}, hsv(5, 0.7, 0.6), 1)
	lamp := hsv(50, 0.5, 1)
	c.glow(lx, gy-h-7, 40, lamp, 0.8, true)
	if s.night {
		dir := 1.0
		if r.IntN(2) == 0 {
			dir = -1
		}
		c.poly([][]vec{{{lx, gy - h - 9}, {lx + dir*W, gy - h - 60}, {lx + dir*W, gy - h + 50}, {lx, gy - h - 5}}}, lamp, 0.15)
	}
}

// shade2 逐像素按当前颜色计算新颜色与覆盖度(0 表示不动),用于局部效果叠加。
func (c *canvas) shade2(f func(x, y float64, cur rgb) (rgb, float64)) {
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			i := (y*c.w + x) * 3
			cur := rgb{c.p[i], c.p[i+1], c.p[i+2]}
			col, a := f((float64(x)+0.5)/K, (float64(y)+0.5)/K, cur)
			if a <= 0 {
				continue
			}
			for k := 0; k < 3; k++ {
				c.p[i+k] = cur[k] + (col[k]-cur[k])*math.Min(a, 1)
			}
		}
	}
}

// memo 把只依赖 x 的曲线按内部像素列预算一遍,避免逐像素重复跑 fbm。
func (c *canvas) memo(f func(x float64) float64) func(x float64) float64 {
	v := make([]float64, c.w)
	for i := range v {
		v[i] = f((float64(i) + 0.5) / K)
	}
	return func(x float64) float64 { return v[clampi(int(x*K), 0, c.w-1)] }
}

// shade2Rect 同 shade2,只扫描给定逻辑矩形。
func (c *canvas) shade2Rect(x0, y0, x1, y1 float64, f func(x, y float64, cur rgb) (rgb, float64)) {
	for y := max(int(y0*K), 0); y < min(int(y1*K)+1, c.h); y++ {
		for x := max(int(x0*K), 0); x < min(int(x1*K)+1, c.w); x++ {
			i := (y*c.w + x) * 3
			cur := rgb{c.p[i], c.p[i+1], c.p[i+2]}
			col, a := f((float64(x)+0.5)/K, (float64(y)+0.5)/K, cur)
			if a <= 0 {
				continue
			}
			for k := 0; k < 3; k++ {
				c.p[i+k] = cur[k] + (col[k]-cur[k])*math.Min(a, 1)
			}
		}
	}
}
