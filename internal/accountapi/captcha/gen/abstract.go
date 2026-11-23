// 抽象/图案类背景。辅助函数统一加 ab 前缀。
package gen

import (
	"math"
	"math/rand/v2"
)

func init() {
	addStyles(
		style{"流体渐变", abFluid},
		style{"波浪层", abWaves},
		style{"等高线", abContour},
		style{"大理石", abMarble},
		style{"剪纸层叠", abPaper},
		style{"半调网点", abHalftone},
		style{"包豪斯", abBauhaus},
		style{"青海波", abSeigaiha},
		style{"等距方块", abIso},
		style{"同心涟漪", abRipples},
		style{"放射光芒", abSunburst},
		style{"水彩晕染", abWatercolor},
		style{"星云", abNebula},
	)
}

// abHues 随机基色相 + 第二色相:多数取类似色,少数取柔和互补,避免彩虹配色。
func abHues(r *rand.Rand) (float64, float64) {
	h := r.Float64() * 360
	if r.IntN(3) == 0 {
		return h, h + 150 + r.Float64()*60
	}
	d := 25 + r.Float64()*35
	if r.IntN(2) == 0 {
		d = -d
	}
	return h, h + d
}

// abHue 在两色相间取色:互补色走 RGB 插值(中段自然去饱和),避免色相插值扫过整条色环变成彩虹。
func abHue(h0, h1, t, s, v float64) rgb {
	if math.Abs(h1-h0) > 90 {
		return mix(hsv(h0, s, v), hsv(h1, s, v), t)
	}
	return hsv(h0+(h1-h0)*t, s, v)
}

func abRamp(cs []rgb, t float64) rgb {
	t = clampf(t, 0, 1) * float64(len(cs)-1)
	i := min(int(t), len(cs)-2)
	return mix(cs[i], cs[i+1], t-float64(i))
}

func abSstep(a, b, x float64) float64 {
	t := clampf((x-a)/(b-a), 0, 1)
	return t * t * (3 - 2*t)
}

func abDir(r *rand.Rand) (float64, float64) {
	a := r.Float64() * 2 * math.Pi
	return math.Cos(a), math.Sin(a)
}

// abShadeLo 在逻辑分辨率网格上求值再双线性放大:多层 fbm/域扭曲这类平滑场逐内部像素算太贵,
// 而最终输出本就是逻辑分辨率,画质几乎无损。
func abShadeLo(c *canvas, f func(x, y float64) rgb) {
	gw, gh := W+2, H+2
	g := make([]rgb, gw*gh)
	for j := 0; j < gh; j++ {
		for i := 0; i < gw; i++ {
			g[j*gw+i] = f(float64(i), float64(j))
		}
	}
	for y := 0; y < c.h; y++ {
		fy := (float64(y) + 0.5) / K
		j := int(fy)
		ty := fy - float64(j)
		for x := 0; x < c.w; x++ {
			fx := (float64(x) + 0.5) / K
			i := int(fx)
			tx := fx - float64(i)
			k := j*gw + i
			a, b, d, e := &g[k], &g[k+1], &g[k+gw], &g[k+gw+1]
			o := (y*c.w + x) * 3
			for ch := 0; ch < 3; ch++ {
				t := a[ch] + (b[ch]-a[ch])*tx
				c.p[o+ch] = t + (d[ch]+(e[ch]-d[ch])*tx-t)*ty
			}
		}
	}
}

// ---------------- 1 流体渐变 ----------------

// 扭曲后的反距离加权混色得到大块柔和渐变,再叠一层顺流向的缎面丝纹提供中频纹理。
func abFluid(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	type pt struct {
		x, y float64
		col  rgb
	}
	ps := make([]pt, 6+r.IntN(4))
	for i := range ps {
		hue := []float64{h0, h1, h0 + 18, h0 - 18}[i%4] + (r.Float64()-0.5)*12
		ps[i] = pt{r.Float64()*W*1.2 - W*0.1, r.Float64()*H*1.2 - H*0.1,
			hsv(hue, 0.25+r.Float64()*0.3, []float64{0.95, 0.72, 0.85}[i%3]+(r.Float64()-0.5)*0.08)}
	}
	nz := newNoise(r)
	f := 0.003 + r.Float64()*0.003
	warp := 70 + r.Float64()*80
	gx, gy := abDir(r)
	sp := 12 + r.Float64()*14
	bend := 4 + r.Float64()*5
	silk := hsv(h0, 0.06, 1)
	abShadeLo(c, func(x, y float64) rgb {
		qx := nz.fbm(x*f, y*f, 3) - 0.5
		qy := nz.fbm(x*f+31.7, y*f+17.3, 3) - 0.5
		wx, wy := x+qx*warp*2, y+qy*warp*2
		var s rgb
		ws := 0.0
		for _, p := range ps {
			d2 := (wx-p.x)*(wx-p.x) + (wy-p.y)*(wy-p.y) + 3000
			w := 1 / (d2 * d2 * d2)
			ws += w
			for k := 0; k < 3; k++ {
				s[k] += p.col[k] * w
			}
		}
		// 丝纹相位以线性项为主,扭曲只让它弯曲:纯噪声等值线会围成一圈圈闭合环,像莫尔纹
		ph := 2 * math.Pi * ((x*gx+y*gy)/sp + qx*bend)
		col := scale(s, (1+0.05*math.Sin(ph))/ws)
		return mix(col, silk, 0.2*abSstep(0.75, 1, math.Cos(ph)))
	})
}

// ---------------- 2 波浪层 ----------------

func abWaves(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	n := 5 + r.IntN(4)
	nz := newNoise(r)
	tilt := (r.Float64() - 0.5) * 0.3
	edges := make([][]float64, n+1)
	for i := 1; i <= n; i++ {
		base := H*(float64(i)-0.3)/(float64(n)+0.4) + (r.Float64()-0.5)*12
		amp := 6 + r.Float64()*16
		f1, f2 := 2*math.Pi/(140+r.Float64()*260), 2*math.Pi/(50+r.Float64()*80)
		p1, p2 := r.Float64()*7, r.Float64()*7
		e := make([]float64, W+2)
		for xi := range e {
			x := float64(xi)
			e[xi] = base + (x-W/2)*tilt + amp*math.Sin(x*f1+p1) + amp*0.35*math.Sin(x*f2+p2) +
				(nz.fbm(x*0.008, float64(i)*7.3, 2)-0.5)*amp*1.5
		}
		edges[i] = e
	}
	cols := make([]rgb, n+1)
	rev := r.IntN(2) == 0
	for i := range cols {
		t := float64(i) / float64(n)
		if rev {
			t = 1 - t
		}
		cols[i] = abHue(h0, h1, t, 0.2+0.4*t, 0.97-0.42*t)
	}
	sh := 5 + r.Float64()*6
	abShadeLo(c, func(x, y float64) rgb {
		xi := int(x)
		k := 0
		for i := 1; i <= n; i++ {
			if y >= edges[i][xi] {
				k = i
			}
		}
		col := cols[k]
		if k > 0 { // 带内从上沿往下略微变深,模拟弧面
			col = scale(col, 1.04-0.08*abSstep(0, 40, y-edges[k][xi]))
		}
		s := 0.0
		for j := k + 1; j <= n; j++ {
			s = math.Max(s, math.Exp(-(edges[j][xi]-y)/sh))
		}
		return scale(col, 1-0.28*s)
	})
}

// ---------------- 3 等高线 ----------------

func abContour(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	nz := newNoise(r)
	f := 0.004 + r.Float64()*0.005
	ca, sa := abDir(r)
	st := 1 + r.Float64()*0.8
	gw, gh := W+2, H+2
	g := make([]float64, gw*gh)
	lo, hi := math.Inf(1), math.Inf(-1)
	for j := 0; j < gh; j++ {
		for i := 0; i < gw; i++ {
			x, y := float64(i), float64(j)
			h := nz.fbm((x*ca+y*sa)*f, (-x*sa+y*ca)*f*st, 3)
			g[j*gw+i] = h
			lo, hi = math.Min(lo, h), math.Max(hi, h)
		}
	}
	for i := range g {
		g[i] = (g[i] - lo) / (hi - lo)
	}
	N := 9 + float64(r.IntN(8))
	pal := []rgb{hsv(h0, 0.45, 0.48), abHue(h0, h1, 0.5, 0.4, 0.68), hsv(h1, 0.3, 0.84), hsv(h1+10, 0.12, 0.96)}
	if r.IntN(2) == 0 {
		pal[0], pal[1], pal[2], pal[3] = pal[3], pal[2], pal[1], pal[0]
	}
	lx, ly := abDir(r)
	idx := 4 + r.IntN(2)
	abShadeLo(c, func(x, y float64) rgb {
		i, j := min(int(x), W), min(int(y), H)
		k := j*gw + i
		h := g[k]
		gx, gy := (g[k+1]-g[max(k-1, 0)])/2, (g[k+gw]-g[max(k-gw, 0)])/2
		t := h * N
		lv := math.Floor(t)
		fr := t - lv
		near := int(math.Round(t))
		d := math.Min(fr, 1-fr) / (math.Hypot(gx, gy)*N + 1e-6) // 到最近等高线的像素距离
		col := abRamp(pal, (lv+0.5)/N)
		// 轻微山体光照,让每个色阶内部也有明暗起伏
		col = scale(col, 1+clampf((gx*lx+gy*ly)/(f*3)*0.12, -0.14, 0.14))
		w := 0.35
		if near%idx == 0 {
			w = 0.8
		}
		a := 1 - abSstep(w, w+0.9, d)
		return mix(col, scale(col, 0.62), a*0.8)
	})
}

// ---------------- 4 大理石 ----------------

func abMarble(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	nz := newNoise(r)
	f := 0.0022 + r.Float64()*0.0025
	warp := 1.8 + r.Float64()*1.8
	agate := r.IntN(2) == 0
	bands := 3 + r.Float64()*3
	if agate {
		bands = 7 + r.Float64()*7
	}
	dx, dy := abDir(r)
	pal := []rgb{hsv(h0, 0.07, 0.96), hsv(h0, 0.2, 0.88), abHue(h0, h1, 0.5, 0.3, 0.78), hsv(h1, 0.3, 0.7), hsv(h1, 0.1, 0.92)}
	vein := hsv(h1, 0.35, 0.5)
	abShadeLo(c, func(x, y float64) rgb {
		u, v := x*f, y*f
		qx := nz.fbm(u, v, 3)
		qy := nz.fbm(u+5.2, v+1.3, 3)
		w := nz.fbm(u+warp*qx, v+warp*qy, 4)
		t := w*bands + (x*dx+y*dy)*f*1.5
		s := math.Sin(t * math.Pi)
		if agate {
			a := 1 - abSstep(0, 0.06, math.Abs(s))
			return mix(abRamp(pal, 0.5+0.5*s), vein, a*0.4)
		}
		// 大理石:底色缓变,主纹取 fbm 的 0.5 等值线(ridged),再加一层更细更淡的次纹
		col := abRamp(pal[:3], clampf((qx-0.3)*2.5, 0, 1))
		a := (1-abSstep(0, 0.012, math.Abs(w-0.5)))*0.5 + (1-abSstep(0, 0.008, math.Abs(qy-0.5)))*0.25
		return mix(col, vein, a)
	})
}

// ---------------- 5 剪纸层叠 ----------------

func abPaper(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	nz := newNoise(r)
	n := 6 + r.IntN(3)
	cols := make([]rgb, n)
	for i := range cols {
		t := float64(i) / float64(n-1)
		cols[i] = abHue(h0, h1, t, 0.22+0.25*t, 0.97-0.32*t)
	}
	sh := 7 + r.Float64()*6
	grain := func(x, y float64, col rgb) rgb {
		return scale(col, 1+(nz.at(x*0.9, y*0.9)-0.5)*0.05)
	}
	if r.IntN(2) == 0 { // 隧道式:同心镂空,越靠前洞越大
		cx, cy := W*(0.3+r.Float64()*0.4), H*(0.3+r.Float64()*0.4)
		maxR := math.Hypot(math.Max(cx, W-cx), math.Max(cy, H-cy))
		const bins = 1440
		rad := make([][]float64, n)
		for i := 1; i < n; i++ {
			base := maxR * (0.12 + 0.88*float64(i)/float64(n)) * 0.92
			rad[i] = make([]float64, bins+1)
			for b := 0; b <= bins; b++ {
				a := float64(b) / bins * 2 * math.Pi
				// 在圆周上采样噪声,首尾自然闭合
				w := nz.fbm(math.Cos(a)*1.6+float64(i)*9.1, math.Sin(a)*1.6+float64(i)*4.7, 3)
				rad[i][b] = base * (0.75 + 0.5*w)
			}
		}
		abShadeLo(c, func(x, y float64) rgb {
			rho := math.Hypot(x-cx, y-cy)
			fb := (math.Atan2(y-cy, x-cx)/(2*math.Pi) + 0.5) * bins
			b := min(int(fb), bins-1)
			ft := fb - float64(b)
			k, s := 0, 0.0
			for i := n - 1; i >= 1; i-- {
				R := rad[i][b]*(1-ft) + rad[i][b+1]*ft
				if rho > R {
					k = i
					break
				}
				s = math.Max(s, math.Exp(-(R-rho)/sh))
			}
			col := cols[n-1-k] // 最深处最亮
			return grain(x, y, scale(col, (1-0.32*s)*(1.03-0.06*float64(k)/float64(n))))
		})
		return
	}
	edges := make([][]float64, n)
	for i := 1; i < n; i++ {
		base := H * (0.3 + 0.72*float64(i)/float64(n))
		amp := 55 + r.Float64()*45
		fq := 0.005 + r.Float64()*0.007
		seed := r.Float64() * 100
		e := make([]float64, W+2)
		for xi := range e {
			x := float64(xi)
			e[xi] = base - (nz.fbm(x*fq+seed, seed, 3)-0.5)*amp*2.2
		}
		edges[i] = e
	}
	sunX, sunY, sunR := W*(0.15+r.Float64()*0.7), H*(0.1+r.Float64()*0.1), 16+r.Float64()*16
	sun := hsv(h0+20, 0.2, 1)
	abShadeLo(c, func(x, y float64) rgb {
		xi := int(x)
		k := 0
		for i := 1; i < n; i++ {
			if y >= edges[i][xi] {
				k = i
			}
		}
		s := 0.0
		for j := k + 1; j < n; j++ {
			s = math.Max(s, math.Exp(-(edges[j][xi]-y)/sh))
		}
		col := scale(cols[k], 1.03-0.08*y/H)
		if k == 0 {
			col = mix(col, sun, 1-abSstep(sunR-0.7, sunR+0.7, math.Hypot(x-sunX, y-sunY)))
		}
		return grain(x, y, scale(col, 1-0.32*s))
	})
}

// ---------------- 6 半调网点 ----------------

func abHalftone(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	nz := newNoise(r)
	sp := 8 + r.Float64()*8
	ang := r.Float64() * math.Pi / 2
	ca, sa := math.Cos(ang), math.Sin(ang)
	umin, umax, vmin, vmax := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, p := range []vec{{0, 0}, {W, 0}, {0, H}, {W, H}} {
		u, v := p.x*ca+p.y*sa, -p.x*sa+p.y*ca
		umin, umax, vmin, vmax = math.Min(umin, u), math.Max(umax, u), math.Min(vmin, v), math.Max(vmax, v)
	}
	i0, j0 := int(math.Floor(umin/sp))-2, int(math.Floor(vmin/sp))-2
	nu, nv := int(math.Ceil(umax/sp))+2-i0+1, int(math.Ceil(vmax/sp))+2-j0+1
	gdx, gdy := abDir(r)
	f := 0.005 + r.Float64()*0.006
	// 半径按网格预计算,逐像素只查表
	rad := make([]float64, nu*nv)
	for j := 0; j < nv; j++ {
		for i := 0; i < nu; i++ {
			cu, cv := (float64(i+i0)+0.5)*sp, (float64(j+j0)+0.5)*sp
			x, y := cu*ca-cv*sa, cu*sa+cv*ca
			t := ((x-W/2)*gdx+(y-H/2)*gdy)/math.Hypot(W, H) + 0.5
			val := clampf(0.55*t+1.2*(nz.fbm(x*f, y*f, 3)-0.5)+0.25, 0.06, 1)
			rad[j*nu+i] = sp * 0.74 * val
		}
	}
	bgA, bgB := hsv(h0, 0.15, 0.97), hsv(h1, 0.25, 0.9)
	dA, dB := hsv(h0, 0.5, 0.58), hsv(h1, 0.5, 0.45)
	bx, by := abDir(r)
	abShadeLo(c, func(x, y float64) rgb {
		u, v := x*ca+y*sa, -x*sa+y*ca
		iu, iv := int(math.Floor(u/sp))-i0, int(math.Floor(v/sp))-j0
		cov := 0.0
		for dj := -1; dj <= 1; dj++ {
			for di := -1; di <= 1; di++ {
				i, j := iu+di, iv+dj
				cu, cv := (float64(i+i0)+0.5)*sp, (float64(j+j0)+0.5)*sp
				d := math.Hypot(u-cu, v-cv)
				cov = math.Max(cov, clampf(rad[j*nu+i]-d+0.5, 0, 1))
			}
		}
		t := ((x-W/2)*bx+(y-H/2)*by)/math.Hypot(W, H) + 0.5
		return mix(mix(bgA, bgB, t), mix(dA, dB, t), cov)
	})
}

// ---------------- 7 包豪斯 ----------------

func abArc(cx, cy, rad, a0, a1 float64) []vec {
	n := max(8, int(rad*math.Abs(a1-a0)/2))
	pts := make([]vec, n+1)
	for i := range pts {
		a := a0 + (a1-a0)*float64(i)/float64(n)
		pts[i] = vec{cx + rad*math.Cos(a), cy + rad*math.Sin(a)}
	}
	return pts
}

func abBauhaus(c *canvas, r *rand.Rand) {
	h0 := r.Float64() * 360
	pal := []rgb{
		hsv(h0+40, 0.12, 0.95),
		hsv(h0, 0.4, 0.78),
		hsv(h0+35+r.Float64()*15, 0.38, 0.88),
		hsv(h0+170+r.Float64()*40, 0.35, 0.55),
		hsv(h0+200, 0.35, 0.3),
	}
	s := 48 + r.Float64()*34
	ox, oy := -r.Float64()*s, -r.Float64()*s
	for y := oy; y < H; y += s {
		for x := ox; x < W; x += s {
			bi := r.IntN(len(pal))
			fi := (bi + 1 + r.IntN(len(pal)-1)) % len(pal)
			bg, fg := pal[bi], pal[fi]
			rect(c, x, y, s+0.5, s+0.5, bg, 1)
			corner := r.IntN(4)
			// 四个角(顺时针 左上/右上/右下/左下)及其指向内部的圆弧起始角
			cxs := [4]float64{x, x + s, x + s, x}
			cys := [4]float64{y, y, y + s, y + s}
			a0 := float64(corner) * math.Pi / 2
			kx, ky := cxs[corner], cys[corner]
			switch r.IntN(8) {
			case 0: // 四分之一圆
				c.poly([][]vec{append([]vec{{kx, ky}}, abArc(kx, ky, s, a0, a0+math.Pi/2)...)}, fg, 1)
			case 1: // 半圆贴边
				mx, my := (cxs[corner]+cxs[(corner+1)%4])/2, (cys[corner]+cys[(corner+1)%4])/2
				c.poly([][]vec{abArc(mx, my, s/2, a0, a0+math.Pi)}, fg, 1)
			case 2: // 对角三角
				c.poly([][]vec{{{cxs[corner], cys[corner]}, {cxs[(corner+1)%4], cys[(corner+1)%4]}, {cxs[(corner+3)%4], cys[(corner+3)%4]}}}, fg, 1)
			case 3: // 圆 + 内圆
				c.circle(x+s/2, y+s/2, s*0.38, fg, 1)
				c.circle(x+s/2, y+s/2, s*0.16, pal[(fi+1+r.IntN(len(pal)-1))%len(pal)], 1)
			case 4: // 条纹
				vert := r.IntN(2) == 0
				for k := 0; k < 3; k++ {
					o := s/12 + float64(k)*s/3
					if vert {
						rect(c, x+o, y, s/6, s, fg, 1)
					} else {
						rect(c, x, y+o, s, s/6, fg, 1)
					}
				}
			case 5: // 同心四分之一环
				for k := 4; k >= 1; k-- {
					col := fg
					if k%2 == 1 {
						col = bg
					}
					c.poly([][]vec{append([]vec{{kx, ky}}, abArc(kx, ky, s*float64(k)/4, a0, a0+math.Pi/2)...)}, col, 1)
				}
			case 6: // 对角两个四分之一圆
				c.poly([][]vec{append([]vec{{kx, ky}}, abArc(kx, ky, s/2, a0, a0+math.Pi/2)...)}, fg, 1)
				ox2, oy2 := cxs[(corner+2)%4], cys[(corner+2)%4]
				b0 := a0 + math.Pi
				c.poly([][]vec{append([]vec{{ox2, oy2}}, abArc(ox2, oy2, s/2, b0, b0+math.Pi/2)...)}, fg, 1)
			default: // 叶形:两条四分之一圆弧围成
				p := abArc(kx, ky, s, a0, a0+math.Pi/2)
				o := (corner + 2) % 4
				q := abArc(cxs[o], cys[o], s, a0+math.Pi, a0+1.5*math.Pi)
				c.poly([][]vec{append(p, q...)}, fg, 1)
			}
		}
	}
}

// ---------------- 8 青海波 ----------------

func abSeigaiha(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	nz := newNoise(r)
	R := 20 + r.Float64()*18
	rings := 3 + r.IntN(3)
	flip := r.IntN(3) == 0
	dark, mid := hsv(h0, 0.5, 0.42), hsv(h0+10, 0.38, 0.62)
	line := hsv(h1, 0.12, 0.95)
	alt := hsv(h1, 0.4, 0.7)
	f := 0.006 + r.Float64()*0.004
	lw := 0.11
	c.shade(func(x, y float64) rgb {
		if flip {
			y = H - y
		}
		// 行距 R/2、后画的行盖住前面:从可能覆盖本像素的最下一行往上找第一个命中的圆
		jmax := int(math.Floor((y + R) / (R / 2)))
		var d, ccx, ccy float64
		for j := jmax; j >= jmax-4; j-- {
			cy := float64(j) * R / 2
			off := 0.0
			if j&1 == 1 {
				off = R
			}
			cx := math.Round((x-off)/(2*R))*2*R + off
			if dd := math.Hypot(x-cx, y-cy); dd < R {
				d, ccx, ccy = dd, cx, cy
				break
			}
		}
		t := d / R * float64(rings)
		k := math.Floor(t)
		fr := t - k
		// 每个扇形取一个整体色调偏移,形成大尺度的明暗起伏
		v := nz.fbm(ccx*f, ccy*f, 2)
		fill := mix(dark, mid, float64(int(k)%2))
		if int(k) == 0 {
			fill = mix(alt, mid, 0.4)
		}
		fill = mix(fill, alt, clampf((v-0.45)*1.6, 0, 0.5))
		pw := 0.6 / (R / float64(rings)) // 半个逻辑像素对应的环宽比例,用于抗锯齿
		a := abSstep(1-lw-pw, 1-lw+pw, fr)
		return mix(fill, line, a)
	})
}

// ---------------- 9 等距方块 ----------------

func abIso(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	s := 15 + r.Float64()*20
	dx, dy := s*math.Sqrt(3), s*1.5
	ox, oy := r.Float64()*dx, r.Float64()*dy
	top, rgt, lft := 1.0, 0.8, 0.62
	if r.IntN(2) == 0 {
		rgt, lft = lft, rgt
	}
	gx, gy := abDir(r)
	accent := hsv(h1+(r.Float64()-0.5)*30, 0.5, 0.82)
	pHole, pAcc := 0.04+r.Float64()*0.08, 0.04+r.Float64()*0.08
	for j := -1; float64(j)*dy+oy < H+s; j++ {
		for i := -1; float64(i)*dx+ox < W+dx; i++ {
			cx, cy := ox+float64(i)*dx, oy+float64(j)*dy
			if j&1 == 1 {
				cx += dx / 2
			}
			t := ((cx-W/2)*gx+(cy-H/2)*gy)/math.Hypot(W, H) + 0.5
			base := abHue(h0, h1, t*0.6, 0.3+0.2*t, 0.9-0.12*t)
			var v [6]vec
			for k := range v {
				a := (-90 + 60*float64(k)) * math.Pi / 180
				v[k] = vec{cx + s*math.Cos(a), cy + s*math.Sin(a)}
			}
			ctr := vec{cx, cy}
			u := r.Float64()
			if u < pHole { // 凹角:三条分割线换成朝下的 Y,读起来像缺了一块
				c.poly([][]vec{{ctr, v[0], v[1], v[2]}}, scale(base, lft*0.85), 1)
				c.poly([][]vec{{ctr, v[2], v[3], v[4]}}, scale(base, top*0.82), 1)
				c.poly([][]vec{{ctr, v[4], v[5], v[0]}}, scale(base, rgt*0.85), 1)
				continue
			}
			if u < pHole+pAcc {
				base = accent
			}
			c.poly([][]vec{{ctr, v[5], v[0], v[1]}}, scale(base, top), 1)
			c.poly([][]vec{{ctr, v[1], v[2], v[3]}}, scale(base, rgt), 1)
			c.poly([][]vec{{ctr, v[3], v[4], v[5]}}, scale(base, lft), 1)
		}
	}
}

// ---------------- 10 同心涟漪 ----------------

func abRipples(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	type src struct{ x, y, k, ph, a float64 }
	ss := make([]src, 3+r.IntN(3))
	lam := 16 + r.Float64()*16
	for i := range ss {
		ss[i] = src{-W*0.1 + r.Float64()*W*1.2, -H*0.1 + r.Float64()*H*1.2,
			2 * math.Pi / (lam * (0.8 + r.Float64()*0.4)), r.Float64() * 7, 100 + r.Float64()*120}
	}
	gx, gy := abDir(r)
	c0, c1 := hsv(h0, 0.4, 0.62), hsv(h1, 0.35, 0.88)
	hiCol := hsv(h1, 0.1, 0.98)
	abShadeLo(c, func(x, y float64) rgb {
		crest, trough := 0.0, 0.0
		for _, s := range ss {
			d := math.Hypot(x-s.x, y-s.y)
			cv, fade := math.Cos(d*s.k+s.ph), 1/(1+d*d/(s.a*s.a))
			// 各组波纹取最大值而非相加:相加会在重叠区干涉出满屏点状莫尔纹
			crest = math.Max(crest, abSstep(0.3, 0.9, cv)*fade)
			trough = math.Max(trough, abSstep(0.2, 1, -cv)*fade)
		}
		t := ((x-W/2)*gx+(y-H/2)*gy)/math.Hypot(W, H) + 0.5
		bg := mix(c0, c1, t)
		// 只把波峰收窄成亮环,波谷做浅暗,避免满屏高对比条纹
		return mix(scale(bg, 1-0.16*trough), hiCol, 0.5*crest)
	})
}

// ---------------- 11 放射光芒 ----------------

func abSunburst(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	var cx, cy float64
	switch r.IntN(3) {
	case 0: // 画面外上方
		cx, cy = -W*0.2+r.Float64()*W*1.4, -40-r.Float64()*120
	case 1: // 画面外侧边
		cx, cy = -60-r.Float64()*100, r.Float64()*H
		if r.IntN(2) == 0 {
			cx = W - cx
		}
	default: // 画内偏心
		cx, cy = W*(0.15+r.Float64()*0.7), H*(0.15+r.Float64()*0.7)
	}
	n := float64(14 + 2*r.IntN(14))
	wob, wph := 0.15+r.Float64()*0.25, r.Float64()*7
	light, darkC := hsv(h0, 0.3, 0.95), abHue(h0, h1, 0.5, 0.42, 0.72)
	glowC := hsv(h0+15, 0.15, 1)
	m := 3 + float64(r.IntN(3))
	abShadeLo(c, func(x, y float64) rgb {
		dx, dy := x-cx, y-cy
		rho := math.Hypot(dx, dy)
		th := math.Atan2(dy, dx)
		t := (th/(2*math.Pi)+0.5)*n + wob*math.Sin(th*3+wph)
		pw := n / math.Max(rho, 1) // 一逻辑像素对应的 sin 相位增量,用于光芒边缘抗锯齿
		ray := abSstep(-pw, pw, math.Sin(2*math.Pi*t))
		base := mix(light, darkC, clampf(rho/900, 0, 1)*0.5+0.25)
		col := mix(base, scale(base, 0.86), ray)
		// 细光芒叠层,给宽光芒内部加中频纹理
		t2 := t*m + 0.5
		f2 := t2 - math.Floor(t2)
		col = scale(col, 1+0.035*math.Cos(f2*2*math.Pi))
		col = mix(col, glowC, 0.55*math.Exp(-rho/140))
		vx, vy := (x-W/2)/(W/2), (y-H/2)/(H/2)
		return scale(col, 1-0.22*(vx*vx+vy*vy)/2)
	})
}

// ---------------- 12 水彩晕染 ----------------

func abWatercolor(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	nz := newNoise(r)
	paper := hsv(h0+30, 0.05, 0.96)
	wash := hsv(h1, 0.25, 0.92)
	wf := 0.004 + r.Float64()*0.003
	abShadeLo(c, func(x, y float64) rgb { return mix(paper, wash, abSstep(0.4, 0.7, nz.fbm(x*wf+20, y*wf, 3))*0.5) })
	blob := func(cx, cy, R, seed float64) []vec {
		const m = 96
		pts := make([]vec, m)
		for i := range pts {
			a := float64(i) / m * 2 * math.Pi
			rr := R * (0.6 + 0.8*nz.fbm(math.Cos(a)*1.4+seed, math.Sin(a)*1.4+seed*0.7, 4))
			pts[i] = vec{cx + rr*math.Cos(a), cy + rr*math.Sin(a)}
		}
		return pts
	}
	for b := 16 + r.IntN(7); b > 0; b-- {
		cx, cy := r.Float64()*W, r.Float64()*H
		R := 30 + r.Float64()*75
		hue := h0
		if r.IntN(3) == 0 {
			hue = h1
		}
		col := hsv(hue+(r.Float64()-0.5)*24, 0.3+r.Float64()*0.3, 0.7+r.Float64()*0.25)
		seed := r.Float64() * 100
		for l := 0; l < 4; l++ {
			jx, jy := (r.Float64()-0.5)*6, (r.Float64()-0.5)*6
			pts := blob(cx+jx, cy+jy, R*(1-0.07*float64(l)), seed+float64(l)*0.6)
			c.poly([][]vec{pts}, col, 0.13)
			// 颜料干燥时在边缘沉积,沿轮廓内侧画一圈更深的窄环
			in := make([]vec, len(pts))
			for i, p := range pts {
				d := math.Hypot(p.x-cx-jx, p.y-cy-jy)
				k := math.Max(0, 1-2.5/math.Max(d, 1))
				in[i] = vec{cx + jx + (p.x-cx-jx)*k, cy + jy + (p.y-cy-jy)*k}
			}
			c.poly([][]vec{pts, in}, scale(col, 0.72), 0.16)
		}
	}
	// 纸纹 + 颜料颗粒:按逻辑像素算一次噪声,再乘到对应的 K×K 内部像素上
	gr := make([]float64, W*H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			fx, fy := float64(x), float64(y)
			gr[y*W+x] = 1 + (nz.at(fx*0.7, fy*0.7)-0.5)*0.07 + (nz.at(fx*0.06+50, fy*0.06)-0.5)*0.06
		}
	}
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			g := gr[(y/K)*W+x/K]
			i := (y*c.w + x) * 3
			for k := 0; k < 3; k++ {
				c.p[i+k] = math.Min(255, c.p[i+k]*g)
			}
		}
	}
}

// ---------------- 13 星云 ----------------

func abNebula(c *canvas, r *rand.Rand) {
	h0, h1 := abHues(r)
	nz := newNoise(r)
	f := 0.004 + r.Float64()*0.004
	bg := hsv(h0+20, 0.6, 0.07)
	ca, cb := hsv(h0, 0.6, 1), hsv(h1, 0.5, 1)
	// 星云沿一条随机斜带分布,而不是满屏均匀
	bx, by := abDir(r)
	bw := 90 + r.Float64()*80
	bo := (r.Float64() - 0.5) * 100
	abShadeLo(c, func(x, y float64) rgb {
		u, v := x*f, y*f
		qx, qy := nz.fbm(u, v, 3), nz.fbm(u+7.7, v+3.1, 3)
		d := nz.fbm(u+2.5*qx, v+2.5*qy, 4)
		dist := ((x-W/2)*by - (y-H/2)*bx - bo) / bw
		band := math.Exp(-dist * dist)
		den := math.Pow(abSstep(0.3, 0.75, d), 1.5) * (0.35 + 0.65*band)
		col := mix(ca, cb, clampf((qx-0.35)*2.5, 0, 1))
		dust := 1 - 0.65*abSstep(0.55, 0.68, qy)
		out := bg
		for k := 0; k < 3; k++ {
			out[k] = math.Min(255, bg[k]+col[k]*den*0.75*dust)
		}
		return out
	})
	for i := 0; i < 600; i++ {
		x, y := r.Float64()*W, r.Float64()*H
		c.glow(x, y, 0.7+r.Float64()*1.2, mix(rgb{255, 255, 255}, ca, r.Float64()*0.3), 0.4+r.Float64()*0.6, true)
	}
	for i := 3 + r.IntN(3); i > 0; i-- {
		x, y := r.Float64()*W, r.Float64()*H
		rad := 10 + r.Float64()*16
		col := mix(rgb{255, 255, 255}, cb, 0.3)
		c.glow(x, y, rad, col, 0.55, true)
		c.glow(x, y, 2.5, rgb{255, 255, 255}, 1, true)
		l := rad * 1.6
		line(c, x-l, y, x+l, y, 0.7, col, 0.35)
		line(c, x, y-l, x, y+l, 0.7, col, 0.35)
	}
}
