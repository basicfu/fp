package captcha

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"testing"

	"github.com/basicfu/fp/internal/accountapi/captcha/gen"
)

// 期望值由业务方原实现(出图 + 线格式)在同一种子下算出:搬家不能改出一个像素或一个字节,
// 否则已发布客户端解出的题与服务端判的答案对不上。Go 升级若改了 image/jpeg 编码,
// 两边要一起重算,不是搬错了。
func TestSameSeedSameBytesAsOrigin(t *testing.T) {
	want := []struct {
		x, y int
		sum  string
	}{
		{475, 155, "8d19f351474f803964cffd0ba7f42b2177957fc637961035d54ea518b49246d3"},
		{484, 211, "80ea86771d05ebd59af27a48a84262620d7fe375cc29d265114a028043cc88a0"},
		{269, 81, "428038e1f74b00db1880712b2db4cfe1f48b03813088fabd6c731511ed8006c6"},
	}
	r := rand.New(rand.NewPCG(1, 2))
	for s, w := range want {
		pz, err := gen.Generate(r, s)
		if err != nil {
			t.Fatal(err)
		}
		b := EncodeCaptcha(&Captcha{ID: "fixed-id", TileY: pz.Y, W: gen.W, H: gen.H, Tile: gen.TS, Pad: gen.ShadowPad,
			Bg: pz.BgJPEG, TilePNG: pz.TilePNG})
		sum := sha256.Sum256(b)
		if pz.X != w.x || pz.Y != w.y || hex.EncodeToString(sum[:]) != w.sum {
			t.Fatalf("风格 %d: (%d,%d) %x", s, pz.X, pz.Y, sum)
		}
	}
}
