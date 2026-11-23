// Package gen 程序化生成滑块验证码:背景每张都是现画的(不打包素材,也没有可还原的原图库),
// 缺口形状与位置随机,生成时就抠好、编码好。
package gen

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand/v2"
)

const (
	W, H      = 600, 320 // 2 倍图,客户端按 300×160 显示
	TS        = 88       // 缺口外框边长(含凸起)
	ShadowPad = 6        // 拼块 PNG 四周的投影外扩
	bgQuality = 75
	bgNoise   = 18
	gapDark   = 0.42
)

type Puzzle struct {
	Style   int
	X, Y    int // 缺口外框左上角
	BgJPEG  []byte
	TilePNG []byte
}

func NumStyles() int         { return len(styles) }
func StyleName(i int) string { return styles[i].name }

func Generate(r *rand.Rand, style int) (Puzzle, error) {
	if style < 0 || style >= len(styles) {
		return Puzzle{}, fmt.Errorf("gen: 没有第 %d 种风格", style)
	}
	c := newCanvas()
	styles[style].draw(c, r)
	bg := c.image(r, bgNoise)
	orig := image.NewRGBA(bg.Bounds())
	copy(orig.Pix, bg.Pix)

	m := mask(gapShapes[r.IntN(len(gapShapes))].inside(r))
	gy := 10 + r.IntN(H-TS-20)
	gx := TS + 40 + r.IntN(W-2*TS-50)
	tile := cutTile(orig, m, gx, gy)
	carveGap(bg, m, gx, gy)

	var bb, tb bytes.Buffer
	if err := jpeg.Encode(&bb, bg, &jpeg.Options{Quality: bgQuality}); err != nil {
		return Puzzle{}, err
	}
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&tb, tile); err != nil {
		return Puzzle{}, err
	}
	return Puzzle{Style: style, X: gx, Y: gy, BgJPEG: bb.Bytes(), TilePNG: tb.Bytes()}, nil
}

func maskAt(m []float64, x, y int) float64 {
	if x < 0 || y < 0 || x >= TS || y >= TS {
		return 0
	}
	return m[y*TS+x]
}

// edgeOf 近似描边强度:覆盖率过渡带及其向内 2px。
func edgeOf(m []float64, x, y int) float64 {
	v := maskAt(m, x, y)
	if v == 0 {
		return 0
	}
	e := 0.0
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			if maskAt(m, x+dx, y+dy) < 0.5 {
				e = math.Max(e, 1-math.Hypot(float64(dx), float64(dy))/3)
			}
		}
	}
	return e * v
}

// carveGap 压暗缺口并做内凹浮雕:左上内缘更暗(投影),右下内缘提亮(反光)。
func carveGap(bg *image.RGBA, m []float64, gx, gy int) {
	for y := 0; y < TS; y++ {
		for x := 0; x < TS; x++ {
			a := m[y*TS+x]
			if a == 0 {
				continue
			}
			tl := 1 - math.Min(maskAt(m, x-3, y-3), maskAt(m, x-2, y-2))
			br := 1 - math.Min(maskAt(m, x+3, y+3), maskAt(m, x+2, y+2))
			i := bg.PixOffset(gx+x, gy+y)
			for k := 0; k < 3; k++ {
				v := float64(bg.Pix[i+k])
				d := v*(1-gapDark)*(1-0.35*tl) + (255-v*(1-gapDark))*0.30*br
				bg.Pix[i+k] = uint8(clampf(v*(1-a)+d*a, 0, 255))
			}
		}
	}
}

// cutTile 从原图抠拼块:略提亮 + 白描边 + 柔和投影,保证在任何底色上都看得清。
func cutTile(bg *image.RGBA, m []float64, gx, gy int) *image.NRGBA {
	t := image.NewNRGBA(image.Rect(0, 0, TS+2*ShadowPad, TS+2*ShadowPad))
	for y := -ShadowPad; y < TS+ShadowPad; y++ {
		for x := -ShadowPad; x < TS+ShadowPad; x++ {
			a := maskAt(m, x, y)
			s := 0.0
			for dy := -3; dy <= 3; dy++ {
				for dx := -3; dx <= 3; dx++ {
					s += maskAt(m, x-2+dx, y-3+dy)
				}
			}
			s = s / 49 * 0.55
			out := color.NRGBA{0, 0, 0, uint8(s * 255 * (1 - a))}
			if a > 0 {
				e := edgeOf(m, x, y)
				c := bg.RGBAAt(gx+x, gy+y)
				f := func(v uint8) uint8 {
					w := math.Min(255, float64(v)*1.12+10)
					return uint8(w*(1-0.85*e) + 255*0.85*e)
				}
				out = color.NRGBA{f(c.R), f(c.G), f(c.B), uint8(math.Min(1, a+s*(1-a)) * 255)}
			}
			t.SetNRGBA(x+ShadowPad, y+ShadowPad, out)
		}
	}
	return t
}
