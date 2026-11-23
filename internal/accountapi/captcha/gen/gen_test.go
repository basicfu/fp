package gen

import (
	"bytes"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"testing"
)

func TestGenerateEveryStyle(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	if NumStyles() < 30 {
		t.Fatalf("风格数 = %d", NumStyles())
	}
	for s := 0; s < NumStyles(); s++ {
		p, err := Generate(r, s)
		if err != nil {
			t.Fatalf("%s: %v", StyleName(s), err)
		}
		bg, err := jpeg.Decode(bytes.NewReader(p.BgJPEG))
		if err != nil || bg.Bounds().Dx() != W || bg.Bounds().Dy() != H {
			t.Fatalf("%s: 背景 %v %v", StyleName(s), err, bg.Bounds())
		}
		tile, err := png.Decode(bytes.NewReader(p.TilePNG))
		if err != nil || tile.Bounds().Dx() != TS+2*ShadowPad || tile.Bounds().Dy() != TS+2*ShadowPad {
			t.Fatalf("%s: 拼块 %v", StyleName(s), err)
		}
		// 缺口不能压到拼块起点(x<TS 时一开局就重叠),也不能出界。
		if p.X < TS+40 || p.X+TS > W-10 || p.Y < 10 || p.Y+TS > H-10 || p.Style != s {
			t.Fatalf("%s: 缺口位置 (%d,%d)", StyleName(s), p.X, p.Y)
		}
	}
}

// 每种形状的遮罩都要有实心、有空白,且不贴边(贴边会让投影被裁掉)。
func TestShapesMaskSane(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for _, gs := range gapShapes {
		m := mask(gs.inside(r))
		var in float64
		for _, v := range m {
			in += v
		}
		if frac := in / float64(TS*TS); frac < 0.2 || frac > 0.8 {
			t.Fatalf("%s: 填充率 %.2f", gs.name, frac)
		}
		for i := 0; i < TS; i++ {
			if m[i] != 0 || m[(TS-1)*TS+i] != 0 || m[i*TS] != 0 || m[i*TS+TS-1] != 0 {
				t.Fatalf("%s: 遮罩贴边", gs.name)
			}
		}
	}
}

// 缺口区域整体比原图暗:这是用户能看出缺口的前提。
func TestGapIsDarker(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	p, err := Generate(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	bg, _ := jpeg.Decode(bytes.NewReader(p.BgJPEG))
	tile, _ := png.Decode(bytes.NewReader(p.TilePNG))
	cx, cy := p.X+TS/2, p.Y+TS/2
	gr, gg, gb, _ := bg.At(cx, cy).RGBA()
	tr, tg, tb, _ := tile.At(TS/2+ShadowPad, TS/2+ShadowPad).RGBA()
	if gr+gg+gb >= tr+tg+tb {
		t.Fatalf("缺口中心 %d 不比拼块中心 %d 暗", gr+gg+gb, tr+tg+tb)
	}
}
