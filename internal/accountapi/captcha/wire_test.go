package captcha

import (
	"bytes"
	"errors"
	"testing"
)

func TestCaptchaRoundTrip(t *testing.T) {
	in := &Captcha{ID: "abc", TileY: 123, W: 600, H: 320, Tile: 88, Pad: 6,
		Bg: []byte{0xFF, 0xD8, 1, 2, 3}, TilePNG: []byte{0x89, 'P', 'N', 'G', 9}}
	out, err := DecodeCaptcha(EncodeCaptcha(in))
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.TileY != 123 || out.W != 600 || out.H != 320 || out.Tile != 88 || out.Pad != 6 ||
		!bytes.Equal(out.Bg, in.Bg) || !bytes.Equal(out.TilePNG, in.TilePNG) {
		t.Fatalf("got %+v", out)
	}
}

// 截断或长度前缀越界都必须报错,不能 panic 或读到别的段里。
func TestCaptchaDecodeRejectsGarbage(t *testing.T) {
	good := EncodeCaptcha(&Captcha{ID: "x", W: 600, H: 320, Tile: 88, Pad: 6, Bg: []byte{1}, TilePNG: []byte{2}})
	for _, b := range [][]byte{nil, {0, 0}, good[:5], good[:len(good)-2], {0xFF, 0xFF, 0xFF, 0xFF, '{'}} {
		if _, err := DecodeCaptcha(b); !errors.Is(err, ErrBadCaptcha) {
			t.Fatalf("%x: err = %v", b, err)
		}
	}
}
