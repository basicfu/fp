package gen

import (
	"image"
	"math"
	"math/rand/v2"
	"sort"
)

// K 倍内部超采样,所有绘制用逻辑坐标,最后盒式降采样得到抗锯齿结果。
const K = 2

type rgb [3]float64

type vec struct{ x, y float64 }

type canvas struct {
	w, h int // 内部像素尺寸
	p    []float64
}

func newCanvas() *canvas {
	return &canvas{w: W * K, h: H * K, p: make([]float64, W*K*H*K*3)}
}

func (c *canvas) blend(x, y int, col rgb, a float64) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h || a <= 0 {
		return
	}
	i := (y*c.w + x) * 3
	for k := 0; k < 3; k++ {
		c.p[i+k] += (col[k] - c.p[i+k]) * a
	}
}

func (c *canvas) add(x, y int, col rgb, a float64) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	i := (y*c.w + x) * 3
	for k := 0; k < 3; k++ {
		c.p[i+k] = math.Min(255, c.p[i+k]+col[k]*a)
	}
}

func (c *canvas) get(x, y int) rgb {
	x, y = clampi(x, 0, c.w-1), clampi(y, 0, c.h-1)
	i := (y*c.w + x) * 3
	return rgb{c.p[i], c.p[i+1], c.p[i+2]}
}

// shade 逐像素着色,f 收逻辑坐标。
func (c *canvas) shade(f func(x, y float64) rgb) {
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			col := f((float64(x)+0.5)/K, (float64(y)+0.5)/K)
			i := (y*c.w + x) * 3
			c.p[i], c.p[i+1], c.p[i+2] = col[0], col[1], col[2]
		}
	}
}

// poly 偶奇规则扫描线填充,支持多轮廓(可挖洞)。
func (c *canvas) poly(contours [][]vec, col rgb, a float64) {
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, ct := range contours {
		for _, p := range ct {
			minY, maxY = math.Min(minY, p.y*K), math.Max(maxY, p.y*K)
		}
	}
	xs := make([]float64, 0, 16)
	for y := max(int(minY), 0); y <= min(int(maxY)+1, c.h-1); y++ {
		sy := float64(y) + 0.5
		xs = xs[:0]
		for _, ct := range contours {
			n := len(ct)
			for i := 0; i < n; i++ {
				a0, b0 := ct[i], ct[(i+1)%n]
				ay, by := a0.y*K, b0.y*K
				if (ay <= sy) != (by <= sy) {
					xs = append(xs, a0.x*K+(sy-ay)/(by-ay)*(b0.x*K-a0.x*K))
				}
			}
		}
		sort.Float64s(xs)
		for i := 0; i+1 < len(xs); i += 2 {
			for x := max(int(math.Ceil(xs[i]-0.5)), 0); x <= min(int(math.Floor(xs[i+1]-0.5)), c.w-1); x++ {
				c.blend(x, y, col, a)
			}
		}
	}
}

func circlePts(cx, cy, r float64) []vec {
	n := max(24, int(r*1.5))
	pts := make([]vec, n)
	for i := range pts {
		t := float64(i) / float64(n) * 2 * math.Pi
		pts[i] = vec{cx + r*math.Cos(t), cy + r*math.Sin(t)}
	}
	return pts
}

func (c *canvas) circle(cx, cy, r float64, col rgb, a float64) {
	c.poly([][]vec{circlePts(cx, cy, r)}, col, a)
}

// glow 径向衰减的柔光圆,additive 时用于光斑/太阳光晕。
func (c *canvas) glow(cx, cy, r float64, col rgb, a float64, additive bool) {
	x0, x1 := int((cx-r)*K), int((cx+r)*K)+1
	y0, y1 := int((cy-r)*K), int((cy+r)*K)+1
	for y := max(y0, 0); y <= min(y1, c.h-1); y++ {
		for x := max(x0, 0); x <= min(x1, c.w-1); x++ {
			d := math.Hypot(float64(x)/K-cx, float64(y)/K-cy) / r
			if d >= 1 {
				continue
			}
			f := (1 - d) * (1 - d) * a
			if additive {
				c.add(x, y, col, f)
			} else {
				c.blend(x, y, col, f)
			}
		}
	}
}

func (c *canvas) image(r *rand.Rand, noise float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			var s rgb
			for dy := 0; dy < K; dy++ {
				for dx := 0; dx < K; dx++ {
					v := c.get(x*K+dx, y*K+dy)
					for k := 0; k < 3; k++ {
						s[k] += v[k]
					}
				}
			}
			n := (r.Float64() - 0.5) * noise
			i := img.PixOffset(x, y)
			for k := 0; k < 3; k++ {
				img.Pix[i+k] = uint8(clampf(s[k]/(K*K)+n, 0, 255))
			}
			img.Pix[i+3] = 255
		}
	}
	return img
}

func clampi(v, a, b int) int { return max(a, min(b, v)) }
func clampf(v, a, b float64) float64 {
	return math.Max(a, math.Min(b, v))
}

func mix(a, b rgb, t float64) rgb {
	return rgb{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t, a[2] + (b[2]-a[2])*t}
}

func scale(a rgb, f float64) rgb {
	return rgb{clampf(a[0]*f, 0, 255), clampf(a[1]*f, 0, 255), clampf(a[2]*f, 0, 255)}
}

// hsv: h 0..360, s/v 0..1
func hsv(h, s, v float64) rgb {
	h = math.Mod(math.Mod(h, 360)+360, 360) / 60
	i := math.Floor(h)
	f := h - i
	p, q, t := v*(1-s), v*(1-s*f), v*(1-s*(1-f))
	var r, g, b float64
	switch int(i) {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return rgb{r * 255, g * 255, b * 255}
}

// 值噪声 + fbm,用于山脊线、云、水纹。
type noise struct{ perm [512]int }

func newNoise(r *rand.Rand) *noise {
	n := &noise{}
	p := r.Perm(256)
	for i := 0; i < 512; i++ {
		n.perm[i] = p[i&255]
	}
	return n
}

func (n *noise) hash(x, y int) float64 {
	return float64(n.perm[(n.perm[x&255]+y)&511]) / 255
}

func smooth(t float64) float64 { return t * t * (3 - 2*t) }

func (n *noise) at(x, y float64) float64 {
	xi, yi := int(math.Floor(x)), int(math.Floor(y))
	xf, yf := smooth(x-math.Floor(x)), smooth(y-math.Floor(y))
	a, b := n.hash(xi, yi), n.hash(xi+1, yi)
	c, d := n.hash(xi, yi+1), n.hash(xi+1, yi+1)
	return a + (b-a)*xf + (c-a)*yf + (a-b-c+d)*xf*yf
}

func (n *noise) fbm(x, y float64, oct int) float64 {
	s, amp, norm := 0.0, 1.0, 0.0
	for i := 0; i < oct; i++ {
		s += n.at(x, y) * amp
		norm += amp
		x, y, amp = x*2.03, y*2.03, amp*0.5
	}
	return s / norm
}
